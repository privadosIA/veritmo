package music

// containers.go: the containers of the music generators that run in one (HeartMuLa, DiffRhythm2) are started and
// stopped by the user, from the Environment screen, and stay up between songs. This file has the calls that start
// and stop them, the state they report, and the check that a run made through the server finds its container up.
// See specs/music-containers.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/project/config"
	"veritmo/internal/server/api"
)

// The engines whose container the user starts and stops.
const (
	engineHeartMuLa   = "heartmula"
	engineDiffRhythm2 = "diffrhythm2"
	// engineCLAP is not a generator: LAION-CLAP, an audio-text model kept stopped for the user to start.
	engineCLAP = "clap"
	// engineVideoVision is the vision service of the video-edit tools: not a generator either.
	engineVideoVision = "videovision"
)

// containerCtl keeps what the server knows that the service files do not: which services are starting, and why the last
// start of each failed.
type containerCtl struct {
	mu       sync.Mutex
	starting map[string]time.Time
	lastErr  map[string]string
}

func (c *containerCtl) startingSince(engine string) (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.starting[engine]
	return t, ok
}

func (c *containerCtl) errorOf(engine string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastErr[engine]
}

// begin marks the start of an engine; it reports false when one is already starting.
func (c *containerCtl) begin(engine string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.starting == nil {
		c.starting, c.lastErr = map[string]time.Time{}, map[string]string{}
	}
	if _, ok := c.starting[engine]; ok {
		return false
	}
	c.starting[engine] = time.Now()
	delete(c.lastErr, engine)
	return true
}

func (c *containerCtl) end(engine, errText string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.starting, engine)
	if errText != "" {
		c.lastErr[engine] = errText
	}
}

func (c *containerCtl) clearError(engine string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.lastErr, engine)
}

// engineOf returns the shared engine of a generator as the server uses it (it never starts a container by itself),
// and false for a name that is not one of them.
func (h *Handler) engineOf(name string, stats bool) (*musicsvc.Engine, bool) {
	var core *musicsvc.Engine
	switch name {
	case engineHeartMuLa:
		core = h.heartMulaEngine().Core()
	case engineDiffRhythm2:
		core = h.diffRhythm2Engine().Core()
	case engineCLAP:
		core = h.clapEngine().Core()
	case engineVideoVision:
		core = h.videoVisionEngine().Core()
	default:
		return nil, false
	}
	core.WithStats = stats
	return core, true
}

// POST /api/music/engines/{engine}/start {"gpu"?: N}
//
//	-> 202 {container: {state: "starting", ...}}
//
// Starts the container on the card asked for, or on the one with most free memory, and waits for the service to say
// ready in the background: GET /api/music/engines tells when it is running, or why it did not get ready.
func (h *Handler) handleEngineStart(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("engine")
	eng, ok := h.engineOf(name, false)
	if !ok {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("no such engine %q: use %s, %s, %s or %s", name, engineHeartMuLa, engineDiffRhythm2, engineCLAP, engineVideoVision))
		return
	}
	var req struct {
		GPU *int `json:"gpu"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil && err != io.EOF {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
			return
		}
	}
	if eng.External.Host != "" {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, "this engine uses the service at "+eng.External.Host+": there is no local service to start")
		return
	}
	a := eng.Availability(r.Context())
	switch a.Reason {
	case musicsvc.ReasonNoService, musicsvc.ReasonNoWeights, musicsvc.ReasonNoGPU, musicsvc.ReasonNoDocker, musicsvc.ReasonNoImage, musicsvc.ReasonNoGPURuntime:
		api.WriteJSON(w, http.StatusUnprocessableEntity, api.APIError{Error: api.APIErrorBody{Code: api.CodeValidation, Message: a.Detail, Reason: a.Reason}})
		return
	}
	if a.Container != nil && a.Container.State == musicsvc.StateRunning {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, "it is already running")
		return
	}
	want := ""
	if req.GPU != nil {
		want = fmt.Sprint(*req.GPU)
	}
	local := eng.Local
	gpus, err := local.GPUs(r.Context())
	if err != nil {
		api.WriteJSON(w, http.StatusUnprocessableEntity, api.APIError{Error: api.APIErrorBody{Code: api.CodeValidation, Message: err.Error(), Reason: musicsvc.ReasonNoGPU}})
		return
	}
	if _, err := musicsvc.PickGPU(eng.Spec, gpus, want); err != nil {
		api.WriteJSON(w, http.StatusUnprocessableEntity, api.APIError{Error: api.APIErrorBody{Code: api.CodeValidation, Message: err.Error(), Reason: musicsvc.ReasonNoGPU}})
		return
	}
	if !h.containers.begin(name) {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, "the container is already starting")
		return
	}
	h.engines.bump()
	go func() {
		start := *eng
		start.NoStart, start.PreferGPU = false, want
		start.Local = local
		_, err := start.Open(h.ws.Context()) // starts it on the chosen card and waits for ready; the container stays up
		if err != nil {
			stopCtx, cancel := context.WithTimeout(context.WithoutCancel(h.ws.Context()), 60*time.Second)
			_ = local.Stop(stopCtx)
			cancel()
			h.containers.end(name, err.Error())
		} else {
			h.containers.end(name, "")
		}
		h.engines.bump()
	}()
	api.WriteJSON(w, http.StatusAccepted, map[string]any{"container": musicsvc.ContainerState{State: musicsvc.StateStarting}})
}

// POST /api/music/engines/{engine}/stop -> 200 {container: {state: "stopped"}}
func (h *Handler) handleEngineStop(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("engine")
	eng, ok := h.engineOf(name, false)
	if !ok {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("no such engine %q: use %s, %s, %s or %s", name, engineHeartMuLa, engineDiffRhythm2, engineCLAP, engineVideoVision))
		return
	}
	if h.ws.UsesGenerator(name) {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, "a run is making songs with this generator: wait for it to end or cancel it")
		return
	}
	local := eng.Local
	if err := local.Stop(r.Context()); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, "could not stop the container: "+err.Error())
		return
	}
	h.containers.clearError(name)
	h.engines.bump()
	api.WriteJSON(w, http.StatusOK, map[string]any{"container": musicsvc.ContainerState{State: musicsvc.StateStopped}})
}

// overlay puts what the server knows on what the service files said: a service that is starting, and why a start failed.
func (h *Handler) overlay(engine string, a *musicsvc.Availability) {
	if a.Container == nil {
		return
	}
	// A service is "running" as a process long before it says ready (it loads its model first): while the start is
	// waiting for ready, it is starting.
	if _, starting := h.containers.startingSince(engine); starting {
		a.Container.State = musicsvc.StateStarting
		a.Available = false
	}
	if a.Container.State == musicsvc.StateStopped {
		a.Container.Error = h.containers.errorOf(engine)
	}
}

// containerPreflight refuses to queue a run of music whose generator runs as a service that is stopped: the run
// would fail at its first song. An address of another machine saved in Settings counts as running.
func (h *Handler) containerPreflight(cfg *config.Config) *launchError {
	if cfg == nil || cfg.Output.Mode != config.ModeMusic || !cfg.Defaults.Music.UsesService() {
		return nil
	}
	name := cfg.Defaults.Music.ResolvedGenerator()
	eng, ok := h.engineOf(name, false)
	if !ok || eng.External.Host != "" {
		return nil
	}
	a := eng.Availability(context.Background())
	h.overlay(name, &a)
	starting := a.Container != nil && a.Container.State == musicsvc.StateStarting
	if a.Reason != musicsvc.ReasonNotRunning && !starting {
		return nil
	}
	msg := fmt.Sprintf("the %s service is not running: start it from the Environment screen", name)
	if starting {
		msg = fmt.Sprintf("the %s service is still starting: wait until the Environment screen says it is running", name)
	}
	return &launchError{Status: http.StatusUnprocessableEntity, Code: api.CodeEngineNotRunning, Msg: msg, Reason: musicsvc.ReasonNotRunning}
}

// BackendError is why the backend of a generator cannot be changed now: Status is the HTTP status it answers with.
type BackendError struct {
	Status int
	Reason string
	Msg    string
}

func (e *BackendError) Error() string { return e.Msg }

// PrepareBackend is called by the settings before the backend of a generator changes to `to`. It checks that the new backend can be
// used (Docker answers, for "docker") and that no run is making songs with the generator, and stops what runs on the old backend so
// that nothing is left behind on the card. It saves nothing. An empty `to` is the default (the .NET service).
func (h *Handler) PrepareBackend(ctx context.Context, name, to string) *BackendError {
	if name != engineHeartMuLa && name != engineDiffRhythm2 {
		return &BackendError{Status: http.StatusNotFound, Msg: "no such generator " + name}
	}
	if to == musicsvc.BackendDocker {
		if st := (musicsvc.Local{Runner: h.heartMulaRunnerOrDefault()}).DockerCheck(ctx); !st.Daemon {
			return &BackendError{Status: http.StatusUnprocessableEntity, Reason: musicsvc.ReasonNoDocker, Msg: "docker cannot be used here: " + st.Error}
		}
	}
	eng, _ := h.engineOf(name, false)
	current := eng.Local.Backend
	if current == "" {
		current = musicsvc.BackendDotnet
	}
	if to == "" {
		to = musicsvc.BackendDotnet
	}
	if current == to {
		return nil
	}
	if h.ws.UsesGenerator(name) {
		return &BackendError{Status: http.StatusConflict, Msg: "a run is making songs with this generator: wait for it to end or cancel it before changing its backend"}
	}
	if _, starting := h.containers.startingSince(name); starting {
		return &BackendError{Status: http.StatusConflict, Msg: "the generator is starting: wait until it is up or stopped"}
	}
	if err := eng.Local.Stop(ctx); err != nil {
		return &BackendError{Status: http.StatusInternalServerError, Msg: "could not stop the " + current + " backend: " + err.Error()}
	}
	h.containers.clearError(name)
	h.engines.bump()
	return nil
}

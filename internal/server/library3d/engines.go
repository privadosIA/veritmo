package library3d

// engines.go: the 3D generator service as the Environment screen and a dashboard see it (spec model-generation): which engines can be used and why
// not, the start and the stop of its container, and the install of its weights. The work is in internal/assets3d/generate and in the container of
// docker/model3d; this file is the HTTP of it, in the shape of the music engines' routes.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"veritmo/internal/assets3d/generate"
	"veritmo/internal/platform/licence"
	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/platform/runner"
	"veritmo/internal/platform/settings"
	"veritmo/internal/server/api"
)

// ReasonLicenceAcceptanceRequired means the install of an engine whose licence has conditions was confirmed without the person's explicit acceptance.
// It is not licence_not_accepted, which says Hugging Face refuses the saved key because its account has not accepted the licence there.
const ReasonLicenceAcceptanceRequired = "licence_acceptance_required"

// Acceptances records and reads the explicit acceptances of the licences with conditions (Stable Fast 3D's). The settings of the server are the default.
type Acceptances interface {
	Accept(id, client string) error
	Accepted(id string) bool
}

type settingsAcceptances struct{}

func (settingsAcceptances) Accept(id, client string) error { return settings.AcceptLicence(id, client) }
func (settingsAcceptances) Accepted(id string) bool        { return settings.LicenceAccepted(id) }

func (h *Handler) acceptances() Acceptances {
	if h.Acceptances != nil {
		return h.Acceptances
	}
	return settingsAcceptances{}
}

// SetService gives the handler the 3D service; without it the routes of the engines answer that there is none.
func (h *Handler) SetService(svc *generate.Service) { h.svc = svc }

// Service is the 3D service the handler uses (nil when there is none).
func (h *Handler) Service() *generate.Service { return h.svc }

func (h *Handler) registerEngines(handle api.HandleFunc) {
	handle("GET /api/models3d/engines", h.handleEngines)
	handle("POST /api/models3d/engines/{engine}/start", h.handleEngineStart)
	handle("POST /api/models3d/engines/{engine}/stop", h.handleEngineStop)
	handle("POST /api/models3d/engines/{engine}/install", h.handleEngineInstall)
	handle("GET /api/models3d/installs/{id}", h.handleInstallGet)
	handle("POST /api/models3d/installs/{id}/cancel", h.handleInstallCancel)
}

func (h *Handler) needService(w http.ResponseWriter) bool {
	if h.svc == nil {
		api.WriteJSON(w, http.StatusServiceUnavailable, api.APIError{Error: api.APIErrorBody{Code: api.CodeUnavailable, Reason: musicsvc.ReasonNoDocker,
			Message: "the 3D generator is not set up on this server"}})
		return false
	}
	return true
}

// unavailable answers an engine that cannot be used with its reason.
func unavailable(w http.ResponseWriter, status int, reason, detail string) {
	api.WriteJSON(w, status, api.APIError{Error: api.APIErrorBody{Code: api.CodeUnavailable, Reason: reason, Message: detail}})
}

func engineOf(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.PathValue("engine")
	if _, ok := generate.Info(name); !ok {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("engine must be one of %s, got %q", strings.Join(generate.Engines(), ", "), name))
		return "", false
	}
	return name, true
}

// GET /api/models3d/engines
func (h *Handler) handleEngines(w http.ResponseWriter, r *http.Request) {
	if !h.needService(w) {
		return
	}
	out := map[string]musicsvc.Availability{}
	for _, e := range generate.Engines() {
		out[e] = h.svc.Availability(r.Context(), e)
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"engines": out})
}

// refuseExternal answers a start, stop or install of a service that runs elsewhere.
func (h *Handler) refuseExternal(w http.ResponseWriter) bool {
	if !h.svc.IsExternal() {
		return false
	}
	api.WriteError(w, http.StatusConflict, api.CodeConflict, "this server uses the 3D service at "+h.svc.External.Host+" (MODEL3D_HOST): its container and its weights are managed there "+
		"(docker compose --profile model3d up -d, and --profile model3d-weights run --rm model3d-weights)")
	return true
}

// POST /api/models3d/engines/{engine}/start
func (h *Handler) handleEngineStart(w http.ResponseWriter, r *http.Request) {
	name, ok := engineOf(w, r)
	if !ok || !h.needService(w) || h.refuseExternal(w) {
		return
	}
	a := h.svc.Availability(r.Context(), name)
	switch {
	case a.Available:
		api.WriteJSON(w, http.StatusOK, map[string]any{"container": h.svc.State(r.Context(), name)})
		return
	case a.Reason != musicsvc.ReasonNotRunning:
		unavailable(w, http.StatusConflict, a.Reason, a.Detail)
		return
	}
	if err := h.svc.Start(r.Context(), name); err != nil {
		unavailable(w, http.StatusConflict, reasonOf(err), err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"container": h.svc.State(r.Context(), name)})
}

// POST /api/models3d/engines/{engine}/stop
func (h *Handler) handleEngineStop(w http.ResponseWriter, r *http.Request) {
	name, ok := engineOf(w, r)
	if !ok || !h.needService(w) || h.refuseExternal(w) {
		return
	}
	if h.generating() {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, "a run is making 3D models: wait for it to end or cancel it before stopping the engine")
		return
	}
	if err := h.svc.Stop(r.Context(), name); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"container": h.svc.State(r.Context(), name)})
}

func reasonOf(err error) string {
	if ue, ok := err.(*musicsvc.UnavailableError); ok {
		return ue.Reason
	}
	return ""
}

// ---------------------------------------------------------------------------------------------------------------------------------------
// The install of the weights

// InstallPlan is what an install would do.
type InstallPlan struct {
	Engine string `json:"engine"`
	// Missing are the files of the weights folder that are not there; Bytes is about how much they take.
	Missing []string `json:"missing"`
	Bytes   int64    `json:"bytes"`
	// Licence is the engine's, which the weights come under, as a client shows it before the person confirms.
	Licence licence.Terms `json:"licence"`
	// Gated says the weights come from a repository whose licence the person's own account has to accept.
	Gated bool `json:"gated"`
	// Backend is the way the engine runs that the install is for (dotnet or docker); Parts what it would run, in order: service, image, weights.
	Backend string   `json:"backend"`
	Parts   []string `json:"parts"`
	// Path is the folder the weights go to; FreeBytes the room there is on its disk.
	Path      string `json:"path"`
	FreeBytes int64  `json:"free_bytes"`
}

// InstallJob is a download of weights.
type InstallJob struct {
	ID         string     `json:"id"`
	Engine     string     `json:"engine"`
	Backend    string     `json:"backend"`
	Parts      []string   `json:"parts"`
	Part       string     `json:"part,omitempty"` // the part that runs now
	State      string     `json:"state"`          // running, done, failed, cancelled
	Error      string     `json:"error,omitempty"`
	Log        []string   `json:"log"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

type installs struct {
	mu      sync.Mutex
	jobs    map[string]*InstallJob
	cancels map[string]context.CancelFunc
	active  string
	counter int
}

func (i *installs) get(id string) (InstallJob, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	j, ok := i.jobs[id]
	if !ok {
		return InstallJob{}, false
	}
	cp := *j
	cp.Log = append([]string{}, j.Log...)
	return cp, true
}

func freeBytes(path string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0
	}
	return int64(st.Bavail) * int64(st.Bsize)
}

// The parts of an install, like those of the music generators: `service` is scripts/get-services.sh (the .NET service of the engine), `image` is `docker build`
// of docker/model3d (the container) and `weights` is the download of the weights (by this program for the .NET service, by fetch.py in the image for the container).
const (
	partService = "service"
	partImage   = "image"
	partWeights = "weights"
)

// installOrder is the order the parts run in: the weights of the container need its image.
var installOrder = []string{partService, partImage, partWeights}

// POST /api/models3d/engines/{engine}/install {"backend"?: "dotnet"|"docker", "parts"?: ["service"|"image"|"weights"], "confirm"?: true}
//
//	-> 200 {plan}            without confirm: what is missing for the backend (the parts and the weight files), the size, the licence and the room on the disk
//	-> 202 {install}         with confirm: a job that runs the parts in order (follow it at /api/models3d/installs/{id})
//
// The backend is the one the engine runs on now (see PUT /api/settings model3d_backend) unless the request names the other, so the container can be
// installed before it is chosen. Parts empty is everything that is missing. 409 while another install runs or when nothing is missing; 422 when the
// engine cannot be installed that way (no Docker, no NVIDIA runtime, an image built without the engine, the key or the licence of a gated engine that
// Hugging Face does not accept: the server only says so, or no room on the disk).
func (h *Handler) handleEngineInstall(w http.ResponseWriter, r *http.Request) {
	name, ok := engineOf(w, r)
	if !ok || !h.needService(w) || h.refuseExternal(w) {
		return
	}
	var req struct {
		Backend string   `json:"backend"`
		Parts   []string `json:"parts"`
		Confirm bool     `json:"confirm"`
		// AcceptLicence is the id of the licence the person accepted (`stability-community`): sent by a client only after they agreed. Client labels who sent it.
		AcceptLicence string `json:"accept_licence"`
		Client        string `json:"client"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil && err != io.EOF {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
			return
		}
	}
	backend := strings.ToLower(strings.TrimSpace(req.Backend))
	if backend == "" {
		backend = h.svc.BackendOf(name)
	}
	if !musicsvc.ValidBackend(backend) {
		unavailable(w, http.StatusUnprocessableEntity, "", "the backend must be dotnet or docker")
		return
	}
	plan, refused := h.planInstall(r.Context(), name, backend, req.Parts)
	if refused != nil {
		unavailable(w, refused.status, refused.reason, refused.detail)
		return
	}
	if len(plan.Parts) == 0 {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, "everything the "+backend+" backend of "+plan.Name+" needs is already there")
		return
	}
	if !req.Confirm {
		api.WriteJSON(w, http.StatusOK, map[string]any{"plan": plan.InstallPlan})
		return
	}
	// An engine whose licence has conditions is never installed on the strength of a confirmation alone: the request carries the acceptance, or it is refused.
	if id := plan.Licence.ID; id != "" {
		if strings.TrimSpace(req.AcceptLicence) != id {
			api.WriteJSON(w, http.StatusBadRequest, api.APIError{Error: api.APIErrorBody{Code: api.CodeInvalid, Reason: ReasonLicenceAcceptanceRequired,
				Message: fmt.Sprintf("%s is under the %s (%s): %s Read it and, if the person agrees, send accept_licence: %q with the confirmation. "+
					"The server never accepts a licence for anyone.", plan.Name, plan.Licence.Name, plan.Licence.URL, plan.Licence.Summary, id)}})
			return
		}
	}
	if len(plan.Missing) > 0 && plan.Bytes > 0 && plan.FreeBytes < plan.Bytes {
		api.WriteJSON(w, http.StatusUnprocessableEntity, api.APIError{Error: api.APIErrorBody{Code: api.CodeValidation, Reason: "no_disk",
			Message: fmt.Sprintf("%s needs %.1f GB free and has %.1f GB", plan.Path, float64(plan.Bytes)/(1<<30), float64(plan.FreeBytes)/(1<<30))}})
		return
	}
	if id := plan.Licence.ID; id != "" {
		if err := h.acceptances().Accept(id, req.Client); err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, fmt.Sprintf("cannot record the acceptance: %v", err))
			return
		}
	}
	job, ok := h.startInstall(name, backend, plan.Parts)
	if !ok {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, "another install is running: wait for it or cancel it")
		return
	}
	api.WriteJSON(w, http.StatusAccepted, map[string]any{"install": job})
}

type installRefusal struct {
	status         int
	reason, detail string
}

type installPlanned struct {
	InstallPlan
	Name string `json:"-"`
}

// planInstall works out what installing would do for an engine and a backend. It starts and changes nothing.
func (h *Handler) planInstall(ctx context.Context, name, backend string, asked []string) (installPlanned, *installRefusal) {
	info, _ := generate.Info(name)
	svc := h.svc.WithBackend(backend)
	if backend == musicsvc.BackendDotnet && !generate.HasService(name) {
		return installPlanned{}, &installRefusal{http.StatusUnprocessableEntity, "", info.Name + " has no .NET service: it runs in the container (backend docker)"}
	}
	a := svc.Availability(ctx, name)
	missing := generate.MissingFor(name, backend)(h.svc.ModelsDir)
	have := map[string]bool{}
	switch backend {
	case musicsvc.BackendDotnet:
		if svc.Services == nil || svc.Services.DotnetFound() != nil {
			return installPlanned{}, &installRefusal{http.StatusUnprocessableEntity, musicsvc.ReasonNoService, "dotnet is not installed: the " + info.Name + " service needs the .NET SDK 8 or newer"}
		}
		have[partService] = true
		if info.Gated && generate.NeedsKey(missing) {
			// the weights cannot be fetched until the account has what Hugging Face asks for: say that first
			if reason, detail := svc.CheckGate(ctx); reason != "" {
				return installPlanned{}, &installRefusal{http.StatusUnprocessableEntity, reason, detail}
			}
		}
	default:
		switch a.Reason {
		case musicsvc.ReasonNoDocker, musicsvc.ReasonNoGPURuntime, generate.ReasonNotBuilt, generate.ReasonKeyMissing, musicsvc.ReasonKeyRejected, generate.ReasonLicenceNotAccepted:
			return installPlanned{}, &installRefusal{http.StatusUnprocessableEntity, a.Reason, a.Detail}
		case musicsvc.ReasonNoImage:
			if name != generate.EngineTripoSR {
				return installPlanned{}, &installRefusal{http.StatusUnprocessableEntity, a.Reason, a.Detail} // the image with Stable Fast 3D is built by hand (docker/model3d/README.md)
			}
		case musicsvc.ReasonUnreachable:
			if info.Gated {
				return installPlanned{}, &installRefusal{http.StatusUnprocessableEntity, a.Reason, a.Detail}
			}
		}
		have[partImage] = true
	}
	have[partWeights] = true
	var parts []string
	for _, p := range installOrder {
		if !have[p] {
			continue
		}
		need := false
		switch p {
		case partService:
			need = a.Reason == musicsvc.ReasonNoService
		case partImage:
			need = a.Reason == musicsvc.ReasonNoImage
		case partWeights:
			need = len(missing) > 0
		}
		if need {
			parts = append(parts, p)
		}
	}
	if len(asked) > 0 {
		want := map[string]bool{}
		for _, p := range asked {
			p = strings.ToLower(strings.TrimSpace(p))
			if !have[p] {
				return installPlanned{}, &installRefusal{http.StatusUnprocessableEntity, "", fmt.Sprintf("the part %q does not exist for the %s backend", p, backend)}
			}
			want[p] = true
		}
		var kept []string
		for _, p := range parts {
			if want[p] {
				kept = append(kept, p)
			}
		}
		parts = kept
	}
	if parts == nil {
		parts = []string{}
	}
	plan := InstallPlan{Engine: name, Backend: backend, Parts: parts, Missing: missing, Licence: info.Terms, Gated: info.Gated, Path: h.svc.ModelsDir}
	if info.Terms.ID != "" {
		accepted := h.acceptances().Accepted(info.Terms.ID)
		plan.Licence.Accepted = &accepted
	}
	if len(missing) > 0 {
		_ = os.MkdirAll(h.svc.ModelsDir, 0o755)
		plan.Bytes, plan.FreeBytes = generate.Spec(name).Sizes.Weights, freeBytes(h.svc.ModelsDir)
	}
	return installPlanned{InstallPlan: plan, Name: info.Name}, nil
}

func (h *Handler) startInstall(engine, backend string, parts []string) (InstallJob, bool) {
	m := &h.installs
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active != "" {
		return InstallJob{}, false
	}
	m.counter++
	id := fmt.Sprintf("inst3d-%d-%d", time.Now().Unix(), m.counter)
	job := &InstallJob{ID: id, Engine: engine, Backend: backend, Parts: parts, State: "running", Log: []string{}, CreatedAt: time.Now().UTC()}
	if m.jobs == nil {
		m.jobs, m.cancels = map[string]*InstallJob{}, map[string]context.CancelFunc{}
	}
	m.jobs[id] = job
	ctx, cancel := context.WithCancel(context.Background())
	m.cancels[id], m.active = cancel, id
	go h.runInstall(ctx, job)
	return *job, true
}

// runInstall runs the parts in order. The container's weights are fetched by `docker run --rm` of the image with the weights folder mounted read-write
// and, for a gated engine, the Hugging Face key passed by the environment of the docker program (never in its arguments); the .NET service's are
// downloaded here.
func (h *Handler) runInstall(ctx context.Context, job *InstallJob) {
	m := &h.installs
	finish := func(state, errText string) {
		m.mu.Lock()
		defer m.mu.Unlock()
		now := time.Now().UTC()
		job.State, job.Error, job.Part, job.FinishedAt = state, errText, "", &now
		m.active = ""
		delete(m.cancels, job.ID)
	}
	log := func(line string) {
		m.mu.Lock()
		job.Log = append(job.Log, line)
		if len(job.Log) > 200 {
			job.Log = job.Log[len(job.Log)-200:]
		}
		m.mu.Unlock()
	}
	logLines := func(out []byte) {
		sc := bufio.NewScanner(strings.NewReader(string(out)))
		for sc.Scan() {
			log(sc.Text())
		}
	}
	for _, part := range job.Parts {
		m.mu.Lock()
		job.Part = part
		m.mu.Unlock()
		var err error
		switch part {
		case partService:
			script, _ := filepath.Abs("scripts/get-services.sh")
			var out []byte
			args := []string{script, "--only", generate.Spec(job.Engine).Service.Dir}
			if info, _ := generate.Info(job.Engine); info.Terms.ID != "" {
				args = append(args, "--accept-licence", info.Terms.ID) // the job exists only because the request carried that acceptance
			}
			out, err = h.svc.Runner.Run(ctx, "bash", args)
			logLines(out)
		case partImage:
			dir, _ := filepath.Abs("docker/model3d")
			var out []byte
			out, err = h.svc.Runner.Run(ctx, musicsvc.DockerProgram(), []string{"build", "-t", generate.Image, dir})
			logLines(out)
		default:
			if job.Backend == musicsvc.BackendDotnet {
				key := ""
				if h.svc.HFKey != nil {
					key = h.svc.HFKey()
				}
				err = h.Fetcher.Fetch(ctx, job.Engine, h.svc.ModelsDir, key, log)
				break
			}
			args := []string{"run", "--rm", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "-e", "HOME=/tmp", "-v", h.svc.ModelsDir + ":/ckpt"}
			var opts []runner.Option
			if info, _ := generate.Info(job.Engine); info.Gated && h.svc.HFKey != nil {
				args = append(args, "-e", "HF_TOKEN")
				opts = append(opts, runner.WithEnv("HF_TOKEN="+strings.TrimSpace(h.svc.HFKey())))
			}
			args = append(args, generate.Image, "python", "/opt/fetch.py", job.Engine)
			var out []byte
			out, err = h.svc.Runner.Run(ctx, musicsvc.DockerProgram(), args, opts...)
			logLines(out)
		}
		switch {
		case ctx.Err() != nil:
			finish("cancelled", "")
			return
		case err != nil:
			finish("failed", part+": "+lastLines(err.Error(), 3))
			return
		}
	}
	finish("done", "")
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// GET /api/models3d/installs/{id}
func (h *Handler) handleInstallGet(w http.ResponseWriter, r *http.Request) {
	j, ok := h.installs.get(r.PathValue("id"))
	if !ok {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, "no such install")
		return
	}
	api.WriteJSON(w, http.StatusOK, j)
}

// POST /api/models3d/installs/{id}/cancel
func (h *Handler) handleInstallCancel(w http.ResponseWriter, r *http.Request) {
	m := &h.installs
	id := r.PathValue("id")
	m.mu.Lock()
	cancel, ok := m.cancels[id]
	m.mu.Unlock()
	if !ok {
		if _, known := h.installs.get(id); !known {
			api.WriteError(w, http.StatusNotFound, api.CodeNotFound, "no such install")
			return
		}
		api.WriteError(w, http.StatusConflict, api.CodeConflict, "the install is not running")
		return
	}
	cancel()
	for i := 0; i < 100; i++ { // it ends when the docker program does
		if j, _ := h.installs.get(id); j.State != "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	j, _ := h.installs.get(id)
	api.WriteJSON(w, http.StatusOK, j)
}

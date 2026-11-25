package music

// heartmula.go: the optional HeartMuLa music engine (a service on this machine or on another one):
// whether there is one to use, the address of an external one and a test of it. See specs/heartmula-engine.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"veritmo/internal/music/heartmula"
	"veritmo/internal/platform/runner"
	"veritmo/internal/platform/settings"
	"veritmo/internal/server/api"
)

// engineCache keeps the answer of GET /api/music/engines for a short time, like the model availability.
type engineCache struct {
	mu      sync.Mutex
	at      time.Time
	version uint64
	body    *enginesAnswer
	ttl     time.Duration // overrides availabilityTTL when set (tests)
}

func (c *engineCache) bump() {
	c.mu.Lock()
	c.version++
	c.body = nil
	c.mu.Unlock()
}

type enginesAnswer struct {
	Engines map[string]heartmula.Availability `json:"engines"`
}

func (h *Handler) heartMulaRunnerOrDefault() runner.CommandRunner {
	if h.heartMulaRunner != nil {
		return h.heartMulaRunner
	}
	return h.host.EnvRunner()
}

func (h *Handler) heartMulaWeightsDir() string {
	if h.heartMulaWeights != "" {
		return h.heartMulaWeights
	}
	abs, err := filepath.Abs(heartmula.WeightsDir)
	if err != nil {
		return heartmula.WeightsDir
	}
	return abs
}

func (h *Handler) heartMulaEngine() *heartmula.Engine {
	return &heartmula.Engine{
		Local:    heartmula.Local{Runner: h.heartMulaRunnerOrDefault(), Models: h.heartMulaWeightsDir(), Services: h.host.Services(), Backend: settings.MusicBackend("heartmula")},
		External: heartmula.ExternalFromSettings(),
		NoStart:  true, // the service is started from Environment; a run never starts it
	}
}

// GET /api/music/engines -> {"engines": {"heartmula": {available, mode?, reason?, detail?, missing?, version?}}}
//
// It asks the service nothing but /health (or the service and the weights for a local one), starts no
// service and is kept 30 seconds.
func (h *Handler) handleMusicEngines(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("stats") == "1" { // the memory the services use: asked every time
		api.WriteJSON(w, http.StatusOK, h.computeEngines(r.Context(), true))
		return
	}
	api.WriteJSON(w, http.StatusOK, h.enginesAnswer(r.Context()))
}

// enginesAnswer is the cached answer of the engines (the environment report uses it too).
func (h *Handler) enginesAnswer(ctx context.Context) *enginesAnswer {
	ttl := h.engines.ttl
	if ttl == 0 {
		ttl = availabilityTTL
	}
	h.engines.mu.Lock()
	if h.engines.body != nil && time.Since(h.engines.at) < ttl {
		body := h.engines.body
		h.engines.mu.Unlock()
		return body
	}
	version := h.engines.version
	h.engines.mu.Unlock()

	body := h.computeEngines(ctx, false)

	h.engines.mu.Lock()
	if h.engines.version == version {
		h.engines.body, h.engines.at = body, time.Now()
	}
	h.engines.mu.Unlock()
	return body
}

// computeEngines asks each generator for its availability and puts on it what the server knows of its container.
func (h *Handler) computeEngines(ctx context.Context, stats bool) *enginesAnswer {
	out := &enginesAnswer{Engines: map[string]heartmula.Availability{}}
	for _, name := range []string{engineHeartMuLa, engineDiffRhythm2, engineCLAP, engineVideoVision} {
		core, _ := h.engineOf(name, stats)
		a := core.Availability(ctx)
		a.Backends = core.Backends(ctx)
		h.overlay(name, &a)
		out.Engines[name] = a
	}
	return out
}

// heartMulaSettingsView is the saved address of an external HeartMuLa service. The token is never sent back.
type heartMulaSettingsView struct {
	Saved    string `json:"saved"`
	TokenSet bool   `json:"token_set"`
	// Remote is true when the address is not this machine: lyrics and tags leave this computer.
	Remote bool `json:"remote"`
	// Consented says the user accepted sending them to that address (always true for this machine).
	Consented bool `json:"consented"`
	// Source says where the address comes from: setting, env (the HEARTMULA_HOST variable, used while nothing is saved) or default (none).
	Source string `json:"source"`
	// Backend is how it runs on this machine: dotnet (the service) or docker (its container); BackendSource where that comes from (setting or default).
	// An address in Saved wins over both.
	Backend       string `json:"backend"`
	BackendSource string `json:"backend_source"`
}

// CurrentHeartMulaSettings is the saved address of the HeartMuLa service, whether a key is set and whether the user
// accepted sending lyrics there.
func CurrentHeartMulaSettings() heartMulaSettingsView {
	host, token := settings.HeartMula()
	v := heartMulaSettingsView{Saved: host, TokenSet: token != "", Source: settings.HeartMulaSource(), Backend: settings.MusicBackend("heartmula"), BackendSource: settings.MusicBackendSource("heartmula")}
	if host != "" {
		v.Remote = settings.Remote(host)
		v.Consented = !v.Remote || settings.HeartMulaAcknowledged(host)
	}
	return v
}

type heartMulaTestResult struct {
	OK      bool   `json:"ok"`
	Host    string `json:"host"`
	Remote  bool   `json:"remote"`
	Version string `json:"version,omitempty"`
	GPU     string `json:"gpu,omitempty"`
	FreeMB  int64  `json:"free_mb,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Error   string `json:"error,omitempty"`
}

// POST /api/settings/heartmula/test {"host"?: "...", "token"?: "..."}  (default: the saved address)
//
//	-> {ok, host, remote, version?, gpu?, free_mb?, reason?, error?}
//
// An address nobody answers on is a normal answer (ok false). Only GET /health is asked.
func (h *Handler) handleHeartMulaTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host  string  `json:"host"`
		Token *string `json:"token"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil && err != io.EOF {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
			return
		}
	}
	host, token := settings.HeartMula()
	if strings.TrimSpace(req.Host) != "" {
		addr, err := settings.ValidateHeartMulaAddress(req.Host)
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
			return
		}
		host = addr
		token = ""
	}
	if req.Token != nil {
		token = *req.Token
	}
	if host == "" {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "no address to test: give one or save it first")
		return
	}
	api.WriteJSON(w, http.StatusOK, testHeartMula(r.Context(), host, token))
}

func testHeartMula(ctx context.Context, host, token string) heartMulaTestResult {
	res := heartMulaTestResult{Host: host, Remote: settings.Remote(host)}
	h, err := heartmula.NewClient(host, token).Health(ctx)
	switch {
	case errors.Is(err, heartmula.ErrUnauthorized):
		res.Reason, res.Error = heartmula.ReasonKeyRejected, err.Error()
	case err != nil:
		res.Reason, res.Error = heartmula.ReasonUnreachable, err.Error()
	case !h.Ready:
		res.Reason, res.Version = heartmula.ReasonUnreachable, h.Version
		res.Error = "the service answers but is not ready"
		if len(h.Missing) > 0 {
			res.Error += ": it lacks " + strings.Join(h.Missing, ", ")
		}
		if h.GPU == nil {
			res.Error += " (it sees no GPU)"
		}
	default:
		res.OK, res.Version = true, h.Version
		if h.GPU != nil {
			res.GPU, res.FreeMB = h.GPU.Name, h.GPU.FreeMB
		}
	}
	return res
}

package music

// diffrhythm2.go: the optional DiffRhythm2 music engine, the same way music_heartmula.go does the HeartMuLa one: whether
// there is one to use, the address of an external one and a test of it. See specs/diffrhythm2-engine.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"veritmo/internal/music/diffrhythm2"
	"veritmo/internal/platform/settings"
	"veritmo/internal/server/api"
)

// diffRhythm2CLI is the diffrhythm2 executable published next to the service (it installs the weights and recognises
// songs with MuQ-MuLan), or why it is not there.
func (h *Handler) diffRhythm2CLI() (exe, why string) {
	if h.host.Services() == nil {
		return "", "the .NET services are not set up (bash scripts/get-services.sh builds them)"
	}
	exe = filepath.Join(h.host.Services().Dir, "diffrhythm2", "diffrhythm2")
	if _, err := os.Stat(exe); err != nil {
		return "", "the diffrhythm2 executable is not built (bash scripts/get-services.sh --only diffrhythm2)"
	}
	return exe, ""
}

func (h *Handler) diffRhythm2WeightsDir() string {
	if h.diffRhythm2Weights != "" {
		return h.diffRhythm2Weights
	}
	abs, err := filepath.Abs(diffrhythm2.WeightsDir)
	if err != nil {
		return diffrhythm2.WeightsDir
	}
	return abs
}

func (h *Handler) diffRhythm2Engine() *diffrhythm2.Engine {
	return &diffrhythm2.Engine{
		Local:    diffrhythm2.Local{Runner: h.heartMulaRunnerOrDefault(), Models: h.diffRhythm2WeightsDir(), Services: h.host.Services(), Backend: settings.MusicBackend("diffrhythm2")},
		External: diffrhythm2.ExternalFromSettings(),
		NoStart:  true, // the service is started from Environment; a run never starts it
	}
}

// diffRhythm2SettingsView is the saved address of an external DiffRhythm2 service. The token is never sent back.
type diffRhythm2SettingsView struct {
	Saved    string `json:"saved"`
	TokenSet bool   `json:"token_set"`
	// Remote is true when the address is not this machine: lyrics and style leave this computer.
	Remote bool `json:"remote"`
	// Consented says the user accepted sending them to that address (always true for this machine).
	Consented bool `json:"consented"`
	// Source says where the address comes from: setting, env (the DIFFRHYTHM2_HOST variable, used while nothing is saved) or default (none).
	Source string `json:"source"`
	// Backend and BackendSource as in the HeartMuLa settings.
	Backend       string `json:"backend"`
	BackendSource string `json:"backend_source"`
}

// CurrentDiffRhythm2Settings is the saved address of the DiffRhythm2 service, whether a key is set and whether the user
// accepted sending lyrics there.
func CurrentDiffRhythm2Settings() diffRhythm2SettingsView {
	host, token := settings.DiffRhythm2()
	v := diffRhythm2SettingsView{Saved: host, TokenSet: token != "", Source: settings.DiffRhythm2Source(), Backend: settings.MusicBackend("diffrhythm2"), BackendSource: settings.MusicBackendSource("diffrhythm2")}
	if host != "" {
		v.Remote = settings.Remote(host)
		v.Consented = !v.Remote || settings.DiffRhythm2Acknowledged(host)
	}
	return v
}

// POST /api/settings/diffrhythm2/test {"host"?: "...", "token"?: "..."}  (default: the saved address)
//
//	-> {ok, host, remote, version?, gpu?, free_mb?, reason?, error?}
//
// An address nobody answers on is a normal answer (ok false). Only GET /health is asked.
func (h *Handler) handleDiffRhythm2Test(w http.ResponseWriter, r *http.Request) {
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
	host, token := settings.DiffRhythm2()
	if strings.TrimSpace(req.Host) != "" {
		addr, err := settings.ValidateDiffRhythm2Address(req.Host)
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
	api.WriteJSON(w, http.StatusOK, testDiffRhythm2(r.Context(), host, token))
}

func testDiffRhythm2(ctx context.Context, host, token string) heartMulaTestResult {
	res := heartMulaTestResult{Host: host, Remote: settings.Remote(host)}
	h, err := diffrhythm2.NewClient(host, token).Health(ctx)
	switch {
	case errors.Is(err, diffrhythm2.ErrUnauthorized):
		res.Reason, res.Error = diffrhythm2.ReasonKeyRejected, err.Error()
	case err != nil:
		res.Reason, res.Error = diffrhythm2.ReasonUnreachable, err.Error()
	case !h.Ready:
		res.Reason, res.Version = diffrhythm2.ReasonUnreachable, h.Version
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

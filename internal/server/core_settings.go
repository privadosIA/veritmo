package server

// core_settings.go: the server's own settings (for now the address of Ollama), kept in
// ~/.config/veritmo/settings.json by internal/platform/settings and taking effect
// without a restart. See specs/ollama-settings.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
	"veritmo/internal/server/music"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/assistant/llm"
	"veritmo/internal/platform/settings"
	"veritmo/internal/server/api"
	"veritmo/internal/video/motion"
)

type ollamaSettingsView struct {
	// Host is the address in use, Source where it comes from (setting, env, default).
	Host   string `json:"host"`
	Source string `json:"source"`
	// Remote is true when the address is not this machine: pictures, clips and
	// texts sent to Ollama leave this computer.
	Remote bool `json:"remote"`
	// Saved is the address saved in the settings, or empty.
	Saved string `json:"saved"`
}

func currentOllamaSettings() ollamaSettingsView {
	r := settings.Ollama()
	return ollamaSettingsView{Host: r.Host, Source: r.Source, Remote: r.Remote, Saved: strings.TrimSpace(settings.Load().OllamaHost)}
}

// browserSettingsView is the browser that renders motion graphics.
type browserSettingsView struct {
	// Saved is the path saved in the settings, or empty (search the PATH).
	Saved string `json:"saved"`
	// Found is whether a browser can be used now; Path and Version describe it.
	Found   bool   `json:"found"`
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
	// Error says why none can be used, with how to fix it.
	Error string `json:"error,omitempty"`
}

func currentBrowserSettings() browserSettingsView {
	v := browserSettingsView{Saved: settings.Browser()}
	b, err := motion.Find(v.Saved)
	if err != nil {
		v.Error = err.Error()
		return v
	}
	v.Found, v.Path, v.Version = true, b.Path, b.Version
	return v
}

// motionModelView is the model that writes motion-graphic pages.
type motionModelView struct {
	// Saved is the model saved in the settings, or empty.
	Saved string `json:"saved"`
	// Default is the one used when none is saved (the assistant's).
	Default string `json:"default"`
}

// currentMotionModel says the model that writes the pages and the one that would with none saved. Working out the second
// asks the provider which models it has, for at most s.motionModelTimeout (4 s when it is zero).
func (s *Server) currentMotionModel() motionModelView {
	def := assist.ModelFromEnv("", os.Getenv)
	if res, err := llm.For(settings.TaskMotionGraphics, ""); err == nil {
		// What would write the pages with none saved: a preferred installed model, the provider's, the assistant's.
		timeout := s.motionModelTimeout
		if timeout == 0 {
			timeout = 4 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		def = motion.DefaultModel(ctx, res)
	}
	return motionModelView{Saved: settings.MotionModel(), Default: def}
}

// GET /api/settings -> {"ollama": {host, source, remote, saved}, "remote": {"max_calls": N}}
func (s *Server) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, http.StatusOK, s.settingsBody())
}

func (s *Server) settingsBody() map[string]any {
	return map[string]any{"ollama": currentOllamaSettings(), "remote": map[string]int{"max_calls": settings.MaxCalls()}, "browser": currentBrowserSettings(), "motion_model": s.currentMotionModel(), "heartmula": music.CurrentHeartMulaSettings(), "diffrhythm2": music.CurrentDiffRhythm2Settings(), "music_motion": map[string]bool{"review_drafts": settings.ReviewDrafts()}, "model3d": map[string]string{"backend": settings.Model3DBackend(), "backend_source": settings.Model3DBackendSource()}}
}

// PUT /api/settings {"ollama_host"?: "192.168.1.50:11434", "browser"?: "/usr/bin/chromium"}
// (an empty text removes the setting; at least one key is required)
//
//	-> like GET; 400 with the reason for an invalid address or browser path.
func (s *Server) handleSettingsPut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OllamaHost *string `json:"ollama_host"`
		Browser    *string `json:"browser"`
		// MotionModel is the model that writes motion-graphic pages ('' removes the setting).
		MotionModel *string `json:"motion_model"`
		// HeartMulaHost is the address of an external HeartMuLa service ('' removes it) and HeartMulaToken its token
		// ('' removes it; absent keeps it); HeartMulaAccept says the user accepts sending lyrics and tags to it.
		HeartMulaHost   *string `json:"heartmula_host"`
		HeartMulaToken  *string `json:"heartmula_token"`
		HeartMulaAccept bool    `json:"heartmula_accept"`
		// The same three for the DiffRhythm2 service (lyrics and style).
		DiffRhythm2Host   *string `json:"diffrhythm2_host"`
		DiffRhythm2Token  *string `json:"diffrhythm2_token"`
		DiffRhythm2Accept bool    `json:"diffrhythm2_accept"`
		// The backend of each generator on this machine: "dotnet" (the service) or "docker" (its container); '' removes the setting (the .NET service).
		HeartMulaBackend   *string `json:"heartmula_backend"`
		DiffRhythm2Backend *string `json:"diffrhythm2_backend"`
		// Model3DBackend is the same for TripoSR, the 3D generator ("dotnet" or "docker"; '' removes the setting).
		Model3DBackend *string `json:"model3d_backend"`
		// MusicMotion holds the settings of the music-motion pieces: review_drafts grades every part draft with the vision model.
		MusicMotion *struct {
			ReviewDrafts *bool `json:"review_drafts"`
		} `json:"music_motion"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if req.OllamaHost == nil && req.Browser == nil && req.MotionModel == nil && req.HeartMulaHost == nil && req.HeartMulaToken == nil && !req.HeartMulaAccept && req.DiffRhythm2Host == nil && req.DiffRhythm2Token == nil && !req.DiffRhythm2Accept && req.HeartMulaBackend == nil && req.DiffRhythm2Backend == nil && req.Model3DBackend == nil && (req.MusicMotion == nil || req.MusicMotion.ReviewDrafts == nil) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "ollama_host, browser, motion_model, heartmula_host or diffrhythm2_host is required (a value, or an empty text to remove the setting)")
		return
	}
	var heartHost, heartToken *string
	if req.HeartMulaHost != nil {
		host := ""
		if strings.TrimSpace(*req.HeartMulaHost) != "" {
			addr, err := settings.ValidateHeartMulaAddress(*req.HeartMulaHost)
			if err != nil {
				api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
				return
			}
			host = addr
		}
		heartHost = &host
	}
	if req.HeartMulaToken != nil {
		tok := strings.TrimSpace(*req.HeartMulaToken)
		if len(tok) > 500 {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "the token is too long")
			return
		}
		heartToken = &tok
	}
	var drHost, drToken *string
	if req.DiffRhythm2Host != nil {
		host := ""
		if strings.TrimSpace(*req.DiffRhythm2Host) != "" {
			addr, err := settings.ValidateDiffRhythm2Address(*req.DiffRhythm2Host)
			if err != nil {
				api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
				return
			}
			host = addr
		}
		drHost = &host
	}
	if req.DiffRhythm2Token != nil {
		tok := strings.TrimSpace(*req.DiffRhythm2Token)
		if len(tok) > 500 {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "the token is too long")
			return
		}
		drToken = &tok
	}
	backends := map[string]*string{"heartmula": req.HeartMulaBackend, "diffrhythm2": req.DiffRhythm2Backend}
	chosen := map[string]string{}
	for _, name := range []string{"heartmula", "diffrhythm2"} {
		raw := backends[name]
		if raw == nil {
			continue
		}
		b := ""
		if strings.TrimSpace(*raw) != "" {
			v, err := settings.ValidateMusicBackend(*raw)
			if err != nil {
				api.WriteJSON(w, http.StatusUnprocessableEntity, api.APIError{Error: api.APIErrorBody{Code: api.CodeValidation, Message: name + "_backend: " + err.Error()}})
				return
			}
			b = v
		}
		if berr := s.music.PrepareBackend(r.Context(), name, b); berr != nil {
			code := api.CodeValidation
			if berr.Status == http.StatusConflict {
				code = api.CodeConflict
			} else if berr.Status >= 500 {
				code = api.CodeInternal
			}
			api.WriteJSON(w, berr.Status, api.APIError{Error: api.APIErrorBody{Code: code, Message: berr.Msg, Reason: berr.Reason}})
			return
		}
		chosen[name] = b
	}
	model3dBackend, setModel3D := "", false
	if req.Model3DBackend != nil {
		if strings.TrimSpace(*req.Model3DBackend) != "" {
			v, err := settings.ValidateMusicBackend(*req.Model3DBackend)
			if err != nil {
				api.WriteJSON(w, http.StatusUnprocessableEntity, api.APIError{Error: api.APIErrorBody{Code: api.CodeValidation, Message: "model3d_backend: " + err.Error()}})
				return
			}
			model3dBackend = v
		}
		if berr := s.models3d.PrepareBackend(r.Context(), model3dBackend); berr != nil {
			code := api.CodeValidation
			if berr.Status == http.StatusConflict {
				code = api.CodeConflict
			} else if berr.Status >= 500 {
				code = api.CodeInternal
			}
			api.WriteJSON(w, berr.Status, api.APIError{Error: api.APIErrorBody{Code: code, Message: berr.Msg, Reason: berr.Reason}})
			return
		}
		setModel3D = true
	}
	f := settings.Load()
	if req.OllamaHost != nil {
		if strings.TrimSpace(*req.OllamaHost) == "" {
			f.OllamaHost = ""
		} else {
			addr, err := settings.ValidateAddress(*req.OllamaHost)
			if err != nil {
				api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
				return
			}
			f.OllamaHost = addr
		}
	}
	if req.MotionModel != nil {
		m, err := settings.ValidateModel(*req.MotionModel)
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
			return
		}
		f.MotionModel = m
	}
	if req.MusicMotion != nil && req.MusicMotion.ReviewDrafts != nil {
		f.ReviewDrafts = *req.MusicMotion.ReviewDrafts
	}
	if req.Browser != nil {
		path := strings.TrimSpace(*req.Browser)
		if path != "" {
			if _, err := motion.Find(path); err != nil {
				api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
				return
			}
		}
		f.Browser = path
	}
	if err := settings.Save(f); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, fmt.Sprintf("cannot save the settings: %v", err))
		return
	}
	if heartHost != nil || heartToken != nil {
		host, token := settings.HeartMula()
		if heartHost != nil {
			host = *heartHost
		}
		if heartToken != nil {
			token = *heartToken
		}
		if err := settings.SetHeartMula(host, token); err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, fmt.Sprintf("cannot save the settings: %v", err))
			return
		}
	}
	if req.HeartMulaAccept {
		if err := settings.AcknowledgeHeartMula(); err != nil {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
			return
		}
	}
	if drHost != nil || drToken != nil {
		host, token := settings.DiffRhythm2()
		if drHost != nil {
			host = *drHost
		}
		if drToken != nil {
			token = *drToken
		}
		if err := settings.SetDiffRhythm2(host, token); err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, fmt.Sprintf("cannot save the settings: %v", err))
			return
		}
	}
	if req.DiffRhythm2Accept {
		if err := settings.AcknowledgeDiffRhythm2(); err != nil {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
			return
		}
	}
	for name, b := range chosen {
		if err := settings.SetMusicBackend(name, b); err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, fmt.Sprintf("cannot save the settings: %v", err))
			return
		}
	}
	if len(chosen) > 0 {
		s.music.BumpEngines()
	}
	if setModel3D {
		if err := settings.SetModel3DBackend(model3dBackend); err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, fmt.Sprintf("cannot save the settings: %v", err))
			return
		}
	}
	api.WriteJSON(w, http.StatusOK, s.settingsBody())
}

// POST /api/settings/ollama/test {"host"?: "..."}  (default: the address in use)
//
//	-> {ok, host, remote, version?, models?: [{name, vision}], error?}
//
// An address nobody answers on is a normal answer (ok false), not an error status.
// Nothing of the user's is sent: only the version, tags and show requests of Ollama.
func (s *Server) handleOllamaTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host string `json:"host"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil && err != io.EOF {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
			return
		}
	}
	host := settings.OllamaURL()
	if strings.TrimSpace(req.Host) != "" {
		addr, err := settings.ValidateAddress(req.Host)
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
			return
		}
		host = addr
	}
	api.WriteJSON(w, http.StatusOK, api.ProbeOllama(r.Context(), host))
}

package library3d

// generations.go is the one-call API of the 3D models (spec model-generation-api): a prompt or a picture becomes a hidden project in models3d mode
// (api-m3d-…) with one entry, that the run queue makes; the state and the step come from its run and its events, the model is the one that run
// put in the library. The project is a managed one like the generations of videos and the picture edits, so it carries the `client` label and shows up
// with `managed=include`.

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"veritmo/internal/assets3d"
	"veritmo/internal/assets3d/generate"
	"veritmo/internal/platform/store"
	"veritmo/internal/project/config"
	"veritmo/internal/server/api"
	"veritmo/internal/server/workspace"
)

// Workspace is what the generations ask of the workspace family: the managed projects and their runs (a *workspace.Handler is one).
type Workspace interface {
	CreateManaged(info *workspace.ManagedInfo, scenes []byte, picture []byte, pictureName string) (workspace.Run, *workspace.LaunchError)
	ReadManaged(id string) (*workspace.ManagedInfo, bool)
	ManagedPage(prefix string, clients []string, params store.ListParams) ([]*workspace.ManagedInfo, int64, error)
	RunRecord(id string) (*workspace.Run, error)
	RunEvents(id string) ([]string, error)
	CancelRun(id string) (workspace.Run, *workspace.LaunchError)
	RemoveManaged(id string) *workspace.LaunchError
	// UsesModels3D says whether a queued or running run makes 3D models.
	UsesModels3D() bool
}

// SetWorkspace gives the handler the workspace the generations are made in; without it their routes answer that the server has none.
func (h *Handler) SetWorkspace(ws Workspace) { h.ws = ws }

// Generation states.
const (
	genQueued    = "queued"
	genRunning   = "running"
	genDone      = "done"
	genFailed    = "failed"
	genCancelled = "cancelled"
)

// maxGenerationBody bounds a request: the picture, and room for the other fields of a multipart one.
const maxGenerationBody = generate.MaxImageBytes + 1<<20

// generationRequest is what a call asks for, and what the record of the generation keeps of it.
type generationRequest struct {
	Prompt string  `json:"prompt,omitempty"`
	Engine string  `json:"engine,omitempty"`
	Detail string  `json:"detail,omitempty"`
	Name   string  `json:"name,omitempty"`
	Client *string `json:"client,omitempty"`
	Adult  *bool   `json:"adult,omitempty"`
}

// generationRecord is what the record of a generation keeps of its call: the request, and the name its picture has in the sources of its project.
type generationRecord struct {
	generationRequest
	Image string `json:"image,omitempty"`
}

// Exported under the names the contract tests and the other families use.
type (
	// GenerationView is a generation as the API shows it.
	GenerationView = generationView
	// GenerationRequest is the body of POST /api/models3d/generations (as JSON; the same fields travel as text in a multipart one).
	GenerationRequest = generationRequest
)

// generationView is a generation as the API shows it.
type generationView struct {
	ID     string `json:"id"`
	Client string `json:"client"`
	State  string `json:"state"`
	// Step is the step a running generation is in: picture (the picture of the prompt) or model.
	Step   string `json:"step,omitempty"`
	Prompt string `json:"prompt,omitempty"`
	Engine string `json:"engine"`
	Detail string `json:"detail"`
	Name   string `json:"name"`
	// Model is the <pack>/<name> in the library, once it is made.
	Model      string     `json:"model,omitempty"`
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Run        string     `json:"run"`
}

func (h *Handler) needWorkspace(w http.ResponseWriter) bool {
	if h.ws == nil || h.host == nil {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, "this server cannot make projects: there is no workspace")
		return false
	}
	return true
}

func bad(w http.ResponseWriter, format string, args ...any) {
	api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf(format, args...))
}

// readGenerationRequest reads the JSON body, or the multipart one with the picture in the `image` part and the other fields as text. It answers the 400
// itself; image is nil when the call has none.
func readGenerationRequest(w http.ResponseWriter, r *http.Request) (req generationRequest, image []byte, ok bool) {
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	api.RaiseBodyLimit(w, r, maxGenerationBody)
	switch ct {
	case "multipart/form-data":
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			bad(w, "the multipart body cannot be read: %v", err)
			return req, nil, false
		}
		req.Prompt, req.Engine, req.Detail, req.Name = r.FormValue("prompt"), r.FormValue("engine"), r.FormValue("detail"), r.FormValue("name")
		if v, has := r.MultipartForm.Value["client"]; has && len(v) > 0 {
			req.Client = &v[0]
		}
		if v := strings.ToLower(strings.TrimSpace(r.FormValue("adult"))); v != "" {
			switch v {
			case "true", "false":
				b := v == "true"
				req.Adult = &b
			default:
				bad(w, "adult must be true or false")
				return req, nil, false
			}
		}
		f, hdr, err := r.FormFile("image")
		if err == nil {
			defer f.Close()
			if hdr.Size > generate.MaxImageBytes {
				api.WriteError(w, http.StatusRequestEntityTooLarge, api.CodeInvalid, fmt.Sprintf("the image part is larger than %d MB", generate.MaxImageBytes>>20))
				return req, nil, false
			}
			image, err = io.ReadAll(io.LimitReader(f, generate.MaxImageBytes+1))
			if err != nil || len(image) == 0 {
				bad(w, "the image part is empty or cannot be read")
				return req, nil, false
			}
		}
	case "application/json", "":
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			bad(w, "the JSON body is not a generation request (a picture goes in a multipart request, as the image part): %v", err)
			return req, nil, false
		}
	default:
		bad(w, "send application/json or multipart/form-data, not %s", ct)
		return req, nil, false
	}
	return req, image, true
}

// imageName is the name of a picture in the sources of its project, from its type.
func imageName(image []byte) string {
	switch http.DetectContentType(image) {
	case "image/jpeg":
		return "picture.jpg"
	case "image/webp":
		return "picture.webp"
	}
	return "picture.png"
}

// POST /api/models3d/generations
func (h *Handler) handleGenerationCreate(w http.ResponseWriter, r *http.Request) {
	if !h.needWorkspace(w) {
		return
	}
	req, image, ok := readGenerationRequest(w, r)
	if !ok {
		return
	}
	in, err := generate.Input{Prompt: req.Prompt, Image: image, Engine: req.Engine, Detail: req.Detail}.Validate()
	if err != nil {
		bad(w, "%v", err)
		return
	}
	req.Prompt, req.Engine, req.Detail = in.Prompt, in.Engine, in.Detail
	if req.Name == "" {
		req.Name = assets3d.SlugOf(in.Prompt)
	}
	if !config.ValidModelID(req.Name) {
		bad(w, "name %q must be a plain lower-case name (letters, digits, '.', '_' or '-', not starting with a symbol, up to 64 characters)", req.Name)
		return
	}
	client := workspace.ClientUnknown
	if req.Client != nil {
		if !config.ValidClient(*req.Client) {
			bad(w, "client %q is not a client name: use %s, one to 32 lowercase letters, digits or hyphens", *req.Client, config.ClientPatternText)
			return
		}
		client = *req.Client
	}
	if h.host.RefuseAdultConfig(w, req.Adult) {
		return
	}
	// What the call needs has to be there before anything is queued: the engine, and for a prompt an image model to draw its picture with.
	if h.svc == nil {
		h.needService(w)
		return
	}
	if a := h.svc.Availability(r.Context(), in.Engine); !a.Available {
		unavailable(w, http.StatusServiceUnavailable, a.Reason, firstText(a.Detail, "the engine "+in.Engine+" cannot be used now"))
		return
	}
	var defaults config.Defaults
	picture := ""
	if in.Prompt != "" {
		if defaults, err = h.host.PictureDefaults(); err != nil {
			unavailable(w, http.StatusServiceUnavailable, "no_image_model", "the picture of a prompt cannot be made: "+err.Error())
			return
		}
	} else {
		defaults = config.Defaults{Sampler: "euler", Resolution: "768x768", Steps: 20}
		picture = imageName(image)
	}

	id := workspace.NewManagedID(workspace.ModelGenPrefix)
	entry := config.ModelEntry{ID: req.Name, Prompt: in.Prompt, Image: picture, Engine: in.Engine, Detail: in.Detail, Adult: req.Adult != nil && *req.Adult}
	cfg := &config.Config{Project: id, Client: client, Adult: req.Adult, Output: config.Output{Mode: config.ModeModels3D}, Defaults: defaults, Models: []config.ModelEntry{entry}}
	yml, err := yaml.Marshal(cfg)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	stored := generationRecord{generationRequest: req, Image: picture}
	stored.Client = &client
	body, _ := json.Marshal(stored)
	info := &workspace.ManagedInfo{ID: id, Kind: workspace.KindModelGeneration, CreatedAt: time.Now().UTC(), Request: body}
	rec, lerr := h.ws.CreateManaged(info, yml, image, picture)
	if lerr != nil {
		if lerr.Reason != "" {
			api.WriteJSON(w, lerr.Status, api.APIError{Error: api.APIErrorBody{Code: lerr.Code, Message: lerr.Msg, Reason: lerr.Reason}})
			return
		}
		api.WriteError(w, lerr.Status, lerr.Code, lerr.Msg)
		return
	}
	api.WriteJSON(w, http.StatusAccepted, h.generationViewOf(info, &rec))
}

func firstText(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// stepOf is the step a run is in, from its events: the picture while the picture phase is the last that began, the model after.
func (h *Handler) stepOf(runID string) string {
	lines, err := h.ws.RunEvents(runID)
	if err != nil {
		return ""
	}
	step := ""
	for _, l := range lines {
		var ev struct {
			Type  string `json:"type"`
			Phase string `json:"phase"`
		}
		if json.Unmarshal([]byte(l), &ev) != nil || ev.Type != "phase_start" {
			continue
		}
		switch ev.Phase {
		case "render":
			step = "picture"
		case "model3d":
			step = "model"
		}
	}
	return step
}

// generationViewOf reads a generation back: its state and step from the run, its model from the library.
func (h *Handler) generationViewOf(info *workspace.ManagedInfo, run *workspace.Run) generationView {
	var req generationRecord
	_ = json.Unmarshal(info.Request, &req)
	client := workspace.ClientUnknown
	if req.Client != nil {
		client = *req.Client
	}
	v := generationView{ID: info.ID, Client: client, Prompt: req.Prompt, Engine: req.Engine, Detail: req.Detail, Name: req.Name, CreatedAt: info.CreatedAt, Run: run.ID, FinishedAt: run.FinishedAt}
	switch run.State {
	case workspace.StateQueued:
		v.State = genQueued
	case workspace.StateRunning:
		v.State = genRunning
		v.Step = h.stepOf(run.ID)
	case workspace.StateSucceeded:
		v.State = genDone
	case workspace.StateCancelled:
		v.State, v.Error = genCancelled, "cancelled"
	case workspace.StateInterrupted:
		v.State, v.Error = genFailed, "interrupted: the server stopped before it ended"
	default:
		v.State, v.Error = genFailed, firstText(run.Error, "the run failed")
	}
	if v.State == genDone {
		if models, _ := h.lib.Models(assets3d.Query{Pack: assets3d.GeneratedPackID, Project: info.ID, Limit: 1}); len(models) > 0 {
			v.Model = models[0].ID
		} else {
			v.State, v.Error = genFailed, "the model is not in the library any more"
		}
	}
	return v
}

// generationOf is the generation with an id, with its record and its run; false when there is none.
func (h *Handler) generationOf(id string) (generationView, bool) {
	if !strings.HasPrefix(id, workspace.ModelGenPrefix) {
		return generationView{}, false
	}
	info, ok := h.ws.ReadManaged(id)
	if !ok || info.Kind != workspace.KindModelGeneration {
		return generationView{}, false
	}
	run, err := h.ws.RunRecord(info.RunID)
	if err != nil {
		return generationView{}, false
	}
	return h.generationViewOf(info, run), true
}

// GET /api/models3d/generations?limit=&offset=&sort=&client=
func (h *Handler) handleGenerationList(w http.ResponseWriter, r *http.Request) {
	if !h.needWorkspace(w) {
		return
	}
	params, clients, ok := workspace.ManagedPageParams(w, r)
	if !ok {
		return
	}
	infos, total, err := h.ws.ManagedPage(workspace.ModelGenPrefix, clients, params)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	out := []generationView{}
	for _, info := range infos {
		if v, ok := h.generationOf(info.ID); ok {
			out = append(out, v)
		}
	}
	store.WriteListHeaders(w, r, total, params)
	api.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) visibleGeneration(w http.ResponseWriter, r *http.Request) (generationView, bool) {
	id := r.PathValue("id")
	if v, ok := h.generationOf(id); ok && h.host.ProjectVisible(id) {
		return v, true
	}
	api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("generation %q not found", id))
	return generationView{}, false
}

// GET /api/models3d/generations/{id}
func (h *Handler) handleGenerationGet(w http.ResponseWriter, r *http.Request) {
	if !h.needWorkspace(w) {
		return
	}
	if v, ok := h.visibleGeneration(w, r); ok {
		api.WriteJSON(w, http.StatusOK, v)
	}
}

// POST /api/models3d/generations/{id}/cancel
func (h *Handler) handleGenerationCancel(w http.ResponseWriter, r *http.Request) {
	if !h.needWorkspace(w) {
		return
	}
	v, ok := h.visibleGeneration(w, r)
	if !ok {
		return
	}
	if v.State != genQueued && v.State != genRunning {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("generation %q is %s: only one that is queued or running can be cancelled", v.ID, v.State))
		return
	}
	if _, lerr := h.ws.CancelRun(v.Run); lerr != nil {
		api.WriteError(w, lerr.Status, lerr.Code, lerr.Msg)
		return
	}
	if v, ok := h.generationOf(r.PathValue("id")); ok {
		api.WriteJSON(w, http.StatusOK, v)
		return
	}
	api.WriteError(w, http.StatusNotFound, api.CodeNotFound, "generation not found")
}

// DELETE /api/models3d/generations/{id}: removes the generation and its project, not the model it made.
func (h *Handler) handleGenerationDelete(w http.ResponseWriter, r *http.Request) {
	if !h.needWorkspace(w) {
		return
	}
	v, ok := h.visibleGeneration(w, r)
	if !ok {
		return
	}
	if lerr := h.ws.RemoveManaged(v.ID); lerr != nil {
		api.WriteError(w, lerr.Status, lerr.Code, lerr.Msg)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

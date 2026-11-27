package generation

// edit.go is the picture edit of the generation API (spec image-edit-api): a picture, a model and a prompt
// become a project in images mode that the run queue makes, with the results read back from its manifest.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg" // the decoders image.DecodeConfig reads the size of the picture with
	_ "image/png"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"veritmo/internal/platform/store"
	"veritmo/internal/server/workspace"

	"veritmo/internal/project/config"
	"veritmo/internal/project/export"
	"veritmo/internal/server/api"
)

// defaultEditStrength is the strength an img2img edit uses when none is written (the Create page's).
const defaultEditStrength = 0.6

// imageEditRequest is the `request` part of POST /api/image-edits.
type imageEditRequest struct {
	Model          string   `json:"model,omitempty"`
	Prompt         string   `json:"prompt,omitempty"`
	NegativePrompt string   `json:"negative_prompt,omitempty"`
	Strength       *float64 `json:"strength,omitempty"`
	Variants       int      `json:"variants,omitempty"`
	// Adult is the label the client puts on the edit (see generationRequest).
	Adult *bool `json:"adult,omitempty"`
	// Client is the dashboard that asks (see generationRequest).
	Client *string `json:"client,omitempty"`
}

// imageEditResult is one picture of an edit: the seed that makes it again and where to fetch it.
type imageEditResult struct {
	Variant int    `json:"variant"`
	Seed    int64  `json:"seed"`
	Image   string `json:"image"`
}

// imageEditView is an edit as the API shows it.
type imageEditView struct {
	ID        string           `json:"id"`
	State     string           `json:"state"`
	Progress  int              `json:"progress"`
	Request   imageEditRequest `json:"request"`
	Client    string           `json:"client"`
	RunID     string           `json:"run_id"`
	CreatedAt time.Time        `json:"created_at"`
	// SizeBytes is the disk the edit takes (its files).
	SizeBytes int64             `json:"size_bytes"`
	Results   []imageEditResult `json:"results,omitempty"`
	Error     string            `json:"error,omitempty"`
}

// editResolution is the size an edit is made at: the picture's own shape in about the pixels the entry was
// measured at, in multiples of `multiple`. Without the size of the picture, the preset's own.
func editResolution(w, h int, preset string, multiple int) string {
	pw, ph, ok := parseRes(preset)
	if !ok {
		pw, ph = 1024, 576
	}
	if w <= 0 || h <= 0 {
		return fmt.Sprintf("%dx%d", pw, ph)
	}
	budget := float64(pw * ph)
	ratio := float64(w) / float64(h)
	round := func(v float64) int {
		n := int(math.Round(v/float64(multiple))) * multiple
		if n < multiple {
			n = multiple
		}
		return n
	}
	rh := round(math.Sqrt(budget / ratio))
	rw := round(float64(rh) * ratio)
	return fmt.Sprintf("%dx%d", rw, rh)
}

// pictureSize reads the size of a png or jpeg picture; 0, 0 when it cannot (webp, for one).
func pictureSize(data []byte) (int, int) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

// planImageEdit turns a request into the configuration of a project in images mode.
func planImageEdit(id string, req imageEditRequest, model imageModelView, entry catalogEntry, m planMachine, pictureName string, w, h int) *config.Config {
	cfg := &config.Config{
		Project: id,
		Output:  config.Output{Mode: config.ModeImages},
		Defaults: config.Defaults{
			Sampler: "euler", FPS: 30, Resolution: "1024x576", Steps: 20,
		},
	}
	preset := ""
	if entry.Preset != nil {
		preset = entry.Preset.Resolution
	}
	if preset == "" && entry.Timing != nil {
		preset = entry.Timing.Resolution
	}
	scene := config.Scene{ID: "imagen_1", Speed: 1, Prompt: strings.TrimSpace(req.Prompt), InitImage: pictureName}
	if model.Kind == editKindInstruction {
		file := func(role string) string { return "models/" + fileOf(entry, role) }
		ms := &config.ModelSet{Edit: true, DiffusionModel: file("model"), VAE: file("vae")}
		if entry.Preset != nil {
			ms.CfgScale = entry.Preset.CfgScale
		}
		if hasRole(entry, "llm_vision") {
			ms.LLM, ms.LLMVision = file("llm"), file("llm_vision")
		} else {
			ms.ClipL, ms.T5XXL = file("clip_l"), file("t5xxl")
		}
		cfg.Defaults.ModelSet = ms
		cfg.Defaults.Steps = 20
		if entry.Preset != nil && entry.Preset.Steps > 0 {
			cfg.Defaults.Steps = entry.Preset.Steps
		}
		cfg.Defaults.Resolution = editResolution(w, h, preset, 16)
		one := 1
		scene.Variants = &one
	} else {
		applyStyleEntryTo(cfg, entry)
		res := editResolution(w, h, preset, 64)
		if mp := maxPicturePixels(&entry, m); mp > 0 {
			res = fitRes(res, mp)
		}
		cfg.Defaults.Resolution = res
		scene.NegativePrompt = strings.TrimSpace(req.NegativePrompt)
		strength := defaultEditStrength
		if req.Strength != nil {
			strength = *req.Strength
		}
		scene.Strength = &strength
		variants := req.Variants
		scene.Variants = &variants
	}
	cfg.Scenes = []config.Scene{scene}
	return cfg
}

// POST /api/image-edits
func (h *Handler) handleImageEditCreate(w http.ResponseWriter, r *http.Request) {
	var req imageEditRequest
	pic, ok := readRequest(w, r, &req)
	if !ok {
		return
	}
	if pic == nil {
		api.Refuse(w, "the picture to edit is needed: send it as the image part of a multipart request")
		return
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		api.Refuse(w, "the prompt is blank: say what to do with the picture")
		return
	}
	if len(req.Prompt) > 2000 || len(req.NegativePrompt) > 2000 {
		api.Refuse(w, "the prompt is longer than 2000 characters")
		return
	}
	if h.host.RefuseAdultConfig(w, req.Adult) {
		return
	}
	client, ok := requestClient(w, req.Client)
	if !ok {
		return
	}
	req.Client = &client
	cat := h.catalogNow()
	m := h.machineNow()
	models := buildImageModels(cat, m)
	if req.Model == "" {
		for _, mv := range models {
			if mv.Default {
				req.Model = mv.ID
			}
		}
		if req.Model == "" {
			api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, "this machine cannot edit a picture now: no model is available")
			return
		}
	}
	var model *imageModelView
	for i := range models {
		if models[i].ID == req.Model {
			model = &models[i]
		}
	}
	if model == nil {
		api.Refuse(w, "model %q is unknown: use one of the identifiers of GET /api/generation/options", req.Model)
		return
	}
	if !model.Available {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, fmt.Sprintf("model %s cannot run now: %s", model.ID, model.Reason))
		return
	}
	if req.Strength != nil && !model.Strength {
		api.Refuse(w, "model %s does not take a strength: it changes the picture by its prompt", model.ID)
		return
	}
	if req.NegativePrompt != "" && !model.NegativePrompt {
		api.Refuse(w, "model %s does not take a negative prompt", model.ID)
		return
	}
	if req.Strength != nil && (*req.Strength <= 0 || *req.Strength > 1) {
		api.Refuse(w, "strength %g is outside what is accepted: above 0 and at most 1", *req.Strength)
		return
	}
	if req.Variants == 0 {
		req.Variants = 1
	}
	if req.Variants < 1 || req.Variants > model.MaxVariants {
		api.Refuse(w, "variants %d is outside what model %s makes: from 1 to %d", req.Variants, model.ID, model.MaxVariants)
		return
	}
	var entry catalogEntry
	for _, e := range cat {
		if e.ID == model.ID {
			entry = e
		}
	}
	id := newManagedID(editPrefix)
	pw, ph := pictureSize(pic.data)
	cfg := planImageEdit(id, req, *model, entry, m, pic.name, pw, ph)
	adult := entry.Adult // a picture made with a model for adults is labelled so, whatever the request says
	if req.Adult != nil {
		adult = adult || *req.Adult
	}
	cfg.Adult = &adult
	cfg.Client = client
	yml, err := scenesYAML(cfg)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	body, _ := json.Marshal(req)
	info := &managedInfo{ID: id, Kind: kindImageEdit, CreatedAt: time.Now().UTC(), Request: body}
	rec, lerr := h.ws.CreateManaged(info, yml, pic.data, pic.name)
	if lerr != nil {
		if lerr.Reason != "" {
			api.WriteJSON(w, lerr.Status, api.APIError{Error: api.APIErrorBody{Code: lerr.Code, Message: lerr.Msg, Reason: lerr.Reason}})
			return
		}
		api.WriteError(w, lerr.Status, lerr.Code, lerr.Msg)
		return
	}
	api.WriteJSON(w, http.StatusAccepted, h.imageEditViewOf(info, &rec))
}

// An image scene goes through render and export, and the manifest is written by a global export.
const imagePhases = 3

func (h *Handler) imageEditViewOf(info *managedInfo, run *Run) imageEditView {
	var req imageEditRequest
	_ = json.Unmarshal(info.Request, &req)
	state, msg := StateOf(run)
	v := imageEditView{ID: info.ID, State: state, Progress: h.ProgressOf(run, imagePhases), Request: req, Client: clientShown(req.Client), RunID: run.ID, CreatedAt: info.CreatedAt, Error: msg}
	v.SizeBytes = h.ws.ProjectSize(info.ID)
	if state != GenDone {
		return v
	}
	raw, err := os.ReadFile(filepath.Join(h.ws.ProjectDir(info.ID), "out", info.ID, "manifest.json"))
	var man export.Manifest
	if err == nil {
		err = json.Unmarshal(raw, &man)
	}
	if err != nil || len(man.Images) == 0 {
		v.State, v.Error = GenFailed, "the pictures are not there any more"
		return v
	}
	for _, e := range man.Images {
		v.Results = append(v.Results, imageEditResult{Variant: e.Variant + 1, Seed: e.EffectiveSeed, Image: "/api/projects/" + info.ID + "/artifacts/" + e.File})
	}
	return v
}

func (h *Handler) imageEditOf(id string) (imageEditView, bool) {
	if !strings.HasPrefix(id, editPrefix) {
		return imageEditView{}, false
	}
	info, ok := h.ws.ReadManaged(id)
	if !ok || info.Kind != kindImageEdit {
		return imageEditView{}, false
	}
	run, err := h.ws.RunRecord(info.RunID)
	if err != nil {
		return imageEditView{}, false
	}
	return h.imageEditViewOf(info, run), true
}

// GET /api/image-edits[?client=a,b&sort=&limit=&offset=]
//
// The picture edits, newest first, each as GET /api/image-edits/{id} answers it; paged like the other lists of the generation API.
func (h *Handler) handleImageEditList(w http.ResponseWriter, r *http.Request) {
	params, clients, ok := workspace.ManagedPageParams(w, r)
	if !ok {
		return
	}
	infos, total, err := h.ws.ManagedPage(editPrefix, clients, params)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	out := []imageEditView{}
	for _, info := range infos {
		if info.Kind != kindImageEdit {
			continue
		}
		if v, ok := h.imageEditOf(info.ID); ok {
			out = append(out, v)
		}
	}
	store.WriteListHeaders(w, r, total, params)
	api.WriteJSON(w, http.StatusOK, out)
}

// GET /api/image-edits/{id}
func (h *Handler) handleImageEditGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if v, ok := h.imageEditOf(id); ok && h.host.ProjectVisible(id) {
		api.WriteJSON(w, http.StatusOK, v)
		return
	}
	api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("image edit %q not found", id))
}

// PATCH /api/image-edits/{id}: changes the client label of the edit (and nothing else).
func (h *Handler) handleImageEditPatch(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.imageEditOf(id); !ok || !h.host.ProjectVisible(id) {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("image edit %q not found", id))
		return
	}
	client, ok := workspace.DecodeRelabel(w, r)
	if !ok {
		return
	}
	if lerr := h.ws.RelabelManaged(id, client); lerr != nil {
		api.WriteError(w, lerr.Status, lerr.Code, lerr.Msg)
		return
	}
	v, _ := h.imageEditOf(id)
	api.WriteJSON(w, http.StatusOK, v)
}

// DELETE /api/image-edits/{id}
func (h *Handler) handleImageEditDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.imageEditOf(id); !ok || !h.host.ProjectVisible(id) {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("image edit %q not found", id))
		return
	}
	if lerr := h.ws.RemoveManaged(id); lerr != nil {
		api.WriteError(w, lerr.Status, lerr.Code, lerr.Msg)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

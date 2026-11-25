package workspace

// blenderwriter.go: the descriptions of 3D scenes (.scene.json in a project's sources): a model writes or revises one from an
// idea, and a person can read and save one by hand. A description is stored only if it passes the same checks as one written
// by hand. See specs/blender-scene-writer.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
	"veritmo/internal/project/pipeline"
	"veritmo/internal/server/api"
	"veritmo/internal/video/blender"
	"veritmo/internal/video/motion"
	videorender "veritmo/internal/video/render"
)

// The seconds a description can be written for.
const (
	minDescriptionSeconds = 0.5
	maxDescriptionSeconds = 600
)

// descriptionView is a stored description as the endpoints answer it.
type descriptionView struct {
	Name string `json:"name"`
	// Description is the file as written (what a person edits); Resolved is the same with every key in seconds and every
	// colour #rrggbb, for the project's tempo and design.
	Description json.RawMessage `json:"description"`
	Resolved    *blender.Spec   `json:"resolved,omitempty"`
}

// descriptionProject resolves {id} and the project's settings for a description: the checks' environment, the frame's
// size and the fps. It answers the error itself.
func (h *Handler) descriptionProject(w http.ResponseWriter, r *http.Request) (id string, cfg *config.Config, env blender.Env, ok bool) {
	id, ok = h.RequireProject(w, r)
	if !ok {
		return
	}
	cfg, err := h.ProjectConfig(id)
	if err != nil {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("the project's configuration cannot be read: %v", err))
		return id, nil, env, false
	}
	return id, cfg, pipeline.BlenderEnv(cfg, h.ProjectDir(id)), true
}

func descriptionName(w http.ResponseWriter, name string) bool {
	if !config.ValidSceneSpecName(name) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid description name %q: use a plain file name ending in %s", name, config.SceneSpecSuffix))
		return false
	}
	return true
}

func (h *Handler) descriptionPath(id, name string) string {
	return filepath.Join(h.SourcesDir(id), name)
}

// storeDescription writes the description to sources/<name> through a temporary name, so a failure leaves the file that was
// there as it was.
func (h *Handler) storeDescription(id, name string, raw []byte) error {
	dir := h.SourcesDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".description-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), h.descriptionPath(id, name))
}

// indented is a description's JSON as it is stored: indented, with a final line break.
func indented(raw []byte) []byte {
	var b bytes.Buffer
	if err := json.Indent(&b, raw, "", "  "); err != nil {
		return raw
	}
	return append(b.Bytes(), '\n')
}

// POST /api/projects/{id}/blender/descriptions {"name", "idea", "seconds", "revise"?: bool, "provider"?: id}
//
//	-> {name, description, resolved, provider, model, attempts}
//
// Asks the model of the motion_graphics task for the description of a 3D scene (or, with revise, for the stored one changed
// as the idea says) and stores it as sources/<name> only if it passes the checks of a hand-written one.
func (h *Handler) handleDescriptionWrite(w http.ResponseWriter, r *http.Request) {
	id, cfg, env, ok := h.descriptionProject(w, r)
	if !ok {
		return
	}
	var req struct {
		Name     string  `json:"name"`
		Idea     string  `json:"idea"`
		Seconds  float64 `json:"seconds"`
		Revise   bool    `json:"revise"`
		Provider string  `json:"provider"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	req.Idea = strings.TrimSpace(req.Idea)
	switch {
	case !descriptionName(w, req.Name):
		return
	case req.Idea == "":
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "idea is required: say in words what the scene shows")
		return
	case utf8.RuneCountInString(req.Idea) > blender.MaxIdeaRunes:
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("the idea is longer than %d characters", blender.MaxIdeaRunes))
		return
	case !(req.Seconds >= minDescriptionSeconds && req.Seconds <= maxDescriptionSeconds):
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("seconds must be from %g to %d", minDescriptionSeconds, maxDescriptionSeconds))
		return
	}
	var current []byte
	if req.Revise {
		var err error
		if current, err = os.ReadFile(h.descriptionPath(id, req.Name)); err != nil {
			api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("there is no stored description %q to revise: write it first", req.Name))
			return
		}
	}
	ctx, wr, info, ok := h.motionWriterFor(w, r, req.Provider, "the scene")
	if !ok {
		return
	}
	res, err := h.describeScene(ctx, wr, cfg, env, req.Idea, req.Seconds, current)
	if err != nil {
		if errors.Is(err, blender.ErrRejected) {
			api.WriteError(w, http.StatusUnprocessableEntity, api.CodeInvalid, err.Error())
			return
		}
		writeMotionError(w, err)
		return
	}
	if err := h.storeDescription(id, req.Name, res.Raw); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, fmt.Sprintf("cannot save the description: %v", err))
		return
	}
	api.WriteJSON(w, http.StatusOK, struct {
		descriptionView
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Attempts int    `json:"attempts"`
	}{descriptionView{Name: req.Name, Description: bytes.TrimSpace(res.Raw), Resolved: res.Spec}, info.Provider, info.Model, res.Attempts})
}

// describeScene asks the model for a description of a scene seconds long (or, with current, for that one changed as the idea
// says) against the settings of cfg and checks it, up to the attempts of the writer. It is what the handler and the dataset
// assistant share; the model's own tests replace it with Overrides.BlenderWrite.
func (h *Handler) describeScene(ctx context.Context, wr motion.Writer, cfg *config.Config, env blender.Env, idea string, seconds float64, current []byte) (blender.WriteResult, error) {
	width, height, _ := videorender.ParseResolution(cfg.Defaults.Resolution) // 0 when unreadable: the prompt says 16:9
	wreq := blender.WriteRequest{Idea: idea, Seconds: seconds, FPS: cfg.Defaults.FPS, Width: width, Height: height, Env: env,
		Families: pipeline.BlenderFamilies(cfg), Current: current}
	write := h.Overrides.BlenderWrite
	if write == nil {
		write = blender.WriteDescription
	}
	return write(ctx, wr, wreq)
}

// SceneWriter takes the model of the motion_graphics task for a caller that writes 3D descriptions without a project of its
// own (the dataset assistant), with the checks of the handlers of this file: the routing of the provider and the wait for a
// run when the model is local. It answers the error itself and reports whether it can go on.
func (h *Handler) SceneWriter(w http.ResponseWriter, r *http.Request, provider string) (context.Context, motion.Writer, motion.ModelInfo, bool) {
	return h.motionWriterFor(w, r, provider, "the scenes")
}

// WriteSceneError answers the error of a model call that wrote or changed a description: blender.ErrRejected is 422 with the
// last problems, anything else as the handlers of the model of the motion_graphics task answer it.
func (h *Handler) WriteSceneError(w http.ResponseWriter, err error) {
	if errors.Is(err, blender.ErrRejected) {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeInvalid, err.Error())
		return
	}
	writeMotionError(w, err)
}

// BlenderUsable says whether Blender can be used here; the error says how to fix it.
func (h *Handler) BlenderUsable(ctx context.Context) error {
	var err error
	if find := h.Overrides.BlenderInfo; find != nil {
		_, err = find(ctx)
	} else {
		_, err = blender.Cached(ctx)
	}
	return err
}

// DescribeSceneWith writes the description of a scene (or changes current) against the settings of cfg: its frame, its frame
// rate, and the tempo and design of defaults.motion. There are no font files (the scene has no project of its own).
func (h *Handler) DescribeSceneWith(ctx context.Context, wr motion.Writer, cfg *config.Config, idea string, seconds float64, current []byte) (blender.WriteResult, error) {
	return h.DescribeSceneIn(ctx, wr, cfg, "", idea, seconds, current)
}

// sceneEnv is what a scene of a piece of a music motion script is checked against: cfg's tempo and design, and, when sources
// is not empty, the models and pictures of that folder. Fonts are never files: a piece takes the design's.
func sceneEnv(cfg *config.Config, sources string) blender.Env {
	env := pipeline.BlenderEnv(cfg, "")
	if sources != "" {
		env.NoFiles, env.NoFontFiles, env.Sources = false, true, sources
	}
	return env
}

// DescribeSceneIn is DescribeSceneWith for a scene that has the 3D models of the folder sources (the writer is told which, and
// a description that places one is checked against them).
func (h *Handler) DescribeSceneIn(ctx context.Context, wr motion.Writer, cfg *config.Config, sources, idea string, seconds float64, current []byte) (blender.WriteResult, error) {
	return h.describeScene(ctx, wr, cfg, sceneEnv(cfg, sources), idea, seconds, current)
}

// DescriptionError is a description that does not pass the checks of a hand-written one; its message names each fault by its place.
type DescriptionError struct{ Err error }

func (e *DescriptionError) Error() string { return e.Err.Error() }
func (e *DescriptionError) Unwrap() error { return e.Err }

// BlenderUnavailableError says Blender cannot be used here (not installed, too old); its message says how to fix it.
type BlenderUnavailableError struct{ Err error }

func (e *BlenderUnavailableError) Error() string { return e.Err.Error() }
func (e *BlenderUnavailableError) Unwrap() error { return e.Err }

// PreviewDescription draws a frame of a description given as bytes, second t of a scene seconds long, against the settings of cfg
// (frame, frame rate, tempo, design), on the CPU. Nothing is stored. The errors are DescriptionError, BlenderUnavailableError or
// any other failure of the drawing.
func (h *Handler) PreviewDescription(ctx context.Context, cfg *config.Config, desc []byte, t, seconds float64, width int) ([]byte, error) {
	return h.PreviewDescriptionIn(ctx, cfg, "", desc, t, seconds, width)
}

// PreviewDescriptionIn is PreviewDescription for a scene that has the 3D models of the folder sources.
func (h *Handler) PreviewDescriptionIn(ctx context.Context, cfg *config.Config, sources string, desc []byte, t, seconds float64, width int) ([]byte, error) {
	spec, err := blender.Parse(desc, seconds, sceneEnv(cfg, sources))
	if err != nil {
		return nil, &DescriptionError{err}
	}
	var info blender.Info
	if find := h.Overrides.BlenderInfo; find != nil {
		info, err = find(ctx)
	} else {
		info, err = blender.Cached(ctx)
	}
	if err != nil {
		return nil, &BlenderUnavailableError{err}
	}
	fonts, err := pipeline.BlenderFontPaths(cfg, spec)
	if err != nil {
		return nil, err
	}
	aw, ah, _ := videorender.ParseResolution(cfg.Defaults.Resolution)
	opts := blender.PreviewOptions{T: t, Seconds: seconds, FPS: cfg.Defaults.FPS, Width: width, AspectW: aw, AspectH: ah, Fonts: fonts, Sources: sources}
	return h.drawFrame(ctx, info.Path, spec, opts)
}

// DescribeSceneFor writes the description of a 3D scene for a dataset: against the design, tempo and frame of project, or
// with no design, no tempo (keys in seconds) and a 16:9 frame when project is empty. It returns blender.ErrRejected when no
// attempt passes the checks, and ErrNoProject when project is not one.
func (h *Handler) DescribeSceneFor(ctx context.Context, wr motion.Writer, project, idea string, seconds float64) (blender.WriteResult, error) {
	cfg := &config.Config{Defaults: config.Defaults{Resolution: "1280x720", FPS: 30}}
	var env blender.Env
	if project != "" {
		if !h.ProjectExists(project) {
			return blender.WriteResult{}, ErrNoProject
		}
		var err error
		if cfg, err = h.ProjectConfig(project); err != nil {
			return blender.WriteResult{}, fmt.Errorf("the project's configuration cannot be read: %w", err)
		}
		env = pipeline.BlenderEnv(cfg, h.ProjectDir(project))
	} else {
		env = pipeline.BlenderEnv(cfg, "")
	}
	return h.describeScene(ctx, wr, cfg, env, idea, seconds, nil)
}

// GET /api/projects/{id}/blender/descriptions/{name} -> {name, description, resolved}
func (h *Handler) handleDescriptionGet(w http.ResponseWriter, r *http.Request) {
	id, _, env, ok := h.descriptionProject(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if !descriptionName(w, name) {
		return
	}
	raw, err := os.ReadFile(h.descriptionPath(id, name))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("there is no stored description %q", name))
		return
	}
	view := descriptionView{Name: name, Description: bytes.TrimSpace(raw)}
	if !json.Valid(raw) {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeInvalid, fmt.Sprintf("the stored file %q is not valid JSON", name))
		return
	}
	if spec, err := blender.Resolve(raw, env); err == nil { // not checked: a file edited by hand may be wrong, and is shown anyway
		view.Resolved = spec
	}
	api.WriteJSON(w, http.StatusOK, view)
}

// PUT /api/projects/{id}/blender/descriptions/{name} {"description": {...}, "seconds": 5} -> as the GET
//
// Stores a description written by hand after the checks of the writer's (against a scene seconds long); the length is not
// stored: a run checks the description again against the real length of the scene.
func (h *Handler) handleDescriptionPut(w http.ResponseWriter, r *http.Request) {
	id, _, env, ok := h.descriptionProject(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if !descriptionName(w, name) {
		return
	}
	var req struct {
		Description json.RawMessage `json:"description"`
		Seconds     float64         `json:"seconds"`
	}
	limit := int64(blender.MaxSpecBytes + 8<<10)
	api.RaiseBodyLimit(w, r, limit)
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if len(req.Description) == 0 || !(req.Seconds >= minDescriptionSeconds && req.Seconds <= maxDescriptionSeconds) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("description and seconds (from %g to %d) are required", minDescriptionSeconds, maxDescriptionSeconds))
		return
	}
	spec, err := blender.Parse(req.Description, req.Seconds, env)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	stored := indented(req.Description)
	if err := h.storeDescription(id, name, stored); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, fmt.Sprintf("cannot save the description: %v", err))
		return
	}
	api.WriteJSON(w, http.StatusOK, descriptionView{Name: name, Description: bytes.TrimSpace(stored), Resolved: spec})
}

// POST /api/projects/{id}/blender/descriptions/{name}/preview {"t": 1.5, "seconds": 5, "width"?: 480} -> image/png
//
// Draws one frame of the stored description at second t of a scene seconds long: small, with few samples, on the CPU (it
// never takes a card from a run). Nothing is stored.
func (h *Handler) handleDescriptionPreview(w http.ResponseWriter, r *http.Request) {
	id, cfg, env, ok := h.descriptionProject(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if !descriptionName(w, name) {
		return
	}
	var req struct {
		T       float64 `json:"t"`
		Seconds float64 `json:"seconds"`
		Width   int     `json:"width"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	switch {
	case !(req.Seconds >= minDescriptionSeconds && req.Seconds <= maxDescriptionSeconds):
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("seconds must be from %g to %d", minDescriptionSeconds, maxDescriptionSeconds))
		return
	case !(req.T >= 0 && req.T <= req.Seconds):
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("t must be inside the scene (0 to %g s)", req.Seconds))
		return
	case req.Width < 0 || req.Width > blender.MaxPreviewWidth:
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("width must be at most %d", blender.MaxPreviewWidth))
		return
	}
	spec, err := blender.Load(h.descriptionPath(id, name), req.Seconds, env)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("there is no stored description %q", name))
			return
		}
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	info, err := h.blenderInfo(r)
	if err != nil {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, err.Error())
		return
	}
	fonts, err := pipeline.BlenderFontPaths(cfg, spec)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	aw, ah, _ := videorender.ParseResolution(cfg.Defaults.Resolution)
	opts := blender.PreviewOptions{T: req.T, Seconds: req.Seconds, FPS: cfg.Defaults.FPS, Width: req.Width, AspectW: aw, AspectH: ah, Sources: h.SourcesDir(id), Fonts: fonts}
	png, err := h.drawFrame(r.Context(), info.Path, spec, opts)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, fmt.Sprintf("cannot draw the frame: %v", err))
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

// drawFrame draws one frame of a checked description with Blender on the CPU (or with the seam of a test).
func (h *Handler) drawFrame(ctx context.Context, bin string, spec *blender.Spec, opts blender.PreviewOptions) ([]byte, error) {
	preview := h.Overrides.BlenderPreview
	if preview == nil {
		preview = func(ctx context.Context, bin string, spec *blender.Spec, o blender.PreviewOptions) ([]byte, error) {
			return blender.Preview(ctx, runner.NewExecRunner(), bin, spec, o)
		}
	}
	return preview(ctx, bin, spec, opts)
}

// blenderInfo finds Blender, as configured for tests or on this machine; the error says how to install it.
func (h *Handler) blenderInfo(r *http.Request) (blender.Info, error) {
	if find := h.Overrides.BlenderInfo; find != nil {
		return find(r.Context())
	}
	return blender.Cached(r.Context())
}

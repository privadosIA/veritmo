package workspace

// blenderstateless.go: the writer and the preview of 3D scene descriptions without a project. A client that has no project
// yet (the one-call generation) writes, changes and looks at a scene before it generates; nothing is stored. The checks are the
// ones of the descriptions of a project, with no design, no tempo and no font files. See specs/blender-scene-writer.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"veritmo/internal/project/config"
	"veritmo/internal/project/pipeline"
	"veritmo/internal/server/api"
	"veritmo/internal/video/blender"
	"veritmo/internal/video/motion"
)

// AspectRatios are the shapes of a frame a request can ask for.
var AspectRatios = []string{"16:9", "9:16", "1:1", "4:3", "3:4"}

// ParseAspect reads "16:9" and the like; ok is false for a shape that is not one of AspectRatios.
func ParseAspect(s string) (w, h int, ok bool) {
	if _, err := fmt.Sscanf(s, "%d:%d", &w, &h); err != nil || w <= 0 || h <= 0 || !slices.Contains(AspectRatios, s) {
		return 0, 0, false
	}
	return w, h, true
}

// sceneFrameLongSide is the long side of the frame a scene is written for when it has no project.
const sceneFrameLongSide = 1280

// frameOf is a frame of the shape aw:ah whose long side is sceneFrameLongSide, with even sides.
func frameOf(aw, ah int) (width, height int) {
	if aw >= ah {
		return sceneFrameLongSide, sceneFrameLongSide * ah / aw / 2 * 2
	}
	return sceneFrameLongSide * aw / ah / 2 * 2, sceneFrameLongSide
}

// sceneConfig is the settings a scene written with no project is for: a frame of the shape, 30 fps, no design and no tempo.
func sceneConfig(aw, ah int) *config.Config {
	w, h := frameOf(aw, ah)
	return &config.Config{Defaults: config.Defaults{Resolution: fmt.Sprintf("%dx%d", w, h), FPS: 30}}
}

// DescribeSceneAspect writes the description of a scene with no project, for a frame of the given shape ("" is 16:9), or
// changes current as the idea says. It returns blender.ErrRejected when no attempt passes the checks.
func (h *Handler) DescribeSceneAspect(ctx context.Context, wr motion.Writer, aspect, idea string, seconds float64, current []byte) (blender.WriteResult, error) {
	aw, ah, ok := ParseAspect(orDefault(aspect, "16:9"))
	if !ok {
		return blender.WriteResult{}, fmt.Errorf("aspect_ratio %q is not one of %v", aspect, AspectRatios)
	}
	cfg := sceneConfig(aw, ah)
	return h.describeScene(ctx, wr, cfg, pipeline.BlenderEnv(cfg, ""), idea, seconds, current)
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// POST /api/blender/descriptions {"idea", "seconds", "aspect_ratio"?, "description"?, "provider"?}
//
//	-> {description, resolved, provider, model, attempts}
//
// The writer of a project's descriptions with no project: the model of the motion_graphics task writes the description of a
// scene seconds long (or, with description, changes that one as the idea says) and nothing is stored.
func (h *Handler) handleStatelessWrite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Idea        string          `json:"idea"`
		Seconds     float64         `json:"seconds"`
		AspectRatio string          `json:"aspect_ratio"`
		Description json.RawMessage `json:"description"`
		Provider    string          `json:"provider"`
	}
	limit := int64(blender.MaxSpecBytes + 16<<10)
	api.RaiseBodyLimit(w, r, limit)
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	req.Idea = strings.TrimSpace(req.Idea)
	switch {
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
	if _, _, ok := ParseAspect(orDefault(req.AspectRatio, "16:9")); !ok {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("aspect_ratio must be one of %v", AspectRatios))
		return
	}
	var current []byte
	if len(req.Description) > 0 && !bytes.Equal(bytes.TrimSpace(req.Description), []byte("null")) {
		current = req.Description
	}
	ctx, wr, info, ok := h.motionWriterFor(w, r, req.Provider, "the scene")
	if !ok {
		return
	}
	res, err := h.DescribeSceneAspect(ctx, wr, req.AspectRatio, req.Idea, req.Seconds, current)
	if err != nil {
		if errors.Is(err, blender.ErrRejected) {
			api.WriteError(w, http.StatusUnprocessableEntity, api.CodeInvalid, err.Error())
			return
		}
		writeMotionError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, struct {
		Description json.RawMessage `json:"description"`
		Resolved    *blender.Spec   `json:"resolved,omitempty"`
		Provider    string          `json:"provider"`
		Model       string          `json:"model"`
		Attempts    int             `json:"attempts"`
	}{bytes.TrimSpace(res.Raw), res.Spec, info.Provider, info.Model, res.Attempts})
}

// POST /api/blender/preview {"description", "t", "seconds", "aspect_ratio"?, "width"?} -> image/png
//
// A frame of a description given in the body, small and drawn on the CPU like the preview of a project; nothing is stored.
func (h *Handler) handleStatelessPreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Description json.RawMessage `json:"description"`
		T           float64         `json:"t"`
		Seconds     float64         `json:"seconds"`
		AspectRatio string          `json:"aspect_ratio"`
		Width       int             `json:"width"`
	}
	limit := int64(blender.MaxSpecBytes + 8<<10)
	api.RaiseBodyLimit(w, r, limit)
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	aw, ah, aspectOK := ParseAspect(orDefault(req.AspectRatio, "16:9"))
	switch {
	case len(bytes.TrimSpace(req.Description)) == 0:
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "description is required")
		return
	case !(req.Seconds >= minDescriptionSeconds && req.Seconds <= maxDescriptionSeconds):
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("seconds must be from %g to %d", minDescriptionSeconds, maxDescriptionSeconds))
		return
	case !(req.T >= 0 && req.T <= req.Seconds):
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("t must be inside the scene (0 to %g s)", req.Seconds))
		return
	case !aspectOK:
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("aspect_ratio must be one of %v", AspectRatios))
		return
	case req.Width < 0 || req.Width > blender.MaxPreviewWidth:
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("width must be at most %d", blender.MaxPreviewWidth))
		return
	}
	cfg := sceneConfig(aw, ah)
	spec, err := blender.Parse(req.Description, req.Seconds, pipeline.BlenderEnv(cfg, ""))
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	info, err := h.blenderInfo(r)
	if err != nil {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, err.Error())
		return
	}
	opts := blender.PreviewOptions{T: req.T, Seconds: req.Seconds, FPS: cfg.Defaults.FPS, Width: req.Width, AspectW: aw, AspectH: ah}
	png, err := h.drawFrame(r.Context(), info.Path, spec, opts)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, fmt.Sprintf("cannot draw the frame: %v", err))
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

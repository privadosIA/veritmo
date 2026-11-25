package workspace

// motion.go: the stored pages of motion-graphic scenes and overlays (motion/ in a project):
// read them with their preview and details, replace them by hand, or ask the model for a new
// one. See specs/motion-graphics.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"veritmo/internal/assistant/ctxpress"
	"veritmo/internal/assistant/llm"
	"veritmo/internal/platform/settings"
	"veritmo/internal/project/config"
	"veritmo/internal/project/pipeline"
	"veritmo/internal/server/api"
	"veritmo/internal/video/motion"
)

// maxMotionPage bounds a page saved by hand.
const maxMotionPage = 512 << 10

// maxReviseRequest bounds the request in words that changes the motion of a page.
const maxReviseRequest = 1000

// motionCompression is the optional compression service (the Settings' ctxpress connection, read on every call) that
// can show the model a reduced view of a page it only has to retime.
var motionCompression = ctxpress.New()

var motionSceneID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// motionView is a stored page as the editor reads it.
type motionView struct {
	Scene   string `json:"scene"`
	Overlay bool   `json:"overlay"`
	// Exists says whether a page is stored; the other page fields are empty when it is not.
	Exists bool   `json:"exists"`
	HTML   string `json:"html"`
	// Preview is the page prepared for a sandboxed frame: its policy and the virtual clock
	// first. The frame is driven with postMessage({type:"seek", t}).
	Preview string `json:"preview,omitempty"`
	// Duration is the length the preview's clock was set for, in seconds.
	Duration float64 `json:"duration"`
	// Origin is "generated" (as the model wrote it), "edited" or "supplied".
	Origin string `json:"origin,omitempty"`
	// Summary is the one-line description of how the page was made.
	Summary string `json:"summary,omitempty"`
	Model   string `json:"model,omitempty"`
	Browser string `json:"browser,omitempty"`
}

// motionTarget resolves {id} and {scene} and the kind of page asked for (?overlay=1 or the body).
func (h *Handler) motionScene(w http.ResponseWriter, r *http.Request) (id string, cfg *config.Config, scene config.Scene, ok bool) {
	id, ok = h.RequireProject(w, r)
	if !ok {
		return
	}
	sceneID := r.PathValue("scene")
	if !motionSceneID.MatchString(sceneID) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid scene id %q", sceneID))
		return id, nil, scene, false
	}
	raw, err := h.ProjectDefinition(id)
	if err == nil {
		cfg, err = config.LoadBytes(raw)
	}
	if err != nil {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("the project's configuration cannot be read: %v", err))
		return id, nil, scene, false
	}
	for _, sc := range cfg.Scenes {
		if sc.ID == sceneID {
			return id, cfg, sc, true
		}
	}
	api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("scene %q not found", sceneID))
	return id, cfg, scene, false
}

// MotionBrowserFor finds the Chrome or Chromium that renders the pages of motion graphics, as configured or found on the PATH.
func (h *Handler) MotionBrowserFor() (motion.Browser, error) {
	if browser := h.Overrides.MotionBrowser; browser != nil {
		return browser()
	}
	return motion.Find(settings.Browser())
}

// view reads the stored page of a scene.
func (h *Handler) motionViewOf(id string, scene config.Scene, overlay bool, duration float64) motionView {
	v := motionView{Scene: scene.ID, Overlay: overlay, Duration: duration}
	html, meta, hasMeta, found := h.projects.Page(id, scene.ID, overlay)
	if !found {
		return v
	}
	v.Exists, v.HTML = true, html
	v.Preview = motion.PreviewHTML(v.HTML, duration)
	if hasMeta {
		v.Origin, v.Summary, v.Model, v.Browser = meta.Origin(v.HTML), meta.Describe(v.HTML), meta.Model, meta.Browser
	} else {
		v.Origin = motion.OriginSupplied
	}
	return v
}

// pageDuration is the length the preview's clock is set for: the ?duration asked for, else
// the page's estimate from the scene.
func pageDuration(r *http.Request, cfg *config.Config, scene config.Scene, overlay bool) float64 {
	if d, err := strconv.ParseFloat(r.URL.Query().Get("duration"), 64); err == nil && d > 0 && d <= 600 {
		return d
	}
	if req, err := pipeline.MotionRequest(cfg, scene, overlay); err == nil {
		return req.Duration
	}
	return 5
}

// GET /api/projects/{id}/scenes/{scene}/motion?overlay=1&duration=<seconds>
//
//	-> {scene, overlay, exists, html, preview, duration, origin, summary, model, browser}
func (h *Handler) handleMotionGet(w http.ResponseWriter, r *http.Request) {
	id, cfg, scene, ok := h.motionScene(w, r)
	if !ok {
		return
	}
	overlay := r.URL.Query().Get("overlay") == "1"
	api.WriteJSON(w, http.StatusOK, h.motionViewOf(id, scene, overlay, pageDuration(r, cfg, scene, overlay)))
}

// PUT /api/projects/{id}/scenes/{scene}/motion {"html": "...", "overlay"?: bool}
//
// Replaces the stored page by hand. The text is checked for what can be seen without running
// it (a complete document, nothing loaded from outside); a run checks it again in the browser.
func (h *Handler) handleMotionPut(w http.ResponseWriter, r *http.Request) {
	id, cfg, scene, ok := h.motionScene(w, r)
	if !ok {
		return
	}
	var req struct {
		HTML    string `json:"html"`
		Overlay bool   `json:"overlay"`
	}
	api.RaiseBodyLimit(w, r, maxMotionPage+4<<10)
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxMotionPage+4<<10)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if len(req.HTML) > maxMotionPage {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("the page is longer than %d KiB", maxMotionPage>>10))
		return
	}
	if probs := motion.StaticProblems(req.HTML); len(probs) > 0 {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, strings.Join(probs, "; "))
		return
	}
	if (req.Overlay && scene.Overlay == nil) || (!req.Overlay && !scene.IsMotionGraphic()) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("scene %q has no %s to hold a page", scene.ID, map[bool]string{false: "motion graphic", true: "overlay"}[req.Overlay]))
		return
	}
	// a page replaced by hand keeps the details of who wrote the one before (the origin says it was edited)
	var keep *motion.Meta
	if _, meta, hasMeta, _ := h.projects.Page(id, scene.ID, req.Overlay); hasMeta {
		keep = &meta
	}
	if err := h.projects.PutPage(id, scene.ID, req.Overlay, req.HTML, keep); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, fmt.Sprintf("cannot save the page: %v", err))
		return
	}
	api.WriteJSON(w, http.StatusOK, h.motionViewOf(id, scene, req.Overlay, pageDuration(r, cfg, scene, req.Overlay)))
}

// motionWriterFor does what every call that asks the model of the motion_graphics task does first: take the provider
// (the body's, else the routing's), refuse a remote one without consent or over its cap, refuse while a run is queued or
// active when the model loads into this machine's memory (what names the work the model would do), and build the
// writer. It answers the error itself and returns false when the call cannot go on.
func (h *Handler) motionWriterFor(w http.ResponseWriter, r *http.Request, provider, what string) (context.Context, motion.Writer, motion.ModelInfo, bool) {
	ctx := llm.WithProvider(r.Context(), provider)
	if err := llm.Check(settings.TaskMotionGraphics, provider); err != nil && api.WriteRoutingError(w, err) {
		return ctx, nil, motion.ModelInfo{}, false
	}
	if rid, busy := h.rm.anyBusy(); busy && api.NeedsLocalMemory(ctx, settings.TaskMotionGraphics) {
		api.WriteError(w, http.StatusConflict, api.CodeConflict,
			fmt.Sprintf("run %s is queued or active: the model that writes %s needs several GB of memory, so it waits until no run is in progress", rid, what))
		return ctx, nil, motion.ModelInfo{}, false
	}
	newWriter := h.Overrides.MotionWriter
	if newWriter == nil {
		newWriter = motion.NewWriter
	}
	wr, info, err := newWriter(ctx)
	if err != nil {
		writeMotionError(w, err)
		return ctx, nil, motion.ModelInfo{}, false
	}
	return ctx, wr, info, true
}

// POST /api/projects/{id}/scenes/{scene}/motion/generate {"overlay"?: bool, "provider"?: id}
//
// Asks the model for a new page from the scene's saved description and replaces the stored
// one. It refuses while a run is in progress when the model loads into this machine's memory.
func (h *Handler) handleMotionGenerate(w http.ResponseWriter, r *http.Request) {
	id, cfg, scene, ok := h.motionScene(w, r)
	if !ok {
		return
	}
	var req struct {
		Overlay  bool   `json:"overlay"`
		Provider string `json:"provider"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
			return
		}
	}
	mreq, err := pipeline.MotionRequest(cfg, scene, req.Overlay)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	br, err := h.MotionBrowserFor()
	if err != nil {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, err.Error())
		return
	}
	ctx, wr, info, ok := h.motionWriterFor(w, r, req.Provider, "the page")
	if !ok {
		return
	}
	gen := h.Overrides.MotionGenerate
	if gen == nil {
		gen = motion.Generate
	}
	html, err := gen(ctx, wr, br.Path, mreq, motion.Limits{})
	if err != nil {
		writeMotionError(w, err)
		return
	}
	kind := "motion_graphic"
	if req.Overlay {
		kind = "overlay"
	}
	meta := motion.Meta{Kind: kind, Browser: br.Version, Provider: info.Provider, Model: info.Model, GeneratedSHA256: motion.SHA256(html), GeneratedAt: time.Now().UTC()}
	if err := h.projects.PutPage(id, scene.ID, req.Overlay, html, &meta); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, fmt.Sprintf("cannot save the page: %v", err))
		return
	}
	api.WriteJSON(w, http.StatusOK, h.motionViewOf(id, scene, req.Overlay, mreq.Duration))
}

// POST /api/projects/{id}/scenes/{scene}/motion/revise {"instruction": "...", "overlay"?: bool, "provider"?: id}
//
//	-> the page as in the GET, plus {revised: {provider, model}}
//
// Changes the motion of the stored page from a request in words, without asking for a new page: the model answers
// with the content of the page's timeline block only, which is checked and written into the stored page with every
// other byte as it was. The page needs a timeline block (422 otherwise). It refuses while a run is in progress when
// the model loads on this machine, like generate.
func (h *Handler) handleMotionRevise(w http.ResponseWriter, r *http.Request) {
	id, cfg, scene, ok := h.motionScene(w, r)
	if !ok {
		return
	}
	var req struct {
		Instruction string `json:"instruction"`
		Overlay     bool   `json:"overlay"`
		Provider    string `json:"provider"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	req.Instruction = strings.TrimSpace(req.Instruction)
	switch {
	case req.Instruction == "":
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "instruction is required: say in words how the motion should change")
		return
	case len([]rune(req.Instruction)) > maxReviseRequest:
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("the instruction is longer than %d characters", maxReviseRequest))
		return
	}
	mreq, err := pipeline.MotionRequest(cfg, scene, req.Overlay)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	page, meta, hasMeta, found := h.projects.Page(id, scene.ID, req.Overlay)
	if !found {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("scene %q has no stored page to revise: generate or save one first", scene.ID))
		return
	}
	if _, has := motion.ExtractTimeline(page); !has {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeInvalid, motion.ErrNoTimeline.Error()+": its motion cannot be revised on its own, generate a new page instead")
		return
	}
	br, err := h.MotionBrowserFor()
	if err != nil {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, err.Error())
		return
	}
	ctx, wr, info, ok := h.motionWriterFor(w, r, req.Provider, "the timeline")
	if !ok {
		return
	}
	revise := h.Overrides.MotionRevise
	if revise == nil {
		revise = motion.Revise
	}
	view := func(ctx context.Context, page string) string {
		return motionCompression.CompressHTML(ctx, page, "html")
	}
	html, err := revise(ctx, wr, br.Path, page, req.Instruction, mreq, motion.Limits{}, view)
	if err != nil {
		writeMotionError(w, err)
		return
	}
	// The details of the page stay what they were: a model that only retimed it did not write it, so a page the model
	// wrote now reads as edited and one supplied by hand stays supplied.
	if !hasMeta {
		meta = motion.Meta{Kind: map[bool]string{false: "motion_graphic", true: "overlay"}[req.Overlay]}
	}
	meta.Browser = br.Version
	if err := h.projects.PutPage(id, scene.ID, req.Overlay, html, &meta); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, fmt.Sprintf("cannot save the page: %v", err))
		return
	}
	api.WriteJSON(w, http.StatusOK, struct {
		motionView
		Revised struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"revised"`
	}{motionView: h.motionViewOf(id, scene, req.Overlay, mreq.Duration), Revised: struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}{info.Provider, info.Model}})
}

func writeMotionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, motion.ErrTimelineRejected), errors.Is(err, motion.ErrNoTimeline):
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeInvalid, err.Error())
	case errors.Is(err, motion.ErrPageRejected):
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeInvalid, err.Error())
	case errors.Is(err, motion.ErrNoBrowser):
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, err.Error())
	default:
		api.WriteAssistError(w, err)
	}
}

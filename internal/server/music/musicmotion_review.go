package music

// musicmotion_review.go: the review of part drafts and of whole pieces by the vision model of the check task, against the rubric
// the writer reads (spec motion-scene-review).

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"veritmo/internal/assistant/describe"
	"veritmo/internal/assistant/llm"
	"veritmo/internal/music/musicmotion"
	"veritmo/internal/platform/settings"
	"veritmo/internal/server/api"
)

// ReviewField is the `review` of a draft request: true, false or {min, attempts}.
type ReviewField struct {
	On  bool
	Ask musicmotion.ReviewAsk
}

// UnmarshalJSON reads true, false or an object.
func (f *ReviewField) UnmarshalJSON(b []byte) error {
	var on bool
	if json.Unmarshal(b, &on) == nil {
		f.On, f.Ask = on, musicmotion.ReviewAsk{}
		return nil
	}
	var ask musicmotion.ReviewAsk
	if err := json.Unmarshal(b, &ask); err != nil {
		return fmt.Errorf("review must be true, false or {min, attempts}")
	}
	f.On, f.Ask = true, ask
	return nil
}

// MarshalJSON writes the field as it was read.
func (f ReviewField) MarshalJSON() ([]byte, error) {
	if f.Ask == (musicmotion.ReviewAsk{}) {
		return json.Marshal(f.On)
	}
	return json.Marshal(f.Ask)
}

// reviewAskOf is what a request asks of the review: its own `review` wins, and without one the setting
// music_motion.review_drafts reviews every draft (without a minimum). nil means no review.
func reviewAskOf(f *ReviewField) *musicmotion.ReviewAsk {
	switch {
	case f != nil && f.On:
		ask := f.Ask
		return &ask
	case f != nil:
		return nil // review: false
	case settings.ReviewDrafts():
		return &musicmotion.ReviewAsk{}
	}
	return nil
}

// visionLook is the Vision of the engine: a chat with images on the provider the check task is routed to.
type visionLook struct {
	provider llm.Provider
	model    string
	unload   func(context.Context)
}

// Unload asks the provider to drop the model, when it is one that holds models in memory.
func (v visionLook) Unload(ctx context.Context) {
	if v.unload != nil {
		v.unload(ctx)
	}
}

func (v visionLook) Look(ctx context.Context, system, user string, images [][]byte) (string, string, error) {
	res, err := v.provider.Chat(ctx, llm.Request{Model: v.model, System: system, User: user, Images: images, JSON: true, MaxTokens: 700, Temperature: 0})
	if err != nil {
		return "", v.model, err
	}
	return res.Text, v.model, nil
}

// reviewVision is the vision model of the check task and what drops it from memory afterwards. A routing problem (consent, a provider
// that is gone) is an error to answer, never a silent fallback.
func (h *Handler) reviewVision(ctx context.Context) (musicmotion.Vision, func(), error) {
	if h.reviewVisionSeam != nil {
		v, err := h.reviewVisionSeam(ctx)
		return v, func() {}, err
	}
	res, err := llm.For(settings.TaskQuality, "")
	if err != nil {
		return nil, nil, err
	}
	model := res.Model
	unload := func() {}
	if res.Builtin || res.Type == settings.TypeOllama {
		if model == "" {
			def, _, _, lerr := describe.FromEnv().ListModels(ctx)
			if lerr != nil {
				return nil, nil, lerr
			}
			if def == "" {
				return nil, nil, fmt.Errorf("no vision model is installed in Ollama (ollama pull %s)", describe.DefaultModel)
			}
			model = def
		}
		if o, ok := res.Provider.(llm.Unloader); ok {
			unload = func() { o.Unload(context.WithoutCancel(ctx), model) } // a vision model is big: the next work needs the memory
		}
	}
	return visionLook{provider: res.Provider, model: model, unload: func(context.Context) { unload() }}, unload, nil
}

// attachReview makes the engine review its drafts. It answers the error itself (a routing problem, memory) and says whether it
// could be done.
func (h *Handler) attachReview(w http.ResponseWriter, ctx context.Context, e *musicmotion.DraftEngine, ask *musicmotion.ReviewAsk) bool {
	if ask == nil {
		return true
	}
	if err := ask.Validate(); err != nil {
		api.Refuse(w, "%v", err)
		return false
	}
	if err := llm.Check(settings.TaskQuality, ""); err != nil && api.WriteRoutingError(w, err) {
		return false
	}
	if id, busy := h.ws.AnyBusy(); busy && api.NeedsLocalMemory(ctx, settings.TaskQuality) {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("the review of this draft cannot start: run %s is queued or active and the vision model that reviews needs several GB of memory, so it waits until no run is in progress", id))
		return false
	}
	v, _, err := h.reviewVision(ctx)
	if err != nil {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, "the review of this draft cannot start: there is no vision model to review the drafts: "+err.Error())
		return false
	}
	e.Vision = v
	return true
}

// --- the review of a whole piece -----------------------------------------------------------------

// The states of a piece review.
const (
	PieceReviewRunning = "running"
	PieceReviewDone    = "done"
	PieceReviewFailed  = "failed"
)

// PieceReview is the review of a whole piece as its view shows it.
type PieceReview struct {
	State      string                    `json:"state"`
	StartedAt  time.Time                 `json:"started_at"`
	FinishedAt *time.Time                `json:"finished_at,omitempty"`
	Model      string                    `json:"model,omitempty"`
	Scenes     []musicmotion.SceneReview `json:"scenes"`
	// Skipped are the script pieces the review left out because Blender draws them (a 3D scene is not a page to grade).
	Skipped []string             `json:"skipped,omitempty"`
	Overall *musicmotion.Overall `json:"overall,omitempty"`
	Error   string               `json:"error,omitempty"`
}

func (h *Handler) saveReview(id string, r PieceReview) {
	raw, _ := json.MarshalIndent(r, "", "  ")
	_ = h.place(id).WriteDoc(musicmotion.DocReview, raw)
}

// pieceReviewOf reads the last review of a piece; one that was running when the server stopped reads as failed.
func (h *Handler) pieceReviewOf(id string) *PieceReview {
	raw, ok := h.place(id).ReadDoc(musicmotion.DocReview)
	if !ok {
		return nil
	}
	var r PieceReview
	if json.Unmarshal(raw, &r) != nil {
		return nil
	}
	if r.State == PieceReviewRunning && !h.proposals.running(id) {
		now := time.Now().UTC()
		r.State, r.FinishedAt, r.Error = PieceReviewFailed, &now, "the review was interrupted by a restart of the server"
	}
	return &r
}

// POST /api/music-motion/pieces/{id}/review: three frames of every script piece and a contact sheet of the whole, graded by the vision
// model in the background; read it on the piece (`review`).
func (h *Handler) handlePieceReview(w http.ResponseWriter, r *http.Request) {
	info, ok := h.pieceFromPath(w, r)
	if !ok || h.refuseIfRendering(w, info) {
		return
	}
	script, has := musicmotion.LoadScriptAt(h.place(info.ID))
	m, mok := h.pieceMap(info.ID)
	st, dok := musicmotion.LoadDesignAt(h.place(info.ID))
	if !has || !mok || !dok || len(script.Pieces) == 0 {
		api.Refuse(w, "the piece has no script yet: propose or set one first")
		return
	}
	var skipped []string
	for _, p := range script.Pieces {
		if p.Blender != nil {
			skipped = append(skipped, p.ID)
		}
	}
	if len(skipped) == len(script.Pieces) {
		api.Refuse(w, "every piece of the script is a 3D scene (blender): there is no page to review")
		return
	}
	if err := llm.Check(settings.TaskQuality, ""); err != nil && api.WriteRoutingError(w, err) {
		return
	}
	if id, busy := h.ws.AnyBusy(); busy && api.NeedsLocalMemory(r.Context(), settings.TaskQuality) {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("run %s is queued or active: the vision model needs several GB of memory, so the review waits until no run is in progress", id))
		return
	}
	b, err := h.ws.MotionBrowserFor()
	if err != nil {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, "there is no Chrome or Chromium to draw the frames: "+err.Error())
		return
	}
	vision, unload, err := h.reviewVision(r.Context())
	if err != nil {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, "there is no vision model to review the piece: "+err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), draftTimeout)
	if !h.proposals.start(info.ID, cancel) {
		cancel()
		unload()
		api.WriteError(w, http.StatusConflict, api.CodeConflict, "a proposal, a batch of scenes or a review of this piece is still running: wait for it")
		return
	}
	rv := PieceReview{State: PieceReviewRunning, StartedAt: time.Now().UTC(), Scenes: []musicmotion.SceneReview{}, Skipped: skipped}
	h.saveReview(info.ID, rv)
	spec := musicmotion.ProjectSpec{Map: m, Design: st.Design, Script: script, Cat: h.mmCatalog(), Lib: h.mmLibrary(), FPS: info.FPS, Width: info.Width, Height: info.Height, World: info.World}
	h.drafts.wg.Add(1)
	go func() {
		defer h.drafts.wg.Done()
		defer h.proposals.end(info.ID)
		defer unload()
		h.runPieceReview(ctx, vision, b.Path, info.ID, spec, rv)
	}()
	api.WriteJSON(w, http.StatusAccepted, h.pieceReviewOf(info.ID))
}

func (h *Handler) runPieceReview(ctx context.Context, vision musicmotion.Vision, browser, id string, spec musicmotion.ProjectSpec, rv PieceReview) {
	finish := func(err error) {
		now := time.Now().UTC()
		rv.FinishedAt = &now
		if err != nil {
			rv.State, rv.Error = PieceReviewFailed, err.Error()
		} else {
			rv.State = PieceReviewDone
		}
		h.saveReview(id, rv)
	}
	defer func() {
		if rec := recover(); rec != nil {
			finish(fmt.Errorf("the review broke: %v", rec))
		}
	}()
	pages, err := musicmotion.BuildPages(spec, nil)
	if err != nil {
		finish(err)
		return
	}
	var sheetFrames [][]byte
	var ids []string
	for _, pg := range pages {
		if ctx.Err() != nil {
			finish(fmt.Errorf("the review was stopped"))
			return
		}
		var piece musicmotion.Piece
		for _, p := range spec.Script.Pieces {
			if p.ID == pg.ID {
				piece = p
			}
		}
		sc, err := musicmotion.SceneOfPiece(piece, spec)
		if err != nil {
			finish(err)
			return
		}
		frames, err := musicmotion.PieceFrames(ctx, browser, pg, spec.Width, spec.Height, musicmotion.ReviewTimes(sc))
		if err != nil {
			finish(fmt.Errorf("script piece %s: %w", pg.ID, err))
			return
		}
		rev, err := musicmotion.ReviewFrames(ctx, vision, frames, sc.Lines, "", spec.World)
		if err != nil {
			finish(fmt.Errorf("the review was stopped"))
			return
		}
		if rev.Model != "" {
			rv.Model = rev.Model
		}
		rv.Scenes = append(rv.Scenes, musicmotion.SceneReview{ScriptPiece: pg.ID, Score: rev.Score, Verdict: rev.Verdict, Notes: rev.Notes, Error: rev.Error})
		sheetFrames, ids = append(sheetFrames, frames[len(frames)/2]), append(ids, pg.ID)
		h.saveReview(id, rv)
	}
	cols := int(math.Min(4, math.Ceil(math.Sqrt(float64(len(sheetFrames))))))
	sheet, err := musicmotion.ContactSheet(sheetFrames, max(cols, 1), 320, 180)
	if err != nil {
		finish(err)
		return
	}
	o, err := musicmotion.ReviewOverall(ctx, vision, sheet, ids)
	if err != nil {
		finish(fmt.Errorf("the review was stopped"))
		return
	}
	// A fallback script's pieces that still use a part the fixed rule chose come before the grades' own notes (a scene older than the batch is still named before them): they are placeholders, not scenes.
	if info, ok := h.readPiece(id); ok && info.ScriptSource == "fallback" {
		if fb := musicmotion.FallbackPieces(spec.Script, spec.Cat); len(fb) > 0 {
			o.Notes = append([]string{"pieces that use a part the fixed rule chose (no scene of their own): " + strings.Join(fb, ", ")}, o.Notes...)
		}
	}
	// The scenes that kept an older version (no answer passed the checks) are named first: the grades are of what is on screen.
	if kept := keptScenes(h.sceneBatchOf(id)); len(kept) > 0 {
		var names []string
		for _, k := range kept {
			names = append(names, k.ScriptPiece)
		}
		o.Notes = append([]string{"scenes older than the last batch (it could not write them again): " + strings.Join(names, ", ")}, o.Notes...)
	}
	rv.Overall = &o
	finish(nil)
}

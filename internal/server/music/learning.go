package music

// learning.go is the API of learning from a reference (spec motion-learning): one call draws a style and proposes a few
// parts from a reference, keeps only what is different from what exists and leaves the rest listed as discarded. The
// work is in internal/music/motionref (LearnEngine); this file serves it and starts the drafts of the parts.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
	"veritmo/internal/platform/store"

	"veritmo/internal/music/motionref"
	"veritmo/internal/music/musicmotion"
	"veritmo/internal/server/api"
)

// learnTimeout bounds one learning (the drafts it starts run on their own and are not counted).
const learnTimeout = 30 * time.Minute

// learningState is the learnings of the server: the store (made when first used, where the settings are then) and the
// jobs that run.
type learningState struct {
	once  sync.Once
	store *motionref.LearningStore
	mu    sync.Mutex // one learning per reference: the check for a running one and the creation are one step
	wg    sync.WaitGroup
}

func (h *Handler) learningStore() *motionref.LearningStore {
	h.learnings.once.Do(func() {
		h.learnings.store = motionref.NewLearningStoreDB(h.ws.Store())
		h.learnings.store.MarkInterrupted()
	})
	return h.learnings.store
}

// waitLearnings waits for the learnings that run (tests).
func (h *Handler) waitLearnings() { h.learnings.wg.Wait() }

// LearnedView is a proposal a learning kept, with what became of it now.
type LearnedView struct {
	// ID is the id of the style or of the draft; State is `ready` for a style, the state of the draft for a part, and
	// `gone` when the style or the draft no longer exists (deleted, or a draft approved into the catalog).
	ID        string               `json:"id"`
	Name      string               `json:"name"`
	Novel     bool                 `json:"novel"`
	SimilarTo *musicmotion.Similar `json:"similar_to,omitempty"`
	State     string               `json:"state"`
	// Reason is why a draft is `failed`.
	Reason string `json:"reason,omitempty"`
}

// LearningView is a learning as it is read.
type LearningView struct {
	ID          string                `json:"id"`
	ReferenceID string                `json:"reference_id"`
	State       string                `json:"state"`
	Reason      string                `json:"reason,omitempty"`
	Strictness  string                `json:"strictness"`
	CreatedAt   time.Time             `json:"created_at"`
	FinishedAt  *time.Time            `json:"finished_at,omitempty"`
	Styles      []LearnedView         `json:"styles"`
	PartDrafts  []LearnedView         `json:"part_drafts"`
	Discarded   []motionref.Discarded `json:"discarded"`
	Skipped     []motionref.Skipped   `json:"skipped"`
}

// LearnRequest is the optional body of the call.
type LearnRequest struct {
	// Strictness is how far from what exists a proposal has to be: low, normal (the default) or high.
	Strictness string `json:"strictness"`
}

func (h *Handler) learningView(l motionref.Learning) LearningView {
	v := LearningView{ID: l.ID, ReferenceID: l.ReferenceID, State: l.State, Reason: l.Reason, Strictness: l.Strictness, CreatedAt: l.CreatedAt,
		FinishedAt: l.FinishedAt, Styles: []LearnedView{}, PartDrafts: []LearnedView{}, Discarded: l.Discarded, Skipped: l.Skipped}
	for _, s := range l.Styles {
		item := LearnedView{ID: s.ID, Name: s.Name, Novel: s.Novel, SimilarTo: s.SimilarTo, State: "ready"}
		if _, err := h.gen.PresetStore().Get(s.ID); err != nil {
			item.State = "gone"
		}
		v.Styles = append(v.Styles, item)
	}
	for _, p := range l.PartDrafts {
		item := LearnedView{ID: p.ID, Name: p.Name, Novel: p.Novel, SimilarTo: p.SimilarTo, State: "gone"}
		if d, err := h.draftStore().Get(p.ID); err == nil {
			item.State, item.Reason = d.State, d.Error
		}
		v.PartDrafts = append(v.PartDrafts, item)
	}
	return v
}

// learnEngine builds the engine of a learning. What the drafts need (a browser, an assistant, a vision model, memory)
// decides whether parts can be proposed; without it the style is still drawn and the reason goes in `skipped`.
func (h *Handler) learnEngine(ctx context.Context, ref motionref.Reference) *motionref.LearnEngine {
	e := &motionref.LearnEngine{Store: h.learningStore(), Presets: h.gen.PresetStore(), Drafts: h.draftStore(), Cat: h.mmCatalog()}
	withPicture := ref.Analysis != nil && len(ref.Analysis.Frames) > 0 && h.draftVisionError(ctx) == ""
	de, _, msg := h.draftEngine(ctx, withPicture)
	if de == nil {
		e.PartsUnavailable = msg
		return e
	}
	e.Writer, e.WithPictures = de.Writer, withPicture
	e.StartDraft = func(ctx context.Context, learningID, request, frame string) (string, error) {
		var picture []byte
		source := ""
		if frame != "" {
			b, err := os.ReadFile(filepath.Join(h.assistant.RefStore().FramesDir(ref.ID), frame))
			if err != nil {
				return "", fmt.Errorf("the frame %q is not there: %w", frame, err)
			}
			picture, source = b, "reference:"+ref.ID+"/"+frame
		}
		d, err := de.Store.Create(request, source, picture)
		if err != nil {
			return "", err
		}
		d.Origin, d.LearningID = musicmotion.OriginLearned, learningID
		d.ReferenceID, d.VideoTitle, d.Video = ref.ID, ref.Title, ref.Source
		if err := de.Store.Save(d); err != nil {
			return "", err
		}
		h.startDraft(d.ID, func(ctx context.Context) { de.Run(ctx, d.ID) })
		return d.ID, nil
	}
	return e
}

// POST /api/music-motion/references/{id}/learn  ({strictness?})
func (h *Handler) handleLearn(w http.ResponseWriter, r *http.Request) {
	ref, err := h.assistant.RefStore().Get(r.PathValue("id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("reference %q not found", r.PathValue("id")))
		return
	}
	if ref.State != motionref.StateReady || ref.Analysis == nil {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, "the reference has not been analysed (its state is "+ref.State+")")
		return
	}
	var req LearnRequest
	if body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<12)); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("cannot read the body: %v", err))
		return
	} else if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
			return
		}
	}
	st, err := musicmotion.ParseStrictness(req.Strictness)
	if err != nil {
		api.Refuse(w, "%v", err)
		return
	}
	h.learnings.mu.Lock()
	defer h.learnings.mu.Unlock()
	if l, running := h.learningStore().Running(ref.ID); running {
		api.WriteJSON(w, http.StatusOK, h.learningView(l))
		return
	}
	e := h.learnEngine(r.Context(), ref)
	l, err := e.Store.Create(ref.ID, st)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), learnTimeout)
	h.learnings.wg.Add(1)
	go func() {
		defer h.learnings.wg.Done()
		defer cancel()
		e.Run(ctx, l.ID, ref)
	}()
	api.WriteJSON(w, http.StatusAccepted, h.learningView(l))
}

// GET /api/music-motion/learnings[?reference=&state=&sort=&limit=&offset=]
//
// reference (or reference_id, as it was called) keeps the learnings of that reference and state the ones in that state. With any of them, or with
// sort, limit or offset, it is a filtered or paged list (X-Total-Count and Link say where the others are); without them it answers every learning.
func (h *Handler) handleLearningList(w http.ResponseWriter, r *http.Request) {
	params, err := store.ParseList(r, motionref.LearningsListSpec)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	q := r.URL.Query()
	reference := q.Get("reference")
	if reference == "" {
		reference = q.Get("reference_id")
	}
	state := q.Get("state")
	if state != "" && state != motionref.LearningRunning && state != motionref.LearningReady && state != motionref.LearningFailed {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("state must be %s, %s or %s", motionref.LearningRunning, motionref.LearningReady, motionref.LearningFailed))
		return
	}
	list, total, err := h.learningStore().Page(reference, state, params)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	out := []LearningView{}
	for _, l := range list {
		out = append(out, h.learningView(l))
	}
	store.WriteListHeaders(w, r, total, params)
	api.WriteJSON(w, http.StatusOK, map[string]any{"learnings": out})
}

// DELETE /api/music-motion/learnings/{id}
//
// Deletes the record of a learning, never the style or the part drafts it proposed. 409 while it is running.
func (h *Handler) handleLearningDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	h.learnings.mu.Lock() // the check and the delete are one step with the start of a learning
	defer h.learnings.mu.Unlock()
	l, err := h.learningStore().Get(id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("learning %q not found", id))
		return
	}
	if l.State == motionref.LearningRunning {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, "the learning is running: wait for it to finish")
		return
	}
	if err := h.learningStore().Delete(id); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/music-motion/learnings/{id}
func (h *Handler) handleLearningGet(w http.ResponseWriter, r *http.Request) {
	l, err := h.learningStore().Get(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, motionref.ErrLearningNotFound) {
			api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("learning %q not found", r.PathValue("id")))
			return
		}
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, h.learningView(l))
}

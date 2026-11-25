package music

// musicmotion_scenes.go: the scenes written for the verses of a piece (spec motion-scene-parts): a draft asked for with
// scene {piece, script_piece, brief?}, a batch that writes one for every script piece in the background, and the approval of
// every ready one at once.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"veritmo/internal/platform/store"

	"veritmo/internal/assistant/llm"
	"veritmo/internal/music/musicmotion"
	"veritmo/internal/platform/settings"
	"veritmo/internal/server/api"
	"veritmo/internal/video/motion"
)

// The states of a batch.
const (
	SceneBatchWriting   = "writing"
	SceneBatchDone      = "done"
	SceneBatchCancelled = "cancelled"
	SceneBatchFailed    = "failed"
)

// SceneDraftState is one draft of a batch as the piece shows it.
type SceneDraftState struct {
	ScriptPiece string `json:"script_piece"`
	DraftID     string `json:"draft_id,omitempty"`
	// State is the draft's own state (writing, ready, failed), or "approved" when the draft is gone because it was approved,
	// or "pending" before it is started.
	State string `json:"state"`
	Error string `json:"error,omitempty"`
	// KeptPrevious says no answer passed the checks, so the script piece keeps the scene it had; Problems are the last ones the checks found.
	KeptPrevious bool     `json:"kept_previous,omitempty"`
	Problems     []string `json:"problems,omitempty"`
	// Repairs is what the program repaired in the draft's last answer before the checks (colours, fonts, Math.random()).
	Repairs []musicmotion.Repair `json:"repairs,omitempty"`
}

// SceneBatch is the state of the batch of scenes of a piece.
type SceneBatch struct {
	State  string            `json:"state"`
	Done   int               `json:"done"`
	Total  int               `json:"total"`
	Drafts []SceneDraftState `json:"drafts"`
	// Earlier are the script pieces of earlier batches that kept their scene and were not part of this one, so the piece still knows which
	// of its scenes are older than the last batch.
	Earlier  []SceneDraftState `json:"earlier,omitempty"`
	Model    string            `json:"model,omitempty"`
	Provider string            `json:"provider,omitempty"`
	// Review is what every draft of the batch was asked to be reviewed with (absent: none).
	Review     *musicmotion.ReviewAsk `json:"review,omitempty"`
	BriefMode  string                 `json:"brief_mode"`
	StartedAt  time.Time              `json:"started_at"`
	FinishedAt *time.Time             `json:"finished_at,omitempty"`
	Error      string                 `json:"error,omitempty"`
	// Fingerprints of the script pieces when the batch started, to refuse an approval after the script changed.
	Fingerprints map[string]string `json:"fingerprints,omitempty"`
}

type sceneJobs struct {
	mu   sync.Mutex
	stop map[string]bool // batches asked to stop after the draft being written
}

func (j *sceneJobs) request(id string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.stop == nil {
		j.stop = map[string]bool{}
	}
	j.stop[id] = true
}

func (j *sceneJobs) wanted(id string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.stop[id]
}

func (j *sceneJobs) clear(id string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	delete(j.stop, id)
}

func (h *Handler) saveSceneBatch(id string, b SceneBatch) {
	raw, _ := json.MarshalIndent(b, "", "  ")
	_ = h.place(id).WriteDoc(musicmotion.DocScenes, raw)
}

// sceneBatchOf reads the last batch of a piece with the live state of each draft. One that was running when the server stopped
// reads as failed.
func (h *Handler) sceneBatchOf(id string) *SceneBatch {
	raw, ok := h.place(id).ReadDoc(musicmotion.DocScenes)
	if !ok {
		return nil
	}
	var b SceneBatch
	if json.Unmarshal(raw, &b) != nil {
		return nil
	}
	if b.State == SceneBatchWriting && !h.proposals.running(id) {
		now := time.Now().UTC()
		b.State, b.FinishedAt, b.Error = SceneBatchFailed, &now, "the batch was interrupted by a restart of the server"
	}
	for i := range b.Drafts {
		d := &b.Drafts[i]
		if d.DraftID == "" {
			continue
		}
		if got, err := h.draftStore().Get(d.DraftID); err == nil {
			d.State, d.Error, d.Repairs = got.State, got.Error, got.Repairs
			if got.State == musicmotion.DraftFailed {
				d.KeptPrevious, d.Problems = true, got.Problems
			}
		} else {
			d.State, d.Error = "approved", ""
		}
	}
	return &b
}

// keptScenes are the script pieces whose last attempt failed, so the piece keeps the scene it had: those of the last batch that failed and the
// ones earlier batches left and this one did not write again.
func keptScenes(b *SceneBatch) []SceneDraftState {
	if b == nil {
		return nil
	}
	var out []SceneDraftState
	for _, d := range b.Drafts {
		if d.KeptPrevious {
			out = append(out, d)
		}
	}
	return append(out, b.Earlier...)
}

// sceneInput builds what a scene is written from out of the saved piece (the seam of the draft engine).
func (h *Handler) sceneInput(ref musicmotion.SceneRef, brief string) (musicmotion.SceneDraftInput, error) {
	info, ok := h.readPiece(ref.Piece)
	if !ok {
		return musicmotion.SceneDraftInput{}, fmt.Errorf("the piece %q does not exist", ref.Piece)
	}
	m, ok := h.pieceMap(info.ID)
	st, hasDesign := musicmotion.LoadDesignAt(h.place(info.ID))
	script, hasScript := musicmotion.LoadScriptAt(h.place(info.ID))
	if !ok || !hasDesign || !hasScript {
		return musicmotion.SceneDraftInput{}, fmt.Errorf("the piece %q has no script yet: propose or set one first", ref.Piece)
	}
	spec := musicmotion.ProjectSpec{Map: m, Design: st.Design, Script: script, Cat: h.mmCatalog(), Lib: h.mmLibrary(), Width: info.Width, Height: info.Height, Title: info.Title, World: info.World, Device: info.Device, Hook: info.Hook, Reference: h.referenceShots(info)}
	return musicmotion.BuildSceneInput(spec, ref, brief)
}

// refuseScene checks the scene a client asks for (before anything is created): 404 for an unknown piece or script piece.
func (h *Handler) refuseScene(w http.ResponseWriter, sc *SceneBody) bool {
	if n := len([]rune(sc.Brief)); n > musicmotion.MaxSceneBrief {
		api.Refuse(w, "scene.brief has %d characters: at most %d", n, musicmotion.MaxSceneBrief)
		return true
	}
	if _, ok := h.readPiece(sc.Piece); !ok {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("piece %q not found", sc.Piece))
		return true
	}
	if _, err := h.sceneInput(musicmotion.SceneRef{Piece: sc.Piece, ScriptPiece: sc.ScriptPiece}, ""); err != nil {
		if errors.Is(err, musicmotion.ErrScenePieceNotFound) {
			api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("script piece %q not found in the script of piece %s", sc.ScriptPiece, sc.Piece))
		} else {
			api.Refuse(w, "%v", err)
		}
		return true
	}
	return false
}

// handleSceneDraftCreate is POST /part-drafts with a scene: a draft written from the piece's own context.
func (h *Handler) handleSceneDraftCreate(w http.ResponseWriter, r *http.Request, req DraftRequest) {
	if req.Manifest != "" || req.Fragment != "" || req.ReferenceID != "" || req.Frame != "" || req.Request != "" && len([]rune(req.Request)) > musicmotion.MaxDraftRequest {
		api.Refuse(w, "a scene is asked for with scene only (and model, provider): no manifest, fragment, reference or frame")
		return
	}
	sc := req.Scene
	sc.Brief = strings.TrimSpace(sc.Brief)
	if sc.Brief == "" {
		sc.Brief = req.Request // a request is taken as the idea when there is no brief
	}
	if h.refuseScene(w, sc) {
		return
	}
	req.Model, req.Provider = strings.TrimSpace(req.Model), strings.TrimSpace(req.Provider)
	if req.Provider != "" {
		if err := llm.Check(settings.TaskMotionGraphics, req.Provider); err != nil && api.WriteRoutingError(w, err) {
			return
		}
	}
	if err := h.checkModel(r.Context(), req.Provider, req.Model); err != nil {
		api.Refuse(w, "%v", err)
		return
	}
	e, status, msg := h.draftEngine(motion.WithModel(llm.WithProvider(r.Context(), req.Provider), req.Model), false)
	if e == nil {
		api.WriteError(w, status, draftErrorCode(status), msg)
		return
	}
	ask := reviewAskOf(req.Review)
	if !h.attachReview(w, r.Context(), e, ask) {
		return
	}
	d, err := e.Store.Create("(scene of "+sc.ScriptPiece+")", "", nil)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	d.Scene, d.Brief, d.Model, d.Provider, d.ReviewAsk = &musicmotion.SceneRef{Piece: sc.Piece, ScriptPiece: sc.ScriptPiece}, sc.Brief, req.Model, req.Provider, ask
	if err := e.Store.Save(d); err != nil {
		_ = e.Store.Delete(d.ID)
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	h.startDraft(d.ID, func(ctx context.Context) { e.Run(ctx, d.ID); e.ReleaseVision(ctx) })
	api.WriteJSON(w, http.StatusAccepted, d)
}

// deleteScenesOf removes what belongs to a piece that is deleted: its scene parts and its scene drafts.
func (h *Handler) deleteScenesOf(piece string) {
	st := h.draftStore()
	for _, d := range st.List() {
		if d.Scene != nil && d.Scene.Piece == piece {
			h.drafts.mu.Lock()
			cancel := h.drafts.jobs[d.ID]
			h.drafts.mu.Unlock()
			if cancel != nil {
				cancel()
			}
			_ = st.Delete(d.ID)
		}
	}
}

// fingerprint is what says a script piece is still the one a batch was written for: its place, its lines and its section.
func fingerprint(p musicmotion.Piece) string {
	raw, _ := json.Marshal(struct {
		Start, End float64
		Section    string
		Lines      []musicmotion.ScriptLine
		Hit        *musicmotion.ScriptHit
	}{p.Start, p.End, p.Section, p.Lines, p.Hit})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

// ScenesRequest is the body of POST /api/music-motion/pieces/{id}/scenes.
type ScenesRequest struct {
	// ScriptPieces are the script pieces to write scenes for (empty: all of them).
	ScriptPieces []string `json:"script_pieces,omitempty"`
	// Only is "failed" to write again just the script pieces that kept their earlier scene because no answer passed the checks.
	Only string `json:"only,omitempty"`
	// BriefMode is "metaphor" (the writer chooses a metaphor for what each verse means; the default) or "literal" (it draws
	// what the words say).
	BriefMode string `json:"brief_mode,omitempty"`
	Model     string `json:"model,omitempty"`
	Provider  string `json:"provider,omitempty"`
	// Review is passed to every draft of the batch (see DraftRequest.Review).
	Review *ReviewField `json:"review,omitempty"`
}

const literalBrief = "Draw what the words of the verse literally say (the scene they describe), not a metaphor for it."

// POST /api/music-motion/pieces/{id}/scenes
func (h *Handler) handleScenesStart(w http.ResponseWriter, r *http.Request) {
	info, ok := h.pieceFromPath(w, r)
	if !ok || h.refuseIfRendering(w, info) {
		return
	}
	var req ScenesRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if req.BriefMode == "" {
		req.BriefMode = "metaphor"
	}
	if req.BriefMode != "metaphor" && req.BriefMode != "literal" {
		api.Refuse(w, `brief_mode must be "metaphor" or "literal", got %q`, req.BriefMode)
		return
	}
	script, has := musicmotion.LoadScriptAt(h.place(info.ID))
	if !has || len(script.Pieces) == 0 {
		api.Refuse(w, "the piece has no script yet: propose or set one first")
		return
	}
	if req.Only != "" && req.Only != "failed" {
		api.Refuse(w, `only must be "failed" or empty, got %q`, req.Only)
		return
	}
	previous := h.sceneBatchOf(info.ID)
	kept := keptScenes(previous)
	if req.Only == "failed" {
		if len(kept) == 0 {
			api.Refuse(w, "no script piece kept an earlier scene: there is nothing to write again")
			return
		}
		only := []string{}
		for _, k := range kept {
			if len(req.ScriptPieces) == 0 || slices.Contains(req.ScriptPieces, k.ScriptPiece) {
				only = append(only, k.ScriptPiece)
			}
		}
		if len(only) == 0 {
			api.Refuse(w, "none of the script pieces asked for kept an earlier scene")
			return
		}
		req.ScriptPieces = only
	}
	chosen := map[string]bool{}
	for _, id := range req.ScriptPieces {
		found := false
		for _, p := range script.Pieces {
			found = found || p.ID == id
			if p.ID == id && p.Blender != nil {
				api.Refuse(w, "script piece %q is a 3D scene (blender): a scene part is not written for it", id)
				return
			}
		}
		if !found {
			api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("script piece %q not found in the script of piece %s", id, info.ID))
			return
		}
		chosen[id] = true
	}
	req.Model, req.Provider = strings.TrimSpace(req.Model), strings.TrimSpace(req.Provider)
	if req.Provider != "" {
		if err := llm.Check(settings.TaskMotionGraphics, req.Provider); err != nil && api.WriteRoutingError(w, err) {
			return
		}
	}
	if err := h.checkModel(r.Context(), req.Provider, req.Model); err != nil {
		api.Refuse(w, "%v", err)
		return
	}
	e, status, msg := h.draftEngine(motion.WithModel(llm.WithProvider(r.Context(), req.Provider), req.Model), false)
	if e == nil {
		api.WriteError(w, status, draftErrorCode(status), msg)
		return
	}
	ask := reviewAskOf(req.Review)
	if !h.attachReview(w, r.Context(), e, ask) {
		return
	}
	batch := SceneBatch{State: SceneBatchWriting, BriefMode: req.BriefMode, Model: req.Model, Provider: req.Provider, Review: ask, StartedAt: time.Now().UTC(),
		Drafts: []SceneDraftState{}, Fingerprints: map[string]string{}}
	for _, p := range script.Pieces {
		if p.Blender != nil || (len(chosen) > 0 && !chosen[p.ID]) {
			continue // a 3D piece is not made from a part
		}
		batch.Drafts = append(batch.Drafts, SceneDraftState{ScriptPiece: p.ID, State: "pending"})
		batch.Fingerprints[p.ID] = fingerprint(p)
	}
	batch.Total = len(batch.Drafts)
	for _, k := range kept { // what earlier batches left and this one does not write again
		if _, now := batch.Fingerprints[k.ScriptPiece]; !now {
			batch.Earlier = append(batch.Earlier, k)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), draftTimeout*time.Duration(max(batch.Total, 1)))
	if !h.proposals.start(info.ID, cancel) {
		cancel()
		api.WriteError(w, http.StatusConflict, api.CodeConflict, "a proposal or a batch of scenes of this piece is still being written: wait for it")
		return
	}
	h.sceneJobs.clear(info.ID)
	h.saveSceneBatch(info.ID, batch)
	h.drafts.wg.Add(1)
	go func() {
		defer h.drafts.wg.Done()
		h.runScenes(ctx, e, info.ID, batch)
	}()
	api.WriteJSON(w, http.StatusAccepted, h.sceneBatchOf(info.ID))
}

// runScenes writes the scenes of a batch one after the other, keeping the state after each.
func (h *Handler) runScenes(ctx context.Context, e *musicmotion.DraftEngine, piece string, b SceneBatch) {
	defer h.proposals.end(piece)
	defer h.sceneJobs.clear(piece)
	defer e.ReleaseVision(context.WithoutCancel(ctx))
	finish := func(state, msg string) {
		now := time.Now().UTC()
		b.State, b.FinishedAt, b.Error = state, &now, msg
		h.saveSceneBatch(piece, b)
	}
	defer func() {
		if rec := recover(); rec != nil {
			finish(SceneBatchFailed, fmt.Sprintf("the batch broke: %v", rec))
		}
	}()
	for i := range b.Drafts {
		if h.sceneJobs.wanted(piece) {
			finish(SceneBatchCancelled, "")
			return
		}
		if ctx.Err() != nil {
			finish(SceneBatchFailed, "the batch was stopped: "+ctx.Err().Error())
			return
		}
		sp := b.Drafts[i].ScriptPiece
		brief := ""
		if b.BriefMode == "literal" {
			brief = literalBrief
		}
		d, err := e.Store.Create("(scene of "+sp+")", "", nil)
		if err != nil {
			finish(SceneBatchFailed, err.Error())
			return
		}
		d.Scene, d.Brief, d.Model, d.Provider, d.ReviewAsk = &musicmotion.SceneRef{Piece: piece, ScriptPiece: sp}, brief, b.Model, b.Provider, b.Review
		if err := e.Store.Save(d); err != nil {
			finish(SceneBatchFailed, err.Error())
			return
		}
		b.Drafts[i].DraftID, b.Drafts[i].State = d.ID, musicmotion.DraftWriting
		h.saveSceneBatch(piece, b)
		e.Run(ctx, d.ID)
		h.keepWorldOfScene(e, piece, d.ID)
		b.Done = i + 1
		h.saveSceneBatch(piece, b)
	}
	finish(SceneBatchDone, "")
}

// keepWorldOfScene keeps with the piece the world the writer of a scene stated in its manifest, when the piece has none: the scenes after
// it are written in the same world (a piece made from a plan already has one and keeps it).
func (h *Handler) keepWorldOfScene(e *musicmotion.DraftEngine, piece, draftID string) {
	d, err := e.Store.Get(draftID)
	if err != nil || d.Manifest == "" {
		return
	}
	var m struct {
		World string `json:"world"`
	}
	if json.Unmarshal([]byte(d.Manifest), &m) != nil {
		return
	}
	world := clipLine(m.World)
	if world == "" {
		return
	}
	info, ok := h.readPiece(piece)
	if !ok || info.World != "" {
		return
	}
	info.World = world
	_ = h.writePiece(info)
}

// DELETE /api/music-motion/pieces/{id}/scenes: the batch stops after the draft it is writing.
func (h *Handler) handleScenesCancel(w http.ResponseWriter, r *http.Request) {
	info, ok := h.pieceFromPath(w, r)
	if !ok {
		return
	}
	b := h.sceneBatchOf(info.ID)
	if b == nil {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, "this piece has no batch of scenes")
		return
	}
	if b.State == SceneBatchWriting {
		h.sceneJobs.request(info.ID)
	}
	api.WriteJSON(w, http.StatusAccepted, b)
}

// ScenesApproveAnswer is what approving the batch did.
type ScenesApproveAnswer struct {
	Approved    []ApprovedScene    `json:"approved"`
	NotApproved []SceneDraftState  `json:"not_approved"`
	Script      musicmotion.Script `json:"script"`
}

// ApprovedScene is a scene that became a part and the part of its script piece.
type ApprovedScene struct {
	ScriptPiece string `json:"script_piece"`
	Part        string `json:"part"`
	DraftID     string `json:"draft_id"`
}

// POST /api/music-motion/pieces/{id}/scenes/approve: every ready draft of the batch becomes a scene part and the part of its
// script piece, and the script is validated and saved as PUT /script does.
func (h *Handler) handleScenesApprove(w http.ResponseWriter, r *http.Request) {
	info, ok := h.pieceFromPath(w, r)
	if !ok || h.refuseIfRendering(w, info) {
		return
	}
	b := h.sceneBatchOf(info.ID)
	if b == nil {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, "this piece has no batch of scenes")
		return
	}
	script, has := musicmotion.LoadScriptAt(h.place(info.ID))
	if !has {
		api.Refuse(w, "the piece has no script")
		return
	}
	byID := map[string]int{}
	for i, p := range script.Pieces {
		byID[p.ID] = i
	}
	for _, d := range b.Drafts { // the script must still be the one the scenes were written for
		i, there := byID[d.ScriptPiece]
		if !there || fingerprint(script.Pieces[i]) != b.Fingerprints[d.ScriptPiece] {
			api.Refuse(w, "the script changed since the batch started: script piece %q is not the one its scene was written for", d.ScriptPiece)
			return
		}
	}
	e, status, msg := h.draftCheckEngine(r.Context())
	if e == nil {
		api.WriteError(w, status, api.CodeUnavailable, msg)
		return
	}
	ans := ScenesApproveAnswer{Approved: []ApprovedScene{}, NotApproved: []SceneDraftState{}}
	// First the checks of every ready draft, which draw a page each and write nothing...
	type prepared struct {
		d SceneDraftState
		p *musicmotion.PreparedApproval
	}
	var ready []prepared
	for _, d := range b.Drafts {
		if d.State != musicmotion.DraftReady {
			ans.NotApproved = append(ans.NotApproved, d)
			continue
		}
		p, probs, err := e.PrepareApproval(r.Context(), d.DraftID)
		switch {
		case err != nil:
			d.Error = err.Error()
			ans.NotApproved = append(ans.NotApproved, d)
		case len(probs) > 0:
			d.State, d.Error = musicmotion.DraftFailed, strings.Join(probs, "; ")
			ans.NotApproved = append(ans.NotApproved, d)
		default:
			ready = append(ready, prepared{d, p})
		}
	}
	if len(ready) > 0 {
		m, ok := h.pieceMap(info.ID)
		st, hasDesign := musicmotion.LoadDesignAt(h.place(info.ID))
		if !ok || !hasDesign {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, "the piece has lost its song map or its design")
			return
		}
		// ...then, in one transaction, the parts, the script with them and the piece: all of it or none.
		var invalid error
		var committed []ApprovedScene
		err := h.ws.Store().Tx(func(tx *store.Store) error {
			cat := musicmotion.NewCatalog(musicmotion.NewDBParts(tx))
			for _, pr := range ready {
				if _, taken := cat.Get(pr.p.Part.Name); taken { // two drafts of the batch with one name: the second is not approved
					pr.d.Error = musicmotion.ErrNameTaken.Error()
					ans.NotApproved = append(ans.NotApproved, pr.d)
					continue
				}
				if err := e.CommitApproval(cat, pr.p); err != nil {
					return err
				}
				i := byID[pr.d.ScriptPiece]
				script.Pieces[i].Part, script.Pieces[i].Params, script.Pieces[i].Animate = pr.p.Part.Name, nil, nil
				if err := e.Store.On(tx).DeleteRecord(pr.d.DraftID); err != nil { // the draft ends with its part
					return err
				}
				committed = append(committed, ApprovedScene{ScriptPiece: pr.d.ScriptPiece, Part: pr.p.Part.Name, DraftID: pr.d.DraftID})
			}
			if len(committed) == 0 {
				return nil
			}
			if err := script.WithSources(h.pieceSources(info.ID)).ValidateAll(cat, h.mmLibrary(), m, st.Design, info.Width, info.Height); err != nil {
				invalid = err
				return err
			}
			if err := musicmotion.SaveScriptAt(dbPlace{ps: pieceStore{st: tx}, id: info.ID}, script); err != nil {
				return err
			}
			info.ScriptEdited, info.ScriptSource = true, "user"
			return pieceStore{st: tx}.put(info)
		})
		switch {
		case invalid != nil:
			api.Refuse(w, "none of the scenes was approved: the script with them is not valid: %v", invalid)
			return
		case err != nil:
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
			return
		}
		for _, c := range committed {
			e.Store.RemoveMedia(c.DraftID) // its picture and previews go once the change is committed
		}
		ans.Approved = committed
	}
	ans.Script = script
	api.WriteJSON(w, http.StatusOK, ans)
}

package music

// partdrafts.go is the API of the drafts of parts (spec motion-part-drafts): the assistant writes a part from a sentence
// and, optionally, a picture; the draft is checked like any part, has a preview and is only a draft until the user
// approves it. The work is in internal/music/musicmotion (DraftEngine).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"veritmo/internal/platform/store"

	"veritmo/internal/assistant/describe"
	"veritmo/internal/assistant/llm"
	"veritmo/internal/music/motionref"
	"veritmo/internal/music/musicmotion"
	"veritmo/internal/platform/settings"
	"veritmo/internal/server/api"
	"veritmo/internal/server/assistant"
	"veritmo/internal/video/motion"
)

const (
	maxDraftPicture = 20 << 20
	maxDraftJSON    = 8 << 20 // the JSON body of a draft: a part sent as text carries what it draws twice
	draftTimeout    = 30 * time.Minute
)

// draftState is the drafts of the server: the store (made when first used, where the settings are then) and the jobs
// that are running, so a delete can stop its one.
type draftState struct {
	once  sync.Once
	store *musicmotion.DraftStore
	mu    sync.Mutex
	jobs  map[string]context.CancelFunc
	wg    sync.WaitGroup
}

func (h *Handler) draftStore() *musicmotion.DraftStore {
	h.drafts.once.Do(func() {
		h.drafts.store = musicmotion.NewDraftStoreDB(filepath.Join(settings.Dir(), "music-motion", "part-drafts"), h.ws.Store())
		h.drafts.store.MarkInterrupted()
		h.drafts.jobs = map[string]context.CancelFunc{}
	})
	return h.drafts.store
}

// waitDrafts waits for the jobs that are running (tests).
func (h *Handler) waitDrafts() { h.drafts.wg.Wait() }

// draftDescribe describes a picture with the vision model the describe task is routed to, after the age check. A
// test seam through h.draftDescriber.
func (h *Handler) draftDescribe(ctx context.Context, picture []byte) (string, error) {
	if h.draftDescriber != nil {
		return h.draftDescriber(ctx, picture)
	}
	d, err := describe.ForContext(ctx, describe.KindImage)
	if err != nil {
		return "", err
	}
	look, err := d.DescribePicture(ctx, picture, describe.Options{})
	if err != nil {
		if errors.Is(err, describe.ErrMinor) {
			return "", fmt.Errorf("%w (%w)", musicmotion.ErrMinorPicture, err)
		}
		return "", err
	}
	return look.Text, nil
}

// draftVisionError says why a picture cannot be described now ("" when it can).
func (h *Handler) draftVisionError(ctx context.Context) string {
	if h.draftVisionCheck != nil {
		return h.draftVisionCheck(ctx)
	}
	if h.draftDescriber != nil {
		return ""
	}
	if err := llm.Check(settings.TaskDescribeImage, ""); err != nil {
		return err.Error()
	}
	d, err := describe.ForContext(ctx, describe.KindImage)
	if err != nil {
		return err.Error()
	}
	if d.Generic == nil {
		if _, _, models, err := d.ListModels(ctx); err != nil {
			return err.Error()
		} else if len(models) == 0 {
			return "no vision model is installed in Ollama (ollama pull " + describe.DefaultModel + ")"
		}
	}
	return ""
}

// draftEngine builds the engine for a request. The assistant is nil when there is none; the error says why the work
// cannot be done now, with the status to answer.
func (h *Handler) draftEngine(ctx context.Context, withPicture bool) (*musicmotion.DraftEngine, int, string) {
	if n := h.drafts.running(); n >= maxDraftJobs {
		return nil, http.StatusTooManyRequests, fmt.Sprintf("%d drafts are being written already: wait for one to finish", n)
	}
	b, err := h.ws.MotionBrowserFor()
	if err != nil {
		return nil, http.StatusServiceUnavailable, "there is no Chrome or Chromium to check and draw the part: " + err.Error()
	}
	if err := llm.Check(settings.TaskMotionGraphics, llm.ProviderFrom(ctx)); err != nil {
		return nil, http.StatusServiceUnavailable, "there is no assistant to write the part: " + err.Error()
	}
	newWriter := h.host.MotionWriter()
	if newWriter == nil {
		newWriter = motion.NewWriter
	}
	wr, _, err := newWriter(ctx)
	if err != nil {
		return nil, http.StatusServiceUnavailable, "there is no assistant to write the part: " + err.Error()
	}
	if withPicture {
		if msg := h.draftVisionError(ctx); msg != "" {
			return nil, http.StatusServiceUnavailable, "there is no vision model to describe the picture: " + msg
		}
	}
	if id, busy := h.ws.AnyBusy(); busy && (api.NeedsLocalMemory(ctx, settings.TaskMotionGraphics) || (withPicture && api.NeedsLocalMemory(ctx, settings.TaskDescribeImage))) {
		return nil, http.StatusConflict, fmt.Sprintf("run %s is queued or active: the models need several GB of memory, so the draft waits until no run is in progress", id)
	}
	e := &musicmotion.DraftEngine{Store: h.draftStore(), Cat: h.mmCatalog(), Browser: b.Path, Writer: wr, SceneInput: h.sceneInput}
	if withPicture {
		e.Describe = h.draftDescribe
	}
	return e, 0, ""
}

// maxDraftJobs is how many drafts of parts, scenes or reviews may be written at once; each holds a model call and a browser.
const maxDraftJobs = 4

// running is the number of draft jobs in flight.
func (d *draftState) running() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.jobs)
}

func (h *Handler) startDraft(id string, run func(ctx context.Context)) {
	ctx, cancel := context.WithTimeout(context.Background(), draftTimeout)
	h.draftStore()
	h.drafts.mu.Lock()
	h.drafts.jobs[id] = cancel
	h.drafts.mu.Unlock()
	h.drafts.wg.Add(1)
	go func() {
		defer h.drafts.wg.Done()
		defer func() {
			cancel()
			h.drafts.mu.Lock()
			delete(h.drafts.jobs, id)
			h.drafts.mu.Unlock()
		}()
		run(ctx)
	}()
}

// DraftRequest is the body that asks for a draft of a part (JSON, or the `request` part of a multipart request with a picture).
type DraftRequest struct {
	// Request is what the part has to do (1 to 500 characters).
	Request string `json:"request"`
	// ReferenceID and Frame name a key frame of a reference as the picture; leave both out for no picture (or send a
	// file in a multipart request).
	ReferenceID string `json:"reference_id"`
	Frame       string `json:"frame"`
	// Manifest and Fragment make the draft from these two files, with no assistant (instead of Request).
	Manifest string `json:"manifest"`
	Fragment string `json:"fragment"`
	// Model is the installed model that writes the part (see GET /api/assist/models); empty uses the one the
	// motion-graphics task is routed to. A model that is not installed is 422, before anything is created.
	Model string `json:"model"`
	// Provider is the id of a saved provider (GET /api/providers) that writes the part instead of the routing, for
	// this draft only; Model then names one of its models. A remote one needs consent (400 consent_required), an
	// unknown one is 400 invalid.
	Provider string `json:"provider"`
	// Scene asks for a scene written for one script piece of one piece (JSON only): the server builds the writer's input
	// from the saved piece, so `request` is not needed (the brief is the idea) and no lyric is sent.
	Scene *SceneBody `json:"scene,omitempty"`
	// Review asks for the draft to be graded by the vision model after its previews: true, or {min, attempts} to also rewrite it
	// (up to attempts times) while it scores under min. Without it the setting music_motion.review_drafts decides.
	Review *ReviewField `json:"review,omitempty"`
}

// SceneBody is the scene field of a draft request.
type SceneBody struct {
	Piece       string `json:"piece"`
	ScriptPiece string `json:"script_piece"`
	// Brief is the idea or metaphor asked for (up to 500 characters); empty lets the writer choose.
	Brief string `json:"brief,omitempty"`
}

// DraftPut is the body that replaces the manifest and the fragment of a draft by hand.
type DraftPut struct {
	Manifest string `json:"manifest"`
	Fragment string `json:"fragment"`
}

var imageMagic = []struct {
	prefix string
	at     int
}{{"\x89PNG\r\n\x1a\n", 0}, {"\xff\xd8\xff", 0}, {"WEBP", 8}}

func isPicture(b []byte) bool {
	for _, m := range imageMagic {
		if len(b) >= m.at+len(m.prefix) && string(b[m.at:m.at+len(m.prefix)]) == m.prefix {
			return true
		}
	}
	return false
}

// POST /api/music-motion/part-drafts  (JSON {request, reference_id?, frame?} or multipart with request and file?)
func (h *Handler) handleDraftCreate(w http.ResponseWriter, r *http.Request) {
	api.RaiseBodyLimit(w, r, maxDraftPicture+1<<20) // a picture upload; the JSON body has its own, smaller limit
	var req DraftRequest
	var picture []byte
	source := ""
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt == "multipart/form-data" {
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid upload: %v", err))
			return
		}
		defer func() { _ = r.MultipartForm.RemoveAll() }()
		req.Request = r.FormValue("request")
		req.Model = strings.TrimSpace(r.FormValue("model"))
		req.Provider = strings.TrimSpace(r.FormValue("provider"))
		if v := strings.TrimSpace(r.FormValue("review")); v != "" {
			on, perr := strconv.ParseBool(v)
			if perr != nil {
				api.Refuse(w, "review must be true or false")
				return
			}
			rf := &ReviewField{On: on}
			rf.Ask.Min, _ = strconv.Atoi(r.FormValue("review_min"))
			rf.Ask.Attempts, _ = strconv.Atoi(r.FormValue("review_attempts"))
			req.Review = rf
		}
		if f, _, err := r.FormFile("file"); err == nil {
			defer f.Close()
			b, err := io.ReadAll(io.LimitReader(f, maxDraftPicture+1))
			if err != nil || len(b) > maxDraftPicture {
				api.Refuse(w, "the picture is larger than %d MB", maxDraftPicture>>20)
				return
			}
			if !isPicture(b) {
				api.Refuse(w, "the file is not a picture (png, jpg or webp)")
				return
			}
			picture, source = b, "upload"
		}
	} else if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxDraftJSON)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v (the limit is %d MiB)", err, maxDraftJSON>>20))
		return
	}
	var fromRef motionref.Reference // the reference whose frame inspires the draft, when one does
	req.Request = strings.TrimSpace(req.Request)
	if req.Scene != nil && mt != "multipart/form-data" {
		h.handleSceneDraftCreate(w, r, req)
		return
	}
	rewrite := req.Manifest != "" || req.Fragment != ""
	if rewrite && req.Request == "" {
		h.handleDraftFromFiles(w, r, req, picture != nil)
		return
	}
	baseName := ""
	if rewrite { // the assistant changes this text according to the request
		if req.ReferenceID != "" || req.Frame != "" || picture != nil {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "a part is changed with a request, a manifest and a fragment: no picture or frame")
			return
		}
		var m struct {
			Name string `json:"name"`
		}
		if req.Manifest == "" || req.Fragment == "" || json.Unmarshal([]byte(req.Manifest), &m) != nil || m.Name == "" {
			api.Refuse(w, "to change a part send its manifest (a JSON with its name) and its fragment together with the request")
			return
		}
		baseName, source = m.Name, musicmotion.RewriteSource
	}
	if n := len([]rune(req.Request)); n == 0 || n > musicmotion.MaxDraftRequest {
		api.Refuse(w, "request must say what the part has to do, in 1 to %d characters", musicmotion.MaxDraftRequest)
		return
	}
	if req.ReferenceID != "" || req.Frame != "" {
		if picture != nil {
			api.Refuse(w, "send a picture or a frame of a reference, not both")
			return
		}
		ref, err := h.assistant.RefStore().Get(req.ReferenceID)
		if err != nil || ref.Analysis == nil {
			api.Refuse(w, "the reference %q does not exist or has not been analysed", req.ReferenceID)
			return
		}
		found := false
		for _, f := range ref.Analysis.Frames {
			found = found || f == req.Frame
		}
		if !found || !assistant.ReferenceFrameName.MatchString(req.Frame) {
			api.Refuse(w, "the frame %q is not one of the frames of the reference", req.Frame)
			return
		}
		b, err := os.ReadFile(filepath.Join(h.assistant.RefStore().FramesDir(ref.ID), req.Frame))
		if err != nil {
			api.Refuse(w, "the frame %q is not there any more", req.Frame)
			return
		}
		picture, source, fromRef = b, "reference:"+ref.ID+"/"+req.Frame, ref
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
	e, status, msg := h.draftEngine(motion.WithModel(llm.WithProvider(r.Context(), req.Provider), req.Model), picture != nil)
	if e == nil {
		api.WriteError(w, status, draftErrorCode(status), msg)
		return
	}
	ask := reviewAskOf(req.Review)
	if !h.attachReview(w, r.Context(), e, ask) {
		return
	}
	d, err := e.Store.Create(req.Request, source, picture)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	if req.Model != "" || req.Provider != "" || ask != nil { // kept with the draft, so a retry writes with the same ones
		d.Model, d.Provider, d.ReviewAsk = req.Model, req.Provider, ask
		if err := e.Store.Save(d); err != nil {
			_ = e.Store.Delete(d.ID)
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
			return
		}
	}
	if rewrite {
		d.Replaces = baseName
		err := e.Store.SaveBase(d.ID, req.Manifest, req.Fragment)
		if err == nil {
			err = e.Store.Save(d)
		}
		if err != nil {
			_ = e.Store.Delete(d.ID)
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
			return
		}
	}
	if fromRef.ID != "" { // the draft remembers the video, whatever happens to the reference
		d.ReferenceID, d.VideoTitle, d.Video = fromRef.ID, fromRef.Title, fromRef.Source
		if err := e.Store.Save(d); err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
			return
		}
	}
	h.startDraft(d.ID, func(ctx context.Context) { e.Run(ctx, d.ID); e.ReleaseVision(ctx) })
	api.WriteJSON(w, http.StatusAccepted, d)
}

// draftErrorCode is the error code of the status draftEngine gives.
func draftErrorCode(status int) string {
	switch status {
	case http.StatusConflict:
		return api.CodeConflict
	case http.StatusTooManyRequests:
		return api.CodeQueueFull
	}
	return api.CodeUnavailable
}

func (h *Handler) draftFromPath(w http.ResponseWriter, r *http.Request) (musicmotion.Draft, bool) {
	d, err := h.draftStore().Get(r.PathValue("id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("draft %q not found", r.PathValue("id")))
		return musicmotion.Draft{}, false
	}
	return d, true
}

// GET /api/music-motion/part-drafts
//
// With state, piece, q, sort, limit or offset it is a filtered or paged list (X-Total-Count and Link say where the others are); without
// them it answers every draft as it always did.
func (h *Handler) handleDraftList(w http.ResponseWriter, r *http.Request) {
	params, err := store.ParseList(r, musicmotion.DraftsListSpec)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	q := r.URL.Query()
	filter := musicmotion.DraftFilter{State: q.Get("state"), Piece: q.Get("piece")}
	if filter.State != "" && filter.State != musicmotion.DraftWriting && filter.State != musicmotion.DraftReady && filter.State != musicmotion.DraftFailed {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("state must be %s, %s or %s", musicmotion.DraftWriting, musicmotion.DraftReady, musicmotion.DraftFailed))
		return
	}
	if !params.Paged && filter == (musicmotion.DraftFilter{}) {
		list := h.draftStore().List()
		for i := range list {
			list[i].Manifest, list[i].Fragment = "", "" // in the read of one
		}
		api.WriteJSON(w, http.StatusOK, map[string]any{"drafts": list})
		return
	}
	list, total, err := h.draftStore().Page(filter, params)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	store.WriteListHeaders(w, r, total, params)
	api.WriteJSON(w, http.StatusOK, map[string]any{"drafts": list})
}

// GET /api/music-motion/part-drafts/{id}
func (h *Handler) handleDraftGet(w http.ResponseWriter, r *http.Request) {
	if d, ok := h.draftFromPath(w, r); ok {
		api.WriteJSON(w, http.StatusOK, d)
	}
}

// DELETE /api/music-motion/part-drafts/{id}
func (h *Handler) handleDraftDelete(w http.ResponseWriter, r *http.Request) {
	d, ok := h.draftFromPath(w, r)
	if !ok {
		return
	}
	h.drafts.mu.Lock()
	cancel := h.drafts.jobs[d.ID]
	h.drafts.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if err := h.draftStore().Delete(d.ID); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var draftPreviewName = regexp.MustCompile(`^[0-9]{2}\.png$`)

// GET /api/music-motion/part-drafts/{id}/preview/{name}
func (h *Handler) handleDraftPreview(w http.ResponseWriter, r *http.Request) {
	d, ok := h.draftFromPath(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	path := h.draftStore().PreviewPath(d.ID, name)
	if !draftPreviewName.MatchString(name) {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, "preview frame not found")
		return
	}
	if _, err := os.Stat(path); err != nil {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, "preview frame not found")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	http.ServeFile(w, r, path)
}

// refuseIfRunning answers 409 for a draft that is being written.
func (h *Handler) refuseIfRunning(w http.ResponseWriter, d musicmotion.Draft) bool {
	h.drafts.mu.Lock()
	_, running := h.drafts.jobs[d.ID]
	h.drafts.mu.Unlock()
	if running || d.State == musicmotion.DraftWriting {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, "the draft is being written: wait for it to finish or delete it")
		return true
	}
	return false
}

// PUT /api/music-motion/part-drafts/{id}  {manifest, fragment}
func (h *Handler) handleDraftPut(w http.ResponseWriter, r *http.Request) {
	d, ok := h.draftFromPath(w, r)
	if !ok || h.refuseIfRunning(w, d) {
		return
	}
	var req DraftPut
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	e, status, msg := h.draftCheckEngine(r.Context())
	if e == nil {
		api.WriteError(w, status, api.CodeUnavailable, msg)
		return
	}
	got, probs, err := e.Edit(r.Context(), d.ID, req.Manifest, req.Fragment)
	switch {
	case errors.Is(err, musicmotion.ErrDraftNotFound):
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("draft %q not found", d.ID))
	case err != nil:
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
	case len(probs) > 0:
		api.Refuse(w, "the part does not pass the checks: %s", strings.Join(probs, "; "))
	default:
		api.WriteJSON(w, http.StatusOK, got)
	}
}

// draftCheckEngine is the engine for what only needs the browser (edit and approve).
func (h *Handler) draftCheckEngine(ctx context.Context) (*musicmotion.DraftEngine, int, string) {
	b, err := h.ws.MotionBrowserFor()
	if err != nil {
		return nil, http.StatusServiceUnavailable, "there is no Chrome or Chromium to check and draw the part: " + err.Error()
	}
	return &musicmotion.DraftEngine{Store: h.draftStore(), Cat: h.mmCatalog(), Browser: b.Path, SceneInput: h.sceneInput}, 0, ""
}

// POST /api/music-motion/part-drafts/{id}/retry
func (h *Handler) handleDraftRetry(w http.ResponseWriter, r *http.Request) {
	d, ok := h.draftFromPath(w, r)
	if !ok || h.refuseIfRunning(w, d) {
		return
	}
	e, status, msg := h.draftEngine(motion.WithModel(llm.WithProvider(r.Context(), d.Provider), d.Model), d.WithPicture && d.Description == "")
	if e == nil {
		api.WriteError(w, status, draftErrorCode(status), msg)
		return
	}
	if !h.attachReview(w, r.Context(), e, d.ReviewAsk) {
		return
	}
	if err := e.Restart(d.ID); err != nil {
		api.Refuse(w, "%v", err)
		return
	}
	got, _ := e.Store.Get(d.ID)
	h.startDraft(d.ID, func(ctx context.Context) { e.Run(ctx, d.ID); e.ReleaseVision(ctx) })
	api.WriteJSON(w, http.StatusAccepted, got)
}

// POST /api/music-motion/part-drafts/{id}/approve
func (h *Handler) handleDraftApprove(w http.ResponseWriter, r *http.Request) {
	d, ok := h.draftFromPath(w, r)
	if !ok || h.refuseIfRunning(w, d) {
		return
	}
	e, status, msg := h.draftCheckEngine(r.Context())
	if e == nil {
		api.WriteError(w, status, api.CodeUnavailable, msg)
		return
	}
	part, probs, err := e.Approve(r.Context(), d.ID)
	switch {
	case errors.Is(err, musicmotion.ErrNameTaken):
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("a part called %q already is in your parts: edit the draft to give it another name", gotName(d)))
	case errors.Is(err, musicmotion.ErrDraftNotReady):
		api.Refuse(w, "the draft is %s: only a ready draft can be approved", d.State)
	case errors.Is(err, musicmotion.ErrDraftNotFound):
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("draft %q not found", d.ID))
	case err != nil:
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
	case len(probs) > 0:
		api.Refuse(w, "the part does not pass the checks: %s", strings.Join(probs, "; "))
	default:
		api.WriteJSON(w, http.StatusOK, catalogPartOf(part))
	}
}

func gotName(d musicmotion.Draft) string { return d.Name }

// partUsers lists what uses a part: the pieces whose script draws it and the styles that suggest it.
func (h *Handler) partUsers(name string) (pieces, styles []string) {
	for _, id := range h.pieces().ids() {
		if script, ok := musicmotion.LoadScriptAt(h.place(id)); ok && script.UsesPart(name) {
			pieces = append(pieces, id)
		}
	}
	for _, p := range h.gen.PresetStore().List() {
		for _, n := range p.Parts {
			if n == name {
				styles = append(styles, p.ID)
				break
			}
		}
	}
	return pieces, styles
}

func partChangeError(w http.ResponseWriter, name string, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, musicmotion.ErrPartNotFound):
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("part %q not found", name))
	case errors.Is(err, musicmotion.ErrBuiltinPart):
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("the part %q comes with the program and cannot be changed", name))
	case errors.Is(err, musicmotion.ErrPartRenamed):
		api.Refuse(w, "%v", err)
	default:
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
	}
	return true
}

// DELETE /api/music-motion/parts/{name}[?force=true]: removes a part of the user's. A part that a piece or a style uses
// is refused with 409 naming them, unless force is true.
func (h *Handler) handleMusicMotionPartDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	cat := h.mmCatalog()
	if p, ok := cat.Get(name); !ok {
		partChangeError(w, name, musicmotion.ErrPartNotFound)
		return
	} else if p.Source != "user" {
		partChangeError(w, name, musicmotion.ErrBuiltinPart)
		return
	}
	if r.URL.Query().Get("force") != "true" {
		if pieces, styles := h.partUsers(name); len(pieces)+len(styles) > 0 {
			api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf(
				"the part %q is used by pieces [%s] and styles [%s]: change them or delete it with force=true",
				name, strings.Join(pieces, ", "), strings.Join(styles, ", ")))
			return
		}
	}
	if partChangeError(w, name, cat.DeleteUserPart(name)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PUT /api/music-motion/parts/{name}  {manifest, fragment}: replaces a part of the user's after the checks of an approval.
func (h *Handler) handleMusicMotionPartPut(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req DraftPut
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if p, ok := h.mmCatalog().Get(name); !ok {
		partChangeError(w, name, musicmotion.ErrPartNotFound)
		return
	} else if p.Source != "user" {
		partChangeError(w, name, musicmotion.ErrBuiltinPart)
		return
	}
	e, status, msg := h.draftCheckEngine(r.Context())
	if e == nil {
		api.WriteError(w, status, api.CodeUnavailable, msg)
		return
	}
	part, probs, err := e.ReplaceUserPart(r.Context(), name, req.Manifest, req.Fragment)
	switch {
	case partChangeError(w, name, err):
	case len(probs) > 0:
		api.Refuse(w, "the part does not pass the checks: %s", strings.Join(probs, "; "))
	default:
		api.WriteJSON(w, http.StatusOK, catalogPartOf(part))
	}
}

// handleDraftFromFiles makes a draft from a manifest and a fragment, with no assistant: it needs only Chrome. The
// answer is the draft, ready or failed with its problems.
func (h *Handler) handleDraftFromFiles(w http.ResponseWriter, r *http.Request, req DraftRequest, withPicture bool) {
	if req.ReferenceID != "" || req.Frame != "" || withPicture {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "a manifest and a fragment make a draft with no picture or frame")
		return
	}
	e, status, msg := h.draftCheckEngine(r.Context())
	if e == nil {
		api.WriteError(w, status, api.CodeUnavailable, msg)
		return
	}
	d, err := e.CreateFromFiles(r.Context(), req.Manifest, req.Fragment)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, d)
}

package assistant

// assistant/references.go is the API of the library of references (spec motion-references): the videos the user gives as
// examples of a good piece, analysed by the server in the background. A reference comes from a YouTube address (as the
// sources of a project do, with no account and no cookies) or from an uploaded video; the video is not kept, only its
// key frames and its analysis. The library is in internal/music/motionref.

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
	"veritmo/internal/music/motionref"
	"veritmo/internal/platform/settings"
	"veritmo/internal/server/api"
)

const (
	// maxReferenceBytes bounds an uploaded or downloaded reference video.
	maxReferenceBytes = 500 << 20
	// referenceTimeout bounds the download and the analysis of one reference.
	referenceTimeout = 30 * time.Minute
	// readShotsTimeout bounds the reading of the shots of one reference by the vision model (up to 40 shots, three calls each).
	readShotsTimeout = 2 * time.Hour
)

// refState is the library of the server: its store (made on first use, where the settings are then) and the analyses
// that are running, so that deleting a reference can stop its one.
type refState struct {
	once  sync.Once
	store *motionref.Store
	mu    sync.Mutex
	jobs  map[string]context.CancelFunc
	wg    sync.WaitGroup
}

// RefStore is the library; the first call marks the analyses a stop left half done as failed.
func (h *Handler) RefStore() *motionref.Store {
	h.refs.once.Do(func() {
		h.refs.store = motionref.NewStoreDB(filepath.Join(settings.Dir(), "references"), h.host.Store())
		h.refs.store.MarkInterrupted()
		h.refs.jobs = map[string]context.CancelFunc{}
	})
	return h.refs.store
}

// refAnalyser is the analyser of references: it looks and reads the shots with the vision model routed to describing.
func (h *Handler) refAnalyser() *motionref.Analyser {
	look := func(ctx context.Context, frames [][]byte) (string, error) {
		d, err := describe.ForContext(ctx, describe.KindVideo)
		if err != nil {
			return "", err
		}
		l, err := d.DescribeLook(ctx, frames, describe.Options{})
		return l.Text, err
	}
	reader := func(ctx context.Context, planned int) (motionref.ShotAsker, error) {
		if h.RefReader != nil {
			return h.RefReader(ctx, planned)
		}
		d, err := describe.ForContext(ctx, describe.KindVideo)
		if err != nil {
			return nil, err
		}
		s, err := d.OpenSession(ctx, describe.Options{}, planned)
		if err != nil {
			return nil, err
		}
		return s, nil
	}
	return &motionref.Analyser{Runner: h.host.Runner(), Looker: look, Reader: reader}
}

// analyseReference measures a video and writes its key frames in framesDir. A test seam through h.RefAnalyse.
func (h *Handler) analyseReference(ctx context.Context, video, framesDir string) (*motionref.Analysis, error) {
	if h.RefAnalyse != nil {
		return h.RefAnalyse(ctx, video, framesDir)
	}
	return h.refAnalyser().Analyse(ctx, video, framesDir)
}

// readReferenceShots reads the shots of a reference with the vision model, saving each change of the state of the reading. The
// job has its own bound and is the one a delete stops from now on.
func (h *Handler) readReferenceShots(ref motionref.Reference, video string) {
	st := h.RefStore()
	an := ref.Analysis
	if an == nil || an.BreakdownState != motionref.BreakdownPending {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), readShotsTimeout)
	defer cancel()
	h.refs.mu.Lock()
	h.refs.jobs[ref.ID] = cancel
	h.refs.mu.Unlock()
	save := func() { _ = st.SaveAnalysis(ref.ID, an) }
	if err := h.refAnalyser().ReadShots(ctx, video, st.ShotsDir(ref.ID), an, save); err != nil {
		return // deleted or stopped: nothing to write
	}
	save()
}

// ReferenceRequest is the JSON body that adds a reference to the library: the address of a video and what the user says about it.
type ReferenceRequest struct {
	URL   string   `json:"url"`
	Title string   `json:"title"`
	Notes string   `json:"notes"`
	Tags  []string `json:"tags"`
}

// ReferencePatch is the body that changes what the user says about a reference; a field that is absent is left alone.
type ReferencePatch struct {
	Title *string   `json:"title"`
	Notes *string   `json:"notes"`
	Tags  *[]string `json:"tags"`
}

// POST /api/music-motion/references  (JSON {url, title, notes, tags} or multipart with a file)
func (h *Handler) handleReferenceCreate(w http.ResponseWriter, r *http.Request) {
	st := h.RefStore()
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var req ReferenceRequest
	var upload string // the uploaded video, in a folder that the job removes
	if mt == "multipart/form-data" {
		api.RaiseBodyLimit(w, r, maxReferenceBytes+1<<20)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid upload: %v", err))
			return
		}
		defer func() { _ = r.MultipartForm.RemoveAll() }()
		req.Title, req.Notes = r.FormValue("title"), r.FormValue("notes")
		if t := r.FormValue("tags"); t != "" {
			if json.Unmarshal([]byte(t), &req.Tags) != nil {
				req.Tags = strings.Split(t, ",")
			}
		}
		f, hdr, err := r.FormFile("file")
		if err != nil {
			api.Refuse(w, "send the video in the form field \"file\" or an address in a JSON body")
			return
		}
		defer f.Close()
		if req.Title == "" {
			req.Title = strings.TrimSuffix(hdr.Filename, filepath.Ext(hdr.Filename))
		}
		if err := os.MkdirAll(st.Dir, 0o755); err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
			return
		}
		work, err := os.MkdirTemp(st.Dir, ".upload-")
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
			return
		}
		keep := false
		defer func() {
			if !keep {
				_ = os.RemoveAll(work)
			}
		}()
		upload = filepath.Join(work, "video"+strings.ToLower(filepath.Ext(hdr.Filename)))
		out, err := os.Create(upload)
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
			return
		}
		_, cerr := io.Copy(out, f)
		out.Close()
		if cerr != nil {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("cannot read the upload: %v", cerr))
			return
		}
		if msg := h.checkVideo(r.Context(), upload); msg != "" {
			api.Refuse(w, "%s", msg)
			return
		}
		title, notes, tags, err := motionref.CheckText(req.Title, req.Notes, req.Tags)
		if err != nil {
			api.Refuse(w, "%v", err)
			return
		}
		ref, err := st.Create(title, notes, tags, "")
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
			return
		}
		keep = true
		h.startReference(ref, func(ctx context.Context) (string, error) { return upload, nil }, func() { _ = os.RemoveAll(work) })
		api.WriteJSON(w, http.StatusAccepted, ref)
		return
	}

	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	target, err := api.ParseYouTubeURL(req.URL)
	if err != nil {
		api.Refuse(w, "%v", err)
		return
	}
	if _, err := h.host.LookPath("yt-dlp"); err != nil {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, "yt-dlp is not installed: run `bash scripts/get-ytdlp.sh`")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), api.URLSourceTimeout)
	defer cancel()
	meta, err := h.host.Runner().Run(ctx, "yt-dlp", []string{"--ignore-config", "--no-playlist", "--no-warnings", "--print", "%(duration)s\n%(title)s", "--", target})
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		api.YtdlpFailure(w, err)
		return
	}
	lines := strings.SplitN(strings.TrimSpace(string(meta)), "\n", 2)
	secs, perr := strconv.ParseFloat(strings.TrimSpace(lines[0]), 64)
	if perr != nil || secs <= 0 {
		api.Refuse(w, "cannot tell how long this video is (a live stream?): only finished videos are accepted")
		return
	}
	if secs > motionref.MaxSeconds {
		api.Refuse(w, "the video lasts %d minutes: the limit is %d", int(secs/60), motionref.MaxSeconds/60)
		return
	}
	if req.Title == "" && len(lines) > 1 {
		req.Title = lines[1]
	}
	title, notes, tags, err := motionref.CheckText(req.Title, req.Notes, req.Tags)
	if err != nil {
		api.Refuse(w, "%v", err)
		return
	}
	ref, err := st.Create(title, notes, tags, target)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	work := filepath.Join(st.Dir, ".work-"+ref.ID) // outside the reference folder: a delete must not be undone by the job
	h.startReference(ref, func(ctx context.Context) (string, error) { return h.DownloadVideo(ctx, target, work) }, func() { _ = os.RemoveAll(work) })
	api.WriteJSON(w, http.StatusAccepted, ref)
}

// checkVideo says why a file is not a usable video ("" when it is): it has a picture, a length and is not too long.
func (h *Handler) checkVideo(ctx context.Context, path string) string {
	out, err := h.host.Runner().Run(ctx, "ffprobe", []string{"-v", "error", "-select_streams", "v:0", "-show_entries", "stream=codec_type:format=duration", "-of", "default=nw=1", path})
	if err != nil || !strings.Contains(string(out), "codec_type=video") {
		return "the file is not a video"
	}
	for _, l := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "duration="); ok {
			if secs, err := strconv.ParseFloat(v, 64); err == nil && secs > motionref.MaxSeconds {
				return fmt.Sprintf("the video lasts %d minutes: the limit is %d", int(secs/60), motionref.MaxSeconds/60)
			}
		}
	}
	return ""
}

// DownloadVideo fetches the video of an address (up to 720 lines, no account, no cookie) into work.
func (h *Handler) DownloadVideo(ctx context.Context, target, work string) (string, error) {
	if err := os.MkdirAll(work, 0o755); err != nil {
		return "", err
	}
	if _, err := h.host.Runner().Run(ctx, "yt-dlp", []string{"--ignore-config", "--no-playlist", "--no-warnings",
		"-f", "bv*[height<=720]+ba/b[height<=720]/b", "--merge-output-format", "mp4",
		"--max-filesize", strconv.FormatInt(maxReferenceBytes, 10), "-o", filepath.Join(work, "video.%(ext)s"), "--", target}); err != nil {
		return "", fmt.Errorf("cannot get the video (unavailable, removed, or yt-dlp is out of date: run scripts/get-ytdlp.sh to update it): %s", api.LastLines(err.Error(), 3))
	}
	files, _ := filepath.Glob(filepath.Join(work, "video.*"))
	if len(files) == 0 {
		return "", errors.New("the download produced no video")
	}
	return files[0], nil
}

// startReference analyses a reference in the background: it gets the video, measures it and sets the state. The
// video is removed afterwards by cleanup. A delete cancels the job, which then writes nothing.
func (h *Handler) startReference(ref motionref.Reference, get func(ctx context.Context) (string, error), cleanup func()) {
	st := h.RefStore()
	ctx, cancel := context.WithTimeout(context.Background(), referenceTimeout)
	h.refs.mu.Lock()
	h.refs.jobs[ref.ID] = cancel
	h.refs.mu.Unlock()
	h.refs.wg.Add(1)
	go func() {
		defer h.refs.wg.Done()
		defer func() {
			cancel()
			cleanup()
			h.refs.mu.Lock()
			delete(h.refs.jobs, ref.ID)
			h.refs.mu.Unlock()
		}()
		video, err := get(ctx)
		var an *motionref.Analysis
		if err == nil {
			an, err = h.analyseReference(ctx, video, st.FramesDir(ref.ID))
		}
		if ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
			return // deleted: nothing to write
		}
		if err != nil {
			ref.State, ref.Error = motionref.StateFailed, err.Error()
		} else {
			ref.State, ref.Analysis = motionref.StateReady, an
		}
		_ = st.Save(ref)
		if err == nil && ctx.Err() == nil {
			h.readReferenceShots(ref, video) // after ready, in the same job: the video is still there
		}
	}()
}

// WaitReferences waits for the analyses that are running (tests).
func (h *Handler) WaitReferences() { h.refs.wg.Wait() }

// GET /api/music-motion/references
//
// With state, q, sort, limit or offset it is a filtered or paged list (X-Total-Count and Link say where the others are); without them it
// answers every reference as it always did.
func (h *Handler) handleReferenceList(w http.ResponseWriter, r *http.Request) {
	params, err := store.ParseList(r, motionref.ReferencesListSpec)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	state := r.URL.Query().Get("state")
	if state != "" && state != motionref.StateAnalysing && state != motionref.StateReady && state != motionref.StateFailed {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("state must be %s, %s or %s", motionref.StateAnalysing, motionref.StateReady, motionref.StateFailed))
		return
	}
	if !params.Paged && state == "" {
		api.WriteJSON(w, http.StatusOK, map[string]any{"references": h.RefStore().List()})
		return
	}
	list, total, err := h.RefStore().Page(state, params)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	store.WriteListHeaders(w, r, total, params)
	api.WriteJSON(w, http.StatusOK, map[string]any{"references": list})
}

func (h *Handler) referenceFromPath(w http.ResponseWriter, r *http.Request) (motionref.Reference, bool) {
	ref, err := h.RefStore().Get(r.PathValue("id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("reference %q not found", r.PathValue("id")))
		return motionref.Reference{}, false
	}
	return ref, true
}

// GET /api/music-motion/references/{id}
func (h *Handler) handleReferenceGet(w http.ResponseWriter, r *http.Request) {
	if ref, ok := h.referenceFromPath(w, r); ok {
		api.WriteJSON(w, http.StatusOK, ref)
	}
}

// PATCH /api/music-motion/references/{id}  {title, notes, tags}, each optional
func (h *Handler) handleReferencePatch(w http.ResponseWriter, r *http.Request) {
	ref, ok := h.referenceFromPath(w, r)
	if !ok {
		return
	}
	var req ReferencePatch
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	got, err := h.RefStore().Update(ref.ID, req.Title, req.Notes, req.Tags)
	if err != nil {
		if errors.Is(err, motionref.ErrNotFound) {
			api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("reference %q not found", ref.ID))
			return
		}
		api.Refuse(w, "%v", err)
		return
	}
	api.WriteJSON(w, http.StatusOK, got)
}

// DELETE /api/music-motion/references/{id}
func (h *Handler) handleReferenceDelete(w http.ResponseWriter, r *http.Request) {
	ref, ok := h.referenceFromPath(w, r)
	if !ok {
		return
	}
	h.refs.mu.Lock()
	cancel := h.refs.jobs[ref.ID]
	h.refs.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if err := h.RefStore().Delete(ref.ID); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ReferenceFrameName is the shape of the name of a key frame of a reference (two digits and .jpg).
var ReferenceFrameName = regexp.MustCompile(`^[0-9]{2}\.jpg$`)

// GET /api/music-motion/references/{id}/frames/{name}
func (h *Handler) handleReferenceFrame(w http.ResponseWriter, r *http.Request) {
	ref, ok := h.referenceFromPath(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if !ReferenceFrameName.MatchString(name) {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, "frame not found")
		return
	}
	path := filepath.Join(h.RefStore().FramesDir(ref.ID), name)
	if _, err := os.Stat(path); err != nil {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, "frame not found")
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeFile(w, r, path)
}

// BreakdownRequest is the optional JSON body of POST /api/music-motion/references/{id}/breakdown.
type BreakdownRequest struct {
	// Read says whether the vision model reads the shots after they are found (default true).
	Read *bool `json:"read"`
}

// POST /api/music-motion/references/{id}/breakdown  {read?}
//
// Finds the shots of a ready reference again, makes its rhythm sheet and, with read, has the vision model read them, in the
// background. The video is not kept, so it is fetched again from the address the reference was added with; a reference made
// from an uploaded file has none and cannot be broken down again. The key frames, the colours, the tempo and the look do not
// change.
func (h *Handler) handleReferenceBreakdown(w http.ResponseWriter, r *http.Request) {
	ref, ok := h.referenceFromPath(w, r)
	if !ok {
		return
	}
	var req BreakdownRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if ref.State != motionref.StateReady || ref.Analysis == nil {
		api.Refuse(w, "the reference is %s: only a ready reference can be broken down", ref.State)
		return
	}
	h.refs.mu.Lock()
	_, running := h.refs.jobs[ref.ID]
	h.refs.mu.Unlock()
	if running {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, "a breakdown of this reference is running")
		return
	}
	if ref.Source == "" {
		api.Refuse(w, "the video of this reference is not kept and it has no address to fetch it again (it was uploaded)")
		return
	}
	read := req.Read == nil || *req.Read
	ref.Analysis.BreakdownState, ref.Analysis.BreakdownNote = motionref.BreakdownPending, ""
	if err := h.RefStore().SaveAnalysis(ref.ID, ref.Analysis); err != nil {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("reference %q not found", ref.ID))
		return
	}
	h.startBreakdown(ref, read)
	api.WriteJSON(w, http.StatusAccepted, ref)
}

// breakdownReference finds the shots of a video and fills the analysis. A test seam through h.RefBreakdown.
func (h *Handler) breakdownReference(ctx context.Context, video string, an *motionref.Analysis) error {
	if h.RefBreakdown != nil {
		return h.RefBreakdown(ctx, video, an)
	}
	return h.refAnalyser().Breakdown(ctx, video, an)
}

// startBreakdown runs the breakdown of a reference in the background: it fetches the video, finds the shots, and reads them
// when read is set. A delete cancels it.
func (h *Handler) startBreakdown(ref motionref.Reference, read bool) {
	st := h.RefStore()
	ctx, cancel := context.WithTimeout(context.Background(), referenceTimeout)
	h.refs.mu.Lock()
	h.refs.jobs[ref.ID] = cancel
	h.refs.mu.Unlock()
	h.refs.wg.Add(1)
	work := filepath.Join(st.Dir, ".work-"+ref.ID)
	go func() {
		defer h.refs.wg.Done()
		defer func() {
			cancel()
			_ = os.RemoveAll(work)
			h.refs.mu.Lock()
			delete(h.refs.jobs, ref.ID)
			h.refs.mu.Unlock()
		}()
		an := *ref.Analysis
		fail := func(note string) {
			an.BreakdownState, an.BreakdownNote = motionref.BreakdownFailed, note
			_ = st.SaveAnalysis(ref.ID, &an)
		}
		video, err := h.DownloadVideo(ctx, ref.Source, work)
		if err == nil {
			err = h.breakdownReference(ctx, video, &an)
		}
		if ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
			return // deleted: nothing to write
		}
		if err != nil {
			fail("the breakdown failed: " + err.Error())
			return
		}
		_ = os.RemoveAll(st.ShotsDir(ref.ID)) // the pictures of the old shots are not those of the new ones
		if !read {
			an.BreakdownState, an.BreakdownNote = motionref.BreakdownSkipped, "the shots were not read: read was false"
			_ = st.SaveAnalysis(ref.ID, &an)
			return
		}
		ref.Analysis = &an
		_ = st.SaveAnalysis(ref.ID, &an)
		h.readReferenceShots(ref, video)
	}()
}

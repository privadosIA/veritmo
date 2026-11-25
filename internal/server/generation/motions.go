package generation

// motions.go is the library of motions (spec video-edit-motion-library): short videos of people moving that the
// video-edit tool `motion-control` applies to any picture. A motion is added from a video, prepared in the background
// (its control through the vision service, a preview) and then used by its identifier. The video itself is not kept.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"veritmo/internal/platform/settings"
	pstore "veritmo/internal/platform/store"
	"veritmo/internal/project/config"
	"veritmo/internal/server/api"
	"veritmo/internal/video/motionlib"
	"veritmo/internal/video/videoedit"
)

// libraryMotionTimeout bounds the preparation of one motion (a download and the control of up to 480 frames).
const libraryMotionTimeout = 30 * time.Minute

// libraryJob is a preparation that is running; done closes when it has ended, so a delete can wait for it.
type libraryJob struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// libraryState is the library of the server: its store (made on first use, where the settings are then) and the
// preparations that are running.
type libraryState struct {
	once  sync.Once
	store *motionlib.Store
	mu    sync.Mutex
	jobs  map[string]*libraryJob
	wg    sync.WaitGroup
}

func (h *Handler) libraryStore() *motionlib.Store {
	h.library.once.Do(func() {
		// the record of each motion is a row of the database; its control frames and its preview are in its folder of the settings directory
		h.library.store = motionlib.NewStoreDB(filepath.Join(settings.Dir(), "motions"), h.host.Workspace().Store())
		h.library.store.MarkInterrupted()
		h.library.jobs = map[string]*libraryJob{}
	})
	return h.library.store
}

// waitLibrary waits for the preparations that are running (tests).
func (h *Handler) waitLibrary() { h.library.wg.Wait() }

// libraryMotionView is a motion as the API shows it.
type libraryMotionView struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Tags     []string `json:"tags"`
	Duration float64  `json:"duration"`
	People   int      `json:"people"`
	State    string   `json:"state"`
	Preview  string   `json:"preview"`
	// Source is the YouTube address the video came from; absent for a video that was uploaded as a file.
	Source string `json:"source,omitempty"`
	Error  string `json:"error,omitempty"`
}

func viewOfLibraryMotion(m motionlib.Motion) libraryMotionView {
	return libraryMotionView{ID: m.ID, Title: m.Title, Tags: m.Tags, Duration: m.Duration, People: m.People, State: m.State,
		Preview: "/api/motions/" + m.ID + "/preview", Source: m.Source, Error: m.Error}
}

// libraryMotionRequest is the `request` part of POST /api/motions.
type libraryMotionRequest struct {
	Title  string   `json:"title,omitempty"`
	Tags   []string `json:"tags,omitempty"`
	Source string   `json:"source,omitempty"`
}

// GET /api/motions[?limit=&offset=&sort=&q=&state=]: the motions, the most recent first; sort is created_at or title, q is searched in the title and the
// tags, state is processing, ready or failed. With a page parameter X-Total-Count and Link say how many there are in all.
func (h *Handler) handleLibraryMotionList(w http.ResponseWriter, r *http.Request) {
	params, err := pstore.ParseList(r, motionlib.ListSpec)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	state := r.URL.Query().Get("state")
	if state != "" && state != motionlib.StateProcessing && state != motionlib.StateReady && state != motionlib.StateFailed {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "state must be processing, ready or failed")
		return
	}
	motions, total, err := h.libraryStore().ListPage(state, params)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	out := []libraryMotionView{}
	for _, m := range motions {
		out = append(out, viewOfLibraryMotion(m))
	}
	pstore.WriteListHeaders(w, r, total, params)
	api.WriteJSON(w, http.StatusOK, out)
}

// PATCH /api/motions/{id} {title?, tags?}: changes the title and the tags sent (with the limits they have when a motion is added); the rest of the
// motion stays. 400 for another field, 404 for a motion that does not exist.
func (h *Handler) handleLibraryMotionPatch(w http.ResponseWriter, r *http.Request) {
	m, ok := h.libraryMotionFromPath(w, r)
	if !ok {
		return
	}
	var in struct {
		Title *string   `json:"title"`
		Tags  *[]string `json:"tags"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("only title and tags can be changed: %v", err))
		return
	}
	title, tags := m.Title, m.Tags
	if in.Title != nil {
		title = *in.Title
	}
	if in.Tags != nil {
		tags = *in.Tags
	}
	title, tags, err := motionlib.CheckText(title, tags)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	if title == "" {
		title = "motion"
	}
	m.Title, m.Tags = title, tags
	if err := h.libraryStore().Save(m); err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, motionlib.ErrNotFound) {
			code = http.StatusNotFound
		}
		api.WriteError(w, code, api.CodeInternal, err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, viewOfLibraryMotion(m))
}

func (h *Handler) libraryMotionFromPath(w http.ResponseWriter, r *http.Request) (motionlib.Motion, bool) {
	m, err := h.libraryStore().Get(r.PathValue("id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("motion %q not found", r.PathValue("id")))
		return motionlib.Motion{}, false
	}
	return m, true
}

// GET /api/motions/{id}
func (h *Handler) handleLibraryMotionGet(w http.ResponseWriter, r *http.Request) {
	if m, ok := h.libraryMotionFromPath(w, r); ok {
		api.WriteJSON(w, http.StatusOK, viewOfLibraryMotion(m))
	}
}

// GET /api/motions/{id}/preview
func (h *Handler) handleLibraryMotionPreview(w http.ResponseWriter, r *http.Request) {
	m, ok := h.libraryMotionFromPath(w, r)
	if !ok {
		return
	}
	f, err := os.Open(h.libraryStore().PreviewPath(m.ID))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("motion %q has no preview yet", m.ID))
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	http.ServeContent(w, r, "preview.mp4", fi.ModTime(), f)
}

// DELETE /api/motions/{id}: stops the preparation if it runs and removes the motion with its files.
func (h *Handler) handleLibraryMotionDelete(w http.ResponseWriter, r *http.Request) {
	m, ok := h.libraryMotionFromPath(w, r)
	if !ok {
		return
	}
	h.libraryStore() // the jobs map exists
	h.library.mu.Lock()
	job := h.library.jobs[m.ID]
	h.library.mu.Unlock()
	if job != nil {
		job.cancel()
		select {
		case <-job.done:
		case <-time.After(20 * time.Second):
			api.WriteError(w, http.StatusConflict, api.CodeConflict, "the preparation did not stop in time; try again")
			return
		}
	}
	if err := h.libraryStore().Delete(m.ID); err != nil && !errors.Is(err, motionlib.ErrNotFound) {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// visionForMotions is the vision service when it can be used, or a 503 with why not.
func (h *Handler) visionForMotions(ctx context.Context) (videoedit.Vision, int, error) {
	v := h.host.VideoEdit().VisionOf()
	if v == nil {
		return nil, http.StatusServiceUnavailable, errors.New("the vision service is not set up")
	}
	if ok, why := v.Ready(ctx); !ok {
		return nil, http.StatusServiceUnavailable, fmt.Errorf("the vision service cannot be used now: %s", why)
	}
	return v, 0, nil
}

// checkMotionVideo says why a video is not one a motion can be made from ("" when it is) and gives what is known of it.
func (h *Handler) checkMotionVideo(ctx context.Context, path string) (videoedit.Info, string) {
	info, err := h.host.VideoEdit().Probe(ctx, path)
	if err != nil {
		return info, "the file is not a video this machine can read"
	}
	if info.Duration > motionlib.MaxSeconds+0.5 {
		return info, fmt.Sprintf("the video lasts %.0f seconds: a motion is at most %d", info.Duration, motionlib.MaxSeconds)
	}
	if info.Duration > 0 && info.Duration < videoedit.MotionSeconds-0.1 {
		return info, fmt.Sprintf("the video lasts %.1f seconds: a motion needs at least %.1f", info.Duration, videoedit.MotionSeconds)
	}
	return info, ""
}

// POST /api/motions: a multipart body (a `request` part and the `video`) or a JSON one with a `source` address.
func (h *Handler) handleLibraryMotionCreate(w http.ResponseWriter, r *http.Request) {
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var req libraryMotionRequest
	var upload string
	work, err := os.MkdirTemp("", "motion-*")
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

	if ct == "multipart/form-data" {
		api.RaiseBodyLimit(w, r, config.MaxSourceBytes+1<<20) // the video and the JSON, nothing else
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid multipart body: %v", err))
			return
		}
		defer func() {
			if r.MultipartForm != nil {
				_ = r.MultipartForm.RemoveAll()
			}
		}()
		if raw := r.FormValue("request"); raw != "" {
			if err := json.Unmarshal([]byte(raw), &req); err != nil {
				api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON in the request part: %v", err))
				return
			}
		}
		f, hdr, ferr := r.FormFile("video")
		if ferr == nil {
			defer f.Close()
			ext := strings.ToLower(filepath.Ext(hdr.Filename))
			if !config.ValidClipName("origen" + ext) {
				api.Refuse(w, "the video part must be an .mp4, .mov, .webm or .mkv file")
				return
			}
			upload = filepath.Join(work, "origen"+ext)
			out, err := os.Create(upload)
			if err != nil {
				api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
				return
			}
			n, cerr := io.Copy(out, io.LimitReader(f, config.MaxSourceBytes+1))
			_ = out.Close()
			switch {
			case cerr != nil || n == 0:
				api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "the video part is empty or cannot be read")
				return
			case n > config.MaxSourceBytes:
				api.WriteError(w, http.StatusRequestEntityTooLarge, api.CodeInvalid, fmt.Sprintf("the video is larger than %d MB", config.MaxSourceBytes>>20))
				return
			}
		}
	} else if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	title, tags, err := motionlib.CheckText(req.Title, req.Tags)
	if err != nil {
		api.Refuse(w, "%v", err)
		return
	}
	switch {
	case upload != "" && req.Source != "":
		api.Refuse(w, "send the video or an address, not both")
		return
	case upload == "" && req.Source == "":
		api.Refuse(w, "the video is needed: send it as the video part, or an address in source")
		return
	}
	v, status, err := h.visionForMotions(r.Context())
	if err != nil {
		api.WriteError(w, status, api.CodeUnavailable, err.Error())
		return
	}

	st := h.libraryStore()
	if upload != "" {
		// The video is looked at now: not a video, too long or too short, or nobody in it are refused before anything is kept.
		info, why := h.checkMotionVideo(r.Context(), upload)
		if why != "" {
			api.Refuse(w, "%s", why)
			return
		}
		switch err := videoedit.CheckPeople(r.Context(), v, upload, info.Duration, work); {
		case errors.Is(err, videoedit.ErrNoPeople):
			api.Refuse(w, "no person was found in the video: a motion is the movement of people")
			return
		case err != nil:
			api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, fmt.Sprintf("the vision service failed: %v", err))
			return
		}
		m, err := st.Create(title, tags)
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
			return
		}
		keep = true
		h.startLibraryMotion(m, v, func(ctx context.Context) (string, error) { return upload, nil }, func() { _ = os.RemoveAll(work) })
		api.WriteJSON(w, http.StatusAccepted, viewOfLibraryMotion(m))
		return
	}

	// An address of a video the server can read (the same ones the sources of a project take): it is downloaded in the
	// background, so what is wrong with the video itself is said by the state `failed`.
	target, err := api.ParseYouTubeURL(req.Source)
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
	switch {
	case perr != nil || secs <= 0:
		api.Refuse(w, "cannot tell how long this video is (a live stream?): only finished videos are accepted")
		return
	case secs > motionlib.MaxSeconds+0.5:
		api.Refuse(w, "the video lasts %.0f seconds: a motion is at most %d", secs, motionlib.MaxSeconds)
		return
	case secs < videoedit.MotionSeconds-0.1:
		api.Refuse(w, "the video lasts %.1f seconds: a motion needs at least %.1f", secs, videoedit.MotionSeconds)
		return
	}
	if title == "" && len(lines) > 1 {
		title, tags, err = motionlib.CheckText(lines[1], tags)
		if err != nil {
			title = ""
		}
	}
	m, err := st.Create(title, tags)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	m.Source = target // the address is kept, the video is not
	if err := st.Save(m); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	dl := filepath.Join(st.Dir, ".work-"+m.ID) // outside the motion's folder: a delete must not be undone by the job
	keep = false
	h.startLibraryMotion(m, v, func(ctx context.Context) (string, error) { return h.assistant.DownloadVideo(ctx, target, dl) }, func() { _ = os.RemoveAll(dl) })
	api.WriteJSON(w, http.StatusAccepted, viewOfLibraryMotion(m))
}

// startLibraryMotion prepares a motion in the background: it gets the video, makes its control and its preview, and sets the
// state. The video and the frames are removed afterwards by cleanup. A delete cancels the job, which then writes nothing.
func (h *Handler) startLibraryMotion(m motionlib.Motion, v videoedit.Vision, get func(ctx context.Context) (string, error), cleanup func()) {
	st := h.libraryStore()
	ctx, cancel := context.WithTimeout(context.Background(), libraryMotionTimeout)
	job := &libraryJob{cancel: cancel, done: make(chan struct{})}
	h.library.mu.Lock()
	h.library.jobs[m.ID] = job
	h.library.mu.Unlock()
	h.library.wg.Add(1)
	go func() {
		defer h.library.wg.Done()
		defer func() {
			cancel()
			cleanup()
			h.library.mu.Lock()
			delete(h.library.jobs, m.ID)
			h.library.mu.Unlock()
			close(job.done)
		}()
		work, err := os.MkdirTemp("", "motion-work-*")
		if err != nil {
			h.finishLibraryMotion(st, m, motionlib.Motion{}, err)
			return
		}
		defer os.RemoveAll(work)
		video, err := get(ctx)
		var info videoedit.Info
		var made videoedit.MotionMade
		if err == nil {
			info, err = h.host.VideoEdit().Probe(ctx, video)
		}
		if err == nil {
			if _, why := h.checkMotionInfo(info); why != "" {
				err = errors.New(why)
			}
		}
		if err == nil {
			made, err = videoedit.PrepareMotion(ctx, v, video, info, st.ControlDir(m.ID), st.PreviewPath(m.ID), work)
			if errors.Is(err, videoedit.ErrNoPeople) {
				err = errors.New("no person was found in the video: a motion is the movement of people")
			}
		}
		if ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
			return // deleted: nothing to write
		}
		done := m
		if err == nil {
			done.State, done.Frames, done.People, done.Width, done.Height = motionlib.StateReady, made.Frames, made.People, made.Width, made.Height
			done.Duration = math.Round(float64(made.Frames)/motionlib.FPS*100) / 100
		}
		h.finishLibraryMotion(st, m, done, err)
	}()
}

// checkMotionInfo is checkMotionVideo for what is already probed.
func (h *Handler) checkMotionInfo(info videoedit.Info) (videoedit.Info, string) {
	switch {
	case info.Duration > motionlib.MaxSeconds+0.5:
		return info, fmt.Sprintf("the video lasts %.0f seconds: a motion is at most %d", info.Duration, motionlib.MaxSeconds)
	case info.Duration > 0 && info.Duration < videoedit.MotionSeconds-0.1:
		return info, fmt.Sprintf("the video lasts %.1f seconds: a motion needs at least %.1f", info.Duration, videoedit.MotionSeconds)
	}
	return info, ""
}

// finishLibraryMotion writes the end of a preparation: the motion ready, or failed with the reason and without its control.
func (h *Handler) finishLibraryMotion(st *motionlib.Store, m, done motionlib.Motion, err error) {
	if cur, gerr := st.Get(m.ID); gerr == nil { // a title or tags changed while it was prepared (PATCH) are kept
		m.Title, m.Tags, done.Title, done.Tags = cur.Title, cur.Tags, cur.Title, cur.Tags
	}
	if err != nil {
		m.State, m.Error = motionlib.StateFailed, err.Error()
		_ = os.RemoveAll(st.ControlDir(m.ID))
		_ = os.Remove(st.PreviewPath(m.ID))
		_ = st.Save(m)
		return
	}
	_ = st.Save(done)
}

// --- the library as motion control uses it -----------------------------------------------------------

// libraryBridge gives motion control the windows of the control of the motions.
type libraryBridge struct{ h *Handler }

func (b libraryBridge) Window(id string, start float64) (motionlib.Window, error) {
	return b.h.libraryStore().WindowAt(id, start)
}

// motionReady says whether a motion can be used now with the window asked for, and why not when it cannot.
func (h *Handler) libraryMotionReady(id string, start float64) (bool, string) {
	if _, err := h.libraryStore().WindowAt(id, start); err != nil {
		if errors.Is(err, motionlib.ErrNotFound) {
			return false, fmt.Sprintf("motion %q does not exist", id)
		}
		return false, fmt.Sprintf("motion %s cannot be used: %v", id, err)
	}
	return true, ""
}

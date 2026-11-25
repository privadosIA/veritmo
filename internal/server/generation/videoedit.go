package generation

// videoedit.go is the video edit of the generation API (spec video-edit-api): a video, a tool and its settings
// become a hidden project in video_edit mode that the run queue makes, with the result read back from the
// description the tool wrote next to it.

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
	"strings"
	"time"
	"veritmo/internal/platform/store"
	"veritmo/internal/server/workspace"

	"veritmo/internal/platform/fsutil"
	"veritmo/internal/project/config"
	"veritmo/internal/project/pipeline"
	"veritmo/internal/server/api"
	"veritmo/internal/video/videoedit"
)

// maxEditSide is the largest side a scaled video may have.
const maxEditSide = 8192

// videoEditRequest is the `request` part of POST /api/video-edits.
type videoEditRequest struct {
	Tool     string         `json:"tool"`
	Settings map[string]any `json:"settings,omitempty"`
	// Motion is a motion of the library, instead of the video part (motion-control only).
	Motion string `json:"motion,omitempty"`
	// Client is the dashboard that asks (see generationRequest).
	Client *string `json:"client,omitempty"`
}

// videoEditResult is what a finished edit made, as the API shows it.
type videoEditResult struct {
	Video         string   `json:"video"`
	Format        string   `json:"format"`
	HasAudio      bool     `json:"has_audio"`
	Width         int      `json:"width,omitempty"`
	Height        int      `json:"height,omitempty"`
	FPS           float64  `json:"fps,omitempty"`
	Duration      float64  `json:"duration,omitempty"`
	Engine        string   `json:"engine,omitempty"`
	Notes         []string `json:"notes,omitempty"`
	ExtendedTimes int      `json:"extended_times,omitempty"`
}

// videoEditView is an edit as the API shows it.
type videoEditView struct {
	ID        string           `json:"id"`
	Tool      string           `json:"tool"`
	State     string           `json:"state"`
	Progress  int              `json:"progress"`
	Request   videoEditRequest `json:"request"`
	Client    string           `json:"client"`
	RunID     string           `json:"run_id"`
	CreatedAt time.Time        `json:"created_at"`
	SizeBytes int64            `json:"size_bytes"`
	Result    *videoEditResult `json:"result,omitempty"`
	Error     string           `json:"error,omitempty"`
}

// videoEditToolView is a tool of GET /api/video-edits/options.
type videoEditToolView struct {
	videoedit.Tool
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// GET /api/video-edits/options
func (h *Handler) handleVideoEditOptions(w http.ResponseWriter, r *http.Request) {
	out := struct {
		Tools []videoEditToolView `json:"tools"`
	}{Tools: []videoEditToolView{}}
	for _, t := range videoedit.Tools() {
		ok, reason := h.host.VideoEdit().AvailabilityOf(t.ID)
		v := videoEditToolView{Tool: t, Available: ok}
		// what the tool has beyond ffmpeg and is not installed (the networks of scale and interpolate)
		v.Notes = append(append([]string(nil), t.Notes...), h.host.VideoEdit().EngineNotes(t.ID)...)
		if v.Settings == nil {
			v.Settings = []videoedit.Setting{} // a tool without settings lists none, not null
		}
		if !ok {
			v.Reason = reason
		}
		out.Tools = append(out.Tools, v)
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// upload is a file of a multipart request, kept on disk until the request is accepted or refused.
type upload struct {
	path string
	name string
}

func (u *upload) remove() {
	if u != nil && u.path != "" {
		_ = os.Remove(u.path)
	}
}

// readVideoEditRequest reads the multipart body of POST /api/video-edits: the `request` JSON, the `video` part
// and the `image` part. The files are saved to temporary files; the caller removes them.
func readVideoEditRequest(w http.ResponseWriter, r *http.Request, out *videoEditRequest) (video, image *upload, ok bool) {
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct != "multipart/form-data" {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "send multipart/form-data: a request part with the JSON and the video part")
		return nil, nil, false
	}
	api.RaiseBodyLimit(w, r, 2*config.MaxSourceBytes+1<<20) // the video, the picture and the JSON
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid multipart body: %v", err))
		return nil, nil, false
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()
	if raw := r.FormValue("request"); raw != "" {
		if err := json.Unmarshal([]byte(raw), out); err != nil {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON in the request part: %v", err))
			return nil, nil, false
		}
	}
	read := func(part, base string, valid func(string) bool, what string) (*upload, bool) {
		f, hdr, err := r.FormFile(part)
		if err != nil {
			return nil, true
		}
		defer f.Close()
		ext := strings.ToLower(filepath.Ext(hdr.Filename))
		if ext == ".jpe" {
			ext = ".jpg"
		}
		name := base + ext
		if !valid(name) {
			api.Refuse(w, "the %s part must be %s", part, what)
			return nil, false
		}
		tmp, err := os.CreateTemp("", "videoedit-*"+ext)
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
			return nil, false
		}
		n, err := io.Copy(tmp, io.LimitReader(f, config.MaxSourceBytes+1))
		_ = tmp.Close()
		if err != nil || n == 0 {
			_ = os.Remove(tmp.Name())
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("the %s part is empty or cannot be read", part))
			return nil, false
		}
		if n > config.MaxSourceBytes {
			_ = os.Remove(tmp.Name())
			api.WriteError(w, http.StatusRequestEntityTooLarge, api.CodeInvalid, fmt.Sprintf("the %s part is larger than %d MB", part, config.MaxSourceBytes>>20))
			return nil, false
		}
		return &upload{path: tmp.Name(), name: name}, true
	}
	var good bool
	if video, good = read("video", "origen", config.ValidClipName, "an .mp4, .mov, .webm or .mkv file"); !good {
		return nil, nil, false
	}
	if image, good = read("image", "referencia", config.ValidImageName, "a .png, .jpg, .jpeg or .webp file"); !good {
		video.remove()
		return nil, nil, false
	}
	return video, image, true
}

// checkEditAgainstVideo refuses the settings that depend on the video itself.
func checkEditAgainstVideo(tool string, settings map[string]any, info videoedit.Info) error {
	switch tool {
	case videoedit.Scale:
		factor := videoedit.Float(settings, "factor")
		if float64(info.Width)*factor > maxEditSide || float64(info.Height)*factor > maxEditSide {
			return fmt.Errorf("a %dx%d video scaled by %g would pass %d pixels in a side", info.Width, info.Height, factor, maxEditSide)
		}
	case videoedit.MotionControl:
		if start := videoedit.Float(settings, "start"); info.Duration > 0 && info.Duration < start+videoedit.MotionSeconds-0.1 {
			return fmt.Errorf("the video lasts %.1f s: motion control needs %.1f s from %g s on", info.Duration, videoedit.MotionSeconds, start)
		}
	case videoedit.Interpolate:
		if target := videoedit.Float(settings, "fps"); target <= math.Round(info.FPS) {
			return fmt.Errorf("the video has %.4g frames per second: a target of %g is not above it", info.FPS, target)
		}
	}
	return nil
}

// prepareMotionControl makes the control of a motion-control request in a temporary folder and returns it (the folder
// of the control; its parent is the one to remove). Without the vision service ready it makes nothing: the tool says
// it is not available before it gets here, and a run that finds no control makes it itself.
func (h *Handler) prepareMotionControl(ctx context.Context, video, image *upload, settings map[string]any) (string, int, error) {
	v := h.host.VideoEdit().VisionOf()
	if v == nil {
		return "", 0, nil
	}
	if ok, _ := v.Ready(ctx); !ok {
		return "", 0, nil
	}
	pw, ph, err := h.host.VideoEdit().PictureShape(ctx, image.path)
	if err != nil {
		return "", http.StatusUnprocessableEntity, err
	}
	work, err := os.MkdirTemp("", "motion-control-*")
	if err != nil {
		return "", http.StatusInternalServerError, err
	}
	control := filepath.Join(work, "control")
	_, err = videoedit.PrepareControl(ctx, v, video.path, videoedit.Float(settings, "start"), pw, ph, filepath.Join(work, "frames"), control)
	switch {
	case errors.Is(err, videoedit.ErrNoPeople):
		return control, http.StatusUnprocessableEntity, err
	case err != nil:
		return control, http.StatusServiceUnavailable, fmt.Errorf("the control of the motion could not be made: %w", err)
	}
	return control, 0, nil
}

// copyDir copies the files of a folder (no subfolders) into dst, which it creates.
func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := fsutil.CopyFile(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// planVideoEdit turns a validated request into the configuration of a project in video_edit mode.
func planVideoEdit(id string, req videoEditRequest, settings map[string]any, video, image *upload) *config.Config {
	ve := &config.VideoEdit{Tool: req.Tool, Motion: req.Motion, Settings: settings}
	if video != nil {
		ve.Source = video.name
	}
	if image != nil {
		ve.Image = image.name
	}
	return &config.Config{
		Project:   id,
		Output:    config.Output{Mode: config.ModeVideoEdit},
		Defaults:  config.Defaults{Sampler: "euler", FPS: 30, Resolution: "832x480", Steps: 20},
		VideoEdit: ve,
	}
}

// POST /api/video-edits
func (h *Handler) handleVideoEditCreate(w http.ResponseWriter, r *http.Request) {
	var req videoEditRequest
	video, image, ok := readVideoEditRequest(w, r, &req)
	if !ok {
		return
	}
	defer video.remove()
	defer image.remove()

	tool, known := videoedit.Lookup(req.Tool)
	if !known {
		api.Refuse(w, "tool %q is unknown: use one of %s", req.Tool, strings.Join(videoedit.IDs(), ", "))
		return
	}
	client, ok := requestClient(w, req.Client)
	if !ok {
		return
	}
	req.Client = &client
	settings, err := videoedit.ValidateSettings(req.Tool, req.Settings)
	if err != nil {
		api.Refuse(w, "%v", err)
		return
	}
	switch {
	case video != nil && req.Motion != "":
		api.Refuse(w, "send the video or a motion of the library, not both")
		return
	case video == nil && req.Motion == "":
		api.Refuse(w, "the video to edit is needed: send it as the video part of a multipart request")
		return
	case req.Motion != "" && !tool.AcceptsMotion:
		api.Refuse(w, "the tool %s does not take a motion of the library", tool.ID)
		return
	}
	switch {
	case tool.NeedsImage && image == nil:
		api.Refuse(w, "the tool %s needs a reference picture: send it as the image part", tool.ID)
		return
	case !tool.NeedsImage && image != nil:
		api.Refuse(w, "the tool %s does not take a reference picture", tool.ID)
		return
	}
	if req.Motion != "" {
		if ok, why := h.libraryMotionReady(req.Motion, videoedit.Float(settings, "start")); !ok {
			api.Refuse(w, "%s", why)
			return
		}
	}
	if ok, why := h.host.VideoEdit().AvailabilityOf(tool.ID); !ok {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, fmt.Sprintf("the tool %s cannot run now: %s", tool.ID, why))
		return
	}
	if tool.ID == videoedit.Extend {
		// The tool is available when some engine is; the one the request names must be too.
		if m := h.host.VideoEdit().ClipMakerOf(); m != nil {
			if _, err := videoedit.PickEngine(m.Engines(r.Context()), videoedit.Str(settings, "engine")); err != nil {
				api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, err.Error())
				return
			}
		}
	}
	if video != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		info, err := h.host.VideoEdit().Probe(ctx, video.path)
		cancel()
		if err != nil {
			api.Refuse(w, "the video part is not a video this machine can read: %v", err)
			return
		}
		if err := checkEditAgainstVideo(tool.ID, settings, info); err != nil {
			api.Refuse(w, "%v", err)
			return
		}
	}

	// Motion control moves the movement of people: it is refused now, before anything heavy, when nobody is in the
	// window. The control made for the check is kept in the project, so the run does not make it again.
	var control string
	if tool.ID == videoedit.MotionControl && video != nil {
		dir, status, err := h.prepareMotionControl(r.Context(), video, image, settings)
		if dir != "" {
			defer os.RemoveAll(filepath.Dir(dir))
			control = dir
		}
		switch {
		case errors.Is(err, videoedit.ErrNoPeople):
			api.Refuse(w, "%v", err)
			return
		case err != nil:
			api.WriteError(w, status, api.CodeUnavailable, err.Error())
			return
		}
	}

	id := newManagedID(videoPrefix)
	cfg := planVideoEdit(id, req, settings, video, image)
	cfg.Client = client
	yml, err := scenesYAML(cfg)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	body, _ := json.Marshal(req)
	info := &managedInfo{ID: id, Kind: kindVideoEdit, CreatedAt: time.Now().UTC(), Request: body}
	rec, lerr := h.ws.CreateManagedStaged(info, yml, func(sources string) error {
		for _, u := range []*upload{video, image} {
			if u == nil {
				continue
			}
			if err := fsutil.CopyFile(u.path, filepath.Join(sources, u.name)); err != nil {
				return err
			}
		}
		if control != "" {
			return copyDir(control, filepath.Join(filepath.Dir(sources), videoedit.ControlDir))
		}
		return nil
	})
	if lerr != nil {
		if lerr.Reason != "" {
			api.WriteJSON(w, lerr.Status, api.APIError{Error: api.APIErrorBody{Code: lerr.Code, Message: lerr.Msg, Reason: lerr.Reason}})
			return
		}
		api.WriteError(w, lerr.Status, lerr.Code, lerr.Msg)
		return
	}
	api.WriteJSON(w, http.StatusAccepted, h.videoEditViewOf(info, &rec))
}

// videoEditViewOf reads an edit back: its state from the run, its result from the description the tool wrote.
func (h *Handler) videoEditViewOf(info *managedInfo, run *Run) videoEditView {
	var req videoEditRequest
	_ = json.Unmarshal(info.Request, &req)
	state, msg := StateOf(run)
	if run.State == stateCancelled {
		state = "cancelled"
	}
	v := videoEditView{ID: info.ID, Tool: req.Tool, State: state, Progress: h.ProgressOf(run, 1), Request: req, Client: clientShown(req.Client), RunID: run.ID, CreatedAt: info.CreatedAt, Error: msg}
	v.SizeBytes = h.ws.ProjectSize(info.ID)
	if state != GenDone {
		return v
	}
	raw, err := os.ReadFile(filepath.Join(h.ws.ProjectDir(info.ID), "out", info.ID, pipeline.VideoEditResultFile))
	var res videoedit.Result
	if err == nil {
		err = json.Unmarshal(raw, &res)
	}
	if err != nil || res.File == "" {
		v.State, v.Error = GenFailed, "the result is not there any more"
		return v
	}
	if _, err := os.Stat(filepath.Join(h.ws.ProjectDir(info.ID), "out", info.ID, res.File)); err != nil {
		v.State, v.Error = GenFailed, "the result is not there any more"
		return v
	}
	v.Result = &videoEditResult{
		Video:  "/api/projects/" + info.ID + "/artifacts/out/" + info.ID + "/" + res.File,
		Format: res.Format, HasAudio: res.HasAudio, Width: res.Width, Height: res.Height, FPS: res.FPS,
		Duration: res.Duration, Engine: res.Engine, Notes: res.Notes, ExtendedTimes: res.ExtendedTimes,
	}
	return v
}

func (h *Handler) videoEditOf(id string) (videoEditView, bool) {
	if !strings.HasPrefix(id, videoPrefix) {
		return videoEditView{}, false
	}
	info, ok := h.ws.ReadManaged(id)
	if !ok || info.Kind != kindVideoEdit {
		return videoEditView{}, false
	}
	run, err := h.ws.RunRecord(info.RunID)
	if err != nil {
		return videoEditView{}, false
	}
	return h.videoEditViewOf(info, run), true
}

// GET /api/video-edits
func (h *Handler) handleVideoEditList(w http.ResponseWriter, r *http.Request) {
	params, clients, ok := workspace.ManagedPageParams(w, r)
	if !ok {
		return
	}
	infos, total, err := h.ws.ManagedPage(videoPrefix, clients, params)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	out := []videoEditView{}
	for _, info := range infos {
		if info.Kind != kindVideoEdit {
			continue
		}
		if v, ok := h.videoEditOf(info.ID); ok {
			out = append(out, v)
		}
	}
	store.WriteListHeaders(w, r, total, params)
	api.WriteJSON(w, http.StatusOK, out)
}

// GET /api/video-edits/{id}
func (h *Handler) handleVideoEditGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if v, ok := h.videoEditOf(id); ok && h.host.ProjectVisible(id) {
		api.WriteJSON(w, http.StatusOK, v)
		return
	}
	api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("video edit %q not found", id))
}

// DELETE /api/video-edits/{id}
func (h *Handler) handleVideoEditDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.videoEditOf(id); !ok || !h.host.ProjectVisible(id) {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("video edit %q not found", id))
		return
	}
	if lerr := h.ws.RemoveManaged(id); lerr != nil {
		api.WriteError(w, lerr.Status, lerr.Code, lerr.Msg)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

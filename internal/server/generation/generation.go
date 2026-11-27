package generation

// generation.go is the generation API (specs video-generation-api and image-edit-api): a video, or a picture
// edit, in one call. The server plans it (generation_plan.go), creates it as a project of its own (a hidden one: it has a
// reserved name and is left out of the project list, the storage and the statistics by project), launches its run
// on the normal queue and answers at once. The state and the progress come from the run; the video or the pictures
// are artifacts of the project.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"veritmo/internal/platform/store"
	"veritmo/internal/server/workspace"

	"gopkg.in/yaml.v3"

	"veritmo/internal/platform/fsutil"
	"veritmo/internal/project/config"
	"veritmo/internal/server/api"
)

// --- state and progress ------------------------------------------------------------------------

// Generation and edit states.
const (
	genQueued  = "queued"
	genRunning = "running"
	GenDone    = "done"
	GenFailed  = "failed"
)

// ProgressOf is 0 while the run waits, 100 when it is done, and in between the share of the phases of its scene
// that have ended (a skipped phase is a phase that ended). expected is the number of phases the run goes through.
func (h *Handler) ProgressOf(run *Run, expected int) int {
	switch run.State {
	case stateQueued:
		return 0
	case stateSucceeded:
		return 100
	}
	lines, err := h.ws.RunEvents(run.ID)
	if err != nil || expected <= 0 {
		return 0
	}
	ended := 0
	for _, l := range lines {
		var ev struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(l), &ev) == nil && (ev.Type == "phase_end" || ev.Type == "skipped") {
			ended++
		}
	}
	p := ended * 100 / expected
	if p > 99 {
		p = 99
	}
	return p
}

// StateOf maps the state of a run to the state of a generation and the message of a failure.
func StateOf(run *Run) (state, message string) {
	switch run.State {
	case stateQueued:
		return genQueued, ""
	case stateRunning:
		return genRunning, ""
	case stateSucceeded:
		return GenDone, ""
	case stateCancelled:
		return GenFailed, "cancelled"
	case stateInterrupted:
		return GenFailed, "interrupted: the server stopped before it ended"
	}
	msg := run.Error
	if msg == "" {
		msg = "the run failed"
	}
	return GenFailed, msg
}

// --- the request -------------------------------------------------------------------------------

// generationRequest is the body of POST /api/generations (the `request` part when there is a picture).
type generationRequest struct {
	Prompt      string  `json:"prompt,omitempty"`
	Option      string  `json:"option,omitempty"`
	Duration    float64 `json:"duration,omitempty"`
	AspectRatio string  `json:"aspect_ratio,omitempty"`
	Quality     string  `json:"quality,omitempty"`
	Audio       bool    `json:"audio,omitempty"`
	// Adult is the label the client puts on the generation (the same as `adult` in a scenes.yaml).
	Adult *bool `json:"adult,omitempty"`
	// Client is the dashboard that asks (config.ValidClient); the handler stores it as asked or as unknown.
	Client *string `json:"client,omitempty"`
	// ReuseInputsOf is the identifier of another generation whose input files (start and last picture, references)
	// this one uses without receiving them again.
	ReuseInputsOf string `json:"reuse_inputs_of,omitempty"`
	// Blender is the 3D scene of the option "blender": its description, as a hand-written one is checked. The view of a
	// generation reports `blender: {}` and lists the description among its inputs.
	Blender *blenderRequest `json:"blender,omitempty"`
}

// generationInput is a file a generation used, with the read-only address it can be fetched from.
type generationInput struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Address string `json:"address"`
}

// generationView is a generation as the API shows it.
type generationView struct {
	ID          string            `json:"id"`
	State       string            `json:"state"`
	Progress    int               `json:"progress"`
	Request     generationRequest `json:"request"`
	Client      string            `json:"client"`
	RunID       string            `json:"run_id"`
	ClipSeconds float64           `json:"clip_seconds"`
	Estimate    int               `json:"estimate,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	// SizeBytes is the disk the generation takes (its files), reported here and not in the storage per project.
	SizeBytes int64  `json:"size_bytes"`
	Video     string `json:"video,omitempty"`
	Error     string `json:"error,omitempty"`
	// Inputs are the files the generation used, in the order they were sent: the start picture first, then the
	// others. A generation made before the inputs existed reports its start picture alone.
	Inputs []generationInput `json:"inputs"`
}

// uploaded is a picture sent with a request.
type uploaded struct {
	name string
	data []byte
}

// readRequest reads a JSON body or a multipart one (`request` JSON part and `image` file part). out is decoded
// from the JSON; the picture, when there is one, is returned.
func readRequest(w http.ResponseWriter, r *http.Request, out any) (*uploaded, bool) {
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct != "multipart/form-data" {
		dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
		if err := dec.Decode(out); err != nil {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
			return nil, false
		}
		return nil, true
	}
	api.RaiseBodyLimit(w, r, config.MaxSourceBytes+1<<20) // the picture and the JSON, nothing else
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid multipart body: %v", err))
		return nil, false
	}
	if raw := r.FormValue("request"); raw != "" {
		if err := json.Unmarshal([]byte(raw), out); err != nil {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON in the request part: %v", err))
			return nil, false
		}
	}
	f, hdr, err := r.FormFile("image")
	if err != nil {
		return nil, true // no picture
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, config.MaxSourceBytes+1))
	if err != nil || len(data) == 0 {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "the image part is empty or cannot be read")
		return nil, false
	}
	if int64(len(data)) > config.MaxSourceBytes {
		api.WriteError(w, http.StatusRequestEntityTooLarge, api.CodeInvalid, fmt.Sprintf("the picture is larger than %d MB", config.MaxSourceBytes>>20))
		return nil, false
	}
	ext := strings.ToLower(filepath.Ext(hdr.Filename))
	if ext == "" {
		if exts, _ := mime.ExtensionsByType(hdr.Header.Get("Content-Type")); len(exts) > 0 {
			ext = exts[0]
		}
	}
	if ext == ".jpe" {
		ext = ".jpg"
	}
	name := "inicio" + ext
	if !config.ValidImageName(name) {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, "the picture must be a .png, .jpg, .jpeg or .webp file")
		return nil, false
	}
	return &uploaded{name: name, data: data}, true
}

// scenesYAML renders a configuration as the scenes.yaml of its project.
func scenesYAML(cfg *config.Config) ([]byte, error) {
	return yaml.Marshal(cfg)
}

// GET /api/generation/options
func (h *Handler) handleGenerationOptions(w http.ResponseWriter, r *http.Request) {
	cat := h.catalogNow()
	m := h.machineNow()
	api.WriteJSON(w, http.StatusOK, generationOptions{
		Video:        h.videoOptions(r.Context(), cat, m, h.ws.FirstAsset("voices")),
		ImageModels:  buildImageModels(cat, m),
		AspectRatios: aspectRatios,
		Qualities:    qualityNames,
	})
}

// POST /api/generations
func (h *Handler) handleGenerationCreate(w http.ResponseWriter, r *http.Request) {
	var req generationRequest
	pic, ok := readRequest(w, r, &req)
	if !ok {
		return
	}
	if r.MultipartForm != nil {
		defer func() { _ = r.MultipartForm.RemoveAll() }()
	}
	inputs, ok := readGenerationInputs(w, r)
	if !ok {
		return
	}
	defer func() { removeInputs(inputs) }()
	if req.ReuseInputsOf != "" {
		if pic != nil || len(inputs) > 0 {
			api.Refuse(w, "send the inputs or reuse them (reuse_inputs_of), not both")
			return
		}
		var msg string
		if pic, inputs, msg = h.reusedInputs(req.ReuseInputsOf); msg != "" {
			api.Refuse(w, "%s", msg)
			return
		}
	}
	// A description sent with the request wins over the one of a generation that is reused: it takes its place.
	var scene []byte
	if req.Blender != nil && len(bytes.TrimSpace(req.Blender.Description)) > 0 {
		scene = req.Blender.Description
		inputs = withoutKind(inputs, inputScene)
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" && pic == nil && len(inputs) == 0 && scene == nil && req.Option != optionBlender {
		api.Refuse(w, "a prompt or a start picture is needed")
		return
	}
	if len(req.Prompt) > 2000 {
		api.Refuse(w, "the prompt is longer than 2000 characters")
		return
	}
	if h.host.RefuseAdultConfig(w, req.Adult) {
		return
	}
	client, ok := requestClient(w, req.Client)
	if !ok {
		return
	}
	req.Client = &client
	cat := h.catalogNow()
	m := h.machineNow()
	voice := h.ws.FirstAsset("voices")
	options := h.videoOptions(r.Context(), cat, m, voice)
	if req.Option == "" {
		for _, o := range options {
			if o.Default {
				req.Option = o.ID
			}
		}
		if req.Option == "" {
			api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, "this machine cannot make a video now: no option is available")
			return
		}
	}
	var opt *videoOptionView
	for i := range options {
		if options[i].ID == req.Option {
			opt = &options[i]
		}
	}
	if opt == nil {
		api.Refuse(w, "option %q is unknown: use one of the identifiers of GET /api/generation/options", req.Option)
		return
	}
	if !opt.Available {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, fmt.Sprintf("option %s cannot run now: %s", opt.ID, opt.Reason))
		return
	}
	if req.Duration == 0 {
		req.Duration = opt.Duration.Default
	}
	if req.Duration < opt.Duration.Min || req.Duration > opt.Duration.Max {
		api.Refuse(w, "duration %g is outside what option %s accepts: from %g to %g seconds", req.Duration, opt.ID, opt.Duration.Min, opt.Duration.Max)
		return
	}
	if req.AspectRatio == "" {
		req.AspectRatio = "16:9"
	}
	if _, _, ok := parseAspect(req.AspectRatio); !ok {
		api.Refuse(w, "aspect ratio %q is not one of %s", req.AspectRatio, strings.Join(aspectRatios, ", "))
		return
	}
	if req.Quality == "" {
		req.Quality = "high"
	}
	if _, ok := qualityScale[req.Quality]; !ok {
		api.Refuse(w, "quality %q is not one of %s", req.Quality, strings.Join(qualityNames, ", "))
		return
	}
	if req.Audio && !opt.Audio {
		api.Refuse(w, "option %s cannot write audio: only MiniMax H3 and LTX 2.3 can", opt.ID)
		return
	}
	sceneName, sceneRaw, msg := h.sceneOfRequest(opt.ID, req, scene, &inputs, pic != nil)
	if msg != "" {
		api.Refuse(w, "%s", msg)
		return
	}
	if msg := h.checkGenerationInputs(r.Context(), *opt, options, pic != nil, inputs); msg != "" {
		api.Refuse(w, "%s", msg)
		return
	}
	choice, _ := findChoice(videoChoices(cat, m), opt.ID)

	id := newManagedID(genPrefix)
	vr := videoRequest{Prompt: req.Prompt, Option: opt.ID, Duration: req.Duration, AspectRatio: req.AspectRatio, Quality: req.Quality, Audio: req.Audio}
	if pic != nil {
		vr.Picture = pic.name
	}
	for _, f := range inputs {
		switch f.Kind {
		case inputEndImage:
			vr.EndPicture = f.Name
		case inputRefImage:
			vr.RefImages = append(vr.RefImages, f.Name)
		case inputRefVideo:
			vr.RefVideos = append(vr.RefVideos, f.Name)
		case inputRefAudio:
			vr.RefAudios = append(vr.RefAudios, f.Name)
		}
	}
	var plan *videoPlan
	var err error
	if sceneRaw != nil {
		sceneName = id + config.SceneSpecSuffix
		plan, err = planBlender(id, sceneName, req, voice)
	} else {
		plan, err = planVideo(id, vr, choice, cat, m, voice)
	}
	if err != nil {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, err.Error())
		return
	}
	adult := false
	if req.Adult != nil {
		adult = *req.Adult
	}
	plan.Config.Adult = &adult
	plan.Config.Client = client
	yml, err := scenesYAML(plan.Config)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	stored := req
	if stored.Blender != nil {
		stored.Blender = &blenderRequest{} // the description is an input, not part of the request that is reported
	}
	body, _ := json.Marshal(stored)
	info := &managedInfo{ID: id, Kind: kindGeneration, CreatedAt: time.Now().UTC(), Request: body, ClipSeconds: plan.ClipSeconds, Estimate: plan.Estimate}
	for _, f := range inputs {
		info.Inputs = append(info.Inputs, managedInput{Kind: f.Kind, Name: f.Name})
	}
	if sceneRaw != nil {
		info.Inputs = append(info.Inputs, managedInput{Kind: inputScene, Name: sceneName})
	}
	rec, lerr := h.ws.CreateManagedStaged(info, yml, func(sources string) error {
		if sceneRaw != nil {
			if err := os.WriteFile(filepath.Join(sources, sceneName), sceneBytes(sceneRaw), 0o644); err != nil {
				return err
			}
		}
		if pic != nil && len(pic.data) > 0 {
			if err := os.WriteFile(filepath.Join(sources, pic.name), pic.data, 0o644); err != nil {
				return err
			}
		}
		for _, f := range inputs {
			if err := fsutil.CopyFile(f.Path, filepath.Join(sources, f.Name)); err != nil {
				return err
			}
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
	view := h.generationViewOf(info, &rec)
	api.WriteJSON(w, http.StatusAccepted, view)
}

// A video scene goes through render, animate, clip and the final join.
const videoPhases = 4

func (h *Handler) generationViewOf(info *managedInfo, run *Run) generationView {
	var req generationRequest
	_ = json.Unmarshal(info.Request, &req)
	state, msg := StateOf(run)
	v := generationView{ID: info.ID, State: state, Progress: h.ProgressOf(run, videoPhases), Request: req, Client: clientShown(req.Client), RunID: run.ID,
		ClipSeconds: info.ClipSeconds, Estimate: info.Estimate, CreatedAt: info.CreatedAt, Error: msg}
	v.SizeBytes = h.ws.ProjectSize(info.ID)
	v.Inputs = h.inputsReported(info)
	if state == GenDone {
		out := "out/" + info.ID + ".mp4"
		if fi, err := os.Stat(filepath.Join(h.ws.ProjectDir(info.ID), filepath.FromSlash(out))); err == nil && fi.Mode().IsRegular() {
			v.Video = "/api/projects/" + info.ID + "/artifacts/" + out
		} else {
			v.State, v.Error = GenFailed, "the video is not there any more"
		}
	}
	return v
}

func (h *Handler) generationOf(id string) (generationView, bool) {
	if !strings.HasPrefix(id, genPrefix) {
		return generationView{}, false
	}
	info, ok := h.ws.ReadManaged(id)
	if !ok || info.Kind != kindGeneration {
		return generationView{}, false
	}
	run, err := h.ws.RunRecord(info.RunID)
	if err != nil {
		return generationView{}, false
	}
	return h.generationViewOf(info, run), true
}

// GET /api/generations
//
// A page of the records (limit, offset, sort=created_at, client=a,b): X-Total-Count and Link say where the others are. Without parameters it answers
// every generation, newest first, as it always did.
func (h *Handler) handleGenerationsList(w http.ResponseWriter, r *http.Request) {
	params, clients, ok := workspace.ManagedPageParams(w, r)
	if !ok {
		return
	}
	infos, total, err := h.ws.ManagedPage(genPrefix, clients, params)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	out := []generationView{}
	for _, info := range infos {
		if info.Kind != kindGeneration {
			continue
		}
		if v, ok := h.generationOf(info.ID); ok {
			out = append(out, v)
		}
	}
	store.WriteListHeaders(w, r, total, params)
	api.WriteJSON(w, http.StatusOK, out)
}

// GET /api/generations/{id}
func (h *Handler) handleGenerationGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if v, ok := h.generationOf(id); ok && h.host.ProjectVisible(id) {
		api.WriteJSON(w, http.StatusOK, v)
		return
	}
	api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("generation %q not found", id))
}

// PATCH /api/generations/{id}: changes the client label of the generation (and nothing else).
func (h *Handler) handleGenerationPatch(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.generationOf(id); !ok || !h.host.ProjectVisible(id) {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("generation %q not found", id))
		return
	}
	client, ok := workspace.DecodeRelabel(w, r)
	if !ok {
		return
	}
	if lerr := h.ws.RelabelManaged(id, client); lerr != nil {
		api.WriteError(w, lerr.Status, lerr.Code, lerr.Msg)
		return
	}
	v, _ := h.generationOf(id)
	api.WriteJSON(w, http.StatusOK, v)
}

// DELETE /api/generations/{id}
func (h *Handler) handleGenerationDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.generationOf(id); !ok || !h.host.ProjectVisible(id) {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("generation %q not found", id))
		return
	}
	if lerr := h.ws.RemoveManaged(id); lerr != nil {
		api.WriteError(w, lerr.Status, lerr.Code, lerr.Msg)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// startPictureOf is the name of the start picture a generation kept in its sources ("" when it had none).
func (h *Handler) startPictureOf(id string) string {
	matches, _ := filepath.Glob(filepath.Join(h.ws.SourcesDir(id), "inicio.*"))
	for _, m := range matches {
		if config.ValidImageName(filepath.Base(m)) {
			return filepath.Base(m)
		}
	}
	return ""
}

// inputsReported are the inputs of a generation as the API shows them: the start picture it kept, then the ones it
// recorded, each with the address it is served from.
func (h *Handler) inputsReported(info *managedInfo) []generationInput {
	out := []generationInput{}
	add := func(kind, name string) {
		out = append(out, generationInput{Kind: kind, Name: name, Address: "/api/projects/" + info.ID + "/artifacts/sources/" + name})
	}
	if name := h.startPictureOf(info.ID); name != "" {
		add(inputStartImage, name)
	}
	for _, in := range info.Inputs {
		add(in.Kind, in.Name)
	}
	return out
}

// reusedInputs is what reuse_inputs_of puts in a request: the start picture and the files another generation kept,
// read from its project. The files are not removed with the request.
func (h *Handler) reusedInputs(from string) (*uploaded, []genInput, string) {
	if !strings.HasPrefix(from, genPrefix) {
		return nil, nil, fmt.Sprintf("reuse_inputs_of: %q is not the identifier of a generation", from)
	}
	info, ok := h.ws.ReadManaged(from)
	if !ok || info.Kind != kindGeneration {
		return nil, nil, fmt.Sprintf("reuse_inputs_of: generation %q does not exist", from)
	}
	var pic *uploaded
	if name := h.startPictureOf(from); name != "" {
		data, err := os.ReadFile(filepath.Join(h.ws.SourcesDir(from), name))
		if err != nil {
			return nil, nil, fmt.Sprintf("reuse_inputs_of: the start picture of %s cannot be read", from)
		}
		pic = &uploaded{name: name, data: data}
	}
	var files []genInput
	for _, in := range info.Inputs {
		files = append(files, genInput{Kind: in.Kind, Name: in.Name, Path: filepath.Join(h.ws.SourcesDir(from), in.Name)})
	}
	if pic == nil && len(files) == 0 {
		return nil, nil, fmt.Sprintf("reuse_inputs_of: generation %s kept no inputs", from)
	}
	return pic, files, ""
}

// Why FinishedVideo cannot give a video.
var (
	// ErrGenerationNotFound is a generation that does not exist or that a server that does not show adult content hides.
	ErrGenerationNotFound = errors.New("the generation does not exist")
	// ErrGenerationNotDone is a generation that has not finished (it is queued, running, failed or cancelled); the error says its state.
	ErrGenerationNotDone = errors.New("the generation is not done")
	// ErrGenerationNoVideo is a generation that finished with no video file.
	ErrGenerationNoVideo = errors.New("the generation has no video")
)

// FinishedVideo is the path of the video of a finished generation, for another family that keeps a copy of it (a music piece keeps it as the
// background of a piece), and whether the generation is marked for adults. A generation a server that does not show adult content hides is
// ErrGenerationNotFound, as for the generation API itself.
func (h *Handler) FinishedVideo(id string) (path string, adult bool, err error) {
	v, ok := h.generationOf(id)
	if !ok || !h.host.ProjectVisible(id) {
		return "", false, ErrGenerationNotFound
	}
	if v.State != GenDone {
		return "", false, fmt.Errorf("%w: it is %s", ErrGenerationNotDone, v.State)
	}
	path = filepath.Join(h.ws.ProjectDir(id), "out", id+".mp4")
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return "", false, ErrGenerationNoVideo
	}
	return path, v.Request.Adult != nil && *v.Request.Adult, nil
}

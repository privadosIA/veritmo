package generation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"veritmo/internal/video/videoedit"
	"veritmo/internal/video/videovision"
)

// videoEditServer is a server whose runs go through the real pipeline with stub tools: scale writes its result at
// once, depth waits until it is cancelled, and interpolate is listed as unavailable.
func videoEditServer(t *testing.T) *testServer {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // the library of motions lives in the settings folder
	s, _ := newProjectsServer(t)
	s.Workspace.SetPipelineLookPath(fakeLook)
	t.Cleanup(s.Workspace.Shutdown)

	// every tool without code, but the stubs below
	tools := s.Stack.VideoEdit
	for _, id := range tools.IDsWithCode() {
		tools.Register(id, videoedit.Impl{})
	}
	tools.Probe = func(_ context.Context, path string) (videoedit.Info, error) {
		raw, _ := os.ReadFile(path)
		if !bytes.HasPrefix(raw, []byte("VIDEO")) {
			return videoedit.Info{}, os.ErrInvalid
		}
		return videoedit.Info{Width: 832, Height: 480, FPS: 16, Duration: 2, HasAudio: true}, nil
	}
	started := make(chan string, 4)
	videoEditStarted = started
	s.Stack.VideoEdit.Register(videoedit.Scale, videoedit.Impl{Run: func(ctx context.Context, job videoedit.Job) (videoedit.Result, error) {
		if err := os.WriteFile(filepath.Join(job.OutDir, "result.mp4"), []byte("mp4"), 0o644); err != nil {
			return videoedit.Result{}, err
		}
		return videoedit.Result{File: "result.mp4", Format: "mp4", HasAudio: true, Width: 832 * videoedit.Int(job.Settings, "factor"), Height: 480, FPS: 16, Duration: 2, Engine: videoedit.Str(job.Settings, "engine")}, nil
	}})
	s.Stack.VideoEdit.Register(videoedit.Depth, videoedit.Impl{Run: func(ctx context.Context, job videoedit.Job) (videoedit.Result, error) {
		started <- job.Video
		<-ctx.Done()
		return videoedit.Result{}, ctx.Err()
	}})
	s.Stack.VideoEdit.Register(videoedit.Interpolate, videoedit.Impl{
		Run:       func(context.Context, videoedit.Job) (videoedit.Result, error) { return videoedit.Result{}, nil },
		Available: func() (bool, string) { return false, "the interpolation network is missing" },
	})
	s.Stack.VideoEdit.Register(videoedit.MotionControl, videoedit.Impl{Run: func(context.Context, videoedit.Job) (videoedit.Result, error) { return videoedit.Result{}, nil }})
	t.Cleanup(func() {
		videoEditStarted = nil
	})
	return s
}

var videoEditStarted chan string

func postVideoEdit(t *testing.T, s *testServer, req any, video, image []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	raw, _ := json.Marshal(req)
	_ = mw.WriteField("request", string(raw))
	if video != nil {
		fw, _ := mw.CreateFormFile("video", "clip.mp4")
		_, _ = fw.Write(video)
	}
	if image != nil {
		fw, _ := mw.CreateFormFile("image", "persona.png")
		_, _ = fw.Write(image)
	}
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/api/video-edits", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	return rec
}

func decodeVideoEdit(t *testing.T, rec *httptest.ResponseRecorder) videoEditView {
	t.Helper()
	var v videoEditView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("not a video edit: %v: %s", err, rec.Body)
	}
	return v
}

func waitVideoEdit(t *testing.T, s *testServer, id, want string) videoEditView {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last videoEditView
	for time.Now().Before(deadline) {
		if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/video-edits/"+id, nil); rec.Code == http.StatusOK {
			if last = decodeVideoEdit(t, rec); last.State == want {
				return last
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("edit %s never reached %s (last %+v)", id, want, last)
	return last
}

var clip = []byte("VIDEO fake")

func TestVideoEditOptions_ListEveryToolInOrderWithItsAvailability(t *testing.T) {
	s := videoEditServer(t)
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/video-edits/options", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("options: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Tools []videoEditToolView `json:"tools"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	var ids []string
	byID := map[string]videoEditToolView{}
	for _, tool := range out.Tools {
		ids = append(ids, tool.ID)
		byID[tool.ID] = tool
	}
	if strings.Join(ids, ",") != "scale,interpolate,reframe,remove-background,depth,extend,motion-control" {
		t.Fatalf("tools = %v", ids)
	}
	if !byID["scale"].Available || byID["scale"].Reason != "" {
		t.Errorf("scale = %+v", byID["scale"])
	}
	if i := byID["interpolate"]; i.Available || i.Reason != "the interpolation network is missing" {
		t.Errorf("interpolate = %+v", i)
	}
	if r := byID["reframe"]; r.Available || r.Reason == "" {
		t.Errorf("a tool without code is listed unavailable with a reason: %+v", r)
	}
	// the networks of scale and interpolate are not installed in this repository: the options say how to get them
	for _, id := range []string{"scale", "interpolate"} {
		if n := byID[id].Notes; len(n) != 1 || !strings.Contains(n[0], "scripts/get-upscalers.sh") {
			t.Errorf("%s notes = %v", id, n)
		}
	}
	if len(byID["depth"].Notes) != 0 {
		t.Errorf("depth has nothing to install: %v", byID["depth"].Notes)
	}
	if !byID["motion-control"].NeedsImage || byID["scale"].NeedsImage {
		t.Error("only motion-control needs a picture")
	}
	if !strings.Contains(rec.Body.String(), `"settings":[]`) {
		t.Errorf("a tool without settings must list an empty array: %s", rec.Body)
	}
}

func TestVideoEditCreate_AValidEditIsAcceptedFollowedAndServed(t *testing.T) {
	s := videoEditServer(t)
	rec := postVideoEdit(t, s, map[string]any{"tool": "scale", "settings": map[string]any{"factor": 2, "engine": "fast"}, "client": "video-studio"}, clip, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	created := decodeVideoEdit(t, rec)
	if !strings.HasPrefix(created.ID, videoPrefix) || created.RunID == "" || created.Tool != "scale" || created.Client != "video-studio" {
		t.Fatalf("created = %+v", created)
	}
	done := waitVideoEdit(t, s, created.ID, GenDone)
	if done.Progress != 100 || done.Result == nil || done.Result.Format != "mp4" || !done.Result.HasAudio || done.Result.Width != 1664 || done.Result.Engine != "fast" {
		t.Fatalf("done = %+v", done)
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, done.Result.Video, nil); rec.Code != http.StatusOK || rec.Body.String() != "mp4" {
		t.Errorf("the result is not served as an artifact: %d %q", rec.Code, rec.Body)
	}
	// the source video is kept in the project, and the run carries the client
	if _, err := os.Stat(filepath.Join(s.Workspace.ProjectDir(created.ID), "sources", "origen.mp4")); err != nil {
		t.Errorf("the source was not kept: %v", err)
	}
	run, err := s.Workspace.RunRecord(created.RunID)
	if err != nil || run.Client != "video-studio" {
		t.Errorf("run client = %q (%v)", run.Client, err)
	}
	// the run's events were the pipeline's
	lines, _ := s.Workspace.RunEvents(created.RunID)
	if joined := strings.Join(lines, "\n"); !strings.Contains(joined, `"phase":"edit"`) || !strings.Contains(joined, "phase_end") {
		t.Errorf("events = %s", joined)
	}
}

func TestVideoEditCreate_WithoutAClientTheEditIsUnknown(t *testing.T) {
	s := videoEditServer(t)
	v := decodeVideoEdit(t, postVideoEdit(t, s, map[string]any{"tool": "scale"}, clip, nil))
	if v.Client != "unknown" {
		t.Errorf("client = %q", v.Client)
	}
	got := decodeVideoEdit(t, doJSON(t, s.Handler(), http.MethodGet, "/api/video-edits/"+v.ID, nil))
	if got.Client != "unknown" || got.Tool != "scale" {
		t.Errorf("view = %+v", got)
	}
}

func TestVideoEditCreate_EveryRefusalCreatesNothing(t *testing.T) {
	s := videoEditServer(t)
	for _, tc := range []struct {
		name  string
		req   any
		video []byte
		image []byte
		code  int
		want  string
	}{
		{"unknown tool", map[string]any{"tool": "teleport"}, clip, nil, 422, "is unknown"},
		{"no tool", map[string]any{}, clip, nil, 422, "is unknown"},
		{"bad client", map[string]any{"tool": "scale", "client": "Video Studio"}, clip, nil, 422, "client"},
		{"factor outside the list", map[string]any{"tool": "scale", "settings": map[string]any{"factor": 3}}, clip, nil, 422, "setting factor"},
		{"unknown setting", map[string]any{"tool": "scale", "settings": map[string]any{"speed": 1}}, clip, nil, 422, "speed"},
		{"no video", map[string]any{"tool": "scale"}, nil, nil, 422, "video to edit is needed"},
		{"not a video", map[string]any{"tool": "scale"}, []byte("just text"), nil, 422, "not a video this machine can read"},
		{"tool that cannot run", map[string]any{"tool": "interpolate", "settings": map[string]any{"fps": 30}}, clip, nil, 503, "interpolation network is missing"},
		{"tool without code", map[string]any{"tool": "reframe"}, clip, nil, 503, "cannot run now"},
		{"picture missing", map[string]any{"tool": "motion-control"}, clip, nil, 422, "needs a reference picture"},
		{"picture surplus", map[string]any{"tool": "scale"}, clip, pngBytes(4, 4), 422, "does not take a reference picture"},
		{"video and motion", map[string]any{"tool": "motion-control", "motion": "baile"}, clip, pngBytes(4, 4), 422, "not both"},
		{"motion that does not exist", map[string]any{"tool": "motion-control", "motion": "baile"}, nil, pngBytes(4, 4), 422, "does not exist"},
		{"motion for a tool without", map[string]any{"tool": "scale", "motion": "baile"}, nil, nil, 422, "does not take a motion"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := postVideoEdit(t, s, tc.req, tc.video, tc.image)
			if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("got %d %s, want %d with %q", rec.Code, rec.Body, tc.code, tc.want)
			}
		})
	}
	if entries, _ := os.ReadDir(s.ProjectsDir); len(managedOf(entries, videoPrefix)) != 0 {
		t.Errorf("a refused request left projects: %v", managedOf(entries, videoPrefix))
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/video-edits", nil); strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("list = %s", rec.Body)
	}
	if left, _ := filepath.Glob(filepath.Join(os.TempDir(), "videoedit-*")); len(left) != 0 {
		t.Errorf("temporary files left behind: %v", left)
	}
}

func managedOf(entries []os.DirEntry, prefix string) []string {
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestVideoEditCreate_LimitsThatDependOnTheVideo(t *testing.T) {
	s := videoEditServer(t)
	for _, tc := range []struct {
		name     string
		info     videoedit.Info
		req      map[string]any
		wantCode int
		want     string
	}{
		{"scale past 8192", videoedit.Info{Width: 4096, Height: 2160, FPS: 30}, map[string]any{"tool": "scale", "settings": map[string]any{"factor": 4}}, 422, "would pass 8192"},
		{"scale at the limit", videoedit.Info{Width: 2048, Height: 1080, FPS: 30}, map[string]any{"tool": "scale", "settings": map[string]any{"factor": 4}}, 202, ""},
		{"interpolate not above", videoedit.Info{Width: 10, Height: 10, FPS: 30}, map[string]any{"tool": "scale"}, 202, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s.Stack.VideoEdit.Probe = func(context.Context, string) (videoedit.Info, error) { return tc.info, nil }
			rec := postVideoEdit(t, s, tc.req, clip, nil)
			if rec.Code != tc.wantCode || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
		})
	}
	// interpolation, which is unavailable in this server's stubs, is checked on its own function
	for _, tc := range []struct {
		fps     float64
		target  float64
		wantErr bool
	}{{30, 24, true}, {30, 30, true}, {29.97, 30, true}, {16, 30, false}, {24, 60, false}} {
		err := checkEditAgainstVideo(videoedit.Interpolate, map[string]any{"fps": tc.target}, videoedit.Info{FPS: tc.fps})
		if (err != nil) != tc.wantErr {
			t.Errorf("from %g to %g: err = %v", tc.fps, tc.target, err)
		}
	}
}

func TestVideoEdits_AreNotAmongTheProjectsTheStorageOrTheStatistics(t *testing.T) {
	s := videoEditServer(t)
	createProject(t, s, "mine")
	v := decodeVideoEdit(t, postVideoEdit(t, s, map[string]any{"tool": "scale"}, clip, nil))
	waitVideoEdit(t, s, v.ID, GenDone)

	var projects []projectSummary
	_ = json.Unmarshal(doJSON(t, s.Handler(), http.MethodGet, "/api/projects", nil).Body.Bytes(), &projects)
	if len(projects) != 1 || projects[0].ID != "mine" {
		t.Errorf("projects = %+v", projects)
	}
	var storage struct {
		Projects []struct {
			ID string `json:"id"`
		} `json:"projects"`
	}
	_ = json.Unmarshal(doJSON(t, s.Handler(), http.MethodGet, "/api/storage", nil).Body.Bytes(), &storage)
	for _, p := range storage.Projects {
		if isManagedProject(p.ID) {
			t.Errorf("%s is in the storage per project", p.ID)
		}
	}
	var stats struct {
		ByProject []struct {
			Name string `json:"name"`
		} `json:"by_project"`
	}
	_ = json.Unmarshal(doJSON(t, s.Handler(), http.MethodGet, "/api/stats", nil).Body.Bytes(), &stats)
	for _, p := range stats.ByProject {
		if isManagedProject(p.Name) {
			t.Errorf("%s is in the statistics by project", p.Name)
		}
	}
	if !isManagedProject(v.ID) || runKindOf(v.ID) != kindEditRun {
		t.Errorf("%s is not a managed project of the edit kind", v.ID)
	}
	var list []videoEditView
	_ = json.Unmarshal(doJSON(t, s.Handler(), http.MethodGet, "/api/video-edits", nil).Body.Bytes(), &list)
	if len(list) != 1 || list[0].ID != v.ID {
		t.Errorf("list = %+v", list)
	}
}

func TestVideoEdits_AFailedToolSaysWhyAndOffersNoResult(t *testing.T) {
	s := videoEditServer(t)
	s.Stack.VideoEdit.Register(videoedit.Scale, videoedit.Impl{Run: func(context.Context, videoedit.Job) (videoedit.Result, error) {
		return videoedit.Result{}, os.ErrPermission
	}})
	v := decodeVideoEdit(t, postVideoEdit(t, s, map[string]any{"tool": "scale"}, clip, nil))
	failed := waitVideoEdit(t, s, v.ID, GenFailed)
	if failed.Result != nil || !strings.Contains(failed.Error, "scale failed") {
		t.Errorf("failed = %+v", failed)
	}
}

func TestVideoEdits_ARunningOneIsCancelledAndRemoved(t *testing.T) {
	s := videoEditServer(t)
	v := decodeVideoEdit(t, postVideoEdit(t, s, map[string]any{"tool": "depth"}, clip, nil))
	select {
	case <-videoEditStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("the tool never started")
	}
	if running := waitVideoEdit(t, s, v.ID, genRunning); running.Progress >= 100 {
		t.Errorf("running = %+v", running)
	}
	if rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/video-edits/"+v.ID, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/video-edits/"+v.ID, nil); rec.Code != http.StatusNotFound {
		t.Errorf("get after delete: %d", rec.Code)
	}
	if _, err := os.Stat(s.Workspace.ProjectDir(v.ID)); !os.IsNotExist(err) {
		t.Errorf("the project is still there: %v", err)
	}
	if runs, _ := s.Workspace.RunsOf(v.ID); len(runs) != 0 {
		t.Errorf("run history left: %d", len(runs))
	}
	if rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/video-edits/"+v.ID, nil); rec.Code != http.StatusNotFound {
		t.Errorf("second delete: %d", rec.Code)
	}
}

func TestVideoEdits_ACancelledRunShowsAsCancelled(t *testing.T) {
	s := videoEditServer(t)
	v := decodeVideoEdit(t, postVideoEdit(t, s, map[string]any{"tool": "depth"}, clip, nil))
	<-videoEditStarted
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/runs/"+v.RunID+"/cancel", nil); rec.Code >= 300 {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body)
	}
	got := waitVideoEdit(t, s, v.ID, "cancelled")
	if got.Result != nil {
		t.Errorf("a cancelled edit offers a result: %+v", got)
	}
}

func TestVideoEditCreate_NeedsAMultipartBody(t *testing.T) {
	s := videoEditServer(t)
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/video-edits", map[string]any{"tool": "scale"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a JSON body: %d %s", rec.Code, rec.Body)
	}
}

func TestVideoEditGet_UnknownIDIs404(t *testing.T) {
	s := videoEditServer(t)
	for _, id := range []string{"nope", "api-vid-20260101-000000-abcdef", "api-edit-20260101-000000-abcdef"} {
		if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/video-edits/"+id, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", id, rec.Code)
		}
	}
}

// The whole way with nothing replaced: a real clip goes up, the real scale makes it larger and the result is served.
func TestVideoEdits_RealScaleThroughTheServer(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	s, _ := newProjectsServer(t)
	t.Cleanup(s.Workspace.Shutdown)
	src := filepath.Join(t.TempDir(), "in.mp4")
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=16:duration=1",
		"-f", "lavfi", "-i", "sine=duration=1", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", src).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	raw, _ := os.ReadFile(src)

	var opts struct {
		Tools []videoEditToolView `json:"tools"`
	}
	_ = json.Unmarshal(doJSON(t, s.Handler(), http.MethodGet, "/api/video-edits/options", nil).Body.Bytes(), &opts)
	for _, tool := range opts.Tools {
		want := tool.ID == "scale" || tool.ID == "interpolate" || tool.ID == "reframe"
		if tool.Available != want {
			t.Errorf("%s available = %v, want %v (%s)", tool.ID, tool.Available, want, tool.Reason)
		}
	}
	rec := postVideoEdit(t, s, map[string]any{"tool": "scale", "settings": map[string]any{"factor": 2}}, raw, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	done := waitVideoEdit(t, s, decodeVideoEdit(t, rec).ID, GenDone)
	if done.Result == nil || done.Result.Width != 640 || done.Result.Height != 360 || !done.Result.HasAudio {
		t.Fatalf("done = %+v", done)
	}
	if got := doJSON(t, s.Handler(), http.MethodGet, done.Result.Video, nil); got.Code != http.StatusOK || got.Body.Len() < 1000 {
		t.Errorf("the result is not served: %d, %d bytes", got.Code, got.Body.Len())
	}
	// a source whose size would pass 8192 once scaled is refused by the real probe too
	big := filepath.Join(t.TempDir(), "big.mp4")
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "color=size=4096x2160:rate=1:duration=1", "-c:v", "libx264", "-pix_fmt", "yuv420p", big).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	bigRaw, _ := os.ReadFile(big)
	if rec := postVideoEdit(t, s, map[string]any{"tool": "scale", "settings": map[string]any{"factor": 4}}, bigRaw, nil); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "8192") {
		t.Errorf("big: %d %s", rec.Code, rec.Body)
	}
}

// fakeClips is a maker of clips that offers the engines it is given.
type fakeClips struct{ engines []videoedit.ClipEngine }

func (f fakeClips) Engines(context.Context) []videoedit.ClipEngine { return f.engines }
func (f fakeClips) MakeClip(context.Context, videoedit.ClipRequest) (videoedit.ClipResult, error) {
	return videoedit.ClipResult{}, os.ErrNotExist
}

func TestVideoEditCreate_ExtendIsRefusedOutsideItsLimitsAndForAnEngineThatCannotRun(t *testing.T) {
	s := videoEditServer(t)
	s.Stack.VideoEdit.SetClipMaker(fakeClips{engines: []videoedit.ClipEngine{
		{ID: videoedit.EngineMiniMaxH3, Available: false, Reason: "MiniMax H3 is not installed"},
		{ID: videoedit.EngineLTX, Available: true},
	}})
	s.Stack.VideoEdit.Register(videoedit.Extend, videoedit.Impl{Run: func(context.Context, videoedit.Job) (videoedit.Result, error) {
		return videoedit.Result{File: "result.mp4", Format: "mp4"}, nil
	}})
	for _, tc := range []struct {
		name     string
		settings map[string]any
		code     int
		want     string
	}{
		{"no prompt", map[string]any{"seconds": 4}, 422, "prompt is required"},
		{"blank prompt", map[string]any{"seconds": 4, "prompt": "  "}, 422, "prompt is required"},
		{"too short", map[string]any{"seconds": 1, "prompt": "p"}, 422, "below the minimum"},
		{"too long", map[string]any{"seconds": 11, "prompt": "p"}, 422, "above the maximum"},
		{"an engine that cannot run", map[string]any{"seconds": 4, "prompt": "p", "engine": "minimax-h3"}, 503, "MiniMax H3 is not installed"},
		{"an engine that takes no first frame", map[string]any{"seconds": 4, "prompt": "p", "engine": "wan"}, 422, "setting engine"},
		{"a good one", map[string]any{"seconds": 4, "prompt": "p", "engine": "ltx"}, 202, ""},
		{"the default engine", map[string]any{"seconds": 4, "prompt": "p"}, 202, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := postVideoEdit(t, s, map[string]any{"tool": "extend", "settings": tc.settings}, clip, nil)
			if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("got %d %s, want %d with %q", rec.Code, rec.Body, tc.code, tc.want)
			}
		})
	}
}

func TestNearestAspect(t *testing.T) {
	for _, tc := range []struct {
		w, h int
		want string
	}{{1920, 1080, "16:9"}, {1072, 1936, "9:16"}, {1000, 1000, "1:1"}, {1600, 1200, "4:3"}, {832, 480, "16:9"}, {1000, 1250, "3:4"}} {
		if got := nearestAspect(tc.w, tc.h); got != tc.want {
			t.Errorf("%dx%d: %s, want %s", tc.w, tc.h, got, tc.want)
		}
	}
}

func TestClipBridge_OnlyTheEnginesThatTakeAFirstFrameAreOffered(t *testing.T) {
	s, _ := newProjectsServer(t)
	t.Cleanup(s.Workspace.Shutdown)
	withFirstFrame := append(baseCatalog(), tH3FF(), tLTX())
	s.GenCatalog = func() []catalogEntry { return withFirstFrame }
	s.GenMachine = func() planMachine { return machine(16384, 32) }
	touchFiles(t, ".", withFirstFrame)
	engines := clipBridge{h: s.gHandler}.Engines(context.Background())
	if len(engines) != 2 || engines[0].ID != "minimax-h3" || engines[1].ID != "ltx" {
		t.Fatalf("engines = %+v", engines)
	}
	for _, e := range engines {
		if !e.Available {
			t.Errorf("%s should be available on a 16 GB machine with everything installed: %s", e.ID, e.Reason)
		}
	}
	// H3 installed with the reference bundle only does not take a first frame: LTX is the one left
	withoutIt := append(baseCatalog(), tLTX())
	s.GenCatalog = func() []catalogEntry { return withoutIt }
	engines = clipBridge{h: s.gHandler}.Engines(context.Background())
	if engines[0].Available || !strings.Contains(engines[0].Reason, "first-frame bundle") || !engines[1].Available {
		t.Errorf("engines = %+v", engines)
	}
	// a card too small for either
	s.GenMachine = func() planMachine { return machine(4096, 8) }
	for _, e := range (clipBridge{h: s.gHandler}).Engines(context.Background()) {
		if e.Available || e.Reason == "" {
			t.Errorf("%s on a 4 GB machine: %+v", e.ID, e)
		}
	}
}

func TestClipBridge_ThePlannedClipIsAGenerationWithTheLastFrameAsItsFirstFrame(t *testing.T) {
	s, _ := newProjectsServer(t)
	t.Cleanup(s.Workspace.Shutdown)
	cat := append(baseCatalog(), tH3FF(), tLTX())
	s.GenCatalog = func() []catalogEntry { return cat }
	s.GenMachine = func() planMachine { return machine(16384, 32) }
	touchFiles(t, ".", cat)
	picture := filepath.Join(t.TempDir(), "last.png")
	if err := os.WriteFile(picture, pngBytes(8, 14), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		engine string
		first  bool
	}{{"minimax-h3", true}, {"ltx", false}} {
		t.Run(tc.engine, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "clip")
			cfg, seconds, err := clipBridge{h: s.gHandler}.prepareClip(videoedit.ClipRequest{
				Engine: tc.engine, Prompt: "she waves", Picture: picture, Seconds: 2, Width: 480, Height: 866, Dir: dir,
			})
			if err != nil {
				t.Fatal(err)
			}
			sc := cfg.Scenes[0]
			a := cfg.Defaults.Animate
			if sc.Image != "inicio.png" || sc.MotionPrompt != "she waves" || !sc.Animate || a == nil || a.FirstFrame != tc.first {
				t.Errorf("scene %+v animate %+v", sc, a)
			}
			if seconds != 2 || sc.SilenceSeconds == nil || *sc.SilenceSeconds != 2 {
				t.Errorf("seconds %g, scene %v: the scene must last what the extension asks, up to the engine's clip", seconds, sc.SilenceSeconds)
			}
			// the picture is in sources/ and the models are linked, so the project validates and runs like any other
			for _, f := range []string{"scenes.yaml", "sources/inicio.png", "models"} {
				if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
					t.Errorf("%s: %v", f, err)
				}
			}
			// the links are absolute: the folder may sit in a project that lives on another disk
			if target, err := os.Readlink(filepath.Join(dir, "models")); err != nil || !filepath.IsAbs(target) {
				t.Errorf("models link = %q, %v", target, err)
			}
			// a portrait video gets a portrait clip
			w, h, err := parseResForTest(a.Resolution)
			if err != nil || w >= h {
				t.Errorf("resolution %s is not portrait", a.Resolution)
			}
		})
	}
	// asking for more than the engine makes gives the engine's own length
	_, seconds, err := clipBridge{h: s.gHandler}.prepareClip(videoedit.ClipRequest{Engine: "ltx", Prompt: "p", Picture: picture, Seconds: 10, Width: 1920, Height: 1080, Dir: filepath.Join(t.TempDir(), "c")})
	if err != nil || seconds >= 10 {
		t.Errorf("seconds = %g, err %v: one clip is shorter than 10 s", seconds, err)
	}
	// an engine that is not one of the two is refused
	if _, _, err := (clipBridge{h: s.gHandler}).prepareClip(videoedit.ClipRequest{Engine: "wan", Picture: picture, Dir: t.TempDir()}); err == nil {
		t.Error("an engine that takes no first frame was accepted")
	}
}

func parseResForTest(res string) (int, int, error) {
	var w, h int
	_, err := fmt.Sscanf(res, "%dx%d", &w, &h)
	return w, h, err
}

// opaquePNG is a PNG of three channels (no alpha), the control sd reads.
func opaquePNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// fakeVision stands in for the vision service: its control is a folder of black RGB frames, one per frame it is given.
type fakeVision struct {
	ready          bool
	withoutPeople  int   // frames of a control without anybody
	nobody         bool  // the mask finds nobody anywhere
	controlErr     error // the control fails with it
	block          bool  // the control waits until it is cancelled
	mu             sync.Mutex
	controlCalls   int
	controlFolders []string
}

// calls is how many controls were asked for (the control runs in the goroutine of the preparation).
func (f *fakeVision) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.controlCalls
}

func (f *fakeVision) Ready(context.Context) (bool, string) { return f.ready, "stopped" }
func (f *fakeVision) Mask(_ context.Context, frames, _ string) (videovision.Summary, error) {
	entries, err := os.ReadDir(frames)
	if err != nil {
		return videovision.Summary{}, err
	}
	without := 0
	if f.nobody {
		without = len(entries)
	}
	return videovision.Summary{Frames: len(entries), Provider: "cpu", FramesWithoutPeople: &without}, nil
}
func (f *fakeVision) Depth(context.Context, string, string) (videovision.Summary, error) {
	return videovision.Summary{}, nil
}
func (f *fakeVision) Control(ctx context.Context, frames, out string, dilate int) (videovision.Summary, error) {
	f.mu.Lock()
	f.controlCalls++
	f.controlFolders = append(f.controlFolders, frames)
	f.mu.Unlock()
	if f.block {
		<-ctx.Done()
		return videovision.Summary{}, ctx.Err()
	}
	if f.controlErr != nil {
		return videovision.Summary{}, f.controlErr
	}
	entries, err := os.ReadDir(frames)
	if err != nil {
		return videovision.Summary{}, err
	}
	var masks []videovision.FrameMask
	for _, e := range entries {
		if err := os.WriteFile(filepath.Join(out, e.Name()), opaquePNG(16, 9), 0o644); err != nil {
			return videovision.Summary{}, err
		}
		masks = append(masks, videovision.FrameMask{Name: e.Name(), Box: []int{1, 1, 8, 8}, Coverage: 0.2, People: 2})
	}
	n := f.withoutPeople
	return videovision.Summary{Frames: len(entries), Provider: "cpu", FramesWithoutPeople: &n, Masks: masks}, nil
}

func TestVideoEditCreate_MotionControlMakesItsControlBeforeAcceptingAndKeepsIt(t *testing.T) {
	s := videoEditServer(t)
	real := videoedit.NewBareCatalog().Probe // the stub of videoEditServer cannot read a real clip
	s.Stack.VideoEdit.Probe = func(ctx context.Context, path string) (videoedit.Info, error) {
		if raw, _ := os.ReadFile(path); bytes.HasPrefix(raw, []byte("\x89PNG")) {
			return videoedit.Info{Width: 480, Height: 866, FPS: 1}, nil // the picture: portrait
		}
		return real(ctx, path)
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	three := filepath.Join(t.TempDir(), "three.mp4")
	one := filepath.Join(t.TempDir(), "one.mp4")
	for path, sec := range map[string]string{three: "3", one: "1"} {
		if b, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=16:duration="+sec, "-c:v", "libx264", "-pix_fmt", "yuv420p", path).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, b)
		}
	}
	threeRaw, _ := os.ReadFile(three)
	oneRaw, _ := os.ReadFile(one)
	picture := pngBytes(480, 866)
	s.Stack.VideoEdit.Register(videoedit.MotionControl, videoedit.Impl{Run: func(_ context.Context, job videoedit.Job) (videoedit.Result, error) {
		if err := os.WriteFile(filepath.Join(job.OutDir, "result.mp4"), []byte("mp4"), 0o644); err != nil {
			return videoedit.Result{}, err
		}
		return videoedit.Result{File: "result.mp4", Format: "mp4"}, nil
	}})
	fv := &fakeVision{ready: true}
	s.Stack.VideoEdit.SetVision(fv)

	// a source with people: accepted, with the control of the window already made and kept in the project
	rec := postVideoEdit(t, s, map[string]any{"tool": "motion-control", "settings": map[string]any{"prompt": "dancing", "start": 0.5}}, threeRaw, picture)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	v := decodeVideoEdit(t, rec)
	waitVideoEdit(t, s, v.ID, GenDone)
	if fv.calls() != 1 {
		t.Errorf("the vision service was asked %d times", fv.calls())
	}
	files, _ := filepath.Glob(filepath.Join(s.Workspace.ProjectDir(v.ID), videoedit.ControlDir, "*.png"))
	if len(files) != 33 {
		t.Errorf("%d control frames kept in the project, want 33", len(files))
	}
	// the window was cut with the shape the picture asks for: portrait
	if got, _ := filepath.Glob(filepath.Join(filepath.Dir(fv.controlFolders[0]), "frames", "*.png")); len(got) != 0 {
		t.Logf("the temporary folder is removed after the request: %v", got)
	}

	// nobody in the window: refused with 422 and nothing is created
	fv.withoutPeople = 33
	if rec := postVideoEdit(t, s, map[string]any{"tool": "motion-control"}, threeRaw, picture); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "no person was found") {
		t.Errorf("nobody: %d %s", rec.Code, rec.Body)
	}
	fv.withoutPeople = 0

	// a video shorter than the window, or a start that leaves too little
	for name, tc := range map[string]struct {
		raw      []byte
		settings map[string]any
	}{
		"too short":       {oneRaw, nil},
		"start too late":  {threeRaw, map[string]any{"start": 2.5}},
		"start in limits": {threeRaw, map[string]any{"start": 0.9}},
	} {
		rec := postVideoEdit(t, s, map[string]any{"tool": "motion-control", "settings": tc.settings}, tc.raw, picture)
		want := http.StatusUnprocessableEntity
		if name == "start in limits" {
			want = http.StatusAccepted
		}
		if rec.Code != want || (want == 422 && !strings.Contains(rec.Body.String(), "needs 2.1 s")) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if entries, _ := os.ReadDir(s.ProjectsDir); len(managedOf(entries, videoPrefix)) != 2 {
		t.Errorf("the refused requests left projects: %v", managedOf(entries, videoPrefix))
	}

	// the service is not ready: nothing is prepared at the request (the run will do it or say why it cannot)
	fv.ready, fv.controlCalls = false, 0
	if rec := postVideoEdit(t, s, map[string]any{"tool": "motion-control"}, threeRaw, picture); rec.Code != http.StatusAccepted || fv.calls() != 0 {
		t.Errorf("not ready: %d, %d calls", rec.Code, fv.calls())
	}
}

package generation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"veritmo/internal/server/workspace"

	"veritmo/internal/platform/events"
	"veritmo/internal/project/config"
)

// touchFiles creates, under root, the files of the installed entries (models/ or models/video/).
func touchFiles(t *testing.T, root string, cat []catalogEntry) {
	t.Helper()
	for _, e := range cat {
		dir := filepath.Join(root, "models")
		if e.Kind == "video" {
			dir = filepath.Join(dir, "video")
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, f := range e.Files {
			if err := os.WriteFile(filepath.Join(dir, f.Name), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// genServer is a launch server whose catalog and machine are the fixtures (a 16 GB card, 32 GB of RAM) and whose
// runs finish at once, writing the video or the pictures like the pipeline.
func genServer(t *testing.T, cat []catalogEntry) *testServer {
	t.Helper()
	s, _ := newProjectsServer(t) // moves into a temp repo with models/ and voices/
	s.GenCatalog = func() []catalogEntry { return cat }
	s.GenMachine = func() planMachine { return machine(16384, 32) }
	touchFiles(t, ".", cat)
	s.Workspace.SetPipelineLookPath(fakeLook)
	s.Workspace.Execute = func(ctx context.Context, rn workspace.ExecRun, em *events.Emitter) (string, error) {
		em.PipelineStart()
		for _, ph := range []string{"render", "animate", "clip"} {
			em.PhaseStart("escena_1", ph)
			em.PhaseEnd("escena_1", ph, "")
		}
		out := "out/" + rn.Rec.Project + ".mp4"
		if err := os.MkdirAll(filepath.Join(rn.Dir, "out"), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(rn.Dir, filepath.FromSlash(out)), []byte("video"), 0o644); err != nil {
			return "", err
		}
		em.Done(out)
		return out, nil
	}
	t.Cleanup(s.Workspace.Shutdown)
	return s
}

func postGeneration(t *testing.T, s *testServer, body any) *httptest.ResponseRecorder {
	t.Helper()
	return doJSON(t, s.Handler(), http.MethodPost, "/api/generations", body)
}

func decodeGeneration(t *testing.T, rec *httptest.ResponseRecorder) generationView {
	t.Helper()
	var g generationView
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
		t.Fatalf("not a generation: %v: %s", err, rec.Body)
	}
	return g
}

func waitGeneration(t *testing.T, s *testServer, id, want string) generationView {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rec := doJSON(t, s.Handler(), http.MethodGet, "/api/generations/"+id, nil)
		if rec.Code == http.StatusOK {
			if g := decodeGeneration(t, rec); g.State == want {
				return g
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("generation %s never reached %s", id, want)
	return generationView{}
}

func multipartGeneration(t *testing.T, req any, name string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	raw, _ := json.Marshal(req)
	if err := mw.WriteField("request", string(raw)); err != nil {
		t.Fatal(err)
	}
	fw, err := mw.CreateFormFile("image", name)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write(data)
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

// --- the YAML ----------------------------------------------------------------------------------

func TestPlanVideo_EveryOptionWritesAScenesYAMLThePipelineLoads(t *testing.T) {
	cat := fullCatalog()
	m := machine(16384, 32)
	for _, picture := range []string{"", "inicio.png"} {
		for _, ch := range videoChoices(cat, m) {
			if !ch.Available {
				continue
			}
			t.Run(ch.ID+"/"+picture, func(t *testing.T) {
				root := t.TempDir()
				touchFiles(t, root, cat)
				if err := os.MkdirAll(filepath.Join(root, "voices"), 0o755); err != nil {
					t.Fatal(err)
				}
				_ = os.WriteFile(filepath.Join(root, "voices", "v.onnx"), []byte("x"), 0o644)
				_ = os.WriteFile(filepath.Join(root, "voices", "v.onnx.json"), []byte("x"), 0o644)
				if picture != "" {
					_ = os.MkdirAll(filepath.Join(root, "sources"), 0o755)
					_ = os.WriteFile(filepath.Join(root, "sources", picture), []byte("x"), 0o644)
				}
				plan, err := planVideo("api-gen-x", videoRequest{Prompt: "a cat on a roof", Option: ch.ID, Duration: 8, AspectRatio: "16:9", Quality: "normal", Picture: picture}, ch, cat, m, "voices/v.onnx")
				if err != nil {
					t.Fatal(err)
				}
				yml, err := scenesYAML(plan.Config)
				if err != nil {
					t.Fatal(err)
				}
				cfg, err := config.LoadBytes(yml)
				if err != nil {
					t.Fatalf("does not load: %v\n%s", err, yml)
				}
				if err := cfg.ValidateInDir(root); err != nil {
					t.Fatalf("does not validate: %v\n%s", err, yml)
				}
				if len(cfg.Scenes) != 1 || !cfg.Scenes[0].Animate || cfg.Scenes[0].SilenceSeconds == nil || *cfg.Scenes[0].SilenceSeconds != 8 {
					t.Errorf("scene = %+v, want one animated scene of 8 seconds", cfg.Scenes)
				}
				if cfg.Scenes[0].Image != picture {
					t.Errorf("the picture is %q, want %q", cfg.Scenes[0].Image, picture)
				}
				if plan.ClipSeconds <= 0 || plan.Estimate <= 0 {
					t.Errorf("clip_seconds %v, estimate %d", plan.ClipSeconds, plan.Estimate)
				}
			})
		}
	}
}

func TestPlanVideo_TheAspectRatioAndTheQualityShapeTheClip(t *testing.T) {
	cat := fullCatalog()
	m := machine(16384, 32)
	ch, _ := findChoice(videoChoices(cat, m), "ltx")
	res := func(aspect, quality string) (int, int) {
		p, err := planVideo("x", videoRequest{Prompt: "p", Duration: 5, AspectRatio: aspect, Quality: quality}, ch, cat, m, "")
		if err != nil {
			t.Fatal(err)
		}
		w, h, _ := parseRes(p.Config.Defaults.Animate.Resolution)
		return w, h
	}
	if w, h := res("16:9", "high"); w <= h || w%32 != 0 || h%32 != 0 {
		t.Errorf("16:9 high = %dx%d", w, h)
	}
	if w, h := res("9:16", "high"); w >= h {
		t.Errorf("9:16 high = %dx%d, want portrait", w, h)
	}
	wh, hh := res("16:9", "high")
	wd, hd := res("16:9", "draft")
	if wd*hd >= wh*hh {
		t.Errorf("draft %dx%d is not smaller than high %dx%d", wd, hd, wh, hh)
	}
}

func TestPlanVideo_AudioWritesTheModelAudioOnlyWhenAsked(t *testing.T) {
	cat := fullCatalog()
	m := machine(16384, 32)
	ch, _ := findChoice(videoChoices(cat, m), "ltx")
	for _, audio := range []bool{false, true} {
		p, _ := planVideo("x", videoRequest{Prompt: "p", Duration: 5, AspectRatio: "16:9", Quality: "high", Audio: audio}, ch, cat, m, "")
		if p.Config.Defaults.Animate.ModelAudio != audio {
			t.Errorf("audio %v: model_audio = %v", audio, p.Config.Defaults.Animate.ModelAudio)
		}
	}
}

func TestPlanVideo_TheCostDoesNotFollowTheDuration(t *testing.T) {
	cat := fullCatalog()
	m := machine(16384, 32)
	ch, _ := findChoice(videoChoices(cat, m), "wan-medium")
	a, _ := planVideo("x", videoRequest{Prompt: "p", Duration: 5, AspectRatio: "16:9", Quality: "high"}, ch, cat, m, "")
	b, _ := planVideo("x", videoRequest{Prompt: "p", Duration: 20, AspectRatio: "16:9", Quality: "high"}, ch, cat, m, "")
	if a.Estimate != b.Estimate || a.ClipSeconds != b.ClipSeconds {
		t.Errorf("5 s: %d s / clip %v, 20 s: %d s / clip %v", a.Estimate, a.ClipSeconds, b.Estimate, b.ClipSeconds)
	}
	if got := *b.Config.Scenes[0].SilenceSeconds; got != 20 {
		t.Errorf("the scene lasts %v, want 20", got)
	}
}

func TestPlanVideo_ARequestWithNoPictureNeedsAnImageModel(t *testing.T) {
	cat := []catalogEntry{tWan("wan5q4", "low", 85, 20, 6144, false)}
	ch := videoChoice{ID: "wan-low", Engine: "wan", Tier: "low", Entry: &cat[0], Available: true}
	if _, err := planVideo("x", videoRequest{Prompt: "p", Duration: 5, AspectRatio: "16:9", Quality: "high"}, ch, cat, machine(16384, 32), ""); err == nil {
		t.Error("a video with no picture and no image model was planned")
	}
	if _, err := planVideo("x", videoRequest{Duration: 5, AspectRatio: "16:9", Quality: "high", Picture: "inicio.png"}, ch, cat, machine(16384, 32), ""); err != nil {
		t.Errorf("a picture needs no image model: %v", err)
	}
}

// --- the options -------------------------------------------------------------------------------

// --- a video in one call -----------------------------------------------------------------------

func TestGenerations_AValidRequestIsAcceptedAndRuns(t *testing.T) {
	s := genServer(t, fullCatalog())
	rec := postGeneration(t, s, map[string]any{"prompt": "a cat on a roof", "option": "ltx", "duration": 8, "aspect_ratio": "16:9", "quality": "normal"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	g := decodeGeneration(t, rec)
	if !strings.HasPrefix(g.ID, genPrefix) || g.RunID == "" || g.ClipSeconds <= 0 || g.Request.Option != "ltx" || g.Request.Duration != 8 {
		t.Errorf("answer = %+v", g)
	}
	done := waitGeneration(t, s, g.ID, GenDone)
	if done.Progress != 100 || done.Video == "" {
		t.Errorf("done = %+v", done)
	}
	// The video is served read-only as an artifact.
	if rec := doJSON(t, s.Handler(), http.MethodGet, done.Video, nil); rec.Code != http.StatusOK || rec.Body.String() != "video" {
		t.Errorf("video: %d %q", rec.Code, rec.Body)
	}
}

func TestGenerations_TheDefaultsAreFilledIn(t *testing.T) {
	s := genServer(t, fullCatalog())
	rec := postGeneration(t, s, map[string]any{"prompt": "a dog"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	g := decodeGeneration(t, rec)
	if g.Request.Option != "h3-full" || g.Request.Duration != 5 || g.Request.AspectRatio != "16:9" || g.Request.Quality != "high" {
		t.Errorf("request = %+v", g.Request)
	}
}

func TestGenerations_AStartPictureIsStoredAndPromptIsOptional(t *testing.T) {
	s := genServer(t, fullCatalog())
	body, ct := multipartGeneration(t, map[string]any{"option": "ltx"}, "photo.PNG", []byte("png-bytes"))
	req := httptest.NewRequest(http.MethodPost, "/api/generations", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	g := decodeGeneration(t, rec)
	data, err := os.ReadFile(filepath.Join(s.Workspace.ProjectDir(g.ID), "sources", "inicio.png"))
	if err != nil || string(data) != "png-bytes" {
		t.Fatalf("the picture was not stored: %v %q", err, data)
	}
	cfg, err := config.Load(s.Workspace.ProjectConfigPath(g.ID))
	if err != nil || cfg.Scenes[0].Image != "inicio.png" {
		t.Errorf("scene image = %+v (%v)", cfg, err)
	}
	waitGeneration(t, s, g.ID, GenDone)
}

func TestGenerations_ARefusedRequestCreatesNothing(t *testing.T) {
	s := genServer(t, baseCatalog()) // no LTX, no AnimateDiff
	cases := []struct {
		name string
		body map[string]any
		want int
		text string
	}{
		{"no prompt and no picture", map[string]any{"option": "wan-low"}, 422, "prompt or a start picture"},
		{"unknown option", map[string]any{"prompt": "p", "option": "nope"}, 422, "unknown"},
		{"option that cannot run", map[string]any{"prompt": "p", "option": "ltx"}, 503, "not installed"},
		{"duration too short", map[string]any{"prompt": "p", "option": "wan-low", "duration": 0.5}, 422, "duration"},
		{"duration too long", map[string]any{"prompt": "p", "option": "wan-low", "duration": 300}, 422, "duration"},
		{"aspect ratio", map[string]any{"prompt": "p", "option": "wan-low", "aspect_ratio": "7:3"}, 422, "aspect ratio"},
		{"quality", map[string]any{"prompt": "p", "option": "wan-low", "quality": "ultra"}, 422, "quality"},
		{"audio on an option that cannot", map[string]any{"prompt": "p", "option": "wan-low", "audio": true}, 422, "audio"},
		{"adult content on a censored server", map[string]any{"prompt": "p", "option": "wan-low", "adult": true}, 422, "adults"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postGeneration(t, s, tc.body)
			if rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.text) {
				t.Fatalf("status %d, want %d with %q: %s", rec.Code, tc.want, tc.text, rec.Body)
			}
		})
	}
	entries, _ := os.ReadDir(s.ProjectsDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), genPrefix) {
			t.Errorf("a refused request left %s behind", e.Name())
		}
	}
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/generations", nil)
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("list = %s", rec.Body)
	}
}

func TestGenerations_ARequestOutsideTheJSONIsRefused(t *testing.T) {
	s := genServer(t, fullCatalog())
	req := httptest.NewRequest(http.MethodPost, "/api/generations", strings.NewReader("{nope"))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d", rec.Code)
	}
	body, ct := multipartGeneration(t, map[string]any{"option": "ltx"}, "notes.txt", []byte("hi"))
	req = httptest.NewRequest(http.MethodPost, "/api/generations", body)
	req.Header.Set("Content-Type", ct)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a .txt as the picture: status %d: %s", rec.Code, rec.Body)
	}
}

// --- following, listing, removing --------------------------------------------------------------

func TestGenerations_FollowedThroughTheRun(t *testing.T) {
	s := genServer(t, fullCatalog())
	release := make(chan struct{})
	s.Workspace.Execute = func(ctx context.Context, rn workspace.ExecRun, em *events.Emitter) (string, error) {
		em.PipelineStart()
		em.PhaseStart("escena_1", "render")
		em.PhaseEnd("escena_1", "render", "tmp/a.png")
		em.PhaseStart("escena_1", "animate")
		em.PhaseEnd("escena_1", "animate", "tmp/a.mp4")
		select {
		case <-release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		return "", fmt.Errorf("the model ran out of memory")
	}
	g := decodeGeneration(t, postGeneration(t, s, map[string]any{"prompt": "p", "option": "ltx"}))
	running := waitGeneration(t, s, g.ID, genRunning)
	deadline := time.Now().Add(2 * time.Second)
	for running.Progress != 50 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		running = waitGeneration(t, s, g.ID, genRunning)
	}
	if running.Progress != 50 || running.RunID != g.RunID {
		t.Errorf("running = %+v, want 50%% of the run %s", running, g.RunID)
	}
	// The same events are the run's own stream.
	if lines, _ := s.Workspace.RunEvents(g.RunID); len(lines) < 5 {
		t.Errorf("the run has %d events", len(lines))
	}
	close(release)
	failed := waitGeneration(t, s, g.ID, GenFailed)
	if !strings.Contains(failed.Error, "ran out of memory") || failed.Video != "" {
		t.Errorf("failed = %+v", failed)
	}
}

func TestGenerations_TheListIsNewestFirstAndNotAmongTheProjects(t *testing.T) {
	s := genServer(t, fullCatalog())
	createProject(t, s, "mine")
	first := decodeGeneration(t, postGeneration(t, s, map[string]any{"prompt": "one", "option": "ltx"}))
	time.Sleep(1100 * time.Millisecond) // the ids and creation times have a resolution of a second
	second := decodeGeneration(t, postGeneration(t, s, map[string]any{"prompt": "two", "option": "ltx"}))
	waitGeneration(t, s, first.ID, GenDone)
	waitGeneration(t, s, second.ID, GenDone)

	var list []generationView
	_ = json.Unmarshal(doJSON(t, s.Handler(), http.MethodGet, "/api/generations", nil).Body.Bytes(), &list)
	if len(list) != 2 || list[0].ID != second.ID || list[1].ID != first.ID {
		t.Fatalf("list = %+v, want the second before the first", list)
	}
	var projects []projectSummary
	_ = json.Unmarshal(doJSON(t, s.Handler(), http.MethodGet, "/api/projects", nil).Body.Bytes(), &projects)
	for _, p := range projects {
		if isManagedProject(p.ID) {
			t.Errorf("%s is among the projects", p.ID)
		}
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
}

func TestGenerations_ADeletedOneIsGoneWithItsFilesAndHistory(t *testing.T) {
	s := genServer(t, fullCatalog())
	g := decodeGeneration(t, postGeneration(t, s, map[string]any{"prompt": "p", "option": "ltx"}))
	done := waitGeneration(t, s, g.ID, GenDone)
	if rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/generations/"+g.ID, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/generations/"+g.ID, nil); rec.Code != http.StatusNotFound {
		t.Errorf("get after delete: %d", rec.Code)
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, done.Video, nil); rec.Code == http.StatusOK {
		t.Errorf("the video is still served")
	}
	if runs, _ := s.Workspace.RunsOf(g.ID); len(runs) != 0 {
		t.Errorf("the run history is still there: %+v", runs)
	}
	if rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/generations/"+g.ID, nil); rec.Code != http.StatusNotFound {
		t.Errorf("a second delete: %d", rec.Code)
	}
}

func TestGenerations_ARunningOneIsCancelledAndRemoved(t *testing.T) {
	s := genServer(t, fullCatalog())
	started := make(chan struct{}, 1)
	s.Workspace.Execute = func(ctx context.Context, rn workspace.ExecRun, em *events.Emitter) (string, error) {
		em.PipelineStart()
		started <- struct{}{}
		<-ctx.Done()
		return "", ctx.Err()
	}
	g := decodeGeneration(t, postGeneration(t, s, map[string]any{"prompt": "p", "option": "ltx"}))
	<-started
	if rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/generations/"+g.ID, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete a running one: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(s.Workspace.ProjectDir(g.ID)); !os.IsNotExist(err) {
		t.Errorf("the project is still on disk: %v", err)
	}
	if runs, _ := s.Workspace.RunsOf(g.ID); len(runs) != 0 {
		t.Errorf("the run history is still there: %+v", runs)
	}
	var list []generationView
	_ = json.Unmarshal(doJSON(t, s.Handler(), http.MethodGet, "/api/generations", nil).Body.Bytes(), &list)
	if len(list) != 0 {
		t.Errorf("list = %+v", list)
	}
}

func TestGenerations_TheReservedNamesCannotBeUsedForAProject(t *testing.T) {
	s := genServer(t, fullCatalog())
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects", map[string]string{"id": "api-gen-mine"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "reserved") {
		t.Errorf("status %d: %s", rec.Code, rec.Body)
	}
}

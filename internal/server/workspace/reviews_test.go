package workspace

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/project/quality"
)

const reviewYAML = `# my project
project: demo
output: {mode: video, file: out/demo.mp4}
defaults:
  model: models/m.safetensors
  voice: voices/v.onnx
  animate: {diffusion_model: models/wan.gguf, t5xxl: models/umt5.gguf, vae: models/wan_vae.safetensors}
scenes:
  - id: bad          # drifts in colour at 1.5 s
    prompt: a bench
    silence_seconds: 2
    animate: true
    seed: 7
    motion_prompt: they wave wildly
  - id: good
    prompt: a park
    silence_seconds: 2
    animate: true
  - id: talk
    prompt: a talker
    tts_text: Hello
    animate: true
`

// reviewServer is a project "demo" with animated scenes whose clips exist:
// "bad" shifts colour at 1.5 s, "good" is steady, "talk" (narrated) shifts too.
func reviewServer(t *testing.T) (*testServer, *fakeExec, string) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	s, fe := launchServer(t)
	for _, f := range []string{"wan.gguf", "umt5.gguf", "wan_vae.safetensors"} {
		os.WriteFile(filepath.Join("models", f), []byte("x"), 0o644)
	}
	dir := s.ProjectDir("demo")
	if err := s.SaveProjectDefinition("demo", []byte(reviewYAML), "save"); err != nil {
		t.Fatal(err)
	}
	if err := s.MaterializeProject("demo"); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(dir, "tmp"), 0o755)
	mk := func(name, graph string) {
		out, err := exec.Command("ffmpeg", "-y", "-v", "error", "-filter_complex", graph, "-map", "[v]", "-pix_fmt", "yuv420p", filepath.Join(dir, "tmp", name)).CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	}
	drift := "color=c=0x808080:s=320x180:d=1.5:r=16[a];color=c=0x2040ff:s=320x180:d=1.5:r=16[b];[a][b]concat=n=2:v=1:a=0[v]"
	mk("scene_bad.anim.avi", drift)
	mk("scene_talk.anim.avi", drift)
	mk("scene_good.anim.avi", "testsrc=s=320x180:d=3:r=16[v]")
	return s, fe, dir
}

type taskStarted struct {
	FindingID string `json:"finding_id"`
	RunID     string `json:"run_id"`
}

type taskSkipped struct {
	FindingID string `json:"finding_id"`
	Reason    string `json:"reason"`
}

type reviewBody struct {
	Review   Review        `json:"review"`
	Findings []FindingJSON `json:"findings"`
}

func getReview(t *testing.T, s *testServer, id string) reviewBody {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/reviews/"+id, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get review: %d %s", rec.Code, rec.Body)
	}
	var b reviewBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	return b
}

// analyzeDemo starts an analysis and waits for it to be ready.
func analyzeDemo(t *testing.T, s *testServer, body map[string]any) reviewBody {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/demo/reviews", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	var rev Review
	json.Unmarshal(rec.Body.Bytes(), &rev)
	if rev.State != reviewAnalyzing || rev.Total != 3 {
		t.Fatalf("started review = %+v", rev)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		b := getReview(t, s, rev.ID)
		if b.Review.State != reviewAnalyzing {
			return b
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the analysis never finished")
	return reviewBody{}
}

func findingOf(b reviewBody, scene string) *FindingJSON {
	for i := range b.Findings {
		if b.Findings[i].Scene == scene {
			return &b.Findings[i]
		}
	}
	return nil
}

func TestReview_AnalyzeRecordsWhatIsWrongWithItsFixAndEvidence(t *testing.T) {
	s, _, dir := reviewServer(t)
	b := analyzeDemo(t, s, map[string]any{})
	if b.Review.State != reviewReady || b.Review.Done != 3 || !strings.Contains(b.Review.Notes, "no vision model") {
		t.Fatalf("review = %+v", b.Review)
	}
	if len(b.Findings) != 2 || findingOf(b, "good") != nil {
		t.Fatalf("a steady clip has no finding: %+v", b.Findings)
	}
	bad := findingOf(b, "bad")
	if bad.Kind != "color_drift" || bad.Source != sourceAuto || bad.Status != findingPending || !bad.HasFrame || bad.FromSeconds != 1.5 {
		t.Fatalf("bad = %+v", bad)
	}
	if bad.Fix.Kind != fixTrim || bad.Fix.Params["seconds"] != 1.2 {
		t.Errorf("a silent scene is cut to its clean part: %+v", bad.Fix)
	}
	// the narrated scene cannot be cut, so it is made again with another seed
	talk := findingOf(b, "talk")
	if talk.Fix.Kind != fixReseed || talk.Fix.Params["seed"] != 1000.0 {
		t.Errorf("talk fix = %+v", talk.Fix)
	}
	// the evidence is a PNG that is served and lives in the project
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/reviews/"+b.Review.ID+"/findings/"+bad.ID+"/frame", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || !strings.HasPrefix(rec.Body.String(), "\x89PNG") {
		t.Fatalf("frame: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	if _, err := os.Stat(filepath.Join(dir, reviewsDir, b.Review.ID, bad.ID+".png")); err != nil {
		t.Errorf("the evidence must be stored in the project: %v", err)
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/reviews/"+b.Review.ID+"/findings/"+findingOf(b, "talk").ID+"/frame", nil); rec.Code != http.StatusOK {
		t.Errorf("every automatic finding has its frame: %d", rec.Code)
	}
	// the review shows in the project's list with its counts
	var list []struct {
		ID       string `json:"id"`
		Findings int    `json:"findings"`
		Pending  int    `json:"pending"`
	}
	json.Unmarshal(doJSON(t, s.Handler(), http.MethodGet, "/api/projects/demo/reviews", nil).Body.Bytes(), &list)
	if len(list) != 1 || list[0].Findings != 2 || list[0].Pending != 2 {
		t.Errorf("list = %+v", list)
	}
}

type scriptedVision struct {
	calls    atomic.Int32
	unloaded atomic.Bool
}

func (v *scriptedVision) Judge(context.Context, []byte) (quality.Judgement, error) {
	v.calls.Add(1)
	return quality.Judgement{Defect: true, Severity: 9, Issue: "fused arms"}, nil
}
func (v *scriptedVision) Unload(context.Context) { v.unloaded.Store(true) }

func TestReview_AVisionModelJudgesTheFramesAndIsUnloaded(t *testing.T) {
	s, _, _ := reviewServer(t)
	v := &scriptedVision{}
	var named string
	s.Overrides.ReviewVision = func(model string) quality.Vision {
		named = model
		return v
	}
	b := analyzeDemo(t, s, map[string]any{"model": "qwen-vl"})
	if named != "qwen-vl" || v.calls.Load() == 0 || !v.unloaded.Load() {
		t.Fatalf("model %q, %d calls, unloaded %v", named, v.calls.Load(), v.unloaded.Load())
	}
	if b.Review.Model != "qwen-vl" || b.Review.Notes != "" {
		t.Errorf("review = %+v", b.Review)
	}
	// every scene is bad from the start for this judge, so even the steady one has a finding
	good := findingOf(b, "good")
	if good == nil || good.Kind != "deformation" || good.Severity != 9 || !strings.Contains(good.Description, "fused arms") || good.Fix.Kind != fixReseed {
		t.Fatalf("good = %+v", good)
	}
}

func TestReview_NothingToAnalyzeAndBusyProjects(t *testing.T) {
	s, _, dir := reviewServer(t)
	s.busy = func(string) (string, bool) { return "run-1", true }
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/demo/reviews", map[string]any{}); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "run-1") {
		t.Errorf("busy: %d %s", rec.Code, rec.Body)
	}
	s.busy = nil
	os.RemoveAll(filepath.Join(dir, "tmp"))
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/demo/reviews", map[string]any{}); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "nothing to analyze") {
		t.Errorf("no clips: %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/nope/reviews", map[string]any{}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown project: %d", rec.Code)
	}
}

func TestReview_TheUsersOwnFindingsAreValidatedAndEdited(t *testing.T) {
	s, _, _ := reviewServer(t)
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/demo/reviews", map[string]any{"analyze": false})
	if rec.Code != http.StatusCreated {
		t.Fatalf("empty review: %d %s", rec.Code, rec.Body)
	}
	var rev Review
	json.Unmarshal(rec.Body.Bytes(), &rev)
	if rev.State != reviewReady {
		t.Fatalf("review = %+v", rev)
	}
	post := func(body map[string]any) *httptest.ResponseRecorder {
		return doJSON(t, s.Handler(), http.MethodPost, "/api/reviews/"+rev.ID+"/findings", body)
	}
	for name, body := range map[string]map[string]any{
		"no description":         {"scene": "bad"},
		"unknown scene":          {"scene": "nope", "description": "x"},
		"bad severity":           {"scene": "bad", "description": "x", "severity": 11},
		"unknown fix":            {"scene": "bad", "description": "x", "fix": map[string]any{"kind": "magic"}},
		"fix without parameters": {"scene": "bad", "description": "x", "fix": map[string]any{"kind": "trim"}},
	} {
		if rec := post(body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	rec = post(map[string]any{"scene": "bad", "description": "faces melt from 1.3 s", "from_seconds": 1.3, "severity": 8})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var f FindingJSON
	json.Unmarshal(rec.Body.Bytes(), &f)
	if f.Source != sourceManual || f.Kind != "observation" || f.Fix.Kind != fixReseed || f.Fix.Params["seed"] != 1007.0 || f.Status != findingPending || f.HasFrame {
		t.Fatalf("finding = %+v", f)
	}
	patch := func(body map[string]any) *httptest.ResponseRecorder {
		return doJSON(t, s.Handler(), http.MethodPatch, "/api/reviews/"+rev.ID+"/findings/"+f.ID, body)
	}
	if rec := patch(map[string]any{"fix": map[string]any{"kind": "motion_prompt", "params": map[string]any{"motion_prompt": "they sway slowly, static camera"}}}); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "sway slowly") {
		t.Errorf("change the fix: %d %s", rec.Code, rec.Body)
	}
	if rec := patch(map[string]any{"skipped": true}); !strings.Contains(rec.Body.String(), `"status":"skipped"`) {
		t.Errorf("skip: %s", rec.Body)
	}
	if rec := patch(map[string]any{"skipped": false}); !strings.Contains(rec.Body.String(), `"status":"pending"`) {
		t.Errorf("unskip: %s", rec.Body)
	}
	if rec := patch(map[string]any{"description": "  "}); rec.Code != http.StatusBadRequest {
		t.Errorf("empty description: %d", rec.Code)
	}
}

func TestReview_TasksEditScenesYAMLAndRunThroughTheQueue(t *testing.T) {
	s, fe, dir := reviewServer(t)
	b := analyzeDemo(t, s, map[string]any{})
	bad, talk := findingOf(b, "bad"), findingOf(b, "talk")

	// a task for one finding: the YAML is edited (comments kept, backup made) and a run limited to the scene is queued
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/reviews/"+b.Review.ID+"/tasks", map[string]any{"finding_ids": []string{bad.ID}, "gpu": "1"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("tasks: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Started []taskStarted `json:"started"`
		Skipped []taskSkipped `json:"skipped"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Started) != 1 || out.Started[0].FindingID != bad.ID {
		t.Fatalf("started = %+v", out)
	}
	yaml, _ := s.ProjectDefinition("demo")
	if !strings.Contains(string(yaml), "silence_seconds: 1.2") || !strings.Contains(string(yaml), "# drifts in colour at 1.5 s") || !strings.Contains(string(yaml), "# my project") {
		t.Fatalf("scenes.yaml:\n%s", yaml)
	}
	// the backup of the original is a version of the definition (the .bak the fix writes is removed)
	if versions, _ := s.Projects().Versions("demo"); len(versions) == 0 {
		t.Error("a version of the original is kept")
	} else if v, _ := s.Projects().Version("demo", versions[0].Version); string(v.Definition) != reviewYAML || v.Reason != "fix" && v.Reason != "save" {
		t.Errorf("the newest version is not the original (%s): %q", v.Reason, v.Definition)
	}
	if _, err := os.Stat(filepath.Join(dir, "scenes.yaml.bak")); err == nil {
		t.Error("the scenes.yaml.bak of the fix is removed")
	}
	runID := out.Started[0].RunID
	run, _ := s.rm.hist.getRun(runID)
	if run.Only != "bad" || run.GPU != "1" || !run.KeepTmp {
		t.Errorf("the run is limited to the scene, on the chosen GPU, keeping tmp: %+v", run)
	}
	// while it runs the finding says so, and cannot be changed or deleted
	waitStart(t, fe)
	if st := findingOf(getReview(t, s, b.Review.ID), "bad").Status; st != findingRunning {
		t.Errorf("status = %s, want running", st)
	}
	if rec := doJSON(t, s.Handler(), http.MethodPatch, "/api/reviews/"+b.Review.ID+"/findings/"+bad.ID, map[string]any{"skipped": true}); rec.Code != http.StatusConflict {
		t.Errorf("patching a running task: %d", rec.Code)
	}
	if rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/reviews/"+b.Review.ID, nil); rec.Code != http.StatusConflict {
		t.Errorf("deleting a review with a running task: %d", rec.Code)
	}
	fe.chanFor(runID) <- true
	waitState(t, s, runID, StateSucceeded)
	got := findingOf(getReview(t, s, b.Review.ID), "bad")
	if got.Status != findingDone || got.RunID != runID {
		t.Errorf("done = %+v", got)
	}

	// the narrated scene cannot be cut: a fix that cannot apply fails with its reason, and nothing runs
	if rec := doJSON(t, s.Handler(), http.MethodPatch, "/api/reviews/"+b.Review.ID+"/findings/"+talk.ID, map[string]any{"fix": map[string]any{"kind": "trim", "params": map[string]any{"seconds": 1.0}}}); rec.Code != http.StatusOK {
		t.Fatalf("patch: %d", rec.Code)
	}
	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/reviews/"+b.Review.ID+"/tasks", map[string]any{})
	json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Started) != 0 || len(out.Skipped) != 1 || !strings.Contains(out.Skipped[0].Reason, "narration") {
		t.Fatalf("a narrated scene cannot be cut: %+v", out)
	}
	if st := findingOf(getReview(t, s, b.Review.ID), "talk"); st.Status != findingFailed || !strings.Contains(st.Message, "narration") {
		t.Errorf("talk = %+v", st)
	}
	// a busy project refuses new tasks
	s.busy = func(string) (string, bool) { return "run-9", true }
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/reviews/"+b.Review.ID+"/tasks", map[string]any{}); rec.Code != http.StatusConflict {
		t.Errorf("busy project: %d", rec.Code)
	}
}

func TestReview_AFailedRunMarksItsTaskFailed(t *testing.T) {
	s, fe, _ := reviewServer(t)
	b := analyzeDemo(t, s, map[string]any{})
	bad := findingOf(b, "bad")
	var out struct {
		Started []taskStarted `json:"started"`
	}
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/reviews/"+b.Review.ID+"/tasks", map[string]any{"finding_ids": []string{bad.ID}})
	json.Unmarshal(rec.Body.Bytes(), &out)
	waitStart(t, fe)
	fe.chanFor(out.Started[0].RunID) <- false
	waitState(t, s, out.Started[0].RunID, StateFailed)
	if got := findingOf(getReview(t, s, b.Review.ID), "bad"); got.Status != findingFailed || !strings.Contains(got.Message, "failed") {
		t.Errorf("failed task = %+v", got)
	}
}

func TestReview_SuggestAGentlerMotionNeedsTheAssistantAndNoActiveRun(t *testing.T) {
	s, _, _ := reviewServer(t)
	b := analyzeDemo(t, s, map[string]any{})
	bad := findingOf(b, "bad")
	var asked string
	s.assistRun = func(_ context.Context, task assist.Task, text, _ string) (string, error) {
		asked = string(task) + ": " + text
		return "they sway slowly, static camera", nil
	}
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/reviews/"+b.Review.ID+"/findings/"+bad.ID+"/suggest", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "sway slowly") {
		t.Fatalf("suggest: %d %s", rec.Code, rec.Body)
	}
	if !strings.HasPrefix(asked, "motion: ") || !strings.Contains(asked, "they wave wildly") || !strings.Contains(asked, bad.Description) {
		t.Errorf("the assistant is told the current movement and the problem: %q", asked)
	}
	s.busy = func(string) (string, bool) { return "run-2", true }
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/reviews/"+b.Review.ID+"/findings/"+bad.ID+"/suggest", nil); rec.Code != http.StatusConflict {
		t.Errorf("busy: %d", rec.Code)
	}
}

func TestReview_DeleteOneAndInBulkLikeRuns(t *testing.T) {
	s, fe, dir := reviewServer(t)
	first := analyzeDemo(t, s, map[string]any{})
	second := analyzeDemo(t, s, map[string]any{})
	evidence := filepath.Join(dir, reviewsDir, first.Review.ID)
	if _, err := os.Stat(evidence); err != nil {
		t.Fatalf("evidence: %v", err)
	}
	// one review, with its evidence
	if rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/reviews/"+first.Review.ID, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(evidence); err == nil {
		t.Error("the evidence goes with the review")
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/reviews/"+first.Review.ID, nil); rec.Code != http.StatusNotFound {
		t.Errorf("deleted review: %d", rec.Code)
	}
	// bulk with a task running: that review is skipped and counted
	var started struct {
		Started []taskStarted `json:"started"`
	}
	json.Unmarshal(doJSON(t, s.Handler(), http.MethodPost, "/api/reviews/"+second.Review.ID+"/tasks", map[string]any{"finding_ids": []string{findingOf(second, "bad").ID}}).Body.Bytes(), &started)
	waitStart(t, fe)
	third := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/demo/reviews", map[string]any{"analyze": false})
	var thirdRev Review
	json.Unmarshal(third.Body.Bytes(), &thirdRev)
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/reviews/delete", map[string]any{"project": "demo"})
	var res struct {
		Deleted int      `json:"deleted"`
		Skipped []string `json:"skipped_active"`
	}
	json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != http.StatusOK || res.Deleted != 1 || len(res.Skipped) != 1 || res.Skipped[0] != second.Review.ID {
		t.Fatalf("bulk: %d %+v", rec.Code, res)
	}
	// both ways of asking at once, or neither, are refused
	for _, body := range []map[string]any{{"ids": []string{"x"}, "project": "demo"}, {}} {
		if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/reviews/delete", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%v: %d", body, rec.Code)
		}
	}
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/reviews/delete", map[string]any{"project": "demo", "before": "yesterday"}); rec.Code != http.StatusBadRequest {
		t.Errorf("bad date: %d", rec.Code)
	}
	fe.chanFor(started.Started[0].RunID) <- true
	waitState(t, s, started.Started[0].RunID, StateSucceeded)
	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/reviews/delete", map[string]any{"ids": []string{second.Review.ID}})
	json.Unmarshal(rec.Body.Bytes(), &res)
	if res.Deleted != 1 {
		t.Errorf("once its run is over the review can go: %+v", res)
	}
}

func TestReview_VisionModelsAreListedFromOllama(t *testing.T) {
	s, _ := newProjectsServer(t)
	s.Overrides.VisionModels = func(context.Context) ([]string, error) { return []string{"small-vl", "big-vl"}, nil }
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/review/vision-models", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `["small-vl","big-vl"]`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	s.Overrides.VisionModels = func(context.Context) ([]string, error) { return nil, os.ErrNotExist }
	if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/review/vision-models", nil); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("Ollama down: %d", rec.Code)
	}
}

func TestReview_ASaveThatFailsIsReported(t *testing.T) {
	hist, err := openHistory(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	rev := &Review{ID: "rev-x", Project: "demo", State: reviewAnalyzing}
	if err := hist.createReview(rev); err != nil {
		t.Fatal(err)
	}
	if err := saveReviewTwice(hist, rev); err != nil {
		t.Fatalf("a healthy database must save: %v", err)
	}
	sqlDB, _ := hist.db.DB()
	sqlDB.Close()
	if err := saveReviewTwice(hist, rev); err == nil {
		t.Fatal("a closed database must report the failure, not lose the write in silence")
	}
}

func TestReview_ShutdownWaitsForAnAnalysisInFlight(t *testing.T) {
	s, _ := newProjectsServer(t)
	s.rv.wg.Add(1)
	done := make(chan struct{})
	go func() {
		s.Shutdown()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("Shutdown returned while an analysis was still running")
	case <-time.After(150 * time.Millisecond):
	}
	s.rv.wg.Done()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown did not return once the analysis ended")
	}
}

type captureHandler struct {
	slog.Handler
	msgs *[]string
}

func (c captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (c captureHandler) Handle(_ context.Context, r slog.Record) error {
	*c.msgs = append(*c.msgs, r.Level.String()+" "+r.Message)
	return nil
}

// A warning of the server goes through slog, so it has a level and can be captured or silenced.
func TestWarnings_GoThroughSlog(t *testing.T) {
	var msgs []string
	old := slog.Default()
	slog.SetDefault(slog.New(captureHandler{Handler: slog.NewTextHandler(io.Discard, nil), msgs: &msgs}))
	defer slog.SetDefault(old)

	s, _ := newProjectsServer(t)
	sqlDB, _ := s.rm.hist.db.DB()
	sqlDB.Close()
	s.saveFindingOrWarn(&Finding{ID: "fnd-x", ReviewID: "rev-x"})
	if len(msgs) != 1 || !strings.HasPrefix(msgs[0], "WARN cannot save a finding") {
		t.Fatalf("want one WARN through slog, got %v", msgs)
	}
}

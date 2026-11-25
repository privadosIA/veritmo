package server

// core_fixtures_test.go: the servers and helpers the HTTP-level tests of the whole server share. The tests of the
// workspace family (internal/server/workspace) have their own, over the workspace alone.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"veritmo/internal/platform/events"
	"veritmo/internal/server/servertest"
	"veritmo/internal/server/workspace"
)

// The types of the answers of the workspace, under the names the tests use.
type (
	projectSummary    = workspace.ProjectSummary
	runView           = workspace.RunView
	findingJSON       = workspace.FindingJSON
	estimateView      = workspace.EstimateView
	databaseView      = workspace.DatabaseView
	monitoringView    = workspace.MonitoringView
	environmentReport = workspace.EnvironmentReport
	tokenStatus       = workspace.TokenStatus
	tokenSources      = workspace.TokenSources
	taskAvailability  = workspace.TaskAvailability
	Review            = workspace.Review
	databaseAnswer    = workspace.DatabaseAnswer
	monitoringRequest = workspace.MonitoringRequest
	phaseDurationView = workspace.PhaseDurationView
	estimateRequest   = workspace.EstimateRequest
	RunSample         = workspace.RunSample
)

const ()

var (
	doJSON = servertest.DoJSON
)

const goodURL = "https://youtu.be/dQw4w9WgXcQ"

// newProjectsServer builds a Server over a temp projects root and chdirs into a temp "repo root" holding models/ and
// voices/ so shared-asset linking and validation behave as in production.
func newProjectsServer(t *testing.T) (*Server, string) {
	t.Helper()
	return newProjectsServerMode(t, "")
}

// newProjectsServerMode is newProjectsServer with the content mode the server starts in.
func newProjectsServerMode(t *testing.T, mode ContentMode) (*Server, string) {
	t.Helper()
	return newProjectsServerWith(t, func(cfg *Config) { cfg.ContentMode = mode })
}

// newProjectsServerWith is newProjectsServer with the configuration adjusted before the server starts.
func newProjectsServerWith(t *testing.T, adjust func(cfg *Config)) (*Server, string) {
	t.Helper()
	repo := t.TempDir()
	for _, d := range []string{"models", "voices"} {
		if err := os.Mkdir(filepath.Join(repo, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "models", "m.safetensors"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"v.onnx", "v.onnx.json"} { // Piper needs the voice's companion config
		if err := os.WriteFile(filepath.Join(repo, "voices", v), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(repo)

	projects := filepath.Join(repo, "projects")
	cfg := Config{Addr: "127.0.0.1:0", ProjectsDir: projects, DBPath: filepath.Join(repo, "o.db"), Sampler: workspace.NoHardware()}
	if adjust != nil {
		adjust(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	quiet(s)
	return s, projects
}

// quiet makes a server of a test not wait for what is not there: a provider that does not answer, and Blender,
// which takes seconds to say its version.
func quiet(s *Server) *Server {
	s.motionModelTimeout = 100 * time.Millisecond
	s.blenderStatus = func() workspace.BinaryStatus {
		return workspace.BinaryStatus{Name: "blender", Fix: "Blender is not installed (a test does not ask it)"}
	}
	return s
}

func createProject(t *testing.T, s *Server, id string) {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects", map[string]string{"id": id})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %s: status %d: %s", id, rec.Code, rec.Body.String())
	}
}

func newModelsServer(t *testing.T) (*Server, string) {
	t.Helper()
	m := t.TempDir()
	s, _ := newProjectsServerWith(t, func(cfg *Config) { cfg.ModelsDir = m })
	return s, m
}

// newMediaServer is a projects server whose projects are kept in a temporary media directory (never the real media directory).
func newMediaServer(t *testing.T) (*Server, string, string) {
	t.Helper()
	m := t.TempDir()
	s, projects := newProjectsServerWith(t, func(cfg *Config) { cfg.MediaDir = m })
	return s, projects, m
}
func putSource(t *testing.T, s *Server, project, name string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/projects/"+project+"/sources/"+name, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func getEnvironment(t *testing.T, s *Server, query string) environmentReport {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/environment"+query, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("environment: status %d: %s", rec.Code, rec.Body.String())
	}
	var rep environmentReport
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	return rep
}

func binByName(t *testing.T, rep environmentReport, name string) binaryStatus {
	t.Helper()
	for _, b := range rep.Binaries {
		if b.Name == name {
			return b
		}
	}
	t.Fatalf("binary %s missing from report", name)
	return binaryStatus{}
}

// fakeLook pretends every pipeline binary exists.
func fakeLook(name string) (string, error) { return "/fake/" + name, nil }

// blockingExecute returns an execute seam whose runs block until released
// (or their context is cancelled) and records starts.
type fakeExec struct {
	started chan string          // receives run IDs as they start
	mu      sync.Mutex           // guards release (the test and the worker both touch it)
	release map[string]chan bool // run ID -> succeed(true)/keep error(false)
}

func newFakeExec() *fakeExec {
	return &fakeExec{started: make(chan string, 16), release: map[string]chan bool{}}
}

func launchRun(t *testing.T, s *Server, project string, req map[string]any) (string, int) {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/"+project+"/runs", req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("launch: status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Run           Run `json:"run"`
		QueuePosition int `json:"queue_position"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Run.ID, resp.QueuePosition
}

func waitState(t *testing.T, s *Server, id string, want workspace.RunState) Run {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rec, err := s.workspace.RunRecord(id)
		if err == nil && rec.State == want {
			return *rec
		}
		time.Sleep(5 * time.Millisecond)
	}
	rec, _ := s.workspace.RunRecord(id)
	t.Fatalf("run %s never reached %s (last: %+v)", id, want, rec)
	return Run{}
}

func waitStart(t *testing.T, fe *fakeExec) string {
	t.Helper()
	select {
	case id := <-fe.started:
		return id
	case <-time.After(3 * time.Second):
		t.Fatal("no run started")
		return ""
	}
}

type statRun struct {
	id, project, gpu string
	state            workspace.RunState
	client           string // the client stored in the run ("" for none)
	created          time.Time
	waitS, durS      float64 // 0 durS: no start/finish timestamps
	events           []string
}

func (f *fakeExec) execute(ctx context.Context, rn workspace.ExecRun, emitter *events.Emitter) (string, error) {
	emitter.PipelineStart()
	f.started <- rn.Rec.ID
	select {
	case ok := <-f.chanFor(rn.Rec.ID):
		if !ok {
			emitter.Error("_global", "boom")
			return "", fmt.Errorf("boom")
		}
		emitter.Done("out/x.mp4")
		return "out/x.mp4", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (f *fakeExec) chanFor(id string) chan bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.release[id]; !ok {
		f.release[id] = make(chan bool, 1)
	}
	return f.release[id]
}

// launchServer returns a projects server with a project created, the fake executor installed and binary lookups faked.
func launchServer(t *testing.T) (*Server, *fakeExec) {
	t.Helper()
	s, _ := newProjectsServer(t)
	createProject(t, s, "demo")
	fe := newFakeExec()
	s.workspace.Execute = fe.execute
	s.workspace.SetPipelineLookPath(fakeLook)
	t.Cleanup(s.workspace.Shutdown)
	return s, fe
}

func ev(typ, phase string, ms int64) string {
	if typ == "phase_end" {
		return fmt.Sprintf(`{"type":"phase_end","scene_id":"a","phase":%q,"duration_ms":%d}`, phase, ms)
	}
	return fmt.Sprintf(`{"type":%q,"scene_id":"a","phase":%q}`, typ, phase)
}

var d0 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

const wide = "?since=2026-01-01&until=2026-12-31"

func seedStat(t *testing.T, s *Server, r statRun) {
	t.Helper()
	run := Run{ID: r.id, Project: r.project, State: r.state, GPU: r.gpu, Client: r.client, CreatedAt: r.created}
	if r.durS > 0 {
		st := r.created.Add(time.Duration(r.waitS * float64(time.Second)))
		fin := st.Add(time.Duration(r.durS * float64(time.Second)))
		run.StartedAt, run.FinishedAt = &st, &fin
	}
	if err := s.workspace.SeedRun(&run); err != nil {
		t.Fatal(err)
	}
	for i, l := range r.events {
		if err := s.workspace.SeedEvent(r.id, i, l); err != nil {
			t.Fatal(err)
		}
	}
}

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

type reviewBody struct {
	Review   Review        `json:"review"`
	Findings []findingJSON `json:"findings"`
}

func getReview(t *testing.T, s *Server, id string) reviewBody {
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

// reviewServer is a project "demo" with animated scenes whose clips exist:
// "bad" shifts colour at 1.5 s, "good" is steady, "talk" (narrated) shifts too.
func reviewServer(t *testing.T) (*Server, *fakeExec, string) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	s, fe := launchServer(t)
	for _, f := range []string{"wan.gguf", "umt5.gguf", "wan_vae.safetensors"} {
		os.WriteFile(filepath.Join("models", f), []byte("x"), 0o644)
	}
	dir := s.projectDir("demo")
	if err := s.workspace.SaveProjectDefinition("demo", []byte(reviewYAML), "save"); err != nil {
		t.Fatal(err)
	}
	if err := s.workspace.MaterializeProject("demo"); err != nil {
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

// analyzeDemo starts an analysis and waits for it to be ready.
func analyzeDemo(t *testing.T, s *Server, body map[string]any) reviewBody {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/demo/reviews", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	var rev Review
	json.Unmarshal(rec.Body.Bytes(), &rev)
	if rev.State != "analyzing" || rev.Total != 3 {
		t.Fatalf("started review = %+v", rev)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		b := getReview(t, s, rev.ID)
		if b.Review.State != "analyzing" {
			return b
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the analysis never finished")
	return reviewBody{}
}

// The contract checker lives in servertest, shared with the tests of the families.
type contractChecker = servertest.ContractChecker

var (
	newContractChecker = servertest.NewContractChecker
	expectFits         = servertest.ExpectFits
)

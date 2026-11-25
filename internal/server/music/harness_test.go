package music

// harness_test.go: the family on top of the stack of the families below it (a real workspace, models, assistant and
// generation over a fake host), for the tests of the package. Tests that need the whole server (the contract in
// openapi.yaml, the content mode, the settings) are HTTP-level tests of package server.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"veritmo/internal/platform/store"

	"veritmo/internal/music/musicmotion"
	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/platform/runner"
	"veritmo/internal/platform/sched"
	"veritmo/internal/platform/settings"
	"veritmo/internal/project/export"
	"veritmo/internal/server/assistant"
	"veritmo/internal/server/generation"
	"veritmo/internal/server/modelcat"
	"veritmo/internal/server/models"
	"veritmo/internal/server/servertest"
	"veritmo/internal/server/servertest/stack"
	"veritmo/internal/server/workspace"
	"veritmo/internal/video/motion"
	"veritmo/internal/video/videoedit"
)

// The names the tests use for what the workspace and the helpers define.
type (
	projectSummary = workspace.ProjectSummary
	cacheReport    = workspace.CacheReport
	libraryResp    = workspace.LibraryResponse
)

const (
	phaseRun    = workspace.PhaseRun
	phaseHit    = workspace.PhaseHit
	phaseAlways = workspace.PhaseAlways
	phaseNA     = workspace.PhaseNA
)

var (
	doJSON            = servertest.DoJSON
	call              = servertest.Call
	writeArtifact     = servertest.WriteArtifact
	isolateSettings   = servertest.IsolateSettings
	findKind          = models.FindKind
	validateAssetName = models.ValidateAssetName
)

// mHandler lets testServer embed the handler under a name that does not clash with its Handler method.
type mHandler = Handler

// testServer is the family on top of the stack. The fields of the stack and these are the seams the tests replace.
type testServer struct {
	*mHandler
	*stack.Stack
	memAvailable  sched.AvailableFunc
	motionBrowser func() (motion.Browser, error)
	motionWriter  func(ctx context.Context) (motion.Writer, motion.ModelInfo, error)
	gen           *generation.Handler
}

type testHost struct{ s *testServer }

func (h testHost) Workspace() *workspace.Handler   { return h.s.Stack.Workspace }
func (h testHost) Models() *models.Handler         { return h.s.Stack.Models }
func (h testHost) Generation() *generation.Handler { return h.s.gen }
func (h testHost) Assistant() *assistant.Handler   { return h.s.Stack.Assistant }

func (h testHost) ProjectsDir() string               { return h.s.ProjectsDir }
func (h testHost) LookPath(n string) (string, error) { return h.s.Look(n) }
func (h testHost) Runner() runner.CommandRunner      { return h.s.MediaRunner }
func (h testHost) EnvRunner() runner.CommandRunner   { return h.s.EnvRunner }
func (h testHost) Services() *musicsvc.ServiceHost   { return h.s.Services }
func (h testHost) MemAvailable() sched.AvailableFunc { return h.s.memAvailable }
func (h testHost) MotionBrowser() func() (motion.Browser, error) {
	return h.s.motionBrowser
}
func (h testHost) MotionCatalog() musicmotion.Catalog {
	return musicmotion.NewCatalog(musicmotion.NewDBParts(h.s.Stack.Store))
}
func (h testHost) MotionWriter() func(ctx context.Context) (motion.Writer, motion.ModelInfo, error) {
	return h.s.motionWriter
}
func (h testHost) DropsAvailability(f http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f(w, r)
		h.s.Stack.Workspace.DropAvailability()
		h.s.mHandler.BumpEngines()
	}
}

type genHost struct{ s *testServer }

func (h genHost) Workspace() *workspace.Handler { return h.s.Stack.Workspace }
func (h genHost) Models() *models.Handler       { return h.s.Stack.Models }
func (h genHost) Assistant() *assistant.Handler { return h.s.Stack.Assistant }
func (h genHost) ProjectsDir() string           { return h.s.ProjectsDir }
func (h genHost) AssetsRoot() string            { return h.s.AssetsRoot }
func (h genHost) LookPath(n string) (string, error) {
	return h.s.Look(n)
}
func (h genHost) Runner() runner.CommandRunner    { return h.s.MediaRunner }
func (h genHost) EnvRunner() runner.CommandRunner { return h.s.EnvRunner }
func (h genHost) VideoEdit() *videoedit.Catalog   { return h.s.Stack.VideoEdit }
func (h genHost) ProjectVisible(string) bool      { return true }
func (h genHost) RefuseAdultConfig(w http.ResponseWriter, adult *bool) bool {
	return h.s.RefuseAdult(w, adult)
}
func (h genHost) Catalog() []modelcat.Entry { return h.s.Catalog }
func (h genHost) MotionCatalog() musicmotion.Catalog {
	return musicmotion.NewCatalog(musicmotion.NewDBParts(h.s.Stack.Store))
}
func (h genHost) MotionWriter(ctx context.Context, provider string) (musicmotion.Writer, context.Context) {
	return h.s.mHandler.MotionWriter(ctx, provider)
}

// newProjectsServer builds the family over a temp projects root and chdirs into a temp "repo root" holding models/
// and voices/ so shared-asset linking and validation behave as in production.
func newProjectsServer(t *testing.T) (*testServer, string) {
	t.Helper()
	s := &testServer{Stack: stack.New(t, catalogForTest(), func(r *store.Registry) { RegisterStore(r, "") })}
	s.gen = generation.New(genHost{s})
	s.mHandler = New(testHost{s})
	s.Stack.Workspace.Overrides = workspace.Overrides{MotionBrowser: func() (motion.Browser, error) {
		if s.motionBrowser != nil {
			return s.motionBrowser()
		}
		return motion.Find(settings.Browser())
	}}
	s.EngineReport = s.mHandler.Engines
	s.Preflight = s.mHandler.ContainerPreflight
	s.Stack.Vision = s.mHandler.VisionEngine
	s.Stack.OverlayVision = s.mHandler.OverlayVision
	s.WhisperPath = s.mHandler.WhisperDir()
	s.Whisper = s.mHandler.WhisperInstalled
	s.Stack.Register(s.gen)
	s.Stack.Register(s.mHandler)
	s.Stack.Mux.HandleFunc("PUT /api/settings", testHost{s}.DropsAvailability(s.putSettingsStub))
	s.Finish()
	s.Stack.VideoEdit.SetVision(s.Models.Vision()) // as the server does: the tools that read a picture with a network ask this service
	return s, s.ProjectsDir
}

// Handler is the handler of every route of the stack and of the family.
func (s *testServer) Handler() http.Handler { return s.Stack.Handler() }

func catalogForTest() []modelcat.Entry {
	_, here, _, _ := runtime.Caller(0)
	return stack.ReadCatalog(filepath.Join(filepath.Dir(here), ".."), catalogFiles())
}

func createProject(t *testing.T, s *testServer, id string) {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects", map[string]string{"id": id})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %s: status %d: %s", id, rec.Code, rec.Body.String())
	}
}

func putSource(t *testing.T, s *testServer, project, name string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/projects/"+project+"/sources/"+name, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func putSourceAs(t *testing.T, s *testServer, project, name, as string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/projects/"+project+"/sources/"+name+"?as="+as, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func getLibrary(t *testing.T, s *testServer) libraryResp {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/library", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("library: %d %s", rec.Code, rec.Body.String())
	}
	var out libraryResp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func getEnvironment(t *testing.T, s *testServer, query string) workspace.EnvironmentReport {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/environment"+query, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("environment: status %d: %s", rec.Code, rec.Body.String())
	}
	var rep workspace.EnvironmentReport
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	return rep
}

func binByName(t *testing.T, rep workspace.EnvironmentReport, name string) workspace.BinaryStatus {
	t.Helper()
	for _, b := range rep.Binaries {
		if b.Name == name {
			return b
		}
	}
	t.Fatalf("binary %s missing from report", name)
	return workspace.BinaryStatus{}
}

// fakeLook pretends every pipeline binary exists.
func fakeLook(name string) (string, error) { return "/fake/" + name, nil }

// songProject writes a music project with a manifest listing the given songs (track, seed) and the audio of
// those whose file should exist.
func songProject(t *testing.T, s *testServer, id string, favorite string, songs ...[2]any) {
	t.Helper()
	createProject(t, s, id)
	y := "project: " + id + "\noutput:\n  mode: music\ntracks:\n  - id: ballad\n    prompt: p\n"
	if favorite != "" {
		y += "favorites:\n  - { track: ballad, seed: " + favorite + " }\n"
	}
	if err := s.Workspace.SaveProjectDefinition(id, []byte(y), "save"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.Workspace.ProjectDir(id), "out", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	m := export.MusicManifest{Project: id, Mode: "music", GeneratedAt: "2026-10-13T10:00:00Z"}
	for i, sg := range songs {
		file := filepath.Join("out", id, sg[0].(string)+".mp3")
		if sg[1].(int64) >= 0 {
			os.WriteFile(filepath.Join(s.Workspace.ProjectDir(id), file), []byte("mp3"), 0o644)
		}
		seed := sg[1].(int64)
		if seed < 0 {
			seed = -seed
		}
		m.Tracks = append(m.Tracks, export.MusicEntry{TrackID: "ballad", Variant: i, EffectiveSeed: seed, Title: "Song " + sg[0].(string), Caption: "pop", File: file, AudioSeconds: 120})
	}
	if err := export.WriteMusicManifest(filepath.Join(dir, "manifest.json"), m); err != nil {
		t.Fatal(err)
	}
	// what the end of a run does: the library follows what the run exported
	if _, err := s.Workspace.SyncLibrary(context.Background(), id); err != nil {
		t.Fatal(err)
	}
}

// songsProject writes a music project with one exported song (and its manifest) and returns the project's folder.
func songsProject(t *testing.T, s *testServer, projects string) string {
	t.Helper()
	createProject(t, s, "songs")
	dir := filepath.Join(projects, "songs")
	if err := s.Workspace.SaveProjectDefinition("songs", []byte(songsYAML), "save"); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out", "songs")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "night_drive.mp3"), []byte("MP3DATA"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := export.MusicManifest{Project: "songs", Mode: "music", Tracks: []export.MusicEntry{{
		TrackID: "night_drive", Caption: "warm acoustic pop", LyricsSung: "[Verse]\none\ntwo\n[Chorus]\nthree\nfour", AudioSeconds: 60,
		SynthModel: "x", File: filepath.Join("out", "songs", "night_drive.mp3"), Title: "Night Drive",
	}}}
	b, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(out, "manifest.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const songsYAML = `project: songs
output: {mode: music}
tracks:
  - id: night_drive
    title: Night Drive
    prompt: warm acoustic pop
    lyrics: |-
      [Verse]
      one
      two
      [Chorus]
      three
      four
`

func get(t *testing.T, s *testServer, path string) string {
	t.Helper()
	rec := call(s.Handler(), http.MethodGet, path, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body)
	}
	return rec.Body.String()
}

func findCatalog(id string) (modelcat.Entry, bool) {
	for _, e := range catalogForTest() {
		if e.ID == id {
			return e, true
		}
	}
	return modelcat.Entry{}, false
}

var (
	newContractChecker = servertest.NewContractChecker
	expectFits         = servertest.ExpectFits
)

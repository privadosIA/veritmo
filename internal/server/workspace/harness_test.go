package workspace

// harness_test.go: a workspace on its own, with a host that replaces what the server gives it, for the tests of the
// package. Tests that need the whole server (authentication, the content mode, the catalog installed on disk) are
// HTTP-level tests of package server.

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/music/heartmula"
	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
	"veritmo/internal/project/pipeline"
	"veritmo/internal/server/api"
	"veritmo/internal/server/modelcat"
	"veritmo/internal/video/videoedit"
)

// wsHandler lets testServer embed the handler under a name that does not clash with its Handler method.
type wsHandler = Handler

// testServer is a workspace with its mux and a fake host. Its fields are the seams the tests replace.
type testServer struct {
	*wsHandler
	mux *http.ServeMux

	assetsRoot  string
	look        func(string) (string, error)
	mediaRunner runner.CommandRunner
	envRunner   runner.CommandRunner
	catalog     []modelcat.Entry
	assistRun   func(ctx context.Context, task assist.Task, text, model string) (string, error)
	engines     map[string]heartmula.Availability
	tokens      TokenInfo
	whisperDir  string
	blender     BinaryStatus
	hidden      map[string]bool
	preflight   func(cfg *config.Config) *LaunchError
	started     time.Time
	videoedit   *videoedit.Catalog
}

type testHost struct{ s *testServer }

func (h testHost) AssetsRoot() string                   { return h.s.assetsRoot }
func (h testHost) LookPath(n string) (string, error)    { return h.s.look(n) }
func (h testHost) Runner() runner.CommandRunner         { return h.s.mediaRunner }
func (h testHost) EnvRunner() runner.CommandRunner      { return h.s.envRunner }
func (h testHost) VideoEdit() *videoedit.Catalog        { return h.s.videoedit }
func (h testHost) Motion() pipeline.MotionEngine        { return nil }
func (h testHost) MusicServices() *musicsvc.ServiceHost { return nil }
func (h testHost) Started() time.Time                   { return h.s.started }
func (h testHost) ProjectVisible(id string) bool        { return !h.s.hidden[id] }
func (h testHost) RunProjectVisible(p string) bool      { return !h.s.hidden[p] }
func (h testHost) OnlyVisibleRunIDs(ids []string) []string {
	return ids
}
func (h testHost) RefuseAdultConfig(http.ResponseWriter, *bool) bool { return false }

// ShowsAdult is false while a test hides a project, as the real host is when it hides the projects for adults.
func (h testHost) ShowsAdult() bool            { return len(h.s.hidden) == 0 }
func (h testHost) ContentFilter() (bool, bool) { return false, false }
func (h testHost) ContentMode() string         { return "all" }
func (h testHost) Catalog() []modelcat.Entry   { return h.s.catalog }
func (h testHost) MachineOf(gpus []GPUStatus) (int, float64) {
	if len(gpus) == 0 {
		return 0, 20
	}
	return gpus[0].TotalVRAMMB, 1
}
func (h testHost) Preflight(cfg *config.Config) *LaunchError {
	if h.s.preflight == nil {
		return nil
	}
	return h.s.preflight(cfg)
}
func (h testHost) AssistRun() func(ctx context.Context, task assist.Task, text, model string) (string, error) {
	return h.s.assistRun
}
func (h testHost) Engines(context.Context) map[string]heartmula.Availability { return h.s.engines }
func (h testHost) Tokens() TokenInfo                                         { return h.s.tokens }
func (h testHost) WhisperDir() string                                        { return h.s.whisperDir }
func (h testHost) WhisperInstalled() bool {
	entries, err := os.ReadDir(h.s.whisperDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".bin") {
			return true
		}
	}
	return false
}
func (h testHost) BlenderStatus() BinaryStatus { return h.s.blender }

func newTestServerWith(t *testing.T, cfg Config) *testServer {
	t.Helper()
	quiet := NoHardware() // no test reads the machine it runs on
	if cfg.Sampler.ReadGPUs == nil {
		cfg.Sampler.ReadGPUs = quiet.ReadGPUs
	}
	if cfg.Sampler.ReadRAM == nil {
		cfg.Sampler.ReadRAM = quiet.ReadRAM
	}
	s := &testServer{mux: http.NewServeMux(), started: time.Now(), hidden: map[string]bool{}, catalog: catalogForTest(t), videoedit: videoedit.NewCatalog()}
	s.look = exec.LookPath
	s.mediaRunner = runner.NewExecRunner()
	s.envRunner = runner.NewExecRunner()
	h, err := New(cfg, testHost{s})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.wsHandler = h
	h.Register(func(pattern string, f http.HandlerFunc) { s.mux.HandleFunc(pattern, api.LimitBody(f)) })
	s.mux.HandleFunc("/api/", api.CatchAll(s.mux))
	t.Cleanup(h.Shutdown)
	return s
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	return newTestServerWith(t, Config{ProjectsDir: t.TempDir(), DBPath: t.TempDir() + "/orchestrator.db"})
}

// Handler is the handler of the endpoints of the family.
// Handler serves the requests of a test. The projects a test writes as folders (a scenes.yaml in the projects folder) are imported into the
// database first, as the importer of the real server does for the folders that existed before it: a folder that has no row is not a project.
func (s *testServer) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = s.ImportFolders()
		s.mux.ServeHTTP(w, r)
	})
}

// catalogForTest is the model catalog the server embeds: the tests read its file, as the families that need the
// installed catalog (models, generation) are not below this package.
func catalogForTest(t testing.TB) []modelcat.Entry {
	t.Helper()
	var all []modelcat.Entry
	_, here, _, _ := runtime.Caller(0)
	for _, name := range []string{"catalog.json", "catalog_private.json"} { // the second exists only in the private edition
		raw, err := os.ReadFile(filepath.Join(filepath.Dir(here), "..", name)) // the tests chdir into a temp root
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		var part []modelcat.Entry
		if err := json.Unmarshal(raw, &part); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		all = append(all, part...)
	}
	return all
}

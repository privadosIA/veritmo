package generation

// harness_test.go: the family on top of a real workspace and a real models family, with a host that replaces what the
// server gives them, for the tests of the package. Tests that need the whole server (authentication, the content
// mode, the contract in openapi.yaml, the assistant and the music-motion pieces) are HTTP-level tests of package server.

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"veritmo/internal/project/config"

	"veritmo/internal/music/musicmotion"
	"veritmo/internal/platform/runner"
	"veritmo/internal/server/assistant"
	"veritmo/internal/server/modelcat"
	"veritmo/internal/server/models"
	"veritmo/internal/server/servertest"
	"veritmo/internal/server/servertest/stack"
	"veritmo/internal/server/workspace"
	"veritmo/internal/video/videoedit"
)

// The names the tests use for what the workspace and the helpers define.
type (
	projectSummary    = workspace.ProjectSummary
	environmentReport = workspace.EnvironmentReport
)

const kindEditRun = workspace.KindEditRun

var (
	doJSON     = servertest.DoJSON
	call       = servertest.Call
	runKindOf  = workspace.RunKindOf
	sourceKind = workspace.SourceKind
	findKind   = models.FindKind
)

// gHandler lets testServer embed the handler under a name that does not clash with its Handler method.
type gHandler = Handler

// testServer is the family on top of the stack of the families below it (a real workspace, models and assistant over
// a fake host). The fields of the stack are the seams the tests replace.
type testServer struct {
	*gHandler
	*stack.Stack
	motionWrite func(ctx context.Context, provider string) (musicmotion.Writer, context.Context)
}

type testHost struct{ s *testServer }

func (h testHost) AssetsRoot() string                { return h.s.AssetsRoot }
func (h testHost) LookPath(n string) (string, error) { return h.s.Look(n) }
func (h testHost) Runner() runner.CommandRunner      { return h.s.MediaRunner }
func (h testHost) EnvRunner() runner.CommandRunner   { return h.s.EnvRunner }
func (h testHost) VideoEdit() *videoedit.Catalog     { return h.s.Stack.VideoEdit }
func (h testHost) ProjectsDir() string               { return h.s.ProjectsDir }
func (h testHost) Catalog() []modelcat.Entry         { return h.s.Catalog }
func (h testHost) ProjectVisible(string) bool        { return true }
func (h testHost) RefuseAdultConfig(w http.ResponseWriter, adult *bool) bool {
	return h.s.RefuseAdult(w, adult)
}
func (h testHost) MotionCatalog() musicmotion.Catalog { return musicmotion.NewCatalog(nil) }
func (h testHost) MotionWriter(ctx context.Context, provider string) (musicmotion.Writer, context.Context) {
	if h.s.motionWrite != nil {
		return h.s.motionWrite(ctx, provider)
	}
	return nil, ctx
}
func (h testHost) Workspace() *workspace.Handler { return h.s.Stack.Workspace }
func (h testHost) Models() *models.Handler       { return h.s.Stack.Models }
func (h testHost) Assistant() *assistant.Handler { return h.s.Stack.Assistant }

// newProjectsServer builds the family over a temp projects root and chdirs into a temp "repo root" holding models/
// and voices/ so shared-asset linking and validation behave as in production.
func newProjectsServer(t *testing.T) (*testServer, string) {
	t.Helper()
	s := &testServer{Stack: stack.New(t, catalogForTest(t))}
	s.gHandler = New(testHost{s})
	s.Blender = s.gHandler.BlenderStatus
	s.Stack.Register(s.gHandler)
	s.Finish()
	return s, s.ProjectsDir
}

func createProject(t *testing.T, s *testServer, id string) {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects", map[string]string{"id": id})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %s: status %d: %s", id, rec.Code, rec.Body.String())
	}
}

func getEnvironment(t *testing.T, s *testServer, query string) environmentReport {
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

func binByName(t *testing.T, rep environmentReport, name string) workspace.BinaryStatus {
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

func (s *testServer) kindDir(k models.AssetKind) string { return filepath.Join(s.AssetsRoot, k.Dir) }

// catalogForTest is the model catalog the server embeds, read from its files.
func catalogForTest(testing.TB) []modelcat.Entry {
	var all []modelcat.Entry
	_, here, _, _ := runtime.Caller(0)
	for _, name := range catalogFiles() { // the tests chdir into a temp root
		raw, err := os.ReadFile(filepath.Join(filepath.Dir(here), "..", name))
		if err != nil {
			panic(err)
		}
		var part []modelcat.Entry
		if err := json.Unmarshal(raw, &part); err != nil {
			panic(name + ": " + err.Error())
		}
		all = append(all, part...)
	}
	return all
}

// The fixture catalogs of the tests, shared with the HTTP-level tests of the server.
var (
	files       = servertest.FixtureFiles
	tInto       = servertest.FixtureTInto
	tSD15       = servertest.FixtureTSD15
	tWan        = servertest.FixtureTWan
	tH3         = servertest.FixtureTH3
	tH3FF       = servertest.FixtureTH3FF
	tLTX        = servertest.FixtureTLTX
	tAD         = servertest.FixtureTAD
	tKontext    = servertest.FixtureTKontext
	tQwen       = servertest.FixtureTQwen
	baseCatalog = servertest.BaseCatalog
	fullCatalog = servertest.FullCatalog
)

func findCatalog(id string) (catalogEntry, bool) {
	for _, e := range catalogForTest(nil) {
		if e.ID == id {
			return e, true
		}
	}
	return catalogEntry{}, false
}

// catalog is the embedded catalog of the server, read from its files.
var catalog = catalogForTest(nil)

const idMiniMax = "video-minimax-h3-ref2va-q4"

func catalogEntryByID(t *testing.T, id string) catalogEntry {
	t.Helper()
	e, ok := findCatalog(id)
	if !ok {
		t.Fatalf("catalog has no %s", id)
	}
	return e
}

func rolesByName(e catalogEntry) map[string]string {
	m := map[string]string{}
	for _, f := range e.Files {
		m[f.Role] = f.Name
	}
	return m
}

func catalogEntryInstalled(t *testing.T, s *testServer, id string) bool {
	t.Helper()
	rec := call(s.Handler(), "GET", "/api/assets/catalog", "")
	var out struct{ Entries []catalogEntry }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	for _, x := range out.Entries {
		if x.ID == id {
			return x.Installed
		}
	}
	t.Fatalf("%s missing from the served catalog", id)
	return false
}

// newAssetServer is the family over a server whose assets root is a fresh temp dir.
func newAssetServer(t *testing.T) *testServer {
	t.Helper()
	s, _ := newProjectsServer(t)
	s.AssetsRoot = t.TempDir()
	return s
}

func writeAsset(t *testing.T, s *testServer, kind, name string, size int) string {
	t.Helper()
	k, _ := models.FindKind(kind)
	p := filepath.Join(s.kindDir(k), name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

var validateAssetName = models.ValidateAssetName

func closeTo(t *testing.T, what string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.5 {
		t.Errorf("%s = %.1f, want %.1f", what, got, want)
	}
}

// expectedClip is how long the animation of a scene takes on the reference machine, by the catalog.
func expectedClip(cfg *config.Config, sc config.Scene) float64 {
	return workspace.ExpectedClip(catalog, cfg, sc)
}

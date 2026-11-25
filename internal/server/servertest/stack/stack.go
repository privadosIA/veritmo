// Package stack builds, for the tests of the families above the workspace (generation, music), the families below
// it (assistant, workspace, models) over a temporary repository and a fake host, with their routes on one mux. Only
// tests import it.
package stack

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"veritmo/internal/music/hyperframes"
	"veritmo/internal/music/motionref"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/music/heartmula"
	"veritmo/internal/music/musicmotion"
	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/platform/runner"
	"veritmo/internal/platform/settings"
	"veritmo/internal/platform/store"
	"veritmo/internal/project/config"
	"veritmo/internal/project/pipeline"
	"veritmo/internal/server/api"
	"veritmo/internal/server/assistant"
	"veritmo/internal/server/modelcat"
	"veritmo/internal/server/models"
	"veritmo/internal/server/workspace"
	"veritmo/internal/video/motionlib"
	"veritmo/internal/video/videoedit"
)

// Stack is the families below the generation family, with the values a test replaces: the host reads them every time.
type Stack struct {
	// Store is the database of the stack: the history and the tables of the music motion engine, migrated.
	Store     *store.Store
	Workspace *workspace.Handler
	Models    *models.Handler
	Assistant *assistant.Handler
	Mux       *http.ServeMux

	ProjectsDir string
	AssetsRoot  string
	Look        func(string) (string, error)
	MediaRunner runner.CommandRunner
	EnvRunner   runner.CommandRunner
	Catalog     []modelcat.Entry
	// VideoEdit is the catalog of the video-edit tools; a test registers its stubs in it.
	VideoEdit *videoedit.Catalog
	// Adult says the server shows adult content too; the default is a censored server.
	Adult    bool
	Services *musicsvc.ServiceHost
	// Engines, BlenderStatus and Preflight are what the families above add to the environment report and to a launch.
	EngineReport func(ctx context.Context) map[string]heartmula.Availability
	Blender      func() workspace.BinaryStatus
	Preflight    func(cfg *config.Config) *workspace.LaunchError
	// Vision and OverlayVision are the vision service of the video-edit tools, which the music family runs.
	Vision        func() *musicsvc.Engine
	OverlayVision func(a *musicsvc.Availability)
	MachineOf     func(gpus []workspace.GPUStatus) (vramMB int, speed float64)
	WhisperPath   string
	// Whisper says whether a Whisper model is installed.
	Whisper func() bool
}

// New builds the stack over a temp projects root and chdirs into a temp "repo root" holding models/ and voices/, so
// shared-asset linking and validation behave as in production. The catalog is the one the server embeds.
func New(t *testing.T, catalog []modelcat.Entry, register ...func(*store.Registry)) *Stack {
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

	s := &Stack{Mux: http.NewServeMux(), ProjectsDir: filepath.Join(repo, "projects"), AssetsRoot: ".", Catalog: catalog,
		Look: exec.LookPath, MediaRunner: runner.NewExecRunner(), EnvRunner: runner.NewExecRunner(), VideoEdit: videoedit.NewCatalog()}
	reg := store.NewRegistry()
	workspace.RegisterStore(reg)
	musicmotion.RegisterStore(reg, musicmotion.Folders{})
	motionref.RegisterStore(reg, motionref.Folders{})
	hyperframes.RegisterStore(reg, "")
	motionlib.RegisterStore(reg, "")
	for _, r := range register { // the tables of the family under test (the stack cannot import it)
		r(reg)
	}
	st, err := store.Open(filepath.Join(repo, "o.db"), settings.Database{}, reg)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatalf("store: %v", err)
	}
	s.Store = st
	s.Workspace, err = workspace.New(workspace.Config{Store: st, ProjectsDir: s.ProjectsDir, DBPath: filepath.Join(repo, "o.db"), Sampler: workspace.NoHardware()}, wsHost{s})
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	t.Cleanup(s.Workspace.Shutdown)
	s.Models = models.New(modelsHost{s})
	t.Cleanup(s.Models.Shutdown)
	s.Assistant = assistant.New(assistantHost{s})
	handle := func(pattern string, f http.HandlerFunc) { s.Mux.HandleFunc(pattern, api.LimitBody(f)) }
	s.Assistant.Register(handle)
	s.Workspace.Register(handle)
	s.Models.Register(handle)
	return s
}

// Register registers the routes of a family on top of the stack.
func (s *Stack) Register(r interface{ Register(api.HandleFunc) }) {
	r.Register(func(pattern string, f http.HandlerFunc) { s.Mux.HandleFunc(pattern, api.LimitBody(f)) })
}

// Finish adds the catch-all of /api/, once every family has registered its routes.
func (s *Stack) Finish() { s.Mux.HandleFunc("/api/", api.CatchAll(s.Mux)) }

// Handler is the handler of every route.
// Handler serves the requests of a test; the projects a test wrote as folders are imported into the database first (see ImportFolders).
func (s *Stack) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = s.Workspace.ImportFolders()
		s.Mux.ServeHTTP(w, r)
	})
}

// RefuseAdult answers 422 when a request would label a project adult on a server that hides them.
func (s *Stack) RefuseAdult(w http.ResponseWriter, adult *bool) bool {
	if s.Adult || adult == nil || !*adult {
		return false
	}
	api.WriteError(w, http.StatusUnprocessableEntity, api.CodeContentNotAllowed, "this server does not keep content for adults")
	return true
}

// ReadCatalog reads the catalog files of the server from disk (the second exists only in the private edition).
func ReadCatalog(dir string, names []string) []modelcat.Entry {
	var all []modelcat.Entry
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(dir, name))
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

type wsHost struct{ s *Stack }

func (h wsHost) AssetsRoot() string                   { return h.s.AssetsRoot }
func (h wsHost) LookPath(n string) (string, error)    { return h.s.Look(n) }
func (h wsHost) Runner() runner.CommandRunner         { return h.s.MediaRunner }
func (h wsHost) EnvRunner() runner.CommandRunner      { return h.s.EnvRunner }
func (h wsHost) VideoEdit() *videoedit.Catalog        { return h.s.VideoEdit }
func (h wsHost) Motion() pipeline.MotionEngine        { return musicmotion.Parts{} }
func (h wsHost) MusicServices() *musicsvc.ServiceHost { return h.s.Services }
func (h wsHost) Started() time.Time                   { return time.Now() }
func (h wsHost) ProjectVisible(string) bool           { return true }
func (h wsHost) RunProjectVisible(string) bool        { return true }
func (h wsHost) OnlyVisibleRunIDs(ids []string) []string {
	return ids
}
func (h wsHost) RefuseAdultConfig(w http.ResponseWriter, adult *bool) bool {
	return h.s.RefuseAdult(w, adult)
}
func (h wsHost) ShowsAdult() bool            { return h.s.Adult }
func (h wsHost) ContentFilter() (bool, bool) { return !h.s.Adult, false }
func (h wsHost) ContentMode() string         { return "all" }
func (h wsHost) Catalog() []modelcat.Entry {
	return h.s.Catalog
}
func (h wsHost) MachineOf(gpus []workspace.GPUStatus) (int, float64) {
	if h.s.MachineOf != nil {
		return h.s.MachineOf(gpus)
	}
	if len(gpus) == 0 {
		return 0, 20
	}
	return gpus[0].TotalVRAMMB, 1
}
func (h wsHost) Preflight(cfg *config.Config) *workspace.LaunchError {
	if h.s.Preflight == nil {
		return nil
	}
	return h.s.Preflight(cfg)
}
func (h wsHost) AssistRun() func(ctx context.Context, task assist.Task, text, model string) (string, error) {
	return h.s.Assistant.RunFunc()
}
func (h wsHost) Engines(ctx context.Context) map[string]heartmula.Availability {
	if h.s.EngineReport == nil {
		return nil
	}
	return h.s.EngineReport(ctx)
}
func (h wsHost) Tokens() workspace.TokenInfo { return workspace.TokenInfo{} }
func (h wsHost) WhisperDir() string          { return h.s.WhisperPath }
func (h wsHost) WhisperInstalled() bool {
	return h.s.Whisper != nil && h.s.Whisper()
}
func (h wsHost) BlenderStatus() workspace.BinaryStatus {
	if h.s.Blender == nil {
		return workspace.BinaryStatus{}
	}
	return h.s.Blender()
}

type modelsHost struct{ s *Stack }

func (h modelsHost) AssetsRoot() string                { return h.s.AssetsRoot }
func (h modelsHost) LookPath(n string) (string, error) { return h.s.Look(n) }
func (h modelsHost) Runner() runner.CommandRunner      { return h.s.MediaRunner }
func (h modelsHost) AnyBusy() (string, bool)           { return h.s.Workspace.AnyBusy() }
func (h modelsHost) ModelsIssue() error                { return nil }
func (h modelsHost) ShowsAdult() bool                  { return h.s.Adult }
func (h modelsHost) Catalog() []modelcat.Entry         { return h.s.Catalog }
func (h modelsHost) HuggingFaceKey() string            { return "" }
func (h modelsHost) DiffRhythm2CLI() (string, string)  { return "", "not installed" }
func (h modelsHost) VisionEngine() *musicsvc.Engine {
	if h.s.Vision == nil {
		return nil
	}
	return h.s.Vision()
}
func (h modelsHost) OverlayVision(a *musicsvc.Availability) {
	if h.s.OverlayVision != nil {
		h.s.OverlayVision(a)
	}
}

type assistantHost struct{ s *Stack }

func (h assistantHost) ProjectDir(id string) string       { return h.s.Workspace.ProjectDir(id) }
func (h assistantHost) ProjectVisible(string) bool        { return true }
func (h assistantHost) SourcesDir(id string) string       { return h.s.Workspace.SourcesDir(id) }
func (h assistantHost) AnyBusy() (string, bool)           { return h.s.Workspace.AnyBusy() }
func (h assistantHost) Runner() runner.CommandRunner      { return h.s.MediaRunner }
func (h assistantHost) AssetsRoot() string                { return h.s.AssetsRoot }
func (h assistantHost) LookPath(n string) (string, error) { return h.s.Look(n) }
func (h assistantHost) Store() *store.Store               { return h.s.Store }
func (h assistantHost) DropsAvailability(f http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f(w, r)
		h.s.Workspace.DropAvailability()
	}
}

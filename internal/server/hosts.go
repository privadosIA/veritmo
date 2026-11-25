package server

// hosts.go: the adapters that give each family package (internal/server/<family>) the services of the server it
// needs, and nothing else.

import (
	"context"
	"time"
	"veritmo/internal/assistant/assist"
	"veritmo/internal/music/heartmula"
	"veritmo/internal/music/musicmotion"
	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/platform/sched"
	"veritmo/internal/platform/store"
	"veritmo/internal/project/config"
	"veritmo/internal/project/pipeline"
	"veritmo/internal/prompts"
	"veritmo/internal/server/assistant"
	"veritmo/internal/server/generation"
	"veritmo/internal/server/modelcat"
	"veritmo/internal/server/models"
	"veritmo/internal/server/workspace"
	"veritmo/internal/video/motion"
	"veritmo/internal/video/videoedit"

	"net/http"

	"veritmo/internal/platform/runner"
)

// assistantHost is what the assistant family sees of the server.
type assistantHost struct{ s *Server }

func (h assistantHost) ProjectDir(id string) string       { return h.s.projectDir(id) }
func (h assistantHost) ProjectVisible(id string) bool     { return h.s.projectVisible(id) }
func (h assistantHost) SourcesDir(id string) string       { return h.s.sourcesDir(id) }
func (h assistantHost) AnyBusy() (string, bool)           { return h.s.workspace.AnyBusy() }
func (h assistantHost) Runner() runner.CommandRunner      { return h.s.mediaRunner }
func (h assistantHost) AssetsRoot() string                { return h.s.assetsRoot }
func (h assistantHost) Store() *store.Store               { return h.s.workspace.Store() }
func (h assistantHost) LookPath(n string) (string, error) { return h.s.look(n) }
func (h assistantHost) DropsAvailability(f http.HandlerFunc) http.HandlerFunc {
	return h.s.dropsAvailability(f)
}

// workspaceHost is what the workspace family (projects and runs) sees of the server.
type workspaceHost struct{ s *Server }

func (h workspaceHost) AssetsRoot() string                   { return h.s.assetsRoot }
func (h workspaceHost) LookPath(n string) (string, error)    { return h.s.look(n) }
func (h workspaceHost) Runner() runner.CommandRunner         { return h.s.mediaRunner }
func (h workspaceHost) EnvRunner() runner.CommandRunner      { return h.s.envRunner }
func (h workspaceHost) Started() time.Time                   { return h.s.started }
func (h workspaceHost) VideoEdit() *videoedit.Catalog        { return h.s.videoedit }
func (h workspaceHost) Motion() pipeline.MotionEngine        { return musicmotion.NewParts(h.s.motionCat) }
func (h workspaceHost) MusicServices() *musicsvc.ServiceHost { return h.s.cfg.Services }

func (h workspaceHost) ProjectVisible(id string) bool         { return h.s.projectVisible(id) }
func (h workspaceHost) RunProjectVisible(project string) bool { return h.s.runProjectVisible(project) }
func (h workspaceHost) OnlyVisibleRunIDs(ids []string) []string {
	return h.s.onlyVisibleRunIDs(ids)
}
func (h workspaceHost) RefuseAdultConfig(w http.ResponseWriter, adult *bool) bool {
	return h.s.refuseAdultConfig(w, adult)
}
func (h workspaceHost) ShowsAdult() bool { return h.s.content == ContentAll }
func (h workspaceHost) ContentFilter() (bool, bool) {
	return h.s.content != ContentAll, h.s.content == ContentStrict
}
func (h workspaceHost) ContentMode() string { return string(h.s.content) }

func (h workspaceHost) Catalog() []modelcat.Entry { return catalog }
func (h workspaceHost) MachineOf(gpus []workspace.GPUStatus) (int, float64) {
	m := generation.MachineOf(gpus, 0)
	return m.VRAMMB, m.Speed
}

func (h workspaceHost) Preflight(cfg *config.Config) *workspace.LaunchError {
	return h.s.music.ContainerPreflight(cfg)
}
func (h workspaceHost) AssistRun() func(ctx context.Context, task assist.Task, text, model string) (string, error) {
	return h.s.assistant.RunFunc()
}
func (h workspaceHost) Engines(ctx context.Context) map[string]heartmula.Availability {
	return h.s.music.Engines(ctx)
}
func (h workspaceHost) Tokens() workspace.TokenInfo {
	return workspace.TokenInfo{
		HuggingFace: h.s.models.HFToken() != "", Civitai: h.s.models.CivitaiToken() != "",
		HuggingFaceSource: keyServices["huggingface"].source(), CivitaiSource: keyServices["civitai"].source(),
	}
}
func (h workspaceHost) WhisperDir() string     { return h.s.music.WhisperDir() }
func (h workspaceHost) WhisperInstalled() bool { return h.s.music.WhisperInstalled() }
func (h workspaceHost) BlenderStatus() workspace.BinaryStatus {
	if h.s.blenderStatus != nil {
		return h.s.blenderStatus()
	}
	return h.s.generation.BlenderStatus()
}

// modelsHost is what the models family (assets, downloads, Civitai, the vision service) sees of the server.
type modelsHost struct{ s *Server }

func (h modelsHost) AssetsRoot() string                     { return h.s.assetsRoot }
func (h modelsHost) LookPath(n string) (string, error)      { return h.s.look(n) }
func (h modelsHost) Runner() runner.CommandRunner           { return h.s.mediaRunner }
func (h modelsHost) AnyBusy() (string, bool)                { return h.s.workspace.AnyBusy() }
func (h modelsHost) ModelsIssue() error                     { return h.s.workspace.ModelsIssue() }
func (h modelsHost) ShowsAdult() bool                       { return h.s.content == ContentAll }
func (h modelsHost) Catalog() []modelcat.Entry              { return catalog }
func (h modelsHost) HuggingFaceKey() string                 { return keyServices["huggingface"].effective() }
func (h modelsHost) DiffRhythm2CLI() (string, string)       { return h.s.music.DiffRhythm2CLI() }
func (h modelsHost) VisionEngine() *musicsvc.Engine         { return h.s.music.VisionEngine() }
func (h modelsHost) OverlayVision(a *musicsvc.Availability) { h.s.music.OverlayVision(a) }

// generationHost is what the generation family (the one-call API, the video-edit tools, the library of motions)
// sees of the server.
type generationHost struct{ s *Server }

func (h generationHost) Workspace() *workspace.Handler { return h.s.workspace }
func (h generationHost) Models() *models.Handler       { return h.s.models }
func (h generationHost) Assistant() *assistant.Handler { return h.s.assistant }

func (h generationHost) VideoEdit() *videoedit.Catalog { return h.s.videoedit }

func (h generationHost) MotionCatalog() musicmotion.Catalog { return h.s.motionCat }

func (h generationHost) ProjectsDir() string               { return h.s.cfg.ProjectsDir }
func (h generationHost) AssetsRoot() string                { return h.s.assetsRoot }
func (h generationHost) LookPath(n string) (string, error) { return h.s.look(n) }
func (h generationHost) Runner() runner.CommandRunner      { return h.s.mediaRunner }
func (h generationHost) EnvRunner() runner.CommandRunner   { return h.s.envRunner }
func (h generationHost) ProjectVisible(id string) bool     { return h.s.projectVisible(id) }
func (h generationHost) Catalog() []modelcat.Entry         { return catalog }
func (h generationHost) RefuseAdultConfig(w http.ResponseWriter, adult *bool) bool {
	return h.s.refuseAdultConfig(w, adult)
}
func (h generationHost) MotionWriter(ctx context.Context, provider string) (musicmotion.Writer, context.Context) {
	return h.s.music.MotionWriter(ctx, provider)
}

// musicHost is what the music family (songs, the music studio, the engines and their containers) sees of the server.
type musicHost struct{ s *Server }

func (h musicHost) Workspace() *workspace.Handler   { return h.s.workspace }
func (h musicHost) Models() *models.Handler         { return h.s.models }
func (h musicHost) Generation() *generation.Handler { return h.s.generation }
func (h musicHost) Assistant() *assistant.Handler   { return h.s.assistant }

func (h musicHost) ProjectsDir() string                { return h.s.cfg.ProjectsDir }
func (h musicHost) MotionCatalog() musicmotion.Catalog { return h.s.motionCat }
func (h musicHost) LookPath(n string) (string, error)  { return h.s.look(n) }
func (h musicHost) Runner() runner.CommandRunner       { return h.s.mediaRunner }
func (h musicHost) EnvRunner() runner.CommandRunner    { return h.s.envRunner }
func (h musicHost) Services() *musicsvc.ServiceHost    { return h.s.cfg.Services }
func (h musicHost) MemAvailable() sched.AvailableFunc  { return h.s.memAvailable }
func (h musicHost) MotionBrowser() func() (motion.Browser, error) {
	return h.s.motionBrowser
}
func (h musicHost) MotionWriter() func(ctx context.Context) (motion.Writer, motion.ModelInfo, error) {
	return h.s.motionWriter
}
func (h musicHost) DropsAvailability(f http.HandlerFunc) http.HandlerFunc {
	return h.s.dropsAvailability(f)
}

// promptsHost is what the store of prompt presets sees of the server: its database, and whether it shows adult content.
type promptsHost struct{ s *Server }

func (h promptsHost) ShowsAdult() bool       { return h.s.content == ContentAll }
func (h promptsHost) Presets() prompts.Store { return prompts.NewDB(h.s.store) }

// model3dHost is what the 3D models handler asks of the server (see library3d.Host).
type model3dHost struct{ s *Server }

func (h model3dHost) ShowsAdult() bool { return h.s.content == ContentAll }
func (h model3dHost) RefuseAdultConfig(w http.ResponseWriter, adult *bool) bool {
	return h.s.refuseAdultConfig(w, adult)
}
func (h model3dHost) ProjectVisible(id string) bool { return h.s.projectVisible(id) }
func (h model3dHost) PictureDefaults() (config.Defaults, error) {
	return h.s.generation.PictureDefaults()
}

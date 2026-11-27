// Package generation is the family of the one-call generation API: a video or a picture edit (POST /api/generations,
// /api/image-edits, /api/video-edits), the plan that picks the models for a machine, the inputs a request may bring,
// the video-edit tools with their library of motions, the Blender scenes and the saved styles of music motion.
package generation

import (
	"context"
	"net/http"
	"veritmo/internal/server/api"
	"veritmo/internal/video/videoedit"

	"sync"
	"veritmo/internal/music/motionref"
	"veritmo/internal/music/musicmotion"
	"veritmo/internal/platform/runner"
	"veritmo/internal/server/assistant"
	"veritmo/internal/server/modelcat"
	"veritmo/internal/server/models"
	"veritmo/internal/server/workspace"
)

// Host is what the family needs from the server beyond the families below it.
type Host interface {
	// Workspace, Models and Assistant are the families below this one.
	Workspace() *workspace.Handler
	Models() *models.Handler
	Assistant() *assistant.Handler

	// ProjectsDir is the root of the projects; AssetsRoot the directory holding models/ and voices/; LookPath finds
	// a program on the PATH; Runner runs ffmpeg and the other commands, EnvRunner the read-only queries.
	ProjectsDir() string
	AssetsRoot() string
	LookPath(name string) (string, error)
	Runner() runner.CommandRunner
	EnvRunner() runner.CommandRunner
	// VideoEdit is the catalog of the video-edit tools: the code behind them, the services they ask and how the
	// machine answers.
	VideoEdit() *videoedit.Catalog

	// ProjectVisible says whether the content mode lets the caller see a project; RefuseAdultConfig answers 422 when
	// a request would label a project adult on a server that hides them.
	ProjectVisible(id string) bool
	RefuseAdultConfig(w http.ResponseWriter, adult *bool) bool
	// MotionWriter is the writer of the assistant for the proposals of music motion (nil when no model is
	// available), with the context carrying the provider.
	MotionWriter(ctx context.Context, provider string) (musicmotion.Writer, context.Context)
	// MotionCatalog is the catalog of parts of the music motion engine (the parts that come with the program and the user's).
	MotionCatalog() musicmotion.Catalog
	// Catalog is the embedded catalog of recommended models.
	Catalog() []modelcat.Entry
}

// Handler serves the endpoints of the family.
type Handler struct {
	host      Host
	ws        *workspace.Handler
	models    *models.Handler
	assistant *assistant.Handler

	// GenCatalog and GenMachine replace the installed catalog and the machine the generation API plans with (test
	// seams); nil reads the disk and the cards.
	GenCatalog func() []modelcat.Entry
	GenMachine func() planMachine

	// BlenderProbeFn replaces what Blender says about itself (a test seam).
	blenderMemo    blenderCache
	blenderProbeFn func(ctx context.Context) blenderProbe

	// probeAudio reads an audio file (length, has sound); tests replace it.
	probeAudio func(ctx context.Context, path string) (seconds float64, hasAudio bool, err error)

	library   libraryState
	presets   *motionref.PresetStore
	presetsMu sync.Mutex
}

// New makes the handler of the family on top of the host.
func New(host Host) *Handler {
	return &Handler{host: host, ws: host.Workspace(), models: host.Models(), assistant: host.Assistant(), probeAudio: ffprobeAudio}
}

// Register registers the endpoints of the family.
func (h *Handler) Register(handle api.HandleFunc) {
	// video-generation-api and image-edit-api
	handle("GET /api/generation/options", h.handleGenerationOptions)
	handle("POST /api/generations", h.handleGenerationCreate)
	handle("GET /api/generations", h.handleGenerationsList)
	handle("GET /api/generations/{id}", h.handleGenerationGet)
	handle("PATCH /api/generations/{id}", h.handleGenerationPatch)
	handle("DELETE /api/generations/{id}", h.handleGenerationDelete)
	handle("POST /api/image-edits", h.handleImageEditCreate)
	handle("GET /api/image-edits", h.handleImageEditList)
	handle("GET /api/image-edits/{id}", h.handleImageEditGet)
	handle("PATCH /api/image-edits/{id}", h.handleImageEditPatch)
	handle("DELETE /api/image-edits/{id}", h.handleImageEditDelete)
	handle("GET /api/video-edits/options", h.handleVideoEditOptions)
	handle("POST /api/video-edits", h.handleVideoEditCreate)
	handle("GET /api/video-edits", h.handleVideoEditList)
	handle("GET /api/video-edits/{id}", h.handleVideoEditGet)
	handle("PATCH /api/video-edits/{id}", h.handleVideoEditPatch)
	handle("DELETE /api/video-edits/{id}", h.handleVideoEditDelete)
	handle("GET /api/motions", h.handleLibraryMotionList)
	handle("POST /api/motions", h.handleLibraryMotionCreate)
	handle("GET /api/motions/{id}", h.handleLibraryMotionGet)
	handle("PATCH /api/motions/{id}", h.handleLibraryMotionPatch)
	handle("DELETE /api/motions/{id}", h.handleLibraryMotionDelete)
	handle("GET /api/motions/{id}/preview", h.handleLibraryMotionPreview)

	// music-motion: the saved styles
	handle("POST /api/music-motion/presets", h.handlePresetCreate)
	handle("GET /api/music-motion/presets", h.handlePresetList)
	handle("GET /api/music-motion/presets/{id}", h.handlePresetGet)
	handle("PUT /api/music-motion/presets/{id}", h.handlePresetPut)
	handle("DELETE /api/music-motion/presets/{id}", h.handlePresetDelete)
}

// BlenderStatus is Blender as an optional binary of the environment report.
func (h *Handler) BlenderStatus() workspace.BinaryStatus { return h.blenderStatus() }

// ClipMaker is the engines of this machine as the `extend` video-edit tool uses them to make its new clip.
func (h *Handler) ClipMaker() videoedit.ClipMaker { return clipBridge{h: h} }

// Library is the library of motions as motion control uses it.
func (h *Handler) Library() videoedit.Library { return libraryBridge{h: h} }

// Machine is the machine a request is planned for.
type Machine = planMachine

// Package music is the family of songs and the music studio: the music-motion API and the drafts of its parts, the
// waveform of a song and its analysis, listening, dictation, loudness, the recognition of a song (Whisper, CLAP and
// MuQ), the music generators HeartMuLa and DiffRhythm2 and the sound classifier CLAP as engines with the start and
// stop of their containers, and the vision service engine of the video-edit tools.
package music

import (
	"context"
	"net/http"
	"time"
	"veritmo/internal/assets3d"

	"veritmo/internal/music/heartmula"
	"veritmo/internal/music/musicmotion"
	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/platform/runner"
	"veritmo/internal/platform/sched"
	"veritmo/internal/project/config"
	"veritmo/internal/server/api"
	"veritmo/internal/server/assistant"
	"veritmo/internal/server/generation"
	"veritmo/internal/server/models"
	"veritmo/internal/server/workspace"
	"veritmo/internal/video/motion"
)

// Host is what the family needs from the server beyond the families below it.
type Host interface {
	// Workspace, Models, Generation and Assistant are the families below this one.
	Workspace() *workspace.Handler
	Models() *models.Handler
	Generation() *generation.Handler
	Assistant() *assistant.Handler

	// ProjectsDir is the root of the projects; LookPath finds a program on the PATH; Runner runs ffmpeg and the
	// other commands, EnvRunner the read-only queries of the environment report.
	ProjectsDir() string
	LookPath(name string) (string, error)
	Runner() runner.CommandRunner
	EnvRunner() runner.CommandRunner
	// Services says where the .NET services are and runs them.
	Services() *musicsvc.ServiceHost

	// MemAvailable reads the machine's free memory (nil: /proc/meminfo).
	MemAvailable() sched.AvailableFunc
	// Seams of the motion graphics: the browser that draws them, the model that writes their pages and the function
	// that writes and checks one; each is nil when the real thing is used.
	MotionBrowser() func() (motion.Browser, error)
	MotionWriter() func(ctx context.Context) (motion.Writer, motion.ModelInfo, error)
	// MotionCatalog is the catalog of parts of the music motion engine (the parts that come with the program and the user's).
	MotionCatalog() musicmotion.Catalog

	// DropsAvailability wraps a handler that changes what the engines report.
	DropsAvailability(h http.HandlerFunc) http.HandlerFunc
}

// Handler serves the endpoints of the family. The exported function fields are test seams: nil uses the real thing.
type Handler struct {
	host Host
	// models3d is the library of 3D model packs (nil until the server gives it): a model of it can be added to a piece.
	models3d *assets3d.Library
	// whisperDir is the folder of the Whisper models (see SetWhisperDir).
	whisperDir string
	// demucsDir is the folder of the weights of the voice separator (see SetDemucsDir).
	demucsDir string
	// peaks and loudness remember what the waveform and the loudness of a file said while the file does not change.
	peaks    peaksCache
	loudness loudnessCache
	// proposalPoll and proposalMaxWait are how a proposal in the background waits for the runs to end (zero: 2 seconds
	// and 15 minutes); tests shorten them.
	proposalPoll, proposalMaxWait time.Duration
	ws                            *workspace.Handler
	models                        *models.Handler
	gen                           *generation.Handler
	assistant                     *assistant.Handler

	// engines caches GET /api/music/engines. HeartMulaRunner runs nvidia-smi for it (nil uses the environment
	// runner); HeartMulaWeights, ClapWeights, DiffRhythm2Weights and VideoVisionWeights are where the weights are
	// (empty: the default under models/). Test seams.
	engines            engineCache
	containers         containerCtl
	installs           installJobs
	heartMulaRunner    runner.CommandRunner
	heartMulaWeights   string
	clapWeights        string
	diffRhythm2Weights string
	videoVisionWeights string

	// hfDir and hfLibs are where the HyperFrames blocks and their libraries are kept (empty: HYPERFRAMES_DIR or hyperframes/, and
	// third_party/hyperframes-libs). Test seams.
	hfDir, hfLibs string

	proposals proposalJobs
	sceneJobs sceneJobs
	drafts    draftState
	learnings learningState
	// draftDescriber describes a picture for a draft (nil: the routed vision model). A test seam.
	draftDescriber   func(ctx context.Context, picture []byte) (string, error)
	draftVisionCheck func(ctx context.Context) string
	// reviewVisionSeam is the vision model that reviews drafts and pieces (nil: the one the check task is routed to). A test seam.
	reviewVisionSeam func(ctx context.Context) (musicmotion.Vision, error)
}

// New makes the handler of the family on top of the host.
func New(host Host) *Handler {
	h := &Handler{host: host, ws: host.Workspace(), models: host.Models(), gen: host.Generation(), assistant: host.Assistant(), whisperDir: defaultWhisperDir, demucsDir: defaultDemucsDir}
	h.ws.OnStorageChanged(h.pieceStorageChanged)
	h.MarkInterrupted()
	h.draftStore() // opens the drafts and marks the ones that were being written when the server stopped
	h.learningStore()
	h.assistant.RefStore() // the same for the learnings that were running and the references that were being analysed
	return h
}

// Register registers the endpoints of the family.
func (h *Handler) Register(handle api.HandleFunc) {
	// music-motion: the pieces and the drafts of their parts
	// hyperframes: the catalog of whole blocks
	handle("GET /api/hyperframes/blocks", h.handleHFBlocks)
	handle("POST /api/hyperframes/blocks/import", h.handleHFImport)
	handle("GET /api/hyperframes/blocks/{name}", h.handleHFBlockGet)
	handle("PUT /api/hyperframes/blocks/{name}", h.handleHFBlockPut)
	handle("DELETE /api/hyperframes/blocks/{name}", h.handleHFBlockDelete)
	handle("GET /api/hyperframes/blocks/{name}/thumbnail", h.handleHFBlockThumbnail)
	handle("GET /api/hyperframes/blocks/{name}/frame", h.handleHFBlockFrame)
	handle("POST /api/hyperframes/blocks/{name}/preview", h.handleHFBlockPreview)
	handle("POST /api/hyperframes/blocks/{name}/check", h.handleHFBlockCheck)
	handle("POST /api/hyperframes/blocks/{name}/render", h.handleHFBlockRender)
	handle("GET /api/hyperframes/clips/{file}", h.handleHFClip)
	handle("GET /api/music-motion/catalog", h.handleMusicMotionCatalog)
	handle("POST /api/assist/plan", h.handleAssistPlan)
	handle("POST /api/assist/dataset", h.handleAssistDataset)
	handle("GET /api/music-motion/parts", h.handleMusicMotionPartsList)
	handle("POST /api/music-motion/parts", h.handleMusicMotionPartPost)
	handle("GET /api/music-motion/parts/{name}", h.handleMusicMotionPartGet)
	handle("PUT /api/music-motion/parts/{name}", h.handleMusicMotionPartPut)
	handle("DELETE /api/music-motion/parts/{name}", h.handleMusicMotionPartDelete)
	handle("POST /api/music-motion/pieces", h.handleMusicMotionCreate)
	handle("GET /api/music-motion/pieces", h.handleMusicMotionList)
	handle("GET /api/music-motion/pieces/{id}", h.handleMusicMotionGet)
	handle("PUT /api/music-motion/pieces/{id}/design", h.handleMusicMotionSetDesign)
	handle("PUT /api/music-motion/pieces/{id}/script", h.handleMusicMotionSetScript)
	handle("POST /api/music-motion/pieces/{id}/script/{piece}/blender", h.handlePieceBlenderWrite)
	handle("POST /api/music-motion/pieces/{id}/script/{piece}/blender/frame", h.handlePieceBlenderFrame)
	handle("GET /api/music-motion/pieces/{id}/models", h.handlePieceModelsList)
	handle("POST /api/music-motion/pieces/{id}/models", h.handlePieceModelAdd)
	handle("PUT /api/music-motion/pieces/{id}/models/{name}", h.handlePieceModelPut)
	handle("DELETE /api/music-motion/pieces/{id}/models/{name}", h.handlePieceModelDelete)
	handle("POST /api/music-motion/pieces/{id}/preview", h.handleMusicMotionPreview)
	handle("GET /api/music-motion/pieces/{id}/frame", h.handleMusicMotionFrame)
	handle("GET /api/music-motion/library/characters", h.handleLibraryCharacters)
	handle("POST /api/music-motion/library/characters", h.libraryPost(characterKind()))
	handle("PUT /api/music-motion/library/characters/{name}", h.libraryPut(characterKind()))
	handle("DELETE /api/music-motion/library/characters/{name}", h.libraryDelete(characterKind()))
	handle("GET /api/music-motion/library/backgrounds", h.handleLibraryBackgrounds)
	handle("POST /api/music-motion/library/backgrounds", h.libraryPost(backgroundKind()))
	handle("PUT /api/music-motion/library/backgrounds/{name}", h.libraryPut(backgroundKind()))
	handle("DELETE /api/music-motion/library/backgrounds/{name}", h.libraryDelete(backgroundKind()))
	handle("GET /api/music-motion/library/characters/{name}", h.handleLibraryCharacter)
	handle("GET /api/music-motion/library/backgrounds/{name}", h.handleLibraryBackground)
	handle("POST /api/music-motion/pieces/{id}/propose", h.handleMusicMotionPropose)
	handle("POST /api/music-motion/pieces/{id}/review", h.handlePieceReview)
	handle("POST /api/music-motion/pieces/{id}/scenes", h.handleScenesStart)
	handle("DELETE /api/music-motion/pieces/{id}/scenes", h.handleScenesCancel)
	handle("POST /api/music-motion/pieces/{id}/scenes/approve", h.handleScenesApprove)
	handle("POST /api/music-motion/pieces/{id}/render", h.handleMusicMotionRender)
	handle("POST /api/music-motion/pieces/{id}/transcribe", h.handleMusicMotionTranscribe)
	handle("PATCH /api/music-motion/pieces/{id}/words", h.handleMusicMotionWords)
	handle("DELETE /api/music-motion/pieces/{id}", h.handleMusicMotionDelete)
	handle("PATCH /api/music-motion/pieces/{id}", h.handleMusicMotionPatch)
	handle("POST /api/music-motion/part-drafts", h.handleDraftCreate)
	handle("GET /api/music-motion/part-drafts", h.handleDraftList)
	handle("GET /api/music-motion/part-drafts/{id}", h.handleDraftGet)
	handle("PUT /api/music-motion/part-drafts/{id}", h.handleDraftPut)
	handle("DELETE /api/music-motion/part-drafts/{id}", h.handleDraftDelete)
	handle("POST /api/music-motion/part-drafts/{id}/retry", h.handleDraftRetry)
	handle("POST /api/music-motion/part-drafts/{id}/approve", h.handleDraftApprove)
	handle("GET /api/music-motion/part-drafts/{id}/preview/{name}", h.handleDraftPreview)
	handle("POST /api/music-motion/references/{id}/learn", h.handleLearn)
	handle("GET /api/music-motion/learnings", h.handleLearningList)
	handle("GET /api/music-motion/learnings/{id}", h.handleLearningGet)
	handle("DELETE /api/music-motion/learnings/{id}", h.handleLearningDelete)

	// music: the waveform of a song and its analysis
	handle("GET /api/projects/{id}/peaks", h.handlePeaks)
	handle("POST /api/projects/{id}/transcribe", h.handleTranscribe)
	handle("POST /api/transcribe", h.handleDictation)
	handle("POST /api/assist/listen", h.handleListenSong)
	handle("GET /api/music/recognizers", h.handleRecognizers)
	handle("POST /api/projects/{id}/recognize", h.handleRecognize)
	handle("POST /api/projects/{id}/measure", h.handleMeasure)
	handle("GET /api/projects/{id}/loudness", h.handleLoudness)
	handle("DELETE /api/projects/{id}/songs/{track}/{variant}", h.handleSongDelete)

	// heartmula-engine
	handle("GET /api/music/engines", h.handleMusicEngines)
	handle("POST /api/music/engines/{engine}/start", h.handleEngineStart)
	handle("POST /api/music/engines/{engine}/stop", h.handleEngineStop)
	handle("POST /api/music/engines/{engine}/install", h.handleEngineInstall)
	handle("GET /api/music/installs", h.handleInstallsList)
	handle("GET /api/music/installs/{id}", h.handleInstallGet)
	handle("POST /api/music/installs/{id}/cancel", h.handleInstallCancel)
	handle("GET /api/music/installs/{id}/events", h.handleInstallEvents)
	handle("POST /api/settings/heartmula/test", h.handleHeartMulaTest)
	// diffrhythm2-engine
	handle("POST /api/settings/diffrhythm2/test", h.handleDiffRhythm2Test)
}

// Engines is what the engines report (the environment report and the metrics read it).
func (h *Handler) Engines(ctx context.Context) map[string]heartmula.Availability {
	return h.enginesAnswer(ctx).Engines
}

// BumpEngines drops the cached answer of the engines (the settings or the providers changed).
func (h *Handler) BumpEngines() { h.engines.bump() }

// ContainerPreflight is the check a launch adds: a music generator whose container is stopped cannot be used.
func (h *Handler) ContainerPreflight(cfg *config.Config) *workspace.LaunchError {
	return h.containerPreflight(cfg)
}

// DiffRhythm2CLI is the diffrhythm2 executable published next to the service, or why it is not there.
func (h *Handler) DiffRhythm2CLI() (exe, why string) { return h.diffRhythm2CLI() }

// VisionEngine is the engine that runs the vision service of the video-edit tools.
func (h *Handler) VisionEngine() *musicsvc.Engine {
	eng, _ := h.engineOf(engineVideoVision, false)
	return eng
}

// OverlayVision adds to the availability of the vision service whether its container is up.
func (h *Handler) OverlayVision(a *musicsvc.Availability) { h.overlay(engineVideoVision, a) }

// MotionWriter is the writer of the assistant for the proposals of music motion (nil when no model is available).
func (h *Handler) MotionWriter(ctx context.Context, provider string) (musicmotion.Writer, context.Context) {
	return h.mmWriter(ctx, provider)
}

// WhisperDir is where the Whisper models are.
func (h *Handler) WhisperDir() string { return h.whisperDir }

// WhisperInstalled says whether a Whisper model is there.
func (h *Handler) WhisperInstalled() bool {
	model, _ := h.whisperModel()
	return model != ""
}

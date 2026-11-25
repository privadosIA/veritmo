// Package workspace is the family of the projects and of the runs that make them: the project API (list, create, edit,
// duplicate, delete), its sources, clips, library, recipe, importing and exporting, the run manager (the queue, the
// lanes, the history in the database), the reviews, the statistics, the time estimates and the calibration, the
// metrics and the monitoring, the storage, the environment report and the availability of each task. It also keeps
// what every family that creates a project for itself needs (the managed projects of the generation API and the
// label of who asked).
package workspace

import (
	"context"
	"net/http"
	"os"
	"sync"
	"time"
	"veritmo/internal/assets3d"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/musicsvc"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/music/heartmula"
	"veritmo/internal/platform/runner"
	"veritmo/internal/platform/settings"
	"veritmo/internal/platform/store"
	"veritmo/internal/project/config"
	"veritmo/internal/project/pipeline"
	"veritmo/internal/project/quality"
	"veritmo/internal/server/api"
	"veritmo/internal/server/modelcat"
	"veritmo/internal/video/blender"
	"veritmo/internal/video/motion"
	"veritmo/internal/video/videoedit"
)

// Config is what the family reads of the configuration of the server.
type Config struct {
	// ProjectsDir is the root directory whose subdirectories holding a scenes.yaml are the projects.
	ProjectsDir string
	// MediaDir, when set, is where whole projects are kept; ModelsDir where the model files live.
	MediaDir  string
	ModelsDir string
	// Store is the database of the data layer, opened and migrated by the server. When nil the handler opens the one DBPath and
	// Database name (what a test that builds a handler on its own does) and closes it when it stops.
	Store *store.Store
	// DBPath is the SQLite file of the run history, unless Database says another.
	DBPath string
	// Database is where the run history is kept and ServerName how this server is called in a shared one.
	Database   settings.Database
	ServerName string
	// Sampler says how the resources of an active run are read; the zero value reads the machine every 2 seconds.
	Sampler SamplerConfig
}

// Host is what the family needs from the server: the services of the other families and the values that tests
// replace after the server is built, so they are asked every time.
type Host interface {
	// AssetsRoot is the directory holding models/ and voices/; LookPath finds a program on the PATH; Runner runs
	// ffmpeg and the other external commands, EnvRunner the read-only queries of the environment report.
	AssetsRoot() string
	LookPath(name string) (string, error)
	Runner() runner.CommandRunner
	EnvRunner() runner.CommandRunner
	// VideoEdit is the catalog of video-edit tools a run of a video-edit project uses.
	VideoEdit() *videoedit.Catalog
	// Motion is the music motion engine: the catalog the parts of a scene are checked against and the builder of their
	// pages (nil when the program has none). MusicServices says where the .NET services of the music generators are.
	Motion() pipeline.MotionEngine
	MusicServices() *musicsvc.ServiceHost
	// Started is when the server was built.
	Started() time.Time

	// ProjectVisible says whether the content mode lets the caller see a project; RunProjectVisible the same for
	// the project of a run (a project that is gone is visible); OnlyVisibleRunIDs keeps the runs of visible
	// projects; RefuseAdultConfig answers 422 when a request would label a project adult on a server that hides
	// them; ShowsAdult says the server shows everything; ContentMode is its name.
	ProjectVisible(id string) bool
	RunProjectVisible(project string) bool
	OnlyVisibleRunIDs(ids []string) []string
	RefuseAdultConfig(w http.ResponseWriter, adult *bool) bool
	ShowsAdult() bool
	// ContentFilter says what the content mode keeps out of a list of projects: the ones marked for adults, and (the strict mode) every one that was
	// not marked as fine.
	ContentFilter() (hideAdult, onlyReviewed bool)
	ContentMode() string

	// Catalog is the model catalog; MachineOf says the memory (MB) and the speed (times the reference machine's)
	// of the best of the cards.
	Catalog() []modelcat.Entry
	MachineOf(gpus []GPUStatus) (vramMB int, speed float64)

	// Preflight is the check of a launch that the other families add (a music generator whose container is
	// stopped); nil means none. AssistRun is the text task of the prompt assistant.
	Preflight(cfg *config.Config) *LaunchError
	AssistRun() func(ctx context.Context, task assist.Task, text, model string) (string, error)

	// What the environment report and the metrics read of the other families.
	Engines(ctx context.Context) map[string]heartmula.Availability
	Tokens() TokenInfo
	WhisperDir() string
	WhisperInstalled() bool
	BlenderStatus() BinaryStatus
}

// Overrides are the test seams of the family that live in the server: nil uses the real thing. They are the hook of a
// test (Handler.Overrides); the server never sets them.
type Overrides struct {
	// ReviewVision builds the vision judge of a review analysis; VisionModels lists the vision-capable models.
	ReviewVision func(model string) quality.Vision
	VisionModels func(ctx context.Context) ([]string, error)
	// MotionBrowser finds the browser that draws motion graphics, MotionWriter builds the model that writes their
	// pages, MotionGenerate writes and checks one and MotionRevise changes the timeline of a stored one.
	MotionBrowser  func() (motion.Browser, error)
	MotionWriter   func(ctx context.Context) (motion.Writer, motion.ModelInfo, error)
	MotionGenerate func(ctx context.Context, w motion.Writer, browser string, r motion.Request, lim motion.Limits) (string, error)
	MotionRevise   func(ctx context.Context, w motion.Writer, browser, page, instruction string, r motion.Request, lim motion.Limits, view motion.Viewer) (string, error)
	// BlenderWrite writes and checks the description of a 3D scene.
	BlenderWrite func(ctx context.Context, w blender.Writer, r blender.WriteRequest) (blender.WriteResult, error)
	// BlenderInfo finds Blender and BlenderPreview draws a frame of a description with it.
	BlenderInfo    func(ctx context.Context) (blender.Info, error)
	BlenderPreview func(ctx context.Context, bin string, spec *blender.Spec, o blender.PreviewOptions) ([]byte, error)
	// StoryboardRun writes the pictures of a videoclip.
	StoryboardRun func(ctx context.Context, style, title string, shots []assist.ShotBrief, model string) (assist.Storyboard, error)
}

// TokenInfo says which account keys are set and where each comes from ("env", "file" or "").
type TokenInfo struct {
	HuggingFace, Civitai             bool
	HuggingFaceSource, CivitaiSource string
}

// Handler serves the endpoints of the family.
type Handler struct {
	cfg  Config
	host Host
	// models3d is the library of 3D model packs (nil until the server gives it): a model of it can be added to a project's sources.
	models3d *assets3d.Library

	rm *runManager
	rv *reviewManager
	// projects are the projects of the database: their definition, versions and pages.
	projects *projectStore
	// avail caches GET /api/models/availability (30 s); the server drops it when settings, providers or routing change.
	avail availabilityCache
	// songs are the songs, stars and playlists of the library, in the database.
	songs *songStore
	// busy reports the run id occupying a project (active or queued); nil means nothing is ever busy. A test seam.
	busy func(projectID string) (runID string, busy bool)

	// Overrides are the seams a test replaces after the Handler is built (see Overrides).
	Overrides Overrides

	// ownStore says the handler opened its database and closes it in Shutdown.
	ownStore bool

	// storageHooks are called when the storage of a project was cleaned (see OnStorageChanged).
	storageMu    sync.Mutex
	storageHooks []func(projectID string)

	// Execute, when set, runs the runs instead of the pipeline: a test seam of package server.
	Execute func(ctx context.Context, rn ExecRun, em *events.Emitter) (string, error)
}

// New builds the family on top of the host: it opens the run history, starts the run manager and the review manager.
func New(cfg Config, host Host) (*Handler, error) {
	ownStore := false
	if cfg.Store == nil {
		reg := store.NewRegistry()
		RegisterStore(reg)
		st, err := store.Open(cfg.DBPath, cfg.Database, reg)
		if err != nil {
			return nil, err
		}
		if err := st.Migrate(); err != nil {
			_ = st.Close()
			return nil, err
		}
		cfg.Store, ownStore = st, true
	}
	hist, err := openHistoryOn(cfg.Store, cfg.ServerName)
	if err != nil {
		if ownStore {
			_ = cfg.Store.Close()
		}
		return nil, err
	}
	h := &Handler{cfg: cfg, host: host, ownStore: ownStore, projects: newProjectStore(cfg.Store), songs: newSongStore(cfg.Store)}
	h.songs.measure = h.measureSong
	h.projects.onDelete = append(h.projects.onDelete, h.songs.deleteProject)
	suffix := newInstanceSuffix(cfg.Database.IsRemote())
	h.rm = newRunManager(hist, cfg.ProjectsDir, cfg.Sampler)
	h.rm.browserCheck = func() error { _, err := h.MotionBrowserFor(); return err } // the same browser the pages are drawn with, and the one a test stands in for
	h.rm.idSuffix = suffix
	h.rm.projects = h.projects
	h.rm.afterIngest = h.syncLibraryQuietly
	h.rm.measure = func(id string) (int64, bool) {
		if _, err := os.Stat(h.ProjectDir(id)); err != nil {
			return 0, false
		}
		_, total := h.projectStorage(id)
		return total, true
	}
	h.rm.catalog = host.Catalog
	pipelineExecute := h.rm.execute
	h.rm.execute = func(ctx context.Context, rn *run, em *events.Emitter) (string, error) {
		if h.Execute != nil {
			return h.executeWith(ctx, rn, em)
		}
		return pipelineExecute(ctx, rn, em)
	}
	h.rm.preflight = host.Preflight
	if !host.ShowsAdult() {
		h.rm.hist.visible = host.RunProjectVisible
	}
	h.rm.hist.modeOf = func(p string) string { return string(h.projectMode(p)) }
	h.rm.gpuName = h.runGPUName
	h.rm.lookPath = host.LookPath
	h.rm.videoEdit = host.VideoEdit()
	h.rm.motion = host.Motion()
	h.rm.services = host.MusicServices()
	h.busy = h.rm.projectBusy
	h.rv = newReviewManager(h.rm.rootCtx)
	h.rv.idSuffix = suffix
	return h, nil
}

// Store is the database of the process (the one the handler was given, or opened).
func (h *Handler) Store() *store.Store { return h.cfg.Store }

// Parts is the catalog the parts of a scene are checked against: what a configuration is given before it is validated.
func (h *Handler) Parts() config.PartCatalog {
	if m := h.host.Motion(); m != nil {
		return m
	}
	return nil
}

// validateInDir validates a configuration as the server does: against its catalog of parts.
func (h *Handler) validateInDir(cfg *config.Config, dir string) error {
	cfg.Parts = h.Parts()
	return cfg.ValidateInDir(dir)
}

// Register registers the endpoints of the family.
func (h *Handler) Register(handle api.HandleFunc) {
	// prometheus-metrics
	handle("GET /api/metrics", h.handleMetrics)

	// artifact-access (read-only by design)
	handle("GET /api/projects/{id}/artifacts/{path...}", h.handleArtifact)

	// project-management
	handle("GET /api/projects", h.handleProjectsList)
	handle("GET /api/projects/{id}", h.handleProjectGet)
	handle("PATCH /api/projects/{id}", h.handleProjectPatch)
	handle("GET /api/projects/{id}/versions", h.handleProjectVersions)
	handle("GET /api/projects/{id}/versions/{version}", h.handleProjectVersionGet)
	handle("POST /api/projects/{id}/versions/{version}/restore", h.handleProjectVersionRestore)
	handle("POST /api/projects", h.handleProjectCreate)
	handle("POST /api/projects/{id}/duplicate", h.handleProjectDuplicate)
	handle("GET /api/projects/{id}/export", h.handleProjectExport)

	// project-review
	handle("GET /api/review/vision-models", h.handleVisionModels)
	handle("POST /api/projects/{id}/reviews", h.handleReviewStart)
	handle("GET /api/projects/{id}/reviews", h.handleReviewList)
	handle("GET /api/reviews/{rid}", h.handleReviewGet)
	handle("DELETE /api/reviews/{rid}", h.handleReviewDelete)
	handle("POST /api/reviews/delete", h.handleReviewsDelete)
	handle("POST /api/reviews/{rid}/findings", h.handleFindingCreate)
	handle("PATCH /api/reviews/{rid}/findings/{fid}", h.handleFindingPatch)
	handle("GET /api/reviews/{rid}/findings/{fid}/frame", h.handleFindingFrame)
	handle("POST /api/reviews/{rid}/findings/{fid}/suggest", h.handleFindingSuggest)
	handle("POST /api/reviews/{rid}/tasks", h.handleReviewTasks)

	// project-management: the project, its configuration, sources, storage and clips
	handle("DELETE /api/projects/{id}", h.handleProjectDelete)
	handle("GET /api/projects/{id}/config", h.handleProjectConfigGet)
	handle("PUT /api/projects/{id}/config", h.handleProjectConfigPut)
	handle("GET /api/projects/{id}/cache", h.handleProjectCache)
	handle("GET /api/projects/{id}/sources", h.handleSourcesList)
	handle("POST /api/projects/{id}/sources", h.handleSourceCopy)
	handle("POST /api/projects/{id}/sources/from-url", h.handleSourceFromURL)
	handle("POST /api/projects/{id}/sources/clip", h.handleSourceClipCopy)
	handle("POST /api/projects/{id}/sources/model", h.handleSourceModel)
	handle("GET /api/clips", h.handleClipsList)
	handle("GET /api/storage", h.handleStorage)
	handle("GET /api/projects/{id}/storage", h.handleProjectStorage)
	handle("POST /api/projects/{id}/storage/clean", h.handleProjectStorageClean)
	handle("PUT /api/projects/{id}/sources/{name}", h.handleSourcePut)
	handle("DELETE /api/projects/{id}/sources/{name}", h.handleSourceDelete)

	// music-library
	handle("GET /api/library", h.handleLibraryGet)
	handle("PUT /api/library/meta", h.handleLibraryMetaPut)
	handle("GET /api/library/songs", h.handleLibrarySongs)
	handle("GET /api/library/songs/{key}", h.handleLibrarySong)
	handle("PUT /api/library/songs/{key}/rating", h.handleLibrarySongRating)
	handle("POST /api/library/rescan", h.handleLibraryRescan)
	handle("GET /api/library/playlists", h.handleLibraryPlaylists)
	handle("POST /api/library/playlists", h.handleLibraryPlaylistCreate)
	handle("GET /api/library/playlists/{id}", h.handleLibraryPlaylistGet)
	handle("PUT /api/library/playlists/{id}", h.handleLibraryPlaylistPut)
	handle("DELETE /api/library/playlists/{id}", h.handleLibraryPlaylistDelete)

	// project-import
	handle("POST /api/import/preview", h.handleImportPreview)
	handle("POST /api/import/confirm", h.handleImportConfirm)
	handle("POST /api/import/package/preview", h.handlePackagePreview)
	handle("POST /api/import/package/confirm", h.handlePackageConfirm)

	// run-control
	handle("POST /api/projects/{id}/runs", h.handleRunLaunch)
	handle("GET /api/runs", h.handleRunsList)
	handle("GET /api/runs/{id}", h.handleRunGet)
	handle("DELETE /api/runs/{id}", h.handleRunDelete)
	handle("GET /api/runs/{id}/resources", h.handleRunResources)
	handle("GET /api/stats", h.handleStats)
	handle("GET /api/stats/phase-duration", h.handlePhaseDuration)
	handle("POST /api/estimate", h.handleEstimate)
	handle("POST /api/runs/delete", h.handleRunsDelete)
	handle("POST /api/runs/{id}/cancel", h.handleRunCancel)

	// run-event-streaming
	handle("GET /api/runs/{id}/events", h.handleRunEvents)

	// environment-status
	handle("GET /api/environment", h.handleEnvironment)
	handle("POST /api/environment/ollama/unload", h.handleOllamaUnload)

	// videoclip and motion-graphics
	handle("POST /api/projects/{id}/videoclip/plan", h.handleVideoclipPlan)
	handle("POST /api/projects/{id}/videoclip", h.handleVideoclipCreate)
	handle("GET /api/projects/{id}/scenes/{scene}/motion", h.handleMotionGet)
	handle("PUT /api/projects/{id}/scenes/{scene}/motion", h.handleMotionPut)
	handle("POST /api/projects/{id}/scenes/{scene}/motion/generate", h.handleMotionGenerate)
	handle("POST /api/projects/{id}/scenes/{scene}/motion/revise", h.handleMotionRevise)

	// 3D scene descriptions written by a model (blender-scene-writer)
	handle("POST /api/projects/{id}/blender/descriptions", h.handleDescriptionWrite)
	handle("GET /api/projects/{id}/blender/descriptions/{name}", h.handleDescriptionGet)
	handle("PUT /api/projects/{id}/blender/descriptions/{name}", h.handleDescriptionPut)
	handle("POST /api/projects/{id}/blender/descriptions/{name}/preview", h.handleDescriptionPreview)
	// ... and the same without a project, storing nothing
	handle("POST /api/blender/descriptions", h.handleStatelessWrite)
	handle("POST /api/blender/preview", h.handleStatelessPreview)

	// data-store
	handle("GET /api/data/status", h.handleDataStatus)
	handle("POST /api/data/import", h.handleDataImport)
	handle("GET /api/data/export", h.handleDataExport)
	handle("POST /api/data/legacy/clean", h.handleDataLegacyClean)

	// database-choice
	handle("GET /api/settings/database", h.handleDatabaseGet)
	handle("PUT /api/settings/database", h.handleDatabasePut)
	handle("POST /api/settings/database/test", h.handleDatabaseTest)
	handle("POST /api/settings/database/copy", h.handleDatabaseCopy)

	// monitoring-settings
	handle("GET /api/settings/monitoring", h.handleMonitoringGet)
	handle("PUT /api/settings/monitoring/{tool}", h.handleMonitoringPut)
	handle("POST /api/settings/monitoring/{tool}/test", h.handleMonitoringTest)

	// model-availability
	handle("GET /api/models/availability", h.handleModelAvailability)
}

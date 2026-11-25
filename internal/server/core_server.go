// Package server implements the `veritmo serve` HTTP bridge: a local
// REST + SSE API over the existing pipeline, projects, artifacts and
// environment, plus, when it is given a directory (--web-dir), the static files of a front end. It never changes
// pipeline semantics — it drives internal/project/pipeline and re-emits its event stream verbatim.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"veritmo/internal/assets3d"
	"veritmo/internal/assets3d/generate"
	"veritmo/internal/music/hyperframes"
	"veritmo/internal/music/musicmotion"
	"veritmo/internal/platform/media"
	"veritmo/internal/project/config"
	"veritmo/internal/server/api"
	"veritmo/internal/video/videoedit"

	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/platform/runner"
	"veritmo/internal/platform/sched"
	"veritmo/internal/platform/settings"
	"veritmo/internal/platform/settingsdb"
	"veritmo/internal/platform/store"
	"veritmo/internal/server/assistant"
	"veritmo/internal/server/generation"
	"veritmo/internal/server/library3d"
	"veritmo/internal/server/models"
	"veritmo/internal/server/music"
	"veritmo/internal/server/prompts"
	"veritmo/internal/server/workspace"
	"veritmo/internal/video/motion"
)

// Config holds the serve subcommand's settings.
type Config struct {
	// Addr is the listen address. The subcommand defaults it to loopback;
	// the server itself does not re-default it.
	Addr string
	// ProjectsDir is the root directory whose subdirectories holding a
	// scenes.yaml are the managed projects.
	ProjectsDir string
	// MediaDir, when set, is where whole projects are kept (spec media-directory): the project folder holds links
	// into <MediaDir>/<id>. Empty keeps projects whole inside ProjectsDir.
	MediaDir string
	// ModelsDir, when set, is where the model files live (spec models-directory): models/ in the repository is a link
	// to it. Empty leaves models/ where it is.
	ModelsDir string
	// Store is the database of the process, opened and checked by the caller (veritmo serve does it, and refuses to start while
	// migrations are pending). When nil, New opens the one DBPath and Database name and applies its migrations: for tests and for an
	// embedder that has no deployment workflow.
	Store *store.Store
	// DBPath is the SQLite file for run history.
	DBPath string
	// Token, when non-empty, is required as a Bearer token on /api/*
	// (except /api/health and CORS preflights).
	Token string
	// DatabaseState makes everything in the process that is not an instance of this server use the database: the settings (once what was kept in
	// settings.json has been imported) and the catalog of HyperFrames blocks the music motion layers read. The program sets it; a test leaves it off
	// and those stay in the files they point to.
	DatabaseState bool
	// CORSOrigins is the exact-match allow-list of cross-origin callers.
	// Empty means same-origin only.
	CORSOrigins []string
	// ContentMode is which projects the server shows (see core_contentmode.go): empty means censored.
	// It is fixed here, when the server starts.
	ContentMode ContentMode
	// Services says where the .NET services of the music generators and CLAP, published in bin/services, are and runs
	// them (see musicsvc.ServiceHost).
	Services *musicsvc.ServiceHost
	// Database is where the run history is kept: empty means the SQLite file at DBPath. ServerName is how this
	// server is called in a history that several share. cmd/veritmo fills both from the settings; tests leave
	// them empty so they never touch a real database.
	Database   settings.Database
	ServerName string
	// Sampler says how the resources of an active run are read (the zero value reads the machine); tests give a quiet one.
	Sampler workspace.SamplerConfig
	// WebDir, when set, is a directory of static files (a built front end) served at the HTTP root. Empty serves a
	// plain-text note that this server has no front end.
	WebDir string
}

// Server is the HTTP bridge. Create it with New.
type Server struct {
	cfg Config
	// store is the one database of the process (see store.Store). ownStore says New opened it (and closes it when the server stops).
	store    *store.Store
	ownStore bool
	// settingsSrc is the Source of the settings New installed (nil: the settings stay in the file); releaseBlocks gives back the catalog of blocks it
	// installed for the music motion layers.
	settingsSrc   settings.Source
	releaseBlocks func()
	// motionCat is the catalog of parts of the music motion engine: the ones that come with the program and the user's, which the database keeps.
	motionCat musicmotion.Catalog
	// content is the normalized cfg.ContentMode.
	content ContentMode
	mux     *http.ServeMux
	// assistant serves the prompt assistant, the descriptions, the references and the providers; workspace the
	// projects and the runs.
	assistant  *assistant.Handler
	workspace  *workspace.Handler
	models     *models.Handler
	generation *generation.Handler
	music      *music.Handler
	models3d   *library3d.Handler
	prompts    *prompts.Handler

	// started is when the server was built, for its uptime.
	started time.Time

	// assetsRoot is the directory holding models/ and voices/ (the
	// process working directory by default; tests point it at a temp dir).
	assetsRoot string
	// cors decides which browser origins may call the API from another origin (core_cors.go).
	cors *corsPolicy

	auth *authenticator
	// patterns records every method+path route registered via handle, so
	// tests can assert that auth covers all of them.
	patterns []string

	// look resolves binaries (environment report and run preflight share
	// it); envRunner executes the report's read-only queries. Both are
	// test seams.
	look      func(string) (string, error)
	envRunner runner.CommandRunner
	// videoedit is the code behind the video-edit tools and the services they ask; the generation API and the runs of
	// video-edit projects use this one.
	videoedit *videoedit.Catalog
	// motionModelTimeout is how long the settings wait for the provider to say which models it has (zero: 4 s). A
	// field so the tests do not wait for a host that is not there.
	motionModelTimeout time.Duration
	// blenderStatus, when set, answers for Blender in the environment report instead of asking it (a test does not
	// start Blender to read its version).
	blenderStatus func() workspace.BinaryStatus
	// mediaRunner runs ffmpeg to turn an uploaded video into an audio source.
	// A test seam.
	mediaRunner runner.CommandRunner

	// memAvailable reads the machine's free memory; nil uses /proc/meminfo (tests set it).
	memAvailable sched.AvailableFunc
	// motionBrowser finds the browser that draws motion graphics and motionWriter builds the model that writes their pages,
	// for the music family (the proposals of music motion); nil uses this machine and the routed provider. Test seams.
	motionBrowser func() (motion.Browser, error)
	motionWriter  func(ctx context.Context) (motion.Writer, motion.ModelInfo, error)
}

// New builds a Server with all API routes registered, opening (and
// migrating) the run history database at cfg.DBPath.
func New(cfg Config) (*Server, error) {
	content, err := normalizeContentMode(cfg.ContentMode)
	if err != nil {
		return nil, err
	}
	if dir := filepath.Dir(cfg.DBPath); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("cannot create history dir: %w", err)
		}
	}
	if cfg.MediaDir != "" {
		abs, err := media.Check(cfg.MediaDir)
		if err != nil {
			return nil, err
		}
		cfg.MediaDir = abs
		for _, id := range media.Projects(cfg.ProjectsDir) {
			if p := media.Pending(filepath.Join(cfg.ProjectsDir, id), abs, id); len(p) > 0 {
				slog.Warn("a project still keeps files outside the media directory: run `veritmo media migrate`", "project", id, "files", strings.Join(p, ", "))
			}
		}
	}
	if cfg.ModelsDir != "" {
		abs, err := filepath.Abs(cfg.ModelsDir)
		if err != nil {
			return nil, fmt.Errorf("models directory %s: %w", cfg.ModelsDir, err)
		}
		cfg.ModelsDir = abs
		if st := media.CheckModels(".", abs); !st.Available || !st.Linked {
			slog.Warn("models directory", "dir", abs, "reason", st.Reason)
		}
	}
	// The one database of the process: the run history and every table of the data layer live in it. The program gives it opened and
	// checked (veritmo serve refuses to start while migrations are pending: the schema is changed by `veritmo data migrate`,
	// run by the deployment workflow). Without one (tests, an embedder with no workflow) New opens DBPath and migrates it, and closes it.
	st, ownStore := cfg.Store, false
	if st == nil {
		var err error
		if st, err = store.Open(cfg.DBPath, cfg.Database, DataRegistry(cfg.ProjectsDir, WithMediaDir(cfg.MediaDir))); err != nil {
			return nil, err
		}
		if err := st.Migrate(); err != nil {
			_ = st.Close()
			return nil, err
		}
		ownStore = true
	}
	// The state that was kept in files and has not been imported yet becomes rows before the first request. An import that fails is
	// recorded (GET /api/data/status) and does not stop the server.
	if _, err := st.RunImports(context.Background(), ""); err != nil {
		slog.Warn("data import could not run", "err", err)
	}
	// From here the settings are the ones of the database (they were imported just now): nothing before this point reads a setting.
	var settingsSrc settings.Source
	var releaseBlocks func()
	if cfg.DatabaseState {
		settingsSrc = settingsdb.New(st)
		settings.UseSource(settingsSrc)
		releaseBlocks = useBlockStore(st)
	}
	s := &Server{store: st, ownStore: ownStore, settingsSrc: settingsSrc, releaseBlocks: releaseBlocks, motionCat: musicmotion.NewCatalog(musicmotion.NewDBParts(st)), cors: newCORSPolicy(cfg.CORSOrigins), cfg: cfg, started: time.Now(), content: content, mux: http.NewServeMux(), auth: newAuthenticator(cfg.Token)}
	s.look = exec.LookPath
	s.envRunner = runner.NewExecRunner()
	s.videoedit = videoedit.NewCatalog()
	s.mediaRunner = runner.NewExecRunner()
	s.assistant = assistant.New(assistantHost{s})
	s.workspace, err = workspace.New(workspace.Config{
		Store: st, ProjectsDir: cfg.ProjectsDir, MediaDir: cfg.MediaDir, ModelsDir: cfg.ModelsDir, DBPath: cfg.DBPath,
		Database: cfg.Database, ServerName: cfg.ServerName, Sampler: cfg.Sampler,
	}, workspaceHost{s})
	if err != nil {
		settings.ReleaseSource(settingsSrc)
		if releaseBlocks != nil {
			releaseBlocks()
		}
		if ownStore {
			_ = st.Close()
		}
		return nil, err
	}
	s.models = models.New(modelsHost{s})
	s.models.SweepPartials()
	s.generation = generation.New(generationHost{s})
	s.music = music.New(musicHost{s})
	s.models3d = library3d.New(assets3d.New(assets3d.ResolveDir(os.Getenv), assets3d.DefaultPacks()))
	s.models3d.Library().Preview = func(ctx context.Context, sources string, description []byte, width int) ([]byte, error) {
		// a square frame of the model on its own: the settings of a scene of no project
		cfg := &config.Config{Defaults: config.Defaults{Resolution: "1024x1024", FPS: 30}}
		return s.workspace.PreviewDescriptionIn(ctx, cfg, sources, description, 0, 1, width)
	}
	s.models3d.SetHost(model3dHost{s})
	s.models3d.SetWorkspace(s.workspace)
	model3dService := NewModel3DService(s.envRunner, true, s.cfg.Services)
	s.models3d.SetService(model3dService)
	s.workspace.SetModelLibrary(s.models3d.Library())
	s.workspace.SetModelService(model3dService) // a run of a models3d project makes its models with it (it never starts the container: NoStart)
	s.music.SetModelLibrary(s.models3d.Library())
	s.prompts = prompts.New(promptsHost{s})
	s.routes()
	s.videoedit.SetVision(s.models.Vision())           // the tools that read a picture with a network ask this server's service
	s.videoedit.SetLibrary(s.generation.Library())     // `motion-control` takes the control of a motion of the library from here
	s.videoedit.SetClipMaker(s.generation.ClipMaker()) // `extend` makes its new clip with this machine's engines
	return s, nil
}

func (s *Server) handle(pattern string, h http.HandlerFunc) {
	s.patterns = append(s.patterns, pattern)
	s.mux.HandleFunc(pattern, api.LimitBody(s.gate(pattern, h)))
}

// routes registers every endpoint. API routes all live under /api/; the root belongs to the front end in WebDir, if
// the operator gave one.
func (s *Server) routes() {
	s.handle("GET /api/health", s.handleHealth)

	// api-documentation
	s.handle("GET /api/openapi.json", s.handleOpenAPI)

	// video-generation-api and image-edit-api, the video-edit tools and the library of motions, and the saved styles
	// of music motion
	s.generation.Register(s.handle)

	// music-motion, the waveform of a song and its analysis, recognition, dictation, loudness, and the engines
	// HeartMuLa, DiffRhythm2 and CLAP with the start and stop of their containers
	s.music.Register(s.handle)

	// model-library: the packs of 3D models, their installation, the catalog of their models and its thumbnails
	s.models3d.Register(s.handle)

	// api-access-control
	s.handle("POST /api/auth/ticket", s.handleAuthTicket)

	// prompt-presets
	s.prompts.Register(s.handle)

	// prompt-assistant, describe-reference, music-motion references and model-providers
	s.assistant.Register(s.handle)

	// projects and runs: their API, the reviews, the statistics, the environment, the metrics and the settings of the
	// database and the monitoring
	s.workspace.Register(s.handle)

	// ollama-settings
	s.handle("GET /api/settings", s.handleSettingsGet)
	s.handle("PUT /api/settings", s.dropsAvailability(s.handleSettingsPut))
	s.handle("POST /api/settings/ollama/test", s.handleOllamaTest)

	// ctxpress-settings
	s.handle("GET /api/settings/ctxpress", s.handleCtxpressGet)
	s.handle("PUT /api/settings/ctxpress", s.handleCtxpressPut)
	s.handle("POST /api/settings/ctxpress/test", s.handleCtxpressTest)

	// model-providers
	s.handle("POST /api/providers/{id}/acknowledge", s.dropsAvailability(s.handleProviderAcknowledge))

	// provider-routing
	s.handle("GET /api/routing", s.handleRoutingGet)
	s.handle("PUT /api/routing", s.dropsAvailability(s.handleRoutingPut))

	// model-management: the inventory of assets, the downloads and the Civitai lookup
	s.models.Register(s.handle)
	s.handle("PUT /api/tokens/{service}", s.handleTokenPut)
	s.handle("DELETE /api/tokens/{service}", s.handleTokenDelete)

	// Catch-all for /api/: shadows the root UI handler so unknown API
	// paths are a JSON 404 — and, because a method-less pattern beats the
	// mux's built-in 405 for wrong-method requests, it probes the other
	// methods itself to answer 405 with an Allow header when the path
	// exists under a different method.
	s.mux.HandleFunc("/api/", api.CatchAll(s.mux))

	// Everything outside /api/ belongs to the front end: the directory the operator gave, else a plain explanation.
	if s.cfg.WebDir != "" {
		s.mux.Handle("/", webDirHandler(s.cfg.WebDir))
	} else {
		s.mux.Handle("/", noFrontEndHandler())
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Handler returns the root http.Handler (used by tests and by Run). API
// requests go through a wrapper that turns the mux's own plain-text 404
// and 405 responses into the structured JSON error shape, so every API
// error is JSON regardless of which layer produced it.
func (s *Server) Handler() http.Handler {
	api := s.auth.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			s.mux.ServeHTTP(&jsonErrorWriter{ResponseWriter: w, r: r}, r)
			return
		}
		s.mux.ServeHTTP(w, r)
	}))
	return corsMiddleware(s.cors, api)
}

// Timeouts of the listener. They protect the server before any handler runs (a client that opens connections and
// sends its headers a byte at a time). There is no read or write timeout for the whole request: uploads of hundreds
// of MB over a slow link, event streams and downloads must go on.
const (
	readHeaderTimeout = 15 * time.Second
	idleTimeout       = 120 * time.Second
	maxHeaderBytes    = 64 << 10
)

// newHTTPServer returns the http.Server that serves h with the listener's timeouts.
func newHTTPServer(h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
}

// Run listens on cfg.Addr and serves until ctx is cancelled, then shuts
// down gracefully: in-flight requests get a short drain window, and the
// run queue (wired in a later task) is cancelled through ctx itself. A
// bind failure is returned immediately, naming the address.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("cannot listen on %s: %w", s.cfg.Addr, err)
	}

	srv := newHTTPServer(s.Handler())

	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		// Cancel the active run (recorded as interrupted) and wait for
		// its worker to persist that state before draining HTTP.
		s.workspace.Shutdown()
		settings.ReleaseSource(s.settingsSrc)
		if s.releaseBlocks != nil {
			s.releaseBlocks()
		}
		if s.ownStore {
			_ = s.store.Close()
		}
		s.models.Shutdown()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// Addr returns the configured listen address.
func (s *Server) Addr() string { return s.cfg.Addr }

// useBlockStore makes the layers of the music motion mode read the HyperFrames blocks from the database st, and returns what gives back the catalog they had.
func useBlockStore(st *store.Store) (release func()) {
	prev := musicmotion.BlockStore
	dir := hyperframes.ResolveDir(os.Getenv)
	musicmotion.BlockStore = func() hyperframes.Store { return hyperframes.NewStoreDB(dir, st) }
	return func() { musicmotion.BlockStore = prev }
}

// absOr is the absolute path of a relative one (the same text when the working folder cannot be read).
func absOr(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// NewModel3DService is the 3D generator service of this machine: its container, its weights in models/model3d and the Hugging Face key saved
// here. The server never starts it by itself (noStart); the command line does. TripoSR runs as the .NET service of services (bin/services/triposr)
// unless the setting model3d_backend says docker; Stable Fast 3D always runs in the container.
func NewModel3DService(r runner.CommandRunner, noStart bool, services *musicsvc.ServiceHost) *generate.Service {
	svc := &generate.Service{Runner: r, ModelsDir: absOr(generate.WeightsDir), NoStart: noStart, Services: services, Backend: settings.Model3DBackend,
		HFKey: func() string { return keyServices["huggingface"].effective() }}
	if host := strings.TrimSpace(os.Getenv("MODEL3D_HOST")); host != "" {
		// a service somewhere else (the compose stack sets it): setting the address is the decision to send pictures there
		token := strings.TrimSpace(os.Getenv("MODEL3D_TOKEN"))
		if f := strings.TrimSpace(os.Getenv("MODEL3D_TOKEN_FILE")); f != "" {
			if b, err := os.ReadFile(f); err == nil {
				token = strings.TrimSpace(string(b))
			}
		}
		svc.External = musicsvc.External{Host: host, Token: token, Consented: true}
	}
	return svc
}

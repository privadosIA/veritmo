// Command veritmo drives the full scenes.yaml -> narrated video
// pipeline: Piper (TTS) -> ffprobe (duration) -> sd (stable-diffusion.cpp
// image render) -> ffmpeg (per-scene clip + final concat). The pipeline
// wiring itself lives in internal/project/pipeline so the HTTP server can reuse it;
// this command only parses flags, loads the config, and reports fatal
// conditions on stderr exactly as it always has.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"veritmo/internal/assets3d"
	"veritmo/internal/assistant/ctxpress"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/assistant/describe"
	"veritmo/internal/platform/edition"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/runner"
	"veritmo/internal/platform/settings"
	"veritmo/internal/project/config"
	"veritmo/internal/project/pipeline"
	"veritmo/internal/project/quality"
	"veritmo/internal/promptfetch"
	"veritmo/internal/server"
)

// loadConfig reads a scenes.yaml and gives it the catalog of parts the music motion engine keeps: the engine imports the
// configuration, so the configuration asks for the catalog (config.PartCatalog) and the program hands it over.
func loadConfig(path string) (*config.Config, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	cfg.Parts = lazyMotionParts(defaultDB)
	return cfg, nil
}

// deps is how the commands make what they talk to: the assistant, the describer, the vision judge of the check and the
// network of the prompt fetch. The command line uses the real ones (realDeps); a test gives fakes.
type deps struct {
	newAssistant func(modelFlag string) *assist.Assistant
	newDescriber func() *describe.Describer
	newVision    func(model string) quality.Vision
	promptsNet   func() promptfetch.Net
}

func realDeps() deps {
	return deps{
		newAssistant: func(modelFlag string) *assist.Assistant { return assist.FromEnv(modelFlag) },
		newDescriber: describe.FromEnv,
		newVision:    func(model string) quality.Vision { return quality.NewOllama(settings.OllamaURL(), model) },
		promptsNet:   func() promptfetch.Net { return promptfetch.HuggingFace{Token: os.Getenv("HF_TOKEN")} },
	}
}

func main() {
	// The settings of every command but the ones that open the database themselves (serve, data) are those of the database, opened when the
	// first one is read.
	if len(os.Args) < 2 || (os.Args[1] != "serve" && os.Args[1] != "data" && os.Args[1] != "--version" && os.Args[1] != "version") {
		useDatabaseSettings(defaultDB, "projects", os.Stderr)
	}
	// Optional: with CTXPRESS_URL set, long prompts go through the ctxpress compression service; without it, nothing changes.
	ctxpress.Install()
	// The `import`, `export`, `assist` and `serve` subcommands dispatch before pipeline flag
	// parsing, so the existing CLI surface is untouched (see
	// specs/scene-import and specs/http-server).
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Fprintln(os.Stdout, edition.VersionLine())
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "import" {
		os.Exit(runImport(os.Args[2:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "export" {
		os.Exit(runExport(os.Args[2:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "check" {
		os.Exit(runCheck(os.Args[2:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "assist" {
		os.Exit(runAssist(os.Args[2:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "describe" {
		os.Exit(runDescribe(os.Args[2:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "media" {
		os.Exit(runMedia(os.Args[2:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "references" {
		os.Exit(runReferences(os.Args[2:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "words" {
		os.Exit(runWords(os.Args[2:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "cors" {
		os.Exit(runCORS(os.Args[2:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "models" {
		os.Exit(runModels(os.Args[2:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "models3d" {
		os.Exit(runModels3D(os.Args[2:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "parts" {
		os.Exit(runParts(os.Args[2:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "blocks" {
		os.Exit(runBlocks(os.Args[2:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "prompts" {
		os.Exit(runPrompts(os.Args[2:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "project" {
		os.Exit(runProject(os.Args[2:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "data" {
		os.Exit(runData(os.Args[2:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		os.Exit(runServe(os.Args[2:], os.Stdout, os.Stderr))
	}

	var (
		configPath string
		keepTmp    bool
		gpuID      string
		jsonMode   bool
		force      bool
		onlyID     string
		vramWarnMB int
		overlap    int
		modeFlag   string
		check      checkOptions
		doCheck    bool
		proj       projectArgs
	)

	flag.StringVar(&configPath, "config", "", "Path to scenes.yaml (positional arg also accepted)")
	flag.StringVar(&modeFlag, "mode", "", "Override output.mode from scenes.yaml (video|images|music); the flag takes precedence")
	flag.BoolVar(&keepTmp, "keep-tmp", false, "Preserve tmp/ (and its cache manifest) after the run instead of deleting it")
	flag.StringVar(&gpuID, "gpu", "", "GPU index to export as CUDA_VISIBLE_DEVICES for the sd phases; a comma-separated list (0,1) runs one scene at a time on each GPU")
	flag.BoolVar(&jsonMode, "json", false, "Emit progress as JSON-lines instead of human-readable text")
	flag.BoolVar(&force, "force", false, "Bypass the cache and re-run every phase for every scene")
	flag.StringVar(&onlyID, "only", "", "Restrict (re)processing to a single scene by ID; other scenes reuse their cached clip")
	flag.IntVar(&overlap, "overlap", 0, "With one GPU or none, narrate and probe up to N scenes ahead and encode the clips while the card renders another scene (0 = one phase after another, the default; 2 is a good value)")
	flag.IntVar(&vramWarnMB, "vram-warn-mb", 2048, "Warn if free VRAM (MB) on --gpu is below this threshold")
	flag.BoolVar(&doCheck, "check", false, "After the run, check the animated clips for deformed people, colour drift and blur (see `veritmo check`)")
	flag.BoolVar(&check.Fix, "fix", false, "With --check: edit scenes.yaml to cut or remake the bad scenes and run the project again (a backup is kept)")
	flag.IntVar(&check.Attempts, "check-attempts", 2, "With --check --fix: rounds of fixing and making again")
	flag.StringVar(&check.Provider, "check-provider", "", "provider for the vision judgement of --check, instead of the one routed to the quality check (a remote one needs the acknowledgement given in Settings)")
	flag.StringVar(&check.Model, "check-model", "", "Ollama vision model for --check (default: $VERITMO_VISION_MODEL; none checks only colour and sharpness)")
	flag.BoolVar(&check.NoVision, "no-vision", false, "With --check: only colour and sharpness")
	proj.register(flag.CommandLine)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [scenes.yaml | --project <id>] [flags]\n\nFlags:\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	// Positional arg overrides --config when both are absent.
	if configPath == "" && flag.NArg() > 0 {
		configPath = flag.Arg(0)
	}
	// --project runs a project of the database: its definition and pages are written to its folder, the run works there (relative paths of the
	// definition are the project's) and what it changed is taken back. A loose file opens no database.
	finishProject := func(string) {}
	if proj.ID != "" {
		if configPath != "" {
			fmt.Fprintf(os.Stderr, "fatal: --project cannot be used with a scenes.yaml or --config\n")
			os.Exit(2)
		}
		path, done, ok := proj.resolve(os.Stderr)
		if !ok {
			os.Exit(1)
		}
		if err := os.Chdir(filepath.Dir(path)); err != nil {
			fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
			os.Exit(1)
		}
		configPath, finishProject = filepath.Base(path), done
	}
	if configPath == "" {
		configPath = "scenes.yaml"
	}

	// --- Load & validate config (before any subprocess) ---
	cfg, err := loadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: failed to load config: %v\n", err)
		finishProject("")
		os.Exit(1)
	}
	cfg.Parts = lazyMotionParts(proj.DBPath)
	// --mode overrides output.mode before validation (flag > YAML) so every
	// mode-dependent check operates on the effective mode.
	if err := pipeline.ApplyModeOverride(cfg, config.OutputMode(modeFlag)); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: config validation failed: %v\n", err)
		os.Exit(1)
	}
	for _, w := range cfg.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}

	// --- Pre-flight dependency check (mode-aware) ---
	if err := pipeline.PreflightFor(cfg, onlyID, exec.LookPath); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}

	if err := pipeline.CheckOnlyScene(cfg, onlyID); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}

	if _, err := pipeline.ParseGPUs(gpuID); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: --gpu: %v\n", err)
		os.Exit(1)
	}

	emitter := events.New(os.Stdout, jsonMode)
	runOpts := pipeline.Options{
		KeepTmp:       keepTmp || doCheck, // the check reads the clips in tmp/
		GPU:           gpuID,
		VRAMWarnMB:    vramWarnMB,
		Force:         force,
		Only:          onlyID,
		Overlap:       overlap,
		MotionParts:   lazyMotionParts(proj.DBPath),
		MusicServices: musicServices(defaultDB), // the same services the server starts from Environment
	}
	if cfg.Output.Mode == config.ModeModels3D {
		// the models of a models3d project: the 3D service of this machine (the command line may start its container) and the library
		runOpts.Models3D = &pipeline.Models3DServices{Service: server.NewModel3DService(runner.NewExecRunner(), false, musicServices(defaultDB)),
			Library: assets3d.New(assets3d.ResolveDir(os.Getenv), assets3d.DefaultPacks())}
	}
	// Ctrl+C cancels the run: the runner then stops every program it started, with the processes they spawned.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if _, err := pipeline.Run(ctx, cfg, runOpts, emitter); err != nil {
		// The error event is already on the stream; exit non-zero exactly
		// as the pre-extraction fatal() did.
		finishProject("run")
		os.Exit(1)
	}
	if doCheck {
		check.JSON = jsonMode
		runOpts.Force, runOpts.Only = false, "" // a round of fixing remakes only what changed
		rerun := func(c *config.Config) error {
			_, err := pipeline.Run(ctx, c, runOpts, emitter)
			return err
		}
		code := checkAfterRun(ctx, configPath, check, rerun, os.Stdout, os.Stderr)
		finishProject("fix")
		os.Exit(code)
	}
	finishProject("run")
}

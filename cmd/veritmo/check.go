package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"veritmo/internal/assistant/llm"
	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/runner"
	"veritmo/internal/platform/settings"
	"veritmo/internal/project/config"
	"veritmo/internal/project/quality"
)

// checkOptions are the settings of the quality check, from the flags of
// `veritmo check` or of a run with --check.
type checkOptions struct {
	Model       string // Ollama vision model; empty checks only what can be measured
	Provider    string // provider for this run instead of the one routed to the quality check
	NoVision    bool   // measure only, even if a model is named
	Fix         bool   // apply the fixes to scenes.yaml
	Attempts    int    // rounds of fix and make again (with --check on a run)
	MinSeverity int
	MinKeep     float64
	FPS         float64
	JSON        bool
	// NewVision builds the vision judge for a model; nil asks the Ollama in use.
	NewVision func(model string) quality.Vision
}

func (o checkOptions) newVision(model string) quality.Vision {
	if o.NewVision != nil {
		return o.NewVision(model)
	}
	return quality.NewOllama(settings.OllamaURL(), model)
}

// visionModelFrom resolves the vision model: the flag, else $ORCHESTRATOR_VISION_MODEL.
func visionModelFrom(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return strings.TrimSpace(os.Getenv("ORCHESTRATOR_VISION_MODEL"))
}

// checkProject checks the animated scenes of a project whose clips exist and
// returns the reports. base is the project folder; scenes with no clip yet are
// skipped with a note. When no vision model is named it says which ones Ollama
// has and checks only what it can measure.
func checkProject(ctx context.Context, cfg *config.Config, base string, o checkOptions, out io.Writer) ([]*quality.SceneReport, error) {
	var vision quality.Vision
	model := ""
	if !o.NoVision {
		// The provider routed to the quality check (or the one named): a remote one needs the
		// acknowledgement given in Settings, and nothing falls back to another provider.
		res, err := llm.For(settings.TaskQuality, o.Provider)
		if err != nil {
			return nil, err
		}
		model = visionModelFrom(o.Model)
		if !res.Builtin && res.Model != "" {
			model = res.Model
		}
		if model != "" && !res.Builtin {
			vision = quality.FromResolved(res, model)
		} else if model != "" {
			vision = o.newVision(model)
		} else if names, err := quality.VisionModels(ctx, settings.OllamaURL()); err == nil && len(names) > 0 {
			fmt.Fprintf(out, "note: no vision model named, so only colour and sharpness are checked; to check the people too, use --check-model <name> (or $ORCHESTRATOR_VISION_MODEL). Vision models in Ollama: %s\n", strings.Join(names, ", "))
		}
	}
	if u, ok := vision.(interface{ Unload(context.Context) }); ok {
		defer u.Unload(context.WithoutCancel(ctx)) // the model is big: the next run needs the memory
	}
	r := runner.NewExecRunner()
	var reports []*quality.SceneReport
	for _, s := range cfg.Scenes {
		if !s.Animate || cfg.Defaults.Animate == nil {
			continue
		}
		raw, _ := cache.AnimatePaths(s.ID)
		path := filepath.Join(base, raw)
		if _, err := os.Stat(path); err != nil {
			fmt.Fprintf(out, "note: %s has no animated clip yet; skipped\n", s.ID)
			continue
		}
		qo := quality.Options{FPS: o.FPS, MinSeverity: o.MinSeverity, MinKeep: o.MinKeep}
		if s.IsSilent() {
			qo.MaxSeconds = s.SilenceDuration() // only the part the scene uses
		}
		rep, err := quality.CheckClip(ctx, r, vision, s.ID, path, qo)
		if err != nil {
			return reports, err
		}
		reports = append(reports, rep)
		printReport(out, rep, o.JSON)
	}
	if data, err := quality.MarshalReports(cfg.Project, reports, model); err == nil {
		dir := filepath.Join(base, cfg.ExportDir())
		if os.MkdirAll(dir, 0o755) == nil {
			_ = os.WriteFile(filepath.Join(dir, "quality.json"), data, 0o644)
		}
	}
	return reports, nil
}

func printReport(out io.Writer, rep *quality.SceneReport, asJSON bool) {
	if asJSON {
		b, _ := json.Marshal(map[string]any{"type": "quality", "scene": rep.ID, "verdict": rep.Verdict, "good_until": rep.GoodUntil, "seconds": rep.Seconds, "issues": rep.Issues})
		fmt.Fprintln(out, string(b))
		return
	}
	fmt.Fprintf(out, "[check] %s\n", rep.Summary())
}

// problems counts the reports that are not ok.
func problems(reports []*quality.SceneReport) int {
	n := 0
	for _, r := range reports {
		if r.Verdict != quality.VerdictOK {
			n++
		}
	}
	return n
}

// fixesFor turns the reports into the changes to scenes.yaml: a silent scene
// with a clean start is cut to it, any other scene that is bad gets another
// seed (a narrated scene cannot be cut: the clip is fitted to its narration).
func fixesFor(cfg *config.Config, reports []*quality.SceneReport) []quality.Fix {
	byID := map[string]config.Scene{}
	for _, s := range cfg.Scenes {
		byID[s.ID] = s
	}
	var fixes []quality.Fix
	for _, rep := range reports {
		s := byID[rep.ID]
		if f := quality.Propose(rep, s.IsSilent(), s.SilenceDuration(), s.Seed); f != nil {
			fixes = append(fixes, *f)
		}
	}
	return fixes
}

// runCheck implements `veritmo check <scenes.yaml>`: it looks at the
// animated clips of a project that has been run and says which ones are
// deformed and from when. With --fix it edits scenes.yaml (a backup is kept)
// so the next run cuts or remakes them. It never runs the pipeline.
func runCheck(args []string, stdout, stderr io.Writer) int {
	return realDeps().check(args, stdout, stderr)
}

func (dp deps) check(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o checkOptions
	o.NewVision = dp.newVision
	fs.StringVar(&o.Model, "model", "", "Ollama vision model that judges the frames (default: $ORCHESTRATOR_VISION_MODEL; none checks only colour and sharpness)")
	fs.StringVar(&o.Provider, "provider", "", "provider for this run instead of the one routed to the quality check (a remote one needs the acknowledgement given in Settings)")
	fs.BoolVar(&o.NoVision, "no-vision", false, "check only colour and sharpness, even if a model is named")
	fs.BoolVar(&o.Fix, "fix", false, "edit scenes.yaml: cut the silent scenes to their clean part and give the others a new seed (scenes.yaml.bak keeps the original)")
	fs.IntVar(&o.MinSeverity, "min-severity", 7, "lowest vision severity (0-10) that counts as a defect")
	fs.Float64Var(&o.MinKeep, "min-keep", 0.8, "least clean seconds worth keeping; with less the scene is remade")
	fs.Float64Var(&o.FPS, "fps", 4, "frames per second of clip that are sampled")
	fs.BoolVar(&o.JSON, "json", false, "one JSON line per scene")
	var proj projectArgs
	proj.register(fs)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: veritmo check [flags] <scenes.yaml> | --project <id>\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var path string
	if proj.ID != "" {
		if fs.NArg() != 0 {
			fmt.Fprintf(stderr, "fatal: check takes a scenes.yaml or --project, not both\n")
			return 2
		}
		p, done, ok := proj.resolve(stderr)
		if !ok {
			return 1
		}
		path = p
		defer done("fix") // a fix applied to the project is taken into the database as a new version
	} else if fs.NArg() != 1 {
		fmt.Fprintf(stderr, "fatal: check needs exactly one scenes.yaml, got %d\n", fs.NArg())
		fs.Usage()
		return 2
	} else {
		path = fs.Arg(0)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		fmt.Fprintf(stderr, "fatal: %v\n", err)
		return 1
	}
	base := filepath.Dir(path)
	if err := cfg.ValidateInDir(base); err != nil {
		fmt.Fprintf(stderr, "fatal: config validation failed: %v\n", err)
		return 1
	}
	reports, err := checkProject(context.Background(), cfg, base, o, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "fatal: %v\n", err)
		return 1
	}
	n := problems(reports)
	fmt.Fprintf(stdout, "checked %d animated scenes: %d ok, %d with problems\n", len(reports), len(reports)-n, n)
	if n == 0 {
		return 0
	}
	fixes := fixesFor(cfg, reports)
	if !o.Fix {
		fmt.Fprintf(stdout, "run again with --fix to edit %s (a backup is kept) and then run the project to apply it\n", path)
		return 3
	}
	if err := quality.ApplyFixes(path, fixes); err != nil {
		fmt.Fprintf(stderr, "fatal: %v\n", err)
		return 1
	}
	for _, f := range fixes {
		switch {
		case f.SilenceSeconds != nil:
			fmt.Fprintf(stdout, "fixed %s: silence_seconds %v\n", f.ID, *f.SilenceSeconds)
		case f.Seed != nil:
			fmt.Fprintf(stdout, "fixed %s: seed %d (it will be made again)\n", f.ID, *f.Seed)
		}
	}
	fmt.Fprintf(stdout, "edited %s; run the project to apply it\n", path)
	return 0
}

// checkAfterRun is the check at the end of a run (--check): it checks the
// clips and, with --fix, edits scenes.yaml and makes the project again, up to
// o.Attempts rounds, until nothing is left or the rounds run out. rerun makes
// the project again from the config on disk. It returns the exit code.
func checkAfterRun(ctx context.Context, configPath string, o checkOptions, rerun func(*config.Config) error, out, errOut io.Writer) int {
	attempts := o.Attempts
	if attempts < 1 {
		attempts = 2
	}
	base := filepath.Dir(configPath)
	for round := 0; ; round++ {
		cfg, err := loadConfig(configPath)
		if err != nil {
			fmt.Fprintf(errOut, "fatal: %v\n", err)
			return 1
		}
		if err := cfg.ValidateInDir(base); err != nil {
			fmt.Fprintf(errOut, "fatal: config validation failed: %v\n", err)
			return 1
		}
		reports, err := checkProject(ctx, cfg, base, o, out)
		if err != nil {
			fmt.Fprintf(errOut, "fatal: %v\n", err)
			return 1
		}
		n := problems(reports)
		fmt.Fprintf(out, "checked %d animated scenes: %d ok, %d with problems\n", len(reports), len(reports)-n, n)
		if n == 0 {
			return 0
		}
		if !o.Fix {
			fmt.Fprintf(out, "run `veritmo check --fix %s` to cut or remake them\n", configPath)
			return 3
		}
		if round >= attempts {
			fmt.Fprintf(out, "%d scenes still have problems after %d rounds; look at them in out/%s/quality.json\n", n, attempts, cfg.Project)
			return 3
		}
		if err := quality.ApplyFixes(configPath, fixesFor(cfg, reports)); err != nil {
			fmt.Fprintf(errOut, "fatal: %v\n", err)
			return 1
		}
		fmt.Fprintf(out, "round %d: scenes.yaml edited (backup in %s.bak); making the project again\n", round+1, configPath)
		fixed, err := loadConfig(configPath)
		if err != nil {
			fmt.Fprintf(errOut, "fatal: %v\n", err)
			return 1
		}
		if err := rerun(fixed); err != nil {
			return 1
		}
	}
}

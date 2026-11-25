package main

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"veritmo/internal/project/importer"
)

// runImport implements the `veritmo import <input> -o <output>`
// subcommand: a pure conversion from a scene dataset (JSON, or HTML with a
// `const data = [...]` script) to a validated scenes.yaml. It never runs
// the pipeline and never touches tmp/, out/, or the cache. It returns the
// process exit code.
func runImport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		outPath        string
		silenceSeconds float64
		forceOverwrite bool
		mode           string
	)
	fs.StringVar(&mode, "mode", "video", "Kind of dataset: video (scenes), motion (animated graphics), music (tracks) or models3d (3D models)")
	fs.StringVar(&outPath, "o", "", "Output scenes.yaml path (required)")
	fs.Float64Var(&silenceSeconds, "silence-seconds", 2.0, "silence_seconds assigned to entries without a dialogue line (> 0)")
	fs.BoolVar(&forceOverwrite, "force-overwrite", false, "Allow overwriting an existing output file")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: veritmo import <input.(json|html)> -o <scenes.yaml> [flags]\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(stderr, "fatal: import needs exactly one input file, got %d\n", fs.NArg())
		fs.Usage()
		return 2
	}
	if outPath == "" {
		fmt.Fprintf(stderr, "fatal: -o <scenes.yaml> is required\n")
		fs.Usage()
		return 2
	}
	if mode != "video" && mode != "music" && mode != "motion" && mode != "models3d" {
		fmt.Fprintf(stderr, "fatal: --mode must be video, motion, music or models3d, got %q\n", mode)
		return 2
	}
	if silenceSeconds <= 0 {
		fmt.Fprintf(stderr, "fatal: --silence-seconds must be > 0, got %v\n", silenceSeconds)
		return 2
	}

	input := fs.Arg(0)
	entries, rejected, err := importer.Load(input)
	if err != nil {
		fmt.Fprintf(stderr, "fatal: %v\n", err)
		return 1
	}

	project := strings.TrimSuffix(filepath.Base(input), filepath.Ext(input))
	mapper := importer.Map
	switch mode {
	case "music":
		mapper = importer.MapMusic
	case "motion":
		mapper = importer.MapMotion
	case "models3d":
		mapper = importer.MapModels3D
	}
	res, err := mapper(entries, rejected, importer.Options{
		Project:        project,
		SilenceSeconds: silenceSeconds,
		Parts:          lazyMotionParts(defaultDB),
	})
	if err != nil {
		fmt.Fprintf(stderr, "fatal: %v\n", err)
		return 1
	}

	if err := res.WriteYAML(outPath, forceOverwrite); err != nil {
		fmt.Fprintf(stderr, "fatal: %v\n", err)
		return 1
	}

	fmt.Fprint(stdout, res.Report())
	fmt.Fprintf(stdout, "wrote %s\n", outPath)
	return 0
}

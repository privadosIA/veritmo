package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"veritmo/internal/project/config"
	"veritmo/internal/project/importer"
	"veritmo/internal/server"
)

// runExport implements `veritmo export <scenes.yaml> -o <file>`: the
// project written as a dataset `veritmo import` reads back (the default),
// or as a package (a zip with scenes.yaml and its sources/). It never runs the
// pipeline. It returns the process exit code.
func runExport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		outPath        string
		format         string
		forceOverwrite bool
		lang           string
	)
	fs.StringVar(&lang, "lang", "en", "Language of the recipe inside a zip: es or en")
	fs.StringVar(&outPath, "o", "", "Output file (required)")
	fs.StringVar(&format, "format", "dataset", "dataset (JSON for veritmo import) or zip (the full package: scenes.yaml, sources/ and the recipe)")
	fs.BoolVar(&forceOverwrite, "force-overwrite", false, "Allow overwriting an existing output file")
	var proj projectArgs
	proj.register(fs)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: veritmo export <scenes.yaml> | --project <id> -o <file> [flags]\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var source string
	switch {
	case proj.ID != "" && fs.NArg() != 0:
		fmt.Fprintf(stderr, "fatal: export takes a scenes.yaml or --project, not both\n")
		return 2
	case proj.ID == "" && fs.NArg() != 1:
		fmt.Fprintf(stderr, "fatal: export needs exactly one scenes.yaml, got %d\n", fs.NArg())
		fs.Usage()
		return 2
	case proj.ID == "":
		source = fs.Arg(0)
	}
	if outPath == "" {
		fmt.Fprintf(stderr, "fatal: -o <file> is required\n")
		fs.Usage()
		return 2
	}
	if format != "dataset" && format != "zip" {
		fmt.Fprintf(stderr, "fatal: --format must be dataset or zip, got %q\n", format)
		return 2
	}
	if !forceOverwrite {
		if _, err := os.Stat(outPath); err == nil {
			fmt.Fprintf(stderr, "fatal: output %s already exists (use --force-overwrite to replace it)\n", outPath)
			return 1
		}
	}

	input := source
	if proj.ID != "" { // a project of the database: its definition is written to its folder and exported from there
		path, done, ok := proj.resolve(stderr)
		if !ok {
			return 1
		}
		defer done("")
		input = path
	}
	var data []byte
	switch format {
	case "dataset":
		cfg, err := config.Load(input)
		if err != nil {
			fmt.Fprintf(stderr, "fatal: %v\n", err)
			return 1
		}
		ds, err := importer.ExportDataset(cfg)
		if err != nil {
			fmt.Fprintf(stderr, "fatal: %v\n", err)
			return 1
		}
		data = ds.JSON
		for _, n := range ds.Notes {
			fmt.Fprintf(stdout, "note: %s\n", n)
		}
		fmt.Fprintf(stdout, "exported %d\n", ds.Exported)
	case "zip":
		var buf bytes.Buffer
		if err := server.WriteProjectPackage(&buf, filepath.Dir(input), ".", lang); err != nil {
			fmt.Fprintf(stderr, "fatal: %v\n", err)
			return 1
		}
		data = buf.Bytes()
	}
	if dir := filepath.Dir(outPath); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintf(stderr, "fatal: %v\n", err)
			return 1
		}
	}
	tmp := outPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		fmt.Fprintf(stderr, "fatal: %v\n", err)
		return 1
	}
	if err := os.Rename(tmp, outPath); err != nil {
		os.Remove(tmp)
		fmt.Fprintf(stderr, "fatal: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %s\n", outPath)
	return 0
}

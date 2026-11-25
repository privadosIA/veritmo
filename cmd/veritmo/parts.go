package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"veritmo/internal/music/musicmotion/hfimport"
	"veritmo/internal/platform/settings"
	"veritmo/internal/video/motion"
)

const partsUsage = `Usage: veritmo parts import-hyperframes [--only a,b] [--dry-run] [--json] [--version V] [--db FILE | --parts-dir DIR] <dir>

Converts the blocks of HyperFrames (npx hyperframes add <name> installs them as compositions/<name>.html; <dir> is that folder, or
one that holds the files, or the registry's <name>/<name>.html layout) into parts of the music motion catalog named hf-<name>. Each
block is converted (its template as the fragment, scaled from its canvas to the frame, its variables as parameters, its fonts and
colour variables the design's), checked like any part and written into the user's parts only if it passes; media, the network,
nested compositions and a canvas are refused. One line per block says written or refused and why. It needs no running server: it uses
the parts folder and the checks the server uses (and Chrome or Chromium). Exit code 0 when at least one block was written (or, with
--dry-run, would be), 1 when none.`

// runParts implements `veritmo parts import-hyperframes` (spec cli, "The command line imports HyperFrames blocks as parts").
func runParts(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "import-hyperframes" {
		fmt.Fprintln(stderr, partsUsage)
		return 2
	}
	fs := flag.NewFlagSet("parts import-hyperframes", flag.ContinueOnError)
	fs.SetOutput(stderr)
	only := fs.String("only", "", "Only these blocks, comma-separated (by name)")
	dry := fs.Bool("dry-run", false, "Check every block and write nothing")
	asJSON := fs.Bool("json", false, "Print the lines as JSON")
	version := fs.String("version", "", "Version of HyperFrames the blocks come from, kept in each part's origin")
	partsDir := fs.String("parts-dir", "", "Folder to write the parts to instead of the database (for a catalog kept as folders)")
	dbPath := fs.String("db", defaultDB, "SQLite file of the database that keeps the user's parts (used when no other database is saved in Settings)")
	fs.Usage = func() { fmt.Fprintln(stderr, partsUsage); fs.PrintDefaults() }
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	browser, err := motion.Find(settings.Browser())
	if err != nil {
		fmt.Fprintf(stderr, "parts import-hyperframes: the part checks need Chrome or Chromium: %v\n", err)
		return 1
	}
	opts := hfimport.Options{Dir: fs.Arg(0), DryRun: *dry, Browser: browser.Path, Version: *version}
	if *partsDir != "" {
		opts.UserDir = *partsDir
	} else {
		cat, st, err := motionCatalog(*dbPath, settings.GetDatabase())
		if err != nil {
			fmt.Fprintf(stderr, "parts import-hyperframes: %v\n", err)
			return 1
		}
		defer func() { _ = st.Close() }()
		opts.Catalog = &cat
	}
	var names []string
	if *only != "" {
		names = strings.Split(*only, ",")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	opts.Only = names
	lines, err := hfimport.Import(ctx, opts)
	if err != nil {
		fmt.Fprintf(stderr, "parts import-hyperframes: %v\n", err)
		return 1
	}
	written := 0
	for _, l := range lines {
		if l.Written {
			written++
		}
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"blocks": lines, "written": written, "dry_run": *dry})
	} else {
		for _, l := range lines {
			switch {
			case l.Written && *dry:
				fmt.Fprintf(stdout, "%-32s would be written as %s\n", l.Block, l.Part)
			case l.Written:
				fmt.Fprintf(stdout, "%-32s written as %s\n", l.Block, l.Part)
			default:
				fmt.Fprintf(stdout, "%-32s refused: %s\n", l.Block, l.Refused)
			}
			if len(l.Pictures) > 0 {
				var ps []string
				for _, p := range l.Pictures {
					ps = append(ps, fmt.Sprintf("%s %d KB", p.Name, (p.Bytes+1023)>>10))
				}
				fmt.Fprintf(stdout, "%-32s   pictures: %s\n", "", strings.Join(ps, ", "))
			}
			for _, w := range l.Warnings {
				fmt.Fprintf(stdout, "%-32s   warning: %s\n", "", w)
			}
		}
		verb := "written"
		if *dry {
			verb = "that would be written"
		}
		fmt.Fprintf(stdout, "%d block(s), %d %s, %d refused\n", len(lines), written, verb, len(lines)-written)
	}
	if written == 0 {
		return 1
	}
	return 0
}

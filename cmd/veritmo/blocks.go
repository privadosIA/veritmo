package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"veritmo/internal/music/hyperframes"
	"veritmo/internal/platform/settings"
	"veritmo/internal/video/motion"
)

const blocksUsage = `Usage:
  veritmo blocks import-hyperframes [--only a,b] [--force] [--no-check] [--workers N] [--version V] [--components <registry/components dir>] <registry/blocks dir>
  veritmo blocks check [--workers N] [name ...]
  veritmo blocks list [--json]
  veritmo blocks fetch-assets [name ...]
  veritmo blocks placeholders [name ...]
  veritmo blocks frame [--runtime upstream|builtin] <name> <seconds> <out.png>

HyperFrames blocks are kept whole, as the registry has them, in the blocks folder of the server (hyperframes/, or $HYPERFRAMES_DIR).
import-hyperframes copies the blocks of a clone of the repository (<dir> is its registry/blocks) and then runs the minimum checks on
each: it draws with nothing asked from outside, two passes give the same frames, no script error and something moves. check runs
them again. Both write hyperframes/report.json and print what does not draw and why. The libraries the blocks load come from
bash scripts/get-hyperframes-libs.sh. Exit code 0 when every block passed, 1 when some did not, 2 on a bad command line.`

// runBlocks implements `veritmo blocks` (spec hyperframes-catalog).
func runBlocks(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, blocksUsage)
		return 2
	}
	fs := flag.NewFlagSet("blocks "+args[0], flag.ContinueOnError)
	dbPath := fs.String("db", defaultDB, "SQLite file of the database that keeps the records of the blocks (used when no other database is saved in Settings)")
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, blocksUsage); fs.PrintDefaults() }
	only := fs.String("only", "", "Only these blocks, comma-separated (import-hyperframes)")
	force := fs.Bool("force", false, "Replace blocks the user edited (import-hyperframes)")
	noFetch := fs.Bool("no-fetch", false, "Do not fetch the pictures and clips the registry keeps off git: placeholders are made instead (import-hyperframes)")
	noCheck := fs.Bool("no-check", false, "Copy the blocks and do not run the checks (import-hyperframes)")
	workers := fs.Int("workers", 4, "Checks run at once")
	runtime := fs.String("runtime", "", "Runtime that draws the blocks: upstream, builtin (default: upstream when installed)")
	version := fs.String("version", "", "Version of HyperFrames the blocks come from (import-hyperframes)")
	components := fs.String("components", "", "Also import the components of this registry/components folder (import-hyperframes)")
	asJSON := fs.Bool("json", false, "Print JSON (list)")
	switch args[0] {
	case "import-hyperframes", "check", "list", "frame", "placeholders", "fetch-assets":
	default:
		fmt.Fprintln(stderr, blocksUsage)
		return 2
	}
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	// the record of each block is a row of the database the server uses; the files of the blocks stay in their folder
	st, err := openStore(*dbPath, "projects", settings.GetDatabase())
	if err == nil {
		if err = st.CheckMigrated(); err != nil {
			_ = st.Close()
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "fatal: %v\n", err)
		return 1
	}
	defer func() { _ = st.Close() }()
	store := hyperframes.NewStoreDB(hyperframes.ResolveDir(os.Getenv), st)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var names []string
	switch args[0] {
	case "frame":
		return blockFrame(ctx, store, fs.Args(), *runtime, stdout, stderr)
	case "fetch-assets":
		// the pictures, clips and textures the registry keeps off git, from its own static host, checked against their SHA-256
		all, _ := store.List()
		want := map[string]bool{}
		for _, n := range fs.Args() {
			want[n] = true
		}
		for _, b := range all {
			if len(want) > 0 && !want[b.Name] {
				continue
			}
			got, err := store.FetchAssets(ctx, b.Name)
			if len(got) > 0 {
				fmt.Fprintf(stdout, "%-34s %d files fetched\n", b.Name, len(got))
				names = append(names, b.Name)
			}
			if err != nil {
				fmt.Fprintf(stderr, "%s: %v\n", b.Name, err)
			}
		}
		if len(names) == 0 {
			fmt.Fprintln(stdout, "nothing to fetch")
			return 0
		}
	case "placeholders":
		// make the files the registry lists and does not ship (generated pictures from <store>/placeholders/, else procedural ones), then check
		all, _ := store.List()
		want := map[string]bool{}
		for _, n := range fs.Args() {
			want[n] = true
		}
		for _, b := range all {
			if len(want) > 0 && !want[b.Name] {
				continue
			}
			made, err := store.FillMissing(b.Name)
			if err != nil {
				fmt.Fprintf(stderr, "%s: %v\n", b.Name, err)
				continue
			}
			if len(made) > 0 {
				fmt.Fprintf(stdout, "%-34s %d files made\n", b.Name, len(made))
				names = append(names, b.Name)
			}
		}
		if len(names) == 0 {
			fmt.Fprintln(stdout, "nothing is missing")
			return 0
		}
	case "list":
		blocks, err := store.List()
		if err != nil {
			fmt.Fprintf(stderr, "blocks list: %v\n", err)
			return 1
		}
		if *asJSON {
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(blocks)
			return 0
		}
		for _, b := range blocks {
			fmt.Fprintf(stdout, "%-34s %-11s %dx%d %.1fs  %s\n", b.Name, hyperframes.StateOf(b), b.Width, b.Height, b.Duration, strings.Join(b.Tags, ","))
		}
		fmt.Fprintf(stdout, "%d block(s) in %s\n", len(blocks), store.Dir)
		return 0
	case "import-hyperframes":
		if fs.NArg() != 1 {
			fs.Usage()
			return 2
		}
		var list []string
		if *only != "" {
			list = strings.Split(*only, ",")
		}
		src := hyperframes.Source{Dir: fs.Arg(0), Version: *version, URL: "https://github.com/heygen-com/hyperframes", Commit: gitCommit(fs.Arg(0)), NoFetch: *noFetch}
		res, err := store.Import(src, list, *force)
		if err != nil {
			fmt.Fprintf(stderr, "blocks import-hyperframes: %v\n", err)
			return 1
		}
		if *components != "" {
			cres, err := store.ImportComponents(src2(src, *components), list, *force)
			if err != nil {
				fmt.Fprintf(stderr, "blocks import-hyperframes: components: %v\n", err)
				return 1
			}
			res = append(res, cres...)
		}
		written := 0
		for _, r := range res {
			switch {
			case r.Error != "":
				fmt.Fprintf(stdout, "%-34s not imported: %s\n", r.Name, r.Error)
			case r.Skipped != "":
				fmt.Fprintf(stdout, "%-34s %s\n", r.Name, r.Skipped)
			case r.Written:
				written++
				names = append(names, r.Name)
			}
		}
		fmt.Fprintf(stdout, "%d block(s) imported into %s\n", written, store.Dir)
		if *noCheck {
			return 0
		}
	case "check":
		names = fs.Args()
	}

	browser, err := motion.Find(settings.Browser())
	if err != nil {
		fmt.Fprintf(stderr, "blocks %s: the checks need Chrome or Chromium: %v\n", args[0], err)
		return 1
	}
	if len(hyperframes.Missing(hyperframes.LibsDir)) > 0 {
		fmt.Fprintln(stderr, "blocks: "+hyperframes.ErrLibs.Error())
		return 1
	}
	if len(names) == 0 {
		all, _ := store.List()
		for _, b := range all {
			names = append(names, b.Name)
		}
	}
	if *runtime != "" && *runtime != "upstream" && *runtime != "builtin" {
		fmt.Fprintln(stderr, "blocks: --runtime is upstream or builtin")
		return 2
	}
	// a heavy scene (three.js on the CPU) may need more than the 10 s a frame of the server's pages gets
	d := hyperframes.Drawer{Store: store, Browser: browser.Path, Runtime: *runtime, Limits: motion.Limits{Load: 40 * time.Second, Frame: 40 * time.Second}}
	jobs := make(chan string)
	var wg sync.WaitGroup
	var mu sync.Mutex
	done := 0
	for i := 0; i < max(1, *workers); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range jobs {
				_, err := d.Check(ctx, n)
				mu.Lock()
				done++
				if err != nil {
					fmt.Fprintf(stderr, "[%d/%d] %s: %v\n", done, len(names), n, err)
				}
				mu.Unlock()
			}
		}()
	}
	for _, n := range names {
		if ctx.Err() != nil {
			break
		}
		jobs <- n
	}
	close(jobs)
	wg.Wait()

	all, _ := store.List()
	rep := hyperframes.BuildReport(all)
	rep.WriteText(stdout)
	if b, err := json.MarshalIndent(rep, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(store.Dir, "report.json"), b, 0o644)
	}
	if rep.ByState["ok"] != rep.Total {
		return 1
	}
	return 0
}

// src2 is src with another folder (the components of the same clone).
func src2(src hyperframes.Source, dir string) hyperframes.Source {
	src.Dir = dir
	return src
}

// gitCommit is the short commit of the repository a folder belongs to, or "" (best effort).
func gitCommit(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// blockFrame draws one frame of a block to a PNG file: veritmo blocks frame <name> <seconds> <out.png>.
func blockFrame(ctx context.Context, store hyperframes.Store, args []string, runtime string, stdout, stderr io.Writer) int {
	if len(args) != 3 {
		fmt.Fprintln(stderr, blocksUsage)
		return 2
	}
	var t float64
	if _, err := fmt.Sscanf(args[1], "%g", &t); err != nil || t < 0 {
		fmt.Fprintln(stderr, "blocks frame: the time must be a number of seconds, 0 or more")
		return 2
	}
	browser, err := motion.Find(settings.Browser())
	if err != nil {
		fmt.Fprintf(stderr, "blocks frame: %v\n", err)
		return 1
	}
	sess, _, err := hyperframes.Drawer{Store: store, Browser: browser.Path, Runtime: runtime, Limits: motion.Limits{Load: 60 * time.Second, Frame: 60 * time.Second}}.Open(ctx, args[0], "")
	if err != nil {
		fmt.Fprintf(stderr, "blocks frame: %v\n", err)
		return 1
	}
	defer sess.Close()
	png, err := sess.Frame(ctx, t)
	if err != nil {
		fmt.Fprintf(stderr, "blocks frame: %v\n", err)
		return 1
	}
	if err := os.WriteFile(args[2], png, 0o644); err != nil {
		fmt.Fprintf(stderr, "blocks frame: %v\n", err)
		return 1
	}
	for _, p := range sess.Problems() {
		fmt.Fprintln(stdout, "problem:", p)
	}
	fmt.Fprintf(stdout, "%s at %gs -> %s\n", args[0], t, args[2])
	return 0
}

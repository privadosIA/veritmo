package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"veritmo/internal/platform/legacyname"

	"veritmo/internal/platform/media"
	"veritmo/internal/platform/settings"
)

// runMedia implements `veritmo media migrate`: it moves the projects of the projects directory into the media
// directory (spec media-directory). Nothing is removed before the copy has been checked, and it can be repeated.
func runMedia(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "migrate" {
		fmt.Fprintln(stderr, "Usage: veritmo media migrate [--media-dir DIR] [--projects-dir DIR] [--dry-run] [--force]")
		return 2
	}
	fs := flag.NewFlagSet("media migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	mediaDir := fs.String("media-dir", "", "Media directory (or set MEDIA_DIR)")
	projectsDir := fs.String("projects-dir", "projects", "Directory of the projects")
	dry := fs.Bool("dry-run", false, "Only list what would move and the space it needs")
	force := fs.Bool("force", false, "Migrate even if a server seems to be running (a run in progress would be corrupted)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	dir := *mediaDir
	if dir == "" {
		dir = os.Getenv("MEDIA_DIR")
	}
	if dir == "" {
		fmt.Fprintln(stderr, "fatal: give --media-dir (or set MEDIA_DIR)")
		return 2
	}
	if !*dry && !*force {
		if pid := runningServer(); pid != 0 {
			fmt.Fprintf(stderr, "fatal: the server seems to be running (pid %d): stop it first (./serve.sh --stop), or use --force\n", pid)
			return 1
		}
	}
	rep, err := media.Migrate(*projectsDir, dir, *dry, nil)
	verb := "moved"
	if *dry {
		verb = "would move"
	}
	for _, r := range rep.Projects {
		fmt.Fprintf(stdout, "%s: %s %d bytes (%s)\n", r.ID, verb, r.Bytes, strings.Join(r.Moved, ", "))
	}
	fmt.Fprintf(stdout, "%d project(s), %d bytes to copy, %d bytes free in the media directory\n", len(rep.Projects), rep.Needed, rep.Free)
	if err != nil {
		fmt.Fprintf(stderr, "fatal: %v\n", err)
		return 1
	}
	if len(rep.Projects) == 0 {
		fmt.Fprintln(stdout, "nothing to do")
	}
	return 0
}

// runningServer returns the pid of a server started by scripts/serve.sh that is still alive, or 0.
func runningServer() int {
	home, err := os.UserHomeDir()
	if err != nil {
		return 0
	}
	files, _ := filepath.Glob(filepath.Join(legacyname.Dir(filepath.Join(home, ".config")), "serve-*.pid"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err == nil && pid > 0 && syscall.Kill(pid, 0) == nil {
			return pid
		}
	}
	return 0
}

// runModels implements `veritmo models migrate`: it moves models/ to the models directory and leaves a link
// (spec models-directory). It can be cut and run again; nothing is deleted before the copy has been checked.
func runModels(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "migrate" {
		fmt.Fprintln(stderr, "Usage: veritmo models migrate [--models-dir DIR] [--root DIR] [--dry-run] [--force]")
		return 2
	}
	fs := flag.NewFlagSet("models migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dirFlag := fs.String("models-dir", "", "Models directory (or MODELS_DIR, or ~/.config/veritmo/models-dir)")
	root := fs.String("root", ".", "Folder that holds models/ (the repository)")
	dry := fs.Bool("dry-run", false, "Only report what would be copied and the space it needs")
	force := fs.Bool("force", false, "Migrate even if a server seems to be running (a model being read would break)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	dir := media.ResolveModelsDir(*dirFlag, os.Getenv, media.UserConfigDir(os.Getenv))
	if dir == "" {
		fmt.Fprintln(stderr, "fatal: give --models-dir (or set MODELS_DIR, or write the path in ~/.config/veritmo/models-dir)")
		return 2
	}
	if !*dry && !*force {
		if pid := runningServer(); pid != 0 {
			fmt.Fprintf(stderr, "fatal: the server seems to be running (pid %d): stop it first (./serve.sh --stop), or use --force\n", pid)
			return 1
		}
	}
	rep, err := media.MigrateModels(*root, dir, *dry, stdout)
	switch {
	case rep.Done:
		fmt.Fprintln(stdout, "models already is the link to the models directory: nothing to do")
	default:
		verb := "copied"
		if *dry {
			verb = "would copy"
		}
		fmt.Fprintf(stdout, "%d file(s): %s %d, skipped %d (already there); %d bytes to copy, %d bytes free\n", rep.Files, verb, rep.Copied, rep.Skipped, rep.Needed, rep.Free)
	}
	if err != nil {
		fmt.Fprintf(stderr, "fatal: %v\n", err)
		return 1
	}
	if rep.Linked && !rep.Done {
		fmt.Fprintln(stdout, "models is now a link to the models directory and the old copy was deleted")
	}
	return 0
}

// runCORS implements `veritmo cors add|remove|list`: which browser origins and networks may call the API from
// another origin (spec cors-settings). It edits the settings file on this machine; the running server follows it.
func runCORS(args []string, stdout, stderr io.Writer) int {
	usage := func() int {
		fmt.Fprintln(stderr, "Usage: veritmo cors add [--network] <origin|range>\n       veritmo cors remove [--network] <origin|range>\n       veritmo cors list")
		return 2
	}
	if len(args) == 0 {
		return usage()
	}
	cmd := args[0]
	fs := flag.NewFlagSet("cors "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	network := fs.Bool("network", false, "the value is a CIDR range such as 192.168.1.0/24 (any IP in it is allowed on any port) instead of an origin")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	switch cmd {
	case "list":
		origins, networks := settings.CORS()
		fmt.Fprintf(stdout, "origins (%d):\n", len(origins))
		for _, o := range origins {
			fmt.Fprintln(stdout, "  "+o)
		}
		fmt.Fprintf(stdout, "networks (%d):\n", len(networks))
		for _, n := range networks {
			fmt.Fprintln(stdout, "  "+n)
		}
		return 0
	case "add", "remove":
		if fs.NArg() != 1 {
			return usage()
		}
		v, changed, err := settings.ChangeCORS(*network, cmd == "remove", fs.Arg(0))
		if err != nil {
			fmt.Fprintf(stderr, "fatal: %v\n", err)
			return 1
		}
		switch {
		case !changed && cmd == "add":
			fmt.Fprintf(stdout, "%s was already allowed\n", v)
		case !changed:
			fmt.Fprintf(stdout, "%s was not in the list\n", v)
		case cmd == "add":
			fmt.Fprintf(stdout, "allowed: %s (a running server follows the file, no restart needed)\n", v)
		default:
			fmt.Fprintf(stdout, "removed: %s\n", v)
		}
		return 0
	}
	return usage()
}

package main

// project.go: the projects of the database on the command line (spec cli, "The command line runs a project of the database" and "The definition can
// be pulled and pushed as a file"): the flag --project <id> of a run, of `check` and of `export`, and `veritmo project pull|push`.

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"veritmo/internal/platform/settings"
	"veritmo/internal/platform/store"
	"veritmo/internal/project/config"
	"veritmo/internal/server/workspace"
)

// openProjectStore opens the database and gives access to its projects. A test replaces it to see that a command that runs a loose file never
// opens a database.
var openProjectStore = func(dbPath, projectsDir string) (*workspace.ProjectsAccess, *store.Store, error) {
	st, err := openStore(dbPath, projectsDir, settings.GetDatabase())
	if err != nil {
		return nil, nil, err
	}
	if err := st.CheckMigrated(); err != nil {
		_ = st.Close()
		return nil, nil, err
	}
	return workspace.OpenProjects(st, projectsDir), st, nil
}

// projectArgs are the flags that name a project of the database.
type projectArgs struct {
	ID          string
	ProjectsDir string
	DBPath      string
}

func (p *projectArgs) register(fs *flag.FlagSet) {
	fs.StringVar(&p.ID, "project", "", "Run a project of the database by its id instead of a scenes.yaml: its saved definition is written to its folder, the work is done there and what it changed is taken back")
	fs.StringVar(&p.ProjectsDir, "projects-dir", "projects", "Folder whose subfolders are the directories of the projects (with --project)")
	fs.StringVar(&p.DBPath, "db", defaultDB, "SQLite file of the database (used when no other database is saved in Settings; with --project)")
}

// resolve opens the project: it checks that it exists (before anything runs), writes its definition and pages to its folder and returns the path of
// its scenes.yaml, and a function to call when the work is over that takes back what changed (the reason says why) and closes the database.
func (p projectArgs) resolve(stderr io.Writer) (path string, done func(reason string), ok bool) {
	access, st, err := openProjectStore(p.DBPath, p.ProjectsDir)
	if err != nil {
		fmt.Fprintf(stderr, "fatal: %v\n", err)
		return "", nil, false
	}
	if !access.Exists(p.ID) {
		_ = st.Close()
		fmt.Fprintf(stderr, "fatal: the project %q is not in the database (see `veritmo project pull` and GET /api/projects)\n", p.ID)
		return "", nil, false
	}
	if err := access.Materialize(p.ID); err != nil {
		_ = st.Close()
		fmt.Fprintf(stderr, "fatal: cannot write the project %q to its folder: %v\n", p.ID, err)
		return "", nil, false
	}
	return access.ConfigPath(p.ID), func(reason string) {
		if reason != "" {
			_ = os.Remove(access.ConfigPath(p.ID) + ".bak") // the version the database keeps is the backup
			if err := access.Ingest(p.ID, reason); err != nil {
				fmt.Fprintf(stderr, "warning: what the run changed could not be taken into the database: %v\n", err)
			}
		}
		_ = st.Close()
	}, true
}

const projectUsage = `Usage:
  veritmo project pull <id> [file]   write the saved definition of a project to a file (default: scenes.yaml in its folder)
  veritmo project push <id> [file]   save a file as the definition of a project after the validation a save through the API passes
                                          (the replaced definition is kept as a version)

Flags: --projects-dir, --db`

func runProject(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "pull" && args[0] != "push") {
		fmt.Fprintln(stderr, projectUsage)
		return 2
	}
	verb := args[0]
	fs := flag.NewFlagSet("project "+verb, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var p projectArgs
	fs.StringVar(&p.ProjectsDir, "projects-dir", "projects", "Folder whose subfolders are the directories of the projects")
	fs.StringVar(&p.DBPath, "db", defaultDB, "SQLite file of the database (used when no other database is saved in Settings)")
	fs.Usage = func() { fmt.Fprintln(stderr, projectUsage); fs.PrintDefaults() }
	// flags may come after the id: split them out
	var positional, flags []string
	for i := 1; i < len(args); i++ {
		if len(args[i]) > 0 && args[i][0] == '-' {
			flags = append(flags, args[i])
			if (args[i] == "--projects-dir" || args[i] == "-projects-dir" || args[i] == "--db" || args[i] == "-db") && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positional = append(positional, args[i])
	}
	if err := fs.Parse(flags); err != nil {
		return 2
	}
	if len(positional) < 1 || len(positional) > 2 {
		fmt.Fprintf(stderr, "fatal: project %s needs a project id and, optionally, a file\n", verb)
		fs.Usage()
		return 2
	}
	id := positional[0]
	access, st, err := openProjectStore(p.DBPath, p.ProjectsDir)
	if err != nil {
		fmt.Fprintf(stderr, "fatal: %v\n", err)
		return 1
	}
	defer func() { _ = st.Close() }()
	if !access.Exists(id) {
		fmt.Fprintf(stderr, "fatal: the project %q is not in the database\n", id)
		return 1
	}
	file := access.ConfigPath(id)
	if len(positional) == 2 {
		file = positional[1]
	}
	if verb == "pull" {
		def, err := access.Definition(id)
		if err != nil {
			fmt.Fprintf(stderr, "fatal: %v\n", err)
			return 1
		}
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			fmt.Fprintf(stderr, "fatal: %v\n", err)
			return 1
		}
		if err := os.WriteFile(file, def, 0o644); err != nil {
			fmt.Fprintf(stderr, "fatal: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "wrote the definition of %s to %s\n", id, file)
		return 0
	}
	text, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintf(stderr, "fatal: %v\n", err)
		return 1
	}
	// the validation of a save through the API: the loader, then the parts and the files of the project's folder
	cfg, err := config.LoadBytes(text)
	if err != nil {
		fmt.Fprintf(stderr, "fatal: %s is not a valid definition, nothing was changed: %v\n", file, err)
		return 1
	}
	cfg.Parts = lazyMotionParts(p.DBPath)
	if err := os.MkdirAll(access.Dir(id), 0o755); err == nil {
		if err := cfg.ValidateInDir(access.Dir(id)); err != nil {
			fmt.Fprintf(stderr, "fatal: %s is not a valid definition, nothing was changed: %v\n", file, err)
			return 1
		}
	}
	changed, err := access.Save(id, text, "save")
	if err != nil {
		fmt.Fprintf(stderr, "fatal: %v\n", err)
		return 1
	}
	if changed {
		fmt.Fprintf(stdout, "saved %s as the definition of %s (the one it replaced is a version)\n", file, id)
	} else {
		fmt.Fprintf(stdout, "%s is the definition of %s already: nothing changed\n", file, id)
	}
	return 0
}

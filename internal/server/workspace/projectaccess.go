package workspace

// projectaccess.go: the projects of a database for the command line, which has no running server (spec cli, "The command line runs a project of
// the database"): the same store the server uses, without its handlers.

import (
	"context"
	"log/slog"
	"path/filepath"

	"veritmo/internal/platform/store"
)

// ProjectsAccess works with the projects of a database and their folders under a projects directory.
type ProjectsAccess struct {
	ps    *projectStore
	songs *songStore
	dir   string
}

// OpenProjects gives access to the projects of an open, migrated store; projectsDir is the folder their directories are under.
func OpenProjects(st *store.Store, projectsDir string) *ProjectsAccess {
	songs := newSongStore(st)
	songs.measure = execMeasure
	return &ProjectsAccess{ps: newProjectStore(st), songs: songs, dir: projectsDir}
}

// Dir is the folder of a project.
func (a *ProjectsAccess) Dir(id string) string { return filepath.Join(a.dir, id) }

// ConfigPath is the scenes.yaml of a project (the working copy Materialize writes).
func (a *ProjectsAccess) ConfigPath(id string) string {
	return filepath.Join(a.Dir(id), definitionFile)
}

// Exists says whether the project is in the database.
func (a *ProjectsAccess) Exists(id string) bool { return a.ps.Exists(id) }

// Definition is the saved text of the scenes.yaml of a project (ErrNoProject for an unknown one).
func (a *ProjectsAccess) Definition(id string) ([]byte, error) {
	row, err := a.ps.Get(id)
	if err != nil {
		return nil, err
	}
	return []byte(row.Definition), nil
}

// Materialize writes the definition and the pages of the project to its folder, for what runs there.
func (a *ProjectsAccess) Materialize(id string) error { return a.ps.Materialize(id, a.Dir(id)) }

// Ingest takes back what a run (or anything else) changed in the folder, and measures the folder again.
func (a *ProjectsAccess) Ingest(id, reason string) error {
	if err := a.ps.Ingest(id, a.Dir(id), reason); err != nil {
		return err
	}
	if err := a.ps.SetSize(id, walkSize(a.Dir(id))); err != nil {
		return err
	}
	a.syncSongs(id)
	return nil
}

// syncSongs makes the songs of the library of a project what its manifest exports now, as the server does when a run ends: a run of the command line
// is told to the library the same way. A failure is a line of the log, not a failure of what was asked.
func (a *ProjectsAccess) syncSongs(id string) {
	if _, err := a.songs.syncFromProject(context.Background(), a.ps, id, a.Dir(id)); err != nil {
		slog.Warn("cannot update the song library of a project", "project", id, "err", err)
	}
}

// Save makes a text the definition of a project; the text it replaces is kept as a version (with the reason). The caller validates it first.
func (a *ProjectsAccess) Save(id string, definition []byte, reason string) (bool, error) {
	changed, err := a.ps.Save(id, definition, reason)
	if err == nil && changed {
		a.syncSongs(id)
	}
	return changed, err
}

// IsNoProject says whether an error is the one of an unknown project.
func IsNoProject(err error) bool { return err == ErrNoProject }

// Create keeps a new project with its definition (ErrProjectExists when the id is taken); its folder is made the first time it is written (Materialize).
func (a *ProjectsAccess) Create(id string, definition []byte) error {
	return a.ps.Create(id, definition)
}

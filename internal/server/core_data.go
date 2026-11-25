package server

import (
	"os"
	"path/filepath"

	"veritmo/internal/music/hyperframes"
	"veritmo/internal/music/motionref"
	"veritmo/internal/music/musicmotion"
	"veritmo/internal/platform/settings"
	"veritmo/internal/platform/settingsdb"
	"veritmo/internal/platform/store"
	"veritmo/internal/prompts"
	"veritmo/internal/server/music"
	"veritmo/internal/server/workspace"
	"veritmo/internal/video/motionlib"
)

// DataRegistry is what the database of the program is told: the tables, the migrations and the importers of every area, asked for one by one
// here and nowhere else. The server and the commands of `orchestrator` open their database with it, so they all see the same schema; an area
// that moves its state to the database adds its line below (with what it needs, given as arguments).
func DataRegistry(projectsDir string, opts ...DataOption) *store.Registry {
	var d dataDirs
	for _, o := range opts {
		o(&d)
	}
	r := store.NewRegistry()
	workspace.RegisterStore(r)
	settingsdb.RegisterStore(r)
	workspace.RegisterProjectsImporter(r, projectsDir)
	workspace.RegisterLibraryImporter(r, projectsDir)
	musicmotion.RegisterStore(r, musicmotion.UserFolders(settings.Dir()))
	motionref.RegisterStore(r, motionref.UserFolders(settings.Dir()))
	music.RegisterStore(r, projectsDir)
	prompts.RegisterStore(r, prompts.StoreDir(d.media, projectsDir))
	hyperframes.RegisterStore(r, hyperframes.ResolveDir(os.Getenv))
	motionlib.RegisterStore(r, filepath.Join(settings.Dir(), "motions"))
	return r
}

// DataOption says something more about where the state kept in files is, for the importers that need it.
type DataOption func(*dataDirs)

type dataDirs struct{ media string }

// WithMediaDir gives the media directory the program was started with (without it the importers look for it as the program does: MEDIA_DIR, then the
// file media-dir next to settings.json).
func WithMediaDir(dir string) DataOption { return func(d *dataDirs) { d.media = dir } }

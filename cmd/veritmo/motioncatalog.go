package main

import (
	"fmt"
	"os"

	"veritmo/internal/music/musicmotion"
	"veritmo/internal/platform/settings"
	"veritmo/internal/platform/store"
)

// defaultDB is the SQLite file of the server (what `serve --db` defaults to): the database the user's parts are kept in unless Settings
// names another.
const defaultDB = ".orchestrator/orchestrator.db"

// motionCatalog opens the database and makes the catalog of parts on it: the ones that come with the program and the user's, which the
// database keeps. The caller closes the store. A database that is not migrated is an error that says to run `veritmo data migrate`.
func motionCatalog(dbPath string, d settings.Database) (musicmotion.Catalog, *store.Store, error) {
	st, err := openStore(dbPath, "", d)
	if err != nil {
		return musicmotion.Catalog{}, nil, err
	}
	if err := st.CheckMigrated(); err != nil {
		_ = st.Close()
		return musicmotion.Catalog{}, nil, err
	}
	return musicmotion.NewCatalog(musicmotion.NewDBParts(st)), st, nil
}

// lazyMotionParts is the engine the pipeline runs a project with: it reads the user's parts from the database the first time a scene names
// a part, so a command that runs a project with no motion part opens no database. With no database yet (no SQLite file and none saved in
// Settings) there are no user's parts to read, and the engine has the ones that come with the program.
func lazyMotionParts(dbPath string) musicmotion.Parts {
	return musicmotion.NewLazyParts(func() (musicmotion.Catalog, error) {
		d := settings.GetDatabase()
		if !d.IsRemote() {
			if _, err := os.Stat(dbPath); err != nil {
				return musicmotion.Catalog{Builtin: musicmotion.BuiltinParts()}, nil
			}
		}
		cat, _, err := motionCatalog(dbPath, d) // the store stays open for the life of the command
		if err != nil {
			return musicmotion.Catalog{}, fmt.Errorf("%w", err)
		}
		return cat, nil
	})
}

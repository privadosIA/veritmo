package main

// settingssource.go: where the commands read and write the settings (change move-settings-and-catalogs-to-database): in the database, like the server,
// so that a command and the server never disagree. The database is opened the first time a setting is read or written, not before: a command that
// never touches a setting (a conversion of a file, a version) opens nothing. Before the first start of the server there is no database file and the
// settings are still the file, so a fresh checkout works as it did.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"veritmo/internal/music/hyperframes"
	"veritmo/internal/music/musicmotion"
	"veritmo/internal/platform/settings"
	"veritmo/internal/platform/settingsdb"
	"veritmo/internal/platform/store"
)

// useDatabaseSettings makes the settings of this process those of the database at dbPath (or of the MySQL or Postgres saved in Settings), and the
// catalog of HyperFrames blocks that the layers of the music motion mode read the one in that database. It returns the lazy source.
func useDatabaseSettings(dbPath, projectsDir string, warn io.Writer) *lazySettings {
	l := &lazySettings{dbPath: dbPath, projectsDir: projectsDir, warn: warn}
	settings.UseSource(l)
	prev := musicmotion.BlockStore
	musicmotion.BlockStore = func() hyperframes.Store {
		if _, err := l.open(); err == nil && l.st != nil {
			return hyperframes.NewStoreDB(hyperframes.ResolveDir(os.Getenv), l.st)
		}
		return prev() // no database yet (or one that cannot be used, said once by the settings): the folder of the blocks
	}
	return l
}

// lazySettings is a settings.Source that opens its database on first use.
type lazySettings struct {
	dbPath, projectsDir string
	warn                io.Writer

	once, warned sync.Once
	src          settings.Source
	st           *store.Store // nil while the settings are the file
	err          error
}

// open opens the database the first time it is called and says what it opened: the database, the settings file while there is no database yet, or the
// reason it cannot be used. It is called for every use and does the work once.
func (l *lazySettings) open() (settings.Source, error) {
	l.once.Do(func() {
		d := settings.GetDatabase()
		if !d.IsRemote() {
			if _, err := os.Stat(l.dbPath); errors.Is(err, os.ErrNotExist) {
				l.src = settings.FileSource() // no database yet: nothing was ever imported, the file is the truth
				return
			}
		}
		st, err := openStore(l.dbPath, l.projectsDir, d)
		if err == nil {
			if err = st.CheckMigrated(); err != nil {
				_ = st.Close()
			}
		}
		if err != nil {
			l.err = fmt.Errorf("the settings are kept in the database and it cannot be used: %w", err)
			return
		}
		l.src, l.st = settingsdb.New(st), st
	})
	return l.src, l.err
}

// Load reads the settings. When the database cannot be used the reader gets no settings, and is told so once; a writer never saves over them
// (settings.ReadFile hands it the error).
func (l *lazySettings) Load() (settings.File, error) {
	src, err := l.open()
	if err != nil {
		l.warned.Do(func() { fmt.Fprintf(l.warn, "warning: %v\n", err) })
		return settings.File{}, err
	}
	return src.Load()
}

// Save writes the settings, or fails when the database cannot be used.
func (l *lazySettings) Save(f settings.File) error {
	src, err := l.open()
	if err != nil {
		return err
	}
	return src.Save(f)
}

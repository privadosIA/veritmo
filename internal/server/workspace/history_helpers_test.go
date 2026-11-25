package workspace

import (
	"veritmo/internal/platform/settings"
	"veritmo/internal/platform/store"
)

// openHistory opens (creating if needed) the SQLite history at path, migrated: what a test needs of a server's database.
func openHistory(path string) (*historyStore, error) {
	return openHistoryWith(path, settings.Database{}, "")
}

// openHistoryWith opens the history in the engine d names (SQLite at path when it names none) as the server called server.
func openHistoryWith(path string, d settings.Database, server string) (*historyStore, error) {
	reg := store.NewRegistry()
	RegisterStore(reg)
	st, err := store.Open(path, d, reg)
	if err != nil {
		return nil, err
	}
	if err := st.Migrate(); err != nil {
		return nil, err
	}
	return openHistoryOn(st, server)
}

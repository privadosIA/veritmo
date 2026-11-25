package settingsdb

// importer.go: the import of the settings that were kept in settings.json (spec data-store; entity settings, design D6 of
// move-settings-and-catalogs-to-database): each setting and each provider becomes a row, and the three kinds of secret (the tokens of HeartMuLa and
// DiffRhythm2 and the secrets of the monitoring tools) are copied to the keys file when it has no entry for them. The file is not changed; cleaning
// it later rewrites it with only the database settings and the server name.

import (
	"context"
	"path/filepath"
	"sort"
	"time"

	"veritmo/internal/platform/settings"
	"veritmo/internal/platform/store"
)

// Importer reads settings.json.
type Importer struct{}

// Entity is the name of the entity.
func (Importer) Entity() string { return "settings" }

// Import creates the rows that are missing and copies the secrets that have no key yet. It never overwrites a row or a key.
func (Importer) Import(_ context.Context, tx *store.Store) (store.Report, error) {
	var rep store.Report
	f, err := settings.ReadSettingsFile()
	if err != nil {
		rep.Skipped = append(rep.Skipped, store.Skipped{Path: settings.Path(), Reason: err.Error()})
		return rep, nil
	}
	want, err := values(f)
	if err != nil {
		return rep, err
	}
	now := time.Now().UTC()
	names := make([]string, 0, len(want))
	for k := range want {
		names = append(names, k)
	}
	sort.Strings(names)
	var rows []Setting
	for _, k := range names {
		rows = append(rows, Setting{Key: k, Value: store.Text(want[k]), UpdatedAt: now})
	}
	created, existing, err := store.InsertMissing(tx, rows)
	if err != nil {
		return rep, err
	}
	rep.Created, rep.Existing = created, existing
	var ps []Provider
	for i, p := range f.Providers {
		ps = append(ps, Provider{ID: p.ID, Position: i, Name: p.Name, Kind: p.Type, UpdatedAt: now, Doc: store.JSON[settings.Provider]{V: p}})
	}
	created, existing, err = store.InsertMissing(tx, ps)
	if err != nil {
		return rep, err
	}
	rep.Created += created
	rep.Existing += existing
	// a secret goes to the keys file only where there is none: a key the user has set since is not replaced by an older one
	toCopy := map[string]string{}
	for id, v := range secrets(f, nil) {
		if v != "" && !settings.HasKey(id) {
			toCopy[id] = v
		}
	}
	if len(toCopy) > 0 {
		if err := settings.SyncKeys(toCopy); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// LegacyFiles is settings.json when it holds more than the bootstrap fields (what cleaning would take out). The keys file is never listed.
func (Importer) LegacyFiles() ([]store.LegacyFile, error) {
	n, err := settings.LegacyBytes()
	if err != nil || n == 0 {
		return nil, err
	}
	return []store.LegacyFile{{Path: filepath.Clean(settings.Path()), Bytes: n}}, nil
}

// CleanLegacy rewrites settings.json with only the database settings and the server name (it never deletes it) and says how many bytes that freed.
func (Importer) CleanLegacy() (int64, error) { return settings.CleanFile() }

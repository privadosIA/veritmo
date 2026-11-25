package musicmotion

// partsimport.go: the import of the user's parts that were kept as folders into the database (spec data-store; entity music-motion/parts).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"veritmo/internal/platform/store"
)

// Folders are the folders the state of the music motion engine was kept in before the database: what the importers read.
type Folders struct {
	// Parts is the folder of the user's parts (one folder per part).
	Parts string
	// Drafts is the folder of the drafts of parts (one folder per draft).
	Drafts string
	// State is the folder of the music motion state, where render-times.json is.
	State string
	// Library is the folder of the user's characters and backgrounds (characters/ and backgrounds/ inside).
	Library string
}

// UserFolders are the folders under the settings folder (settings.Dir) where the engine kept its state.
func UserFolders(settingsDir string) Folders {
	return Folders{Parts: filepath.Join(settingsDir, "music-motion", "parts"), Drafts: filepath.Join(settingsDir, "music-motion", "part-drafts"), State: filepath.Join(settingsDir, "music-motion"), Library: filepath.Join(settingsDir, "music-motion", "library")}
}

type partsImporter struct{ dir string }

func (partsImporter) Entity() string { return "music-motion/parts" }

// partFiles are the state files of a part folder.
var partFiles = []string{"manifest.json", originFile, "part.html"}

// readPartDir reads the part in the folder dir/name as a row, or says why it cannot be one: the reason is what the import records.
func readPartDir(dir, name string) (partRow, error) {
	raw, err := os.ReadFile(filepath.Join(dir, name, "manifest.json"))
	if err != nil {
		return partRow{}, fmt.Errorf("the folder has no manifest.json")
	}
	frag, _ := os.ReadFile(filepath.Join(dir, name, "part.html"))
	var origin *PartOrigin
	if b, err := os.ReadFile(filepath.Join(dir, name, originFile)); err == nil {
		var o PartOrigin
		if json.Unmarshal(b, &o) == nil {
			origin = &o
		}
	}
	if _, err := parsePart(name, raw, string(frag), origin, "user"); err != nil { // the checks a folder is read with, and its name
		return partRow{}, err
	}
	r, err := rowOf(raw, string(frag), origin)
	if err != nil {
		return partRow{}, err
	}
	if st, err := os.Stat(filepath.Join(dir, name)); err == nil {
		r.CreatedAt, r.UpdatedAt = st.ModTime().UTC(), st.ModTime().UTC()
	}
	return r, nil
}

func (i partsImporter) Import(ctx context.Context, tx *store.Store) (store.Report, error) {
	var rep store.Report
	entries, err := os.ReadDir(i.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return rep, nil
		}
		return rep, err
	}
	var rows []partRow
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		r, err := readPartDir(i.dir, e.Name())
		if err != nil {
			rep.Skipped = append(rep.Skipped, store.Skipped{Path: filepath.Join(i.dir, e.Name()), Reason: err.Error()})
			continue
		}
		rows = append(rows, r)
	}
	rep.Created, rep.Existing, err = store.InsertMissing(tx, rows)
	return rep, err
}

func (i partsImporter) LegacyFiles() ([]store.LegacyFile, error) {
	entries, err := os.ReadDir(i.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []store.LegacyFile
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		for _, f := range partFiles {
			p := filepath.Join(i.dir, e.Name(), f)
			if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
				out = append(out, store.LegacyFile{Path: p, Bytes: st.Size()})
			}
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Path < out[b].Path })
	return out, nil
}

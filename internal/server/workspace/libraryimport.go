package workspace

// libraryimport.go: the import of the song library that was kept as files into the database (spec data-store; entity library, design D5 of
// move-song-library-to-database): the songs every music project exports (its manifest), then the stars and playlists of projects/.library.json.
// A star for a song that does not exist and a place in a playlist for one are left out and reported. Nothing is overwritten and no file is changed.

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"veritmo/internal/platform/store"
	"veritmo/internal/project/config"
)

// RegisterLibraryImporter adds to a registry the importer of the song library kept in the projects folder projectsDir (server.DataRegistry calls
// it). The lengths of the audio are measured with the ffmpeg of the machine; without it the songs are imported with none, and a rescan fills them.
func RegisterLibraryImporter(r *store.Registry, projectsDir string) {
	r.AddImporter(LibraryImporter{Dir: projectsDir, Measure: execMeasure})
}

// LibraryImporter reads the manifests of the projects and the file of the stars and playlists.
type LibraryImporter struct {
	Dir string
	// Measure is the length of an audio file in seconds, 0 when it cannot be told; nil measures nothing.
	Measure func(ctx context.Context, path string) float64
}

// Entity is the name of the entity.
func (LibraryImporter) Entity() string { return "library" }

// After says the songs are made from the rows of the projects, which are imported first.
func (LibraryImporter) After() []string { return []string{"projects"} }

// legacyMeta reads the file of the stars and playlists; ok is false when there is none.
func (i LibraryImporter) legacyMeta() (m libraryMeta, ok bool, err error) {
	raw, err := os.ReadFile(filepath.Join(i.Dir, libraryMetaFile))
	if os.IsNotExist(err) {
		return m, false, nil
	}
	if err != nil {
		return m, false, err
	}
	return m, true, json.Unmarshal(raw, &m)
}

// Import creates the songs, stars and playlists that are missing.
func (i LibraryImporter) Import(ctx context.Context, tx *store.Store) (store.Report, error) {
	var rep store.Report
	var existing []string
	if err := tx.DB.Model(&SongRow{}).Pluck("song_key", &existing).Error; err != nil {
		return rep, err
	}
	have := map[string]bool{}
	for _, k := range existing {
		have[k] = true
	}
	var projects []ProjectRow
	if err := tx.DB.Select("id", "client", "definition").Where("mode = ? AND valid = ?", string(config.ModeMusic), true).Order("id").Find(&projects).Error; err != nil {
		return rep, err
	}
	var rows []SongRow
	measured := 0
	for _, p := range projects {
		cfg, err := config.LoadBytes([]byte(p.Definition))
		if err != nil {
			continue
		}
		for _, f := range songsOf(p.ID, filepath.Join(i.Dir, p.ID), cfg, p.Client) {
			if have[f.song.Key] {
				rep.Existing++
				continue
			}
			have[f.song.Key] = true
			row := rowOf(f.song, f.createdAt.UTC())
			if st, err := os.Stat(f.path); err == nil {
				row.FileSize, row.FileMTime = st.Size(), st.ModTime().UnixNano()
			}
			if i.Measure != nil {
				if row.Duration = i.Measure(ctx, f.path); row.Duration > 0 {
					measured++
				}
				row.Doc.V.Duration = row.Duration
			}
			row.UpdatedAt = time.Now().UTC()
			rows = append(rows, row)
		}
	}
	created, _, err := store.InsertMissing(tx, rows)
	if err != nil {
		return rep, err
	}
	rep.Created += created
	if created > 0 {
		slog.Info("song library imported", "songs", created, "measured", measured)
	}
	meta, ok, err := i.legacyMeta()
	if err != nil {
		rep.Skipped = append(rep.Skipped, store.Skipped{Path: filepath.Join(i.Dir, libraryMetaFile), Reason: err.Error()})
		return rep, nil
	}
	if !ok {
		return rep, nil
	}
	return i.importMeta(tx, meta, rep)
}

// importMeta puts the stars and the playlists of the file on the songs that exist.
func (i LibraryImporter) importMeta(tx *store.Store, meta libraryMeta, rep store.Report) (store.Report, error) {
	file := filepath.Join(i.Dir, libraryMetaFile)
	skip := func(reason string) { rep.Skipped = append(rep.Skipped, store.Skipped{Path: file, Reason: reason}) }
	var keys []string
	if err := tx.DB.Model(&SongRow{}).Pluck("song_key", &keys).Error; err != nil {
		return rep, err
	}
	exists := make(map[string]bool, len(keys))
	for _, k := range keys {
		exists[k] = true
	}
	stars := make([]string, 0, len(meta.Ratings))
	for k := range meta.Ratings {
		stars = append(stars, k)
	}
	sort.Strings(stars)
	for _, k := range stars {
		v := meta.Ratings[k]
		switch {
		case !exists[k]:
			skip("the stars of " + k + " are left out: it is not a song of the library")
		case v < 1 || v > 5:
			skip("the stars of " + k + " are left out: they are not from 1 to 5")
		default:
			// a song that already has stars keeps them
			if err := tx.DB.Model(&SongRow{}).Where("song_key = ? AND rating = 0", k).Update("rating", v).Error; err != nil {
				return rep, err
			}
		}
	}
	var next int64
	if err := tx.DB.Model(&PlaylistRow{}).Count(&next).Error; err != nil {
		return rep, err
	}
	for _, p := range meta.Playlists {
		var n int64
		if err := tx.DB.Model(&PlaylistRow{}).Where("id = ?", p.ID).Count(&n).Error; err != nil {
			return rep, err
		}
		if n > 0 {
			rep.Existing++
			continue
		}
		now := time.Now().UTC()
		if err := tx.DB.Create(&PlaylistRow{ID: p.ID, Name: p.Name, Position: int(next), CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			return rep, err
		}
		next++
		var kept []string
		for _, k := range p.Songs {
			if exists[k] {
				kept = append(kept, k)
			} else {
				skip("the place of " + k + " in the playlist " + p.ID + " is dropped: it is not a song of the library")
			}
		}
		if err := writePlaces(tx, p.ID, kept); err != nil {
			return rep, err
		}
		rep.Created++
	}
	return rep, nil
}

// LegacyFiles is the file of the stars and playlists, which stays on disk after the import. The manifests of the projects are the output of the
// runs, not state of the library, and are never listed.
func (i LibraryImporter) LegacyFiles() ([]store.LegacyFile, error) {
	fi, err := os.Stat(filepath.Join(i.Dir, libraryMetaFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []store.LegacyFile{{Path: filepath.Join(i.Dir, libraryMetaFile), Bytes: fi.Size()}}, nil
}

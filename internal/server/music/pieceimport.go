package music

// pieceimport.go: the import of the pieces that were kept as folders (piece.json and its six documents) into the database (spec data-store;
// entity music-motion/pieces).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"veritmo/internal/music/musicmotion"
	"veritmo/internal/platform/store"
)

// pieceImporter reads the pieces in the projects folder (the folders named api-mm-...).
type pieceImporter struct{ projects string }

func (pieceImporter) Entity() string { return "music-motion/pieces" }

// pieceStateFiles are the state files of a piece folder: its record and its documents. The song, the vocal stem, the frames and what a render
// writes are media and are never imported or listed.
func pieceStateFiles() []string {
	files := []string{pieceRecord}
	for _, k := range musicmotion.DocKinds {
		files = append(files, musicmotion.DocFile(k))
	}
	return files
}

const pieceRecord = "piece.json"

func (i pieceImporter) folders() []string {
	entries, err := os.ReadDir(i.projects)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), piecePrefix) {
			out = append(out, e.Name())
		}
	}
	return out
}

func (i pieceImporter) Import(ctx context.Context, tx *store.Store) (store.Report, error) {
	var rep store.Report
	var rows []pieceRow
	var docs []pieceDocRow
	for _, id := range i.folders() {
		dir := filepath.Join(i.projects, id)
		raw, err := os.ReadFile(filepath.Join(dir, pieceRecord))
		if err != nil {
			rep.Skipped = append(rep.Skipped, store.Skipped{Path: dir, Reason: "the folder has no piece.json"})
			continue
		}
		var info pieceInfo
		if err := json.Unmarshal(raw, &info); err != nil || info.ID != id {
			reason := "piece.json is not valid"
			if err == nil {
				reason = "piece.json names another piece (" + info.ID + ")"
			}
			rep.Skipped = append(rep.Skipped, store.Skipped{Path: filepath.Join(dir, pieceRecord), Reason: reason})
			continue
		}
		rows = append(rows, rowOfPiece(&info))
		for _, kind := range musicmotion.DocKinds {
			body, err := os.ReadFile(filepath.Join(dir, musicmotion.DocFile(kind)))
			if err != nil {
				continue
			}
			if !json.Valid(body) {
				rep.Skipped = append(rep.Skipped, store.Skipped{Path: filepath.Join(dir, musicmotion.DocFile(kind)), Reason: "it is not valid JSON"})
				continue
			}
			docs = append(docs, pieceDocRow{PieceID: id, Kind: kind, Body: store.Text(body)})
		}
	}
	var err error
	rep.Created, rep.Existing, err = store.InsertMissing(tx, rows)
	if err != nil {
		return rep, err
	}
	if _, _, err := store.InsertMissing(tx, docs); err != nil {
		return rep, err
	}
	return rep, nil
}

func (i pieceImporter) LegacyFiles() ([]store.LegacyFile, error) {
	var out []store.LegacyFile
	for _, id := range i.folders() {
		for _, f := range pieceStateFiles() {
			p := filepath.Join(i.projects, id, f)
			if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
				out = append(out, store.LegacyFile{Path: p, Bytes: st.Size()})
			}
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Path < out[b].Path })
	return out, nil
}

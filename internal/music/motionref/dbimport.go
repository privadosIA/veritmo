package motionref

// dbimport.go: the import of the references, the learnings and the styles that were kept as files into the database (entities music-motion/references,
// music-motion/learnings and music-motion/presets). Nothing is overwritten and no file is touched; the key frames of a reference stay where they are.

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

func listDir(dir string) []os.DirEntry {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	return entries
}

func sortedLegacy(out []store.LegacyFile) []store.LegacyFile {
	sort.Slice(out, func(a, b int) bool { return out[a].Path < out[b].Path })
	return out
}

func legacyOf(path string) (store.LegacyFile, bool) {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return store.LegacyFile{}, false
	}
	return store.LegacyFile{Path: path, Bytes: st.Size()}, true
}

// --- references -----------------------------------------------------------------------------------

type referencesImporter struct{ dir string }

func (referencesImporter) Entity() string { return "music-motion/references" }

func (i referencesImporter) Import(ctx context.Context, tx *store.Store) (store.Report, error) {
	var rep store.Report
	var rows []referenceRow
	for _, e := range listDir(i.dir) {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") { // .upload-* and .work-* are work in progress
			continue
		}
		dir := filepath.Join(i.dir, e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, "reference.json"))
		if err != nil {
			rep.Skipped = append(rep.Skipped, store.Skipped{Path: dir, Reason: "the folder has no reference.json"})
			continue
		}
		var ref Reference
		if json.Unmarshal(raw, &ref) != nil || ref.ID != e.Name() || !ValidID(ref.ID) {
			rep.Skipped = append(rep.Skipped, store.Skipped{Path: filepath.Join(dir, "reference.json"), Reason: "reference.json is not valid or names another reference"})
			continue
		}
		if ref.Tags == nil {
			ref.Tags = []string{}
		}
		doc, err := referenceDoc(ref)
		if err != nil {
			return rep, err
		}
		rows = append(rows, referenceRow{ID: ref.ID, State: ref.State, Title: clipTo(ref.Title, 255), Video: clipTo(ref.Source, 1000), CreatedAt: ref.CreatedAt, Doc: doc})
	}
	var err error
	rep.Created, rep.Existing, err = store.InsertMissing(tx, rows)
	return rep, err
}

func (i referencesImporter) LegacyFiles() ([]store.LegacyFile, error) {
	var out []store.LegacyFile
	for _, e := range listDir(i.dir) {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			if f, ok := legacyOf(filepath.Join(i.dir, e.Name(), "reference.json")); ok {
				out = append(out, f)
			}
		}
	}
	return sortedLegacy(out), nil
}

// --- learnings ------------------------------------------------------------------------------------

type learningsImporter struct{ dir string }

func (learningsImporter) Entity() string { return "music-motion/learnings" }

func (i learningsImporter) Import(ctx context.Context, tx *store.Store) (store.Report, error) {
	var rep store.Report
	var rows []learningRow
	for _, e := range listDir(i.dir) {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".json" {
			continue
		}
		path := filepath.Join(i.dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			rep.Skipped = append(rep.Skipped, store.Skipped{Path: path, Reason: err.Error()})
			continue
		}
		var l Learning
		if json.Unmarshal(raw, &l) != nil || l.ID+".json" != name || !ValidLearningID(l.ID) {
			rep.Skipped = append(rep.Skipped, store.Skipped{Path: path, Reason: "the file is not a learning, or names another one"})
			continue
		}
		l.fill()
		b, err := json.MarshalIndent(l, "", "  ")
		if err != nil {
			return rep, err
		}
		rows = append(rows, learningRow{ID: l.ID, ReferenceID: l.ReferenceID, State: l.State, CreatedAt: l.CreatedAt, Doc: store.Text(b)})
	}
	var err error
	rep.Created, rep.Existing, err = store.InsertMissing(tx, rows)
	return rep, err
}

func (i learningsImporter) LegacyFiles() ([]store.LegacyFile, error) {
	var out []store.LegacyFile
	for _, e := range listDir(i.dir) {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			if f, ok := legacyOf(filepath.Join(i.dir, e.Name())); ok {
				out = append(out, f)
			}
		}
	}
	return sortedLegacy(out), nil
}

// --- styles ---------------------------------------------------------------------------------------

type presetsImporter struct{ dir string }

func (presetsImporter) Entity() string { return "music-motion/presets" }

func (i presetsImporter) Import(ctx context.Context, tx *store.Store) (store.Report, error) {
	var rep store.Report
	var rows []presetRow
	for _, e := range listDir(i.dir) {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".json" {
			continue
		}
		path := filepath.Join(i.dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			rep.Skipped = append(rep.Skipped, store.Skipped{Path: path, Reason: err.Error()})
			continue
		}
		var p musicmotion.Preset
		if json.Unmarshal(raw, &p) != nil || p.ID+".json" != name || !ValidPresetID(p.ID) {
			rep.Skipped = append(rep.Skipped, store.Skipped{Path: path, Reason: "the file is not a style, or names another one"})
			continue
		}
		if p.Parts == nil {
			p.Parts = []string{}
		}
		if p.Adjustments == nil {
			p.Adjustments = []string{}
		}
		b, err := json.MarshalIndent(p, "", "  ")
		if err != nil {
			return rep, err
		}
		rows = append(rows, presetRow{ID: p.ID, Name: clipTo(p.Name, 255), CreatedAt: p.CreatedAt, Doc: store.Text(b)})
	}
	var err error
	rep.Created, rep.Existing, err = store.InsertMissing(tx, rows)
	return rep, err
}

func (i presetsImporter) LegacyFiles() ([]store.LegacyFile, error) {
	var out []store.LegacyFile
	for _, e := range listDir(i.dir) {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			if f, ok := legacyOf(filepath.Join(i.dir, e.Name())); ok {
				out = append(out, f)
			}
		}
	}
	return sortedLegacy(out), nil
}

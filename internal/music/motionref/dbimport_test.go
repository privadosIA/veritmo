package motionref

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/platform/store"
)

func importInto(t *testing.T, dirs Folders, entity string) (store.ImportRecord, *store.Store) {
	t.Helper()
	reg := store.NewRegistry()
	RegisterStore(reg, dirs)
	st, err := store.OpenMemory(reg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	recs, err := st.RunImports(context.Background(), entity)
	if err != nil || len(recs) != 1 {
		t.Fatalf("%+v %v", recs, err)
	}
	return recs[0], st
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTheReferenceFoldersBecomeRowsAndTheirFramesAreStillThere(t *testing.T) {
	dir := t.TempDir()
	id := "ref-20261110-100000-aaaaaa"
	write(t, filepath.Join(dir, id, "reference.json"), `{"id":"`+id+`","title":"neon","tags":["a"],"state":"ready","created_at":"2026-11-10T10:00:00Z","analysis":{"duration":6,"cuts":1,"mean_shot":3}}`)
	write(t, filepath.Join(dir, id, "frames", "00.jpg"), "jpg")
	write(t, filepath.Join(dir, "ref-20261110-100100-bbbbbb", "frames", "00.jpg"), "no record")
	write(t, filepath.Join(dir, ".work-x", "reference.json"), "{}")
	rec, st := importInto(t, Folders{References: dir}, "music-motion/references")
	if rec.Created != 1 || len(rec.Skipped) != 1 {
		t.Fatalf("%+v", rec)
	}
	s := NewStoreDB(dir, st)
	got, err := s.Get(id)
	if err != nil || got.Title != "neon" || got.Analysis == nil || got.Analysis.MeanShot != 3 {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(s.FramesDir(id), "00.jpg")); err != nil {
		t.Fatal("the key frames are where they were")
	}
	if _, err := os.Stat(filepath.Join(dir, id, "reference.json")); err != nil {
		t.Fatal("the file is untouched")
	}
	files, _ := referencesImporter{dir: dir}.LegacyFiles()
	if len(files) != 1 || !strings.HasSuffix(files[0].Path, "reference.json") {
		t.Fatalf("only the record is legacy: %+v", files)
	}
	if recs, _ := st.RunImports(context.Background(), "music-motion/references"); recs[0].Created != 0 || recs[0].Existing != 1 {
		t.Fatalf("a second run creates nothing: %+v", recs)
	}
}

func TestTheLearningFilesBecomeRows(t *testing.T) {
	dir := t.TempDir()
	id := "learning-20261110-100000-aaaaaa"
	write(t, filepath.Join(dir, id+".json"), `{"id":"`+id+`","reference_id":"ref-x","state":"ready","strictness":"normal","created_at":"2026-11-10T10:00:00Z"}`)
	write(t, filepath.Join(dir, "learning-20261110-100100-bbbbbb.json"), `{"id":"learn-other"}`)
	write(t, filepath.Join(dir, "notes.txt"), "x")
	rec, st := importInto(t, Folders{Learnings: dir}, "music-motion/learnings")
	if rec.Created != 1 || len(rec.Skipped) != 1 {
		t.Fatalf("%+v", rec)
	}
	l, err := NewLearningStoreDB(st).Get(id)
	if err != nil || l.State != LearningReady || l.Styles == nil || len(NewLearningStoreDB(st).List("ref-x")) != 1 {
		t.Fatalf("%+v %v", l, err)
	}
	if recs, _ := st.RunImports(context.Background(), "music-motion/learnings"); recs[0].Created != 0 {
		t.Fatalf("%+v", recs)
	}
}

func TestTheStyleFilesBecomeRows(t *testing.T) {
	dir := t.TempDir()
	id := "preset-20261110-100000-aaaaaa"
	write(t, filepath.Join(dir, id+".json"), `{"id":"`+id+`","name":"neon","created_at":"2026-11-10T10:00:00Z"}`)
	write(t, filepath.Join(dir, "preset-bad.json"), `{`)
	rec, st := importInto(t, Folders{Presets: dir}, "music-motion/presets")
	if rec.Created != 1 || len(rec.Skipped) != 1 {
		t.Fatalf("%+v", rec)
	}
	p, err := NewPresetStoreDB(st).Get(id)
	if err != nil || p.Name != "neon" || p.Parts == nil || p.Adjustments == nil {
		t.Fatalf("%+v %v", p, err)
	}
	if recs, _ := st.RunImports(context.Background(), "music-motion/presets"); recs[0].Created != 0 {
		t.Fatalf("%+v", recs)
	}
	if missing, _ := st.CheckSchema(); len(missing) > 0 {
		t.Fatal(missing)
	}
}

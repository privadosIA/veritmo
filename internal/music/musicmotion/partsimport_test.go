package musicmotion

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/platform/store"
)

func writePartFolder(t *testing.T, dir, name string, manifest []byte, fragment string, origin string) {
	t.Helper()
	d := filepath.Join(dir, name)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "manifest.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	if fragment != "" {
		if err := os.WriteFile(filepath.Join(d, "part.html"), []byte(fragment), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if origin != "" {
		if err := os.WriteFile(filepath.Join(d, "origin.json"), []byte(origin), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// media beside the state files (never imported, never listed)
	if err := os.WriteFile(filepath.Join(d, "preview.png"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func importStore(t *testing.T, dir string) *store.Store {
	t.Helper()
	reg := store.NewRegistry()
	RegisterStore(reg, Folders{Parts: dir})
	st, err := store.OpenMemory(reg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestTheUsersPartFoldersBecomeRowsAndAnInvalidOneIsSkippedWithItsReason(t *testing.T) {
	dir := t.TempDir()
	writePartFolder(t, dir, "clock", manifestOf("clock"), `<div>tick</div>`, `{"title":"A video","video":"https://example.test/v","approved_at":"2026-11-01T10:00:00Z"}`)
	writePartFolder(t, dir, "hf-card", manifestOf("hf-card"), `<p>card</p>`, `{"source":"hyperframes","block":"card","approved_at":"2026-11-02T10:00:00Z"}`)
	writePartFolder(t, dir, "scene-abc-1", testSceneManifest("scene-abc-1", "piece-a", "p1"), `<p>s</p>`, "")
	writePartFolder(t, dir, "broken", manifestOf("broken"), `<img src="https://x.invalid/a.png">`, "")
	writePartFolder(t, dir, "mismatch", manifestOf("other"), `<p>x</p>`, "")
	if err := os.MkdirAll(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	st := importStore(t, dir)
	recs, err := st.RunImports(context.Background(), "music-motion/parts")
	if err != nil || len(recs) != 1 {
		t.Fatalf("%+v %v", recs, err)
	}
	r := recs[0]
	if r.Entity != "music-motion/parts" || r.State != store.ImportDone || r.Created != 3 || len(r.Skipped) != 3 {
		t.Fatalf("%+v", r)
	}
	reasons := map[string]string{}
	for _, s := range r.Skipped {
		reasons[filepath.Base(s.Path)] = s.Reason
	}
	if !strings.Contains(reasons["broken"], "external resource") || !strings.Contains(reasons["mismatch"], "folder") || !strings.Contains(reasons["empty"], "manifest.json") {
		t.Fatalf("each skipped folder says why: %v", reasons)
	}
	// the parts are in the catalog, with their origin, and the files are untouched
	cat := Catalog{Builtin: builtin(), Parts: NewDBParts(st)}
	p, ok := cat.Get("hf-card")
	if !ok || p.Origin == nil || p.Origin.Source != "hyperframes" {
		t.Fatalf("%+v %v", p, ok)
	}
	if s, ok := cat.Get("scene-abc-1"); !ok || s.Scene == nil || s.Scene.Piece != "piece-a" {
		t.Fatalf("%+v %v", s, ok)
	}
	if _, err := os.Stat(filepath.Join(dir, "clock", "manifest.json")); err != nil {
		t.Fatal("the import never touches a file")
	}
	// a second run imports nothing and a part changed since is left as it is
	st.DB.Model(&partRow{}).Where("name = ?", "clock").Update("fragment_html", "<p>edited</p>")
	recs, err = st.RunImports(context.Background(), "music-motion/parts")
	if err != nil || recs[0].Created != 0 || recs[0].Existing != 3 {
		t.Fatalf("%+v %v", recs, err)
	}
	if p, _ := cat.Get("clock"); p.Fragment != "<p>edited</p>" {
		t.Fatal("a row that exists is never overwritten")
	}
	if missing, _ := st.CheckSchema(); len(missing) > 0 {
		t.Fatal(missing)
	}
}

func TestThePartsImporterListsOnlyTheStateFilesAsLeftBehind(t *testing.T) {
	dir := t.TempDir()
	writePartFolder(t, dir, "clock", manifestOf("clock"), `<div>tick</div>`, `{"approved_at":"2026-11-01T10:00:00Z"}`)
	files, err := partsImporter{dir: dir}.LegacyFiles()
	if err != nil || len(files) != 3 {
		t.Fatalf("%+v %v", files, err)
	}
	for _, f := range files {
		if strings.HasSuffix(f.Path, "preview.png") {
			t.Fatalf("media is never listed: %s", f.Path)
		}
	}
	if files, err := (partsImporter{dir: filepath.Join(dir, "nope")}).LegacyFiles(); err != nil || len(files) != 0 {
		t.Fatalf("a folder that does not exist has nothing: %v %v", files, err)
	}
}

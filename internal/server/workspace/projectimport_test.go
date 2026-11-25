package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"veritmo/internal/platform/store"
)

func writeFile(t *testing.T, path, text string, mod time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if !mod.IsZero() {
		_ = os.Chtimes(path, mod, mod)
	}
}

func TestProjectsImporter_FoldersBecomeRowsVersionsPagesAndRecords(t *testing.T) {
	dir := t.TempDir()
	old, older := time.Now().Add(-48*time.Hour), time.Now().Add(-72*time.Hour)
	// a valid project with two backups and a page with its provenance
	writeFile(t, filepath.Join(dir, "demo", "scenes.yaml"), videoYAML, time.Time{})
	writeFile(t, filepath.Join(dir, "demo", "scenes.yaml.bak"), "# the first backup\n"+videoYAML, older)
	writeFile(t, filepath.Join(dir, "demo", "scenes.yaml.bak2"), "# the second backup\n"+videoYAML, old)
	writeFile(t, filepath.Join(dir, "demo", "motion", "a.html"), "<p>page</p>", time.Time{})
	writeFile(t, filepath.Join(dir, "demo", "motion", "a.meta.json"), `{"kind":"motion_graphic","model":"gemma"}`, time.Time{})
	writeFile(t, filepath.Join(dir, "demo", "motion", "a.song.json"), `{}`, time.Time{}) // not a page
	writeFile(t, filepath.Join(dir, "demo", "out", "final.mp4"), "0123456789", time.Time{})
	// an invalid one is imported too, marked as invalid
	writeFile(t, filepath.Join(dir, "broken", "scenes.yaml"), "project: [unclosed\n", time.Time{})
	// a managed project with its record
	writeFile(t, filepath.Join(dir, "api-gen-20261110-100000-aaaaaa", "scenes.yaml"), musicYAML, time.Time{})
	writeFile(t, filepath.Join(dir, "api-gen-20261110-100000-aaaaaa", "generation.json"),
		`{"id":"api-gen-20261110-100000-aaaaaa","kind":"generation","run_id":"run-1","created_at":"2026-11-10T10:00:00Z","request":{"client":"video-studio"}}`, time.Time{})
	// a folder with no scenes.yaml is not a project
	writeFile(t, filepath.Join(dir, "notes", "readme.txt"), "x", time.Time{})

	reg := store.NewRegistry()
	RegisterStore(reg)
	RegisterProjectsImporter(reg, dir)
	st, err := store.OpenMemory(reg)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	recs, err := st.RunImports(context.Background(), "projects")
	if err != nil || len(recs) != 1 || recs[0].Created != 3 {
		t.Fatalf("%+v %v", recs, err)
	}
	ps := newProjectStore(st)
	demo, err := ps.Get("demo")
	if err != nil || !demo.Valid || demo.Name != "Demo" || string(demo.Definition) != videoYAML || demo.SizeBytes == nil || *demo.SizeBytes < 10 {
		t.Fatalf("%+v %v", demo, err)
	}
	bad, _ := ps.Get("broken")
	if bad.Valid || bad.Error == "" {
		t.Fatalf("an invalid one is imported, marked invalid: %+v", bad)
	}
	vs, _ := ps.Versions("demo")
	if len(vs) != 2 || vs[0].Version != 2 || vs[1].Version != 1 || vs[0].Reason != "import" {
		t.Fatalf("%+v", vs)
	}
	if v1, _ := ps.Version("demo", 1); v1.Definition != store.Text("# the first backup\n"+videoYAML) {
		t.Fatalf("the oldest backup is version 1: %q", v1.Definition)
	}
	if html, meta, hasMeta, found := ps.Page("demo", "a", false); !found || html != "<p>page</p>" || !hasMeta || meta.Model != "gemma" {
		t.Fatalf("%q %+v %v %v", html, meta, hasMeta, found)
	}
	if pg, _ := ps.Pages("demo"); len(pg) != 1 {
		t.Fatalf("only the page is a page: %d", len(pg))
	}
	var rec ManagedRow
	if err := st.DB.Where("id = ?", "api-gen-20261110-100000-aaaaaa").Take(&rec).Error; err != nil || rec.Client != "video-studio" || rec.Kind != "generation" {
		t.Fatalf("%+v %v", rec, err)
	}
	if gen, _ := ps.Get("api-gen-20261110-100000-aaaaaa"); gen.Client != "video-studio" || gen.Kind != "generation" {
		t.Fatalf("the client of a managed project comes from its record: %+v", gen)
	}
	if ps.Exists("notes") {
		t.Fatal("a folder with no scenes.yaml is not a project")
	}
	// a second run creates nothing, and no file was changed or removed
	if recs, _ := st.RunImports(context.Background(), "projects"); recs[0].Created != 0 || recs[0].Existing != 3 {
		t.Fatalf("%+v", recs)
	}
	for _, f := range []string{"demo/scenes.yaml", "demo/scenes.yaml.bak", "demo/scenes.yaml.bak2", "demo/motion/a.html", "api-gen-20261110-100000-aaaaaa/generation.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	// the files that stay behind: the backups and the records, never the scenes.yaml or the pages
	files, _ := ProjectsImporter{Dir: dir}.LegacyFiles()
	got := map[string]bool{}
	for _, f := range files {
		got[filepath.Base(f.Path)] = true
	}
	if !got["scenes.yaml.bak"] || !got["scenes.yaml.bak2"] || !got["generation.json"] || got["scenes.yaml"] || got["a.html"] {
		t.Fatalf("%v", got)
	}
	if missing, _ := st.CheckSchema(); len(missing) > 0 {
		t.Fatal(missing)
	}
}

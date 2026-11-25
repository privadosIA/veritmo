package motionlib

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"veritmo/internal/platform/store"
)

func dbLibrary(t *testing.T, dir string) (*Store, *store.Store) {
	t.Helper()
	reg := store.NewRegistry()
	RegisterStore(reg, dir)
	st, err := store.OpenMemory(reg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	return NewStoreDB(dir, st), st
}

func titles(ms []Motion) string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Title)
	}
	return strings.Join(out, ",")
}

func TestStoreDB_ListPageSortsSearchesFiltersAndPages(t *testing.T) {
	s, _ := dbLibrary(t, t.TempDir())
	base := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	for i, c := range []struct {
		title, state string
		tags         []string
	}{
		{"Slow walk", StateReady, []string{"Walk", "slow"}},
		{"Dance", StateReady, []string{"party"}},
		{"Wave", StateFailed, nil},
		{"Another walk", StateProcessing, []string{"walk"}},
	} {
		id := fmt.Sprintf("mot-20261101-10000%d-abcde%d", i, i)
		if err := os.MkdirAll(filepath.Join(s.Dir, id), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := s.Save(Motion{ID: id, Title: c.title, Tags: c.tags, State: c.state, CreatedAt: base.Add(time.Duration(i) * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	list := func(state string, p store.ListParams) (string, int64) {
		ms, total, err := s.ListPage(state, p)
		if err != nil {
			t.Fatal(err)
		}
		return titles(ms), total
	}
	if got, total := list("", store.ListParams{}); got != "Another walk,Wave,Dance,Slow walk" || total != 4 {
		t.Fatalf("the most recent first: %s %d", got, total)
	}
	if got, _ := list("", store.ListParams{Sort: "created_at"}); got != "Slow walk,Dance,Wave,Another walk" {
		t.Fatalf("%s", got)
	}
	if got, _ := list("", store.ListParams{Sort: "title"}); got != "Another walk,Dance,Slow walk,Wave" {
		t.Fatalf("by title: %s", got)
	}
	if got, total := list("", store.ListParams{Q: "WALK"}); got != "Another walk,Slow walk" || total != 2 {
		t.Fatalf("q in the title and the tags: %s %d", got, total)
	}
	if got, _ := list("", store.ListParams{Q: "party"}); got != "Dance" {
		t.Fatalf("q in a tag: %s", got)
	}
	if got, total := list(StateReady, store.ListParams{}); got != "Dance,Slow walk" || total != 2 {
		t.Fatalf("state: %s %d", got, total)
	}
	if got, total := list("", store.ListParams{Limit: 2, Offset: 1}); got != "Wave,Dance" || total != 4 {
		t.Fatalf("a page: %s %d", got, total)
	}
	if got, total := list(StateReady, store.ListParams{Q: "walk", Limit: 5}); got != "Slow walk" || total != 1 {
		t.Fatalf("all together: %s %d", got, total)
	}
}

func TestStoreDB_ARecordIsARowAndNoMotionJSONIsWritten(t *testing.T) {
	s, st := dbLibrary(t, t.TempDir())
	m, err := s.Create("A walk", []string{"slow"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.Path(m.ID), "motion.json")); err == nil {
		t.Fatal("motion.json was written")
	}
	var n int64
	st.DB.Model(&Row{}).Where("id = ?", m.ID).Count(&n)
	if n != 1 {
		t.Fatalf("%d rows", n)
	}
	// a deleted motion is not brought back by a late Save (a preparation that ends after the delete)
	if err := s.Delete(m.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(m); err != ErrNotFound {
		t.Fatalf("%v", err)
	}
	st.DB.Model(&Row{}).Count(&n)
	if n != 0 {
		t.Fatalf("%d rows left after the delete", n)
	}
}

func TestImporter_TheRecordsOfTheMotionsBecomeRowsAndTheirFilesStay(t *testing.T) {
	dir := t.TempDir()
	files := NewStore(dir)
	ready, err := files.Create("Walk", []string{"slow", "walk"})
	if err != nil {
		t.Fatal(err)
	}
	ready.State, ready.Frames, ready.Duration = StateReady, 64, 4
	if err := files.Save(ready); err != nil {
		t.Fatal(err)
	}
	preview := files.PreviewPath(ready.ID)
	frame := filepath.Join(files.ControlDir(ready.ID), "0001.png")
	_ = os.WriteFile(preview, []byte("mp4"), 0o644)
	_ = os.WriteFile(frame, []byte("png"), 0o644)
	// a motion.json that is not one, and a folder with none
	broken := filepath.Join(dir, "mot-20261101-100000-bad000")
	_ = os.MkdirAll(broken, 0o755)
	_ = os.WriteFile(filepath.Join(broken, "motion.json"), []byte("{no"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "mot-20261101-100001-empty0"), 0o755)

	s, st := dbLibrary(t, dir)
	recs, err := st.RunImports(context.Background(), "motions")
	if err != nil || len(recs) != 1 || recs[0].State != store.ImportDone || recs[0].Created != 1 || len(recs[0].Skipped) != 1 {
		t.Fatalf("%+v %v", recs, err)
	}
	got, err := s.Get(ready.ID)
	if err != nil || got.Title != "Walk" || got.Frames != 64 || got.State != StateReady || len(got.Tags) != 2 || !got.CreatedAt.Equal(ready.CreatedAt) {
		t.Fatalf("%+v %v", got, err)
	}
	// the previews and the control frames are where they were, and a window is still found
	for _, p := range []string{preview, frame} {
		if _, err := os.Stat(p); err != nil {
			t.Fatal(err)
		}
	}
	if w, err := s.WindowAt(ready.ID, 0); err != nil || w.Dir != s.ControlDir(ready.ID) {
		t.Fatalf("%+v %v", w, err)
	}
	// a second import creates nothing and overwrites nothing
	got.Title = "Renamed after the import"
	if err := s.Save(got); err != nil {
		t.Fatal(err)
	}
	recs, _ = st.RunImports(context.Background(), "motions")
	if again, _ := s.Get(ready.ID); recs[0].Created != 0 || recs[0].Existing != 1 || again.Title != "Renamed after the import" {
		t.Fatalf("%+v %+v", recs[0], again)
	}
	// only the motion.json files are listed as left behind
	legacy, _ := Importer{Dir: dir}.LegacyFiles()
	if len(legacy) != 2 {
		t.Fatalf("%d", len(legacy))
	}
	for _, f := range legacy {
		if filepath.Base(f.Path) != "motion.json" {
			t.Fatalf("%+v", f)
		}
	}
}

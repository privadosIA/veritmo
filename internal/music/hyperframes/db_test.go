package hyperframes

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/platform/store"
)

func dbStore(t *testing.T, dir string) (Store, *store.Store) {
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

func names(bs []Block) string {
	var out []string
	for _, b := range bs {
		out = append(out, b.Name)
	}
	return strings.Join(out, ",")
}

func TestStoreDB_AListOfBlocksIsFilteredSortedAndPaged(t *testing.T) {
	s, _ := dbStore(t, t.TempDir())
	checked := func(state string) *CheckResult {
		switch state {
		case "ok":
			return &CheckResult{OK: true}
		case "failed":
			return &CheckResult{Problems: []string{"x"}}
		}
		return nil
	}
	for i, b := range []Block{
		{Name: "aurora", Title: "Zebra title", Description: "northern lights", Tags: []string{"sky", "Night"}, Checks: checked("ok")},
		{Name: "bokeh", Title: "Bokeh", Description: "soft lights", Tags: []string{"sky"}, Checks: checked("failed")},
		{Name: "comp-a", Title: "A component", Kind: "component", Family: "Text", Tags: []string{"night"}},
		{Name: "comp-b", Title: "Another", Kind: "component", Family: "shapes", Checks: checked("ok")},
	} {
		if err := s.Save(b); err != nil {
			t.Fatalf("%d: %v", i, err)
		}
	}
	list := func(f Filter, p store.ListParams) Page {
		pg, err := s.ListPage(f, p)
		if err != nil {
			t.Fatal(err)
		}
		return pg
	}
	if pg := list(Filter{}, store.ListParams{}); names(pg.Blocks) != "aurora,bokeh,comp-a,comp-b" || pg.Total != 4 || pg.States["ok"] != 2 || pg.States["failed"] != 1 || pg.States["unchecked"] != 1 {
		t.Fatalf("%s %d %v", names(pg.Blocks), pg.Total, pg.States)
	}
	for _, c := range []struct {
		name string
		f    Filter
		want string
	}{
		{"text in the description", Filter{Q: "LIGHTS"}, "aurora,bokeh"},
		{"text in a tag", Filter{Q: "night"}, "aurora,comp-a"},
		{"a tag, whole", Filter{Tag: "sky"}, "aurora,bokeh"},
		{"a tag is not a part of one", Filter{Tag: "sk"}, ""},
		{"state", Filter{State: "ok"}, "aurora,comp-b"},
		{"components", Filter{Kind: "component"}, "comp-a,comp-b"},
		{"blocks", Filter{Kind: "block"}, "aurora,bokeh"},
		{"family, whatever its case", Filter{Family: "TEXT"}, "comp-a"},
		{"two filters", Filter{Kind: "component", State: "ok"}, "comp-b"},
	} {
		if pg := list(c.f, store.ListParams{}); names(pg.Blocks) != c.want {
			t.Errorf("%s: %q, want %q", c.name, names(pg.Blocks), c.want)
		}
	}
	if pg := list(Filter{}, store.ListParams{Sort: "title"}); names(pg.Blocks) != "comp-a,comp-b,bokeh,aurora" {
		// A component, Another, Bokeh, Zebra title
		t.Fatalf("by title: %s", names(pg.Blocks))
	}
	pg := list(Filter{Q: "lights"}, store.ListParams{Limit: 1, Offset: 1})
	if names(pg.Blocks) != "bokeh" || pg.Total != 2 || pg.States["ok"] != 1 || pg.States["failed"] != 1 {
		t.Fatalf("a page: the totals are of everything that matches: %s %d %v", names(pg.Blocks), pg.Total, pg.States)
	}
}

func TestStoreDB_DeletingABlockRemovesItsRecordAndItsFolderOnly(t *testing.T) {
	eachStore(t, func(t *testing.T, newStore func() Store) {
		s := newStore()
		if _, err := s.Import(Source{Dir: fixture(t), URL: "u"}, nil, false); err != nil {
			t.Fatal(err)
		}
		dir, _ := s.Path("aurora")
		other := filepath.Join(s.Dir, "blocks", "keep")
		_ = os.MkdirAll(other, 0o755)
		if err := s.Save(Block{Name: "keep", Title: "Keep", HTML: "k.html"}); err != nil {
			t.Fatal(err)
		}
		if err := s.Delete("aurora"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Get("aurora"); err != ErrNotFound {
			t.Fatalf("%v", err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("the folder is left: %v", err)
		}
		if _, err := s.Get("keep"); err != nil {
			t.Fatalf("another block was touched: %v", err)
		}
		if err := s.Delete("aurora"); err != ErrNotFound {
			t.Fatalf("twice: %v", err)
		}
		if err := s.Delete("../x"); err == nil {
			t.Fatal("a name that is not a block name was accepted")
		}
	})
}

func TestStoreDB_ABlockWhoseRecordCannotBeKeptLeavesNoFolderBehind(t *testing.T) {
	s, st := dbStore(t, t.TempDir())
	_ = st.Close() // every write of the record fails
	res, err := s.Import(Source{Dir: fixture(t), URL: "u", NoFetch: true}, []string{"aurora"}, false)
	if err != nil || len(res) != 1 || res[0].Error == "" || res[0].Written {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "blocks", "aurora")); !os.IsNotExist(err) {
		t.Fatalf("a block with no record was left in its folder: %v", err)
	}
}

func TestImporter_TheRecordsOfTheBlocksBecomeRowsAndTheirFilesStayWhere(t *testing.T) {
	dir := t.TempDir()
	files := Store{Dir: dir}
	if _, err := files.Import(Source{Dir: fixture(t), URL: "u", NoFetch: true}, nil, false); err != nil {
		t.Fatal(err)
	}
	thumb := filepath.Join(dir, "blocks", "aurora", ThumbFile)
	if err := os.WriteFile(thumb, []byte("jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}
	// a block.json that is not one, and a folder with none
	_ = os.MkdirAll(filepath.Join(dir, "blocks", "broken"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "blocks", "broken", "block.json"), []byte("{no"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "blocks", "empty"), 0o755)
	before := map[string]string{}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			before[p] = string(b)
		}
		return nil
	})

	s, st := dbStore(t, dir)
	recs, err := st.RunImports(context.Background(), "hyperframes/blocks")
	if err != nil || len(recs) != 1 || recs[0].State != store.ImportDone || recs[0].Created != 1 || len(recs[0].Skipped) != 1 {
		t.Fatalf("%+v %v", recs, err)
	}
	b, err := s.Get("aurora")
	want, _ := files.Get("aurora")
	if err != nil || fmt.Sprint(b) != fmt.Sprint(want) || b.Origin.Poster == "" || b.Width != 1920 {
		t.Fatalf("what the database has is what block.json had:\n%+v\n%+v\n%v", b, want, err)
	}
	// every file of the block is where it was, byte for byte; the thumbnail is served from there
	for p, content := range before {
		if got, err := os.ReadFile(p); err != nil || string(got) != content {
			t.Fatalf("%s changed: %v", p, err)
		}
	}
	if got, err := os.ReadFile(filepath.Join(s.Dir, "blocks", "aurora", ThumbFile)); err != nil || string(got) != "jpeg" {
		t.Fatalf("the thumbnail: %q %v", got, err)
	}
	// a second import creates nothing and overwrites nothing: an edit made since stays
	b.Title = "Edited since the import"
	if err := s.Save(b); err != nil {
		t.Fatal(err)
	}
	recs, _ = st.RunImports(context.Background(), "hyperframes/blocks")
	if again, _ := s.Get("aurora"); recs[0].Created != 0 || recs[0].Existing != 1 || again.Title != "Edited since the import" {
		t.Fatalf("%+v %+v", recs[0], again)
	}
	// only the block.json files are listed as left behind
	legacy, _ := Importer{Dir: dir}.LegacyFiles()
	for _, f := range legacy {
		if filepath.Base(f.Path) != "block.json" {
			t.Fatalf("%+v", f)
		}
	}
	if len(legacy) != 2 {
		t.Fatalf("%d", len(legacy))
	}
}

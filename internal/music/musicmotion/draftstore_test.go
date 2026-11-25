package musicmotion

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"veritmo/internal/platform/store"
)

// draftStores are the two implementations of the draft store: the same assertions run on both.
func draftStores(t *testing.T) map[string]*DraftStore {
	return map[string]*DraftStore{
		"folder":   NewDraftStore(t.TempDir()),
		"database": NewDraftStoreDB(t.TempDir(), memStore(t)),
	}
}

func eachDraftStore(t *testing.T, fn func(t *testing.T, s *DraftStore)) {
	t.Helper()
	for name, s := range draftStores(t) {
		t.Run(name, func(t *testing.T) { fn(t, s) })
	}
}

func TestDraftStore_CreateGetSaveAndTheFilesOfADraft(t *testing.T) {
	eachDraftStore(t, func(t *testing.T, s *DraftStore) {
		d, err := s.Create("a coral dot", "manual", nil)
		if err != nil || d.State != DraftWriting || !ValidDraftID(d.ID) {
			t.Fatalf("%+v %v", d, err)
		}
		d.State, d.Attempts, d.Name = DraftReady, 2, "probe"
		d.Problems = []string{"one"}
		d.Scene = &SceneRef{Piece: "piece-a", ScriptPiece: "p3"}
		d.Manifest, d.Fragment = "ignored", "ignored" // the text of the files is kept by SaveFiles, not by Save
		if err := s.Save(d); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveFiles(d.ID, `{"name":"probe"}`, "<p>x</p>"); err != nil {
			t.Fatal(err)
		}
		got, err := s.Get(d.ID)
		if err != nil || got.State != DraftReady || got.Attempts != 2 || got.Name != "probe" || got.Request != "a coral dot" || got.Manifest != `{"name":"probe"}` ||
			got.Fragment != "<p>x</p>" || len(got.Problems) != 1 || got.Scene == nil || got.Scene.ScriptPiece != "p3" {
			t.Fatalf("%+v %v", got, err)
		}
		// a save does not lose the files
		got.Attempts = 3
		if err := s.Save(got); err != nil {
			t.Fatal(err)
		}
		if again, _ := s.Get(d.ID); again.Manifest != `{"name":"probe"}` || again.Attempts != 3 {
			t.Fatalf("%+v", again)
		}
		if _, err := s.Get("nope"); !errors.Is(err, ErrDraftNotFound) {
			t.Fatalf("%v", err)
		}
		if _, err := s.Get("draft-20260101-000000-aaaaaa"); !errors.Is(err, ErrDraftNotFound) {
			t.Fatalf("%v", err)
		}
	})
}

func TestDraftStore_ADeletedDraftIsNotBroughtBackAndItsMediaGoes(t *testing.T) {
	eachDraftStore(t, func(t *testing.T, s *DraftStore) {
		d, _ := s.Create("a", "manual", []byte("\x89PNG\r\n\x1a\nxx"))
		if _, err := s.SavePreview(d.ID, [][]byte{[]byte("one"), []byte("two")}); err != nil {
			t.Fatal(err)
		}
		if b, err := os.ReadFile(s.PreviewPath(d.ID, "01.png")); err != nil || string(b) != "two" {
			t.Fatalf("a preview frame is a file: %q %v", b, err)
		}
		if err := s.Delete(d.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(s.Dir, d.ID)); !os.IsNotExist(err) {
			t.Fatal("its picture and previews are gone")
		}
		if err := s.Save(d); !errors.Is(err, ErrDraftNotFound) {
			t.Fatalf("a deleted draft is not brought back: %v", err)
		}
		if err := s.SaveFiles(d.ID, "m", "f"); !errors.Is(err, ErrDraftNotFound) {
			t.Fatalf("%v", err)
		}
		if _, err := s.SavePreview(d.ID, nil); !errors.Is(err, ErrDraftNotFound) {
			t.Fatalf("%v", err)
		}
		if err := s.Delete(d.ID); !errors.Is(err, ErrDraftNotFound) {
			t.Fatalf("%v", err)
		}
	})
}

func TestDraftStore_ListIsNewestFirstAndIncludesTheFiles(t *testing.T) {
	eachDraftStore(t, func(t *testing.T, s *DraftStore) {
		for _, r := range []string{"first", "second", "third"} {
			d, err := s.Create(r, "manual", nil)
			if err != nil {
				t.Fatal(err)
			}
			_ = s.SaveFiles(d.ID, `{"name":"`+r+`"}`, "<p>"+r+"</p>")
		}
		list := s.List()
		if len(list) != 3 {
			t.Fatalf("%d drafts", len(list))
		}
		// newest first, a later id first on the same second
		for i := 1; i < len(list); i++ {
			if list[i].CreatedAt.After(list[i-1].CreatedAt) || (list[i].CreatedAt.Equal(list[i-1].CreatedAt) && list[i].ID > list[i-1].ID) {
				t.Fatalf("%v is out of order", list[i].ID)
			}
		}
		if !strings.Contains(list[0].Manifest, `"name"`) {
			t.Fatalf("a list holds the text of the files: %+v", list[0])
		}
	})
}

func TestDraftStore_MarkInterruptedAndTheBaseOfARewrite(t *testing.T) {
	eachDraftStore(t, func(t *testing.T, s *DraftStore) {
		writing, _ := s.Create("a", "manual", nil)
		ready, _ := s.Create("b", "manual", nil)
		ready.State = DraftReady
		_ = s.Save(ready)
		if n := s.MarkInterrupted(); n != 1 {
			t.Fatalf("%d", n)
		}
		if got, _ := s.Get(writing.ID); got.State != DraftFailed || !strings.Contains(got.Error, "interrupted") {
			t.Fatalf("%+v", got)
		}
		if got, _ := s.Get(ready.ID); got.State != DraftReady {
			t.Fatalf("%+v", got)
		}
		if _, _, err := s.LoadBase(writing.ID); err == nil {
			t.Fatal("a draft with no base has none to load")
		}
		if err := s.SaveBase(writing.ID, `{"name":"old"}`, "<p>old</p>"); err != nil {
			t.Fatal(err)
		}
		m, f, err := s.LoadBase(writing.ID)
		if err != nil || m != `{"name":"old"}` || f != "<p>old</p>" {
			t.Fatalf("%q %q %v", m, f, err)
		}
		if err := s.SaveBase("draft-20260101-000000-aaaaaa", "m", "f"); !errors.Is(err, ErrDraftNotFound) {
			t.Fatalf("%v", err)
		}
	})
}

func TestDraftStore_ATransactionOfTheDatabaseStoreIsUndoneWithIt(t *testing.T) {
	st := memStore(t)
	s := NewDraftStoreDB(t.TempDir(), st)
	d, _ := s.Create("a", "manual", nil)
	boom := errors.New("boom")
	err := st.Tx(func(tx *store.Store) error {
		if err := s.On(tx).DeleteRecord(d.ID); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if _, err := s.Get(d.ID); err != nil {
		t.Fatalf("the draft is still there when the transaction was undone: %v", err)
	}
	if err := st.Tx(func(tx *store.Store) error { return s.On(tx).DeleteRecord(d.ID) }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(d.ID); !errors.Is(err, ErrDraftNotFound) {
		t.Fatalf("%v", err)
	}
}

func dbDraftEngine(t *testing.T) (*DraftEngine, *store.Store) {
	t.Helper()
	st := memStore(t)
	return &DraftEngine{Store: NewDraftStoreDB(t.TempDir(), st), Cat: NewCatalog(NewDBParts(st)), Browser: browserOrSkip(t)}, st
}

func readyDraftWithPart(t *testing.T, e *DraftEngine) Draft {
	t.Helper()
	d, err := e.Store.Create("a dot", "manual", []byte("\x89PNG\r\n\x1a\nxx"))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Store.SaveFiles(d.ID, testManifest, movingFragment); err != nil {
		t.Fatal(err)
	}
	d.State, d.Name = DraftReady, "probe"
	if err := e.Store.Save(d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestApprovingADraftIsOneChangeThePartAndTheEndOfTheDraft(t *testing.T) {
	e, st := dbDraftEngine(t)
	d := readyDraftWithPart(t, e)
	part, probs, err := e.Approve(context.Background(), d.ID)
	if err != nil || len(probs) != 0 || part.Name != "probe" {
		t.Fatalf("%v %v %+v", err, probs, part)
	}
	if _, err := e.Store.Get(d.ID); !errors.Is(err, ErrDraftNotFound) {
		t.Fatalf("the draft is gone with the approval: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.Store.Dir, d.ID)); !os.IsNotExist(err) {
		t.Fatal("and its picture")
	}
	if _, ok := e.Cat.Get("probe"); !ok {
		t.Fatal("the part is in the catalog")
	}

	// a second draft whose part cannot be kept: the draft stays ready and nothing else changes
	d2 := readyDraftWithPart(t, e)
	e.Cat.DeleteUserPart("probe")
	if err := st.DB.Callback().Create().Before("gorm:create").Register("test:fail-the-part", func(tx *gorm.DB) {
		if tx.Statement.Table == "mm_parts" {
			_ = tx.AddError(errors.New("the disk is full"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, _, err = e.Approve(context.Background(), d2.ID)
	_ = st.DB.Callback().Create().Remove("test:fail-the-part")
	if err == nil || !strings.Contains(err.Error(), "disk is full") {
		t.Fatalf("%v", err)
	}
	if got, gerr := e.Store.Get(d2.ID); gerr != nil || got.State != DraftReady {
		t.Fatalf("the draft stays ready: %+v %v", got, gerr)
	}
	if _, ok := e.Cat.Get("probe"); ok {
		t.Fatal("no part was kept")
	}
	if _, err := os.Stat(filepath.Join(e.Store.Dir, d2.ID)); err != nil {
		t.Fatal("its picture stays with it")
	}
	// the disk is back: the same draft is approved
	if _, _, err := e.Approve(context.Background(), d2.ID); err != nil {
		t.Fatal(err)
	}
}

func TestTheDraftFoldersBecomeRowsAndTheirPreviewIsStillServedFromTheFolder(t *testing.T) {
	dir := t.TempDir()
	mk := func(id, state, name string, withFiles bool) {
		d := filepath.Join(dir, id)
		_ = os.MkdirAll(filepath.Join(d, "preview"), 0o755)
		rec := `{"id":"` + id + `","state":"` + state + `","request":"a dot","attempts":2,"name":"` + name + `","problems":[],"created_at":"2026-11-10T10:00:00Z","preview":["00.png"],"scene":{"piece":"piece-a","script_piece":"p2"}}`
		_ = os.WriteFile(filepath.Join(d, "draft.json"), []byte(rec), 0o644)
		_ = os.WriteFile(filepath.Join(d, "preview", "00.png"), []byte("png"), 0o644)
		_ = os.WriteFile(filepath.Join(d, "frame.jpg"), []byte("jpg"), 0o644)
		if withFiles {
			_ = os.WriteFile(filepath.Join(d, "manifest.json"), []byte(`{"name":"probe"}`), 0o644)
			_ = os.WriteFile(filepath.Join(d, "part.html"), []byte("<p>x</p>"), 0o644)
			_ = os.WriteFile(filepath.Join(d, "base-manifest.json"), []byte(`{"name":"old"}`), 0o644)
			_ = os.WriteFile(filepath.Join(d, "base-part.html"), []byte("<p>old</p>"), 0o644)
		}
	}
	mk("draft-20261110-100000-aaaaaa", "ready", "probe", true)
	mk("draft-20261110-100100-bbbbbb", "failed", "", false)
	_ = os.MkdirAll(filepath.Join(dir, "not-a-draft"), 0o755)
	_ = os.MkdirAll(filepath.Join(dir, "draft-20261110-100200-cccccc"), 0o755) // no draft.json
	reg := store.NewRegistry()
	RegisterStore(reg, Folders{Drafts: dir})
	st, err := store.OpenMemory(reg)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	recs, err := st.RunImports(context.Background(), "music-motion/part-drafts")
	if err != nil || len(recs) != 1 || recs[0].Created != 2 || len(recs[0].Skipped) != 2 {
		t.Fatalf("%+v %v", recs, err)
	}
	s := NewDraftStoreDB(dir, st)
	d, err := s.Get("draft-20261110-100000-aaaaaa")
	if err != nil || d.State != DraftReady || d.Manifest != `{"name":"probe"}` || d.Fragment != "<p>x</p>" || d.Scene == nil || d.Scene.ScriptPiece != "p2" || len(d.Preview) != 1 {
		t.Fatalf("%+v %v", d, err)
	}
	if m, f, err := s.LoadBase(d.ID); err != nil || m != `{"name":"old"}` || f != "<p>old</p>" {
		t.Fatalf("%q %q %v", m, f, err)
	}
	if b, err := os.ReadFile(s.PreviewPath(d.ID, "00.png")); err != nil || string(b) != "png" {
		t.Fatalf("the preview frame is served from its folder: %q %v", b, err)
	}
	files, _ := draftsImporter{dir: dir}.LegacyFiles()
	for _, f := range files {
		if strings.HasSuffix(f.Path, "frame.jpg") || strings.HasSuffix(f.Path, ".png") {
			t.Fatalf("media is never listed: %s", f.Path)
		}
	}
	if recs, _ := st.RunImports(context.Background(), "music-motion/part-drafts"); recs[0].Created != 0 || recs[0].Existing != 2 {
		t.Fatalf("%+v", recs)
	}
	if missing, _ := st.CheckSchema(); len(missing) > 0 {
		t.Fatal(missing)
	}
}

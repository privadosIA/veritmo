package prompts

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pstore "veritmo/internal/platform/store"
)

// memory is a store of presets on a database in memory, with the importer reading dir.
func memory(t *testing.T, dir string) (*DB, *pstore.Store) {
	t.Helper()
	reg := pstore.NewRegistry()
	RegisterStore(reg, dir)
	st, err := pstore.OpenMemory(reg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	return NewDB(st), st
}

func preset(id, kind, title, prompt string, tags ...string) *Preset {
	if tags == nil {
		tags = []string{}
	}
	return &Preset{ID: id, Kind: kind, Title: title, Prompt: prompt, Tags: tags, CreatedAt: "2026-11-19T10:00:00Z"}
}

func titles(ps []*Preset) string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Title)
	}
	return strings.Join(out, ",")
}

func TestDB_SaveUpsertsRemoveAndGet(t *testing.T) {
	db, _ := memory(t, t.TempDir())
	if err := db.Save([]*Preset{preset("p-1", "image", "One", "first"), preset("p-2", "video", "Two", "second", "slow")}); err != nil {
		t.Fatal(err)
	}
	// saving an id that is there replaces it, whole, in one change with the others
	changed := preset("p-1", "image", "One renamed", "first changed", "neon")
	changed.NSFW, changed.Own, changed.Source, changed.SourceID = true, true, "user", "x"
	if err := db.Save([]*Preset{changed, preset("p-3", "music", "Three", "third")}); err != nil {
		t.Fatal(err)
	}
	got, err := db.Get("p-1")
	if err != nil || got.Title != "One renamed" || got.Prompt != "first changed" || !got.NSFW || !got.Own || got.Tags[0] != "neon" {
		t.Fatalf("%+v %v", got, err)
	}
	if all, total, _ := db.List(Filter{}, pstore.ListParams{}); total != 3 || len(all) != 3 {
		t.Fatalf("%d", total)
	}
	if _, err := db.Get("p-nope"); err != ErrNotFound {
		t.Fatalf("%v", err)
	}
	if err := db.Remove("p-2"); err != nil {
		t.Fatal(err)
	}
	if err := db.Remove("p-2"); err != ErrNotFound {
		t.Fatalf("%v", err)
	}
	if err := db.Save(nil); err != nil {
		t.Fatal(err)
	}
	// a preset with no tags reads back with an empty list, never nil
	if p, _ := db.Get("p-3"); p.Tags == nil {
		t.Fatal("nil tags")
	}
}

func TestDB_ListFiltersSortsAndPages(t *testing.T) {
	db, _ := memory(t, t.TempDir())
	var ps []*Preset
	for i := 0; i < 30; i++ {
		kind := "image"
		if i%3 == 0 {
			kind = "video"
		}
		p := preset(fmt.Sprintf("p-%03d", i), kind, fmt.Sprintf("T%02d", i), "prompt "+fmt.Sprint(i), "all", fmt.Sprint("n", i%2))
		p.CreatedAt = fmt.Sprintf("2026-11-19T10:00:%02dZ", i)
		p.NSFW = i%5 == 0
		ps = append(ps, p)
	}
	_ = db.Save(ps)

	all, total, err := db.List(Filter{}, pstore.ListParams{})
	if err != nil || total != 30 || all[0].Title != "T29" || all[29].Title != "T00" {
		t.Fatalf("newest first: %d %s %v", total, titles(all[:2]), err)
	}
	if v, total, _ := db.List(Filter{Kind: "video"}, pstore.ListParams{}); total != 10 || v[0].Kind != "video" {
		t.Fatalf("%d", total)
	}
	if _, total, _ := db.List(Filter{HideNSFW: true}, pstore.ListParams{}); total != 24 {
		t.Fatalf("hidden nsfw: %d", total)
	}
	if _, total, _ := db.List(Filter{Tags: []string{"all", "n1"}}, pstore.ListParams{}); total != 15 {
		t.Fatalf("tags: %d", total)
	}
	if _, total, _ := db.List(Filter{Tags: []string{"n"}}, pstore.ListParams{}); total != 0 {
		t.Fatalf("a tag is matched whole: %d", total)
	}
	page, total, _ := db.List(Filter{}, pstore.ListParams{Limit: 5, Offset: 10, Sort: "title"})
	if total != 30 || titles(page) != "T10,T11,T12,T13,T14" {
		t.Fatalf("%s", titles(page))
	}
	desc, _, _ := db.List(Filter{}, pstore.ListParams{Limit: 2, Sort: "created_at", Desc: true})
	if titles(desc) != "T29,T28" {
		t.Fatalf("%s", titles(desc))
	}
	// the search: title, prompt and tags, whatever the case
	if _, total, _ := db.List(Filter{}, pstore.ListParams{Q: "T1"}); total != 10 {
		t.Fatalf("q: %d", total)
	}
	if hits, total, _ := db.List(Filter{Kind: "video"}, pstore.ListParams{Q: "PROMPT 6"}); total != 1 || hits[0].Title != "T06" {
		t.Fatalf("q in the prompt: %d", total)
	}
}

func TestDB_KeysAndFreshIDs(t *testing.T) {
	db, _ := memory(t, t.TempDir())
	a := preset("p-a", "image", "A", "a")
	a.Source, a.SourceID = "civitai", "42"
	_ = db.Save([]*Preset{a, preset("p-b", "image", "B", "b")})
	keys, err := db.Keys()
	if err != nil || len(keys) != 1 || keys[Key(a)] != "p-a" {
		t.Fatalf("%v %v", keys, err)
	}
	ids, err := db.FreshIDs(2000)
	if err != nil || len(ids) != 2000 {
		t.Fatalf("%d %v", len(ids), err)
	}
	seen := map[string]bool{"p-a": true, "p-b": true}
	for _, id := range ids {
		if seen[id] || !strings.HasPrefix(id, "p-") {
			t.Fatalf("%q repeats or is taken", id)
		}
		seen[id] = true
	}
}

// writePresetFile writes a preset as the files of before the database kept it.
func writePresetFile(t *testing.T, dir string, p *Preset) {
	t.Helper()
	if err := Save(dir, p); err != nil {
		t.Fatal(err)
	}
}

func TestImporter_ThreeKindsOneInvalidFileAndACacheFolderThatIsNotRead(t *testing.T) {
	dir := t.TempDir()
	writePresetFile(t, dir, preset("p-1", "image", "Neon", "neon street", "night"))
	writePresetFile(t, dir, preset("p-2", "video", "Dolly", "slow dolly"))
	own := preset("p-3", "music", "Mine", "a ballad")
	own.Own, own.Source = true, "user"
	writePresetFile(t, dir, own)
	// a file that is not a preset, and one that is in the wrong folder
	if err := os.WriteFile(filepath.Join(dir, "image", "broken.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	moved := preset("p-9", "video", "Moved", "elsewhere")
	b, _ := json.Marshal(moved)
	_ = os.WriteFile(filepath.Join(dir, "image", "p-9.json"), b, 0o644)
	// the downloads: never read as presets, never touched
	cacheFile := filepath.Join(dir, CacheDir, "owner", "repo", "x.json")
	if err := os.MkdirAll(filepath.Dir(cacheFile), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(cacheFile, []byte(`{"id":"p-c","kind":"image","prompt":"cache"}`), 0o644)

	db, st := memory(t, dir)
	recs, err := st.RunImports(context.Background(), "prompts")
	if err != nil || len(recs) != 1 || recs[0].State != pstore.ImportDone {
		t.Fatalf("%+v %v", recs, err)
	}
	if recs[0].Created != 3 || len(recs[0].Skipped) != 2 {
		t.Fatalf("%+v", recs[0])
	}
	for _, s := range recs[0].Skipped {
		if !strings.HasSuffix(s.Path, "broken.json") && !strings.HasSuffix(s.Path, "p-9.json") {
			t.Fatalf("%+v", s)
		}
		if s.Reason == "" {
			t.Fatal("a skipped file says why")
		}
	}
	if all, total, _ := db.List(Filter{}, pstore.ListParams{}); total != 3 || len(all) != 3 {
		t.Fatalf("%d", total)
	}
	if got, err := db.Get("p-3"); err != nil || !got.Own || got.Source != "user" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := db.Get("p-c"); err != ErrNotFound {
		t.Fatalf("the cache was read as presets: %v", err)
	}
	if _, err := os.Stat(cacheFile); err != nil {
		t.Fatal("the cache was touched")
	}
	// a second import creates nothing and overwrites nothing
	changed := preset("p-1", "image", "Renamed after import", "neon street")
	_ = db.Save([]*Preset{changed})
	recs, _ = st.RunImports(context.Background(), "prompts")
	if recs[0].Created != 0 || recs[0].Existing != 3 {
		t.Fatalf("%+v", recs[0])
	}
	if got, _ := db.Get("p-1"); got.Title != "Renamed after import" {
		t.Fatalf("%+v", got)
	}
	// every file stays; the cache is not listed as state
	files, _ := Importer{Dir: dir}.LegacyFiles()
	for _, f := range files {
		if strings.Contains(f.Path, CacheDir) {
			t.Fatalf("%+v", f)
		}
	}
	if len(files) != 5 { // 3 presets, the broken one and the moved one
		t.Fatalf("%d files", len(files))
	}
}

func TestStoreDir(t *testing.T) {
	t.Setenv("MEDIA_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got := StoreDir("", "/data/projects"); got != "/data/prompts" {
		t.Error(got)
	}
	if got := StoreDir("/mnt/media", "/data/projects"); got != "/mnt/media/prompts" {
		t.Error(got)
	}
	t.Setenv("MEDIA_DIR", "/from/env")
	if got := StoreDir("", "projects"); got != "/from/env/prompts" {
		t.Error(got)
	}
	t.Setenv("MEDIA_DIR", "off")
	if got := StoreDir("", "/data/projects"); got != "/data/prompts" {
		t.Error("off ignores the media directory:", got)
	}
	t.Setenv("MEDIA_DIR", "")
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	_ = os.MkdirAll(filepath.Join(cfg, "veritmo"), 0o755)
	_ = os.WriteFile(filepath.Join(cfg, "veritmo", "media-dir"), []byte("/saved\nrest"), 0o644)
	if got := StoreDir("", "projects"); got != "/saved/prompts" {
		t.Error(got)
	}
}

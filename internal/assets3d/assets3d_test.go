package assets3d

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"veritmo/internal/video/blender"
)

func TestPacksListShipsAndIsChecked(t *testing.T) {
	defs := DefaultPacks()
	if len(defs) == 0 {
		t.Fatal("the list that ships is empty")
	}
	for _, d := range defs {
		if d.Licence != "CC0" || d.SHA256 == "" || !strings.HasPrefix(d.Page, "https://") {
			t.Errorf("%s: %+v", d.ID, d)
		}
	}
	good := `{"id":"a","name":"A","author":"x","licence":"CC0","page":"https://kenney.nl/assets/a","sha256":"` + strings.Repeat("a", 64) + `","fetch":"kenney","licence_words":["x"]}`
	for name, tc := range map[string]struct {
		mutate func(string) string
		want   string
	}{
		"not cc0":   {func(s string) string { return strings.Replace(s, `"CC0"`, `"CC-BY"`, 1) }, "only CC0"},
		"no hash":   {func(s string) string { return strings.Replace(s, strings.Repeat("a", 64), "abc", 1) }, "pinned"},
		"unknown":   {func(s string) string { return strings.Replace(s, `"kenney"`, `"ftp"`, 1) }, "fetch must be"},
		"http page": {func(s string) string { return strings.Replace(s, "https://", "http://", 1) }, "https"},
		"no words":  {func(s string) string { return strings.Replace(s, `["x"]`, `[]`, 1) }, "words the licence"},
		"bad id":    {func(s string) string { return strings.Replace(s, `"id":"a"`, `"id":"A b"`, 1) }, "id must be"},
		"url off host": {func(s string) string {
			return strings.Replace(s, `"fetch":"kenney"`, `"fetch":"url","url":"https://evil.example/x.zip"`, 1)
		}, "host of the page"},
	} {
		if _, err := LoadPacks([]byte("["+tc.mutate(good)+"]"), true); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := LoadPacks([]byte("["+good+","+good+"]"), true); err == nil || !strings.Contains(err.Error(), "repeated") {
		t.Errorf("a repeated id: %v", err)
	}
}

func TestInstallFollowsTheSignedLinkAndIndexesThePack(t *testing.T) {
	l, f := installed(t)
	if f.linkHosts.Load() != 1 {
		t.Errorf("the link is asked for once, with the cookie and the referer of the page: %d", f.linkHosts.Load())
	}
	v, _ := l.Pack("test-pack")
	if v.Models != 4 || v.Source != "catalog" || len(v.Skipped) != 1 || !strings.Contains(v.Skipped[0].Reason, "not a usable model") {
		t.Errorf("the pack: %+v", v)
	}
	files := l.FilesDir("test-pack")
	for _, name := range []string{"Models/GLB format/char-a.glb", "Models/GLB format/Textures/colormap.png", "Previews/char-a.png", "License.txt"} {
		if _, err := os.Stat(filepath.Join(files, filepath.FromSlash(name))); err != nil {
			t.Errorf("%s was kept: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(files, "Models", "FBX format", "char-a.fbx")); err == nil {
		t.Error("the other formats are not kept")
	}
	if _, err := os.Stat(filepath.Join(l.packDir("test-pack"), "pack.zip")); err == nil {
		t.Error("the zip is not kept")
	}
	m, err := l.Model("test-pack", "char-a")
	if err != nil || strings.Join(m.Clips, ",") != "idle,sprint,walk" || m.Triangles != 100 || strings.Join(m.Needs, ",") != "colormap.png" || m.Licence != "CC0" || m.Attribution {
		t.Errorf("the model: %+v %v", m, err)
	}
	if strings.Join(m.Tags, ",") != "characters,char,a" {
		t.Errorf("tags from the pack and from the name: %v", m.Tags)
	}
	if err := l.Install("test-pack"); !errors.Is(err, ErrConflict) {
		t.Errorf("installing an installed pack: %v", err)
	}
	if err := l.Install("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a pack that is not in the list: %v", err)
	}
	// a library opened again on the same folder sees it
	if v, _ := New(l.Dir(), l.defs).Pack("test-pack"); v.State != StateInstalled {
		t.Errorf("after a restart: %+v", v)
	}
}

func TestInstallFailuresLeaveNothingAndSayWhy(t *testing.T) {
	z := goodPack(t)
	cases := map[string]struct {
		prepare func(f *fakeHost, d *PackDef)
		want    string
	}{
		"a pack that changed":    {func(f *fakeHost, d *PackDef) { d.SHA256 = hashOf([]byte("another version")) }, "the pack changed"},
		"the page has no link":   {func(f *fakeHost, d *PackDef) { f.pageHasID = false }, "no longer has a download link"},
		"another licence text":   {func(f *fakeHost, d *PackDef) { d.LicenceWords = []string{"Attribution required"} }, "does not say"},
		"a link to another host": {func(f *fakeHost, d *PackDef) { f.linkURL = func(string) string { return "https://evil.example/x.zip" } }, "not on"},
	}
	for name, tc := range cases {
		f := newFakeHost(t, z)
		d := f.def(z)
		tc.prepare(f, &d)
		l := New(t.TempDir(), []PackDef{d})
		if err := l.Install("test-pack"); err != nil {
			t.Fatal(err)
		}
		l.Wait("test-pack")
		v, _ := l.Pack("test-pack")
		if v.State != StateFailed || !strings.Contains(v.Error, tc.want) {
			t.Errorf("%s: %+v", name, v)
		}
		if entries, _ := os.ReadDir(l.Dir()); len(entries) != 0 {
			t.Errorf("%s left files: %v", name, entries)
		}
		if _, ok := l.record("test-pack"); ok {
			t.Errorf("%s: a pack that failed has no record", name)
		}
	}
}

func TestACancelledInstallGoesBackToAvailableAndLeavesNothing(t *testing.T) {
	z := goodPack(t)
	f := newFakeHost(t, z)
	f.slow = make(chan struct{})
	defer close(f.slow)
	l := New(t.TempDir(), []PackDef{f.def(z)})
	if err := l.Install("test-pack"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for f.linkHosts.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if v, _ := l.Pack("test-pack"); v.State != StateInstalling || v.Progress == nil {
		t.Fatalf("while it downloads: %+v", v)
	}
	if err := l.Cancel("test-pack"); err != nil {
		t.Fatal(err)
	}
	l.Wait("test-pack")
	if v, _ := l.Pack("test-pack"); v.State != StateAvailable || v.Error != "" {
		t.Errorf("after cancel: %+v", v)
	}
	if entries, _ := os.ReadDir(l.Dir()); len(entries) != 0 {
		t.Errorf("cancel leaves nothing: %v", entries)
	}
	if err := l.Cancel("test-pack"); !errors.Is(err, ErrConflict) {
		t.Errorf("cancel of what is not installing: %v", err)
	}
}

func TestAStoppedInstallIsCleanedAtStart(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "half.partial", "files"), 0o755)
	os.MkdirAll(filepath.Join(dir, ".tmp-x"), 0o755)
	New(dir, nil)
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("%v", entries)
	}
}

func TestUnzipRefusesWhatAnArchiveMustNotDo(t *testing.T) {
	cases := map[string][]entry{
		"traversal":         {{name: "../x.glb", data: []byte("x")}},
		"deep traversal":    {{name: "a/../../x.glb", data: []byte("x")}},
		"absolute":          {{name: "/etc/x.glb", data: []byte("x")}},
		"windows traversal": {{name: `..\x.glb`, data: []byte("x")}},
		"a link":            {{name: "l.glb", data: []byte("/etc/passwd"), mode: uint32(fs.ModeSymlink | 0o777)}},
		"a repeated name":   {{name: "a.glb", data: []byte("1")}, {name: "a.glb", data: []byte("2")}},
	}
	for name, es := range cases {
		zp := filepath.Join(t.TempDir(), "p.zip")
		os.WriteFile(zp, makeZip(t, es...), 0o644)
		dir := filepath.Join(t.TempDir(), "out")
		if err := unzipSafe(zp, dir); !errors.Is(err, ErrUnsafeZip) {
			t.Errorf("%s: %v", name, err)
		}
		if _, err := os.Stat(dir); err == nil {
			t.Errorf("%s left the folder", name)
		}
	}
	// too many entries
	var many []entry
	for i := 0; i <= MaxEntries; i++ {
		many = append(many, entry{name: fmt.Sprintf("f%d.txt", i), data: []byte("x")})
	}
	zp := filepath.Join(t.TempDir(), "many.zip")
	os.WriteFile(zp, makeZip(t, many...), 0o644)
	if err := unzipSafe(zp, filepath.Join(t.TempDir(), "o")); !errors.Is(err, ErrUnsafeZip) || !strings.Contains(err.Error(), "entries") {
		t.Errorf("too many entries: %v", err)
	}
	// a bomb: counted while copying, whatever the header says
	old := maxUnpacked
	maxUnpacked = 1 << 10
	defer func() { maxUnpacked = old }()
	zp = filepath.Join(t.TempDir(), "bomb.zip")
	os.WriteFile(zp, makeZip(t, entry{name: "big.txt", data: bytes.Repeat([]byte("a"), 4096)}), 0o644)
	dir := filepath.Join(t.TempDir(), "o")
	if err := unzipSafe(zp, dir); !errors.Is(err, ErrUnsafeZip) || !strings.Contains(err.Error(), "unpacked") {
		t.Errorf("a bomb: %v", err)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("a bomb leaves nothing")
	}
	// spaces and other formats are fine: only what the library uses is kept
	maxUnpacked = old
	zp = filepath.Join(t.TempDir(), "ok.zip")
	os.WriteFile(zp, makeZip(t, entry{name: "A Folder/a b.glb", data: []byte("x")}, entry{name: "A Folder/a.fbx", data: []byte("x")}), 0o644)
	dir = filepath.Join(t.TempDir(), "o")
	if err := unzipSafe(zp, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "A Folder", "a b.glb")); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "A Folder", "a.fbx")); err == nil {
		t.Error("an .fbx is not kept")
	}
}

func TestImportAZipTheUserHas(t *testing.T) {
	l := New(t.TempDir(), nil)
	z := goodPack(t)
	v, err := l.Import(context.Background(), ImportMeta{ID: "my-pack", Name: "My pack", Author: "Quaternius", Licence: "CC0 by Quaternius", SourceURL: "https://example.org/p"}, bytes.NewReader(z))
	if err != nil {
		t.Fatal(err)
	}
	if v.State != StateInstalled || v.Source != "user" || v.Licence != "CC0 by Quaternius" || v.Attribution || v.Models != 4 {
		t.Errorf("%+v", v)
	}
	if ms, total := l.Models(Query{Pack: "my-pack"}); total != 4 || ms[0].Licence != "CC0 by Quaternius" || ms[0].Author != "Quaternius" {
		t.Errorf("its models carry its licence: %+v", ms)
	}
	// a licence that is not CC0 asks for attribution
	v2, err := l.Import(context.Background(), ImportMeta{ID: "other", Licence: "CC BY 4.0, name the author"}, bytes.NewReader(z))
	if err != nil || !v2.Attribution || v2.Author != "unknown" {
		t.Errorf("%+v %v", v2, err)
	}
	for name, tc := range map[string]struct {
		meta ImportMeta
		body []byte
		want error
	}{
		"no licence":   {ImportMeta{ID: "x1"}, z, ErrInvalid},
		"a bad id":     {ImportMeta{ID: "../x", Licence: "CC0"}, z, ErrInvalid},
		"an id in use": {ImportMeta{ID: "my-pack", Licence: "CC0"}, z, ErrConflict},
		"no model":     {ImportMeta{ID: "x2", Licence: "CC0"}, makeZip(t, entry{name: "a.png", data: []byte("x")}), ErrNoModels},
		"not a zip":    {ImportMeta{ID: "x3", Licence: "CC0"}, []byte("plain text"), ErrInvalid},
		"empty":        {ImportMeta{ID: "x4", Licence: "CC0"}, nil, ErrInvalid},
		"unsafe":       {ImportMeta{ID: "x5", Licence: "CC0"}, makeZip(t, entry{name: "../x.glb", data: []byte("x")}), ErrInvalid},
	} {
		if _, err := l.Import(context.Background(), tc.meta, bytes.NewReader(tc.body)); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
	if entries, _ := os.ReadDir(l.Dir()); len(entries) != 2 {
		t.Errorf("only the two packs are left: %v", entries)
	}
}

func TestRemoveAndTheListOfAPackComesBack(t *testing.T) {
	l, _ := installed(t)
	if err := l.Remove("test-pack"); err != nil {
		t.Fatal(err)
	}
	if v, _ := l.Pack("test-pack"); v.State != StateAvailable || v.Models != 0 {
		t.Errorf("a pack of the list is available again: %+v", v)
	}
	if _, total := l.Models(Query{}); total != 0 {
		t.Errorf("its models are gone: %d", total)
	}
	if err := l.Remove("test-pack"); !errors.Is(err, ErrNotFound) {
		t.Errorf("remove again: %v", err)
	}
	if _, err := l.Pack("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a pack that is neither: %v", err)
	}
}

func TestSearchTheCatalog(t *testing.T) {
	l, _ := installed(t)
	all, total := l.Models(Query{})
	if total != 4 || len(all) != 4 || all[0].ID != "test-pack/char-a" {
		t.Fatalf("%+v", all)
	}
	for name, tc := range map[string]struct {
		q    Query
		want []string
	}{
		"by name":      {Query{Q: "lamp"}, []string{"prop-lamp"}},
		"by a tag":     {Query{Tag: "characters"}, []string{"char-a", "char-b", "prop-bare", "prop-lamp"}},
		"with clips":   {Query{HasClips: true}, []string{"char-a", "char-b"}},
		"case":         {Query{Q: "LAMP"}, []string{"prop-lamp"}},
		"a page":       {Query{Limit: 1, Offset: 1}, []string{"char-b"}},
		"a pack":       {Query{Pack: "test-pack", Q: "a"}, []string{"char-a", "char-b", "prop-bare", "prop-lamp"}},
		"another pack": {Query{Pack: "nope"}, nil},
		"past the end": {Query{Offset: 10}, nil},
	} {
		got, _ := l.Models(tc.q)
		var names []string
		for _, m := range got {
			names = append(names, m.Name)
		}
		if strings.Join(names, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s: %v, want %v", name, names, tc.want)
		}
	}
	if _, total := l.Models(Query{Limit: 1}); total != 4 {
		t.Errorf("the total is the whole: %d", total)
	}
	if _, err := l.Model("test-pack", "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("%v", err)
	}
}

func TestThumbnails(t *testing.T) {
	l, _ := installed(t)
	ctx := context.Background()
	b, ct, err := l.Thumbnail(ctx, "test-pack", "char-a")
	if err != nil || string(b) != "preview of a" || ct != "image/png" {
		t.Errorf("the pack's own preview: %q %q %v", b, ct, err)
	}
	if b, _, err := l.Thumbnail(ctx, "test-pack", "prop-lamp"); err != nil || string(b) != "isometric lamp" {
		t.Errorf("the isometric view of a kit is a preview too: %q %v", b, err)
	}
	if b, _, err := l.Thumbnail(ctx, "test-pack", "char-b"); err != nil || string(b) != "side of b" {
		t.Errorf("a side view is a preview when there is nothing better: %q %v", b, err)
	}
	if _, _, err := l.Thumbnail(ctx, "test-pack", "prop-nothing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("%v", err)
	}
	// a model with no picture at all is drawn
	drawn := 0
	l.Preview = func(_ context.Context, sources string, desc []byte, width int) ([]byte, error) {
		drawn++
		if width != 256 || !strings.Contains(string(desc), `"file":"prop-bare.glb"`) {
			t.Errorf("what is drawn: %d %s", width, desc)
		}
		if m, err := blender.ReadModel(filepath.Join(sources, "prop-bare.glb")); err != nil || m.Triangles != 10 {
			t.Errorf("the model is in a folder of its own: %v %v", m, err)
		}
		return []byte("drawn"), nil
	}
	for i := 0; i < 2; i++ {
		if b, _, err := l.Thumbnail(ctx, "test-pack", "prop-bare"); err != nil || string(b) != "drawn" {
			t.Fatalf("%q %v", b, err)
		}
	}
	if drawn != 1 {
		t.Errorf("a thumbnail is drawn once and kept: %d", drawn)
	}
	if _, _, err := l.Thumbnail(ctx, "test-pack", "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("%v", err)
	}
}

func TestCopyAModelRenamesItsPictureAndRewritesTheFile(t *testing.T) {
	l, _ := installed(t)
	dst := filepath.Join(t.TempDir(), "sources")
	res, err := l.CopyModel("test-pack/char-a", dst, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Name != "char-a.glb" || len(res.Pictures) != 1 || res.Pictures[0] != "test-pack--colormap.png" || res.Already {
		t.Fatalf("%+v", res)
	}
	m, err := blender.ReadModel(filepath.Join(dst, "char-a.glb"))
	if err != nil || strings.Join(m.Needs, ",") != "test-pack--colormap.png" || strings.Join(m.Clips, ",") != "idle,sprint,walk" {
		t.Fatalf("the copy: %+v %v", m, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "test-pack--colormap.png")); string(b) != "atlas pixels" {
		t.Errorf("the picture: %q", b)
	}
	// the library's own file is untouched
	orig, _ := blender.ReadModel(filepath.Join(l.FilesDir("test-pack"), "Models", "GLB format", "char-a.glb"))
	if strings.Join(orig.Needs, ",") != "colormap.png" {
		t.Errorf("the library's copy must not change: %v", orig.Needs)
	}
	// a second model of the pack shares the picture; the same model again is already there
	res2, err := l.CopyModel("test-pack/char-b", dst, "")
	if err != nil || res2.Already || len(res2.Pictures) != 1 {
		t.Fatalf("%+v %v", res2, err)
	}
	entries, _ := os.ReadDir(dst)
	if len(entries) != 3 {
		t.Errorf("two models and one picture: %v", entries)
	}
	if again, err := l.CopyModel("test-pack/char-a", dst, ""); err != nil || !again.Already {
		t.Errorf("the same model again: %+v %v", again, err)
	}
	// a model with no picture has none to copy
	if res3, err := l.CopyModel("test-pack/prop-lamp", dst, "lamp-one.glb"); err != nil || res3.Name != "lamp-one.glb" || len(res3.Pictures) != 0 {
		t.Errorf("%+v %v", res3, err)
	}
	// another file of the same name, or of the picture's name, is a conflict and nothing is written
	other := filepath.Join(t.TempDir(), "sources")
	os.MkdirAll(other, 0o755)
	os.WriteFile(filepath.Join(other, "char-a.glb"), []byte("mine"), 0o644)
	if _, err := l.CopyModel("test-pack/char-a", other, ""); !errors.Is(err, ErrConflict) {
		t.Errorf("a name taken: %v", err)
	}
	if _, err := l.CopyModel("test-pack/char-a", other, "char-a2.glb"); err != nil {
		t.Errorf("another name: %v", err)
	}
	os.WriteFile(filepath.Join(other, "test-pack--colormap.png"), []byte("different"), 0o644)
	if _, err := l.CopyModel("test-pack/char-b", other, ""); !errors.Is(err, ErrConflict) {
		t.Errorf("a picture of other content: %v", err)
	}
	if _, err := os.Stat(filepath.Join(other, "char-b.glb")); err == nil {
		t.Error("a conflict writes nothing")
	}
	for from, want := range map[string]error{"nope": ErrInvalid, "test-pack/nope": ErrNotFound, "nope/x": ErrNotFound} {
		if _, err := l.CopyModel(from, other, ""); !errors.Is(err, want) {
			t.Errorf("%s: %v", from, err)
		}
	}
	if _, err := l.CopyModel("test-pack/char-a", other, "../x.glb"); !errors.Is(err, ErrInvalid) {
		t.Errorf("a name that is a path: %v", err)
	}
}

func TestTwoPacksWithTheSameAtlasNameKeepTheirOwn(t *testing.T) {
	l, _ := installed(t)
	other := makeZip(t,
		entry{name: "GLB/other-a.glb", data: character("walk")},
		entry{name: "GLB/Textures/colormap.png", data: []byte("a different atlas")},
	)
	if _, err := l.Import(context.Background(), ImportMeta{ID: "pack-two", Licence: "CC0"}, bytes.NewReader(other)); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "sources")
	if _, err := l.CopyModel("test-pack/char-a", dst, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := l.CopyModel("pack-two/other-a", dst, ""); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(filepath.Join(dst, "test-pack--colormap.png"))
	b, _ := os.ReadFile(filepath.Join(dst, "pack-two--colormap.png"))
	if string(a) != "atlas pixels" || string(b) != "a different atlas" {
		t.Errorf("each model has its own pack's atlas: %q %q", a, b)
	}
}

func TestACopiedModelPassesTheChecksOfAScene(t *testing.T) {
	l, _ := installed(t)
	dst := filepath.Join(t.TempDir(), "sources")
	if _, err := l.CopyModel("test-pack/char-a", dst, ""); err != nil {
		t.Fatal(err)
	}
	doc := `{"version":1,"camera":{"keys":[{"t":0,"location":[0,-6,1]}]},"objects":[{"id":"a","type":"model","file":"char-a.glb","size":1.7,"animation":{"clip":"sprint"}}]}`
	env := blender.Env{Sources: dst, Palette: blender.DefaultPalette}
	spec, err := blender.Parse([]byte(doc), 5, env)
	if err != nil {
		t.Fatalf("a scene that places the copy: %v", err)
	}
	staged, err := blender.StageModels(spec, dst, filepath.Join(t.TempDir(), blender.ModelsDir))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(staged["char-a.glb"]), "test-pack--colormap.png")); err != nil {
		t.Errorf("the staged model has its picture where the file names it: %v", err)
	}
}

var _ = fs.ModeSymlink

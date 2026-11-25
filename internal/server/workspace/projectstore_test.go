package workspace

import (
	"errors"
	"strings"
	"testing"

	"veritmo/internal/platform/store"
	"veritmo/internal/video/motion"
)

func memProjects(t *testing.T) (*projectStore, *store.Store) {
	t.Helper()
	reg := store.NewRegistry()
	RegisterStore(reg)
	st, err := store.OpenMemory(reg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	return newProjectStore(st), st
}

const videoYAML = "# my video\nproject: Demo\nclient: lab\noutput: {mode: video, file: out/demo.mp4}\ndefaults:\n  model: m.safetensors\nscenes:\n  - id: a\n    prompt: a street\n    silence_seconds: 2\n  - id: b\n    prompt: a park\n    silence_seconds: 2\n"
const imagesYAML = "project: Pics\nadult: true\noutput: {mode: images}\ndefaults:\n  model: m.safetensors\nscenes:\n  - id: one\n    prompt: a kite\n"
const musicYAML = "project: Songs\noutput: {mode: music}\ntracks:\n  - id: t1\n    title: One\n    prompt: pop\n"

func TestDeriveProject(t *testing.T) {
	v := deriveProject("demo", []byte(videoYAML), "")
	if !v.Valid || v.Name != "Demo" || v.Mode != "video" || v.SceneCount != 2 || v.Client != "lab" || v.Kind != kindProject || v.Adult != nil || v.DefinitionSHA != sha([]byte(videoYAML)) || string(v.Definition) != videoYAML {
		t.Fatalf("%+v", v)
	}
	im := deriveProject("pics", []byte(imagesYAML), "")
	if im.Mode != "images" || im.Adult == nil || !*im.Adult || im.Client != clientEmbedded {
		t.Fatalf("%+v", im)
	}
	mu := deriveProject("songs", []byte(musicYAML), "")
	if mu.Mode != "music" || mu.TrackCount != 1 || mu.SceneCount != 0 {
		t.Fatalf("%+v", mu)
	}
	// an invalid definition: marked with the error, the client still read
	bad := deriveProject("bad", []byte("project: x\nclient: lab\nscenes: nope\n"), "")
	if bad.Valid || bad.Error == "" || bad.Client != "lab" || bad.Name != "" || bad.Mode != "" {
		t.Fatalf("%+v", bad)
	}
	if broken := deriveProject("broken", []byte("project: [unclosed\n"), ""); broken.Valid || broken.Client != clientEmbedded {
		t.Fatalf("%+v", broken)
	}
	// the kind comes from the id, the client of a managed project from its stored request
	for id, kind := range map[string]string{"api-gen-20261110-100000-aaaaaa": "generation", "api-edit-1": "image_edit", "api-vid-1": "video_edit", "api-mm-1": "animation", "plain": "project"} {
		if got := deriveProject(id, []byte(imagesYAML), "").Kind; got != kind {
			t.Errorf("%s: %s, want %s", id, got, kind)
		}
	}
	if m := deriveProject("api-gen-1", []byte(musicYAML), "video-studio"); m.Client != "video-studio" {
		t.Fatalf("%+v", m)
	}
	if m := deriveProject("api-gen-1", []byte(musicYAML), ""); m.Client != ClientUnknown {
		t.Fatalf("%+v", m)
	}
}

func TestProjectStore_CreateGetExistsAndTheVersionsOfSave(t *testing.T) {
	ps, _ := memProjects(t)
	if err := ps.Create("demo", []byte(videoYAML)); err != nil {
		t.Fatal(err)
	}
	if err := ps.Create("demo", []byte(videoYAML)); !errors.Is(err, ErrProjectExists) {
		t.Fatalf("%v", err)
	}
	if !ps.Exists("demo") || ps.Exists("nope") {
		t.Fatal("exists")
	}
	if _, err := ps.Get("nope"); !errors.Is(err, ErrNoProject) {
		t.Fatalf("%v", err)
	}
	// the same text: nothing changes, no version
	if changed, err := ps.Save("demo", []byte(videoYAML), "save"); err != nil || changed {
		t.Fatalf("%v %v", changed, err)
	}
	if vs, _ := ps.Versions("demo"); len(vs) != 0 {
		t.Fatalf("%d versions", len(vs))
	}
	// two saves: the two texts they replace, newest first, and the columns follow the text
	second := strings.Replace(videoYAML, "client: lab", "client: video-studio", 1)
	third := strings.Replace(second, "Demo", "Demo two", 1)
	if changed, err := ps.Save("demo", []byte(second), "save"); err != nil || !changed {
		t.Fatalf("%v %v", changed, err)
	}
	if _, err := ps.Save("demo", []byte(third), "fix"); err != nil {
		t.Fatal(err)
	}
	vs, err := ps.Versions("demo")
	if err != nil || len(vs) != 2 || vs[0].Version != 2 || vs[0].Reason != "fix" || vs[1].Reason != "save" || vs[0].Bytes != len(second) || vs[0].Definition != "" {
		t.Fatalf("%+v %v", vs, err)
	}
	if v2, _ := ps.Version("demo", 2); string(v2.Definition) != second {
		t.Fatalf("%q", v2.Definition)
	}
	if v1, _ := ps.Version("demo", 1); string(v1.Definition) != videoYAML {
		t.Fatal("the first text is the first version")
	}
	row, _ := ps.Get("demo")
	if row.Name != "Demo two" || row.Client != "video-studio" || string(row.Definition) != third || row.UpdatedAt.Before(row.CreatedAt) {
		t.Fatalf("%+v", row)
	}
	if _, err := ps.Version("demo", 9); !errors.Is(err, ErrNoVersion) {
		t.Fatalf("%v", err)
	}
	if _, err := ps.Version("nope", 1); !errors.Is(err, ErrNoProject) {
		t.Fatalf("%v", err)
	}
	if _, err := ps.Save("nope", []byte(videoYAML), "save"); !errors.Is(err, ErrNoProject) {
		t.Fatalf("%v", err)
	}
}

func TestProjectStore_KeepsTheTwentyNewestVersionsAndRestores(t *testing.T) {
	ps, _ := memProjects(t)
	_ = ps.Create("demo", []byte(videoYAML))
	for i := 1; i <= 25; i++ {
		text := strings.Replace(videoYAML, "Demo", "Demo "+strings.Repeat("x", i), 1)
		if _, err := ps.Save("demo", []byte(text), "save"); err != nil {
			t.Fatal(err)
		}
	}
	vs, _ := ps.Versions("demo")
	if len(vs) != KeepVersions || vs[0].Version != 25 || vs[KeepVersions-1].Version != 6 {
		t.Fatalf("%d versions, from %d to %d", len(vs), vs[0].Version, vs[len(vs)-1].Version)
	}
	// restore version 10 (the text of "Demo xxxxxxxxx", nine x): it is the definition and what it replaced becomes a version
	v10, _ := ps.Version("demo", 10)
	before, _ := ps.Get("demo")
	if err := ps.Restore("demo", 10); err != nil {
		t.Fatal(err)
	}
	row, _ := ps.Get("demo")
	if row.Definition != v10.Definition {
		t.Fatalf("%q", row.Definition)
	}
	vs, _ = ps.Versions("demo")
	if vs[0].Reason != "restore" {
		t.Fatalf("%+v", vs[0])
	}
	if top, _ := ps.Version("demo", vs[0].Version); top.Definition != before.Definition {
		t.Fatal("the replaced text is the newest version")
	}
}

func TestProjectStore_DuplicateCopiesTheDefinitionAndThePagesButNotTheVersions(t *testing.T) {
	ps, _ := memProjects(t)
	_ = ps.Create("demo", []byte(videoYAML))
	_, _ = ps.Save("demo", []byte(strings.Replace(videoYAML, "Demo", "Demo 2", 1)), "save")
	if err := ps.PutPage("demo", "a", false, "<p>page</p>", &motion.Meta{Kind: "motion_graphic", Model: "gemma"}); err != nil {
		t.Fatal(err)
	}
	if err := ps.Duplicate("demo", "copy"); err != nil {
		t.Fatal(err)
	}
	src, _ := ps.Get("demo")
	cp, err := ps.Get("copy")
	if err != nil || cp.Definition != src.Definition || cp.Name != "Demo 2" {
		t.Fatalf("%+v %v", cp, err)
	}
	if vs, _ := ps.Versions("copy"); len(vs) != 0 {
		t.Fatal("a copy starts with no versions")
	}
	html, meta, hasMeta, found := ps.Page("copy", "a", false)
	if !found || html != "<p>page</p>" || !hasMeta || meta.Model != "gemma" {
		t.Fatalf("%q %+v %v %v", html, meta, hasMeta, found)
	}
	if err := ps.Duplicate("demo", "copy"); !errors.Is(err, ErrProjectExists) {
		t.Fatalf("%v", err)
	}
	if err := ps.Duplicate("nope", "x"); !errors.Is(err, ErrNoProject) {
		t.Fatalf("%v", err)
	}
}

func TestProjectStore_DeleteRemovesAllTheRowsInOneTransaction(t *testing.T) {
	ps, st := memProjects(t)
	_ = ps.Create("demo", []byte(videoYAML))
	_, _ = ps.Save("demo", []byte(strings.Replace(videoYAML, "Demo", "Demo 2", 1)), "save")
	_ = ps.PutPage("demo", "a", false, "<p>x</p>", nil)
	_ = ps.PutPage("demo", "a", true, "<p>o</p>", nil)
	_ = ps.Create("other", []byte(videoYAML))
	_ = ps.PutPage("other", "a", false, "<p>keep</p>", nil)
	if err := ps.Delete("demo"); err != nil {
		t.Fatal(err)
	}
	for _, m := range []any{&ProjectVersion{}, &ProjectPage{}} {
		var n int64
		st.DB.Model(m).Where("project_id = ?", "demo").Count(&n)
		if n != 0 {
			t.Fatalf("%T rows left: %d", m, n)
		}
	}
	if ps.Exists("demo") || !ps.Exists("other") {
		t.Fatal("only that project goes")
	}
	if _, _, _, found := ps.Page("other", "a", false); !found {
		t.Fatal("the pages of another project stay")
	}
	if err := ps.Delete("demo"); !errors.Is(err, ErrNoProject) {
		t.Fatalf("%v", err)
	}
}

func TestProjectStore_PagesAreKeptReplacedAndDeleted(t *testing.T) {
	ps, _ := memProjects(t)
	_ = ps.Create("demo", []byte(videoYAML))
	if _, _, _, found := ps.Page("demo", "a", false); found {
		t.Fatal("no page yet")
	}
	_ = ps.PutPage("demo", "a", false, "<p>1</p>", &motion.Meta{Kind: "motion_graphic"})
	_ = ps.PutPage("demo", "a", false, "<p>2</p>", nil)
	_ = ps.PutPage("demo", "a", true, "<p>overlay</p>", nil)
	html, _, hasMeta, _ := ps.Page("demo", "a", false)
	if html != "<p>2</p>" || hasMeta {
		t.Fatalf("replaced: %q %v", html, hasMeta)
	}
	if pg, _ := ps.Pages("demo"); len(pg) != 2 {
		t.Fatalf("%d pages", len(pg))
	}
	if ok, _ := ps.DeletePage("demo", "a", true); !ok {
		t.Fatal("deleted")
	}
	if ok, _ := ps.DeletePage("demo", "a", true); ok {
		t.Fatal("already gone")
	}
}

func TestProjectStore_SizeAndTheClientOfAManagedProject(t *testing.T) {
	ps, st := memProjects(t)
	_ = ps.Create("api-gen-20261110-100000-aaaaaa", []byte(musicYAML))
	if row, _ := ps.Get("api-gen-20261110-100000-aaaaaa"); row.SizeBytes != nil || row.Client != ClientUnknown {
		t.Fatalf("%+v", row)
	}
	_ = ps.SetSize("api-gen-20261110-100000-aaaaaa", 1234)
	st.DB.Create(&ManagedRow{ID: "api-gen-20261110-100000-aaaaaa", Kind: "generation", Client: "video-studio"})
	if err := ps.TouchClient("api-gen-20261110-100000-aaaaaa"); err != nil {
		t.Fatal(err)
	}
	row, _ := ps.Get("api-gen-20261110-100000-aaaaaa")
	if row.SizeBytes == nil || *row.SizeBytes != 1234 || row.Client != "video-studio" {
		t.Fatalf("%+v", row)
	}
	if missing, _ := st.CheckSchema(); len(missing) > 0 {
		t.Fatal(missing)
	}
}

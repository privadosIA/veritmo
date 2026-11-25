package assets3d

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"veritmo/internal/video/blender"
)

// plainGLB is a model with no clips and no pictures, with the given number of triangle indices (3 a triangle).
func plainGLB(indices int) []byte {
	return glbBytes(`{"accessors":[{"count":` + strconv.Itoa(indices) + `}],"meshes":[{"primitives":[{"attributes":{"POSITION":0},"indices":0}]}]}`)
}

var skate = GenMeta{Prompt: "a retro roller skate, glossy pink", Engine: "triposr", EngineVersion: "triposr@107cefd", Detail: "normal", Licence: "MIT"}

func TestAddGeneratedCreatesThePackAndRecordsWhatMadeTheModel(t *testing.T) {
	l := New(t.TempDir(), nil)
	v, err := l.AddGenerated(plainGLB(300), skate)
	if err != nil {
		t.Fatal(err)
	}
	if v.ID != "generated/a-retro-roller-skate-glossy" || v.Pack != "generated" || v.Triangles != 100 || v.Licence != "MIT" || v.Attribution {
		t.Errorf("%+v", v)
	}
	if v.Prompt != skate.Prompt || v.Engine != "triposr" || v.EngineVersion != "triposr@107cefd" || v.GeneratedAt == nil {
		t.Errorf("what made it: %+v", v)
	}
	p, err := l.Pack("generated")
	if err != nil || p.State != StateInstalled || p.Source != "generated" || p.Models != 1 {
		t.Errorf("the pack: %+v %v", p, err)
	}
	// the catalog finds it by its name, by its tags and by its prompt
	for _, q := range []string{"skate", "generated", "triposr", "pink"} {
		if got, _ := l.Models(Query{Q: q}); len(got) != 1 {
			t.Errorf("q=%s: %v", q, got)
		}
	}
	// a library opened again sees it, and it can be copied into a project like any model
	l2 := New(l.Dir(), nil)
	dst := filepath.Join(t.TempDir(), "sources")
	res, err := l2.CopyModel(v.ID, dst, "")
	if err != nil || res.Name != "a-retro-roller-skate-glossy.glb" {
		t.Fatalf("%+v %v", res, err)
	}
	if m, err := blender.ReadModel(filepath.Join(dst, res.Name)); err != nil || m.Triangles != 100 {
		t.Errorf("the copy: %+v %v", m, err)
	}
}

func TestAddGeneratedNamesAndRefusals(t *testing.T) {
	l := New(t.TempDir(), nil)
	a, _ := l.AddGenerated(plainGLB(30), skate)
	b, err := l.AddGenerated(plainGLB(30), skate)
	if err != nil || b.Name != a.Name+"-2" {
		t.Errorf("a taken name gets a number: %+v %v", b, err)
	}
	c, _ := l.AddGenerated(plainGLB(30), GenMeta{Name: "trofeo", Prompt: "x", Engine: "sf3d", Licence: "Stability AI Community License", Attribution: true})
	if c.Name != "trofeo" || !c.Attribution || c.Licence != "Stability AI Community License" {
		t.Errorf("a name given, the licence of its engine: %+v", c)
	}
	if v, _ := l.AddGenerated(plainGLB(30), GenMeta{Picture: "lampara.png", Engine: "triposr", Licence: "MIT"}); v.Name != "lampara-png" || v.Prompt != "lampara.png" {
		t.Errorf("from a picture: %+v", v)
	}
	for name, tc := range map[string]struct {
		glb  []byte
		meta GenMeta
	}{
		"not a model": {[]byte("nope"), skate},
		"too many":    {plainGLB(3 * (blender.MaxModelTriangles + 1)), skate},
		"a bad name":  {plainGLB(30), GenMeta{Name: "../x", Engine: "triposr", Licence: "MIT"}},
		"no engine":   {plainGLB(30), GenMeta{Prompt: "x", Licence: "MIT"}},
		"no licence":  {plainGLB(30), GenMeta{Prompt: "x", Engine: "triposr"}},
	} {
		if _, err := l.AddGenerated(tc.glb, tc.meta); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if p, _ := l.Pack("generated"); p.Models != 4 {
		t.Errorf("a refusal adds nothing: %+v", p)
	}
	if entries, _ := os.ReadDir(filepath.Join(l.Dir(), "generated", "files")); len(entries) != 4 {
		t.Errorf("and leaves no file: %v", entries)
	}
}

func TestThePackGeneratedCannotBeImportedOverAndCanBeRemoved(t *testing.T) {
	l := New(t.TempDir(), nil)
	if _, err := l.Import(context.Background(), ImportMeta{ID: "generated", Licence: "CC0"}, bytes.NewReader(goodPack(t))); !errors.Is(err, ErrConflict) {
		t.Errorf("an import over generated: %v", err)
	}
	l.AddGenerated(plainGLB(30), skate)
	if err := l.Remove("generated"); err != nil {
		t.Fatal(err)
	}
	if _, total := l.Models(Query{}); total != 0 {
		t.Errorf("its models go with it: %d", total)
	}
	if _, err := l.Pack("generated"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the pack is gone: %v", err)
	}
	if _, err := l.AddGenerated(plainGLB(30), skate); err != nil {
		t.Errorf("and comes back with the next one: %v", err)
	}
}

func TestSlugOf(t *testing.T) {
	for in, want := range map[string]string{
		"a retro roller skate, glossy pink":     "a-retro-roller-skate-glossy",
		"Un patín de ruedas retro":              "un-patin-de-ruedas-retro",
		"   ":                                   "model",
		"¡¡¡":                                   "model",
		"supercalifragilisticoespialidosoextra": "supercalifragilisticoespialidoso"[:32],
	} {
		if got := SlugOf(in); got != want {
			t.Errorf("SlugOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAddGeneratedRecordsTheProjectAndEntryAndReplacesTheModelOfTheSameEntry(t *testing.T) {
	l := New(t.TempDir(), nil)
	meta := GenMeta{Name: "patin", Prompt: "a roller skate", Engine: "triposr", EngineVersion: "triposr@107cefd", Detail: "normal", Licence: "MIT", Project: "objetos", Entry: "patin"}
	a, err := l.AddGenerated(plainGLB(30), meta)
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "patin" || a.Project != "objetos" || a.Entry != "patin" || a.Source != "generated" || a.Adult {
		t.Errorf("%+v", a)
	}
	// a thumbnail drawn for it is dropped when the model is made again: it would show the old one
	thumb := filepath.Join(l.packDir(GeneratedPackID), "thumbs", "patin.png")
	os.MkdirAll(filepath.Dir(thumb), 0o755)
	os.WriteFile(thumb, []byte("old"), 0o644)
	meta.Prompt, meta.Adult = "a green roller skate", true
	b, err := l.AddGenerated(plainGLB(60), meta)
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != "patin" || b.Triangles != 20 || b.Prompt != "a green roller skate" || !b.Adult {
		t.Errorf("replaced: %+v", b)
	}
	if all, _ := l.Models(Query{Pack: GeneratedPackID}); len(all) != 1 {
		t.Errorf("one model, not two: %v", all)
	}
	if _, err := os.Stat(thumb); err == nil {
		t.Errorf("the old thumbnail is still there")
	}
	// the same entry name in another project is another model
	meta.Project = "otro"
	c, err := l.AddGenerated(plainGLB(30), meta)
	if err != nil || c.Name != "patin-2" || c.Project != "otro" {
		t.Errorf("another project: %+v %v", c, err)
	}
	// a model made without a project is never replaced
	d, _ := l.AddGenerated(plainGLB(30), GenMeta{Name: "patin", Engine: "triposr", Licence: "MIT"})
	if d.Name != "patin-3" {
		t.Errorf("without a project: %+v", d)
	}
}

func TestModelsFilterByOriginAndHideAdult(t *testing.T) {
	l := New(t.TempDir(), nil)
	base := GenMeta{Engine: "triposr", Licence: "MIT"}
	add := func(name, prompt, engine, project string, adult bool) {
		m := base
		m.Name, m.Prompt, m.Engine, m.Project, m.Entry, m.Adult = name, prompt, engine, project, name, adult
		if _, err := l.AddGenerated(plainGLB(30), m); err != nil {
			t.Fatal(err)
		}
	}
	add("patin", "a retro roller skate, glossy pink", "triposr", "objetos", false)
	add("copa", "a golden trophy", "sf3d", "objetos", false)
	add("gato", "a white cat", "triposr", "mascotas", true)
	add("suelto", "a lamp", "triposr", "", false)
	names := func(q Query) string {
		got, _ := l.Models(q)
		var n []string
		for _, m := range got {
			n = append(n, m.Name)
		}
		sortStrings(n)
		return joinStrings(n)
	}
	for _, tc := range []struct {
		name string
		q    Query
		want string
	}{
		{"all", Query{}, "copa gato patin suelto"},
		{"source generated", Query{Source: "generated"}, "copa gato patin suelto"},
		{"source catalog", Query{Source: "catalog"}, ""},
		{"source user", Query{Source: "user"}, ""},
		{"engine", Query{Engine: "sf3d"}, "copa"},
		{"project", Query{Project: "objetos"}, "copa patin"},
		{"project and engine", Query{Project: "objetos", Engine: "triposr"}, "patin"},
		{"a project nobody made", Query{Project: "nada"}, ""},
		{"search in the prompt", Query{Q: "roller skate"}, "patin"},
		{"search in the prompt of another", Query{Q: "trophy"}, "copa"},
		{"adult hidden", Query{HideAdult: true}, "copa patin suelto"},
		{"adult hidden and searched", Query{HideAdult: true, Q: "cat"}, ""},
		{"adult shown and searched", Query{Q: "cat"}, "gato"},
	} {
		if got := names(tc.q); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	if v, _ := l.Model("generated", "gato"); !v.Adult || v.Project != "mascotas" {
		t.Errorf("the view says: %+v", v)
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func joinStrings(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += " "
		}
		out += x
	}
	return out
}

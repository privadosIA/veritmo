package musicmotion

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/platform/store"
)

func memStore(t *testing.T) *store.Store {
	t.Helper()
	reg := store.NewRegistry()
	RegisterStore(reg, Folders{})
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

// partStores are the two implementations of PartStore: the same assertions run on both.
func partStores(t *testing.T) map[string]PartStore {
	return map[string]PartStore{
		"folder":   dirParts{dir: t.TempDir()},
		"database": NewDBParts(memStore(t)),
	}
}

func manifestOf(name string) []byte {
	return []byte(fmt.Sprintf(`{"name":%q,"description":"A part called %s","version":1,"levels":["simple","medium"],"params":[]}`, name, name))
}

func testSceneManifest(name, piece, script string) []byte {
	return []byte(fmt.Sprintf(`{"name":%q,"description":"A scene","version":1,"levels":["simple"],"params":[],"scene":{"piece":%q,"script_piece":%q}}`, name, piece, script))
}

func eachStore(t *testing.T, fn func(t *testing.T, st PartStore)) {
	t.Helper()
	for name, st := range partStores(t) {
		t.Run(name, func(t *testing.T) { fn(t, st) })
	}
}

func TestPartStore_CreateGetAndTheNameThatExists(t *testing.T) {
	eachStore(t, func(t *testing.T, st PartStore) {
		origin := &PartOrigin{Title: "A video", Video: "https://example.test/v"}
		if err := st.Create(manifestOf("clock"), `<div>tick</div>`, origin); err != nil {
			t.Fatal(err)
		}
		p, ok := st.Get("clock")
		if !ok || p.Name != "clock" || p.Source != "user" || p.Fragment != `<div>tick</div>` || p.Origin == nil || p.Origin.Title != "A video" {
			t.Fatalf("%+v %v", p, ok)
		}
		if err := st.Create(manifestOf("clock"), `<div>again</div>`, nil); !errors.Is(err, ErrPartExists) {
			t.Fatalf("a name that exists: %v", err)
		}
		if _, ok := st.Get("nope"); ok {
			t.Fatal("a part that is not there")
		}
		if _, ok := st.Get("../../etc/passwd"); ok {
			t.Fatal("a name that is not a name reads nothing")
		}
	})
}

func TestPartStore_APartThatDoesNotPassTheCatalogChecksIsNotKept(t *testing.T) {
	eachStore(t, func(t *testing.T, st PartStore) {
		if err := st.Create(manifestOf("bad"), `<img src="https://x.invalid/a.png">`, nil); err == nil && st != nil {
			// a folder keeps what it is given (the readers leave an invalid part out); a database refuses it
			if _, ok := st.Get("bad"); ok {
				t.Fatal("an invalid part must never be listed")
			}
		}
		if err := st.Create([]byte(`{"name":"Bad Name"}`), `<p>x</p>`, nil); err == nil {
			t.Fatal("a manifest with no valid name is refused")
		}
	})
}

func TestPartStore_GetManyReadsEachPartOnceAndLeavesOutTheMissing(t *testing.T) {
	eachStore(t, func(t *testing.T, st PartStore) {
		for _, n := range []string{"alpha", "beta", "gamma"} {
			if err := st.Create(manifestOf(n), `<p>`+n+`</p>`, nil); err != nil {
				t.Fatal(err)
			}
		}
		got := st.GetMany([]string{"alpha", "gamma", "missing"})
		if len(got) != 2 || got["alpha"].Name != "alpha" || got["gamma"].Fragment != "<p>gamma</p>" {
			t.Fatalf("%v", got)
		}
		if len(st.GetMany(nil)) != 0 {
			t.Fatal("nothing asked, nothing read")
		}
	})
}

func TestPartStore_ListFiltersTheScenesAndThePieceAndSortsByName(t *testing.T) {
	eachStore(t, func(t *testing.T, st PartStore) {
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		must(st.Create(manifestOf("zeta"), `<p>z</p>`, nil))
		must(st.Create(manifestOf("alpha"), `<p>a</p>`, nil))
		must(st.Create(testSceneManifest("scene-aaa-1", "piece-a", "p1"), `<p>s</p>`, nil))
		must(st.Create(testSceneManifest("scene-aaa-2", "piece-a", "p2"), `<p>s</p>`, nil))
		must(st.Create(testSceneManifest("scene-bbb-1", "piece-b", "p1"), `<p>s</p>`, nil))
		names := func(ps []Part) string {
			var n []string
			for _, p := range ps {
				n = append(n, p.Name)
			}
			return strings.Join(n, ",")
		}
		if got := names(st.List(SceneFilter{Scenes: NoScenes})); got != "alpha,zeta" {
			t.Errorf("NoScenes: %s", got)
		}
		if got := names(st.List(SceneFilter{Scenes: OnlyScenes})); got != "scene-aaa-1,scene-aaa-2,scene-bbb-1" {
			t.Errorf("OnlyScenes: %s", got)
		}
		if got := names(st.List(SceneFilter{Scenes: OnlyScenes, Piece: "piece-a"})); got != "scene-aaa-1,scene-aaa-2" {
			t.Errorf("OfPiece: %s", got)
		}
		if got := names(st.List(SceneFilter{Scenes: AllParts})); got != "alpha,scene-aaa-1,scene-aaa-2,scene-bbb-1,zeta" {
			t.Errorf("AllParts: %s", got)
		}
	})
}

func TestPartStore_ReplaceKeepsTheOriginAndRefusesAnotherNameOrAMissingPart(t *testing.T) {
	eachStore(t, func(t *testing.T, st PartStore) {
		if err := st.Create(manifestOf("clock"), `<p>old</p>`, &PartOrigin{Title: "kept"}); err != nil {
			t.Fatal(err)
		}
		if err := st.Replace("clock", []byte(`{"name":"clock","description":"New","version":2,"levels":["simple"],"params":[]}`), `<p>new</p>`); err != nil {
			t.Fatal(err)
		}
		p, _ := st.Get("clock")
		if p.Fragment != "<p>new</p>" || p.Description != "New" || p.Origin == nil || p.Origin.Title != "kept" {
			t.Fatalf("%+v", p)
		}
		if err := st.Replace("clock", manifestOf("other"), `<p>x</p>`); err == nil {
			t.Fatal("a manifest that names another part is refused")
		}
		if err := st.Replace("nope", manifestOf("nope"), `<p>x</p>`); !errors.Is(err, ErrPartNotFound) {
			t.Fatalf("%v", err)
		}
		if p, _ := st.Get("clock"); p.Fragment != "<p>new</p>" {
			t.Fatal("a refused replace changes nothing")
		}
	})
}

func TestPartStore_DeleteAndDeleteScenes(t *testing.T) {
	eachStore(t, func(t *testing.T, st PartStore) {
		for _, p := range []struct {
			m []byte
		}{{manifestOf("keep")}, {manifestOf("gone")}, {testSceneManifest("scene-a-1", "piece-a", "p1")}, {testSceneManifest("scene-a-2", "piece-a", "p2")}, {testSceneManifest("scene-b-1", "piece-b", "p1")}} {
			if err := st.Create(p.m, `<p>x</p>`, nil); err != nil {
				t.Fatal(err)
			}
		}
		if err := st.Delete("gone"); err != nil {
			t.Fatal(err)
		}
		if err := st.Delete("gone"); !errors.Is(err, ErrPartNotFound) {
			t.Fatalf("%v", err)
		}
		n, err := st.DeleteScenes("piece-a")
		if err != nil || n != 2 {
			t.Fatalf("%d %v", n, err)
		}
		if _, ok := st.Get("scene-b-1"); !ok {
			t.Fatal("the scenes of another piece stay")
		}
		if _, ok := st.Get("keep"); !ok {
			t.Fatal("a part that is not a scene stays")
		}
	})
}

func TestCatalog_ReadsOnePartAndTheUsersWinOverTheBuiltin(t *testing.T) {
	for name, st := range partStores(t) {
		t.Run(name, func(t *testing.T) {
			cat := Catalog{Builtin: builtin(), Parts: st}
			if err := cat.CreatePart(manifestOf("clock"), `<p>tick</p>`, nil); err != nil {
				t.Fatal(err)
			}
			if err := cat.CreatePart([]byte(`{"name":"toggle","description":"mine","version":2,"levels":["simple"],"params":[]}`), `<p>mine</p>`, nil); !errors.Is(err, ErrPartExists) {
				t.Fatalf("a name of a part that comes with the program: %v", err)
			}
			got := cat.GetMany([]string{"clock", "toggle", "nope"})
			if len(got) != 2 || got["clock"].Source != "user" || got["toggle"].Source != "builtin" {
				t.Fatalf("%v", got)
			}
			if names := cat.List(); len(names) != 2 || names[0].Name != "clock" || names[1].Name != "toggle" {
				t.Fatalf("%+v", names)
			}
			if err := cat.DeleteUserPart("toggle"); !errors.Is(err, ErrBuiltinPart) {
				t.Fatalf("%v", err)
			}
			if err := cat.DeleteUserPart("nope"); !errors.Is(err, ErrPartNotFound) {
				t.Fatalf("%v", err)
			}
			if err := cat.ReplacePart("clock", []byte(`{"name":"clock","description":"v2","version":2,"levels":["simple"],"params":[]}`), `<p>v2</p>`); err != nil {
				t.Fatal(err)
			}
			if p, _ := cat.Get("clock"); p.Description != "v2" {
				t.Fatalf("%+v", p)
			}
			if err := cat.DeleteUserPart("clock"); err != nil {
				t.Fatal(err)
			}
			if _, ok := cat.Get("clock"); ok {
				t.Fatal("deleted")
			}
		})
	}
}

func TestCatalog_ABuiltinPartIsParsedOnceByNewCatalogAndOnEachCallByALiteral(t *testing.T) {
	cat := NewCatalog(nil)
	a, b := cat.builtinParts(), cat.builtinParts()
	if len(a) == 0 || len(a) != len(b) {
		t.Fatalf("%d %d", len(a), len(b))
	}
	// the same map: parsed once
	a["__marker"] = Part{}
	if _, ok := cat.builtinParts()["__marker"]; !ok {
		t.Fatal("NewCatalog parses the built-in parts once and keeps them")
	}
	delete(a, "__marker")
}

func TestDBPartsRowsAreTheFilesByteForByteAndAPartLeftOutIsNotListed(t *testing.T) {
	st := memStore(t)
	parts := NewDBParts(st)
	manifest := []byte("{\n  \"name\": \"clock\",\n  \"description\": \"Spaced  out\",\n  \"version\": 1,\n  \"levels\": [\"simple\"],\n  \"params\": []\n}\n")
	fragment := "<div>\n\tté & <b>x</b>\n</div>\n"
	if err := parts.Create(manifest, fragment, nil); err != nil {
		t.Fatal(err)
	}
	var row partRow
	if err := st.DB.First(&row, "name = ?", "clock").Error; err != nil {
		t.Fatal(err)
	}
	if string(row.ManifestJSON) != string(manifest) || string(row.FragmentHTML) != fragment {
		t.Fatalf("the manifest and the fragment are kept as they came: %q %q", row.ManifestJSON, row.FragmentHTML)
	}
	// a row that no longer passes the checks (written by another version) is left out with a warning, like a folder
	st.DB.Model(&partRow{}).Where("name = ?", "clock").Update("fragment_html", `<img src="https://x.invalid/a.png">`)
	if _, ok := parts.Get("clock"); ok || len(parts.List(SceneFilter{Scenes: AllParts})) != 0 {
		t.Fatal("an invalid part is not in the catalog")
	}
}

func TestTheStoreOfPartsGivesItsSchemaThroughItsMigration(t *testing.T) {
	st := memStore(t)
	if missing, err := st.CheckSchema(); err != nil || len(missing) > 0 {
		t.Fatalf("missing %v %v", missing, err)
	}
}

var _ = filepath.Join
var _ = os.Stat

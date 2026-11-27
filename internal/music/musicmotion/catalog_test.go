package musicmotion

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

const toggleManifest = `{"name":"toggle","description":"A switch that flips on the beat","version":1,"levels":["simple","medium"],
"params":[{"name":"label","type":"string","default":"allow","max_length":12},{"name":"count","type":"int","default":3,"min":1,"max":6},
{"name":"tone","type":"enum","default":"soft","options":["soft","hard"]},{"name":"glow","type":"bool","default":false},{"name":"speed","type":"number","default":1,"min":0.5,"max":2}]}`

func builtin() fstest.MapFS {
	return fstest.MapFS{
		"toggle/manifest.json": {Data: []byte(toggleManifest)},
		"toggle/part.html":     {Data: []byte(`<div class="t">{{label}}</div>`)},
	}
}

func TestAValidPartIsListedWithItsParameters(t *testing.T) {
	parts := Catalog{Builtin: builtin()}.List()
	if len(parts) != 1 || parts[0].Name != "toggle" || parts[0].Source != "builtin" || len(parts[0].Params) != 5 {
		t.Fatalf("%+v", parts)
	}
	if !parts[0].Supports(LevelSimple) || parts[0].Supports(LevelAdvanced) {
		t.Fatal("levels")
	}
}

func TestAPartAddedByTheUserShowsUpOnTheNextList(t *testing.T) {
	user := t.TempDir()
	c := Catalog{Builtin: builtin(), UserDir: user}
	if len(c.List()) != 1 {
		t.Fatal("start with one")
	}
	dir := filepath.Join(user, "clock")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"name":"clock","description":"A clock","version":1,"levels":["simple"],"params":[]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "part.html"), []byte(`<div>tick</div>`), 0o644)
	parts := c.List()
	if len(parts) != 2 || parts[0].Name != "clock" || parts[0].Source != "user" {
		t.Fatalf("%+v", parts)
	}
	if _, ok := c.Get("clock"); !ok {
		t.Fatal("a script must be able to use it")
	}
}

func TestABrokenPartIsLeftOutAndTheOthersWork(t *testing.T) {
	fsys := builtin()
	fsys["bad/manifest.json"] = &fstest.MapFile{Data: []byte(`{"name":"bad","description":"x","version":1,"levels":["extreme"],"params":[]}`)}
	fsys["bad/part.html"] = &fstest.MapFile{Data: []byte(`<p>x</p>`)}
	fsys["nofrag/manifest.json"] = &fstest.MapFile{Data: []byte(`{"name":"nofrag","description":"x","version":1,"levels":["simple"],"params":[]}`)}
	fsys["external/manifest.json"] = &fstest.MapFile{Data: []byte(`{"name":"external","description":"x","version":1,"levels":["simple"],"params":[]}`)}
	fsys["external/part.html"] = &fstest.MapFile{Data: []byte(`<img src="https://x.invalid/a.png">`)}
	fsys["takeover/manifest.json"] = &fstest.MapFile{Data: []byte(`{"name":"takeover","description":"x","version":1,"levels":["simple"],"params":[]}`)}
	fsys["takeover/part.html"] = &fstest.MapFile{Data: []byte(`<script>window.__seek = function(){}</script>`)}
	fsys["wrongname/manifest.json"] = &fstest.MapFile{Data: []byte(`{"name":"other","description":"x","version":1,"levels":["simple"],"params":[]}`)}
	fsys["wrongname/part.html"] = &fstest.MapFile{Data: []byte(`<p>x</p>`)}
	parts := Catalog{Builtin: fsys}.List()
	if len(parts) != 1 || parts[0].Name != "toggle" {
		t.Fatalf("only the valid part should remain: %+v", parts)
	}
}

func TestParametersTakeDefaultsAndRefuseWhatIsOutsideTheirLimits(t *testing.T) {
	p := Catalog{Builtin: builtin()}.List()[0]
	got, err := p.ResolveParams(map[string]any{"label": "no"})
	if err != nil || got["label"] != "no" || got["count"] != int64(3) || got["tone"] != "soft" || got["glow"] != false || got["speed"] != 1.0 {
		t.Fatalf("%v %v", got, err)
	}
	for name, bad := range map[string]map[string]any{
		"unknown":   {"colour": "red"},
		"too long":  {"label": "this label is far too long"},
		"too many":  {"count": 9.0},
		"not whole": {"count": 2.5},
		"bad enum":  {"tone": "loud"},
		"bad type":  {"glow": "yes"},
		"too slow":  {"speed": 0.1},
	} {
		if _, err := p.ResolveParams(bad); err == nil {
			t.Errorf("%s: should be refused", name)
		} else if !strings.Contains(err.Error(), "toggle") {
			t.Errorf("%s: the error must name the part: %v", name, err)
		}
	}
	if _, err := p.ResolveParams(map[string]any{"count": 9.0}); err == nil || !strings.Contains(err.Error(), `"count"`) {
		t.Errorf("the error must name the parameter: %v", err)
	}
}

func TestAUserPartReplacesABuiltinOfTheSameName(t *testing.T) {
	user := t.TempDir()
	dir := filepath.Join(user, "toggle")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"name":"toggle","description":"mine","version":2,"levels":["simple"],"params":[]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "part.html"), []byte(`<div>mine</div>`), 0o644)
	p, _ := Catalog{Builtin: builtin(), UserDir: user}.Get("toggle")
	if p.Source != "user" || p.Description != "mine" {
		t.Fatalf("%+v", p)
	}
}

func overVideoBuiltin(manifestExtra string) fstest.MapFS {
	return fstest.MapFS{
		"lower/manifest.json": {Data: []byte(`{"name":"lower","description":"A lower third","version":1,"levels":["simple"],"params":[]` + manifestExtra + `}`)},
		"lower/part.html":     {Data: []byte(`<div>hi</div>`)},
	}
}

func TestOverVideoIsFalseWhenAbsentAndTrueWhenSet(t *testing.T) {
	for extra, want := range map[string]bool{"": false, `,"over_video":false`: false, `,"over_video":true`: true} {
		parts := Catalog{Builtin: overVideoBuiltin(extra)}.List()
		if len(parts) != 1 || parts[0].OverVideo != want {
			t.Errorf("%q: %+v, want over_video %v", extra, parts, want)
		}
	}
}

func TestOverVideoIsListedInTheCatalogForEveryPart(t *testing.T) {
	raw, err := json.Marshal(Catalog{Builtin: overVideoBuiltin("")}.List())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"over_video":false`) {
		t.Fatalf("a part that is not marked does not say so: %s", raw)
	}
}

func TestAnOverVideoThatIsNotABooleanLeavesThePartOut(t *testing.T) {
	for _, extra := range []string{`,"over_video":"yes"`, `,"over_video":1`, `,"over_video":null,"over_vidoe":true`} {
		if _, err := parseManifest([]byte(`{"name":"lower","description":"A lower third","version":1,"levels":["simple"],"params":[]`+extra+`}`), "lower"); err == nil {
			t.Errorf("%q was accepted", extra)
		} else if extra == `,"over_video":"yes"` && !strings.Contains(err.Error(), "over_video") {
			t.Errorf("the refusal does not name the field: %v", err)
		}
	}
}

func TestTheFeaturesAnnounceVideoBackgroundAndTrack(t *testing.T) {
	for _, want := range []string{"video-background", "video-track"} {
		found := false
		for _, f := range Features {
			found = found || f == want
		}
		if !found {
			t.Fatalf("features %v lack %s", Features, want)
		}
	}
}

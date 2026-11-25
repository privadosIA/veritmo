package musicmotion

import (
	"bytes"
	"context"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/music/hyperframes"
	"veritmo/internal/video/motion"
)

// blockStoreWith writes a block into a new store, points the layers at it and gives the libraries a folder with every pinned
// file, so that the block can be used.
func blockStoreWith(t *testing.T, name, html string, checks *hyperframes.CheckResult) hyperframes.Store {
	t.Helper()
	reg := t.TempDir()
	dir := filepath.Join(reg, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	item := `{"name":"` + name + `","title":"T","dimensions":{"width":320,"height":180},"duration":2,"files":[{"path":"` + name + `.html","type":"hyperframes:composition"}],"variables":[{"id":"word","default":"hi"}]}`
	for p, s := range map[string]string{"registry-item.json": item, name + ".html": html} {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st := hyperframes.Store{Dir: t.TempDir()}
	if _, err := st.Import(hyperframes.Source{Dir: reg}, nil, false); err != nil {
		t.Fatal(err)
	}
	if checks != nil {
		b, _ := st.Get(name)
		b.Checks = checks
		if err := st.Save(b); err != nil {
			t.Fatal(err)
		}
	}
	old := BlockStore
	BlockStore = func() hyperframes.Store { return st }
	t.Cleanup(func() { BlockStore = old })
	// the libraries are installed (empty files: these tests do not draw them)
	libs := t.TempDir()
	for _, l := range hyperframes.Manifest() {
		p, _ := hyperframes.File(libs, l.URL)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte("x"), 0o644)
	}
	oldLibs := hyperframes.LibsDir
	hyperframes.LibsDir = libs
	t.Cleanup(func() { hyperframes.LibsDir = oldLibs })
	return st
}

func blockScript(item LayerItem) Script {
	s := testScript()
	s.Layers = []Layer{{ID: "fx", Items: []LayerItem{item}}}
	return s
}

func TestABlockIsAValidLayerItemAndRoundTrips(t *testing.T) {
	blockStoreWith(t, "spark", "<html><body>x</body></html>", &hyperframes.CheckResult{OK: true})
	s := blockScript(LayerItem{Block: "spark", Params: map[string]any{"word": "yo"}, Start: 12, End: 16})
	if err := s.Validate(Catalog{Builtin: BuiltinParts()}, testMap(), good()); err != nil {
		t.Fatal(err)
	}
}

func TestABadBlockItemIsRefusedAndNamed(t *testing.T) {
	cat := Catalog{Builtin: BuiltinParts()}
	blockStoreWith(t, "spark", "<html></html>", &hyperframes.CheckResult{OK: false, Problems: []string{"two passes give different frames at 1s"}})
	for _, c := range []struct {
		name string
		item LayerItem
		want string
	}{
		{"unknown block", LayerItem{Block: "nope", Start: 1, End: 3}, `the block "nope" is not in the catalog`},
		{"a block that failed its checks", LayerItem{Block: "spark", Start: 1, End: 3}, `cannot be used (failed): two passes give different frames`},
		{"a part and a block", LayerItem{Part: "toggle", Block: "spark", Start: 1, End: 3}, "names a part and a block"},
		{"an animation", LayerItem{Block: "spark", Start: 1, End: 3, Animate: Animate{"x": {{T: 0, Value: 1}}}}, "cannot animate"},
		{"not a block name", LayerItem{Block: "../x", Start: 1, End: 3}, "is not a block name"},
	} {
		err := blockScript(c.item).Validate(cat, testMap(), good())
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

func TestABlockNeedsTheLibraries(t *testing.T) {
	blockStoreWith(t, "spark", "<html></html>", &hyperframes.CheckResult{OK: true})
	hyperframes.LibsDir = t.TempDir() // nothing installed
	err := blockScript(LayerItem{Block: "spark", Start: 1, End: 3}).Validate(Catalog{Builtin: BuiltinParts()}, testMap(), good())
	if err == nil || !strings.Contains(err.Error(), "libraries") {
		t.Fatalf("%v", err)
	}
}

func TestABlockLayerIsDrawnInAnIframeThePageSeeks(t *testing.T) {
	blockStoreWith(t, "spark", `<html><head><title>x</title></head><body class="a&b">{{not a slot}}</body></html>`, &hyperframes.CheckResult{OK: true})
	spec := ProjectSpec{Map: testMap(), Design: good(), Script: blockScript(LayerItem{Block: "spark", Params: map[string]any{"word": "yo"}, Start: 12, End: 16}), Cat: Catalog{Builtin: BuiltinParts()}, Lib: Library{}, FPS: 30, Width: 640, Height: 360}
	pages, err := BuildPages(spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	// the block touches the second piece only
	if strings.Contains(pages[0].HTML, "data-hf-block") || !strings.Contains(pages[1].HTML, `data-hf-block="spark"`) {
		t.Fatal("the block must be in the pieces its range touches and no other")
	}
	h := pages[1].HTML
	for _, want := range []string{"<iframe", `name="hfvars:{&#34;word&#34;:&#34;yo&#34;}"`, "http://motion.invalid/hf/spark/", "scale(2.00000)", "__pendingSeeks", "from:12,to:16"} {
		if !strings.Contains(h, want) {
			t.Errorf("the page has no %q", want)
		}
	}
	if !strings.Contains(h, "{{not a slot}}") {
		t.Error("the block's text must go in as it is (no part parameters are filled in it)")
	}
}

func TestABlockLayerShowsOnlyInsideItsRangeInARealPage(t *testing.T) {
	browser := browserOrSkip(t)
	html := `<!doctype html><html><body style="background:transparent"><div id="b" style="width:60px;height:60px;background:#ff0000"></div>
<script>var v=window.__hyperframes.getVariables(); document.getElementById('b').title=v.word;
window.__timelines={}; window.__timelines.spark={pause:function(){},seek:function(t){document.getElementById('b').style.marginLeft=(t*40)+'px'}};</script></body></html>`
	blockStoreWith(t, "spark", html, &hyperframes.CheckResult{OK: true})
	// the real libraries are not needed by this block: the folder of empty ones stands in
	spec := ProjectSpec{Map: testMap(), Design: good(), Script: blockScript(LayerItem{Block: "spark", Start: 12, End: 16}), Cat: Catalog{Builtin: BuiltinParts()}, Lib: Library{}, FPS: 30, Width: 640, Height: 360}
	pages, err := BuildPages(spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	pg := pages[1] // the piece from 10 s to 20 s
	page := motion.Page{HTML: pg.HTML, Width: 640, Height: 360, Duration: pg.Seconds, Song: pg.Song}
	hyperframes.Extend(&page)
	sess, err := motion.Open(context.Background(), browser, page, motion.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	hasRed := func(secs float64) bool {
		pngBytes, err := sess.Frame(context.Background(), secs)
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(bytes.NewReader(pngBytes))
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		b := img.Bounds()
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				r, g, bl, _ := img.At(x, y).RGBA()
				if r>>8 > 200 && int(r>>8)-int(g>>8) > 60 && int(r>>8)-int(bl>>8) > 60 { // red, even if the page softens it
					n++
				}
			}
		}
		return n > 500
	}
	if hasRed(1) { // 11 s of the song: before the item
		t.Error("the block shows before its range")
	}
	if !hasRed(3) { // 13 s
		t.Error("the block does not show inside its range")
	}
	if hasRed(7) { // 17 s: after it
		t.Error("the block shows after its range")
	}
	if p := sess.Problems(); len(p) != 0 {
		t.Errorf("problems: %v", p)
	}
}

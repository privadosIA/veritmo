package musicmotion

import (
	"context"
	"strings"
	"testing"

	"veritmo/internal/music/hyperframes"
	"veritmo/internal/music/songmap"
)

func TestTheMenuHasOnlyWhatCanBeDrawnNow(t *testing.T) {
	blockStoreWith(t, "spark", "<html></html>", &hyperframes.CheckResult{OK: true})
	m := BlockMenu()
	if len(m) != 1 || m[0].Name != "spark" || m[0].Kind != "block" || len(m[0].Vars) != 1 || m[0].Vars[0].ID != "word" {
		t.Fatalf("%+v", m)
	}
	// a block that failed its checks is not offered
	blockStoreWith(t, "broken", "<html></html>", &hyperframes.CheckResult{OK: false, Problems: []string{"x"}})
	if len(BlockMenu()) != 0 {
		t.Fatal("a failed block must not be in the menu")
	}
	// nor is anything when the libraries are not installed
	blockStoreWith(t, "spark", "<html></html>", &hyperframes.CheckResult{OK: true})
	hyperframes.LibsDir = t.TempDir()
	if len(BlockMenu()) != 0 {
		t.Fatal("nothing is offered without the libraries")
	}
}

const scriptWithBlocks = `{"pieces":[
 {"section":"Intro","start":0,"end":12,"part":"glass-card","params":{"title":"Intro"}},
 {"section":"Verse","start":12,"end":30,"part":"terminal","lines":[{"line":0,"keyword":"line"},{"line":1,"keyword":"line"}]},
 {"section":"Chorus","start":30,"end":45,"part":"stamp","lines":[{"line":2,"keyword":"allow"}],"hit":{"word":"ALLOW","at":30.2,"length":0.8}},
 {"section":"Verse","start":45,"end":60,"part":"flat-slabs","lines":[{"line":3,"keyword":"verse"}]}],
 "layers":[{"id":"fx","items":[
  {"block":"spark","params":{"word":"yo","nope":1},"start":13,"end":19},
  {"block":"spark","start":18,"end":22},
  {"block":"ghost","start":23,"end":25},
  {"block":"spark","start":58,"end":70},
  {"part":"toggle","start":1,"end":5}]}]}`

func TestTheWriterIsOfferedTheMenuAndItsBlockLayersAreCheckedAndKept(t *testing.T) {
	blockStoreWith(t, "spark", "<html></html>", &hyperframes.CheckResult{OK: true})
	x := in(LevelSimple)
	x.Map = songmap.Slice(x.Map, 0, 60)
	w := &fakeWriter{answers: []string{scriptWithBlocks}}
	p, err := ProposeScript(context.Background(), w, x)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.users[0], "Blocks (optional") || !strings.Contains(w.users[0], "- spark (block") || !strings.Contains(w.users[0], "word=hi") {
		t.Fatalf("the writer must be told the blocks:\n%s", w.users[0])
	}
	var fx *Layer
	for i := range p.Script.Layers {
		if p.Script.Layers[i].ID == "fx" {
			fx = &p.Script.Layers[i]
		}
	}
	if fx == nil || len(fx.Items) != 1 || fx.Items[0].Block != "spark" || fx.Items[0].Start != 13 || fx.Items[0].Params["word"] != "yo" || fx.Items[0].Params["nope"] != nil {
		t.Fatalf("only the valid item is kept, with the declared variable only: %+v", p.Script.Layers)
	}
	for _, want := range []string{`"ghost" is not in the menu`, "outside the song", "overlaps"} {
		found := false
		for _, wn := range p.Warnings {
			found = found || strings.Contains(wn, want)
		}
		if !found {
			t.Errorf("no warning about %q: %v", want, p.Warnings)
		}
	}
}

func TestWithoutTheMenuTheWriterIsNotOfferedBlocksAndTheirLayersAreIgnored(t *testing.T) {
	blockStoreWith(t, "spark", "<html></html>", &hyperframes.CheckResult{OK: true})
	x := in(LevelSimple)
	x.Map = songmap.Slice(x.Map, 0, 60)
	x.NoBlocks = true
	w := &fakeWriter{answers: []string{scriptWithBlocks}}
	p, err := ProposeScript(context.Background(), w, x)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(w.users[0], "Blocks (optional") {
		t.Fatal("blocks are off: no menu")
	}
	for _, l := range p.Script.Layers {
		for _, it := range l.Items {
			if it.Block != "" {
				t.Fatalf("a block layer slipped in: %+v", l)
			}
		}
	}
}

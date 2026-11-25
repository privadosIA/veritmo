package musicmotion

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"veritmo/internal/music/musicmotion/fonts"
)

func themed(theme *Theme) Design {
	d := checkDesign()
	d.Theme = theme
	return d
}

func TestAThemeIsCheckedBeforeItIsUsed(t *testing.T) {
	ok := Theme{CSS: "#verse{font-family:var(--display);font-size:9vh}@font-face{font-family:x;src:url(data:font/woff2;base64,AAAA)}",
		Assets: map[string]string{"mascot": `<svg viewBox="0 0 10 10"><circle cx="5" cy="5" r="4"/></svg>`}, HUD: &HUD{TL: "LGTM Inc.", TR: "ticket HR-2291"}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("a", MaxHUDText+1)
	svg := `<svg viewBox="0 0 1 1"/>`
	cases := []struct {
		th   Theme
		want string
	}{
		{Theme{CSS: `@import url(https://example.com/x.css);`}, `design.theme.css has "@import"`},
		{Theme{CSS: `#a{background:url(https://example.com/x.png)}`}, "url() that is not a data: URL"},
		{Theme{CSS: `#a{background:url("x.png")}`}, "url() that is not a data: URL"},
		{Theme{CSS: `#a{width:expression(alert(1))}`}, "expression"},
		{Theme{CSS: `#a{behavior:url(data:x)}`}, "behavior"},
		{Theme{CSS: `#a{background:javascript:x}`}, "javascript"},
		{Theme{CSS: `</style><script>`}, `"<"`},
		{Theme{CSS: strings.Repeat("a", MaxThemeCSS+1)}, "the most is 20480"},
		{Theme{Assets: map[string]string{"a": svg, "b": svg, "c": svg, "d": svg, "e": svg, "f": svg}}, "6 drawings"},
		{Theme{Assets: map[string]string{"Mascot": svg}}, "lower case letters"},
		{Theme{Assets: map[string]string{"x": `<svg><script>alert(1)</script></svg>`}}, `design.theme.assets["x"]: it has a script`},
		{Theme{Assets: map[string]string{"x": `<div/>`}}, "it is not an SVG"},
		{Theme{HUD: &HUD{BR: long}}, "design.theme.hud.br"},
	}
	for _, c := range cases {
		if err := c.th.Validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v", c.want, err)
		}
	}
	// the design says which field: PUT /design answers with it
	d := checkDesign()
	d.Theme = &Theme{CSS: "@import 'x';"}
	if err := d.Validate(); err == nil || !strings.HasPrefix(err.Error(), "design.theme.css") {
		t.Fatalf("%v", err)
	}
	d.Theme = nil
	d.TransitionSeconds = 2.5
	if err := d.Validate(); err == nil || !strings.Contains(err.Error(), "design.transition_seconds") {
		t.Fatalf("%v", err)
	}
}

func TestThePageCarriesTheThemeAndFillsTheAssetSlots(t *testing.T) {
	frag := `<style>#x{position:absolute}</style><div id="x">{{asset:mascot}}{{asset:logo}}</div><script>window.__partSeek=function(t,b){};</script>`
	part, probs := ParsePart([]byte(testManifest), frag)
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	svg := `<svg viewBox="0 0 10 10"><circle id="m" r="4"/></svg>`
	sc := Scene{Part: part, Params: map[string]any{}, Design: themed(&Theme{CSS: "#verse{font-size:9vh}", Assets: map[string]string{"mascot": svg}, HUD: &HUD{TL: "A"}}), Mode: ModeDark, Duration: 4, Width: 1280, Height: 720}
	page, err := BuildPage(sc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(page, `<div id="x">`+svg+`</div>`) {
		t.Fatalf("the mascot is placed and an unknown slot is empty:\n%s", page)
	}
	i, j := strings.Index(page, "#verse{position:absolute"), strings.Index(page, `<style data-theme>#verse{font-size:9vh}</style>`)
	if i < 0 || j < 0 || j < i {
		t.Fatalf("the theme comes after the page's own stylesheet (%d, %d)", i, j)
	}
	if !strings.Contains(page, `window.__theme={"assets":{"mascot":`) || !strings.Contains(page, `"hud":{"tl":"A"}`) {
		t.Fatalf("window.__theme: %s", page[:min(len(page), 400)])
	}
	if strings.Index(page, "window.__theme") > strings.Index(page, "window.__partSeek") {
		t.Fatal("the data goes before the script of the part")
	}
	// no theme: the page has none of it (and the same fonts as always)
	sc.Design = themed(nil)
	plain, _ := BuildPage(sc)
	if strings.Contains(plain, "data-theme") || !strings.Contains(plain, `window.__theme={"assets":{},"hud":{}}`) || strings.Contains(plain, "mascot") {
		t.Fatal("a design with no theme has no theme in its page")
	}
	// unknown slots are reported as warnings
	if got := UnknownAssets(frag, sc.Design.Theme); len(got) != 2 {
		t.Fatalf("%v", got)
	}
	if got := UnknownAssets(frag, &Theme{Assets: map[string]string{"mascot": svg}}); len(got) != 1 || got[0] != "logo" {
		t.Fatalf("%v", got)
	}
	// a per-piece override replaces the HUD texts only
	over := themed(&Theme{CSS: "a{}", HUD: &HUD{TL: "A"}}).With(&DesignOverride{HUD: &HUD{TL: "B", BR: "C"}})
	if over.Theme.HUD.TL != "B" || over.Theme.HUD.BR != "C" || over.Theme.CSS != "a{}" {
		t.Fatalf("%+v", over.Theme)
	}
	base := themed(&Theme{HUD: &HUD{TL: "A"}})
	_ = base.With(&DesignOverride{HUD: &HUD{TL: "Z"}})
	if base.Theme.HUD.TL != "A" {
		t.Fatal("the design is not changed")
	}
}

func TestADesignWithATravelsThroughJSON(t *testing.T) {
	d := themed(&Theme{CSS: "#verse{font-size:9vh}", HUD: &HUD{TL: "A"}})
	d.TransitionSeconds = 0.3
	raw, _ := json.Marshal(d)
	var back Design
	if err := json.Unmarshal(raw, &back); err != nil || back.Theme == nil || back.Theme.HUD.TL != "A" || back.TransitionSeconds != 0.3 {
		t.Fatalf("%v %+v", err, back)
	}
	raw, _ = json.Marshal(checkDesign())
	if strings.Contains(string(raw), "theme") || strings.Contains(string(raw), "transition") {
		t.Fatalf("a design without them writes none: %s", raw)
	}
}

func TestThePackedFontsAreEmbeddedAndTheOldNamesStillWork(t *testing.T) {
	for name, p := range packed {
		for _, w := range p.Weights {
			if b, err := fonts.ReadFile(name, w); err != nil || len(b) < 5000 || string(b[:4]) != "wOF2" {
				t.Errorf("%s %d: %v", name, w, err)
			}
		}
		if b, err := os.ReadFile("fonts/" + name + ".LICENSE.txt"); err != nil || !strings.Contains(string(b), "SIL Open Font License") {
			t.Errorf("%s has its licence file: %v", name, err)
		}
		if Fonts[name] == "" || !strings.HasPrefix(Fonts[name], `"`+p.Family+`"`) {
			t.Errorf("%s: %q", name, Fonts[name])
		}
	}
	if len(packed) != 6 {
		t.Fatal("the pack has six fonts")
	}
	for _, old := range []string{"grotesk", "heavy", "mono", "serif", "rounded", "condensed"} {
		if Fonts[old] == "" || !slices.Contains(FontNames(), old) {
			t.Errorf("the old name %s keeps working", old)
		}
		d := checkDesign()
		d.Display, d.Body = old, old
		if err := d.Validate(); err != nil {
			t.Errorf("%s: %v", old, err)
		}
	}
	if !strings.HasPrefix(Fonts["grotesk"], `"Inter", "Helvetica Neue"`) || !strings.Contains(Fonts["mono"], `"JetBrains Mono", "DejaVu Sans Mono"`) || strings.Contains(Fonts["rounded"], "Inter") {
		t.Fatalf("an old name names the font of the pack first, then its former stack: %q %q", Fonts["grotesk"], Fonts["mono"])
	}
	for _, n := range []string{"inter", "space-grotesk", "jetbrains-mono", "playfair-display", "bebas-neue", "fraunces"} {
		d := checkDesign()
		d.Display = n
		if err := d.Validate(); err != nil {
			t.Errorf("a packed name is valid: %s %v", n, err)
		}
	}
	files := FontFiles()
	if files["bebas-neue"].Licence != "OFL-1.1" || len(files["bebas-neue"].Weights) != 1 || files["grotesk"].Family != "Inter" || files["rounded"].Family != "" {
		t.Fatalf("%+v", files)
	}
}

func TestAPageEmbedsOnlyTheFontsTheDesignNames(t *testing.T) {
	part := parsed(t, movingFragment)
	build := func(display, body string) string {
		d := checkDesign()
		d.Display, d.Body = display, body
		p, err := BuildPage(Scene{Part: part, Params: map[string]any{}, Design: d, Mode: ModeDark, Duration: 4, Width: 1280, Height: 720})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := build("bebas-neue", "grotesk")
	for _, want := range []string{`font-family:"Bebas Neue"`, `font-family:"Inter";font-style:normal;font-weight:400`, `font-weight:700`, "data:font/woff2;base64,", `--display:"Bebas Neue", Impact`, `--body:"Inter", "Helvetica Neue"`} {
		if !strings.Contains(p, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Contains(p, `"Fraunces"`) && strings.Contains(p, "font-family:\"Fraunces\"") || strings.Count(p, "@font-face") != 3 {
		t.Errorf("only the faces named: %d", strings.Count(p, "@font-face"))
	}
	if q := build("rounded", "condensed"); strings.Contains(q, "@font-face") || strings.Contains(q, "data:font") {
		t.Error("system stacks embed nothing")
	}
	if q := build("inter", "grotesk"); strings.Count(q, "@font-face") != 2 {
		t.Errorf("a font that is both is embedded once: %d", strings.Count(q, "@font-face"))
	}
	if FontFaceCSS("nope", "rounded") != "" {
		t.Error("nothing for names that are not in the pack")
	}
}

func TestTheFrameParts(t *testing.T) {
	cat := Catalog{Builtin: BuiltinParts()}
	for _, name := range []string{"hud-chrome", "grain"} {
		p, ok := cat.Get(name)
		if !ok || !p.Manual || len(p.Levels) != 3 || p.Source != "builtin" {
			t.Fatalf("%s: %+v", name, p)
		}
	}
	// they are drawn from t and beat only: the same moment is the same picture; the grain differs between moments
	browser := browserOrSkip(t)
	for _, name := range []string{"hud-chrome", "grain"} {
		p, _ := cat.Get(name)
		params, _ := p.ResolveParams(map[string]any{})
		sc := Scene{Part: p, Params: params, Design: themed(&Theme{HUD: &HUD{TL: "LGTM Inc.", TR: "ticket HR-2291", BL: "Q3", BR: "v7"}}), Mode: ModeDark, Duration: 4, Width: 1280, Height: 720}
		a, err := renderAt(context.Background(), browser, sc, []float64{1.0, 1.0, 2.37})
		if err != nil {
			t.Fatal(err)
		}
		if string(a[0]) != string(a[1]) {
			t.Errorf("%s: two draws of one moment differ", name)
		}
		if string(a[0]) == string(a[2]) {
			t.Errorf("%s: two moments look the same", name)
		}
	}
}

func TestTheFixedRuleWritesNoThemeAndTheProposerOnlyAddsTheFrameForAHud(t *testing.T) {
	cat := Catalog{Builtin: BuiltinParts()}
	m := longMap()
	in := ScriptInput{Map: m, Design: Fallback("a terminal", nil), Level: LevelSimple, Cat: cat}
	prop, err := ProposeScript(context.Background(), nil, in)
	if err != nil || len(prop.Script.Layers) != 0 {
		t.Fatalf("no hud, no frame: %v %+v", err, prop.Script.Layers)
	}
	if d := Fallback("a terminal", []string{"chorus"}); d.Theme != nil {
		t.Fatal("the fixed rule writes no theme")
	}
	in.Design = themed(&Theme{HUD: &HUD{TL: "LGTM Inc."}})
	in.Design.Display, in.Design.Body = "heavy", "mono"
	prop, err = ProposeScript(context.Background(), nil, in)
	if err != nil || len(prop.Script.Layers) != 2 {
		t.Fatalf("%v %+v", err, prop.Script.Layers)
	}
	for _, l := range prop.Script.Layers {
		if len(l.Items) != 1 || l.Items[0].Start != 0 || l.Items[0].End != prop.Script.Pieces[len(prop.Script.Pieces)-1].End {
			t.Fatalf("%+v", l)
		}
	}
	if err := prop.Script.Validate(cat, m, in.Design); err != nil {
		t.Fatal(err)
	}
}

// A theme that breaks its rules does not lose a design that is otherwise good.
func TestADesignWrittenWithABadThemeKeepsTheDesign(t *testing.T) {
	answer := `{"dark":"#0b0f14","light":"#e8f1ec","accent":"#2bff88","display":"inter","body":"jetbrains-mono","default_mode":"dark","concept":"a terminal","theme":{"css":"@import url(x);","hud":{"tl":"A"}}}`
	d, err := parseDesign(answer)
	if err != nil || d.Theme != nil || d.Display != "inter" {
		t.Fatalf("%v %+v", err, d)
	}
	d, err = parseDesign(strings.Replace(answer, `@import url(x);`, `#verse{font-size:9vh}`, 1))
	if err != nil || d.Theme == nil || d.Theme.HUD.TL != "A" {
		t.Fatalf("%v %+v", err, d)
	}
	if !strings.Contains(designSystem, `"theme"`) || !strings.Contains(designSystem, `"hud"`) {
		t.Fatal("the design prompt asks for a theme")
	}
}

func TestPiecesAreJoinedWithCrossfadesThatKeepTheSongInStep(t *testing.T) {
	cat := Catalog{Builtin: BuiltinParts()}
	spec := pieceSpec(t, cat)
	spec.Dir, spec.Name, spec.FPS = t.TempDir(), "p", 30
	spec.SongFile = writeTemp(t, "song.mp3", "ID3")
	read := func() string {
		if _, err := WriteProject(spec); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(filepath.Join(spec.Dir, "scenes.yaml"))
		return string(b)
	}
	// no crossfade: the file is what it always was
	cuts := read()
	if strings.Contains(cuts, "transition_seconds") || !strings.Contains(cuts, "silence_seconds: 10\n") {
		t.Fatalf("%s", cuts)
	}
	// the design's crossfade, shortened by a piece that sets its own and limited to half of the shorter piece
	spec.Design.TransitionSeconds = 0.4
	zero, big := 0.0, 2.0
	spec.Script.Pieces[2].TransitionSeconds = &zero
	spec.Script.Pieces[1].TransitionSeconds = &big
	fades := Crossfades(spec.Script, spec.Design)
	if len(fades) != 3 || fades[0] != 0 || fades[1] != 2 || fades[2] != 0 {
		t.Fatalf("%v", fades)
	}
	spec.Script.Pieces[1].TransitionSeconds = nil
	spec.Script.Pieces[2].TransitionSeconds = nil
	got := read()
	// p1 is 10 s and runs 0.4 s into p2, which runs 0.4 s into p3; the last one is not extended; a fade is written for every join
	for _, want := range []string{"id: p1\n    motion_graphic: \"music piece: counter (verse)\"\n    silence_seconds: 10.4\n", "id: p2\n", "silence_seconds: 10.4\n    transition_seconds: 0.4\n", "id: p3\n", "silence_seconds: 10\n    transition_seconds: 0.4\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("scenes.yaml lacks %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "transition_seconds") != 2 {
		t.Errorf("the first piece has none:\n%s", got)
	}
	// every piece is as long as the sum of the song: lengths minus fades
	total := 0.0
	for _, sec := range []float64{10.4, 10.4, 10} {
		total += sec
	}
	if total-0.8 != 30 {
		t.Fatalf("the video lasts as long as the song: %v", total-0.8)
	}
	spec.Script.Pieces[1].TransitionSeconds = &big
	if err := spec.Script.Validate(cat, spec.Map, spec.Design); err != nil {
		t.Fatal(err)
	}
	bad := 2.5
	spec.Script.Pieces[1].TransitionSeconds = &bad
	if err := spec.Script.Validate(cat, spec.Map, spec.Design); err == nil || !strings.Contains(err.Error(), "transition_seconds") {
		t.Fatalf("%v", err)
	}
}

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestADraftThatUsesAnUnknownAssetIsReadyWithAWarning(t *testing.T) {
	frag := `<style>#d{position:absolute;left:560px;top:200px;width:120px;height:120px;background:var(--accent)}</style><div id="d">{{asset:logo}}</div>
<script>window.__partSeek=function(t,b){document.getElementById('d').style.transform='translateX('+(b.number*37%400)+'px)';};</script>`
	w := &scriptedWriter{answers: []string{answerOf(testManifest, frag)}}
	e := draftEngine(t, w, nil)
	d, _ := e.Store.Create("a part with a logo", "", nil)
	e.Run(context.Background(), d.ID)
	got, _ := e.Store.Get(d.ID)
	if got.State != DraftReady || len(got.Problems) != 0 || len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "unknown asset logo") {
		t.Fatalf("%+v", got)
	}
	// a scene draft whose design's theme has the drawing has no warning
	spec := pieceSpec(t, e.Cat)
	spec.Design.Theme = &Theme{Assets: map[string]string{"logo": `<svg viewBox="0 0 1 1"/>`}}
	e.SceneInput = func(ref SceneRef, brief string) (SceneDraftInput, error) { return BuildSceneInput(spec, ref, brief) }
	big := strings.Replace(frag, "left:560px;top:200px;width:120px;height:120px", "left:10vw;top:10vh;width:80vw;height:80vh", 1) // a scene fills the frame
	w.answers = []string{answerOf(`{"name":"x","description":"a logo","version":1,"levels":["simple"],"params":[]}`, big)}
	w.asked = nil
	d2, _ := e.Store.Create("(scene)", "", nil)
	d2.Scene = &SceneRef{Piece: "api-mm-20261113-081046-a1b2c3", ScriptPiece: "p1"}
	_ = e.Store.Save(d2)
	e.Run(context.Background(), d2.ID)
	if got2, _ := e.Store.Get(d2.ID); got2.State != DraftReady || len(got2.Warnings) != 0 {
		t.Fatalf("%+v", got2)
	}
}

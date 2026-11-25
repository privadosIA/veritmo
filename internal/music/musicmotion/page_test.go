package musicmotion

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"veritmo/internal/music/songmap"
	"veritmo/internal/video/motion"
)

func browserOrSkip(t *testing.T) string {
	t.Helper()
	b, err := motion.Find("")
	if err != nil {
		t.Skip("no Chrome or Chromium on this machine")
	}
	return b.Path
}

// pulsePart is a small part for the tests: a dot that swells inside every beat and a label.
func pulsePart() Part {
	return Part{
		Manifest: Manifest{Name: "pulse", Description: "a dot that swells on the beat", Version: 1, Levels: []string{LevelSimple},
			Params: []Param{{Name: "label", Type: "string", Default: "ok", MaxLength: 20}}},
		Fragment: `<style>#dot{position:absolute;left:560px;top:200px;width:160px;height:160px;border-radius:50%;background:var(--accent)}
#lab{position:absolute;left:0;right:0;top:80px;text-align:center;font-size:48px}</style>
<div id="dot"></div><div id="lab">{{label}}</div>
<script>window.__partSeek=function(t,b){document.getElementById('dot').style.transform='scale('+(1+0.4*Math.pow(1-b.position,3))+')';};</script>`,
		Source: "builtin",
	}
}

func testScene() Scene {
	d := good()
	p := pulsePart()
	params, _ := p.ResolveParams(map[string]any{"label": "allow <b>"})
	return Scene{Part: p, Params: params, Design: d, Mode: ModeDark, Duration: 4, Width: 1280, Height: 720,
		Lines: []Line{{Text: "second line", Start: 0.5, End: 2, Keyword: 2, Words: []Word{{"first", 0.5, 1, true}, {"line", 1, 1.5, true}, {"forget", 1.5, 2, true}}}, {Text: "last", Start: 2.2, End: 4, Keyword: 0, Words: []Word{{"last", 2.2, 3, true}}}},
		Hit:   &Hit{Word: "ALLOW", At: 3.0, Length: 0.8}}
}

func openPage(t *testing.T, sc Scene) (*motion.Session, string) {
	t.Helper()
	html, err := BuildPage(sc)
	if err != nil {
		t.Fatal(err)
	}
	song := songmap.Build(songmap.Input{Duration: sc.Duration, BPM: 120})
	raw, _ := json.Marshal(song)
	s, err := motion.Open(context.Background(), browserOrSkip(t), motion.Page{HTML: html, Width: sc.Width, Height: sc.Height, Duration: sc.Duration, Song: raw}, motion.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, html
}

func evalAt(t *testing.T, s *motion.Session, at float64, expr string, out any) {
	t.Helper()
	if _, err := s.Frame(context.Background(), at); err != nil {
		t.Fatal(err)
	}
	if err := s.EvalJSON(context.Background(), "JSON.stringify("+expr+")", out); err != nil {
		t.Fatal(err)
	}
}

// shownLine is the words (spans) of the verse line that is on screen, as an array (empty when none is).
const shownLine = `(function(){var l=Array.from(document.querySelectorAll('#verse .line')).filter(function(x){return x.style.display!=='none'})[0];return l?Array.from(l.children):[];})()`

func TestAPageIsCompleteAndLoadsNothingFromOutside(t *testing.T) {
	html, err := BuildPage(testScene())
	if err != nil {
		t.Fatal(err)
	}
	if probs := motion.StaticProblems(html); len(probs) != 0 {
		t.Fatalf("%v", probs)
	}
	if strings.Contains(html, "allow <b>") || !strings.Contains(html, "allow &lt;b&gt;") {
		t.Fatal("a parameter must be escaped in the text")
	}
}

func TestAPageWithAnUnknownSlotIsRefused(t *testing.T) {
	sc := testScene()
	sc.Part.Fragment = `<div>{{nope}}</div>`
	if _, err := BuildPage(sc); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("%v", err)
	}
}

func TestTheWordsAppearAtTheirTimeAndNotBefore(t *testing.T) {
	s, _ := openPage(t, testScene())
	var op []string
	evalAt(t, s, 0.3, shownLine+`.map(function(w){return w.style.opacity})`, &op)
	if len(op) != 0 {
		t.Fatalf("no line before its start: %v", op)
	}
	var ops []string
	words := shownLine + `.map(function(w){return w.style.opacity})`
	evalAt(t, s, 1.2, words, &ops)
	if len(ops) != 3 || ops[0] != "1" || ops[2] != "0" {
		t.Fatalf("at 1.2 s the first word is shown, the third not yet: %v", ops)
	}
	evalAt(t, s, 1.9, words, &ops)
	if ops[2] != "1" {
		t.Fatalf("at 1.9 s all three are shown: %v", ops)
	}
}

func TestTheKeywordIsInTheAccentColour(t *testing.T) {
	s, _ := openPage(t, testScene())
	var cols []string
	evalAt(t, s, 1.9, shownLine+`.map(function(w){return getComputedStyle(w).color})`, &cols)
	// accent #2bff88 = rgb(43, 255, 136); the text colour is the light tone #e8f1ec = rgb(232, 241, 236)
	if len(cols) != 3 || cols[2] != "rgb(43, 255, 136)" || cols[0] != "rgb(232, 241, 236)" {
		t.Fatalf("the keyword is the accent and the rest the text colour: %v", cols)
	}
}

func TestTheWordHitFillsTheFrameAtItsTime(t *testing.T) {
	s, _ := openPage(t, testScene())
	type st struct {
		Vis string  `json:"vis"`
		W   float64 `json:"w"`
	}
	read := func(at float64) st {
		var r st
		evalAt(t, s, at, `{vis: getComputedStyle(document.getElementById('hit')).visibility, w: document.getElementById('hit').scrollWidth}`, &r)
		return r
	}
	if r := read(2.9); r.Vis != "hidden" {
		t.Fatalf("not before its time: %+v", r)
	}
	if r := read(3.3); r.Vis != "visible" || r.W < 600 {
		t.Fatalf("at 3.3 s the word fills the frame: %+v", r)
	}
	if r := read(3.9); r.Vis != "hidden" {
		t.Fatalf("gone after its length: %+v", r)
	}
}

func TestAPageThatPassesTheChecksOfAMotionPage(t *testing.T) {
	html, err := BuildPage(testScene())
	if err != nil {
		t.Fatal(err)
	}
	song := songmap.Build(songmap.Input{Duration: 4, BPM: 120})
	raw, _ := json.Marshal(song)
	probs, err := motion.Check(context.Background(), browserOrSkip(t), motion.Page{HTML: html, Width: 1280, Height: 720, Duration: 4, Song: raw}, motion.Limits{})
	if err != nil || len(probs) != 0 {
		t.Fatalf("%v %v", probs, err)
	}
}

func TestASceneWithAProblemIsRefusedBeforeBuilding(t *testing.T) {
	for name, edit := range map[string]func(*Scene){
		"no duration":      func(s *Scene) { s.Duration = 0 },
		"bad mode":         func(s *Scene) { s.Mode = "grey" },
		"keyword outside":  func(s *Scene) { s.Lines[0].Keyword = 7 },
		"hit of two words": func(s *Scene) { s.Hit.Word = "two words" },
		"hit past the end": func(s *Scene) { s.Hit.At = 9 },
	} {
		sc := testScene()
		edit(&sc)
		if _, err := BuildPage(sc); err == nil {
			t.Errorf("%s: should be refused", name)
		}
	}
}

// partScene is a scene made from a part with its default parameters, 120 bpm, with a verse.
func partScene(t *testing.T, p Part) Scene {
	t.Helper()
	params, err := p.ResolveParams(nil)
	if err != nil {
		t.Fatal(err)
	}
	return Scene{Part: p, Params: params, Design: good(), Mode: ModeDark, Duration: 4, Width: 1280, Height: 720,
		Lines: []Line{{Text: "one two three", Start: 0.5, End: 4, Keyword: 1, Words: []Word{{"one", 0.5, 1, true}, {"two", 1, 1.5, true}, {"three", 1.5, 2, true}}}}}
}

func framesOf(t *testing.T, sc Scene, times []float64) [][]byte {
	t.Helper()
	s, _ := openPage(t, sc)
	var out [][]byte
	for _, at := range times {
		f, err := s.Frame(context.Background(), at)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, f)
	}
	return out
}

// Every part of the catalog that comes with the program, rendered at 30 fps over a beat: it passes the checks
// of a motion page, it changes inside a beat and between beats, and two renders give the same frames.
func TestEveryBuiltinPartIsAGoodPageThatMovesOnTheBeat(t *testing.T) {
	browser := browserOrSkip(t)
	for _, p := range (Catalog{Builtin: BuiltinParts()}).List() {
		p := p
		t.Run(p.Name, func(t *testing.T) {
			probs, err := CheckPart(context.Background(), browser, p, CheckOptions{NoMovement: p.Name == "blank"})
			if err != nil || len(probs) != 0 {
				t.Fatalf("%v %v", probs, err)
			}
		})
	}
}

// With DUMP_PARTS_DIR set, writes a frame of every built-in part there (a look at the parts while writing them).
func TestDumpPartsForALook(t *testing.T) {
	dir := os.Getenv("DUMP_PARTS_DIR")
	if dir == "" {
		t.Skip("set DUMP_PARTS_DIR to write a frame of every part")
	}
	at := 2.3
	if v, err := strconv.ParseFloat(os.Getenv("DUMP_PARTS_AT"), 64); err == nil {
		at = v
	}
	for _, p := range (Catalog{Builtin: BuiltinParts()}).List() {
		for _, mode := range []string{ModeDark, ModeLight} {
			sc := partScene(t, p)
			sc.Mode = mode
			if mode == ModeLight {
				sc.Design.Accent = "#d12f58"
			}
			f := framesOf(t, sc, []float64{at})
			if err := os.WriteFile(filepath.Join(dir, p.Name+"-"+mode+".png"), f[0], 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// With MEASURE_PARTS set, renders 6 s of every built-in part at 30 and 60 fps and prints the time it takes.
func TestMeasureRenderTimes(t *testing.T) {
	if os.Getenv("MEASURE_PARTS") == "" {
		t.Skip("set MEASURE_PARTS=1 to measure the render times")
	}
	browser := browserOrSkip(t)
	only := os.Getenv("MEASURE_ONLY") // part names, separated by commas: measure only those
	for _, p := range (Catalog{Builtin: BuiltinParts()}).List() {
		if only != "" && !strings.Contains(","+only+",", ","+p.Name+",") {
			continue
		}
		sc := partScene(t, p)
		sc.Duration = 6
		html, err := BuildPage(sc)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(songmap.Build(songmap.Input{Duration: 6, BPM: 120}))
		for _, fps := range []int{30, 60} {
			start := time.Now()
			_, _, err := motion.Render(context.Background(), motion.Job{Page: motion.Page{HTML: html, Width: 1280, Height: 720, Duration: 6, Song: raw},
				FPS: fps, Browser: browser, Out: filepath.Join(t.TempDir(), "out.mp4")})
			if err != nil {
				t.Fatal(err)
			}
			el := time.Since(start)
			t.Logf("%-14s %2d fps  %6.1f s for 6 s of video  (%.2f s per video second)", p.Name, fps, el.Seconds(), el.Seconds()/6)
		}
	}
}

// verseAt reads, at a time, the background and the colour of each word of the verse that is on screen.
func verseAt(t *testing.T, s *motion.Session, at float64) [][2]string {
	t.Helper()
	var out [][2]string
	evalAt(t, s, at, shownLine+`.map(function(w){var c=getComputedStyle(w);return [c.backgroundColor,c.color]})`, &out)
	return out
}

func TestTheVerseStyleMarksTheWordsAsTheyAreSung(t *testing.T) {
	const accent, text, transparent = "rgb(43, 255, 136)", "rgb(232, 241, 236)", "rgba(0, 0, 0, 0)"
	bg := "rgb(11, 15, 20)"
	mk := func(style string) *motion.Session {
		sc := testScene()
		sc.Design.VerseStyle = style
		sc.Lines = []Line{{Text: "a b c", Start: 0.5, End: 4, Keyword: -1, Words: []Word{{"alpha", 0.5, 1, true}, {"bravo", 1, 1.5, true}, {"charlie", 1.5, 2, true}}}}
		sc.Hit = nil
		s, _ := openPage(t, sc)
		return s
	}
	// The second word is being sung at 1.2 s.
	hl := verseAt(t, mk(VerseStyleHighlight), 1.2)
	if len(hl) != 3 || hl[0][0] != accent || hl[1][0] != accent || hl[2][0] != transparent || hl[0][1] != bg {
		t.Fatalf("highlight: the first two words are marked, the third is not: %v", hl)
	}
	ka := verseAt(t, mk(VerseStyleKaraoke), 1.2)
	if ka[0][1] != accent || ka[1][1] != accent || ka[2][1] != text || ka[0][0] != transparent {
		t.Fatalf("karaoke: the first two words are in the accent colour: %v", ka)
	}
	pl := verseAt(t, mk(VerseStylePlain), 1.2)
	if pl[0][1] != text || pl[0][0] != transparent || pl[1][0] != transparent {
		t.Fatalf("plain is as before: %v", pl)
	}
	// A design with no field behaves as plain.
	none := verseAt(t, mk(""), 1.2)
	if !reflect.DeepEqual(none, pl) {
		t.Fatalf("no style is plain: %v against %v", none, pl)
	}
	// The word of the moment moves on with the voice.
	later := verseAt(t, mk(VerseStyleHighlight), 1.9)
	if later[2][0] != accent {
		t.Fatalf("by 1.9 s the third is marked: %v", later)
	}
}

func TestAVerseStyleThatDoesNotExistIsRefusedNamingTheField(t *testing.T) {
	d := good()
	d.VerseStyle = "wiggle"
	err := d.Validate()
	if err == nil || !strings.Contains(err.Error(), "design.verse_style") || !strings.Contains(err.Error(), "karaoke") || !strings.Contains(err.Error(), "glow") {
		t.Fatalf("%v", err)
	}
	for _, ok := range []string{"", "plain", "highlight", "karaoke", "glow"} {
		d.VerseStyle = ok
		if err := d.Validate(); err != nil {
			t.Fatalf("%q: %v", ok, err)
		}
	}
}

// lettersShown counts the letters of the word hit that are visible (opacity above one half) at a time.
func lettersShown(t *testing.T, s *motion.Session, at float64) (shown, total int, visibility string) {
	t.Helper()
	var r struct {
		Shown int    `json:"shown"`
		Total int    `json:"total"`
		Vis   string `json:"vis"`
	}
	evalAt(t, s, at, `(function(){var c=Array.from(document.querySelectorAll('#hit .ch'));return {shown:c.filter(function(x){return parseFloat(x.style.opacity)>0.5}).length,total:c.length,vis:getComputedStyle(document.getElementById('hit')).visibility}})()`, &r)
	return r.Shown, r.Total, r.Vis
}

func TestTheMetalAndGlowHitsEnterLetterByLetterAndPlainStaysOnePiece(t *testing.T) {
	for _, style := range []string{HitStyleMetal, HitStyleGlow} {
		sc := testScene() // the hit is ALLOW, from 3.0 s for 0.8 s
		sc.Design.HitStyle = style
		s, _ := openPage(t, sc)
		early, total, _ := lettersShown(t, s, 3.12)
		later, _, _ := lettersShown(t, s, 3.5)
		if total != 5 || early < 1 || early > 4 || later != 5 {
			t.Errorf("%s: %d of %d letters shortly after it starts and %d a little later", style, early, total, later)
		}
		if _, _, vis := lettersShown(t, s, 3.9); vis != "hidden" {
			t.Errorf("%s: gone after its length, but it is %s", style, vis)
		}
	}
	plain, _ := openPage(t, testScene())
	if _, total, vis := lettersShown(t, plain, 3.3); total != 0 || vis != "visible" {
		t.Errorf("plain is one piece of text: %d letters, %s", total, vis)
	}
}

func TestAHitStyleThatDoesNotExistIsRefusedNamingTheField(t *testing.T) {
	d := good()
	d.HitStyle = "neon"
	if err := d.Validate(); err == nil || !strings.Contains(err.Error(), "design.hit_style") || !strings.Contains(err.Error(), "metal") {
		t.Fatalf("%v", err)
	}
	for _, ok := range []string{"", "plain", "metal", "glow"} {
		d.HitStyle = ok
		if err := d.Validate(); err != nil {
			t.Fatalf("%q: %v", ok, err)
		}
	}
}

func TestAMetalOrGlowPageStillPassesTheChecksOfAMotionPage(t *testing.T) {
	for _, style := range []string{HitStyleMetal, HitStyleGlow} {
		sc := testScene()
		sc.Design.HitStyle = style
		sc.Design.VerseStyle = VerseStyleHighlight
		html, err := BuildPage(sc)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(songmap.Build(songmap.Input{Duration: 4, BPM: 120}))
		probs, err := motion.Check(context.Background(), browserOrSkip(t), motion.Page{HTML: html, Width: 1280, Height: 720, Duration: 4, Song: raw}, motion.Limits{})
		if err != nil || len(probs) != 0 {
			t.Fatalf("%s: %v %v", style, probs, err)
		}
	}
}

func TestAGlowingVerseHasATextShadowOfTheTextColourAndNoBackground(t *testing.T) {
	sc := testScene()
	sc.Design.VerseStyle = VerseStyleGlow
	sc.Lines = []Line{{Text: "x", Start: 0.5, End: 4, Keyword: 2, Words: []Word{{"alpha", 0.5, 1, true}, {"bravo", 1, 1.5, true}, {"charlie", 1.5, 2, true}}}}
	sc.Hit = nil
	s, _ := openPage(t, sc)
	var out [][3]string
	evalAt(t, s, 1.9, shownLine+`.map(function(w){var c=getComputedStyle(w);return [c.textShadow,c.backgroundColor,c.color]})`, &out)
	if len(out) != 3 {
		t.Fatalf("%v", out)
	}
	for i, w := range out[:2] {
		if !strings.Contains(w[0], "rgb(232, 241, 236)") || w[1] != "rgba(0, 0, 0, 0)" || w[2] != "rgb(232, 241, 236)" {
			t.Errorf("word %d is in the text colour with a glow of it and no background: %v", i, w)
		}
	}
	if !strings.Contains(out[2][0], "rgb(43, 255, 136)") || out[2][2] != "rgb(43, 255, 136)" {
		t.Errorf("the keyword keeps the accent colour (with its own glow): %v", out[2])
	}
}

// spyPart records the beat object it is given on every frame.
func spyPart() Part {
	return Part{
		Manifest: Manifest{Name: "spy", Description: "records the beat object of each frame", Version: 1, Levels: []string{LevelSimple}},
		Fragment: `<div id="x"></div><script>window.__partSeek=function(t,b){window.__seen={t:t,word:b.word,line:b.line,number:b.number,time:b.time};};</script>`,
		Source:   "builtin",
	}
}

func TestThePartIsToldTheWordAndTheLineBeingSung(t *testing.T) {
	sc := testScene()
	sc.Part = spyPart()
	sc.Params = nil
	sc.Hit = nil
	s, _ := openPage(t, sc)
	var seen struct {
		Word *struct {
			Text     string  `json:"text"`
			Index    int     `json:"index"`
			Line     int     `json:"line"`
			Start    float64 `json:"start"`
			End      float64 `json:"end"`
			Position float64 `json:"position"`
		} `json:"word"`
		Line *struct {
			Index    int     `json:"index"`
			Text     string  `json:"text"`
			Start    float64 `json:"start"`
			End      float64 `json:"end"`
			Position float64 `json:"position"`
		} `json:"line"`
		Number *int `json:"number"`
	}
	// 1.2 s is 40 % of "line" (1 to 1.5 s... that is 0.2/0.5) in the first line, which runs from 0.5 to 2 s
	evalAt(t, s, 1.2, "window.__seen", &seen)
	if seen.Word == nil || seen.Word.Text != "line" || seen.Word.Index != 1 || seen.Word.Line != 0 || seen.Word.Start != 1 || seen.Word.End != 1.5 || math.Abs(seen.Word.Position-0.4) > 1e-6 {
		t.Fatalf("word at 1.2 s: %+v", seen.Word)
	}
	if seen.Line == nil || seen.Line.Index != 0 || seen.Line.Text != "second line" || seen.Line.Start != 0.5 || seen.Line.End != 2 || math.Abs(seen.Line.Position-(0.7/1.5)) > 1e-6 {
		t.Fatalf("line at 1.2 s: %+v", seen.Line)
	}
	if seen.Number == nil {
		t.Fatal("the rest of the beat object is still there")
	}
	// the second line comes after a break from 2 to 2.2 s: nothing is sung there
	seen.Word, seen.Line = nil, nil
	evalAt(t, s, 2.1, "window.__seen", &seen)
	if seen.Word != nil || seen.Line != nil {
		t.Fatalf("in a break there is no word and no line: %+v %+v", seen.Word, seen.Line)
	}
	// before the first word
	seen.Word, seen.Line = nil, nil
	evalAt(t, s, 0.2, "window.__seen", &seen)
	if seen.Word != nil || seen.Line != nil {
		t.Fatalf("before the voice: %+v %+v", seen.Word, seen.Line)
	}
	// the second line
	evalAt(t, s, 2.5, "window.__seen", &seen)
	if seen.Word == nil || seen.Word.Text != "last" || seen.Word.Line != 1 || seen.Line == nil || seen.Line.Index != 1 {
		t.Fatalf("second line: %+v %+v", seen.Word, seen.Line)
	}
}

func TestTheWordsOfTheSceneSayWhetherTheyWereHeard(t *testing.T) {
	sc := testScene()
	sc.Lines[0].Words[1].Heard = false
	html, err := BuildPage(sc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, `"t":"line","s":1,"e":1.5,"heard":false`) || !strings.Contains(html, `"t":"first","s":0.5,"e":1,"heard":true`) {
		t.Fatal("window.__scene.lines[].words[] must carry heard")
	}
}

// hitBox is where the word hit is drawn: the box of its text and its font size, at a time inside its span.
type hitBox struct {
	Left, Right, Top, Bottom, Size float64
}

func readHit(t *testing.T, s *motion.Session, at float64) hitBox {
	t.Helper()
	var b hitBox
	evalAt(t, s, at, `(function(){var h=document.getElementById('hit');var r=document.createRange();r.selectNodeContents(h);var b=r.getBoundingClientRect();`+
		`return {Left:b.left,Right:b.right,Top:b.top,Bottom:b.bottom,Size:parseFloat(getComputedStyle(h).fontSize)};})()`, &b)
	return b
}

func TestALongWordHitFitsInsideTheFrameInEveryLook(t *testing.T) {
	for _, style := range []string{"", HitStyleMetal, HitStyleGlow} {
		sc := testScene()
		sc.Design.Display = "serif"
		sc.Design.HitStyle = style
		sc.Lines = nil
		sc.Hit = &Hit{Word: "FLIGHT", At: 1, Length: 1.5}
		s, _ := openPage(t, sc)
		b := readHit(t, s, 1.8)
		if b.Left < 0 || b.Right > float64(sc.Width) || b.Right-b.Left > 0.9*float64(sc.Width)+1 {
			t.Errorf("hit style %q: FLIGHT spans x=%.0f to %.0f of %d (font %.0f px): it must fit inside 90 %% of the width", style, b.Left, b.Right, sc.Width, b.Size)
		}
		if b.Size >= 0.6*float64(sc.Height) {
			t.Errorf("hit style %q: a wide word is drawn smaller than 60 %% of the height, got %.0f px", style, b.Size)
		}
	}
}

func TestAShortWordHitIsDrawnAtSixtyPercentOfTheHeight(t *testing.T) {
	sc := testScene()
	sc.Lines = nil
	sc.Hit = &Hit{Word: "go", At: 1, Length: 1.5}
	s, _ := openPage(t, sc)
	b := readHit(t, s, 1.8)
	if math.Abs(b.Size-0.6*float64(sc.Height)) > 1 {
		t.Fatalf("go is drawn at %.1f px, want %.0f (60 %% of %d)", b.Size, 0.6*float64(sc.Height), sc.Height)
	}
}

func TestTheWordHitIsTheSameInTwoRenders(t *testing.T) {
	sc := testScene()
	sc.Design.Display = "serif"
	sc.Lines = nil
	sc.Hit = &Hit{Word: "FLIGHT", At: 1, Length: 1.5}
	var frames [2][]byte
	for i := range frames {
		s, _ := openPage(t, sc)
		f, err := s.Frame(context.Background(), 1.8)
		if err != nil {
			t.Fatal(err)
		}
		frames[i] = f
	}
	if string(frames[0]) != string(frames[1]) {
		t.Fatal("two renders of the same hit give different frames")
	}
}

// hogPart makes the word hit as big as it can be, as a part that forgot to leave room would.
func hogPart() Part {
	return Part{
		Manifest: Manifest{Name: "hog", Description: "draws nothing but makes the hit enormous", Version: 1, Levels: []string{LevelSimple}},
		Fragment: `<div id="x"></div><script>window.__partSeek=function(t,b){var h=document.getElementById('hit');if(h){h.style.fontSize='900px';}};</script>`,
		Source:   "builtin",
	}
}

func TestAnOversizedWordHitFailsThePageChecksNamingTheHit(t *testing.T) {
	// the generic check looks at 0, 2 and 4 s: the hit sits between them, where only its own check looks
	sc := testScene()
	sc.Part, sc.Params = hogPart(), nil
	sc.Hit = &Hit{Word: "FLIGHT", At: 2.4, Length: 1}
	sc.Duration = 4
	probs, err := CheckPart(context.Background(), browserOrSkip(t), hogPart(), CheckOptions{Scene: &sc, NoMovement: true})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(probs, "\n")
	if !strings.Contains(joined, "word hit") || !strings.Contains(joined, "cut by the") {
		t.Fatalf("problems = %q, want one that names the word hit and the edge", probs)
	}
	// and a hit that fits passes
	ok := testScene()
	ok.Hit = &Hit{Word: "FLIGHT", At: 2.4, Length: 1}
	probs, err = CheckPart(context.Background(), browserOrSkip(t), pulsePart(), CheckOptions{Scene: &ok, NoMovement: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(probs) != 0 {
		t.Fatalf("a hit that fits must pass the checks: %q", probs)
	}
}

// The display face may not be in when the page is built. The hit is measured again on every seek until the fonts are loaded, and once
// after, so a word measured in the fallback face (NIGHT in the serif) ends up inside 90 % of the frame at 0.4 s.
func TestTheWordHitIsFittedWithTheDisplayFontLoaded(t *testing.T) {
	for _, display := range []string{"serif", "mono", "fraunces", "heavy"} {
		sc := testScene()
		sc.Design.Display = display
		sc.Lines = nil
		sc.Design.HitStyle = HitStyleGlow
		sc.Hit = &Hit{Word: "night", At: 0, Length: 2}
		s, _ := openPage(t, sc)
		var loaded bool
		evalAt(t, s, 0.1, `document.fonts.status==='loaded'`, &loaded)
		b := readHit(t, s, 0.4)
		if b.Left < 0.03*float64(sc.Width)-1 || b.Right > float64(sc.Width)*0.97+1 || b.Right-b.Left > 0.9*float64(sc.Width)+1 {
			t.Errorf("display %q: NIGHT spans x=%.0f to %.0f of %d at 0.4 s (fonts loaded: %v): it must fit inside 90 %% of the width", display, b.Left, b.Right, sc.Width, loaded)
		}
		// the size is the same on later frames: the fit does not wander once the fonts are in
		later := readHit(t, s, 1.0)
		if math.Abs(later.Size-b.Size) > 0.5 {
			t.Errorf("display %q: the hit is %.1f px at 0.4 s and %.1f px at 1.0 s", display, b.Size, later.Size)
		}
	}
}

// A cut of the page's own word hit, with a part that never touches the layers, is the server's: it is not refused, it is written down.
func TestAHouseHitThatIsCutIsTheServersProblemNotThePartsWhenItIsNotTouched(t *testing.T) {
	sc := testScene()
	sc.Design.Theme = &Theme{CSS: `#hit{font-size:900px !important}`}
	sc.Hit = &Hit{Word: "FLIGHT", At: 2.4, Length: 1}
	sc.Duration = 4
	var server []string
	probs, err := CheckPart(context.Background(), browserOrSkip(t), pulsePart(), CheckOptions{Scene: &sc, NoMovement: true, Server: &server})
	if err != nil {
		t.Fatal(err)
	}
	if len(probs) != 0 {
		t.Fatalf("the part does not touch the hit and must not be refused: %q", probs)
	}
	if len(server) == 0 || !strings.HasPrefix(server[0], "server: the word hit leaves the frame") {
		t.Fatalf("server = %q, want the hit problem prefixed server:", server)
	}
	// a part that does touch the hit is still refused for it
	server = nil
	probs, err = CheckPart(context.Background(), browserOrSkip(t), hogPart(), CheckOptions{Scene: &sc, NoMovement: true, Server: &server})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(probs, "\n"), "word hit") || len(server) != 0 {
		t.Fatalf("a part that restyles the hit is refused: probs=%q server=%q", probs, server)
	}
}

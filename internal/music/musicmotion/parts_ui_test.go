package musicmotion

import (
	"testing"

	"veritmo/internal/video/motion"
)

// uiScene is a scene of one of the interface parts with the parameters given (the rest are the defaults), 120 bpm (a
// beat every 0.5 s), no verse.
func uiScene(t *testing.T, part string, params map[string]any, duration float64) Scene {
	t.Helper()
	p, ok := (Catalog{Builtin: BuiltinParts()}).Get(part)
	if !ok {
		t.Fatalf("the part %s is not in the catalog", part)
	}
	resolved, err := p.ResolveParams(params)
	if err != nil {
		t.Fatal(err)
	}
	return Scene{Part: p, Params: resolved, Design: good(), Mode: ModeDark, Duration: duration, Width: 1280, Height: 720}
}

func evalNumber(t *testing.T, s *motion.Session, at float64, expr string) float64 {
	t.Helper()
	var v float64
	evalAt(t, s, at, expr, &v)
	return v
}

func TestTheToastIsOutBeforeItsFirstBeatInTheMiddleAndOutAfterItsLast(t *testing.T) {
	s, _ := openPage(t, uiScene(t, "achievement-toast", map[string]any{"beats": 6, "progress": 80}, 6))
	opacity := func(at float64) float64 {
		return evalNumber(t, s, at, `parseFloat(document.getElementById('at').style.opacity)`)
	}
	width := func(at float64) float64 {
		return evalNumber(t, s, at, `parseFloat(document.querySelector('#at .bar i').style.width)`)
	}
	if opacity(0) > 0.05 {
		t.Fatalf("it is out before its first beat: %v", opacity(0))
	}
	if opacity(1.5) < 0.95 || width(1.5) <= 1 || width(1.5) >= 79 {
		t.Fatalf("in the middle it is in and the bar is part of the way: opacity %v width %v", opacity(1.5), width(1.5))
	}
	if opacity(3.6) > 0.05 {
		t.Fatalf("after its last beat it is out again: %v", opacity(3.6))
	}
	if width(2.4) <= width(1.2) {
		t.Fatalf("the bar fills as the beats go: %v then %v", width(1.2), width(2.4))
	}
}

func TestTheSunburstTurnsAndPulses(t *testing.T) {
	s, _ := openPage(t, uiScene(t, "sunburst", nil, 4))
	angle := func(at float64) string {
		var v string
		evalAt(t, s, at, `document.querySelector('#sb .rays').style.transform`, &v)
		return v
	}
	if angle(1.0) == angle(1.4) || angle(1.0) == angle(2.0) {
		t.Fatal("the rays must turn")
	}
}

func TestTheSearchBoxTypesAndListsOneMoreSuggestionOnEveryBeat(t *testing.T) {
	s, _ := openPage(t, uiScene(t, "search-box", map[string]any{"type_beats": 4}, 6))
	typed := func(at float64) string {
		var v string
		evalAt(t, s, at, `document.querySelector('#sx .q').textContent`, &v)
		return v
	}
	listed := func(at float64) float64 {
		return evalNumber(t, s, at, `document.querySelectorAll('#sx .item.on').length`)
	}
	start, middle, end := typed(0.5), typed(1.0), typed(2.5)
	if len(start) == 0 || len(middle) <= len(start) || end != "it's only a fancy autocomplete" {
		t.Fatalf("the phrase grows and ends whole: %q %q %q", start, middle, end)
	}
	if listed(0.2) != 0 {
		t.Fatal("no suggestion before the first beat")
	}
	if listed(2.0) != 4 || listed(3.0) != 5 {
		t.Fatalf("four suggestions after four beats and the five there are after six: %v %v", listed(2.0), listed(3.0))
	}
	if first := evalNumber(t, s, 2.0, `document.querySelector('#sx .item.first').classList.contains('on') ? 1 : 0`); first != 1 {
		t.Fatal("the first suggestion is listed (and marked)")
	}
}

func TestTheCursorPressesTheButtonOnItsBeat(t *testing.T) {
	s, _ := openPage(t, uiScene(t, "click-button", map[string]any{"press_beat": 4}, 6))
	label := func(at float64) string {
		var v string
		evalAt(t, s, at, `document.querySelector('#cb .btn').textContent`, &v)
		return v
	}
	if label(0.5) != "Don't ask again" || label(1.9) != "Don't ask again" {
		t.Fatalf("before the press the first label: %q %q", label(0.5), label(1.9))
	}
	if label(2.1) != "Allowed" || label(4) != "Allowed" {
		t.Fatalf("after the press the second label stays: %q %q", label(2.1), label(4))
	}
	overlap := func(at float64) float64 {
		return evalNumber(t, s, at, `(function(){var a=document.querySelector('#cb .btn').getBoundingClientRect(), c=document.querySelector('#cb .cursor').getBoundingClientRect(); var cx=c.left+c.width*0.2, cy=c.top+c.height*0.2; return (cx>=a.left&&cx<=a.right&&cy>=a.top&&cy<=a.bottom)?1:0})()`)
	}
	if overlap(0.3) != 0 || overlap(2.1) != 1 {
		t.Fatalf("the cursor is away at the start and on the button at the press: %v %v", overlap(0.3), overlap(2.1))
	}
	moving := evalNumber(t, s, 1.0, `parseFloat(document.querySelector('#cb .cursor').style.left)`)
	later := evalNumber(t, s, 1.5, `parseFloat(document.querySelector('#cb .cursor').style.left)`)
	if moving == later {
		t.Fatal("the cursor is seen moving before the press")
	}
}

func TestTheRollCounterRollsFromOneNumberToAnotherAndTearsAtZero(t *testing.T) {
	s, _ := openPage(t, uiScene(t, "roll-counter", map[string]any{"from": 10, "to": 0, "beats": 4, "glitch": true}, 6))
	shown := func(at float64) string {
		var v string
		evalAt(t, s, at, `document.getElementById('rc').getAttribute('data-shown')`, &v)
		return v
	}
	if shown(0) != "10" {
		t.Fatalf("it starts at from: %q", shown(0))
	}
	if shown(1.99) != "0" {
		t.Fatalf("it ends at to when the beats are over: %q", shown(1.99))
	}
	mid := shown(1.0)
	if mid == "10" || mid == "0" {
		t.Fatalf("in the middle it is between the two: %q", mid)
	}
	// A digit is in the middle of its roll: its next digit is visible.
	if evalNumber(t, s, 0.6, `Array.from(document.querySelectorAll('#rc .nxt')).filter(function(x){return x.style.visibility==='visible'}).length`) < 1 {
		t.Fatal("a digit must be seen rolling")
	}
	// The glitch: on the beat after it reaches 0 the value is negative and the digits are torn.
	if g := shown(2.25); g != "-1" {
		t.Fatalf("after zero the glitch shows a negative value: %q", g)
	}
	if evalNumber(t, s, 2.25, `getComputedStyle(document.querySelector('#rc .neg')).visibility==='visible' ? 1 : 0`) != 1 {
		t.Fatal("the minus sign is shown")
	}
	if shown(3.1) != "0" {
		t.Fatalf("it settles at zero after the glitch: %q", shown(3.1))
	}
	// Without the glitch it stays at zero.
	s2, _ := openPage(t, uiScene(t, "roll-counter", map[string]any{"from": 10, "to": 0, "beats": 4, "glitch": false}, 6))
	var v string
	evalAt(t, s2, 2.25, `document.getElementById('rc').getAttribute('data-shown')`, &v)
	if v != "0" {
		t.Fatalf("no glitch, no negative value: %q", v)
	}
	// Fewer digits than the counter: no leading zero is shown.
	s3, _ := openPage(t, uiScene(t, "roll-counter", map[string]any{"from": 120, "to": 7, "beats": 3, "glitch": false}, 6))
	evalAt(t, s3, 1.6, `document.getElementById('rc').getAttribute('data-shown')`, &v)
	if v != "7" {
		t.Fatalf("a number of one digit is shown with one digit: %q", v)
	}
}

func TestTheTableFillsOneRowPerBeatAndMarksOne(t *testing.T) {
	s, _ := openPage(t, uiScene(t, "data-table", map[string]any{"highlight": 3}, 6))
	rows := func(at float64) float64 {
		return evalNumber(t, s, at, `document.querySelectorAll('#dt tr.row.on').length`)
	}
	if rows(0.1) != 0 || rows(0.5) != 1 || rows(2.0) != 4 || rows(4.0) != 6 {
		t.Fatalf("one row after one beat, four after four, all six at the end: %v %v %v %v", rows(0.1), rows(0.5), rows(2.0), rows(4.0))
	}
	var colour string
	evalAt(t, s, 2.0, `getComputedStyle(document.querySelector('#dt tr.hl td')).color`, &colour)
	if colour != "rgb(43, 255, 136)" {
		t.Fatalf("the marked row is in the accent colour: %s", colour)
	}
	if evalNumber(t, s, 2.0, `document.querySelectorAll('#dt tr.hl').length`) != 1 {
		t.Fatal("one row is marked")
	}
}

func TestTheTreeEntersALinePerBeatAndItsBarsGrow(t *testing.T) {
	s, _ := openPage(t, uiScene(t, "dependency-tree", nil, 8))
	lines := func(at float64) float64 {
		return evalNumber(t, s, at, `document.querySelectorAll('#dp .line.on').length`)
	}
	bar := func(at float64) float64 {
		return evalNumber(t, s, at, `parseFloat(document.querySelector('#dp .line .track i').style.width)`)
	}
	if lines(0.1) != 1 || lines(2.0) != 5 || lines(5.0) != 9 {
		t.Fatalf("a line per beat: %v %v %v", lines(0.1), lines(2.0), lines(5.0))
	}
	if !(bar(0.1) < bar(0.5) && bar(0.5) < bar(1.5)) || bar(3.0) < 85 {
		t.Fatalf("the bar of the first line grows to its value (90%%): %v %v %v", bar(0.1), bar(0.5), bar(3.0))
	}
}

func TestThePasswordFieldShowsADotPerBeatAndFillsItsBar(t *testing.T) {
	s, _ := openPage(t, uiScene(t, "password-field", map[string]any{"password": "ChangeMe", "strength": 1}, 12))
	dots := func(at float64) float64 {
		return evalNumber(t, s, at, `document.querySelector('#pw .dots').textContent.length`)
	}
	filled := func(at float64) float64 {
		return evalNumber(t, s, at, `document.querySelectorAll('#pw .steps i.on').length`)
	}
	if dots(0.1) != 0 || dots(1.0) != 2 || dots(4.5) != 8 {
		t.Fatalf("a dot per beat up to the length of the password: %v %v %v", dots(0.1), dots(1.0), dots(4.5))
	}
	if filled(3.0) != 0 || filled(6.0) != 1 {
		t.Fatalf("the bar fills after the typing, as many steps as the strength: %v %v", filled(3.0), filled(6.0))
	}
	strong, _ := openPage(t, uiScene(t, "password-field", map[string]any{"strength": 4, "per_beat": 4}, 12))
	if evalNumber(t, strong, 6.0, `document.querySelectorAll('#pw .steps i.on').length`) != 4 {
		t.Fatal("four steps for a strength of four")
	}
}

func TestTheTimelineStripFollowsTheSongAndAFixedProgressStays(t *testing.T) {
	sc := uiScene(t, "timeline-strip", nil, 6)
	sc.SongStart, sc.SongLength = 80, 200 // the piece starts at 40 % of the song
	s, _ := openPage(t, sc)
	cursor := func(at float64) float64 {
		return evalNumber(t, s, at, `parseFloat(document.querySelector('#tl .cursor').style.left)`)
	}
	if c := cursor(1.0); c < 40.2 || c > 41 {
		t.Fatalf("a second into a piece that starts at 40 %% of a 200 s song the cursor is a little beyond 40 %%: %v", c)
	}
	if cursor(4.0) <= cursor(1.0) {
		t.Fatal("the cursor moves as the song goes")
	}
	passed := func(at float64) float64 {
		return evalNumber(t, s, at, `document.querySelectorAll('#tl .mark.pass').length`)
	}
	if passed(1.0) != 2 {
		t.Fatalf("at 40 %% the first two of five marks (0 and 25 %%) are passed: %v", passed(1.0))
	}
	var colour string
	evalAt(t, s, 1.0, `getComputedStyle(document.querySelector('#tl .mark.pass .lab')).color`, &colour)
	if colour != "rgb(43, 255, 136)" {
		t.Fatalf("a label the cursor has passed is in the accent colour: %s", colour)
	}
	fixed, _ := openPage(t, uiScene(t, "timeline-strip", map[string]any{"progress": 25}, 6))
	a := evalNumber(t, fixed, 0.5, `parseFloat(document.querySelector('#tl .cursor').style.left)`)
	b := evalNumber(t, fixed, 5.0, `parseFloat(document.querySelector('#tl .cursor').style.left)`)
	if a != 25 || b != 25 {
		t.Fatalf("with progress 25 the cursor stays at a quarter: %v %v", a, b)
	}
	// With no song it goes along its own scene.
	own, _ := openPage(t, uiScene(t, "timeline-strip", nil, 6))
	if x := evalNumber(t, own, 3.0, `parseFloat(document.querySelector('#tl .cursor').style.left)`); x < 49 || x > 51 {
		t.Fatalf("no song: halfway through the scene the cursor is halfway: %v", x)
	}
}

func TestThePhosphorTerminalIsGreenOrAmberAndTypesOnTheBeat(t *testing.T) {
	colour := func(tone string) string {
		s, _ := openPage(t, uiScene(t, "phosphor-terminal", map[string]any{"tone": tone}, 6))
		var c string
		evalAt(t, s, 2.5, `getComputedStyle(document.getElementById('pt')).color`, &c)
		return c
	}
	if g, a := colour("green"), colour("amber"); g != "rgb(61, 255, 114)" || a != "rgb(255, 176, 0)" {
		t.Fatalf("the phosphor is green or amber whatever the design: %s %s", g, a)
	}
	s, _ := openPage(t, uiScene(t, "phosphor-terminal", nil, 6))
	typed := func(at float64) float64 {
		return evalNumber(t, s, at, `document.getElementById('pt').textContent.length`)
	}
	if !(typed(0.5) < typed(2.0)) || typed(0.1) > 3 {
		t.Fatalf("it shows more of its lines after four beats than after one: %v %v", typed(0.5), typed(2.0))
	}
}

// pixelsAt renders a part twice with the parameters given and says whether the frames at a time are different.
func framesDiffer(t *testing.T, part string, a, b map[string]any, at float64) bool {
	t.Helper()
	fa := framesOf(t, uiScene(t, part, a, 6), []float64{at})
	fb := framesOf(t, uiScene(t, part, b, 6), []float64{at})
	return string(fa[0]) != string(fb[0])
}

func TestTheOscilloscopeAndTheSunDrawWhatTheirParametersSay(t *testing.T) {
	if !framesDiffer(t, "oscilloscope", map[string]any{"lobes": 2}, map[string]any{"lobes": 5}, 1.0) {
		t.Fatal("another number of lobes is another figure")
	}
	if !framesDiffer(t, "synth-sun", map[string]any{"stripes": 4}, map[string]any{"stripes": 10}, 1.0) {
		t.Fatal("another number of stripes is another sun")
	}
	if !framesDiffer(t, "synth-sun", map[string]any{"speed": 0}, map[string]any{"speed": 3}, 1.3) {
		t.Fatal("the grid comes towards the viewer at its speed (and stands still at 0)")
	}
}

func TestTheMeshAndTheNebulaFollowTheirParametersAndTheSeed(t *testing.T) {
	if !framesDiffer(t, "warp-grid", map[string]any{"density": 8}, map[string]any{"density": 30}, 1.0) {
		t.Fatal("another density is another mesh")
	}
	if !framesDiffer(t, "nebula", map[string]any{"seed": 1}, map[string]any{"seed": 2}, 1.0) {
		t.Fatal("another seed is another cloud")
	}
	if framesDiffer(t, "nebula", map[string]any{"seed": 5}, map[string]any{"seed": 5}, 1.0) {
		t.Fatal("the same seed is the same cloud")
	}
}

func TestTheCloudKeepsItsWordsInTheFrameAndTheyAppearOverTheBeats(t *testing.T) {
	s, _ := openPage(t, uiScene(t, "word-cloud", map[string]any{"words": "uno|dos|tres", "amount": 12}, 6))
	var early, late, out int
	evalAt(t, s, 0.2, `Array.from(document.querySelectorAll('#wc > div')).filter(function(e){return parseFloat(e.style.opacity)>0.5}).length`, &early)
	evalAt(t, s, 5.5, `Array.from(document.querySelectorAll('#wc > div')).filter(function(e){return parseFloat(e.style.opacity)>0.5}).length`, &late)
	if early >= late {
		t.Fatalf("words appear over the beats: %v shown early, %v late", early, late)
	}
	evalAt(t, s, 5.5, `Array.from(document.querySelectorAll('#wc > div')).filter(function(e){var r=e.getBoundingClientRect();return r.left<-1||r.top<-1||r.right>innerWidth+1||r.bottom>innerHeight+1}).length`, &out)
	if out != 0 {
		t.Fatalf("%v words fall out of the frame", out)
	}
}

func TestTheNetworkLightsUpLayerByLayer(t *testing.T) {
	a := framesOf(t, uiScene(t, "network-diagram", map[string]any{"layers": 5, "nodes": 4}, 8), []float64{0.1, 7.0})
	if string(a[0]) == string(a[1]) {
		t.Fatal("the layers light up over the beats")
	}
	if !framesDiffer(t, "network-diagram", map[string]any{"layers": 3}, map[string]any{"layers": 6}, 6.0) {
		t.Fatal("another number of layers is another network")
	}
}

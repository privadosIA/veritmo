package musicmotion

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/music/songmap"
)

// A scene that owns the verse: the words come from window.__scene.lines, each at its time. It follows the rules a scene is checked
// against: the verse at 14 vh and a frame that fills a good part of the picture.
const ownVerseManifest = `{"name":"probe","description":"the verse drawn by the part","version":1,"levels":["simple"],"verse":"own","params":[]}`
const ownVerseFragment = `<style>#w{position:absolute;left:10vw;right:10vw;top:30vh;font-size:14vh;color:var(--accent)}#b{position:absolute;left:8vw;top:10vh;width:70vw;height:80vh;border:1vh solid var(--fg)}</style><div id="w"></div><div id="b"></div>
<script>(function(){var L=window.__scene.lines, w=document.getElementById('w'), b=document.getElementById('b');
window.__partSeek=function(t,beat){var s='';L.forEach(function(l){(l.words||[]).forEach(function(x){if(t>=x.s)s+=x.t+' ';});});w.textContent=s;b.style.transform='translateX('+(Math.max(0,beat.number)*30%600)+'px)';};})();</script>`

func ownVersePart(t *testing.T) Part {
	t.Helper()
	p, probs := ParsePart([]byte(ownVerseManifest), ownVerseFragment)
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	return p
}

// pieceSpec is a project spec with a script of three pieces over a song with two lines of words.
func pieceSpec(t *testing.T, cat Catalog) ProjectSpec {
	t.Helper()
	m := songmap.Map{Duration: 30, BPM: 120, Words: []songmap.Word{
		{Text: "Greg", Start: 2, End: 2.4, Line: 0}, {Text: "used", Start: 2.5, End: 2.9, Line: 0}, {Text: "to", Start: 3, End: 3.2, Line: 0},
		{Text: "run", Start: 3.3, End: 3.6, Line: 0}, {Text: "billing", Start: 3.7, End: 4.4, Line: 0},
		{Text: "then", Start: 12, End: 12.4, Line: 1}, {Text: "he", Start: 12.5, End: 12.7, Line: 1}, {Text: "left", Start: 12.8, End: 13.3, Line: 1}}}
	one, two := 0, 1
	script := Script{Version: ScriptVersion, Pieces: []Piece{
		{ID: "p1", Start: 0, End: 10, Section: "verse", Part: "counter", Lines: []ScriptLine{{Line: &one, Keyword: "billing"}}},
		{ID: "p2", Start: 10, End: 20, Section: "verse", Part: "counter", Lines: []ScriptLine{{Line: &two, Keyword: "left"}}},
		{ID: "p3", Start: 20, End: 30, Section: "chorus", Part: "counter"}}}
	return ProjectSpec{Map: m, Design: Fallback("a terminal", nil), Script: script, Cat: cat, Lib: NewLibrary(nil), Width: 1280, Height: 720}
}

func TestTheSceneInputCarriesTheWordsTheirTimesAndThePlaceInTheSong(t *testing.T) {
	cat := Catalog{Builtin: BuiltinParts()}
	in, err := BuildSceneInput(pieceSpec(t, cat), SceneRef{Piece: "api-mm-x", ScriptPiece: "p2"}, "  a door closing  ")
	if err != nil {
		t.Fatal(err)
	}
	if in.Index != 2 || in.Total != 3 || in.Section != "verse" || in.SectionIndex != 1 || in.Start != 10 || in.End != 20 || in.Brief != "a door closing" || len(in.Neighbours) != 2 {
		t.Fatalf("%+v", in)
	}
	user := SceneUser(in, cat, "")
	for _, want := range []string{"then@2.00", "he@2.50", "left@2.80 (keyword)", `"verse"`, "a door closing", "10.00 seconds long", "piece 1 uses", "piece 3 uses", "EXAMPLE of a scene", "window.__scene"} {
		if !strings.Contains(user, want) && !strings.Contains(SceneSystem()+user, want) {
			t.Errorf("the writer's message lacks %q:\n%s", want, user)
		}
	}
	if strings.Contains(user, "Greg") {
		t.Error("the words of another piece are not in this one's input")
	}
	if !strings.Contains(SceneUser(in, cat, "it was too quiet"), "previous answer was refused: it was too quiet") {
		t.Error("a retry carries the problem")
	}
	if strings.Contains(SceneUser(SceneDraftInput{Scene: Scene{Design: Fallback("x", nil), Mode: ModeDark, Duration: 5}, Ref: SceneRef{ScriptPiece: "p"}}, cat, ""), "The verse (times") {
		t.Error("a piece with no lines says so")
	}
	if _, err := BuildSceneInput(pieceSpec(t, cat), SceneRef{Piece: "x", ScriptPiece: "nope"}, ""); err == nil {
		t.Fatal("an unknown script piece is an error")
	}
	if !strings.Contains(SceneSystem(), "YOU draw the lyric") || !strings.Contains(SceneSystem(), "One visual metaphor") {
		t.Fatal("the scene prompt asks for the lyric to be drawn and carries the rubric")
	}
}

func TestTheSceneExampleIsNotACatalogPartAndFollowsItsOwnRules(t *testing.T) {
	manifest, fragment := SceneExample()
	p, probs := ParsePart([]byte(manifest), fragment)
	if len(probs) > 0 || !p.OwnsVerse() || p.IsScene() {
		t.Fatalf("%v %+v", probs, p.Manifest)
	}
	for _, c := range (Catalog{Builtin: BuiltinParts()}).ListAll() {
		if c.Name == "scene-example" {
			t.Fatal("the example is kept out of every catalog")
		}
	}
	probs, err := CheckPart(context.Background(), browserOrSkip(t), p, CheckOptions{})
	if err != nil || len(probs) > 0 {
		t.Fatalf("%v %v", err, probs)
	}
}

func TestAPartThatOwnsTheVerseLeavesThePageWithoutIt(t *testing.T) {
	own := ownVersePart(t)
	sc, _ := checkScene(own)
	page, err := BuildPage(sc)
	if err != nil || strings.Contains(page, `id="verse"`) || !strings.Contains(page, `id="hit"`) || !strings.Contains(page, `"lines":[{`) {
		t.Fatalf("%v verse-less page, hit kept, lines in window.__scene", err)
	}
	both := own
	both.Hit = DrawnByPart
	page, _ = BuildPage(Scene{Part: both, Params: map[string]any{}, Design: checkDesign(), Mode: ModeDark, Duration: 4, Width: 1280, Height: 720, Hit: &Hit{Word: "x", At: 1, Length: 1}})
	if strings.Contains(page, `id="verse"`) || strings.Contains(page, `id="hit"`) || strings.Contains(page, `id="hitbg"`) {
		t.Fatal("a part that owns the hit too leaves its layers out")
	}
	plain := parsed(t, movingFragment)
	page, _ = BuildPage(Scene{Part: plain, Params: map[string]any{}, Design: checkDesign(), Mode: ModeDark, Duration: 4, Width: 1280, Height: 720})
	if !strings.Contains(page, `id="verse"`) || !strings.Contains(page, `id="hit"`) {
		t.Fatal("any other part is drawn as it always was")
	}
	if _, probs := ParsePart([]byte(strings.Replace(ownVerseManifest, `"own"`, `"mine"`, 1)), ownVerseFragment); len(probs) == 0 {
		t.Fatal("verse is page or own")
	}
}

func TestAnOwnVersePartDrawsTheWordsAtTheirTimeInARealBrowser(t *testing.T) {
	browser := browserOrSkip(t)
	own := ownVersePart(t)
	probs, err := CheckPart(context.Background(), browser, own, CheckOptions{})
	if err != nil || len(probs) > 0 {
		t.Fatalf("%v %v", err, probs)
	}
	// a part that owns the verse and draws nothing of it: frames never change with a word
	still := strings.Replace(ownVerseFragment, "w.textContent=s;", "", 1)
	still = strings.Replace(still, "b.style.transform=", "var _u=", 1)
	still = strings.Replace(still, ";};})();", ";b.style.transform='translateX('+(Math.floor((t-0.7)/0.5)*50)+'px)';};})();", 1) // moves (at 0.7, 1.2, 1.7 s), but on a clock that ignores the words
	p, probs := ParsePart([]byte(ownVerseManifest), still)
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	probs, err = CheckPart(context.Background(), browser, p, CheckOptions{})
	if err != nil || len(probs) == 0 || !strings.Contains(strings.Join(probs, ";"), "did not appear at its time") {
		t.Fatalf("a word that is not drawn at its time is refused: %v %v", err, probs)
	}
	// in the real scene of a script piece
	cat := Catalog{Builtin: BuiltinParts()}
	in, _ := BuildSceneInput(pieceSpec(t, cat), SceneRef{Piece: "x", ScriptPiece: "p1"}, "")
	if probs, err := CheckPart(context.Background(), browser, own, CheckOptions{Scene: &in.Scene}); err != nil || len(probs) > 0 {
		t.Fatalf("%v %v", err, probs)
	}
	if frames, err := PreviewFramesIn(context.Background(), browser, own, 4, CheckOptions{Scene: &in.Scene}); err != nil || len(frames) != 4 {
		t.Fatalf("%v", err)
	}
}

func writeUserPart(t *testing.T, dir, name, manifest string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, name, "manifest.json"), []byte(manifest), 0o644)
	_ = os.WriteFile(filepath.Join(dir, name, "part.html"), []byte(movingFragment), 0o644)
}

func sceneManifestFor(name, piece, sp string) string {
	return `{"name":"` + name + `","description":"a scene","version":1,"levels":["simple"],"verse":"own","scene":{"piece":"` + piece + `","script_piece":"` + sp + `"},"params":[]}`
}

func TestSceneStayOutOfTheCatalogListUntilAsked(t *testing.T) {
	user := t.TempDir()
	writeUserPart(t, user, "scene-aaaa-1", sceneManifestFor("scene-aaaa-1", "api-mm-a", "p1"))
	writeUserPart(t, user, "scene-bbbb-1", sceneManifestFor("scene-bbbb-1", "api-mm-b", "p1"))
	writeUserPart(t, user, "my-part", `{"name":"my-part","description":"x","version":1,"levels":["simple"],"params":[]}`)
	cat := Catalog{Builtin: BuiltinParts(), UserDir: user}
	names := func(ps []Part) string {
		var n []string
		for _, p := range ps {
			n = append(n, p.Name)
		}
		return strings.Join(n, ",")
	}
	if got := names(cat.List()); strings.Contains(got, "scene-") || !strings.Contains(got, "my-part") {
		t.Fatalf("%s", got)
	}
	if got := names(cat.ListScenes("")); got != "scene-aaaa-1,scene-bbbb-1" {
		t.Fatalf("%s", got)
	}
	if got := names(cat.ListScenes("api-mm-b")); got != "scene-bbbb-1" {
		t.Fatalf("%s", got)
	}
	if p, ok := cat.Get("scene-aaaa-1"); !ok || p.Scene == nil || p.Scene.ScriptPiece != "p1" || !p.OwnsVerse() {
		t.Fatalf("a scene is found by name and read back with its fields: %+v", p)
	}
	if n := cat.DeleteScenes("api-mm-a"); n != 1 {
		t.Fatalf("%d", n)
	}
	if _, ok := cat.Get("scene-aaaa-1"); ok {
		t.Fatal("deleted with its piece")
	}
	if _, ok := cat.Get("scene-bbbb-1"); !ok {
		t.Fatal("another piece's scene stays")
	}
}

func TestAScriptCannotUseAnotherPiecesSceneOrGiveAnExtraTheVerse(t *testing.T) {
	user := t.TempDir()
	writeUserPart(t, user, "scene-aaaa-1", sceneManifestFor("scene-aaaa-1", "api-mm-a", "p1"))
	cat := Catalog{Builtin: BuiltinParts(), UserDir: user}
	s := Script{Version: ScriptVersion, Pieces: []Piece{{ID: "p1", Start: 0, End: 10, Part: "scene-aaaa-1"}, {ID: "p2", Start: 10, End: 20, Part: "counter", With: []PartRef{{Part: "scene-aaaa-1"}}}}}
	if err := s.CheckSceneParts(cat, "api-mm-a"); err != nil {
		t.Fatalf("its own piece may use it (as main or extra): %v", err)
	}
	err := s.CheckSceneParts(cat, "api-mm-b")
	if err == nil || !strings.Contains(err.Error(), "scene-aaaa-1") || !strings.Contains(err.Error(), "api-mm-a") || !strings.Contains(err.Error(), "api-mm-b") {
		t.Fatalf("%v", err)
	}
	m := songmap.Map{Duration: 20}
	err = s.Validate(cat, m, Fallback("x", nil))
	if err == nil || !strings.Contains(err.Error(), "draws the lyric") {
		t.Fatalf("an extra cannot own the verse: %v", err)
	}
}

func TestADraftOfASceneIsWrittenFromThePieceAndKeepsItsScene(t *testing.T) {
	cat := Catalog{Builtin: BuiltinParts()}
	w := &scriptedWriter{answers: []string{answerOf(`{"name":"whatever","description":"the door that closes","version":1,"levels":["simple"],"verse":"own","params":[]}`, ownVerseFragment)}}
	e := draftEngine(t, w, nil)
	spec := pieceSpec(t, e.Cat)
	_ = cat
	var asked []SceneRef
	e.SceneInput = func(ref SceneRef, brief string) (SceneDraftInput, error) {
		asked = append(asked, ref)
		return BuildSceneInput(spec, ref, brief)
	}
	d, err := e.Store.Create("(scene)", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	d.Scene, d.Brief = &SceneRef{Piece: "api-mm-20261113-081046-a1b2c3", ScriptPiece: "p1"}, "a door closing"
	_ = e.Store.Save(d)
	e.Run(context.Background(), d.ID)
	got, _ := e.Store.Get(d.ID)
	if got.State != DraftReady || got.Name != "scene-a1b2c3-1" || len(got.Preview) != DraftPreviewFrames || got.Scene == nil || got.Scene.ScriptPiece != "p1" {
		t.Fatalf("%+v", got)
	}
	var m Manifest
	if err := json.Unmarshal([]byte(got.Manifest), &m); err != nil || m.Name != "scene-a1b2c3-1" || m.Scene == nil || m.Scene.Piece != "api-mm-20261113-081046-a1b2c3" || !m.OwnsVerse() {
		t.Fatalf("the program decides the name and the scene: %v %s", err, got.Manifest)
	}
	if len(w.asked) != 1 || !strings.Contains(w.asked[0], "Greg@2.00") || !strings.Contains(w.asked[0], "a door closing") || !strings.Contains(w.asked[0], "billing@3.70 (keyword)") {
		t.Fatalf("%v", w.asked)
	}
	// approve: the part exists with its scene field, and a second scene gets the next number
	part, probs, err := e.Approve(context.Background(), d.ID)
	if err != nil || len(probs) > 0 || part.Scene == nil || part.Name != "scene-a1b2c3-1" {
		t.Fatalf("%v %v %+v", err, probs, part)
	}
	d2, _ := e.Store.Create("(scene)", "", nil)
	d2.Scene = &SceneRef{Piece: "api-mm-20261113-081046-a1b2c3", ScriptPiece: "p2"}
	_ = e.Store.Save(d2)
	e.Run(context.Background(), d2.ID)
	if got2, _ := e.Store.Get(d2.ID); got2.Name != "scene-a1b2c3-2" || got2.State != DraftReady {
		t.Fatalf("%+v", got2)
	}
	// an unknown script piece fails the draft before the writer is asked
	d3, _ := e.Store.Create("(scene)", "", nil)
	d3.Scene = &SceneRef{Piece: "x", ScriptPiece: "nope"}
	_ = e.Store.Save(d3)
	before := len(w.asked)
	e.Run(context.Background(), d3.ID)
	if got3, _ := e.Store.Get(d3.ID); got3.State != DraftFailed || !strings.Contains(got3.Error, "nope") || len(w.asked) != before {
		t.Fatalf("%+v", got3)
	}
	// editing keeps the name and the scene
	edited, probs, err := e.Edit(context.Background(), d2.ID, `{"name":"other","description":"x","version":1,"levels":["simple"],"verse":"own","params":[]}`, ownVerseFragment)
	if err != nil || len(probs) > 0 || edited.Name != "scene-a1b2c3-2" || !strings.Contains(edited.Manifest, `"script_piece": "p2"`) {
		t.Fatalf("%v %v %+v", err, probs, edited)
	}
}

func TestTheWritersAreToldAboutTheWordAndTheLineOfTheBeat(t *testing.T) {
	for name, system := range map[string]string{"scene": SceneSystem(), "part": DraftSystem()} {
		if !strings.Contains(system, "beat.word") || !strings.Contains(system, "beat.line") {
			t.Errorf("the %s writer must be told that the beat carries beat.word and beat.line", name)
		}
	}
}

// The writer of a scene is told what the scenes around it and the scenes already approved draw, so that a metaphor is not repeated.
func TestTheSceneInputListsTheMetaphorsAlreadyUsed(t *testing.T) {
	user := t.TempDir()
	sc := func(name, sp, desc string) {
		writeUserPart(t, user, name, `{"name":"`+name+`","description":"`+desc+`","version":1,"levels":["simple"],"verse":"own","scene":{"piece":"api-mm-x","script_piece":"`+sp+`"},"params":[]}`)
	}
	sc("scene-x-1", "p1", "a door that closes slowly on a bright room")
	sc("scene-x-2", "p3", "a road that bends toward a red sun")
	writeUserPart(t, user, "scene-y-1", `{"name":"scene-y-1","description":"another piece's scene","version":1,"levels":["simple"],"verse":"own","scene":{"piece":"api-mm-y","script_piece":"p1"},"params":[]}`)
	cat := Catalog{Builtin: BuiltinParts(), UserDir: user}
	spec := pieceSpec(t, cat)
	spec.Script.Pieces[0].Part, spec.Script.Pieces[2].Part = "scene-x-1", "scene-x-2"

	in, err := BuildSceneInput(spec, SceneRef{Piece: "api-mm-x", ScriptPiece: "p2"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Neighbours) != 2 || in.Neighbours[0].Index != 1 || in.Neighbours[0].Part != "scene-x-1" || in.Neighbours[0].Description != "a door that closes slowly on a bright room" {
		t.Fatalf("neighbours: %+v", in.Neighbours)
	}
	if len(in.Used) != 2 || in.Used[0] != "a door that closes slowly on a bright room" || in.Used[1] != "a road that bends toward a red sun" {
		t.Fatalf("used: %q (another piece's scene is not in it)", in.Used)
	}
	msg := SceneUser(in, cat, "")
	for _, want := range []string{"Already used in this video (do not repeat any of these metaphors)", "a road that bends toward a red sun", "piece 1 uses \"scene-x-1\": a door that closes"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the writer's message lacks %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "another piece's scene") {
		t.Error("a scene of another piece is not listed")
	}
	// Writing p3 again: its own previous scene is what is replaced, so it is not listed.
	again, _ := BuildSceneInput(spec, SceneRef{Piece: "api-mm-x", ScriptPiece: "p3"}, "")
	if len(again.Used) != 1 || again.Used[0] != "a door that closes slowly on a bright room" {
		t.Fatalf("used when writing p3 again: %q", again.Used)
	}
	// With nothing approved the section is absent.
	none, _ := BuildSceneInput(pieceSpec(t, Catalog{Builtin: BuiltinParts()}), SceneRef{Piece: "api-mm-x", ScriptPiece: "p2"}, "")
	if strings.Contains(SceneUser(none, cat, ""), "Already used") {
		t.Error("nothing is used yet")
	}
}

// The page's own word hit overflowing, with a scene that never touches it, is the server's fault: the draft is ready at the first attempt
// and says so in its problems, instead of being refused and written again.
func TestAHouseHitThatOverflowsDoesNotCostTheSceneAnAttempt(t *testing.T) {
	w := &scriptedWriter{answers: []string{answerOf(`{"name":"whatever","description":"the door that closes","version":1,"levels":["simple"],"verse":"own","params":[]}`, ownVerseFragment)}}
	e := draftEngine(t, w, nil)
	spec := pieceSpec(t, e.Cat)
	spec.Design.Theme = &Theme{CSS: `#hit{font-size:900px !important}`}
	spec.Script.Pieces[0].Hit = &ScriptHit{Word: "billing", At: 3.7, Length: 1.5}
	e.SceneInput = func(ref SceneRef, brief string) (SceneDraftInput, error) { return BuildSceneInput(spec, ref, brief) }
	d, err := e.Store.Create("(scene)", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	d.Scene = &SceneRef{Piece: "api-mm-20261113-081046-a1b2c3", ScriptPiece: "p1"}
	_ = e.Store.Save(d)
	e.Run(context.Background(), d.ID)
	got, _ := e.Store.Get(d.ID)
	if got.State != DraftReady || got.Attempts != 1 || len(got.Problems) == 0 {
		t.Fatalf("%+v", got)
	}
	for _, p := range got.Problems {
		if !strings.HasPrefix(p, "server: ") {
			t.Errorf("a problem of the page's own hit is the server's: %q", p)
		}
	}
}

func TestTheSceneWriterIsToldTheCraftAndTheWorldOfTheVideo(t *testing.T) {
	cat := Catalog{Builtin: BuiltinParts()}
	for _, want := range []string{"0.5 s plus 0.3 s for each word", "dead still", "40 to 50 % shorter", "30 to 50 % done", "expo.out", "power2.inOut", "power3.in", "40 to 190 ms early", "60 to 90 % of the width", "2.5 to 4 %",
		"read as generated", "a breathing, floating or pulsing loop", "a waveform or spectrum bar as the main picture", "more than 6 seconds", "groups of words", "belong to one film", `add "world"`} {
		if !strings.Contains(SceneSystem(), want) {
			t.Errorf("the writer's instruction lacks %q", want)
		}
	}
	if !strings.Contains(SceneSystem(), "drawn from the world and the device of the video") || !strings.Contains(reviewSystem(), "does it belong to the world and the device of the video") {
		t.Error("the metaphor criterion asks for the world and the device, for the writer and the reviewer alike")
	}

	spec := pieceSpec(t, cat)
	spec.World, spec.Device, spec.Hook = "a night road, headlights and dust", "one object across the song", "a pair of headlights cut the dark"
	first, _ := BuildSceneInput(spec, SceneRef{Piece: "api-mm-x", ScriptPiece: "p1"}, "")
	second, _ := BuildSceneInput(spec, SceneRef{Piece: "api-mm-x", ScriptPiece: "p2"}, "")
	if first.World != spec.World || first.Device != spec.Device || first.Hook != spec.Hook || second.World != spec.World || second.Device != spec.Device || second.Hook != "" {
		t.Fatalf("world and device reach every scene, the hook only the first: %+v / %+v", first, second)
	}
	u1, u2 := SceneUser(first, cat, ""), SceneUser(second, cat, "")
	for _, want := range []string{"The world of the video (where it happens and what it looks like; every metaphor belongs to it): a night road, headlights and dust", "The device that carries the video (the metaphors serve it): one object across the song", "the first two seconds of the video show: a pair of headlights cut the dark"} {
		if !strings.Contains(u1, want) {
			t.Errorf("the first scene's message lacks %q", want)
		}
	}
	if strings.Contains(u2, "first two seconds") || !strings.Contains(u2, "a night road") {
		t.Errorf("the second scene reads the world but not the hook:\n%s", u2)
	}
	spec.World = ""
	none, _ := BuildSceneInput(spec, SceneRef{Piece: "api-mm-x", ScriptPiece: "p1"}, "")
	if u := SceneUser(none, cat, ""); !strings.Contains(u, "No world was chosen for this video: choose one") || !strings.Contains(u, `"world" in the manifest`) {
		t.Errorf("a piece with no world is told to choose one and keep it:\n%s", u)
	}
}

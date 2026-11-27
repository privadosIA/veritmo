package musicmotion

import (
	"context"
	"strings"
	"testing"
)

// fillingFragment paints the whole frame and still moves on the beat, so it passes every other check of a part.
const fillingFragment = `<div id="wall" style="position:absolute;left:0;top:0;width:100%;height:100%;background:#1b2a3a"></div><div id="dot" style="position:absolute;left:20px;top:20px;width:50px;height:50px;background:#2bff88"></div><script>window.__partSeek=function(t,b){document.getElementById('dot').style.left=(20+b.position*100)+'px';};</script>`

func fillingPart(t *testing.T, claim bool) Part {
	t.Helper()
	m := testManifest
	if claim {
		m = strings.Replace(m, `"version":1,`, `"version":1,"over_video":true,`, 1)
	}
	p, probs := ParsePart([]byte(m), fillingFragment)
	if len(probs) != 0 {
		t.Fatalf("%v", probs)
	}
	return p
}

func TestAPartThatFillsTheFrameFailsTheCheckOverAVideo(t *testing.T) {
	browser := browserOrSkip(t)
	p := fillingPart(t, false)
	if probs, err := CheckPart(context.Background(), browser, p, CheckOptions{}); err != nil || len(probs) != 0 {
		t.Fatalf("it is a good part on its own: %v %v", probs, err)
	}
	probs, err := CheckPart(context.Background(), browser, p, CheckOptions{OverVideo: true})
	if err != nil || len(probs) != 1 || !strings.Contains(probs[0], "hides the video") {
		t.Fatalf("%v %v", probs, err)
	}
}

func TestAManifestThatClaimsOverVideoIsMeasured(t *testing.T) {
	browser := browserOrSkip(t)
	probs, err := CheckPart(context.Background(), browser, fillingPart(t, true), CheckOptions{})
	if err != nil || len(probs) != 1 || !strings.Contains(probs[0], "manifest says over_video") {
		t.Fatalf("%v %v", probs, err)
	}
}

func TestAPartThatLeavesTheVideoVisiblePassesTheCheck(t *testing.T) {
	browser := browserOrSkip(t)
	p, probs := ParsePart([]byte(testManifest), movingFragment)
	if len(probs) != 0 {
		t.Fatal(probs)
	}
	if probs, err := CheckPart(context.Background(), browser, p, CheckOptions{OverVideo: true}); err != nil || len(probs) != 0 {
		t.Fatalf("%v %v", probs, err)
	}
}

func TestAnApprovedDraftIsMarkedOverVideoByWhatWasMeasured(t *testing.T) {
	for _, c := range []struct {
		name, fragment string
		want           bool
	}{{"leaves the video", movingFragment, true}, {"fills the frame", fillingFragment, false}} {
		t.Run(c.name, func(t *testing.T) {
			e := draftEngine(t, &scriptedWriter{answers: []string{answerOf(testManifest, c.fragment)}}, nil)
			d, _ := e.Store.Create("a dot", "", nil)
			e.Run(context.Background(), d.ID)
			part, probs, err := e.Approve(context.Background(), d.ID)
			if err != nil || len(probs) != 0 {
				t.Fatalf("%v %v", err, probs)
			}
			got, ok := e.Cat.Get("probe")
			if !ok || got.OverVideo != c.want || part.OverVideo != c.want {
				t.Fatalf("over_video: part %v, catalog %v (found %v), want %v", part.OverVideo, got.OverVideo, ok, c.want)
			}
		})
	}
}

func TestWithOverVideoKeepsTheRestOfTheManifest(t *testing.T) {
	raw, err := WithOverVideo([]byte(testManifest), true)
	if err != nil {
		t.Fatal(err)
	}
	p, err2 := parseManifest(raw, "probe")
	if err2 != nil || !p.OverVideo || p.Name != "probe" || p.Description != "a test part" || len(p.Levels) != 1 {
		t.Fatalf("%v %+v", err2, p)
	}
	raw, _ = WithOverVideo(raw, false)
	if strings.Contains(string(raw), "over_video") {
		t.Fatalf("not cleared: %s", raw)
	}
}

// The same verse-drawing scene as ownVerseFragment, with a wall painted over the whole frame behind it.
const wallScene = `<style>#wall{position:absolute;left:0;top:0;width:100vw;height:100vh;background:var(--dark)}#w{position:absolute;left:10vw;right:10vw;top:30vh;font-size:14vh;color:var(--accent)}#b{position:absolute;left:8vw;top:10vh;width:70vw;height:80vh;border:1vh solid var(--fg)}</style><div id="wall"></div><div id="w"></div><div id="b"></div>
<script>(function(){var L=window.__scene.lines, w=document.getElementById('w'), b=document.getElementById('b');
window.__partSeek=function(t,beat){var s='';L.forEach(function(l){(l.words||[]).forEach(function(x){if(t>=x.s)s+=x.t+' ';});});w.textContent=s;b.style.transform='translateX('+(Math.max(0,beat.number)*30%600)+'px)';};})();</script>`

func TestTheWriterOfASceneOverAVideoIsToldSoAndItsInputKeepsItsOwnBackground(t *testing.T) {
	cat := Catalog{Builtin: BuiltinParts()}
	spec := pieceSpec(t, cat)
	spec.Script.Pieces[0].Part = "blank"
	spec.Script.Pieces[0].Background = &PieceBackground{Clip: "a.mp4"}
	over, err := BuildSceneInput(spec, SceneRef{Piece: "api-mm-x", ScriptPiece: "p1"}, "")
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := BuildSceneInput(spec, SceneRef{Piece: "api-mm-x", ScriptPiece: "p2"}, "")
	if !over.OverVideo || plain.OverVideo {
		t.Fatalf("OverVideo: %v %v", over.OverVideo, plain.OverVideo)
	}
	if over.Scene.Background != nil {
		t.Error("the scene is checked and previewed on the design's own background")
	}
	if msg := SceneUser(over, cat, ""); !strings.Contains(msg, "A video plays behind this scene") || !strings.Contains(msg, "paint no background of its own") {
		t.Errorf("the writer is not told:\n%s", msg)
	}
	if strings.Contains(SceneUser(plain, cat, ""), "A video plays behind") {
		t.Error("a piece with no background is told nothing of the kind")
	}
}

func TestASceneThatHidesTheVideoIsRefusedWithTheReasonAndWrittenAgain(t *testing.T) {
	w := &scriptedWriter{answers: []string{
		answerOf(`{"name":"whatever","description":"the door that closes","version":1,"levels":["simple"],"verse":"own","params":[]}`, wallScene),
		answerOf(`{"name":"whatever","description":"the door that closes","version":1,"levels":["simple"],"verse":"own","params":[]}`, ownVerseFragment)}}
	e := draftEngine(t, w, nil)
	spec := pieceSpec(t, e.Cat)
	spec.Script.Pieces[0].Part = "blank"
	spec.Script.Pieces[0].Background = &PieceBackground{Clip: "a.mp4"}
	e.SceneInput = func(ref SceneRef, brief string) (SceneDraftInput, error) { return BuildSceneInput(spec, ref, brief) }
	d, err := e.Store.Create("(scene)", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	d.Scene = &SceneRef{Piece: "api-mm-20261113-081046-a1b2c3", ScriptPiece: "p1"}
	_ = e.Store.Save(d)
	e.Run(context.Background(), d.ID)
	got, _ := e.Store.Get(d.ID)
	if got.State != DraftReady || got.Attempts != 2 {
		t.Fatalf("the first answer hides the video and the second does not: %+v", got)
	}
	if len(w.asked) != 2 || !strings.Contains(w.asked[1], "hides the video") {
		t.Fatalf("the writer is told why it was refused: %q", w.asked)
	}
	// the same wall on a piece with no video is not a problem for this check
	spec.Script.Pieces[0].Background = nil
	w2 := &scriptedWriter{answers: []string{answerOf(`{"name":"whatever","description":"the door that closes","version":1,"levels":["simple"],"verse":"own","params":[]}`, wallScene)}}
	e2 := draftEngine(t, w2, nil)
	e2.SceneInput = e.SceneInput
	d2, _ := e2.Store.Create("(scene)", "", nil)
	d2.Scene = d.Scene
	_ = e2.Store.Save(d2)
	e2.Run(context.Background(), d2.ID)
	if got2, _ := e2.Store.Get(d2.ID); strings.Contains(strings.Join(got2.Problems, " "), "hides the video") {
		t.Fatalf("a scene over no video is not measured: %+v", got2.Problems)
	}
}

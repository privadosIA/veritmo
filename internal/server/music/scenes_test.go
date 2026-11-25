package music

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"veritmo/internal/music/musicmotion"
	"veritmo/internal/video/motion"
)

// A scene that owns the verse: it draws the words of window.__scene.lines at their times and moves with the beat.
const sceneManifest = `{"name":"whatever","description":"the door that closes","version":1,"levels":["simple"],"verse":"own","params":[]}`
const sceneFragment = `<style>#w{position:absolute;left:10vw;right:10vw;top:30vh;font-size:14vh;color:var(--accent)}#b{position:absolute;left:8vw;top:10vh;width:70vw;height:80vh;border:1vh solid var(--fg)}</style><div id="w"></div><div id="b"></div>
<script>(function(){var L=window.__scene.lines, w=document.getElementById('w'), b=document.getElementById('b');
window.__partSeek=function(t,beat){var s='';L.forEach(function(l){(l.words||[]).forEach(function(x){if(t>=x.s)s+=x.t+' ';});});w.textContent=s;b.style.transform='translateX('+(Math.max(0,beat.number)*30%600)+'px)';};})();</script>`

// sceneWriter answers every request with the same scene and records what it was asked; gate, when set, holds each answer.
type sceneWriter struct {
	mu    sync.Mutex
	asked []string
	gate  chan struct{}
	// manifest, when set, is the manifest.json of every answer (the default is sceneManifest).
	manifest string
	// bad, when set, makes the answer a scene with a colour of its own (refused by the checks) for the user messages it says yes to.
	bad func(user string) bool
}

func (w *sceneWriter) Write(ctx context.Context, system, user string, max int) (string, error) {
	w.mu.Lock()
	w.asked = append(w.asked, user)
	g := w.gate
	w.mu.Unlock()
	if g != nil {
		select {
		case <-g:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if w.bad != nil && w.bad(user) {
		return draftAnswer(sceneManifest, sceneFragment+"<style>#w{color:crimson}</style>"), nil
	}
	if w.manifest != "" {
		return draftAnswer(w.manifest, sceneFragment), nil
	}
	return draftAnswer(sceneManifest, sceneFragment), nil
}

func (w *sceneWriter) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.asked)
}

// sceneServer is a music-motion server with a real browser, a piece and a writer of scenes (skipped without Chrome).
func sceneServer(t *testing.T) (*testServer, *sceneWriter, PieceView) {
	t.Helper()
	b, err := motion.Find("")
	if err != nil {
		t.Skip("no Chrome or Chromium on this machine")
	}
	s := mmServer(t)
	w := &sceneWriter{}
	s.motionBrowser = func() (motion.Browser, error) { return b, nil }
	s.motionWriter = func(context.Context) (motion.Writer, motion.ModelInfo, error) { return w, motion.ModelInfo{}, nil }
	return s, w, newPiece(t, s, nil)
}

func postScene(t *testing.T, s *testServer, scene map[string]any) (int, musicmotion.Draft, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"scene": scene})
	return postDraft(t, s, string(body))
}

func waitDraft(t *testing.T, s *testServer, id string) musicmotion.Draft {
	t.Helper()
	for i := 0; i < 1200; i++ {
		if _, d := getDraft(t, s, id); d.State != musicmotion.DraftWriting {
			return d
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("the draft never finished")
	return musicmotion.Draft{}
}

func waitBatch(t *testing.T, s *testServer, id string, states ...string) SceneBatch {
	t.Helper()
	for i := 0; i < 2400; i++ {
		if b := pieceOf(t, s, id).Scenes; b != nil {
			for _, st := range states {
				if b.State == st {
					return *b
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("the batch never reached %v: %+v", states, pieceOf(t, s, id).Scenes)
	return SceneBatch{}
}

func catalogNames(t *testing.T, s *testServer, query string) []string {
	t.Helper()
	var c PieceCatalog
	if err := json.Unmarshal([]byte(get(t, s, "/api/music-motion/catalog"+query)), &c); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, p := range c.Parts {
		out = append(out, p.Name)
	}
	return out
}

func has(names []string, prefix string) bool {
	for _, n := range names {
		if strings.HasPrefix(n, prefix) {
			return true
		}
	}
	return false
}

func TestScenes_ADraftAskedForASceneIsWrittenFromThePieceAndHiddenFromTheCatalog(t *testing.T) {
	s, w, v := sceneServer(t)
	sp := v.Script.Pieces[1].ID
	code, d, body := postScene(t, s, map[string]any{"piece": v.ID, "script_piece": sp, "brief": "a door closing"})
	if code != http.StatusAccepted || d.Scene == nil || d.Scene.Piece != v.ID || d.Brief != "a door closing" {
		t.Fatalf("%d %s", code, body)
	}
	got := waitDraft(t, s, d.ID)
	if got.State != musicmotion.DraftReady || !strings.HasPrefix(got.Name, "scene-") || len(got.Name) > 32 || len(got.Preview) != musicmotion.DraftPreviewFrames {
		t.Fatalf("%+v", got)
	}
	if len(w.asked) != 1 || !strings.Contains(w.asked[0], "script piece 2 of") || !strings.Contains(w.asked[0], sp) || !strings.Contains(w.asked[0], "a door closing") {
		t.Fatalf("the writer is given the piece's context: %s", w.asked)
	}
	if strings.Contains(body, "lyric") {
		t.Error("the client sent no lyric")
	}
	// approval: a part of the user's with the scene, not in the default catalog
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/part-drafts/"+d.ID+"/approve", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if names := catalogNames(t, s, ""); has(names, "scene-") {
		t.Fatalf("a scene is not in the default list: %v", names)
	}
	if names := catalogNames(t, s, "?include=scene"); !has(names, "scene-") {
		t.Fatalf("include=scene lists it: %v", names)
	}
	if names := catalogNames(t, s, "?piece="+v.ID); !has(names, "scene-") {
		t.Fatalf("piece= lists its scenes: %v", names)
	}
	if names := catalogNames(t, s, "?piece=api-mm-other"); has(names, "scene-") {
		t.Fatalf("another piece's scenes are not listed: %v", names)
	}
	var part UserPart
	if err := json.Unmarshal([]byte(get(t, s, "/api/music-motion/parts/"+got.Name)), &part); err != nil || part.Manifest.Scene == nil || part.Manifest.Scene.ScriptPiece != sp || !part.Manifest.OwnsVerse() {
		t.Fatalf("it is read back like any part: %v %+v", err, part.Manifest)
	}
	// the script of its own piece may use it; the script of another piece may not
	script := *v.Script
	script.Pieces = append([]musicmotion.Piece(nil), script.Pieces...)
	script.Pieces[0].Part = got.Name
	if rec := doJSON(t, s.Handler(), http.MethodPut, "/api/music-motion/pieces/"+v.ID+"/script", script); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	other := newPiece(t, s, nil)
	oscript := *other.Script
	oscript.Pieces = append([]musicmotion.Piece(nil), oscript.Pieces...)
	oscript.Pieces[0].Part = got.Name
	rec = doJSON(t, s.Handler(), http.MethodPut, "/api/music-motion/pieces/"+other.ID+"/script", oscript)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), got.Name) || !strings.Contains(rec.Body.String(), v.ID) || !strings.Contains(rec.Body.String(), other.ID) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	// deleting the piece deletes its scenes
	if rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/music-motion/pieces/"+v.ID, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if names := catalogNames(t, s, "?include=scene"); has(names, "scene-") {
		t.Fatalf("the scenes went with the piece: %v", names)
	}
}

func TestScenes_WhatCannotBeAskedIsRefusedBeforeAnythingIsCreated(t *testing.T) {
	s, w, v := sceneServer(t)
	sp := v.Script.Pieces[0].ID
	for name, c := range map[string]struct {
		scene map[string]any
		code  int
	}{
		"unknown piece":        {map[string]any{"piece": "api-mm-nope", "script_piece": sp}, http.StatusNotFound},
		"unknown script piece": {map[string]any{"piece": v.ID, "script_piece": "zzz"}, http.StatusNotFound},
		"a long brief":         {map[string]any{"piece": v.ID, "script_piece": sp, "brief": strings.Repeat("a", 501)}, http.StatusUnprocessableEntity},
	} {
		if code, _, body := postScene(t, s, c.scene); code != c.code {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	if code, _, _ := postDraft(t, s, `{"scene":{"piece":"`+v.ID+`","script_piece":"`+sp+`"},"manifest":"{}","fragment":"x"}`); code != http.StatusUnprocessableEntity {
		t.Errorf("a scene with files is refused: %d", code)
	}
	if code, _, _ := postDraft(t, s, `{}`); code != http.StatusUnprocessableEntity {
		t.Errorf("without a scene the request is still required: %d", code)
	}
	if w.count() != 0 {
		t.Error("the writer was not asked")
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/music-motion/part-drafts", nil); strings.Contains(rec.Body.String(), `"scene"`) {
		t.Errorf("no draft was created: %s", rec.Body)
	}
}

func TestScenes_ABatchWritesOneDraftPerScriptPieceAndApprovesThemTogether(t *testing.T) {
	s, w, v := sceneServer(t)
	ids := []string{v.Script.Pieces[0].ID, v.Script.Pieces[1].ID}
	base := "/api/music-motion/pieces/" + v.ID
	if rec := doJSON(t, s.Handler(), http.MethodPost, base+"/scenes", map[string]any{"script_pieces": []string{"nope"}}); rec.Code != http.StatusNotFound {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodPost, base+"/scenes", map[string]any{"brief_mode": "poetic"}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodPost, base+"/scenes/approve", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("no batch, nothing to approve: %d", rec.Code)
	}
	rec := doJSON(t, s.Handler(), http.MethodPost, base+"/scenes", map[string]any{"script_pieces": ids, "brief_mode": "literal"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	b := waitBatch(t, s, v.ID, SceneBatchDone, SceneBatchFailed)
	if b.State != SceneBatchDone || b.Done != 2 || b.Total != 2 || len(b.Drafts) != 2 || b.BriefMode != "literal" {
		t.Fatalf("%+v", b)
	}
	for i, d := range b.Drafts {
		if d.ScriptPiece != ids[i] || d.State != musicmotion.DraftReady || d.DraftID == "" {
			t.Fatalf("%+v", d)
		}
	}
	if w.count() != 2 || !strings.Contains(w.asked[0], "literally say") {
		t.Fatalf("the brief mode reaches the writer: %v", w.asked)
	}
	// nothing is approved until asked: the script keeps its parts and the catalog has no scene
	if before := pieceOf(t, s, v.ID).Script.Pieces[0].Part; strings.HasPrefix(before, "scene-") {
		t.Fatalf("not approved yet: %s", before)
	}
	// the script changed since the batch started: refused, naming the script piece
	edited := *pieceOf(t, s, v.ID).Script
	edited.Pieces = append([]musicmotion.Piece(nil), edited.Pieces...)
	edited.Pieces[1].Section = "coda"
	if rec := doJSON(t, s.Handler(), http.MethodPut, base+"/script", edited); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec = doJSON(t, s.Handler(), http.MethodPost, base+"/scenes/approve", nil)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), ids[1]) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	edited.Pieces[1].Section = v.Script.Pieces[1].Section
	_ = doJSON(t, s.Handler(), http.MethodPut, base+"/script", edited)
	rec = doJSON(t, s.Handler(), http.MethodPost, base+"/scenes/approve", nil)
	var ans ScenesApproveAnswer
	_ = json.Unmarshal(rec.Body.Bytes(), &ans)
	if rec.Code != http.StatusOK || len(ans.Approved) != 2 || len(ans.NotApproved) != 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	after := pieceOf(t, s, v.ID)
	for i, a := range ans.Approved {
		if a.ScriptPiece != ids[i] || after.Script.Pieces[i].Part != a.Part || !strings.HasPrefix(a.Part, "scene-") || len(after.Script.Pieces[i].Params) != 0 {
			t.Fatalf("%+v %+v", a, after.Script.Pieces[i])
		}
	}
	if after.Scenes == nil || after.Scenes.Drafts[0].State != "approved" {
		t.Fatalf("%+v", after.Scenes)
	}
}

func TestScenes_ABatchBlocksOtherWorkAndStopsAfterTheDraftItIsWriting(t *testing.T) {
	s, w, v := sceneServer(t)
	w.gate = make(chan struct{})
	ids := []string{v.Script.Pieces[0].ID, v.Script.Pieces[1].ID}
	base := "/api/music-motion/pieces/" + v.ID
	if rec := doJSON(t, s.Handler(), http.MethodPost, base+"/scenes", map[string]any{"script_pieces": ids}); rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for w.count() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	for _, c := range []struct{ method, path string }{{http.MethodPost, base + "/scenes"}, {http.MethodPost, base + "/propose"}, {http.MethodPost, base + "/scenes/approve"}, {http.MethodPut, base + "/script"}} {
		var body any = map[string]any{"what": "design"}
		if c.method == http.MethodPut {
			body = *v.Script
		}
		if rec := doJSON(t, s.Handler(), c.method, c.path, body); rec.Code != http.StatusConflict {
			t.Errorf("%s %s while the batch writes: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
	if rec := doJSON(t, s.Handler(), http.MethodDelete, base+"/scenes", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	w.mu.Lock()
	g := w.gate
	w.gate = nil
	w.mu.Unlock()
	close(g) // the draft being written finishes; the second is never started
	b := waitBatch(t, s, v.ID, SceneBatchCancelled, SceneBatchDone)
	if b.State != SceneBatchCancelled || b.Done != 1 || w.count() != 1 || b.Drafts[1].State != "pending" {
		t.Fatalf("%+v asked %d", b, w.count())
	}
	if rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/music-motion/pieces/api-mm-none/scenes", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("%d", rec.Code)
	}
}

func TestScenes_TheCatalogSaysThereAreScenes(t *testing.T) {
	has := false
	for _, f := range musicmotion.Features {
		has = has || f == "scene"
	}
	if !has {
		t.Fatal(musicmotion.Features)
	}
}

func TestTheme_ADesignCarriesItAndTheCatalogSaysSo(t *testing.T) {
	s := mmServer(t)
	v := newPiece(t, s, nil)
	base := "/api/music-motion/pieces/" + v.ID
	d := v.Design.Design
	d.Theme = &musicmotion.Theme{CSS: "@import url(https://example.com/x.css);"}
	rec := doJSON(t, s.Handler(), http.MethodPut, base+"/design", d)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "design.theme.css") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	d.Theme = &musicmotion.Theme{CSS: "#verse{font-size:9vh}", Assets: map[string]string{"mascot": `<svg viewBox="0 0 1 1"/>`}, HUD: &musicmotion.HUD{TL: "LGTM Inc."}}
	d.TransitionSeconds = 0.3
	d.Display = "bebas-neue"
	if rec := doJSON(t, s.Handler(), http.MethodPut, base+"/design", d); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	got := pieceOf(t, s, v.ID)
	if got.Design.Design.Theme == nil || got.Design.Design.Theme.HUD.TL != "LGTM Inc." || got.Design.Design.TransitionSeconds != 0.3 {
		t.Fatalf("%+v", got.Design.Design)
	}
	// the preview is built with the theme and the embedded font
	rec = doJSON(t, s.Handler(), http.MethodPost, base+"/preview", map[string]any{})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "data-theme") || !strings.Contains(rec.Body.String(), "data:font/woff2") {
		t.Fatalf("%d %.300s", rec.Code, rec.Body)
	}
	// a crossfade out of range in the script is refused
	script := *got.Script
	script.Pieces = append([]musicmotion.Piece(nil), script.Pieces...)
	bad := 3.0
	script.Pieces[1].TransitionSeconds = &bad
	if rec := doJSON(t, s.Handler(), http.MethodPut, base+"/script", script); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "transition_seconds") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var c PieceCatalog
	if err := json.Unmarshal([]byte(get(t, s, "/api/music-motion/catalog")), &c); err != nil {
		t.Fatal(err)
	}
	feat := map[string]bool{}
	for _, f := range c.Features {
		feat[f] = true
	}
	if !feat["theme"] || c.FontFiles["bebas-neue"].Licence != "OFL-1.1" || c.FontFiles["grotesk"].Family != "Inter" || c.FontStacks["inter"] == "" || len(c.Fonts) < 12 {
		t.Fatalf("%v %+v %d", c.Features, c.FontFiles["bebas-neue"], len(c.Fonts))
	}
	names := catalogNames(t, s, "")
	if !has(names, "hud-chrome") || !has(names, "grain") {
		t.Fatalf("%v", names)
	}
}

// An import creates the scenes of a dataset as drafts from text: the manifest keeps its scene as sent and a name of up to 32
// characters is valid.
func TestScenes_ADraftFromTextKeepsTheSceneItIsSent(t *testing.T) {
	s, _, v := sceneServer(t)
	name := "scene-" + v.ID[len(v.ID)-10:] + "-1"
	if len(name) > 32 {
		t.Fatal(name)
	}
	manifest := `{"name":"` + name + `","description":"imported","version":1,"levels":["simple"],"verse":"own","scene":{"piece":"` + v.ID + `","script_piece":"` + v.Script.Pieces[1].ID + `"},"params":[]}`
	body, _ := json.Marshal(map[string]any{"manifest": manifest, "fragment": sceneFragment})
	code, d, out := postDraft(t, s, string(body))
	if code != http.StatusOK || d.State != musicmotion.DraftReady || d.Name != name {
		t.Fatalf("%d %s", code, out)
	}
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/part-drafts/"+d.ID+"/approve", nil); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var part UserPart
	if err := json.Unmarshal([]byte(get(t, s, "/api/music-motion/parts/"+name)), &part); err != nil || part.Manifest.Scene == nil || part.Manifest.Scene.Piece != v.ID || part.Manifest.Scene.ScriptPiece != v.Script.Pieces[1].ID {
		t.Fatalf("%v %+v", err, part.Manifest.Scene)
	}
	if names := catalogNames(t, s, "?piece="+v.ID); !has(names, name) {
		t.Fatalf("%v", names)
	}
	script := *v.Script
	script.Pieces = append([]musicmotion.Piece(nil), script.Pieces...)
	script.Pieces[1].Part = name
	if rec := doJSON(t, s.Handler(), http.MethodPut, "/api/music-motion/pieces/"+v.ID+"/script", script); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// A script piece whose attempts all fail keeps the scene it had: the batch says so with the last problems, `only: "failed"` writes
// just those again, and the review names the scenes that are older than the batch.
func TestScenes_TheBatchSaysWhatKeptItsSceneAndOnlyFailedWritesThoseAgain(t *testing.T) {
	s, w, v := sceneServer(t)
	ids := []string{v.Script.Pieces[0].ID, v.Script.Pieces[1].ID}
	base := "/api/music-motion/pieces/" + v.ID
	w.bad = func(user string) bool { return strings.Contains(user, "("+ids[1]+")") }
	if rec := doJSON(t, s.Handler(), http.MethodPost, base+"/scenes", map[string]any{"only": "failed"}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("nothing kept an earlier scene yet: %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodPost, base+"/scenes", map[string]any{"script_pieces": ids}); rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	b := waitBatch(t, s, v.ID, SceneBatchDone, SceneBatchFailed)
	if b.Drafts[0].KeptPrevious || b.Drafts[0].State != musicmotion.DraftReady {
		t.Fatalf("the first is written: %+v", b.Drafts[0])
	}
	if d := b.Drafts[1]; !d.KeptPrevious || d.State != musicmotion.DraftFailed || len(d.Problems) == 0 || !strings.Contains(strings.Join(d.Problems, ";"), "crimson") {
		t.Fatalf("the second keeps its scene and says why: %+v", d)
	}
	rec := doJSON(t, s.Handler(), http.MethodPost, base+"/scenes/approve", nil)
	var ans ScenesApproveAnswer
	_ = json.Unmarshal(rec.Body.Bytes(), &ans)
	if rec.Code != http.StatusOK || len(ans.Approved) != 1 || len(ans.NotApproved) != 1 || !ans.NotApproved[0].KeptPrevious {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodPost, base+"/scenes", map[string]any{"only": "most"}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("only is failed or nothing: %d", rec.Code)
	}
	// only: failed writes the second one again and nothing else
	before := w.count()
	w.bad = nil
	if rec := doJSON(t, s.Handler(), http.MethodPost, base+"/scenes", map[string]any{"only": "failed"}); rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	b = waitBatch(t, s, v.ID, SceneBatchDone, SceneBatchFailed)
	if len(b.Drafts) != 1 || b.Drafts[0].ScriptPiece != ids[1] || b.Drafts[0].State != musicmotion.DraftReady || w.count() != before+1 {
		t.Fatalf("only the failed one is written again: %+v (writer asked %d times, was %d)", b.Drafts, w.count(), before)
	}
}

func TestScenes_ABatchOfSomePiecesRemembersTheOnesAnEarlierBatchLeftOld(t *testing.T) {
	s, w, v := sceneServer(t)
	ids := []string{v.Script.Pieces[0].ID, v.Script.Pieces[1].ID}
	base := "/api/music-motion/pieces/" + v.ID
	w.bad = func(user string) bool { return strings.Contains(user, "("+ids[1]+")") }
	_ = doJSON(t, s.Handler(), http.MethodPost, base+"/scenes", map[string]any{"script_pieces": ids})
	waitBatch(t, s, v.ID, SceneBatchDone, SceneBatchFailed)
	// a batch of the first piece alone: the second is still older than the batches, and the piece knows it
	if rec := doJSON(t, s.Handler(), http.MethodPost, base+"/scenes", map[string]any{"script_pieces": []string{ids[0]}}); rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	b := waitBatch(t, s, v.ID, SceneBatchDone, SceneBatchFailed)
	if len(b.Earlier) != 1 || b.Earlier[0].ScriptPiece != ids[1] || !b.Earlier[0].KeptPrevious {
		t.Fatalf("%+v", b)
	}
	if got := keptScenes(&b); len(got) != 1 || got[0].ScriptPiece != ids[1] {
		t.Fatalf("%+v", got)
	}
}

func TestScenes_TheWorldTheFirstSceneStatesIsKeptWithThePieceAndTheNextSceneReadsIt(t *testing.T) {
	s, w, v := sceneServer(t)
	w.manifest = `{"name":"whatever","description":"the door that closes","version":1,"levels":["simple"],"verse":"own","params":[],"world":"  a night road,  headlights and dust "}`
	ids := []string{v.Script.Pieces[0].ID, v.Script.Pieces[1].ID}
	base := "/api/music-motion/pieces/" + v.ID
	if got := pieceOf(t, s, v.ID); got.World != "" {
		t.Fatalf("a piece made without a plan has no world: %q", got.World)
	}
	if rec := doJSON(t, s.Handler(), http.MethodPost, base+"/scenes", map[string]any{"script_pieces": ids}); rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if b := waitBatch(t, s, v.ID, SceneBatchDone, SceneBatchFailed); b.State != SceneBatchDone {
		t.Fatalf("%+v", b)
	}
	if len(w.asked) != 2 || !strings.Contains(w.asked[0], "No world was chosen for this video") {
		t.Fatalf("the first scene is told to choose a world: %v", w.asked)
	}
	if !strings.Contains(w.asked[1], "every metaphor belongs to it): a night road, headlights and dust") || strings.Contains(w.asked[1], "No world was chosen") {
		t.Fatalf("the second scene carries the first's world:\n%s", w.asked[1])
	}
	if got := pieceOf(t, s, v.ID).World; got != "a night road, headlights and dust" {
		t.Fatalf("the piece keeps the world: %q", got)
	}
	// a piece made from a plan keeps its own world: a scene's does not replace it
	planned := newPiece(t, s, map[string]any{"world": "a desert town", "device": "a number lands"})
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces/"+planned.ID+"/scenes", map[string]any{"script_pieces": []string{planned.Script.Pieces[0].ID}}); rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	waitBatch(t, s, planned.ID, SceneBatchDone, SceneBatchFailed)
	if got := pieceOf(t, s, planned.ID).World; got != "a desert town" {
		t.Fatalf("a planned piece keeps its world: %q", got)
	}
	if !strings.Contains(w.asked[2], "a desert town") || !strings.Contains(w.asked[2], "a number lands") {
		t.Fatalf("world and device of the plan reach the writer:\n%s", w.asked[2])
	}
}

func TestScenes_ApprovingABatchIsAllOrNothing(t *testing.T) {
	s, _, v := sceneServer(t)
	ids := []string{v.Script.Pieces[0].ID, v.Script.Pieces[1].ID}
	base := "/api/music-motion/pieces/" + v.ID
	if rec := doJSON(t, s.Handler(), http.MethodPost, base+"/scenes", map[string]any{"script_pieces": ids}); rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if b := waitBatch(t, s, v.ID, SceneBatchDone, SceneBatchFailed); b.State != SceneBatchDone {
		t.Fatalf("%+v", b)
	}
	scriptBefore := pieceOf(t, s, v.ID).Script
	// the second write of the approval (the script) fails: the parts of the first write must not be there
	db := s.Stack.Store.DB
	if err := db.Callback().Create().Before("gorm:create").Register("test:fail-the-script", func(tx *gorm.DB) {
		if tx.Statement.Table == "mm_piece_documents" {
			_ = tx.AddError(errors.New("the disk is full"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, s.Handler(), http.MethodPost, base+"/scenes/approve", nil)
	_ = db.Callback().Create().Remove("test:fail-the-script")
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "disk is full") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if scenes := s.mmCatalog().ListScenes(v.ID); len(scenes) != 0 {
		t.Fatalf("no scene part may remain when the approval failed: %d", len(scenes))
	}
	after := pieceOf(t, s, v.ID)
	if after.Script.Pieces[0].Part != scriptBefore.Pieces[0].Part || (after.Scenes != nil && after.Scenes.Drafts[0].State == "approved") {
		t.Fatalf("the script and the batch stay as they were: %+v", after.Script.Pieces[0])
	}
	// and a second try, with the disk back, approves them
	if rec := doJSON(t, s.Handler(), http.MethodPost, base+"/scenes/approve", nil); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if scenes := s.mmCatalog().ListScenes(v.ID); len(scenes) != 2 {
		t.Fatalf("%d scene parts", len(scenes))
	}
}

func TestPieces_DeletingAPieceTakesItsRowsItsDocumentsAndItsScenesAndOnlyThose(t *testing.T) {
	s := mmServer(t)
	a, b := newPiece(t, s, nil), newPiece(t, s, nil)
	for _, p := range []string{a.ID, b.ID} {
		m := fmt.Sprintf(`{"name":"scene-%s-1","description":"a scene","version":1,"levels":["simple"],"params":[],"scene":{"piece":%q,"script_piece":"p1"}}`, p[len(p)-6:], p)
		if err := s.mmCatalog().CreatePart([]byte(m), draftMoving, nil); err != nil {
			t.Fatal(err)
		}
	}
	if rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/music-motion/pieces/"+a.ID, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if _, ok := s.pieces().get(a.ID); ok {
		t.Fatal("the row is gone")
	}
	if _, ok := s.pieces().doc(a.ID, musicmotion.DocScript); ok {
		t.Fatal("its documents are gone")
	}
	if len(s.mmCatalog().ListScenes(a.ID)) != 0 || len(s.mmCatalog().ListScenes(b.ID)) != 1 {
		t.Fatal("only the scenes of the deleted piece go")
	}
	if _, ok := s.pieces().get(b.ID); !ok {
		t.Fatal("the other piece stays")
	}
	if _, ok := s.pieces().doc(b.ID, musicmotion.DocScript); !ok {
		t.Fatal("and its documents")
	}
}

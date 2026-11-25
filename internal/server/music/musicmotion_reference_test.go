package music

import (
	"net/http"
	"strings"
	"testing"

	"veritmo/internal/music/motionref"
	"veritmo/internal/music/musicmotion"
)

const sheetParagraph = "Cuts: 9, 14, 6/min by thirds; first cut at 2.1 s; median shot 2.1 s; test sheet."

// addReference puts a ready reference in the library of the test: with its shots and rhythm sheet, without them (one analysed
// before they existed), or still analysing.
func addReference(t *testing.T, s *testServer, kind string) string {
	t.Helper()
	st := s.Assistant.RefStore()
	ref, err := st.Create("a reference", "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if kind == "analysing" {
		return ref.ID
	}
	yes := true
	ref.State = motionref.StateReady
	ref.Analysis = &motionref.Analysis{Duration: 100, Cuts: 2, MeanShot: 33}
	if kind == "sheet" {
		ref.Analysis.Shots = []motionref.Shot{
			{Index: 0, Start: 0, End: 40, Read: &motionref.ShotReading{Subject: "figure", Text: "none", TextSize: "small", Entrance: "cut", Camera: "pan"}},
			{Index: 1, Start: 40, End: 41.8, BeatCut: &yes, Read: &motionref.ShotReading{Subject: "typography", Text: "word", TextSize: "huge", Entrance: "letter", Camera: "static"}},
			{Index: 2, Start: 41.8, End: 100},
		}
		ref.Analysis.Rhythm = &motionref.Rhythm{MedianShotByThird: [3]float64{4, 2.1, 5}}
		ref.Analysis.RhythmText = sheetParagraph
	}
	if err := st.Save(ref); err != nil {
		t.Fatal(err)
	}
	return ref.ID
}

func pieceCount(t *testing.T, s *testServer) int {
	t.Helper()
	return strings.Count(get(t, s, "/api/music-motion/pieces"), `"id":"api-mm-`)
}

func TestReference_IsKeptReplacedClearedAndShownAndItsSheetReachesTheAssistant(t *testing.T) {
	s := mmServer(t)
	w := &recordingWriter{other: "nothing useful"}
	withWriter(s, w, "")
	a, b := addReference(t, s, "sheet"), addReference(t, s, "sheet")
	v := newPiece(t, s, map[string]any{"reference": a})
	if v.Reference != a || pieceOf(t, s, v.ID).Reference != a {
		t.Fatalf("kept with the piece and shown in its view: %q", v.Reference)
	}
	// a proposal that sends none reads the piece's reference: the design and the script are told the sheet
	if code, _, body := propose(t, s, v.ID, map[string]any{"what": "design"}); code != 200 || !w.told(sheetParagraph) {
		t.Fatalf("%d %s", code, body)
	}
	if code, _, body := propose(t, s, v.ID, map[string]any{"what": "script"}); code != 200 || !w.told("Median shot of the reference by thirds: 4.0 s, 2.1 s, 5.0 s") {
		t.Fatalf("%d %s", code, body)
	}
	// one sent with a proposal replaces it
	if code, _, body := propose(t, s, v.ID, map[string]any{"what": "design", "reference": b}); code != 200 || pieceOf(t, s, v.ID).Reference != b {
		t.Fatalf("%d %s", code, body)
	}
	// an empty text clears it, and the assistant is told nothing of it
	before := len(w.users)
	if code, _, body := propose(t, s, v.ID, map[string]any{"what": "design", "reference": ""}); code != 200 || pieceOf(t, s, v.ID).Reference != "" {
		t.Fatalf("%d %s", code, body)
	}
	w.mu.Lock()
	last := w.users[before:]
	w.mu.Unlock()
	for _, u := range last {
		if strings.Contains(u, "Rhythm of the reference") {
			t.Fatalf("a cleared reference is not told: %s", u)
		}
	}
}

func TestReference_NotReadyOrUnknownIsRefusedAndNothingChanges(t *testing.T) {
	s := mmServer(t)
	withWriter(s, &recordingWriter{other: "x"}, "")
	analysing := addReference(t, s, "analysing")
	n := pieceCount(t, s)
	for _, id := range []string{analysing, "ref-20260101-000000-abcdef"} {
		rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces", map[string]any{"project": "songs", "track_id": "night_drive", "client": "video-studio", "reference": id})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: %d %s", id, rec.Code, rec.Body)
		}
	}
	if pieceCount(t, s) != n {
		t.Fatal("nothing is created")
	}
	ok := addReference(t, s, "sheet")
	v := newPiece(t, s, map[string]any{"reference": ok})
	for _, id := range []string{analysing, "ref-20260101-000000-abcdef"} {
		if code, _, body := propose(t, s, v.ID, map[string]any{"what": "design", "reference": id}); code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: %d %s", id, code, body)
		}
	}
	if got := pieceOf(t, s, v.ID).Reference; got != ok {
		t.Fatalf("a refused proposal changes nothing: %q", got)
	}
}

func TestReference_WithoutASheetIsAcceptedAndTheProposalSaysSoInItsNotes(t *testing.T) {
	s := mmServer(t)
	w := &recordingWriter{other: "nothing useful"}
	withWriter(s, w, "")
	old := addReference(t, s, "old")
	v := newPiece(t, s, map[string]any{"reference": old})
	code, a, body := propose(t, s, v.ID, map[string]any{"what": "design"})
	if code != 200 || len(a.Notes) != 1 || !strings.Contains(a.Notes[0], "no rhythm sheet") || w.told("Rhythm of the reference") {
		t.Fatalf("%d %s", code, body)
	}
	// a proposal with no reference at all has no notes
	if code, a, _ := propose(t, s, v.ID, map[string]any{"what": "design", "reference": ""}); code != 200 || len(a.Notes) != 0 {
		t.Fatalf("%d %+v", code, a.Notes)
	}
}

func TestReference_TheFixedRuleOfANewPieceFollowsTheShots(t *testing.T) {
	s := mmServer(t)
	plain := newPiece(t, s, nil)
	ref := addReference(t, s, "sheet")
	followed := newPiece(t, s, map[string]any{"reference": ref})
	if plain.Script == nil || followed.Script == nil {
		t.Fatal("the view has the script")
	}
	if len(followed.Script.Pieces) <= len(plain.Script.Pieces) {
		t.Fatalf("a reference that cuts every 2 to 4 s makes more pieces than the six-second rule: %d against %d", len(followed.Script.Pieces), len(plain.Script.Pieces))
	}
}

func TestReference_TheSceneWriterIsGivenTheCueOfTheShot(t *testing.T) {
	s := mmServer(t)
	ref := addReference(t, s, "sheet")
	v := newPiece(t, s, map[string]any{"reference": ref})
	first := v.Script.Pieces[0].ID
	in, err := s.mHandler.sceneInput(musicmotion.SceneRef{Piece: v.ID, ScriptPiece: first}, "")
	if err != nil || !strings.Contains(in.Archetype, "In the reference video") {
		t.Fatalf("%v %q", err, in.Archetype)
	}
	plain := newPiece(t, s, nil)
	if in, _ := s.mHandler.sceneInput(musicmotion.SceneRef{Piece: plain.ID, ScriptPiece: plain.Script.Pieces[0].ID}, ""); in.Archetype != "" {
		t.Fatalf("no reference, no cue: %q", in.Archetype)
	}
}

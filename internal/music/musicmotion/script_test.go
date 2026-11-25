package musicmotion

import (
	"encoding/json"
	"strings"
	"testing"

	"veritmo/internal/music/songmap"
	"veritmo/internal/music/transcribe"
)

func testMap() songmap.Map {
	return songmap.Build(songmap.Input{Duration: 20, BPM: 120, Lyrics: "[Verse]\na\n[Chorus]\nb\n",
		Segments: []transcribe.Segment{{Start: 2, End: 4, Text: "give me the answer"}, {Start: 12, End: 14, Text: "allow allow it"}}})
}

func testScript() Script {
	zero, one := 0, 1
	return Script{Version: ScriptVersion, Pieces: []Piece{
		{ID: "p1", Start: 0, End: 10, Section: "Verse", Part: "toggle", Params: map[string]any{"label": "allow", "count": 3.0},
			Lines: []ScriptLine{{Line: &zero, Keyword: "answer"}}},
		{ID: "p2", Start: 10, End: 20, Section: "Chorus", Part: "stamp", Mode: ModeLight,
			Lines: []ScriptLine{{Line: &one, Keyword: "allow"}}, Hit: &ScriptHit{Word: "ALLOW", At: 12.5, Length: 0.8}},
	}}
}

func TestAGoodScriptIsValid(t *testing.T) {
	if err := testScript().Validate(Catalog{Builtin: BuiltinParts()}, testMap(), good()); err != nil {
		t.Fatal(err)
	}
}

func TestEachBadPieceIsRefusedAndNamed(t *testing.T) {
	cat := Catalog{Builtin: BuiltinParts()}
	cases := []struct {
		name string
		edit func(*Script)
		want string
	}{
		{"ends before it starts", func(s *Script) { s.Pieces[1].End = 9 }, `"p2"`},
		{"a gap", func(s *Script) { s.Pieces[1].Start = 12 }, "gap"},
		{"an overlap", func(s *Script) { s.Pieces[1].Start = 8 }, "overlaps"},
		{"does not start at 0", func(s *Script) { s.Pieces[0].Start = 1 }, "start at 0"},
		{"does not reach the end", func(s *Script) { s.Pieces[1].End = 18 }, "whole song"},
		{"unknown part", func(s *Script) { s.Pieces[0].Part = "hologram" }, "hologram"},
		{"parameter out of limits", func(s *Script) { s.Pieces[0].Params["count"] = 9.0 }, `"count"`},
		{"unknown parameter", func(s *Script) { s.Pieces[0].Params["speed"] = 1.0 }, `"speed"`},
		{"bad mode", func(s *Script) { s.Pieces[1].Mode = "grey" }, "mode"},
		{"keyword not in the line", func(s *Script) { s.Pieces[0].Lines[0].Keyword = "banana" }, "banana"},
		{"hit not in the lines", func(s *Script) { s.Pieces[1].Hit.Word = "BANANA" }, "word hit"},
		{"hit of two words", func(s *Script) { s.Pieces[1].Hit.Word = "ALLOW IT" }, "one word"},
		{"hit outside the piece", func(s *Script) { s.Pieces[1].Hit.At = 25 }, "inside the piece"},
		{"repeated id", func(s *Script) { s.Pieces[1].ID = "p1" }, "repeated"},
		{"a line with no text", func(s *Script) { n := 7; s.Pieces[0].Lines[0] = ScriptLine{Line: &n} }, "no text"},
		{"bad version", func(s *Script) { s.Version = 9 }, "version"},
	}
	for _, c := range cases {
		s := testScript()
		c.edit(&s)
		err := s.Validate(cat, testMap(), good())
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error with %q", c.name, err, c.want)
		}
	}
}

func TestAnUntimedLineNeedsItsOwnTimes(t *testing.T) {
	s := testScript()
	s.Pieces[0].Lines = []ScriptLine{{Text: "free text line", Keyword: "text"}}
	if err := s.Validate(Catalog{Builtin: BuiltinParts()}, testMap(), good()); err == nil || !strings.Contains(err.Error(), "start and an end") {
		t.Fatalf("%v", err)
	}
	s.Pieces[0].Lines[0].Start, s.Pieces[0].Lines[0].End = 1, 3
	if err := s.Validate(Catalog{Builtin: BuiltinParts()}, testMap(), good()); err != nil {
		t.Fatal(err)
	}
}

func TestTheScriptIsAFileThatCanBeReadAndEdited(t *testing.T) {
	dir := t.TempDir()
	if err := SaveScript(dir, testScript()); err != nil {
		t.Fatal(err)
	}
	got, ok := LoadScript(dir)
	a, _ := json.Marshal(got)
	b, _ := json.Marshal(testScript())
	if !ok || string(a) != string(b) {
		t.Fatalf("the script must come back as it was stored")
	}
}

func gridMap(bpm float64, from float64, n int) songmap.Map {
	m := songmap.Map{BPM: bpm, Duration: from + float64(n)*60/bpm}
	for i := 0; i < n; i++ {
		m.Beats = append(m.Beats, from+float64(i)*60/bpm)
	}
	return m
}

func TestABoundaryNearABeatMovesToIt(t *testing.T) {
	m := gridMap(120, 0, 100) // beats every 0.5 s
	if got := SnapBoundary(31.4, m); got != 31.5 {
		t.Fatalf("31.4 s should go to 31.5 s, got %v", got)
	}
	if got := SnapBoundary(31.74, m); got != 31.5 {
		t.Fatalf("got %v", got)
	}
}

func TestABoundaryWithNoBeatWithinHalfABeatKeepsItsTime(t *testing.T) {
	m := songmap.Map{BPM: 120, Beats: []float64{0, 10}} // a song whose pulse is lost in between
	if got := SnapBoundary(5.1, m); got != 5.1 {
		t.Fatalf("got %v", got)
	}
	if got := SnapBoundary(5.1, songmap.Map{}); got != 5.1 {
		t.Fatalf("no tempo, no snapping: %v", got)
	}
}

func TestAScriptIsSnappedToTheBeatAndAHitToTheHalfBeat(t *testing.T) {
	s := testScript()
	s.Pieces[0].End, s.Pieces[1].Start = 10.2, 10.2
	s.Pieces[1].Hit.At = 12.58
	m := gridMap(120, 0, 41)
	b, h := SnapScript(&s, m)
	if b != 1 || h != 1 || s.Pieces[1].Start != 10.0 || s.Pieces[0].End != 10.0 || s.Pieces[1].Hit.At != 12.5 {
		t.Fatalf("b=%d h=%d %+v", b, h, s.Pieces)
	}
	if s.Pieces[0].Start != 0 || s.Pieces[1].End != 20 {
		t.Fatal("the ends of the song never move")
	}
}

func TestASnapThatWouldLeaveAPieceTooShortIsSkipped(t *testing.T) {
	s := Script{Version: 1, Pieces: []Piece{{ID: "a", Start: 0, End: 1.1, Part: "toggle"}, {ID: "b", Start: 1.1, End: 20, Part: "toggle"}}}
	m := gridMap(60, 0, 30) // a beat per second: 1.1 would go to 1.0, leaving the first piece with exactly 1 s: fine
	SnapScript(&s, m)
	if s.Pieces[0].End != 1.0 {
		t.Fatalf("%+v", s.Pieces)
	}
	s = Script{Version: 1, Pieces: []Piece{{ID: "a", Start: 0, End: 0.4, Part: "toggle"}, {ID: "b", Start: 0.4, End: 20, Part: "toggle"}}}
	SnapScript(&s, m)
	if s.Pieces[0].End != 0.4 {
		t.Fatalf("a piece would be left under a second: %+v", s.Pieces)
	}
}

func TestAHitStaysInsideItsPieceWhenTheCutMovesPastIt(t *testing.T) {
	// The chorus starts at 9.8 s and its first word is sung at 9.85: the cut goes to the beat at 10.0 s and the hit,
	// which was inside, would be before the piece. It must be pushed in.
	s := Script{Version: 1, Pieces: []Piece{
		{ID: "a", Start: 0, End: 9.8, Part: "toggle"},
		{ID: "b", Start: 9.8, End: 20, Part: "stamp", Hit: &ScriptHit{Word: "ALLOW", At: 9.85, Length: 0.5}},
	}}
	SnapScript(&s, gridMap(120, 0, 41))
	b := s.Pieces[1]
	if b.Start != 10 || b.Hit.At < b.Start || b.Hit.At >= b.End {
		t.Fatalf("the hit must be inside the piece [%v, %v): %+v", b.Start, b.End, b.Hit)
	}
	// A hit at the very end of its piece is pulled back inside too.
	s = Script{Version: 1, Pieces: []Piece{{ID: "a", Start: 0, End: 20, Part: "toggle", Hit: &ScriptHit{Word: "X", At: 19.99, Length: 0.3}}}}
	SnapScript(&s, gridMap(120, 0, 41))
	if s.Pieces[0].Hit.At >= 20 {
		t.Fatalf("%+v", s.Pieces[0].Hit)
	}
}

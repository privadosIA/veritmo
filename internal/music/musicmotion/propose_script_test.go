package musicmotion

import (
	"context"
	"math"
	"strings"
	"testing"

	"veritmo/internal/music/songmap"
	"veritmo/internal/music/transcribe"
)

func longMap() songmap.Map {
	return songmap.Build(songmap.Input{Duration: 60, BPM: 120, Lyrics: "[Intro]\n[Verse]\nline one here\nline two here\n[Chorus]\nallow allow it\n[Verse]\nanother verse line\n",
		Segments: []transcribe.Segment{{Start: 12, End: 15, Text: "line one here"}, {Start: 16, End: 19, Text: "line two here"}, {Start: 30, End: 33, Text: "allow allow it"}, {Start: 45, End: 48, Text: "another verse line"}}})
}

func in(level string) ScriptInput {
	return ScriptInput{Map: longMap(), Design: good(), Level: level, Cat: Catalog{Builtin: BuiltinParts()}}
}

func covers(t *testing.T, s Script, total float64) {
	t.Helper()
	if s.Pieces[0].Start != 0 || math.Abs(s.Pieces[len(s.Pieces)-1].End-total) > 1e-6 {
		t.Fatalf("the script must cover the song: %+v", s.Pieces)
	}
	for i := 1; i < len(s.Pieces); i++ {
		if math.Abs(s.Pieces[i].Start-s.Pieces[i-1].End) > 1e-6 {
			t.Fatalf("gap or overlap between %s and %s", s.Pieces[i-1].ID, s.Pieces[i].ID)
		}
	}
}

func TestTheFixedRuleCoversTheSongWithPartsOfTheLevel(t *testing.T) {
	for _, level := range Levels {
		p, err := ProposeScript(context.Background(), nil, in(level))
		if err != nil {
			t.Fatal(level, err)
		}
		if p.Source != "fallback" {
			t.Fatal(p.Source)
		}
		covers(t, p.Script, 60)
		for _, pc := range p.Script.Pieces {
			part, ok := in(level).Cat.Get(pc.Part)
			if !ok || !part.Supports(level) {
				t.Fatalf("%s: %s is not a part of level %s", pc.ID, pc.Part, level)
			}
			if pc.End-pc.Start > MaxPieceSeconds+1 {
				t.Errorf("%s lasts %.1f s: sections are cut in pieces", pc.ID, pc.End-pc.Start)
			}
		}
		if err := p.Script.Validate(in(level).Cat, in(level).Map, in(level).Design); err != nil {
			t.Fatal(level, err)
		}
	}
}

func TestTheFixedRuleGivesLinesKeywordsAndAChorusHit(t *testing.T) {
	p, _ := ProposeScript(context.Background(), nil, in(LevelSimple))
	var lines, hits int
	for _, pc := range p.Script.Pieces {
		lines += len(pc.Lines)
		if pc.Hit != nil {
			hits++
			if !IsChorus(pc.Section) || !strings.EqualFold(pc.Hit.Word, "allow") {
				t.Errorf("the hit belongs to the chorus: %+v in %s", pc.Hit, pc.Section)
			}
		}
	}
	if lines != 4 || hits != 1 {
		t.Fatalf("lines=%d hits=%d", lines, hits)
	}
}

// The fixed rule writes no mode: a piece follows the design, so a design saved later is followed by every piece.
func TestTheFixedRuleWritesNoModeAndThePiecesFollowTheDesign(t *testing.T) {
	d := good()
	d.Modes = map[string]string{"chorus": ModeLight}
	d.Accent = "#d12f58" // readable on both tones
	x := in(LevelSimple)
	x.Design = d
	p, _ := ProposeScript(context.Background(), nil, x)
	for _, pc := range p.Script.Pieces {
		if pc.Mode != "" {
			t.Errorf("%s (%s): the fixed rule wrote the mode %q", pc.ID, pc.Section, pc.Mode)
		}
		want := ModeDark
		if IsChorus(pc.Section) {
			want = ModeLight
		}
		if got := ResolveMode(d, pc); got != want {
			t.Errorf("%s (%s): resolved %s, want %s", pc.ID, pc.Section, got, want)
		}
	}
	// The design changes later: the same pieces follow it.
	d.DefaultMode, d.Modes = ModeLight, nil
	for _, pc := range p.Script.Pieces {
		if got := ResolveMode(d, pc); got != ModeLight {
			t.Errorf("%s: resolved %s on a light design", pc.ID, got)
		}
	}
}

func TestAModeOfItsOwnBeatsTheDesign(t *testing.T) {
	d := good() // dark
	if got := ResolveMode(d, Piece{Section: "verse", Mode: ModeLight}); got != ModeLight {
		t.Fatalf("an explicit light mode resolved %s", got)
	}
	if got := ResolveMode(d, Piece{Section: "verse"}); got != ModeDark {
		t.Fatalf("no mode on a dark design resolved %s", got)
	}
}

// An assistant that writes no mode leaves it empty; one that writes a mode keeps it.
func TestTheAssistantsScriptKeepsOnlyTheModesItWrote(t *testing.T) {
	answer := `{"pieces":[{"section":"verse","start":0,"end":20,"part":"toggle"},{"section":"chorus","start":20,"end":40,"part":"stamp","mode":"light"}]}`
	s, err := parseScript(answer, in(LevelSimple))
	if err != nil {
		t.Fatal(err)
	}
	if s.Pieces[0].Mode != "" || s.Pieces[1].Mode != ModeLight {
		t.Fatalf("modes %q and %q", s.Pieces[0].Mode, s.Pieces[1].Mode)
	}
}

func TestTheScriptPromptTellsTheAssistantNotToWriteAMode(t *testing.T) {
	s := &systemWriter{answers: []string{assistantScript}}
	if _, err := ProposeScript(context.Background(), s, in(LevelSimple)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.systems[0], `Do not write a "mode"`) {
		t.Fatalf("the script prompt does not say the mode comes from the design:\n%s", s.systems[0])
	}
}

func TestAPartAboveTheLevelIsReplacedAndReported(t *testing.T) {
	s := Script{Version: 1, Pieces: []Piece{{ID: "p1", Start: 0, End: 30, Part: "wire-sphere"}, {ID: "p2", Start: 30, End: 60, Part: "toggle"}}}
	rep, err := ApplyLevel(&s, in(LevelSimple).Cat, LevelSimple)
	if err != nil || len(rep) != 1 || rep[0].Piece != "p1" || rep[0].From != "wire-sphere" || s.Pieces[0].Part == "wire-sphere" || s.Pieces[1].Part != "toggle" {
		t.Fatalf("%+v %v %+v", rep, err, s.Pieces)
	}
	if _, err := ApplyLevel(&s, in(LevelSimple).Cat, "extreme"); err == nil {
		t.Fatal("a level that does not exist is refused")
	}
}

const assistantScript = `{"pieces":[
 {"section":"Intro","start":0,"end":12,"part":"glass-card","params":{"title":"Intro"}},
 {"section":"Verse","start":12,"end":30,"part":"terminal","lines":[{"line":0,"keyword":"line"},{"line":1,"keyword":"line"}]},
 {"section":"Chorus","start":30,"end":45,"part":"stamp","lines":[{"line":2,"keyword":"allow"}],"hit":{"word":"ALLOW","at":30.2,"length":0.8}},
 {"section":"Verse","start":45,"end":60,"part":"flat-slabs","lines":[{"line":3,"keyword":"verse"}]}]}`

func TestAValidAssistantScriptIsUsed(t *testing.T) {
	x := in(LevelSimple)
	x.Map = songmap.Slice(x.Map, 0, 60) // same map
	w := &fakeWriter{answers: []string{"Here:\n" + assistantScript}}
	p, err := ProposeScript(context.Background(), w, x)
	if err != nil {
		t.Fatal(err)
	}
	if p.Source != "assistant" || w.calls != 1 || p.Script.Pieces[2].Part != "stamp" || p.Script.Pieces[0].ID != "p1" {
		t.Fatalf("%s calls=%d %+v", p.Source, w.calls, p.Script.Pieces)
	}
	covers(t, p.Script, x.Map.Duration)
	if !strings.Contains(w.users[0], "glass-card") || !strings.Contains(w.users[0], "Chorus") || !strings.Contains(w.users[0], "allow allow it") {
		t.Fatalf("the assistant must be told the catalog, the sections and the lines:\n%s", w.users[0])
	}
}

func TestAnInvalidAssistantScriptIsAskedAgainWithTheReason(t *testing.T) {
	bad := strings.Replace(assistantScript, `"part":"stamp"`, `"part":"hologram"`, 1)
	w := &fakeWriter{answers: []string{bad, assistantScript}}
	p, err := ProposeScript(context.Background(), w, in(LevelSimple))
	if err != nil || p.Source != "assistant" || w.calls != 2 {
		t.Fatalf("%v %s calls=%d", err, p.Source, w.calls)
	}
	if !strings.Contains(w.users[1], "hologram") {
		t.Fatalf("the second request must carry the reason:\n%s", w.users[1])
	}
}

func TestThreeFailedAnswersFallBackToTheFixedRule(t *testing.T) {
	w := &fakeWriter{answers: []string{"nothing useful"}}
	p, err := ProposeScript(context.Background(), w, in(LevelSimple))
	if err != nil || p.Source != "fallback" || w.calls != Attempts {
		t.Fatalf("%v %s calls=%d", err, p.Source, w.calls)
	}
	covers(t, p.Script, 60)
}

func TestAnAssistantPartAboveTheLevelIsReplaced(t *testing.T) {
	adv := strings.Replace(assistantScript, `"part":"stamp"`, `"part":"particles"`, 1)
	w := &fakeWriter{answers: []string{adv}}
	p, err := ProposeScript(context.Background(), w, in(LevelSimple))
	if err != nil || p.Source != "assistant" || len(p.Replacements) != 1 || p.Replacements[0].From != "particles" {
		t.Fatalf("%v %+v", err, p)
	}
}

func TestTheFixedRuleTakesTheStylesPartsInTurnAndItsRhythm(t *testing.T) {
	in := ScriptInput{Map: testMap(), Design: good(), Level: LevelSimple, Cat: NewCatalog(nil), Parts: []string{"counter", "no-such-part", "toggle"}, Rhythm: 2.5}
	s, err := FixedScript(in)
	if err != nil {
		t.Fatal(err)
	}
	home := ""
	for i, p := range s.Pieces {
		want := []string{"counter", "toggle"}[i%2]
		if IsChorus(p.Section) { // every chorus piece uses the part the first one was given in turn: the home part
			if home == "" {
				home = want
			}
			want = home
			if i > 0 && IsChorus(s.Pieces[i-1].Section) && s.Pieces[i-1].Part == home { // between two home pieces another part
				if p.Part == home {
					t.Fatalf("piece %d repeats the home part %s", i, home)
				}
				continue
			}
			if i > 0 && IsChorus(s.Pieces[i-1].Section) && p.Part != home && p.Part == s.Pieces[i-1].Part {
				t.Fatalf("piece %d repeats %s", i, p.Part)
			}
			if p.Part != home {
				continue
			}
		}
		if p.Part != want {
			t.Fatalf("piece %d: %s, want %s", i, p.Part, want)
		}
		if p.End-p.Start > 2.5+1e-6 {
			t.Fatalf("piece %d lasts %v", i, p.End-p.Start)
		}
	}
	// A list with nothing usable (or empty) leaves the usual parts, and no rhythm the usual length.
	in.Parts, in.Rhythm = []string{"no-such-part"}, 0
	s, err = FixedScript(in)
	if err != nil || len(s.Pieces) == 0 {
		t.Fatal(err)
	}
	used := map[string]bool{}
	for _, p := range s.Pieces {
		used[p.Part] = true
	}
	if len(used) < 2 {
		t.Fatalf("%v", used)
	}
}

func TestASectionThatStartsWhereTheSongEndsDoesNotMakeAPieceOfNoLength(t *testing.T) {
	input := in(LevelSimple)
	input.Map.Sections = append(input.Map.Sections, songmap.Section{Tag: "outro", Start: input.Map.Duration, End: input.Map.Duration})
	s, err := FixedScript(input)
	if err != nil {
		t.Fatal(err)
	}
	covers(t, s, input.Map.Duration)
	if err := s.Validate(input.Cat, input.Map, input.Design); err != nil {
		t.Fatal(err)
	}
}

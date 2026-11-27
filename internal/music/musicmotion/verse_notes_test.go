package musicmotion

import (
	"context"
	"testing"
)

func notesScript(part string, layout *VerseLayout, lines bool) (Script, Design) {
	p := Piece{ID: "p1", Start: 0, End: 10, Section: "verse", Part: part}
	if lines {
		p.Lines = []ScriptLine{{Keyword: "road"}}
	}
	d := Design{Dark: "#111111", Light: "#eeeeee", Accent: "#b8411f", Display: "serif", Body: "serif"}
	if layout != nil {
		p.Design = &DesignOverride{VerseLayout: layout}
	}
	return Script{Version: 1, Pieces: []Piece{p}}, d
}

func TestVerseNotesSaysWhenTheVerseFallsOnThePart(t *testing.T) {
	cat := Catalog{Builtin: BuiltinParts()}
	slabs, _ := cat.Get("flat-slabs") // top 0.3, center 0.33, bottom 0

	s, d := notesScript("flat-slabs", &VerseLayout{Position: VersePositionTop}, true)
	notes := VerseNotes(cat, s, d)
	if len(notes) != 1 || notes[0].Piece != "p1" || notes[0].Part != "flat-slabs" || notes[0].Band != "top" || notes[0].Share != slabs.Bands.Top || notes[0].Free != "bottom" {
		t.Fatalf("top over flat bars: %+v", notes)
	}

	s, d = notesScript("flat-slabs", nil, true)
	if notes := VerseNotes(cat, s, d); len(notes) != 0 {
		t.Errorf("the bottom is clear of flat-slabs: %+v", notes)
	}

	s, d = notesScript("blank", &VerseLayout{Position: VersePositionTop}, true)
	if notes := VerseNotes(cat, s, d); len(notes) != 0 {
		t.Errorf("blank draws nothing: %+v", notes)
	}

	// sunburst paints every third on purpose: the verse is meant to be read over it and there is no band to advise
	s, d = notesScript("sunburst", nil, true)
	if notes := VerseNotes(cat, s, d); len(notes) != 0 {
		t.Errorf("a part made to fill the frame gave a note: %+v", notes)
	}
}

func TestVerseNotesFollowsTheDesignsLayoutAndThePiecesOwnWins(t *testing.T) {
	cat := Catalog{Builtin: BuiltinParts()}
	s, d := notesScript("flat-slabs", nil, true)
	d.VerseLayout = &VerseLayout{Position: VersePositionCenter}
	if notes := VerseNotes(cat, s, d); len(notes) != 1 || notes[0].Band != "center" {
		t.Fatalf("the design's layout: %+v", notes)
	}
	s.Pieces[0].Design = &DesignOverride{VerseLayout: &VerseLayout{Position: VersePositionBottom}}
	if notes := VerseNotes(cat, s, d); len(notes) != 0 {
		t.Fatalf("the piece's own layout wins: %+v", notes)
	}
}

func TestVerseNotesIsSilentAboutWhatItDoesNotKnow(t *testing.T) {
	cat := Catalog{Builtin: BuiltinParts()}
	for name, c := range map[string]struct {
		part  string
		lines bool
	}{"no verse in the piece": {"flat-slabs", false}, "a 3D piece": {"", true}, "a part that is not in the catalog": {"nope", true}} {
		s, d := notesScript(c.part, &VerseLayout{Position: VersePositionTop}, c.lines)
		if notes := VerseNotes(cat, s, d); len(notes) != 0 {
			t.Errorf("%s: %+v", name, notes)
		}
	}
	// a part with no bands is unknown, not empty
	dir := t.TempDir()
	user := Catalog{Builtin: BuiltinParts(), UserDir: dir}
	if err := user.CreatePart([]byte(testManifest), movingFragment, OriginOfDraft(Draft{})); err != nil {
		t.Fatal(err)
	}
	s, d := notesScript("probe", &VerseLayout{Position: VersePositionTop}, true)
	if notes := VerseNotes(user, s, d); len(notes) != 0 {
		t.Errorf("a part with no bands gave a note: %+v", notes)
	}
}

func TestFreeBandIsTheClearestOneOrNone(t *testing.T) {
	for _, c := range []struct {
		b    Bands
		want string
	}{
		{Bands{Top: 0.5, Center: 0.5, Bottom: 0}, "bottom"},
		{Bands{Top: 0, Center: 0.5, Bottom: 0.5}, "top"},
		{Bands{Top: 0.5, Center: 0.05, Bottom: 0.5}, "center"},
		{Bands{Top: 0.5, Center: 0.15, Bottom: 0.5}, ""}, // half full is not clear
		{Bands{Top: 0, Center: 0, Bottom: 0}, "bottom"},  // a tie goes to the bottom
	} {
		if got := freeBand(c.b); got != c.want {
			t.Errorf("%+v: %q, want %q", c.b, got, c.want)
		}
	}
}

func placeScript(part string, withTrack bool) Script {
	s, _ := notesScript(part, nil, true)
	if withTrack {
		s.Video = []VideoItem{{ID: "v1", Clip: "a.mp4", Start: 0, End: 10}}
	}
	return s
}

func TestPlaceVerseMovesAVerseAwayFromAPartThatFillsTheBottom(t *testing.T) {
	// a part that paints the bottom third and leaves the top clear
	user := Catalog{Builtin: BuiltinParts(), UserDir: t.TempDir()}
	manifest := `{"name":"floor","description":"a floor","version":1,"bands":{"top":0,"center":0.02,"bottom":0.9},"over_video":true,"levels":["simple"],"params":[]}`
	if err := user.CreatePart([]byte(manifest), movingFragment, OriginOfDraft(Draft{})); err != nil {
		t.Fatal(err)
	}
	s, d := notesScript("floor", nil, true)
	s.Video = []VideoItem{{ID: "v1", Clip: "a.mp4", Start: 0, End: 10}}
	d.VerseLayout = &VerseLayout{Scale: 1.2, Align: VerseAlignLeft}
	notes := PlaceVerse(&s, user, d)
	l := s.Pieces[0].Design.VerseLayout
	if len(notes) != 1 || l == nil || l.Position != VersePositionTop || l.Scale != 1.2 || l.Align != VerseAlignLeft {
		t.Fatalf("%v %+v", notes, l)
	}
	// what it proposed is what VerseNotes accepts
	if n := VerseNotes(user, s, d); len(n) != 0 {
		t.Errorf("the proposal still has a note: %+v", n)
	}
}

func TestPlaceVerseLeavesEverythingElseAsItWas(t *testing.T) {
	cat := Catalog{Builtin: BuiltinParts()}
	user := Catalog{Builtin: BuiltinParts(), UserDir: t.TempDir()}
	manifest := `{"name":"floor","description":"a floor","version":1,"bands":{"top":0,"center":0.02,"bottom":0.9},"levels":["simple"],"params":[]}`
	if err := user.CreatePart([]byte(manifest), movingFragment, OriginOfDraft(Draft{})); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		cat       Catalog
		part      string
		track     bool
		own       bool
		noLines   bool
		designTop bool
	}{
		"a piece away from the track":           {user, "floor", false, false, false, false},
		"a bottom that is clear":                {cat, "flat-slabs", true, false, false, false},
		"a layout of its own":                   {user, "floor", true, true, false, false},
		"a design that already moves it":        {user, "floor", true, false, false, true},
		"a piece with no verse":                 {user, "floor", true, false, true, false},
		"a 3D piece":                            {user, "", true, false, false, false},
		"a part with no measured bands":         {Catalog{Builtin: BuiltinParts(), UserDir: t.TempDir()}, "probe", true, false, false, false},
		"no clear band (a part that fills all)": {cat, "sunburst", true, false, false, false},
	} {
		s := placeScript(c.part, c.track)
		d := Design{Dark: "#111111", Light: "#eeeeee", Accent: "#b8411f", Display: "serif", Body: "serif"}
		if c.own {
			s.Pieces[0].Design = &DesignOverride{VerseLayout: &VerseLayout{Position: VersePositionCenter}}
		}
		if c.noLines {
			s.Pieces[0].Lines = nil
		}
		if c.designTop {
			d.VerseLayout = &VerseLayout{Position: VersePositionTop}
		}
		if c.part == "probe" {
			if err := c.cat.CreatePart([]byte(testManifest), movingFragment, OriginOfDraft(Draft{})); err != nil {
				t.Fatal(err)
			}
		}
		before := s.Pieces[0].Design
		if notes := PlaceVerse(&s, c.cat, d); len(notes) != 0 || s.Pieces[0].Design != before {
			t.Errorf("%s: %v %+v", name, notes, s.Pieces[0].Design)
		}
	}
}

func TestAProposalWithNoTrackIsNotTouchedByTheVerse(t *testing.T) {
	for _, level := range Levels {
		p, err := ProposeScript(context.Background(), nil, in(level))
		if err != nil {
			t.Fatal(err)
		}
		for _, pc := range p.Script.Pieces {
			if pc.Design != nil {
				t.Fatalf("%s: a piece of a script with no track got a design override: %+v", pc.ID, pc.Design)
			}
		}
	}
}

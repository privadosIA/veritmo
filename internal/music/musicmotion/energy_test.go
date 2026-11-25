package musicmotion

import (
	"context"
	"strings"
	"testing"

	"veritmo/internal/music/songmap"
)

// loudChorusMap is longMap with a loudness curve: the chorus is loud, the rest quiet.
func loudChorusMap() songmap.Map {
	m := longMap()
	m.LoudnessHop = 0.25
	m.Loudness = make([]float64, int(m.Duration/m.LoudnessHop))
	for i := range m.Loudness {
		m.Loudness[i] = 0.05
		at := float64(i) * m.LoudnessHop
		for _, s := range m.Sections {
			if IsChorus(s.Tag) && at >= s.Start && at < s.End {
				m.Loudness[i] = 0.3
			}
		}
	}
	return m
}

func TestSectionEnergiesRankTheLoudestFirstAndSayWhichIsWhich(t *testing.T) {
	m := loudChorusMap()
	e := SectionEnergies(m)
	if len(e) != len(m.Sections) {
		t.Fatalf("%d energies for %d sections", len(e), len(m.Sections))
	}
	for _, s := range e {
		if IsChorus(s.Tag) != (s.Rank <= 1 && s.Loudness > 0.2) && IsChorus(s.Tag) {
			t.Errorf("the chorus must be the loudest: %+v", s)
		}
		if IsChorus(s.Tag) && (s.Rank != 1 || s.Loudness < 0.25) {
			t.Errorf("chorus: %+v", s)
		}
	}
	if SectionEnergies(longMap()) != nil {
		t.Error("a map with no loudness has no energies")
	}
	for _, c := range []struct {
		rank, n int
		want    string
	}{{1, 1, EnergyNormal}, {1, 2, EnergyLoud}, {2, 2, EnergyQuiet}, {1, 5, EnergyLoud}, {3, 5, EnergyNormal}, {5, 5, EnergyQuiet}, {2, 5, EnergyLoud}} {
		if got := EnergyClass(c.rank, c.n); got != c.want {
			t.Errorf("rank %d of %d: %s, want %s", c.rank, c.n, got, c.want)
		}
	}
	e2 := []SectionEnergy{{Loudness: 0.3, Rank: 1}, {Loudness: 0.05, Rank: 3}, {Loudness: 0.1, Rank: 2}}
	if !IsBreakdown(e2, 1) || IsBreakdown(e2, 2) || IsBreakdown(e2, 0) {
		t.Error("the breakdown is the quietest section right after a louder one")
	}
}

func TestTheFixedRuleFollowsTheEnergyAndEveryChorusPieceSharesOnePart(t *testing.T) {
	m := loudChorusMap()
	input := ScriptInput{Map: m, Design: good(), Level: LevelSimple, Cat: Catalog{Builtin: BuiltinParts()}}
	s, err := FixedScript(input)
	if err != nil {
		t.Fatal(err)
	}
	var verse, chorus []float64
	home, prevChorus, others := "", "", 0
	for _, p := range s.Pieces {
		d := p.End - p.Start
		if IsChorus(p.Section) {
			chorus = append(chorus, d)
			if home == "" {
				home = p.Part
			}
			if prevChorus != "" && p.Part == prevChorus {
				t.Errorf("two chorus pieces in a row use %q (%s)", p.Part, p.ID)
			}
			prevChorus = p.Part
			if p.Part != home {
				others++
			}
		} else {
			verse = append(verse, d)
		}
	}
	if home == "" || s.Home != home {
		t.Fatalf("the script names its home part: %q vs %q", s.Home, home)
	}
	_ = others
	maxChorus, minVerse := 0.0, 1e9
	for _, d := range chorus {
		maxChorus = max(maxChorus, d)
	}
	for _, d := range verse {
		minVerse = min(minVerse, d)
	}
	bar := BarSeconds(m)
	if maxChorus > 2*bar+1.2 {
		t.Errorf("a chorus piece lasts %.1f s, two bars are %.1f s", maxChorus, 2*bar)
	}
	if !(minVerse > maxChorus) {
		t.Errorf("the verse pieces (shortest %.1f s) must be longer than the chorus pieces (longest %.1f s)", minVerse, maxChorus)
	}
	covers(t, s, 60)
	if err := s.Validate(input.Cat, m, input.Design); err != nil {
		t.Fatal(err)
	}
}

func TestAStyleRhythmStillSetsTheLengthOfThePieces(t *testing.T) {
	m := loudChorusMap()
	s, err := FixedScript(ScriptInput{Map: m, Design: good(), Level: LevelSimple, Cat: Catalog{Builtin: BuiltinParts()}, Rhythm: 5})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range s.Pieces {
		if p.End-p.Start > 6 {
			t.Errorf("%s lasts %.1f s with a rhythm of 5 s", p.ID, p.End-p.Start)
		}
	}
}

func TestTheScriptProposalIsToldTheEnergyOfEachSectionAndTheCraftRules(t *testing.T) {
	m := loudChorusMap()
	input := ScriptInput{Map: m, Design: good(), Level: LevelSimple, Cat: Catalog{Builtin: BuiltinParts()}, Device: "one object across the song", Hook: "headlights cut the dark"}
	user := scriptUser(input, "")
	for _, want := range []string{"energy 1 of", "loudness 0.30", "Tempo: 120 bpm, a bar lasts 2.00 s", "The device that carries the video: one object across the song", "The first two seconds show: headlights cut the dark"} {
		if !strings.Contains(user, want) {
			t.Errorf("the user message lacks %q:\n%s", want, user)
		}
	}
	for _, want := range []string{"pieces of 8 to 15 seconds in the quiet sections", "one piece per two to four bars", `"home": "<part>"`, "rhymes with the opening", "longer than 8 seconds carries a change", "a third of the seams", "between 0.3 and 0.6"} {
		if !strings.Contains(scriptSystem, want) {
			t.Errorf("the instruction lacks %q", want)
		}
	}
	s, err := parseScript(`{"home":"toggle","pieces":[{"section":"Verse","start":0,"end":60,"part":"toggle"}]}`, input)
	if err != nil || s.Home != "toggle" {
		t.Fatalf("home is read: %v %+v", err, s)
	}
	if s, _ := parseScript(`{"home":"no-such-part","pieces":[{"start":0,"end":60,"part":"toggle"}]}`, input); s.Home != "" {
		t.Errorf("a home that is not in the catalog is dropped: %q", s.Home)
	}
	if _, err := ProposeScript(context.Background(), nil, input); err != nil {
		t.Fatal(err)
	}
}

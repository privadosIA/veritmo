package musicmotion

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"veritmo/internal/music/songmap"
)

// nineWords is one line of nine sung words, a quarter of a second each with a half-second pause after the fourth.
func nineWords(start float64) Line {
	texts := []string{"we", "will", "be", "there,", "before", "the", "morning", "light", "comes"}
	var ws []Word
	at := start
	for i, w := range texts {
		ws = append(ws, Word{Text: w, Start: at, End: at + 0.25, Heard: true})
		at += 0.25
		if i == 3 {
			at += 0.5
		}
	}
	return Line{Text: strings.Join(texts, " "), Start: start, End: ws[len(ws)-1].End, Words: ws, Keyword: 6}
}

func TestALongLineIsGroupedByTheEnergyOfItsSection(t *testing.T) {
	l := nineWords(2)
	for energy, bounds := range map[string][2]int{EnergyLoud: {2, 3}, EnergyNormal: {3, 5}, EnergyQuiet: {4, 6}} {
		gs := LineGroups(l, energy)
		if len(gs) < 2 || gs[0].From != 0 || gs[len(gs)-1].To != 9 {
			t.Fatalf("%s: the groups cover the line: %+v", energy, gs)
		}
		for i, g := range gs {
			size := g.To - g.From
			last := i == len(gs)-1
			if size < 1 || size > bounds[1]+1 || (size < bounds[0] && !(last && size > 1) && energy != EnergyQuiet) {
				t.Errorf("%s: group %d has %d words, want %d to %d: %+v", energy, i, size, bounds[0], bounds[1], gs)
			}
			if i > 0 && g.From != gs[i-1].To {
				t.Errorf("%s: the groups are one after the other: %+v", energy, gs)
			}
		}
	}
	// the pause after "there," and its comma are the good place to break in a normal section: 4 words, then the rest
	if gs := LineGroups(l, EnergyNormal); gs[0].To != 4 {
		t.Errorf("the first group of a normal line ends at the phrase: %+v", gs)
	}
	// the keyword (word 6) is inside exactly one group, which is the one that carries the accent
	in := 0
	for _, g := range LineGroups(l, EnergyLoud) {
		if g.From <= l.Keyword && l.Keyword < g.To {
			in++
		}
	}
	if in != 1 {
		t.Errorf("the keyword is in one group, found %d", in)
	}
}

func TestAGroupOpensBeforeItsFirstWordAndIsRemovedAfterItsLastOrWhenTheNextOpens(t *testing.T) {
	l := nineWords(2)
	gs := LineGroups(l, EnergyLoud)
	if gs[0].Open != 2 {
		t.Errorf("never before the line starts: %v", gs[0])
	}
	for i, g := range gs {
		first, last := l.Words[g.From], l.Words[g.To-1]
		if i > 0 && g.Open > first.Start-groupOpenBefore+1e-9 && g.Open != l.Words[g.From-1].End {
			t.Errorf("group %d opens 0.2 s before its first word (or when the word before it ends): open %.3f, first word at %.3f", i, g.Open, first.Start)
		}
		if g.Open > first.Start+1e-9 {
			t.Errorf("group %d opens after its first word: %+v", i, g)
		}
		if g.Close < last.End-1e-9 {
			t.Errorf("group %d is removed before its last word ends: %+v", i, g)
		}
		if i+1 < len(gs) && g.Close > gs[i+1].Open+1e-9 {
			t.Errorf("group %d is removed when the next opens: %+v / %+v", i, g, gs[i+1])
		}
		if i+1 == len(gs) && g.Close != round3(last.End+groupCloseAfter) {
			t.Errorf("the last group is removed 0.12 s after its last word: %+v", g)
		}
	}
	// two renders are the same: nothing depends on anything but the line
	if !reflect.DeepEqual(LineGroups(l, EnergyLoud), LineGroups(l, EnergyLoud)) {
		t.Error("deterministic")
	}
}

func TestAShortLineOrOneWithNoTimedWordsIsNotGrouped(t *testing.T) {
	five := Line{Text: "a b c d e", Start: 1, End: 2, Words: []Word{{"a", 1, 1.2, true}, {"b", 1.2, 1.4, true}, {"c", 1.4, 1.6, true}, {"d", 1.6, 1.8, true}, {"e", 1.8, 2, true}}}
	if LineGroups(five, EnergyLoud) != nil {
		t.Error("five words: one line, as before")
	}
	six := five
	six.Words = append(append([]Word(nil), five.Words...), Word{"f", 2, 2.2, true})
	if LineGroups(six, EnergyLoud) != nil {
		t.Error("six words: still one line")
	}
	if LineGroups(Line{Text: "one two three four five six seven eight", Start: 1, End: 3}, EnergyLoud) != nil {
		t.Error("a line with no timed words has nothing to group")
	}
	// a last word that would be alone joins the group before it
	seven := six
	seven.Words = append(append([]Word(nil), six.Words...), Word{"g", 2.2, 2.4, true})
	for _, g := range LineGroups(seven, EnergyLoud) {
		if g.To-g.From == 1 {
			t.Errorf("no group of one word at the end: %+v", LineGroups(seven, EnergyLoud))
		}
	}
}

func TestTheSceneGroupsItsLongLinesByTheEnergyOfItsSectionAndADesignCanTurnItOff(t *testing.T) {
	m := loudChorusMap()
	var chorus, verse Section2
	for _, s := range m.Sections {
		if IsChorus(s.Tag) && chorus.tag == "" {
			chorus = Section2{s.Tag, s.Start, s.End}
		}
		if !IsChorus(s.Tag) && verse.tag == "" {
			verse = Section2{s.Tag, s.Start, s.End}
		}
	}
	line := nineWords(0)
	place := func(sec Section2) songmap.Map {
		mm := m
		mm.Words = nil
		at := sec.start + 1
		for i, w := range line.Words {
			mm.Words = append(mm.Words, songmap.Word{Text: w.Text, Start: at + w.Start, End: at + w.End, Line: 0, Heard: true, Confidence: 1})
			_ = i
		}
		return mm
	}
	groupedIn := func(sec Section2, off bool) []Group {
		mm := place(sec)
		zero := 0
		piece := Piece{ID: "p1", Start: sec.start, End: sec.end, Section: sec.tag, Part: "counter", Lines: []ScriptLine{{Line: &zero, Keyword: "morning"}}}
		d := good()
		if off {
			f := false
			d.VerseGroups = &f
		}
		sc, _, err := sceneFor(piece, ProjectSpec{Map: mm, Design: d, Script: Script{Version: ScriptVersion, Pieces: []Piece{piece}}, Cat: Catalog{Builtin: BuiltinParts()}, Width: 1280, Height: 720})
		if err != nil || len(sc.Lines) != 1 {
			t.Fatalf("%v %+v", err, sc.Lines)
		}
		return sc.Lines[0].Groups
	}
	loud, calm := groupedIn(chorus, false), groupedIn(verse, false)
	if len(loud) <= len(calm) || len(loud) == 0 {
		t.Fatalf("a loud section has more, smaller groups than a calm one: %d vs %d", len(loud), len(calm))
	}
	for _, g := range loud {
		if g.To-g.From > 4 {
			t.Errorf("a loud group has %d words: %+v", g.To-g.From, loud)
		}
	}
	if groupedIn(chorus, true) != nil {
		t.Error("verse_groups: false shows the line whole")
	}
}

// Section2 is a section by value for the test above.
type Section2 struct {
	tag        string
	start, end float64
}

func TestThePageShowsOneGroupOfAGroupedLineAtATimeAndNoMoreThanItsWords(t *testing.T) {
	sc := testScene()
	line := nineWords(0.5)
	line.Groups = LineGroups(line, EnergyLoud)
	sc.Lines = []Line{line}
	sc.Hit = nil
	sc.Duration = 6
	s, _ := openPage(t, sc)
	shown := `(function(){var l=Array.from(document.querySelectorAll('#verse .line')).filter(function(x){return x.style.display!=='none'})[0];if(!l)return null;return Array.from(l.querySelectorAll('.group')).filter(function(g){return g.style.display!=='none'}).map(function(g){return Array.from(g.children).map(function(w){return w.textContent+(w.classList.contains('k')?'*':'')+':'+w.style.opacity})})})()`
	var got [][]string
	evalAt(t, s, 0.6, shown, &got)
	if len(got) != 1 || len(got[0]) != len(strings.Fields(strings.Join(textsOf(line, 0, line.Groups[0].To), " "))) {
		t.Fatalf("at 0.6 s only the first group is on the page: %v", got)
	}
	for _, g := range line.Groups {
		mid := (line.Words[g.From].Start + line.Words[g.To-1].End) / 2
		evalAt(t, s, mid, shown, &got)
		if len(got) != 1 || len(got[0]) != g.To-g.From {
			t.Fatalf("at %.2f s exactly the group %d-%d is shown: %v", mid, g.From, g.To, got)
		}
	}
	evalAt(t, s, 5.9, shown, &got)
	if len(got) != 0 {
		t.Errorf("after the last word no group stays on the page: %v", got)
	}
	// the keyword's group carries the accent
	last := line.Groups[len(line.Groups)-1]
	for _, g := range line.Groups {
		if g.From <= line.Keyword && line.Keyword < g.To {
			last = g
		}
	}
	evalAt(t, s, (line.Words[last.From].Start+line.Words[last.To-1].End)/2, shown, &got)
	found := false
	for _, w := range got[0] {
		found = found || strings.Contains(w, "*")
	}
	if !found {
		t.Errorf("the group of the keyword carries the accent: %v", got)
	}
	// the pictures of two renders are the same
	a, _ := s.Frame(context.Background(), 1.0)
	s2, _ := openPage(t, sc)
	b, _ := s2.Frame(context.Background(), 1.0)
	if string(a) != string(b) {
		t.Error("two renders of the same frame differ")
	}
}

func textsOf(l Line, from, to int) []string {
	var out []string
	for _, w := range l.Words[from:to] {
		out = append(out, w.Text)
	}
	return out
}

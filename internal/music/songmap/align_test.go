package songmap

import (
	"math"
	"reflect"
	"strings"
	"testing"

	"veritmo/internal/music/transcribe"
)

func hw(text string, start, end float64) transcribe.Word {
	return transcribe.Word{Text: text, Start: start, End: end}
}

func texts(ws []Word) string {
	var out []string
	for _, w := range ws {
		out = append(out, w.Text)
	}
	return strings.Join(out, " ")
}

func inOrder(t *testing.T, ws []Word) {
	t.Helper()
	for i, w := range ws {
		if w.End < w.Start {
			t.Fatalf("%q ends before it starts: %+v", w.Text, w)
		}
		if i > 0 && w.Start < ws[i-1].Start {
			t.Fatalf("%q starts before %q: %+v", w.Text, ws[i-1].Text, ws)
		}
	}
}

func TestWrittenWordsKeepTheLinesAndSkipTheTags(t *testing.T) {
	ws := WrittenWords("[Verse]\nMonday morning, nine a.m.\nsecond line\n[Chorus]\nallow it\n")
	var got []string
	for _, w := range ws {
		got = append(got, w.Text)
	}
	if strings.Join(got, " ") != "Monday morning, nine a.m. second line allow it" {
		t.Fatalf("%v", got)
	}
	if ws[0].Line != 0 || ws[3].Line != 0 || ws[4].Line != 1 || ws[6].Line != 2 {
		t.Fatalf("the lines are numbered over the whole lyrics: %+v", ws)
	}
}

func TestWordsThatMatchTakeTheirTimeAndANumberTakesItWhereItIsHeard(t *testing.T) {
	written := WrittenWords("Monday morning, nine a.m.")
	heard := []transcribe.Word{hw(" Monday", 0, 0.44), hw(" morning,", 0.44, 1.1), hw(" 9", 1.1, 1.32), hw(" a.m.", 1.32, 1.94)}
	got := AlignWords(written, heard, nil)
	if texts(got) != "Monday morning, nine a.m." {
		t.Fatalf("the written words are kept as written: %s", texts(got))
	}
	want := [][2]float64{{0, 0.44}, {0.44, 1.1}, {1.1, 1.32}, {1.32, 1.94}}
	for i, w := range got {
		if w.Start != want[i][0] || w.End != want[i][1] {
			t.Errorf("%q: %v-%v, want %v", w.Text, w.Start, w.End, want[i])
		}
	}
	inOrder(t, got)
}

func TestAWordTheVoiceSkipsGetsATimeBetweenItsNeighbours(t *testing.T) {
	written := WrittenWords("one two three four five")
	heard := []transcribe.Word{hw("one", 0, 0.5), hw("two", 0.5, 1), hw("four", 3, 3.5), hw("five", 3.5, 4)}
	got := AlignWords(written, heard, nil)
	if texts(got) != "one two three four five" {
		t.Fatalf("%s", texts(got))
	}
	three := got[2]
	if three.Start < 1-1e-9 || three.End > 3+1e-9 || three.End <= three.Start {
		t.Fatalf("`three` sits between `two` and `four`: %+v", three)
	}
	if got[3].Start != 3 || got[0].Start != 0 {
		t.Fatalf("the heard ones keep their time: %+v", got)
	}
	inOrder(t, got)
}

func TestARepeatedLineTheLyricsWriteOnceIsLeftOut(t *testing.T) {
	written := WrittenWords("hello there my friend")
	heard := []transcribe.Word{
		hw("hello", 0, 0.4), hw("there", 0.4, 0.8), hw("my", 0.8, 1), hw("friend", 1, 1.5),
		hw("hello", 2, 2.4), hw("there", 2.4, 2.8), hw("my", 2.8, 3), hw("friend", 3, 3.5),
	}
	got := AlignWords(written, heard, nil)
	if texts(got) != "hello there my friend" || len(got) != 4 {
		t.Fatalf("%s", texts(got))
	}
	inOrder(t, got)
	if got[3].End > 3.6 {
		t.Fatalf("every written word ends with a time: %+v", got)
	}
}

func TestWordsAtTheEdgesGetATimeToo(t *testing.T) {
	written := WrittenWords("intro words then sung here and outro words")
	heard := []transcribe.Word{hw("sung", 10, 10.5), hw("here", 10.5, 11)}
	got := AlignWords(written, heard, nil)
	for _, w := range got {
		if w.End <= w.Start {
			t.Fatalf("%q has no time: %+v", w.Text, got)
		}
	}
	if got[0].Start < 0 || got[2].End > 10+1e-9 || got[5].Start < 11-1e-9 {
		t.Fatalf("what comes before is before and what comes after is after: %+v", got)
	}
	inOrder(t, got)
}

func TestNothingHeardKeepsTheWordsAndIsNotAnError(t *testing.T) {
	got := AlignWords(WrittenWords("a b c"), nil, nil)
	if len(got) != 3 || texts(got) != "a b c" {
		t.Fatalf("%+v", got)
	}
	inOrder(t, got) // nothing was heard: the times mean nothing, but they are in order and the words are there
	if got := AlignWords(nil, []transcribe.Word{hw("x", 0, 1)}, nil); len(got) != 0 {
		t.Fatalf("no lyrics, no words: %+v", got)
	}
}

func TestTheAlignmentOfTheSameInputIsTheSame(t *testing.T) {
	written := WrittenWords("one two three four five six seven")
	heard := []transcribe.Word{hw("one", 0, 1), hw("tree", 2, 3), hw("four", 3, 4), hw("six", 5, 6), hw("seven", 6, 7), hw("extra", 7, 8)}
	a := AlignWords(written, heard, nil)
	b := AlignWords(written, heard, nil)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("two alignments of the same input differ")
	}
	for _, w := range a {
		if math.IsNaN(w.Start) || math.IsNaN(w.End) {
			t.Fatal("nan")
		}
	}
}

func TestAWordCutOrJoinedByWhisperStillMatches(t *testing.T) {
	written := WrittenWords("quick sync today")
	heard := []transcribe.Word{hw("quicksync", 1, 2), hw("today", 2, 3)}
	got := AlignWords(written, heard, nil)
	if got[0].Start != 1 || got[2].Start != 2 {
		t.Fatalf("%+v", got)
	}
	inOrder(t, got)
}

const heardLyrics = "[Intro]\n[Verse]\nline one here\nline two here\n[Chorus]\nallow it now\n[Verse]\nanother verse line\n"

func heardWords() []transcribe.Word {
	var out []transcribe.Word
	add := func(start float64, words ...string) {
		for i, w := range words {
			out = append(out, hw(w, start+float64(i)*0.5, start+float64(i)*0.5+0.45))
		}
	}
	add(12, "line", "one", "here")
	add(16, "line", "two", "here")
	add(30, "allow", "it", "now")
	add(45, "another", "verse", "line")
	return out
}

func TestTheMapFromHeardWordsHasTheWrittenWordsAndTheirLineNumbers(t *testing.T) {
	heard := Build(Input{Duration: 60, BPM: 120, Lyrics: heardLyrics, Heard: heardWords()})
	est := Build(Input{Duration: 60, BPM: 120, Lyrics: heardLyrics, EstimateWords: true})
	if heard.WordsEstimated || !est.WordsEstimated {
		t.Fatalf("heard %v estimated %v", heard.WordsEstimated, est.WordsEstimated)
	}
	if len(heard.Words) != len(est.Words) {
		t.Fatalf("%d words heard, %d estimated", len(heard.Words), len(est.Words))
	}
	for i := range heard.Words {
		if heard.Words[i].Line != est.Words[i].Line || heard.Words[i].Text != est.Words[i].Text {
			t.Fatalf("word %d: line %d %q against line %d %q: scripts name lines by number", i, heard.Words[i].Line, heard.Words[i].Text, est.Words[i].Line, est.Words[i].Text)
		}
	}
	if heard.Words[6].Text != "allow" || heard.Words[6].Start != 30 {
		t.Fatalf("the chorus word is where it is heard: %+v", heard.Words[6])
	}
}

func TestSectionsStartWhereTheirFirstWordIsHeard(t *testing.T) {
	m := Build(Input{Duration: 60, BPM: 120, Lyrics: heardLyrics, Heard: heardWords()})
	want := map[string]float64{"Intro": 0, "Verse": 11.6, "Chorus": 29.6}
	for _, s := range m.Sections[:3] {
		if math.Abs(s.Start-want[s.Tag]) > 0.45 {
			t.Errorf("%s starts at %.2f s, want about %.1f s: %+v", s.Tag, s.Start, want[s.Tag], m.Sections)
		}
	}
	if m.Sections[0].End != m.Sections[1].Start || m.Sections[2].End != m.Sections[3].Start || m.Sections[3].End != 60 {
		t.Fatalf("sections follow each other and end with the song: %+v", m.Sections)
	}
	if m.Sections[2].Start > 30 {
		t.Fatalf("the chorus must start at or before its first word (30 s): %+v", m.Sections[2])
	}
}

func TestWithoutWrittenLyricsTheLinesComeFromThePauses(t *testing.T) {
	heard := []transcribe.Word{hw("first", 1, 1.4), hw("line", 1.5, 1.9), hw("second", 4, 4.4), hw("line", 4.5, 4.9), hw("third", 4.95, 5.3)}
	m := Build(Input{Duration: 10, BPM: 100, Heard: heard})
	if len(m.Sections) != 1 || m.Sections[0].Tag != "Song" || m.Sections[0].End != 10 || m.WordsEstimated {
		t.Fatalf("%+v", m.Sections)
	}
	lines := []int{}
	for _, w := range m.Words {
		lines = append(lines, w.Line)
	}
	if !reflect.DeepEqual(lines, []int{0, 0, 1, 1, 1}) {
		t.Fatalf("lines %v", lines)
	}
}

func TestTheBeatsAndTheLoudnessDoNotChangeWithHeardWords(t *testing.T) {
	a := Build(Input{Duration: 60, BPM: 120, Lyrics: heardLyrics, Loudness: []float64{1, 2, 3}})
	b := Build(Input{Duration: 60, BPM: 120, Lyrics: heardLyrics, Loudness: []float64{1, 2, 3}, Heard: heardWords()})
	if !reflect.DeepEqual(a.Beats, b.Beats) || !reflect.DeepEqual(a.Loudness, b.Loudness) || a.BPM != b.BPM {
		t.Fatal("the beats and the loudness are the same")
	}
}

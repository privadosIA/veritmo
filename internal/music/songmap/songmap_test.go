package songmap

import (
	"math"
	"math/rand"
	"reflect"
	"testing"

	"veritmo/internal/music/transcribe"
)

const testRate = 8000

// clicks is a pulse: a short decaying burst every 60/bpm seconds from first, over a little noise.
func clicks(seconds, bpm, first float64) []float32 {
	rng := rand.New(rand.NewSource(1))
	out := make([]float32, int(seconds*testRate))
	for i := range out {
		out[i] = float32(rng.NormFloat64() * 0.005)
	}
	period := 60 / bpm
	for t := first; t < seconds; t += period {
		start := int(t * testRate)
		for j := 0; j < int(0.03*testRate) && start+j < len(out); j++ {
			out[start+j] += float32(0.8 * math.Exp(-float64(j)/60) * math.Sin(2*math.Pi*110*float64(j)/testRate))
		}
	}
	return out
}

const lyrics = "[Verse]\nfirst line of the song\nsecond line here\n[Chorus]\nallow allow allow\n"

func TestAMapOfASongWithLyricsHasEverything(t *testing.T) {
	samples := clicks(40, 120, 0.3)
	segs := []transcribe.Segment{{Start: 2, End: 4, Text: "first line of the song"}, {Start: 4, End: 6, Text: "second line here"}}
	m := FromSamples(samples, testRate, 120, lyrics, segs, "k")
	if m.BPM != 120 || m.Duration < 39.9 || m.Duration > 40.1 {
		t.Fatalf("bpm/duration: %+v", m)
	}
	if !m.GridAnchored || len(m.Beats) < 75 {
		t.Fatalf("beats: anchored=%v n=%d", m.GridAnchored, len(m.Beats))
	}
	if math.Abs(m.Beats[0]-0.3) > 0.06 {
		t.Fatalf("the first beat should fall near 0.3 s, got %v", m.Beats[0])
	}
	if d := m.Beats[1] - m.Beats[0]; math.Abs(d-0.5) > 1e-6 {
		t.Fatalf("beats are 0.5 s apart at 120 bpm, got %v", d)
	}
	if len(m.Sections) != 2 || m.Sections[0].Tag != "Verse" || m.Sections[1].Tag != "Chorus" {
		t.Fatalf("sections: %+v", m.Sections)
	}
	if m.Sections[0].Start != 0 || m.Sections[0].End != m.Sections[1].Start || m.Sections[1].End != m.Duration && math.Abs(m.Sections[1].End-m.Duration) > 0.002 {
		t.Fatalf("sections must cover the song with no gap: %+v", m.Sections)
	}
	if len(m.Words) != 8 || m.Words[0].Text != "first" || m.Words[0].Start != 2 {
		t.Fatalf("words: %+v", m.Words)
	}
	if last := m.Words[4]; last.Text != "song" || math.Abs(last.End-4) > 0.002 {
		t.Fatalf("the last word of a line ends with the line: %+v", last)
	}
	if len(m.Loudness) < 150 || m.LoudnessHop != 0.25 {
		t.Fatalf("loudness: %d values, hop %v", len(m.Loudness), m.LoudnessHop)
	}
}

func TestWordsOfALongerLetterCountTakeLonger(t *testing.T) {
	w := words([]transcribe.Segment{{Start: 0, End: 3, Text: "a extraordinary"}}, true)
	if len(w) != 2 || w[1].End-w[1].Start <= w[0].End-w[0].Start {
		t.Fatalf("%+v", w)
	}
}

func TestAnInstrumentalSongHasBeatsAndLoudnessAndNoWords(t *testing.T) {
	m := FromSamples(clicks(30, 100, 0.2), testRate, 100, "", nil, "k")
	if len(m.Words) != 0 || m.Words == nil {
		t.Fatalf("words should be an empty list: %#v", m.Words)
	}
	if len(m.Beats) == 0 || len(m.Loudness) == 0 {
		t.Fatalf("beats %d loudness %d", len(m.Beats), len(m.Loudness))
	}
	if len(m.Sections) != 1 || m.Sections[0].Tag != "Instrumental" || m.Sections[0].End < 29.9 {
		t.Fatalf("sections: %+v", m.Sections)
	}
}

func TestWithoutAPulseTheGridStartsAtZeroAndSaysSo(t *testing.T) {
	m := Build(Input{Duration: 10, BPM: 120})
	if m.GridAnchored || len(m.Beats) != 20 || m.Beats[0] != 0 {
		t.Fatalf("%+v", m)
	}
	if got := Build(Input{Duration: 10}); len(got.Beats) != 0 {
		t.Fatalf("no tempo, no beats: %+v", got.Beats)
	}
}

func TestTheSameInputsGiveTheSameMap(t *testing.T) {
	samples := clicks(30, 110, 0.4)
	a := FromSamples(samples, testRate, 110, lyrics, nil, "k")
	b := FromSamples(samples, testRate, 110, lyrics, nil, "k")
	if !reflect.DeepEqual(a, b) {
		t.Fatal("two maps of the same song differ")
	}
}

func TestACachedMapIsNotBuiltAgain(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	build := func() (Map, error) { calls++; return Build(Input{Duration: 10, BPM: 120}), nil }
	a, err := Cached(dir, "k1", false, build)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Cached(dir, "k1", false, build)
	if calls != 1 || !reflect.DeepEqual(a, b) {
		t.Fatalf("calls=%d equal=%v", calls, reflect.DeepEqual(a, b))
	}
	Cached(dir, "k2", false, build) // another audio: built again
	Cached(dir, "k2", true, build)  // asked for: built again
	if calls != 3 {
		t.Fatalf("calls=%d, want 3", calls)
	}
}

func TestKeyTellsAudiosApart(t *testing.T) {
	ka, kb, kaAgain := Key([]byte("a")), Key([]byte("b")), Key([]byte("a"))
	if ka == kb || ka != kaAgain {
		t.Fatal("key")
	}
}

func TestASliceIsShiftedAndKeepsTheBeatBefore(t *testing.T) {
	m := Build(Input{Duration: 20, BPM: 120, Lyrics: lyrics, Segments: []transcribe.Segment{{Start: 6, End: 8, Text: "one two"}, {Start: 12, End: 13, Text: "late"}}})
	s := Slice(m, 5.25, 10)
	if s.Duration != 4.75 || s.BPM != 120 {
		t.Fatalf("%+v", s)
	}
	if s.Beats[0] >= 0 || s.Beats[0] != -0.25 {
		t.Fatalf("the beat before the start (at 5.0 s) must be kept at -0.25: %v", s.Beats[:3])
	}
	if s.BeatBase != 10 {
		t.Fatalf("beat 5.0 s is the 10th (0-based) of the song: %d", s.BeatBase)
	}
	if len(s.Words) != 2 || s.Words[0].Start != 0.75 {
		t.Fatalf("words inside the range, shifted: %+v", s.Words)
	}
	for _, sec := range s.Sections {
		if sec.Start < 0 || sec.End > 4.75+1e-9 {
			t.Fatalf("sections are cut to the range: %+v", s.Sections)
		}
	}
	if got := Slice(m, 4, 4); len(got.Beats) != 0 || got.Duration != 0 {
		t.Fatalf("an empty range gives an empty map: %+v", got)
	}
	whole := Slice(m, 0, 20)
	if whole.BeatBase != 0 || whole.Beats[0] != 0 {
		t.Fatalf("%+v", whole)
	}
}

func TestSectionsStartWhereTheirFirstWordIsSung(t *testing.T) {
	m := Build(Input{Duration: 60, BPM: 120, Lyrics: "[Intro]\n[Verse]\nline one here\nline two here\n[Chorus]\nallow allow it\n[Verse]\nanother verse line\n",
		Segments: []transcribe.Segment{{Start: 12, End: 15, Text: "line one here"}, {Start: 16, End: 19, Text: "line two here"}, {Start: 30, End: 33, Text: "allow allow it"}, {Start: 45, End: 48, Text: "another verse line"}}})
	want := map[string]float64{"Intro": 0, "Verse": 11.6, "Chorus": 29.6}
	for _, s := range m.Sections[:3] {
		if math.Abs(s.Start-want[s.Tag]) > 0.5 {
			t.Errorf("%s starts at %.2f s, want about %.1f s: %+v", s.Tag, s.Start, want[s.Tag], m.Sections)
		}
	}
	if last := m.Sections[3]; last.Tag != "Verse" || math.Abs(last.Start-44.6) > 0.5 || last.End != 60 {
		t.Errorf("the last verse starts where its words do: %+v", m.Sections)
	}
}

func TestWithoutATranscriptionTheLinesCanBeSpreadOverTheirSections(t *testing.T) {
	m := Build(Input{Duration: 40, BPM: 120, EstimateWords: true, Lyrics: "[Verse]\nfirst line here\nsecond line here\n[Chorus]\nallow it now\n"})
	if !m.WordsEstimated || len(m.Words) != 9 {
		t.Fatalf("%+v", m.Words)
	}
	chorus := m.Sections[1]
	for _, w := range m.Words {
		if w.Line == 2 && (w.Start < chorus.Start || w.End > chorus.End) {
			t.Fatalf("the chorus line must sit inside the chorus %+v: %+v", chorus, w)
		}
	}
	if m.Words[0].Start < 0 || m.Words[3].Start < m.Words[2].End-1e-9 && m.Words[3].Line == m.Words[2].Line {
		t.Fatalf("%+v", m.Words)
	}
	plain := Build(Input{Duration: 40, BPM: 120, Lyrics: "[Verse]\nfirst line here\n"})
	if plain.WordsEstimated || len(plain.Words) != 0 {
		t.Fatal("estimating is asked for, never assumed")
	}
	if got := Build(Input{Duration: 40, BPM: 120, EstimateWords: true}); got.WordsEstimated {
		t.Fatal("no lyrics, nothing to estimate")
	}
}

func TestAnalyseFollowsTheTempoTheSongReallyHas(t *testing.T) {
	// Asked for 120 bpm, the song came out at 117.7: a grid at 120 would be half a beat off after a minute.
	samples := clicks(60, 117.7, 0.3)
	m := Analyse(samples, testRate, Input{BPM: 120})
	if math.Abs(m.BPM-117.7) > 0.8 || !m.GridAnchored {
		t.Fatalf("bpm %v anchored %v", m.BPM, m.GridAnchored)
	}
	// The last beats still fall on the clicks: beat k is at 0.3 + k*60/117.7.
	for _, k := range []int{5, 40, 90} {
		want := 0.3 + float64(k)*60/117.7
		best := 99.0
		for _, b := range m.Beats {
			best = math.Min(best, math.Abs(b-want))
		}
		if best > 0.05 {
			t.Fatalf("beat %d is %.0f ms from its click", k, best*1000)
		}
	}
	// With no pulse to follow, the plain grid is the answer.
	flat := Analyse(make([]float32, 30*testRate), testRate, Input{BPM: 120})
	if flat.GridAnchored || flat.BPM != 120 || len(flat.Beats) != 60 {
		t.Fatalf("%+v", flat.GridAnchored)
	}
}

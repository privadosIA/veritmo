package songmap

import (
	"encoding/json"
	"math"
	"os"
	"reflect"
	"testing"

	"veritmo/internal/music/separate"
	"veritmo/internal/music/transcribe"
)

// voiceOn is an envelope with the voice heard in the given [from, to) spans and silence elsewhere, up to total seconds.
func voiceOn(total float64, spans ...[2]float64) *separate.Env {
	n := int(math.Round(total / separate.Hop))
	v := make([]bool, n)
	for _, sp := range spans {
		for f := int(math.Round(sp[0] / separate.Hop)); f < int(math.Round(sp[1]/separate.Hop)) && f < n; f++ {
			v[f] = true
		}
	}
	return &separate.Env{Hop: separate.Hop, Voiced: v}
}

func sure(text string, start, end, conf float64) transcribe.Word {
	return transcribe.Word{Text: text, Start: start, End: end, Confidence: conf}
}

func TestAlignWords_AWordHeardOverTheDrumsIsLeftOut(t *testing.T) {
	written := WrittenWords("one two three")
	// "uh" is written by Whisper over a drum fill (no voice in 5-6 s) and sits where "two" is not sung
	heard := []transcribe.Word{sure("one", 1, 1.5, 0.9), sure("uh", 5, 5.5, 0.8), sure("two", 8, 8.5, 0.9), sure("three", 9, 9.5, 0.9)}
	env := voiceOn(12, [2]float64{1, 1.6}, [2]float64{8, 9.6})
	got := AlignWords(written, heard, env)
	if got[1].Start != 8 || got[2].Start != 9 {
		t.Fatalf("two and three keep their times: %+v", got)
	}
	// without the envelope the drum word is a mismatch that takes the place of nothing either, but it is still heard
	if got := AlignWords(written, heard, nil); got[1].Start == 8 && got[1].Heard {
		t.Logf("without the envelope: %+v", got) // documents today's behaviour; the order is kept either way
	}
}

func TestAlignWords_AGapWithAnInstrumentalBarIsFilledWhereTheVoiceIs(t *testing.T) {
	written := WrittenWords("start middle end")
	heard := []transcribe.Word{sure("start", 0, 1, 0.9), sure("end", 7, 8, 0.9)}
	// 6 s between them (1 to 7 s), voice only in the last 2 s of it
	env := voiceOn(9, [2]float64{0, 1}, [2]float64{5, 8})
	got := AlignWords(written, heard, env)
	mid := got[1]
	if mid.Heard || mid.Start < 5 || mid.End > 7 {
		t.Fatalf("the word must sit inside 5-7 s: %+v", mid)
	}
	// without an envelope it is spread over the whole gap
	if g := AlignWords(written, heard, nil)[1]; g.Start != 1 || g.End != 7 {
		t.Fatalf("no envelope: %+v", g)
	}
}

func TestAlignWords_NoVoiceBetweenTheNeighboursKeepsTheProportionalGap(t *testing.T) {
	written := WrittenWords("start middle end")
	heard := []transcribe.Word{sure("start", 0, 1, 0.9), sure("end", 7, 8, 0.9)}
	env := voiceOn(9, [2]float64{0, 1}, [2]float64{7, 8}) // silence all through the gap
	if g := AlignWords(written, heard, env)[1]; g.Start != 1 || g.End != 7 {
		t.Fatalf("with no voice to hold it the word is spread as without an envelope: %+v", g)
	}
}

func TestAlignWords_SeveralWordsAcrossAnInstrumentalBarDoNotLandInIt(t *testing.T) {
	written := WrittenWords("a b c d e")
	heard := []transcribe.Word{sure("a", 0, 1, 0.9), sure("e", 9, 10, 0.9)}
	// the voice is heard 1-2 s and 8-9 s: b, c and d share those two seconds, none starts in the silence 2-8 s
	env := voiceOn(11, [2]float64{0, 2}, [2]float64{8, 10})
	got := AlignWords(written, heard, env)
	for _, w := range got[1:4] {
		if w.Start >= 2 && w.Start < 8 {
			t.Fatalf("%q starts in the silence: %+v", w.Text, got)
		}
		if w.End < w.Start {
			t.Fatalf("%q ends before it starts: %+v", w.Text, got)
		}
	}
	for i := 1; i < len(got); i++ {
		if got[i].Start < got[i-1].Start {
			t.Fatalf("out of order: %+v", got)
		}
	}
}

func TestAlignWords_AnUnsureWordDoesNotDisplaceTheWrittenOrder(t *testing.T) {
	written := WrittenWords("the cat sat")
	// "dog" is not written; heard with doubt between "the" and "cat" it must not take the place of "cat"
	heard := []transcribe.Word{sure("the", 0, 0.5, 0.9), sure("dog", 1, 1.5, 0.2), sure("cat", 2, 2.5, 0.9), sure("sat", 3, 3.5, 0.9)}
	got := AlignWords(written, heard, nil)
	if got[1].Start != 2 || got[1].Confidence != 0.9 || got[2].Start != 3 {
		t.Fatalf("cat and sat keep their own times: %+v", got)
	}
	// the same word with full confidence is a sure substitution: "nine" written, "9" heard
	got = AlignWords(WrittenWords("monday nine am"), []transcribe.Word{sure("monday", 0, 1, 0.9), sure("9", 1, 1.5, 0.95), sure("am", 2, 2.5, 0.9)}, nil)
	if !got[1].Heard || got[1].Start != 1 {
		t.Fatalf("a sure word stands in for a written one spelt another way: %+v", got)
	}
	// and an unsure one does not
	got = AlignWords(WrittenWords("monday nine am"), []transcribe.Word{sure("monday", 0, 1, 0.9), sure("9", 1, 1.5, 0.2), sure("am", 2, 2.5, 0.9)}, nil)
	if got[1].Heard {
		t.Fatalf("an unsure word must not stand in for a written one: %+v", got)
	}
}

func TestAlignWords_CarriesTheConfidenceOfTheWordsItHeard(t *testing.T) {
	got := AlignWords(WrittenWords("a b"), []transcribe.Word{sure("a", 0, 1, 0.75)}, nil)
	if !got[0].Heard || got[0].Confidence != 0.75 || got[1].Heard || got[1].Confidence != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestAlignWords_WithAnEnvelopeIsDeterministic(t *testing.T) {
	written := WrittenWords("a b c d e f")
	heard := []transcribe.Word{sure("a", 0, 1, 0.9), sure("x", 3, 3.5, 0.5), sure("f", 9, 10, 0.9)}
	env := voiceOn(11, [2]float64{0, 2}, [2]float64{3, 3.6}, [2]float64{6, 10})
	first := AlignWords(written, heard, env)
	if second := AlignWords(written, heard, env); !reflect.DeepEqual(first, second) {
		t.Fatalf("two runs differ:\n%+v\n%+v", first, second)
	}
}

func TestSnapToBeats(t *testing.T) {
	beats := []float64{30, 30.5, 31, 31.5, 32}
	// just off the beat: moved, length kept
	w := []Word{{Text: "a", Start: 31.46, End: 31.7, Heard: true}}
	snapToBeats(w, beats, 120)
	if w[0].Start != 31.5 || math.Abs((w[0].End-w[0].Start)-0.24) > 1e-9 {
		t.Fatalf("snapped: %+v", w[0])
	}
	// a word that was interpolated is never moved
	w = []Word{{Text: "a", Start: 31.48, End: 31.7}}
	snapToBeats(w, beats, 120)
	if w[0].Start != 31.48 {
		t.Fatalf("interpolated moved: %+v", w[0])
	}
	// farther than the window stays
	w = []Word{{Text: "a", Start: 31.3, End: 31.4, Heard: true}}
	snapToBeats(w, beats, 120)
	if w[0].Start != 31.3 {
		t.Fatalf("too far: %+v", w[0])
	}
	// a snap that would cross the next word's start is refused
	w = []Word{{Text: "a", Start: 31.46, End: 31.48, Heard: true}, {Text: "b", Start: 31.49, End: 31.6, Heard: true}}
	snapToBeats(w, beats, 120)
	if w[0].Start != 31.46 {
		t.Fatalf("crossing snap done: %+v", w)
	}
	// ...and one that would go before the start of the word before it
	w = []Word{{Text: "a", Start: 31.57, End: 31.6, Heard: true}, {Text: "b", Start: 31.58, End: 31.8, Heard: true}}
	snapToBeats(w, beats, 120)
	if w[0].Start != 31.57 || w[1].Start != 31.58 {
		t.Fatalf("ordering: %+v", w)
	}
}

func TestSnapToBeats_HalfBeatsOnlyInSlowSongs(t *testing.T) {
	beats := []float64{0, 1, 2, 3} // 60 bpm
	w := []Word{{Text: "a", Start: 1.46, End: 1.8, Heard: true}}
	snapToBeats(w, beats, 60)
	if w[0].Start != 1.5 {
		t.Fatalf("a half beat is a place in a song under 100 bpm: %+v", w[0])
	}
	w = []Word{{Text: "a", Start: 1.46, End: 1.8, Heard: true}}
	snapToBeats(w, beats, 120)
	if w[0].Start != 1.46 {
		t.Fatalf("at 120 bpm only the beat counts: %+v", w[0])
	}
}

func TestWithHeard_SetsTheSourceSnapsAndGatesWithTheVoice(t *testing.T) {
	m := Map{Duration: 12, BPM: 120, GridAnchored: true, Beats: []float64{0, 0.5, 1, 1.5, 2, 2.5, 3, 3.5, 4}}
	heard := []transcribe.Word{sure("hello", 1.03, 1.4, 0.9), sure("world", 2.52, 2.9, 0.9)}
	got := WithHeard(m, "hello world", heard, nil, nil, SourceHeardVoice)
	if got.WordsSource != SourceHeardVoice || got.WordsEstimated {
		t.Fatalf("source %q estimated %v", got.WordsSource, got.WordsEstimated)
	}
	if got.Words[0].Start != 1 || got.Words[1].Start != 2.5 {
		t.Fatalf("heard words snap to the beat: %+v", got.Words)
	}
	if !got.Words[0].Heard || got.Words[0].Confidence != 0.9 {
		t.Fatalf("%+v", got.Words[0])
	}
	if got.Beats[1] != 0.5 || got.BPM != 120 {
		t.Fatal("beats and tempo stay")
	}
	// the default source is the mix
	if WithHeard(m, "hello world", heard, nil, nil, "").WordsSource != SourceHeard {
		t.Fatal("no source means the mix")
	}
	// a grid that was not found in the audio is not snapped to
	m.GridAnchored = false
	if got := WithHeard(m, "hello world", heard, nil, nil, SourceHeard); got.Words[0].Start != 1.03 {
		t.Fatalf("an unanchored grid must not move the words: %+v", got.Words[0])
	}
	// nothing heard gives the map back
	if got := WithHeard(m, "hello", nil, nil, nil, SourceHeard); !reflect.DeepEqual(got, m) {
		t.Fatal("no heard words, no change")
	}
}

func TestLoad_AMapOfThePreviousVersionIsUpgradedWithoutTouchingItsTimes(t *testing.T) {
	dir := t.TempDir()
	old := `{"version":1,"audio_key":"k","duration":10,"bpm":120,"beats":[0,0.5],"beat_base":0,"grid_anchored":true,
	"sections":[{"tag":"Verse","start":0,"end":10}],"words":[{"text":"hi","start":1.25,"end":1.5,"line":0}],
	"loudness_hop":0.25,"loudness":[0.1]}`
	if err := os.WriteFile(Path(dir), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	m, ok := Load(dir, "k")
	if !ok {
		t.Fatal("a map of the previous version must load")
	}
	if m.Version != Version || m.WordsSource != SourceHeard || !m.Words[0].Heard || m.Words[0].Confidence != 0 || m.Words[0].Start != 1.25 || m.Words[0].End != 1.5 {
		t.Fatalf("%+v", m)
	}
	est := `{"version":1,"audio_key":"k","duration":10,"bpm":120,"beats":[],"beat_base":0,"grid_anchored":false,
	"sections":[],"words":[{"text":"hi","start":1,"end":2,"line":0}],"words_estimated":true,"loudness_hop":0.25,"loudness":[]}`
	_ = os.WriteFile(Path(dir), []byte(est), 0o644)
	m, ok = Load(dir, "k")
	if !ok || m.WordsSource != SourceEstimated || m.Words[0].Heard || !m.WordsEstimated {
		t.Fatalf("estimated: %+v", m)
	}
	// another version is still refused
	_ = os.WriteFile(Path(dir), []byte(`{"version":9,"audio_key":"k"}`), 0o644)
	if _, ok := Load(dir, "k"); ok {
		t.Fatal("an unknown version must not load")
	}
}

func TestSaveLoad_KeepsTheWordFields(t *testing.T) {
	dir := t.TempDir()
	m := Map{Version: Version, AudioKey: "k", Duration: 5, Beats: []float64{}, Sections: []Section{}, Loudness: []float64{}, WordsSource: SourceHeardVoice,
		Words: []Word{{Text: "a", Start: 1, End: 2, Line: 0, Heard: true, Confidence: 0.8}}}
	if err := Save(dir, m); err != nil {
		t.Fatal(err)
	}
	got, ok := Load(dir, "k")
	if !ok || !reflect.DeepEqual(got, m) {
		t.Fatalf("round trip: %+v", got)
	}
	raw, _ := os.ReadFile(Path(dir))
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	if fields["words_source"] != "heard-voice" {
		t.Fatalf("words_source in the file: %s", raw)
	}
}

func TestBuild_SaysWhereTheWordsComeFrom(t *testing.T) {
	segs := []transcribe.Segment{{Start: 0, End: 2, Text: "hello there"}}
	if m := Build(Input{Duration: 10, BPM: 120, Lyrics: "[Verse]\nhello there", Segments: segs}); m.WordsSource != SourceHeard || !m.Words[0].Heard {
		t.Fatalf("segments: %q %+v", m.WordsSource, m.Words)
	}
	if m := Build(Input{Duration: 10, BPM: 120, Lyrics: "[Verse]\nhello there", EstimateWords: true}); m.WordsSource != SourceEstimated || !m.WordsEstimated || m.Words[0].Heard {
		t.Fatalf("estimated: %q %+v", m.WordsSource, m.Words)
	}
	if m := Build(Input{Duration: 10, BPM: 120}); m.WordsSource != "" {
		t.Fatalf("no words, no source: %q", m.WordsSource)
	}
	if m := Build(Input{Duration: 10, BPM: 120, Lyrics: "hello", Heard: []transcribe.Word{sure("hello", 1, 2, 0.9)}, HeardSource: SourceHeardVoice}); m.WordsSource != SourceHeardVoice {
		t.Fatalf("heard: %q", m.WordsSource)
	}
}

// --- the snap to the attacks of the voice ----------------------------------------------------------

func TestSnapToAttacks_ANearbyAttackMovesTheWordKeepingItsLength(t *testing.T) {
	w := []Word{{Text: "a", Start: 33.46, End: 33.9, Heard: true}}
	snapToAttacks(w, []float64{33.0, 33.38, 34.0})
	if w[0].Start != 33.38 || math.Abs((w[0].End-w[0].Start)-0.44) > 1e-9 || w[0].Snapped != SnappedAttack {
		t.Fatalf("%+v", w[0])
	}
	// too far: nothing happens
	w = []Word{{Text: "a", Start: 33.6, End: 33.9, Heard: true}}
	snapToAttacks(w, []float64{33.38, 34.0})
	if w[0].Start != 33.6 || w[0].Snapped != "" {
		t.Fatalf("an attack 220 ms away is out of reach: %+v", w[0])
	}
	// the nearest of two, on either side
	w = []Word{{Text: "a", Start: 10.0, End: 10.2, Heard: true}}
	snapToAttacks(w, []float64{9.93, 10.04})
	if w[0].Start != 10.04 {
		t.Fatalf("nearest attack: %+v", w[0])
	}
	// no attacks: untouched
	w = []Word{{Text: "a", Start: 10.0, End: 10.2, Heard: true}}
	snapToAttacks(w, nil)
	if w[0].Start != 10.0 || w[0].Snapped != "" {
		t.Fatalf("%+v", w[0])
	}
}

func TestSnapToAttacks_NeverTooCloseToTheNeighbours(t *testing.T) {
	// the attack at 5.04 would put the second word 20 ms after the first word's start
	w := []Word{{Text: "a", Start: 5.02, End: 5.3, Heard: true}, {Text: "b", Start: 5.1, End: 5.4, Heard: true}}
	snapToAttacks(w, []float64{5.04})
	if w[0].Snapped != SnappedAttack || w[1].Start != 5.1 || w[1].Snapped != "" {
		t.Fatalf("%+v", w)
	}
	// and before the next word: the attack would leave 30 ms to the word after
	w = []Word{{Text: "a", Start: 5.0, End: 5.2, Heard: true}, {Text: "b", Start: 5.06, End: 5.4, Heard: true}}
	snapToAttacks(w, []float64{5.03})
	if w[0].Start != 5.0 || w[0].Snapped != "" {
		t.Fatalf("a word too close to the next one stays: %+v", w[0])
	}
}

func TestSnapToAttacks_AnInterpolatedWordIsNeverMoved(t *testing.T) {
	w := []Word{{Text: "a", Start: 7.01, End: 7.3}}
	snapToAttacks(w, []float64{7.0})
	if w[0].Start != 7.01 || w[0].Snapped != "" {
		t.Fatalf("%+v", w[0])
	}
}

func TestWithHeard_AnAttackComesBeforeTheBeatAndMovesTheWordOnlyOnce(t *testing.T) {
	m := Map{Duration: 12, BPM: 120, GridAnchored: true, Beats: []float64{0, 0.5, 1, 1.5, 2, 2.5, 3, 3.5, 4}}
	// "hello" starts 40 ms before a beat but 100 ms after an attack: the attack wins (and the word is not moved again onto the beat);
	// "world" has no attack within 120 ms and is 30 ms off the beat at 2.5: the beat takes it
	heard := []transcribe.Word{sure("hello", 1.03, 1.4, 0.9), sure("world", 2.53, 2.9, 0.9)}
	got := WithHeard(m, "hello world", heard, nil, []float64{0.93, 3.5}, SourceHeardVoice)
	if got.Words[0].Start != 0.93 || got.Words[0].Snapped != SnappedAttack {
		t.Fatalf("hello: %+v", got.Words[0])
	}
	if got.Words[1].Start != 2.5 || got.Words[1].Snapped != SnappedBeat {
		t.Fatalf("world: %+v", got.Words[1])
	}
	if got.Attacks != 2 || got.WordsSource != SourceHeardVoice {
		t.Fatalf("attacks %d source %q", got.Attacks, got.WordsSource)
	}
	// without attacks nothing changes from before: the beat takes both
	got = WithHeard(m, "hello world", heard, nil, nil, SourceHeard)
	if got.Attacks != 0 || got.Words[0].Snapped != SnappedBeat || got.Words[0].Start != 1 {
		t.Fatalf("%+v attacks %d", got.Words[0], got.Attacks)
	}
}

func TestLoad_AnOldMapHasNoSnappedAndNoAttacks(t *testing.T) {
	dir := t.TempDir()
	old := `{"version":2,"audio_key":"k","duration":10,"bpm":120,"beats":[0,0.5],"beat_base":0,"grid_anchored":true,
	"sections":[{"tag":"Verse","start":0,"end":10}],"words":[{"text":"hi","start":1.25,"end":1.5,"line":0,"heard":true,"confidence":0.9}],
	"words_source":"heard-voice","loudness_hop":0.25,"loudness":[0.1]}`
	if err := os.WriteFile(Path(dir), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	m, ok := Load(dir, "k")
	if !ok || m.Attacks != 0 || m.Words[0].Snapped != "" || m.Words[0].Start != 1.25 {
		t.Fatalf("%v %+v", ok, m)
	}
}

func TestMatchedCount_CountsTheWrittenWordsAHearingRecognised(t *testing.T) {
	written := WrittenWords("one two three four")
	heard := []transcribe.Word{sure("one", 1, 1.5, 0.9), sure("three", 3, 3.5, 0.9), sure("four", 4, 4.5, 0.9)}
	if got := MatchedCount(written, heard, nil); got != 3 {
		t.Fatalf("matched %d, want 3 (two is not heard)", got)
	}
	if got := MatchedCount(written, nil, nil); got != 0 {
		t.Fatalf("nothing heard: %d", got)
	}
	// a word heard where the voice is not, with the envelope, is not a match
	env := voiceOn(6, [2]float64{1, 1.6}, [2]float64{4, 4.6})
	if got := MatchedCount(written, heard, env); got != 2 {
		t.Fatalf("matched %d, want 2 (three sits where the voice is not)", got)
	}
	if got := MatchedCount(nil, heard, nil); got != 0 {
		t.Fatalf("no lyrics: %d", got)
	}
}

package wordsbench

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/music/audiomeasure"
	"veritmo/internal/music/separate"
	"veritmo/internal/music/songmap"
	"veritmo/internal/music/transcribe"
	"veritmo/internal/platform/runner"
)

// heardJSON is what the fake whisper-cli writes: four words with the time of their tokens, which also reads as segments.
const heardJSON = `{"result":{"language":"en"},"transcription":[
 {"offsets":{"from":1000,"to":1500},"text":" one","tokens":[{"text":" one","offsets":{"from":1000,"to":1500},"p":0.9,"t_dtw":104}]},
 {"offsets":{"from":3000,"to":3500},"text":" two","tokens":[{"text":" two","offsets":{"from":3000,"to":3500},"p":0.9,"t_dtw":300}]},
 {"offsets":{"from":10000,"to":10500},"text":" three","tokens":[{"text":" three","offsets":{"from":10000,"to":10500},"p":0.9,"t_dtw":1002}]},
 {"offsets":{"from":12000,"to":12500},"text":" four","tokens":[{"text":" four","offsets":{"from":12000,"to":12500},"p":0.9,"t_dtw":1200}]}]}`

// project writes a music project with the given songs (a manifest and an audio file each).
func project(t *testing.T, root, id string, entries ...map[string]any) {
	t.Helper()
	dir := filepath.Join(root, id, "out", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := os.WriteFile(filepath.Join(root, id, e["file"].(string)), []byte("audio"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_ = dir
	raw, _ := json.Marshal(map[string]any{"tracks": entries})
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func track(proj, id, lyrics string, instrumental bool) map[string]any {
	return map[string]any{"track_id": id, "variant": 0, "file": "out/" + proj + "/" + id + ".mp3", "lyrics_sung": lyrics, "instrumental": instrumental, "vocal_language": "en", "title": id, "bpm": 120}
}

func TestSongs_TakesTheSongsWithWrittenLyrics(t *testing.T) {
	root := t.TempDir()
	project(t, root, "p", track("p", "a", "one two three four five", false), track("p", "b", "", false), track("p", "c", "la la la la la", true),
		track("p", "d", "six seven eight nine ten", false))
	project(t, root, "q", track("q", "e", "alpha beta gamma delta", false))
	// a track whose file is gone is left out
	if err := os.Remove(filepath.Join(root, "p", "out", "p", "d.mp3")); err != nil {
		t.Fatal(err)
	}
	got, err := Songs(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range got {
		ids = append(ids, s.Project+"/"+s.TrackID)
	}
	if strings.Join(ids, " ") != "p/a q/e" {
		t.Fatalf("songs = %v", ids)
	}
	if one, _ := Songs(root, 1); len(one) != 1 {
		t.Fatalf("the limit: %v", one)
	}
	// one project directly
	if got, _ := Songs(filepath.Join(root, "q"), 0); len(got) != 1 || got[0].TrackID != "e" || got[0].Language != "en" || got[0].BPM != 120 {
		t.Fatalf("%+v", got)
	}
	if got, _ := Songs(t.TempDir(), 0); len(got) != 0 {
		t.Fatal("an empty folder has no songs")
	}
}

// wavFile is a mono 16-bit wav of a voice that sings two seconds in every four, 20 s long, at 16 kHz.
func wavFile() []byte {
	const rate, secs = 16000, 20
	data := make([]byte, rate*secs*2)
	for i := 0; i < rate*secs; i++ {
		t := float64(i) / rate
		v := 0.0
		if math.Mod(t, 4) < 2 {
			v = 0.3 * math.Sin(2*math.Pi*220*t)
		}
		binary.LittleEndian.PutUint16(data[i*2:], uint16(int16(v*32767)))
	}
	h := make([]byte, 44)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+len(data)))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], 1)
	binary.LittleEndian.PutUint32(h[24:], rate)
	binary.LittleEndian.PutUint32(h[28:], rate*2)
	binary.LittleEndian.PutUint16(h[32:], 2)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(len(data)))
	return append(h, data...)
}

// fakeTools stands for ffmpeg, whisper-cli and demucs.
func fakeTools(withSeparator bool) (Tools, *runner.FakeRunner) {
	return fakeToolsHearing(withSeparator, func(string) string { return heardJSON })
}

// fakeToolsHearing is fakeTools with a whisper-cli that answers what hears gives for the file it is asked to read (mix16k.wav,
// voice16k.wav).
func fakeToolsHearing(withSeparator bool, hears func(file string) string) (Tools, *runner.FakeRunner) {
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		switch inv.Name {
		case "ffmpeg":
			last := inv.Args[len(inv.Args)-1]
			if last == "-" { // the samples of the song: 20 s of noise-free tone as f32le
				n := 20 * audiomeasure.Rate
				raw := make([]byte, n*4)
				for i := 0; i < n; i++ {
					binary.LittleEndian.PutUint32(raw[i*4:], math.Float32bits(float32(0.1*math.Sin(float64(i)/20))))
				}
				return runner.ScriptedResult{Stdout: raw}
			}
			return runner.ScriptedResult{Err: os.WriteFile(last, wavFile(), 0o644)}
		case "whisper-cli":
			file, out := "", ""
			for i, a := range inv.Args {
				switch a {
				case "-f":
					file = filepath.Base(inv.Args[i+1])
				case "-of":
					out = inv.Args[i+1]
				}
			}
			if out != "" {
				return runner.ScriptedResult{Err: os.WriteFile(out+".json", []byte(hears(file)), 0o644)}
			}
		case "demucs":
			for _, n := range []string{"target_0_drums.wav", "target_1_bass.wav", "target_2_other.wav", "target_3_vocals.wav"} {
				if err := os.WriteFile(filepath.Join(inv.Args[2], n), wavFile(), 0o644); err != nil {
					return runner.ScriptedResult{Err: err}
				}
			}
		}
		return runner.ScriptedResult{}
	})
	tools := Tools{Runner: fr, FFmpeg: "ffmpeg", Whisper: transcribe.Tool{Runner: fr, Binary: "whisper-cli", Model: "ggml-large-v3-turbo-q5_0.bin"}}
	if withSeparator {
		tools.Separator = &separate.Tool{Runner: fr, Binary: "demucs", Model: "m.bin"}
	}
	return tools, fr
}

// songsOf makes a project with the given tracks (id, lyrics) and lists its songs.
func songsOf(t *testing.T, tracks ...[2]string) []Song {
	t.Helper()
	root := t.TempDir()
	var entries []map[string]any
	for _, tr := range tracks {
		entries = append(entries, track("p", tr[0], tr[1], false))
	}
	project(t, root, "p", entries...)
	songs, err := Songs(root, 0)
	if err != nil || len(songs) != len(tracks) {
		t.Fatalf("%v %v", songs, err)
	}
	return songs
}

func TestRun_MeasuresFourWaysAgainstTheAttacksAndOnlyDistinctSongsCount(t *testing.T) {
	tools, fr := fakeTools(true)
	songs := songsOf(t, [2]string{"a", "one two three four"}, [2]string{"b", "One, two; three four!"}, [2]string{"c", "five six seven eight"})
	rep := Run(context.Background(), tools, songs, nil)
	if len(rep.Songs) != 3 {
		t.Fatalf("%+v", rep.Songs)
	}
	for _, s := range rep.Songs {
		if s.Error != "" {
			t.Fatalf("%s: %s", s.TrackID, s.Error)
		}
	}
	s := rep.Songs[0]
	if len(s.Ways) != 4 || s.Ways[0].Way != WaySegments || s.Ways[1].Way != WayTokensMix || s.Ways[2].Way != WayTokensVoice || s.Ways[3].Way != WayTokensVoiceSnapped {
		t.Fatalf("ways: %+v", s.Ways)
	}
	if len(s.Attacks) < 3 || len(s.Beats) == 0 || s.BPM <= 0 || len(s.Ways[3].Words) != 4 {
		t.Fatalf("attacks=%d beats=%d bpm=%v words=%d", len(s.Attacks), len(s.Beats), s.BPM, len(s.Ways[3].Words))
	}
	// the second song has the lyrics of the first with other punctuation and case: a repeat
	if rep.Songs[0].SameAs != "" || rep.Songs[1].SameAs != "p/a" || rep.Songs[2].SameAs != "" {
		t.Fatalf("same as: %q %q %q", rep.Songs[0].SameAs, rep.Songs[1].SameAs, rep.Songs[2].SameAs)
	}
	if rep.Summary.Songs != 3 || rep.Summary.Distinct != 2 || rep.Summary.Decision != "not decided" || !strings.Contains(rep.Summary.Reason, "needs 8") {
		t.Fatalf("%+v", rep.Summary)
	}
	// the voice is separated once per song, Whisper heard three times (the snapped way reuses the voice words)
	count := map[string]int{}
	for _, c := range fr.Calls() {
		count[c.Name]++
	}
	if count["demucs"] != 3 || count["whisper-cli"] != 9 {
		t.Fatalf("calls: %v", count)
	}
	// the words that took the time of an attack say so, and the snapped way has at least one
	snapped := 0
	for _, w := range s.Ways[3].Words {
		if w.Snapped == "attack" {
			snapped++
		}
	}
	if snapped == 0 {
		t.Fatalf("no word was moved onto an attack: %+v", s.Ways[3].Words)
	}
	table := Table(rep)
	for _, want := range []string{"segments-mix", "tokens-mix", "tokens-voice-snapped", "same lyrics as p/a: not counted", "Median over 2 distinct songs", "decision: not decided"} {
		if !strings.Contains(table, want) {
			t.Errorf("the table lacks %q:\n%s", want, table)
		}
	}
}

func TestRecompute_GivesTheSameNumbersFromTheJSONAlone(t *testing.T) {
	tools, _ := fakeTools(true)
	songs := songsOf(t, [2]string{"a", "one two three four"}, [2]string{"c", "five six seven eight"})
	rep := Run(context.Background(), tools, songs, nil)
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	var back Report
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	again, err := json.Marshal(Recompute(back))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(again) {
		t.Fatalf("a report measured again from its JSON must be the same:\n%s\n%s", raw, again)
	}
}

func TestRun_WithoutTheSeparatorItSaysItCannotMeasureAndHearsNothing(t *testing.T) {
	tools, fr := fakeTools(false)
	rep := Run(context.Background(), tools, songsOf(t, [2]string{"a", "one two three four"}), nil)
	if len(rep.Songs) != 0 || len(rep.Notes) != 1 || !strings.Contains(rep.Notes[0], "cannot be measured against the voice") || rep.Summary.Decision != "not decided" {
		t.Fatalf("%+v", rep)
	}
	if len(fr.Calls()) != 0 {
		t.Fatalf("nothing must be run: %v", fr.Calls())
	}
	if !strings.Contains(Table(rep), "NOTE:") {
		t.Fatal("the table carries the note")
	}
}

func TestRun_ASongThatFailsDoesNotStopTheOthers(t *testing.T) {
	tools, fr := fakeTools(true)
	songs := songsOf(t, [2]string{"a", "one two three four"}, [2]string{"c", "five six seven eight"})
	failing := runner.NewFakeRunner()
	calls := 0
	failing.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if inv.Name == "ffmpeg" && inv.Args[len(inv.Args)-1] == "-" {
			calls++
			if calls == 1 {
				return runner.ScriptedResult{Err: os.ErrInvalid}
			}
		}
		out, err := fr.Run(context.Background(), inv.Name, inv.Args)
		return runner.ScriptedResult{Stdout: out, Err: err}
	})
	tools.Runner = failing
	rep := Run(context.Background(), tools, songs, nil)
	if rep.Songs[0].Error == "" || rep.Songs[1].Error != "" || len(rep.Songs[1].Ways) != 4 {
		t.Fatalf("%+v", rep.Songs)
	}
	if rep.Summary.Songs != 1 || !strings.Contains(Table(rep), "failed:") {
		t.Fatalf("the failed song is shown and the summary uses the other: %+v", rep.Summary)
	}
}

func TestMeasure_OffsetsSharesOrphansAndBeats(t *testing.T) {
	words := []songmap.Word{
		{Text: "a", Start: 0.98, End: 1.3, Heard: true},
		{Text: "b", Start: 2.12, End: 2.5, Heard: true},
		{Text: "c", Start: 3.5, End: 3.9, Heard: true},
		{Text: "d", Start: 5.0, End: 5.2},
	}
	attacks := []float64{1.0, 2.0, 3.0, 10.0}
	m, rows := Measure(words, attacks, []float64{1.0, 2.0, 3.0}, 60)
	if m.Words != 4 || m.Heard != 3 || m.InSpan != 3 || m.WithAttack != 2 {
		t.Fatalf("counts: %+v", m)
	}
	// offsets: attack minus start = +20 ms and -120 ms; "c" has no attack within 300 ms
	if rows[0].OffsetMs == nil || *rows[0].OffsetMs != 20 || rows[1].OffsetMs == nil || *rows[1].OffsetMs != -120 || rows[2].OffsetMs != nil || rows[3].OffsetMs != nil {
		t.Fatalf("offsets: %+v", rows)
	}
	if *m.MedianAbsMs != 70 || *m.SignedMedianMs != -50 {
		t.Fatalf("medians: %v %v", *m.MedianAbsMs, *m.SignedMedianMs)
	}
	if m.Within50 != 33.3 || m.Within100 != 33.3 || m.Within150 != 66.7 {
		t.Fatalf("shares: %v %v %v", m.Within50, m.Within100, m.Within150)
	}
	// the attack at 3.0 s has no word start within 300 ms (the nearest is 3.5); the one at 10 s is outside the span
	if m.OrphanAttacks != 1 {
		t.Fatalf("orphans = %d", m.OrphanAttacks)
	}
	// one of four starts is within 60 ms of a beat; chance at 60 bpm is 2 x 60 ms / 1 s = 12 %
	if m.BeatHit != 25 || m.BeatChance != 12 {
		t.Fatalf("beat %v chance %v", m.BeatHit, m.BeatChance)
	}
	empty, _ := Measure(nil, attacks, nil, 0)
	if empty.Words != 0 || empty.MedianAbsMs != nil {
		t.Fatalf("%+v", empty)
	}
}

// resultOf is a measured song whose voice way has the given share within 50 ms before and after snapping.
func resultOf(i int, before, after, voiceSeconds float64) SongResult {
	mk := func(way string, w50, seconds float64) WayResult {
		return WayResult{Way: way, Timing: "tokens", Seconds: seconds, Metrics: Metrics{Words: 100, Heard: 90, InSpan: 90, Within50: w50}}
	}
	return SongResult{Song: Song{TrackID: fmt.Sprintf("t%d", i)}, Seconds: 100, LyricsKey: fmt.Sprint(i),
		Ways: []WayResult{mk(WayTokensMix, before-10, 20), mk(WayTokensVoice, before, voiceSeconds), mk(WayTokensVoiceSnapped, after, voiceSeconds)}}
}

func TestSummarise_TheDecisionRule(t *testing.T) {
	songs := func(n int, before, after, seconds float64) []SongResult {
		var out []SongResult
		for i := 0; i < n; i++ {
			out = append(out, resultOf(i, before, after, seconds))
		}
		return out
	}
	if d := Summarise(songs(8, 30, 55, 90)); d.Decision != "auto" || *d.GainPoints != 25 {
		t.Fatalf("a gain of 25 points in 0.9 times: %+v", d)
	}
	if d := Summarise(songs(8, 30, 45, 90)); d.Decision != "false" || !strings.Contains(d.Reason, "needs 20") {
		t.Fatalf("a gain of 15 points: %+v", d)
	}
	if d := Summarise(songs(8, 30, 70, 350)); d.Decision != "false" || !strings.Contains(d.Reason, "under 3") {
		t.Fatalf("too slow: %+v", d)
	}
	if d := Summarise(songs(7, 30, 70, 90)); d.Decision != "not decided" {
		t.Fatalf("seven songs are not eight: %+v", d)
	}
	// eight songs of which four share their lyrics are four distinct ones
	rep := songs(8, 30, 70, 90)
	for i := 4; i < 8; i++ {
		rep[i].SameAs = "t0"
	}
	if d := Summarise(rep); d.Decision != "not decided" || d.Distinct != 4 || d.Songs != 8 {
		t.Fatalf("%+v", d)
	}
	if d := Summarise(nil); d.Decision != "not decided" {
		t.Fatalf("%+v", d)
	}
}

func TestLyricsKey_IgnoresCasePunctuationAndSpaces(t *testing.T) {
	if LyricsKey("Hello, World!\nSecond line") != LyricsKey("hello world second   line") {
		t.Fatal("the same words are the same lyrics")
	}
	if LyricsKey("hello world") == LyricsKey("hello there") {
		t.Fatal("other words are other lyrics")
	}
}

// words at 0.5, 4.5, 8.5 and 12.5 s sit where the fake voice sings (0-2, 4-6, 8-10, 12-14 s).
func fourWords(words ...string) string {
	at := map[string]int{"one": 500, "two": 4500, "three": 8500, "four": 12500}
	var segs []string
	for _, w := range words {
		segs = append(segs, fmt.Sprintf(`{"offsets":{"from":%d,"to":%d},"text":" %s","tokens":[{"text":" %s","offsets":{"from":%d,"to":%d},"p":0.9,"t_dtw":%d}]}`,
			at[w], at[w]+500, w, w, at[w], at[w]+500, at[w]/10))
	}
	return `{"result":{"language":"en"},"transcription":[` + strings.Join(segs, ",") + `]}`
}

func TestRun_ReportsWhetherTheFallbackToTheMixWouldTriggerAndWhatItKeeps(t *testing.T) {
	tools, _ := fakeToolsHearing(true, func(file string) string {
		if file == "voice16k.wav" {
			return fourWords("one") // the stem loses three of the four words
		}
		return fourWords("one", "two", "three", "four")
	})
	lost := Run(context.Background(), tools, songsOf(t, [2]string{"a", "one two three four"}), nil)
	f := lost.Songs[0].Fallback
	if f == nil || f.Voice != 1 || f.Mix != 4 || f.Written != 4 || f.Kept != KeptMix || !f.Triggered() {
		t.Fatalf("%+v", f)
	}
	if lost.Summary.Fallbacks != 1 {
		t.Fatalf("the summary counts the songs where it triggers: %+v", lost.Summary)
	}
	table := Table(lost)
	for _, want := range []string{"fallback: voice matched 1/4, mix 4/4 → mix", "fallback to the mix (the voice matched under 75 % of the written words): 1 of 1 distinct songs"} {
		if !strings.Contains(table, want) {
			t.Errorf("the table lacks %q:\n%s", want, table)
		}
	}
	// the JSON carries it, and measuring the report again from the JSON keeps it
	raw, _ := json.Marshal(lost)
	var back Report
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if again := Recompute(back); again.Songs[0].Fallback == nil || *again.Songs[0].Fallback != *f || again.Summary.Fallbacks != 1 {
		t.Fatalf("%+v", again.Songs[0].Fallback)
	}

	enough, _ := fakeToolsHearing(true, func(string) string { return fourWords("one", "two", "three", "four") })
	ok := Run(context.Background(), enough, songsOf(t, [2]string{"a", "one two three four"}), nil)
	f = ok.Songs[0].Fallback
	if f == nil || f.Kept != KeptNotNeeded || f.Triggered() || ok.Summary.Fallbacks != 0 {
		t.Fatalf("%+v %+v", f, ok.Summary)
	}
	if table := Table(ok); !strings.Contains(table, "fallback: not needed (voice 4/4)") {
		t.Fatalf("%s", table)
	}

	// both hearings lose the same words: the voice is kept, and it is said
	same, _ := fakeToolsHearing(true, func(string) string { return fourWords("one") })
	tie := Run(context.Background(), same, songsOf(t, [2]string{"a", "one two three four"}), nil)
	if f := tie.Songs[0].Fallback; f == nil || f.Kept != KeptVoice || !f.Triggered() {
		t.Fatalf("%+v", f)
	}
}

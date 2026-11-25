package transcribe

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"veritmo/internal/platform/runner"
)

func TestCleanDropsLoopsAndNoise(t *testing.T) {
	var in []Segment
	for i := 0; i < 12; i++ {
		in = append(in, Segment{Text: " Oh, la la "})
	}
	in = append(in, Segment{Text: "[Music]"}, Segment{Text: " ♪ "}, Segment{Text: ""}, Segment{Text: "(música) hola"})
	got, dropped := Clean(in)
	if len(got) != 4 || dropped != 12 { // 3 kept of the loop + "(música) hola"; 9 + 3 dropped
		t.Fatalf("kept %d dropped %d: %+v", len(got), dropped, got)
	}
}

func TestCleanKeepsARepeatedChorusWithVerseBetween(t *testing.T) {
	in := []Segment{{Text: "a b"}, {Text: "a b"}, {Text: "a b"}, {Text: "verse"}, {Text: "a b"}, {Text: "a b"}}
	got, dropped := Clean(in)
	if len(got) != 6 || dropped != 0 {
		t.Fatalf("kept %d dropped %d", len(got), dropped)
	}
}

const sampleJSON = `{"result":{"language":"es"},"transcription":[
 {"offsets":{"from":0,"to":2500},"text":" Camino por la calle"},
 {"offsets":{"from":2500,"to":5000},"text":" bajo la lluvia"}]}`

func TestRunPassesTheFlagsAndReadsTheJSON(t *testing.T) {
	var gotArgs []string
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		gotArgs = inv.Args
		for i, a := range inv.Args {
			if a == "-of" {
				if err := os.WriteFile(inv.Args[i+1]+".json", []byte(sampleJSON), 0o644); err != nil {
					return runner.ScriptedResult{Err: err}
				}
			}
		}
		return runner.ScriptedResult{}
	})
	tool := Tool{Runner: fr, Binary: "whisper-cli", Model: filepath.Join("m", "ggml-x.bin")}
	res, err := tool.Run(context.Background(), "a.wav", "")
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(gotArgs, " ")
	for _, want := range []string{"-mc 0", "-l auto", "-oj", "-np"} {
		if !strings.Contains(line, want) {
			t.Errorf("args %q lack %q", line, want)
		}
	}
	if strings.Contains(line, "--vad") {
		t.Errorf("the voice-activity filter must not be used: %q", line)
	}
	if res.Language != "es" || !res.LanguageDetected || res.Text != "Camino por la calle\nbajo la lluvia" || len(res.Segments) != 2 || res.Segments[1].Start != 2.5 || res.Model != "ggml-x.bin" {
		t.Fatalf("%+v", res)
	}
}

func TestRunWithTheLanguageGiven(t *testing.T) {
	var gotArgs []string
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		gotArgs = inv.Args
		for i, a := range inv.Args {
			if a == "-of" {
				_ = os.WriteFile(inv.Args[i+1]+".json", []byte(`{"result":{"language":"en"},"transcription":[]}`), 0o644)
			}
		}
		return runner.ScriptedResult{}
	})
	tool := Tool{Runner: fr, Binary: "whisper-cli", Model: "m.bin"}
	res, err := tool.Run(context.Background(), "a.wav", "en")
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(gotArgs, " ")
	if !strings.Contains(line, "-l en") || strings.Contains(line, "-l auto") {
		t.Errorf("args %q", line)
	}
	if res.LanguageDetected || res.Language != "en" || res.Text != "" || len(res.Segments) != 0 {
		t.Fatalf("an instrumental source must be a valid empty result: %+v", res)
	}
}

func TestKeepRepeatsKeepsASpokenLineSaidManyTimes(t *testing.T) {
	raw := []byte(`{"result":{"language":"en"},"transcription":[
 {"offsets":{"from":0,"to":1000},"text":" more"},{"offsets":{"from":1000,"to":2000},"text":" more"},
 {"offsets":{"from":2000,"to":3000},"text":" more"},{"offsets":{"from":3000,"to":4000},"text":" more"}]}`)
	song, err := parse(raw, false)
	if err != nil || len(song.Segments) != maxRepeat || song.Dropped != 1 {
		t.Fatalf("a song keeps %d repeats: %+v %v", maxRepeat, song, err)
	}
	said, err := parse(raw, true)
	if err != nil || len(said.Segments) != 4 || said.Dropped != 0 {
		t.Fatalf("a dictation keeps them all: %+v %v", said, err)
	}
}

const wordsJSON = `{"result":{"language":"en"},"transcription":[
 {"offsets":{"from":0,"to":0},"text":""},
 {"offsets":{"from":0,"to":440},"text":" Monday"},
 {"offsets":{"from":440,"to":1100},"text":" morning,"},
 {"offsets":{"from":1100,"to":1320},"text":" 9"},
 {"offsets":{"from":1320,"to":1940},"text":" a.m."},
 {"offsets":{"from":1940,"to":1940},"text":" ghost"},
 {"offsets":{"from":2000,"to":2300},"text":" [Music]"},
 {"offsets":{"from":2300,"to":2400},"text":" ♪"},
 {"offsets":{"from":2400,"to":2600},"text":" (applause)"},
 {"offsets":{"from":2600,"to":2900},"text":" The"}]}`

func TestRunWordsPassesTheWordFlagsAndReadsEveryWord(t *testing.T) {
	var gotArgs []string
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		gotArgs = inv.Args
		for i, a := range inv.Args {
			if a == "-of" {
				if err := os.WriteFile(inv.Args[i+1]+".json", []byte(wordsJSON), 0o644); err != nil {
					return runner.ScriptedResult{Err: err}
				}
			}
		}
		return runner.ScriptedResult{}
	})
	heard, err := (Tool{Runner: fr, Binary: "whisper-cli", Model: "m.bin"}).RunWords(context.Background(), "a.wav", "")
	if err != nil {
		t.Fatal(err)
	}
	words := heard.Words
	if heard.Timing != TimingSegments {
		t.Fatalf("a model with no DTW preset is timed by segments: %q", heard.Timing)
	}
	line := strings.Join(gotArgs, " ")
	for _, want := range []string{"-ml 1", "-sow", "-mc 0", "-l auto", "-oj"} {
		if !strings.Contains(line, want) {
			t.Errorf("args %q lack %q", line, want)
		}
	}
	var got []string
	for _, w := range words {
		got = append(got, w.Text)
		if w.End <= w.Start {
			t.Errorf("%q ends before it starts", w.Text)
		}
	}
	// The empty token, the word with no length, the bracketed sounds and the note are gone; the number stays.
	if strings.Join(got, "|") != "Monday|morning,|9|a.m.|The" {
		t.Fatalf("words: %v", got)
	}
	if words[0].Start != 0 || words[1].Start != 0.44 || words[1].End != 1.1 {
		t.Fatalf("times in seconds: %+v", words[:2])
	}
	for i := 1; i < len(words); i++ {
		if words[i].Start < words[i-1].Start {
			t.Fatal("the words must come in order")
		}
	}
}

func TestRunWordsDropsALongLoopOnASilenceAndKeepsAShortOne(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"result":{"language":"en"},"transcription":[`)
	for i := 0; i < 5; i++ { // a short "oh oh oh oh oh" is sung
		b.WriteString(`{"offsets":{"from":` + itoa(i*100) + `,"to":` + itoa(i*100+90) + `},"text":" oh"},`)
	}
	for i := 0; i < 30; i++ { // forty times is Whisper stuck on a held note
		b.WriteString(`{"offsets":{"from":` + itoa(1000+i*100) + `,"to":` + itoa(1000+i*100+90) + `},"text":" you"},`)
	}
	b.WriteString(`{"offsets":{"from":9000,"to":9500},"text":" end"}]}`)
	ws, err := parseWords([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	oh, you := 0, 0
	for _, w := range ws {
		switch strings.TrimSpace(w.Text) {
		case "oh":
			oh++
		case "you":
			you++
		}
	}
	if oh != 5 || you != maxWordRepeat || ws[len(ws)-1].Text != "end" {
		t.Fatalf("oh=%d you=%d last=%q", oh, you, ws[len(ws)-1].Text)
	}
}

func TestRunWordsOfASongWithNoWordsIsAnEmptyListNotAnError(t *testing.T) {
	ws, err := parseWords([]byte(`{"result":{"language":"en"},"transcription":[{"offsets":{"from":0,"to":0},"text":""}]}`))
	if err != nil || ws == nil || len(ws) != 0 {
		t.Fatalf("%v %v", ws, err)
	}
	if _, err := parseWords([]byte("not json")); err == nil {
		t.Fatal("a result that cannot be read is an error")
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

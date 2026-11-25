package transcribe

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"

	"veritmo/internal/platform/runner"
)

func TestDtwPresetOfTheNameOfTheModel(t *testing.T) {
	for name, want := range map[string]string{
		"ggml-large-v3-turbo.bin":                     "large.v3.turbo",
		"ggml-large-v3-turbo-q5_0.bin":                "large.v3.turbo",
		"models/whisper/ggml-large-v3-turbo-q8_0.bin": "large.v3.turbo",
		"ggml-large-v3.bin":                           "large.v3",
		"ggml-large-v3-q5_0.bin":                      "large.v3",
		"ggml-large-v2.bin":                           "large.v2",
		"ggml-large-v1.bin":                           "large.v1",
		"ggml-medium.bin":                             "medium",
		"ggml-medium.en.bin":                          "medium.en",
		"ggml-small.bin":                              "small",
		"ggml-small.en-q5_1.bin":                      "small.en",
		"ggml-base.bin":                               "base",
		"ggml-base.en.bin":                            "base.en",
		"ggml-tiny.bin":                               "tiny",
		"ggml-tiny.en.bin":                            "tiny.en",
		"ggml-something-else.bin":                     "",
		"m.bin":                                       "",
		"":                                            "",
	} {
		if got := dtwPreset(name); got != want {
			t.Errorf("%q: %q, want %q", name, got, want)
		}
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// A real run of the pinned whisper-cli (-nfa -ojf -dtw large.v3.turbo, model large-v3-turbo-q5_0) on 20 s of a sung song.
func TestTheWordsOfARealRunHaveTheTimesOfTheirFirstToken(t *testing.T) {
	words, ok, err := parseTokens(fixture(t, "whisper-full-dtw.json"))
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	var got []string
	for _, w := range words[:6] {
		got = append(got, w.Text)
	}
	// "Es" + "cribe" are one word; the comma sticks to "noche"; the special tokens are not text
	if strings.Join(got, "|") != "Escribe|la|noche,|escribe|el|día" {
		t.Fatalf("%v", got)
	}
	for i, want := range []float64{0.58, 3.04, 3.70, 4.36, 5.06, 5.58} {
		if math.Abs(words[i].Start-want) > 1e-9 {
			t.Fatalf("word %d (%s) starts at %v, want %v (the DTW time of its first token)", i, words[i].Text, words[i].Start, want)
		}
	}
	// its end is where the next word starts
	if words[0].End != 3.04 || words[2].End != 4.36 {
		t.Fatalf("ends: %+v", words[:3])
	}
	// the confidence is the geometric mean of the probabilities of its tokens (" Es" 0.23 and "cribe" 0.84 in this run)
	if c := words[0].Confidence; c < 0.42 || c > 0.45 {
		t.Fatalf("confidence of Escribe: %v", c)
	}
	last := words[len(words)-1]
	prev := -1.0
	for _, w := range words {
		if w.Start < prev || w.End < w.Start+minWordLength-1e-9 || w.Confidence < minConfidence || w.Confidence > 1 {
			t.Fatalf("a word out of order, with no length or with no confidence: %+v", w)
		}
		prev = w.Start
	}
	if last.End > 20.1 {
		t.Fatalf("the last word ends inside the 20 s of the clip: %+v", last)
	}
}

// The same clip run with flash attention on (the default of whisper-cli): every token has t_dtw -1.
func TestARunWithNoTokenTimesIsNotTimedByTokens(t *testing.T) {
	if _, ok, err := parseTokens(fixture(t, "whisper-full-nodtw.json")); err != nil || ok {
		t.Fatalf("%v %v", ok, err)
	}
}

func fakeWhisper(t *testing.T, outputs ...string) (*runner.FakeRunner, *[][]string) {
	t.Helper()
	var calls [][]string
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		calls = append(calls, inv.Args)
		out := outputs[min(len(calls), len(outputs))-1]
		for i, a := range inv.Args {
			if a == "-of" {
				if err := os.WriteFile(inv.Args[i+1]+".json", []byte(out), 0o644); err != nil {
					return runner.ScriptedResult{Err: err}
				}
			}
		}
		return runner.ScriptedResult{}
	})
	return fr, &calls
}

func TestRunWordsAsksForTokenTimesWhenTheModelHasAPreset(t *testing.T) {
	fr, calls := fakeWhisper(t, string(fixture(t, "whisper-full-dtw.json")))
	heard, err := (Tool{Runner: fr, Binary: "whisper-cli", Model: "models/ggml-large-v3-turbo-q5_0.bin"}).RunWords(context.Background(), "a.wav", "es")
	if err != nil || heard.Timing != TimingTokens || len(heard.Words) < 20 {
		t.Fatalf("%+v %v", heard.Timing, err)
	}
	if len(*calls) != 1 {
		t.Fatalf("one call: %d", len(*calls))
	}
	line := strings.Join((*calls)[0], " ")
	for _, want := range []string{"-dtw large.v3.turbo", "-ojf", "-nfa", "-mc 0", "-l es", "-np"} {
		if !strings.Contains(line, want) {
			t.Errorf("args %q lack %q", line, want)
		}
	}
	for _, not := range []string{"-ml", "-sow"} {
		if strings.Contains(line, not) {
			t.Errorf("args %q have %q: the output is not cut in words", line, not)
		}
	}
}

func TestRunWordsFallsBackToSegmentsWhenNoTokenHasATime(t *testing.T) {
	fr, calls := fakeWhisper(t, string(fixture(t, "whisper-full-nodtw.json")), wordsJSON)
	heard, err := (Tool{Runner: fr, Binary: "whisper-cli", Model: "ggml-large-v3-turbo-q5_0.bin"}).RunWords(context.Background(), "a.wav", "")
	if err != nil || heard.Timing != TimingSegments || len(heard.Words) != 5 {
		t.Fatalf("%+v %v", heard, err)
	}
	if len(*calls) != 2 || !strings.Contains(strings.Join((*calls)[0], " "), "-dtw") || !strings.Contains(strings.Join((*calls)[1], " "), "-ml 1 -sow") {
		t.Fatalf("the token call and then today's: %v", *calls)
	}
}

func TestAWordTheModelDoesNotBelieveIsDropped(t *testing.T) {
	raw := `{"transcription":[{"offsets":{"from":0,"to":4000},"text":" sure maybe not","tokens":[
	 {"text":" sure","offsets":{"from":100,"to":500},"p":0.95,"t_dtw":50},
	 {"text":" maybe","offsets":{"from":500,"to":900},"p":0.04,"t_dtw":140},
	 {"text":" not","offsets":{"from":900,"to":1200},"p":0.9,"t_dtw":210}]}]}`
	words, ok, err := parseTokens([]byte(raw))
	if err != nil || !ok || len(words) != 2 || words[0].Text != "sure" || words[1].Text != "not" {
		t.Fatalf("%+v %v %v", words, ok, err)
	}
	if words[0].End != 1.4 {
		t.Fatalf("a word ends where the next token-word starts, even one that is then dropped: %+v", words[0])
	}
}

func TestBracketedTokensAndSpecialTokensAreNotWords(t *testing.T) {
	raw := `{"transcription":[{"offsets":{"from":0,"to":6000},"text":" [Music] hey","tokens":[
	 {"text":"[_BEG_]","offsets":{"from":0,"to":0},"p":0.5,"t_dtw":-1},
	 {"text":" [","offsets":{"from":0,"to":100},"p":0.9,"t_dtw":10},
	 {"text":"Music","offsets":{"from":100,"to":900},"p":0.9,"t_dtw":50},
	 {"text":"]","offsets":{"from":900,"to":1000},"p":0.9,"t_dtw":90},
	 {"text":" hey","offsets":{"from":3000,"to":3400},"p":0.8,"t_dtw":310},
	 {"text":"[_TT_300]","offsets":{"from":6000,"to":6000},"p":0.3,"t_dtw":-1}]}]}`
	words, ok, err := parseTokens([]byte(raw))
	if err != nil || !ok || len(words) != 1 || words[0].Text != "hey" || words[0].Start != 3.1 || words[0].End != 6 {
		t.Fatalf("%+v %v %v", words, ok, err)
	}
}

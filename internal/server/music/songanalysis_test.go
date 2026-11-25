package music

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/music/audiomeasure"
	"veritmo/internal/platform/runner"
)

// clickTrack is decoded mono audio with a noise burst on every beat of a tempo.
func clickTrack(bpm float64, seconds int) []byte {
	n := seconds * audiomeasure.Rate
	samples := make([]float32, n)
	rng := rand.New(rand.NewSource(1))
	step := 60 / bpm * audiomeasure.Rate
	for beat := 0.0; beat*step < float64(n); beat++ {
		start := int(beat * step)
		for i := 0; i < 2000 && start+i < n; i++ {
			samples[start+i] += float32((rng.Float64()*2 - 1) * math.Exp(-float64(i)/300))
		}
	}
	out := make([]byte, 0, n*4)
	for _, v := range samples {
		out = binary.LittleEndian.AppendUint32(out, math.Float32bits(v))
	}
	return out
}

func postJSON(s *testServer, path, body string) (int, string) {
	rec := call(s.Handler(), http.MethodPost, path, body)
	return rec.Code, rec.Body.String()
}

func TestMeasure_AnswersTheTempoOfASource(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "songs")
	putSource(t, s, "songs", "base.mp3", []byte("MP3"))
	fakePeaks(t, s, clickTrack(120, 30), nil)

	code, body := postJSON(s, "/api/projects/songs/measure", `{"source":"base.mp3"}`)
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	var a struct {
		Duration float64  `json:"duration"`
		BPM      *float64 `json:"bpm"`
	}
	if err := json.Unmarshal([]byte(body), &a); err != nil {
		t.Fatal(err)
	}
	if a.Duration != 30 || a.BPM == nil || math.Abs(*a.BPM-120) > 4 {
		t.Fatalf("%s", body)
	}
	if !strings.Contains(body, `"bpm_alternatives":[`) {
		t.Fatalf("alternatives must be a list, never null: %s", body)
	}
}

func TestMeasure_SilenceHasNoTempoOrKeyAndABadSourceIsRefused(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "songs")
	putSource(t, s, "songs", "quiet.wav", []byte("RIFF"))
	putSourceAs(t, s, "songs", "pic.png", "picture", []byte("\x89PNG\r\n\x1a\n"))
	fakePeaks(t, s, make([]byte, audiomeasure.Rate*4*20), nil)

	code, body := postJSON(s, "/api/projects/songs/measure", `{"source":"quiet.wav"}`)
	if code != http.StatusOK || !strings.Contains(body, `"bpm":null`) || !strings.Contains(body, `"keyscale":null`) {
		t.Fatalf("%d %s", code, body)
	}
	for _, c := range []struct {
		name, body string
		want       int
	}{
		{"no source", `{}`, http.StatusBadRequest},
		{"a path", `{"source":"../x.mp3"}`, http.StatusBadRequest},
		{"a picture", `{"source":"pic.png"}`, http.StatusBadRequest},
		{"unknown", `{"source":"nope.mp3"}`, http.StatusNotFound},
		{"bad json", `{`, http.StatusBadRequest},
	} {
		if code, body := postJSON(s, "/api/projects/songs/measure", c.body); code != c.want {
			t.Errorf("%s: %d %s", c.name, code, body)
		}
	}
	if code, _ := postJSON(s, "/api/projects/ghost/measure", `{"source":"quiet.wav"}`); code != http.StatusNotFound {
		t.Errorf("unknown project: %d", code)
	}
}

func TestMeasure_AnUnreadableAudioIs422(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "songs")
	putSource(t, s, "songs", "bad.mp3", []byte("x"))
	fakePeaks(t, s, nil, errors.New("Invalid data found"))
	if code, body := postJSON(s, "/api/projects/songs/measure", `{"source":"bad.mp3"}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("%d %s", code, body)
	}
}

// whisperSetup points the model directory at a temp folder (with a fake model when asked) and
// fakes ffmpeg (writes the wav) and whisper-cli (writes the JSON given).
func whisperSetup(t *testing.T, s *testServer, withModel bool, result string) *runner.FakeRunner {
	t.Helper()
	dir := t.TempDir()
	s.whisperDir = dir
	if withModel {
		if err := os.WriteFile(filepath.Join(dir, "ggml-large-v3-turbo-q5_0.bin"), []byte("model"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s.memAvailable = func() (int64, error) { return 8 << 30, nil }
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		switch inv.Name {
		case "ffmpeg":
			return runner.ScriptedResult{Err: os.WriteFile(inv.Args[len(inv.Args)-1], []byte("WAV"), 0o644)}
		case "/fake/whisper-cli":
			for i, a := range inv.Args {
				if a == "-of" {
					return runner.ScriptedResult{Err: os.WriteFile(inv.Args[i+1]+".json", []byte(result), 0o644)}
				}
			}
		}
		return runner.ScriptedResult{}
	})
	s.MediaRunner = fr
	s.Look = func(name string) (string, error) { return "/fake/" + name, nil }
	return fr
}

const spanishResult = `{"result":{"language":"es"},"transcription":[
 {"offsets":{"from":0,"to":3000},"text":" Camino por la calle"},
 {"offsets":{"from":3000,"to":6000},"text":" bajo la lluvia"},
 {"offsets":{"from":6000,"to":8000},"text":" ♪"}]}`

func TestTranscribe_ReadsTheLyricsOfASource(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "songs")
	putSource(t, s, "songs", "balada.mp3", []byte("MP3"))
	fr := whisperSetup(t, s, true, spanishResult)

	code, body := postJSON(s, "/api/projects/songs/transcribe", `{"source":"balada.mp3"}`)
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	var a struct {
		Language         string `json:"language"`
		LanguageDetected bool   `json:"language_detected"`
		Text             string `json:"text"`
		Segments         []struct{ Start, End float64 }
		Dropped          int    `json:"dropped"`
		Model            string `json:"model"`
	}
	if err := json.Unmarshal([]byte(body), &a); err != nil {
		t.Fatal(err)
	}
	if a.Language != "es" || !a.LanguageDetected || a.Text != "Camino por la calle\nbajo la lluvia" || len(a.Segments) != 2 || a.Dropped != 1 || a.Model != "ggml-large-v3-turbo-q5_0.bin" {
		t.Fatalf("%s", body)
	}
	var sawWhisper bool
	for _, c := range fr.Calls() {
		if c.Name == "/fake/whisper-cli" {
			sawWhisper = true
			line := strings.Join(c.Args, " ")
			if !strings.Contains(line, "-mc 0") || !strings.Contains(line, "-l auto") || strings.Contains(line, "--vad") {
				t.Errorf("whisper flags: %s", line)
			}
		}
		if c.Name == "ffmpeg" && !strings.Contains(strings.Join(c.Args, " "), "-ar 16000") {
			t.Errorf("ffmpeg call: %v", c.Args)
		}
	}
	if !sawWhisper {
		t.Fatal("whisper was not run")
	}
}

func TestTranscribe_TheLanguageIsGivenAndAnInstrumentalIsEmpty(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "songs")
	putSource(t, s, "songs", "inst.mp3", []byte("MP3"))
	fr := whisperSetup(t, s, true, `{"result":{"language":"en"},"transcription":[{"offsets":{"from":0,"to":9000},"text":" [Music]"}]}`)
	code, body := postJSON(s, "/api/projects/songs/transcribe", `{"source":"inst.mp3","language":"en"}`)
	if code != http.StatusOK || !strings.Contains(body, `"text":""`) || !strings.Contains(body, `"language_detected":false`) || !strings.Contains(body, `"segments":[]`) {
		t.Fatalf("%d %s", code, body)
	}
	for _, c := range fr.Calls() {
		if c.Name == "/fake/whisper-cli" && !strings.Contains(strings.Join(c.Args, " "), "-l en") {
			t.Errorf("the language must be passed: %v", c.Args)
		}
	}
}

func TestTranscribe_Refusals(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "songs")
	putSource(t, s, "songs", "a.mp3", []byte("MP3"))
	whisperSetup(t, s, false, spanishResult)

	if code, body := postJSON(s, "/api/projects/songs/transcribe", `{"source":"a.mp3"}`); code != http.StatusServiceUnavailable || !strings.Contains(body, "get-whisper.sh") {
		t.Fatalf("no model: %d %s", code, body)
	}
	whisperSetup(t, s, true, spanishResult)
	if code, _ := postJSON(s, "/api/projects/songs/transcribe", `{"source":"a.mp3","language":"Spanish"}`); code != http.StatusBadRequest {
		t.Errorf("a language name: %d", code)
	}
	s.memAvailable = func() (int64, error) { return 1 << 30, nil }
	if code, body := postJSON(s, "/api/projects/songs/transcribe", `{"source":"a.mp3"}`); code != http.StatusServiceUnavailable || !strings.Contains(body, "missing") {
		t.Errorf("short memory: %d %s", code, body)
	}
	s.memAvailable = func() (int64, error) { return 8 << 30, nil }
	s.Workspace.SimulateRun("run-x", nil)
	if code, _ := postJSON(s, "/api/projects/songs/transcribe", `{"source":"a.mp3"}`); code != http.StatusConflict {
		t.Errorf("a run in progress: %d", code)
	}
}

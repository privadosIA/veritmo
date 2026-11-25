package music

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"veritmo/internal/platform/runner"
)

func postAudio(s *testServer, path string, body []byte) (int, string) {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// wavOf is a wav as ffmpeg writes it (16 kHz mono 16-bit) that lasts the given seconds.
func wavOf(seconds float64) []byte {
	return make([]byte, wavHeaderBytes+int(seconds*wavBytesPerSecond))
}

const sentence = `{"result":{"language":"es"},"transcription":[
 {"offsets":{"from":0,"to":2500},"text":" hazme un vídeo"},
 {"offsets":{"from":2500,"to":5000},"text":" de un gato en una azotea"}]}`

// dictationSetup is whisperSetup with an ffmpeg that writes a wav of the given length and remembers
// the file it was asked to read.
func dictationSetup(t *testing.T, s *testServer, result string, seconds float64) (*runner.FakeRunner, *string) {
	t.Helper()
	fr := whisperSetup(t, s, true, result)
	inputs := new(string)
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		switch inv.Name {
		case "ffmpeg":
			for i, a := range inv.Args {
				if a == "-i" {
					*inputs = inv.Args[i+1]
				}
			}
			return runner.ScriptedResult{Err: os.WriteFile(inv.Args[len(inv.Args)-1], wavOf(seconds), 0o644)}
		case "/fake/whisper-cli":
			for i, a := range inv.Args {
				if a == "-of" {
					return runner.ScriptedResult{Err: os.WriteFile(inv.Args[i+1]+".json", []byte(result), 0o644)}
				}
			}
		}
		return runner.ScriptedResult{}
	})
	return fr, inputs
}

type dictated struct {
	Text             string  `json:"text"`
	Language         string  `json:"language"`
	LanguageDetected bool    `json:"language_detected"`
	Duration         float64 `json:"duration"`
	Model            string  `json:"model"`
}

func TestDictation_TranscribesASentenceAndKeepsNothing(t *testing.T) {
	s, _ := newProjectsServer(t)
	fr, input := dictationSetup(t, s, sentence, 5)

	code, body := postAudio(s, "/api/transcribe", []byte("webm-bytes"))
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	var a dictated
	if err := json.Unmarshal([]byte(body), &a); err != nil {
		t.Fatal(err)
	}
	if a.Text != "hazme un vídeo de un gato en una azotea" || a.Language != "es" || !a.LanguageDetected || a.Duration != 5 || a.Model != "ggml-large-v3-turbo-q5_0.bin" {
		t.Fatalf("%s", body)
	}
	if *input == "" {
		t.Fatal("ffmpeg did not read the recording")
	}
	if _, err := os.Stat(*input); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the recording must be gone after the call: %v", err)
	}
	for _, c := range fr.Calls() {
		if c.Name == "/fake/whisper-cli" && !strings.Contains(strings.Join(c.Args, " "), "-l auto") {
			t.Errorf("the language must be detected: %v", c.Args)
		}
	}
}

func TestDictation_TheLanguageIsGivenAndRepeatedWordsAreKept(t *testing.T) {
	s, _ := newProjectsServer(t)
	fr, _ := dictationSetup(t, s, `{"result":{"language":"en"},"transcription":[
 {"offsets":{"from":0,"to":1000},"text":" more"},{"offsets":{"from":1000,"to":2000},"text":" more"},
 {"offsets":{"from":2000,"to":3000},"text":" more"},{"offsets":{"from":3000,"to":4000},"text":" more"},
 {"offsets":{"from":4000,"to":5000},"text":" more"},{"offsets":{"from":5000,"to":6000},"text":" faster"}]}`, 6)
	code, body := postAudio(s, "/api/transcribe?language=en", []byte("x"))
	var a dictated
	if err := json.Unmarshal([]byte(body), &a); err != nil || code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	if a.Text != "more more more more more faster" || a.Language != "en" || a.LanguageDetected {
		t.Fatalf("%s", body)
	}
	for _, c := range fr.Calls() {
		if c.Name == "/fake/whisper-cli" && !strings.Contains(strings.Join(c.Args, " "), "-l en") {
			t.Errorf("the language must be passed: %v", c.Args)
		}
	}
}

func TestDictation_NoSpeechIsAnEmptyText(t *testing.T) {
	for name, result := range map[string]string{
		"blank":         `{"result":{"language":"es"},"transcription":[{"offsets":{"from":0,"to":3000},"text":" [BLANK_AUDIO]"}]}`,
		"too short":     `{"result":{"language":"es"},"transcription":[{"offsets":{"from":0,"to":300},"text":" eh"}]}`,
		"hallucination": `{"result":{"language":"es"},"transcription":[{"offsets":{"from":0,"to":3000},"text":" ¡Gracias por ver el vídeo!"}]}`,
		"thank you":     `{"result":{"language":"en"},"transcription":[{"offsets":{"from":0,"to":4000},"text":" Thank you."}]}`,
		"nothing":       `{"result":{"language":"es"},"transcription":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := newProjectsServer(t)
			dictationSetup(t, s, result, 3)
			code, body := postAudio(s, "/api/transcribe", []byte("x"))
			if code != http.StatusOK || !strings.Contains(body, `"text":""`) {
				t.Fatalf("%d %s", code, body)
			}
		})
	}
}

func TestDictation_Refusals(t *testing.T) {
	s, _ := newProjectsServer(t)
	dictationSetup(t, s, sentence, 5)

	if code, _ := postAudio(s, "/api/transcribe", nil); code != http.StatusBadRequest {
		t.Errorf("an empty body: %d", code)
	}
	if code, _ := postAudio(s, "/api/transcribe?language=Spanish", []byte("x")); code != http.StatusBadRequest {
		t.Errorf("a language name: %d", code)
	}
	if code, body := postAudio(s, "/api/transcribe", make([]byte, dictationMaxBytes+1)); code != http.StatusRequestEntityTooLarge || !strings.Contains(body, "5 MB") {
		t.Errorf("too big: %d %s", code, body)
	}
	dictationSetup(t, s, sentence, 90)
	if code, body := postAudio(s, "/api/transcribe", []byte("x")); code != http.StatusRequestEntityTooLarge || !strings.Contains(body, "60 seconds") {
		t.Errorf("too long: %d %s", code, body)
	}
	dictationSetup(t, s, sentence, 60)
	if code, body := postAudio(s, "/api/transcribe", []byte("x")); code != http.StatusOK {
		t.Errorf("exactly the limit: %d %s", code, body)
	}

	fr := whisperSetup(t, s, true, sentence)
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if inv.Name == "ffmpeg" {
			return runner.ScriptedResult{Err: errors.New("Invalid data found when processing input")}
		}
		return runner.ScriptedResult{}
	})
	if code, body := postAudio(s, "/api/transcribe", []byte("x")); code != http.StatusUnprocessableEntity || !strings.Contains(body, "cannot read this audio") {
		t.Errorf("unreadable: %d %s", code, body)
	}
}

func TestDictation_NotInstalledOrShortOfMemory(t *testing.T) {
	s, _ := newProjectsServer(t)
	whisperSetup(t, s, false, sentence)
	if code, body := postAudio(s, "/api/transcribe", []byte("x")); code != http.StatusServiceUnavailable || !strings.Contains(body, "get-whisper.sh") {
		t.Fatalf("no model: %d %s", code, body)
	}
	dictationSetup(t, s, sentence, 5)
	s.memAvailable = func() (int64, error) { return 1 << 30, nil }
	if code, body := postAudio(s, "/api/transcribe", []byte("x")); code != http.StatusServiceUnavailable || !strings.Contains(body, "missing") {
		t.Errorf("short memory: %d %s", code, body)
	}
}

func TestDictation_DoesNotWaitForARun(t *testing.T) {
	s, _ := newProjectsServer(t)
	dictationSetup(t, s, sentence, 5)
	s.Workspace.SimulateRun("run-x", nil)
	if code, body := postAudio(s, "/api/transcribe", []byte("x")); code != http.StatusOK || !strings.Contains(body, "gato") {
		t.Fatalf("a run in progress: %d %s", code, body)
	}
}

func TestEnvironment_SaysWhetherDictationCanWork(t *testing.T) {
	s, _ := newProjectsServer(t)
	whisperSetup(t, s, false, sentence)
	if st := getEnvironment(t, s, "").Dictation; st.Available || !strings.Contains(st.Reason, "model") {
		t.Errorf("without the model: %+v", st)
	}
	whisperSetup(t, s, true, sentence)
	if st := getEnvironment(t, s, "").Dictation; !st.Available || st.Reason != "" {
		t.Errorf("installed: %+v", st)
	}
	s.Look = func(name string) (string, error) {
		if name == "ffmpeg" {
			return "", errors.New("not found")
		}
		return "/fake/" + name, nil
	}
	if st := getEnvironment(t, s, "").Dictation; st.Available || !strings.Contains(st.Reason, "ffmpeg") {
		t.Errorf("without ffmpeg: %+v", st)
	}
}

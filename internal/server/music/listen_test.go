package music

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/platform/runner"
)

const sungWords = `{"result":{"language":"es"},"transcription":[
 {"offsets":{"from":0,"to":4000},"text":" la desplumadora no para de girar sin parar de noche y de día"},
 {"offsets":{"from":4000,"to":8000},"text":" plumas por el aire plumas por todo el lugar"}]}`

// listenSetup is dictationSetup with an ffmpeg that also answers the read of the samples (silence), and an
// assistant that describes the song and remembers what it was given.
func listenSetup(t *testing.T, s *testServer, result string, seconds float64) *[]string {
	t.Helper()
	fr := whisperSetup(t, s, true, result)
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		switch inv.Name {
		case "ffmpeg":
			if strings.Contains(strings.Join(inv.Args, " "), "f32le") {
				return runner.ScriptedResult{Stdout: make([]byte, 4*22050)}
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
	asked := &[]string{}
	s.Assistant.AssistRun = func(ctx context.Context, task assist.Task, text, model string) (string, error) {
		*asked = append(*asked, string(task)+"|"+text)
		return "STYLE=fast techno with synth and a male rap voice; THEME=una máquina que depluma pollos sin parar", nil
	}
	return asked
}

func TestListen_DescribesTheSongWithoutReturningItsLyrics(t *testing.T) {
	s, _ := newProjectsServer(t)
	asked := listenSetup(t, s, sungWords, 20)
	code, body := postAudio(s, "/api/assist/listen", []byte("webm-bytes"))
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	var a listenAnswer
	if err := json.Unmarshal([]byte(body), &a); err != nil {
		t.Fatal(err)
	}
	if a.Language != "es" || a.NoWords || a.Style == "" || a.Theme == "" || !strings.Contains(a.Request, "A new original song in this style: fast techno") || !strings.Contains(a.Request, "It is about: una máquina") {
		t.Fatalf("%s", body)
	}
	if strings.Contains(body, "plumas por el aire") || strings.Contains(a.Request, "plumas") {
		t.Fatalf("the lyrics that were heard must not come back: %s", body)
	}
	if len(*asked) != 1 || !strings.HasPrefix((*asked)[0], "listen|") || !strings.Contains((*asked)[0], "plumas por el aire") {
		t.Fatalf("the assistant is given the words once: %v", *asked)
	}
}

func TestListen_ASongWithoutWordsDoesNotClaimToBeInstrumental(t *testing.T) {
	s, _ := newProjectsServer(t)
	asked := listenSetup(t, s, `{"result":{"language":"es"},"transcription":[{"offsets":{"from":0,"to":9000},"text":" [MÚSICA]"}]}`, 20)
	code, body := postAudio(s, "/api/assist/listen", []byte("x"))
	var a listenAnswer
	if err := json.Unmarshal([]byte(body), &a); err != nil || code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	if !a.NoWords || a.Theme != "" || strings.Contains(strings.ToLower(a.Request), "instrumental") {
		t.Fatalf("no words is said, but nobody claims there is no voice: %s", body)
	}
	if !strings.Contains((*asked)[0], "Lyrics: none recognised") {
		t.Errorf("the model is told no words were recognised: %v", *asked)
	}
	if !strings.Contains((*asked)[0], "Sound (measured)") && a.Sound != nil {
		t.Errorf("the measured sound goes to the model when there is some: %v", *asked)
	}
}

func TestListen_AGenreTheModelCannotTellIsSaidSo(t *testing.T) {
	s, _ := newProjectsServer(t)
	listenSetup(t, s, sungWords, 20)
	s.Assistant.AssistRun = func(ctx context.Context, task assist.Task, text, model string) (string, error) {
		return "STYLE=unknown genre, steady dark texture with a bass pulse; THEME=una máquina", nil
	}
	code, body := postAudio(s, "/api/assist/listen", []byte("x"))
	var a listenAnswer
	if err := json.Unmarshal([]byte(body), &a); err != nil || code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	if !a.GenreUnknown || len(a.Warnings) == 0 || !strings.Contains(a.Warnings[0], "genre could not be told") {
		t.Fatalf("%s", body)
	}
}

func TestListen_ARecordingThatIsTooShortOrEmptyIsRefused(t *testing.T) {
	s, _ := newProjectsServer(t)
	listenSetup(t, s, sungWords, 2)
	if code, body := postAudio(s, "/api/assist/listen", []byte("x")); code != http.StatusUnprocessableEntity || !strings.Contains(body, "too short") {
		t.Errorf("short: %d %s", code, body)
	}
	if code, _ := postAudio(s, "/api/assist/listen", nil); code != http.StatusBadRequest {
		t.Errorf("empty: %d", code)
	}
	if code, _ := postAudio(s, "/api/assist/listen?language=español", []byte("x")); code != http.StatusBadRequest {
		t.Errorf("language: %d", code)
	}
}

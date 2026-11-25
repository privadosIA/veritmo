package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/platform/edition"
	"veritmo/internal/platform/sched"
)

// fakeOllamaServer answers like a healthy Ollama and records the model each
// chat request named and how many requests arrived.
func fakeOllamaServer(t *testing.T, answer string) (*httptest.Server, *atomic.Value, *atomic.Int32) {
	t.Helper()
	var model atomic.Value
	model.Store("")
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{
				{"name": "qwen3:8b", "model": "qwen3:8b", "size": 5 << 30},
				{"name": "other:7b", "model": "other:7b", "size": 5 << 30}}})
		case "/api/chat":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			model.Store(body["model"])
			_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"content": answer}, "done_reason": "stop"})
		case "/api/ps":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{}})
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &model, &hits
}

func useFake(t *testing.T, srv *httptest.Server) deps {
	t.Helper()
	d := realDeps()
	d.newAssistant = func(modelFlag string) *assist.Assistant {
		a := assist.New(srv.URL, assist.ModelFromEnv(modelFlag, func(k string) string {
			return map[string]string{"VERITMO_ASSIST_MODEL": "qwen3:8b"}[k]
		}))
		a.MemAvailable = func() (int64, error) { return 27 * sched.GiB, nil }
		return a
	}
	return d
}

func TestAssistCmd_PrintsExactlyTheGeneratedLine(t *testing.T) {
	srv, model, _ := fakeOllamaServer(t, "<think>x</think> A woman reads in a sunlit cafe.")
	d := useFake(t, srv)
	var out, errBuf bytes.Buffer
	code := d.assist([]string{"image", "Una", "mujer", "lee"}, &out, &errBuf)
	if code != 0 || out.String() != "A woman reads in a sunlit cafe.\n" || errBuf.Len() != 0 {
		t.Fatalf("code %d, stdout %q, stderr %q", code, out.String(), errBuf.String())
	}
	if model.Load() != "qwen3:8b" {
		t.Errorf("model = %v", model.Load())
	}
}

func TestAssistCmd_FlagWinsAndMayFollowTheArguments(t *testing.T) {
	srv, model, _ := fakeOllamaServer(t, "Slow camera drift.")
	d := useFake(t, srv)
	var out, errBuf bytes.Buffer
	code := d.assist([]string{"motion", "una mujer lee", "--model", "other:7b"}, &out, &errBuf)
	if code != 0 || out.String() != "Slow camera drift.\n" {
		t.Fatalf("code %d, stdout %q, stderr %q", code, out.String(), errBuf.String())
	}
	if model.Load() != "other:7b" {
		t.Errorf("--model must be used even after the positional arguments, got %v", model.Load())
	}
}

func TestAssistCmd_InvalidInputNeverContactsOllama(t *testing.T) {
	srv, _, hits := fakeOllamaServer(t, "x")
	d := useFake(t, srv)
	for name, args := range map[string][]string{
		"unknown task": {"foo", "texto"},
		"empty text":   {"image", "   "},
		"no text":      {"image"},
		"nothing":      {},
	} {
		var out, errBuf bytes.Buffer
		if code := d.assist(args, &out, &errBuf); code == 0 || out.Len() != 0 || errBuf.Len() == 0 {
			t.Errorf("%s: code %d, stdout %q, stderr %q", name, code, out.String(), errBuf.String())
		}
	}
	if hits.Load() != 0 {
		t.Errorf("Ollama was contacted %d times for invalid input", hits.Load())
	}
}

func TestAssistCmd_ErrorsGoToStderrWithNonZeroStatus(t *testing.T) {
	srv, _, _ := fakeOllamaServer(t, "x")
	d := useFake(t, srv)
	var out, errBuf bytes.Buffer
	code := d.assist([]string{"image", "texto", "--model", "missing:1b"}, &out, &errBuf)
	if code != 1 || out.Len() != 0 || !strings.Contains(errBuf.String(), "ollama pull missing:1b") {
		t.Fatalf("code %d, stdout %q, stderr %q", code, out.String(), errBuf.String())
	}
}

const musicReply = "STYLE: Slow pop ballad with piano and warm vocals\nLYRICS:\n[Verse]\nCamino bajo la lluvia\n\n[Chorus]\nVuelvo a casa"

func TestAssistCmd_MusicPrintsTheStyleAndTheLyrics(t *testing.T) {
	srv, _, _ := fakeOllamaServer(t, musicReply)
	d := useFake(t, srv)
	var out, errBuf bytes.Buffer
	code := d.assist([]string{"music", "Una", "balada", "sobre", "volver", "a", "casa"}, &out, &errBuf)
	want := "STYLE: Slow pop ballad with piano and warm vocals\nLYRICS:\n[Verse]\nCamino bajo la lluvia\n\n[Chorus]\nVuelvo a casa\n"
	if code != 0 || out.String() != want || errBuf.Len() != 0 {
		t.Fatalf("code %d, stdout %q, stderr %q", code, out.String(), errBuf.String())
	}
}

func TestAssistCmd_MusicInstrumentalPrintsTheStyleOnly(t *testing.T) {
	srv, _, _ := fakeOllamaServer(t, musicReply)
	d := useFake(t, srv)
	var out, errBuf bytes.Buffer
	code := d.assist([]string{"--instrumental", "music", "lo-fi", "para", "estudiar"}, &out, &errBuf)
	if code != 0 || out.String() != "STYLE: Slow pop ballad with piano and warm vocals\n" {
		t.Fatalf("code %d, stdout %q, stderr %q", code, out.String(), errBuf.String())
	}
}

func TestAssistCmd_ARefusalIsAnErrorNotAPrompt(t *testing.T) {
	srv, _, _ := fakeOllamaServer(t, "I'm sorry, but I can't help with that request.")
	d := useFake(t, srv)
	var out, errBuf bytes.Buffer
	code := d.assist([]string{"image", "una escena"}, &out, &errBuf)
	if code == 0 || out.Len() != 0 {
		t.Fatalf("code %d, stdout %q", code, out.String())
	}
	if msg := errBuf.String(); !strings.Contains(msg, "refused") || strings.Contains(msg, "another installed model") != edition.Private() {
		t.Errorf("stderr = %q", msg)
	}
}

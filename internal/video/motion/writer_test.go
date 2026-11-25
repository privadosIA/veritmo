package motion

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/assistant/llm"
	"veritmo/internal/platform/settings"
)

// The writer goes through the provider routed to the motion-graphics task: here the
// built-in Ollama, served by a fake that records the request.
func TestWriterAsksTheRoutedProvider(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("VERITMO_ASSIST_MODEL", "writer-model:1b")
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"content": "<html></html>"}})
	}))
	defer srv.Close()
	t.Setenv("OLLAMA_HOST", srv.URL)

	w, info, err := NewWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Model != "writer-model:1b" || info.Provider != "Ollama" {
		t.Fatalf("who wrote the page must be reported: %+v", info)
	}
	out, err := w.Write(context.Background(), "sys", "usr", 123)
	if err != nil || out != "<html></html>" {
		t.Fatalf("%q %v", out, err)
	}
	msgs, _ := got["messages"].([]any)
	if got["keep_alive"] != "0" {
		t.Errorf("the model must be dropped from the GPU right after the page is written: keep_alive = %v", got["keep_alive"])
	}
	if got["model"] != "writer-model:1b" || len(msgs) != 2 || !strings.Contains(string(mustJSON(msgs)), "usr") {
		t.Fatalf("request: %v", got)
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// The model saved for the task wins over the assistant's, and says who wrote the page.
func TestWriterUsesTheModelSavedForTheTask(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("VERITMO_ASSIST_MODEL", "assistant-model:8b")
	if err := os.MkdirAll(filepath.Join(cfg, "veritmo"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "veritmo", "settings.json"), []byte(`{"motion_model":"coder:30b"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"content": "<html></html>"}})
	}))
	defer srv.Close()
	t.Setenv("OLLAMA_HOST", srv.URL)
	w, info, err := NewWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(context.Background(), "s", "u", 10); err != nil {
		t.Fatal(err)
	}
	if info.Model != "coder:30b" || got["model"] != "coder:30b" {
		t.Fatalf("the saved model is asked and reported: %+v %v", info, got["model"])
	}
}

type capturingProvider struct {
	got  llm.Request
	resp llm.Response
}

func (c *capturingProvider) Chat(_ context.Context, r llm.Request) (llm.Response, error) {
	c.got = r
	return c.resp, nil
}
func (c *capturingProvider) Models(context.Context) ([]llm.Model, error) { return nil, nil }

func TestARemoteProviderGetsRoomForItsThinkingAndALocalOneDoesNot(t *testing.T) {
	remote := &capturingProvider{resp: llm.Response{Text: "page"}}
	w := &llmWriter{res: llm.Resolved{Name: "Gemini", Type: settings.TypeOpenAI, Provider: remote}, model: "m"}
	if _, err := w.Write(context.Background(), "s", "u", maxTokens); err != nil || remote.got.MaxTokens != maxTokens*remoteTokenFactor {
		t.Fatalf("a provider that may think gets %d tokens, got %d (%v)", maxTokens*remoteTokenFactor, remote.got.MaxTokens, err)
	}
	local := &capturingProvider{resp: llm.Response{Text: "page"}}
	w = &llmWriter{res: llm.Resolved{Name: "Ollama", Type: settings.TypeOllama, Provider: local}, model: "m"}
	if _, err := w.Write(context.Background(), "s", "u", maxTokens); err != nil || local.got.MaxTokens != maxTokens {
		t.Fatalf("an Ollama keeps %d tokens, got %d (%v)", maxTokens, local.got.MaxTokens, err)
	}
}

func TestAnAnswerThatRanOutOfTokensBeforeWritingSaysSo(t *testing.T) {
	p := &capturingProvider{resp: llm.Response{Text: "  ", Truncated: true}}
	w := &llmWriter{res: llm.Resolved{Name: "Gemini", Type: settings.TypeOpenAI, Provider: p}, model: "m"}
	_, err := w.Write(context.Background(), "s", "u", maxTokens)
	if err == nil || !strings.Contains(err.Error(), "ran out of its") || !strings.Contains(err.Error(), "Gemini") {
		t.Fatalf("an empty truncated answer must say why: %v", err)
	}
}

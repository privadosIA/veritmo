package motion

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"veritmo/internal/assistant/llm"
	"veritmo/internal/platform/settings"
)

// ollamaWith serves /api/tags with the given models and points the built-in Ollama at it.
func ollamaWith(t *testing.T, names ...string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		var models []map[string]any
		for _, n := range names {
			models = append(models, map[string]any{"name": n, "size": 1})
		}
		json.NewEncoder(w).Encode(map[string]any{"models": models})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OLLAMA_HOST", srv.URL)
}

func resolvedMotion(t *testing.T) llm.Resolved {
	t.Helper()
	res, err := llm.For(settings.TaskMotionGraphics, "")
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestResolveModel_AnInstalledPreferredModelBeatsTheAssistants(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ORCHESTRATOR_ASSIST_MODEL", "assistant:8b")
	ollamaWith(t, "assistant:8b", "gemma4:31b", "gemma4:26b")
	if got := ResolveModel(context.Background(), resolvedMotion(t)); got != "gemma4:26b" {
		t.Errorf("model = %q, want gemma4:26b (the first preferred one installed)", got)
	}
	ollamaWith(t, "assistant:8b", "gemma4:31b")
	if got := ResolveModel(context.Background(), resolvedMotion(t)); got != "gemma4:31b" {
		t.Errorf("model = %q, want gemma4:31b", got)
	}
}

func TestResolveModel_NoneInstalledFallsBackToTheAssistants(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ORCHESTRATOR_ASSIST_MODEL", "assistant:8b")
	ollamaWith(t, "assistant:8b", "qwen3-coder:30b")
	if got := ResolveModel(context.Background(), resolvedMotion(t)); got != "assistant:8b" {
		t.Errorf("model = %q, want the assistant's", got)
	}
}

func TestResolveModel_TheSavedModelWinsAndDefaultModelIgnoresIt(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("ORCHESTRATOR_ASSIST_MODEL", "assistant:8b")
	if err := os.MkdirAll(filepath.Join(cfg, "veritmo"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "veritmo", "settings.json"), []byte(`{"motion_model":"coder:30b"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ollamaWith(t, "gemma4:26b")
	res := resolvedMotion(t)
	if got := ResolveModel(context.Background(), res); got != "coder:30b" {
		t.Errorf("model = %q, want the saved one", got)
	}
	if got := DefaultModel(context.Background(), res); got != "gemma4:26b" {
		t.Errorf("default = %q, want the preferred installed one", got)
	}
}

func TestResolveModel_AnUnreachableOllamaKeepsTheAssistants(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ORCHESTRATOR_ASSIST_MODEL", "assistant:8b")
	t.Setenv("OLLAMA_HOST", "http://127.0.0.1:1")
	if got := ResolveModel(context.Background(), resolvedMotion(t)); got != "assistant:8b" {
		t.Errorf("model = %q, want the assistant's", got)
	}
}

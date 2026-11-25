//go:build !private

package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"veritmo/internal/platform/edition"
	"veritmo/internal/platform/settings"
)

func TestPublicEditionDoesNotCallDeniedModels(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`{"models":[{"name":"qwen3:8b","size":1},{"name":"x-uncensored:7b","size":2}],"data":[{"id":"gpt-4o"},{"id":"my-unfiltered"}]}`))
	}))
	defer srv.Close()
	for name, p := range map[string]Provider{"ollama": &Ollama{Host: srv.URL}, "openai": NewOpenAI(srv.URL, "")} {
		if _, err := p.Chat(context.Background(), Request{Model: "x-uncensored:7b", User: "hi"}); !errors.Is(err, edition.ErrModelNotAllowed) {
			t.Errorf("%s: want ErrModelNotAllowed, got %v", name, err)
		}
	}
	if calls != 0 {
		t.Fatalf("nothing may reach the server: %d calls", calls)
	}
	ms, err := (&Ollama{Host: srv.URL}).Installed(context.Background())
	if err != nil || len(ms) != 1 || ms[0].Name != "qwen3:8b" {
		t.Errorf("ollama list: %v %v", ms, err)
	}
	ms, err = NewOpenAI(srv.URL, "").Models(context.Background())
	if err != nil || len(ms) != 1 || ms[0].Name != "gpt-4o" {
		t.Errorf("openai list: %v %v", ms, err)
	}
}

// A provider saved with an address this edition does not use (the settings of a
// private install) is not used for any task.
func TestPublicEditionBlocksSavedProviderWithFreeAddress(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OLLAMA_HOST", "")
	f := settings.Load()
	f.Providers = []settings.Provider{{ID: "mine", Name: "Mine", Type: settings.TypeOpenAI, BaseURL: "https://my-server.example/v1", TextModel: "m"}}
	if err := settings.Save(f); err != nil {
		t.Fatal(err)
	}
	if _, err := For(settings.TaskAssistant, "mine"); !errors.Is(err, ErrProviderBlocked) {
		t.Fatalf("want ErrProviderBlocked, got %v", err)
	}
	if _, err := settings.AddProvider(settings.Provider{ID: "other", Name: "Other", Type: settings.TypeOpenAI, BaseURL: "https://my-server.example/v1", TextModel: "m"}); !errors.Is(err, settings.ErrHostNotAllowed) {
		t.Fatalf("adding must be refused, got %v", err)
	}
	if _, err := settings.AddProvider(settings.Provider{ID: "oa", Name: "OpenAI", Type: settings.TypeOpenAI, BaseURL: "https://api.openai.com/v1", TextModel: "gpt-4o"}); err != nil {
		t.Fatalf("a listed host is accepted: %v", err)
	}
}

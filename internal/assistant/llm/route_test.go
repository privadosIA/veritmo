package llm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"veritmo/internal/platform/settings"
)

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OLLAMA_HOST", "")
}

func addProvider(t *testing.T, p settings.Provider) {
	t.Helper()
	if _, err := settings.AddProvider(p); err != nil {
		t.Fatal(err)
	}
}

func TestFor_DefaultIsTheBuiltinOllamaAndNeedsNoConsent(t *testing.T) {
	isolate(t)
	for _, task := range settings.Tasks {
		r, err := For(task, "")
		if err != nil || !r.Builtin || r.Type != "ollama" || r.Host != "http://127.0.0.1:11434" || r.Ollama == nil || r.Model != "" {
			t.Fatalf("%s: %+v %v", task, r, err)
		}
	}
	// a remote OLLAMA_HOST is the user's own setting: still no acknowledgement for the default
	t.Setenv("OLLAMA_HOST", "10.0.0.2:11434")
	r, err := For(settings.TaskAssistant, "")
	if err != nil || !r.Remote || !r.Builtin {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestFor_ModelPerTaskAndNoModel(t *testing.T) {
	isolate(t)
	addProvider(t, settings.Provider{ID: "lan", Name: "LAN", Type: settings.TypeOpenAI, BaseURL: "http://127.0.0.1:8000/v1", TextModel: "text-m"})
	r, err := For(settings.TaskAssistant, "lan")
	if err != nil || r.Model != "text-m" || r.Remote || r.Type != "openai" {
		t.Fatalf("%+v %v", r, err)
	}
	_, err = For(settings.TaskDescribeImage, "lan")
	var nm *NoModelError
	if !errors.Is(err, ErrNoModel) || !errors.As(err, &nm) || !strings.Contains(err.Error(), "vision") {
		t.Fatalf("a missing vision model must be named: %v", err)
	}
	if _, err := For(settings.TaskAssistant, "ghost"); !errors.Is(err, ErrUnknownProvider) {
		t.Errorf("unknown: %v", err)
	}
}

func TestFor_RemoteNeedsConsentBoundToTheAddress(t *testing.T) {
	isolate(t)
	addProvider(t, settings.Provider{ID: "cloud", Name: "Cloud", Type: settings.TypeOpenAI, BaseURL: "https://api.example.com/v1", TextModel: "t", VisionModel: "v"})
	_, err := For(settings.TaskAssistant, "cloud")
	var ce *ConsentError
	if !errors.Is(err, ErrConsentRequired) || !errors.As(err, &ce) || ce.Name != "Cloud" || !strings.Contains(err.Error(), "texts") {
		t.Fatalf("consent must be required naming the provider and what is sent: %v", err)
	}
	_, err = For(settings.TaskQuality, "cloud")
	if !strings.Contains(err.Error(), "frames of your clips") {
		t.Errorf("quality sends frames: %v", err)
	}
	if err := settings.Acknowledge("cloud"); err != nil {
		t.Fatal(err)
	}
	r, err := For(settings.TaskAssistant, "cloud")
	if err != nil || !r.Remote || r.Model != "t" {
		t.Fatalf("%+v %v", r, err)
	}
	// the routing is used when there is no override
	if err := settings.SetRouting(map[string]string{settings.TaskDescribeImage: "cloud"}); err != nil {
		t.Fatal(err)
	}
	r, err = For(settings.TaskDescribeImage, "")
	if err != nil || r.ID != "cloud" || r.Model != "v" {
		t.Fatalf("%+v %v", r, err)
	}
	// an address change takes the consent away at use time, even though the routing is saved
	p, _ := settings.FindProvider("cloud")
	p.BaseURL = "https://moved.example.com/v1"
	if _, err := settings.UpdateProvider("cloud", p); err != nil {
		t.Fatal(err)
	}
	if _, err := For(settings.TaskDescribeImage, ""); !errors.Is(err, ErrConsentRequired) {
		t.Fatalf("a new address needs a new acknowledgement: %v", err)
	}
}

func TestFor_ExtraOllamaDeclaresNoModelNeeded(t *testing.T) {
	isolate(t)
	addProvider(t, settings.Provider{ID: "lab", Name: "Lab", Type: settings.TypeOllama, BaseURL: "http://127.0.0.1:11434"})
	r, err := For(settings.TaskDescribeImage, "lab")
	if err != nil || r.Builtin || r.Ollama == nil || r.Model != "" || r.Type != "ollama" {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestContextOverride(t *testing.T) {
	isolate(t)
	addProvider(t, settings.Provider{ID: "lan", Name: "LAN", Type: settings.TypeOpenAI, BaseURL: "http://127.0.0.1:8000/v1", TextModel: "m", VisionModel: "v"})
	ctx := WithProvider(context.Background(), "lan")
	r, err := ForContext(ctx, settings.TaskAssistant)
	if err != nil || r.ID != "lan" {
		t.Fatalf("%+v %v", r, err)
	}
	r, _ = ForContext(context.Background(), settings.TaskAssistant)
	if r.ID != "ollama" {
		t.Errorf("no override, no change: %+v", r)
	}
	if ProviderFrom(WithProvider(context.Background(), "")) != "" {
		t.Error("an empty id is no override")
	}
	if err := Check(settings.TaskAssistant, "lan"); err != nil {
		t.Error(err)
	}
}

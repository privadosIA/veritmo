package main

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/platform/settings"
	"veritmo/internal/project/config"
)

func isolateConfig(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OLLAMA_HOST", "127.0.0.1:1") // the built-in Ollama is unreachable here
}

// openaiServer answers chat completions like an OpenAI-compatible server and counts the calls.
func openaiServer(t *testing.T, reply func(sys string) string) (*httptest.Server, *int) {
	t.Helper()
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		sys := ""
		if msgs, ok := body["messages"].([]any); ok && len(msgs) > 0 {
			sys, _ = msgs[0].(map[string]any)["content"].(string)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]any{"content": reply(sys)}, "finish_reason": "stop"}}})
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func TestAssistCLI_ProviderFlagAndRoutingUseTheProvider(t *testing.T) {
	isolateConfig(t)
	srv, calls := openaiServer(t, func(string) string { return "A photorealistic photograph of a woman reading in a sunlit cafe." })
	if _, err := settings.AddProvider(settings.Provider{ID: "sim", Name: "Sim", Type: settings.TypeOpenAI, BaseURL: srv.URL + "/v1", TextModel: "gpt-sim"}); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := runAssist([]string{"--provider", "sim", "image", "una mujer lee"}, &out, &errb); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "woman reading") || *calls != 1 {
		t.Fatalf("out %q calls %d", out.String(), *calls)
	}
	// routed in the settings: the terminal follows it
	if err := settings.SetRouting(map[string]string{settings.TaskAssistant: "sim"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := runAssist([]string{"image", "una mujer lee"}, &out, &errb); code != 0 || *calls != 2 {
		t.Fatalf("code %d calls %d: %s", code, *calls, errb.String())
	}
}

func TestAssistCLI_RemoteProviderWithoutConsentStopsWithTheSettingsHint(t *testing.T) {
	isolateConfig(t)
	if _, err := settings.AddProvider(settings.Provider{ID: "cloud", Name: "Cloud", Type: settings.TypeOpenAI, BaseURL: "https://api.example.com/v1", TextModel: "t", VisionModel: "v"}); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := runAssist([]string{"--provider", "cloud", "image", "algo"}, &out, &errb); code != 1 {
		t.Fatalf("code %d", code)
	}
	if out.Len() != 0 || !strings.Contains(errb.String(), "Settings") || !strings.Contains(errb.String(), "Cloud") {
		t.Errorf("stdout %q stderr %q", out.String(), errb.String())
	}
	// an unknown provider is also an error, never a fallback
	errb.Reset()
	if code := runAssist([]string{"--provider", "ghost", "image", "algo"}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "ghost") {
		t.Errorf("code %d: %s", code, errb.String())
	}
}

func TestDescribeCLI_ProviderFlagUsesItsVisionModel(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	isolateConfig(t)
	text := "A photorealistic portrait of one adult woman in her late twenties with long wavy auburn hair, wearing a black lace bra and matching panties, sitting on the edge of an unmade bed in a dim bedroom, soft warm window light from the left, shallow depth of field, medium shot, confident smile."
	srv, calls := openaiServer(t, func(sys string) string {
		if strings.HasPrefix(sys, "You check a picture") {
			return `{"people":1,"minor":false,"unclear":false}`
		}
		return text
	})
	if _, err := settings.AddProvider(settings.Provider{ID: "sim", Name: "Sim", Type: settings.TypeOpenAI, BaseURL: srv.URL + "/v1", VisionModel: "gpt-vision"}); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for i := 0; i < 64; i++ {
		img.Set(i, i, color.RGBA{200, 50, 50, 255})
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	file := filepath.Join(t.TempDir(), "a.png")
	_ = os.WriteFile(file, buf.Bytes(), 0o644)

	var out, errb bytes.Buffer
	if code := runDescribe([]string{"--provider", "sim", file}, &out, &errb); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	if strings.TrimSpace(out.String()) != text || *calls != 2 {
		t.Fatalf("out %q calls %d", out.String(), *calls)
	}
	if !strings.Contains(errb.String(), "described by gpt-vision (sim)") {
		t.Errorf("the provider is shown on stderr: %q", errb.String())
	}
}

func TestDescribeCLI_RemoteWithoutConsentStops(t *testing.T) {
	isolateConfig(t)
	if _, err := settings.AddProvider(settings.Provider{ID: "cloud", Name: "Cloud", Type: settings.TypeOpenAI, BaseURL: "https://api.example.com/v1", VisionModel: "v"}); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "a.png")
	_ = os.WriteFile(file, []byte("x"), 0o644)
	var out, errb bytes.Buffer
	if code := runDescribe([]string{"--provider", "cloud", file}, &out, &errb); code != 1 || out.Len() != 0 || !strings.Contains(errb.String(), "Settings") {
		t.Fatalf("code %d out %q err %q", code, out.String(), errb.String())
	}
}

func TestCheck_RemoteQualityProviderWithoutConsentDoesNotStart(t *testing.T) {
	isolateConfig(t)
	if _, err := settings.AddProvider(settings.Provider{ID: "cloud", Name: "Cloud", Type: settings.TypeOpenAI, BaseURL: "https://api.example.com/v1", VisionModel: "v"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	_, err := checkProject(context.Background(), &config.Config{}, t.TempDir(), checkOptions{Provider: "cloud"}, &out)
	if err == nil || !strings.Contains(err.Error(), "Settings") {
		t.Fatalf("want a consent error pointing at Settings, got %v", err)
	}
	// after the acknowledgement the provider is used, with its own model
	if err := settings.Acknowledge("cloud"); err != nil {
		t.Fatal(err)
	}
	if _, err := checkProject(context.Background(), &config.Config{}, t.TempDir(), checkOptions{Provider: "cloud"}, &out); err != nil {
		t.Fatalf("acknowledged: %v", err)
	}
}

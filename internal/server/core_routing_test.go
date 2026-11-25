package server

import (
	"bytes"
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
	"sync"
	"testing"

	"veritmo/internal/assistant/describe"
	"veritmo/internal/platform/settings"
)

type routingBody struct {
	Routing  map[string]routedTask `json:"routing"`
	MaxCalls int                   `json:"max_calls"`
}

func getRouting(t *testing.T, s *Server) routingBody {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/routing", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var b routingBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	return b
}

func addProv(t *testing.T, s *Server, body map[string]any) {
	t.Helper()
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/providers", body); rec.Code != http.StatusCreated {
		t.Fatalf("add provider: %d %s", rec.Code, rec.Body)
	}
}

// openaiSim is a loopback OpenAI-compatible server for chat calls, recording what it gets.
type openaiSim struct {
	mu    sync.Mutex
	paths []string
	reply func(sys string) string
}

func (o *openaiSim) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.mu.Lock()
		o.paths = append(o.paths, r.URL.Path)
		o.mu.Unlock()
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		sys := ""
		if msgs, ok := body["messages"].([]any); ok && len(msgs) > 0 {
			sys, _ = msgs[0].(map[string]any)["content"].(string)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]any{"content": o.reply(sys)}, "finish_reason": "stop"}}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRouting_DefaultsAndTheCapInTheAnswer(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	b := getRouting(t, s)
	if b.MaxCalls != 60 || len(b.Routing) != 5 {
		t.Fatalf("%+v", b)
	}
	for _, task := range settings.Tasks {
		r := b.Routing[task]
		if r.Provider != "ollama" || r.Name != "Ollama" || r.Remote || !r.Consented {
			t.Errorf("%s: %+v", task, r)
		}
	}
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/settings", nil)
	if !strings.Contains(rec.Body.String(), `"remote":{"max_calls":60}`) {
		t.Errorf("the settings answer carries the cap: %s", rec.Body)
	}
}

func TestRouting_SaveRoutesAndRefusesWhatCannotRun(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	addProv(t, s, map[string]any{"name": "Local oai", "type": "openai", "base_url": "http://127.0.0.1:8000/v1", "text_model": "t"})
	h := s.Handler()

	rec := doJSON(t, h, http.MethodPut, "/api/routing", map[string]any{"routing": map[string]string{"assistant": "local-oai"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("a local provider needs no consent: %d %s", rec.Code, rec.Body)
	}
	if r := getRouting(t, s).Routing["assistant"]; r.Provider != "local-oai" || r.Model != "t" || r.Remote {
		t.Fatalf("%+v", r)
	}
	// the provider has no vision model: refused with the reason, nothing changes
	rec = doJSON(t, h, http.MethodPut, "/api/routing", map[string]any{"routing": map[string]string{"describe_image": "local-oai"}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "no_model") || !strings.Contains(rec.Body.String(), "vision") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for body, want := range map[string]map[string]string{"nope": {"nope": "ollama"}, "ghost": {"assistant": "ghost"}} {
		if rec := doJSON(t, h, http.MethodPut, "/api/routing", map[string]any{"routing": want}); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", body, rec.Code)
		}
	}
	if getRouting(t, s).Routing["describe_image"].Provider != "ollama" {
		t.Error("a refused change must change nothing")
	}
	// back to the built-in
	doJSON(t, h, http.MethodPut, "/api/routing", map[string]any{"routing": map[string]string{"assistant": "ollama"}})
	if getRouting(t, s).Routing["assistant"].Provider != "ollama" {
		t.Error("routing to ollama puts the task back")
	}
	if rec := doJSON(t, h, http.MethodPut, "/api/routing", map[string]any{}); rec.Code != http.StatusBadRequest {
		t.Errorf("an empty body: %d", rec.Code)
	}
}

func TestRouting_RemoteNeedsConsent(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	addProv(t, s, map[string]any{"name": "Cloud", "type": "openai", "base_url": "https://api.example.com/v1", "text_model": "t", "vision_model": "v"})
	h := s.Handler()
	rec := doJSON(t, h, http.MethodPut, "/api/routing", map[string]any{"routing": map[string]string{"describe_image": "cloud"}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "consent_required") || !strings.Contains(rec.Body.String(), "Cloud") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if getRouting(t, s).Routing["describe_image"].Provider != "ollama" {
		t.Fatal("without consent the routing is unchanged")
	}
	rec = doJSON(t, h, http.MethodPut, "/api/routing", map[string]any{"routing": map[string]string{"describe_image": "cloud"}, "acknowledge_remote": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	r := getRouting(t, s).Routing["describe_image"]
	if r.Provider != "cloud" || !r.Remote || !r.Consented || r.Model != "v" {
		t.Fatalf("%+v", r)
	}
	// the address changes: the consent stops counting (the routing stays, and is shown as not consented)
	rec = doJSON(t, h, http.MethodPut, "/api/providers/cloud", map[string]any{"name": "Cloud", "type": "openai", "base_url": "https://moved.example.com/v1", "text_model": "t", "vision_model": "v"})
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if r := getRouting(t, s).Routing["describe_image"]; r.Consented {
		t.Fatalf("a new address needs a new acknowledgement: %+v", r)
	}
	// acknowledging by itself
	if rec := doJSON(t, h, http.MethodPost, "/api/providers/cloud/acknowledge", nil); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if r := getRouting(t, s).Routing["describe_image"]; !r.Consented {
		t.Fatalf("%+v", r)
	}
	if rec := doJSON(t, h, http.MethodPost, "/api/providers/ollama/acknowledge", nil); rec.Code != http.StatusNotFound {
		t.Errorf("the built-in needs none: %d", rec.Code)
	}
	if rec := doJSON(t, h, http.MethodPost, "/api/providers/ghost/acknowledge", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown: %d", rec.Code)
	}
}

func TestRouting_ProviderInUseCannotBeDeletedAndTheCapIsSaved(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	addProv(t, s, map[string]any{"name": "Lan", "type": "openai", "base_url": "http://127.0.0.1:8000/v1", "vision_model": "v"})
	h := s.Handler()
	doJSON(t, h, http.MethodPut, "/api/routing", map[string]any{"routing": map[string]string{"quality": "lan"}})
	rec := doJSON(t, h, http.MethodDelete, "/api/providers/lan", nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "quality") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	doJSON(t, h, http.MethodPut, "/api/routing", map[string]any{"routing": map[string]string{"quality": "ollama"}})
	if rec := doJSON(t, h, http.MethodDelete, "/api/providers/lan", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("unrouted, it goes: %d", rec.Code)
	}
	rec = doJSON(t, h, http.MethodPut, "/api/routing", map[string]any{"max_calls": 25})
	if rec.Code != http.StatusOK || getRouting(t, s).MaxCalls != 25 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, h, http.MethodPut, "/api/routing", map[string]any{"max_calls": 0}); rec.Code != http.StatusBadRequest {
		t.Errorf("the cap is at least 1: %d", rec.Code)
	}
}

func TestAssist_RoutedProviderAndPerCallOverrideWithoutOllama(t *testing.T) {
	isolateSettings(t)
	sim := &openaiSim{reply: func(string) string { return "A photorealistic photograph of a woman reading in a sunlit cafe." }}
	srv := sim.serve(t)
	t.Setenv("OLLAMA_HOST", "127.0.0.1:1") // Ollama is unreachable: anything that touched it would fail
	s, _ := newProjectsServer(t)
	addProv(t, s, map[string]any{"name": "Sim", "type": "openai", "base_url": srv.URL + "/v1", "text_model": "gpt-sim"})
	h := s.Handler()

	// the per-call provider replaces the routing for that call only
	rec := doJSON(t, h, http.MethodPost, "/api/assist", map[string]string{"task": "image", "text": "una mujer lee", "provider": "sim"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "woman reading") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(sim.paths) != 1 || sim.paths[0] != "/v1/chat/completions" {
		t.Fatalf("exactly one chat call: %v", sim.paths)
	}
	// without the parameter and without routing the built-in Ollama is used (here unreachable): no fallback
	rec = doJSON(t, h, http.MethodPost, "/api/assist", map[string]string{"task": "image", "text": "una mujer lee"})
	if rec.Code == http.StatusOK || len(sim.paths) != 1 {
		t.Fatalf("no routing, no override: the built-in is used and nothing goes to the other provider: %d calls=%d", rec.Code, len(sim.paths))
	}
	// routed: the same call now goes to the provider
	doJSON(t, h, http.MethodPut, "/api/routing", map[string]any{"routing": map[string]string{"assistant": "sim"}})
	rec = doJSON(t, h, http.MethodPost, "/api/assist", map[string]string{"task": "image", "text": "una mujer lee"})
	if rec.Code != http.StatusOK || len(sim.paths) != 2 {
		t.Fatalf("%d %s calls=%d", rec.Code, rec.Body, len(sim.paths))
	}
}

func TestAssist_ProviderOverrideNeedsConsentIfRemote(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	addProv(t, s, map[string]any{"name": "Cloud", "type": "openai", "base_url": "https://api.example.com/v1", "text_model": "t"})
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist", map[string]string{"task": "image", "text": "x", "provider": "cloud"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "consent_required") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/assist", map[string]string{"task": "image", "text": "x", "provider": "ghost"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown provider: %d", rec.Code)
	}
}

func TestAssist_ARemoteProviderDoesNotWaitForARunButOllamaDoes(t *testing.T) {
	isolateSettings(t)
	sim := &openaiSim{reply: func(string) string { return "A photorealistic photograph of a woman reading in a sunlit cafe." }}
	srv := sim.serve(t)
	s, _ := newProjectsServer(t)
	addProv(t, s, map[string]any{"name": "Sim", "type": "openai", "base_url": srv.URL + "/v1", "text_model": "m"})
	// a run in progress: the built-in Ollama (this machine's memory) waits, an OpenAI-compatible provider needs none
	releaseRun := s.workspace.SimulateRun("r1", nil)
	defer func() {
		releaseRun()
	}()
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist", map[string]string{"task": "image", "text": "x"}); rec.Code != http.StatusConflict {
		t.Fatalf("the local Ollama waits for a run: %d", rec.Code)
	}
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist", map[string]string{"task": "image", "text": "x", "provider": "sim"}); rec.Code != http.StatusOK {
		t.Fatalf("another provider needs no memory of this machine: %d %s", rec.Code, rec.Body)
	}
}

func TestDescribe_TooManyCallsIs400WithItsCode(t *testing.T) {
	isolateSettings(t)
	s, file := describeServer(t, func(path string, opts describe.Options) (describe.Result, error) {
		return describe.Result{}, &describe.TooManyCallsError{Planned: 36, Cap: 30, Provider: "Cloud"}
	})
	_ = file
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/describe", map[string]any{"project": "p", "source": "ref.png"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "too_many_calls") || !strings.Contains(rec.Body.String(), "36") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestDescribe_RefusalBodyCarriesTheAlternativesWithTheirProviders(t *testing.T) {
	isolateSettings(t)
	s, _ := describeServer(t, func(path string, opts describe.Options) (describe.Result, error) {
		return describe.Result{}, &describe.RefusedError{
			Provider: "cloud", ProviderName: "Cloud", Model: "gpt-4o",
			Choices: []describe.Alternative{{Provider: "ollama", Name: "Ollama", Model: "gemma4:26b"}},
		}
	})
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/describe", map[string]any{"project": "p", "source": "ref.png"})
	var body struct {
		Error struct {
			Code         string                 `json:"code"`
			Provider     string                 `json:"provider"`
			Alternatives []describe.Alternative `json:"alternatives"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusUnprocessableEntity || body.Error.Code != "describe_refused" || body.Error.Provider != "cloud" ||
		len(body.Error.Alternatives) != 1 || body.Error.Alternatives[0].Provider != "ollama" || body.Error.Alternatives[0].Model != "gemma4:26b" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestDescribe_ThroughARoutedProviderForReal(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	isolateSettings(t)
	text := "A photorealistic portrait of one adult woman in her late twenties with long wavy auburn hair, wearing a black lace bra and matching panties, sitting on the edge of an unmade bed in a dim bedroom, soft warm window light from the left, shallow depth of field, medium shot, confident smile."
	sim := &openaiSim{reply: func(sys string) string {
		if strings.HasPrefix(sys, "You check a picture") {
			return `{"people":1,"minor":false,"unclear":false}`
		}
		return text
	}}
	srv := sim.serve(t)
	t.Setenv("OLLAMA_HOST", "127.0.0.1:1")
	s, projects := newProjectsServer(t)
	createProject(t, s, "p")
	addProv(t, s, map[string]any{"name": "Sim", "type": "openai", "base_url": srv.URL + "/v1", "vision_model": "gpt-vision"})
	doJSON(t, s.Handler(), http.MethodPut, "/api/routing", map[string]any{"routing": map[string]string{"describe_image": "sim"}})
	src := filepath.Join(projects, "p", "sources")
	_ = os.MkdirAll(src, 0o755)
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for i := 0; i < 64; i++ {
		img.Set(i, i, color.RGBA{200, 50, 50, 255})
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	_ = os.WriteFile(filepath.Join(src, "ref.png"), buf.Bytes(), 0o644)

	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/describe", map[string]any{"project": "p", "source": "ref.png"})
	var res describe.Result
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != http.StatusOK || res.Provider != "sim" || res.Model != "gpt-vision" || res.Prompt != text {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for _, p := range sim.paths {
		if p != "/v1/chat/completions" {
			t.Errorf("only chat calls (nothing of Ollama's): %s", p)
		}
	}
}

func TestRouting_TokenIsRequired(t *testing.T) {
	isolateSettings(t)
	s := newAuthServer(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/routing"},
		{http.MethodPut, "/api/routing"},
		{http.MethodPost, "/api/providers/x/acknowledge"},
	} {
		if rec := do(s.Handler(), c.method, c.path, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a token: %d, want 401", c.method, c.path, rec.Code)
		}
	}
}

func TestReviewStart_RoutedRemoteWithoutConsentDoesNotStart(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	createProject(t, s, "p")
	addProv(t, s, map[string]any{"name": "Cloud", "type": "openai", "base_url": "https://api.example.com/v1", "vision_model": "v"})
	doJSON(t, s.Handler(), http.MethodPut, "/api/routing", map[string]any{"routing": map[string]string{"quality": "cloud"}, "acknowledge_remote": true})
	// the address changes afterwards: the consent is gone and the check must not start
	doJSON(t, s.Handler(), http.MethodPut, "/api/providers/cloud", map[string]any{"name": "Cloud", "type": "openai", "base_url": "https://moved.example.com/v1", "vision_model": "v"})
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/p/reviews", map[string]any{})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "consent_required") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

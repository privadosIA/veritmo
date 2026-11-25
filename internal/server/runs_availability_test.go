package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"veritmo/internal/platform/settings"
)

type availBody struct {
	Tasks map[string]taskAvailability `json:"tasks"`
}

func getAvailability(t *testing.T, s *Server) availBody {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/models/availability", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var b availBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	return b
}

// ollamaWith serves tags and show for the given models (name -> capabilities) and counts the tags calls.
func ollamaWith(t *testing.T, models map[string][]string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var tags atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			tags.Add(1)
			list := []map[string]any{}
			for n := range models {
				list = append(list, map[string]any{"name": n, "size": 1})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"models": list})
		case "/api/show":
			var req struct{ Model string }
			_ = json.NewDecoder(r.Body).Decode(&req)
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": models[req.Model]})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &tags
}

func TestAvailability_AWorkingOllamaMakesEveryTaskAvailable(t *testing.T) {
	isolateSettings(t)
	srv, _ := ollamaWith(t, map[string][]string{"qwen3:8b": {"completion"}, "gemma4:26b": {"completion", "vision"}})
	t.Setenv("OLLAMA_HOST", srv.URL)
	s, _ := newProjectsServer(t)
	b := getAvailability(t, s)
	if len(b.Tasks) != 5 {
		t.Fatalf("%+v", b)
	}
	for task, a := range b.Tasks {
		if !a.Available || a.Reason != "" || a.Provider.ID != "ollama" || a.Provider.Type != "ollama" {
			t.Errorf("%s: %+v", task, a)
		}
	}
}

func TestAvailability_OllamaStopped(t *testing.T) {
	isolateSettings(t)
	t.Setenv("OLLAMA_HOST", "127.0.0.1:1")
	s, _ := newProjectsServer(t)
	for task, a := range getAvailability(t, s).Tasks {
		if a.Available || a.Reason != "unreachable" || a.Provider.Name != "Ollama" {
			t.Errorf("%s: %+v", task, a)
		}
	}
}

func TestAvailability_OnlyTextModelsNoVision(t *testing.T) {
	isolateSettings(t)
	srv, _ := ollamaWith(t, map[string][]string{"qwen3:8b": {"completion"}})
	t.Setenv("OLLAMA_HOST", srv.URL)
	s, _ := newProjectsServer(t)
	b := getAvailability(t, s)
	if !b.Tasks["assistant"].Available {
		t.Errorf("the assistant has a chat model: %+v", b.Tasks["assistant"])
	}
	if !b.Tasks["motion_graphics"].Available {
		t.Errorf("writing a motion-graphics page needs a chat model, not one that reads pictures: %+v", b.Tasks["motion_graphics"])
	}
	for _, task := range []string{"describe_image", "describe_video", "quality"} {
		if a := b.Tasks[task]; a.Available || a.Reason != "no_vision_model" {
			t.Errorf("%s: %+v", task, a)
		}
	}
}

func TestAvailability_OnlyEmbeddingModelsIsNoChatModel(t *testing.T) {
	isolateSettings(t)
	srv, _ := ollamaWith(t, map[string][]string{"nomic-embed-text:latest": {"embedding"}, "dengcao/Qwen3-Reranker-0.6B:F16": {"completion"}})
	t.Setenv("OLLAMA_HOST", srv.URL)
	s, _ := newProjectsServer(t)
	b := getAvailability(t, s)
	for _, task := range []string{"assistant", "motion_graphics"} {
		if a := b.Tasks[task]; a.Available || a.Reason != "no_models" {
			t.Errorf("%s: embedding and reranker models do not count: %+v", task, a)
		}
	}
}

func TestAvailability_CloudProviderStates(t *testing.T) {
	isolateSettings(t)
	t.Setenv("OLLAMA_HOST", "127.0.0.1:1") // Ollama is down: only the routed provider matters
	s, _ := newProjectsServer(t)
	ok, _ := fakeOpenAI(t, 200)
	addProv(t, s, map[string]any{"name": "Sim", "type": "openai", "base_url": ok.URL + "/v1", "text_model": "t", "vision_model": "v", "key": secretKey})
	doJSON(t, s.Handler(), http.MethodPut, "/api/routing", map[string]any{"routing": map[string]string{"assistant": "sim", "describe_image": "sim"}})

	b := getAvailability(t, s)
	for _, task := range []string{"assistant", "describe_image"} {
		if a := b.Tasks[task]; !a.Available || a.Provider.ID != "sim" || a.Provider.Type != "openai" {
			t.Errorf("%s routed to a working provider: %+v", task, a)
		}
	}
	if a := b.Tasks["quality"]; a.Available || a.Reason != "unreachable" {
		t.Errorf("a task still on the stopped Ollama: %+v", a)
	}

	// the provider rejects the key
	bad, _ := fakeOpenAI(t, http.StatusUnauthorized)
	doJSON(t, s.Handler(), http.MethodPut, "/api/providers/sim", map[string]any{"name": "Sim", "type": "openai", "base_url": bad.URL + "/v1", "text_model": "t", "vision_model": "v"})
	if a := getAvailability(t, s).Tasks["assistant"]; a.Available || a.Reason != "key_rejected" {
		t.Errorf("a rejected key: %+v", a)
	}
}

func TestAvailability_NoConsentDoesNotContactTheProvider(t *testing.T) {
	isolateSettings(t)
	t.Setenv("OLLAMA_HOST", "127.0.0.1:1")
	s, _ := newProjectsServer(t)
	addProv(t, s, map[string]any{"name": "Cloud", "type": "openai", "base_url": "https://api.example.com/v1", "text_model": "t", "vision_model": "v"})
	doJSON(t, s.Handler(), http.MethodPut, "/api/routing", map[string]any{"routing": map[string]string{"assistant": "cloud"}, "acknowledge_remote": true})
	// the address changes afterwards: the acknowledgement stops counting
	doJSON(t, s.Handler(), http.MethodPut, "/api/providers/cloud", map[string]any{"name": "Cloud", "type": "openai", "base_url": "https://moved.example.com/v1", "text_model": "t", "vision_model": "v"})
	a := getAvailability(t, s).Tasks["assistant"]
	if a.Available || a.Reason != "no_consent" || a.Provider.Name != "Cloud" || a.Provider.Type != "openai" {
		t.Errorf("%+v", a)
	}
}

func TestAvailability_ProviderWithoutTheModelOfTheTask(t *testing.T) {
	isolateSettings(t)
	t.Setenv("OLLAMA_HOST", "127.0.0.1:1")
	s, _ := newProjectsServer(t)
	ok, _ := fakeOpenAI(t, 200)
	addProv(t, s, map[string]any{"name": "Sim", "type": "openai", "base_url": ok.URL + "/v1", "text_model": "t", "vision_model": "v"})
	doJSON(t, s.Handler(), http.MethodPut, "/api/routing", map[string]any{"routing": map[string]string{"describe_image": "sim"}})
	// the model is removed from the provider behind the routing's back (a hand-edited settings file)
	p := map[string]any{"id": "sim", "name": "Sim", "type": "openai", "base_url": ok.URL + "/v1", "text_model": "t"}
	writeProvidersFile(t, p)
	s.workspace.DropAvailability()
	if a := getAvailability(t, s).Tasks["describe_image"]; a.Available || a.Reason != "no_model" {
		t.Errorf("%+v", a)
	}
}

func TestAvailability_CachedAndDroppedByAnyChange(t *testing.T) {
	isolateSettings(t)
	srv, tags := ollamaWith(t, map[string][]string{"qwen3:8b": {"completion"}, "gemma4:26b": {"completion", "vision"}})
	t.Setenv("OLLAMA_HOST", srv.URL)
	s, _ := newProjectsServer(t)
	getAvailability(t, s)
	first := tags.Load()
	if first == 0 {
		t.Fatal("the first call must ask Ollama")
	}
	getAvailability(t, s)
	getAvailability(t, s)
	if tags.Load() != first {
		t.Fatalf("within 30 s Ollama is asked once: %d then %d", first, tags.Load())
	}
	for name, change := range map[string]func(){
		"the Ollama address": func() {
			doJSON(t, s.Handler(), http.MethodPut, "/api/settings", map[string]any{"ollama_host": srv.URL})
		},
		"a provider": func() {
			doJSON(t, s.Handler(), http.MethodPost, "/api/providers", map[string]any{"name": "Extra", "type": "openai", "base_url": "http://127.0.0.1:9/v1", "text_model": "t"})
		},
		"the routing":    func() { doJSON(t, s.Handler(), http.MethodPut, "/api/routing", map[string]any{"max_calls": 31}) },
		"an acknowledge": func() { doJSON(t, s.Handler(), http.MethodPost, "/api/providers/nope/acknowledge", nil) },
	} {
		before := tags.Load()
		getAvailability(t, s) // cached
		if tags.Load() != before {
			t.Fatalf("%s: should be cached before the change", name)
		}
		change()
		getAvailability(t, s)
		if tags.Load() == before {
			t.Errorf("%s must drop the cache", name)
		}
	}
}

func TestAvailability_CacheExpires(t *testing.T) {
	isolateSettings(t)
	srv, tags := ollamaWith(t, map[string][]string{"qwen3:8b": {"completion"}})
	t.Setenv("OLLAMA_HOST", srv.URL)
	s, _ := newProjectsServer(t)
	s.workspace.SetAvailabilityTTL(20 * time.Millisecond)
	getAvailability(t, s)
	n := tags.Load()
	time.Sleep(60 * time.Millisecond)
	getAvailability(t, s)
	if tags.Load() == n {
		t.Error("an expired answer must be checked again")
	}
}

func TestAvailability_TokenIsRequiredAndNothingOfTheUserIsSent(t *testing.T) {
	isolateSettings(t)
	a := newAuthServer(t)
	if rec := do(a.Handler(), http.MethodGet, "/api/models/availability", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("without a token: %d", rec.Code)
	}
	// only tags and show are asked of Ollama, never a chat or a load
	var other atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"qwen3:8b","size":1}]}`))
		case "/api/show":
			_, _ = w.Write([]byte(`{"capabilities":["completion"]}`))
		default:
			other.Add(1)
		}
	}))
	defer srv.Close()
	t.Setenv("OLLAMA_HOST", srv.URL)
	s, _ := newProjectsServer(t)
	getAvailability(t, s)
	if other.Load() != 0 {
		t.Errorf("availability must not chat with or load any model: %d other calls", other.Load())
	}
}

// writeProvidersFile rewrites the saved provider list directly, as a hand edit would.
func writeProvidersFile(t *testing.T, providers ...map[string]any) {
	t.Helper()
	data, _ := os.ReadFile(settingsFilePath())
	var f map[string]any
	_ = json.Unmarshal(data, &f)
	f["providers"] = providers
	out, _ := json.Marshal(f)
	if err := os.WriteFile(settingsFilePath(), out, 0o600); err != nil {
		t.Fatal(err)
	}
}

func settingsFilePath() string { return settings.Path() }

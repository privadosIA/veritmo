package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"veritmo/internal/platform/providers"
	"veritmo/internal/platform/settings"
	"veritmo/internal/server/assistant"
)

// fakeChat serves the chat and, when models is true, /v1/models; it records the Authorization header and
// the last chat body. chatStatus is what the chat answers.
func fakeChat(t *testing.T, chatStatus int, models bool) (*httptest.Server, *atomic.Value, *atomic.Value) {
	t.Helper()
	var auth, body atomic.Value
	auth.Store("")
	body.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization"))
		switch {
		case r.URL.Path == "/v1/chat/completions":
			b, _ := io.ReadAll(r.Body)
			body.Store(string(b))
			if chatStatus != http.StatusOK {
				w.WriteHeader(chatStatus)
				_, _ = w.Write([]byte(`{"error":{"message":"nope"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"OK"},"finish_reason":"stop"}]}`))
		case r.URL.Path == "/v1/models" && models:
			_, _ = w.Write([]byte(`{"data":[{"id":"models/gemini-2.5-pro"},{"id":"gemini-2.5-flash"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &auth, &body
}

func TestPresets_TheRouteListsThemAll(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/providers/presets", nil)
	var res struct {
		Presets []providers.Preset `json:"presets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var ids []string
	for _, p := range res.Presets {
		ids = append(ids, p.ID)
		if p.BaseURL == "" || len(p.TextModels) == 0 || p.KeyHint == "" {
			t.Errorf("preset %q is incomplete: %+v", p.ID, p)
		}
	}
	if strings.Join(ids, ",") != "anthropic,gemini,openrouter,openai" {
		t.Errorf("presets and their order: %v", ids)
	}
	if res.Presets[0].ListsModels || !res.Presets[1].ListsModels {
		t.Errorf("anthropic must not list models and gemini must")
	}
}

func TestPresets_CreateFromAnthropicWithOnlyAKey(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/providers", map[string]any{"preset": "anthropic", "key": secretKey})
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var v assistant.ProviderView
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if v.Type != "openai" || v.BaseURL != "https://api.anthropic.com/v1" || v.TextModel != "claude-sonnet-5-5" || v.VisionModel != "claude-sonnet-5-5" ||
		v.Preset != "anthropic" || v.ListsModels || !v.KeySet || !v.Remote {
		t.Fatalf("%+v", v)
	}
	if strings.Contains(rec.Body.String(), secretKey) {
		t.Error("the key must not be in the answer")
	}
}

func TestPresets_CreateFromGeminiKeepingWhatTheRequestNames(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/providers", map[string]any{"preset": "gemini", "key": secretKey, "text_model": "gemini-2.5-flash"})
	var v assistant.ProviderView
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if rec.Code != http.StatusCreated || v.TextModel != "gemini-2.5-flash" || v.VisionModel != "gemini-flash-latest" || !v.ListsModels ||
		v.BaseURL != "https://generativelanguage.googleapis.com/v1beta/openai" {
		t.Fatalf("%d %+v", rec.Code, v)
	}
}

func TestPresets_UnknownPresetIs400NamingTheKnownOnes(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/providers", map[string]any{"preset": "nope"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "anthropic, gemini, openrouter, openai") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestPresets_EditingTheAddressClearsThePreset(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	doJSON(t, s.Handler(), http.MethodPost, "/api/providers", map[string]any{"preset": "openrouter", "key": secretKey})
	l, _ := listProviders(t, s)
	id := l.Providers[1].ID
	if l.Providers[1].Preset != "openrouter" {
		t.Fatalf("%+v", l.Providers[1])
	}
	// the same address keeps it, even with another model
	doJSON(t, s.Handler(), http.MethodPut, "/api/providers/"+id, map[string]any{"name": "OR", "type": "openai", "base_url": "https://openrouter.ai/api/v1", "text_model": "x/y"})
	l, _ = listProviders(t, s)
	if l.Providers[1].Preset != "openrouter" {
		t.Errorf("the preset must stay when the address is the same: %+v", l.Providers[1])
	}
	doJSON(t, s.Handler(), http.MethodPut, "/api/providers/"+id, map[string]any{"name": "OR", "type": "openai", "base_url": "https://api.openai.com/v1", "text_model": "x/y"})
	l, _ = listProviders(t, s)
	if l.Providers[1].Preset != "" {
		t.Errorf("a new address clears the preset: %+v", l.Providers[1])
	}
}

func saveFake(t *testing.T, id, preset, base, text string) {
	t.Helper()
	if _, err := settings.AddProvider(settings.Provider{ID: id, Name: id, Type: "openai", BaseURL: base, TextModel: text, VisionModel: text, Preset: preset}); err != nil {
		t.Fatal(err)
	}
	if err := settings.SetKey(id, secretKey); err != nil {
		t.Fatal(err)
	}
}

func testResult(t *testing.T, s *Server, id string) assistant.ProviderTestResult {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/providers/"+id+"/test", nil)
	var res assistant.ProviderTestResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), secretKey) {
		t.Errorf("the key must not be in the answer: %s", rec.Body)
	}
	return res
}

func TestProviderTest_ChatAndModelsBothAnswer(t *testing.T) {
	isolateSettings(t)
	srv, auth, body := fakeChat(t, http.StatusOK, true)
	s, _ := newProjectsServer(t)
	saveFake(t, "g", "gemini", srv.URL+"/v1", "gemini-2.5-pro")
	res := testResult(t, s, "g")
	if !res.OK || res.Error != "" || len(res.Models) != 2 || res.Models[0].Name != "gemini-2.5-pro" || res.ModelsNote != "" {
		t.Fatalf("%+v", res)
	}
	if auth.Load() != "Bearer "+secretKey {
		t.Errorf("the key goes as a Bearer token: %q", auth.Load())
	}
	b := body.Load().(string)
	if !strings.Contains(b, `"Reply with OK"`) || !strings.Contains(b, `"max_tokens":512`) || !strings.Contains(b, `"gemini-2.5-pro"`) {
		t.Errorf("the chat is the short constant one to the text model: %s", b)
	}
	l, _ := listProviders(t, s)
	if !l.Providers[1].TestOK || l.Providers[1].TestedAt == "" {
		t.Errorf("the test is remembered: %+v", l.Providers[1])
	}
}

func TestProviderTest_ChatOkWithoutAModelsEndpoint(t *testing.T) {
	isolateSettings(t)
	srv, _, _ := fakeChat(t, http.StatusOK, false)
	s, _ := newProjectsServer(t)
	saveFake(t, "claude", "anthropic", srv.URL+"/v1", "claude-sonnet-5-5")
	res := testResult(t, s, "claude")
	if !res.OK || len(res.Models) != 0 || !strings.Contains(res.ModelsNote, "type the model names by hand") {
		t.Fatalf("a provider that does not list models is not a failure: %+v", res)
	}
}

func TestProviderTest_AKeyThatListsButCannotChatFails(t *testing.T) {
	isolateSettings(t)
	srv, _, _ := fakeChat(t, http.StatusUnauthorized, true)
	s, _ := newProjectsServer(t)
	saveFake(t, "or", "openrouter", srv.URL+"/v1", "x/y")
	res := testResult(t, s, "or")
	if res.OK || !strings.Contains(res.Error, "the key was rejected by") {
		t.Fatalf("listing models proves nothing about the key: %+v", res)
	}
	l, _ := listProviders(t, s)
	if l.Providers[1].TestOK {
		t.Error("a failed test is remembered as failed")
	}
}

func TestProviderTest_ChangingTheKeyForgetsTheTest(t *testing.T) {
	isolateSettings(t)
	srv, _, _ := fakeChat(t, http.StatusOK, true)
	s, _ := newProjectsServer(t)
	saveFake(t, "g", "gemini", srv.URL+"/v1", "m")
	testResult(t, s, "g")
	doJSON(t, s.Handler(), http.MethodPut, "/api/providers/g", map[string]any{"name": "g", "type": "openai", "base_url": srv.URL + "/v1", "text_model": "m", "key": secretKey + "2"})
	l, _ := listProviders(t, s)
	if l.Providers[1].TestOK || l.Providers[1].TestedAt != "" {
		t.Errorf("a new key clears the test: %+v", l.Providers[1])
	}
}

func TestProviderTest_NoTextModelFallsBackToTheList(t *testing.T) {
	isolateSettings(t)
	srv, _, _ := fakeChat(t, http.StatusOK, true)
	s, _ := newProjectsServer(t)
	saveFake(t, "lm", "", srv.URL+"/v1", "")
	if res := testResult(t, s, "lm"); !res.OK || len(res.Models) != 2 || res.ChatMS != 0 {
		t.Errorf("%+v", res)
	}
	srv2, _, _ := fakeChat(t, http.StatusOK, false)
	saveFake(t, "claude", "anthropic", srv2.URL+"/v1", "")
	if res := testResult(t, s, "claude"); res.OK || !strings.Contains(res.Error, "set a text model") {
		t.Errorf("a provider with no model and no list cannot be tested: %+v", res)
	}
}

func suggestedFor(t *testing.T, s *Server) map[string]map[string]string {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/routing", nil)
	var res struct {
		Routing   map[string]map[string]any    `json:"routing"`
		Suggested map[string]map[string]string `json:"suggested"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for task, r := range res.Routing {
		if r["provider"] != "ollama" {
			t.Errorf("a suggestion changes nothing by itself: %s routes to %v", task, r["provider"])
		}
	}
	return res.Suggested
}

func TestRoutingSuggestion(t *testing.T) {
	isolateSettings(t)
	srv, _, _ := fakeChat(t, http.StatusOK, true)
	s, _ := newProjectsServer(t)
	if sg := suggestedFor(t, s); sg != nil {
		t.Fatalf("only Ollama: no suggestion, got %v", sg)
	}
	saveFake(t, "g", "gemini", srv.URL+"/v1", "gemini-2.5-pro")
	saveFake(t, "custom", "", srv.URL+"/v1", "m")
	if sg := suggestedFor(t, s); sg != nil {
		t.Fatalf("untested: no suggestion, got %v", sg)
	}
	testResult(t, s, "g")
	testResult(t, s, "custom")
	sg := suggestedFor(t, s)
	for _, task := range []string{"motion_graphics", "quality"} {
		if sg[task]["provider_id"] != "g" || sg[task]["name"] != "g" || sg[task]["reason"] == "" {
			t.Errorf("%s: %v", task, sg[task])
		}
	}
	// a better-ranked preset wins
	saveFake(t, "claude", "anthropic", srv.URL+"/v1", "claude-sonnet-5-5")
	testResult(t, s, "claude")
	if sg := suggestedFor(t, s); sg["motion_graphics"]["provider_id"] != "claude" {
		t.Errorf("anthropic ranks first: %v", sg)
	}
	// taking the suggestion is the person's PUT, with consent
	rec := doJSON(t, s.Handler(), http.MethodPut, "/api/routing", map[string]any{"routing": map[string]string{"motion_graphics": "g"}, "acknowledge_remote": true})
	if rec.Code == http.StatusBadRequest {
		t.Errorf("PUT /api/routing is unchanged: %d %s", rec.Code, rec.Body)
	}
}

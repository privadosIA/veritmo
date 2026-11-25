package server

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"veritmo/internal/platform/settings"
	"veritmo/internal/server/assistant"
)

const secretKey = "sk-test-DO-NOT-LEAK-123456"

// fakeOpenAI serves /v1/models and records the Authorization header it received.
func fakeOpenAI(t *testing.T, status int) (*httptest.Server, *atomic.Value) {
	t.Helper()
	var auth atomic.Value
	auth.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization"))
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if status != 200 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"rejected ` + secretKey + `"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4o"},{"id":"gpt-4o-mini"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &auth
}

type provList struct {
	Providers []assistant.ProviderView `json:"providers"`
}

func listProviders(t *testing.T, s *Server) (provList, string) {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/providers", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var l provList
	if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil {
		t.Fatal(err)
	}
	return l, rec.Body.String()
}

func TestProviders_ListHasTheBuiltinFirst(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	l, _ := listProviders(t, s)
	if len(l.Providers) != 1 {
		t.Fatalf("%+v", l)
	}
	b := l.Providers[0]
	if b.ID != "ollama" || !b.Builtin || b.Type != "ollama" || b.BaseURL != "http://127.0.0.1:11434" || b.Remote || b.KeySet {
		t.Errorf("built-in: %+v", b)
	}
	// it follows the Ollama address
	doJSON(t, s.Handler(), http.MethodPut, "/api/settings", map[string]string{"ollama_host": "192.168.1.50"})
	l, _ = listProviders(t, s)
	if l.Providers[0].BaseURL != "http://192.168.1.50:11434" || !l.Providers[0].Remote {
		t.Errorf("the built-in must follow the Ollama setting: %+v", l.Providers[0])
	}
}

func TestProviders_CreateEditDelete(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	h := s.Handler()
	rec := doJSON(t, h, http.MethodPost, "/api/providers", map[string]any{
		"name": "Open Router", "type": "openai", "base_url": "https://openrouter.ai/api/v1/",
		"text_model": "gpt-4o-mini", "vision_model": "gpt-4o", "key": secretKey,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var v assistant.ProviderView
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if v.ID != "open-router" || v.BaseURL != "https://openrouter.ai/api/v1" || !v.Remote || !v.KeySet || v.Builtin {
		t.Fatalf("created: %+v", v)
	}
	if strings.Contains(rec.Body.String(), secretKey) {
		t.Fatalf("the key must not be in the answer: %s", rec.Body)
	}

	// edit without a key field: the key stays
	rec = doJSON(t, h, http.MethodPut, "/api/providers/open-router", map[string]any{
		"name": "OpenRouter", "type": "openai", "base_url": "https://openrouter.ai/api/v1", "vision_model": "gpt-4o-mini",
	})
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if rec.Code != http.StatusOK || v.Name != "OpenRouter" || v.VisionModel != "gpt-4o-mini" || !v.KeySet {
		t.Fatalf("edit: %d %+v", rec.Code, v)
	}
	// an empty key removes it, the provider remains
	rec = doJSON(t, h, http.MethodPut, "/api/providers/open-router", map[string]any{
		"name": "OpenRouter", "type": "openai", "base_url": "https://openrouter.ai/api/v1", "key": "",
	})
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if rec.Code != http.StatusOK || v.KeySet {
		t.Fatalf("removing the key: %d %+v", rec.Code, v)
	}
	// setting a key again
	rec = doJSON(t, h, http.MethodPut, "/api/providers/open-router", map[string]any{
		"name": "OpenRouter", "type": "openai", "base_url": "https://openrouter.ai/api/v1", "key": secretKey,
	})
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if !v.KeySet {
		t.Fatalf("key not set again: %+v", v)
	}

	l, raw := listProviders(t, s)
	if len(l.Providers) != 2 || strings.Contains(raw, secretKey) {
		t.Fatalf("list: %d providers, key leaked: %v", len(l.Providers), strings.Contains(raw, secretKey))
	}

	if rec := doJSON(t, h, http.MethodDelete, "/api/providers/open-router", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	l, _ = listProviders(t, s)
	if len(l.Providers) != 1 {
		t.Errorf("the provider must be gone: %+v", l)
	}
	if rec := doJSON(t, h, http.MethodDelete, "/api/providers/open-router", nil); rec.Code != http.StatusNotFound {
		t.Errorf("deleting twice: %d", rec.Code)
	}
	if rec := doJSON(t, h, http.MethodPut, "/api/providers/nope", map[string]any{"name": "x", "type": "openai", "base_url": "https://h/v1"}); rec.Code != http.StatusNotFound {
		t.Errorf("editing an unknown id: %d", rec.Code)
	}
}

func TestProviders_KeyOnlyInKeysFile(t *testing.T) {
	dir := isolateSettings(t)
	s, _ := newProjectsServer(t)
	doJSON(t, s.Handler(), http.MethodPost, "/api/providers", map[string]any{
		"name": "A", "type": "openai", "base_url": "https://api.example.com/v1", "key": secretKey,
	})
	cfg := filepath.Join(dir, "veritmo")
	keys, err := os.ReadFile(filepath.Join(cfg, "keys.json"))
	if err != nil || !strings.Contains(string(keys), secretKey) {
		t.Fatalf("the key must be in keys.json: %v %s", err, keys)
	}
	fi, _ := os.Stat(filepath.Join(cfg, "keys.json"))
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("keys.json mode %v", fi.Mode().Perm())
	}
	entries, _ := os.ReadDir(cfg)
	for _, e := range entries {
		if e.Name() == "keys.json" {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(cfg, e.Name()))
		if strings.Contains(string(b), secretKey) {
			t.Errorf("the key leaked into %s", e.Name())
		}
	}
	// deleting the provider deletes the key
	doJSON(t, s.Handler(), http.MethodDelete, "/api/providers/a", nil)
	if _, err := os.Stat(filepath.Join(cfg, "keys.json")); !os.IsNotExist(err) {
		t.Error("deleting the provider must delete its key")
	}
}

func TestProviders_InvalidInputIs400(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	for name, body := range map[string]map[string]any{
		"capital id":     {"id": "Open", "name": "x", "type": "openai", "base_url": "https://h/v1"},
		"builtin id":     {"id": "ollama", "name": "x", "type": "openai", "base_url": "https://h/v1"},
		"credentials":    {"name": "x", "type": "openai", "base_url": "https://u:p@h/v1"},
		"path not v1":    {"name": "x", "type": "openai", "base_url": "https://h/chat"},
		"query":          {"name": "x", "type": "openai", "base_url": "https://h/v1?x=1"},
		"no scheme":      {"name": "x", "type": "openai", "base_url": "h.example.com/v1"},
		"bad type":       {"name": "x", "type": "anthropic", "base_url": "https://h/v1"},
		"no name":        {"type": "openai", "base_url": "https://h/v1"},
		"key with space": {"name": "x", "type": "openai", "base_url": "https://h/v1", "key": "has space"},
	} {
		rec := doJSON(t, s.Handler(), http.MethodPost, "/api/providers", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d: %s", name, rec.Code, rec.Body)
		}
	}
	if l, _ := listProviders(t, s); len(l.Providers) != 1 {
		t.Errorf("nothing may be saved by a refused request: %+v", l)
	}
}

func TestProviders_BuiltinCannotBeEditedOrRemoved(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodPut, "/api/providers/ollama"},
		{http.MethodDelete, "/api/providers/ollama"},
	} {
		rec := doJSON(t, s.Handler(), c.method, c.path, map[string]any{"name": "x", "type": "ollama", "base_url": "http://h"})
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Ollama settings") {
			t.Errorf("%s %s: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
}

func TestProviders_NoKeyOverPlainHTTPToAnotherMachine(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/providers", map[string]any{
		"name": "LAN", "type": "openai", "base_url": "http://192.168.1.60:8000/v1", "key": secretKey,
	})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "https") {
		t.Fatalf("plain http with a key must be refused with a hint to use https: %d %s", rec.Code, rec.Body)
	}
	// without a key it is fine
	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/providers", map[string]any{"name": "LAN", "type": "openai", "base_url": "http://192.168.1.60:8000/v1"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("no key, plain http: %d %s", rec.Code, rec.Body)
	}
	// adding a key later is refused too
	rec = doJSON(t, s.Handler(), http.MethodPut, "/api/providers/lan", map[string]any{"name": "LAN", "type": "openai", "base_url": "http://192.168.1.60:8000/v1", "key": secretKey})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a key added over plain http: %d", rec.Code)
	}
	// loopback http with a key is allowed
	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/providers", map[string]any{"name": "Local", "type": "openai", "base_url": "http://127.0.0.1:8000/v1", "key": secretKey})
	if rec.Code != http.StatusCreated {
		t.Errorf("loopback http with a key: %d %s", rec.Code, rec.Body)
	}
}

func TestProviders_TestListsModelsAndSendsTheKeyAsBearer(t *testing.T) {
	isolateSettings(t)
	srv, auth := fakeOpenAI(t, 200)
	s, _ := newProjectsServer(t)
	doJSON(t, s.Handler(), http.MethodPost, "/api/providers", map[string]any{"name": "Sim", "type": "openai", "base_url": srv.URL + "/v1", "key": secretKey})
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/providers/sim/test", nil)
	var res assistant.ProviderTestResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != http.StatusOK || !res.OK || len(res.Models) != 2 || res.Models[0].Name != "gpt-4o" || res.Remote {
		t.Fatalf("%d %+v", rec.Code, res)
	}
	if auth.Load() != "Bearer "+secretKey {
		t.Errorf("the key must be sent as a Bearer token, got %q", auth.Load())
	}
	if strings.Contains(rec.Body.String(), secretKey) {
		t.Errorf("the key must not be in the answer: %s", rec.Body)
	}
}

func TestProviders_TestRejectedKeyIs200WithOKFalseAndNoKeyInTheMessage(t *testing.T) {
	isolateSettings(t)
	srv, _ := fakeOpenAI(t, http.StatusUnauthorized)
	s, _ := newProjectsServer(t)
	doJSON(t, s.Handler(), http.MethodPost, "/api/providers", map[string]any{"name": "Sim", "type": "openai", "base_url": srv.URL + "/v1", "key": secretKey})
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/providers/sim/test", nil)
	var res assistant.ProviderTestResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != http.StatusOK || res.OK || !strings.Contains(res.Error, "the key was rejected by") {
		t.Fatalf("%d %+v", rec.Code, res)
	}
	if strings.Contains(rec.Body.String(), secretKey) {
		t.Errorf("the key must not be in the message: %s", rec.Body)
	}
}

func TestProviders_TestNothingAnswering(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	doJSON(t, s.Handler(), http.MethodPost, "/api/providers", map[string]any{"name": "Dead", "type": "openai", "base_url": "http://127.0.0.1:1/v1"})
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/providers/dead/test", nil)
	var res assistant.ProviderTestResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != http.StatusOK || res.OK || !strings.Contains(res.Error, "127.0.0.1:1") {
		t.Fatalf("want 200 ok false naming the address: %d %+v", rec.Code, res)
	}
}

func TestProviders_TestDoesNotFollowRedirectsNorLeakTheKey(t *testing.T) {
	isolateSettings(t)
	var leaked atomic.Value
	leaked.Store("")
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Store(r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	s, _ := newProjectsServer(t)
	doJSON(t, s.Handler(), http.MethodPost, "/api/providers", map[string]any{"name": "R", "type": "openai", "base_url": redirector.URL + "/v1", "key": secretKey})
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/providers/r/test", nil)
	var res assistant.ProviderTestResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if res.OK || leaked.Load() != "" {
		t.Fatalf("a redirect must not be followed: ok=%v key sent to the target=%q", res.OK, leaked.Load())
	}
}

func TestProviders_TestTheBuiltinOllamaAndAnExtraOne(t *testing.T) {
	isolateSettings(t)
	ollama, _ := fakeOllama(t)
	s, _ := newProjectsServer(t)
	t.Setenv("OLLAMA_HOST", ollama.URL)
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/providers/ollama/test", nil)
	var res assistant.ProviderTestResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != http.StatusOK || !res.OK || len(res.Models) != 2 || res.Models[0].Vision == nil {
		t.Fatalf("built-in: %d %+v", rec.Code, res)
	}
	doJSON(t, s.Handler(), http.MethodPost, "/api/providers", map[string]any{"name": "Lab", "type": "ollama", "base_url": ollama.URL})
	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/providers/lab/test", nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if !res.OK {
		t.Fatalf("extra ollama: %+v", res)
	}
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/providers/nope/test", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown id: %d", rec.Code)
	}
}

func TestProviders_TheKeyNeverReachesTheLogs(t *testing.T) {
	isolateSettings(t)
	srv, _ := fakeOpenAI(t, http.StatusUnauthorized)
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)
	s, _ := newProjectsServer(t)
	h := s.Handler()
	doJSON(t, h, http.MethodPost, "/api/providers", map[string]any{"name": "Sim", "type": "openai", "base_url": srv.URL + "/v1", "key": secretKey})
	doJSON(t, h, http.MethodPost, "/api/providers/sim/test", nil)
	doJSON(t, h, http.MethodPut, "/api/providers/sim", map[string]any{"name": "Sim", "type": "openai", "base_url": srv.URL + "/v1", "key": secretKey + "2"})
	doJSON(t, h, http.MethodGet, "/api/providers", nil)
	doJSON(t, h, http.MethodDelete, "/api/providers/sim", nil)
	if strings.Contains(buf.String(), secretKey) {
		t.Fatalf("the key reached the log: %s", buf.String())
	}
}

func TestProviders_TokenIsRequired(t *testing.T) {
	isolateSettings(t)
	s := newAuthServer(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/providers"},
		{http.MethodPost, "/api/providers"},
		{http.MethodPut, "/api/providers/x"},
		{http.MethodDelete, "/api/providers/x"},
		{http.MethodPost, "/api/providers/x/test"},
	} {
		if rec := do(s.Handler(), c.method, c.path, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a token: %d, want 401", c.method, c.path, rec.Code)
		}
	}
}

// databaseSettingsServer starts a server whose settings are in the database, with settings.json (and the keys file) as they were before the upgrade.
func databaseSettingsServer(t *testing.T, legacy string, keys map[string]string) *Server {
	t.Helper()
	dir := isolateSettings(t)
	t.Setenv("HOME", dir)
	cfgDir := filepath.Join(dir, "veritmo")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "settings.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if len(keys) > 0 {
		raw, _ := json.Marshal(keys)
		if err := os.WriteFile(filepath.Join(cfgDir, "keys.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, _ := newProjectsServerWith(t, func(cfg *Config) { cfg.DatabaseState = true })
	t.Cleanup(func() { settings.UseSource(nil) }) // a test does not stop its server: give the source back
	return s
}

const legacyProviders = `{"providers": [
  {"id": "zeta", "name": "Zeta", "type": "openai", "base_url": "https://zeta.example/v1", "text_model": "m"},
  {"id": "alpha", "name": "Alpha", "type": "openai", "base_url": "https://alpha.example/v1"}
], "routing": {"assistant": "zeta"}, "consent": {"zeta": "https://zeta.example/v1", "alpha": "https://alpha.example/v1"}}`

func TestProviders_AfterTheImportTheSameProvidersInTheSameOrderEachWithItsKey(t *testing.T) {
	s := databaseSettingsServer(t, legacyProviders, map[string]string{"zeta": secretKey})
	l, body := listProviders(t, s)
	if len(l.Providers) != 3 || l.Providers[0].ID != "ollama" || l.Providers[1].ID != "zeta" || l.Providers[2].ID != "alpha" {
		t.Fatalf("the order the user gave: %+v", l.Providers)
	}
	if !l.Providers[1].KeySet || l.Providers[2].KeySet {
		t.Fatalf("keys: %+v", l.Providers)
	}
	if strings.Contains(body, secretKey) {
		t.Fatal("a key is in the answer")
	}
	// the providers are rows of the database now: the file is not read for them any more
	raw, _ := json.Marshal(map[string]any{"providers": []any{}})
	if err := os.WriteFile(settings.Path(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if l, _ := listProviders(t, s); len(l.Providers) != 3 {
		t.Fatalf("a hand edit of settings.json changed the providers: %+v", l.Providers)
	}
}

func TestProviders_OneProviderWithHasKeyAndNeverItsKey(t *testing.T) {
	s := databaseSettingsServer(t, legacyProviders, map[string]string{"zeta": secretKey})
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/providers/zeta", nil)
	var v assistant.ProviderView
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if rec.Code != http.StatusOK || v.ID != "zeta" || v.Name != "Zeta" || !v.KeySet || strings.Contains(rec.Body.String(), secretKey) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/providers/ollama", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"builtin":true`) {
		t.Fatalf("the built-in: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/providers/nope", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown: %d", rec.Code)
	}
	// "presets" is its own route, not a provider
	if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/providers/presets", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "presets") {
		t.Fatalf("presets: %d", rec.Code)
	}
}

func TestProviders_DeletingOneRemovesItsRowItsKeyAndItsAcknowledgement(t *testing.T) {
	s := databaseSettingsServer(t, legacyProviders, map[string]string{"zeta": secretKey, "alpha": "alpha-key"})
	h := s.Handler()
	// the one a task is routed to is not deleted: no task is left pointing at a provider that is gone
	if rec := doJSON(t, h, http.MethodDelete, "/api/providers/zeta", nil); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "assistant") {
		t.Fatalf("in use: %d %s", rec.Code, rec.Body.String())
	}
	if !settings.HasKey("zeta") {
		t.Fatal("a refused delete removed the key")
	}
	if rec := doJSON(t, h, http.MethodDelete, "/api/providers/alpha", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, h, http.MethodGet, "/api/providers/alpha", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("after the delete: %d", rec.Code)
	}
	if settings.HasKey("alpha") || settings.Acknowledged("alpha", "https://alpha.example/v1") {
		t.Fatal("its key or its acknowledgement is left")
	}
	var n int64
	s.store.DB.Table("providers").Where("id = ?", "alpha").Count(&n)
	if n != 0 {
		t.Fatal("its row is left")
	}
	if !settings.HasKey("zeta") || settings.RoutedProvider("assistant") != "zeta" {
		t.Fatal("the other provider was touched")
	}
	if rec := doJSON(t, h, http.MethodDelete, "/api/providers/alpha", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("twice: %d", rec.Code)
	}
}

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// fakeCtxpress is a ctxpress with a JWT-like check: Authorization must be "Bearer good" (compress scope) or "Bearer noscope" (403 on /v1/compress).
func fakeCtxpress(t *testing.T, engine string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer good", "Bearer noscope":
		default:
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/v1/compress" {
			var in struct {
				Context map[string]string `json:"context"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			if tool := in.Context["tool"]; tool != "" && tool != "compress_text" && tool != "compress_file" && tool != "cli" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/v1/compress":
			if r.Header.Get("Authorization") == "Bearer noscope" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte(`{"text":"shrunk","engine_used":"` + engine + `","ratio":0.4}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func ctxpressDo(t *testing.T, s *Server, method, path string, body any) (int, ctxpressView, string) {
	t.Helper()
	rec := doJSON(t, s.Handler(), method, path, body)
	var v ctxpressView
	if rec.Code == http.StatusOK {
		_ = json.Unmarshal(rec.Body.Bytes(), &v)
	}
	return rec.Code, v, rec.Body.String()
}

func TestCtxpressSettings_DefaultThenSaveTokenNeverReturnedThenReset(t *testing.T) {
	dir := isolateSettings(t)
	t.Setenv("CTXPRESS_URL", "")
	t.Setenv("CTXPRESS_TOKEN", "")
	t.Setenv("CTXPRESS_MIN_CHARS", "")
	s, _ := newProjectsServer(t)
	code, v, body := ctxpressDo(t, s, http.MethodGet, "/api/settings/ctxpress", nil)
	if code != 200 || v.Enabled || v.Source != "none" || v.MinChars != 2000 || v.TokenSet || v.URL != "" {
		t.Fatalf("default: %d %s", code, body)
	}
	code, v, body = ctxpressDo(t, s, http.MethodPut, "/api/settings/ctxpress", map[string]any{"enabled": true, "url": "http://127.0.0.1:8080/", "min_chars": 3000, "token": "super-secret-token"})
	if code != 200 || !v.Enabled || v.URL != "http://127.0.0.1:8080" || v.MinChars != 3000 || !v.TokenSet || v.Source != "setting" || v.Remote {
		t.Fatalf("save: %d %s", code, body)
	}
	if strings.Contains(body, "super-secret-token") {
		t.Fatal("the token is in the answer")
	}
	_, _, got := ctxpressDo(t, s, http.MethodGet, "/api/settings/ctxpress", nil)
	if strings.Contains(got, "super-secret-token") {
		t.Fatal("the token is in GET")
	}
	// settings.json has no token; the key store does
	if raw, _ := os.ReadFile(dir + "/veritmo/settings.json"); strings.Contains(string(raw), "super-secret-token") {
		t.Fatal("the token is in settings.json")
	}
	if raw, _ := os.ReadFile(dir + "/veritmo/keys.json"); !strings.Contains(string(raw), "super-secret-token") {
		t.Fatal("the token is not in the key store")
	}
	// the generic settings answer and the environment report do not carry it either
	if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/settings", nil); strings.Contains(rec.Body.String(), "super-secret-token") {
		t.Fatal("the token is in GET /api/settings")
	}
	// off, token kept
	code, v, _ = ctxpressDo(t, s, http.MethodPut, "/api/settings/ctxpress", map[string]any{"enabled": false})
	if code != 200 || v.Enabled || !v.TokenSet || v.URL != "http://127.0.0.1:8080" {
		t.Fatalf("off: %d %+v", code, v)
	}
	// reset
	code, v, _ = ctxpressDo(t, s, http.MethodPut, "/api/settings/ctxpress", map[string]any{"reset": true})
	if code != 200 || v.Source != "none" || v.TokenSet || v.URL != "" {
		t.Fatalf("reset: %d %+v", code, v)
	}
}

func TestCtxpressSettings_TheEnvironmentIsTheDefaultAndABadRequestChangesNothing(t *testing.T) {
	isolateSettings(t)
	t.Setenv("CTXPRESS_URL", "http://127.0.0.1:9090")
	t.Setenv("CTXPRESS_TOKEN", "envtok")
	t.Setenv("CTXPRESS_MIN_CHARS", "")
	s, _ := newProjectsServer(t)
	_, v, _ := ctxpressDo(t, s, http.MethodGet, "/api/settings/ctxpress", nil)
	if !v.Enabled || v.Source != "env" || v.URL != "http://127.0.0.1:9090" || !v.TokenSet || !v.EnvURLSet || !v.EnvTokenSet {
		t.Fatalf("env: %+v", v)
	}
	for _, c := range []struct {
		body map[string]any
		want string
	}{
		{map[string]any{}, "required"},
		{map[string]any{"url": "ftp://x"}, "http"},
		{map[string]any{"min_chars": 5}, "min_chars"},
		{map[string]any{"token": "has space"}, "token"},
		{map[string]any{"reset": true, "url": "http://a"}, "reset"},
	} {
		code, _, body := ctxpressDo(t, s, http.MethodPut, "/api/settings/ctxpress", c.body)
		if code != http.StatusBadRequest || !strings.Contains(body, c.want) {
			t.Errorf("%v: %d %s", c.body, code, body)
		}
	}
	// still the environment's: nothing was saved
	if _, v, _ = ctxpressDo(t, s, http.MethodGet, "/api/settings/ctxpress", nil); v.Source != "env" {
		t.Fatalf("a refused request changed something: %+v", v)
	}
	// it cannot be turned on without an address
	t.Setenv("CTXPRESS_URL", "")
	if code, _, _ := ctxpressDo(t, s, http.MethodPut, "/api/settings/ctxpress", map[string]any{"enabled": true}); code != http.StatusBadRequest {
		t.Fatalf("on without an address: %d", code)
	}
}

func TestCtxpressSettings_RemotePlainHTTPWarns(t *testing.T) {
	isolateSettings(t)
	t.Setenv("CTXPRESS_URL", "")
	s, _ := newProjectsServer(t)
	_, v, _ := ctxpressDo(t, s, http.MethodPut, "/api/settings/ctxpress", map[string]any{"enabled": true, "url": "http://10.20.30.40:8080"})
	if !v.Remote || !strings.Contains(v.Warning, "clear text") {
		t.Fatalf("%+v", v)
	}
	_, v, _ = ctxpressDo(t, s, http.MethodPut, "/api/settings/ctxpress", map[string]any{"url": "https://ctx.example.com"})
	if !v.Remote || v.Warning != "" {
		t.Fatalf("https does not warn: %+v", v)
	}
}

func testBody(t *testing.T, s *Server, body any) ctxpressTestResult {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/settings/ctxpress/test", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var r ctxpressTestResult
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rec.Body.String(), "good") && strings.Contains(rec.Body.String(), "Bearer") {
		t.Fatal("the token is in the answer")
	}
	return r
}

func TestCtxpressTest_StatusesOfTheConnection(t *testing.T) {
	isolateSettings(t)
	t.Setenv("CTXPRESS_URL", "")
	t.Setenv("CTXPRESS_TOKEN", "")
	s, _ := newProjectsServer(t)
	if r := testBody(t, s, nil); r.Status != "not_configured" {
		t.Fatalf("nothing: %+v", r)
	}
	ok := fakeCtxpress(t, "llmlingua2")
	none := fakeCtxpress(t, "none")
	for name, c := range map[string]struct {
		url, token, want string
	}{
		"ok":            {ok.URL, "good", "ok"},
		"invalid token": {ok.URL, "wrong", "invalid_token"},
		"no token":      {ok.URL, "", "invalid_token"},
		"no scope":      {ok.URL, "noscope", "no_scope"},
		"degraded":      {none.URL, "good", "degraded"},
		"unreachable":   {"http://127.0.0.1:1", "good", "unreachable"},
	} {
		r := testBody(t, s, map[string]any{"url": c.url, "token": c.token})
		if r.Status != c.want {
			t.Errorf("%s: %+v", name, r)
		}
		if c.want == "ok" && (r.EngineUsed != "llmlingua2" || r.Ratio != 0.4) {
			t.Errorf("%s: %+v", name, r)
		}
	}
	// the saved connection is what it tests without a body, and nothing was saved by the tests above
	if code, _, _ := ctxpressDo(t, s, http.MethodPut, "/api/settings/ctxpress", map[string]any{"enabled": true, "url": ok.URL, "token": "good"}); code != 200 {
		t.Fatal(code)
	}
	if r := testBody(t, s, nil); r.Status != "ok" || r.URL != ok.URL {
		t.Fatalf("saved: %+v", r)
	}
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/settings/ctxpress/test", map[string]any{"url": "mailto:x"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a bad address is 400: %d", rec.Code)
	}
}

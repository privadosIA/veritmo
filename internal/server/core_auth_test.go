package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testToken = "s3cret-token"

func newAuthServer(t *testing.T, origins ...string) *Server {
	t.Helper()
	s, err := New(Config{
		Addr:        "127.0.0.1:0",
		ProjectsDir: t.TempDir(),
		DBPath:      t.TempDir() + "/orchestrator.db",
		Token:       testToken,
		CORSOrigins: origins,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func do(h http.Handler, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAuth_Token(t *testing.T) {
	h := newAuthServer(t).Handler()
	bearer := func(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }

	if rec := do(h, "GET", "/api/projects", bearer(testToken)); rec.Code != 200 {
		t.Fatalf("valid token: status %d", rec.Code)
	}
	for name, hdr := range map[string]map[string]string{
		"missing": nil,
		"wrong":   bearer("nope"),
		"prefix":  bearer(testToken[:3]),
		"basic":   {"Authorization": "Basic " + testToken},
	} {
		rec := do(h, "GET", "/api/projects", hdr)
		if rec.Code != 401 {
			t.Errorf("%s: status %d, want 401", name, rec.Code)
		}
		if decodeAPIError(t, rec.Body.String()) != codeUnauthorized {
			t.Errorf("%s: wrong error code", name)
		}
	}
	if rec := do(h, "GET", "/api/health", nil); rec.Code != 200 {
		t.Errorf("health must stay open, got %d", rec.Code)
	}
}

func TestAuth_NoTokenPassthrough(t *testing.T) {
	h := newTestServer(t).Handler()
	if rec := do(h, "GET", "/api/projects", nil); rec.Code != 200 {
		t.Fatalf("no token configured: status %d", rec.Code)
	}
}

func TestAuth_EveryRouteRequiresToken(t *testing.T) {
	s := newAuthServer(t)
	h := s.Handler()
	if len(s.patterns) < 15 {
		t.Fatalf("expected the route list to be recorded, got %d", len(s.patterns))
	}
	for i, p := range s.patterns {
		method, path, _ := strings.Cut(p, " ")
		if path == "/api/health" {
			continue // deliberately open
		}
		path = strings.NewReplacer("{id}", "x", "{path...}", "a/b").Replace(path)
		// Each request comes from its own address: the failed-attempts limit is per address and not what is tested here.
		req := httptest.NewRequest(method, path, nil)
		req.RemoteAddr = fmt.Sprintf("10.1.%d.%d:5000", i/250, i%250)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Errorf("%s without credentials: status %d, want 401", p, rec.Code)
		}
	}
	// Unknown API paths must not leak existence either.
	if rec := do(h, "GET", "/api/nope", nil); rec.Code != 401 {
		t.Errorf("unknown path: status %d, want 401", rec.Code)
	}
}

func issue(t *testing.T, s *Server) string {
	t.Helper()
	rec := do(s.Handler(), "POST", "/api/auth/ticket", map[string]string{"Authorization": "Bearer " + testToken})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ticket"`) || !strings.Contains(rec.Body.String(), `"expiresAt"`) {
		t.Fatalf("ticket issue: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	i := strings.Index(body, `"ticket":"`) + len(`"ticket":"`)
	return body[i : i+strings.Index(body[i:], `"`)]
}

func TestAuth_Tickets(t *testing.T) {
	s := newAuthServer(t)
	h := s.Handler()

	if rec := do(h, "POST", "/api/auth/ticket", nil); rec.Code != 401 {
		t.Fatalf("ticket endpoint without token: %d", rec.Code)
	}
	tk := issue(t, s)

	// Allowed routes: past auth (404 project/run not found is fine).
	for _, path := range []string{"/api/runs/1/events?ticket=" + tk, "/api/downloads/1/events?ticket=" + tk, "/api/projects/p/artifacts/out/x.png?ticket=" + tk, "/api/motions/m1/preview?ticket=" + tk,
		"/api/music-motion/references/ref-20260101-000000-abcdef/frames/00.jpg?ticket=" + tk, "/api/music-motion/part-drafts/draft-20260101-000000-abcdef/preview/00.png?ticket=" + tk,
		"/api/models3d/models/kenney-mini-characters/character-female-a/thumbnail?ticket=" + tk,
		"/api/models3d/models/kenney-mini-characters/character-female-a/file?ticket=" + tk} {
		if rec := do(h, "GET", path, nil); rec.Code == 401 {
			t.Errorf("%s: valid ticket rejected", path)
		}
	}
	// Wrong route / wrong method.
	if rec := do(h, "GET", "/api/projects?ticket="+tk, nil); rec.Code != 401 {
		t.Errorf("ticket on list route: %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/projects/p/runs?ticket="+tk, nil); rec.Code != 401 {
		t.Errorf("ticket on mutating route: %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/downloads?ticket="+tk, nil); rec.Code != 401 {
		t.Errorf("ticket on download creation: %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/downloads?ticket="+tk, nil); rec.Code != 401 {
		t.Errorf("ticket on download list: %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/auth/ticket?ticket="+tk, nil); rec.Code != 401 {
		t.Errorf("ticket must not mint tickets: %d", rec.Code)
	}
	// Forged.
	forged := tk[:len(tk)-2] + "xx"
	if rec := do(h, "GET", "/api/runs/1/events?ticket="+forged, nil); rec.Code != 401 {
		t.Errorf("forged ticket: %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/runs/1/events?ticket=garbage", nil); rec.Code != 401 {
		t.Errorf("garbage ticket: %d", rec.Code)
	}
	// Expired.
	s.auth.now = func() time.Time { return time.Now().Add(ticketTTL + time.Second) }
	if rec := do(h, "GET", "/api/runs/1/events?ticket="+tk, nil); rec.Code != 401 {
		t.Errorf("expired ticket: %d", rec.Code)
	}
	// Tickets from another process (different secret) are invalid.
	other := newAuthServer(t)
	if rec := do(h, "GET", "/api/runs/1/events?ticket="+issue(t, other), nil); rec.Code != 401 {
		t.Errorf("foreign ticket: %d", rec.Code)
	}
}

func TestAuth_ArtifactReferrerPolicy(t *testing.T) {
	s := newAuthServer(t)
	rec := do(s.Handler(), "GET", "/api/projects/p/artifacts/a.png", map[string]string{"Authorization": "Bearer " + testToken})
	if rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("Referrer-Policy = %q", rec.Header().Get("Referrer-Policy"))
	}
}

func TestCORS(t *testing.T) {
	s := newAuthServer(t, "http://ui.example:4200")
	h := s.Handler()
	pre := map[string]string{
		"Origin":                         "http://ui.example:4200",
		"Access-Control-Request-Method":  "PUT",
		"Access-Control-Request-Headers": "authorization",
	}
	rec := do(h, "OPTIONS", "/api/projects/p/config", pre)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight (no token): status %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "http://ui.example:4200" ||
		!strings.Contains(rec.Header().Get("Access-Control-Allow-Methods"), "PUT") || !strings.Contains(rec.Header().Get("Access-Control-Allow-Methods"), "PATCH") ||
		!strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Fatalf("preflight headers: %v", rec.Header())
	}
	if rec.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("credentials must not be allowed")
	}

	rec = do(h, "GET", "/api/health", map[string]string{"Origin": "http://ui.example:4200"})
	if rec.Header().Get("Access-Control-Allow-Origin") != "http://ui.example:4200" || rec.Header().Get("Vary") != "Origin" {
		t.Fatalf("simple request headers: %v", rec.Header())
	}
	// A page can read the headers of a paged list (spec api-listing).
	for _, name := range []string{"X-Total-Count", "Link"} {
		if !strings.Contains(rec.Header().Get("Access-Control-Expose-Headers"), name) {
			t.Fatalf("%s must be exposed to the browser: %q", name, rec.Header().Get("Access-Control-Expose-Headers"))
		}
	}
	// 401 responses still carry CORS headers so the browser can read them.
	rec = do(h, "GET", "/api/projects", map[string]string{"Origin": "http://ui.example:4200"})
	if rec.Code != 401 || rec.Header().Get("Access-Control-Allow-Origin") == "" {
		t.Fatalf("401 must carry CORS: %d %v", rec.Code, rec.Header())
	}

	rec = do(h, "GET", "/api/health", map[string]string{"Origin": "http://evil.example"})
	for k := range rec.Header() {
		if strings.HasPrefix(k, "Access-Control-") {
			t.Fatalf("unlisted origin got %s", k)
		}
	}
	rec = do(newTestServer(t).Handler(), "GET", "/api/health", map[string]string{"Origin": "http://ui.example:4200"})
	for k := range rec.Header() {
		if strings.HasPrefix(k, "Access-Control-") {
			t.Fatalf("default config got %s", k)
		}
	}
}

func fromAddr(h http.Handler, addr, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/api/projects", nil)
	req.RemoteAddr = addr
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAuth_GuessingTheTokenIsLimitedPerAddress(t *testing.T) {
	s := newAuthServer(t)
	now := time.Now()
	s.auth.now = func() time.Time { return now }
	h := s.Handler()

	for i := 0; i < maxFailures; i++ {
		if rec := fromAddr(h, "203.0.113.9:1000", "wrong"); rec.Code != 401 {
			t.Fatalf("failure %d: status %d, want 401", i+1, rec.Code)
		}
	}
	rec := fromAddr(h, "203.0.113.9:1001", "wrong")
	if rec.Code != 429 || !strings.Contains(rec.Body.String(), "too_many_attempts") || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("over the limit: status %d (%s), want 429 too_many_attempts with Retry-After", rec.Code, rec.Body.String())
	}
	// A valid token from the same address, and any request from another address, are not affected.
	if rec := fromAddr(h, "203.0.113.9:1002", testToken); rec.Code != 200 {
		t.Errorf("the right token from the limited address: status %d, want 200", rec.Code)
	}
	if rec := fromAddr(h, "198.51.100.7:1000", "wrong"); rec.Code != 401 {
		t.Errorf("another address: status %d, want 401", rec.Code)
	}
	// The window passes.
	now = now.Add(failureSpan + time.Second)
	if rec := fromAddr(h, "203.0.113.9:1003", "wrong"); rec.Code != 401 {
		t.Errorf("after the window: status %d, want 401", rec.Code)
	}
}

func TestAuth_WithoutATokenNothingIsCounted(t *testing.T) {
	s, err := New(Config{Addr: "127.0.0.1:0", ProjectsDir: t.TempDir(), DBPath: t.TempDir() + "/o.db"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxFailures*3; i++ {
		if rec := fromAddr(s.Handler(), "203.0.113.9:1000", "anything"); rec.Code == 429 {
			t.Fatal("a server without a token must not limit anyone")
		}
	}
}

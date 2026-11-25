package server

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"veritmo/internal/platform/settings"
	"veritmo/internal/platform/settingsdb"
)

// corsServer is a server with an isolated settings file (never the developer's own), whose log lines are collected.
func corsServer(t *testing.T, flagOrigins ...string) (*Server, *[]string, func(string)) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := newAuthServer(t, flagOrigins...)
	var mu sync.Mutex
	var logs []string
	s.cors.logf = func(f string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, strings.TrimSpace(sprintf(f, a...)))
	}
	write := func(body string) {
		p := settings.Path()
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		// the file is followed by modification time: make sure a quick rewrite looks different
		future := time.Now().Add(time.Duration(len(body)+1) * time.Second)
		_ = os.Chtimes(p, future, future)
	}
	return s, &logs, write
}

func sprintf(f string, a ...any) string {
	return strings.TrimSpace(strings.ReplaceAll(f, "\n", " ")) + " " + joinAny(a)
}

func joinAny(a []any) string {
	var b strings.Builder
	for _, x := range a {
		b.WriteString(" ")
		if e, ok := x.(error); ok {
			b.WriteString(e.Error())
		} else if s, ok := x.(string); ok {
			b.WriteString(s)
		}
	}
	return b.String()
}

func allowedBy(s *Server, origin string) bool {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/health", nil)
	req.Header.Set("Origin", origin)
	s.Handler().ServeHTTP(rec, req)
	return rec.Header().Get("Access-Control-Allow-Origin") == origin
}

func TestCORSSettings_ExactOriginAndRange(t *testing.T) {
	s, _, write := corsServer(t)
	write(`{"cors_origins":["http://192.168.1.50:4200/","http://dashboard.example.lan:4200"],"cors_networks":["192.168.1.0/24","100.64.0.0/10","fd7a:115c:a1e0::/48"]}`)
	for origin, want := range map[string]bool{
		"http://192.168.1.50:4200":          true,  // exact, trailing slash in the file ignored
		"http://192.168.1.77:4300":          true,  // range, other port
		"https://192.168.1.9":               true,  // range, other scheme
		"http://192.168.2.5:4300":           false, // outside the range
		"http://dashboard.example.lan:4200": true,  // a name: exact only
		"http://dashboard.example.lan:4300": false, // a name is never matched by a range
		"http://100.90.129.105:4200":        true,  // tailnet
		"http://[fd7a:115c:a1e0::1]:80":     true,  // IPv6 range
		"http://evil.example":               false,
		"null":                              false,
	} {
		if got := allowedBy(s, origin); got != want {
			t.Errorf("%s: allowed = %v, want %v", origin, got, want)
		}
	}
}

func TestCORSSettings_FlagStillWorksAndAdds(t *testing.T) {
	s, _, write := corsServer(t, "http://flag.example:1")
	write(`{"cors_origins":["http://file.example:2"]}`)
	if !allowedBy(s, "http://flag.example:1") || !allowedBy(s, "http://file.example:2") {
		t.Fatal("the flag and the file must both apply")
	}
	if allowedBy(s, "http://other.example:3") {
		t.Fatal("an unlisted origin was allowed")
	}
}

func TestCORSSettings_NothingConfiguredIsSameOriginOnly(t *testing.T) {
	s, _, _ := corsServer(t)
	if allowedBy(s, "http://192.168.1.50:4200") {
		t.Fatal("allowed with nothing configured")
	}
}

func TestCORSSettings_BadEntriesAreIgnoredWithAWarning(t *testing.T) {
	s, logs, write := corsServer(t)
	write(`{"cors_origins":["ftp://x","http://ok.example:1"],"cors_networks":["not-a-range","10.0.0.0/8"]}`)
	if !allowedBy(s, "http://ok.example:1") || !allowedBy(s, "http://10.1.2.3:5") {
		t.Fatal("the valid entries must still apply")
	}
	joined := strings.Join(*logs, "\n")
	if !strings.Contains(joined, "ftp://x") || !strings.Contains(joined, "not-a-range") {
		t.Fatalf("each bad entry is named in a warning: %q", joined)
	}
}

func TestCORSSettings_FollowsTheFileLive(t *testing.T) {
	s, _, write := corsServer(t)
	if allowedBy(s, "http://late.example:1") {
		t.Fatal("allowed before it was added")
	}
	write(`{"cors_origins":["http://late.example:1"]}`)
	if !allowedBy(s, "http://late.example:1") {
		t.Fatal("not allowed after the file changed (no restart)")
	}
	write(`{}`)
	if allowedBy(s, "http://late.example:1") {
		t.Fatal("still allowed after it was removed")
	}
}

func TestCORSSettings_ABrokenFileKeepsTheLastGoodLists(t *testing.T) {
	s, logs, write := corsServer(t)
	write(`{"cors_origins":["http://keep.example:1"]}`)
	if !allowedBy(s, "http://keep.example:1") {
		t.Fatal("setup")
	}
	write(`{"cors_origins": [`) // a typo while editing
	if !allowedBy(s, "http://keep.example:1") {
		t.Fatal("a broken file dropped the lists it had")
	}
	if allowedBy(s, "http://new.example:1") {
		t.Fatal("a broken file opened access")
	}
	if !strings.Contains(strings.Join(*logs, "\n"), "keeping the CORS lists") {
		t.Fatalf("the broken file must be reported: %v", *logs)
	}
}

func TestCORSSettings_ARejectedOriginIsLoggedOnce(t *testing.T) {
	s, logs, write := corsServer(t)
	for i := 0; i < 3; i++ {
		allowedBy(s, "http://192.168.1.50:4200")
	}
	count := func() int {
		n := 0
		for _, l := range *logs {
			if strings.Contains(l, "http://192.168.1.50:4200") && strings.Contains(l, "veritmo cors add") {
				n++
			}
		}
		return n
	}
	if count() != 1 {
		t.Fatalf("lines = %d, want 1: %v", count(), *logs)
	}
	// the server's own origin (a POST from a page it serves) is not reported
	req := httptest.NewRequest("POST", "http://myhost:4666/api/health", nil)
	req.Header.Set("Origin", "http://myhost:4666")
	s.Handler().ServeHTTP(httptest.NewRecorder(), req)
	for _, l := range *logs {
		if strings.Contains(l, "myhost") {
			t.Fatalf("own origin logged: %s", l)
		}
	}
	// once allowed, it is served and nothing new is written
	write(`{"cors_origins":["http://192.168.1.50:4200"]}`)
	before := len(*logs)
	if !allowedBy(s, "http://192.168.1.50:4200") || len(*logs) != before {
		t.Fatal("an allowed origin must be served silently")
	}
}

func TestCORSSettings_ThePutOfOtherSettingsKeepsTheLists(t *testing.T) {
	s, _, write := corsServer(t)
	write(`{"cors_origins":["http://keep.example:1"],"cors_networks":["10.0.0.0/8"]}`)
	h := s.Handler()
	// the API neither accepts nor changes them
	req := httptest.NewRequest("PUT", "/api/settings", strings.NewReader(`{"cors_origins":["http://evil.example"]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("PUT with only cors_origins: %d %s", rec.Code, rec.Body)
	}
	// and saving its own keys leaves them as they were
	req = httptest.NewRequest("PUT", "/api/settings", strings.NewReader(`{"ollama_host":"http://127.0.0.1:11434","cors_origins":["http://evil.example"]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code >= 500 {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	o, n := settings.CORS()
	if len(o) != 1 || o[0] != "http://keep.example:1" || len(n) != 1 || n[0] != "10.0.0.0/8" {
		t.Fatalf("the lists changed: %v %v", o, n)
	}
	for _, path := range []string{"/api/settings", "/api/environment"} {
		req = httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+testToken)
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if strings.Contains(rec.Body.String(), "cors_") || strings.Contains(rec.Body.String(), "keep.example") {
			t.Fatalf("%s exposes the CORS lists: %s", path, rec.Body)
		}
	}
}

// With the settings in the database a change made by another process (the command `veritmo cors`) reaches the policy within 5 seconds.
func TestCORS_TheSettingsOfTheDatabaseAreReadAgainEvery5Seconds(t *testing.T) {
	s, _, _ := corsServer(t)
	src := settingsdb.New(s.store)
	settings.UseSource(src)
	t.Cleanup(func() { settings.UseSource(nil) })
	clock := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	s.cors.now = func() time.Time { return clock }

	if allowedBy(s, "http://192.168.1.50:4200") {
		t.Fatal("allowed before anything was saved")
	}
	f, _ := src.Load()
	f.CORSOrigins = []string{"http://192.168.1.50:4200"}
	if err := src.Save(f); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(4 * time.Second)
	if allowedBy(s, "http://192.168.1.50:4200") {
		t.Fatal("the policy followed the change before 5 seconds")
	}
	clock = clock.Add(time.Second)
	if !allowedBy(s, "http://192.168.1.50:4200") {
		t.Fatal("the policy did not follow the change after 5 seconds")
	}
	// removed again: it is closed again after the next refresh, and a database that is down leaves the lists in force
	f.CORSOrigins = nil
	_ = src.Save(f)
	clock = clock.Add(5 * time.Second)
	if allowedBy(s, "http://192.168.1.50:4200") {
		t.Fatal("a removed origin is still allowed")
	}
}

// The stack gives the origin of its dashboard by environment: it is allowed, another origin is not, and the settings file is not written.
func TestCORSSettings_AnOriginOfTheEnvironmentIsAllowedAndAnotherIsRefused(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "http://dashboard.example:4200")
	t.Setenv("CORS_NETWORKS", "")
	s, _, _ := corsServer(t)
	if !allowedBy(s, "http://dashboard.example:4200") {
		t.Fatal("the origin of the environment is refused")
	}
	if allowedBy(s, "http://evil.example") {
		t.Fatal("another origin is allowed")
	}
	if _, err := os.Stat(settings.Path()); err == nil {
		t.Fatal("the settings file was written for an environment origin")
	}
	// the variable empty: nothing besides the server's own
	t.Setenv("CORS_ORIGINS", "")
	s2, _, _ := corsServer(t)
	if allowedBy(s2, "http://dashboard.example:4200") {
		t.Fatal("no variable, no origin")
	}
}

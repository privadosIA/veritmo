package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestRunReferencesAddsEveryAddressAndListsThem(t *testing.T) {
	var mu sync.Mutex
	var posted []map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		auth = r.Header.Get("Authorization")
		switch r.Method {
		case http.MethodPost:
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			if strings.Contains(b["url"].(string), "bad") {
				w.WriteHeader(422)
				w.Write([]byte(`{"error":{"code":"validation","message":"use the https address of one YouTube video"}}`))
				return
			}
			posted = append(posted, b)
			w.WriteHeader(202)
			w.Write([]byte(`{"id":"ref-20260101-000000-abcdef","title":"T","state":"analysing","tags":[],"created_at":"2026-01-01T00:00:00Z"}`))
		default:
			w.Write([]byte(`{"references":[{"id":"ref-20260101-000000-abcdef","title":"T","state":"ready","tags":[],"created_at":"2026-01-01T00:00:00Z"}]}`))
		}
	}))
	defer srv.Close()
	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := runReferences(args, &out, &errb)
		return code, out.String(), errb.String()
	}
	code, out, errs := run("add", "--server", srv.URL, "--token", "secret", "--tag", "pop", "https://youtu.be/AAAAAAAAAAA", "https://youtu.be/BBBBBBBBBBB")
	if code != 0 || strings.Count(out, "added as") != 2 || errs != "" {
		t.Fatalf("%d %q %q", code, out, errs)
	}
	if len(posted) != 2 || auth != "Bearer secret" || posted[0]["url"] != "https://youtu.be/AAAAAAAAAAA" || posted[1]["tags"].([]any)[0] != "pop" {
		t.Fatalf("%v %q", posted, auth)
	}
	// One refused address does not stop the others, and the exit code says it failed.
	code, out, errs = run("add", "--server", srv.URL, "https://example.com/bad", "https://youtu.be/CCCCCCCCCCC")
	if code != 1 || !strings.Contains(errs, "not added (422: use the https address") || strings.Count(out, "added as") != 1 {
		t.Fatalf("%d %q %q", code, out, errs)
	}
	if code, out, _ := run("list", "--server", srv.URL); code != 0 || !strings.Contains(out, "ready") || !strings.Contains(out, "1 reference(s)") {
		t.Fatalf("%d %q", code, out)
	}
	if code, _, _ := run(); code != 2 {
		t.Fatalf("no args: %d", code)
	}
	if code, _, _ := run("add", "--server", srv.URL); code != 2 {
		t.Fatalf("no addresses: %d", code)
	}
	if code, _, errs := run("list", "--server", "http://127.0.0.1:1"); code != 1 || !strings.Contains(errs, "cannot list") {
		t.Fatalf("no server: %d %q", code, errs)
	}
}

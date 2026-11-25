// Package servertest holds the helpers of the tests of internal/server and of its sub-packages: calling a handler,
// reading a server-sent stream, waiting for a condition. It is only imported by tests.
package servertest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"veritmo/internal/server/api"
)

// DoJSON sends a request with body (nil for none) encoded as JSON and returns the recorded answer.
func DoJSON(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = httptest.NewRequest(method, path, bytes.NewReader(b))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// Call sends a request with a raw body and returns the recorded answer.
func Call(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// WaitUntil polls cond every 10 ms for three seconds and fails the test if it never holds.
func WaitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the condition never came true")
}

// IsolateSettings points the settings at an empty directory, so a test never reads or writes the user's.
func IsolateSettings(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("OLLAMA_HOST", "")
	return dir
}

// WriteArtifact writes a file inside a project of the projects directory and returns its full path.
func WriteArtifact(t *testing.T, projects, project, rel string, data []byte) string {
	t.Helper()
	full := filepath.Join(projects, project, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return full
}

// DecodeAPIError asserts the structured JSON error shape and returns its code.
func DecodeAPIError(t *testing.T, body string) string {
	t.Helper()
	var e api.APIError
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		t.Fatalf("error body is not the JSON error shape: %v\nbody: %s", err, body)
	}
	if e.Error.Code == "" || e.Error.Message == "" {
		t.Fatalf("error body missing code or message: %s", body)
	}
	return e.Error.Code
}

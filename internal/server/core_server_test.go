package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"veritmo/internal/server/api"
	"veritmo/internal/server/workspace"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(Config{
		Addr:        "127.0.0.1:0",
		ProjectsDir: t.TempDir(),
		DBPath:      t.TempDir() + "/orchestrator.db",
		Sampler:     workspace.NoHardware(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return quiet(s)
}

// decodeAPIError asserts the structured JSON error shape and returns its code.
func decodeAPIError(t *testing.T, body string) string {
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

func TestRouting_ErrorShapes(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()

	cases := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantCode   string
	}{
		{"health ok", http.MethodGet, "/api/health", http.StatusOK, ""},
		{"unknown api path is JSON 404", http.MethodGet, "/api/definitely-not-here", http.StatusNotFound, api.CodeNotFound},
		{"wrong method is JSON 405", http.MethodPost, "/api/health", http.StatusMethodNotAllowed, api.CodeMethodNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantCode == "" {
				return
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			if code := decodeAPIError(t, rec.Body.String()); code != tc.wantCode {
				t.Errorf("error code = %q, want %q", code, tc.wantCode)
			}
		})
	}
}

func TestRoot_WithoutWebUITag_ExplainsMissingUI(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no front end") {
		t.Errorf("root response should say the server has no front end, got: %s", rec.Body.String())
	}
}

func TestRun_BindConflict_ReturnsErrorNamingAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	s, err := New(Config{Addr: addr, ProjectsDir: t.TempDir(), DBPath: t.TempDir() + "/o.db"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	runErr := s.Run(ctx)
	if runErr == nil {
		t.Fatal("expected bind error, got nil")
	}
	if !strings.Contains(runErr.Error(), addr) {
		t.Errorf("bind error should name the address %q, got: %v", addr, runErr)
	}
}

func TestRun_ContextCancel_ShutsDownCleanly(t *testing.T) {
	s := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error on graceful shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

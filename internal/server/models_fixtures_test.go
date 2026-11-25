package server

// models_fixtures_test.go: the servers and helpers the HTTP-level tests of the whole server use to drive downloads and
// to read the catalog. The tests of the models family itself (internal/server/models) have their own over the family
// alone.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"veritmo/internal/server/models"
	"veritmo/internal/server/servertest"
)

type Download = models.Download

const (
	dlSucceeded = models.StateSucceeded
	dlFailed    = models.StateFailed
)

var (
	call = servertest.Call
)

// newAssetServer returns a server whose assets root is a fresh temp dir.
func newAssetServer(t *testing.T) *Server {
	t.Helper()
	s := newTestServer(t)
	s.assetsRoot = t.TempDir()
	return s
}

// newDLServer is newAssetServer with downloads that may reach local servers.
func newDLServer(t *testing.T) *Server {
	t.Helper()
	s := newAssetServer(t)
	s.models.AllowPrivateAddresses()
	t.Cleanup(s.models.Shutdown)
	return s
}

func postDownload(t *testing.T, s *Server, body string) (*httptest.ResponseRecorder, Download) {
	t.Helper()
	rec := call(s.Handler(), "POST", "/api/downloads", body)
	var out struct{ Download Download }
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out.Download
}

func waitDL(t *testing.T, s *Server, id string, states ...string) Download {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rec, ok := s.models.Download(id)
		if !ok {
			t.Fatalf("download %s vanished", id)
		}
		for _, st := range states {
			if rec.State == st {
				return rec
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	rec, _ := s.models.Download(id)
	t.Fatalf("download %s stuck in %q, wanted %v", id, rec.State, states)
	return Download{}
}

func startDL(t *testing.T, s *Server, body string) Download {
	t.Helper()
	_, d := postDownload(t, s, body)
	return waitDL(t, s, d.ID, dlSucceeded, dlFailed)
}

// routeAll makes every download connection reach srv, whatever host the URL names.
func routeAll(t *testing.T, s *Server, srv *httptest.Server) {
	t.Helper()
	s.models.RouteAllTo(srv.Listener.Addr().String())
}

// authLog records the Authorization header each Host received.
type authLog struct {
	mu   sync.Mutex
	seen map[string][]string
}

func (a *authLog) add(host, auth string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.seen == nil {
		a.seen = map[string][]string{}
	}
	a.seen[host] = append(a.seen[host], auth)
}

func (a *authLog) get(host string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.seen[host]...)
}

func fileServer(t *testing.T, payload string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/missing.safetensors"):
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, "/short.safetensors"):
			w.Header().Set("Content-Length", "100")
			_, _ = w.Write([]byte("only ten b"))
		default:
			_, _ = w.Write([]byte(payload))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

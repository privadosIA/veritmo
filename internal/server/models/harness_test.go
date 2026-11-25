package models

// harness_test.go: the family on its own, with a host that replaces what the server gives it, for the tests of the
// package. Tests that need the whole server (the content mode over the generation API, the environment report, the
// vision service with its containers) are HTTP-level tests of package server.

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/platform/runner"
	"veritmo/internal/server/modelcat"
	"veritmo/internal/server/servertest"
)

// The names the tests use for the entries of the catalog.
type (
	catalogEntry = modelcat.Entry
	catalogFile  = modelcat.File
)

var decodeAPIError = servertest.DecodeAPIError

// catalog is the embedded catalog of the server, read from its files.
var catalog = loadCatalog()

func loadCatalog() []catalogEntry {
	var all []catalogEntry
	for _, name := range catalogFiles() {
		raw, err := os.ReadFile(name)
		if err != nil {
			panic(err)
		}
		var c []catalogEntry
		if err := json.Unmarshal(raw, &c); err != nil {
			panic(name + ": " + err.Error())
		}
		all = append(all, c...)
	}
	return all
}

func findCatalog(id string) (catalogEntry, bool) {
	for _, e := range catalog {
		if e.ID == id {
			return e, true
		}
	}
	return catalogEntry{}, false
}

// mHandler lets testServer embed the handler under a name that does not clash with its Handler method.
type mHandler = Handler

// testServer is the family with its mux and a fake host. Its fields are the seams the tests replace.
type testServer struct {
	*mHandler
	mux *http.ServeMux

	assetsRoot  string
	look        func(string) (string, error)
	mediaRunner runner.CommandRunner
	adult       bool // the server shows adult models too (the default)
	busyRun     string
	issue       error
	hfKey       string
	services    *musicsvc.ServiceHost
}

type testHost struct{ s *testServer }

func (h testHost) AssetsRoot() string                { return h.s.assetsRoot }
func (h testHost) LookPath(n string) (string, error) { return h.s.look(n) }
func (h testHost) Runner() runner.CommandRunner      { return h.s.mediaRunner }
func (h testHost) AnyBusy() (string, bool)           { return h.s.busyRun, h.s.busyRun != "" }
func (h testHost) ModelsIssue() error                { return h.s.issue }
func (h testHost) ShowsAdult() bool                  { return h.s.adult }
func (h testHost) Catalog() []modelcat.Entry         { return catalog }
func (h testHost) HuggingFaceKey() string            { return h.s.hfKey }
func (h testHost) DiffRhythm2CLI() (string, string) {
	if h.s.services == nil {
		return "", "the .NET services are not set up (bash scripts/get-services.sh builds them)"
	}
	exe := filepath.Join(h.s.services.Dir, "diffrhythm2", "diffrhythm2")
	if _, err := os.Stat(exe); err != nil {
		return "", "the diffrhythm2 executable is not built (bash scripts/get-services.sh --only diffrhythm2)"
	}
	return exe, ""
}
func (h testHost) VisionEngine() *musicsvc.Engine { return nil }
func (h testHost) OverlayVision(*musicsvc.Availability) {
}

// newTestServer is the family over a temp assets root.
func newTestServer(t *testing.T) *testServer {
	t.Helper()
	s := &testServer{mux: http.NewServeMux(), assetsRoot: t.TempDir(), look: exec.LookPath, mediaRunner: runner.NewFakeRunner(), adult: true}
	s.mHandler = New(testHost{s})
	s.Register(func(pattern string, h http.HandlerFunc) { s.mux.HandleFunc(pattern, h) })
	t.Cleanup(s.Shutdown)
	return s
}

// Handler is the handler of the endpoints of the family.
func (s *testServer) Handler() http.Handler { return s.mux }

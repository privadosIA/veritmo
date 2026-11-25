package workspace

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/server/api"
)

func writeArtifact(t *testing.T, projects, project, rel string, data []byte) string {
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

func TestArtifacts_RangeRequestOnMp4(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "demo")
	writeArtifact(t, projects, "demo", "tmp/scene_intro.mp4", []byte("0123456789"))

	req := httptest.NewRequest(http.MethodGet, "/api/projects/demo/artifacts/tmp/scene_intro.mp4", nil)
	req.Header.Set("Range", "bytes=2-5")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if got := rec.Body.String(); got != "2345" {
		t.Errorf("range body = %q, want 2345", got)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "video/mp4") {
		t.Errorf("Content-Type = %q, want video/mp4", ct)
	}
	if rec.Header().Get("Content-Range") == "" {
		t.Error("missing Content-Range header")
	}
}

func TestArtifacts_ManifestJSONRoundTrip(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "demo")
	manifest := []byte(`{"project":"demo","mode":"images","images":[]}`)
	writeArtifact(t, projects, "demo", "out/demo/manifest.json", manifest)

	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/projects/demo/artifacts/out/demo/manifest.json", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != string(manifest) {
		t.Errorf("manifest altered in transit: %s", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "json") {
		t.Errorf("Content-Type = %q, want json", ct)
	}
}

func TestArtifacts_TraversalAndSymlinkEscapeRejected(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "demo")

	// Secret outside the project.
	secret := filepath.Join(projects, "..", "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Symlink inside tmp/ pointing outside the project.
	if err := os.MkdirAll(filepath.Join(projects, "demo", "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(projects, "demo", "tmp", "evil.wav")); err != nil {
		t.Fatal(err)
	}

	cases := []string{
		"/api/projects/demo/artifacts/tmp/evil.wav",         // symlink escape
		"/api/projects/demo/artifacts/models/m.safetensors", // via shared-asset symlink dir
	}
	for _, path := range cases {
		rec := doJSON(t, s.Handler(), http.MethodGet, path, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400 (%s)", path, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), projects) {
			t.Errorf("%s: response leaks filesystem details: %s", path, rec.Body.String())
		}
	}

	// Lexical traversal at the handler level (the mux already cleans
	// URL paths; exercise the handler's own defense directly).
	if _, ok := s.ConfinedArtifactPath("demo", "../demo2/x"); ok {
		t.Error("lexical traversal accepted")
	}
	if _, ok := s.ConfinedArtifactPath("demo", "/etc/passwd"); ok {
		t.Error("absolute path accepted")
	}
}

func TestArtifacts_MissingIsDistinctNotFound(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "demo")

	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/projects/demo/artifacts/tmp/scene_intro.wav", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if code := decodeAPIError(t, rec.Body.String()); code != api.CodeNotFound {
		t.Errorf("code = %s, want not_found", code)
	}
	if !strings.Contains(rec.Body.String(), "scene_intro.wav") {
		t.Errorf("not-found should name the artifact: %s", rec.Body.String())
	}
}

func TestArtifacts_NoWriteEndpoints(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "demo")
	writeArtifact(t, projects, "demo", "tmp/x.png", []byte("png"))

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := doJSON(t, s.Handler(), method, "/api/projects/demo/artifacts/tmp/x.png", nil)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s on artifact: status %d, want 405", method, rec.Code)
		}
	}
}

// Artifacts are rewritten in place by a later run, so the browser must
// revalidate them instead of reusing its heuristic cache of the previous one.
func TestArtifacts_AreRevalidatedByTheBrowser(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "demo")
	writeArtifact(t, projects, "demo", "out/demo/manifest.json", []byte(`{}`))
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/projects/demo/artifacts/out/demo/manifest.json", nil)
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", got)
	}
	if rec.Header().Get("Last-Modified") == "" {
		t.Fatal("it must stay revalidatable: Last-Modified is missing")
	}
}

func TestArtifacts_ABuiltScenesBlendCanBeDownloaded(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "demo")
	blend := []byte("BLENDER-v503\x00\x01binary")
	writeArtifact(t, projects, "demo", "tmp/scene_titulo.blend", blend)

	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/projects/demo/artifacts/tmp/scene_titulo.blend", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != string(blend) {
		t.Errorf("the .blend came back changed: %q", rec.Body.String())
	}
}

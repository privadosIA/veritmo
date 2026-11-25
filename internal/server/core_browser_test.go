package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeChrome(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "my-chrome")
	if err := os.WriteFile(p, []byte("#!/bin/sh\necho 'Chromium 127.0.1.2'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEnvironment_ReportsTheBrowserOptionalWithItsHint(t *testing.T) {
	isolateSettings(t)
	t.Setenv("PATH", t.TempDir())
	s, _ := newProjectsServer(t)
	b := binByName(t, getEnvironment(t, s, ""), "chrome")
	if b.Required || b.Present || !strings.Contains(b.Fix, "Settings") {
		t.Fatalf("a missing browser is optional and names how to fix it: %+v", b)
	}
	p := fakeChrome(t)
	if rec := doJSON(t, s.Handler(), http.MethodPut, "/api/settings", map[string]any{"browser": p}); rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	b = binByName(t, getEnvironment(t, s, ""), "chrome")
	if !b.Present || b.Path != p || b.Version != "127.0.1.2" {
		t.Fatalf("the saved browser must be reported with its version: %+v", b)
	}
}

func TestSettings_BrowserPathValidatedAndRemovable(t *testing.T) {
	isolateSettings(t)
	t.Setenv("PATH", t.TempDir())
	s, _ := newProjectsServer(t)
	if rec := doJSON(t, s.Handler(), http.MethodPut, "/api/settings", map[string]any{"browser": "/no/such/chrome"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("an invalid path is a 400, got %d", rec.Code)
	}
	p := fakeChrome(t)
	rec := doJSON(t, s.Handler(), http.MethodPut, "/api/settings", map[string]any{"browser": p})
	var a struct {
		Browser browserSettingsView `json:"browser"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil || !a.Browser.Found || a.Browser.Saved != p {
		t.Fatalf("saved browser: %+v %v", a.Browser, err)
	}
	rec = doJSON(t, s.Handler(), http.MethodPut, "/api/settings", map[string]any{"browser": ""})
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil || a.Browser.Saved != "" || a.Browser.Found {
		t.Fatalf("removed browser: %+v %v", a.Browser, err)
	}
}

func TestSettings_MotionModelSavedValidatedAndShownInTheTasks(t *testing.T) {
	isolateSettings(t)
	t.Setenv("VERITMO_ASSIST_MODEL", "qwen3:8b")
	t.Setenv("OLLAMA_HOST", "http://127.0.0.1:1") // no Ollama to ask: the machine's own models must not change the answer
	s, _ := newProjectsServer(t)
	read := func() (int, struct {
		MotionModel motionModelView `json:"motion_model"`
	}) {
		rec := doJSON(t, s.Handler(), http.MethodGet, "/api/settings", nil)
		var a struct {
			MotionModel motionModelView `json:"motion_model"`
		}
		json.Unmarshal(rec.Body.Bytes(), &a)
		return rec.Code, a
	}
	if _, a := read(); a.MotionModel.Saved != "" || a.MotionModel.Default != "qwen3:8b" {
		t.Fatalf("by default none is saved and the assistant's is the one used: %+v", a.MotionModel)
	}
	for _, bad := range []string{"two words", "line\nbreak", strings.Repeat("x", 121)} {
		if rec := doJSON(t, s.Handler(), http.MethodPut, "/api/settings", map[string]any{"motion_model": bad}); rec.Code != http.StatusBadRequest {
			t.Errorf("%q must be refused, got %d", bad, rec.Code)
		}
	}
	if rec := doJSON(t, s.Handler(), http.MethodPut, "/api/settings", map[string]any{"motion_model": " qwen3-coder:30b "}); rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	if _, a := read(); a.MotionModel.Saved != "qwen3-coder:30b" {
		t.Fatalf("saved (trimmed): %+v", a.MotionModel)
	}
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/routing", nil)
	var rt struct {
		Routing map[string]struct {
			Model string `json:"model"`
		} `json:"routing"`
	}
	json.Unmarshal(rec.Body.Bytes(), &rt)
	if rt.Routing["motion_graphics"].Model != "qwen3-coder:30b" || rt.Routing["assistant"].Model != "" {
		t.Fatalf("the Tasks card shows the model of the motion task only: %+v", rt.Routing)
	}
	doJSON(t, s.Handler(), http.MethodPut, "/api/settings", map[string]any{"motion_model": ""})
	if _, a := read(); a.MotionModel.Saved != "" {
		t.Fatal("an empty text removes the setting")
	}
}

// With none saved the default shown is the one that would write the pages: a preferred model the Ollama has.
func TestSettings_MotionModelDefaultIsAPreferredInstalledOne(t *testing.T) {
	isolateSettings(t)
	t.Setenv("VERITMO_ASSIST_MODEL", "qwen3:8b")
	ol := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{{"name": "qwen3:8b", "size": 1}, {"name": "gemma4:26b", "size": 2}}})
	}))
	defer ol.Close()
	t.Setenv("OLLAMA_HOST", ol.URL)
	s, _ := newProjectsServer(t)
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/settings", nil)
	var a struct {
		MotionModel motionModelView `json:"motion_model"`
	}
	json.Unmarshal(rec.Body.Bytes(), &a)
	if a.MotionModel.Saved != "" || a.MotionModel.Default != "gemma4:26b" {
		t.Errorf("motion_model = %+v, want default gemma4:26b", a.MotionModel)
	}
}

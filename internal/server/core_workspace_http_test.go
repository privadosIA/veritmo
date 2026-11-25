package server

// core_workspace_http_test.go: tests of the routes of the workspace (internal/server/workspace) that need the whole
// server: the token of the API, the music services, the keys of the download manager, the start of the server.

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func lineValue(out, name string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, name+" ") {
			return strings.TrimPrefix(l, name+" ")
		}
	}
	return ""
}

func TestEstimate_ATokenIsNeeded(t *testing.T) {
	s := newAuthServer(t)
	for _, tc := range []struct{ method, path string }{{http.MethodPost, "/api/estimate"}, {http.MethodGet, "/api/stats/phase-duration"}} {
		if rec := doJSON(t, s.Handler(), tc.method, tc.path, map[string]any{}); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a token: %d", tc.method, tc.path, rec.Code)
		}
	}
}

func TestMedia_ServerRefusesToStartWithAnUnwritableDirectory(t *testing.T) {
	repo := t.TempDir()
	f := filepath.Join(repo, "file")
	os.WriteFile(f, []byte("x"), 0o644)
	_, err := New(Config{ProjectsDir: filepath.Join(repo, "projects"), DBPath: filepath.Join(repo, "o.db"), MediaDir: filepath.Join(f, "media")})
	if err == nil || !strings.Contains(err.Error(), "media directory") {
		t.Fatalf("err = %v", err)
	}
}

func TestMetrics_NeedsTheTokenLikeTheRestOfTheAPI(t *testing.T) {
	h := newAuthServer(t).Handler()
	if rec := do(h, "GET", "/api/metrics", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: %d", rec.Code)
	}
	rec := do(h, "GET", "/api/metrics", map[string]string{"Authorization": "Bearer " + testToken})
	if rec.Code != http.StatusOK {
		t.Fatalf("with the token: %d", rec.Code)
	}
	if v, err := strconv.ParseFloat(strings.TrimSpace(lineValue(rec.Body.String(), "veritmo_uptime_seconds")), 64); err != nil || v < 0 {
		t.Errorf("uptime: %q", lineValue(rec.Body.String(), "veritmo_uptime_seconds"))
	}
}

func TestEnvironment_Tokens(t *testing.T) {
	// the Civitai key file lives under $HOME: isolate it, and no variable
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CIVITAI_TOKEN", "")
	t.Setenv("HF_TOKEN", "")

	t.Run("no keys", func(t *testing.T) {
		s, _ := newProjectsServer(t)
		if got := getEnvironment(t, s, "").Tokens; got != (tokenStatus{}) {
			t.Errorf("tokens = %+v, want both false", got)
		}
		rec := doJSON(t, s.Handler(), http.MethodGet, "/api/environment", nil)
		if !strings.Contains(rec.Body.String(), `"tokens":{"huggingface":false,"civitai":false}`) {
			t.Errorf("serialized shape: %s", rec.Body.String())
		}
	})

	t.Run("one key configured", func(t *testing.T) {
		s, _ := newProjectsServer(t)
		s.models.SetKeys(func() string { return "hf_x" }, nil)
		got := getEnvironment(t, s, "").Tokens
		if !got.HuggingFace || got.Civitai {
			t.Errorf("tokens = %+v, want only huggingface", got)
		}
	})

	t.Run("a Civitai key from the environment", func(t *testing.T) {
		t.Setenv("CIVITAI_TOKEN", "civ_env")
		s, _ := newProjectsServer(t)
		if got := getEnvironment(t, s, "").Tokens; !got.Civitai || got.HuggingFace {
			t.Errorf("tokens = %+v, want only civitai", got)
		}
	})

	t.Run("the Civitai key file is picked up without a restart", func(t *testing.T) {
		s, _ := newProjectsServer(t)
		if getEnvironment(t, s, "").Tokens.Civitai {
			t.Fatal("no key yet: civitai must be false")
		}
		dir := filepath.Join(home, ".config")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "civitai-token"), []byte("civ_file_secret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if !getEnvironment(t, s, "").Tokens.Civitai {
			t.Error("the key file was created after startup: the next report must show it")
		}
		// an empty or whitespace-only file is not a key
		if err := os.WriteFile(filepath.Join(dir, "civitai-token"), []byte("  \n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if getEnvironment(t, s, "").Tokens.Civitai {
			t.Error("a blank key file must not count as a key")
		}
	})

	t.Run("nothing secret leaves the server", func(t *testing.T) {
		dir := filepath.Join(home, ".config")
		_ = os.MkdirAll(dir, 0o755)
		if err := os.WriteFile(filepath.Join(dir, "civitai-token"), []byte("civ_file_secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CIVITAI_TOKEN", "civ_env_secret")
		s, _ := newProjectsServer(t)
		s.models.SetKeys(func() string { return "hf_secret_value" }, nil)
		body := doJSON(t, s.Handler(), http.MethodGet, "/api/environment", nil).Body.String()
		for _, secret := range []string{"hf_secret_value", "civ_env_secret", "civ_file_secret", "civitai-token", home} {
			if strings.Contains(body, secret) {
				t.Errorf("the report leaks %q: %s", secret, body)
			}
		}
	})
}

func TestModels_InstallRefusedWhenTheDirectoryIsGone(t *testing.T) {
	s, m := newModelsServer(t)
	if err := os.RemoveAll(m); err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/downloads", map[string]string{"id": "anything"})
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "models directory") {
		t.Fatalf("install: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(m); err == nil {
		t.Fatal("the refused install created the directory")
	}
	env := doJSON(t, s.Handler(), http.MethodGet, "/api/environment", nil)
	if !strings.Contains(env.Body.String(), `"available":false`) {
		t.Fatalf("the environment must say it is unavailable: %s", env.Body)
	}
}

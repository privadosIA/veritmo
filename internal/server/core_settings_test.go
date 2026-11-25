package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"veritmo/internal/platform/runner"
	"veritmo/internal/server/api"
)

// isolateSettings points the config dir at a temp dir and clears OLLAMA_HOST.
func isolateSettings(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("OLLAMA_HOST", "")
	return dir
}

type settingsAnswer struct {
	Ollama ollamaSettingsView `json:"ollama"`
}

func getSettings(t *testing.T, s *Server) settingsAnswer {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/settings", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var a settingsAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestSettings_DefaultThenSaveThenRemove(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	a := getSettings(t, s)
	if a.Ollama.Host != "http://127.0.0.1:11434" || a.Ollama.Source != "default" || a.Ollama.Remote || a.Ollama.Saved != "" {
		t.Fatalf("default: %+v", a.Ollama)
	}

	rec := doJSON(t, s.Handler(), http.MethodPut, "/api/settings", map[string]string{"ollama_host": "192.168.1.50"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	a = getSettings(t, s)
	if a.Ollama.Host != "http://192.168.1.50:11434" || a.Ollama.Source != "setting" || !a.Ollama.Remote || a.Ollama.Saved != "http://192.168.1.50:11434" {
		t.Fatalf("saved: %+v", a.Ollama)
	}

	// Empty removes it; the environment (here set) is next.
	t.Setenv("OLLAMA_HOST", "10.0.0.2:11434")
	rec = doJSON(t, s.Handler(), http.MethodPut, "/api/settings", map[string]string{"ollama_host": ""})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	a = getSettings(t, s)
	if a.Ollama.Source != "env" || a.Ollama.Host != "http://10.0.0.2:11434" || a.Ollama.Saved != "" {
		t.Fatalf("removed: %+v", a.Ollama)
	}
}

func TestSettings_InvalidAddressIs400AndChangesNothing(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	doJSON(t, s.Handler(), http.MethodPut, "/api/settings", map[string]string{"ollama_host": "10.1.1.1"})
	for in, want := range map[string]string{
		"http://user:pass@host:11434": "credentials",
		"http://host:11434/path":      "no path",
		"ftp://host":                  "only http and https",
	} {
		rec := doJSON(t, s.Handler(), http.MethodPut, "/api/settings", map[string]string{"ollama_host": in})
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%q: status %d: %s (want a reason with %q)", in, rec.Code, rec.Body, want)
		}
	}
	if got := getSettings(t, s).Ollama.Saved; got != "http://10.1.1.1:11434" {
		t.Errorf("an invalid address must leave the setting unchanged, saved = %q", got)
	}
	if rec := doJSON(t, s.Handler(), http.MethodPut, "/api/settings", map[string]string{"other": "x"}); rec.Code != http.StatusBadRequest {
		t.Errorf("a missing ollama_host must be 400, got %d", rec.Code)
	}
}

func TestSettings_FileIsPrivate(t *testing.T) {
	dir := isolateSettings(t)
	s, _ := newProjectsServer(t)
	doJSON(t, s.Handler(), http.MethodPut, "/api/settings", map[string]string{"ollama_host": "10.1.1.1"})
	fi, err := os.Stat(filepath.Join(dir, "veritmo", "settings.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("settings.json must exist with mode 0600: %v %v", err, fi)
	}
}

// fakeOllama answers version, tags and show like Ollama does.
func fakeOllama(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/api/version":
			_, _ = w.Write([]byte(`{"version":"0.12.3"}`))
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"gemma4:26b"},{"name":"qwen3:8b"}]}`))
		case "/api/show":
			var req struct{ Model string }
			_ = json.NewDecoder(r.Body).Decode(&req)
			caps := `["completion"]`
			if req.Model == "gemma4:26b" {
				caps = `["completion","vision"]`
			}
			_, _ = w.Write([]byte(`{"capabilities":` + caps + `}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestOllamaTest_ReportsVersionAndModelsWithVision(t *testing.T) {
	isolateSettings(t)
	srv, _ := fakeOllama(t)
	s, _ := newProjectsServer(t)
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/settings/ollama/test", map[string]string{"host": srv.URL})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var res api.OllamaTestResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if !res.OK || res.Version != "0.12.3" || res.Remote || len(res.Models) != 2 {
		t.Fatalf("unexpected %+v", res)
	}
	vision := map[string]bool{}
	for _, m := range res.Models {
		vision[m.Name] = m.Vision
	}
	if !vision["gemma4:26b"] || vision["qwen3:8b"] {
		t.Errorf("vision flags wrong: %v", vision)
	}
	// Testing saves nothing.
	if got := getSettings(t, s).Ollama.Saved; got != "" {
		t.Errorf("a test must not save, saved = %q", got)
	}
}

func TestOllamaTest_DefaultsToTheAddressInUse(t *testing.T) {
	isolateSettings(t)
	srv, hits := fakeOllama(t)
	s, _ := newProjectsServer(t)
	t.Setenv("OLLAMA_HOST", srv.URL)
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/settings/ollama/test", nil)
	var res api.OllamaTestResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != http.StatusOK || !res.OK || hits.Load() == 0 {
		t.Fatalf("status %d hits %d: %s", rec.Code, hits.Load(), rec.Body)
	}
}

func TestOllamaTest_NothingAnsweringIs200WithOKFalse(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/settings/ollama/test", map[string]string{"host": "http://127.0.0.1:1"})
	var res api.OllamaTestResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != http.StatusOK || res.OK || !strings.Contains(res.Error, "127.0.0.1:1") {
		t.Fatalf("want 200 with ok false naming the address, got %d %+v", rec.Code, res)
	}
}

func TestOllamaTest_InvalidAddressIs400AndRemoteIsFlagged(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/settings/ollama/test", map[string]string{"host": "http://h/path"}); rec.Code != http.StatusBadRequest {
		t.Errorf("an invalid host must be 400, got %d", rec.Code)
	}
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/settings/ollama/test", map[string]string{"host": "http://192.0.2.1:11434"})
	var res api.OllamaTestResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if !res.Remote {
		t.Errorf("a non-loopback address must be flagged remote: %+v", res)
	}
}

func TestOllamaTest_DoesNotFollowRedirects(t *testing.T) {
	isolateSettings(t)
	var hitTarget atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitTarget.Store(true)
		_, _ = w.Write([]byte(`{"version":"x"}`))
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	}))
	defer redirector.Close()
	s, _ := newProjectsServer(t)
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/settings/ollama/test", map[string]string{"host": redirector.URL})
	var res api.OllamaTestResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if res.OK || hitTarget.Load() {
		t.Fatalf("a redirect must not be followed: ok=%v followed=%v", res.OK, hitTarget.Load())
	}
}

func TestSettings_TokenIsRequired(t *testing.T) {
	isolateSettings(t)
	s := newAuthServer(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/settings"},
		{http.MethodPut, "/api/settings"},
		{http.MethodPost, "/api/settings/ollama/test"},
	} {
		if rec := do(s.Handler(), c.method, c.path, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a token: %d, want 401", c.method, c.path, rec.Code)
		}
		if rec := do(s.Handler(), c.method, c.path, map[string]string{"Authorization": "Bearer " + testToken}); rec.Code == http.StatusUnauthorized {
			t.Errorf("%s %s with the token must not be 401", c.method, c.path)
		}
	}
}

func TestEnvironment_ReportsTheOllamaInUseAndSkipsTheLocalCaveatWhenRemote(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	s.look = func(name string) (string, error) { return "/fake/" + name, nil }
	canned := map[string]string{
		"--query-gpu=index,name,memory.free,memory.total":        "0, NVIDIA GeForce RTX 5060 Ti, 14900, 16311\n",
		"--query-gpu=index,uuid":                                 "0, GPU-bbb\n",
		"--query-compute-apps=gpu_uuid,process_name,used_memory": "GPU-bbb, /usr/bin/ollama, 4096\n",
	}
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if inv.Name != "nvidia-smi" {
			return runner.ScriptedResult{Stdout: []byte("v1\n")}
		}
		return runner.ScriptedResult{Stdout: []byte(canned[inv.Args[0]])}
	})
	s.envRunner = fr

	// Local Ollama: the caveat is attached.
	rep := getEnvironment(t, s, "")
	if rep.Ollama.Host != "http://127.0.0.1:11434" || rep.Ollama.Source != "default" || rep.Ollama.Remote {
		t.Fatalf("report: %+v", rep.Ollama)
	}
	if len(rep.GPUs[0].Warnings) != 1 {
		t.Fatalf("a local Ollama holding memory carries the caveat: %+v", rep.GPUs[0].Warnings)
	}

	// Remote Ollama: no caveat, and the address and source are reported.
	doJSON(t, s.Handler(), http.MethodPut, "/api/settings", map[string]string{"ollama_host": "192.168.1.50"})
	rep = getEnvironment(t, s, "")
	if rep.Ollama.Host != "http://192.168.1.50:11434" || rep.Ollama.Source != "setting" || !rep.Ollama.Remote {
		t.Fatalf("report: %+v", rep.Ollama)
	}
	if len(rep.GPUs[0].Warnings) != 0 {
		t.Errorf("a remote Ollama holds none of this machine's memory, got %+v", rep.GPUs[0].Warnings)
	}
}

// dockerAnswering is a runner whose docker answers (or not) like the real one does for `docker info`.
func dockerAnswering(ok bool) runner.CommandRunner {
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if inv.Name == "docker" && !ok {
			return runner.ScriptedResult{Err: errors.New("Cannot connect to the Docker daemon")}
		}
		if inv.Name == "docker" && len(inv.Args) > 0 && inv.Args[0] == "info" {
			return runner.ScriptedResult{Stdout: []byte(`{"nvidia":{},"runc":{}}`)}
		}
		return runner.ScriptedResult{}
	})
	return fr
}

func backendOf(t *testing.T, body []byte, engine string) (backend, source string) {
	t.Helper()
	var out map[string]struct {
		Backend       string `json:"backend"`
		BackendSource string `json:"backend_source"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out[engine].Backend, out[engine].BackendSource
}

// The backend of each generator is a setting: saved, shown with its source and removable; a value that is not one of the two is
// refused with 422, and so is docker when Docker does not answer (nothing is saved then).
func TestSettings_TheMusicBackendIsSavedShownAndValidated(t *testing.T) {
	isolateSettings(t)
	s := newTestServer(t)
	h := s.Handler()
	s.envRunner = dockerAnswering(true)

	rec := doJSON(t, h, http.MethodGet, "/api/settings", nil)
	if b, src := backendOf(t, rec.Body.Bytes(), "heartmula"); b != "dotnet" || src != "default" {
		t.Errorf("by default: %s %s", b, src)
	}
	rec = doJSON(t, h, http.MethodPut, "/api/settings", map[string]any{"diffrhythm2_backend": "docker"})
	if rec.Code != http.StatusOK {
		t.Fatalf("docker: %d %s", rec.Code, rec.Body)
	}
	if b, src := backendOf(t, rec.Body.Bytes(), "diffrhythm2"); b != "docker" || src != "setting" {
		t.Errorf("saved: %s %s", b, src)
	}
	if b, src := backendOf(t, rec.Body.Bytes(), "heartmula"); b != "dotnet" || src != "default" {
		t.Errorf("the other generator is not touched: %s %s", b, src)
	}
	if b, _ := backendOf(t, doJSON(t, h, http.MethodGet, "/api/settings", nil).Body.Bytes(), "diffrhythm2"); b != "docker" {
		t.Error("it is kept: a later GET shows it")
	}
	if rec := doJSON(t, h, http.MethodPut, "/api/settings", map[string]any{"heartmula_backend": "podman"}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("another value: %d %s", rec.Code, rec.Body)
	}
	rec = doJSON(t, h, http.MethodPut, "/api/settings", map[string]any{"diffrhythm2_backend": ""})
	if b, src := backendOf(t, rec.Body.Bytes(), "diffrhythm2"); rec.Code != http.StatusOK || b != "dotnet" || src != "default" {
		t.Errorf("removed: %d %s %s", rec.Code, b, src)
	}
}

func TestSettings_TheDockerBackendIsRefusedWhenDockerDoesNotAnswer(t *testing.T) {
	isolateSettings(t)
	s := newTestServer(t)
	h := s.Handler()
	s.envRunner = dockerAnswering(false)
	rec := doJSON(t, h, http.MethodPut, "/api/settings", map[string]any{"heartmula_backend": "docker"})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"reason":"no_docker"`) {
		t.Fatalf("docker does not answer: %d %s", rec.Code, rec.Body)
	}
	if b, src := backendOf(t, doJSON(t, h, http.MethodGet, "/api/settings", nil).Body.Bytes(), "heartmula"); b != "dotnet" || src != "default" {
		t.Errorf("nothing was saved: %s %s", b, src)
	}
	// the .NET service needs no docker
	if rec := doJSON(t, h, http.MethodPut, "/api/settings", map[string]any{"heartmula_backend": "dotnet"}); rec.Code != http.StatusOK {
		t.Errorf("dotnet: %d %s", rec.Code, rec.Body)
	}
}

// The backend of the 3D generator (TripoSR) is a setting like the music ones: saved, shown in the model3d block with its source, removable,
// and the container is refused with 422 when Docker does not answer.
func TestSettings_TheModel3DBackendIsSavedShownAndValidated(t *testing.T) {
	isolateSettings(t)
	s := newTestServer(t)
	h := s.Handler()
	s.models3d.Service().Runner = dockerAnswering(true)

	if b, src := backendOf(t, doJSON(t, h, http.MethodGet, "/api/settings", nil).Body.Bytes(), "model3d"); b != "dotnet" || src != "default" {
		t.Errorf("by default: %s %s", b, src)
	}
	rec := doJSON(t, h, http.MethodPut, "/api/settings", map[string]any{"model3d_backend": "docker"})
	if b, src := backendOf(t, rec.Body.Bytes(), "model3d"); rec.Code != http.StatusOK || b != "docker" || src != "setting" {
		t.Errorf("saved: %d %s %s", rec.Code, b, src)
	}
	if rec := doJSON(t, h, http.MethodPut, "/api/settings", map[string]any{"model3d_backend": "podman"}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("another value: %d %s", rec.Code, rec.Body)
	}
	rec = doJSON(t, h, http.MethodPut, "/api/settings", map[string]any{"model3d_backend": ""})
	if b, src := backendOf(t, rec.Body.Bytes(), "model3d"); rec.Code != http.StatusOK || b != "dotnet" || src != "default" {
		t.Errorf("removed: %d %s %s", rec.Code, b, src)
	}
	s.models3d.Service().Runner = dockerAnswering(false)
	rec = doJSON(t, h, http.MethodPut, "/api/settings", map[string]any{"model3d_backend": "docker"})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"reason":"no_docker"`) {
		t.Fatalf("docker does not answer: %d %s", rec.Code, rec.Body)
	}
	if b, _ := backendOf(t, doJSON(t, h, http.MethodGet, "/api/settings", nil).Body.Bytes(), "model3d"); b != "dotnet" {
		t.Errorf("nothing was saved: %s", b)
	}
}

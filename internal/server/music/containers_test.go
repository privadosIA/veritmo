package music

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"veritmo/internal/music/clap"
	"veritmo/internal/music/diffrhythm2"
	"veritmo/internal/music/heartmula"
	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
	"veritmo/internal/video/videovision"
)

// runFakeService is what the test binary does when the server starts it as a service (see TestMain and
// installServices): "ready" answers /health like the real services do on the port the server chose, "silent" only
// waits, so a start never gets ready.
func runFakeService(mode string) {
	if mode == "silent" {
		select {}
	}
	port := ""
	for _, name := range []string{"HEARTMULA_PORT", "DIFFRHYTHM2_PORT", "CLAP_PORT"} {
		if v := os.Getenv(name); v != "" {
			port = v
		}
	}
	_ = http.ListenAndServe("127.0.0.1:"+port, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(heartmula.Health{Ready: true, Version: heartmula.Version, GPU: &heartmula.GPU{Name: "x", FreeMB: 15000, TotalMB: 16311}})
	}))
	os.Exit(1)
}

// installServices installs the four services in a temporary folder, with the test binary standing in for dotnet
// (it answers ready, or stays silent when VERITMO_FAKE_SERVICE says so), and stops what a test started.
func installServices(t *testing.T, s *testServer) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	services := t.TempDir()
	host := &musicsvc.ServiceHost{Dir: services, StateDir: t.TempDir(), Dotnet: exe}
	specs := []musicsvc.Spec{heartmula.Spec(), diffrhythm2.Spec(), clap.Spec(), videovision.Spec()}
	for _, sp := range specs {
		if err := os.MkdirAll(filepath.Join(services, sp.Service.Dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(host.DLLPath(sp.Service), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("VERITMO_FAKE_SERVICE", "ready")
	s.Services = host
	t.Cleanup(func() {
		for _, sp := range specs {
			_ = musicsvc.Local{Spec: sp, Services: host}.Stop(context.Background())
		}
	})
}

// startFake starts the service of an engine by hand, as if the user had started it from the Environment screen.
func startFake(t *testing.T, s *testServer, sp musicsvc.Spec, port int) {
	t.Helper()
	l := musicsvc.Local{Spec: sp, Runner: s.heartMulaRunner, Models: t.TempDir(), Services: s.Services}
	if err := l.Start(context.Background(), musicsvc.GPUInfo{Index: 1}, port); err != nil {
		t.Fatal(err)
	}
}

// containerServer is a server whose music services are installed (see installServices), with a 16 GB card and the
// HeartMuLa weights.
func containerServer(t *testing.T) *testServer {
	t.Helper()
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	installServices(t, s)
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if inv.Name == "nvidia-smi" {
			return runner.ScriptedResult{Stdout: []byte("1, NVIDIA GeForce RTX 5060 Ti, 15900, 16311\n")}
		}
		return runner.ScriptedResult{}
	})
	s.heartMulaRunner = fr
	s.heartMulaWeights = t.TempDir()
	writeHeartMulaWeights(t, s.heartMulaWeights)
	s.engines.ttl = 1
	return s
}

func TestContainers_StartAndStopFromTheEnvironment(t *testing.T) {
	s := containerServer(t)
	h := s.Handler()

	if a := getEngines(t, s); a.Reason != musicsvc.ReasonNotRunning || a.Container == nil || a.Container.State != musicsvc.StateStopped {
		t.Fatalf("stopped: %+v", a)
	}
	rec := doJSON(t, h, http.MethodPost, "/api/music/engines/heartmula/start", map[string]any{"gpu": 1})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	waitUntil(t, func() bool { a := getEngines(t, s); return a.Available && a.Container.State == musicsvc.StateRunning })
	if a := getEngines(t, s); a.Container.GPU == nil || *a.Container.GPU != 1 {
		t.Errorf("it runs on the card asked for: %+v", a.Container)
	}
	if rec := doJSON(t, h, http.MethodPost, "/api/music/engines/heartmula/start", nil); rec.Code != http.StatusConflict {
		t.Errorf("already running: %d", rec.Code)
	}
	if rec := doJSON(t, h, http.MethodGet, "/api/music/engines?stats=1", nil); !strings.Contains(rec.Body.String(), `"memory_mb":`) {
		t.Errorf("stats: %s", rec.Body)
	}
	if rec := doJSON(t, h, http.MethodPost, "/api/music/engines/heartmula/stop", nil); rec.Code != http.StatusOK {
		t.Fatalf("stop: %d %s", rec.Code, rec.Body)
	}
	if a := getEngines(t, s); a.Reason != musicsvc.ReasonNotRunning {
		t.Errorf("stopped again: %+v", a)
	}
	if rec := doJSON(t, h, http.MethodPost, "/api/music/engines/nope/start", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown engine: %d", rec.Code)
	}
}

func TestContainers_AStartThatFailsLeavesItStoppedWithTheError(t *testing.T) {
	s := containerServer(t)
	t.Setenv("VERITMO_FAKE_SERVICE", "silent") // the service starts but never says ready
	oldT, oldP := heartmula.StartTimeout, heartmula.PollEvery
	heartmula.StartTimeout, heartmula.PollEvery = 300*1e6, 50*1e6
	t.Cleanup(func() { heartmula.StartTimeout, heartmula.PollEvery = oldT, oldP })
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music/engines/heartmula/start", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("start: %d", rec.Code)
	}
	waitUntil(t, func() bool {
		a := getEngines(t, s)
		return a.Container != nil && a.Container.State == musicsvc.StateStopped && a.Container.Error != ""
	})
}

func musicConfig(generator string) *config.Config {
	cfg := &config.Config{}
	cfg.Output.Mode = config.ModeMusic
	cfg.Defaults.Music = &config.Music{Generator: generator}
	return cfg
}

func TestContainers_ARunIsRefusedWhileItsServiceIsStopped(t *testing.T) {
	s := containerServer(t)
	lerr := s.containerPreflight(musicConfig(config.GeneratorHeartMuLa))
	if lerr == nil || lerr.Status != http.StatusUnprocessableEntity || lerr.Code != "engine_not_running" || lerr.Reason != musicsvc.ReasonNotRunning {
		t.Fatalf("stopped: %+v", lerr)
	}
	if _, running := (musicsvc.Local{Spec: heartmula.Spec(), Services: s.Services}).Running(context.Background()); running {
		t.Error("the check starts nothing")
	}
	if lerr := s.containerPreflight(musicConfig(config.GeneratorACEStep)); lerr != nil {
		t.Errorf("ACE-Step has no service: %+v", lerr)
	}
	t.Setenv("VERITMO_FAKE_SERVICE", "silent")
	startFake(t, s, heartmula.Spec(), 1)
	if lerr := s.containerPreflight(musicConfig(config.GeneratorHeartMuLa)); lerr != nil {
		t.Errorf("running: %+v", lerr)
	}
}

func TestContainers_ItIsNotStoppedWhileARunUsesIt(t *testing.T) {
	s := containerServer(t)
	t.Setenv("VERITMO_FAKE_SERVICE", "silent")
	startFake(t, s, heartmula.Spec(), 1)
	releaseRun := s.Workspace.SimulateRun("r1", musicConfig(config.GeneratorHeartMuLa))
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music/engines/heartmula/stop", nil); rec.Code != http.StatusConflict {
		t.Errorf("a run uses it: %d", rec.Code)
	}
	releaseRun()
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music/engines/heartmula/stop", nil); rec.Code != http.StatusOK {
		t.Errorf("no run: %d", rec.Code)
	}
}

// The contract must list every engine the start and stop calls accept (LAION-CLAP is one: the dashboard's typed
// client only offers what the description enumerates).
func TestContract_StartAndStopListEveryEngine(t *testing.T) {
	raw, err := os.ReadFile("../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/music/engines/{engine}/start", "/api/music/engines/{engine}/stop"} {
		var got []string
		post, _ := doc.Paths[path]["post"].(map[string]any)
		params, _ := post["parameters"].([]any)
		for _, p := range params {
			pm, _ := p.(map[string]any)
			if pm["name"] != "engine" {
				continue
			}
			schema, _ := pm["schema"].(map[string]any)
			enum, _ := schema["enum"].([]any)
			for _, e := range enum {
				got = append(got, fmt.Sprint(e))
			}
		}
		for _, want := range []string{engineHeartMuLa, engineDiffRhythm2, engineCLAP} {
			found := false
			for _, g := range got {
				found = found || g == want
			}
			if !found {
				t.Errorf("%s does not list the engine %q (it lists %v)", path, want, got)
			}
		}
	}
}

func TestMetrics_ReportsTheMusicServices(t *testing.T) {
	s := containerServer(t)
	h := s.Handler()
	if rec := doJSON(t, h, http.MethodGet, "/api/metrics", nil); !strings.Contains(rec.Body.String(), `veritmo_music_service_running{service="heartmula"} 0`) {
		t.Fatalf("stopped: %s", rec.Body)
	}
	if rec := doJSON(t, h, http.MethodPost, "/api/music/engines/heartmula/start", map[string]any{"gpu": 1}); rec.Code != http.StatusAccepted {
		t.Fatalf("start: %d", rec.Code)
	}
	waitUntil(t, func() bool {
		rec := doJSON(t, h, http.MethodGet, "/api/metrics", nil)
		return strings.Contains(rec.Body.String(), `veritmo_music_service_running{service="heartmula"} 1`)
	})
}

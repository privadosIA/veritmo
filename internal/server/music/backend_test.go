package music

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"veritmo/internal/music/heartmula"
	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/platform/runner"
	"veritmo/internal/platform/settings"
)

// dockerFake is a docker for the server's music tests: it answers the commands the container backend uses and remembers them.
type dockerFake struct {
	mu      sync.Mutex
	daemon  bool
	running bool
	noImage bool
	gpu     string
	calls   []string
}

func (d *dockerFake) handle(inv runner.Invocation) runner.ScriptedResult {
	if inv.Name == "nvidia-smi" {
		return runner.ScriptedResult{Stdout: []byte("1, NVIDIA GeForce RTX 5060 Ti, 15900, 16311\n")}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, strings.Join(inv.Args, " "))
	if !d.daemon {
		return runner.ScriptedResult{Err: errors.New("Cannot connect to the Docker daemon")}
	}
	switch inv.Args[0] {
	case "info":
		return runner.ScriptedResult{Stdout: []byte(`{"nvidia":{},"runc":{}}`)}
	case "image":
		if d.noImage {
			return runner.ScriptedResult{Err: errors.New("No such image")}
		}
	case "run":
		d.running = true
		for i, a := range inv.Args {
			if a == "--gpus" {
				d.gpu = strings.TrimPrefix(inv.Args[i+1], "device=")
			}
		}
	case "rm":
		d.running = false
	case "inspect":
		if !d.running {
			return runner.ScriptedResult{Err: errors.New("No such object")}
		}
		return runner.ScriptedResult{Stdout: []byte("running|2026-11-19T12:00:00Z|" + d.gpu)}
	}
	return runner.ScriptedResult{}
}

func (d *dockerFake) saw(prefix string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func backendServer(t *testing.T, d *dockerFake) *testServer {
	t.Helper()
	s := containerServer(t)
	fr := runner.NewFakeRunner()
	fr.SetHandler(d.handle)
	s.heartMulaRunner = fr
	return s
}

// The backend is a setting of each generator, shown with its source; removing it goes back to the .NET service.
func TestBackend_SettingIsShownWithItsSource(t *testing.T) {
	s := backendServer(t, &dockerFake{daemon: true})
	get := func() (string, string) {
		v := CurrentHeartMulaSettings()
		return v.Backend, v.BackendSource
	}
	if b, src := get(); b != "dotnet" || src != "default" {
		t.Errorf("by default: %s %s", b, src)
	}
	if err := settings.SetMusicBackend("heartmula", "docker"); err != nil {
		t.Fatal(err)
	}
	if b, src := get(); b != "docker" || src != "setting" {
		t.Errorf("saved: %s %s", b, src)
	}
	if v := CurrentDiffRhythm2Settings(); v.Backend != "dotnet" || v.BackendSource != "default" {
		t.Errorf("only HeartMuLa changed: %+v", v)
	}
	// the engine now looks for the container: Docker, its image and the runtime are asked, not the .NET service
	if a := getEngines(t, s); a.Mode != heartmula.ModeLocal || a.Reason != heartmula.ReasonNotRunning || a.Container == nil {
		t.Errorf("the docker backend: %+v", a)
	}
	if err := settings.SetMusicBackend("heartmula", ""); err != nil {
		t.Fatal(err)
	}
	if b, src := get(); b != "dotnet" || src != "default" {
		t.Errorf("removed: %s %s", b, src)
	}
}

func TestBackend_PrepareRefusesWithoutDockerAndForAnUnknownGenerator(t *testing.T) {
	d := &dockerFake{daemon: false}
	s := backendServer(t, d)
	ctx := context.Background()
	if err := s.mHandler.PrepareBackend(ctx, "nope", "docker"); err == nil || err.Status != http.StatusNotFound {
		t.Errorf("unknown generator: %v", err)
	}
	err := s.mHandler.PrepareBackend(ctx, "diffrhythm2", "docker")
	if err == nil || err.Status != http.StatusUnprocessableEntity || err.Reason != musicsvc.ReasonNoDocker {
		t.Errorf("docker does not answer: %+v", err)
	}
	if err := s.mHandler.PrepareBackend(ctx, "heartmula", "dotnet"); err != nil {
		t.Errorf("dotnet needs no docker: %v", err)
	}
}

// Changing the backend stops what runs on the old one, so that nothing is left holding the card.
func TestBackend_PrepareStopsTheOldOne(t *testing.T) {
	d := &dockerFake{daemon: true, running: true, gpu: "1"}
	s := backendServer(t, d)
	ctx := context.Background()
	if err := settings.SetMusicBackend("heartmula", "docker"); err != nil {
		t.Fatal(err)
	}
	if err := s.mHandler.PrepareBackend(ctx, "heartmula", "dotnet"); err != nil {
		t.Fatal(err)
	}
	if !d.saw("rm -f vt-heartmula") {
		t.Errorf("the container is removed when the backend changes: %v", d.calls)
	}
	// saving the backend that is already in use stops nothing
	d.mu.Lock()
	d.calls = nil
	d.mu.Unlock()
	if err := settings.SetMusicBackend("heartmula", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.mHandler.PrepareBackend(ctx, "heartmula", "dotnet"); err != nil {
		t.Fatal(err)
	}
	if d.saw("rm") {
		t.Errorf("same backend: %v", d.calls)
	}
}

// /start runs the container on the card asked for; a container that never says ready is removed and the reason is kept.
func TestBackend_StartRunsTheContainerAndRemovesOneThatDoesNotGetReady(t *testing.T) {
	d := &dockerFake{daemon: true}
	s := backendServer(t, d)
	h := s.Handler()
	oldTimeout, oldPoll := heartmula.StartTimeout, heartmula.PollEvery
	heartmula.StartTimeout, heartmula.PollEvery = 300*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { heartmula.StartTimeout, heartmula.PollEvery = oldTimeout, oldPoll })

	if err := settings.SetMusicBackend("heartmula", "docker"); err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, h, http.MethodPost, "/api/music/engines/heartmula/start", map[string]any{"gpu": 1})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	waitUntil(t, func() bool { a := getEngines(t, s); return a.Container != nil && a.Container.Error != "" })
	if !d.saw("run -d --name vt-heartmula --restart no --gpus device=1 -p 127.0.0.1:8787:8787") {
		t.Errorf("docker run: %v", d.calls)
	}
	d.mu.Lock()
	running := d.running
	d.mu.Unlock()
	if running {
		t.Error("a container that does not get ready is removed")
	}
	// no 409 for a container the server manages: an address typed by hand is what keeps the 409
	if err := settings.SetHeartMula("192.168.77.5:8787", ""); err != nil {
		t.Fatal(err)
	}
	s.mHandler.BumpEngines()
	if rec := doJSON(t, h, http.MethodPost, "/api/music/engines/heartmula/start", nil); rec.Code != http.StatusConflict {
		t.Errorf("an address typed by hand: %d", rec.Code)
	}
}

// GET /api/music/engines carries `backends` for the two generators that have a container, and for no other engine.
func TestBackend_EnginesAnswerCarriesTheBackendsOfTheGenerators(t *testing.T) {
	d := &dockerFake{daemon: true}
	s := backendServer(t, d)
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/music/engines", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var out struct {
		Engines map[string]struct {
			Backends map[string]musicsvc.BackendInfo `json:"backends"`
		} `json:"engines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"heartmula", "diffrhythm2"} {
		b := out.Engines[name].Backends
		if len(b) != 2 || b[musicsvc.BackendDotnet].Missing == nil && !b[musicsvc.BackendDotnet].Available {
			t.Errorf("%s: %+v", name, b)
		}
		if dk := b[musicsvc.BackendDocker]; dk.Available && len(dk.Missing) != 0 {
			t.Errorf("%s docker: %+v", name, dk)
		}
	}
	// the image of the fake docker is there, the weights of HeartMuLa are written by containerServer: docker is complete for it
	if dk := out.Engines["heartmula"].Backends[musicsvc.BackendDocker]; !dk.Available {
		t.Errorf("heartmula with docker, image and weights: %+v", dk)
	}
	for _, name := range []string{"clap", "videovision"} {
		if len(out.Engines[name].Backends) != 0 {
			t.Errorf("%s has no container: %+v", name, out.Engines[name].Backends)
		}
	}
}

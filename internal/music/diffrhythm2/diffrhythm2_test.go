package diffrhythm2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/platform/runner"
)

// weights writes a complete weights folder of the service in dir.
func weights(t *testing.T, dir string) {
	t.Helper()
	for _, f := range serviceFiles {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMissingForService(t *testing.T) {
	dir := t.TempDir()
	if got := MissingForService(dir); len(got) != len(serviceFiles) {
		t.Errorf("an empty folder lacks everything: %v", got)
	}
	weights(t, dir)
	if got := MissingForService(dir); len(got) != 0 {
		t.Errorf("a complete folder lacks nothing: %v", got)
	}
	// an empty model file is not a model
	os.WriteFile(filepath.Join(dir, "model.safetensors"), nil, 0o644)
	if got := MissingForService(dir); len(got) != 1 || got[0] != "model.safetensors" {
		t.Errorf("an empty file counts as missing: %v", got)
	}
}

func TestMaxSecsFor(t *testing.T) {
	for in, want := range map[float64]float64{0: 210, -5: 210, 60: 75, 195: 210, 300: 210} {
		if got := MaxSecsFor(in); got != want {
			t.Errorf("MaxSecsFor(%v) = %v, want %v", in, got, want)
		}
	}
}

func TestRequestFieldsAreTheServicesOwn(t *testing.T) {
	b, _ := json.Marshal(Request{Lyrics: "l", Style: "Pop", MaxSecs: 90, Seed: 3, CfgStrength: 2, Steps: 16})
	for _, k := range []string{`"lyrics"`, `"style"`, `"max_secs"`, `"seed"`, `"cfg_strength"`, `"steps"`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("%s is not in %s", k, b)
		}
	}
	if strings.Contains(string(b), "tags") {
		t.Errorf("this service reads a style, not tags: %s", b)
	}
}

// serviceHost installs a service that is a shell script standing in for dotnet: it only waits.
func serviceHost(t *testing.T) *musicsvc.ServiceHost {
	t.Helper()
	root := t.TempDir()
	services := filepath.Join(root, "services")
	if err := os.MkdirAll(filepath.Join(services, "diffrhythm2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(services, "diffrhythm2", "diffrhythm2-service.dll"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(root, "dotnet")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &musicsvc.ServiceHost{Dir: services, StateDir: filepath.Join(root, "state"), BinDir: filepath.Join(root, "bin"), Dotnet: fake}
}

// smi is what nvidia-smi says in these tests.
type smi struct{ out string }

func (m *smi) handler(inv runner.Invocation) runner.ScriptedResult {
	if inv.Name == "nvidia-smi" {
		return runner.ScriptedResult{Stdout: []byte(m.out)}
	}
	return runner.ScriptedResult{}
}

const gpus = "0, NVIDIA GeForce RTX 4070, 11900, 12282\n1, NVIDIA GeForce RTX 5060 Ti, 15900, 16311\n"

func localWith(t *testing.T, m *smi) Local {
	t.Helper()
	fr := runner.NewFakeRunner()
	fr.SetHandler(m.handler)
	dir := t.TempDir()
	weights(t, dir)
	return Local{Runner: fr, Models: dir, Services: serviceHost(t)}
}

// The engine of this generator: its service, its weights, the memory it needs and its words.
func TestLocalReadyNamesTheFirstThingThatFails(t *testing.T) {
	m := &smi{gpus}
	e := &Engine{Local: localWith(t, m)}
	e.Local.Services = &musicsvc.ServiceHost{Dir: t.TempDir(), StateDir: t.TempDir()}
	a := e.Availability(context.Background())
	if a.Available || a.Reason != ReasonNoService || !strings.Contains(a.Detail, "DiffRhythm2") {
		t.Fatalf("no service: %+v", a)
	}
	e = &Engine{Local: localWith(t, m)}
	e.Local.Models = t.TempDir() // empty
	a = e.Availability(context.Background())
	if a.Reason != ReasonNoWeights || len(a.Missing) == 0 {
		t.Fatalf("no weights: %+v", a)
	}
	e = &Engine{Local: localWith(t, m)}
	if a := e.Availability(context.Background()); !a.Available || a.Mode != ModeLocal || a.Version != Version {
		t.Fatalf("ready: %+v", a)
	}
	m.out = "0, small card, 6000, 6144\n"
	if a := (&Engine{Local: localWith(t, m)}).Availability(context.Background()); a.Reason != ReasonNoGPU || !strings.Contains(a.Detail, "DiffRhythm2") {
		t.Fatalf("a card that is too small: %+v", a)
	}
}

func TestOpenReusesARunningServiceAndNamesTheGenerator(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Health{Ready: true, Version: "1", GPU: &GPU{Name: "x", FreeMB: 15000}})
	}))
	defer srv.Close()
	addr := srv.Listener.Addr().String()
	port, _ := strconv.Atoi(addr[strings.LastIndex(addr, ":")+1:])
	l := localWith(t, &smi{gpus})
	ctx := context.Background()
	if err := l.Start(ctx, GPUInfo{Index: 1}, port); err != nil {
		t.Fatal(err)
	}
	defer l.Stop(ctx)
	e := &Engine{Local: l}
	c, err := e.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c.Service != "DiffRhythm2" {
		t.Errorf("service %q", c.Service)
	}
	e.Close()
	if _, running := l.Running(ctx); !running {
		t.Error("a running service is not stopped by a run that did not start it")
	}
}

func TestOpenPicksTheBestCardAndCloseStopsTheService(t *testing.T) {
	l := localWith(t, &smi{gpus})
	e := &Engine{Local: l, StartTimeout: 80 * time.Millisecond, PollEvery: 10 * time.Millisecond}
	ctx := context.Background()
	if _, err := e.Open(ctx); err == nil {
		t.Fatal("nothing answers on the port: Open must give up")
	}
	if st := l.State(ctx, false); st.State != "running" || st.GPU == nil || *st.GPU != 1 {
		t.Fatalf("it runs on the card with most free memory: %+v", st)
	}
	e.Close()
	if _, running := l.Running(ctx); running {
		t.Error("the service is stopped when the run ends")
	}
}

func TestOpenNeedsRoomOnAGPU(t *testing.T) {
	l := localWith(t, &smi{"0, a, 7000, 12282\n1, b, 7500, 16311\n"})
	e := &Engine{Local: l}
	_, err := e.Open(context.Background())
	if err == nil {
		t.Fatal("nothing is started when no card has room")
	}
	if _, running := l.Running(context.Background()); running {
		t.Error("nothing is started")
	}
}

func TestExternalNeedsTheAcceptanceAndSaysWhatIsSent(t *testing.T) {
	e := &Engine{External: External{Host: "10.0.0.5:8788"}}
	a := e.Availability(context.Background())
	if a.Available || a.Reason != ReasonNoConsent || !strings.Contains(a.Detail, "lyrics and style") || a.Mode != ModeExternal {
		t.Errorf("%+v", a)
	}
}

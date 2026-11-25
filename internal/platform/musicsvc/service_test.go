package musicsvc

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"veritmo/internal/platform/runner"
)

// fakeHost installs a "service" that is a shell script standing in for dotnet: it writes the environment it got and waits.
func fakeHost(t *testing.T) (*ServiceHost, Local) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "services")
	if err := os.MkdirAll(filepath.Join(dir, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "demo", "demo.dll"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(root, "dotnet")
	script := "#!/bin/sh\nenv > \"$(dirname \"$1\")/env.txt\"\nexec sleep 60\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	h := &ServiceHost{Dir: dir, StateDir: filepath.Join(root, "state"), BinDir: filepath.Join(root, "bin"), Dotnet: fake}
	spec := Spec{Name: "Demo", Container: "vt-demo", Service: ServiceSpec{Dir: "demo", DLL: "demo.dll", Prefix: "DEMO", Device: true, Espeak: true},
		Missing: func(string) []string { return nil }}
	return h, Local{Spec: spec, Models: filepath.Join(root, "models"), Services: h}
}

func TestServiceStartStateStop(t *testing.T) {
	h, l := fakeHost(t)
	ctx := context.Background()
	if _, ok := l.Running(ctx); ok {
		t.Fatal("nothing runs yet")
	}
	if st := l.State(ctx, false); st.State != StateStopped {
		t.Fatalf("state = %q", st.State)
	}
	if err := l.Start(ctx, GPUInfo{Index: 1}, 4321); err != nil {
		t.Fatal(err)
	}
	defer l.Stop(ctx)

	port, ok := l.Running(ctx)
	if !ok || port != 4321 {
		t.Fatalf("Running = %d, %v", port, ok)
	}
	st := l.State(ctx, true)
	if st.State != StateRunning || st.GPU == nil || *st.GPU != 1 || st.Port != 4321 || st.Since == "" {
		t.Errorf("State = %+v", st)
	}
	var env string
	for i := 0; i < 50; i++ {
		b, err := os.ReadFile(filepath.Join(h.Dir, "demo", "env.txt"))
		if err == nil && len(b) > 0 {
			env = string(b)
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, want := range []string{"DEMO_CKPT=" + l.Models, "DEMO_PORT=4321", "DEMO_DEVICE=cuda:0", "CUDA_VISIBLE_DEVICES=1", "CUDA_DEVICE_ORDER=PCI_BUS_ID", "DEMO_ESPEAK_LIB=" + filepath.Join(h.BinDir, "libespeak-ng.so.1")} {
		if !strings.Contains(env, want+"\n") {
			t.Errorf("the service environment lacks %q", want)
		}
	}

	if err := l.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := l.Running(ctx); ok {
		t.Error("still running after Stop")
	}
	if err := l.Stop(ctx); err != nil {
		t.Errorf("stopping what is not running: %v", err)
	}
}

// A service that takes its devices by number (HeartMuLa) keeps every card visible: the model goes to the card chosen
// and the codec to the other card with most free memory.
func TestServiceWithCardsPutsTheCodecOnTheOtherCard(t *testing.T) {
	h, l := fakeHost(t)
	l.Spec.Service.Cards = true
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		return runner.ScriptedResult{Stdout: []byte("0, RTX 4070, 11000, 12282\n1, RTX 5060 Ti, 15800, 16311\n")}
	})
	l.Runner = fr
	ctx := context.Background()
	if err := l.Start(ctx, GPUInfo{Index: 0}, 4322); err != nil {
		t.Fatal(err)
	}
	defer l.Stop(ctx)
	var env string
	for i := 0; i < 50 && env == ""; i++ {
		if b, err := os.ReadFile(filepath.Join(h.Dir, "demo", "env.txt")); err == nil {
			env = string(b)
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, want := range []string{"Model__Device=cuda:0", "Codec__Device=cuda:1"} {
		if !strings.Contains(env, want+"\n") {
			t.Errorf("the environment lacks %q", want)
		}
	}
	if strings.Contains(env, "CUDA_VISIBLE_DEVICES") {
		t.Error("every card must stay visible")
	}
}

func TestServiceStateIgnoresAnotherProcess(t *testing.T) {
	h, l := fakeHost(t)
	if err := os.MkdirAll(h.StateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Our own pid, which is not the service: a reused pid must not be taken for it (nor killed).
	if err := os.WriteFile(h.statePath("vt-demo"), []byte(`{"pid":`+itoa(os.Getpid())+`,"port":1,"gpu":0}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := l.Running(context.Background()); ok {
		t.Error("another program was taken for the service")
	}
	if err := l.Stop(context.Background()); err != nil {
		t.Error(err)
	}
}

func TestServiceNotInstalled(t *testing.T) {
	h, l := fakeHost(t)
	_ = os.Remove(filepath.Join(h.Dir, "demo", "demo.dll"))
	e := &Engine{Spec: l.Spec, Local: l, NoStart: true}
	a := e.Availability(context.Background())
	if a.Available || a.Reason != ReasonNoService {
		t.Errorf("availability = %+v", a)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

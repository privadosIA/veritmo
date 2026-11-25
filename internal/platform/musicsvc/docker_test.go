package musicsvc

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"veritmo/internal/platform/runner"
)

// fakeDocker is a docker that answers like the real one for the commands the container backend uses, and remembers what was run.
type fakeDocker struct {
	mu       sync.Mutex
	daemon   bool
	runtimes string
	image    bool
	running  bool
	gpu      string
	calls    [][]string
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{daemon: true, runtimes: `{"nvidia":{},"runc":{}}`, image: true}
}

func (d *fakeDocker) handle(inv runner.Invocation) runner.ScriptedResult {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch inv.Name {
	case "nvidia-smi":
		return runner.ScriptedResult{Stdout: []byte("1, NVIDIA GeForce RTX 5060 Ti, 15900, 16311\n")}
	case "docker":
	default:
		return runner.ScriptedResult{Err: errors.New("unexpected program " + inv.Name)}
	}
	d.calls = append(d.calls, inv.Args)
	if !d.daemon {
		return runner.ScriptedResult{Err: errors.New("Cannot connect to the Docker daemon")}
	}
	switch inv.Args[0] {
	case "info":
		return runner.ScriptedResult{Stdout: []byte(d.runtimes)}
	case "image":
		if !d.image {
			return runner.ScriptedResult{Err: errors.New("No such image")}
		}
	case "run":
		d.running = true
		for i, a := range inv.Args {
			if a == "--gpus" {
				d.gpu = strings.TrimPrefix(inv.Args[i+1], "device=")
			}
		}
		return runner.ScriptedResult{Stdout: []byte("abc123\n")}
	case "rm":
		d.running, d.gpu = false, ""
	case "inspect":
		if !d.running {
			return runner.ScriptedResult{Err: errors.New("No such object")}
		}
		return runner.ScriptedResult{Stdout: []byte("running|2026-11-19T12:00:00Z|" + d.gpu + "\n")}
	}
	return runner.ScriptedResult{}
}

func (d *fakeDocker) ran(prefix ...string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.calls {
		if len(c) >= len(prefix) && strings.Join(c[:len(prefix)], " ") == strings.Join(prefix, " ") {
			return c
		}
	}
	return nil
}

func dockerEngine(t *testing.T, d *fakeDocker) *Engine {
	t.Helper()
	fr := runner.NewFakeRunner()
	fr.SetHandler(d.handle)
	spec := Spec{Name: "Demo", Container: "vt-demo", MinFreeMB: 8000, Missing: func(string) []string { return nil },
		Docker: DockerSpec{Image: "veritmo/demo:1", Port: 8799}}
	return &Engine{Spec: spec, Local: Local{Spec: spec, Runner: fr, Models: "/w/models/music/demo", Backend: BackendDocker}, NoStart: true}
}

// The container is run the way scripts/music-containers.sh runs it: loopback only, the weights read-only, one card, no restart policy.
func TestDocker_StartRunsTheContainerOnTheCardLoopbackAndReadOnly(t *testing.T) {
	d := newFakeDocker()
	l := dockerEngine(t, d).local()
	ctx := context.Background()
	if err := l.Start(ctx, GPUInfo{Index: 1}, 0); err != nil {
		t.Fatal(err)
	}
	run := strings.Join(d.ran("run"), " ")
	for _, want := range []string{"-d", "--name vt-demo", "--restart no", "--gpus device=1", "-p 127.0.0.1:8799:8799", "-v /w/models/music/demo:/ckpt:ro", "veritmo/demo:1"} {
		if !strings.Contains(run, want) {
			t.Errorf("docker run lacks %q: %s", want, run)
		}
	}
	if d.ran("rm", "-f", "vt-demo") == nil {
		t.Error("a container of the same name is removed before it is run")
	}
	port, ok := l.Running(ctx)
	if !ok || port != 8799 {
		t.Errorf("Running = %d, %v", port, ok)
	}
	st := l.State(ctx, false)
	if st.State != StateRunning || st.Port != 8799 || st.GPU == nil || *st.GPU != 1 || st.Since == "" {
		t.Errorf("State = %+v", st)
	}
	if err := l.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if st := l.State(ctx, false); st.State != StateStopped {
		t.Errorf("after Stop: %+v", st)
	}
}

func TestDocker_StartRefusesWithoutDockerOrImage(t *testing.T) {
	d := newFakeDocker()
	d.image = false
	if err := dockerEngine(t, d).local().Start(context.Background(), GPUInfo{}, 0); err == nil || !strings.Contains(err.Error(), "image") {
		t.Errorf("no image: %v", err)
	}
	if d.ran("run") != nil {
		t.Error("nothing is run without the image")
	}
	d = newFakeDocker()
	d.daemon = false
	if err := dockerEngine(t, d).local().Start(context.Background(), GPUInfo{}, 0); err == nil || !strings.Contains(err.Error(), "docker") {
		t.Errorf("no daemon: %v", err)
	}
}

// The availability says what to fix, in the order a person would: docker, the image, the NVIDIA runtime.
func TestDocker_AvailabilityReasons(t *testing.T) {
	ctx := context.Background()
	d := newFakeDocker()
	d.daemon = false
	if a := dockerEngine(t, d).Availability(ctx); a.Available || a.Reason != ReasonNoDocker {
		t.Errorf("no docker: %+v", a)
	}
	d = newFakeDocker()
	d.image = false
	if a := dockerEngine(t, d).Availability(ctx); a.Available || a.Reason != ReasonNoImage {
		t.Errorf("no image: %+v", a)
	}
	d = newFakeDocker()
	d.runtimes = `{"runc":{}}`
	if a := dockerEngine(t, d).Availability(ctx); a.Available || a.Reason != ReasonNoGPURuntime {
		t.Errorf("no runtime: %+v", a)
	}
	d = newFakeDocker()
	e := dockerEngine(t, d)
	a := e.Availability(ctx)
	if a.Available || a.Reason != ReasonNotRunning || a.Container == nil || a.Container.State != StateStopped {
		t.Errorf("stopped container: %+v", a)
	}
	if err := e.local().Start(ctx, GPUInfo{Index: 1}, 0); err != nil {
		t.Fatal(err)
	}
	a = e.Availability(ctx)
	if !a.Available || a.Container == nil || a.Container.State != StateRunning || a.Container.Port != 8799 {
		t.Errorf("running container: %+v", a)
	}
}

// The default backend is the .NET service: a generator whose backend is empty or dotnet never calls docker.
func TestDocker_DotnetBackendNeverCallsDocker(t *testing.T) {
	d := newFakeDocker()
	e := dockerEngine(t, d)
	e.Local.Backend = BackendDotnet
	_ = e.Availability(context.Background())
	if len(d.calls) != 0 {
		t.Errorf("docker was called: %v", d.calls)
	}
	if ValidBackend("") || !ValidBackend("docker") || !ValidBackend("dotnet") || ValidBackend("podman") {
		t.Error("ValidBackend")
	}
}

// Backends says, per way of running, what is missing in the order a person would fix it, and how much disk it takes.
func TestBackends_SayWhatEachWayLacks(t *testing.T) {
	ctx := context.Background()
	d := newFakeDocker()
	e := dockerEngine(t, d)
	e.Spec.Sizes = Sizes{Service: 5, Image: 14, Weights: 9}
	e.Local.Spec = e.Spec
	e.Local.Services = &ServiceHost{Dir: t.TempDir(), StateDir: t.TempDir()}
	e.Spec.Missing = func(string) []string { return []string{"a.safetensors"} }
	e.Local.Spec.Missing = e.Spec.Missing

	b := e.Backends(ctx)
	if dn := b[BackendDotnet]; dn.Available || strings.Join(dn.Missing, ",") != "service,weights" || dn.Reason != ReasonNoService || dn.NeedBytes != 14 {
		t.Errorf("dotnet with nothing: %+v", dn)
	}
	if dk := b[BackendDocker]; dk.Available || strings.Join(dk.Missing, ",") != "weights" || dk.Reason != ReasonNoWeights || dk.NeedBytes != 9 {
		t.Errorf("docker with the image: %+v", dk)
	}

	d.image = false
	d.runtimes = `{"runc":{}}`
	if dk := e.Backends(ctx)[BackendDocker]; strings.Join(dk.Missing, ",") != "image,weights,gpu_runtime" || dk.Reason != ReasonNoImage || dk.NeedBytes != 23 {
		t.Errorf("docker without the image or the runtime: %+v", dk)
	}

	d.daemon = false
	if dk := e.Backends(ctx)[BackendDocker]; dk.Available || dk.Missing[0] != MissingDocker || dk.Reason != ReasonNoDocker {
		t.Errorf("no docker: %+v", dk)
	}

	// everything there: both are available and nothing is missing
	d = newFakeDocker()
	e = dockerEngine(t, d)
	e.Local.Services = nil
	if dk := e.Backends(ctx)[BackendDocker]; !dk.Available || len(dk.Missing) != 0 || dk.NeedBytes != 0 {
		t.Errorf("docker complete: %+v", dk)
	}
	// a generator with no container has no backends
	e.Spec.Docker = DockerSpec{}
	if e.Backends(ctx) != nil {
		t.Error("an engine without a container answers nil")
	}
}

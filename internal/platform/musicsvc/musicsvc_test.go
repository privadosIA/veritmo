package musicsvc

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"veritmo/internal/platform/runner"
)

var testSpec = Spec{
	Name: "Other", Version: "3", Container: "vt-other", WeightsDir: "models/music/other",
	MinFreeMB: 8000, Missing: func(string) []string { return []string{"a"} }, Sends: "lyrics and a style",
}

// What the user reads names the generator, whichever one it is.
func TestMessagesNameTheGenerator(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer srv.Close()
	_, err := NewClient("Other", srv.URL, "").Health(context.Background())
	if !errors.Is(err, ErrUnauthorized) || !strings.Contains(err.Error(), "Other service") {
		t.Errorf("want ErrUnauthorized naming Other, got %v", err)
	}
	_, err = NewClient("Other", "127.0.0.1:1", "").Health(context.Background())
	if !errors.Is(err, ErrUnreachable) || !strings.Contains(err.Error(), "Other service") {
		t.Errorf("want ErrUnreachable naming Other, got %v", err)
	}
}

func TestPickGPUUsesTheMinimumOfTheSpec(t *testing.T) {
	gpus := []GPUInfo{{Index: 0, Name: "small", FreeMB: 6000, TotalMB: 12000}, {Index: 1, Name: "big", FreeMB: 9000, TotalMB: 16000}}
	g, err := PickGPU(testSpec, gpus, "")
	if err != nil || g.Index != 1 {
		t.Fatalf("%v %v", g, err)
	}
	_, err = PickGPU(testSpec, gpus[:1], "")
	if !errors.Is(err, ErrNoRoom) || !strings.Contains(err.Error(), "Other") {
		t.Errorf("want ErrNoRoom naming Other, got %v", err)
	}
}

func TestExternalConsentSaysWhatIsSent(t *testing.T) {
	e := &Engine{Spec: testSpec, External: External{Host: "10.0.0.5:8788"}}
	a := e.Availability(context.Background())
	if a.Available || a.Reason != ReasonNoConsent || !strings.Contains(a.Detail, "lyrics and a style") {
		t.Errorf("%+v", a)
	}
}

// testEngine is an engine whose service is the fake of fakeHost and whose only card is a 16 GB one.
func testEngine(t *testing.T) (*Engine, Local) {
	t.Helper()
	h, l := fakeHost(t)
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if inv.Name == "nvidia-smi" {
			return runner.ScriptedResult{Stdout: []byte("1, NVIDIA GeForce RTX 5060 Ti, 15900, 16311\n")}
		}
		return runner.ScriptedResult{}
	})
	spec := l.Spec
	spec.Version, spec.MinFreeMB, spec.Sends = "3", 8000, "lyrics and a style"
	l.Spec, l.Runner = spec, fr
	return &Engine{Spec: spec, Local: Local{Runner: fr, Models: l.Models, Services: h}}, l
}

// An engine that does not start the service says so instead, in the availability and when it is opened.
func TestNoStart_AStoppedServiceIsNotRunningNotAvailable(t *testing.T) {
	e, _ := testEngine(t)
	e.NoStart = true
	a := e.Availability(context.Background())
	if a.Available || a.Reason != ReasonNotRunning || a.Mode != ModeLocal || a.Container == nil || a.Container.State != StateStopped || !strings.Contains(a.Detail, "Environment") {
		t.Errorf("stopped: %+v", a)
	}
	if _, err := e.Open(context.Background()); err == nil {
		t.Fatalf("Open must fail: %v", err)
	} else if ue, ok := err.(*UnavailableError); !ok || ue.Reason != ReasonNotRunning {
		t.Errorf("the reason: %v", err)
	}
	if _, ok := e.local().Running(context.Background()); ok {
		t.Error("nothing is started")
	}
}

func TestNoStart_ARunningServiceIsAvailableAndIsUsed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ready":true,"version":"3","gpu":{"name":"x","free_mb":9000,"total_mb":16000}}`))
	}))
	defer srv.Close()
	addr := srv.Listener.Addr().String()
	port, _ := strconv.Atoi(addr[strings.LastIndex(addr, ":")+1:])
	e, l := testEngine(t)
	e.NoStart = true
	ctx := context.Background()
	if err := l.Start(ctx, GPUInfo{Index: 1}, port); err != nil {
		t.Fatal(err)
	}
	defer l.Stop(ctx)
	a := e.Availability(ctx)
	if !a.Available || a.Container == nil || a.Container.State != StateRunning || a.Container.Port != port {
		t.Errorf("running: %+v", a)
	}
	c, err := e.Open(ctx)
	if err != nil || c == nil {
		t.Fatalf("Open: %v", err)
	}
	e.Close()
	if _, ok := l.Running(ctx); !ok {
		t.Error("a service that was already up is left alone")
	}
}

// Without NoStart (the command line) a stopped service is still available: the run starts it.
func TestWithoutNoStart_AStoppedServiceIsAvailable(t *testing.T) {
	e, _ := testEngine(t)
	a := e.Availability(context.Background())
	if !a.Available || a.Reason != "" {
		t.Errorf("%+v", a)
	}
}

func TestAvailability_SaysWhatIsMissing(t *testing.T) {
	e, _ := testEngine(t)
	e.Local.Services = &ServiceHost{Dir: t.TempDir(), StateDir: t.TempDir()}
	if a := e.Availability(context.Background()); a.Available || a.Reason != ReasonNoService {
		t.Errorf("not installed: %+v", a)
	}
	e, _ = testEngine(t)
	e.Spec.Missing = func(string) []string { return []string{"a.safetensors"} }
	if a := e.Availability(context.Background()); a.Available || a.Reason != ReasonNoWeights || len(a.Missing) != 1 {
		t.Errorf("no weights: %+v", a)
	}
}

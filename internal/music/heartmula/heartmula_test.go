package heartmula

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/platform/runner"
)

// weights writes a complete weights folder in dir.
func weights(t *testing.T, dir string) {
	t.Helper()
	files := map[string]string{
		"config.json": "{}", "gen_config.json": "{}", "tokenizer.json": "{}",
		"HeartMuLa-oss-3B/config.json":                      "{}",
		"HeartMuLa-oss-3B/model.safetensors.index.json":     `{"weight_map": {"a": "model-00001-of-00002.safetensors", "b": "model-00002-of-00002.safetensors", "c": "model-00001-of-00002.safetensors"}}`,
		"HeartMuLa-oss-3B/model-00001-of-00002.safetensors": "x",
		"HeartMuLa-oss-3B/model-00002-of-00002.safetensors": "x",
		"HeartCodec-oss/config.json":                        "{}",
		"HeartCodec-oss/model.safetensors.index.json":       `{"weight_map": {"a": "model-00001-of-00001.safetensors"}}`,
		"HeartCodec-oss/model-00001-of-00001.safetensors":   "x",
	}
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMissing_ListsWhatIsNotThere(t *testing.T) {
	dir := t.TempDir()
	if m := Missing(dir); len(m) < 7 {
		t.Errorf("an empty folder lacks everything: %v", m)
	}
	weights(t, dir)
	if m := Missing(dir); len(m) != 0 {
		t.Errorf("a complete folder lacks nothing: %v", m)
	}
	// A download that stopped half way: one shard of the language model is not there.
	os.Remove(filepath.Join(dir, "HeartMuLa-oss-3B/model-00002-of-00002.safetensors"))
	m := Missing(dir)
	if len(m) != 1 || m[0] != "HeartMuLa-oss-3B/model-00002-of-00002.safetensors" {
		t.Errorf("the shard the index names must be missing: %v", m)
	}
	os.WriteFile(filepath.Join(dir, "HeartCodec-oss/model.safetensors.index.json"), []byte("not json"), 0o644)
	if m := Missing(dir); !strings.Contains(strings.Join(m, ","), "unreadable") {
		t.Errorf("an unreadable index is reported: %v", m)
	}
}

func TestPickGPU(t *testing.T) {
	gpus := []GPUInfo{{Index: 0, Name: "RTX 4070", FreeMB: 10600, TotalMB: 12282}, {Index: 1, Name: "RTX 5060 Ti", FreeMB: 15000, TotalMB: 16311}}
	if g, err := PickGPU(gpus, ""); err != nil || g.Index != 1 {
		t.Errorf("the card with most free memory: %+v %v", g, err)
	}
	if g, err := PickGPU(gpus, "0"); err != nil || g.Index != 0 {
		t.Errorf("the card asked for: %+v %v", g, err)
	}
	gpus[1].FreeMB = 9000
	if g, err := PickGPU(gpus, ""); err != nil || g.Index != 0 {
		t.Errorf("the only one with room: %+v %v", g, err)
	}
	gpus[0].FreeMB = 5000
	_, err := PickGPU(gpus, "")
	if !errors.Is(err, ErrNoRoom) || !strings.Contains(err.Error(), "GPU 0 (RTX 4070) has 4.9 GB free") || !strings.Contains(err.Error(), "GPU 1 (RTX 5060 Ti) has 8.8 GB free") {
		t.Errorf("the error says how much each has: %v", err)
	}
	if _, err := PickGPU(gpus, "7"); err == nil || !strings.Contains(err.Error(), "not there") {
		t.Errorf("a card that does not exist: %v", err)
	}
}

// serviceHost installs a service that is a shell script standing in for dotnet: it only waits.
func serviceHost(t *testing.T, dir, dll string) *musicsvc.ServiceHost {
	t.Helper()
	root := t.TempDir()
	services := filepath.Join(root, "services")
	if err := os.MkdirAll(filepath.Join(services, dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(services, dir, dll), []byte("x"), 0o644); err != nil {
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

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

const twoGPUs = "0, NVIDIA GeForce RTX 4070, 10700, 12282\n1, NVIDIA GeForce RTX 5060 Ti, 15900, 16311\n"

// localWith is the engine installed, with its weights, on a machine with the cards nvidia-smi says.
func localWith(t *testing.T, m *smi) Local {
	t.Helper()
	fr := runner.NewFakeRunner()
	fr.SetHandler(m.handler)
	dir := t.TempDir()
	weights(t, dir)
	return Local{Runner: fr, Models: dir, Services: serviceHost(t, "heartmula", "HeartMuLa.Net.Server.dll")}
}

func TestLocalReady_ReportsWhatIsMissingInOrder(t *testing.T) {
	m := &smi{twoGPUs}
	l := localWith(t, m)
	e := &Engine{Local: l}
	if a := e.Availability(context.Background()); !a.Available || a.Mode != ModeLocal {
		t.Fatalf("everything there: %+v", a)
	}
	notInstalled := &musicsvc.ServiceHost{Dir: t.TempDir(), StateDir: t.TempDir()}
	for name, tc := range map[string]struct {
		mut    func()
		reason string
	}{
		"no service": {func() { e.Local.Services = notInstalled }, ReasonNoService},
		"no weights": {func() { e.Local.Services = l.Services; os.Remove(filepath.Join(l.Models, "tokenizer.json")) }, ReasonNoWeights},
		"small gpu":  {func() { weights(t, l.Models); m.out = "0, Tiny, 4000, 6000\n" }, ReasonNoGPU},
	} {
		tc.mut()
		a := e.Availability(context.Background())
		if a.Available || a.Reason != tc.reason {
			t.Errorf("%s: %+v, want %s", name, a, tc.reason)
		}
		if tc.reason == ReasonNoWeights && len(a.Missing) != 1 {
			t.Errorf("the missing files are listed: %v", a.Missing)
		}
	}
}

// service is a fake HeartMuLa service.
func service(t *testing.T, token string, h Health) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/health":
			json.NewEncoder(w).Encode(h)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestExternalAvailability(t *testing.T) {
	ready := Health{Ready: true, Version: "1", GPU: &GPU{Name: "x", FreeMB: 15000}}
	srv := service(t, "tok", ready)
	e := &Engine{External: External{Host: srv.URL, Token: "tok", Consented: true}}
	if a := e.Availability(context.Background()); !a.Available || a.Mode != ModeExternal || a.Version != "1" {
		t.Errorf("a ready external service: %+v", a)
	}
	e.External.Token = "wrong"
	if a := e.Availability(context.Background()); a.Available || a.Reason != ReasonKeyRejected {
		t.Errorf("a wrong token: %+v", a)
	}
	e.External.Token, e.External.Consented = "tok", false
	if a := e.Availability(context.Background()); a.Available || a.Reason != ReasonNoConsent {
		t.Errorf("not accepted: %+v", a)
	}
	e.External = External{Host: "http://127.0.0.1:1", Consented: true}
	if a := e.Availability(context.Background()); a.Available || a.Reason != ReasonUnreachable {
		t.Errorf("nobody there: %+v", a)
	}
	notReady := service(t, "", Health{Ready: false, Missing: []string{"HeartCodec-oss"}})
	e.External = External{Host: notReady.URL, Consented: true}
	a := e.Availability(context.Background())
	if a.Available || a.Reason != ReasonUnreachable || !strings.Contains(a.Detail, "HeartCodec-oss") || !strings.Contains(a.Detail, "no GPU") {
		t.Errorf("a service without weights or GPU: %+v", a)
	}
}

func TestExternalWinsOverLocal(t *testing.T) {
	srv := service(t, "", Health{Ready: true, Version: "1"})
	l := localWith(t, &smi{twoGPUs})
	e := &Engine{Local: l, External: External{Host: srv.URL, Consented: true}}
	c, err := e.Open(context.Background())
	if err != nil || c.BaseURL != srv.URL {
		t.Fatalf("the external service is used: %v %v", c, err)
	}
	if _, running := l.Running(context.Background()); running {
		t.Error("no service is started when an external address is saved")
	}
	e.Close()
}

func portOf(t *testing.T, hostport string) int {
	t.Helper()
	i := strings.LastIndex(hostport, ":")
	var n int
	if err := json.Unmarshal([]byte(hostport[i+1:]), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A service that is already running and ready is used as it is, and left running when the run ends.
func TestOpenUsesARunningServiceAndLeavesItAlone(t *testing.T) {
	srv := service(t, "", Health{Ready: true, Version: "1", GPU: &GPU{Name: "x"}})
	l := localWith(t, &smi{twoGPUs})
	ctx := context.Background()
	port := portOf(t, srv.Listener.Addr().String())
	if err := l.Start(ctx, GPUInfo{Index: 1}, port); err != nil {
		t.Fatal(err)
	}
	defer l.Stop(ctx)
	e := &Engine{Local: l}
	c, err := e.Open(ctx)
	if err != nil || !strings.HasSuffix(c.BaseURL, ":"+itoa(port)) {
		t.Fatalf("a running service that answers is used: %v %v", c, err)
	}
	e.Close()
	if _, running := l.Running(ctx); !running {
		t.Error("a service this run did not start is left alone")
	}
}

func TestOpenPicksTheCardAndCloseStopsTheService(t *testing.T) {
	l := localWith(t, &smi{twoGPUs})
	e := &Engine{Local: l}
	StartTimeout, PollEvery = 80*time.Millisecond, 10*time.Millisecond
	defer func() { StartTimeout, PollEvery = 180*time.Second, time.Second }()
	ctx := context.Background()
	_, err := e.Open(ctx) // nothing will answer: it times out, but the service was started
	var ue *UnavailableError
	if !errors.As(err, &ue) || ue.Reason != ReasonUnreachable {
		t.Fatalf("a service that never answers: %v", err)
	}
	st := l.State(ctx, false)
	if st.State != "running" || st.GPU == nil || *st.GPU != 1 {
		t.Errorf("it runs on the card with most free memory: %+v", st)
	}
	e.Close()
	if _, running := l.Running(ctx); running {
		t.Error("the service is stopped when the run ends")
	}
}

func TestOpenNeedsRoomOnAGPU(t *testing.T) {
	m := &smi{"0, RTX 4070, 4000, 12282\n1, RTX 5060 Ti, 9000, 16311\n"}
	l := localWith(t, m)
	e := &Engine{Local: l}
	_, err := e.Open(context.Background())
	if !errors.Is(err, ErrNoRoom) {
		t.Errorf("nothing is started when no card has room: %v", err)
	}
	if _, running := l.Running(context.Background()); running {
		t.Error("nothing is started")
	}
}

func TestClientCalls(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		switch {
		case r.URL.Path == "/generate" && r.Method == http.MethodPost:
			var req Request
			json.NewDecoder(r.Body).Decode(&req)
			if req.Tags != "piano" || req.Seed != 7 || req.MaxMS != 120000 {
				http.Error(w, `{"error":"bad request"}`, 400)
				return
			}
			w.WriteHeader(202)
			w.Write([]byte(`{"id":"abc123"}`))
		case r.URL.Path == "/jobs/abc123":
			w.Write([]byte(`{"id":"abc123","state":"running","progress":{"done":10,"total":40},"error":"","seconds":2.5}`))
		case r.URL.Path == "/jobs/abc123/audio":
			w.Write([]byte("RIFFdata"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := NewClient(strings.TrimPrefix(srv.URL, "http://"), "tok")
	ctx := context.Background()
	id, err := c.Generate(ctx, Request{Lyrics: "x", Tags: "piano", Seed: 7, MaxMS: 120000})
	if err != nil || id != "abc123" || gotAuth != "Bearer tok" {
		t.Fatalf("generate: %q %v (auth %q)", id, err, gotAuth)
	}
	j, err := c.Job(ctx, id)
	if err != nil || j.State != "running" || j.Progress.Done != 10 || j.Progress.Total != 40 {
		t.Errorf("job: %+v %v", j, err)
	}
	var buf strings.Builder
	if err := c.Audio(ctx, id, &buf); err != nil || buf.String() != "RIFFdata" {
		t.Errorf("audio: %q %v", buf.String(), err)
	}
	if _, err := c.Generate(ctx, Request{Lyrics: "x", Tags: "other"}); err == nil || !strings.Contains(err.Error(), "bad request") {
		t.Errorf("the service's reason is kept: %v", err)
	}
	if err := (&Client{BaseURL: srv.URL}).Audio(ctx, "nope", &buf); err == nil {
		t.Error("a missing audio is an error")
	}
}

func TestBaseURL(t *testing.T) {
	for in, want := range map[string]string{"host:8787": "http://host:8787", "https://h/": "https://h", " 10.0.0.1:1 ": "http://10.0.0.1:1", "": ""} {
		if got := BaseURL(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

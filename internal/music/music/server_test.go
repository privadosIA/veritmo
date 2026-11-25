package music

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"veritmo/internal/project/config"
)

// fakeAce is an ace-server: it records what it is sent and answers the job API.
type fakeAce struct {
	mu       sync.Mutex
	jobs     map[string]*fakeJob
	next     int
	posts    []string          // path of each submission
	fields   map[string][]byte // multipart fields of the last synth
	requests [][]byte          // request JSON of each submission
	cancels  []string
	// failSynth makes synth jobs fail with this message.
	failSynth string
	// hold keeps jobs running until released.
	hold chan struct{}
}

type fakeJob struct {
	kind   string
	status string
	body   []byte
	ctype  string
}

func newFakeAce() *fakeAce { return &fakeAce{jobs: map[string]*fakeJob{}, fields: map[string][]byte{}} }

func (f *fakeAce) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"status":"ok"}`) })
	submit := func(kind string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			defer f.mu.Unlock()
			request := body
			if mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err == nil && strings.HasPrefix(mt, "multipart/") {
				mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
				f.fields = map[string][]byte{}
				for {
					p, err := mr.NextPart()
					if err != nil {
						break
					}
					data, _ := io.ReadAll(p)
					f.fields[p.FormName()] = data
				}
				request = f.fields["request"]
			}
			f.posts = append(f.posts, kind)
			f.requests = append(f.requests, request)
			f.next++
			id := fmt.Sprint(f.next)
			j := &fakeJob{kind: kind, status: "done"}
			switch {
			case f.hold != nil:
				j.status = "running"
			case kind == "lm":
				var req map[string]any
				json.Unmarshal(request, &req)
				req["audio_codes"] = "1,2,3"
				req["lyrics"] = "composed"
				j.body, _ = json.Marshal([]any{req})
				j.ctype = "application/json"
			case f.failSynth != "":
				j.status = "failed"
				j.body = []byte(`{"error":"` + f.failSynth + `"}`)
			default:
				var mb bytes.Buffer
				w := multipart.NewWriter(&mb)
				w.SetBoundary("ace-batch-boundary")
				h := map[string][]string{"Content-Type": {"audio/mpeg"}}
				p, _ := w.CreatePart(h)
				p.Write([]byte("MP3DATA"))
				h2 := map[string][]string{"Content-Type": {"application/octet-stream"}}
				p2, _ := w.CreatePart(h2)
				p2.Write([]byte("latents"))
				w.Close()
				j.body, j.ctype = mb.Bytes(), "multipart/mixed; boundary=ace-batch-boundary"
			}
			f.jobs[id] = j
			io.WriteString(w, `{"id":"`+id+`"}`)
		}
	}
	mux.HandleFunc("/lm", submit("lm"))
	mux.HandleFunc("/synth", submit("synth"))
	mux.HandleFunc("/job", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		id := r.URL.Query().Get("id")
		j, ok := f.jobs[id]
		if !ok {
			http.Error(w, `{"error":"no such job"}`, 404)
			return
		}
		if r.URL.Query().Get("cancel") == "1" {
			f.cancels = append(f.cancels, id)
			j.status = "cancelled"
			io.WriteString(w, `{"status":"cancelled"}`)
			return
		}
		if r.URL.Query().Get("result") == "1" {
			if j.ctype != "" {
				w.Header().Set("Content-Type", j.ctype)
			}
			w.Write(j.body)
			return
		}
		if f.hold != nil && j.status == "running" {
			select {
			case <-f.hold:
				j.status = "done"
			default:
			}
		}
		io.WriteString(w, `{"status":"`+j.status+`"}`)
	})
	return mux
}

func TestClient_ComposeReturnsTheEnrichedRequest(t *testing.T) {
	f := newFakeAce()
	srv := httptest.NewServer(f.Handler())
	defer srv.Close()
	c := &Client{Base: srv.URL}
	out, err := c.Compose(context.Background(), []byte(`{"caption":"jazz","seed":3}`))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil || got["caption"] != "jazz" || got["audio_codes"] != "1,2,3" {
		t.Fatalf("%v %s", err, out)
	}
}

func TestClient_SynthReturnsTheAudioPartAndSendsSourcesAsParts(t *testing.T) {
	f := newFakeAce()
	srv := httptest.NewServer(f.Handler())
	defer srv.Close()
	c := &Client{Base: srv.URL}

	audio, err := c.Synth(context.Background(), []byte(`{"caption":"x"}`), nil, nil)
	if err != nil || string(audio) != "MP3DATA" {
		t.Fatalf("%v %q", err, audio)
	}
	if len(f.fields) != 0 {
		t.Fatal("a plain request is plain JSON, not multipart")
	}

	src := &AudioFile{Name: "base.mp3", Data: []byte("SOURCE")}
	if _, err := c.Synth(context.Background(), []byte(`{"task_type":"cover"}`), src, src); err != nil {
		t.Fatal(err)
	}
	if string(f.fields["audio"]) != "SOURCE" || string(f.fields["ref_audio"]) != "SOURCE" || string(f.fields["request"]) != `{"task_type":"cover"}` {
		t.Fatalf("fields = %v", f.fields)
	}
	if _, err := c.Synth(context.Background(), []byte(`{}`), src, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.fields["ref_audio"]; ok {
		t.Fatal("no reference unless asked")
	}
}

func TestClient_AFailedJobCarriesTheServersMessage(t *testing.T) {
	f := newFakeAce()
	f.failSynth = "CUDA out of memory"
	srv := httptest.NewServer(f.Handler())
	defer srv.Close()
	_, err := (&Client{Base: srv.URL}).Synth(context.Background(), []byte(`{}`), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "failed") || !strings.Contains(err.Error(), "CUDA out of memory") {
		t.Fatalf("%v", err)
	}
}

func TestClient_CancellingTheContextCancelsTheJob(t *testing.T) {
	f := newFakeAce()
	f.hold = make(chan struct{})
	srv := httptest.NewServer(f.Handler())
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := (&Client{Base: srv.URL}).Synth(ctx, []byte(`{}`), nil, nil); done <- err }()
	time.Sleep(400 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not return")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.cancels) != 1 {
		t.Fatalf("cancel requests = %v", f.cancels)
	}
}

func TestClient_AServerThatAnswersGarbage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "not json") }))
	defer srv.Close()
	if _, err := (&Client{Base: srv.URL}).Compose(context.Background(), []byte(`{}`)); err == nil {
		t.Fatal("expected an error")
	}
}

// engineWith returns a ServerEngine whose "process" is the fake server.
func engineWith(t *testing.T, f *fakeAce, started *[]StartOptions) *ServerEngine {
	t.Helper()
	srv := httptest.NewServer(f.Handler())
	t.Cleanup(srv.Close)
	return NewServerEngine(ServerOptions{
		ModelsDir: "models/music", AdaptersDir: "models/music/adapters", KeepLoaded: true, Dir: t.TempDir(),
		Start: func(ctx context.Context, o StartOptions) (*Process, error) {
			*started = append(*started, o)
			return &Process{Base: srv.URL, stop: func() error { return nil }}, nil
		},
	})
}

func TestServerEngine_ComposeAndSynthWriteTheSong(t *testing.T) {
	f := newFakeAce()
	var started []StartOptions
	e := engineWith(t, f, &started)
	ws := Workspace{Base: t.TempDir()}
	env := []string{"CUDA_VISIBLE_DEVICES=1"}

	out, err := e.Compose(context.Background(), ws, "models/music", "tmp/w", Request{Caption: "jazz", Lyrics: "[Instrumental]", LMModel: "lm.gguf"}, env)
	if err != nil || !strings.Contains(string(out), `"audio_codes": "1,2,3"`) {
		t.Fatalf("%v %s", err, out)
	}
	if err := e.Synth(context.Background(), ws, "models/music", "models/music/adapters", "tmp/w", out, "mp3", "tmp/a.mp3", env, nil); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(ws.Path("tmp/a.mp3"))
	if err != nil || string(b) != "MP3DATA" {
		t.Fatalf("%v %q", err, b)
	}
	if len(started) != 1 || started[0].ModelsDir != "models/music" || !started[0].KeepLoaded {
		t.Fatalf("started = %+v: one server for the whole run", started)
	}
	if !e.Resident() {
		t.Fatal("keeping models loaded makes the engine resident")
	}
}

func TestServerEngine_OneServerPerGPUEnvironment(t *testing.T) {
	f := newFakeAce()
	var started []StartOptions
	e := engineWith(t, f, &started)
	ws := Workspace{Base: t.TempDir()}
	for _, env := range [][]string{{"CUDA_VISIBLE_DEVICES=0"}, {"CUDA_VISIBLE_DEVICES=1"}, {"CUDA_VISIBLE_DEVICES=0"}} {
		if err := e.Synth(context.Background(), ws, "m", "", "tmp/w", []byte(`{}`), "mp3", "tmp/a.mp3", env, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(started) != 2 || started[0].Env[0] != "CUDA_VISIBLE_DEVICES=0" || started[1].Env[0] != "CUDA_VISIBLE_DEVICES=1" {
		t.Fatalf("started = %+v", started)
	}
}

func TestServerEngine_TheAdaptersFolderIsPassedOnlyWhenItExists(t *testing.T) {
	f := newFakeAce()
	var started []StartOptions
	e := engineWith(t, f, &started)
	ws := Workspace{Base: e.opts.Dir}
	if err := e.Synth(context.Background(), ws, "m", "", "w", []byte(`{}`), "mp3", "a.mp3", nil, nil); err != nil {
		t.Fatal(err)
	}
	if started[0].AdaptersDir != "" {
		t.Fatalf("no folder, no flag: %q", started[0].AdaptersDir)
	}
	e.Close()

	var again []StartOptions
	e2 := engineWith(t, newFakeAce(), &again)
	e2.opts.Dir = ws.Base
	os.MkdirAll(filepath.Join(ws.Base, "models/music/adapters"), 0o755)
	if err := e2.Synth(context.Background(), ws, "m", "", "w", []byte(`{}`), "mp3", "a.mp3", nil, nil); err != nil {
		t.Fatal(err)
	}
	if again[0].AdaptersDir != "models/music/adapters" {
		t.Fatalf("%q", again[0].AdaptersDir)
	}
}

func TestServerEngine_ATaskSendsItsSourceAndReference(t *testing.T) {
	f := newFakeAce()
	var started []StartOptions
	e := engineWith(t, f, &started)
	ws := Workspace{Base: t.TempDir()}
	os.MkdirAll(ws.Path("sources"), 0o755)
	os.WriteFile(ws.Path("sources/base.mp3"), []byte("SOURCE"), 0o644)
	src := SourceOf(config.Track{Task: config.TaskCoverNoFSQ, Source: "base.mp3"})
	if err := e.Synth(context.Background(), ws, "m", "", "w", []byte(`{"task_type":"cover-nofsq"}`), "mp3", "a.mp3", nil, src); err != nil {
		t.Fatal(err)
	}
	if string(f.fields["audio"]) != "SOURCE" || string(f.fields["ref_audio"]) != "SOURCE" {
		t.Fatalf("%v", f.fields)
	}
	cover := SourceOf(config.Track{Task: config.TaskCover, Source: "base.mp3"})
	e.Synth(context.Background(), ws, "m", "", "w", []byte(`{}`), "mp3", "b.mp3", nil, cover)
	if _, ok := f.fields["ref_audio"]; ok || string(f.fields["audio"]) != "SOURCE" {
		t.Fatalf("a plain cover has no reference: %v", f.fields)
	}
	if err := e.Synth(context.Background(), ws, "m", "", "w", []byte(`{}`), "mp3", "c.mp3", nil, &SourceAudio{Path: "sources/gone.mp3"}); err == nil {
		t.Fatal("a missing source must fail")
	}
}

func TestServerEngine_CloseStopsTheServersOnceAndRefusesNewWork(t *testing.T) {
	stops := 0
	e := NewServerEngine(ServerOptions{Start: func(ctx context.Context, o StartOptions) (*Process, error) {
		srv := httptest.NewServer(newFakeAce().Handler())
		return &Process{Base: srv.URL, stop: func() error { stops++; srv.Close(); return nil }}, nil
	}})
	ws := Workspace{Base: t.TempDir()}
	if err := e.Synth(context.Background(), ws, "m", "", "w", []byte(`{}`), "mp3", "a.mp3", nil, nil); err != nil {
		t.Fatal(err)
	}
	e.Close()
	e.Close()
	if stops != 1 {
		t.Fatalf("stops = %d", stops)
	}
	if err := e.Synth(context.Background(), ws, "m", "", "w", []byte(`{}`), "mp3", "a.mp3", nil, nil); err == nil {
		t.Fatal("a closed engine must not start servers")
	}
}

func TestServerEngine_AServerThatCannotStartFailsThePhase(t *testing.T) {
	e := NewServerEngine(ServerOptions{Start: func(ctx context.Context, o StartOptions) (*Process, error) {
		return nil, fmt.Errorf("ace-server exited before it was ready:\nCUDA error")
	}})
	_, err := e.Compose(context.Background(), Workspace{Base: t.TempDir()}, "m", "w", Request{}, nil)
	if err == nil || !strings.Contains(err.Error(), "CUDA error") || !strings.HasPrefix(err.Error(), "compose:") {
		t.Fatalf("%v", err)
	}
}

// --- real process lifecycle, with this test binary playing ace-server --------

func TestMain(m *testing.M) {
	if os.Getenv("FAKE_ACE_SERVER") != "" {
		runFakeServerProcess()
		return
	}
	os.Exit(m.Run())
}

// runFakeServerProcess is the "ace-server" the lifecycle tests start: it
// parses --port, optionally dies at once, otherwise serves the fake API.
func runFakeServerProcess() {
	args := os.Args[1:]
	port := ""
	for i, a := range args {
		if a == "--port" {
			port = args[i+1]
		}
	}
	if os.Getenv("FAKE_ACE_SERVER") == "die" {
		fmt.Fprintln(os.Stderr, "[Server] FATAL: no models found")
		os.Exit(3)
	}
	if rec := os.Getenv("FAKE_ACE_ARGS"); rec != "" {
		os.WriteFile(rec, []byte(strings.Join(args, " ")+"\nENV="+os.Getenv("CUDA_VISIBLE_DEVICES")), 0o644)
	}
	http.ListenAndServe("127.0.0.1:"+port, newFakeAce().Handler())
}

// fakeBinary returns a script that re-runs the test binary as the fake server.
func fakeBinary(t *testing.T, mode string, extraEnv ...string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "ace-server")
	env := "FAKE_ACE_SERVER=" + mode
	for _, e := range extraEnv {
		env += " " + e
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec env "+env+" "+exe+" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestStartProcess_StartsWaitsForHealthAndStops(t *testing.T) {
	if _, err := exec.LookPath("env"); err != nil {
		t.Skip("no env")
	}
	rec := filepath.Join(t.TempDir(), "args")
	p, err := startProcess(context.Background(), StartOptions{
		Binary: fakeBinary(t, "ok", "FAKE_ACE_ARGS="+rec), ModelsDir: "models/music", AdaptersDir: "models/music/adapters",
		KeepLoaded: true, Dir: t.TempDir(), Env: []string{"CUDA_VISIBLE_DEVICES=1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Base, "http://127.0.0.1:") {
		t.Fatalf("must listen on loopback: %s", p.Base)
	}
	audio, err := (&Client{Base: p.Base}).Synth(context.Background(), []byte(`{}`), nil, nil)
	if err != nil || string(audio) != "MP3DATA" {
		t.Fatalf("%v %q", err, audio)
	}
	got, _ := os.ReadFile(rec)
	for _, want := range []string{"--models models/music", "--host 127.0.0.1", "--adapters models/music/adapters", "--keep-loaded", "ENV=1"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := http.Get(p.Base + "/health"); err == nil {
		t.Fatal("the server must be gone after Stop")
	}
	p.Stop() // twice is fine
}

func TestStartProcess_WithoutKeepLoadedOrAdaptersTheFlagsAreAbsent(t *testing.T) {
	rec := filepath.Join(t.TempDir(), "args")
	p, err := startProcess(context.Background(), StartOptions{Binary: fakeBinary(t, "ok", "FAKE_ACE_ARGS="+rec), ModelsDir: "m", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop()
	got, _ := os.ReadFile(rec)
	if strings.Contains(string(got), "--keep-loaded") || strings.Contains(string(got), "--adapters") {
		t.Fatalf("%s", got)
	}
}

func TestStartProcess_AServerThatDiesReportsItsOutput(t *testing.T) {
	_, err := startProcess(context.Background(), StartOptions{Binary: fakeBinary(t, "die"), ModelsDir: "m", Dir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "exited before it was ready") || !strings.Contains(err.Error(), "no models found") {
		t.Fatalf("%v", err)
	}
}

func TestStartProcess_AMissingProgram(t *testing.T) {
	_, err := startProcess(context.Background(), StartOptions{Binary: "/nonexistent/ace-server", ModelsDir: "m", Dir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "cannot start") {
		t.Fatalf("%v", err)
	}
}

func TestServerEngine_AFailureCarriesTheServerOutputAndAMemoryHint(t *testing.T) {
	f := newFakeAce()
	f.failSynth = "synth failed"
	srv := httptest.NewServer(f.Handler())
	defer srv.Close()
	for _, keep := range []bool{true, false} {
		e := NewServerEngine(ServerOptions{KeepLoaded: keep, Start: func(ctx context.Context, o StartOptions) (*Process, error) {
			return &Process{Base: srv.URL, stop: func() error { return nil }, log: func() string {
				return "[Load] loading\ncudaMalloc failed: out of memory\n[VAE-Enc] FATAL"
			}}, nil
		}})
		err := e.Synth(context.Background(), Workspace{Base: t.TempDir()}, "m", "", "w", []byte(`{}`), "mp3", "a.mp3", nil, nil)
		if err == nil || !strings.Contains(err.Error(), "ace-server output:") || !strings.Contains(err.Error(), "out of memory") {
			t.Fatalf("keep=%v: %v", keep, err)
		}
		if hint := strings.Contains(err.Error(), "keep_loaded: false"); hint != keep {
			t.Fatalf("keep=%v: the hint belongs to resident servers only: %v", keep, err)
		}
	}
}

func TestServerEngine_ACancelledRunAddsNoLog(t *testing.T) {
	e := NewServerEngine(ServerOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.withLog(ctx, nil, fmt.Errorf("boom")); err.Error() != "boom" {
		t.Fatalf("%v", err)
	}
}

func TestServerEngine_AReferenceIsSentWithoutASource(t *testing.T) {
	f := newFakeAce()
	var started []StartOptions
	e := engineWith(t, f, &started)
	ws := Workspace{Base: t.TempDir()}
	os.MkdirAll(ws.Path("sources"), 0o755)
	os.WriteFile(ws.Path("sources/sound.mp3"), []byte("TIMBRE"), 0o644)
	src := SourceOf(config.Track{Reference: "sound.mp3"})
	if err := e.Synth(context.Background(), ws, "m", "", "w", []byte(`{}`), "mp3", "a.mp3", nil, src); err != nil {
		t.Fatal(err)
	}
	if string(f.fields["ref_audio"]) != "TIMBRE" {
		t.Fatalf("%v", f.fields)
	}
	if _, ok := f.fields["audio"]; ok {
		t.Fatal("a reference is not a source")
	}
}

package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"veritmo/internal/music/music"
	"veritmo/internal/project/config"
)

// fakeAceServer is a minimal ace-server: it enriches /lm requests and answers
// /synth with an mp3 part, recording what it received.
type fakeAceServer struct {
	mu       sync.Mutex
	paths    []string
	synthReq []map[string]any
	audio    []byte // the "audio" part of the last multipart submission
	starts   int
	stops    int
	srv      *httptest.Server
}

func newFakeAceServer(t *testing.T) *fakeAceServer {
	t.Helper()
	f := &fakeAceServer{}
	jobs := map[string]func(w http.ResponseWriter){}
	mux := http.NewServeMux()
	next := 0
	submit := func(path string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			defer f.mu.Unlock()
			f.paths = append(f.paths, path)
			req := body
			if ct := r.Header.Get("Content-Type"); strings.HasPrefix(ct, "multipart/") {
				_, params, _ := mime.ParseMediaType(ct)
				mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
				for {
					p, err := mr.NextPart()
					if err != nil {
						break
					}
					data, _ := io.ReadAll(p)
					switch p.FormName() {
					case "request":
						req = data
					case "audio":
						f.audio = data
					}
				}
			}
			var doc map[string]any
			json.Unmarshal(req, &doc)
			next++
			id := string(rune('0' + next%10))
			if path == "/lm" {
				doc["audio_codes"] = "1,2,3"
				doc["lyrics"] = "composed"
				jobs[id] = func(w http.ResponseWriter) {
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode([]any{doc})
				}
			} else {
				f.synthReq = append(f.synthReq, doc)
				jobs[id] = func(w http.ResponseWriter) {
					var mb bytes.Buffer
					mw := multipart.NewWriter(&mb)
					mw.SetBoundary("b")
					p, _ := mw.CreatePart(map[string][]string{"Content-Type": {"audio/mpeg"}})
					p.Write(bytes.Repeat([]byte("a"), 16000))
					mw.Close()
					w.Header().Set("Content-Type", "multipart/mixed; boundary=b")
					w.Write(mb.Bytes())
				}
			}
			io.WriteString(w, `{"id":"`+id+`"}`)
		}
	}
	mux.HandleFunc("/lm", submit("/lm"))
	mux.HandleFunc("/synth", submit("/synth"))
	mux.HandleFunc("/job", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		j := jobs[r.URL.Query().Get("id")]
		f.mu.Unlock()
		if r.URL.Query().Get("result") == "1" {
			j(w)
			return
		}
		io.WriteString(w, `{"status":"done"}`)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	serverStartHook = func(ctx context.Context, o music.StartOptions) (*music.Process, error) {
		f.mu.Lock()
		f.starts++
		f.mu.Unlock()
		return music.NewTestProcess(f.srv.URL, func() error { f.mu.Lock(); f.stops++; f.mu.Unlock(); return nil }), nil
	}
	t.Cleanup(func() { serverStartHook = nil })
	return f
}

func serverConfig(tracks ...config.Track) *config.Config {
	c := musicConfig(tracks...)
	c.Defaults.Music = &config.Music{Engine: config.EngineServer}
	return c
}

func TestMusicServer_ComposeAndSynthGoThroughTheServerAndNoProgramRuns(t *testing.T) {
	env := newMusicEnv(t)
	f := newFakeAceServer(t)
	seed := 5
	cfg := serverConfig(config.Track{ID: "a", Prompt: "jazz", Seed: &seed})
	if _, _, err := env.run(t, cfg, Options{}); err != nil {
		t.Fatal(err)
	}
	if len(engineCalls(env.fr)) != 0 {
		t.Fatalf("no program may run with the server engine: %v", engineCalls(env.fr))
	}
	if strings.Join(f.paths, ",") != "/lm,/synth" {
		t.Fatalf("jobs = %v", f.paths)
	}
	if f.starts != 1 || f.stops != 1 {
		t.Fatalf("one server, stopped at the end: starts %d stops %d", f.starts, f.stops)
	}
	m := readMusicManifest(t, env.dir)
	if len(m.Tracks) != 1 || m.Tracks[0].LyricsSung != "composed" {
		t.Fatalf("%+v", m.Tracks)
	}
	if _, err := os.Stat(filepath.Join(env.dir, m.Tracks[0].File)); err != nil {
		t.Fatal("the song must be exported")
	}
}

func TestMusicServer_SwitchingEnginesKeepsTheCache(t *testing.T) {
	env := newMusicEnv(t)
	f := newFakeAceServer(t)
	seed := 5
	cli := musicConfig(config.Track{ID: "a", Prompt: "jazz", Seed: &seed})
	if _, _, err := env.run(t, cli, Options{}); err != nil {
		t.Fatal(err)
	}
	env.fr = newMusicEnv(t).fr
	srv := serverConfig(config.Track{ID: "a", Prompt: "jazz", Seed: &seed})
	if _, _, err := env.run(t, srv, Options{}); err != nil {
		t.Fatal(err)
	}
	if len(f.paths) != 0 || f.starts != 0 {
		t.Fatalf("a cache hit must not even start the server: %v starts %d", f.paths, f.starts)
	}
}

func TestMusicServer_ACoverSendsItsSource(t *testing.T) {
	env := newMusicEnv(t)
	f := newFakeAceServer(t)
	installSource(t, env, "base.mp3", "ORIGINAL")
	seed := 5
	cfg := serverConfig(config.Track{ID: "c", Prompt: "jazz", Seed: &seed, Task: config.TaskCover, Source: "base.mp3"})
	if _, _, err := env.run(t, cfg, Options{}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.paths, ",") != "/synth" {
		t.Fatalf("a cover never composes: %v", f.paths)
	}
	if string(f.audio) != "ORIGINAL" {
		t.Fatalf("audio part = %q", f.audio)
	}
	if f.synthReq[0]["task_type"] != "cover" {
		t.Fatalf("%v", f.synthReq[0])
	}
}

func TestMusicServer_TwoGPUsStartOneServerEach(t *testing.T) {
	env := newMusicEnv(t)
	f := newFakeAceServer(t)
	a, b := 1, 2
	cfg := serverConfig(config.Track{ID: "a", Prompt: "x", Lyrics: "la", Seed: &a}, config.Track{ID: "b", Prompt: "y", Lyrics: "lo", Seed: &b})
	if _, _, err := env.run(t, cfg, Options{GPU: "0,1", MemAvailable: func() (int64, error) { return 64 << 30, nil }}); err != nil {
		t.Fatal(err)
	}
	if f.starts != 2 || f.stops != 2 {
		t.Fatalf("starts %d stops %d", f.starts, f.stops)
	}
}

func TestMusicServer_PreflightNeedsOnlyTheServerProgram(t *testing.T) {
	cfg := serverConfig(config.Track{ID: "a", Prompt: "x"})
	cfg.Defaults.Music.Engine = config.EngineServer
	got := strings.Join(RequiredBinariesFor(cfg, ""), ",")
	if got != "ace-server" {
		t.Fatalf("required = %s", got)
	}
	cfg.Defaults.Music.Engine = ""
	if got := strings.Join(RequiredBinariesFor(cfg, ""), ","); got != "ace-synth,ace-lm" {
		t.Fatalf("programs engine = %s", got)
	}
	video := &config.Config{Output: config.Output{Mode: config.ModeVideo}, BGM: &config.BGM{Prompt: "p"}, Defaults: config.Defaults{Music: &config.Music{Engine: config.EngineServer}}}
	has := func(c *config.Config, name string) bool {
		for _, b := range RequiredBinariesFor(c, "") {
			if b == name {
				return true
			}
		}
		return false
	}
	if !has(video, "ace-server") || has(video, "ace-synth") {
		t.Fatal("a generated soundtrack through the server needs ace-server only")
	}
}

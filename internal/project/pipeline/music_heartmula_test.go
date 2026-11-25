package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"veritmo/internal/music/heartmula"
	"veritmo/internal/project/config"
)

// fakeService is a HeartMuLa service that records the requests it gets and answers every job as done.
type fakeService struct {
	mu       sync.Mutex
	requests []heartmula.Request
	fail     bool
	forgot   int
}

func (f *fakeService) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/generate":
		var req heartmula.Request
		json.NewDecoder(r.Body).Decode(&req)
		f.requests = append(f.requests, req)
		w.WriteHeader(202)
		w.Write([]byte(`{"id":"j` + itoa(int64(len(f.requests))) + `"}`))
	case strings.HasSuffix(r.URL.Path, "/audio"):
		w.Write([]byte("RIFFwav"))
	case r.Method == http.MethodDelete:
		f.forgot++
		w.Write([]byte(`{"ok":true}`))
	case strings.HasPrefix(r.URL.Path, "/jobs/"):
		if f.fail {
			w.Write([]byte(`{"state":"failed","error":"CUDA out of memory"}`))
			return
		}
		w.Write([]byte(`{"state":"done","progress":{"done":5,"total":5}}`))
	}
}

// useFakeService points the pipeline at a fake service for the length of the test.
func useFakeService(t *testing.T) *fakeService {
	t.Helper()
	f := &fakeService{}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	heartMulaOpenHook = func(context.Context) (*heartmula.Client, error) {
		return heartmula.NewClient(srv.URL, ""), nil
	}
	t.Cleanup(func() { heartMulaOpenHook = nil })
	return f
}

func heartMulaProject(tracks ...config.Track) *config.Config {
	c := musicConfig(tracks...)
	c.Defaults.Music = &config.Music{Generator: config.GeneratorHeartMuLa}
	return c
}

var weddingTrack = config.Track{ID: "boda", Prompt: "romantic wedding song", Lyrics: "[Verse]\nIn the glow", Tags: "piano,happy"}

func TestHeartMula_SongsAreMadeByTheServiceAndGoThroughTheUsualExport(t *testing.T) {
	svc := useFakeService(t)
	env := newMusicEnv(t)
	d := 90.0
	tr := weddingTrack
	tr.DurationSeconds = &d
	out, evs, err := env.run(t, heartMulaProject(tr), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := engineCalls(env.fr); len(got) != 0 {
		t.Errorf("no ACE-Step program runs: %v", got)
	}
	if got := strings.Join(phases(evs, "boda"), ","); got != "phase_start:synth,phase_end:synth,phase_start:export,phase_end:export" {
		t.Errorf("no compose phase: %s", got)
	}
	if len(svc.requests) != 1 {
		t.Fatalf("requests = %+v", svc.requests)
	}
	r := svc.requests[0]
	if r.Lyrics != "[Verse]\nIn the glow" || r.Tags != "piano,happy" || r.Seed != 1001 || r.MaxMS != 105000 {
		t.Errorf("the request carries the lyrics, the tags, the seed and the longest length: %+v", r)
	}
	if svc.forgot != 1 {
		t.Errorf("the job is dropped on the service: %d", svc.forgot)
	}
	if out != filepath.Join("out", "songs", "manifest.json") {
		t.Errorf("artifact = %s", out)
	}
	if _, err := os.Stat(filepath.Join(env.dir, "out", "songs", "boda.mp3")); err != nil {
		t.Errorf("the song is exported as an mp3: %v", err)
	}
	e := readMusicManifest(t, env.dir).Tracks[0]
	if e.SynthModel != "heartmula" || e.UseLM || e.LMModel != "" || e.EffectiveSeed != 1001 || e.LyricsSung != "[Verse]\nIn the glow" {
		t.Errorf("entry = %+v", e)
	}
	if e.Check != nil {
		for _, issue := range e.Check.Issues {
			if issue.Code == "duration" {
				t.Errorf("the duration asked for is only a limit: no duration check (%+v)", issue)
			}
		}
	}
}

func TestHeartMula_VariantsAreOneRequestEachWithConsecutiveSeeds(t *testing.T) {
	svc := useFakeService(t)
	env := newMusicEnv(t)
	n := 2
	tr := weddingTrack
	tr.Variants = &n
	if _, _, err := env.run(t, heartMulaProject(tr), Options{}); err != nil {
		t.Fatal(err)
	}
	if len(svc.requests) != 2 || svc.requests[0].Seed != 1001 || svc.requests[1].Seed != 1002 {
		t.Errorf("requests = %+v", svc.requests)
	}
	for _, f := range []string{"boda_v1.mp3", "boda_v2.mp3"} {
		if _, err := os.Stat(filepath.Join(env.dir, "out", "songs", f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}

func TestHeartMula_TheCacheDependsOnWhatMakesTheSong(t *testing.T) {
	svc := useFakeService(t)
	env := newMusicEnv(t)
	cfg := heartMulaProject(weddingTrack)
	if _, _, err := env.run(t, cfg, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, evs, err := env.run(t, heartMulaProject(weddingTrack), Options{}); err != nil || len(svc.requests) != 1 {
		t.Fatalf("an unchanged run is cached: %d requests, %v (%s)", len(svc.requests), err, strings.Join(phases(evs, "boda"), ","))
	}
	changed := weddingTrack
	changed.Tags = "piano,sad"
	if _, _, err := env.run(t, heartMulaProject(changed), Options{}); err != nil || len(svc.requests) != 2 {
		t.Fatalf("other tags make the song again: %d requests, %v", len(svc.requests), err)
	}
	moved := changed
	moved.Lyrics = "[Verse]\nOther words"
	if _, _, err := env.run(t, heartMulaProject(moved), Options{}); err != nil || len(svc.requests) != 3 {
		t.Fatalf("other lyrics make the song again: %d requests, %v", len(svc.requests), err)
	}
}

func TestHeartMula_ATrackWithoutTagsFailsNamingIt(t *testing.T) {
	useFakeService(t)
	env := newMusicEnv(t)
	tr := weddingTrack
	tr.Tags = ""
	_, evs, err := env.run(t, heartMulaProject(tr), Options{})
	if err == nil || !strings.Contains(err.Error(), "tags") {
		t.Fatalf("err = %v", err)
	}
	var named bool
	for _, ev := range evs {
		if ev.SceneID == "boda" && strings.Contains(ev.Message, "needs tags") {
			named = true
		}
	}
	if !named {
		t.Error("the error event names the track and says what is missing")
	}
}

func TestHeartMula_AFailedSongStopsTheRunWithTheServicesReason(t *testing.T) {
	svc := useFakeService(t)
	svc.fail = true
	env := newMusicEnv(t)
	_, evs, err := env.run(t, heartMulaProject(weddingTrack), Options{})
	if err == nil || !strings.Contains(err.Error(), "out of memory") {
		t.Fatalf("err = %v", err)
	}
	var reported bool
	for _, ev := range evs {
		if ev.SceneID == "boda" && strings.Contains(ev.Message, "CUDA out of memory") {
			reported = true
		}
	}
	if !reported {
		t.Error("the reason reaches the error event")
	}
}

func TestHeartMula_ACEStepProjectsAreUnchanged(t *testing.T) {
	useFakeService(t) // set, but a project without the generator must not touch it
	env := newMusicEnv(t)
	cfg := musicConfig(config.Track{ID: "a", Prompt: "folk", Lyrics: "[Verse]\nmy words"})
	if _, _, err := env.run(t, cfg, Options{}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(engineCalls(env.fr), ","); got != "ace-synth" {
		t.Errorf("ACE-Step runs as before: %s", got)
	}
}

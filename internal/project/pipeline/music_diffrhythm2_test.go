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

	"veritmo/internal/music/diffrhythm2"
	"veritmo/internal/project/config"
)

// fakeDR2 is a DiffRhythm2 service that records the requests it gets and answers every job as done.
type fakeDR2 struct {
	mu       sync.Mutex
	requests []diffrhythm2.Request
	fail     bool
	forgot   int
}

func (f *fakeDR2) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/generate":
		var req diffrhythm2.Request
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
		w.Write([]byte(`{"state":"done","progress":{"done":1,"total":1}}`))
	}
}

func useFakeDR2(t *testing.T) *fakeDR2 {
	t.Helper()
	f := &fakeDR2{}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	diffRhythm2OpenHook = func(context.Context) (*diffrhythm2.Client, error) {
		return diffrhythm2.NewClient(srv.URL, ""), nil
	}
	t.Cleanup(func() { diffRhythm2OpenHook = nil })
	return f
}

func dr2Project(tracks ...config.Track) *config.Config {
	c := musicConfig(tracks...)
	c.Defaults.Music = &config.Music{Generator: config.GeneratorDiffRhythm2}
	return c
}

var dr2Track = config.Track{ID: "boda", Prompt: "romantic wedding song", Lyrics: "[Verse]\nIn the glow of the evening light", Tags: "pop,piano,warm"}

func TestDiffRhythm2_SongsAreMadeByTheServiceAndGoThroughTheUsualExport(t *testing.T) {
	svc := useFakeDR2(t)
	env := newMusicEnv(t)
	d := 90.0
	tr := dr2Track
	tr.DurationSeconds = &d
	out, evs, err := env.run(t, dr2Project(tr), Options{})
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
	if r.Lyrics != "[Verse]\nIn the glow of the evening light" || r.Style != "pop, piano, warm" || r.Seed != 1001 || r.MaxSecs != 105 {
		t.Errorf("the request carries the lyrics, the style, the seed and the longest length: %+v", r)
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
	if e.SynthModel != "diffrhythm2" || e.UseLM || e.LMModel != "" || e.EffectiveSeed != 1001 {
		t.Errorf("entry = %+v", e)
	}
}

func TestDiffRhythm2_TheGuidanceAndTheStepsReachTheService(t *testing.T) {
	svc := useFakeDR2(t)
	env := newMusicEnv(t)
	cfg := dr2Project(dr2Track)
	g, n := 3.0, 24
	cfg.Defaults.Music.DR2CfgStrength, cfg.Defaults.Music.DR2Steps = &g, &n
	if _, _, err := env.run(t, cfg, Options{}); err != nil {
		t.Fatal(err)
	}
	if r := svc.requests[0]; r.CfgStrength != 3 || r.Steps != 24 {
		t.Errorf("request = %+v", r)
	}
}

func TestDiffRhythm2_TheCacheDependsOnWhatMakesTheSong(t *testing.T) {
	svc := useFakeDR2(t)
	env := newMusicEnv(t)
	if _, _, err := env.run(t, dr2Project(dr2Track), Options{}); err != nil {
		t.Fatal(err)
	}
	if _, evs, err := env.run(t, dr2Project(dr2Track), Options{}); err != nil || len(svc.requests) != 1 {
		t.Fatalf("an unchanged run is cached: %d requests, %v (%s)", len(svc.requests), err, strings.Join(phases(evs, "boda"), ","))
	}
	changed := dr2Track
	changed.Tags = "rock,drums"
	if _, _, err := env.run(t, dr2Project(changed), Options{}); err != nil || len(svc.requests) != 2 {
		t.Fatalf("other tags make the song again: %d requests, %v", len(svc.requests), err)
	}
	cfg := dr2Project(changed)
	n := 24
	cfg.Defaults.Music.DR2Steps = &n
	if _, _, err := env.run(t, cfg, Options{}); err != nil || len(svc.requests) != 3 {
		t.Fatalf("other steps make the song again: %d requests, %v", len(svc.requests), err)
	}
}

func TestDiffRhythm2_ATrackWithoutTagsFailsNamingIt(t *testing.T) {
	useFakeDR2(t)
	env := newMusicEnv(t)
	tr := dr2Track
	tr.Tags = ""
	_, evs, err := env.run(t, dr2Project(tr), Options{})
	if err == nil || !strings.Contains(err.Error(), "tags") {
		t.Fatalf("err = %v", err)
	}
	var named bool
	for _, ev := range evs {
		if ev.SceneID == "boda" && strings.Contains(ev.Message, "diffrhythm2 generator needs tags") {
			named = true
		}
	}
	if !named {
		t.Error("the error event names the track and the generator")
	}
}

func TestDiffRhythm2_SpanishLyricsAreRefusedBeforeAnySongIsMade(t *testing.T) {
	svc := useFakeDR2(t)
	env := newMusicEnv(t)
	tr := dr2Track
	tr.Lyrics = "[Verse]\nCamino por la calle vacía esta noche"
	tr.VocalLanguage = "es"
	_, evs, err := env.run(t, dr2Project(tr), Options{})
	if err == nil || len(svc.requests) != 0 {
		t.Fatalf("err = %v, requests = %d", err, len(svc.requests))
	}
	var said bool
	for _, ev := range evs {
		if ev.SceneID == "boda" && strings.Contains(ev.Message, "English lyrics only") {
			said = true
		}
	}
	if !said {
		t.Error("the error event says that this generator reads English only")
	}
}

func TestDiffRhythm2_AFailedSongStopsTheRunWithTheServicesReason(t *testing.T) {
	svc := useFakeDR2(t)
	svc.fail = true
	env := newMusicEnv(t)
	_, _, err := env.run(t, dr2Project(dr2Track), Options{})
	if err == nil || !strings.Contains(err.Error(), "out of memory") {
		t.Fatalf("err = %v", err)
	}
}

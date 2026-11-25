package music

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/music/diffrhythm2"
	"veritmo/internal/project/config"
)

func TestBuildRequest_DiffRhythm2CarriesTheStyleNotTheAceStepFields(t *testing.T) {
	g, n := 3.0, 24
	cfg := &config.Config{Defaults: config.Defaults{Music: &config.Music{Generator: config.GeneratorDiffRhythm2, DR2CfgStrength: &g, DR2Steps: &n}}}
	d := 60.0
	req := BuildRequest(cfg, config.Track{ID: "a", Prompt: "p", Lyrics: "[Verse]\nhello", Tags: "pop,piano", DurationSeconds: &d}, 5)
	if req.SynthModel != DiffRhythm2SynthModel || req.Style != "pop, piano" || req.Tags != "" || req.LMModel != "" {
		t.Errorf("%+v", req)
	}
	if req.DR2CfgStrength == nil || *req.DR2CfgStrength != 3 || req.DR2Steps == nil || *req.DR2Steps != 24 {
		t.Errorf("guidance and steps: %+v", req)
	}
	// another generator's request does not carry them: its cache keys stay as they were
	ace := BuildRequest(&config.Config{Defaults: config.Defaults{Music: &config.Music{}}}, config.Track{ID: "a", Prompt: "p"}, 5)
	b, _ := json.Marshal(ace)
	if strings.Contains(string(b), "diffrhythm2") {
		t.Errorf("an ACE-Step request does not mention the other generator: %s", b)
	}
}

func TestDiffRhythm2Engine_MakesASongThroughTheService(t *testing.T) {
	var got diffrhythm2.Request
	forgot := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/generate":
			json.NewDecoder(r.Body).Decode(&got)
			w.WriteHeader(202)
			w.Write([]byte(`{"id":"j1"}`))
		case strings.HasSuffix(r.URL.Path, "/audio"):
			w.Write([]byte("RIFFwav"))
		case r.Method == http.MethodDelete:
			forgot++
			w.Write([]byte(`{"ok":true}`))
		default:
			w.Write([]byte(`{"state":"done"}`))
		}
	}))
	defer srv.Close()
	e := &DiffRhythm2Engine{Open: func(context.Context) (*diffrhythm2.Client, error) { return diffrhythm2.NewClient(srv.URL, ""), nil }}
	dir := t.TempDir()
	seed := int64(9)
	d := 100.0
	req, _ := json.Marshal(Request{Lyrics: "[start]\nhello", Style: "pop, piano", Seed: &seed, Duration: d})
	if err := e.Synth(context.Background(), Workspace{Base: dir}, "", "", "", req, "wav", "out/a.wav", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got.Style != "pop, piano" || got.Seed != 9 || got.MaxSecs != 115 || got.Lyrics != "[start]\nhello" {
		t.Errorf("request = %+v", got)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "out", "a.wav")); err != nil || string(b) != "RIFFwav" {
		t.Errorf("the WAV is written: %v %q", err, b)
	}
	if forgot != 1 {
		t.Errorf("the job is forgotten: %d", forgot)
	}
	if !e.Resident() {
		t.Error("the service holds its own memory")
	}
}

func TestDiffRhythm2Engine_RefusesWhatItCannotMake(t *testing.T) {
	e := &DiffRhythm2Engine{Open: func(context.Context) (*diffrhythm2.Client, error) {
		return diffrhythm2.NewClient("127.0.0.1:1", ""), nil
	}}
	ws := Workspace{Base: t.TempDir()}
	req, _ := json.Marshal(Request{Lyrics: "hello", Style: "pop"})
	if err := e.Synth(context.Background(), ws, "", "", "", req, "wav", "a.wav", nil, &SourceAudio{Path: "sources/x.mp3"}); err == nil {
		t.Error("a task has a source: not for this engine")
	}
	if err := e.Synth(context.Background(), ws, "", "", "", req, "mp3", "a.mp3", nil, nil); err == nil {
		t.Error("it writes WAV")
	}
	noStyle, _ := json.Marshal(Request{Lyrics: "hello"})
	if err := e.Synth(context.Background(), ws, "", "", "", noStyle, "wav", "a.wav", nil, nil); err == nil || !strings.Contains(err.Error(), "tags") {
		t.Errorf("a track with no tags: %v", err)
	}
	if _, err := e.Compose(context.Background(), ws, "", "", Request{}, nil); err == nil {
		t.Error("it has no compose phase")
	}
}

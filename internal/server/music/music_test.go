package music

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/music/music"
	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
)

const musicYAML = `project: songs
output: {mode: music}
tracks:
  - id: ballad
    prompt: Spanish pop ballad
    lyrics: "[Verse]\nhola"
    seed: 7
  - id: idea
    prompt: lofi beat
`

// installMusicModels puts stub engine models where a project's defaults look.
func installMusicModels(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join("models", "music"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{config.DefaultMusicLM, config.DefaultMusicSynth, "Qwen3-Embedding-0.6B-Q8_0.gguf", "vae-BF16.gguf"} {
		if err := os.WriteFile(filepath.Join("models", "music", f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func putConfig(t *testing.T, s *testServer, id, yaml string) *httptest.ResponseRecorder {
	t.Helper()
	return doJSON(t, s.Handler(), http.MethodPut, "/api/projects/"+id+"/config", map[string]string{"yaml": yaml})
}

func TestMusicProject_SavesAndIsListedWithItsModeAndTracks(t *testing.T) {
	s, _ := newProjectsServer(t)
	installMusicModels(t)
	createProject(t, s, "songs")
	if rec := putConfig(t, s, "songs", musicYAML); rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/projects", nil)
	var list []projectSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	for _, p := range list {
		if p.ID == "songs" {
			if p.Mode != "music" || p.TrackCount != 2 || p.SceneCount != 0 || p.Invalid {
				t.Fatalf("summary = %+v", p)
			}
			return
		}
	}
	t.Fatal("music project not listed")
}

func TestMusicProject_MissingModelsAreReportedWithAWayToFixThem(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "songs")
	before, _ := os.ReadFile(filepath.Join(projects, "songs", "scenes.yaml"))
	rec := putConfig(t, s, "songs", musicYAML)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	for _, frag := range []string{config.DefaultMusicSynth, "Music tab", "text encoder"} {
		if !strings.Contains(rec.Body.String(), frag) {
			t.Errorf("message should mention %q: %s", frag, rec.Body)
		}
	}
	after, _ := os.ReadFile(filepath.Join(projects, "songs", "scenes.yaml"))
	if string(before) != string(after) {
		t.Error("an invalid save must leave the file untouched")
	}
}

func TestMusicProject_ScenesAreRejected(t *testing.T) {
	s, _ := newProjectsServer(t)
	installMusicModels(t)
	createProject(t, s, "songs")
	bad := musicYAML + "scenes:\n  - {id: s, prompt: p, tts_text: x}\n"
	rec := putConfig(t, s, "songs", bad)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "uses tracks, not scenes") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestMusicProject_CacheReportPerTrackPhases(t *testing.T) {
	s, projects := newProjectsServer(t)
	installMusicModels(t)
	createProject(t, s, "songs")
	if rec := putConfig(t, s, "songs", musicYAML); rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	get := func() map[string]map[string]string {
		rec := doJSON(t, s.Handler(), http.MethodGet, "/api/projects/songs/cache", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("cache: %d %s", rec.Code, rec.Body)
		}
		var rep cacheReport
		if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
			t.Fatal(err)
		}
		if rep.Mode != "music" || len(rep.Scenes) != 2 {
			t.Fatalf("report = %+v", rep)
		}
		out := map[string]map[string]string{}
		for _, sc := range rep.Scenes {
			out[sc.SceneID] = sc.Phases
		}
		return out
	}

	cold := get()
	if cold["ballad"]["compose"] != phaseNA || cold["ballad"]["synth"] != phaseRun || cold["ballad"]["export"] != phaseAlways {
		t.Fatalf("own-lyrics track: %v", cold["ballad"])
	}
	if cold["idea"]["compose"] != phaseRun || cold["idea"]["synth"] != phaseRun {
		t.Fatalf("caption-only track: %v", cold["idea"])
	}

	// Record what a run of the seeded own-lyrics track would have cached.
	dir := filepath.Join(projects, "songs")
	cfg, _ := config.LoadBytes([]byte(musicYAML))
	req := music.BuildRequest(cfg, cfg.Tracks[0], 7)
	body, err := music.SynthRequest(nil, req)
	if err != nil {
		t.Fatal(err)
	}
	hash := cache.SynthHash(cache.SynthParams{Request: body, SynthModel: config.DefaultMusicSynth, VAE: "vae-BF16.gguf"})
	_, audio := cache.MusicPaths("ballad", "mp3")
	writeArtifact(t, projects, "songs", filepath.ToSlash(audio), []byte("audio"))
	m := &cache.Manifest{Scenes: map[string]cache.SceneEntry{"ballad": {SynthHash: hash, AudioPath: audio}}}
	if err := cache.SaveManifest(filepath.Join(dir, cache.ManifestPath()), m); err != nil {
		t.Fatal(err)
	}
	warm := get()
	if warm["ballad"]["synth"] != phaseHit {
		t.Fatalf("a cached own-lyrics track must report a synth hit: %v", warm["ballad"])
	}
	if warm["idea"]["synth"] != phaseRun {
		t.Fatalf("the unseeded track must still re-run: %v", warm["idea"])
	}
}

func TestEnvironment_MusicEngineIsOptionalAndModelsAreInventoried(t *testing.T) {
	s, _ := newProjectsServer(t)
	installMusicModels(t)
	s.Look = func(name string) (string, error) {
		if name == "ace-synth" {
			return "/fake/bin/ace-synth", nil
		}
		return "", fmt.Errorf("not found")
	}
	s.EnvRunner = runner.NewFakeRunner()
	rep := getEnvironment(t, s, "")

	synth := binByName(t, rep, "ace-synth")
	if !synth.Present || synth.Required {
		t.Fatalf("ace-synth = %+v", synth)
	}
	lm := binByName(t, rep, "ace-lm")
	if lm.Present || lm.Required || !strings.Contains(lm.Fix, "build-acestep.sh") {
		t.Fatalf("ace-lm = %+v", lm)
	}
	if len(rep.Music) != 4 {
		t.Fatalf("music inventory = %+v", rep.Music)
	}
	for _, m := range rep.Models {
		if strings.HasSuffix(m.Name, ".gguf") && strings.Contains(m.Name, "acestep") {
			t.Errorf("music files must not be listed as image models: %s", m.Name)
		}
	}
}

func TestArtifacts_AudioIsServedWithRangeAndAudioMediaType(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "songs")
	writeArtifact(t, projects, "songs", "out/songs/a.mp3", []byte("0123456789"))
	writeArtifact(t, projects, "songs", "out/songs/a.wav", []byte("RIFF0123456789"))

	req := httptest.NewRequest(http.MethodGet, "/api/projects/songs/artifacts/out/songs/a.mp3", nil)
	req.Header.Set("Range", "bytes=3-6")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "3456" {
		t.Fatalf("range: %d %q", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "audio/") {
		t.Errorf("mp3 Content-Type = %q", ct)
	}
	rec = doJSON(t, s.Handler(), http.MethodGet, "/api/projects/songs/artifacts/out/songs/a.wav", nil)
	if ct := rec.Header().Get("Content-Type"); rec.Code != 200 || !strings.HasPrefix(ct, "audio/") {
		t.Errorf("wav: %d %q", rec.Code, ct)
	}
	rec = doJSON(t, s.Handler(), http.MethodGet, "/api/projects/songs/artifacts/../../etc/passwd", nil)
	if rec.Code == http.StatusOK {
		t.Error("traversal must be rejected for audio too")
	}
}

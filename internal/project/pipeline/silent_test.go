package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
)

func TestSilentScenes_MixedProjectPipeline(t *testing.T) {
	chdirTemp(t)
	if err := os.WriteFile("m.safetensors", []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("tmp", 0o755); err != nil {
		t.Fatal(err)
	}

	seed := 7
	silence := 2.0
	cfg := &config.Config{
		Project:    "demo",
		OutputFile: "out/demo.mp4",
		Defaults: config.Defaults{
			Model: "m.safetensors", Voice: writeVoice(t), Resolution: "1280x720", Sampler: "euler", Steps: 30, FPS: 30,
		},
		Scenes: []config.Scene{
			{ID: "talk", Prompt: "a city", TTSText: "Hello.", Speed: 1.0, Seed: &seed},
			{ID: "quiet", Prompt: "a sunset", SilenceSeconds: &silence, Seed: &seed,
				Motion: &config.Motion{Type: config.MotionZoomIn}},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if inv.Name == "ffprobe" {
			return runner.ScriptedResult{Stdout: []byte(`{"format":{"duration":"3.0"}}`)}
		}
		if inv.Name == "piper" {
			return runner.ScriptedResult{Stdout: []byte("RIFF....WAVE")}
		}
		return runner.ScriptedResult{}
	})

	var buf bytes.Buffer
	emitter := events.New(&buf, true)
	manifest := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}

	final, _ := runVideo(context.Background(), cfg, emitter, fr,
		ttsRunnerAdapter{r: fr}, assembleRunnerAdapter{r: fr},
		manifest, cache.NewOnlyFilter(""), false, nil, "tmp", "", nil, nil)

	if final != "out/demo.mp4" {
		t.Errorf("final = %q", final)
	}

	// Invocation list: talk = piper, ffprobe, sd, ffmpeg(clip);
	// quiet = sd, ffmpeg(clip) only; then ffmpeg(concat).
	var names []string
	for _, c := range fr.Calls() {
		names = append(names, c.Name)
	}
	want := []string{"piper", "ffprobe", "sd", "ffmpeg", "sd", "ffmpeg", "ffmpeg"}
	if strings.Join(names, " ") != strings.Join(want, " ") {
		t.Fatalf("invocations = %v, want %v", names, want)
	}

	// The quiet clip (5th call) uses anullsrc, -t 2.0, zoom filter, no stillimage.
	quietClip := fr.Calls()[5]
	joined := strings.Join(quietClip.Args, " ")
	if !strings.Contains(joined, "anullsrc") {
		t.Errorf("quiet clip must use a silent audio source: %v", quietClip.Args)
	}
	if !strings.Contains(joined, "-t 2.000000") {
		t.Errorf("quiet clip duration must be silence_seconds: %v", quietClip.Args)
	}
	if !strings.Contains(joined, "zoompan=") {
		t.Errorf("quiet clip should carry its zoom_in filter: %v", quietClip.Args)
	}
	if strings.Contains(joined, "stillimage") {
		t.Errorf("non-static motion must drop -tune stillimage: %v", quietClip.Args)
	}

	// Events: quiet emits no tts/probe events of any kind.
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var ev events.Event
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if ev.SceneID == "quiet" && (ev.Phase == "tts" || ev.Phase == "probe") {
			t.Errorf("silent scene emitted a %s event: %+v", ev.Phase, ev)
		}
	}

	// Manifest stores a clip hash for both scenes.
	for _, id := range []string{"talk", "quiet"} {
		if manifest.Scenes[id].ClipHash == "" {
			t.Errorf("scene %q should store a clip hash", id)
		}
	}

	// Second run: everything cached — only ffprobe (always-run probe for the
	// narrated scene) and the final concat ffmpeg are invoked.
	fr2 := runner.NewFakeRunner()
	fr2.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if inv.Name == "ffprobe" {
			return runner.ScriptedResult{Stdout: []byte(`{"format":{"duration":"3.0"}}`)}
		}
		if inv.Name == "piper" {
			return runner.ScriptedResult{Stdout: []byte("RIFF....WAVE")}
		}
		return runner.ScriptedResult{}
	})
	var buf2 bytes.Buffer
	runVideo(context.Background(), cfg, events.New(&buf2, true), fr2,
		ttsRunnerAdapter{r: fr2}, assembleRunnerAdapter{r: fr2},
		manifest, cache.NewOnlyFilter(""), false, nil, "tmp", "", nil, nil)
	names = nil
	for _, c := range fr2.Calls() {
		names = append(names, c.Name)
	}
	if strings.Join(names, " ") != "ffprobe ffmpeg" {
		t.Errorf("cached run invocations = %v, want [ffprobe ffmpeg]", names)
	}
}

func TestSilentScenes_ClipHashMigration(t *testing.T) {
	chdirTemp(t)
	if err := os.WriteFile("m.safetensors", []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("tmp", 0o755); err != nil {
		t.Fatal(err)
	}

	seed := 7
	cfg := &config.Config{
		Project:    "demo",
		OutputFile: "out/demo.mp4",
		Defaults: config.Defaults{
			Model: "m.safetensors", Voice: writeVoice(t), Resolution: "1280x720", Sampler: "euler", Steps: 30, FPS: 30,
		},
		Scenes: []config.Scene{
			{ID: "talk", Prompt: "a city", TTSText: "Hello.", Speed: 1.0, Seed: &seed},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	run := func(m *cache.Manifest) []string {
		fr := fakeVideoRunner(t)
		var buf bytes.Buffer
		runVideo(context.Background(), cfg, events.New(&buf, true), fr,
			ttsRunnerAdapter{r: fr}, assembleRunnerAdapter{r: fr},
			m, cache.NewOnlyFilter(""), false, nil, "tmp", "", nil, nil)
		var names []string
		for _, c := range fr.Calls() {
			names = append(names, c.Name)
		}
		return names
	}

	manifest := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
	run(manifest) // populate

	// Simulate a legacy manifest: drop the stored clip hash.
	e := manifest.Scenes["talk"]
	e.ClipHash = ""
	manifest.UpdateEntry("talk", e)

	// Migration run: tts and render hit, clip re-encodes once.
	names := run(manifest)
	if strings.Join(names, " ") != "ffprobe ffmpeg ffmpeg" {
		t.Errorf("migration run = %v, want [ffprobe ffmpeg ffmpeg] (clip re-encode only)", names)
	}

	// Next run converges to full hits.
	names = run(manifest)
	if strings.Join(names, " ") != "ffprobe ffmpeg" {
		t.Errorf("post-migration run = %v, want [ffprobe ffmpeg]", names)
	}
}

func fakeVideoRunner(t *testing.T) *runner.FakeRunner {
	t.Helper()
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if inv.Name == "ffprobe" {
			return runner.ScriptedResult{Stdout: []byte(`{"format":{"duration":"3.0"}}`)}
		}
		if inv.Name == "piper" {
			return runner.ScriptedResult{Stdout: []byte("RIFF....WAVE")}
		}
		return runner.ScriptedResult{}
	})
	return fr
}

func TestPreflight_AllSilentProjectStillRequiresPiper(t *testing.T) {
	// The check is mode-dependent, not content-dependent: video mode
	// requires piper even when every scene is silent.
	err := Preflight(config.ModeVideo, false, lookFrom("sd", "ffmpeg", "ffprobe"))
	if err == nil || !strings.Contains(err.Error(), "piper") {
		t.Fatalf("video-mode preflight must require piper regardless of scene content, got: %v", err)
	}
}

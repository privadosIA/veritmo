package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
)

func animateProject(t *testing.T) *config.Config {
	t.Helper()
	chdirTemp(t)
	for _, f := range []string{"m.safetensors", "wan.gguf", "umt5.gguf", "wan_vae.safetensors"} {
		if err := os.WriteFile(f, []byte("stub"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll("tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	seed := 7
	cfg := &config.Config{
		Project: "demo",
		Output:  config.Output{File: "out/demo.mp4"},
		Defaults: config.Defaults{
			Model: "m.safetensors", Voice: writeVoice(t), Resolution: "1280x720", Sampler: "euler", Steps: 30, FPS: 30,
			Animate: &config.Animate{DiffusionModel: "wan.gguf", T5XXL: "umt5.gguf", VAE: "wan_vae.safetensors"},
		},
		Scenes: []config.Scene{
			{ID: "moving", Prompt: "a city", TTSText: "Hello.", Speed: 1, Seed: &seed, Animate: true, MotionPrompt: "cars drive by"},
			{ID: "still", Prompt: "a sunset", TTSText: "Bye.", Speed: 1, Seed: &seed},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	return cfg
}

func animateRunner(failSD bool) *runner.FakeRunner {
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		switch inv.Name {
		case "ffprobe":
			return runner.ScriptedResult{Stdout: []byte(`{"format":{"duration":"5.0"}}`)}
		case "piper":
			return runner.ScriptedResult{Stdout: []byte("RIFF....WAVE")}
		case "sd":
			if failSD && strings.Contains(strings.Join(inv.Args, " "), "vid_gen") {
				return runner.ScriptedResult{Err: errors.New("sd: out of memory")}
			}
		}
		return runner.ScriptedResult{}
	})
	return fr
}

func runAnimateVideo(t *testing.T, cfg *config.Config, fr *runner.FakeRunner, m *cache.Manifest) (*bytes.Buffer, error) {
	t.Helper()
	var buf bytes.Buffer
	_, err := runVideo(context.Background(), cfg, events.New(&buf, true), fr,
		ttsRunnerAdapter{r: fr}, assembleRunnerAdapter{r: fr},
		m, cache.NewOnlyFilter(""), false, []string{"CUDA_VISIBLE_DEVICES=1"}, "tmp", "", nil, nil)
	return &buf, err
}

func phasesOf(buf *bytes.Buffer, scene string) []string {
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var ev events.Event
		if json.Unmarshal([]byte(line), &ev) == nil && ev.SceneID == scene && ev.Type == events.TypePhaseStart {
			out = append(out, ev.Phase)
		}
	}
	return out
}

func TestAnimate_PhaseOrderInvocationsAndCache(t *testing.T) {
	cfg := animateProject(t)
	m := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
	fr := animateRunner(false)
	buf, err := runAnimateVideo(t, cfg, fr, m)
	if err != nil {
		t.Fatal(err)
	}

	if got := strings.Join(phasesOf(buf, "moving"), " "); got != "tts probe render animate clip" {
		t.Errorf("animated phases = %q", got)
	}
	if got := strings.Join(phasesOf(buf, "still"), " "); got != "tts probe render clip" {
		t.Errorf("non-animated scene must emit no animate event: %q", got)
	}

	var vidGen, ffmpegClipFromAnim, sdTotal int
	for _, c := range fr.Calls() {
		joined := strings.Join(c.Args, " ")
		if c.Name == "sd" {
			sdTotal++
			if strings.Contains(joined, "vid_gen") {
				vidGen++
				if !strings.Contains(joined, "-p cars drive by") || !strings.Contains(joined, "-i tmp/scene_moving.png") {
					t.Errorf("video invocation = %v", c.Args)
				}
				if !strings.Contains(strings.Join(c.Env, " "), "CUDA_VISIBLE_DEVICES=1") {
					t.Errorf("animate must use the GPU override, env = %v", c.Env)
				}
			}
		}
		if c.Name == "ffmpeg" && strings.Contains(joined, "-stream_loop -1 -i tmp/scene_moving.anim.mp4") {
			ffmpegClipFromAnim++
		}
	}
	if vidGen != 1 || sdTotal != 3 {
		t.Errorf("sd calls: vid_gen=%d total=%d, want 1 and 3 (2 renders + 1 animation, never chained)", vidGen, sdTotal)
	}
	if ffmpegClipFromAnim != 1 {
		t.Errorf("clip of the animated scene must read the animation, got %d", ffmpegClipFromAnim)
	}
	if e := m.Scenes["moving"]; e.AnimateHash == "" || e.AnimPath != "tmp/scene_moving.anim.mp4" {
		t.Errorf("manifest entry = %+v", e)
	}
	if m.Scenes["still"].AnimateHash != "" {
		t.Error("non-animated scene must not store an animate hash")
	}

	// Second run, nothing changed: no sd, no piper; animate is skipped silently.
	fr2 := animateRunner(false)
	buf2, err := runAnimateVideo(t, cfg, fr2, m)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range fr2.Calls() {
		if c.Name == "sd" || c.Name == "piper" {
			t.Errorf("cached run invoked %s %v", c.Name, c.Args)
		}
	}
	for _, p := range phasesOf(buf2, "moving") {
		if p == "animate" {
			t.Error("cached animate must be silent")
		}
	}

	// Changing only the motion prompt re-animates but does not re-render.
	cfg.Scenes[0].MotionPrompt = "birds fly"
	fr3 := animateRunner(false)
	if _, err := runAnimateVideo(t, cfg, fr3, m); err != nil {
		t.Fatal(err)
	}
	var sd3 []string
	for _, c := range fr3.Calls() {
		if c.Name == "sd" {
			sd3 = append(sd3, strings.Join(c.Args, " "))
		}
	}
	if len(sd3) != 1 || !strings.Contains(sd3[0], "vid_gen") || !strings.Contains(sd3[0], "-p birds fly") {
		t.Errorf("only the animation should re-run, sd calls = %v", sd3)
	}
}

func TestAnimate_FailureStopsTheRunWithAnimateError(t *testing.T) {
	cfg := animateProject(t)
	m := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
	fr := animateRunner(true)
	buf, err := runAnimateVideo(t, cfg, fr, m)
	if err == nil {
		t.Fatal("expected an error when the animation fails")
	}
	var found bool
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var ev events.Event
		if json.Unmarshal([]byte(line), &ev) == nil && ev.SceneID == "moving" && ev.Type == events.TypeError {
			found = strings.Contains(ev.Message, "animate failed") && strings.Contains(ev.Message, "out of memory")
		}
	}
	if !found {
		t.Errorf("no animate error event with stderr in:\n%s", buf.String())
	}
	for _, c := range fr.Calls() {
		if c.Name == "ffmpeg" && strings.Contains(strings.Join(c.Args, " "), "scene_moving.mp4") {
			t.Error("no clip must be produced for the failed scene (no fallback to the still)")
		}
	}
}

// runAnimateVideoOnly is runAnimateVideo restricted to one scene, like --only.
func runAnimateVideoOnly(t *testing.T, cfg *config.Config, fr *runner.FakeRunner, m *cache.Manifest, only string) (*bytes.Buffer, error) {
	t.Helper()
	var buf bytes.Buffer
	_, err := runVideo(context.Background(), cfg, events.New(&buf, true), fr,
		ttsRunnerAdapter{r: fr}, assembleRunnerAdapter{r: fr},
		m, cache.NewOnlyFilter(only), false, []string{"CUDA_VISIBLE_DEVICES=1"}, "tmp", "", nil, nil)
	return &buf, err
}

func TestOnly_DoesNotMakeALaterRunTreatTheOtherScenesAsProduced(t *testing.T) {
	cfg := animateProject(t)
	m := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
	// a fresh project, one scene at a time: the other scene was never rendered, nor animated
	if _, err := runAnimateVideoOnly(t, cfg, animateRunner(false), m, cfg.Scenes[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Scenes[cfg.Scenes[1].ID]; ok {
		t.Fatalf("the scene --only left out must have no entry yet: %+v", m.Scenes[cfg.Scenes[1].ID])
	}
	fr := animateRunner(false)
	buf, err := runAnimateVideoOnly(t, cfg, fr, m, cfg.Scenes[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(phasesOf(buf, cfg.Scenes[1].ID), ","); !strings.Contains(got, "render") || !strings.Contains(got, "clip") {
		t.Fatalf("the scene run second must be produced, not skipped as cached: %v", got)
	}
}

func TestOnly_KeepsTheEntryOfAnotherSceneWhoseConfigChanged(t *testing.T) {
	cfg := animateProject(t)
	m := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
	if _, err := runAnimateVideo(t, cfg, animateRunner(false), m); err != nil {
		t.Fatal(err)
	}
	other := cfg.Scenes[1].ID
	before := m.Scenes[other]
	cfg.Scenes[1].Prompt = "a completely different prompt" // edited, but only the first scene is run
	if _, err := runAnimateVideoOnly(t, cfg, animateRunner(false), m, cfg.Scenes[0].ID); err != nil {
		t.Fatal(err)
	}
	if m.Scenes[other] != before {
		t.Fatalf("the entry of a scene --only left out must stay as its last run left it:\n%+v\n%+v", before, m.Scenes[other])
	}
	// the next full run therefore sees that it changed and renders it again
	fr := animateRunner(false)
	if _, err := runAnimateVideo(t, cfg, fr, m); err != nil {
		t.Fatal(err)
	}
	if got := sdCallFor(t, fr, "tmp/scene_"+other+".png"); !strings.Contains(got, "different prompt") {
		t.Errorf("the edited scene must render with its new prompt: %s", got)
	}
}

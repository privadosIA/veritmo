package pipeline

import (
	"os"
	"strings"
	"testing"

	"veritmo/internal/platform/cache"
	"veritmo/internal/project/config"
)

func ltxAudioProject(t *testing.T, modelAudio bool) *config.Config {
	t.Helper()
	cfg := animateProject(t)
	for _, f := range []string{"dit.gguf", "ltx_vae.safetensors", "ltx_audio_vae.safetensors", "gemma.gguf", "connectors.safetensors"} {
		if err := os.WriteFile(f, []byte("stub"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg.Defaults.Animate = &config.Animate{
		Engine: config.EngineLTX, DiffusionModel: "dit.gguf", VAE: "ltx_vae.safetensors", AudioVAE: "ltx_audio_vae.safetensors",
		LLM: "gemma.gguf", Connectors: "connectors.safetensors", ModelAudio: modelAudio,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	return cfg
}

// clipArgsOf returns the ffmpeg call that builds the animated scene's clip.
func clipArgsOf(t *testing.T, calls []string) string {
	t.Helper()
	for _, c := range calls {
		if strings.Contains(c, "-stream_loop -1 -i tmp/scene_moving.anim.mp4") {
			return c
		}
	}
	t.Fatalf("no clip call in %v", calls)
	return ""
}

func TestModelAudio_AmbienceReachesTheClipOfAnAnimatedScene(t *testing.T) {
	cfg := ltxAudioProject(t, true)
	m := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
	fr := animateRunner(false)
	if _, err := runAnimateVideo(t, cfg, fr, m); err != nil {
		t.Fatal(err)
	}
	var all []string
	extracted := 0
	for _, c := range fr.Calls() {
		if c.Name != "ffmpeg" {
			continue
		}
		j := strings.Join(c.Args, " ")
		all = append(all, j)
		if strings.HasSuffix(j, "tmp/scene_moving.anim.ambient.wav") {
			extracted++
		}
	}
	if extracted != 1 {
		t.Errorf("the model's audio must be extracted once, got %d", extracted)
	}
	clip := clipArgsOf(t, all)
	if !strings.Contains(clip, "-i tmp/scene_moving.anim.ambient.wav") || !strings.Contains(clip, "volume=-18dB") {
		t.Errorf("the clip must mix the ambience: %s", clip)
	}
	for _, c := range all {
		if strings.Contains(c, "scene_still") && strings.Contains(c, "ambient") {
			t.Errorf("a scene that does not animate gets no ambience: %s", c)
		}
	}
}

func TestModelAudio_OffKeepsTheClipAsBefore(t *testing.T) {
	cfg := ltxAudioProject(t, false)
	m := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
	fr := animateRunner(false)
	if _, err := runAnimateVideo(t, cfg, fr, m); err != nil {
		t.Fatal(err)
	}
	for _, c := range fr.Calls() {
		if j := strings.Join(c.Args, " "); c.Name == "ffmpeg" && strings.Contains(j, "ambient") {
			t.Errorf("no ambience without model_audio: %s", j)
		}
	}
	// Turning it on re-animates (and so re-clips) the animated scene only.
	cfg.Defaults.Animate.ModelAudio = true
	fr2 := animateRunner(false)
	if _, err := runAnimateVideo(t, cfg, fr2, m); err != nil {
		t.Fatal(err)
	}
	vidGen, renders := 0, 0
	for _, c := range fr2.Calls() {
		if c.Name == "sd" {
			if strings.Contains(strings.Join(c.Args, " "), "vid_gen") {
				vidGen++
			} else {
				renders++
			}
		}
	}
	if vidGen != 1 || renders != 0 {
		t.Errorf("turning model_audio on: vid_gen=%d renders=%d, want 1 and 0", vidGen, renders)
	}
}

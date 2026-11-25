package animate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
)

func ltxParams() Params {
	p := testParams()
	p.Defaults.Animate = &config.Animate{
		Engine:         config.EngineLTX,
		DiffusionModel: "models/video/dit.gguf",
		VAE:            "models/video/vae.safetensors",
		AudioVAE:       "models/video/audio_vae.safetensors",
		LLM:            "models/video/gemma.gguf",
		Connectors:     "models/video/connectors.safetensors",
	}
	return p
}

func TestBuildSDArgs_LTXDefaults(t *testing.T) {
	p := ltxParams()
	args, err := BuildSDArgs(*p.Defaults.Animate, "move", 7, "tmp/anim_s1_init.png", p.RawPath)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-M", "vid_gen", "--diffusion-model", "models/video/dit.gguf",
		"--vae", "models/video/vae.safetensors", "--audio-vae", "models/video/audio_vae.safetensors",
		"--llm", "models/video/gemma.gguf", "--embeddings-connectors", "models/video/connectors.safetensors",
		"-i", "tmp/anim_s1_init.png", "-p", "move", "-s", "7", "--sampling-method", "euler",
		"-W", "768", "-H", "416", "--video-frames", "49", "--fps", "24", "--steps", "8", "--cfg-scale", "1",
		"--offload-to-cpu", "--mmap", "-o", "tmp/anim_s1.avi"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("LTX args:\n got  %q\n want %q", args, want)
	}
	for _, a := range args {
		if a == "--t5xxl" || a == "-r" || a == "--flow-shift" || a == "--rng" || strings.HasPrefix(a, "--high-noise") || strings.Contains(a, "<lora:") {
			t.Errorf("no Wan, reference or LoRA option may appear: %s", a)
		}
	}
}

func TestRun_LTXFitsThenSDThenPingPong(t *testing.T) {
	fr := runner.NewFakeRunner()
	if _, err := Run(context.Background(), fr, ltxParams(), nil); err != nil {
		t.Fatal(err)
	}
	calls := fr.Calls()
	if len(calls) != 3 || calls[0].Name != "ffmpeg" || calls[1].Name != "sd" || calls[2].Name != "ffmpeg" {
		t.Fatalf("calls = %+v", calls)
	}
	if !strings.Contains(argValue(t, calls[0].Args, "-vf"), "crop=768:416") {
		t.Errorf("fit must use the animation resolution: %v", calls[0].Args)
	}
	if argValue(t, calls[1].Args, "-i") != "tmp/anim_s1_init.png" {
		t.Errorf("sd must get the fitted copy as first frame: %v", calls[1].Args)
	}
	if !strings.Contains(strings.Join(calls[2].Args, " "), "lt(n,48)") {
		t.Errorf("ping-pong must use 49 frames: %v", calls[2].Args)
	}
}

func TestRun_LTXSdFailureNamesTheScene(t *testing.T) {
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if inv.Name == "sd" {
			return runner.ScriptedResult{Err: errors.New("out of memory")}
		}
		return runner.ScriptedResult{}
	})
	_, err := Run(context.Background(), fr, ltxParams(), nil)
	if err == nil || !strings.Contains(err.Error(), `scene "s1"`) {
		t.Errorf("error = %v", err)
	}
}

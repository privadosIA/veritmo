package animate

import (
	"context"
	"strings"
	"testing"

	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
)

func miniMaxParams() Params {
	p := testParams()
	p.Defaults.Animate = &config.Animate{
		Engine:         config.EngineMiniMaxH3,
		DiffusionModel: "models/video/dit.gguf",
		VAE:            "models/video/vae.safetensors",
		AudioVAE:       "models/video/audio_vae.safetensors",
		LLM:            "models/video/llm.gguf",
	}
	return p
}

func TestBuildSDArgs_MiniMaxDefaults(t *testing.T) {
	p := miniMaxParams()
	args, err := BuildSDArgs(*p.Defaults.Animate, "move", 7, "tmp/anim_s1_init.png", p.RawPath)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-M", "vid_gen", "--diffusion-model", "models/video/dit.gguf",
		"--vae", "models/video/vae.safetensors", "--audio-vae", "models/video/audio_vae.safetensors",
		"--llm", "models/video/llm.gguf", "-r", "tmp/anim_s1_init.png",
		"-p", MiniMaxPromptPrefix + "move", "-s", "7", "-W", "864", "-H", "480",
		"--video-frames", "56", "--fps", "24", "--steps", "20", "--cfg-scale", "1",
		"--diffusion-fa", "--offload-to-cpu", "--mmap", "--rng", "cpu", "-o", "tmp/anim_s1.avi"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("MiniMax args:\n got  %q\n want %q", args, want)
	}
	for _, a := range args {
		if a == "--t5xxl" || a == "-i" || a == "--flow-shift" || strings.HasPrefix(a, "--high-noise") {
			t.Errorf("no Wan or init-image option may appear: %s", a)
		}
	}
}

func TestRun_MiniMaxFitsThenSDThenPingPong(t *testing.T) {
	fr := runner.NewFakeRunner()
	if _, err := Run(context.Background(), fr, miniMaxParams(), nil); err != nil {
		t.Fatal(err)
	}
	calls := fr.Calls()
	if len(calls) != 3 || calls[0].Name != "ffmpeg" || calls[1].Name != "sd" || calls[2].Name != "ffmpeg" {
		t.Fatalf("calls = %+v", calls)
	}
	if !strings.Contains(argValue(t, calls[0].Args, "-vf"), "crop=864:480") {
		t.Errorf("fit must use the animation resolution: %v", calls[0].Args)
	}
	if argValue(t, calls[1].Args, "-r") != "tmp/anim_s1_init.png" {
		t.Errorf("sd must get the fitted copy as reference: %v", calls[1].Args)
	}
	if !strings.Contains(strings.Join(calls[2].Args, " "), "lt(n,55)") {
		t.Errorf("ping-pong must use 56 frames: %v", calls[2].Args)
	}
}

func TestBuildSDArgs_MiniMaxLoRAIsATagInThePromptAndItsDirectory(t *testing.T) {
	p := miniMaxParams()
	p.Defaults.Animate.LoRA = "models/video/minimax_h3_ref2v_turbo_4step.safetensors"
	args, err := BuildSDArgs(p.Defaults.Animate.Resolved(), "move", 7, "tmp/anim_s1_init.png", p.RawPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := argValue(t, args, "-p"); got != MiniMaxPromptPrefix+"move <lora:minimax_h3_ref2v_turbo_4step:1>" {
		t.Errorf("prompt = %q", got)
	}
	if got := argValue(t, args, "--lora-model-dir"); got != "models/video" {
		t.Errorf("--lora-model-dir = %q", got)
	}
	if got := argValue(t, args, "--steps"); got != "4" {
		t.Errorf("a LoRA must default the steps to 4, got %s", got)
	}
	if args[len(args)-2] != "-o" {
		t.Errorf("the output must stay last: %v", args)
	}
}

func TestBuildSDArgs_MiniMaxFirstFrameUsesTheInitImageAndNoReferencePrefix(t *testing.T) {
	p := miniMaxParams()
	a := *p.Defaults.Animate
	a.FirstFrame = true
	a.LoRA = "models/video/minimax_h3_fl2v_turbo_8step.safetensors"
	args, err := BuildSDArgs(a, "move", 7, "tmp/anim_s1_init.png", p.RawPath)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, "\x00")
	if !strings.Contains(joined, "-i\x00tmp/anim_s1_init.png") {
		t.Errorf("the picture must go as the first frame (-i): %q", args)
	}
	for i, x := range args {
		if x == "-r" {
			t.Errorf("no reference image may be passed in first-frame mode: %q", args)
		}
		if x == "-p" && args[i+1] != "move <lora:minimax_h3_fl2v_turbo_8step:1>" {
			t.Errorf("the prompt must be the motion alone plus the LoRA tag, got %q", args[i+1])
		}
		if x == "--steps" && args[i+1] != "8" {
			t.Errorf("the first-frame turbo LoRA runs 8 steps by default, got %s", args[i+1])
		}
	}
}

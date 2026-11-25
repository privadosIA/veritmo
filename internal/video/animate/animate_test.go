package animate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
)

func argValue(t *testing.T, args []string, flag string) string {
	t.Helper()
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	t.Fatalf("flag %s not in %v", flag, args)
	return ""
}

func testParams() Params {
	return Params{
		Scene: config.Scene{ID: "s1", Prompt: "a woman reading"},
		Defaults: config.Defaults{
			SubjectPrompt: "photo of",
			Animate:       &config.Animate{DiffusionModel: "wan.gguf", T5XXL: "umt5.gguf", VAE: "vae.safetensors"},
		},
		Seed:    7,
		Image:   "tmp/scene_s1.png",
		RawPath: "tmp/anim_s1.avi",
		OutPath: "tmp/anim_s1.mp4",
	}
}

func TestBuildSDArgs_Defaults(t *testing.T) {
	p := testParams()
	args, err := BuildSDArgs(*p.Defaults.Animate, "move", 7, p.Image, p.RawPath)
	if err != nil {
		t.Fatal(err)
	}
	for flag, want := range map[string]string{
		"-M": "vid_gen", "--diffusion-model": "wan.gguf", "--vae": "vae.safetensors", "--t5xxl": "umt5.gguf",
		"-i": p.Image, "-p": "move", "-s": "7", "-W": "832", "-H": "480",
		"--video-frames": "33", "--fps": "16", "--steps": "20", "--cfg-scale": "6", "-o": p.RawPath,
	} {
		if got := argValue(t, args, flag); got != want {
			t.Errorf("%s = %q, want %q", flag, got, want)
		}
	}
	for _, f := range []string{"--offload-to-cpu", "--vae-tiling", "--diffusion-fa"} {
		if !strings.Contains(strings.Join(args, " "), f) {
			t.Errorf("missing %s", f)
		}
	}
}

// The single-model command must stay exactly what it was before the
// two-expert mode existed (captured from that version).
func TestBuildSDArgs_SingleModelIsUnchanged(t *testing.T) {
	p := testParams()
	args, err := BuildSDArgs(*p.Defaults.Animate, "move", 7, p.Image, p.RawPath)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-M", "vid_gen", "--diffusion-model", "wan.gguf", "--vae", "vae.safetensors", "--t5xxl", "umt5.gguf",
		"-i", "tmp/scene_s1.png", "-p", "move", "-s", "7", "--sampling-method", "euler", "-W", "832", "-H", "480",
		"--video-frames", "33", "--fps", "16", "--steps", "20", "--cfg-scale", "6",
		"--offload-to-cpu", "--vae-tiling", "--diffusion-fa", "-o", "tmp/anim_s1.avi"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("single-model args changed:\n got  %q\n want %q", args, want)
	}
	for _, a := range args {
		if strings.HasPrefix(a, "--high-noise") || a == "--flow-shift" {
			t.Errorf("no two-expert option may appear without the expert: %s", a)
		}
	}
}

func twoExpertArgs(t *testing.T, a config.Animate) []string {
	t.Helper()
	a.DiffusionModel, a.T5XXL, a.VAE = "low.gguf", "umt5.gguf", "wan21_vae.safetensors"
	args, err := BuildSDArgs(a, "move", 7, "tmp/scene_s1.png", "tmp/anim_s1.avi")
	if err != nil {
		t.Fatal(err)
	}
	return args
}

func TestBuildSDArgs_TwoExpertAddsItsOptionsWithMeasuredDefaults(t *testing.T) {
	args := twoExpertArgs(t, config.Animate{HighNoiseDiffusionModel: "models/video/high.gguf"})
	for flag, want := range map[string]string{
		"--diffusion-model":            "low.gguf",
		"--high-noise-diffusion-model": "models/video/high.gguf",
		"--steps":                      "10",
		"--cfg-scale":                  "3.5",
		"--high-noise-steps":           "8",
		"--high-noise-cfg-scale":       "3.5",
		"--high-noise-sampling-method": "euler",
		"--flow-shift":                 "3.0",
	} {
		if got := argValue(t, args, flag); got != want {
			t.Errorf("%s = %q, want %q", flag, got, want)
		}
	}
	joined := strings.Join(args, " ")
	for _, f := range []string{"--offload-to-cpu", "--vae-tiling", "--diffusion-fa"} {
		if !strings.Contains(joined, f) {
			t.Errorf("memory flag %s must stay in two-expert mode", f)
		}
	}
	if args[len(args)-2] != "-o" {
		t.Errorf("the output stays last: %q", args[len(args)-2:])
	}
}

func TestBuildSDArgs_TwoExpertUserValuesAndZeroCfg(t *testing.T) {
	zero := 0.0
	args := twoExpertArgs(t, config.Animate{
		HighNoiseDiffusionModel: "high.gguf", Steps: 14, CfgScale: &zero, HighNoiseSteps: 5, HighNoiseCfgScale: &zero,
	})
	for flag, want := range map[string]string{"--steps": "14", "--cfg-scale": "0", "--high-noise-steps": "5", "--high-noise-cfg-scale": "0"} {
		if got := argValue(t, args, flag); got != want {
			t.Errorf("%s = %q, want %q", flag, got, want)
		}
	}
}

func TestPrompt(t *testing.T) {
	p := testParams()
	if got := Prompt(p.Scene, p.Defaults); got != "photo of a woman reading" {
		t.Errorf("fallback prompt = %q", got)
	}
	p.Scene.MotionPrompt = "she turns the page"
	if got := Prompt(p.Scene, p.Defaults); got != "she turns the page" {
		t.Errorf("motion prompt = %q", got)
	}
}

func TestBuildPingPongArgs(t *testing.T) {
	args := BuildPingPongArgs("a.avi", "a.mp4", 33)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "reverse") || !strings.Contains(joined, "lt(n,32)") || !strings.Contains(joined, "concat=n=2") {
		t.Errorf("args = %v", args)
	}
	if args[len(args)-1] != "a.mp4" || argValue(t, args, "-i") != "a.avi" {
		t.Errorf("paths wrong: %v", args)
	}
}

func TestRun_InvokesSDThenFFmpegWithEnv(t *testing.T) {
	fr := runner.NewFakeRunner()
	if _, err := Run(context.Background(), fr, testParams(), []string{"CUDA_VISIBLE_DEVICES=1"}); err != nil {
		t.Fatal(err)
	}
	calls := fr.Calls()
	if len(calls) != 2 || calls[0].Name != "sd" || calls[1].Name != "ffmpeg" {
		t.Fatalf("calls = %+v", calls)
	}
	if argValue(t, calls[0].Args, "-o") != "tmp/anim_s1.avi" || argValue(t, calls[1].Args, "-i") != "tmp/anim_s1.avi" {
		t.Errorf("sd output and ffmpeg input must be the same .avi: %v / %v", calls[0].Args, calls[1].Args)
	}
	if calls[1].Args[len(calls[1].Args)-1] != "tmp/anim_s1.mp4" {
		t.Errorf("ffmpeg output = %v", calls[1].Args)
	}
}

func TestRun_FailureNamesSceneAndStopsBeforeFFmpeg(t *testing.T) {
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		return runner.ScriptedResult{Err: errors.New("sd exploded: out of memory")}
	})
	_, err := Run(context.Background(), fr, testParams(), nil)
	if err == nil || !strings.Contains(err.Error(), `"s1"`) || !strings.Contains(err.Error(), "out of memory") {
		t.Fatalf("err = %v", err)
	}
	if n := len(fr.Calls()); n != 1 {
		t.Errorf("%d calls, want 1 (no ffmpeg after sd failure)", n)
	}
}

func TestRun_ASceneAnimatesWithItsOwnFramesStepsAndResolution(t *testing.T) {
	run := func(scene config.Scene) (sd, ffmpeg []string) {
		p := testParams()
		p.Scene = scene
		fr := runner.NewFakeRunner()
		if _, err := Run(context.Background(), fr, p, nil); err != nil {
			t.Fatal(err)
		}
		calls := fr.Calls()
		return calls[0].Args, calls[1].Args
	}
	sd, ff := run(config.Scene{ID: "s1", Prompt: "x", Animation: &config.SceneAnimation{Resolution: "1024x576", Frames: 49, Steps: 12}})
	for flag, want := range map[string]string{"-W": "1024", "-H": "576", "--video-frames": "49", "--steps": "12", "--fps": "16"} {
		if got := argValue(t, sd, flag); got != want {
			t.Errorf("%s = %q, want %q", flag, got, want)
		}
	}
	// the ping-pong keeps the frames that sd generated for this scene (49 -> select n < 48)
	if joined := strings.Join(ff, " "); !strings.Contains(joined, "lt(n,48)") {
		t.Errorf("ping-pong must use 49 frames: %s", joined)
	}
	sd, _ = run(config.Scene{ID: "s2", Prompt: "x"})
	if argValue(t, sd, "--video-frames") != "33" || argValue(t, sd, "--steps") != "20" {
		t.Errorf("a scene without a block must use the project's: %v", sd)
	}
}

func TestRun_ReportsTheModelLoadSdSays(t *testing.T) {
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if inv.Name == "sd" {
			return runner.ScriptedResult{Stdout: []byte("[I] loading tensors completed, taking 0.72s\n[I] loading tensors completed, taking 0.61s\n")}
		}
		return runner.ScriptedResult{}
	})
	loadMs, err := Run(context.Background(), fr, testParams(), nil)
	if err != nil || loadMs != 1330 {
		t.Errorf("loadMs = %d, err = %v; want 1330", loadMs, err)
	}
}

func TestRun_KeepsTheLoadWhenAFollowingStepFails(t *testing.T) {
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		switch inv.Name {
		case "sd":
			return runner.ScriptedResult{Stdout: []byte("[I] loading tensors completed, taking 1.00s\n")}
		case "ffmpeg":
			return runner.ScriptedResult{Err: errors.New("boom")}
		}
		return runner.ScriptedResult{}
	})
	loadMs, err := Run(context.Background(), fr, testParams(), nil)
	if err == nil || loadMs != 1000 {
		t.Errorf("loadMs = %d, err = %v; want 1000 and an error", loadMs, err)
	}
}

func TestRun_LoadIsZeroWhenSdSaysNothing(t *testing.T) {
	if loadMs, err := Run(context.Background(), runner.NewFakeRunner(), testParams(), nil); err != nil || loadMs != 0 {
		t.Errorf("loadMs = %d, err = %v; want 0", loadMs, err)
	}
}

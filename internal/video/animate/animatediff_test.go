package animate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
)

func animateDiffParams() Params {
	p := testParams()
	p.Defaults.Animate = &config.Animate{
		Engine:       config.EngineAnimateDiff,
		Checkpoint:   "models/v1-5-pruned-emaonly.safetensors",
		MotionModule: "models/video/mm_sd15_v3.safetensors",
	}
	return p
}

// The exact command measured by hand on 2026-10-11.
func TestBuildSDArgs_AnimateDiffDefaults(t *testing.T) {
	p := animateDiffParams()
	args, err := BuildSDArgs(*p.Defaults.Animate, "move", 7, "tmp/anim_s1_init.png", p.RawPath)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-M", "vid_gen", "--model", "models/v1-5-pruned-emaonly.safetensors",
		"--motion-module", "models/video/mm_sd15_v3.safetensors",
		"-i", "tmp/anim_s1_init.png", "--strength", "0.75", "-p", "move", "-s", "7",
		"--sampling-method", "euler", "--scheduler", "karras", "-W", "512", "-H", "512",
		"--video-frames", "16", "--fps", "8", "--steps", "25", "--cfg-scale", "7",
		"--offload-to-cpu", "--diffusion-fa", "-o", "tmp/anim_s1.avi"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("AnimateDiff args:\n got  %q\n want %q", args, want)
	}
	for _, a := range args {
		if a == "--diffusion-model" || a == "--t5xxl" || a == "--vae" || a == "--flow-shift" || strings.HasPrefix(a, "--high-noise") {
			t.Errorf("no Wan option may appear: %s", a)
		}
	}
}

func TestBuildFitArgs(t *testing.T) {
	args := BuildFitArgs("tmp/scene_s1.png", "tmp/anim_s1_init.png", 512, 512)
	if got := argValue(t, args, "-vf"); got != "scale=512:512:force_original_aspect_ratio=increase,crop=512:512" {
		t.Errorf("filter = %q", got)
	}
	if argValue(t, args, "-i") != "tmp/scene_s1.png" || args[len(args)-1] != "tmp/anim_s1_init.png" {
		t.Errorf("paths wrong: %v", args)
	}
	if FittedPath("tmp/anim_s1.avi") != "tmp/anim_s1_init.png" {
		t.Errorf("fitted path = %q", FittedPath("tmp/anim_s1.avi"))
	}
}

func TestRun_AnimateDiffFitsThenSDThenPingPong(t *testing.T) {
	fr := runner.NewFakeRunner()
	if _, err := Run(context.Background(), fr, animateDiffParams(), []string{"CUDA_VISIBLE_DEVICES=1"}); err != nil {
		t.Fatal(err)
	}
	calls := fr.Calls()
	if len(calls) != 3 || calls[0].Name != "ffmpeg" || calls[1].Name != "sd" || calls[2].Name != "ffmpeg" {
		t.Fatalf("calls = %+v", calls)
	}
	// the still is read, never written; sd gets the fitted copy
	if argValue(t, calls[0].Args, "-i") != "tmp/scene_s1.png" || calls[0].Args[len(calls[0].Args)-1] != "tmp/anim_s1_init.png" {
		t.Errorf("fit step: %v", calls[0].Args)
	}
	if argValue(t, calls[1].Args, "-i") != "tmp/anim_s1_init.png" {
		t.Errorf("sd must animate the fitted copy: %v", calls[1].Args)
	}
	if argValue(t, calls[2].Args, "-i") != "tmp/anim_s1.avi" || calls[2].Args[len(calls[2].Args)-1] != "tmp/anim_s1.mp4" {
		t.Errorf("ping-pong: %v", calls[2].Args)
	}
	// 16 frames -> select n < 15 for the reversed half
	if !strings.Contains(strings.Join(calls[2].Args, " "), "lt(n,15)") {
		t.Errorf("ping-pong must use 16 frames: %v", calls[2].Args)
	}
}

func TestRun_AnimateDiffSceneUsesItsOwnFramesAndResolution(t *testing.T) {
	p := animateDiffParams()
	p.Scene.Animation = &config.SceneAnimation{Resolution: "640x352", Frames: 24, Steps: 12}
	fr := runner.NewFakeRunner()
	if _, err := Run(context.Background(), fr, p, nil); err != nil {
		t.Fatal(err)
	}
	calls := fr.Calls()
	if !strings.Contains(argValue(t, calls[0].Args, "-vf"), "crop=640:352") {
		t.Errorf("fit must use the scene's resolution: %v", calls[0].Args)
	}
	for flag, want := range map[string]string{"-W": "640", "-H": "352", "--video-frames": "24", "--steps": "12", "--fps": "8"} {
		if got := argValue(t, calls[1].Args, flag); got != want {
			t.Errorf("%s = %q, want %q", flag, got, want)
		}
	}
}

func TestRun_AnimateDiffFailuresNameTheSceneAndStop(t *testing.T) {
	for name, failOn := range map[string]string{"fit": "ffmpeg", "sd": "sd"} {
		t.Run(name, func(t *testing.T) {
			fr := runner.NewFakeRunner()
			fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
				if inv.Name == failOn {
					return runner.ScriptedResult{Err: errors.New("boom: " + failOn)}
				}
				return runner.ScriptedResult{}
			})
			_, err := Run(context.Background(), fr, animateDiffParams(), nil)
			if err == nil || !strings.Contains(err.Error(), `"s1"`) || !strings.Contains(err.Error(), "boom: "+failOn) {
				t.Fatalf("err = %v", err)
			}
			wantCalls := map[string]int{"fit": 1, "sd": 2}[name]
			if n := len(fr.Calls()); n != wantCalls {
				t.Errorf("%d calls, want %d (nothing after the failure)", n, wantCalls)
			}
		})
	}
}

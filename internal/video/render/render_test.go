package render

import (
	"context"
	"errors"
	"testing"

	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
)

func TestParseResolution(t *testing.T) {
	w, h, err := ParseResolution("1280x720")
	if err != nil {
		t.Fatalf("ParseResolution() error = %v, want nil", err)
	}
	if w != 1280 || h != 720 {
		t.Fatalf("ParseResolution() = (%d, %d), want (1280, 720)", w, h)
	}

	if _, _, err := ParseResolution("bogus"); err == nil {
		t.Fatal("ParseResolution(\"bogus\") error = nil, want non-nil")
	}
}

func TestBuildArgs_OmitsNegativePromptWhenEmpty(t *testing.T) {
	args, err := BuildArgs(Model{Checkpoint: "model.safetensors"}, "a cat", "", 42, "512x512", "euler", 20, "out.png")
	if err != nil {
		t.Fatalf("BuildArgs() error = %v, want nil", err)
	}
	for _, a := range args {
		if a == Flags.NegativePrompt {
			t.Fatalf("args contain %q despite empty negative prompt: %v", Flags.NegativePrompt, args)
		}
	}
}

func TestBuildArgs_IncludesAllFlags(t *testing.T) {
	args, err := BuildArgs(Model{Checkpoint: "model.safetensors"}, "a cat", "blurry", 42, "512x768", "dpmpp_2m", 30, "out.png")
	if err != nil {
		t.Fatalf("BuildArgs() error = %v, want nil", err)
	}
	want := []string{
		Flags.Model, "model.safetensors",
		Flags.Prompt, "a cat",
		Flags.NegativePrompt, "blurry",
		Flags.Seed, "42",
		Flags.SamplingMethod, "dpmpp_2m",
		Flags.Steps, "30",
		Flags.Width, "512",
		Flags.Height, "768",
		Flags.Output, "out.png",
	}
	if len(args) != len(want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}

func TestRun_UsesUnsetSeedSentinelWhenSceneSeedIsNil(t *testing.T) {
	fr := runner.NewFakeRunner()
	scene := config.Scene{ID: "s1", Prompt: "a cat"}
	defaults := config.Defaults{Model: "model.safetensors", Resolution: "512x512", Sampler: "euler", Steps: 20}

	if err := Run(context.Background(), fr, scene, defaults, "out.png", nil); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	calls := fr.Calls()
	if len(calls) != 1 {
		t.Fatalf("got %d invocations, want 1", len(calls))
	}
	if calls[0].Name != Binary {
		t.Fatalf("binary = %q, want %q", calls[0].Name, Binary)
	}
	foundSeed := false
	for i, a := range calls[0].Args {
		if a == Flags.Seed && i+1 < len(calls[0].Args) {
			foundSeed = true
			if calls[0].Args[i+1] != "-1" {
				t.Fatalf("seed = %q, want %q", calls[0].Args[i+1], "-1")
			}
		}
	}
	if !foundSeed {
		t.Fatalf("args missing seed flag: %v", calls[0].Args)
	}
}

func TestRun_PassesEnvOverrides(t *testing.T) {
	fr := runner.NewFakeRunner()
	scene := config.Scene{ID: "s1", Prompt: "a cat"}
	defaults := config.Defaults{Model: "model.safetensors", Resolution: "512x512", Sampler: "euler", Steps: 20}

	if err := Run(context.Background(), fr, scene, defaults, "out.png", []string{"CUDA_VISIBLE_DEVICES=1"}); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	calls := fr.Calls()
	if len(calls) != 1 || len(calls[0].Env) != 1 || calls[0].Env[0] != "CUDA_VISIBLE_DEVICES=1" {
		t.Fatalf("env = %v, want [CUDA_VISIBLE_DEVICES=1]", calls[0].Env)
	}
}

func TestRun_PropagatesRunnerError(t *testing.T) {
	fr := runner.NewFakeRunner()
	fr.Script(runner.ScriptedResult{Err: errBoom})
	scene := config.Scene{ID: "s1", Prompt: "a cat"}
	defaults := config.Defaults{Model: "m", Resolution: "512x512", Sampler: "euler", Steps: 1}

	if err := Run(context.Background(), fr, scene, defaults, "out.png", nil); err == nil {
		t.Fatal("Run() error = nil, want non-nil")
	}
}

var errBoom = &boomError{}

type boomError struct{}

func (*boomError) Error() string { return "exit status 1 (stderr: boom)" }

func TestEditResolution_KeepsTheShapeOfThePictureInTheAskedPixels(t *testing.T) {
	cases := []struct {
		w, h  int
		asked string
		want  string
	}{
		{1920, 1080, "1024x576", "1024x576"},
		{960, 1367, "1024x576", "640x912"},
		{1000, 1000, "1024x576", "768x768"},
		{500, 100, "1024x576", "1680x336"},
		{0, 0, "1024x576", "1024x576"},
	}
	for _, c := range cases {
		got, err := EditResolution(c.w, c.h, c.asked)
		if err != nil || got != c.want {
			t.Errorf("EditResolution(%d, %d, %s) = %q, %v; want %q", c.w, c.h, c.asked, got, err, c.want)
		}
	}
	if _, err := EditResolution(100, 100, "bad"); err == nil {
		t.Error("a bad resolution must be an error")
	}
}

const sdLoadOutput = "[I] loading tensors completed, taking 0.20s (read: 0.04s)\n[I] loading tensors completed, taking 1.20s (read: 0.65s)\n[I] sampling completed, taking 4.34s\n"

func TestRunVariant_ReportsTheModelLoadSdSays(t *testing.T) {
	fr := runner.NewFakeRunner()
	fr.Script(runner.ScriptedResult{Stdout: []byte(sdLoadOutput)})
	res, err := RunVariant(context.Background(), fr, config.Scene{ID: "a", Prompt: "a cat"}, config.Defaults{Model: "m.safetensors", Resolution: "512x512", Sampler: "euler", Steps: 5}, 0, 1, "a.png", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.LoadMs != 1400 {
		t.Errorf("LoadMs = %d, want 1400", res.LoadMs)
	}
}

func TestRunVariant_LoadIsZeroWhenSdSaysNothing(t *testing.T) {
	fr := runner.NewFakeRunner()
	res, err := RunVariant(context.Background(), fr, config.Scene{ID: "a", Prompt: "a cat"}, config.Defaults{Model: "m.safetensors", Resolution: "512x512", Sampler: "euler", Steps: 5}, 0, 1, "a.png", nil)
	if err != nil || res.LoadMs != 0 {
		t.Errorf("res = %+v, err = %v; want LoadMs 0 and no error", res, err)
	}
}

func TestRunVariant_AddsTheLoadOfEveryAttemptWhenTheCardRunsOut(t *testing.T) {
	fr := runner.NewFakeRunner()
	fr.Script(
		runner.ScriptedResult{Stdout: []byte("[I] loading tensors completed, taking 1.00s\n"), Err: errors.New("sd: CUDA error: out of memory")},
		runner.ScriptedResult{Stdout: []byte("[I] loading tensors completed, taking 2.00s\n")},
	)
	res, err := RunVariant(context.Background(), fr, config.Scene{ID: "a", Prompt: "a cat"}, config.Defaults{Model: "m.safetensors", Resolution: "1024x1024", Sampler: "euler", Steps: 5}, 0, 1, "a.png", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.LoadMs != 3000 || len(fr.Calls()) != 2 {
		t.Errorf("LoadMs = %d over %d calls, want 3000 over 2", res.LoadMs, len(fr.Calls()))
	}
}

package render

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
)

const oomError = "runner: sd --diffusion-model m.safetensors -W 1280 -H 704: exit status 1 (stderr: [E] krea2 segment 1/29 (krea2.blocks.0) failed during weight preparation --- ggml_runner.cpp:898)"

func TestIsOutOfMemory_RecognizesWhatTheProgramSaysWhenTheCardIsFull(t *testing.T) {
	for _, msg := range []string{oomError, "CUDA error: out of memory", "ggml_cuda: cudaMalloc failed", "failed to allocate buffer"} {
		if !IsOutOfMemory(fmt.Errorf("%s", msg)) {
			t.Errorf("%q is a memory failure", msg)
		}
	}
	for _, msg := range []string{"exit status 1 (stderr: invalid argument)", "model file not found"} {
		if IsOutOfMemory(fmt.Errorf("%s", msg)) {
			t.Errorf("%q is not a memory failure", msg)
		}
	}
	if IsOutOfMemory(nil) {
		t.Error("no error is no memory failure")
	}
}

func TestShrinkResolution_ThreeQuartersOfThePixelsInMultiplesOf64AndNeverTooSmall(t *testing.T) {
	cases := map[string]string{"1280x704": "1088x640", "1024x576": "896x512", "768x768": "640x640"}
	for in, want := range cases {
		got := ShrinkResolution(in)
		if got != want {
			t.Errorf("ShrinkResolution(%s) = %s, want %s", in, got, want)
		}
		w1, h1, _ := ParseResolution(in)
		w2, h2, _ := ParseResolution(got)
		if w2*h2 >= w1*h1 || w2%64 != 0 || h2%64 != 0 {
			t.Errorf("%s -> %s is not a smaller multiple of 64", in, got)
		}
	}
	if got := ShrinkResolution("512x512"); got != "512x512" {
		t.Errorf("too small to shrink: %s", got)
	}
	if got := ShrinkResolution("nonsense"); got != "nonsense" {
		t.Errorf("a bad value is left alone: %s", got)
	}
}

func sceneAndDefaults() (config.Scene, config.Defaults) {
	return config.Scene{ID: "s1", Prompt: "a cat"}, config.Defaults{Model: "model.safetensors", Resolution: "1280x704", Sampler: "euler", Steps: 8}
}

func TestRunVariant_AMemoryFailureIsRetriedSmallerAndSaysTheSizeUsed(t *testing.T) {
	fr := runner.NewFakeRunner()
	fr.Script(runner.ScriptedResult{Err: fmt.Errorf("%s", oomError)}, runner.ScriptedResult{})
	scene, defaults := sceneAndDefaults()
	res, err := RunVariant(context.Background(), fr, scene, defaults, 0, 7, "out.png", nil)
	if err != nil {
		t.Fatalf("a retry should have worked: %v", err)
	}
	calls := fr.Calls()
	if len(calls) != 2 {
		t.Fatalf("sd ran %d times, want 2", len(calls))
	}
	first, second := strings.Join(calls[0].Args, " "), strings.Join(calls[1].Args, " ")
	if !strings.Contains(first, "-W 1280 -H 704") || !strings.Contains(second, "-W 1088 -H 640") {
		t.Errorf("sizes: %q then %q", first, second)
	}
	if res.Resolution != "1088x640" {
		t.Errorf("result resolution = %q", res.Resolution)
	}
	if !strings.Contains(ShrunkNote("1280x704", res), "1088x640") || ShrunkNote("1088x640", res) != "" {
		t.Error("the note says what happened only when the size changed")
	}
}

func TestRunVariant_ItGivesUpAfterTwoRetriesAndNeverRetriesOtherFailures(t *testing.T) {
	fr := runner.NewFakeRunner()
	fr.Script(runner.ScriptedResult{Err: fmt.Errorf("%s", oomError)}, runner.ScriptedResult{Err: fmt.Errorf("%s", oomError)}, runner.ScriptedResult{Err: fmt.Errorf("%s", oomError)})
	scene, defaults := sceneAndDefaults()
	if _, err := RunVariant(context.Background(), fr, scene, defaults, 0, 7, "out.png", nil); err == nil || !IsOutOfMemory(err) {
		t.Fatalf("after %d retries the failure must come out: %v", MaxMemoryRetries, err)
	}
	if n := len(fr.Calls()); n != 1+MaxMemoryRetries {
		t.Errorf("sd ran %d times, want %d", n, 1+MaxMemoryRetries)
	}
	other := runner.NewFakeRunner()
	other.Script(runner.ScriptedResult{Err: fmt.Errorf("exit status 1 (stderr: unknown model format)")})
	if _, err := RunVariant(context.Background(), other, scene, defaults, 0, 7, "out.png", nil); err == nil || len(other.Calls()) != 1 {
		t.Errorf("another failure must not be retried: %v, %d calls", err, len(other.Calls()))
	}
}

func TestRunVariant_APictureFromAnImageKeepsItsSizeAndFailsAtOnce(t *testing.T) {
	fr := runner.NewFakeRunner()
	fr.Script(runner.ScriptedResult{Err: fmt.Errorf("%s", oomError)})
	scene, defaults := sceneAndDefaults()
	scene.InitImage = "me.png"
	if _, err := RunVariant(context.Background(), fr, scene, defaults, 0, 7, "out.png", nil); err == nil || len(fr.Calls()) != 1 {
		t.Errorf("an img2img picture was fitted to its size: %v, %d calls", err, len(fr.Calls()))
	}
}

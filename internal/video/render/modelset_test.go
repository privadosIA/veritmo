package render

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
)

func renderArgs(t *testing.T, d config.Defaults) []string {
	t.Helper()
	fr := runner.NewFakeRunner()
	scene := config.Scene{ID: "s1", Prompt: "a cat"}
	if _, err := RunVariant(context.Background(), fr, scene, d, 0, 42, "out.png", nil); err != nil {
		t.Fatalf("RunVariant() error = %v", err)
	}
	return fr.Calls()[0].Args
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func TestRunVariant_ModelSetReplacesCheckpoint(t *testing.T) {
	one := 1.0
	args := renderArgs(t, config.Defaults{
		Resolution: "1024x576", Sampler: "euler", Steps: 8,
		ModelSet: &config.ModelSet{DiffusionModel: "d.gguf", LLM: "l.gguf", VAE: "v.safetensors", CfgScale: &one},
	})
	for flag, want := range map[string]string{
		Flags.DiffusionModel: "d.gguf", Flags.LLM: "l.gguf", Flags.VAE: "v.safetensors", Flags.CfgScale: "1",
	} {
		if got := argValue(t, args, flag); got != want {
			t.Errorf("%s = %q, want %q", flag, got, want)
		}
	}
	if hasFlag(args, Flags.Model) {
		t.Errorf("args %v must not contain %s", args, Flags.Model)
	}
}

func TestRunVariant_ModelSetWithoutCfgScale(t *testing.T) {
	args := renderArgs(t, config.Defaults{
		Resolution: "512x512", Sampler: "euler", Steps: 8,
		ModelSet: &config.ModelSet{DiffusionModel: "d", LLM: "l", VAE: "v"},
	})
	if hasFlag(args, Flags.CfgScale) {
		t.Errorf("args %v must not contain %s", args, Flags.CfgScale)
	}
}

func TestBuildArgs_SingleCheckpointUnchanged(t *testing.T) {
	got, err := BuildArgs(Model{Checkpoint: "m.safetensors"}, "a cat", "", 42, "512x512", "euler", 20, "out.png")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-m", "m.safetensors", "-p", "a cat", "-s", "42", "--sampling-method", "euler",
		"--steps", "20", "-W", "512", "-H", "512", "-o", "out.png"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
}

func TestRunVariant_InitImageAddsTheStartingPictureAndStrength(t *testing.T) {
	d := config.Defaults{Model: "m.safetensors", Resolution: "512x512", Sampler: "euler", Steps: 10}
	run := func(scene config.Scene) []string {
		fr := runner.NewFakeRunner()
		if _, err := RunVariant(context.Background(), fr, scene, d, 0, 42, "out.png", nil); err != nil {
			t.Fatal(err)
		}
		return fr.Calls()[0].Args
	}
	plain := run(config.Scene{ID: "s1", Prompt: "a cat"})
	if hasFlag(plain, "-i") || hasFlag(plain, "--strength") {
		t.Fatalf("a scene without init_image keeps its arguments: %v", plain)
	}
	half := 0.4
	got := run(config.Scene{ID: "s1", Prompt: "a cat", InitImage: "photo.jpg", Strength: &half})
	joined := ""
	for _, a := range got {
		joined += a + " "
	}
	if !strings.Contains(joined, "-i tmp/scene_s1.init.png --strength 0.4 ") {
		t.Fatalf("%s", joined)
	}
	if def := run(config.Scene{ID: "s1", Prompt: "a cat", InitImage: "photo.jpg"}); !strings.Contains(strings.Join(def, " "), "--strength 0.6") {
		t.Fatalf("default strength: %v", def)
	}
	zero := 0.0
	if z := run(config.Scene{ID: "s1", Prompt: "a cat", InitImage: "photo.jpg", Strength: &zero}); !strings.Contains(strings.Join(z, " "), "--strength 0") {
		t.Fatalf("0 is a value: %v", z)
	}
}

func TestRunVariant_AnEditingSetPassesItsEncodersAndTheReferencePicture(t *testing.T) {
	one := 1.0
	fr := runner.NewFakeRunner()
	scene := config.Scene{ID: "s1", Prompt: "put a red cap on the man", InitImage: "foto.png"}
	d := config.Defaults{
		Resolution: "1024x576", Sampler: "euler", Steps: 20,
		ModelSet: &config.ModelSet{Edit: true, DiffusionModel: "k.gguf", VAE: "ae.safetensors", ClipL: "c.safetensors", T5XXL: "t.safetensors", CfgScale: &one},
	}
	if _, err := RunVariant(context.Background(), fr, scene, d, 0, 42, "out.png", nil); err != nil {
		t.Fatal(err)
	}
	args := fr.Calls()[0].Args
	joined := strings.Join(args, " ")
	for _, want := range []string{"--diffusion-model k.gguf", "--vae ae.safetensors", "--clip_l c.safetensors", "--t5xxl t.safetensors", "--diffusion-fa", "-r " + cache.InitImagePath("s1"), "-p put a red cap on the man", "--cfg-scale 1"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	for _, not := range []string{"--llm", " -i ", "--strength"} {
		if strings.Contains(joined, not) {
			t.Errorf("an editing set must not pass %q: %s", not, joined)
		}
	}
}

func TestRunVariant_AnOrdinarySetStillStartsFromThePicture(t *testing.T) {
	fr := runner.NewFakeRunner()
	scene := config.Scene{ID: "s1", Prompt: "a cat", InitImage: "foto.png"}
	d := config.Defaults{Resolution: "1024x576", Sampler: "euler", Steps: 8, ModelSet: &config.ModelSet{DiffusionModel: "d.gguf", LLM: "l.gguf", VAE: "v.safetensors"}}
	if _, err := RunVariant(context.Background(), fr, scene, d, 0, 42, "out.png", nil); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(fr.Calls()[0].Args, " ")
	if !strings.Contains(joined, "-i "+cache.InitImagePath("s1")) || !strings.Contains(joined, "--strength 0.6") || strings.Contains(joined, " -r ") {
		t.Errorf("args = %s", joined)
	}
}

func TestRunVariant_AQwenEditingSetPassesItsLLMAndTheMemoryFlags(t *testing.T) {
	cfg := 2.5
	fr := runner.NewFakeRunner()
	scene := config.Scene{ID: "s1", Prompt: "make the sky sunset orange", InitImage: "foto.png"}
	d := config.Defaults{
		Resolution: "1024x576", Sampler: "euler", Steps: 20,
		ModelSet: &config.ModelSet{Edit: true, DiffusionModel: "q.gguf", VAE: "qv.safetensors", LLM: "vl.gguf", LLMVision: "mm.gguf", CfgScale: &cfg},
	}
	if _, err := RunVariant(context.Background(), fr, scene, d, 0, 42, "out.png", nil); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(fr.Calls()[0].Args, " ")
	for _, want := range []string{"--diffusion-model q.gguf", "--vae qv.safetensors", "--llm vl.gguf", "--llm_vision mm.gguf", "--flow-shift 3",
		"--mmap", "--offload-to-cpu", "--diffusion-fa", "--vae-tiling", "-r " + cache.InitImagePath("s1"), "--cfg-scale 2.5", "--sampling-method euler"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	for _, not := range []string{"--clip_l", "--t5xxl", " -i ", "--strength"} {
		if strings.Contains(joined, not) {
			t.Errorf("a Qwen editing set must not pass %q: %s", not, joined)
		}
	}
}

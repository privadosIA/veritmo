package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
)

func imageSetup(t *testing.T) {
	t.Helper()
	chdirTemp(t)
	os.WriteFile("m.safetensors", []byte("stub"), 0o644)
	os.MkdirAll("tmp", 0o755)
	os.MkdirAll("sources", 0o755)
	os.WriteFile(filepath.Join("sources", "photo.jpg"), []byte("jpeg one"), 0o644)
}

func runImageScenes(t *testing.T, scenes []config.Scene, manifest *cache.Manifest) (*runner.FakeRunner, *cache.Manifest, string) {
	t.Helper()
	cfg := &config.Config{
		Project:  "demo",
		Output:   config.Output{File: "out/demo.mp4"},
		Defaults: config.Defaults{Model: "m.safetensors", Voice: "v.onnx", Resolution: "1280x720", Sampler: "euler", Steps: 30, FPS: 30},
		Scenes:   scenes,
	}
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		switch inv.Name {
		case "ffprobe":
			return runner.ScriptedResult{Stdout: []byte(`{"format":{"duration":"3.0"}}`)}
		case "piper":
			return runner.ScriptedResult{Stdout: []byte("RIFF....WAVE")}
		}
		return runner.ScriptedResult{}
	})
	var buf bytes.Buffer
	if manifest == nil {
		manifest = &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
	}
	if _, err := runVideo(context.Background(), cfg, events.New(&buf, true), fr,
		ttsRunnerAdapter{r: fr}, assembleRunnerAdapter{r: fr},
		manifest, cache.NewOnlyFilter(""), false, nil, "tmp", "", nil, nil); err != nil {
		t.Fatalf("%v\n%s", err, buf.String())
	}
	return fr, manifest, buf.String()
}

func callNames(fr *runner.FakeRunner) string {
	var n []string
	for _, c := range fr.Calls() {
		n = append(n, c.Name)
	}
	return strings.Join(n, " ")
}

func TestImageScene_FitsThePictureInsteadOfRendering(t *testing.T) {
	imageSetup(t)
	fr, _, out := runImageScenes(t, []config.Scene{{ID: "a", Image: "photo.jpg", TTSText: "Hello.", Speed: 1.0, Motion: &config.Motion{Type: config.MotionRotateCW}}}, nil)
	if got := callNames(fr); got != "piper ffprobe ffmpeg ffmpeg ffmpeg" {
		t.Fatalf("invocations = %s: no sd for a given picture", got)
	}
	fit := strings.Join(fr.Calls()[2].Args, " ")
	if !strings.Contains(fit, "-i sources/photo.jpg") || !strings.Contains(fit, "scale=1280:720:force_original_aspect_ratio=increase,crop=1280:720") || !strings.HasSuffix(fit, "tmp/scene_a.png") {
		t.Fatalf("fit = %s", fit)
	}
	clip := strings.Join(fr.Calls()[3].Args, " ")
	if !strings.Contains(clip, "-i tmp/scene_a.png") || !strings.Contains(clip, "rotate=a=") {
		t.Fatalf("the clip uses the fitted picture and rotates it: %s", clip)
	}
	if !strings.Contains(out, `"phase":"render"`) || !strings.Contains(out, "tmp/scene_a.png") {
		t.Fatalf("the fit is the render phase:\n%s", out)
	}
}

func TestImageScene_AMixedProjectCallsSDOnlyForPromptScenes(t *testing.T) {
	imageSetup(t)
	seed := 3
	fr, _, _ := runImageScenes(t, []config.Scene{
		{ID: "a", Image: "photo.jpg", TTSText: "One.", Speed: 1.0},
		{ID: "b", Prompt: "a city", TTSText: "Two.", Speed: 1.0, Seed: &seed},
	}, nil)
	sd := 0
	for _, c := range fr.Calls() {
		if c.Name == "sd" {
			sd++
		}
	}
	if sd != 1 {
		t.Fatalf("sd calls = %d: only the prompt scene renders (%s)", sd, callNames(fr))
	}
}

func TestImageScene_CachedUntilThePictureChanges(t *testing.T) {
	imageSetup(t)
	scenes := []config.Scene{{ID: "a", Image: "photo.jpg", TTSText: "Hello.", Speed: 1.0}}
	_, m, _ := runImageScenes(t, scenes, nil)
	fr, m, out := runImageScenes(t, scenes, m)
	if !strings.Contains(out, `"type":"skipped","scene_id":"a","phase":"render"`) || strings.Contains(callNames(fr), "ffmpeg ffmpeg ffmpeg") {
		t.Fatalf("unchanged must be cached: %s\n%s", callNames(fr), out)
	}
	// the prompt of a scene with a picture does not matter
	scenes[0].Prompt = "irrelevant now"
	_, m, out = runImageScenes(t, scenes, m)
	if !strings.Contains(out, `"type":"skipped","scene_id":"a","phase":"render"`) {
		t.Fatalf("editing the prompt must not re-fit:\n%s", out)
	}
	os.WriteFile(filepath.Join("sources", "photo.jpg"), []byte("another picture"), 0o644)
	fr, _, out = runImageScenes(t, scenes, m)
	if strings.Contains(out, `"type":"skipped","scene_id":"a","phase":"render"`) || !strings.Contains(callNames(fr), "ffmpeg") {
		t.Fatalf("a replaced picture must be fitted again:\n%s", out)
	}
}

func TestImageScene_PreflightNeedsSDOnlyWhenSomeSceneRenders(t *testing.T) {
	has := func(scenes ...config.Scene) bool {
		cfg := &config.Config{Output: config.Output{Mode: config.ModeVideo}, Scenes: scenes}
		for _, b := range RequiredBinariesFor(cfg, "") {
			if b == "sd" {
				return true
			}
		}
		return false
	}
	if has(config.Scene{ID: "a", Image: "p.jpg"}, config.Scene{ID: "b", Image: "q.png"}) {
		t.Fatal("every scene has a picture: sd is not needed")
	}
	if !has(config.Scene{ID: "a", Image: "p.jpg"}, config.Scene{ID: "b", Prompt: "x"}) {
		t.Fatal("a prompt scene needs sd")
	}
	if !has() {
		t.Fatal("no scenes keeps the old requirements")
	}
}

// --- img2img -----------------------------------------------------------------

func runInitImages(t *testing.T, scenes []config.Scene, manifest *cache.Manifest) (*runner.FakeRunner, *cache.Manifest, string) {
	t.Helper()
	cfg := &config.Config{
		Project:  "proj",
		Output:   config.Output{Mode: config.ModeImages},
		Defaults: config.Defaults{Model: "m.safetensors", Resolution: "512x512", Sampler: "euler", Steps: 20},
		Scenes:   scenes,
	}
	fr := fakeSD(t)
	var buf bytes.Buffer
	if manifest == nil {
		manifest = &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
	}
	final, err := runImages(context.Background(), cfg, events.New(&buf, true), fr, manifest, cache.NewOnlyFilter(""), false, nil, RandomSeed, "", nil, nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, buf.String())
	}
	return fr, manifest, final
}

func TestInitImage_FittedOnceThenEveryVariantStartsFromIt(t *testing.T) {
	imageSetup(t)
	three, seed, strength := 3, 10, 0.4
	fr, _, final := runInitImages(t, []config.Scene{{ID: "hero", Prompt: "watercolor", Seed: &seed, Variants: &three, InitImage: "photo.jpg", Strength: &strength}}, nil)
	var names []string
	for _, c := range fr.Calls() {
		names = append(names, c.Name)
	}
	if got := strings.Join(names, " "); got != "ffmpeg sd sd sd" {
		t.Fatalf("invocations = %s: one fit, then a render per variant", got)
	}
	fit := strings.Join(fr.Calls()[0].Args, " ")
	if !strings.Contains(fit, "-i sources/photo.jpg") || !strings.Contains(fit, "crop=512:512") || !strings.HasSuffix(fit, "tmp/scene_hero.init.png") {
		t.Fatalf("fit = %s", fit)
	}
	seeds := map[string]bool{}
	for _, c := range fr.Calls()[1:] {
		a := strings.Join(c.Args, " ")
		if !strings.Contains(a, "-i tmp/scene_hero.init.png --strength 0.4") {
			t.Fatalf("sd = %s", a)
		}
		seeds[argAfter(c.Args, "-s")] = true
	}
	if len(seeds) != 3 {
		t.Fatalf("each variant has its own seed: %v", seeds)
	}
	data, _ := os.ReadFile(final)
	var m struct {
		Images []struct {
			InitImage string   `json:"init_image"`
			Strength  *float64 `json:"strength"`
		}
	}
	json.Unmarshal(data, &m)
	if len(m.Images) != 3 || m.Images[0].InitImage != "photo.jpg" || m.Images[0].Strength == nil || *m.Images[0].Strength != 0.4 {
		t.Fatalf("%+v", m.Images)
	}
}

func TestInitImage_CachedUntilThePictureOrTheStrengthChange(t *testing.T) {
	imageSetup(t)
	seed, half := 10, 0.5
	scene := config.Scene{ID: "a", Prompt: "p", Seed: &seed, InitImage: "photo.jpg", Strength: &half}
	_, m, _ := runInitImages(t, []config.Scene{scene}, nil)
	fr, m, _ := runInitImages(t, []config.Scene{scene}, m)
	if len(fr.Calls()) != 0 {
		t.Fatalf("unchanged must be cached: %v", callNames(fr))
	}
	os.WriteFile(filepath.Join("sources", "photo.jpg"), []byte("another picture"), 0o644)
	fr, m, _ = runInitImages(t, []config.Scene{scene}, m)
	if callNames(fr) != "ffmpeg sd" {
		t.Fatalf("a replaced picture re-renders: %s", callNames(fr))
	}
	other := 0.9
	scene.Strength = &other
	fr, _, _ = runInitImages(t, []config.Scene{scene}, m)
	if callNames(fr) != "ffmpeg sd" {
		t.Fatalf("a new strength re-renders: %s", callNames(fr))
	}
}

func TestInitImage_AVideoSceneRendersFromItAndFeedsTheClip(t *testing.T) {
	imageSetup(t)
	seed := 3
	fr, _, _ := runImageScenes(t, []config.Scene{{ID: "a", Prompt: "oil painting", InitImage: "photo.jpg", TTSText: "Hello.", Speed: 1.0, Seed: &seed}}, nil)
	if got := callNames(fr); got != "piper ffprobe ffmpeg sd ffmpeg ffmpeg" {
		t.Fatalf("invocations = %s", got)
	}
	if sd := strings.Join(fr.Calls()[3].Args, " "); !strings.Contains(sd, "-i tmp/scene_a.init.png --strength 0.6") {
		t.Fatalf("sd = %s", sd)
	}
}

func TestInitImage_ImagesModePreflightNeedsFFmpeg(t *testing.T) {
	cfg := &config.Config{Output: config.Output{Mode: config.ModeImages}, Scenes: []config.Scene{{ID: "a", Prompt: "p", InitImage: "p.jpg"}}}
	has := func(c *config.Config) bool {
		for _, b := range RequiredBinariesFor(c, "") {
			if b == "ffmpeg" {
				return true
			}
		}
		return false
	}
	if !has(cfg) {
		t.Fatal("the fit needs ffmpeg")
	}
	cfg.Scenes[0].InitImage = ""
	if has(cfg) {
		t.Fatal("images mode alone does not need ffmpeg")
	}
}

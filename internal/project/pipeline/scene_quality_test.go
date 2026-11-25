package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
)

// sdCallFor returns the arguments of the sd render whose output is that PNG.
func sdCallFor(t *testing.T, fr *runner.FakeRunner, png string) string {
	t.Helper()
	for _, c := range fr.Calls() {
		joined := strings.Join(c.Args, " ")
		if c.Name == "sd" && strings.HasSuffix(joined, png) {
			return joined
		}
	}
	t.Fatalf("no sd call writes %s", png)
	return ""
}

func TestSceneQuality_VideoScenesRenderAtTheirOwnSizeAndTheClipKeepsTheProjects(t *testing.T) {
	cfg := animateProject(t)
	cfg.Scenes[1].Render = &config.SceneRender{Resolution: "1280x704", Steps: 12} // "still"
	cfg.Scenes[0].Animate = false
	m := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
	fr := animateRunner(false)
	if _, err := runAnimateVideo(t, cfg, fr, m); err != nil {
		t.Fatal(err)
	}
	if got := sdCallFor(t, fr, "tmp/scene_moving.png"); !strings.Contains(got, "-W 1280 -H 720") || !strings.Contains(got, "--steps 30") {
		t.Errorf("a scene without a block renders at the project's: %s", got)
	}
	if got := sdCallFor(t, fr, "tmp/scene_still.png"); !strings.Contains(got, "-W 1280 -H 704") || !strings.Contains(got, "--steps 12") {
		t.Errorf("the scene with a block renders at its own: %s", got)
	}
	var stillClip, movingClip string
	for _, c := range fr.Calls() {
		joined := strings.Join(c.Args, " ")
		if c.Name == "ffmpeg" && strings.Contains(joined, "scene_still.png") && strings.Contains(joined, "-vf") {
			stillClip = joined
		}
		if c.Name == "ffmpeg" && strings.Contains(joined, "scene_moving.png") && strings.Contains(joined, "-vf") {
			movingClip = joined
		}
	}
	if !strings.Contains(stillClip, "scale=1280:720:force_original_aspect_ratio=increase,crop=1280:720") {
		t.Errorf("the clip of the scene at another size is cover-cropped to the project's: %s", stillClip)
	}
	if strings.Contains(movingClip, "force_original_aspect_ratio") {
		t.Errorf("a scene at the project's size is untouched: %s", movingClip)
	}
}

func TestSceneQuality_OnlyTheChangedSceneIsRegenerated(t *testing.T) {
	cfg := animateProject(t)
	m := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
	if _, err := runAnimateVideo(t, cfg, animateRunner(false), m); err != nil {
		t.Fatal(err)
	}
	// raise the steps of one scene only
	cfg.Scenes[1].Render = &config.SceneRender{Steps: 45}
	fr := animateRunner(false)
	if _, err := runAnimateVideo(t, cfg, fr, m); err != nil {
		t.Fatal(err)
	}
	var renders []string
	for _, c := range fr.Calls() {
		if c.Name == "sd" && !strings.Contains(strings.Join(c.Args, " "), "vid_gen") {
			renders = append(renders, strings.Join(c.Args, " "))
		}
	}
	if len(renders) != 1 || !strings.Contains(renders[0], "scene_still.png") || !strings.Contains(renders[0], "--steps 45") {
		t.Errorf("only the scene whose steps changed must render again: %v", renders)
	}
}

func TestSceneQuality_NoBlocksKeepTheCacheKeys(t *testing.T) {
	cfg := animateProject(t)
	before := cache.ImageHash(SceneImageParams(cfg, cfg.Scenes[1], 7, 0))
	cfg.Scenes[1].Render = &config.SceneRender{} // an empty block is the same as none
	if got := cache.ImageHash(SceneImageParams(cfg, cfg.Scenes[1], 7, 0)); got != before {
		t.Error("an empty block must not change the image hash")
	}
	cfg.Scenes[1].Render = &config.SceneRender{Steps: 12}
	if got := cache.ImageHash(SceneImageParams(cfg, cfg.Scenes[1], 7, 0)); got == before {
		t.Error("a different step count must change the image hash")
	}
}

func TestSceneQuality_EachAnimatedSceneHasItsOwnDurationAndCacheKey(t *testing.T) {
	cfg := animateProject(t)
	cfg.Scenes[1].Animate = true
	cfg.Scenes[1].Animation = &config.SceneAnimation{Frames: 49, Steps: 12}
	a, b := cfg.Scenes[0], cfg.Scenes[1]
	if got := animatedDuration(cfg.Defaults.Animate, a); got != 4 { // (2*33-2)/16
		t.Errorf("project frames: duration = %v, want 4", got)
	}
	if got := animatedDuration(cfg.Defaults.Animate, b); got != 6 { // (2*49-2)/16
		t.Errorf("49 frames: duration = %v, want 6", got)
	}
	pa, pb := SceneAnimateParams(cfg, a, ""), SceneAnimateParams(cfg, b, "")
	if pb.Frames != 49 || pb.Steps != 12 || pa.Frames != 33 || cache.AnimateHash(pa) == cache.AnimateHash(pb) {
		t.Errorf("params a=%+v b=%+v", pa, pb)
	}
}

func TestSceneQuality_ImagesModeManifestRecordsEachScenesResolution(t *testing.T) {
	imageSetup(t)
	cfg := &config.Config{
		Project:  "proj",
		Output:   config.Output{Mode: config.ModeImages},
		Defaults: config.Defaults{Model: "m.safetensors", Resolution: "512x512", Sampler: "euler", Steps: 20},
		Scenes: []config.Scene{
			{ID: "plain", Prompt: "a"},
			{ID: "big", Prompt: "b", Render: &config.SceneRender{Resolution: "768x768", Steps: 30}},
		},
	}
	fr := fakeSD(t)
	var buf bytes.Buffer
	manifest := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
	final, err := runImages(context.Background(), cfg, events.New(&buf, true), fr, manifest, cache.NewOnlyFilter(""), false, nil, RandomSeed, "", nil, nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, buf.String())
	}
	data, err := os.ReadFile(final)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Scenes []struct {
			SceneID    string `json:"scene_id"`
			Resolution [2]int `json:"resolution"`
			Steps      int    `json:"steps"`
		} `json:"scenes"`
		Images []struct {
			SceneID    string `json:"scene_id"`
			Resolution [2]int `json:"resolution"`
			Steps      int    `json:"steps"`
		} `json:"images"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	entries := m.Images
	if len(entries) == 0 {
		entries = m.Scenes
	}
	got := map[string][3]int{}
	for _, e := range entries {
		got[e.SceneID] = [3]int{e.Resolution[0], e.Resolution[1], e.Steps}
	}
	if got["plain"] != [3]int{512, 512, 20} || got["big"] != [3]int{768, 768, 30} {
		t.Errorf("manifest = %v (%s)", got, data)
	}
}

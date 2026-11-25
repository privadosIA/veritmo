package pipeline

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
)

func clipSetup(t *testing.T) {
	t.Helper()
	imageSetup(t)
	os.WriteFile(filepath.Join("sources", "stand-up.mp4"), []byte("video one"), 0o644)
}

func silentClipScene(id string) config.Scene {
	one := 1.5
	return config.Scene{ID: id, Clip: "stand-up.mp4", SilenceSeconds: &one, Speed: 1.0}
}

func TestClipScene_UsesTheFileAndNeverRenders(t *testing.T) {
	clipSetup(t)
	fr, _, out := runImageScenes(t, []config.Scene{silentClipScene("a")}, nil)
	for _, c := range fr.Calls() {
		if c.Name == "sd" || c.Name == "piper" {
			t.Fatalf("a scene with its own clip and silence needs neither sd nor piper: %s", callNames(fr))
		}
	}
	calls := fr.Calls()
	poster := strings.Join(calls[0].Args, " ")
	if calls[0].Name != "ffmpeg" || !strings.Contains(poster, "-i sources/stand-up.mp4") || !strings.HasSuffix(poster, "tmp/scene_a.png") {
		t.Fatalf("the render phase takes the first frame: %s", poster)
	}
	var clip string
	for _, c := range calls {
		if c.Name == "ffmpeg" && strings.HasSuffix(strings.Join(c.Args, " "), "tmp/scene_a.mp4") {
			clip = strings.Join(c.Args, " ")
		}
	}
	if !strings.Contains(clip, "-stream_loop -1 -i sources/stand-up.mp4") || !strings.Contains(clip, "-t 1.500000") || !strings.Contains(clip, "crop=1280:720") {
		t.Fatalf("the clip is cut from the file to the scene's length: %s", clip)
	}
	if strings.Contains(out, `"phase":"animate"`) {
		t.Fatalf("no animate phase:\n%s", out)
	}
}

func TestClipScene_CachedUntilTheFileChanges(t *testing.T) {
	clipSetup(t)
	scenes := []config.Scene{silentClipScene("a")}
	_, m, _ := runImageScenes(t, scenes, nil)
	fr, m, out := runImageScenes(t, scenes, m)
	if !strings.Contains(out, `"type":"skipped","scene_id":"a","phase":"render"`) || callNames(fr) != "ffmpeg" {
		t.Fatalf("unchanged must be cached (only the final concat runs): %s\n%s", callNames(fr), out)
	}
	os.WriteFile(filepath.Join("sources", "stand-up.mp4"), []byte("video two"), 0o644)
	fr, _, out = runImageScenes(t, scenes, m)
	if strings.Contains(out, `"type":"skipped","scene_id":"a","phase":"render"`) || !strings.Contains(callNames(fr), "ffmpeg") {
		t.Fatalf("a replaced file must be made again:\n%s", out)
	}
}

func TestClipScene_NeedsNoSDInPreflight(t *testing.T) {
	cfg := &config.Config{Output: config.Output{Mode: config.ModeVideo}, Scenes: []config.Scene{{ID: "a", Clip: "x.mp4"}}}
	for _, b := range RequiredBinariesFor(cfg, "") {
		if b == "sd" {
			t.Fatal("every scene has its own clip: sd is not needed")
		}
	}
}

func TestKeptClips_KeptAfterTheRunAndPrunedWhenTheSceneIsGone(t *testing.T) {
	clipSetup(t)
	// the runner writes nothing, so give the scene clips their files as a run would
	runImageScenes(t, []config.Scene{silentClipScene("a"), silentClipScene("b")}, nil)
	os.MkdirAll("tmp", 0o755)
	for _, id := range []string{"a", "b"} {
		os.WriteFile(filepath.Join("tmp", "scene_"+id+".mp4"), []byte("clip "+id), 0o644)
	}
	if err := keepClip("", "a", "tmp/scene_a.mp4"); err != nil {
		t.Fatal(err)
	}
	if err := keepClip("", "b", "tmp/scene_b.mp4"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join("out", "clips", "a.mp4"))
	if err != nil || string(got) != "clip a" {
		t.Fatalf("kept clip = %q, %v", got, err)
	}
	if err := keepClip("", "ghost", "tmp/scene_ghost.mp4"); err != nil {
		t.Fatalf("a clip that was never made is not an error: %v", err)
	}
	// scene b is removed from the project: the next run prunes its clip
	os.WriteFile(filepath.Join("out", "clips", ".clip-half"), []byte("x"), 0o644)
	runImageScenes(t, []config.Scene{silentClipScene("a")}, nil)
	entries, _ := os.ReadDir(filepath.Join("out", "clips"))
	if len(entries) != 1 || entries[0].Name() != "a.mp4" {
		t.Fatalf("out/clips = %v: only the live scene's clip stays", entries)
	}
}

func trimmedClipScene(start, end float64) config.Scene {
	s := silentClipScene("a")
	s.ClipStart = &start
	if end > 0 {
		s.ClipEnd = &end
	}
	return s
}

func TestClipScene_TrimCutsThePartAndTakesTheFrameAtItsStart(t *testing.T) {
	clipSetup(t) // the fake ffprobe says every file lasts 3.0 s
	fr, _, _ := runImageScenes(t, []config.Scene{trimmedClipScene(0.5, 1.5)}, nil)
	var poster, cut, clip string
	for _, c := range fr.Calls() {
		line := strings.Join(c.Args, " ")
		switch {
		case strings.HasSuffix(line, "tmp/scene_a.png"):
			poster = line
		case strings.HasSuffix(line, "tmp/scene_a.cut.mp4"):
			cut = line
		case strings.HasSuffix(line, "tmp/scene_a.mp4"):
			clip = line
		}
	}
	if !strings.Contains(poster, "-ss 0.500 -i sources/stand-up.mp4") {
		t.Fatalf("the scene picture is the frame at the start of the part: %s", poster)
	}
	if !strings.Contains(cut, "-ss 0.500 -t 1.000 -i sources/stand-up.mp4") {
		t.Fatalf("the part is cut: %s", cut)
	}
	if !strings.Contains(clip, "-stream_loop -1 -i tmp/scene_a.cut.mp4") {
		t.Fatalf("the scene is built from the cut part: %s", clip)
	}
}

func TestClipScene_TrimChangesTheCacheKeyAndAWholeClipDoesNot(t *testing.T) {
	clipSetup(t)
	whole := []config.Scene{silentClipScene("a")}
	_, m, _ := runImageScenes(t, whole, nil)
	fr, m, _ := runImageScenes(t, whole, m)
	if callNames(fr) != "ffmpeg" {
		t.Fatalf("a whole clip stays cached: %s", callNames(fr))
	}
	fr, _, out := runImageScenes(t, []config.Scene{trimmedClipScene(0.5, 0)}, m)
	if strings.Contains(out, `"type":"skipped","scene_id":"a","phase":"render"`) || !strings.Contains(callNames(fr), "ffmpeg ffmpeg") {
		t.Fatalf("setting the start makes the scene again:\n%s", out)
	}
}

func TestClipScene_TrimBeyondTheFileStopsTheRun(t *testing.T) {
	clipSetup(t)
	cfg := &config.Config{
		Project:  "demo",
		Output:   config.Output{File: "out/demo.mp4"},
		Defaults: config.Defaults{Voice: "v.onnx", Resolution: "1280x720", FPS: 30},
		Scenes:   []config.Scene{trimmedClipScene(5, 0)},
	}
	if err := runVideoWith(t, cfg); err == nil || !strings.Contains(err.Error(), "beyond the end") {
		t.Fatalf("err = %v", err)
	}
}

// runVideoWith runs cfg with a fake runner whose ffprobe says every file lasts 3 s, and returns the error.
func runVideoWith(t *testing.T, cfg *config.Config) error {
	t.Helper()
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if inv.Name == "ffprobe" {
			return runner.ScriptedResult{Stdout: []byte(`{"format":{"duration":"3.0"}}`)}
		}
		return runner.ScriptedResult{}
	})
	var buf bytes.Buffer
	_, err := runVideo(context.Background(), cfg, events.New(&buf, true), fr,
		ttsRunnerAdapter{r: fr}, assembleRunnerAdapter{r: fr},
		&cache.Manifest{Scenes: map[string]cache.SceneEntry{}}, cache.NewOnlyFilter(""), false, nil, "tmp", "", nil, nil)
	return err
}

func TestClipScene_SpeedIsExactAndPartOfTheCacheKey(t *testing.T) {
	clipSetup(t)
	half := 0.5
	scene := silentClipScene("a")
	scene.ClipSpeed = &half
	fr, m, _ := runImageScenes(t, []config.Scene{scene}, nil)
	var clip string
	for _, c := range fr.Calls() {
		if line := strings.Join(c.Args, " "); strings.HasSuffix(line, "tmp/scene_a.mp4") {
			clip = line
		}
	}
	if !strings.Contains(clip, "setpts=2.000000*PTS") {
		t.Fatalf("half speed: %s", clip)
	}
	fr, m, _ = runImageScenes(t, []config.Scene{scene}, m)
	if callNames(fr) != "ffmpeg" {
		t.Fatalf("the same speed stays cached: %s", callNames(fr))
	}
	two := 2.0
	scene.ClipSpeed = &two
	fr, _, out := runImageScenes(t, []config.Scene{scene}, m)
	if strings.Contains(out, `"type":"skipped","scene_id":"a","phase":"clip"`) || !strings.Contains(callNames(fr), "ffmpeg ffmpeg") {
		t.Fatalf("another speed makes the clip again:\n%s", out)
	}
}

func runWithFade(t *testing.T, project float64, scenes []config.Scene) (*runner.FakeRunner, string) {
	t.Helper()
	cfg := &config.Config{
		Project:  "demo",
		Output:   config.Output{File: "out/demo.mp4"},
		Defaults: config.Defaults{Voice: "v.onnx", Resolution: "1280x720", FPS: 30, TransitionSeconds: project},
		Scenes:   scenes,
	}
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if inv.Name == "ffprobe" {
			return runner.ScriptedResult{Stdout: []byte(`{"format":{"duration":"3.0"}}`)}
		}
		return runner.ScriptedResult{}
	})
	var buf bytes.Buffer
	if _, err := runVideo(context.Background(), cfg, events.New(&buf, true), fr,
		ttsRunnerAdapter{r: fr}, assembleRunnerAdapter{r: fr},
		&cache.Manifest{Scenes: map[string]cache.SceneEntry{}}, cache.NewOnlyFilter(""), false, nil, "tmp", "", nil, nil); err != nil {
		t.Fatalf("%v\n%s", err, buf.String())
	}
	return fr, buf.String()
}

func lastJoinCall(fr *runner.FakeRunner) string {
	calls := fr.Calls()
	return strings.Join(calls[len(calls)-1].Args, " ")
}

func TestTransitions_ProjectFadeSceneOverrideAndLimit(t *testing.T) {
	clipSetup(t)
	zero, long := 0.0, 5.0
	a, b, c := silentClipScene("a"), silentClipScene("b"), silentClipScene("c") // 1.5 s each
	fr, out := runWithFade(t, 0.4, []config.Scene{a, b, c})
	join := lastJoinCall(fr)
	if !strings.Contains(join, "-filter_complex") || strings.Contains(join, "-f concat") ||
		!strings.Contains(join, "xfade=transition=fade:duration=0.400:offset=1.100") {
		t.Fatalf("the project fade joins every pair: %s", join)
	}
	if strings.Contains(out, `"type":"warning"`) {
		t.Fatalf("no warning when the fade fits:\n%s", out)
	}

	// the scene's own value overrides: a cut into b, a too long fade into c (limited to half of 1.5 s)
	b.TransitionSeconds, c.TransitionSeconds = &zero, &long
	fr, out = runWithFade(t, 0.4, []config.Scene{a, b, c})
	join = lastJoinCall(fr)
	if !strings.Contains(join, "concat=n=2:v=1:a=1") || !strings.Contains(join, "duration=0.750") {
		t.Fatalf("a cut into b and a limited fade into c: %s", join)
	}
	if !strings.Contains(out, "was shortened to 0.75 s") {
		t.Fatalf("the limit is reported:\n%s", out)
	}

	// no fades anywhere: the demuxer with stream copy, as before
	fr, _ = runWithFade(t, 0, []config.Scene{a, silentClipScene("b")})
	if join = lastJoinCall(fr); !strings.Contains(join, "-f concat") || strings.Contains(join, "filter_complex") {
		t.Fatalf("no fades keeps the stream copy: %s", join)
	}
}

func TestManifest_SavedSceneBySceneSoAnInterruptedRunKeepsWhatItMade(t *testing.T) {
	clipSetup(t)
	cfg := &config.Config{
		Project:  "demo",
		Output:   config.Output{File: "out/demo.mp4"},
		Defaults: config.Defaults{Voice: "v.onnx", Resolution: "1280x720", FPS: 30},
		Scenes:   []config.Scene{silentClipScene("a"), silentClipScene("b")},
	}
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		line := strings.Join(inv.Args, " ")
		if inv.Name == "ffprobe" {
			return runner.ScriptedResult{Stdout: []byte(`{"format":{"duration":"3.0"}}`)}
		}
		if strings.HasSuffix(line, "tmp/scene_b.mp4") { // scene b is interrupted while it is made
			return runner.ScriptedResult{Err: errors.New("signal: killed")}
		}
		return runner.ScriptedResult{}
	})
	var buf bytes.Buffer
	manifest := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
	_, err := runVideo(context.Background(), cfg, events.New(&buf, true), fr,
		ttsRunnerAdapter{r: fr}, assembleRunnerAdapter{r: fr}, manifest, cache.NewOnlyFilter(""), false, nil, "tmp", "", nil, nil)
	if err == nil {
		t.Fatal("scene b fails, so the run fails")
	}
	saved, lerr := cache.LoadManifest(cache.ManifestPath())
	if lerr != nil {
		t.Fatal(lerr)
	}
	if _, ok := saved.Scenes["a"]; !ok {
		t.Fatalf("scene a was finished before the failure, so the manifest on disk has it: %+v", saved.Scenes)
	}
	if _, ok := saved.Scenes["b"]; ok {
		t.Fatal("scene b never finished: it must not be recorded as made")
	}
}

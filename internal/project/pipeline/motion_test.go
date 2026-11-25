package pipeline

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
	"veritmo/internal/video/motion"
)

const testPage = `<!doctype html><html><head><style>html,body{margin:0;background:#003}
#b{position:absolute;top:20px;width:40px;height:40px;background:#f00;animation:go 2s linear both}@keyframes go{from{left:0}to{left:150px}}</style></head><body><div id="b"></div></body></html>`

// motionStubs replaces the browser lookup and the renderer; jobs collects what was rendered.
func motionStubs(t *testing.T) *[]motion.Job {
	t.Helper()
	jobs := &[]motion.Job{}
	oldFind, oldRender, oldWriter := findBrowser, renderMotion, newMotionWriter
	findBrowser = func() (motion.Browser, error) { return motion.Browser{Path: "/fake/chrome", Version: "1.2.3"}, nil }
	renderMotion = func(ctx context.Context, j motion.Job) (int, []string, error) {
		*jobs = append(*jobs, j)
		if err := os.MkdirAll(filepath.Dir(j.Out), 0o755); err != nil {
			return 0, nil, err
		}
		return motion.Frames(j.Page.Duration, j.FPS), nil, os.WriteFile(j.Out, []byte("video"), 0o644)
	}
	newMotionWriter = func(context.Context) (motion.Writer, motion.ModelInfo, error) {
		t.Error("the model must not be asked: the pages are stored")
		return nil, motion.ModelInfo{}, nil
	}
	t.Cleanup(func() { findBrowser, renderMotion, newMotionWriter = oldFind, oldRender, oldWriter })
	return jobs
}

func storePages(t *testing.T, id string, scene, overlay bool) {
	t.Helper()
	if scene {
		if err := motion.Store("", id, false, testPage); err != nil {
			t.Fatal(err)
		}
	}
	if overlay {
		if err := motion.Store("", id, true, testPage); err != nil {
			t.Fatal(err)
		}
	}
}

func silent(sec float64) *float64 { return &sec }

func motionScene(id string) config.Scene {
	return config.Scene{ID: id, MotionGraphic: "three bullets", SilenceSeconds: silent(1.5), Speed: 1.0}
}

func lastClipCall(t *testing.T, fr *runner.FakeRunner, id string) string {
	t.Helper()
	for _, c := range fr.Calls() {
		line := strings.Join(c.Args, " ")
		if c.Name == "ffmpeg" && strings.HasSuffix(line, "tmp/scene_"+id+".mp4") {
			return line
		}
	}
	t.Fatalf("no clip call for %s: %s", id, callNames(fr))
	return ""
}

func TestMotionScene_PlaysThePageForTheSceneLengthAndNeverRendersWithSD(t *testing.T) {
	imageSetup(t)
	jobs := motionStubs(t)
	storePages(t, "a", true, false)
	fr, _, out := runImageScenes(t, []config.Scene{motionScene("a")}, nil)
	for _, c := range fr.Calls() {
		if c.Name == "sd" || c.Name == "piper" {
			t.Fatalf("a silent motion scene needs neither sd nor piper: %s", callNames(fr))
		}
	}
	if len(*jobs) != 1 {
		t.Fatalf("one page rendered, got %d", len(*jobs))
	}
	j := (*jobs)[0]
	if j.Page.Duration != 1.5 || j.Page.Transparent || j.FPS != 30 || j.Page.Width != 1280 || j.Page.Height != 720 || j.Browser != "/fake/chrome" || j.Out != "tmp/scene_a.motion.mp4" {
		t.Fatalf("job = %+v", j)
	}
	poster := strings.Join(fr.Calls()[0].Args, " ")
	if !strings.Contains(poster, "-i tmp/scene_a.motion.mp4") || !strings.HasSuffix(poster, "tmp/scene_a.png") {
		t.Fatalf("the first frame of the video is the scene picture: %s", poster)
	}
	clip := lastClipCall(t, fr, "a")
	if !strings.Contains(clip, "-stream_loop -1 -i tmp/scene_a.motion.mp4") || !strings.Contains(clip, "-t 1.500000") || !strings.Contains(clip, "setpts=1.000000*PTS") {
		t.Fatalf("the clip takes the video at its own speed: %s", clip)
	}
	if strings.Contains(out, `"phase":"animate"`) || strings.Contains(out, `"phase":"motion"`) {
		t.Fatalf("no animate phase, and no motion phase when the pages were stored:\n%s", out)
	}
}

func TestMotionScene_CachedUntilThePageOrTheLengthChanges(t *testing.T) {
	imageSetup(t)
	jobs := motionStubs(t)
	storePages(t, "a", true, false)
	scenes := []config.Scene{motionScene("a")}
	_, m, _ := runImageScenes(t, scenes, nil)
	_, m, out := runImageScenes(t, scenes, m)
	if len(*jobs) != 1 || !strings.Contains(out, `"type":"skipped","scene_id":"a","phase":"render"`) {
		t.Fatalf("unchanged must be cached (%d renders):\n%s", len(*jobs), out)
	}
	// a hand edit of the page
	if err := motion.Store("", "a", false, strings.ReplaceAll(testPage, "#003", "#030")); err != nil {
		t.Fatal(err)
	}
	_, m, _ = runImageScenes(t, scenes, m)
	if len(*jobs) != 2 {
		t.Fatalf("an edited page must be rendered again, renders = %d", len(*jobs))
	}
	// a longer silence is a longer video
	scenes[0].SilenceSeconds = silent(2.5)
	runImageScenes(t, scenes, m)
	if len(*jobs) != 3 || (*jobs)[2].Page.Duration != 2.5 {
		t.Fatalf("a longer scene must be rendered again at the new length: %+v", *jobs)
	}
}

func TestMotionScene_LongerNarrationIsRenderedAgainAtItsLength(t *testing.T) {
	imageSetup(t)
	jobs := motionStubs(t)
	storePages(t, "a", true, false)
	narrated := func(text string) []config.Scene {
		return []config.Scene{{ID: "a", MotionGraphic: "bullets", TTSText: text, Speed: 1.0}}
	}
	_, m, _ := runImageScenes(t, narrated("One."), nil)
	runImageScenes(t, narrated("One. And now much more to say."), m)
	if len(*jobs) != 2 || (*jobs)[0].Page.Duration != 3 { // the fake ffprobe says every audio lasts 3.0 s
		t.Fatalf("a changed narration must render the page again at the probed length: %+v", *jobs)
	}
}

func TestMotionScene_NeedsNoSDInPreflight(t *testing.T) {
	cfg := &config.Config{Output: config.Output{Mode: config.ModeVideo}, Scenes: []config.Scene{{ID: "a", MotionGraphic: "x"}}}
	for _, b := range RequiredBinariesFor(cfg, "") {
		if b == "sd" {
			t.Fatal("every scene is a motion graphic: sd is not needed")
		}
	}
}

func TestOverlay_IsDrawnOverTheScene(t *testing.T) {
	imageSetup(t)
	jobs := motionStubs(t)
	storePages(t, "a", false, true)
	ext, err := motion.OverlayExt()
	if err != nil {
		t.Skipf("no alpha encoder here: %v", err)
	}
	seed := 3
	sc := config.Scene{ID: "a", Prompt: "a city", Seed: &seed, SilenceSeconds: silent(4), Speed: 1.0,
		Overlay: &config.Overlay{Text: "Chapter 1", Position: config.OverlayTop, Start: fp(1), End: fp(3)}}
	fr, _, _ := runImageScenes(t, []config.Scene{sc}, nil)
	if len(*jobs) != 1 {
		t.Fatalf("one overlay rendered, got %d", len(*jobs))
	}
	j := (*jobs)[0]
	if !j.Page.Transparent || j.Page.Duration != 2 || j.Out != "tmp/scene_a.overlay."+ext {
		t.Fatalf("the overlay is a transparent video as long as its window: %+v", j)
	}
	clip := lastClipCall(t, fr, "a")
	for _, want := range []string{"-i tmp/scene_a.overlay." + ext, "-filter_complex", "overlay=0:0", "between(t,1.000000,3.000000)", "-map [v] -map 1:a"} {
		if !strings.Contains(clip, want) {
			t.Errorf("the clip must contain %q: %s", want, clip)
		}
	}
	if ext == "webm" && !strings.Contains(clip, "-c:v libvpx-vp9 -i tmp/scene_a.overlay.webm") {
		t.Errorf("a VP9 overlay is decoded by libvpx to keep its alpha: %s", clip)
	}
}

func TestOverlay_AlsoOverAnOwnClipAndAMotionScene(t *testing.T) {
	clipSetup(t)
	motionStubs(t)
	storePages(t, "c", false, true)
	storePages(t, "m", true, true)
	if _, err := motion.OverlayExt(); err != nil {
		t.Skip(err)
	}
	c := silentClipScene("c")
	c.SilenceSeconds = silent(3)
	c.Overlay = &config.Overlay{Text: "Live"}
	m := motionScene("m")
	m.SilenceSeconds = silent(3)
	m.Overlay = &config.Overlay{Text: "Title"}
	fr, _, _ := runImageScenes(t, []config.Scene{c, m}, nil)
	for _, id := range []string{"c", "m"} {
		if clip := lastClipCall(t, fr, id); !strings.Contains(clip, "overlay=0:0") {
			t.Errorf("scene %s: the overlay is composited:\n%s", id, clip)
		}
	}
}

func TestOverlay_BeyondTheSceneIsSkippedWithAWarning(t *testing.T) {
	imageSetup(t)
	jobs := motionStubs(t)
	storePages(t, "a", false, true)
	seed := 3
	// the fake ffprobe says the narration lasts 3 s; the overlay starts at 5
	sc := config.Scene{ID: "a", Prompt: "x", Seed: &seed, TTSText: "Hello.", Speed: 1.0, Overlay: &config.Overlay{Text: "late", Start: fp(5)}}
	fr, _, out := runImageScenes(t, []config.Scene{sc}, nil)
	if len(*jobs) != 0 || !strings.Contains(out, `"type":"warn"`) || !strings.Contains(out, "not drawn") {
		t.Fatalf("an overlay that starts after the scene is not drawn, with a warning (%d renders):\n%s", len(*jobs), out)
	}
	if strings.Contains(lastClipCall(t, fr, "a"), "overlay=") {
		t.Fatal("no overlay in the clip")
	}
}

func TestOverlay_ChangesTheClipHashOnlyWhenItsPageOrRangeChanges(t *testing.T) {
	imageSetup(t)
	storePages(t, "a", false, true)
	cfg := &config.Config{Defaults: config.Defaults{Resolution: "1280x720", FPS: 30}}
	sc := config.Scene{ID: "a", Prompt: "x", SilenceSeconds: silent(4), Overlay: &config.Overlay{Text: "T"}}
	base := SceneClipHash(cfg, sc, "")
	plain := sc
	plain.Overlay = nil
	if SceneClipHash(cfg, plain, "") != cache.ClipHash(SceneClipParams(cfg, plain, "")) || base == SceneClipHash(cfg, plain, "") {
		t.Fatal("a scene without overlay keeps its old hash; one with overlay has another")
	}
	motion.Store("", "a", true, testPage+"<!-- edited -->")
	if SceneClipHash(cfg, sc, "") == base {
		t.Fatal("an edited overlay page changes the clip hash")
	}
	moved := sc
	moved.Overlay = &config.Overlay{Text: "T", Position: config.OverlayTop}
	if SceneClipHash(cfg, moved, "") == SceneClipHash(cfg, sc, "") {
		t.Fatal("a moved overlay changes the clip hash")
	}
}

// With a real Chrome the model is asked only for the pages that are missing, and the run
// reports the work under a "motion" phase.
func TestMotionScene_TheModelIsAskedOnlyForMissingPages(t *testing.T) {
	b, err := motion.Find("")
	if err != nil {
		t.Skip("no Chrome or Chromium on this machine")
	}
	imageSetup(t)
	jobs := motionStubs(t)
	findBrowser = func() (motion.Browser, error) { return b, nil }
	asked := 0
	newMotionWriter = func(context.Context) (motion.Writer, motion.ModelInfo, error) {
		return writerFunc(func(system, user string) (string, error) { asked++; return testPage, nil }), motion.ModelInfo{Provider: "Ollama", Model: "m:1b"}, nil
	}
	scenes := []config.Scene{motionScene("a")}
	_, m, out := runImageScenes(t, scenes, nil)
	if asked != 1 || !strings.Contains(out, `"phase":"motion"`) {
		t.Fatalf("the model is asked once for the missing page (asked %d):\n%s", asked, out)
	}
	if data, err := os.ReadFile(filepath.Join("motion", "a.html")); err != nil || string(data) != testPage {
		t.Fatalf("the page is stored in the project: %v", err)
	}
	_, _, out = runImageScenes(t, scenes, m)
	if asked != 1 || len(*jobs) != 1 || strings.Contains(out, `"phase":"motion"`) {
		t.Fatalf("a stored page is not written again (asked %d, renders %d):\n%s", asked, len(*jobs), out)
	}
}

func TestMotionScene_NoBrowserFailsTheSceneWithTheFix(t *testing.T) {
	imageSetup(t)
	motionStubs(t)
	findBrowser = func() (motion.Browser, error) { return motion.Browser{}, motion.ErrNoBrowser }
	storePages(t, "a", true, false)
	cfg := &config.Config{Project: "d", Output: config.Output{File: "out/d.mp4"},
		Defaults: config.Defaults{Model: "m.safetensors", Voice: "v.onnx", Resolution: "1280x720", Sampler: "euler", Steps: 30, FPS: 30},
		Scenes:   []config.Scene{motionScene("a")}}
	fr := runner.NewFakeRunner()
	var buf bytes.Buffer
	_, err := runVideo(context.Background(), cfg, events.New(&buf, true), fr, ttsRunnerAdapter{r: fr}, assembleRunnerAdapter{r: fr},
		&cache.Manifest{Scenes: map[string]cache.SceneEntry{}}, cache.NewOnlyFilter(""), false, nil, "tmp", "", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "motion failed") {
		t.Fatalf("got %v", err)
	}
}

type writerFunc func(system, user string) (string, error)

func (f writerFunc) Write(_ context.Context, system, user string, _ int) (string, error) {
	return f(system, user)
}

func TestMotionScene_RecordsHowEachPageWasMade(t *testing.T) {
	b, err := motion.Find("")
	if err != nil {
		t.Skip("no Chrome or Chromium on this machine")
	}
	imageSetup(t)
	motionStubs(t)
	findBrowser = func() (motion.Browser, error) { return b, nil }
	newMotionWriter = func(context.Context) (motion.Writer, motion.ModelInfo, error) {
		return writerFunc(func(system, user string) (string, error) { return testPage, nil }), motion.ModelInfo{Provider: "Ollama", Model: "writer:7b"}, nil
	}
	scenes := []config.Scene{motionScene("a")}
	_, m, _ := runImageScenes(t, scenes, nil)
	meta, ok := motion.LoadMeta("", "a", false)
	if !ok || meta.Kind != "motion_graphic" || meta.Model != "writer:7b" || meta.Provider != "Ollama" || meta.Browser != b.Version {
		t.Fatalf("meta = %+v", meta)
	}
	if o := meta.Origin(testPage); o != motion.OriginGenerated {
		t.Fatalf("origin = %s", o)
	}
	// edited by hand: the next run keeps who wrote it and notices the change
	edited := testPage + "<!-- mine -->"
	motion.Store("", "a", false, edited)
	runImageScenes(t, scenes, m)
	meta, _ = motion.LoadMeta("", "a", false)
	if meta.Model != "writer:7b" || meta.Origin(edited) != motion.OriginEdited {
		t.Fatalf("an edited page is reported as edited, keeping its writer: %+v", meta)
	}
}

// A scene of a music piece has a song map next to its page: the render is given it (the page reads the beat
// clock from it) and a change of the map draws the scene again; a scene without one is hashed as before.
func TestMotionScene_TheSongMapIsGivenToTheRenderAndChangesTheHash(t *testing.T) {
	imageSetup(t)
	jobs := motionStubs(t)
	storePages(t, "a", true, false)
	cfg := &config.Config{Defaults: config.Defaults{Resolution: "320x180", FPS: 24}}
	scene := motionScene("a")
	plain := MotionImageHash(cfg, scene, "", "")
	song := `{"bpm":120,"beats":[0,0.5,1]}`
	if err := os.WriteFile(motion.SongPath("", "a"), []byte(song), 0o644); err != nil {
		t.Fatal(err)
	}
	withSong := MotionImageHash(cfg, scene, "", "")
	if withSong == plain {
		t.Fatal("a scene that gets a song map must not share the hash of one that has none")
	}
	if err := os.WriteFile(motion.SongPath("", "a"), []byte(`{"bpm":100,"beats":[0,0.6,1.2]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if MotionImageHash(cfg, scene, "", "") == withSong {
		t.Fatal("a new song map must draw the scene again")
	}
	_, _, _ = runImageScenes(t, []config.Scene{motionScene("a")}, nil)
	if len(*jobs) != 1 || string((*jobs)[0].Page.Song) != `{"bpm":100,"beats":[0,0.6,1.2]}` {
		t.Fatalf("the render must be given the song map of the scene: %+v", *jobs)
	}
	os.Remove(motion.SongPath("", "a"))
	if MotionImageHash(cfg, scene, "", "") != plain {
		t.Fatal("without a song map the hash is the one it always was")
	}
}

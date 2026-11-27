package pipeline

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
	"veritmo/internal/video/motion"
)

// a layer page: nothing but a white square, on the transparent background the layer is drawn with
const layerPage = `<!doctype html><html><body style="margin:0"><div style="position:absolute;left:300px;top:150px;width:60px;height:60px;background:#fff"></div></body></html>`

func trackTools(t *testing.T) {
	t.Helper()
	if _, err := motion.Find(""); err != nil {
		t.Skip("no Chrome or Chromium on this machine")
	}
	for _, n := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(n); err != nil {
			t.Skipf("%s is not installed", n)
		}
	}
	if _, err := motion.OverlayExt(); err != nil {
		t.Skip(err)
	}
}

func makeClip(t *testing.T, name, colour string) {
	t.Helper()
	if out, err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "color=c="+colour+":s=320x180:r=16:d=2", "-pix_fmt", "yuv420p", "sources/"+name).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}

func runTrackProject(t *testing.T, cfg *config.Config, manifest *cache.Manifest) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	r := runner.NewExecRunner()
	_, err := runVideo(context.Background(), cfg, events.New(&buf, true), r, ttsRunnerAdapter{r: r}, assembleRunnerAdapter{r: r}, manifest, cache.NewOnlyFilter(""), false, nil, "tmp", "", nil, nil)
	return buf.String(), err
}

func trackConfig(fade float64) *config.Config {
	zero := 0.0
	return &config.Config{Project: "track", Output: config.Output{File: "out/track.mp4"},
		Defaults: config.Defaults{Resolution: "640x360", FPS: 25, TransitionSeconds: fade},
		VideoTrack: []config.VideoTrackItem{
			{ID: "v1", Clip: "red.mp4", Start: 0, End: 3, Scrim: &zero},
			{ID: "v2", Clip: "green.mp4", Start: 3.5, End: 6, Scrim: &zero},
		},
		Scenes: []config.Scene{
			{ID: "s1", MotionGraphic: "a", SilenceSeconds: silent(2), Speed: 1, Layer: true},
			{ID: "s2", MotionGraphic: "b", SilenceSeconds: silent(2), Speed: 1, Layer: true},
			{ID: "s3", MotionGraphic: "c", SilenceSeconds: silent(2), Speed: 1, Layer: true},
		}}
}

func trackProjectDir(t *testing.T) {
	t.Helper()
	chdirTemp(t)
	for _, d := range []string{"tmp", "out", "sources"} {
		os.MkdirAll(d, 0o755)
	}
	makeClip(t, "red.mp4", "0xe02020")
	makeClip(t, "green.mp4", "0x20c040")
	for _, id := range []string{"s1", "s2", "s3"} {
		motion.Store("", id, false, layerPage)
	}
}

// A whole project with a video track, real Chrome and ffmpeg: three layer scenes laid over two clips with a gap between them, no model or GPU.
func TestTrack_EndToEndLayersOverTheTrackWithAGap(t *testing.T) {
	trackTools(t)
	trackProjectDir(t)
	cfg := trackConfig(0)
	out, err := runTrackProject(t, cfg, &cache.Manifest{Scenes: map[string]cache.SceneEntry{}})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{`"scene_id":"track:v1","phase":"track"`, `"scene_id":"track:v2","phase":"track"`, `"scene_id":"track:_join","phase":"track"`, `"scene_id":"_global","phase":"stack"`} {
		if !strings.Contains(out, want) {
			t.Errorf("the events lack %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, `"phase":"concat"`) || strings.Contains(out, `"phase":"clip"`) {
		t.Errorf("a project with a track neither concatenates nor builds a clip for its layers:\n%s", out)
	}
	d, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", "out/track.mp4").Output()
	if v, _ := strconv.ParseFloat(strings.TrimSpace(string(d)), 64); err != nil || v < 5.9 || v > 6.1 {
		t.Fatalf("duration %q: %v", d, err)
	}
	near := func(p [3]int, want [3]int) bool {
		for i := range p {
			if x := p[i] - want[i]; x > 30 || x < -30 {
				return false
			}
		}
		return true
	}
	if p := pixel(t, "out/track.mp4", 1, 10, 10); !near(p, [3]int{224, 32, 32}) {
		t.Errorf("the first clip shows under the first layer: %v", p)
	}
	if p := pixel(t, "out/track.mp4", 1, 320, 170); !near(p, [3]int{255, 255, 255}) {
		t.Errorf("the square of the layer is over the clip: %v", p)
	}
	if p := pixel(t, "out/track.mp4", 3.25, 10, 10); !near(p, [3]int{0, 0, 0}) {
		t.Errorf("in the gap the picture is the colour of the track (black without a design): %v", p)
	}
	if p := pixel(t, "out/track.mp4", 3.25, 320, 170); !near(p, [3]int{255, 255, 255}) {
		t.Errorf("the layers go on over the gap: %v", p)
	}
	if p := pixel(t, "out/track.mp4", 4.8, 10, 10); !near(p, [3]int{32, 192, 64}) {
		t.Errorf("the second clip: %v", p)
	}
	if n := strings.Count(strings.TrimSpace(mustOut(t, exec.Command("ffprobe", "-v", "error", "-select_streams", "a", "-show_entries", "stream=index", "-of", "csv=p=0", "out/track.mp4"))), "\n") + 1; n != 1 {
		t.Errorf("one sound track: %d", n)
	}
}

func mustOut(t *testing.T, c *exec.Cmd) string {
	t.Helper()
	o, err := c.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(o)
}

// What is drawn again: a clip changes, only its item, the track and the stack are built; no layer is drawn. A page changes, only that layer
// and the stack are drawn; no item is built.
func TestTrack_OnlyWhatChangedIsMadeAgain(t *testing.T) {
	trackTools(t)
	trackProjectDir(t)
	manifest := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
	if out, err := runTrackProject(t, trackConfig(0), manifest); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	// nothing changed: nothing is made
	out, err := runTrackProject(t, trackConfig(0), manifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, `"phase_start"`) && (strings.Contains(l, `"track"`) || strings.Contains(l, `"stack"`) || strings.Contains(l, `"render"`)) {
			t.Fatalf("nothing changed and something was made: %s", l)
		}
	}
	// the first clip is replaced by another colour
	makeClip(t, "red.mp4", "0xe0e020")
	out, err = runTrackProject(t, trackConfig(0), manifest)
	if err != nil {
		t.Fatal(err)
	}
	made := func(out, scene, phase string) bool {
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, `"phase_start"`) && strings.Contains(l, `"scene_id":"`+scene+`"`) && strings.Contains(l, `"phase":"`+phase+`"`) {
				return true
			}
		}
		return false
	}
	if !made(out, "track:v1", "track") || !made(out, "track:_join", "track") || !made(out, "_global", "stack") {
		t.Errorf("the item, the track and the stack are made again:\n%s", out)
	}
	if made(out, "track:v2", "track") || made(out, "s1", "render") || made(out, "s2", "render") || made(out, "s3", "render") {
		t.Errorf("the other item and every layer stay:\n%s", out)
	}
	// the page of one layer changes
	motion.Store("", "s2", false, strings.Replace(layerPage, "#fff", "#0ff", 1))
	out, err = runTrackProject(t, trackConfig(0), manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !made(out, "s2", "render") || !made(out, "_global", "stack") || made(out, "s1", "render") || made(out, "s3", "render") {
		t.Errorf("only that layer and the stack:\n%s", out)
	}
	if made(out, "track:v1", "track") || made(out, "track:v2", "track") || made(out, "track:_join", "track") {
		t.Errorf("the track is not built again for a change of text:\n%s", out)
	}
	// one change at a time to the track: each makes the item it touches (or all of them), the track and the stack, and no layer
	step := func(what string, edit func(*config.Config), items []string, join bool) {
		t.Helper()
		cfg := trackConfig(0)
		edit(cfg)
		out, err := runTrackProject(t, cfg, manifest)
		if err != nil {
			t.Fatalf("%s: %v\n%s", what, err, out)
		}
		for _, id := range []string{"v1", "v2"} {
			want := false
			for _, i := range items {
				want = want || i == id
			}
			if got := made(out, "track:"+id, "track"); got != want {
				t.Errorf("%s: item %s made = %v, want %v\n%s", what, id, got, want, out)
			}
		}
		if got := made(out, "track:_join", "track"); got != join {
			t.Errorf("%s: the join made = %v, want %v", what, got, join)
		}
		if !made(out, "_global", "stack") {
			t.Errorf("%s: the stack is made again", what)
		}
		for _, id := range []string{"s1", "s2", "s3"} {
			if made(out, id, "render") {
				t.Errorf("%s: the layer %s is drawn again", what, id)
			}
		}
	}
	half := 0.5
	step("a scrim", func(c *config.Config) { z := 0.2; c.VideoTrack[1].Scrim = &z }, []string{"v2"}, true)
	step("a trim", func(c *config.Config) {
		z := 0.0
		c.VideoTrack[1].Scrim = &z
		c.VideoTrack[0].ClipStart = &half
		c.VideoTrack[1].Scrim = &z
	}, []string{"v1", "v2"}, true)
	step("the colour of the gaps", func(c *config.Config) {
		z := 0.2
		c.VideoTrack[1].Scrim, c.VideoTrack[0].ClipStart = &z, &half
		c.Defaults.Motion = &config.MotionDefaults{Design: &config.MotionDesign{Dark: "#101030", Light: "#f0f0f0", Accent: "#ff0000", Display: "heavy", Body: "mono", DefaultMode: "dark"}}
	}, []string{"v1", "v2"}, true)
	// a local scrim is part of the item's hash: with the same project as the step before plus a local scrim on one item, that item, the join and
	// the stack are made again, no layer is drawn and the other item is a hit
	withScrim := func(c *config.Config) { z := 0.2; c.VideoTrack[1].Scrim = &z }
	step("the base with a scrim", withScrim, []string{"v1", "v2"}, true)
	step("a local scrim", func(c *config.Config) {
		withScrim(c)
		c.VideoTrack[1].LocalScrims = []config.LocalScrim{{Band: "bottom", Scrim: 0.5}}
	}, []string{"v2"}, true)
}

// Two touching clips with different pixel aspect ratios, crossfaded, and a gap after them: what the real run of 2026-11-26 broke on.
func TestTrack_ClipsWithOtherPixelAspectRatiosCanBeCrossfaded(t *testing.T) {
	trackTools(t)
	trackProjectDir(t)
	for name, sar := range map[string]string{"red.mp4": "2/1", "green.mp4": "3/2"} {
		colour := map[string]string{"red.mp4": "0xe02020", "green.mp4": "0x20c040"}[name]
		if out, err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "color=c="+colour+":s=320x180:r=16:d=2", "-vf", "setsar="+sar, "-pix_fmt", "yuv420p", "sources/"+name).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	cfg := trackConfig(0)
	half, zero := 0.5, 0.0
	cfg.VideoTrack = []config.VideoTrackItem{
		{ID: "v1", Clip: "red.mp4", Start: 0, End: 3, Scrim: &zero},
		{ID: "v2", Clip: "green.mp4", Start: 3, End: 5, Scrim: &zero, TransitionSeconds: &half},
	}
	out, err := runTrackProject(t, cfg, &cache.Manifest{Scenes: map[string]cache.SceneEntry{}})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if p := pixel(t, "out/track.mp4", 4.5, 10, 10); p[1] < 150 || p[0] > 90 {
		t.Errorf("the second clip: %v", p)
	}
	if p := pixel(t, "out/track.mp4", 5.5, 10, 10); p[0] > 40 || p[1] > 40 {
		t.Errorf("the gap after them: %v", p)
	}
}

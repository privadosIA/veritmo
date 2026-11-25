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

const realBackground = `<!doctype html><html><head><style>html,body{margin:0;background:#001a66}
#b{position:absolute;top:20px;width:60px;height:60px;background:#0f0;animation:go 2s linear both}@keyframes go{from{left:0}to{left:300px}}</style></head><body><div id="b"></div></body></html>`

const realOverlay = `<!doctype html><html><body style="margin:0"><div style="position:absolute;left:600px;top:300px;width:80px;height:80px;background:#f00"></div></body></html>`

// pixel reads one pixel of a video at time t as r,g,b.
func pixel(t *testing.T, video string, at float64, x, y int) [3]int {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-v", "error", "-ss", strconv.FormatFloat(at, 'f', 3, 64), "-i", video, "-frames:v", "1",
		"-vf", "crop=2:2:"+strconv.Itoa(x)+":"+strconv.Itoa(y), "-pix_fmt", "rgb24", "-f", "rawvideo", "-").Output()
	if err != nil || len(out) < 3 {
		t.Fatalf("pixel of %s at %.2f: %v (%d bytes)", video, at, err, len(out))
	}
	return [3]int{int(out[0]), int(out[1]), int(out[2])}
}

// A whole project with real Chrome and ffmpeg: two motion scenes joined with a crossfade, the
// first with an overlay drawn over it, and no GPU, voice or model involved.
func TestMotion_EndToEndWithRealChromeAndFFmpeg(t *testing.T) {
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
	chdirTemp(t)
	os.MkdirAll("tmp", 0o755)
	os.MkdirAll("out", 0o755)
	motion.Store("", "a", false, realBackground)
	motion.Store("", "a", true, realOverlay)
	motion.Store("", "b", false, realBackground)

	two := 0.4
	cfg := &config.Config{Project: "e2e", Output: config.Output{File: "out/e2e.mp4"},
		Defaults: config.Defaults{Resolution: "1280x720", FPS: 25, TransitionSeconds: two},
		Scenes: []config.Scene{
			{ID: "a", MotionGraphic: "bg", SilenceSeconds: silent(1.5), Speed: 1, Overlay: &config.Overlay{Text: "T"}},
			{ID: "b", MotionGraphic: "bg", SilenceSeconds: silent(1.5), Speed: 1},
		}}
	var buf bytes.Buffer
	r := runner.NewExecRunner()
	_, err := runVideo(context.Background(), cfg, events.New(&buf, true), r, ttsRunnerAdapter{r: r}, assembleRunnerAdapter{r: r},
		&cache.Manifest{Scenes: map[string]cache.SceneEntry{}}, cache.NewOnlyFilter(""), false, nil, "tmp", "", nil, nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, buf.String())
	}
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", "out/e2e.mp4").Output()
	if err != nil {
		t.Fatal(err)
	}
	d, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if d < 2.5 || d > 2.7 { // 1.5 + 1.5 - 0.4 of crossfade
		t.Fatalf("duration %.2f, want about 2.6", d)
	}
	// Inside the overlay square the picture shows the overlay; around it, the page below.
	red := pixel(t, "out/clips/a.mp4", 0.7, 640, 340)
	if red[0] < 200 || red[1] > 60 || red[2] > 60 {
		t.Fatalf("inside the overlay the pixel is red, got %v", red)
	}
	below := pixel(t, "out/clips/a.mp4", 0.7, 100, 650)
	if below[0] > 40 || below[2] < 70 || below[1] > 60 {
		t.Fatalf("around the overlay the page below shows (dark blue), got %v", below)
	}
	// The scene without an overlay has no red.
	if p := pixel(t, "out/clips/b.mp4", 0.7, 640, 340); p[0] > 60 {
		t.Fatalf("scene b has no overlay: %v", p)
	}
}

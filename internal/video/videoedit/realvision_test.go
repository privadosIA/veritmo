package videoedit

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	_ "image/png"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/video/videovision"
)

// realVision starts the real vision service (the one built in bin/services/videovision, on the CPU) with the models
// of models/video, installs it as the tools' vision and stops it at the end. The test is skipped on a machine
// that does not have the service built, dotnet or the models.
func realVision(t *testing.T, c *Catalog) Vision {
	t.Helper()
	root := repoRoot(t)
	dll := filepath.Join(root, "bin", "services", "videovision", "videovision-service.dll")
	models := filepath.Join(root, "models", "video")
	if _, err := os.Stat(dll); err != nil {
		t.Skip("the vision service is not built (scripts/get-services.sh --only videovision)")
	}
	if missing := videovision.Missing(models); len(missing) > 0 {
		t.Skipf("model files missing from %s: %v", models, missing)
	}
	if _, err := exec.LookPath("dotnet"); err != nil {
		t.Skip("dotnet is not installed")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	cmd := exec.Command("dotnet", dll)
	cmd.Dir = filepath.Dir(dll)
	cmd.Env = append(os.Environ(), fmt.Sprintf("VIDEOVISION_PORT=%d", port), "VIDEOVISION_CKPT="+models, "VIDEOVISION_DEVICE=cpu")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the vision service did not answer")
		}
		time.Sleep(100 * time.Millisecond)
	}
	v := &directVision{c: musicsvc.NewClient("Video vision", fmt.Sprintf("127.0.0.1:%d", port), "")}
	c.SetVision(v)
	t.Cleanup(func() { c.SetVision(nil) })
	return v
}

// directVision talks to a service at a known address.
type directVision struct{ c *musicsvc.Client }

func (d *directVision) Ready(context.Context) (bool, string) { return true, "" }
func (d *directVision) Mask(ctx context.Context, f, o string) (videovision.Summary, error) {
	return videovision.Mask(ctx, d.c, f, o)
}
func (d *directVision) Depth(ctx context.Context, f, o string) (videovision.Summary, error) {
	return videovision.Depth(ctx, d.c, f, o)
}
func (d *directVision) Control(ctx context.Context, f, o string, dilate int) (videovision.Summary, error) {
	return videovision.Control(ctx, d.c, f, o, dilate)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, _ := os.Getwd()
	for d := wd; d != "/" && d != "."; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
	}
	t.Fatal("no go.mod above the test folder")
	return ""
}

// personClip is a few seconds of the clip of a person that the owner left for the tests, or a skip.
func personClip(t *testing.T, seconds float64) string {
	t.Helper()
	src := os.Getenv("VIDEOEDIT_TEST_PERSON_CLIP")
	if _, err := os.Stat(src); src == "" || err != nil {
		t.Skip("no clip of a person to test with (VIDEOEDIT_TEST_PERSON_CLIP)")
	}
	if ok, _ := ffmpegAvailable(); !ok {
		t.Skip("ffmpeg is not installed")
	}
	out := filepath.Join(t.TempDir(), "person.mp4")
	if b, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-i", src, "-t", ftoa(seconds), "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", out).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return out
}

// frameAt decodes the frame at t seconds of a video to an image (RGBA, so a transparent WebM shows its alpha).
func frameAt(t *testing.T, video string, at float64) image.Image {
	t.Helper()
	args := []string{"-hide_banner", "-loglevel", "error", "-y"}
	if strings.HasSuffix(video, ".webm") {
		args = append(args, "-c:v", "libvpx-vp9") // the native VP9 decoder drops the alpha
	}
	args = append(args, "-ss", ftoa(at), "-i", video, "-frames:v", "1", "-pix_fmt", "rgba", "-f", "image2pipe", "-vcodec", "png", "-")
	out, err := exec.Command("ffmpeg", args...).Output()
	if err != nil || len(out) == 0 {
		t.Fatalf("cannot read the frame at %gs of %s: %v", at, video, err)
	}
	img, _, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func isGreen(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	return g>>8 > 190 && r>>8 < 70 && b>>8 < 70
}

func TestRemoveBackground_RealPerson_TheyAreOnGreenInEveryFrameWithTheOriginalsLengthAndSound(t *testing.T) {
	c := NewCatalog()
	realVision(t, c)
	src := personClip(t, 3)
	info, err := c.Probe(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	res, _ := runTool(t, c, RemoveBackground, src, map[string]any{"background": "#00ff00"})
	if res.Format != "mp4" || res.Width != info.Width || res.Height != info.Height || !near(res.Duration, info.Duration, 0.2) || !res.HasAudio || near(res.FPS, info.FPS, 0.01) == false {
		t.Fatalf("res = %+v, source %+v", res, info)
	}
	out := filepath.Join(res.dir, res.File)
	for _, at := range []float64{0.1, 1.0, 2.0, 2.8} {
		img := frameAt(t, out, at)
		b := img.Bounds()
		if c := img.At(b.Min.X+8, b.Min.Y+8); !isGreen(c) {
			t.Errorf("at %gs the corner (the kitchen) is not green: %v", at, c)
		}
		if c := img.At(b.Min.X+b.Dx()/2, b.Min.Y+b.Dy()*6/10); isGreen(c) {
			t.Errorf("at %gs the torso of the person is green: %v", at, c)
		}
	}
}

func TestRemoveBackground_RealPerson_WithAlphaTheFramesAreTransparentOutsideThem(t *testing.T) {
	c := NewCatalog()
	realVision(t, c)
	src := personClip(t, 2)
	res, _ := runTool(t, c, RemoveBackground, src, map[string]any{"alpha": true})
	if res.Format != "webm" || res.File != "result.webm" || !res.HasAudio {
		t.Fatalf("res = %+v", res)
	}
	out := filepath.Join(res.dir, res.File)
	for _, at := range []float64{0.2, 1.2} {
		img := frameAt(t, out, at)
		b := img.Bounds()
		if _, _, _, a := img.At(b.Min.X+8, b.Min.Y+8).RGBA(); a>>8 > 20 {
			t.Errorf("at %gs the background is not transparent: alpha %d", at, a>>8)
		}
		if _, _, _, a := img.At(b.Min.X+b.Dx()/2, b.Min.Y+b.Dy()*6/10).RGBA(); a>>8 < 235 {
			t.Errorf("at %gs the person is not opaque: alpha %d", at, a>>8)
		}
	}
}

func TestRemoveBackground_RealClipWithNobodyIsDoneWithANoteAndUnchanged(t *testing.T) {
	c := NewCatalog()
	realVision(t, c)
	if ok, _ := ffmpegAvailable(); !ok {
		t.Skip("ffmpeg is not installed")
	}
	src := filepath.Join(t.TempDir(), "empty.mp4")
	if b, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "color=c=gray:size=320x180:rate=16:duration=1",
		"-f", "lavfi", "-i", "sine=duration=1", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", src).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	res, warned := runTool(t, c, RemoveBackground, src, nil)
	if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "no person was found") || !strings.Contains(warned, "no person") {
		t.Errorf("notes = %v, warned %q", res.Notes, warned)
	}
	if res.Width != 320 || res.Height != 180 || !res.HasAudio || res.Format != "mp4" || !near(res.Duration, 1, 0.2) {
		t.Errorf("the video must come out as it was: %+v", res)
	}
	img := frameAt(t, filepath.Join(res.dir, res.File), 0.5)
	if c := img.At(10, 10); isGreen(c) {
		t.Errorf("the picture was changed: %v", c)
	}
}

func TestRemoveBackground_WithoutTheServiceItSaysSo(t *testing.T) {
	c := NewCatalog()
	c.SetVision(nil)
	src := movingClip(t, 160, 90, 16, 1, false)
	impl, _ := c.ImplOf(RemoveBackground)
	st, _ := ValidateSettings(RemoveBackground, nil)
	out := t.TempDir()
	if _, err := impl.Run(context.Background(), Job{Dir: out, OutDir: out, Video: src, Settings: st, Warn: func(string) {}}); err == nil || !strings.Contains(err.Error(), "vision service") {
		t.Errorf("err = %v", err)
	}
	if ok, why := impl.Available(); ok || why == "" {
		t.Errorf("available = %v %q", ok, why)
	}
}

func TestRemoveBackgroundArgs(t *testing.T) {
	info := Info{Width: 640, Height: 360, FPS: 29.97}
	green := strings.Join(removeBackgroundArgs("in.mp4", "m/f_%05d.png", "out.mp4", info, false, "#00ff00"), " ")
	for _, want := range []string{"-framerate 29.97", "scale=640:360", "alphamerge", "color=c=0x00ff00:s=640x360:r=29.97", "overlay=shortest=1", "libx264", "-map 0:a:0?"} {
		if !strings.Contains(green, want) {
			t.Errorf("green args lack %q: %s", want, green)
		}
	}
	alpha := strings.Join(removeBackgroundArgs("in.mp4", "m/f_%05d.png", "out.webm", info, true, "#00ff00"), " ")
	for _, want := range []string{"yuva420p", "libvpx-vp9", "libopus", "alphamerge"} {
		if !strings.Contains(alpha, want) {
			t.Errorf("alpha args lack %q: %s", want, alpha)
		}
	}
	if strings.Contains(alpha, "overlay") || strings.Contains(alpha, "color=c=") {
		t.Errorf("alpha has no plain colour: %s", alpha)
	}
}

// grey is the luma of a pixel of a frame, 0 to 255.
func grey(c color.Color) float64 {
	r, g, b, _ := c.RGBA()
	return (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)) / 257
}

func TestDepth_RealClip_AStillObjectKeepsItsGreyFromTheFirstFrameToTheLast(t *testing.T) {
	c := NewCatalog()
	realVision(t, c)
	src := personClip(t, 6)
	info, err := c.Probe(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	res, _ := runTool(t, c, Depth, src, nil)
	if res.Width != info.Width || res.Height != info.Height || near(res.FPS, info.FPS, 0.01) == false || !near(res.Duration, info.Duration, 0.2) || res.HasAudio {
		t.Fatalf("res = %+v, source %+v", res, info)
	}
	out := filepath.Join(res.dir, res.File)
	first, last := frameAt(t, out, 0.0), frameAt(t, out, info.Duration-0.1)
	// the cabinet at the top left and the wall on the right do not move for the whole clip
	for _, p := range [][2]float64{{0.08, 0.10}, {0.10, 0.30}, {0.92, 0.25}, {0.5, 0.04}} {
		b := first.Bounds()
		x, y := b.Min.X+int(p[0]*float64(b.Dx())), b.Min.Y+int(p[1]*float64(b.Dy()))
		g1, g2 := grey(first.At(x, y)), grey(last.At(x, y))
		if d := g1 - g2; d > 14 || d < -14 {
			t.Errorf("the still point (%.2f, %.2f) went from grey %.0f to %.0f", p[0], p[1], g1, g2)
		}
	}
	// near is white and far black: the person (near) is lighter than the wall behind them
	mid := first.Bounds()
	person := grey(first.At(mid.Min.X+mid.Dx()/2, mid.Min.Y+mid.Dy()*6/10))
	wall := grey(first.At(mid.Min.X+mid.Dx()*92/100, mid.Min.Y+mid.Dy()*25/100))
	if person <= wall {
		t.Errorf("the person (%.0f) must be lighter than the wall (%.0f)", person, wall)
	}
}

func TestDepth_RealClip_AClipOf33FramesGives33FramesOfTheSameSize(t *testing.T) {
	c := NewCatalog()
	realVision(t, c)
	if ok, _ := ffmpegAvailable(); !ok {
		t.Skip("ffmpeg is not installed")
	}
	src := filepath.Join(t.TempDir(), "c.mp4")
	if b, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=416x240:rate=16:duration=2.0625",
		"-frames:v", "33", "-c:v", "libx264", "-pix_fmt", "yuv420p", src).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	res, _ := runTool(t, c, Depth, src, nil)
	out, err := exec.Command("ffprobe", "-v", "error", "-count_frames", "-select_streams", "v:0", "-show_entries", "stream=nb_read_frames,width,height", "-of", "csv=p=0", filepath.Join(res.dir, res.File)).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "416,240,33" {
		t.Errorf("width,height,frames = %s, want 416,240,33", got)
	}
}

func TestDepthArgs(t *testing.T) {
	a := strings.Join(depthArgs("d/f_%05d.png", "out.mp4", Info{Width: 1072, Height: 1936, FPS: 30}), " ")
	for _, want := range []string{"-framerate 30", "scale=1072:1936", "-an", "yuv420p", "libx264"} {
		if !strings.Contains(a, want) {
			t.Errorf("args lack %q: %s", want, a)
		}
	}
}

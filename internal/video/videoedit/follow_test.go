package videoedit

import (
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/video/videovision"
)

func TestBoxCentres_AlongTheAxisTheWindowMovesOn(t *testing.T) {
	masks := []videovision.FrameMask{{Box: []int{10, 20, 30, 60}}, {Box: nil}, {Box: []int{0, 0, 100, 100}}}
	h := boxCentres(masks, true, 2)
	if h[0] != 40 || !math.IsNaN(h[1]) || h[2] != 100 {
		t.Errorf("horizontal = %v", h)
	}
	v := boxCentres(masks, false, 1)
	if v[0] != 40 || v[2] != 50 {
		t.Errorf("vertical = %v", v)
	}
}

func TestFillGaps(t *testing.T) {
	nan := math.NaN()
	v := []float64{nan, nan, 5, nan, nan, nan, 9, nan}
	if !fillGaps(v) {
		t.Fatal("there are values")
	}
	if v[0] != 5 || v[1] != 5 || v[3] != 5 || v[4] != 5 || v[5] != 9 || v[7] != 9 {
		t.Errorf("filled = %v", v)
	}
	if fillGaps([]float64{nan, nan}) {
		t.Error("nobody anywhere must say so")
	}
}

func TestSmooth_MovingAverageThatShortensAtTheEnds(t *testing.T) {
	got := smooth([]float64{0, 0, 12, 0, 0}, 3)
	want := []float64{0, 4, 4, 4, 0}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-9 {
			t.Fatalf("smooth = %v, want %v", got, want)
		}
	}
	// a window of one sample changes nothing
	if one := smooth([]float64{3, 9}, 1); one[0] != 3 || one[1] != 9 {
		t.Errorf("one = %v", one)
	}
}

func TestWindowPositions_StayInsideThePicture(t *testing.T) {
	got := windowPositions([]float64{0, 100, 500, 1000}, 300, 800)
	want := []float64{0, 0, 350, 500}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("positions = %v, want %v", got, want)
		}
	}
}

func TestInterpolate_LinearBetweenSamplesAndHeldAtTheEnds(t *testing.T) {
	pos := []float64{0, 100, 300} // every 0.25 s
	for _, tc := range []struct{ t, want float64 }{{-1, 0}, {0, 0}, {0.125, 50}, {0.25, 100}, {0.375, 200}, {0.5, 300}, {9, 300}} {
		if got := interpolate(pos, 4, tc.t); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("at %g: %g, want %g", tc.t, got, tc.want)
		}
	}
}

func TestSendcmdScript_OneCommandPerFrameOnTheRightAxis(t *testing.T) {
	s := sendcmdScript([]float64{0, 40}, 8, 0.25, true)
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) != 3 || lines[0] != "0.0000 crop x 0;" || lines[1] != "0.1250 crop x 20;" || lines[2] != "0.2500 crop x 40;" {
		t.Errorf("script = %q", s)
	}
	if !strings.Contains(sendcmdScript([]float64{5}, 4, 0.2, false), "crop y 5;") {
		t.Error("a vertical window moves y")
	}
}

func TestFilterEscape(t *testing.T) {
	if got := filterEscape(`/a b/c:d,e'f`); got != `/a b/c\:d\,e\'f` {
		t.Errorf("escaped = %s", got)
	}
}

// A wide frame where a person walks from the left edge to the right edge: a crop to portrait that follows them keeps
// them inside the window in every frame; a centred one would lose them.
func TestReframe_RealPerson_TheWindowFollowsThemAcrossAWideFrame(t *testing.T) {
	c := NewCatalog()
	v := realVision(t, c)
	person := personClip(t, 6)
	wide := filepath.Join(t.TempDir(), "wide.mp4")
	// the person, 700 px tall, crosses a 1920x1080 frame in 6 s
	graph := "[1:v]scale=-2:700[p];[0:v][p]overlay=x='20+(W-w-40)*t/6':y=190:shortest=1,format=yuv420p"
	if b, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "color=c=0x303030:size=1920x1080:rate=30:duration=6",
		"-i", person, "-filter_complex", graph, "-t", "6", "-c:v", "libx264", wide).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	res, _ := runTool(t, c, Reframe, wide, map[string]any{"aspect_ratio": "9:16", "mode": "crop"})
	if res.Engine != "crop-follow" || len(res.Notes) != 0 || res.Width != 608 || res.Height != 1080 {
		t.Fatalf("res = %+v", res)
	}
	out := filepath.Join(res.dir, res.File)
	frames := t.TempDir()
	if b, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-i", out, "-vf", "fps=3", filepath.Join(frames, "f_%03d.png")).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	masks := t.TempDir()
	sum, err := v.Mask(context.Background(), frames, masks)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Masks) < 15 {
		t.Fatalf("only %d frames were read", len(sum.Masks))
	}
	for i, m := range sum.Masks {
		if len(m.Box) != 4 {
			t.Errorf("frame %d: the person is not in the window", i+1)
			continue
		}
		// the whole person is inside, not cut by the window's edges (the window is 608 wide; they are about 390)
		if m.Box[0] < 4 || m.Box[2] > res.Width-4 || m.Coverage < 0.12 {
			t.Errorf("frame %d: the person's box %v (coverage %.2f) touches the edge of the %d-wide window", i+1, m.Box, m.Coverage, res.Width)
		}
	}
	samples(t, "reencuadre-sigue-persona", res)
}

func TestReframe_WithoutTheServiceTheWindowIsCentredAndSaysWhy(t *testing.T) {
	c := NewCatalog()
	c.SetVision(nil)
	src := movingClip(t, 640, 360, 16, 1, true)
	res, warned := runTool(t, c, Reframe, src, map[string]any{"aspect_ratio": "9:16", "mode": "crop"})
	if res.Engine != "crop" || len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "centred") || !strings.Contains(warned, "vision service") || res.Width != 202 {
		t.Errorf("res = %+v warned %q", res, warned)
	}
}

func TestReframe_RealClipWithNobodyIsCentredWithANote(t *testing.T) {
	c := NewCatalog()
	realVision(t, c)
	if ok, _ := ffmpegAvailable(); !ok {
		t.Skip("ffmpeg is not installed")
	}
	src := filepath.Join(t.TempDir(), "empty.mp4")
	if b, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "color=c=gray:size=640x360:rate=16:duration=1", "-c:v", "libx264", "-pix_fmt", "yuv420p", src).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	res, _ := runTool(t, c, Reframe, src, map[string]any{"aspect_ratio": "1:1", "mode": "crop"})
	if res.Engine != "crop" || len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "no person was found") {
		t.Errorf("res = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(res.dir, res.File)); err != nil {
		t.Error(err)
	}
}

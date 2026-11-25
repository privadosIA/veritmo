package videoedit

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/video/motionlib"
)

type storeLibrary struct{ st *motionlib.Store }

func (l storeLibrary) Window(id string, start float64) (motionlib.Window, error) {
	return l.st.WindowAt(id, start)
}

// readyMotion puts a motion of `frames` control frames into a library: the frame k is a grey of 3k in a 832x480
// picture, so the window that is cut out can be told by the grey of its frames.
func readyMotion(t *testing.T, frames int) (*motionlib.Store, string) {
	t.Helper()
	st := motionlib.NewStore(t.TempDir())
	m, err := st.Create("baile", nil)
	if err != nil {
		t.Fatal(err)
	}
	for k := 0; k < frames; k++ {
		img := image.NewRGBA(image.Rect(0, 0, 832, 480))
		g := uint8(3 * k)
		for i := 0; i < len(img.Pix); i += 4 {
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = g, g, g, 255
		}
		f, _ := os.Create(filepath.Join(st.ControlDir(m.ID), fmt.Sprintf("%04d.png", k+1)))
		_ = png.Encode(f, img)
		_ = f.Close()
	}
	m.State, m.Frames, m.Duration, m.Width, m.Height, m.People = motionlib.StateReady, frames, float64(frames)/16, 832, 480, 1
	if err := st.Save(m); err != nil {
		t.Fatal(err)
	}
	return st, m.ID
}

func withLibrary(c *Catalog, l Library) { c.SetLibrary(l) }

func greyAt(t *testing.T, path string, x, y int) float64 {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return grey(img.At(x, y))
}

func TestMotionControl_AMotionOfTheLibraryGivesItsWindowFittedToThePictureWithoutVisionWork(t *testing.T) {
	c := NewCatalog()
	st, id := readyMotion(t, 64) // 4 s
	withLibrary(c, storeLibrary{st})
	v, sd := &stubVision{ready: true, people: true}, &sdStandIn{frames: 33}
	withStubs(c, v, sd)

	// a portrait picture: the landscape control is fitted into 480x832 with black bars, which is the control's background
	job := motionJob(t, map[string]any{"start": 1.0, "prompt": "p"}, 480, 866)
	job.Video, job.Motion = "", id
	res, err := c.runMotionControl(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	if v.called != 0 {
		t.Errorf("the vision service did %d jobs: a motion that is ready needs none", v.called)
	}
	control := arg(sd.args, "--control-video")
	if err := CheckControlDir(control); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(control, "0001.png")
	// start 1.0 s is the frame 17 of the motion (the one numbered 16: grey 48); the last of the window is the 49th (grey 144)
	if g := greyAt(t, first, 240, 416); g < 45 || g > 51 {
		t.Errorf("first frame of the window has grey %.0f, want 48 (the frame at 1.0 s)", g)
	}
	if g := greyAt(t, filepath.Join(control, "0033.png"), 240, 416); g < 141 || g > 147 {
		t.Errorf("last frame of the window has grey %.0f, want 144", g)
	}
	if g := greyAt(t, first, 240, 10); g > 3 {
		t.Errorf("the bar above the picture has grey %.0f: it must be black, the background of the control", g)
	}
	if !controlHasSize(control, 480, 832) || arg(sd.args, "-W") != "480" || arg(sd.args, "-H") != "832" {
		t.Errorf("the shape follows the picture: %v", sd.args)
	}
	if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "1.0 to 3.1 s") || !strings.Contains(res.Notes[0], id) {
		t.Errorf("notes = %v: the result says which window was used", res.Notes)
	}
	if res.Engine != "wan2.1-vace-1.3b" || res.FPS != 16 {
		t.Errorf("res = %+v", res)
	}
	if _, err := os.Stat(job.Video); err == nil {
		t.Error("there was no video")
	}

	// a landscape picture: the control is the stored one, no bars
	land := motionJob(t, map[string]any{"start": 0.0}, 1920, 1080)
	land.Video, land.Motion = "", id
	if _, err := c.runMotionControl(context.Background(), land); err != nil {
		t.Fatal(err)
	}
	c2 := arg(sd.args, "--control-video")
	if g := greyAt(t, filepath.Join(c2, "0001.png"), 400, 5); g > 3 || !controlHasSize(c2, 832, 480) {
		t.Errorf("landscape: grey %.0f at the top of the first frame (0 for the frame 0)", g)
	}
	if g := greyAt(t, filepath.Join(c2, "0033.png"), 400, 5); g < 93 || g > 99 {
		t.Errorf("landscape: the last frame has grey %.0f, want 96 (the frame 32)", g)
	}
	if v.called != 0 {
		t.Errorf("vision called %d times", v.called)
	}
}

func TestMotionControl_AWindowPastTheEndOrAMotionThatIsNotReadyIsRefused(t *testing.T) {
	c := NewCatalog()
	st, id := readyMotion(t, 64) // 4 s: the window of 2.06 s fits from 0 to 1.9 s
	withLibrary(c, storeLibrary{st})
	withStubs(c, &stubVision{ready: true, people: true}, &sdStandIn{frames: 33})
	for _, tc := range []struct {
		start float64
		ok    bool
	}{{0, true}, {1.9, true}, {2.0, false}, {3.9, false}} {
		job := motionJob(t, map[string]any{"start": tc.start}, 480, 866)
		job.Video, job.Motion = "", id
		_, err := c.runMotionControl(context.Background(), job)
		if tc.ok != (err == nil) || (!tc.ok && !strings.Contains(err.Error(), "does not fit")) {
			t.Errorf("start %g: %v", tc.start, err)
		}
	}
	m, _ := st.Get(id)
	m.State = motionlib.StateProcessing
	_ = st.Save(m)
	job := motionJob(t, nil, 480, 866)
	job.Video, job.Motion = "", id
	if _, err := c.runMotionControl(context.Background(), job); err == nil || !strings.Contains(err.Error(), "being prepared") {
		t.Errorf("a motion that is processing: %v", err)
	}
	job.Motion = "mot-20260101-000000-abcdef"
	if _, err := c.runMotionControl(context.Background(), job); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("an unknown motion: %v", err)
	}
	c.SetLibrary(nil)
	if _, err := c.runMotionControl(context.Background(), job); err == nil || !strings.Contains(err.Error(), "library") {
		t.Errorf("no library: %v", err)
	}
}

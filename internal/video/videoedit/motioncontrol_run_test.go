package videoedit

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/video/videovision"
)

// stubVision writes a control of black three-channel frames the size of the ones it is given, or a short one.
type stubVision struct {
	ready   bool
	short   bool
	people  bool
	called  int
	lastDir string
}

func (s *stubVision) Ready(context.Context) (bool, string) {
	return s.ready, "the vision service is not running"
}
func (s *stubVision) Mask(context.Context, string, string) (videovision.Summary, error) {
	return videovision.Summary{}, nil
}
func (s *stubVision) Depth(context.Context, string, string) (videovision.Summary, error) {
	return videovision.Summary{}, nil
}
func (s *stubVision) Control(_ context.Context, frames, out string, _ int) (videovision.Summary, error) {
	s.called++
	s.lastDir = out
	files, _ := filepath.Glob(filepath.Join(frames, "*.png"))
	if s.short {
		files = files[:5]
	}
	for _, f := range files {
		fh, _ := os.Open(f)
		cfg, _, _ := image.DecodeConfig(fh)
		_ = fh.Close()
		img := image.NewRGBA(image.Rect(0, 0, cfg.Width, cfg.Height))
		for i := 3; i < len(img.Pix); i += 4 {
			img.Pix[i] = 255
		}
		o, _ := os.Create(filepath.Join(out, filepath.Base(f)))
		_ = png.Encode(o, img)
		_ = o.Close()
	}
	without := 0
	if !s.people {
		without = len(files)
	}
	return videovision.Summary{Frames: len(files), FramesWithoutPeople: &without}, nil
}

// sdStandIn records what sd was asked and writes the .avi it would: frames frames at 16 per second.
type sdStandIn struct {
	frames  int
	nothing bool
	err     error
	dir     string
	env     []string
	args    []string
}

func (s *sdStandIn) run(_ context.Context, dir string, env, args []string) ([]byte, error) {
	s.dir, s.env, s.args = dir, env, args
	if s.err != nil {
		return nil, s.err
	}
	if s.nothing {
		return nil, nil
	}
	out := args[len(args)-1]
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=64x48:rate=16", "-frames:v", fmt.Sprint(s.frames), "-c:v", "mjpeg", out+".avi")
	if b, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, b)
	}
	return nil, nil
}

func arg(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// motionJob is a job with a source clip, a picture and a project folder.
func motionJob(t *testing.T, settings map[string]any, picW, picH int) Job {
	t.Helper()
	src := movingClip(t, 320, 180, 16, 4, false)
	dir := t.TempDir()
	pic := filepath.Join(dir, "ref.png")
	o, _ := os.Create(pic)
	_ = png.Encode(o, image.NewRGBA(image.Rect(0, 0, picW, picH)))
	_ = o.Close()
	st, err := ValidateSettings(MotionControl, settings)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	_ = os.MkdirAll(out, 0o755)
	return Job{Dir: dir, OutDir: out, Video: src, Image: pic, Settings: st, GPU: "1", Warn: func(string) {}}
}

func withStubs(c *Catalog, v *stubVision, sd *sdStandIn) {
	c.SetVision(v)
	c.RunSD = sd.run
}

func TestSdArgs_TheMeasuredRecipe(t *testing.T) {
	a := sdArgs(832, 480, "/p/ref.png", "/c", "two dancers", 0.6, 6, "/o/m.mp4")
	joined := strings.Join(a, " ")
	for _, want := range []string{"-M vid_gen", "--diffusion-model models/video/wan2.1_vace_1.3B_fp16.safetensors", "--vae models/video/wan_2.1_vae.safetensors",
		"--t5xxl models/video/umt5-xxl-encoder-Q8_0.gguf", "-W 832", "-H 480", "--steps 20", "--cfg-scale 6", "--sampling-method euler",
		"--diffusion-fa", "--vae-tiling", "--offload-to-cpu", "-i /p/ref.png", "--control-video /c", "--vace-strength 0.6", "--video-frames 33", "-p two dancers"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args lack %q: %s", want, joined)
		}
	}
	if a[len(a)-1] != "/o/m.mp4" {
		t.Errorf("the output is the last argument: %v", a[len(a)-2:])
	}
	if blank := sdArgs(832, 480, "r", "c", "  ", 1, 6, "o"); arg(blank, "-p") == "" {
		t.Error("a blank prompt must still give sd one")
	}
	// the guidance is the cfg scale: 6 is the measured recipe and a lower one is a choice
	if g := sdArgs(832, 480, "r", "c", "p", 0.6, 4.5, "o"); arg(g, "--cfg-scale") != "4.5" {
		t.Errorf("cfg scale = %q", arg(g, "--cfg-scale"))
	}
}

func TestMotionControl_TheGuidanceReachesSdAndDefaultsToTheMeasuredOne(t *testing.T) {
	c := NewCatalog()
	v, sd := &stubVision{ready: true, people: true}, &sdStandIn{frames: 33}
	withStubs(c, v, sd)
	if _, err := c.runMotionControl(context.Background(), motionJob(t, nil, 480, 866)); err != nil {
		t.Fatal(err)
	}
	if arg(sd.args, "--cfg-scale") != "6" {
		t.Errorf("the default guidance is the measured 6, got %q", arg(sd.args, "--cfg-scale"))
	}
	if _, err := c.runMotionControl(context.Background(), motionJob(t, map[string]any{"guidance": 4.0}, 480, 866)); err != nil {
		t.Fatal(err)
	}
	if arg(sd.args, "--cfg-scale") != "4" {
		t.Errorf("guidance 4 gave %q", arg(sd.args, "--cfg-scale"))
	}
}

func TestMotionControl_RunsSdOnTheControlAndGivesTheConvertedClip(t *testing.T) {
	c := NewCatalog()
	v, sd := &stubVision{ready: true, people: true}, &sdStandIn{frames: 33}
	withStubs(c, v, sd)
	job := motionJob(t, map[string]any{"prompt": "she dances", "strength": 0.8, "start": 0.5}, 480, 866)
	res, err := c.runMotionControl(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	// portrait picture, portrait clip; the control is the one made now (none was kept in the project)
	if arg(sd.args, "-W") != "480" || arg(sd.args, "-H") != "832" || arg(sd.args, "--vace-strength") != "0.8" || arg(sd.args, "-p") != "she dances" {
		t.Errorf("args = %v", sd.args)
	}
	if v.called != 1 || arg(sd.args, "--control-video") != v.lastDir {
		t.Errorf("control called %d times, sd read %s, made at %s", v.called, arg(sd.args, "--control-video"), v.lastDir)
	}
	if sd.dir != job.Dir || len(sd.env) != 1 || sd.env[0] != "CUDA_VISIBLE_DEVICES=1" {
		t.Errorf("sd ran in %s with %v: it runs in the project's folder, on the card of the run", sd.dir, sd.env)
	}
	if res.File != "result.mp4" || res.Format != "mp4" || res.HasAudio || res.Engine != "wan2.1-vace-1.3b" || res.FPS != 16 {
		t.Errorf("res = %+v", res)
	}
	if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "0.5 to 2.6 s") {
		t.Errorf("notes = %v: the result says which part was used", res.Notes)
	}
	if n, _ := countFrames(context.Background(), filepath.Join(job.OutDir, res.File)); n != 33 {
		t.Errorf("frames = %d", n)
	}
}

func TestMotionControl_UsesTheControlTheRequestKeptWhenItFits(t *testing.T) {
	c := NewCatalog()
	v, sd := &stubVision{ready: true, people: true}, &sdStandIn{frames: 33}
	withStubs(c, v, sd)
	job := motionJob(t, nil, 1920, 1080) // landscape picture: 832x480
	kept := filepath.Join(job.Dir, ControlDir)
	work := t.TempDir()
	if _, err := PrepareControl(context.Background(), v, job.Video, 0, 832, 480, filepath.Join(work, "f"), kept); err != nil {
		t.Fatal(err)
	}
	v.called = 0
	if _, err := c.runMotionControl(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if v.called != 0 || arg(sd.args, "--control-video") != kept {
		t.Errorf("the kept control was not used: %d calls, sd read %s", v.called, arg(sd.args, "--control-video"))
	}
	// a kept control of another shape is made again (the picture changed between the request and the run)
	jobPortrait := motionJob(t, nil, 480, 866)
	if _, err := PrepareControl(context.Background(), v, jobPortrait.Video, 0, 832, 480, filepath.Join(work, "f2"), filepath.Join(jobPortrait.Dir, ControlDir)); err != nil {
		t.Fatal(err)
	}
	v.called = 0
	if _, err := c.runMotionControl(context.Background(), jobPortrait); err != nil {
		t.Fatal(err)
	}
	if v.called != 1 {
		t.Errorf("a control of the wrong size must be made again: %d calls", v.called)
	}
}

func TestMotionControl_AClipThatIsNotOfTheRightLengthIsNotOffered(t *testing.T) {
	c := NewCatalog()
	for name, tc := range map[string]struct {
		sd   *sdStandIn
		v    *stubVision
		want string
	}{
		"20 frames":       {&sdStandIn{frames: 20}, &stubVision{ready: true, people: true}, "20 frames, not the 33"},
		"no file":         {&sdStandIn{nothing: true}, &stubVision{ready: true, people: true}, "without writing a video"},
		"sd fails":        {&sdStandIn{err: fmt.Errorf("sd failed: out of memory")}, &stubVision{ready: true, people: true}, "out of memory"},
		"a short control": {&sdStandIn{frames: 33}, &stubVision{ready: true, people: true, short: true}, "has 5 frames, not 33"},
		"nobody":          {&sdStandIn{frames: 33}, &stubVision{ready: true}, "no person was found"},
	} {
		t.Run(name, func(t *testing.T) {
			withStubs(c, tc.v, tc.sd)
			job := motionJob(t, nil, 480, 866)
			_, err := c.runMotionControl(context.Background(), job)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one with %q", err, tc.want)
			}
			if name == "a short control" || name == "nobody" {
				if tc.sd.args != nil {
					t.Error("sd must not be launched on a control that is not complete")
				}
			}
			if _, err := os.Stat(filepath.Join(job.OutDir, ResultName)); err == nil {
				t.Error("a failed run left a result")
			}
		})
	}
}

func TestMotionControl_ALibraryMotionNeedsTheLibrary(t *testing.T) {
	c := NewCatalog()
	withStubs(c, &stubVision{ready: true, people: true}, &sdStandIn{frames: 33})
	job := motionJob(t, nil, 480, 866)
	job.Video, job.Motion = "", "baile"
	if _, err := c.runMotionControl(context.Background(), job); err == nil || !strings.Contains(err.Error(), "library") {
		t.Errorf("err = %v", err)
	}
}

func TestMotionAvailable_SaysWhichPartIsMissing(t *testing.T) {
	c := NewCatalog()
	if ok, _ := ffmpegAvailable(); !ok {
		t.Skip("ffmpeg is not installed")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "models", "video"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	c.LookPath = func(string) (string, error) { return "/bin/sd", nil }
	c.GPUMemoryMB = func() (int, error) { return 12288, nil }
	c.SetVision(&stubVision{ready: true})

	ok, why := c.motionAvailable()
	if ok || !strings.Contains(why, VaceModel) || !strings.Contains(why, VaceVAE) || !strings.Contains(why, VaceEncoder) {
		t.Errorf("no model files: %v %q", ok, why)
	}
	for _, f := range vaceFiles[1:] {
		_ = os.WriteFile(filepath.Join(root, "models", "video", f), []byte("x"), 0o644)
	}
	if ok, why := c.motionAvailable(); ok || !strings.Contains(why, VaceModel) || strings.Contains(why, VaceVAE) {
		t.Errorf("only the video model is missing: %v %q", ok, why)
	}
	_ = os.WriteFile(filepath.Join(root, "models", "video", VaceModel), []byte("x"), 0o644)
	if ok, why := c.motionAvailable(); !ok {
		t.Errorf("everything is there: %q", why)
	}
	c.SetVision(&stubVision{ready: false})
	if ok, why := c.motionAvailable(); ok || !strings.Contains(why, "vision service") {
		t.Errorf("vision stopped: %v %q", ok, why)
	}
	c.SetVision(&stubVision{ready: true})
	c.GPUMemoryMB = func() (int, error) { return 4096, nil }
	if ok, why := c.motionAvailable(); ok || !strings.Contains(why, "6.0 GB") || !strings.Contains(why, "4.0 GB") {
		t.Errorf("a small card: %v %q: it must say what it needs and what there is", ok, why)
	}
	c.GPUMemoryMB = func() (int, error) { return 0, fmt.Errorf("no NVIDIA card can be read") }
	if ok, why := c.motionAvailable(); ok || !strings.Contains(why, "NVIDIA") {
		t.Errorf("no card: %v %q", ok, why)
	}
	c.GPUMemoryMB = func() (int, error) { return 12288, nil }
	c.LookPath = func(string) (string, error) { return "", fmt.Errorf("not found") }
	if ok, why := c.motionAvailable(); ok || !strings.Contains(why, "sd") {
		t.Errorf("no sd: %v %q", ok, why)
	}
}

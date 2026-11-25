package videoedit

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"veritmo/internal/video/videovision"
)

// What motion control makes: a clip of 33 frames at 16 per second (about 2 seconds), the length VACE 1.3B was measured at.
const (
	MotionFrames = 33
	MotionFPS    = 16
	// MotionSeconds is the length of the window of the source the control is taken from.
	MotionSeconds = float64(MotionFrames) / MotionFPS
	// ControlDilate is how many pixels the mask of the people is grown by before it cuts the depth.
	ControlDilate = 4
)

// ControlDir is the folder of a video-edit project where the control made when the request was accepted is kept.
const ControlDir = "control"

// ErrNoPeople says that nobody is in the frames the control is taken from.
var ErrNoPeople = errors.New("no person was found in the part of the video the motion is taken from: motion control moves the movement of people")

// ShapeFor is the size of the clip for a reference picture: the shape of the picture among the three the model was
// measured near (about 400 000 pixels, both sides multiples of 16): landscape 832x480, portrait 480x832 or square 640x640.
func ShapeFor(pictureW, pictureH int) (w, h int) {
	if pictureW <= 0 || pictureH <= 0 {
		return 832, 480
	}
	switch r := float64(pictureW) / float64(pictureH); {
	case r >= 1.5: // 3:2, 16:9, wider
		return 832, 480
	case r <= 0.67: // 2:3, 9:16, narrower
		return 480, 832
	}
	return 640, 640
}

// windowArgs extracts the window of the video the control is taken from: 33 frames at 16 per second from start, the
// picture fitted into w x h with black bars (never cut: the whole of every person is needed).
func windowArgs(video string, start float64, w, h int, dir string) []string {
	filter := fmt.Sprintf("fps=%d,scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1", MotionFPS, w, h, w, h)
	return []string{"-hide_banner", "-loglevel", "error", "-y", "-ss", trimFloat(start), "-i", video, "-an", "-t", trimFloat(MotionSeconds + 0.25),
		"-vf", filter, "-frames:v", fmt.Sprint(MotionFrames), filepath.Join(dir, "%04d.png")}
}

// CheckControlDir checks that a folder holds what sd needs as a control video: 33 PNG of three channels (it refuses a
// grey one and takes an empty or short folder for no control at all, which looks like a success and is not), all of
// one size.
func CheckControlDir(dir string) error { return checkControlFrames(dir, MotionFrames) }

// checkControlFrames is CheckControlDir for a control of n frames (a motion of the library has as many as its video).
func checkControlFrames(dir string, n int) error {
	files, _ := filepath.Glob(filepath.Join(dir, "*.png"))
	sort.Strings(files)
	if len(files) != n || n == 0 {
		return fmt.Errorf("the control has %d frames, not %d", len(files), n)
	}
	var w, h int
	for i, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			return err
		}
		cfg, _, err := image.DecodeConfig(fh)
		_ = fh.Close()
		if err != nil {
			return fmt.Errorf("the control frame %s is not a picture: %w", filepath.Base(f), err)
		}
		if cfg.ColorModel != color.RGBAModel {
			return fmt.Errorf("the control frame %s is not an RGB picture of three channels", filepath.Base(f))
		}
		if i == 0 {
			w, h = cfg.Width, cfg.Height
		} else if cfg.Width != w || cfg.Height != h {
			return fmt.Errorf("the control frame %s is %dx%d, the first is %dx%d", filepath.Base(f), cfg.Width, cfg.Height, w, h)
		}
	}
	return nil
}

// PrepareControl takes the window of a video, asks the vision service for the control (the depth of the people,
// black elsewhere) and checks it. It refuses with ErrNoPeople when nobody is in any of the 33 frames, before anything
// heavy is started. framesDir and controlDir are folders of the caller's, created here.
func PrepareControl(ctx context.Context, v Vision, video string, start float64, w, h int, framesDir, controlDir string) (videovision.Summary, error) {
	if v == nil {
		return videovision.Summary{}, fmt.Errorf("the vision service is not set up")
	}
	for _, d := range []string{framesDir, controlDir} {
		if err := os.RemoveAll(d); err != nil {
			return videovision.Summary{}, err
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			return videovision.Summary{}, err
		}
	}
	if err := runFFmpeg(ctx, windowArgs(video, start, w, h, framesDir)); err != nil {
		return videovision.Summary{}, err
	}
	if got, _ := filepath.Glob(filepath.Join(framesDir, "*.png")); len(got) < MotionFrames {
		return videovision.Summary{}, fmt.Errorf("the video has only %d frames from %.1f s on: motion control needs %d (%.1f s)", len(got), start, MotionFrames, MotionSeconds)
	}
	sum, err := v.Control(ctx, framesDir, controlDir, ControlDilate)
	if err != nil {
		return videovision.Summary{}, err
	}
	if sum.FramesWithoutPeople != nil && *sum.FramesWithoutPeople >= sum.Frames {
		return sum, ErrNoPeople
	}
	if err := CheckControlDir(controlDir); err != nil {
		return sum, err
	}
	return sum, nil
}

// PictureShape reads the size of a reference picture and gives the size of the clip.
func (c *Catalog) PictureShape(ctx context.Context, picture string) (int, int, error) {
	info, err := c.Probe(ctx, picture)
	if err != nil {
		return 0, 0, fmt.Errorf("the reference picture cannot be read: %w", err)
	}
	w, h := ShapeFor(info.Width, info.Height)
	return w, h, nil
}

// --- the run ----------------------------------------------------------------------------------------

// The files the video model is made of, in models/video: Wan 2.1 VACE 1.3B (the safetensors of Comfy-Org: the GGUF
// versions do not load, they lack vace_patch_embedding.weight), its VAE and the text encoder.
const (
	VaceModel   = "wan2.1_vace_1.3B_fp16.safetensors"
	VaceVAE     = "wan_2.1_vae.safetensors"
	VaceEncoder = "umt5-xxl-encoder-Q8_0.gguf"
	// MotionModelsDir is where they are, relative to the project root (the run's folder links it).
	MotionModelsDir = "models/video"
	// MinVRAMMB is the memory of a card the model needs: it peaked at 5.5 GB at 832x480, and a little room.
	MinVRAMMB = 6144
	// motionEngine is what the result says made it.
	motionEngine = "wan2.1-vace-1.3b"
)

var vaceFiles = []string{VaceModel, VaceVAE, VaceEncoder}

// runSD runs the stable-diffusion.cpp program in dir with the environment given on top of the process's own (the
// Catalog's RunSD, which a test replaces: the program takes minutes and a graphics card).
func runSD(ctx context.Context, dir string, env, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "sd", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		tail := strings.TrimSpace(string(out))
		if len(tail) > 700 {
			tail = tail[len(tail)-700:]
		}
		return out, fmt.Errorf("sd failed: %w: %s", err, tail)
	}
	return out, nil
}

// gpuMemoryMB is the memory of the biggest graphics card (the Catalog's GPUMemoryMB).
func gpuMemoryMB() (int, error) {
	out, err := exec.Command("nvidia-smi", "--query-gpu=memory.total", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return 0, fmt.Errorf("no NVIDIA card can be read: %w", err)
	}
	best := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		var mb int
		if _, err := fmt.Sscanf(strings.TrimSpace(line), "%d", &mb); err == nil && mb > best {
			best = mb
		}
	}
	if best == 0 {
		return 0, fmt.Errorf("nvidia-smi lists no card")
	}
	return best, nil
}

// motionFilesMissing names the model files that are not in the folder.
func motionFilesMissing(dir string) []string {
	var out []string
	for _, f := range vaceFiles {
		if fi, err := os.Stat(filepath.Join(dir, f)); err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 {
			out = append(out, f)
		}
	}
	return out
}

// motionAvailable is the availability of the tool: ffmpeg and sd, the three model files (named when missing), the
// vision service and a card with room. It says which of them is not there.
func (c *Catalog) motionAvailable() (bool, string) {
	if ok, why := ffmpegAvailable(); !ok {
		return false, why
	}
	if _, err := c.LookPath("sd"); err != nil {
		return false, "sd (stable-diffusion.cpp) is not installed: run scripts/build-sd.sh"
	}
	if missing := motionFilesMissing(MotionModelsDir); len(missing) > 0 {
		return false, fmt.Sprintf("the model file %s is missing from %s", strings.Join(missing, ", "), MotionModelsDir)
	}
	if ok, why := c.NeedsVision(); !ok {
		return false, why
	}
	have, err := c.GPUMemoryMB()
	if err != nil {
		return false, err.Error()
	}
	if have < MinVRAMMB {
		return false, fmt.Sprintf("it needs a graphics card with %.1f GB and the biggest one has %.1f GB", float64(MinVRAMMB)/1024, float64(have)/1024)
	}
	return true, ""
}

// sdArgs is the recipe measured on 2026-11-06 (Wan 2.1 VACE 1.3B, 20 steps, 317 s for 33 frames on an RTX 5060 Ti and
// 5.5 GB of video memory): the control video is a folder of frames, the picture is the reference, and the strength
// says how much the motion rules. The model paths are relative to the folder sd runs in (the project's).
func sdArgs(w, h int, picture, control, prompt string, strength, guidance float64, out string) []string {
	if strings.TrimSpace(prompt) == "" {
		prompt = "the people of the picture move, natural soft lighting, photograph"
	}
	return []string{
		"-M", "vid_gen",
		"--diffusion-model", MotionModelsDir + "/" + VaceModel,
		"--vae", MotionModelsDir + "/" + VaceVAE,
		"--t5xxl", MotionModelsDir + "/" + VaceEncoder,
		"-W", fmt.Sprint(w), "-H", fmt.Sprint(h),
		"--steps", "20", "--cfg-scale", trimFloat(guidance), "--sampling-method", "euler",
		"--diffusion-fa", "--vae-tiling", "--offload-to-cpu",
		"-i", picture, "--control-video", control,
		"--vace-strength", trimFloat(strength), "--video-frames", fmt.Sprint(MotionFrames),
		"-p", prompt, "-o", out,
	}
}

// countFrames counts the frames of a video by decoding it.
func countFrames(ctx context.Context, path string) (int, error) {
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-count_frames", "-select_streams", "v:0", "-show_entries", "stream=nb_read_frames", "-of", "csv=p=0", path).Output()
	if err != nil {
		return 0, err
	}
	var n int
	if _, err := fmt.Sscan(strings.TrimSpace(string(out)), &n); err != nil {
		return 0, fmt.Errorf("ffprobe did not say how many frames: %q", out)
	}
	return n, nil
}

func (c *Catalog) runMotionControl(ctx context.Context, job Job) (Result, error) {
	if job.Image == "" {
		return Result{}, fmt.Errorf("motion control needs a reference picture")
	}
	if job.Video == "" && job.Motion == "" {
		return Result{}, fmt.Errorf("motion control needs a video or a motion of the library")
	}
	w, h, err := c.PictureShape(ctx, job.Image)
	if err != nil {
		return Result{}, err
	}
	start := Float(job.Settings, "start")
	res := Result{Engine: motionEngine}

	var control string
	switch {
	case job.Motion != "":
		// A motion of the library has its control made: a window of it is cut and fitted to the picture's shape. The
		// vision service does no work.
		lib := c.LibraryOf()
		if lib == nil {
			return Result{}, fmt.Errorf("the library of motions is not set up")
		}
		win, err := lib.Window(job.Motion, start)
		if err != nil {
			return Result{}, err
		}
		work, err := workDir(job, "motion")
		if err != nil {
			return Result{}, err
		}
		control = filepath.Join(work, "control")
		if err := RefitControl(ctx, win, w, h, control); err != nil {
			return Result{}, err
		}
		res.Notes = append(res.Notes, fmt.Sprintf("the motion %s is used from %.1f to %.1f s, %d frames at %d per second", job.Motion, win.From, win.To, MotionFrames, MotionFPS))
	default:
		res.Notes = append(res.Notes, fmt.Sprintf("the motion is taken from %.1f to %.1f s of the video, %d frames at %d per second", start, start+MotionSeconds, MotionFrames, MotionFPS))
		// The control the request made is used when it is there and fits; otherwise it is made now.
		control = filepath.Join(job.Dir, ControlDir)
		if err := CheckControlDir(control); err != nil || !controlHasSize(control, w, h) {
			v, verr := c.requireVision()
			if verr != nil {
				return Result{}, verr
			}
			work, err := workDir(job, "motion")
			if err != nil {
				return Result{}, err
			}
			control = filepath.Join(work, "control")
			if _, err := PrepareControl(ctx, v, job.Video, start, w, h, filepath.Join(work, "frames"), control); err != nil {
				return Result{}, err
			}
		}
	}
	// A folder that is empty or short does not make sd fail: it makes a picture-to-video clip, which looks like a success
	// and is not one. It is checked once more right before sd is launched.
	if err := CheckControlDir(control); err != nil {
		return Result{}, fmt.Errorf("the control is not what sd needs: %w", err)
	}

	gen, err := workDir(job, "motion-clip")
	if err != nil {
		return Result{}, err
	}
	var env []string
	if job.GPU != "" {
		env = append(env, "CUDA_VISIBLE_DEVICES="+job.GPU)
	}
	out := filepath.Join(gen, "motion.mp4")
	if _, err := c.RunSD(ctx, job.Dir, env, sdArgs(w, h, job.Image, control, Str(job.Settings, "prompt"), Float(job.Settings, "strength"), Float(job.Settings, "guidance"), out)); err != nil {
		return Result{}, err
	}
	made, _ := filepath.Glob(out + "*") // sd writes motion.mp4 as motion.mp4.avi
	if len(made) == 0 {
		return Result{}, fmt.Errorf("sd finished without writing a video")
	}
	result := filepath.Join(job.OutDir, ResultName)
	args := []string{"-hide_banner", "-loglevel", "error", "-y", "-i", made[0], "-an", "-c:v", "libx264", "-preset", "medium", "-crf", "16",
		"-pix_fmt", "yuv420p", "-movflags", "+faststart", result}
	if err := runFFmpeg(ctx, args); err != nil {
		return Result{}, err
	}
	n, err := countFrames(ctx, result)
	if err != nil {
		return Result{}, err
	}
	if n != MotionFrames {
		_ = os.Remove(result)
		return Result{}, fmt.Errorf("the clip has %d frames, not the %d it should: it is not offered", n, MotionFrames)
	}
	return c.finish(ctx, job, res, ResultName, "mp4")
}

// controlHasSize says whether the control frames are w x h.
func controlHasSize(dir string, cw, ch int) bool {
	files, _ := filepath.Glob(filepath.Join(dir, "*.png"))
	if len(files) == 0 {
		return false
	}
	f, err := os.Open(files[0])
	if err != nil {
		return false
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	return err == nil && cfg.Width == cw && cfg.Height == ch
}

package videoedit

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"veritmo/internal/platform/fsutil"
)

// The two networks the better engines of `scale` and `interpolate` run, as programs of their own (ncnn on Vulkan: any
// graphics card, no CUDA, no Python). scripts/get-upscalers.sh installs them under bin/.
const (
	// RealESRGANModel is the family of networks used to enlarge: made for video, the best of the three that were compared
	// on frames of generated video (the photo one barely beats lanczos and the anime one paints the skin).
	RealESRGANModel = "realesr-animevideov3"
	// RIFEModel is the network that makes the frames in between.
	RIFEModel = "rife-v4.6"
	// upscalerHint is what the notes tell a person to do when a program is not there.
	upscalerHint = "bash scripts/get-upscalers.sh"
)

// defaultToolsDir is the folder that holds bin/realesrgan and bin/rife (the Catalog's ToolsDir, which a test points
// elsewhere). It is the bin/ of the folder the server was started in, like the models.
func defaultToolsDir() string {
	abs, err := filepath.Abs("bin")
	if err != nil {
		return "bin"
	}
	return abs
}

func (c *Catalog) realesrganPath() string {
	return filepath.Join(c.ToolsDir(), "realesrgan", "realesrgan-ncnn-vulkan")
}
func (c *Catalog) realesrganModels() string {
	return filepath.Join(c.ToolsDir(), "realesrgan", "models")
}
func (c *Catalog) rifePath() string  { return filepath.Join(c.ToolsDir(), "rife", "rife-ncnn-vulkan") }
func (c *Catalog) rifeModel() string { return filepath.Join(c.ToolsDir(), "rife", RIFEModel) }

// RealESRGANInstalled says whether the enlarging network can be run, and why not when it cannot.
func (c *Catalog) RealESRGANInstalled() (bool, string) {
	if !fsutil.Exists(c.realesrganPath()) {
		return false, "the Real-ESRGAN program is not installed"
	}
	for _, x := range []string{"x2", "x3", "x4"} {
		if !fsutil.Exists(filepath.Join(c.realesrganModels(), RealESRGANModel+"-"+x+".bin")) {
			return false, fmt.Sprintf("the model %s-%s is missing", RealESRGANModel, x)
		}
	}
	return true, ""
}

// RIFEInstalled says whether the network that makes the frames in between can be run.
func (c *Catalog) RIFEInstalled() (bool, string) {
	if !fsutil.Exists(c.rifePath()) {
		return false, "the RIFE program is not installed"
	}
	if !fsutil.Exists(filepath.Join(c.rifeModel(), "flownet.bin")) {
		return false, "the model " + RIFEModel + " is missing"
	}
	return true, ""
}

// EngineNotes are the lines the options add to a tool about the engines it has beyond ffmpeg: what is not installed and
// how to get it. A tool whose engines are all there gets none.
func (c *Catalog) EngineNotes(id string) []string {
	switch id {
	case Scale:
		if ok, why := c.RealESRGANInstalled(); !ok {
			return []string{"The quality engine (a super-resolution network) is not available: " + why + ". Install it with `" + upscalerHint + "`; until then `quality` makes the fast one."}
		}
	case Interpolate:
		if ok, why := c.RIFEInstalled(); !ok {
			return []string{"The interpolation network is not available: " + why + ". Install it with `" + upscalerHint + "`; until then the frames in between are made with a filter."}
		}
	}
	return nil
}

// runProgram runs a program and gives its output; on failure the error carries the end of what it said. The Catalog's
// RunProgram, which a test replaces because the programs need a graphics card.
func runProgram(ctx context.Context, name string, args []string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		tail := strings.TrimSpace(string(out))
		if len(tail) > 500 {
			tail = tail[len(tail)-500:]
		}
		return out, fmt.Errorf("%s failed: %w: %s", filepath.Base(name), err, tail)
	}
	return out, nil
}

var vulkanName = regexp.MustCompile(`deviceName\s*=\s*(.+)`)

// parseVulkanDevices reads the names of the devices in the order vulkaninfo lists them, which is the order of the -g of
// the ncnn programs.
func parseVulkanDevices(summary string) []string {
	var names []string
	for _, m := range vulkanName.FindAllStringSubmatch(summary, -1) {
		names = append(names, strings.TrimSpace(m[1]))
	}
	return names
}

// vulkanIndex is the index of the device a card is for the ncnn programs. The run's card is a CUDA index; Vulkan lists
// the cards in an order of its own (and the integrated graphics too), so the card is looked for by its name.
func vulkanIndex(cudaName string, devices []string) (int, bool) {
	for i, d := range devices {
		if d == cudaName || strings.HasSuffix(d, cudaName) || strings.HasSuffix(cudaName, d) {
			return i, true
		}
	}
	return 0, false
}

// vulkanDevice is the value of -g for the card of a run ("" asks for the program's own choice, the first device).
func vulkanDevice(ctx context.Context, gpu string) string {
	if gpu == "" || strings.Contains(gpu, ",") {
		return ""
	}
	smi, err := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=index,name", "--format=csv,noheader").Output()
	if err != nil {
		return gpu
	}
	var name string
	for _, line := range strings.Split(strings.TrimSpace(string(smi)), "\n") {
		if idx, n, ok := strings.Cut(line, ","); ok && strings.TrimSpace(idx) == gpu {
			name = strings.TrimSpace(n)
		}
	}
	info, err := exec.CommandContext(ctx, "vulkaninfo", "--summary").Output()
	if err != nil || name == "" {
		return gpu
	}
	if i, ok := vulkanIndex(name, parseVulkanDevices(string(info))); ok {
		return fmt.Sprint(i)
	}
	return gpu
}

// freeBytes is the room there is on the disk of a folder (the Catalog's FreeBytes).
func freeBytes(dir string) uint64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return math.MaxUint64
	}
	return uint64(st.Bavail) * uint64(st.Bsize)
}

// frameRate is the rate of a video as ffmpeg takes it.
func frameRate(info Info) string {
	if info.FPSText != "" && info.FPSText != "0/0" {
		return info.FPSText
	}
	return trimFloat(info.FPS)
}

// extractAllFrames writes every frame of a video at its own size and rate as PNG (00000001.png ...) into dir.
func extractAllFrames(ctx context.Context, video, dir string, info Info) (int, error) {
	args := []string{"-hide_banner", "-loglevel", "error", "-y", "-i", video, "-an", "-vf", "fps=" + frameRate(info), filepath.Join(dir, "%08d.png")}
	if err := runFFmpeg(ctx, args); err != nil {
		return 0, err
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.png"))
	if len(files) == 0 {
		return 0, fmt.Errorf("no frame could be read from the video")
	}
	return len(files), nil
}

func countPNG(dir string) int {
	files, _ := filepath.Glob(filepath.Join(dir, "*.png"))
	return len(files)
}

// diskNeed is a safe guess of the room a folder of n PNG frames of w x h takes (PNG is about a byte and a half a pixel).
func diskNeed(n, w, h int) uint64 { return uint64(float64(n) * float64(w) * float64(h) * 1.5) }

// scaleWithNetwork enlarges the video with Real-ESRGAN: every frame goes through the network and ffmpeg puts the video
// together again with the sound of the original. It fails (and the caller falls back to the filter) when the room for
// the frames is not there, when the program fails or when it does not give every frame.
func (c *Catalog) scaleWithNetwork(ctx context.Context, job Job, info Info, factor int) (Result, error) {
	work, err := workDir(job, "scale-quality")
	if err != nil {
		return Result{}, err
	}
	frames, up := filepath.Join(work, "frames"), filepath.Join(work, "up")
	for _, d := range []string{frames, up} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return Result{}, err
		}
	}
	n := int(math.Ceil(info.Duration*info.FPS)) + 1
	if need := diskNeed(n, info.Width*(factor+1), info.Height*(factor+1)); need > c.FreeBytes(work)/10*8 {
		return Result{}, fmt.Errorf("the frames of the enlarged video would take about %.1f GB and the disk has %.1f GB free", float64(need)/1e9, float64(c.FreeBytes(work))/1e9)
	}
	count, err := extractAllFrames(ctx, job.Video, frames, info)
	if err != nil {
		return Result{}, err
	}
	args := []string{"-i", frames, "-o", up, "-m", c.realesrganModels(), "-n", RealESRGANModel, "-s", fmt.Sprint(factor), "-f", "png"}
	if g := vulkanDevice(ctx, job.GPU); g != "" {
		args = append(args, "-g", g)
	}
	if _, err := c.RunProgram(ctx, c.realesrganPath(), args); err != nil {
		return Result{}, err
	}
	if got := countPNG(up); got != count {
		return Result{}, fmt.Errorf("the network gave %d frames of the %d it was given", got, count)
	}
	w, h := even(float64(info.Width*factor)), even(float64(info.Height*factor))
	out := filepath.Join(job.OutDir, ResultName)
	ff := []string{"-hide_banner", "-loglevel", "error", "-y", "-framerate", frameRate(info), "-i", filepath.Join(up, "%08d.png"), "-i", job.Video,
		"-map", "0:v:0", "-map", "1:a:0?", "-vf", fmt.Sprintf("scale=%d:%d:flags=lanczos", w, h),
		"-c:v", "libx264", "-preset", "medium", "-crf", "16", "-pix_fmt", "yuv420p", "-movflags", "+faststart", "-c:a", "aac", "-b:a", "192k", "-shortest", out}
	if err := runFFmpeg(ctx, ff); err != nil {
		return Result{}, err
	}
	return Result{Engine: RealESRGANModel}, nil
}

// interpolateWithNetwork makes the frames in between with RIFE, for a video of the same length at the target rate.
func (c *Catalog) interpolateWithNetwork(ctx context.Context, job Job, info Info, target int) (Result, error) {
	work, err := workDir(job, "interpolate-network")
	if err != nil {
		return Result{}, err
	}
	frames, mid := filepath.Join(work, "frames"), filepath.Join(work, "mid")
	for _, d := range []string{frames, mid} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return Result{}, err
		}
	}
	count, err := extractAllFrames(ctx, job.Video, frames, info)
	if err != nil {
		return Result{}, err
	}
	duration := float64(count) / info.FPS
	want := int(math.Round(duration * float64(target)))
	if want <= count {
		return Result{}, fmt.Errorf("a target of %d frames per second gives %d frames for the %d there are", target, want, count)
	}
	if need := diskNeed(count+want, info.Width, info.Height); need > c.FreeBytes(work)/10*8 {
		return Result{}, fmt.Errorf("the frames would take about %.1f GB and the disk has %.1f GB free", float64(need)/1e9, float64(c.FreeBytes(work))/1e9)
	}
	args := []string{"-i", frames, "-o", mid, "-m", c.rifeModel(), "-n", fmt.Sprint(want)}
	if g := vulkanDevice(ctx, job.GPU); g != "" {
		args = append(args, "-g", g)
	}
	if _, err := c.RunProgram(ctx, c.rifePath(), args); err != nil {
		return Result{}, err
	}
	if got := countPNG(mid); got != want {
		return Result{}, fmt.Errorf("the network gave %d frames, not the %d asked for", got, want)
	}
	out := filepath.Join(job.OutDir, ResultName)
	ff := []string{"-hide_banner", "-loglevel", "error", "-y", "-framerate", fmt.Sprint(target), "-i", filepath.Join(mid, "%08d.png"), "-an",
		"-c:v", "libx264", "-preset", "medium", "-crf", "16", "-pix_fmt", "yuv420p", "-movflags", "+faststart", out}
	if err := runFFmpeg(ctx, ff); err != nil {
		return Result{}, err
	}
	return Result{Engine: RIFEModel}, nil
}

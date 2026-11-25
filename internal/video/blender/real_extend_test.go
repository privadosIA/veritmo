package blender

// Real Blender tests of what extend-blender-scene-build added: the step ease, the backdrop, the design fonts and the check
// that the kept .blend, reopened in a clean Blender, gives the frames of the pipeline. Skipped without VERITMO_REAL_BLENDER=1.

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"veritmo/internal/platform/runner"
)

// queryBlend opens a .blend in a clean Blender and runs a short script on it; the lines of the script's output that start
// with "Q " are returned without it.
func queryBlend(t *testing.T, r runner.CommandRunner, bin, blend, py string) []string {
	t.Helper()
	out, err := r.Run(context.Background(), bin, []string{"--factory-startup", "-b", "-noaudio", blend, "--disable-autoexec", "--python-exit-code", "1", "--python-expr", py})
	if err != nil {
		t.Fatalf("query of %s: %v\n%s", blend, err, out)
	}
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "Q "); ok {
			lines = append(lines, v)
		}
	}
	return lines
}

// luminance reads a PNG as a grid of luminances (0 to 255).
func luminance(t *testing.T, path string) (w, h int, y []float64) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	b := img.Bounds()
	w, h = b.Dx(), b.Dy()
	y = make([]float64, w*h)
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			r, g, bl, _ := img.At(b.Min.X+i, b.Min.Y+j).RGBA()
			y[j*w+i] = (0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(bl)) / 257
		}
	}
	return w, h, y
}

func meanLuminance(t *testing.T, path string) float64 {
	t.Helper()
	_, _, y := luminance(t, path)
	var s float64
	for _, v := range y {
		s += v
	}
	return s / float64(len(y))
}

var ssimAll = regexp.MustCompile(`All:([0-9.]+)`)

// ssim is the structural similarity of two PNGs of the same size, measured with ffmpeg's ssim filter.
func ssim(t *testing.T, a, b string) float64 {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-hide_banner", "-i", a, "-i", b, "-lavfi", "ssim", "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("ssim: %v\n%s", err, out)
	}
	m := ssimAll.FindStringSubmatch(string(out))
	if m == nil {
		t.Fatalf("no ssim in %s", out)
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	return v
}

// renderSpec renders a description at a tiny size and returns the folder, with the kept .blend as a.blend.
func renderSpec(t *testing.T, doc string, env Env, fonts map[string]string, count int, w, h int) string {
	t.Helper()
	info, r := realBlender(t)
	spec, err := Parse([]byte(doc), float64(count)/30, env)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err := Render(context.Background(), r, info.Path, Params{
		Dir: dir, Out: "f", Samples: 8, Device: "auto", GPU: os.Getenv("CUDA_VISIBLE_DEVICES") != "", Width: w, Height: h, Count: count, FPS: 30,
		Spec: spec, Sources: env.Sources, Fonts: fonts, BlendOut: "a.blend",
	}, nil); err != nil {
		t.Fatal(err)
	}
	return dir
}

func frameFile(dir string, n int) string {
	return filepath.Join(dir, "f", fmt.Sprintf("f_%06d.png", n))
}

// A hit does not anticipate: at 120 bpm and 30 fps beat 4 is frame 61, and a step key holds the value until it.
func TestRealAStepKeyHoldsTheValueUntilItsFrameAndJumpsOnIt(t *testing.T) {
	info, r := realBlender(t)
	doc := `{"version":1,"camera":{"keys":[{"t":0,"location":[0,-6,1],"target":[0,0,0]}]},"materials":{"m":{"color":"accent","emission":0}},` +
		`"objects":[{"id":"ball","type":"sphere","size":1,"material":"m","keys":[{"beat":0,"emission":0},{"beat":4,"emission":4,"ease":"step"},{"beat":4.5,"emission":0,"ease":"out"}]}]}`
	dir := renderSpec(t, doc, testEnv(""), nil, 90, 32, 18)
	lines := queryBlend(t, r, info.Path, filepath.Join(dir, "a.blend"), `import bpy
mat = bpy.data.materials['ball.m']
sock = mat.node_tree.nodes['Principled BSDF'].inputs['Emission Strength']
for f in (1, 30, 59, 60, 61, 62, 75):
    bpy.context.scene.frame_set(f)
    print('Q', f, round(sock.default_value, 4))
`)
	got := map[int]float64{}
	for _, l := range lines {
		var f int
		var v float64
		if _, err := fmt.Sscanf(l, "%d %f", &f, &v); err == nil {
			got[f] = v
		}
	}
	if got[1] != 0 || got[30] != 0 || got[59] != 0 || got[60] != 0 {
		t.Errorf("the emission must stay 0 up to frame 60: %v", got)
	}
	if got[61] != 4 {
		t.Errorf("the emission must be 4 on frame 61: %v", got)
	}
	if !(got[62] > 0 && got[62] < 4) || got[75] >= got[62] {
		t.Errorf("after the hit the emission decays (out): %v", got)
	}
}

// edge is the strongest step between two neighbouring rows of the band, in the mean of the columns given: how hard a
// horizon line shows there.
func edge(t *testing.T, path string, rowFrom, rowTo, colFrom, colTo int) float64 {
	t.Helper()
	w, _, y := luminance(t, path)
	best := 0.0
	for j := rowFrom; j < rowTo; j++ {
		var a, b float64
		for i := colFrom; i < colTo; i++ {
			a += y[j*w+i]
			b += y[(j+1)*w+i]
		}
		if d := math.Abs(b-a) / float64(colTo-colFrom); d > best {
			best = d
		}
	}
	return best
}

func TestRealABackdropHasNoHorizonWhereAPlaneHasOne(t *testing.T) {
	scene := func(floor string) string {
		return `{"version":1,"world":{"color":"bg"},"camera":{"lens":50,"keys":[{"t":0,"location":[0,-9,1.2],"target":[0,0,0.6]}]},` +
			`"lights":[{"type":"area","location":[3,-4,5],"target":[0,0,0],"power":600,"size":3},{"type":"sun","rotation":[50,0,30],"power":2}],` +
			`"materials":{"ground":{"color":"#3a4452","roughness":0.9}},"objects":[` + floor + `]}`
	}
	plane := renderSpec(t, scene(`{"id":"floor","type":"plane","size":40,"material":"ground"}`), testEnv(""), nil, 1, 320, 180)
	stage := renderSpec(t, scene(`{"id":"floor","type":"backdrop","size":40,"curve":4,"material":"ground"}`), testEnv(""), nil, 1, 320, 180)
	// the band of rows where the plane's far edge is, over the left fifth of the picture (nothing else is there)
	onPlane := edge(t, frameFile(plane, 1), 40, 120, 0, 64)
	onStage := edge(t, frameFile(stage, 1), 40, 120, 0, 64)
	t.Logf("strongest row-to-row step in the horizon band: plane %.2f, backdrop %.2f (luminance of 255)", onPlane, onStage)
	if onPlane < 3 {
		t.Fatalf("the test cannot tell: the plane's horizon is only %.2f", onPlane)
	}
	if onStage > onPlane/3 {
		t.Errorf("the backdrop must show no horizon: %.2f against the plane's %.2f", onStage, onPlane)
	}
}

func TestRealTheDesignFontIsDrawnAndPackedInTheBlend(t *testing.T) {
	info, r := realBlender(t)
	fonts := realFonts(t)
	text := func(font string) string {
		return `{"version":1,"world":{"color":"bg"},"camera":{"lens":50,"keys":[{"t":0,"location":[0,-6,0],"target":[0,0,0]}]},` +
			`"lights":[{"type":"sun","rotation":[60,0,20],"power":3}],"materials":{"m":{"color":"fg"}},` +
			`"objects":[{"id":"t","type":"text","text":"Hola","size":1.5,"align":"center","material":"m","font":` + font + `,"keys":[{"t":0,"rotation":[90,0,0]}]}]}`
	}
	withFont := renderSpec(t, text(`"display"`), testEnv(""), fonts, 1, 160, 90)
	builtin := renderSpec(t, strings.Replace(text(`"display"`), `"font":"display",`, "", 1), testEnv(""), nil, 1, 160, 90)
	if a, _ := os.ReadFile(frameFile(withFont, 1)); string(a) == mustRead(t, frameFile(builtin, 1)) {
		t.Error("the title must be drawn in the design's font, not in Blender's")
	}
	lines := queryBlend(t, r, info.Path, filepath.Join(withFont, "a.blend"), `import bpy
for f in bpy.data.fonts:
    print('Q', f.name, f.packed_file is not None, f.packed_file.size if f.packed_file else 0)
`)
	want, err := os.Stat(fonts["display"])
	if err != nil {
		t.Fatal(err)
	}
	var packed bool
	for _, l := range lines {
		if strings.Contains(l, " True ") && strings.HasSuffix(l, " "+strconv.FormatInt(want.Size(), 10)) {
			packed = true
		}
	}
	if !packed {
		t.Errorf("the .blend must hold the display font packed (%d bytes): %q", want.Size(), lines)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The kept .blend, copied to another folder and opened in a clean Blender, renders the frames the pipeline made; and the
// accent sphere is brighter on the frame of each beat than on the one before.
func TestRealTheReopenedBlendGivesTheFramesOfThePipelineAndTheSphereHitsOnTheBeat(t *testing.T) {
	info, r := realBlender(t)
	dir, _ := buildExample(t, 320, 180, 8)
	other := t.TempDir()
	blend := filepath.Join(other, "reopened.blend")
	if err := os.WriteFile(blend, []byte(mustRead(t, filepath.Join(dir, "titulo.blend"))), 0o644); err != nil {
		t.Fatal(err)
	}
	args := []string{"--factory-startup", "-b", "-noaudio", blend, "--disable-autoexec", "-o", "//g_###", "-F", "PNG", "-f", "1,75,150"}
	var env []string
	if os.Getenv("CUDA_VISIBLE_DEVICES") != "" {
		args = append(args, "--", "--cycles-device", "OPTIX") // the same card the pipeline rendered on
		env = os.Environ()
	}
	if out, err := r.Run(context.Background(), info.Path, args, runner.WithEnv(env...)); err != nil {
		t.Fatalf("reopened render: %v\n%s", err, out)
	}
	for _, n := range []int{1, 75, 150} {
		score := ssim(t, filepath.Join(dir, "frames", fmt.Sprintf("f_%06d.png", n)), filepath.Join(other, fmt.Sprintf("g_%03d.png", n)))
		t.Logf("frame %d: SSIM %.4f", n, score)
		if score < 0.99 {
			t.Errorf("frame %d of the reopened .blend differs from the pipeline's: SSIM %.4f, want >= 0.99", n, score)
		}
	}
	mean := func(n int) float64 {
		return meanLuminance(t, filepath.Join(dir, "frames", fmt.Sprintf("f_%06d.png", n)))
	}
	for b := 1; b <= 9; b++ {
		f := b*15 + 1 // beat b at 120 bpm and 30 fps
		before, on, after := mean(f-1), mean(f), mean(f+1)
		if !(on > before) || !(on >= after) {
			t.Errorf("beat %d (frame %d): brightness before %.3f, on the beat %.3f, after %.3f: the beat frame must be the brightest, and the one before dimmer", b, f, before, on, after)
		}
	}
}

// With DUMP_VIDEOS_DIR set, renders the example at the project's size at 16 and at 32 samples on the card the process is
// given, compares every 15th frame with SSIM, and leaves the 32-sample clip, the .blend, frames 1, 30, 31, 75 and 150, the
// numbers and the log in $DUMP_VIDEOS_DIR/blender-build-2/. The default for built scenes is chosen from the result.
func TestRealMeasureTheExampleAt16And32Samples(t *testing.T) {
	if os.Getenv("DUMP_VIDEOS_DIR") == "" {
		t.Skip("set DUMP_VIDEOS_DIR (and VERITMO_REAL_BLENDER=1) to measure 16 against 32 samples at 1280x720")
	}
	info, r := realBlender(t)
	type run struct {
		dir     string
		seconds float64
	}
	measure := func(samples int) run {
		start := time.Now()
		dir, _ := buildExample(t, 1280, 720, samples)
		return run{dir, time.Since(start).Seconds()}
	}
	a, b := measure(16), measure(32)
	var sum float64
	var n int
	var per []string
	for f := 1; f <= 150; f += 15 {
		s := ssim(t, filepath.Join(a.dir, "frames", fmt.Sprintf("f_%06d.png", f)), filepath.Join(b.dir, "frames", fmt.Sprintf("f_%06d.png", f)))
		sum += s
		n++
		per = append(per, fmt.Sprintf("%d:%.4f", f, s))
	}
	mean := sum / float64(n)
	choice := 32
	if mean >= 0.98 {
		choice = 16
	}
	if err := Join(context.Background(), r, b.dir, "frames", 30, 5, filepath.Join(b.dir, "titulo.mp4")); err != nil {
		t.Fatal(err)
	}
	dumpTo(t, filepath.Join(os.Getenv("DUMP_VIDEOS_DIR"), "blender-build-2"), b.dir, []int{1, 30, 31, 75, 150}, []string{
		"scene: testdata/blender-build (titulo.scene.json), 5 s, 150 frames, 1280x720 at 30 fps",
		fmt.Sprintf("Blender %s, CUDA_VISIBLE_DEVICES=%q", info.Version, os.Getenv("CUDA_VISIBLE_DEVICES")),
		fmt.Sprintf("16 samples: %.1f s to build and render the 150 frames", a.seconds),
		fmt.Sprintf("32 samples: %.1f s to build and render the 150 frames", b.seconds),
		fmt.Sprintf("mean SSIM of 16 against 32 over every 15th frame (%d frames): %.4f", n, mean),
		"per frame: " + strings.Join(per, " "),
		fmt.Sprintf("default for built scenes: %d samples (16 when the mean is >= 0.98, else 32)", choice),
	})
	t.Logf("16 samples %.1f s, 32 samples %.1f s, mean SSIM %.4f -> default %d", a.seconds, b.seconds, mean, choice)
}

// One frame rendered alone is the frame the whole clip has: the keys are placed against the whole scene.
func TestRealAFrameRenderedAloneIsTheFrameOfTheWholeClip(t *testing.T) {
	info, r := realBlender(t)
	dir, _ := buildExample(t, 320, 180, 8) // the whole clip, frames 1..150
	sources, err := filepath.Abs("../../../testdata/blender-build/sources")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := Load(filepath.Join(sources, "titulo.scene.json"), 5, realEnv(t, sources))
	if err != nil {
		t.Fatal(err)
	}
	alone := t.TempDir()
	if _, err := Render(context.Background(), r, info.Path, Params{
		Dir: alone, Out: "f", Samples: 8, Device: "auto", GPU: os.Getenv("CUDA_VISIBLE_DEVICES") != "", Width: 320, Height: 180, Count: 150, FPS: 30,
		Spec: spec, Sources: sources, Fonts: realFonts(t), Frame: 31,
	}, nil); err != nil {
		t.Fatal(err)
	}
	if n := countFrames(filepath.Join(alone, "f")); n != 1 {
		t.Fatalf("one frame was asked for and %d were rendered", n)
	}
	score := ssim(t, filepath.Join(dir, "frames", "f_000031.png"), filepath.Join(alone, "f", "f_000031.png"))
	t.Logf("frame 31 alone against the whole clip: SSIM %.4f", score)
	if score < 0.99 {
		t.Errorf("frame 31 rendered alone differs from the clip's: SSIM %.4f, want >= 0.99", score)
	}
}

// The preview of the example is a small PNG of the asked width, drawn on the CPU.
func TestRealThePreviewIsASmallPNGOnTheCPU(t *testing.T) {
	info, r := realBlender(t)
	sources, err := filepath.Abs("../../../testdata/blender-build/sources")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := Load(filepath.Join(sources, "titulo.scene.json"), 5, realEnv(t, sources))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CUDA_VISIBLE_DEVICES", "1") // a card is offered: the preview must not use it
	start := time.Now()
	data, err := Preview(context.Background(), r, info.Path, spec, PreviewOptions{T: 1, Seconds: 5, FPS: 30, Width: 480, AspectW: 1280, AspectH: 720, Sources: sources, Fonts: realFonts(t)})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width != 480 || cfg.Height != 270 {
		t.Fatalf("the preview: %+v %v", cfg, err)
	}
	t.Logf("preview of 480x270 on the CPU: %.1f s, %d bytes", time.Since(start).Seconds(), len(data))
}

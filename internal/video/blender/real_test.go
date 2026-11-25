package blender

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"veritmo/internal/platform/runner"
)

// Real Blender: skipped unless VERITMO_REAL_BLENDER=1 (it needs Blender installed).
func realBlender(t *testing.T) (Info, runner.CommandRunner) {
	t.Helper()
	if os.Getenv("VERITMO_REAL_BLENDER") != "1" {
		t.Skip("set VERITMO_REAL_BLENDER=1 to run against the installed Blender")
	}
	if _, err := exec.LookPath("blender"); err != nil {
		t.Skip("no blender")
	}
	r := runner.NewExecRunner()
	info, err := Detect(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return info, r
}

func TestRealDetectAndDevices(t *testing.T) {
	info, r := realBlender(t)
	t.Logf("blender %s at %s", info.Version, info.Path)
	devs, err := Devices(context.Background(), r, info.Path, "CUDA_VISIBLE_DEVICES=")
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 1 || devs[0].Type != "CPU" {
		t.Errorf("with the cards hidden only the CPU must be left: %+v", devs)
	}
	devs, err = Devices(context.Background(), r, info.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("devices: %+v", devs)
}

// makeBlend writes a small animated scene: a text object "Title", a sphere with the material "Ball" and a camera.
func makeBlend(t *testing.T, r runner.CommandRunner, bin, path string, frames int) {
	t.Helper()
	py := fmt.Sprintf(`import bpy, math
bpy.ops.wm.read_factory_settings(use_empty=True)
s = bpy.context.scene
s.frame_start, s.frame_end = 1, %d
bpy.ops.mesh.primitive_uv_sphere_add(radius=1, location=(0, 0, 1))
sp = bpy.context.object
m = bpy.data.materials.new('Ball'); m.use_nodes = True
sp.data.materials.append(m)
sp.keyframe_insert('location', frame=1); sp.location.z = 2; sp.keyframe_insert('location', frame=%d)
bpy.ops.object.text_add(location=(-1, 0, 0)); bpy.context.object.name = 'Title'; bpy.context.object.data.body = 'x'
bpy.ops.object.light_add(type='SUN', location=(2, -3, 5))
bpy.ops.object.camera_add(location=(0, -7, 2), rotation=(math.radians(80), 0, 0)); s.camera = bpy.context.object
bpy.ops.wm.save_as_mainfile(filepath=%q)
`, frames, frames, path)
	if _, err := r.Run(context.Background(), bin, []string{"--factory-startup", "-b", "-noaudio", "--python-exit-code", "1", "--python-expr", py}); err != nil {
		t.Fatal(err)
	}
}

func TestRealRenderInjectsAndCuts(t *testing.T) {
	info, r := realBlender(t)
	dir := t.TempDir()
	blend := filepath.Join(dir, "scene.blend")
	makeBlend(t, r, info.Path, blend, 4)
	render := func(out string, inject map[string]any, device string, gpu bool, count int) Result {
		res, err := Render(context.Background(), r, info.Path, Params{
			File: blend, Dir: dir, Out: out, Inject: inject, Samples: 4, Device: device, GPU: gpu, Width: 160, Height: 90, Count: count,
		}, nil)
		if err != nil {
			t.Fatalf("%s: %v", out, err)
		}
		return res
	}
	awkward := "He said \"hi\"\\n 'x'\nline 2"
	red := render("red", map[string]any{"Title": awkward, "Ball": "#ff0000"}, "cpu", false, 10)
	if red.Device != "CPU" || red.First != 1 || red.Last != 4 {
		t.Errorf("a 4-frame file asked for 10: the range is the file's, got %+v", red)
	}
	if n := countFrames(filepath.Join(dir, "red")); n != 4 {
		t.Errorf("frames = %d, want 4", n)
	}
	render("blue", map[string]any{"Ball": "#0000ff"}, "cpu", false, 2)
	a, _ := os.ReadFile(filepath.Join(dir, "red", "f_000001.png"))
	b, _ := os.ReadFile(filepath.Join(dir, "blue", "f_000001.png"))
	if string(a) == string(b) {
		t.Error("another colour must give another picture")
	}
	if _, err := Render(context.Background(), r, info.Path, Params{File: blend, Dir: dir, Out: "bad", Inject: map[string]any{"Nope": "x"}, Samples: 4, Width: 16, Height: 16, Count: 1}, nil); err == nil || !strings.Contains(err.Error(), "Nope") {
		t.Errorf("an unknown name must be refused naming it: %v", err)
	}
	if _, err := Render(context.Background(), r, info.Path, Params{File: blend, Dir: dir, Out: "gpu", Device: "gpu", GPU: false, Samples: 4, Width: 16, Height: 16, Count: 1}, []string{"CUDA_VISIBLE_DEVICES="}); err == nil || !strings.Contains(err.Error(), "no usable GPU") {
		t.Errorf("device gpu without a card: %v", err)
	}
}

func TestRealRenderOnOneGPU(t *testing.T) {
	info, r := realBlender(t)
	dir := t.TempDir()
	blend := filepath.Join(dir, "scene.blend")
	makeBlend(t, r, info.Path, blend, 3)
	res, err := Render(context.Background(), r, info.Path, Params{File: blend, Dir: dir, Out: "g", Samples: 4, Device: "auto", GPU: true, Width: 160, Height: 90, Count: 3}, []string{"CUDA_VISIBLE_DEVICES=1"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Device != "OPTIX" && res.Device != "CUDA" {
		t.Errorf("with a card given the render must use it, got %q", res.Device)
	}
	devs, _ := Devices(context.Background(), r, info.Path, "CUDA_VISIBLE_DEVICES=1")
	gpus := 0
	for _, d := range devs {
		if d.Type == "OPTIX" {
			gpus++
		}
	}
	if gpus != 1 {
		t.Errorf("with one card pinned Cycles must see exactly one: %+v", devs)
	}
}

func TestRealJoinHoldsTheLastFrameAndCuts(t *testing.T) {
	info, r := realBlender(t)
	dir := t.TempDir()
	blend := filepath.Join(dir, "scene.blend")
	makeBlend(t, r, info.Path, blend, 4) // 4 frames of animation
	if _, err := Render(context.Background(), r, info.Path, Params{File: blend, Dir: dir, Out: "f", Samples: 2, Device: "cpu", Width: 160, Height: 90, Count: 24}, nil); err != nil {
		t.Fatal(err)
	}
	duration := func(out string) float64 {
		b, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-count_frames", "-show_entries", "stream=nb_read_frames", "-of", "csv=p=0", out).Output()
		if err != nil {
			t.Fatal(err)
		}
		n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		return float64(n) / 24
	}
	for _, secs := range []float64{3, 0.1} { // longer than the 4 frames: held; shorter: cut
		out := filepath.Join(dir, fmt.Sprintf("o%v.mp4", secs))
		if err := Join(context.Background(), runner.NewExecRunner(), dir, "f", 24, secs, out); err != nil {
			t.Fatal(err)
		}
		if got := duration(out); got < secs-0.05 || got > secs+0.05 {
			t.Errorf("a scene of %v s gave %v s", secs, got)
		}
	}
}

func TestRealAFileWithAnAutoRunScriptDoesNotRunIt(t *testing.T) {
	info, r := realBlender(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	blend := filepath.Join(dir, "boom.blend")
	py := fmt.Sprintf(`import bpy
bpy.ops.wm.read_factory_settings(use_empty=True)
tx = bpy.data.texts.new('boom.py')
tx.write("open('%s', 'w').write('x')")
tx.use_module = True
bpy.ops.object.camera_add(); bpy.context.scene.camera = bpy.context.object
bpy.ops.wm.save_as_mainfile(filepath=%q)
`, marker, blend)
	if _, err := r.Run(context.Background(), info.Path, []string{"--factory-startup", "-b", "-noaudio", "--python-exit-code", "1", "--python-expr", py}); err != nil {
		t.Fatal(err)
	}
	os.Remove(marker)
	// control: opened without the flag, the script runs (when this Blender allows auto-run at all)
	r.Run(context.Background(), info.Path, []string{"--factory-startup", "-b", "-noaudio", blend, "--python-expr", "pass"})
	_, controlRan := os.Stat(marker)
	os.Remove(marker)
	if _, err := Render(context.Background(), r, info.Path, Params{File: blend, Dir: dir, Out: "f", Samples: 1, Device: "cpu", Width: 16, Height: 16, Count: 1}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the script of the file ran during the render")
	}
	if controlRan != nil {
		t.Log("note: this Blender does not auto-run scripts even without the flag, so the check is weak here")
	}
}

// looseSpec is a description resolved but not checked (see Resolve), for the tests of what the script does with one that
// Load would have refused.
func looseSpec(t *testing.T, doc string) *Spec {
	t.Helper()
	s, err := Resolve([]byte(doc), Env{Palette: DefaultPalette})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// realEnv is the environment the example is checked in: its palette, the tempo of its project (120 bpm) and a design with
// both fonts of the pack (display Playfair Display, body JetBrains Mono).
func realEnv(t *testing.T, sources string) Env {
	t.Helper()
	return Env{Palette: PaletteFromDesign("#0b0f14", "#e8f1ec", "#ff3b30"), Sources: sources, BPM: 120, DesignFonts: map[string]bool{FontDisplay: true, FontBody: true}}
}

// realFonts decodes the fonts of the design of the example to TrueType in a temporary folder.
func realFonts(t *testing.T) map[string]string {
	t.Helper()
	dir := t.TempDir()
	out := map[string]string{}
	for role, file := range map[string]string{FontDisplay: "playfair-display-700", FontBody: "jetbrains-mono-400"} {
		raw, err := os.ReadFile("../../music/musicmotion/fonts/" + file + ".woff2")
		if err != nil {
			t.Fatal(err)
		}
		if out[role], err = EnsureTTF(dir, raw); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// buildExample renders the example project (testdata/blender-build) as a built scene at a small size and returns the
// folder it worked in. When DUMP_VIDEOS_DIR is set, the clip, the .blend, three frames and the log are copied to
// <it>/blender-build/ (the samba share) so the owner can look at them; nothing is written there otherwise.
func buildExample(t *testing.T, w, h, samples int) (string, Result) {
	t.Helper()
	info, r := realBlender(t)
	dir := t.TempDir()
	sources, err := filepath.Abs("../../../testdata/blender-build/sources")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := Load(filepath.Join(sources, "titulo.scene.json"), 5, realEnv(t, sources))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Render(context.Background(), r, info.Path, Params{
		Dir: dir, Out: "frames", Samples: samples, Device: "auto", GPU: os.Getenv("CUDA_VISIBLE_DEVICES") != "", Width: w, Height: h, Count: 150, FPS: 30,
		Spec: spec, Sources: sources, Fonts: realFonts(t), BlendOut: "titulo.blend",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return dir, res
}

func TestRealBuiltSceneRendersKeepsTheBlendAndMoves(t *testing.T) {
	dir, res := buildExample(t, 320, 180, 8)
	if res.First != 1 || res.Last != 150 {
		t.Errorf("range = %d..%d, want 1..150", res.First, res.Last)
	}
	if n := countFrames(filepath.Join(dir, "frames")); n != 150 {
		t.Fatalf("frames = %d, want 150", n)
	}
	if fi, err := os.Stat(filepath.Join(dir, "titulo.blend")); err != nil || fi.Size() == 0 {
		t.Errorf("the .blend was not kept: %v", err)
	}
	frame := func(n int) string {
		b, err := os.ReadFile(filepath.Join(dir, "frames", fmt.Sprintf("f_%06d.png", n)))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if frame(1) == frame(18) || frame(18) == frame(150) || frame(1) == frame(150) {
		t.Error("frames 1, 18 and 150 must differ: the title rises and the camera moves")
	}
	out := filepath.Join(dir, "titulo.mp4")
	if err := Join(context.Background(), runner.NewExecRunner(), dir, "frames", 30, 5, out); err != nil {
		t.Fatal(err)
	}
	b, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", out).Output()
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := strconv.ParseFloat(strings.TrimSpace(string(b)), 64); d < 4.95 || d > 5.05 {
		t.Errorf("the clip lasts %v s, want 5", d)
	}
	dumpBuilt(t, dir, res)
}

// dumpBuilt copies what a built-scene test made to $DUMP_VIDEOS_DIR/blender-build/ when that is set (the samba share,
// for the owner to look at: nothing there is deleted). Ordinary runs leave nothing behind.
func dumpBuilt(t *testing.T, dir string, res Result, log ...string) {
	t.Helper()
	if os.Getenv("DUMP_VIDEOS_DIR") == "" {
		return
	}
	dumpTo(t, filepath.Join(os.Getenv("DUMP_VIDEOS_DIR"), "blender-build"), dir, []int{1, 18, 150}, log)
	t.Logf("dumped (device %s)", res.Device)
}

// dumpTo copies the clip, the .blend, the frames asked for and the log of a built example (rendered in dir) to the folder to.
func dumpTo(t *testing.T, to, dir string, frames []int, log []string) {
	t.Helper()
	if err := os.MkdirAll(to, 0o755); err != nil {
		t.Fatal(err)
	}
	copy := func(from, name string) {
		b, err := os.ReadFile(from)
		if err != nil {
			t.Errorf("dump %s: %v", name, err)
			return
		}
		if err := os.WriteFile(filepath.Join(to, name), b, 0o644); err != nil {
			t.Errorf("dump %s: %v", name, err)
		}
	}
	copy(filepath.Join(dir, "titulo.mp4"), "titulo.mp4")
	copy(filepath.Join(dir, "titulo.blend"), "titulo.blend")
	for _, n := range frames {
		copy(filepath.Join(dir, "frames", fmt.Sprintf("f_%06d.png", n)), fmt.Sprintf("frame-%03d.png", n))
	}
	if len(log) > 0 {
		if err := os.WriteFile(filepath.Join(to, "run.log"), []byte(strings.Join(log, "\n")+"\n"), 0o644); err != nil {
			t.Errorf("dump run.log: %v", err)
		}
	}
}

// With DUMP_VIDEOS_DIR set, renders the example at the project's own size (1280x720, 30 fps, 32 samples) on the card the
// process is given (CUDA_VISIBLE_DEVICES), times it, joins the clip and leaves everything on the share for the owner.
func TestRealBuiltExampleAtTheProjectsSize(t *testing.T) {
	if os.Getenv("DUMP_VIDEOS_DIR") == "" {
		t.Skip("set DUMP_VIDEOS_DIR (and VERITMO_REAL_BLENDER=1) to measure the example at 1280x720 and keep the result")
	}
	started := time.Now()
	dir, res := buildExample(t, 1280, 720, 32)
	rendered := time.Since(started)
	out := filepath.Join(dir, "titulo.mp4")
	joinStart := time.Now()
	if err := Join(context.Background(), runner.NewExecRunner(), dir, "frames", 30, 5, out); err != nil {
		t.Fatal(err)
	}
	info, _ := Detect(context.Background(), runner.NewExecRunner())
	dumpBuilt(t, dir, res,
		"scene: testdata/blender-build (titulo.scene.json), 5 s, 150 frames",
		fmt.Sprintf("size: 1280x720 at 30 fps, 32 samples, Blender %s", info.Version),
		fmt.Sprintf("CUDA_VISIBLE_DEVICES=%q device used: %s", os.Getenv("CUDA_VISIBLE_DEVICES"), res.Device),
		fmt.Sprintf("build + render of the 150 frames: %.1f s; joining them into the clip: %.1f s", rendered.Seconds(), time.Since(joinStart).Seconds()),
		fmt.Sprintf("started %s", started.Format(time.RFC3339)))
}

func TestRealBuiltAwkwardTextIsKeptAsDataAndNothingRuns(t *testing.T) {
	info, r := realBlender(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran") // Blender runs in dir, so a relative `touch ran` would land here
	awkward := "He said \"hi\" \\n 'x'\nline 2 __import__('os').system('touch ran') %s {x} é"
	doc, _ := json.Marshal(map[string]any{
		"version": 1,
		"camera":  map[string]any{"keys": []any{map[string]any{"t": 0, "location": []float64{0, -6, 1}, "target": []float64{0, 0, 0}}}},
		"objects": []any{map[string]any{"id": "t", "type": "text", "text": awkward, "size": 1}},
	})
	spec := filepath.Join(dir, "a.scene.json")
	os.WriteFile(spec, doc, 0o644)
	loaded, err := Load(spec, 1, Env{Palette: DefaultPalette, Sources: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Render(context.Background(), r, info.Path, Params{
		Dir: dir, Out: "f", Samples: 1, Device: "cpu", Width: 32, Height: 18, Count: 1, FPS: 24,
		Spec: loaded, Sources: dir, BlendOut: "a.blend",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a command in the text of a description ran")
	}
	// the text in the kept .blend is exactly the one written
	out, err := r.Run(context.Background(), info.Path, []string{"--factory-startup", "-b", "-noaudio", filepath.Join(dir, "a.blend"), "--disable-autoexec",
		"--python-expr", "import bpy, json; print('VERITMO_BODY ' + json.dumps(bpy.data.objects['t'].data.body))"})
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(line, "VERITMO_BODY "); ok {
			json.Unmarshal([]byte(v), &got)
		}
	}
	if got != awkward {
		t.Errorf("the text became %q", got)
	}
}

func TestRealAFailedRenderOfABuiltSceneStillLeavesTheBlend(t *testing.T) {
	info, r := realBlender(t)
	dir := t.TempDir()
	spec := looseSpec(t, `{"version":1,"camera":{"keys":[{"t":0,"location":[0,-6,1],"target":[0,0,0]}]},"objects":[{"id":"c","type":"cube","size":1}]}`)
	_, err := Render(context.Background(), r, info.Path, Params{
		Dir: dir, Out: "f", Samples: 1, Device: "gpu", GPU: false, Width: 32, Height: 18, Count: 1, FPS: 24,
		Spec: spec, Sources: dir, BlendOut: "a.blend",
	}, []string{"CUDA_VISIBLE_DEVICES="})
	if err == nil || !strings.Contains(err.Error(), "no usable GPU") {
		t.Fatalf("the render must fail on the device: %v", err)
	}
	if fi, statErr := os.Stat(filepath.Join(dir, "a.blend")); statErr != nil || fi.Size() == 0 {
		t.Errorf("a render that fails after the build must leave the .blend: %v", statErr)
	}
}

func TestRealABrokenDescriptionStopsWithItsOwnMessage(t *testing.T) {
	info, r := realBlender(t)
	dir := t.TempDir()
	// Go would refuse this one (Load checks it); the script is the second line of defence, so it is handed unchecked
	spec := looseSpec(t, `{"version":1,"camera":{"keys":[{"t":0}]},"objects":[{"id":"c","type":"monkey","size":1}]}`)
	_, err := Render(context.Background(), r, info.Path, Params{
		Dir: dir, Out: "f", Samples: 1, Device: "cpu", Width: 32, Height: 18, Count: 1, FPS: 24,
		Spec: spec, Sources: dir, BlendOut: "a.blend",
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "build:") {
		t.Errorf("err = %v", err)
	}
}

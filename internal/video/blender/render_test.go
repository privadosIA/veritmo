package blender

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"veritmo/internal/platform/runner"
)

// fakeRender makes the fake Blender write n frames into the folder it was told and report a device.
func fakeRender(t *testing.T, n int, device string) *runner.FakeRunner {
	t.Helper()
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		var cfg map[string]any
		for _, e := range inv.Env {
			if v, ok := strings.CutPrefix(e, EnvVar+"="); ok {
				if err := json.Unmarshal([]byte(v), &cfg); err != nil {
					t.Errorf("the settings are not JSON: %v", err)
				}
			}
		}
		dir, _ := cfg["out"].(string)
		for i := 1; i <= n; i++ {
			os.WriteFile(filepath.Join(dir, fmt.Sprintf("f_%06d.png", i)), []byte("png"), 0o644)
		}
		return runner.ScriptedResult{Stdout: []byte("Blender\nVERITMO_DEVICE " + device + "\nVERITMO_RANGE 1 " + fmt.Sprint(n) + "\n")}
	})
	return fr
}

func TestRender_TextTravelsInTheEnvironmentNeverInTheScript(t *testing.T) {
	dir := t.TempDir()
	awkward := "He said \"hi\"\\n\nline 2 ' ''' \u00e9"
	fr := fakeRender(t, 3, "CPU")
	_, err := Render(context.Background(), fr, "/usr/bin/blender", Params{
		File: "turn.blend", Dir: dir, Out: "tmp/f", Inject: map[string]any{"Title": awkward},
		Samples: 8, Device: "auto", Width: 160, Height: 90, Count: 3,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := fr.Calls()[0]
	if strings.Contains(strings.Join(c.Args, "\x00"), "He said") {
		t.Fatal("the text must not be in the arguments")
	}
	if c.Args[len(c.Args)-1] != Script() {
		t.Error("the script must be exactly the fixed one")
	}
	var cfg struct {
		Inject map[string]any `json:"inject"`
	}
	for _, e := range c.Env {
		if v, ok := strings.CutPrefix(e, EnvVar+"="); ok {
			json.Unmarshal([]byte(v), &cfg)
		}
	}
	if cfg.Inject["Title"] != awkward {
		t.Errorf("the text arrived as %q", cfg.Inject["Title"])
	}
}

func TestRender_ArgumentsDisableAutoexecAndAreScriptless(t *testing.T) {
	args := BuildArgs("/p/turn.blend")
	joined := strings.Join(args[:len(args)-1], " ")
	for _, want := range []string{"--factory-startup", "-b", "/p/turn.blend", "--disable-autoexec", "--python-exit-code 1", "--python-expr"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %q", want, joined)
		}
	}
	if strings.Contains(joined, ".py") {
		t.Error("no Python file is involved")
	}
}

func TestRender_PassesThePinsAndReportsTheDevice(t *testing.T) {
	dir := t.TempDir()
	fr := fakeRender(t, 2, "OPTIX")
	res, err := Render(context.Background(), fr, "blender", Params{File: "a.blend", Dir: dir, Out: "f", GPU: true, Width: 64, Height: 64, Count: 2}, []string{"CUDA_VISIBLE_DEVICES=1"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Device != "OPTIX" || res.First != 1 || res.Last != 2 {
		t.Errorf("result = %+v", res)
	}
	c := fr.Calls()[0]
	if c.Env[0] != "CUDA_VISIBLE_DEVICES=1" || c.Dir != dir {
		t.Errorf("env=%v dir=%q", c.Env, c.Dir)
	}
}

func TestRender_EmptiesTheFramesFolderFirst(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "f"), 0o755)
	stale := filepath.Join(dir, "f", "f_000099.png")
	os.WriteFile(stale, []byte("old"), 0o644)
	if _, err := Render(context.Background(), fakeRender(t, 1, "CPU"), "blender", Params{File: "a.blend", Dir: dir, Out: "f", Width: 8, Height: 8, Count: 1}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("a frame of an earlier render was kept")
	}
}

func TestRender_ShowsTheScriptsOwnError(t *testing.T) {
	fr := runner.NewFakeRunner()
	fr.Script(runner.ScriptedResult{Err: fmt.Errorf("exit status 1 (stderr: noise\nVERITMO_ERROR inject: there is no text object, material or object property named 'Nope' in the file\n)")})
	// the fake returns the error as a whole: explain() works on the captured stderr, so check the fallback keeps it
	_, err := Render(context.Background(), fr, "blender", Params{File: "a.blend", Dir: t.TempDir(), Out: "f", Width: 8, Height: 8, Count: 1}, nil)
	if err == nil || !strings.Contains(err.Error(), "Nope") {
		t.Fatalf("err = %v", err)
	}
}

func TestExplainPrefersTheScriptsLine(t *testing.T) {
	err := explain(fmt.Errorf("exit status 1"), "x\nVERITMO_ERROR device: gpu was asked for\ny")
	if err.Error() != "device: gpu was asked for" {
		t.Errorf("err = %v", err)
	}
}

func TestRender_NoFramesIsAnError(t *testing.T) {
	fr := runner.NewFakeRunner()
	fr.Script(runner.ScriptedResult{Stdout: []byte("VERITMO_DEVICE CPU\n")})
	if _, err := Render(context.Background(), fr, "blender", Params{File: "a.blend", Dir: t.TempDir(), Out: "f", Width: 8, Height: 8, Count: 1}, nil); err == nil || !strings.Contains(err.Error(), "no frames") {
		t.Errorf("err = %v", err)
	}
	if _, err := Render(context.Background(), fr, "blender", Params{File: "a.blend", Dir: t.TempDir(), Out: "f"}, nil); err == nil {
		t.Error("a scene with no frames to render is an error")
	}
}

func TestJoinArgsHoldsAndCutsToTheSceneLength(t *testing.T) {
	a := strings.Join(JoinArgs("/p/f", 24, 5, "/p/out.mp4"), " ")
	for _, want := range []string{"-framerate 24", "-pattern_type glob", "/p/f/f_*.png", "tpad=stop_mode=clone:stop_duration=5.000", "-t 5.000", "/p/out.mp4"} {
		if !strings.Contains(a, want) {
			t.Errorf("missing %q in %q", want, a)
		}
	}
}

func TestSettingsDefaults(t *testing.T) {
	data, _ := Params{Width: 1, Height: 1, Count: 1}.Settings("/o")
	var m map[string]any
	json.Unmarshal(data, &m)
	if m["device"] != "auto" || m["gpu"] != false || m["out"] != "/o" {
		t.Errorf("settings = %v", m)
	}
	if _, ok := m["inject"].(map[string]any); !ok {
		t.Errorf("inject must be an object, got %T", m["inject"])
	}
}

func TestBuildArgs_ABuiltSceneHasNoFileAndStaysScriptless(t *testing.T) {
	args := BuildArgs("")
	joined := strings.Join(args[:len(args)-1], " ")
	if joined != "--factory-startup -b -noaudio --disable-autoexec --python-exit-code 1 --python-expr" {
		t.Errorf("args = %q", joined)
	}
	if args[len(args)-1] != Script() || strings.Contains(joined, ".py") {
		t.Error("the script is the fixed inline one and no Python file is involved")
	}
}

func TestRender_ABuiltSceneTravelsAsAPathAndSettings(t *testing.T) {
	dir := t.TempDir()
	fr := fakeRender(t, 3, "CPU")
	spec, err := Resolve([]byte(minimal), testEnv(""))
	if err != nil {
		t.Fatal(err)
	}
	fonts := map[string]string{"display": "/cache/fonts/a.ttf"}
	_, err = Render(context.Background(), fr, "blender", Params{
		Dir: dir, Out: "f", Width: 16, Height: 9, Count: 3, FPS: 30,
		Spec: spec, Sources: "sources", Fonts: fonts, BlendOut: "out/t.blend",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := fr.Calls()[0]
	for _, a := range c.Args {
		if strings.HasSuffix(a, ".blend") || strings.HasSuffix(a, ".scene.json") {
			t.Errorf("no file goes on the command line, got %q", a)
		}
	}
	var cfg struct {
		Build, Sources string
		BlendOut       string `json:"blend_out"`
		FPS            int    `json:"fps"`
		Fonts          map[string]string
	}
	for _, e := range c.Env {
		if v, ok := strings.CutPrefix(e, EnvVar+"="); ok {
			json.Unmarshal([]byte(v), &cfg)
		}
	}
	if cfg.Build != filepath.Join(dir, "f", DescriptionFile) || cfg.Sources != filepath.Join(dir, "sources") || cfg.BlendOut != filepath.Join(dir, "out/t.blend") || cfg.FPS != 30 || cfg.Fonts["display"] != fonts["display"] {
		t.Errorf("settings = %+v", cfg)
	}
	// What the script reads is the resolved description, written next to the frames.
	written, err := os.ReadFile(filepath.Join(dir, "f", DescriptionFile))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := spec.JSON()
	if string(written) != string(want) {
		t.Errorf("the description written is not the resolved one:\n%s\n%s", written, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "out")); err != nil {
		t.Error("the folder of the .blend is created before Blender starts")
	}
}

func TestRender_ABuiltSceneNeedsTheFrameRateAndNoFile(t *testing.T) {
	spec, _ := Resolve([]byte(minimal), testEnv(""))
	for name, p := range map[string]Params{
		"no fps":    {Spec: spec, Count: 1},
		"with file": {Spec: spec, File: "a.blend", FPS: 24, Count: 1},
	} {
		p.Dir, p.Out, p.Width, p.Height = t.TempDir(), "f", 8, 8
		if _, err := Render(context.Background(), fakeRender(t, 1, "CPU"), "blender", p, nil); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestSettings_AFileSceneCarriesNothingOfTheBuild(t *testing.T) {
	data, _ := Params{File: "a.blend", Width: 1, Height: 1, Count: 1}.Settings("/o")
	var m map[string]any
	json.Unmarshal(data, &m)
	for _, k := range []string{"build", "blend_out", "fonts", "sources", "fps"} {
		if _, ok := m[k]; ok {
			t.Errorf("a file scene must not set %q", k)
		}
	}
}

// builderPart is the part of the script that reads a description.
func builderPart(t *testing.T) string {
	t.Helper()
	i := strings.Index(script, "# --- build")
	j := strings.Index(script, "if cfg.get('build'):\n    try:")
	if i < 0 || j < i {
		t.Fatal("the builder is not where the test expects it")
	}
	return script[i:j]
}

func TestScript_NothingOfADescriptionIsEvaluated(t *testing.T) {
	for _, bad := range []string{"eval(", "exec(", "compile(", "__import__", "getattr(bpy.ops", "getattr(bpy", "importlib", "os.system", "subprocess"} {
		if strings.Contains(script, bad) {
			t.Errorf("the script contains %q", bad)
		}
	}
	b := builderPart(t)
	for _, bad := range []string{"%", ".format(", "setattr(bpy"} {
		if strings.Contains(b, bad) {
			t.Errorf("the builder contains %q: values of a description are never formatted into text", bad)
		}
	}
	if regexp.MustCompile(`(^|[^A-Za-z0-9_])[fF]['"]`).MatchString(b) {
		t.Error("the builder has an f-string: values of a description are never formatted into text")
	}
	// setattr is used on the objects the builder itself made, with attribute names that are fixed in its own code
	for _, line := range strings.Split(b, "\n") {
		if strings.Contains(line, "setattr(") && !strings.Contains(line, "setattr(ob, attr, value)") {
			t.Errorf("unexpected setattr: %s", line)
		}
	}
	for _, call := range []string{"key_channel(ob, 'location'", "key_channel(ob, 'rotation_euler'", "key_channel(ob, 'scale'", "key_channel(cam, 'location'", "key_channel(cam, 'rotation_euler'", "key_channel(target, 'location'"} {
		if !strings.Contains(b, call) {
			t.Errorf("the attribute names come only from fixed calls, %q is missing", call)
		}
	}
}

func TestScript_TheEasingTableIsTheDesigns(t *testing.T) {
	for name, want := range map[string]string{
		"linear": "('LINEAR', 'AUTO')", "inout": "('BEZIER', 'AUTO')", "in": "('CUBIC', 'EASE_IN')", "out": "('CUBIC', 'EASE_OUT')",
		"back": "('BACK', 'EASE_OUT')", "bounce": "('BOUNCE', 'EASE_OUT')", "elastic": "('ELASTIC', 'EASE_OUT')",
		"step": "('CONSTANT', 'AUTO')",
	} {
		if !strings.Contains(script, "'"+name+"': "+want) {
			t.Errorf("ease %q is not mapped to %s", name, want)
		}
	}
	if !strings.Contains(script, "AUTO_CLAMPED") {
		t.Error("the Bezier handles are auto-clamped")
	}
	if len(eases) != 8 {
		t.Errorf("the contract has %d easings, the table of the script has 8", len(eases))
	}
	for name := range eases {
		if !strings.Contains(script, "'"+name+"': (") {
			t.Errorf("the contract's ease %q is not in the script's table", name)
		}
	}
}

func TestScript_AKeyAtTheLengthLandsOnTheLastFrame(t *testing.T) {
	if !strings.Contains(script, "min(int(math.floor(t * fps + 0.5)) + 1, count)") {
		t.Error("frame_of must be min(round(t*fps)+1, count)")
	}
}

func TestScript_TheBackdropIsAPlainMeshWithNoModifier(t *testing.T) {
	b := builderPart(t)
	for _, want := range []string{"def backdrop_mesh(", "range(17)", "from_pydata", "use_smooth = True", "o['type'] == 'backdrop'"} {
		if !strings.Contains(b, want) {
			t.Errorf("the builder is missing %q", want)
		}
	}
	// the only modifiers of the builder are the F-curve modifiers that repeat a clip (fc.modifiers), never one of an object
	if strings.Contains(b, "ob.modifiers") || strings.Contains(b, "modifier_add") || strings.Contains(b, "target.modifiers") {
		t.Error("a backdrop has no modifier, so the .blend opens the same anywhere")
	}
}

func TestSettings_TheFontsOfTheDesignTravelAsPaths(t *testing.T) {
	spec, _ := Resolve([]byte(minimal), testEnv(""))
	data, err := Params{Spec: spec, FPS: 30, Fonts: map[string]string{"display": "/c/a.ttf"}, Width: 1, Height: 1, Count: 1}.Settings("/o")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Fonts map[string]string `json:"fonts"`
	}
	json.Unmarshal(data, &m)
	if m.Fonts["display"] != "/c/a.ttf" {
		t.Errorf("fonts = %v", m.Fonts)
	}
}

// The server's projects folder is relative ("projects"), and Blender runs inside the project's folder: every path in the
// settings must be absolute or it is read again from inside the folder (a run of a built scene failed with
// projects/x/tmp/scene.blender/scene.json not found).
func TestRender_ARelativeProjectFolderGivesAbsolutePathsToBlender(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	fr := fakeRender(t, 2, "CPU")
	spec, _ := Resolve([]byte(minimal), testEnv(""))
	_, err := Render(context.Background(), fr, "blender", Params{
		Dir: "projects/demo", Out: "tmp/scene_a.blender", Width: 8, Height: 8, Count: 2, FPS: 30,
		Spec: spec, Sources: "sources", BlendOut: "tmp/scene_a.blend",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := fr.Calls()[0]
	var cfg struct {
		Build, Sources, Out string
		BlendOut            string `json:"blend_out"`
	}
	for _, e := range c.Env {
		if v, ok := strings.CutPrefix(e, EnvVar+"="); ok {
			json.Unmarshal([]byte(v), &cfg)
		}
	}
	proj := filepath.Join(root, "projects", "demo")
	if cfg.Build != filepath.Join(proj, "tmp", "scene_a.blender", DescriptionFile) || cfg.Out != filepath.Join(proj, "tmp", "scene_a.blender") ||
		cfg.Sources != filepath.Join(proj, "sources") || cfg.BlendOut != filepath.Join(proj, "tmp", "scene_a.blend") {
		t.Errorf("settings = %+v, want every path under %s", cfg, proj)
	}
	if c.Dir != proj {
		t.Errorf("Blender runs in %q, want %q", c.Dir, proj)
	}
}

func TestJoin_ARelativeProjectFolderGivesAbsolutePathsToFfmpeg(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	frames := filepath.Join(root, "projects", "demo", "tmp", "scene_a.blender")
	os.MkdirAll(frames, 0o755)
	os.WriteFile(filepath.Join(frames, "f_000001.png"), []byte("png"), 0o644)
	fr := runner.NewFakeRunner()
	if err := Join(context.Background(), fr, "projects/demo", "tmp/scene_a.blender", 30, 5, "tmp/scene_a.blender.mp4"); err != nil {
		t.Fatal(err)
	}
	c := fr.Calls()[0]
	joined := strings.Join(c.Args, " ")
	proj := filepath.Join(root, "projects", "demo")
	if !strings.Contains(joined, filepath.Join(proj, "tmp", "scene_a.blender", "f_*.png")) || !strings.HasSuffix(joined, filepath.Join(proj, "tmp", "scene_a.blender.mp4")) || c.Dir != proj {
		t.Errorf("ffmpeg gets %q in %q, want every path under %s", joined, c.Dir, proj)
	}
}

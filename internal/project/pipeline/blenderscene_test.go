package pipeline

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
	"veritmo/internal/video/blender"
)

type blenderCall struct {
	Params blender.Params
	Env    []string
	Bin    string
}

type joinCall struct {
	Frames  string
	FPS     int
	Seconds float64
	Out     string
}

// blenderStubs replaces Blender: found as version 5.2.3, "rendering" is recorded and answers with the device given.
func blenderStubs(t *testing.T, device string) (*[]blenderCall, *[]joinCall) {
	t.Helper()
	renders, joins := &[]blenderCall{}, &[]joinCall{}
	oldD, oldR, oldJ := detectBlender, renderBlender, joinBlender
	detectBlender = func(context.Context) (blender.Info, error) {
		return blender.Info{Path: "/fake/blender", Version: "5.2.3"}, nil
	}
	renderBlender = func(ctx context.Context, r runner.CommandRunner, bin string, p blender.Params, env []string) (blender.Result, error) {
		*renders = append(*renders, blenderCall{Params: p, Env: env, Bin: bin})
		return blender.Result{Device: device, First: 1, Last: p.Count}, nil
	}
	joinBlender = func(ctx context.Context, r runner.CommandRunner, dir, frames string, fps int, seconds float64, out string) error {
		*joins = append(*joins, joinCall{frames, fps, seconds, out})
		return os.WriteFile(filepath.Join(dir, out), []byte("video"), 0o644)
	}
	t.Cleanup(func() { detectBlender, renderBlender, joinBlender = oldD, oldR, oldJ })
	return renders, joins
}

func blenderScene(id string) config.Scene {
	return config.Scene{ID: id, Blender: &config.SceneBlender{File: "turn.blend", Inject: map[string]any{"Title": "Hi"}}, SilenceSeconds: silent(2), Speed: 1.0}
}

func runBlenderScenes(t *testing.T, scenes []config.Scene, manifest *cache.Manifest, env []string) (*runner.FakeRunner, *cache.Manifest, string) {
	t.Helper()
	cfg := &config.Config{
		Project:  "demo",
		Output:   config.Output{File: "out/demo.mp4"},
		Defaults: config.Defaults{Model: "m.safetensors", Voice: "v.onnx", Resolution: "1280x720", Sampler: "euler", Steps: 30, FPS: 30},
		Scenes:   scenes,
	}
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if inv.Name == "ffprobe" {
			return runner.ScriptedResult{Stdout: []byte(`{"format":{"duration":"3.0"}}`)}
		}
		return runner.ScriptedResult{}
	})
	var buf bytes.Buffer
	if manifest == nil {
		manifest = &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
	}
	if _, err := runVideo(context.Background(), cfg, events.New(&buf, true), fr, ttsRunnerAdapter{r: fr}, assembleRunnerAdapter{r: fr},
		manifest, cache.NewOnlyFilter(""), false, env, "tmp", "", nil, nil); err != nil {
		t.Fatalf("%v\n%s", err, buf.String())
	}
	return fr, manifest, buf.String()
}

func TestBlenderScene_RendersForTheSceneLengthWithoutSD(t *testing.T) {
	imageSetup(t)
	os.WriteFile(filepath.Join("sources", "turn.blend"), []byte("BLEND one"), 0o644)
	renders, joins := blenderStubs(t, "OPTIX")
	fr, _, out := runBlenderScenes(t, []config.Scene{blenderScene("a")}, nil, []string{"CUDA_VISIBLE_DEVICES=1"})
	for _, c := range fr.Calls() {
		if c.Name == "sd" || c.Name == "piper" {
			t.Fatalf("a silent Blender scene needs neither sd nor piper: %s", callNames(fr))
		}
	}
	if len(*renders) != 1 {
		t.Fatalf("one render, got %d", len(*renders))
	}
	r := (*renders)[0]
	p := r.Params
	if p.Count != 60 || p.Width != 1280 || p.Height != 720 || p.File != "sources/turn.blend" || p.Device != "auto" || !p.GPU || p.Samples != config.DefaultBlenderSamples || p.Inject["Title"] != "Hi" {
		t.Errorf("params = %+v", p)
	}
	if r.Bin != "/fake/blender" || len(r.Env) != 1 || r.Env[0] != "CUDA_VISIBLE_DEVICES=1" {
		t.Errorf("bin=%q env=%v: the card of the run must reach Blender", r.Bin, r.Env)
	}
	if len(*joins) != 1 || (*joins)[0].Seconds != 2 || (*joins)[0].FPS != 30 || (*joins)[0].Out != "tmp/scene_a.blender.mp4" {
		t.Errorf("joins = %+v", *joins)
	}
	clip := lastClipCall(t, fr, "a")
	if !strings.Contains(clip, "-stream_loop -1 -i tmp/scene_a.blender.mp4") || !strings.Contains(clip, "-t 2.000000") || !strings.Contains(clip, "setpts=1.000000*PTS") {
		t.Errorf("the clip takes the render at its own speed: %s", clip)
	}
	if strings.Contains(out, `"phase":"animate"`) || strings.Contains(out, "rendered on the CPU") {
		t.Errorf("no animate phase, and no CPU warning on a card:\n%s", out)
	}
}

func TestBlenderScene_NoCardMeansCPUAndSaysSo(t *testing.T) {
	imageSetup(t)
	os.WriteFile(filepath.Join("sources", "turn.blend"), []byte("x"), 0o644)
	renders, _ := blenderStubs(t, "CPU")
	_, _, out := runBlenderScenes(t, []config.Scene{blenderScene("a")}, nil, nil)
	if (*renders)[0].Params.GPU {
		t.Error("a run with no card must not tell Blender it has one")
	}
	if !strings.Contains(out, "rendered on the CPU") {
		t.Errorf("the log must say it was slower:\n%s", out)
	}
}

func TestBlenderScene_CachedUntilSomethingThatMattersChanges(t *testing.T) {
	imageSetup(t)
	blend := filepath.Join("sources", "turn.blend")
	os.WriteFile(blend, []byte("BLEND one"), 0o644)
	renders, _ := blenderStubs(t, "OPTIX")
	scenes := []config.Scene{blenderScene("a")}
	_, m, _ := runBlenderScenes(t, scenes, nil, []string{"CUDA_VISIBLE_DEVICES=0"})
	_, m, out := runBlenderScenes(t, scenes, m, []string{"CUDA_VISIBLE_DEVICES=1"}) // another card: same picture
	if len(*renders) != 1 || !strings.Contains(out, `"type":"skipped","scene_id":"a","phase":"render"`) {
		t.Fatalf("unchanged (even on another card) must be cached (%d renders):\n%s", len(*renders), out)
	}
	step := func(what string, change func()) {
		t.Helper()
		before := len(*renders)
		change()
		_, m, _ = runBlenderScenes(t, scenes, m, nil)
		if len(*renders) != before+1 {
			t.Errorf("%s must render again", what)
		}
	}
	step("a changed file", func() { os.WriteFile(blend, []byte("BLEND two"), 0o644) })
	step("a changed text", func() { scenes[0].Blender.Inject = map[string]any{"Title": "Bye"} })
	step("changed samples", func() { scenes[0].Blender.Samples = 8 })
	step("a longer scene", func() { scenes[0].SilenceSeconds = silent(3) })
}

func TestBlenderScene_NeedsNoDiffusionModel(t *testing.T) {
	cfg := &config.Config{Scenes: []config.Scene{blenderScene("a")}}
	if needsRender(cfg) {
		t.Error("a project of Blender scenes needs no diffusion program")
	}
	cfg.Scenes = append(cfg.Scenes, config.Scene{ID: "b", Prompt: "p"})
	if !needsRender(cfg) {
		t.Error("an ordinary scene still does")
	}
}

func TestBlenderScene_HashIgnoresTheDeviceAndFollowsTheVersion(t *testing.T) {
	imageSetup(t)
	os.WriteFile(filepath.Join("sources", "turn.blend"), []byte("x"), 0o644)
	blenderStubs(t, "OPTIX")
	cfg := &config.Config{Defaults: config.Defaults{Resolution: "1280x720", FPS: 30}}
	a, b := blenderScene("a"), blenderScene("a")
	b.Blender.Device = "cpu"
	if BlenderImageHash(cfg, a, "", "") != BlenderImageHash(cfg, b, "", "") {
		t.Error("the device is not part of the key")
	}
	h := BlenderImageHash(cfg, a, "", "")
	detectBlender = func(context.Context) (blender.Info, error) { return blender.Info{Version: "5.3.0"}, nil }
	if BlenderImageHash(cfg, a, "", "") == h {
		t.Error("another Blender version renders again")
	}
}

const buildDoc = `{"version":1,"world":{"color":"bg"},"camera":{"keys":[{"t":0,"location":[0,-6,1],"target":[0,0,0]},{"t":2,"location":[0,-5,1]}]},` +
	`"materials":{"m":{"color":"accent"}},"objects":[{"id":"a","type":"cube","size":1,"material":"m"}]}`

func builtScene(id string) config.Scene {
	return config.Scene{ID: id, Blender: &config.SceneBlender{Build: "t.scene.json", Samples: 8}, SilenceSeconds: silent(2), Speed: 1.0}
}

// blenderBuildStubs is blenderStubs for built scenes: the stub "renders" and writes the .blend at the path it was given.
func blenderBuildStubs(t *testing.T, failWith error) *[]blenderCall {
	t.Helper()
	renders, _ := blenderStubs(t, "OPTIX")
	renderBlender = func(ctx context.Context, r runner.CommandRunner, bin string, p blender.Params, env []string) (blender.Result, error) {
		*renders = append(*renders, blenderCall{Params: p, Env: env, Bin: bin})
		if p.BlendOut != "" {
			os.WriteFile(filepath.Join(p.Dir, p.BlendOut), []byte("BLEND"), 0o644)
		}
		if failWith != nil {
			return blender.Result{}, failWith
		}
		return blender.Result{Device: "OPTIX", First: 1, Last: p.Count}, nil
	}
	return renders
}

func TestBlenderScene_ABuiltSceneIsRenderedFromItsDescriptionAndKeepsTheBlend(t *testing.T) {
	imageSetup(t)
	os.WriteFile(filepath.Join("sources", "t.scene.json"), []byte(buildDoc), 0o644)
	renders := blenderBuildStubs(t, nil)
	fr, _, _ := runBlenderScenes(t, []config.Scene{builtScene("a")}, nil, []string{"CUDA_VISIBLE_DEVICES=1"})
	for _, c := range fr.Calls() {
		if c.Name == "sd" || c.Name == "piper" {
			t.Fatalf("a built scene needs neither sd nor piper: %s", callNames(fr))
		}
	}
	if len(*renders) != 1 {
		t.Fatalf("one render, got %d", len(*renders))
	}
	p := (*renders)[0].Params
	if p.File != "" || p.Spec == nil || p.Sources != "sources" || p.BlendOut != "tmp/scene_a.blend" || p.FPS != 30 || p.Count != 60 || p.Samples != 8 || len(p.Inject) != 0 {
		t.Errorf("params = %+v", p)
	}
	if got := p.Spec.Materials["m"].Color; got != blender.DefaultPalette.Accent || p.Spec.World.Color != blender.DefaultPalette.BG {
		t.Errorf("a project without a design gets the fixed palette, resolved into the description: material %q, world %q", got, p.Spec.World.Color)
	}
	if _, err := os.Stat(BlendPath("a")); err != nil {
		t.Errorf("the .blend is kept at %s: %v", BlendPath("a"), err)
	}
}

func TestBlenderScene_ABuiltSceneTakesTheDesignsColours(t *testing.T) {
	cfg := &config.Config{Defaults: config.Defaults{Motion: &config.MotionDefaults{Design: &config.MotionDesign{Dark: "#101010", Light: "#eeeeee", Accent: "#ff3b30"}}}}
	if got := blenderPalette(cfg); got != (blender.Palette{BG: "#101010", FG: "#eeeeee", Accent: "#ff3b30"}) {
		t.Errorf("palette = %+v", got)
	}
}

func TestBlenderScene_ADescriptionThatBreaksTheContractFailsBeforeBlenderStarts(t *testing.T) {
	imageSetup(t)
	os.WriteFile(filepath.Join("sources", "t.scene.json"), []byte(`{"version":1,"camera":{"keys":[{"t":0},{"t":9}]}}`), 0o644)
	renders := blenderBuildStubs(t, nil)
	cfg := &config.Config{
		Project: "demo", Output: config.Output{File: "out/demo.mp4"},
		Defaults: config.Defaults{Model: "m.safetensors", Voice: "v.onnx", Resolution: "1280x720", Sampler: "euler", Steps: 30, FPS: 30},
		Scenes:   []config.Scene{builtScene("a")},
	}
	fr := runner.NewFakeRunner()
	var buf bytes.Buffer
	_, err := runVideo(context.Background(), cfg, events.New(&buf, true), fr, ttsRunnerAdapter{r: fr}, assembleRunnerAdapter{r: fr},
		&cache.Manifest{Scenes: map[string]cache.SceneEntry{}}, cache.NewOnlyFilter(""), false, nil, "tmp", "", nil, nil)
	out := buf.String()
	if err == nil && !strings.Contains(out, "camera.keys[1].t") {
		t.Fatalf("the run must fail naming the path:\n%s", out)
	}
	if !strings.Contains(out, "camera.keys[1].t") || !strings.Contains(out, "t.scene.json") {
		t.Errorf("the message names the file and the JSON path:\nerr=%v\n%s", err, out)
	}
	if len(*renders) != 0 {
		t.Error("Blender must not start for a description that breaks the contract")
	}
}

func TestBlenderScene_ARenderThatFailsAfterTheBuildSaysSoAndKeepsTheBlend(t *testing.T) {
	imageSetup(t)
	os.WriteFile(filepath.Join("sources", "t.scene.json"), []byte(buildDoc), 0o644)
	blenderBuildStubs(t, errors.New("blender: no usable GPU"))
	cfg := &config.Config{
		Project: "demo", Output: config.Output{File: "out/demo.mp4"},
		Defaults: config.Defaults{Model: "m.safetensors", Voice: "v.onnx", Resolution: "1280x720", Sampler: "euler", Steps: 30, FPS: 30},
		Scenes:   []config.Scene{builtScene("a")},
	}
	fr := runner.NewFakeRunner()
	var buf bytes.Buffer
	runVideo(context.Background(), cfg, events.New(&buf, true), fr, ttsRunnerAdapter{r: fr}, assembleRunnerAdapter{r: fr},
		&cache.Manifest{Scenes: map[string]cache.SceneEntry{}}, cache.NewOnlyFilter(""), false, nil, "tmp", "", nil, nil)
	if !strings.Contains(buf.String(), "the render failed (the built scene was kept as tmp/scene_a.blend)") {
		t.Errorf("the error must say the render failed and where the scene is:\n%s", buf.String())
	}
	if _, err := os.Stat(BlendPath("a")); err != nil {
		t.Errorf("the .blend must be there: %v", err)
	}
}

func TestBlenderScene_ABuiltSceneIsCachedUntilSomethingThatMattersChanges(t *testing.T) {
	imageSetup(t)
	spec := filepath.Join("sources", "t.scene.json")
	os.WriteFile(spec, []byte(buildDoc), 0o644)
	renders := blenderBuildStubs(t, nil)
	scenes := []config.Scene{builtScene("a")}
	_, m, _ := runBlenderScenes(t, scenes, nil, nil)
	_, m, out := runBlenderScenes(t, scenes, m, nil)
	if len(*renders) != 1 || !strings.Contains(out, `"type":"skipped","scene_id":"a","phase":"render"`) {
		t.Fatalf("unchanged must be cached (%d renders):\n%s", len(*renders), out)
	}
	step := func(what string, change func()) {
		t.Helper()
		before := len(*renders)
		change()
		_, m, _ = runBlenderScenes(t, scenes, m, nil)
		if len(*renders) != before+1 {
			t.Errorf("%s must render again", what)
		}
	}
	step("a changed description", func() { os.WriteFile(spec, []byte(strings.Replace(buildDoc, `"size":1`, `"size":2`, 1)), 0o644) })
	step("a missing .blend", func() { os.Remove(BlendPath("a")) })
	step("changed samples", func() { scenes[0].Blender.Samples = 16 })
	step("a longer scene", func() { scenes[0].SilenceSeconds = silent(3) })
}

func TestBlenderScene_TheKeyFollowsTheDesignColoursTheSceneUses(t *testing.T) {
	imageSetup(t)
	os.WriteFile(filepath.Join("sources", "t.scene.json"), []byte(buildDoc), 0o644) // names bg and accent, not fg
	blenderBuildStubs(t, nil)
	design := func(dark, light, accent string) *config.Config {
		return &config.Config{Defaults: config.Defaults{Resolution: "1280x720", FPS: 30,
			Motion: &config.MotionDefaults{Design: &config.MotionDesign{Dark: dark, Light: light, Accent: accent}}}}
	}
	s := builtScene("a")
	base := BlenderImageHash(design("#101010", "#eeeeee", "#ff3b30"), s, "", "")
	if BlenderImageHash(design("#101010", "#eeeeee", "#00ff00"), s, "", "") == base {
		t.Error("a new accent colour renders a scene that uses accent again")
	}
	if BlenderImageHash(design("#202020", "#eeeeee", "#ff3b30"), s, "", "") == base {
		t.Error("a new background colour renders a scene that uses bg again")
	}
	if BlenderImageHash(design("#101010", "#ffffff", "#ff3b30"), s, "", "") != base {
		t.Error("a colour the scene does not name must not change its key")
	}
}

func TestBlenderScene_AFileSceneKeepsTheKeyItAlwaysHad(t *testing.T) {
	// the omitted fields of a built scene must not change the hash of a .blend scene: nothing already cached is lost
	p := cache.BlenderParams{FileSHA256: "f", Inject: "{}", Samples: 8, Resolution: "1x1", FPS: 24, Length: "l", Version: "5"}
	b, _ := json.Marshal(p)
	if strings.Contains(string(b), "build") || strings.Contains(string(b), "palette") || strings.Contains(string(b), "font") {
		t.Errorf("a file scene serialises as before: %s", b)
	}
}

// motionCfg is a project with a design and, when bpm is not 0, a tempo.
func motionCfg(bpm float64, display string, scenes ...config.Scene) *config.Config {
	m := &config.MotionDefaults{Design: &config.MotionDesign{Dark: "#101010", Light: "#eeeeee", Accent: "#ff3b30", Display: display, Body: "mono"}}
	if bpm > 0 {
		m.BPM = &bpm
	}
	return &config.Config{
		Project: "demo", Output: config.Output{File: "out/demo.mp4"},
		Defaults: config.Defaults{Model: "m.safetensors", Voice: "v.onnx", Resolution: "1280x720", Sampler: "euler", Steps: 30, FPS: 30, Motion: m},
		Scenes:   scenes,
	}
}

func runCfg(t *testing.T, cfg *config.Config) (string, error) {
	t.Helper()
	fr := runner.NewFakeRunner()
	var buf bytes.Buffer
	_, err := runVideo(context.Background(), cfg, events.New(&buf, true), fr, ttsRunnerAdapter{r: fr}, assembleRunnerAdapter{r: fr},
		&cache.Manifest{Scenes: map[string]cache.SceneEntry{}}, cache.NewOnlyFilter(""), false, nil, "tmp", "", nil, nil)
	return buf.String(), err
}

const beatDoc = `{"version":1,"camera":{"keys":[{"t":0,"location":[0,-6,1],"target":[0,0,0]},{"beat":2,"location":[0,-5,1]}]},` +
	`"materials":{"m":{"color":"accent"}},"objects":[{"id":"a","type":"text","text":"Hi","size":1,"material":"m","font":"display"}]}`

func TestBlenderScene_BeatsUseTheProjectsTempoAndTheDesignFontIsDecodedForBlender(t *testing.T) {
	imageSetup(t)
	fonts := t.TempDir()
	old := blenderFontDir
	blenderFontDir = func() string { return fonts }
	t.Cleanup(func() { blenderFontDir = old })
	os.WriteFile(filepath.Join("sources", "t.scene.json"), []byte(beatDoc), 0o644)
	renders := blenderBuildStubs(t, nil)
	if out, err := runCfg(t, motionCfg(120, "playfair-display", builtScene("a"))); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	p := (*renders)[0].Params
	if k := p.Spec.Camera.Keys[1]; k.T == nil || *k.T != 1 || k.Beat != nil {
		t.Errorf("beat 2 at 120 bpm is second 1, got %+v", k)
	}
	path := p.Fonts[blender.FontDisplay]
	if info, err := os.Stat(path); err != nil || info.Size() < 10000 || filepath.Dir(path) != fonts {
		t.Fatalf("the display font must be a decoded TTF in the font cache: %q %v", path, err)
	}
	if head, _ := os.ReadFile(path); len(head) < 4 || string(head[:4]) != "\x00\x01\x00\x00" {
		t.Errorf("a TrueType file starts with 0x00010000, got %q", head[:4])
	}
	if _, ok := p.Fonts[blender.FontBody]; ok {
		t.Error("only the fonts the description uses are decoded")
	}
}

func TestBlenderScene_ABeatWithoutATempoIsRefusedBeforeBlenderStarts(t *testing.T) {
	imageSetup(t)
	os.WriteFile(filepath.Join("sources", "t.scene.json"), []byte(beatDoc), 0o644)
	renders := blenderBuildStubs(t, nil)
	out, err := runCfg(t, motionCfg(0, "playfair-display", builtScene("a")))
	if !strings.Contains(out+fmt.Sprint(err), "camera.keys[1].beat: the project has no tempo") {
		t.Errorf("the message must name the first key that uses a beat:\n%v\n%s", err, out)
	}
	if len(*renders) != 0 {
		t.Error("Blender must not start")
	}
}

func TestBlenderScene_ADisplayFontTheDesignDoesNotHaveIsRefused(t *testing.T) {
	imageSetup(t)
	os.WriteFile(filepath.Join("sources", "t.scene.json"), []byte(beatDoc), 0o644)
	renders := blenderBuildStubs(t, nil)
	out, err := runCfg(t, motionCfg(120, "rounded", builtScene("a"))) // a system stack: it has no file
	if !strings.Contains(out+fmt.Sprint(err), "objects[0].font: the design's display font is not one of the server's font pack") {
		t.Errorf("the message must say the design has no display font in the pack:\n%v\n%s", err, out)
	}
	if len(*renders) != 0 {
		t.Error("Blender must not start")
	}
}

func TestBlenderScene_TheKeyFollowsTheTempoAndTheDesignFonts(t *testing.T) {
	imageSetup(t)
	os.WriteFile(filepath.Join("sources", "t.scene.json"), []byte(beatDoc), 0o644)
	blenderBuildStubs(t, nil)
	s := builtScene("a")
	hash := func(bpm float64, display string) string {
		return BlenderImageHash(motionCfg(bpm, display, s), s, "", "")
	}
	base := hash(120, "playfair-display")
	if hash(120, "playfair-display") != base {
		t.Fatal("the key must be stable")
	}
	if hash(90, "playfair-display") == base {
		t.Error("a new tempo renders a scene that uses beats again")
	}
	if hash(120, "inter") == base {
		t.Error("a new display font renders a scene that uses it again")
	}
	// A scene that uses neither beats nor the fonts of the design does not follow them.
	os.WriteFile(filepath.Join("sources", "t.scene.json"), []byte(`{"version":1,"camera":{"keys":[{"t":0},{"t":1}]},"objects":[{"id":"a","type":"cube","size":1}]}`), 0o644)
	plain := hash(120, "playfair-display")
	if hash(90, "inter") != plain {
		t.Error("a tempo and fonts the scene does not use must not change its key")
	}
}

func TestBlenderScene_TheKeyFollowsTheContentOfTheModelsAndTheirPictures(t *testing.T) {
	imageSetup(t)
	glb := func(js string) []byte {
		for len(js)%4 != 0 {
			js += " "
		}
		out := []byte("glTF")
		out = binary.LittleEndian.AppendUint32(out, 2)
		out = binary.LittleEndian.AppendUint32(out, uint32(20+len(js)))
		out = binary.LittleEndian.AppendUint32(out, uint32(len(js)))
		out = binary.LittleEndian.AppendUint32(out, 0x4E4F534A)
		return append(out, js...)
	}
	write := func(name string, data []byte) { os.WriteFile(filepath.Join("sources", name), data, 0o644) }
	write("c.glb", glb(`{"animations":[{"name":"run"}],"images":[{"uri":"Textures/map.png"}]}`))
	write("map.png", []byte("one"))
	write("t.scene.json", []byte(`{"version":1,"camera":{"keys":[{"t":0},{"t":1}]},"objects":[{"id":"a","type":"model","file":"c.glb","size":1,"animation":{"clip":"run"}}]}`))
	blenderBuildStubs(t, nil)
	s := builtScene("a")
	hash := func() string { return BlenderImageHash(motionCfg(120, "inter", s), s, "", "") }
	base := hash()
	if hash() != base {
		t.Fatal("the key must be stable")
	}
	write("other.glb", glb(`{}`))
	if hash() != base {
		t.Error("a file of the sources that the scene does not use must not change its key")
	}
	write("map.png", []byte("two"))
	if hash() == base {
		t.Error("a texture replaced by another renders the scene again")
	}
	write("map.png", []byte("one"))
	if hash() != base {
		t.Fatal("back to the first texture, back to the first key")
	}
	write("c.glb", glb(`{"animations":[{"name":"run"},{"name":"walk"}],"images":[{"uri":"Textures/map.png"}]}`))
	if hash() == base {
		t.Error("a model replaced by another with the same name renders the scene again")
	}
}

func TestBlenderEnv_ARunInPlaceHasSourcesAndAScenePlacedOnItsOwnHasNone(t *testing.T) {
	cfg := &config.Config{}
	if blenderEnv(cfg, "").NoFiles {
		t.Error("the command line runs a scenes.yaml in the current folder: its sources are there (found by running the models example)")
	}
	if blenderEnv(cfg, "/p").NoFiles {
		t.Error("a project's folder has sources")
	}
	if !BlenderEnv(cfg, "").NoFiles {
		t.Error("with no folder the exported env is the one of a scene with no project")
	}
	if BlenderEnv(cfg, "/p").NoFiles {
		t.Error("a project's folder has sources")
	}
}

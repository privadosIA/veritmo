package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
	"veritmo/internal/project/export"
)

func TestPhasePlan(t *testing.T) {
	phases, concat := PhasePlan(config.ModeVideo)
	wantVideo := []string{"tts", "probe", "render", "clip"}
	if len(phases) != len(wantVideo) || !concat {
		t.Fatalf("video plan = %v concat=%v, want %v concat=true", phases, concat, wantVideo)
	}
	for i := range wantVideo {
		if phases[i] != wantVideo[i] {
			t.Errorf("video phases[%d] = %q, want %q", i, phases[i], wantVideo[i])
		}
	}

	phases, concat = PhasePlan(config.ModeImages)
	wantImages := []string{"render", "export"}
	if len(phases) != len(wantImages) || concat {
		t.Fatalf("images plan = %v concat=%v, want %v concat=false", phases, concat, wantImages)
	}
	for i := range wantImages {
		if phases[i] != wantImages[i] {
			t.Errorf("images phases[%d] = %q, want %q", i, phases[i], wantImages[i])
		}
	}
}

func lookFrom(present ...string) func(string) (string, error) {
	set := make(map[string]bool, len(present))
	for _, p := range present {
		set[p] = true
	}
	return func(name string) (string, error) {
		if set[name] {
			return "/fake/bin/" + name, nil
		}
		return "", fmt.Errorf("executable file not found in $PATH")
	}
}

func TestPreflight(t *testing.T) {
	cases := []struct {
		name         string
		mode         config.OutputMode
		contactSheet bool
		present      []string
		wantErr      string // substring; empty means success
	}{
		{"video requires all four", config.ModeVideo, false, []string{"piper", "sd", "ffmpeg", "ffprobe"}, ""},
		{"video missing piper fails", config.ModeVideo, false, []string{"sd", "ffmpeg", "ffprobe"}, "piper"},
		{"images needs only sd", config.ModeImages, false, []string{"sd"}, ""},
		{"images without piper/ffmpeg/ffprobe passes", config.ModeImages, false, []string{"sd"}, ""},
		{"images contact sheet requires ffmpeg", config.ModeImages, true, []string{"sd"}, "ffmpeg"},
		{"images contact sheet with ffmpeg passes", config.ModeImages, true, []string{"sd", "ffmpeg"}, ""},
		{"images missing sd fails", config.ModeImages, false, []string{"ffmpeg"}, "sd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Preflight(tc.mode, tc.contactSheet, lookFrom(tc.present...))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected success, got: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error naming %q, got: %v", tc.wantErr, err)
			}
		})
	}
}

func TestModeFlag(t *testing.T) {
	t.Run("images override drops output.file requirement", func(t *testing.T) {
		cfg := &config.Config{
			Project: "p",
			Output:  config.Output{Mode: config.ModeVideo},
			Scenes:  []config.Scene{{ID: "s1", Prompt: "a", TTSText: "b"}},
		}
		if err := ApplyModeOverride(cfg, "images"); err != nil {
			t.Fatalf("ApplyModeOverride: %v", err)
		}
		if cfg.Output.Mode != config.ModeImages {
			t.Fatalf("mode = %q, want images", cfg.Output.Mode)
		}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("images override must not require output.file, got: %v", err)
		}
	})

	t.Run("video override restores video validation", func(t *testing.T) {
		cfg := &config.Config{
			Project: "p",
			Output:  config.Output{Mode: config.ModeImages},
			Scenes:  []config.Scene{{ID: "s1", Prompt: "a"}},
		}
		if err := ApplyModeOverride(cfg, "video"); err != nil {
			t.Fatalf("ApplyModeOverride: %v", err)
		}
		err := cfg.Validate()
		if err == nil {
			t.Fatal("video override must enforce video invariants (output path, tts_text)")
		}
	})

	t.Run("invalid value is rejected", func(t *testing.T) {
		cfg := &config.Config{}
		err := ApplyModeOverride(cfg, "audio")
		if err == nil || !strings.Contains(err.Error(), `"video", "images", "music", "video_edit" or "models3d"`) {
			t.Fatalf("expected accepted-values error, got: %v", err)
		}
	})

	t.Run("empty flag leaves YAML mode untouched", func(t *testing.T) {
		cfg := &config.Config{Output: config.Output{Mode: config.ModeImages}}
		if err := ApplyModeOverride(cfg, ""); err != nil {
			t.Fatalf("ApplyModeOverride: %v", err)
		}
		if cfg.Output.Mode != config.ModeImages {
			t.Errorf("mode = %q, want untouched images", cfg.Output.Mode)
		}
	})
}

// chdirTemp switches into a fresh temp dir for tests that exercise the
// relative tmp/ and out/ layout, restoring the previous cwd on cleanup.
func chdirTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	return dir
}

// writeVoice creates a Piper voice stub (and its companion config) in the
// current directory and returns its relative path.
func writeVoice(t *testing.T) string {
	t.Helper()
	for _, f := range []string{"v.onnx", "v.onnx.json"} {
		if err := os.WriteFile(f, []byte("stub"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return "v.onnx"
}

// fakeSD returns a FakeRunner whose handler creates the PNG named by the -o
// argument, mimicking a successful sd (or ffmpeg) invocation.
func fakeSD(t *testing.T) *runner.FakeRunner {
	t.Helper()
	fr := runner.NewFakeRunner()
	writeOut := func(p string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("png "+p), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		for i, a := range inv.Args {
			if a == "-o" && i+1 < len(inv.Args) {
				writeOut(inv.Args[i+1])
			}
		}
		// ffmpeg's contact-sheet output is its final positional argument.
		if inv.Name == "ffmpeg" && len(inv.Args) > 0 {
			writeOut(inv.Args[len(inv.Args)-1])
		}
		return runner.ScriptedResult{}
	})
	return fr
}

// imagesConfig builds a two-scene images-mode config; the model file is
// created in the current (temp) working directory so Validate's existence
// check passes.
func imagesConfig(t *testing.T) *config.Config {
	t.Helper()
	if err := os.WriteFile("m.safetensors", []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	three := 3
	seed := 100
	return &config.Config{
		Project: "proj",
		Output:  config.Output{Mode: config.ModeImages},
		Defaults: config.Defaults{
			Model: "m.safetensors", Resolution: "512x512", Sampler: "euler", Steps: 20,
		},
		Scenes: []config.Scene{
			{ID: "intro", Prompt: "a city", Seed: &seed},
			{ID: "hero", Prompt: "a car", Seed: &seed, Variants: &three},
		},
	}
}

func collectEvents(buf *bytes.Buffer) []map[string]any {
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var ev map[string]any
		if json.Unmarshal([]byte(line), &ev) == nil {
			out = append(out, ev)
		}
	}
	return out
}

func TestRunImages_PhasesInvocationsAndManifest(t *testing.T) {
	chdirTemp(t)
	cfg := imagesConfig(t)
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	fr := fakeSD(t)
	var buf bytes.Buffer
	emitter := events.New(&buf, true)
	manifest := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}

	final, _ := runImages(context.Background(), cfg, emitter, fr, manifest, cache.NewOnlyFilter(""), false, nil, RandomSeed, "", nil, nil)

	// sd invoked once for intro + three times for hero, sequentially.
	calls := fr.Calls()
	if len(calls) != 4 {
		t.Fatalf("got %d sd invocations, want 4", len(calls))
	}
	for i, c := range calls {
		if c.Name != "sd" {
			t.Errorf("call %d binary = %q, want sd (no piper/ffprobe/ffmpeg)", i, c.Name)
		}
	}

	// Event stream: only render/export phases, never tts/probe/clip/concat.
	for _, ev := range collectEvents(&buf) {
		if phase, ok := ev["phase"].(string); ok {
			switch phase {
			case "render", "export":
			default:
				t.Errorf("unexpected phase %q in images mode", phase)
			}
		}
	}

	// Exported layout + manifest.
	for _, name := range []string{"intro.png", "hero_v1.png", "hero_v2.png", "hero_v3.png"} {
		if _, err := os.Stat(filepath.Join("out", "proj", name)); err != nil {
			t.Errorf("missing exported %s: %v", name, err)
		}
	}
	data, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	var m export.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Images) != 4 {
		t.Fatalf("manifest has %d images, want 4", len(m.Images))
	}
	// hero variants carry deterministic seeds 100, 101, 102.
	seeds := map[int]int64{}
	for _, e := range m.Images {
		if e.SceneID == "hero" {
			seeds[e.Variant] = e.EffectiveSeed
		}
	}
	for i := 0; i < 3; i++ {
		if seeds[i] != int64(100+i) {
			t.Errorf("hero variant %d seed = %d, want %d", i, seeds[i], 100+i)
		}
	}
}

func TestFlags_OnlyRestrictsScenesInImagesMode(t *testing.T) {
	chdirTemp(t)
	cfg := imagesConfig(t)
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	// Pre-existing export from an earlier full run must stay untouched.
	if err := os.MkdirAll(filepath.Join("out", "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join("out", "proj", "intro.png")
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	fr := fakeSD(t)
	var buf bytes.Buffer
	emitter := events.New(&buf, true)
	manifest := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}

	final, _ := runImages(context.Background(), cfg, emitter, fr, manifest, cache.NewOnlyFilter("hero"), false, nil, RandomSeed, "", nil, nil)

	if len(fr.Calls()) != 3 {
		t.Errorf("got %d invocations, want 3 (hero variants only)", len(fr.Calls()))
	}
	if got, _ := os.ReadFile(stale); string(got) != "stale" {
		t.Error("--only must leave other scenes' exported files untouched")
	}
	data, _ := os.ReadFile(final)
	var m export.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	for _, e := range m.Images {
		if e.SceneID != "hero" {
			t.Errorf("manifest should only contain hero entries, got scene %q", e.SceneID)
		}
	}
	if len(m.Images) != 3 {
		t.Errorf("manifest has %d entries, want 3", len(m.Images))
	}
}

func TestFlags_ForceBypassesRenderCacheForAllVariants(t *testing.T) {
	chdirTemp(t)
	cfg := imagesConfig(t)
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	fr := fakeSD(t)
	manifest := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}

	var buf bytes.Buffer
	runImages(context.Background(), cfg, events.New(&buf, true), fr, manifest, cache.NewOnlyFilter(""), false, nil, RandomSeed, "", nil, nil)
	if len(fr.Calls()) != 4 {
		t.Fatalf("first run: %d invocations, want 4", len(fr.Calls()))
	}

	// Second run, no force: everything is cached (explicit seeds).
	fr2 := fakeSD(t)
	buf.Reset()
	runImages(context.Background(), cfg, events.New(&buf, true), fr2, manifest, cache.NewOnlyFilter(""), false, nil, RandomSeed, "", nil, nil)
	if len(fr2.Calls()) != 0 {
		t.Fatalf("cached run: %d invocations, want 0", len(fr2.Calls()))
	}

	// Third run with --force: every variant re-renders.
	fr3 := fakeSD(t)
	buf.Reset()
	runImages(context.Background(), cfg, events.New(&buf, true), fr3, manifest, cache.NewOnlyFilter(""), true, nil, RandomSeed, "", nil, nil)
	if len(fr3.Calls()) != 4 {
		t.Fatalf("--force run: %d invocations, want 4 (all variants re-rendered)", len(fr3.Calls()))
	}
}

func TestRunImages_ContactSheetInvokesFFmpeg(t *testing.T) {
	chdirTemp(t)
	cfg := imagesConfig(t)
	cfg.Output.ContactSheet = true
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	fr := fakeSD(t)
	var buf bytes.Buffer
	manifest := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}

	runImages(context.Background(), cfg, events.New(&buf, true), fr, manifest, cache.NewOnlyFilter(""), false, nil, RandomSeed, "", nil, nil)

	var ffmpegCalls int
	for _, c := range fr.Calls() {
		if c.Name == "ffmpeg" {
			ffmpegCalls++
			joined := strings.Join(c.Args, " ")
			if !strings.Contains(joined, "tile=") {
				t.Errorf("ffmpeg args missing tile filter: %v", c.Args)
			}
		}
	}
	if ffmpegCalls != 1 {
		t.Errorf("ffmpeg invoked %d times, want 1 (contact sheet)", ffmpegCalls)
	}
	if _, err := os.Stat(filepath.Join("out", "proj", "contact_sheet.png")); err != nil {
		t.Errorf("contact_sheet.png not produced: %v", err)
	}
}

func TestRunImages_ModelSetInManifestAndArgs(t *testing.T) {
	chdirTemp(t)
	cfg := imagesConfig(t)
	for _, n := range []string{"d.gguf", "l.gguf", "v.safetensors"} {
		if err := os.WriteFile(n, []byte("stub"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	one := 1.0
	cfg.Defaults.Model = ""
	cfg.Defaults.ModelSet = &config.ModelSet{DiffusionModel: "d.gguf", LLM: "l.gguf", VAE: "v.safetensors", CfgScale: &one}
	cfg.Scenes = cfg.Scenes[:1]
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	fr := fakeSD(t)
	manifest := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
	final, _ := runImages(context.Background(), cfg, events.New(&bytes.Buffer{}, true), fr, manifest, cache.NewOnlyFilter(""), false, nil, RandomSeed, "", nil, nil)

	args := fr.Calls()[0].Args
	if !strings.Contains(strings.Join(args, " "), "--diffusion-model d.gguf --llm l.gguf --vae v.safetensors --cfg-scale 1") {
		t.Errorf("sd args = %v", args)
	}
	data, err := os.ReadFile(final)
	if err != nil {
		t.Fatal(err)
	}
	var m export.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if ms := m.Images[0].ModelSet; ms == nil || ms.DiffusionModel != "d.gguf" || ms.VAE != "v.safetensors" {
		t.Errorf("manifest model_set = %+v", ms)
	}
	// Changing the vae makes the cached render stale.
	before := cache.ImageHash(SceneImageParams(cfg, cfg.Scenes[0], 100, 0))
	cfg.Defaults.ModelSet.VAE = "other.safetensors"
	if after := cache.ImageHash(SceneImageParams(cfg, cfg.Scenes[0], 100, 0)); after == before {
		t.Error("changing the vae must change the image hash")
	}
}

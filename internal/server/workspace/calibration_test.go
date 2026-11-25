package workspace

import (
	"math"
	"testing"

	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
)

func closeTo(t *testing.T, what string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.5 {
		t.Errorf("%s = %.1f, want %.1f", what, got, want)
	}
}

func imageCfg(res string, steps int) *config.Config {
	return &config.Config{
		Output: config.Output{Mode: config.ModeVideo},
		Defaults: config.Defaults{
			Resolution: res, Steps: steps,
			ModelSet: &config.ModelSet{DiffusionModel: "models/krea2_turbo-Q4_K_M.gguf", LLM: "models/Qwen3VL-4B-Instruct-Q4_K_M.gguf", VAE: "models/wan_2.1_vae.safetensors"},
		},
		Scenes: []config.Scene{{ID: "a", Prompt: "p"}, {ID: "b", Prompt: "p", Render: &config.SceneRender{Steps: 4}}},
	}
}

func TestExpectedPicture_ScalesTheCatalogTimeByPixelsAndSteps(t *testing.T) {
	cfg := imageCfg("1024x576", 8)
	closeTo(t, "the model's own settings", ExpectedPicture(catalogForTest(t), cfg, cfg.Scenes[0], 1), 18)
	closeTo(t, "half the steps in the scene", ExpectedPicture(catalogForTest(t), cfg, cfg.Scenes[1], 1), 9)
	closeTo(t, "four times the pixels", ExpectedPicture(catalogForTest(t), imageCfg("2048x1152", 8), cfg.Scenes[0], 1), 72)
	closeTo(t, "four versions", ExpectedPicture(catalogForTest(t), cfg, cfg.Scenes[0], 4), 72)
	cfg.Defaults.ModelSet.DiffusionModel = "models/some-unknown.safetensors"
	if got := ExpectedPicture(catalogForTest(t), cfg, cfg.Scenes[0], 1); got != 0 {
		t.Errorf("an unknown model has no expected time, got %v", got)
	}
}

func TestExpectedClip_UsesTheEnginesEntryAndTheTurboBasis(t *testing.T) {
	cfg := &config.Config{Defaults: config.Defaults{Animate: &config.Animate{DiffusionModel: "models/video/Wan2.2-TI2V-5B-Q8_0.gguf", T5XXL: "t", VAE: "v"}}}
	closeTo(t, "Wan 5B at its defaults", ExpectedClip(catalogForTest(t), cfg, config.Scene{}), 85)
	h3 := &config.Config{Defaults: config.Defaults{Animate: &config.Animate{Engine: config.EngineMiniMaxH3, DiffusionModel: "d", VAE: "v", AudioVAE: "a", LLM: "l"}}}
	closeTo(t, "H3 at 20 steps", ExpectedClip(catalogForTest(t), h3, config.Scene{}), 258)
	h3.Defaults.Animate.LoRA = "models/video/turbo.safetensors"
	closeTo(t, "H3 with the turbo LoRA (4 steps)", ExpectedClip(catalogForTest(t), h3, config.Scene{}), 96)
	closeTo(t, "a scene with its own frames", ExpectedClip(catalogForTest(t), cfg, config.Scene{Animation: &config.SceneAnimation{Frames: 17}}), 85*17.0/33)
}

func TestCalibrationSamples_ComparePhasesWithTheirExpectedTimeAndSkipWhatSaysNothing(t *testing.T) {
	cfg := imageCfg("1024x576", 8)
	lines := []string{
		`{"type":"phase_end","scene_id":"a","phase":"render","duration_ms":36000}`, // expected 18: twice as slow
		`{"type":"phase_end","scene_id":"b","phase":"render","duration_ms":18000}`, // expected 9
		`{"type":"phase_end","scene_id":"a","phase":"tts","duration_ms":9000}`,     // not a phase of a card
		`{"type":"phase_end","scene_id":"zz","phase":"render","duration_ms":9000}`, // not a scene of the project
		`{"type":"skipped","scene_id":"a","phase":"render"}`,
		`not json`,
		`{"type":"phase_end","scene_id":"a","phase":"render","duration_ms":900000}`, // 47x: not about speed
	}
	all := CalibrationSamples(catalogForTest(t), "run-1", "RTX", cfg, lines, true)
	var got []CalibrationSample
	for _, s := range all {
		if s.Kind == "render" || s.Kind == "animate" {
			got = append(got, s)
		}
	}
	if len(got) != 2 {
		t.Fatalf("samples = %+v", got)
	}
	// the narration of scene a is the rest of that scene, kept apart: it says nothing about the speed of the card
	var rest []CalibrationSample
	for _, s := range all {
		if s.Kind == kindScene {
			rest = append(rest, s)
		}
	}
	if len(rest) != 1 || rest[0].Actual != 9 || rest[0].Ratio != 0 || rest[0].Expected != 0 {
		t.Errorf("rest samples = %+v, want one for scene a of 9 s and no ratio", rest)
	}
	closeTo(t, "ratio of a", got[0].Ratio*10, 20)
	closeTo(t, "ratio of b", got[1].Ratio*10, 20)
	if got[0].GPU != "RTX" || got[0].Kind != "render" || got[0].RunID != "run-1" {
		t.Errorf("sample = %+v", got[0])
	}
}

func TestSpeedOf_IsTheMedianOfTheLatestSamplesAndNeedsAFew(t *testing.T) {
	s, _ := newProjectsServer(t)
	h := s.rm.hist
	if _, _, ok := h.speedOf("RTX"); ok {
		t.Error("no samples: no speed")
	}
	_ = h.addCalibration([]CalibrationSample{{GPU: "RTX", Kind: "render", Ratio: 1.0}})
	if _, n, ok := h.speedOf("RTX"); ok || n != 1 {
		t.Errorf("one sample is not enough: ok %v n %d", ok, n)
	}
	_ = h.addCalibration([]CalibrationSample{{GPU: "RTX", Kind: "render", Ratio: 3.0}, {GPU: "RTX", Kind: "animate", Ratio: 1.2}})
	f, n, ok := h.speedOf("RTX")
	if !ok || n != 3 || f != 1.2 {
		t.Errorf("median of 1.0, 3.0, 1.2 = %v (n %d ok %v), want 1.2", f, n, ok)
	}
	if _, _, ok := h.speedOf("another card"); ok {
		t.Error("samples of another card must not count")
	}
}

func TestAddCalibration_KeepsOnlyTheLatestOfEachCard(t *testing.T) {
	s, _ := newProjectsServer(t)
	for i := 0; i < calibrationKeep+15; i++ {
		_ = s.rm.hist.addCalibration([]CalibrationSample{{GPU: "RTX", Kind: "render", Ratio: 1}})
	}
	var n int64
	s.rm.hist.db.Model(&CalibrationSample{}).Where("gpu = ?", "RTX").Count(&n)
	if n != calibrationKeep {
		t.Errorf("kept %d samples, want %d", n, calibrationKeep)
	}
}

func TestEnvironment_ACardCarriesItsMeasuredSpeed(t *testing.T) {
	s, _ := newProjectsServer(t)
	s.look = func(name string) (string, error) { return "/fake/" + name, nil }
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if inv.Name == "nvidia-smi" && len(inv.Args) > 0 && inv.Args[0] == "--query-gpu=index,name,memory.free,memory.total" {
			return runner.ScriptedResult{Stdout: []byte("0, NVIDIA GeForce RTX 4070, 11000, 12282\n1, NVIDIA GeForce RTX 5060 Ti, 15000, 16311\n")}
		}
		return runner.ScriptedResult{Stdout: []byte("")}
	})
	s.envRunner = fr
	_ = s.rm.hist.addCalibration([]CalibrationSample{{GPU: "NVIDIA GeForce RTX 5060 Ti", Ratio: 1.4}, {GPU: "NVIDIA GeForce RTX 5060 Ti", Ratio: 1.6}})
	rep := getEnvironment(t, s, "")
	if rep.GPUs[1].SpeedFactor != 1.5 || rep.GPUs[1].SpeedSamples != 2 || rep.GPUs[0].SpeedFactor != 0 {
		t.Errorf("gpus = %+v", rep.GPUs)
	}
	if got := s.runGPUName("1"); got != "NVIDIA GeForce RTX 5060 Ti" {
		t.Errorf("runGPUName(1) = %q", got)
	}
	if s.runGPUName("") != "" || s.runGPUName("0,1") != "" {
		t.Error("with several cards and none chosen, or several in one run, nothing can be attributed")
	}
}

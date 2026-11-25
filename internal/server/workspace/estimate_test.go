package workspace

import (
	"encoding/json"
	"gopkg.in/yaml.v3"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
	"veritmo/internal/project/pipeline"
)

// Specs run-statistics ("The history gives the typical duration of a phase") and project-estimate.

const (
	estGPU   = "NVIDIA GeForce RTX 5060 Ti"
	estModel = "sd_xl_base_1.0.safetensors"
)

// historyServer is a projects server whose only card is a 5060 Ti.
func historyServer(t *testing.T) *testServer {
	t.Helper()
	s, _ := newProjectsServer(t)
	s.look = func(name string) (string, error) { return "/fake/" + name, nil }
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if inv.Name == "nvidia-smi" && len(inv.Args) > 0 && strings.HasPrefix(inv.Args[0], "--query-gpu=index,name") {
			return runner.ScriptedResult{Stdout: []byte("0, " + estGPU + ", 15000, 16311\n")}
		}
		return runner.ScriptedResult{}
	})
	s.envRunner = fr
	return s
}

func render(sec float64, over func(*CalibrationSample)) CalibrationSample {
	c := CalibrationSample{RunID: "r", GPU: estGPU, Kind: "render", Expected: sec, Actual: sec, Ratio: 1, At: time.Now().UTC(),
		Model: estModel, Resolution: "1024x576", Steps: 20, Units: 1, Succeeded: true}
	if over != nil {
		over(&c)
	}
	return c
}

// restSamples is the history of the rest of a project: n scenes of `scene` seconds and n joins of `join` seconds.
func restSamples(n int, scene, join float64) []CalibrationSample {
	var out []CalibrationSample
	for i := 0; i < n; i++ {
		out = append(out,
			CalibrationSample{RunID: "r", GPU: estGPU, Kind: kindScene, Actual: scene, Units: 1, Succeeded: true, At: time.Now().UTC()},
			CalibrationSample{RunID: "r", GPU: estGPU, Kind: kindJoin, Actual: join, Units: 1, Succeeded: true, At: time.Now().UTC()})
	}
	return out
}

func addSamples(t *testing.T, s *testServer, samples ...CalibrationSample) {
	t.Helper()
	if err := s.rm.hist.addCalibration(samples); err != nil {
		t.Fatal(err)
	}
}

func phaseKeyRender() phaseKey {
	return phaseKey{Phase: "render", Model: estModel, Resolution: "1024x576", Steps: 20}
}

func TestPhaseDuration_EnoughHistoryGivesTheMedianTheP95AndTheCount(t *testing.T) {
	s := historyServer(t)
	for _, sec := range []float64{10, 12, 11, 30, 11} {
		addSamples(t, s, render(sec, nil))
	}
	st := s.rm.hist.phaseDuration(estGPU, phaseKeyRender())
	if !st.Enough() || st.Executions != 5 || st.Median != 11 {
		t.Fatalf("stat = %+v, want 5 executions and a median of 11", st)
	}
	if st.P95 < 20 || st.P95 > 30 {
		t.Errorf("p95 = %v, want between the slow runs", st.P95)
	}
}

func TestPhaseDuration_TooFewExecutionsIsNotEnough(t *testing.T) {
	s := historyServer(t)
	addSamples(t, s, render(10, nil), render(11, nil))
	if st := s.rm.hist.phaseDuration(estGPU, phaseKeyRender()); st.Enough() || st.Executions != 2 {
		t.Errorf("stat = %+v, want 2 executions and not enough", st)
	}
}

func TestPhaseDuration_FailedRunsOtherCardsAndOtherSettingsDoNotCount(t *testing.T) {
	s := historyServer(t)
	for i := 0; i < 3; i++ {
		addSamples(t, s, render(10, nil))
	}
	addSamples(t, s,
		render(99, func(c *CalibrationSample) { c.Succeeded = false }),                                // the run failed
		render(99, func(c *CalibrationSample) { c.GPU = "NVIDIA GeForce RTX 4070" }),                  // another card
		render(99, func(c *CalibrationSample) { c.Resolution = "1280x720" }),                          // another size
		render(99, func(c *CalibrationSample) { c.Steps = 8 }),                                        // other steps
		render(99, func(c *CalibrationSample) { c.Model = "other.safetensors" }),                      // another model
		render(99, func(c *CalibrationSample) { c.Kind = "animate" }),                                 // another phase
		render(99, func(c *CalibrationSample) { c.At = time.Now().UTC().Add(-120 * 24 * time.Hour) }), // too old
		render(99, func(c *CalibrationSample) { c.Model = "" }),                                       // stored before the columns existed
	)
	st := s.rm.hist.phaseDuration(estGPU, phaseKeyRender())
	if st.Executions != 3 || st.Median != 10 {
		t.Errorf("stat = %+v, want only the three good executions (median 10)", st)
	}
}

func TestPhaseDuration_APhaseOfSeveralVariantsCountsPerPicture(t *testing.T) {
	s := historyServer(t)
	for i := 0; i < 3; i++ {
		addSamples(t, s, render(40, func(c *CalibrationSample) { c.Units = 4 }))
	}
	if st := s.rm.hist.phaseDuration(estGPU, phaseKeyRender()); st.Median != 10 {
		t.Errorf("median = %v, want 40 s over 4 variants = 10 s a picture", st.Median)
	}
}

func TestCalibrationSamples_CarryTheKeyAndWhetherTheRunSucceeded(t *testing.T) {
	cfg := imageCfg("1024x576", 8)
	lines := []string{`{"type":"phase_end","scene_id":"a","phase":"render","duration_ms":36000}`}
	for _, ok := range []bool{true, false} {
		got := CalibrationSamples(catalogForTest(t), "run-1", "RTX", cfg, lines, ok)
		if len(got) != 1 {
			t.Fatalf("samples = %+v", got)
		}
		g := got[0]
		if g.Model != "krea2_turbo-Q4_K_M.gguf" || g.Resolution != "1024x576" || g.Steps != 8 || g.Units != 1 || g.Succeeded != ok {
			t.Errorf("sample = %+v", g)
		}
	}
}

func get2(t *testing.T, s *testServer, path string) (int, map[string]any) {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodGet, path, nil)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestPhaseDurationEndpoint(t *testing.T) {
	s := historyServer(t)
	for _, sec := range []float64{10, 12, 11} {
		addSamples(t, s, render(sec, nil))
	}
	q := url.Values{"phase": {"render"}, "model": {estModel}, "resolution": {"1024x576"}, "gpu": {estGPU}, "steps": {"20"}}
	code, body := get2(t, s, "/api/stats/phase-duration?"+q.Encode())
	if code != 200 || body["enough"] != true || body["executions"].(float64) != 3 || body["median_seconds"].(float64) != 11 {
		t.Errorf("enough: %d %v", code, body)
	}
	q.Set("steps", "8")
	code, body = get2(t, s, "/api/stats/phase-duration?"+q.Encode())
	if code != 200 || body["enough"] != false || body["executions"].(float64) != 0 {
		t.Errorf("not enough: %d %v", code, body)
	}
	if _, has := body["median_seconds"]; has {
		t.Error("without enough history no duration is given")
	}
	for _, bad := range []string{"phase=tts&model=m&resolution=1x1&gpu=g", "phase=render&resolution=1x1&gpu=g", "phase=render&model=m&resolution=1x1&gpu=g&steps=x"} {
		if code, _ := get2(t, s, "/api/stats/phase-duration?"+bad); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", bad, code)
		}
	}
}

// --- the estimate of a project -----------------------------------------------------------------

func yamlOf(t *testing.T, cfg *config.Config) string {
	t.Helper()
	b, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// sdxlProject is two pictures of SDXL at 1024x576 and 20 steps: by the table 14 s * (576/1024)... times the speed.
func sdxlProject(scenes int) *config.Config {
	cfg := &config.Config{Project: "p", Output: config.Output{Mode: config.ModeVideo, File: "out/p.mp4"},
		Defaults: config.Defaults{Model: "models/" + estModel, Voice: "voices/v.onnx", Resolution: "1024x576", Sampler: "euler", Steps: 20, FPS: 24}}
	for i := 0; i < scenes; i++ {
		cfg.Scenes = append(cfg.Scenes, config.Scene{ID: "s" + string(rune('a'+i)), Prompt: "p", TTSText: "t", Speed: 1})
	}
	return cfg
}

func estimateOf(t *testing.T, s *testServer, body map[string]any) map[string]any {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/estimate", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("estimate: %d %s", rec.Code, rec.Body)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

func TestEstimate_WithoutHistoryItIsTheTableAndSaysSo(t *testing.T) {
	s := historyServer(t)
	e := estimateOf(t, s, map[string]any{"yaml": yamlOf(t, sdxlProject(2))})
	// two pictures and the rest (narration, clip, join), all by the table
	if e["source"] != "table" || e["history_phases"].(float64) != 0 || e["table_phases"].(float64) != 3 || e["seconds"].(float64) <= 0 {
		t.Errorf("estimate = %v", e)
	}
	if _, has := e["executions"]; has {
		t.Error("a table estimate rests on no executions")
	}
}

func TestEstimate_WithHistoryForEveryPhaseItIsBuiltFromIt(t *testing.T) {
	s := historyServer(t)
	for i := 0; i < 4; i++ {
		addSamples(t, s, render(100, nil)) // far from the table's figure, so the source is unmistakable
	}
	addSamples(t, s, restSamples(4, 3, 1)...)
	e := estimateOf(t, s, map[string]any{"yaml": yamlOf(t, sdxlProject(2))})
	if e["source"] != "history" || e["history_phases"].(float64) != 3 || e["table_phases"].(float64) != 0 || e["executions"].(float64) != 4 {
		t.Fatalf("estimate = %v", e)
	}
	// two pictures of 100 s, two scenes of 3 s and a join of 1 s
	if got := e["seconds"].(float64); got != 207 {
		t.Errorf("seconds = %v, want 2 x 100 + 2 x 3 + 1 = 207", got)
	}
}

func TestEstimate_MixedUsesTheHistoryWhereItHasEnoughAndTheTableForTheRest(t *testing.T) {
	s := historyServer(t)
	for i := 0; i < 3; i++ {
		addSamples(t, s, render(100, nil))
	}
	cfg := sdxlProject(2)
	cfg.Scenes[1].Render = &config.SceneRender{Steps: 8} // another key: no history for it
	e := estimateOf(t, s, map[string]any{"yaml": yamlOf(t, cfg)})
	if e["history_phases"].(float64) != 1 || e["table_phases"].(float64) != 2 {
		t.Errorf("without the rest in the history: %v", e)
	}
	addSamples(t, s, restSamples(3, 3, 1)...)
	e = estimateOf(t, s, map[string]any{"yaml": yamlOf(t, cfg)})
	// the first picture and the rest by the history, the second picture (other steps: no history) by the table
	if e["source"] != "mixed" || e["history_phases"].(float64) != 2 || e["table_phases"].(float64) != 1 {
		t.Errorf("estimate = %v", e)
	}
}

func TestEstimate_APhaseTheCacheWillReuseAddsNothing(t *testing.T) {
	s := historyServer(t)
	createProject(t, s, "demo")
	cfg := sdxlProject(2)
	cfg.Project = "demo"
	cfg.Output.File = "out/demo.mp4"
	// the manifest of demo holds the picture of the first scene
	dir := s.ProjectDir("demo")
	hash, err := pipelineVideoImageHash(cfg, cfg.Scenes[0], dir)
	if err != nil {
		t.Fatal(err)
	}
	m := &cache.Manifest{Scenes: map[string]cache.SceneEntry{cfg.Scenes[0].ID: {ImageHash: hash}}}
	if err := os.MkdirAll(filepath.Join(dir, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveManifest(filepath.Join(dir, cache.ManifestPath()), m); err != nil {
		t.Fatal(err)
	}
	without := estimateOf(t, s, map[string]any{"yaml": yamlOf(t, cfg)})
	with := estimateOf(t, s, map[string]any{"yaml": yamlOf(t, cfg), "project": "demo"})
	if with["cached_phases"].(float64) != 1 || with["table_phases"].(float64) != 2 { // the second picture and the rest
		t.Errorf("with the cache: %v", with)
	}
	if with["seconds"].(float64) >= without["seconds"].(float64) {
		t.Errorf("a cached picture must make it faster: %v vs %v", with["seconds"], without["seconds"])
	}
}

func TestEstimate_RefusesWhatItCannotRead(t *testing.T) {
	s := historyServer(t)
	for name, body := range map[string]map[string]any{
		"broken yaml": {"yaml": "project: [unclosed"},
		"music":       {"yaml": "project: m\noutput:\n  mode: music\ntracks:\n  - id: a\n    prompt: p\n"},
	} {
		if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/estimate", body); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want 422", name, rec.Code)
		}
	}
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/estimate", map[string]any{"yaml": yamlOf(t, sdxlProject(1)), "project": "nope"}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown project: status %d, want 404", rec.Code)
	}
}

func pipelineVideoImageHash(cfg *config.Config, sc config.Scene, dir string) (string, error) {
	return pipeline.VideoImageHash(cfg, sc, dir)
}

func TestCPUSamples_AddUpTheRestOfEachSceneAndTheJoinAndNeverMoveTheSpeed(t *testing.T) {
	cfg := imageCfg("1024x576", 8) // scenes a and b
	lines := []string{
		`{"type":"phase_end","scene_id":"a","phase":"tts","duration_ms":1000}`,
		`{"type":"phase_end","scene_id":"a","phase":"probe","duration_ms":200}`,
		`{"type":"phase_end","scene_id":"a","phase":"render","duration_ms":9000}`,
		`{"type":"phase_end","scene_id":"a","phase":"clip","duration_ms":800}`,
		`{"type":"phase_end","scene_id":"b","phase":"clip","duration_ms":700}`,
		`{"type":"phase_end","scene_id":"zz","phase":"tts","duration_ms":5000}`, // not a scene of the project
		`{"type":"phase_end","scene_id":"_global","phase":"concat","duration_ms":500}`,
		`{"type":"skipped","scene_id":"b","phase":"tts"}`,
	}
	got := cpuSamples("r", "RTX", cfg, lines, true)
	if len(got) != 3 {
		t.Fatalf("samples = %+v, want scene a, scene b and the join", got)
	}
	closeTo(t, "scene a", got[0].Actual*10, 20) // 1.0 + 0.2 + 0.8 = 2.0 s
	closeTo(t, "scene b", got[1].Actual*10, 7)
	closeTo(t, "join", got[2].Actual*10, 5)
	for _, g := range got {
		if g.Ratio != 0 || g.Expected != 0 {
			t.Errorf("%+v: a CPU sample has no ratio", g)
		}
	}
	// stored next to the card's own samples they do not change its speed
	s := historyServer(t)
	_ = s.rm.hist.addCalibration([]CalibrationSample{{GPU: estGPU, Kind: "render", Ratio: 2}, {GPU: estGPU, Kind: "render", Ratio: 2}})
	_ = s.rm.hist.addCalibration(restSamples(10, 3, 1))
	if f, n, ok := s.rm.hist.speedOf(estGPU); !ok || f != 2 || n != 2 {
		t.Errorf("speed = %v from %d samples (ok %v), want 2 from the two renders", f, n, ok)
	}
}

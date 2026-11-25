package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/runner"
	"veritmo/internal/platform/sched"
	"veritmo/internal/project/config"
	"veritmo/internal/project/export"
)

// ctxRunner makes a FakeRunner honour context cancellation like the real
// exec runner does (it kills the subprocess), so a blocked fake sd returns
// when the pipeline cancels the run.
type ctxRunner struct{ inner *runner.FakeRunner }

func (r ctxRunner) Run(ctx context.Context, name string, args []string, opts ...runner.Option) ([]byte, error) {
	type res struct {
		out []byte
		err error
	}
	done := make(chan res, 1)
	go func() {
		out, err := r.inner.Run(ctx, name, args, opts...)
		done <- res{out, err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case x := <-done:
		return x.out, x.err
	}
}

// parallelProject builds a video project with one animated scene per id.
func parallelProject(t *testing.T, ids ...string) *config.Config {
	t.Helper()
	chdirTemp(t)
	for _, f := range []string{"m.safetensors", "wan.gguf", "umt5.gguf", "wan_vae.safetensors"} {
		if err := os.WriteFile(f, []byte("stub"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll("tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	seed := 7
	cfg := &config.Config{
		Project: "demo",
		Output:  config.Output{File: "out/demo.mp4"},
		Defaults: config.Defaults{
			Model: "m.safetensors", Voice: writeVoice(t), Resolution: "1280x720", Sampler: "euler", Steps: 30, FPS: 30,
			Animate: &config.Animate{DiffusionModel: "wan.gguf", T5XXL: "umt5.gguf", VAE: "wan_vae.safetensors"},
		},
	}
	for _, id := range ids {
		cfg.Scenes = append(cfg.Scenes, config.Scene{
			ID: id, Prompt: "prompt-" + id, TTSText: "Hello " + id, Speed: 1, Seed: &seed,
			Animate: true, MotionPrompt: "motion-" + id,
		})
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// sceneOfSD finds the scene an sd invocation belongs to from its arguments.
func sceneOfSD(args []string) string {
	j := strings.Join(args, " ")
	i := strings.Index(j, "-p ")
	if i < 0 {
		return ""
	}
	p := j[i+3:]
	for _, pre := range []string{"prompt-", "motion-"} {
		if strings.HasPrefix(p, pre) {
			rest := p[len(pre):]
			if k := strings.IndexByte(rest, ' '); k >= 0 {
				rest = rest[:k]
			}
			return rest
		}
	}
	return ""
}

func gpuOf(env []string) string {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "CUDA_VISIBLE_DEVICES="); ok {
			return v
		}
	}
	return ""
}

// baseHandler answers the non-sd tools like a healthy machine.
func baseHandler(inv runner.Invocation) (runner.ScriptedResult, bool) {
	switch inv.Name {
	case "ffprobe":
		return runner.ScriptedResult{Stdout: []byte(`{"format":{"duration":"5.0"}}`)}, true
	case "piper":
		return runner.ScriptedResult{Stdout: []byte("RIFF....WAVE")}, true
	case "ffmpeg":
		return runner.ScriptedResult{}, true
	}
	return runner.ScriptedResult{}, false
}

func runParallel(t *testing.T, cfg *config.Config, r runner.CommandRunner, gpus []string, budget *sched.Budget, m *cache.Manifest) (*bytes.Buffer, error) {
	t.Helper()
	var buf bytes.Buffer
	_, err := runVideo(context.Background(), cfg, events.New(&buf, true), r,
		ttsRunnerAdapter{r: r}, assembleRunnerAdapter{r: r},
		m, cache.NewOnlyFilter(""), false, nil, "tmp", "", gpus, budget)
	return &buf, err
}

func TestParallel_ScenesOverlapOnTwoGPUsWithPerSceneEnv(t *testing.T) {
	cfg := parallelProject(t, "a", "b", "c", "d")
	fr := runner.NewFakeRunner()
	var running, peak int64
	var mu sync.Mutex
	gpuByScene := map[string]map[string]bool{}
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if res, ok := baseHandler(inv); ok {
			return res
		}
		sc := sceneOfSD(inv.Args)
		mu.Lock()
		if gpuByScene[sc] == nil {
			gpuByScene[sc] = map[string]bool{}
		}
		gpuByScene[sc][gpuOf(inv.Env)] = true
		mu.Unlock()
		cur := atomic.AddInt64(&running, 1)
		for {
			p := atomic.LoadInt64(&peak)
			if cur <= p || atomic.CompareAndSwapInt64(&peak, p, cur) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		atomic.AddInt64(&running, -1)
		return runner.ScriptedResult{}
	})
	m := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
	if _, err := runParallel(t, cfg, fr, []string{"0", "1"}, sched.NewBudget(100*sched.GiB), m); err != nil {
		t.Fatal(err)
	}
	if peak != 2 {
		t.Errorf("peak concurrent sd = %d, want 2 (one per GPU)", peak)
	}
	usedGPU := map[string]bool{}
	for sc, gpus := range gpuByScene {
		if len(gpus) != 1 {
			t.Errorf("scene %s ran on GPUs %v, want exactly one", sc, gpus)
		}
		for g := range gpus {
			usedGPU[g] = true
		}
	}
	if !usedGPU["0"] || !usedGPU["1"] || len(gpuByScene) != 4 {
		t.Errorf("scenes by GPU = %v", gpuByScene)
	}
}

func TestParallel_FinalOrderFollowsProjectOrderNotCompletion(t *testing.T) {
	cfg := parallelProject(t, "a", "b")
	fr := runner.NewFakeRunner()
	bDone := make(chan struct{})
	var once sync.Once
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if res, ok := baseHandler(inv); ok {
			// scene b's clip (its own ffmpeg) marks b as finished
			if inv.Name == "ffmpeg" && strings.Contains(strings.Join(inv.Args, " "), "scene_b.mp4") {
				once.Do(func() { close(bDone) })
			}
			// scene a's clip may only start once b is completely done
			if inv.Name == "ffmpeg" && strings.Contains(strings.Join(inv.Args, " "), "scene_a.mp4") {
				select {
				case <-bDone:
				case <-time.After(3 * time.Second):
				}
			}
			return res
		}
		return runner.ScriptedResult{}
	})
	m := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
	if _, err := runParallel(t, cfg, fr, []string{"0", "1"}, sched.NewBudget(100*sched.GiB), m); err != nil {
		t.Fatal(err)
	}
	list, err := os.ReadFile("tmp/concat_list.txt")
	if err != nil {
		t.Fatal(err)
	}
	ia, ib := strings.Index(string(list), "scene_a.mp4"), strings.Index(string(list), "scene_b.mp4")
	if ia < 0 || ib < 0 || ia > ib {
		t.Errorf("concat list must follow project order (a before b even though b finished first):\n%s", list)
	}
}

func TestParallel_RAMBudgetSerializesLargeAndOverlapsSmall(t *testing.T) {
	run := func(budget *sched.Budget) int64 {
		cfg := parallelProject(t, "a", "b", "c", "d")
		fr := runner.NewFakeRunner()
		var running, peak int64
		fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
			if res, ok := baseHandler(inv); ok {
				return res
			}
			cur := atomic.AddInt64(&running, 1)
			for {
				p := atomic.LoadInt64(&peak)
				if cur <= p || atomic.CompareAndSwapInt64(&peak, p, cur) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			atomic.AddInt64(&running, -1)
			return runner.ScriptedResult{}
		})
		m := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
		if _, err := runParallel(t, cfg, fr, []string{"0", "1"}, budget, m); err != nil {
			t.Fatal(err)
		}
		return peak
	}
	// The stub model files are 4 bytes, so a 1-byte budget makes every sd
	// estimate exceed the whole budget: they must run one at a time.
	if peak := run(sched.NewBudget(1)); peak != 1 {
		t.Errorf("oversized estimates: peak = %d, want 1 (serialized)", peak)
	}
	if peak := run(sched.NewBudget(0)); peak != 1 {
		t.Errorf("zero budget (unreadable memory): peak = %d, want 1", peak)
	}
	if peak := run(sched.NewBudget(100 * sched.GiB)); peak != 2 {
		t.Errorf("roomy budget: peak = %d, want 2", peak)
	}
}

func TestParallel_FailureCancelsTheOtherSceneAndPairsEvents(t *testing.T) {
	cfg := parallelProject(t, "a", "b", "c", "d")
	fr := runner.NewFakeRunner()
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	aInSD := make(chan struct{})
	var aOnce sync.Once
	var cStarted, dStarted int64
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if res, ok := baseHandler(inv); ok {
			return res
		}
		switch sceneOfSD(inv.Args) {
		case "b":
			<-aInSD // fail only once scene a is inside its sd render
			return runner.ScriptedResult{Err: errString("sd: out of memory")}
		case "a":
			aOnce.Do(func() { close(aInSD) })
			<-block // runs until the pipeline cancels it
		case "c":
			atomic.AddInt64(&cStarted, 1)
		case "d":
			atomic.AddInt64(&dStarted, 1)
		}
		return runner.ScriptedResult{}
	})
	m := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
	buf, err := runParallel(t, cfg, ctxRunner{fr}, []string{"0", "1"}, sched.NewBudget(100*sched.GiB), m)
	if err == nil || !strings.Contains(err.Error(), "render failed") || !strings.Contains(err.Error(), "out of memory") {
		t.Fatalf("run error = %v, want scene b's failure", err)
	}
	if cStarted+dStarted != 0 {
		t.Errorf("scenes started after the failure: c=%d d=%d", cStarted, dStarted)
	}

	var errs []events.Event
	open := map[string]string{} // scene -> phase started and not yet closed
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var ev events.Event
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		switch ev.Type {
		case events.TypePhaseStart:
			open[ev.SceneID] = ev.Phase
		case events.TypePhaseEnd:
			delete(open, ev.SceneID)
		case events.TypeError:
			errs = append(errs, ev)
			delete(open, ev.SceneID)
		}
	}
	if len(errs) != 2 {
		t.Fatalf("want an error for the failing scene and one for the stopped scene, got %+v", errs)
	}
	first, second := errs[0], errs[1]
	if first.SceneID != "b" || !strings.Contains(first.Message, "out of memory") {
		t.Errorf("first error = %+v", first)
	}
	if second.SceneID != "a" || !strings.HasPrefix(second.Message, "stopped:") {
		t.Errorf("second error = %+v", second)
	}
	if len(open) != 0 {
		t.Errorf("phase_start events left without an end or error: %v", open)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestParallel_ImagesModeSeedsAndOrderIndependentOfCompletion(t *testing.T) {
	chdirTemp(t)
	if err := os.WriteFile("m.safetensors", []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	two := 2
	cfg := &config.Config{
		Project: "proj",
		Output:  config.Output{Mode: config.ModeImages},
		Defaults: config.Defaults{
			Model: "m.safetensors", Resolution: "512x512", Sampler: "euler", Steps: 20,
		},
		Scenes: []config.Scene{
			{ID: "one", Prompt: "p1", Variants: &two},
			{ID: "two", Prompt: "p2"},
			{ID: "three", Prompt: "p3"},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	fr := fakeSD(t)
	// Make scene "one" the slowest so it finishes last.
	inner := fr
	slow := runner.NewFakeRunner()
	slow.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if strings.Contains(strings.Join(inv.Args, " "), "p1") {
			time.Sleep(60 * time.Millisecond)
		}
		out, err := inner.Run(context.Background(), inv.Name, inv.Args)
		_ = out
		if err != nil {
			return runner.ScriptedResult{Err: err}
		}
		return runner.ScriptedResult{}
	})
	var n int64
	rng := func() int64 { return atomic.AddInt64(&n, 1) * 1000 }
	manifest := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
	var buf bytes.Buffer
	final, err := runImages(context.Background(), cfg, events.New(&buf, true), slow, manifest, cache.NewOnlyFilter(""), false, nil, rng, "",
		[]string{"0", "1"}, sched.NewBudget(100*sched.GiB))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(final)
	if err != nil {
		t.Fatal(err)
	}
	var mf export.Manifest
	if err := json.Unmarshal(data, &mf); err != nil {
		t.Fatal(err)
	}
	type row struct {
		id   string
		seed int64
	}
	var got []row
	for _, e := range mf.Images {
		got = append(got, row{e.SceneID, e.EffectiveSeed})
	}
	// Seeds are drawn in project order: one -> 1000, 2000; two -> 3000; three -> 4000.
	want := []row{{"one", 1000}, {"one", 2000}, {"two", 3000}, {"three", 4000}}
	if len(got) != len(want) {
		t.Fatalf("manifest rows = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("manifest row %d = %v, want %v (project order and seeds drawn up front)", i, got[i], want[i])
		}
	}
}

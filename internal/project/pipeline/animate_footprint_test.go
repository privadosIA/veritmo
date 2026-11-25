package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/sched"
	"veritmo/internal/project/config"
)

// sparse creates a file that reports size bytes without using the disk.
func sparse(t *testing.T, dir, name string, size int64) string {
	t.Helper()
	p := filepath.Join(dir, name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		t.Skipf("filesystem without sparse files: %v", err)
	}
	return name
}

const gb = int64(1_000_000_000)

func animateCfg(a *config.Animate) *config.Config {
	return &config.Config{Defaults: config.Defaults{Animate: a}}
}

func TestAnimateFootprint_CountsBothExpertsAndOnlyWhenSet(t *testing.T) {
	dir := t.TempDir()
	low := sparse(t, dir, "low.gguf", 8_750_000_000)
	high := sparse(t, dir, "high.gguf", 8_750_000_000)
	t5 := sparse(t, dir, "umt5.gguf", 3_500_000_000)
	vae := sparse(t, dir, "vae.safetensors", 250_000_000)

	single := animateFootprint(animateCfg(&config.Animate{DiffusionModel: low, T5XXL: t5, VAE: vae}), dir)
	if want := 8_750_000_000 + 3_500_000_000 + 250_000_000; single != int64(want) {
		t.Errorf("single-model footprint = %d, want %d (unchanged)", single, want)
	}
	two := animateFootprint(animateCfg(&config.Animate{DiffusionModel: low, HighNoiseDiffusionModel: high, T5XXL: t5, VAE: vae}), dir)
	if want := 8_750_000_000*2 + 3_500_000_000 + 250_000_000; two != int64(want) {
		t.Errorf("two-expert footprint = %d, want %d", two, want)
	}
	if got := sched.Estimate(two); got != int64(float64(two)*sched.Factor) {
		t.Errorf("reservation is the estimate of the sum: %d", got)
	}
}

// With a typical budget of this machine (MemAvailable minus the 4 GiB margin
// is about 22-24 GiB depending on what else is running), a two-expert
// animation estimates at or above it (24.4 GB) and so runs alone: it waits for smaller reservations and
// holds everything while it runs, instead of being refused or deadlocking.
func TestAnimateFootprint_TwoExpertRunsAloneUnderATypicalBudget(t *testing.T) {
	two := int64(8_750_000_000*2 + 3_500_000_000 + 250_000_000)
	need := sched.Estimate(two)
	budget := sched.NewBudget(22 * (1 << 30))
	if need <= budget.Total() {
		t.Fatalf("premise: the estimate (%d) should exceed the budget (%d)", need, budget.Total())
	}
	small, err := budget.Acquire(context.Background(), 5*gb)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan func(), 1)
	go func() {
		rel, err := budget.Acquire(context.Background(), need)
		if err == nil {
			got <- rel
		}
	}()
	select {
	case <-got:
		t.Fatal("a two-expert animation must wait while another process holds memory")
	case <-time.After(100 * time.Millisecond):
	}
	small()
	var rel func()
	select {
	case rel = <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("it must run once the budget is idle (no deadlock)")
	}
	other := make(chan struct{})
	go func() {
		r, _ := budget.Acquire(context.Background(), gb)
		r()
		close(other)
	}()
	select {
	case <-other:
		t.Fatal("while it runs alone nothing else may hold memory")
	case <-time.After(100 * time.Millisecond):
	}
	rel()
	select {
	case <-other:
	case <-time.After(2 * time.Second):
		t.Fatal("the next process must start after it finishes")
	}
}

func TestSceneAnimateParams_TwoExpertOnlyWhenSetAndKeyUnchangedOtherwise(t *testing.T) {
	scene := config.Scene{ID: "s", Prompt: "p", Animate: true}
	single := animateCfg(&config.Animate{DiffusionModel: "d", T5XXL: "t", VAE: "v"})
	sp := SceneAnimateParams(single, scene, "")
	if sp.HighNoiseModel != "" || sp.HighNoiseSteps != 0 || sp.HighNoiseCfgScale != 0 || sp.FlowShift != "" {
		t.Errorf("single-model params must carry no two-expert values: %+v", sp)
	}
	if sp.Steps != 20 || sp.CfgScale != 6 {
		t.Errorf("single-model defaults changed: %+v", sp)
	}

	two := animateCfg(&config.Animate{DiffusionModel: "d", HighNoiseDiffusionModel: "h", T5XXL: "t", VAE: "v"})
	tp := SceneAnimateParams(two, scene, "")
	if tp.HighNoiseModel != "h" || tp.HighNoiseSteps != 8 || tp.HighNoiseCfgScale != 3.5 || tp.FlowShift != "3.0" || tp.Steps != 10 || tp.CfgScale != 3.5 {
		t.Errorf("two-expert params: %+v", tp)
	}
	if cache.AnimateHash(sp) == cache.AnimateHash(tp) {
		t.Error("the two modes must not share a cache key")
	}
}

func TestAnimateFootprint_AnimateDiffIsCheckpointPlusModule(t *testing.T) {
	dir := t.TempDir()
	ckpt := sparse(t, dir, "sd15.safetensors", 4_300_000_000)
	mm := sparse(t, dir, "mm.safetensors", 840_000_000)
	// Wan files set too: they must not be counted for AnimateDiff.
	wan := sparse(t, dir, "wan.gguf", 8_750_000_000)
	got := animateFootprint(animateCfg(&config.Animate{
		Engine: config.EngineAnimateDiff, Checkpoint: ckpt, MotionModule: mm, DiffusionModel: wan,
	}), dir)
	if want := int64(4_300_000_000 + 840_000_000); got != want {
		t.Errorf("AnimateDiff footprint = %d, want %d", got, want)
	}
}

func TestSceneAnimateParams_AnimateDiffAndClipHashFollowIt(t *testing.T) {
	scene := config.Scene{ID: "s", Prompt: "p", Animate: true}
	mk := func(strength float64) *config.Config {
		return animateCfg(&config.Animate{Engine: config.EngineAnimateDiff, Checkpoint: "c", MotionModule: "m", Strength: &strength})
	}
	p := SceneAnimateParams(mk(0.75), scene, "")
	if p.Engine != "animatediff" || p.Checkpoint != "c" || p.MotionModule != "m" || p.Strength != 0.75 ||
		p.Resolution != "512x512" || p.Frames != 16 || p.FPS != 8 || p.Steps != 25 || p.CfgScale != 7 {
		t.Errorf("params = %+v", p)
	}
	if p.DiffusionModel != "" || p.T5XXL != "" || p.VAE != "" {
		t.Errorf("no Wan models in an AnimateDiff key: %+v", p)
	}
	a, b := mk(0.75), mk(0.6)
	if cache.AnimateHash(SceneAnimateParams(a, scene, "")) == cache.AnimateHash(SceneAnimateParams(b, scene, "")) {
		t.Error("strength must change the animate hash")
	}
	if cache.ClipHash(SceneClipParams(a, scene, "")) == cache.ClipHash(SceneClipParams(b, scene, "")) {
		t.Error("the clip hash must follow the animate hash")
	}
	// 16 frames at 8 fps: (2*16-2)/8 s
	if d := animatedDuration(a.Defaults.Animate, scene); d != 3.75 {
		t.Errorf("duration = %v, want 3.75", d)
	}
}

func TestSceneInputHashes_FollowTheContentAndTheOrder(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, config.SourcesSubdir), 0o755)
	put := func(name, body string) {
		os.WriteFile(filepath.Join(dir, config.SourcesSubdir, name), []byte(body), 0o644)
	}
	put("e.png", "1")
	put("a.png", "2")
	put("v.mp4", "3")
	put("s.wav", "4")
	sc := config.Scene{EndImage: "e.png", References: &config.SceneReferences{Images: []string{"a.png"}, Videos: []string{"v.mp4"}, Audios: []string{"s.wav"}}}
	end, refs := sceneInputHashes(dir, sc)
	if end == "" || len(refs) != 3 || !strings.HasPrefix(refs[0], "image:") || !strings.HasPrefix(refs[1], "video:") || !strings.HasPrefix(refs[2], "audio:") {
		t.Fatalf("end=%q refs=%v", end, refs)
	}
	put("e.png", "changed")
	if end2, _ := sceneInputHashes(dir, sc); end2 == end {
		t.Error("a changed file must change its hash")
	}
	if e, r := sceneInputHashes(dir, config.Scene{}); e != "" || r != nil {
		t.Errorf("a scene with none has none: %q %v", e, r)
	}
	if _, r := sceneInputHashes(dir, config.Scene{References: &config.SceneReferences{Images: []string{"gone.png"}}}); !strings.HasPrefix(r[0], "image:unreadable") {
		t.Errorf("an unreadable file is marked: %v", r)
	}
}

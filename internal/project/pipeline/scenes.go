package pipeline

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"

	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/sched"
	"veritmo/internal/project/config"
)

// runState is what the scenes of one run share: the cache manifest (guarded
// by mu), the first failure, and the optional RAM budget.
type runState struct {
	mu       sync.Mutex
	emitter  *events.Emitter
	manifest *cache.Manifest
	budget   *sched.Budget
	first    error
}

// fail records and reports a scene failure. The first failure of the run
// emits the scene's error event and becomes the run's error; scenes stopped
// afterwards (their subprocesses were cancelled) still emit an error event
// so every phase_start stays paired, but say they were stopped, and the run
// keeps returning the first failure.
func (s *runState) fail(sceneID, msg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.first == nil {
		s.first = fmt.Errorf("%s", msg)
		s.emitter.Error(sceneID, msg)
		return s.first
	}
	s.emitter.Error(sceneID, fmt.Sprintf("stopped: %v", s.first))
	return s.first
}

// runError is the error a run returns once its scenes ended with err: the
// first reported failure when there is one.
func (s *runState) runError(err error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.first != nil {
		return s.first
	}
	return err
}

// reserve takes need bytes of the run's RAM budget before an sd phase
// starts, so the phase's duration does not include the wait. Without a
// budget (a run on 0 or 1 GPU) it is free. A cancelled wait returns the
// context error without an event: no phase had started.
func (s *runState) reserve(ctx context.Context, need int64) (release func(), err error) {
	if s.budget == nil {
		return func() {}, nil
	}
	return s.budget.Acquire(ctx, need)
}

// runScenes runs body for every scene; see runItems for the scheduling.
func runScenes(ctx context.Context, scenes []config.Scene, gpus []string, env []string, body func(ctx context.Context, idx int, scene config.Scene, env []string) error) error {
	return runItems(ctx, len(scenes), gpus, env, func(ctx context.Context, idx int, env []string) error {
		return body(ctx, idx, scenes[idx], env)
	})
}

// runItems runs body for items 0..n-1 (scenes, or tracks in music mode).
// With 0 or 1 GPU the items run one after another in project order with env,
// exactly as the sequential pipeline always has. With several GPUs one
// worker per GPU takes the next unprocessed item (in project order) as soon
// as it is free and runs it with that GPU as the only visible device, so a
// GPU never has two items at once. The first error cancels the items still
// running, no further item starts, and that error is returned after every
// worker has stopped.
func runItems(ctx context.Context, n int, gpus []string, env []string, body func(ctx context.Context, idx int, env []string) error) error {
	if len(gpus) <= 1 {
		for i := 0; i < n; i++ {
			if err := body(ctx, i, env); err != nil {
				return err
			}
		}
		return nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		next     int64 = -1
		once     sync.Once
		firstErr error
		wg       sync.WaitGroup
	)
	for _, gpu := range gpus {
		gpuEnv := append(append([]string(nil), env...), "CUDA_VISIBLE_DEVICES="+gpu)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				i := int(atomic.AddInt64(&next, 1))
				if i >= n {
					return
				}
				if err := body(ctx, i, gpuEnv); err != nil {
					once.Do(func() { firstErr = err; cancel() })
					return
				}
			}
		}()
	}
	wg.Wait()
	return firstErr
}

// modelBytes sums the sizes of the files that exist; a missing file counts
// as 0 (validation reports it elsewhere).
func modelBytes(base string, paths ...string) int64 {
	var total int64
	for _, p := range paths {
		if p == "" {
			continue
		}
		if fi, err := os.Stat(joinDir(base, p)); err == nil {
			total += fi.Size()
		}
	}
	return total
}

// renderFootprint is the total size of the model files an sd render loads.
func renderFootprint(cfg *config.Config, base string) int64 {
	if ms := cfg.Defaults.ModelSet; ms != nil {
		return modelBytes(base, ms.DiffusionModel, ms.LLM, ms.VAE)
	}
	return modelBytes(base, cfg.Defaults.Model)
}

// animateFootprint is the total size of the Wan files an animation loads,
// both experts included in the two-expert mode.
func animateFootprint(cfg *config.Config, base string) int64 {
	if a := cfg.Defaults.Animate; a != nil {
		if a.IsAnimateDiff() {
			return modelBytes(base, a.Checkpoint, a.MotionModule)
		}
		// The high-noise expert is "" for single-model projects and then
		// counts as 0, so their reservation is unchanged.
		if a.IsMiniMaxH3() {
			return modelBytes(base, a.DiffusionModel, a.VAE, a.AudioVAE, a.LLM, a.LoRA)
		}
		if a.IsLTX() {
			return modelBytes(base, a.DiffusionModel, a.VAE, a.AudioVAE, a.LLM, a.Connectors)
		}
		return modelBytes(base, a.DiffusionModel, a.HighNoiseDiffusionModel, a.T5XXL, a.VAE)
	}
	return 0
}

// videoJob is what the three stages of a video scene pass on: the hashes, the cache decision, the artifact paths and
// the length of the scene (known after the probe).
type videoJob struct {
	idx                                         int
	scene                                       config.Scene
	audioHash, imageHash, clipHash, animateHash string
	renderW, renderH                            int
	animated                                    bool
	decision                                    cache.Decision
	durationSeconds                             float64
	wavPath, pngPath, mp4Path                   string
	animRawPath, animPath                       string
}

// overlapBlocker says why the stages of a video cannot overlap ("" when they can): with several GPUs the scenes are
// already shared out among workers, and a motion graphic or an overlay draws its page with a browser (and may ask a
// language model for it) that must not run next to a render.
func overlapBlocker(cfg *config.Config, gpus []string) string {
	if len(gpus) > 1 {
		return "more than one GPU is listed (each GPU already works on its own scene)"
	}
	for _, s := range cfg.Scenes {
		if s.IsMotionGraphic() || s.Overlay != nil {
			return fmt.Sprintf("scene %q draws a motion graphic or an overlay with a browser", s.ID)
		}
	}
	return ""
}

// runStaged runs the three stages of items 0..n-1 as a pipeline: prepare (CPU: narration, probe), gpu (the card) and
// finish (CPU: the clip). Each stage handles the items in project order and one at a time, so the card never has two
// phases at once; while the card works on item i, prepare may be up to `lookahead` items ahead and finish may be
// closing earlier ones. The first error cancels the rest, no further stage starts, and that error is returned after
// every stage has stopped. The results land where the stages put them (by item index), so the order of the output
// does not depend on the order phases finish in.
func runStaged[J any](ctx context.Context, n, lookahead int, env []string,
	prepare func(ctx context.Context, i int) (J, error),
	gpu func(ctx context.Context, job J, env []string) error,
	finish func(ctx context.Context, job J) error) error {
	if lookahead < 1 {
		lookahead = 1
	}
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		once     sync.Once
		firstErr error
		wg       sync.WaitGroup
	)
	fail := func(err error) { once.Do(func() { firstErr = err; cancel() }) }
	// lookahead scenes ahead of the card: one being prepared and lookahead-1 waiting.
	prepared := make(chan J, lookahead-1)
	drawn := make(chan J, 1)

	wg.Add(3)
	go func() {
		defer wg.Done()
		defer close(prepared)
		for i := 0; i < n && ctx.Err() == nil; i++ {
			job, err := prepare(ctx, i)
			if err != nil {
				fail(err)
				return
			}
			select {
			case prepared <- job:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		defer close(drawn)
		for job := range prepared {
			if ctx.Err() != nil {
				return
			}
			if err := gpu(ctx, job, env); err != nil {
				fail(err)
				return
			}
			select {
			case drawn <- job:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for job := range drawn {
			if ctx.Err() != nil {
				return
			}
			if err := finish(ctx, job); err != nil {
				fail(err)
				return
			}
		}
	}()
	wg.Wait()
	if firstErr == nil && parent.Err() != nil {
		return parent.Err()
	}
	return firstErr
}

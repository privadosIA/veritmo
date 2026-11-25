package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
)

// The overlap option (spec parallel-scenes): the narration and probe of the scenes ahead, and the clip of a drawn scene,
// run while the card works on another scene; the result does not change and a failure stops everything.

func runOverlapVideo(t *testing.T, cfg *config.Config, fr *runner.FakeRunner, overlap int, gpus []string) (*bytes.Buffer, string, error) {
	t.Helper()
	var buf bytes.Buffer
	m := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
	final, err := runVideoOverlap(context.Background(), cfg, events.New(&buf, true), fr,
		ttsRunnerAdapter{r: fr}, assembleRunnerAdapter{r: fr},
		m, cache.NewOnlyFilter(""), false, []string{"CUDA_VISIBLE_DEVICES=1"}, "tmp", "", gpus, nil, overlap, nil)
	return &buf, final, err
}

// sayRunner is the animate runner with a hook on each invocation (nil does nothing).
func sayRunner(hook func(inv runner.Invocation) error) *runner.FakeRunner {
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if hook != nil {
			if err := hook(inv); err != nil {
				return runner.ScriptedResult{Err: err}
			}
		}
		switch inv.Name {
		case "ffprobe":
			return runner.ScriptedResult{Stdout: []byte(`{"format":{"duration":"5.0"}}`)}
		case "piper":
			return runner.ScriptedResult{Stdout: []byte("RIFF....WAVE")}
		}
		return runner.ScriptedResult{}
	})
	return fr
}

func isSpeech(inv runner.Invocation, text string) bool {
	return inv.Name == "piper" && strings.Contains(string(inv.Stdin), text)
}

func isRenderOf(inv runner.Invocation, scene string) bool {
	return inv.Name == "sd" && strings.Contains(strings.Join(inv.Args, " "), "tmp/scene_"+scene+".png")
}

func TestOverlap_TheNarrationOfTheNextSceneRunsWhileTheFirstOneRenders(t *testing.T) {
	cfg := animateProject(t)
	var mu sync.Mutex
	spokeBye := make(chan struct{})
	once := sync.Once{}
	var renderEndedBeforeBye atomic.Bool
	fr := sayRunner(func(inv runner.Invocation) error {
		switch {
		case isSpeech(inv, "Bye."):
			once.Do(func() { close(spokeBye) })
		case isRenderOf(inv, "moving"):
			// The render of scene 1 does not end until scene 2's narration has been asked for.
			select {
			case <-spokeBye:
			case <-time.After(3 * time.Second):
				mu.Lock()
				renderEndedBeforeBye.Store(true)
				mu.Unlock()
			}
		}
		return nil
	})
	if _, _, err := runOverlapVideo(t, cfg, fr, 2, nil); err != nil {
		t.Fatal(err)
	}
	if renderEndedBeforeBye.Load() {
		t.Error("the narration of scene 2 did not start while scene 1 was rendering")
	}
}

func TestOverlap_NoRenderStartsBeforeThePreviousSceneGPUPhasesEnd(t *testing.T) {
	cfg := animateProject(t)
	var busy, maxBusy atomic.Int32
	fr := sayRunner(func(inv runner.Invocation) error {
		if inv.Name == "sd" {
			n := busy.Add(1)
			for {
				m := maxBusy.Load()
				if n <= m || maxBusy.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			busy.Add(-1)
		}
		return nil
	})
	if _, _, err := runOverlapVideo(t, cfg, fr, 3, nil); err != nil {
		t.Fatal(err)
	}
	if maxBusy.Load() != 1 {
		t.Errorf("%d sd processes at the same time, want never more than 1", maxBusy.Load())
	}
}

// concatOf is what the final join was given: the order of the clips.
func concatOf(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("tmp/concat_list.txt")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestOverlap_TheResultIsTheSameWithAndWithoutTheOption(t *testing.T) {
	var lists, manifests [2]string
	for i, overlap := range []int{0, 3} {
		cfg := animateProject(t)
		m := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
		var buf bytes.Buffer
		fr := sayRunner(nil)
		if _, err := runVideoOverlap(context.Background(), cfg, events.New(&buf, true), fr, ttsRunnerAdapter{r: fr}, assembleRunnerAdapter{r: fr},
			m, cache.NewOnlyFilter(""), false, nil, "tmp", "", nil, nil, overlap, nil); err != nil {
			t.Fatalf("overlap %d: %v", overlap, err)
		}
		// the clips as the join lists them, relative to the project, and the manifest entries in scene order
		lists[i] = strings.ReplaceAll(concatOf(t), mustGetwd(t), "")
		var order []string
		for _, s := range cfg.Scenes {
			e := m.Scenes[s.ID]
			order = append(order, fmt.Sprintf("%s|%s|%s|%s|%s", s.ID, e.AudioHash, e.ImageHash, e.ClipHash, e.AnimateHash))
		}
		manifests[i] = strings.Join(order, "\n")
	}
	if lists[0] != lists[1] {
		t.Errorf("the order of the clips changed:\n%s\nvs\n%s", lists[0], lists[1])
	}
	if manifests[0] != manifests[1] {
		t.Errorf("the manifest changed:\n%s\nvs\n%s", manifests[0], manifests[1])
	}
	if !strings.Contains(lists[1], "scene_moving") || strings.Index(lists[1], "scene_moving") > strings.Index(lists[1], "scene_still") {
		t.Errorf("the clips are not in project order: %s", lists[1])
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	d, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestOverlap_OffIsTheSequentialRun(t *testing.T) {
	cfg := animateProject(t)
	fr := sayRunner(nil)
	if _, _, err := runOverlapVideo(t, cfg, fr, 0, nil); err != nil {
		t.Fatal(err)
	}
	// every invocation of scene 1 comes before the narration of scene 2
	var byeAt, lastMovingAt int
	for i, c := range fr.Calls() {
		if isSpeech(c, "Bye.") {
			byeAt = i
		}
		if strings.Contains(strings.Join(c.Args, " "), "scene_moving") {
			lastMovingAt = i
		}
	}
	if byeAt == 0 || lastMovingAt > byeAt {
		t.Errorf("sequential order broken: the narration of scene 2 is call %d, the last of scene 1 is %d", byeAt, lastMovingAt)
	}
}

func TestOverlap_AFailureInTheNarrationAheadStopsTheRun(t *testing.T) {
	cfg := animateProject(t)
	release := make(chan struct{})
	fr := sayRunner(func(inv runner.Invocation) error {
		switch {
		case isSpeech(inv, "Bye."):
			defer close(release)
			return errors.New("piper: voice missing")
		case isRenderOf(inv, "moving"):
			select {
			case <-release:
			case <-time.After(3 * time.Second):
			}
		}
		return nil
	})
	buf, _, err := runOverlapVideo(t, cfg, fr, 2, nil)
	if err == nil || !strings.Contains(err.Error(), "tts failed") {
		t.Fatalf("err = %v, want the tts failure of scene 2", err)
	}
	evs := collectEvents(buf)
	// no phase_start stays open: each is followed by its end or an error of the same scene
	open := map[string]int{}
	for _, ev := range evs {
		key := fmt.Sprint(ev["scene_id"])
		switch ev["type"] {
		case "phase_start":
			open[key]++
		case "phase_end", "error":
			if open[key] > 0 {
				open[key]--
			}
		}
	}
	for k, n := range open {
		if n != 0 {
			t.Errorf("scene %s has %d phase(s) left open", k, n)
		}
	}
	for _, c := range fr.Calls() {
		if strings.Contains(strings.Join(c.Args, " "), "scene_still") && (c.Name == "sd" || c.Name == "ffmpeg") {
			t.Errorf("a phase of scene 2 started after its narration failed: %s %v", c.Name, c.Args)
		}
	}
	if !strings.Contains(buf.String(), `"type":"error"`) {
		t.Error("no error event")
	}
}

func TestOverlap_IsIgnoredWithAWarningWhenItCannotApply(t *testing.T) {
	cfg := animateProject(t)
	buf, _, err := runOverlapVideo(t, cfg, sayRunner(nil), 2, []string{"0", "1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "overlap ignored") {
		t.Errorf("no warning for several GPUs: %s", buf.String())
	}
	if why := overlapBlocker(&config.Config{Scenes: []config.Scene{{ID: "g", MotionGraphic: "a page"}}}, nil); !strings.Contains(why, `"g"`) {
		t.Errorf("a motion graphic must block the overlap, got %q", why)
	}
	if why := overlapBlocker(&config.Config{Scenes: []config.Scene{{ID: "o", Overlay: &config.Overlay{Text: "x"}}}}, []string{"0"}); why == "" {
		t.Error("an overlay must block the overlap")
	}
	if why := overlapBlocker(&config.Config{Scenes: []config.Scene{{ID: "a"}}}, []string{"0"}); why != "" {
		t.Errorf("a plain project with one GPU can overlap, got %q", why)
	}
}

// --- runStaged -------------------------------------------------------------------------------

func TestRunStaged_EachStageSeesTheItemsInOrderAndTheLookaheadIsBounded(t *testing.T) {
	const n, lookahead = 8, 2
	var prepared, drawn atomic.Int32
	var mu sync.Mutex
	var gpuOrder, finishOrder []int
	maxAhead := int32(0)
	err := runStaged(context.Background(), n, lookahead, nil,
		func(ctx context.Context, i int) (int, error) {
			p := prepared.Add(1)
			if ahead := p - drawn.Load(); ahead > atomic.LoadInt32(&maxAhead) {
				atomic.StoreInt32(&maxAhead, ahead)
			}
			return i, nil
		},
		func(ctx context.Context, job int, env []string) error {
			time.Sleep(5 * time.Millisecond)
			mu.Lock()
			gpuOrder = append(gpuOrder, job)
			mu.Unlock()
			drawn.Add(1)
			return nil
		},
		func(ctx context.Context, job int) error {
			mu.Lock()
			finishOrder = append(finishOrder, job)
			mu.Unlock()
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if gpuOrder[i] != i || finishOrder[i] != i {
			t.Fatalf("gpu %v, finish %v: not in project order", gpuOrder, finishOrder)
		}
	}
	// prepared minus the ones the card already finished: the one being drawn, the waiting ones and the one in hand
	if got := atomic.LoadInt32(&maxAhead); got > lookahead+2 {
		t.Errorf("prepare got %d scenes ahead of the card, want at most %d", got, lookahead+2)
	}
}

func TestRunStaged_TheFirstErrorCancelsTheRestAndIsTheOneReturned(t *testing.T) {
	boom := errors.New("boom")
	var started atomic.Int32
	err := runStaged(context.Background(), 20, 2, nil,
		func(ctx context.Context, i int) (int, error) { return i, nil },
		func(ctx context.Context, job int, env []string) error {
			started.Add(1)
			if job == 3 {
				return boom
			}
			return nil
		},
		func(ctx context.Context, job int) error { return nil })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if started.Load() > 3+1+2+1 {
		t.Errorf("%d gpu phases started, the run was not stopped", started.Load())
	}
}

func TestRunStaged_AFailureInTheFinishStageStopsTheCard(t *testing.T) {
	boom := errors.New("clip failed")
	var gpuRuns atomic.Int32
	err := runStaged(context.Background(), 30, 1, nil,
		func(ctx context.Context, i int) (int, error) { return i, nil },
		func(ctx context.Context, job int, env []string) error {
			gpuRuns.Add(1)
			time.Sleep(2 * time.Millisecond)
			return nil
		},
		func(ctx context.Context, job int) error {
			if job == 1 {
				return boom
			}
			return nil
		})
	if !errors.Is(err, boom) || gpuRuns.Load() >= 30 {
		t.Errorf("err = %v after %d gpu phases", err, gpuRuns.Load())
	}
}

func TestRunStaged_ACancelledContextEndsTheRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	err := runStaged(ctx, 100, 2, nil,
		func(ctx context.Context, i int) (int, error) { return i, nil },
		func(ctx context.Context, job int, env []string) error {
			if job == 2 {
				cancel()
			}
			return nil
		},
		func(ctx context.Context, job int) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestRunStaged_NoItemsIsAnEmptyRun(t *testing.T) {
	if err := runStaged(context.Background(), 0, 2, nil,
		func(ctx context.Context, i int) (int, error) { t.Fatal("prepare called"); return 0, nil },
		func(ctx context.Context, job int, env []string) error { return nil },
		func(ctx context.Context, job int) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

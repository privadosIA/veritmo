package pipeline

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/runner"
)

// Spec events, "phase_end may report the model load time of the phase": the phases that run sd carry what sd says it
// spent loading models, summed over their invocations; no other phase carries it, and it is never above the duration.

const loadOutput = "[I] loading tensors completed, taking 0.010s\n[I] loading tensors completed, taking 0.010s\n"

// loadingRunner is a runner whose sd takes a little while, writes the file it was asked for and says it loaded models
// (or says nothing); piper, ffprobe and ffmpeg answer as in the animate tests.
func loadingRunner(t *testing.T, says bool, sleep time.Duration) *runner.FakeRunner {
	t.Helper()
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		for i, a := range inv.Args {
			if a == "-o" && i+1 < len(inv.Args) {
				_ = os.MkdirAll(filepath.Dir(inv.Args[i+1]), 0o755)
				_ = os.WriteFile(inv.Args[i+1], []byte("png"), 0o644)
			}
		}
		res := runner.ScriptedResult{}
		switch inv.Name {
		case "sd":
			time.Sleep(sleep)
			if says {
				res.Stdout = []byte(loadOutput)
			}
		case "ffprobe":
			res.Stdout = []byte(`{"format":{"duration":"5.0"}}`)
		case "piper":
			res.Stdout = []byte("RIFF....WAVE")
		}
		return res
	})
	return fr
}

func loadOf(events []map[string]any, scene, phase string) (load float64, has bool, duration float64) {
	for _, ev := range events {
		if ev["type"] == "phase_end" && ev["scene_id"] == scene && ev["phase"] == phase {
			l, ok := ev["load_ms"].(float64)
			d, _ := ev["duration_ms"].(float64)
			return l, ok, d
		}
	}
	return 0, false, 0
}

func TestLoadMs_RenderAndAnimateReportWhatSdSaysAndNoOtherPhaseDoes(t *testing.T) {
	cfg := animateProject(t)
	m := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
	buf, err := runAnimateVideo(t, cfg, loadingRunner(t, true, 60*time.Millisecond), m)
	if err != nil {
		t.Fatal(err)
	}
	evs := collectEvents(buf)
	for _, tc := range []struct{ scene, phase string }{{"moving", "render"}, {"moving", "animate"}, {"still", "render"}} {
		load, has, dur := loadOf(evs, tc.scene, tc.phase)
		if !has || load != 20 || load > dur {
			t.Errorf("%s %s: load_ms = %v (present %v), duration_ms = %v; want 20 and not above the duration", tc.scene, tc.phase, load, has, dur)
		}
	}
	for _, phase := range []string{"tts", "probe", "clip"} {
		if _, has, _ := loadOf(evs, "moving", phase); has {
			t.Errorf("the %s phase does not run sd and must not carry load_ms", phase)
		}
	}
}

func TestLoadMs_IsOmittedWhenSdSaysNothingAndThePhaseStillSucceeds(t *testing.T) {
	cfg := animateProject(t)
	m := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
	buf, err := runAnimateVideo(t, cfg, loadingRunner(t, false, 60*time.Millisecond), m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "load_ms") {
		t.Errorf("no load time was said: %s", buf.String())
	}
}

func TestLoadMs_ImagesModeSumsTheVariantsOfAScene(t *testing.T) {
	chdirTemp(t)
	cfg := imagesConfig(t)
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	loading := loadingRunner(t, true, 40*time.Millisecond)
	var buf bytes.Buffer
	manifest := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
	if _, err := runImages(context.Background(), cfg, events.New(&buf, true), loading, manifest, cache.NewOnlyFilter(""), false, nil, RandomSeed, "", nil, nil); err != nil {
		t.Fatal(err)
	}
	evs := collectEvents(&buf)
	if load, has, dur := loadOf(evs, "intro", "render"); !has || load != 20 || load > dur {
		t.Errorf("intro (1 variant): load_ms = %v (present %v), duration %v; want 20", load, has, dur)
	}
	if load, has, dur := loadOf(evs, "hero", "render"); !has || load != 60 || load > dur {
		t.Errorf("hero (3 variants): load_ms = %v (present %v), duration %v; want 60", load, has, dur)
	}
	if _, has, _ := loadOf(evs, "hero", "export"); has {
		t.Error("the export phase does not run sd and must not carry load_ms")
	}
}

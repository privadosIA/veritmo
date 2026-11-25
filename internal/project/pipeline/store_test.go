package pipeline

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
)

// Spec cache, "An artifact already produced for the same inputs is reused by any scene": the narration and the picture
// are kept under the hash of their inputs (tmp/store/) and a phase with the same hash takes the file instead of running.

func storeProject(t *testing.T, scenes ...config.Scene) *config.Config {
	t.Helper()
	chdirTemp(t)
	if err := os.WriteFile("m.safetensors", []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Project: "demo",
		Output:  config.Output{File: "out/demo.mp4"},
		Defaults: config.Defaults{
			Model: "m.safetensors", Voice: writeVoice(t), Resolution: "768x432", Sampler: "euler", Steps: 10, FPS: 24,
		},
		Scenes: scenes,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	return cfg
}

func scene(id, prompt, text string, seed int) config.Scene {
	return config.Scene{ID: id, Prompt: prompt, TTSText: text, Speed: 1, Seed: &seed}
}

// runStore runs the project with a manifest and says how many times piper and a render (sd) ran.
func runStore(t *testing.T, cfg *config.Config, m *cache.Manifest, force bool, hook func(runner.Invocation)) (piper, renders int, buf *bytes.Buffer, err error) {
	t.Helper()
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if hook != nil {
			hook(inv)
		}
		for i, a := range inv.Args {
			if a == "-o" && i+1 < len(inv.Args) {
				_ = os.WriteFile(inv.Args[i+1], []byte("png"), 0o644)
			}
		}
		switch inv.Name {
		case "ffprobe":
			return runner.ScriptedResult{Stdout: []byte(`{"format":{"duration":"2.0"}}`)}
		case "piper":
			return runner.ScriptedResult{Stdout: []byte("RIFF....WAVE-" + string(inv.Stdin))}
		}
		return runner.ScriptedResult{}
	})
	buf = &bytes.Buffer{}
	_, err = runVideo(context.Background(), cfg, events.New(buf, true), fr, ttsRunnerAdapter{r: fr}, assembleRunnerAdapter{r: fr},
		m, cache.NewOnlyFilter(""), force, nil, "tmp", "", nil, nil)
	for _, c := range fr.Calls() {
		switch {
		case c.Name == "piper":
			piper++
		case c.Name == "sd":
			renders++
		}
	}
	return piper, renders, buf, err
}

func newManifest() *cache.Manifest { return &cache.Manifest{Scenes: map[string]cache.SceneEntry{}} }

func skippedPhases(buf *bytes.Buffer, scene string) []string {
	var out []string
	for _, ev := range collectEvents(buf) {
		if ev["type"] == "skipped" && ev["scene_id"] == scene {
			out = append(out, ev["phase"].(string))
		}
	}
	return out
}

func TestStore_TheSameSentenceAndPictureInTwoScenesAreMadeOnce(t *testing.T) {
	cfg := storeProject(t,
		scene("one", "a red barn", "Hello there.", 5),
		scene("two", "a red barn", "Hello there.", 5))
	piper, renders, buf, err := runStore(t, cfg, newManifest(), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if piper != 1 || renders != 1 {
		t.Errorf("piper ran %d times and sd %d, want once each", piper, renders)
	}
	if got := strings.Join(skippedPhases(buf, "two"), ","); got != "tts,render" {
		t.Errorf("scene two skipped %q, want tts and render", got)
	}
	if got := skippedPhases(buf, "one"); len(got) != 0 {
		t.Errorf("scene one made them and cannot have skipped %v", got)
	}
	if b, _ := os.ReadFile("tmp/scene_two.wav"); !strings.Contains(string(b), "Hello there.") {
		t.Errorf("scene two got %q as its narration", b)
	}
}

func TestStore_ARenamedSceneReusesWhatItsOldNameMade(t *testing.T) {
	cfg := storeProject(t, scene("old", "a red barn", "Hello there.", 5))
	m := newManifest()
	if piper, renders, _, err := runStore(t, cfg, m, false, nil); err != nil || piper != 1 || renders != 1 {
		t.Fatalf("first run: piper %d, sd %d, %v", piper, renders, err)
	}
	renamed := *cfg
	renamed.Scenes = []config.Scene{scene("new", "a red barn", "Hello there.", 5)}
	piper, renders, buf, err := runStore(t, &renamed, m, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if piper != 0 || renders != 0 {
		t.Errorf("renaming the scene made piper run %d times and sd %d: nothing changed", piper, renders)
	}
	if got := strings.Join(skippedPhases(buf, "new"), ","); got != "tts,render" {
		t.Errorf("skipped %q, want tts and render", got)
	}
}

func TestStore_DifferentInputsAreMadeAgain(t *testing.T) {
	cfg := storeProject(t,
		scene("one", "a red barn", "Hello there.", 5),
		scene("two", "a red barn", "Another sentence.", 5), // other narration, same picture
		scene("three", "a blue barn", "Hello there.", 5),   // other picture, same narration
		scene("four", "a red barn", "Hello there.", 6))     // other seed
	piper, renders, _, err := runStore(t, cfg, newManifest(), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if piper != 2 {
		t.Errorf("piper ran %d times, want 2 (two different sentences)", piper)
	}
	if renders != 3 {
		t.Errorf("sd ran %d times, want 3 (red/5, blue/5 and red/6)", renders)
	}
}

func TestStore_ADamagedStoredFileIsMadeAgainAndReplaced(t *testing.T) {
	cfg := storeProject(t, scene("one", "a red barn", "Hello there.", 5))
	if _, _, _, err := runStore(t, cfg, newManifest(), false, nil); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir("tmp/store")
	if len(entries) != 2 {
		t.Fatalf("the store has %d files, want the narration and the picture", len(entries))
	}
	for _, e := range entries {
		if err := os.WriteFile("tmp/store/"+e.Name(), nil, 0o644); err != nil { // emptied
			t.Fatal(err)
		}
	}
	other := *cfg
	other.Scenes = []config.Scene{scene("two", "a red barn", "Hello there.", 5)}
	piper, renders, _, err := runStore(t, &other, newManifest(), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if piper != 1 || renders != 1 {
		t.Errorf("with the stored files empty piper ran %d and sd %d, want once each", piper, renders)
	}
	for _, e := range mustReadDir(t, "tmp/store") {
		if fi, _ := e.Info(); fi.Size() == 0 {
			t.Errorf("%s is still empty: the entry was not replaced", e.Name())
		}
	}
}

func mustReadDir(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	e, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestStore_ForceBypassesTheStore(t *testing.T) {
	cfg := storeProject(t,
		scene("one", "a red barn", "Hello there.", 5),
		scene("two", "a red barn", "Hello there.", 5))
	piper, renders, _, err := runStore(t, cfg, newManifest(), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if piper != 2 || renders != 2 {
		t.Errorf("--force: piper %d, sd %d, want twice each (nothing is reused)", piper, renders)
	}
	// but what it made is kept for the next run
	next := *cfg
	next.Scenes = []config.Scene{scene("three", "a red barn", "Hello there.", 5)}
	if piper, renders, _, err := runStore(t, &next, newManifest(), false, nil); err != nil || piper != 0 || renders != 0 {
		t.Errorf("after a forced run: piper %d, sd %d, %v; want the store used", piper, renders, err)
	}
}

func TestStore_OffWithAZeroLimit(t *testing.T) {
	t.Setenv("ORCHESTRATOR_STORE_MB", "0")
	cfg := storeProject(t,
		scene("one", "a red barn", "Hello there.", 5),
		scene("two", "a red barn", "Hello there.", 5))
	piper, renders, _, err := runStore(t, cfg, newManifest(), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if piper != 2 || renders != 2 {
		t.Errorf("with the store off piper ran %d and sd %d, want twice each", piper, renders)
	}
	if _, err := os.Stat("tmp/store"); !os.IsNotExist(err) {
		t.Error("a turned-off store must create nothing")
	}
}

func TestStore_ASceneWithItsOwnPictureIsNotStoredAsARender(t *testing.T) {
	cfg := storeProject(t, scene("one", "a red barn", "Hello there.", 5))
	cfg.Scenes[0].Prompt = ""
	if err := os.MkdirAll("sources", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("sources/own.png", []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.Scenes[0].Image = "own.png"
	if _, _, _, err := runStore(t, cfg, newManifest(), false, nil); err != nil {
		t.Fatal(err)
	}
	for _, e := range mustReadDir(t, "tmp/store") {
		if strings.HasSuffix(e.Name(), ".png") {
			t.Errorf("a given picture was stored as a render: %s", e.Name())
		}
	}
}

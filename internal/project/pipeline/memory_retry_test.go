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
)

func TestRunImages_APictureThatDoesNotFitTheCardIsMadeSmallerAndTheRunSaysSo(t *testing.T) {
	chdirTemp(t)
	if err := os.WriteFile("m.safetensors", []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Project:  "proj",
		Output:   config.Output{Mode: config.ModeImages},
		Defaults: config.Defaults{Model: "m.safetensors", Resolution: "1280x704", Sampler: "euler", Steps: 8},
		Scenes:   []config.Scene{{ID: "hero", Prompt: "a car"}},
	}
	fr := runner.NewFakeRunner()
	failed := false
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if !failed {
			failed = true
			return runner.ScriptedResult{Err: fmt.Errorf("exit status 1 (stderr: [E] krea2 segment 1/29 failed during weight preparation)")}
		}
		for i, a := range inv.Args {
			if a == "-o" && i+1 < len(inv.Args) {
				os.MkdirAll(filepath.Dir(inv.Args[i+1]), 0o755)
				os.WriteFile(inv.Args[i+1], []byte("png"), 0o644)
			}
		}
		return runner.ScriptedResult{}
	})
	var buf bytes.Buffer
	manifest := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
	if _, err := runImages(context.Background(), cfg, events.New(&buf, true), fr, manifest, cache.NewOnlyFilter(""), false, nil, RandomSeed, "", nil, nil); err != nil {
		t.Fatalf("a memory failure must not fail the run: %v", err)
	}
	if n := len(fr.Calls()); n != 2 {
		t.Fatalf("sd ran %d times, want 2", n)
	}
	if !strings.Contains(strings.Join(fr.Calls()[1].Args, " "), "-W 1088 -H 640") {
		t.Errorf("the second try should be smaller: %v", fr.Calls()[1].Args)
	}
	var m struct{ Images []struct{ Resolution [2]int } }
	if data, err := os.ReadFile(filepath.Join("out", "proj", "manifest.json")); err != nil || json.Unmarshal(data, &m) != nil || len(m.Images) != 1 || m.Images[0].Resolution != [2]int{1088, 640} {
		t.Errorf("the manifest must say the size the picture was made at: %v %+v", err, m)
	}
	var warned bool
	for _, ev := range collectEvents(&buf) {
		if ev["type"] == "warn" && strings.Contains(fmt.Sprint(ev["message"]), "ran out of memory at 1280x704") {
			warned = true
		}
	}
	if !warned {
		t.Error("a warning must say the picture was made smaller")
	}
}

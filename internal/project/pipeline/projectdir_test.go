package pipeline

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
)

// TestRunVideo_ProjectDirPathsResolveAgainstChildCwd is the regression for
// the concat failure seen when running through `veritmo serve`: the
// pipeline runs with a project directory (Options.Dir), every subprocess
// gets that directory as its cwd, so each path handed to a subprocess must
// be project-relative. The concat list used to be passed as
// "<dir>/tmp/concat_list.txt", which ffmpeg then resolved against the
// project directory a second time ("No such file or directory").
func TestRunVideo_ProjectDirPathsResolveAgainstChildCwd(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "test")
	if err := os.MkdirAll(filepath.Join(proj, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(proj, "voices"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "voices", "v.onnx"), []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Relative like a real server run: dir is a path from the server's cwd.
	t.Chdir(root)
	dir := filepath.Join("projects", "test")

	cfg := &config.Config{
		Project:    "test",
		OutputFile: "out/test.mp4",
		Defaults:   config.Defaults{Model: "m.safetensors", Voice: "voices/v.onnx", Resolution: "512x512", Sampler: "euler", Steps: 4, FPS: 30},
		Scenes:     []config.Scene{{ID: "a", Prompt: "x", TTSText: "hola", Speed: 1.0}},
	}

	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		switch inv.Name {
		case "ffprobe":
			return runner.ScriptedResult{Stdout: []byte(`{"format":{"duration":"2.5"}}`)}
		case "piper":
			return runner.ScriptedResult{Stdout: []byte("RIFF....WAVE")}
		}
		return runner.ScriptedResult{}
	})
	dr := dirRunner{r: fr, dir: dir}

	var buf bytes.Buffer
	emitter := events.New(&buf, true)
	manifest := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
	if _, err := runVideo(context.Background(), cfg, emitter, dr,
		ttsRunnerAdapter{r: dr}, assembleRunnerAdapter{r: dr},
		manifest, cache.NewOnlyFilter(""), false, nil, "tmp", dir, nil, nil); err != nil {
		t.Fatalf("runVideo: %v\n%s", err, buf.String())
	}

	var concat *runner.Invocation
	for _, c := range fr.Calls() {
		c := c
		if c.Name == "ffmpeg" {
			for i, a := range c.Args {
				if a == "concat" && i > 0 && c.Args[i-1] == "-f" {
					concat = &c
				}
			}
		}
	}
	if concat == nil {
		t.Fatal("no concat invocation recorded")
	}
	if concat.Dir != dir {
		t.Fatalf("concat cwd = %q, want the project dir %q", concat.Dir, dir)
	}
	var list string
	for i, a := range concat.Args {
		if a == "-i" {
			list = concat.Args[i+1]
		}
	}
	if filepath.IsAbs(list) {
		t.Fatalf("concat list %q must be project-relative", list)
	}
	// What ffmpeg will actually open: the child's cwd joined with the arg.
	if _, err := os.Stat(filepath.Join(concat.Dir, list)); err != nil {
		t.Fatalf("ffmpeg (cwd %s) cannot open its concat list %q: %v", concat.Dir, list, err)
	}

	// --- Narration: Piper runs in the project dir, gets the voice
	// project-relative and no output path; the WAV lands at the resolved path.
	var piper *runner.Invocation
	for _, c := range fr.Calls() {
		c := c
		if c.Name == "piper" {
			piper = &c
		}
	}
	if piper == nil {
		t.Fatal("no piper invocation recorded")
	}
	if piper.Dir != dir {
		t.Errorf("piper cwd = %q, want %q", piper.Dir, dir)
	}
	var model string
	for i, a := range piper.Args {
		if a == "--model" {
			model = piper.Args[i+1]
		}
		if strings.Contains(a, dir) || strings.HasSuffix(a, ".wav") {
			t.Errorf("piper argument %q carries a file path / the project dir", a)
		}
	}
	if model != "voices/v.onnx" {
		t.Errorf("--model = %q, want the project-relative voices/v.onnx", model)
	}
	if _, err := os.Stat(filepath.Join(piper.Dir, model)); err != nil {
		t.Errorf("piper (cwd %s) cannot open its voice %q: %v", piper.Dir, model, err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "tmp", "scene_a.wav")); err != nil || len(b) == 0 {
		t.Errorf("WAV not written at the project-resolved path: %v", err)
	}
}

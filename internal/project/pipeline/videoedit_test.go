package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/platform/events"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
	"veritmo/internal/video/videoedit"
)

func videoEditConfig(tool string, settings map[string]any) *config.Config {
	return &config.Config{
		Project: "api-vid-x",
		Output:  config.Output{Mode: config.ModeVideoEdit},
		VideoEdit: &config.VideoEdit{
			Tool: tool, Source: "origen.mp4", Settings: settings,
		},
	}
}

func runEdit(t *testing.T, tools *videoedit.Catalog, cfg *config.Config) (string, string, error) {
	t.Helper()
	dir := t.TempDir()
	var buf bytes.Buffer
	art, err := Run(context.Background(), cfg, Options{Dir: dir, Runner: runner.NewFakeRunner(), VideoEdit: tools}, events.New(&buf, true))
	return dir, art + "\n" + buf.String(), err
}

func TestRunVideoEdit_HandsTheJobToTheTool(t *testing.T) {
	tools := videoedit.NewBareCatalog()
	var got videoedit.Job
	tools.Register(videoedit.Scale, videoedit.Impl{Run: func(ctx context.Context, job videoedit.Job) (videoedit.Result, error) {
		got = job
		job.Warn("used the fallback")
		return videoedit.Result{File: "result.mp4", Format: "mp4", HasAudio: true, Width: 1664, Height: 960},
			os.WriteFile(filepath.Join(job.OutDir, "result.mp4"), []byte("mp4"), 0o644)
	}})

	dir, out, err := runEdit(t, tools, videoEditConfig(videoedit.Scale, map[string]any{"factor": 2}))
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if !strings.HasPrefix(out, "out/api-vid-x/result.mp4\n") {
		t.Errorf("the artifact is %q", strings.SplitN(out, "\n", 2)[0])
	}
	if got.Video != filepath.Join(dir, "sources", "origen.mp4") || got.OutDir != filepath.Join(dir, "out", "api-vid-x") {
		t.Errorf("job paths = %q, %q", got.Video, got.OutDir)
	}
	if videoedit.Int(got.Settings, "factor") != 2 || videoedit.Str(got.Settings, "engine") != "fast" {
		t.Errorf("settings = %v (the defaults must be filled in)", got.Settings)
	}
	for _, want := range []string{`"phase":"edit"`, `used the fallback`, `"type":"phase_end"`} {
		if !strings.Contains(out, want) {
			t.Errorf("the events lack %s:\n%s", want, out)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "out", "api-vid-x", VideoEditResultFile))
	if err != nil {
		t.Fatal(err)
	}
	var res videoedit.Result
	if json.Unmarshal(raw, &res) != nil || res.Width != 1664 || !res.HasAudio {
		t.Errorf("result.json = %s", raw)
	}
}

func TestRunVideoEdit_AToolWithoutCodeFailsSayingSo(t *testing.T) {
	tools := videoedit.NewBareCatalog()
	_, out, err := runEdit(t, tools, videoEditConfig(videoedit.Depth, nil))
	if err == nil || !strings.Contains(err.Error(), "not available in this build") {
		t.Fatalf("err = %v\n%s", err, out)
	}
}

func TestRunVideoEdit_AToolThatFailsOrLeavesNothing(t *testing.T) {
	tools := videoedit.NewBareCatalog()
	tools.Register(videoedit.Depth, videoedit.Impl{Run: func(context.Context, videoedit.Job) (videoedit.Result, error) {
		return videoedit.Result{}, errors.New("the model did not load")
	}})
	_, out, err := runEdit(t, tools, videoEditConfig(videoedit.Depth, nil))
	if err == nil || !strings.Contains(err.Error(), "depth failed: the model did not load") || !strings.Contains(out, `"type":"error"`) {
		t.Fatalf("err = %v\n%s", err, out)
	}

	tools.Register(videoedit.Depth, videoedit.Impl{Run: func(context.Context, videoedit.Job) (videoedit.Result, error) {
		return videoedit.Result{File: "result.mp4"}, nil // says it made a file that is not there
	}})
	_, _, err = runEdit(t, tools, videoEditConfig(videoedit.Depth, nil))
	if err == nil || !strings.Contains(err.Error(), "did not leave its result") {
		t.Fatalf("err = %v", err)
	}
}

func TestVideoEditMode_PlanAndBinaries(t *testing.T) {
	phases, concat := PhasePlan(config.ModeVideoEdit)
	if len(phases) != 1 || phases[0] != "edit" || concat {
		t.Errorf("plan = %v %v", phases, concat)
	}
	if got := RequiredBinaries(config.ModeVideoEdit, false); strings.Join(got, ",") != "ffmpeg,ffprobe" {
		t.Errorf("binaries = %v", got)
	}
	cfg := &config.Config{}
	if err := ApplyModeOverride(cfg, config.ModeVideoEdit); err != nil || cfg.Output.Mode != config.ModeVideoEdit {
		t.Errorf("override: %v %q", err, cfg.Output.Mode)
	}
}

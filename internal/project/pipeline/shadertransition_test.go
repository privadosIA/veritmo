package pipeline

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"veritmo/internal/platform/events"
	"veritmo/internal/project/config"
	"veritmo/internal/video/assemble"
	"veritmo/internal/video/motion"
)

type execFFmpeg struct{ dir string }

func (e execFFmpeg) Run(ctx context.Context, args []string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg", append([]string{"-v", "error"}, args...)...)
	cmd.Dir = e.dir
	out, err := cmd.CombinedOutput()
	return "", string(out), err
}

func TestAShaderJoinGivesAVideoThatReplacesTheCrossfade(t *testing.T) {
	if _, err := motion.Find(""); err != nil {
		t.Skip("no Chrome or Chromium")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	run := execFFmpeg{dir}
	for name, colour := range map[string]string{"a.mp4": "red", "b.mp4": "blue"} {
		_, stderr, err := run.Run(context.Background(), []string{"-y", "-f", "lavfi", "-i", "color=c=" + colour + ":s=320x180:r=10:d=3", "-f", "lavfi", "-i", "sine=frequency=440:duration=3",
			"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-ar", "44100", "-ac", "2", "-shortest", name})
		if err != nil {
			t.Fatalf("%v %s", err, stderr)
		}
	}
	cfg := &config.Config{Defaults: config.Defaults{Resolution: "320x180", FPS: 10}, Scenes: []config.Scene{{ID: "a"}, {ID: "b"}}}
	join := []assemble.JoinClip{{Path: "a.mp4", Duration: 3}, {Path: "b.mp4", Duration: 3, Fade: 0.5, Kind: "shader:cross-warp-morph"}}
	renderShaderJoins(context.Background(), cfg, join, run, dir, "tmp", events.New(&bytes.Buffer{}, true))
	if join[1].ShaderVideo == "" || join[1].Kind != "shader:cross-warp-morph" {
		t.Fatalf("the transition was not drawn: %+v", join[1])
	}
	if _, err := os.Stat(filepath.Join(dir, join[1].ShaderVideo)); err != nil {
		t.Fatal(err)
	}
	if stderr, err := assemble.JoinWithFades(context.Background(), run, join, 10, "out.mp4"); err != nil {
		t.Fatalf("%v %s", err, stderr)
	}
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", filepath.Join(dir, "out.mp4")).Output()
	if err != nil {
		t.Fatal(err)
	}
	secs, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if secs < 5.3 || secs > 5.8 { // 3 + 3 - 0.5
		t.Fatalf("the joined video lasts %.2f s, not about 5.5", secs)
	}
}

func TestAShaderJoinThatCannotBeDrawnFallsBackToACrossfade(t *testing.T) {
	old := findBrowser
	findBrowser = func() (motion.Browser, error) { return motion.Browser{}, motion.ErrNoBrowser }
	t.Cleanup(func() { findBrowser = old })
	cfg := &config.Config{Defaults: config.Defaults{Resolution: "320x180", FPS: 10}, Scenes: []config.Scene{{ID: "a"}, {ID: "b"}}}
	join := []assemble.JoinClip{{Path: "a.mp4", Duration: 3}, {Path: "b.mp4", Duration: 3, Fade: 0.5, Kind: "shader:glitch"}}
	renderShaderJoins(context.Background(), cfg, join, execFFmpeg{t.TempDir()}, t.TempDir(), "tmp", events.New(&bytes.Buffer{}, true))
	if join[1].Kind != "fade" || join[1].ShaderVideo != "" {
		t.Fatalf("%+v", join[1])
	}
}

func TestJoinClipsCarryTheKindAndTheAccentOfEachShaderTransition(t *testing.T) {
	half := 0.5
	cfg := &config.Config{Defaults: config.Defaults{Resolution: "320x180", FPS: 10, Transition: "shader:glitch", TransitionAccent: "#ff8800", TransitionSeconds: 0.5},
		Scenes: []config.Scene{{ID: "a"}, {ID: "b"}, {ID: "c", Transition: "shader:light-leak", TransitionAccent: "#00ffaa", TransitionSeconds: &half}}}
	join := joinClips(cfg, []string{"a.mp4", "b.mp4", "c.mp4"}, []float64{4, 4, 4}, events.New(&bytes.Buffer{}, true))
	if join[1].Kind != "shader:glitch" || join[1].Accent != "#ff8800" {
		t.Errorf("the project's own: %+v", join[1])
	}
	if join[2].Kind != "shader:light-leak" || join[2].Accent != "#00ffaa" {
		t.Errorf("the scene's own: %+v", join[2])
	}
}

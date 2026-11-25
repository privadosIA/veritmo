package music

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
)

// TestSynthWithRealEngine runs the real ace-synth on the DiT-only path (own
// lyrics, no language model) for a few seconds of audio. It needs the
// engine installed by scripts/build-acestep.sh in the repo's bin/ and the
// models in models/music/; otherwise, or with -short, it skips. It is the
// check that the request fields, the .gguf model names and the output
// naming the wrapper relies on still match what the program does.
func TestSynthWithRealEngine(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the real-engine smoke test in -short mode")
	}
	repo, _ := filepath.Abs(filepath.Join("..", "..", ".."))
	models := filepath.Join(repo, "models", "music")
	if _, err := os.Stat(filepath.Join(repo, "bin", "ace-synth")); err != nil {
		t.Skip("ace-synth not installed (run scripts/build-acestep.sh)")
	}
	if _, err := os.Stat(filepath.Join(models, config.DefaultMusicSynth)); err != nil {
		t.Skip("music models not installed in models/music")
	}
	t.Setenv("PATH", filepath.Join(repo, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := exec.LookPath("ace-synth"); err != nil {
		t.Skip(err)
	}

	shift, guidance := 3.0, 1.0
	cfg := &config.Config{Defaults: config.Defaults{Music: &config.Music{InferenceSteps: 8, Shift: &shift, GuidanceScale: &guidance}}}
	dur := 5.0
	track := config.Track{ID: "smoke", Prompt: "calm piano", Lyrics: "[Verse]\nla la la", VocalLanguage: "en", DurationSeconds: &dur}
	req, err := SynthRequest(nil, BuildRequest(cfg, track, 1))
	if err != nil {
		t.Fatal(err)
	}
	var check Request
	if err := json.Unmarshal(req, &check); err != nil || check.SynthModel != config.DefaultMusicSynth {
		t.Fatalf("request = %s (%v)", req, err)
	}

	ws := Workspace{Base: t.TempDir()}
	dst := filepath.Join("tmp", "smoke.mp3")
	// Like the pipeline, run the engine with the project directory as cwd.
	if err := Synth(context.Background(), inDir{r: runner.NewExecRunner(), dir: ws.Base}, ws, models, "", filepath.Join("tmp", "smoke.work"), req, "mp3", dst, nil, nil); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(ws.Path(dst))
	if err != nil || fi.Size() < 10_000 {
		t.Fatalf("audio = %v %v", fi, err)
	}
	if s, _ := AudioSeconds(ws.Path(dst), "mp3", 0); s < 3 || s > 8 {
		t.Fatalf("expected about 5 s of audio, estimated %.1f s", s)
	}
}

// inDir runs every command with dir as its working directory.
type inDir struct {
	r   runner.CommandRunner
	dir string
}

func (d inDir) Run(ctx context.Context, name string, args []string, opts ...runner.Option) ([]byte, error) {
	return d.r.Run(ctx, name, args, append(opts, runner.WithDir(d.dir))...)
}

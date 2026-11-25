package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/project/config"
	"veritmo/internal/project/quality"
)

// checkProjectDir makes a video project with two animated silent scenes whose
// clips exist: "bad" shifts its colour at 1.5 s, "good" is steady.
func checkProjectDir(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	for _, f := range []string{"m.safetensors", "wan.gguf", "umt5.gguf", "wan_vae.safetensors", "voice.onnx", "voice.onnx.json"} {
		os.WriteFile(filepath.Join(dir, f), []byte("stub"), 0o644)
	}
	os.MkdirAll(filepath.Join(dir, "tmp"), 0o755)
	mk := func(name, graph string) {
		out, err := exec.Command("ffmpeg", "-y", "-v", "error", "-filter_complex", graph, "-map", "[v]", "-pix_fmt", "yuv420p", filepath.Join(dir, "tmp", name)).CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	}
	mk("scene_bad.anim.avi", "color=c=0x808080:s=320x180:d=1.5:r=16[a];color=c=0x2040ff:s=320x180:d=1.5:r=16[b];[a][b]concat=n=2:v=1:a=0[v]")
	mk("scene_good.anim.avi", "testsrc=s=320x180:d=3:r=16[v]")
	yaml := `# my project
project: demo
output: {mode: video, file: out/demo.mp4}
defaults:
  model: m.safetensors
  voice: voice.onnx
  animate: {diffusion_model: wan.gguf, t5xxl: umt5.gguf, vae: wan_vae.safetensors}
scenes:
  - id: bad          # the one that drifts
    prompt: a bench
    silence_seconds: 2
    animate: true
    seed: 7
  - id: good
    prompt: a park
    silence_seconds: 2
    animate: true
  - id: still
    prompt: a picture
    silence_seconds: 2
`
	os.WriteFile(filepath.Join(dir, "scenes.yaml"), []byte(yaml), 0o644)
	return dir
}

func TestCheck_ReportsFixesAndThenFindsItClean(t *testing.T) {
	dir := checkProjectDir(t)
	path := filepath.Join(dir, "scenes.yaml")
	var out, errOut bytes.Buffer

	if code := runCheck([]string{"--no-vision", path}, &out, &errOut); code != 3 {
		t.Fatalf("problems without --fix exit 3, got %d: %s%s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "bad: trim to 1.2s") || !strings.Contains(out.String(), "good: ok") || !strings.Contains(out.String(), "checked 2 animated scenes: 1 ok, 1 with problems") {
		t.Fatalf("out = %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "out", "demo", "quality.json")); err != nil {
		t.Errorf("the report must be written: %v", err)
	}
	if got, _ := os.ReadFile(path); !strings.Contains(string(got), "silence_seconds: 2") || strings.Contains(string(got), "1.2") {
		t.Fatal("without --fix the file must not change")
	}

	out.Reset()
	if code := runCheck([]string{"--no-vision", "--fix", path}, &out, &errOut); code != 0 {
		t.Fatalf("--fix exit %d: %s%s", code, out.String(), errOut.String())
	}
	edited, _ := os.ReadFile(path)
	if !strings.Contains(string(edited), "silence_seconds: 1.2") || !strings.Contains(string(edited), "# the one that drifts") || !strings.Contains(string(edited), "# my project") {
		t.Fatalf("the scene is cut and the comments stay:\n%s", edited)
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Error("a backup of the original must be kept")
	}

	// only the part the scene still uses is looked at, so it is clean now
	out.Reset()
	if code := runCheck([]string{"--no-vision", path}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "bad: ok") {
		t.Fatalf("after the cut: %d %s", code, out.String())
	}
}

func TestCheck_AVisionVerdictRetriesWithANewSeedAndANarratedSceneCannotBeCut(t *testing.T) {
	dir := checkProjectDir(t)
	path := filepath.Join(dir, "scenes.yaml")
	data, _ := os.ReadFile(path)
	// "bad" now has narration: the clip is fitted to it, so it cannot be cut
	data = []byte(strings.Replace(string(data), "    silence_seconds: 2\n    animate: true\n    seed: 7", "    tts_text: Hello\n    animate: true\n    seed: 7", 1))
	os.WriteFile(path, data, 0o644)
	dp := realDeps()
	dp.newVision = func(string) quality.Vision { return everyFrame{} }

	var out, errOut bytes.Buffer
	if code := dp.check([]string{"--model", "fake", "--fix", path}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s%s", code, out.String(), errOut.String())
	}
	edited, _ := os.ReadFile(path)
	if !strings.Contains(string(edited), "seed: 1007") {
		t.Fatalf("the bad narrated scene gets the seed + 1000:\n%s", edited)
	}
	cfg, _ := config.Load(path)
	if cfg.Scenes[0].Seed == nil || *cfg.Scenes[0].Seed != 1007 {
		t.Errorf("seed = %v", cfg.Scenes[0].Seed)
	}
	// the scene without a seed gets one too, as the vision model dislikes every frame
	if cfg.Scenes[1].Seed == nil || *cfg.Scenes[1].Seed != 1000 {
		t.Errorf("good scene seed = %v", cfg.Scenes[1].Seed)
	}
}

// everyFrame is a Vision that finds a severe defect in every frame.
type everyFrame struct{}

func (everyFrame) Judge(context.Context, []byte) (quality.Judgement, error) {
	return quality.Judgement{Defect: true, Severity: 9, Issue: "fused arms"}, nil
}

func TestCheckAfterRun_FixesRerunsAndStopsWhenClean(t *testing.T) {
	dir := checkProjectDir(t)
	path := filepath.Join(dir, "scenes.yaml")
	var out, errOut bytes.Buffer
	reruns := 0
	rerun := func(c *config.Config) error {
		reruns++
		return nil
	}
	code := checkAfterRun(context.Background(), path, checkOptions{NoVision: true, Fix: true, Attempts: 3}, rerun, &out, &errOut)
	if code != 0 || reruns != 1 {
		t.Fatalf("exit %d after %d reruns: %s%s", code, reruns, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "round 1: scenes.yaml edited") {
		t.Errorf("out = %s", out.String())
	}
	// without --fix the problems are reported and nothing is edited
	dir2 := checkProjectDir(t)
	out.Reset()
	if code := checkAfterRun(context.Background(), filepath.Join(dir2, "scenes.yaml"), checkOptions{NoVision: true}, rerun, &out, &errOut); code != 3 || reruns != 1 {
		t.Errorf("exit %d, reruns %d", code, reruns)
	}
}

func TestCheckAfterRun_GivesUpAfterTheRounds(t *testing.T) {
	dir := checkProjectDir(t)
	dp := realDeps()
	dp.newVision = func(string) quality.Vision { return everyFrame{} }
	var out, errOut bytes.Buffer
	reruns := 0
	code := checkAfterRun(context.Background(), filepath.Join(dir, "scenes.yaml"), checkOptions{Model: "fake", Fix: true, Attempts: 2, NewVision: dp.newVision}, func(*config.Config) error { reruns++; return nil }, &out, &errOut)
	if code != 3 || reruns != 2 || !strings.Contains(out.String(), "still have problems after 2 rounds") {
		t.Fatalf("exit %d, reruns %d: %s", code, reruns, out.String())
	}
}

func TestCheck_Usage(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runCheck(nil, &out, &errOut); code != 2 {
		t.Errorf("no file: %d", code)
	}
	if code := runCheck([]string{filepath.Join(t.TempDir(), "nope.yaml")}, &out, &errOut); code != 1 {
		t.Errorf("a missing project: %d", code)
	}
}

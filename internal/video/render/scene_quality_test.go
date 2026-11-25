package render

import (
	"context"
	"strings"
	"testing"

	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
)

func sdArgs(t *testing.T, scene config.Scene, d config.Defaults) string {
	t.Helper()
	fr := runner.NewFakeRunner()
	if err := Run(context.Background(), fr, scene, d, "out.png", nil); err != nil {
		t.Fatal(err)
	}
	return strings.Join(fr.Calls()[0].Args, " ")
}

func TestRun_ASceneRendersAtItsOwnResolutionAndSteps(t *testing.T) {
	d := config.Defaults{Model: "m.safetensors", Resolution: "1024x576", Sampler: "euler", Steps: 8}
	plain := sdArgs(t, config.Scene{ID: "b", Prompt: "x"}, d)
	custom := sdArgs(t, config.Scene{ID: "a", Prompt: "x", Render: &config.SceneRender{Resolution: "1280x704", Steps: 12}}, d)
	if !strings.Contains(plain, "-W 1024 -H 576") || !strings.Contains(plain, "--steps 8") {
		t.Errorf("a scene without a block must use the project's: %s", plain)
	}
	if !strings.Contains(custom, "-W 1280 -H 704") || !strings.Contains(custom, "--steps 12") {
		t.Errorf("a scene with a block must use its own: %s", custom)
	}
}

func TestRun_NothingSetKeepsTheCommandAsItWas(t *testing.T) {
	d := config.Defaults{Model: "m.safetensors", Resolution: "512x512", Sampler: "euler", Steps: 20}
	if got := sdArgs(t, config.Scene{ID: "a", Prompt: "x"}, d); strings.Contains(got, "--cfg-scale") {
		t.Errorf("a single checkpoint with no cfg must pass none: %s", got)
	}
}

func TestRun_CfgOfAScene(t *testing.T) {
	cfg := 3.0
	single := config.Defaults{Model: "m.safetensors", Resolution: "512x512", Sampler: "euler", Steps: 20}
	if got := sdArgs(t, config.Scene{ID: "a", Prompt: "x", Render: &config.SceneRender{CfgScale: &cfg}}, single); strings.Count(got, "--cfg-scale") != 1 || !strings.Contains(got, "--cfg-scale 3") {
		t.Errorf("single checkpoint: %s", got)
	}
	one := 1.0
	set := config.Defaults{ModelSet: &config.ModelSet{DiffusionModel: "d", LLM: "l", VAE: "v", CfgScale: &one}, Resolution: "1024x576", Sampler: "euler", Steps: 8}
	if got := sdArgs(t, config.Scene{ID: "a", Prompt: "x"}, set); strings.Count(got, "--cfg-scale") != 1 || !strings.Contains(got, "--cfg-scale 1") {
		t.Errorf("set without a scene cfg keeps the set's: %s", got)
	}
	// the scene's cfg replaces the set's: exactly one flag, with the scene's value
	if got := sdArgs(t, config.Scene{ID: "a", Prompt: "x", Render: &config.SceneRender{CfgScale: &cfg}}, set); strings.Count(got, "--cfg-scale") != 1 || !strings.Contains(got, "--cfg-scale 3") {
		t.Errorf("set with a scene cfg: %s", got)
	}
}

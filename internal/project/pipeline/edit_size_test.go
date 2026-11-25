package pipeline

import (
	"context"
	"testing"

	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
)

func editCfg(edit bool) *config.Config {
	return &config.Config{Defaults: config.Defaults{Resolution: "1024x576", ModelSet: &config.ModelSet{Edit: edit}}}
}

func TestWithEditSize_AnEditKeepsTheShapeOfItsPicture(t *testing.T) {
	fr := runner.NewFakeRunner()
	fr.Script(runner.ScriptedResult{Stdout: []byte(`{"streams": [{"width": 960, "height": 1367}]}`)})
	scene := config.Scene{ID: "s", Prompt: "put sunglasses on her", InitImage: "foto.jpg"}
	got := withEditSize(context.Background(), fr, editCfg(true), scene)
	if got.Render == nil || got.Render.Resolution != "640x912" {
		t.Fatalf("render = %+v", got.Render)
	}
	if scene.Render != nil {
		t.Error("the scene of the configuration must not be changed")
	}
}

func TestWithEditSize_KeepsTheScenesOwnOtherSettings(t *testing.T) {
	fr := runner.NewFakeRunner()
	fr.Script(runner.ScriptedResult{Stdout: []byte(`{"streams": [{"width": 1000, "height": 1000}]}`)})
	scene := config.Scene{ID: "s", InitImage: "foto.jpg", Render: &config.SceneRender{Steps: 30}}
	got := withEditSize(context.Background(), fr, editCfg(true), scene)
	if got.Render.Steps != 30 || got.Render.Resolution != "768x768" {
		t.Errorf("render = %+v", got.Render)
	}
}

func TestWithEditSize_LeavesOtherScenesAlone(t *testing.T) {
	fr := runner.NewFakeRunner()
	scene := config.Scene{ID: "s", InitImage: "foto.jpg"}
	if got := withEditSize(context.Background(), fr, editCfg(false), scene); got.Render != nil || len(fr.Calls()) != 0 {
		t.Error("an img2img project keeps its size and asks nothing")
	}
	if got := withEditSize(context.Background(), fr, editCfg(true), config.Scene{ID: "s"}); got.Render != nil || len(fr.Calls()) != 0 {
		t.Error("a scene without a picture to edit keeps its size")
	}
}

func TestWithEditSize_APictureThatCannotBeReadKeepsTheProjectsSize(t *testing.T) {
	fr := runner.NewFakeRunner()
	fr.Script(runner.ScriptedResult{Stdout: []byte("not json")})
	got := withEditSize(context.Background(), fr, editCfg(true), config.Scene{ID: "s", InitImage: "foto.jpg"})
	if got.Render != nil {
		t.Errorf("render = %+v", got.Render)
	}
}

package importer

import (
	"strings"
	"testing"

	"veritmo/internal/project/config"
)

func TestMap_BlenderLine(t *testing.T) {
	entries := []Entry{
		{ID: "hola", FullPrompt: "Dialogue: \"Hola\"\nBlender: hola.scene.json\na title card that spins"},
		{ID: "only", FullPrompt: "Blender: only.scene.json"},
		{ID: "path", FullPrompt: "x\nBlender: ../x.scene.json"},
		{ID: "blend", FullPrompt: "x\nBlender: x.blend"},
		{ID: "twice", FullPrompt: "x\nBlender: a.scene.json\nblender: b.scene.json"},
		{ID: "moves", FullPrompt: "x\nBlender: m.scene.json\nMotion: slow zoom"},
	}
	res, err := Map(entries, nil, Options{Project: "p", SilenceSeconds: 2})
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 3 || res.Rejected != 3 {
		t.Fatalf("imported %d rejected %d: %s", res.Imported, res.Rejected, res.Report())
	}
	hola, only, moves := res.Config.Scenes[0], res.Config.Scenes[1], res.Config.Scenes[2]
	if hola.Blender == nil || hola.Blender.Build != "hola.scene.json" || hola.TTSText != "Hola" || hola.Prompt != "a title card that spins" {
		t.Errorf("hola = %+v", hola)
	}
	if only.Blender == nil || only.Blender.Build != "only.scene.json" || only.Prompt != "" {
		t.Errorf("only = %+v", only)
	}
	if moves.Animate || moves.MotionPrompt != "" {
		t.Errorf("Motion must not be applied beside a 3D scene: %+v", moves)
	}
	report := res.Report()
	for _, want := range []string{"blender.build ← hola.scene.json", "must be the name of a .scene.json file", "a second Blender line", "nothing to animate"} {
		if !strings.Contains(report, want) {
			t.Errorf("the report lacks %q:\n%s", want, report)
		}
	}
	if err := res.Validate(); err != nil {
		t.Errorf("the scenes must validate without their files: %v", err)
	}
	yml := string(res.EmitYAML())
	if !strings.Contains(yml, "blender:\n      build: hola.scene.json") || strings.Contains(yml, "prompt: \"\"") {
		t.Errorf("yaml:\n%s", yml)
	}
}

func TestMapMotion_ABlenderLineMakesA3DGraphic(t *testing.T) {
	entries := []Entry{
		{ID: "logo", FullPrompt: "Duration: 6\nBlender: logo.scene.json"},
		{ID: "mixed", FullPrompt: "a rotating logo\nGraphic: spinning\nPart: stamp\nText: hi\nDuration: 4\nBlender: mixed.scene.json"},
		{ID: "plain", FullPrompt: "a red bar chart growing\nDuration: 5"},
		{ID: "bad", FullPrompt: "x\nBlender: ../x.scene.json"},
	}
	res, err := MapMotion(entries, nil, Options{Project: "p", SilenceSeconds: 2})
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 3 || res.Rejected != 1 {
		t.Fatalf("imported %d rejected %d: %s", res.Imported, res.Rejected, res.Report())
	}
	logo, mixed, plain := res.Config.Scenes[0], res.Config.Scenes[1], res.Config.Scenes[2]
	if logo.Blender == nil || logo.Blender.Build != "logo.scene.json" || *logo.SilenceSeconds != 6 || logo.MotionGraphic != "" || logo.MotionPart != "" {
		t.Errorf("logo = %+v", logo)
	}
	if mixed.Blender == nil || *mixed.SilenceSeconds != 4 || mixed.MotionGraphic != "" || mixed.MotionPart != "" {
		t.Errorf("mixed = %+v", mixed)
	}
	if plain.Blender != nil || plain.MotionGraphic != "a red bar chart growing" {
		t.Errorf("a graphic with no line is as it was: %+v", plain)
	}
	report := res.Report()
	for _, want := range []string{"blender.build ← logo.scene.json", "the scene is a 3D scene", "Part, Param, Text, BPM and Design lines"} {
		if !strings.Contains(report, want) {
			t.Errorf("the report lacks %q:\n%s", want, report)
		}
	}
	if err := res.Validate(); err != nil {
		t.Errorf("the scenes must validate: %v", err)
	}
	if yml := string(res.EmitYAML()); !strings.Contains(yml, "build: logo.scene.json") || strings.Contains(yml, "motion_graphic: \"\"") {
		t.Errorf("yaml:\n%s", yml)
	}
}

func TestExportDataset_BlenderScenesRoundTripAndTheBlendFileIsLeftOut(t *testing.T) {
	two := 2.0
	cfg := &config.Config{
		Project:  "three-d",
		Defaults: config.Defaults{SubjectPrompt: "a red fox"},
		Output:   config.Output{Mode: config.ModeVideo},
		Scenes: []config.Scene{
			{ID: "hola", TTSText: "Hola", Blender: &config.SceneBlender{Build: "hola.scene.json"}},
			{ID: "ball", Prompt: "a ball that bounces", SilenceSeconds: &two, Blender: &config.SceneBlender{Build: "ball.scene.json"}},
			{ID: "set", SilenceSeconds: &two, Blender: &config.SceneBlender{File: "set.blend"}},
			{ID: "plain", Prompt: "a quiet street", SilenceSeconds: &two},
		},
	}
	d, err := ExportDataset(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if d.Exported != 3 {
		t.Fatalf("exported %d (the .blend scene is left out): %v", d.Exported, d.Notes)
	}
	notes := strings.Join(d.Notes, "|")
	for _, want := range []string{"set.blend", "hola.scene.json, ball.scene.json", "project package"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes lack %q: %v", want, d.Notes)
		}
	}
	entries, rej, err := LoadBytes(d.JSON)
	if err != nil || len(rej) != 0 {
		t.Fatalf("%v %v", err, rej)
	}
	res, err := Map(entries, nil, Options{Project: "three-d", SilenceSeconds: 2})
	if err != nil || res.Rejected != 0 {
		t.Fatalf("%v %s", err, res.Report())
	}
	hola, ball, plain := res.Config.Scenes[0], res.Config.Scenes[1], res.Config.Scenes[2]
	if hola.Blender == nil || hola.Blender.Build != "hola.scene.json" || hola.TTSText != "Hola" || hola.Prompt != "" {
		t.Errorf("hola = %+v", hola)
	}
	if ball.Blender == nil || ball.Blender.Build != "ball.scene.json" || ball.Prompt != "a ball that bounces" {
		t.Errorf("ball = %+v", ball)
	}
	if plain.Blender != nil || !strings.Contains(plain.Prompt, "a quiet street") {
		t.Errorf("plain = %+v", plain)
	}
}

func TestExportDataset_AGraphicsProjectWithA3DSceneRoundTripsThroughTheMotionMode(t *testing.T) {
	six, four := 6.0, 4.0
	cfg := &config.Config{Project: "g", Output: config.Output{Mode: config.ModeVideo}, Scenes: []config.Scene{
		{ID: "logo", SilenceSeconds: &six, Blender: &config.SceneBlender{Build: "logo.scene.json"}},
		{ID: "bars", MotionGraphic: "a red bar chart growing", SilenceSeconds: &four},
	}}
	d, err := ExportDataset(cfg)
	if err != nil || d.Exported != 2 {
		t.Fatalf("%v %+v", err, d)
	}
	entries, rej, err := LoadBytes(d.JSON)
	if err != nil || len(rej) != 0 {
		t.Fatalf("%v %v", err, rej)
	}
	res, err := MapMotion(entries, nil, Options{Project: "g", SilenceSeconds: 2})
	if err != nil || res.Rejected != 0 {
		t.Fatalf("%v %s", err, res.Report())
	}
	logo, bars := res.Config.Scenes[0], res.Config.Scenes[1]
	if logo.Blender == nil || logo.Blender.Build != "logo.scene.json" || *logo.SilenceSeconds != 6 || bars.MotionGraphic != "a red bar chart growing" || *bars.SilenceSeconds != 4 {
		t.Errorf("logo = %+v bars = %+v", logo, bars)
	}
}

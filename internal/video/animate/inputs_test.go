package animate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
)

func firstFrameParams() Params {
	p := miniMaxParams()
	p.Defaults.Animate.FirstFrame = true
	return p
}

func TestBuildSDArgs_H3FirstAndLastFrame(t *testing.T) {
	p := firstFrameParams()
	args, err := BuildSDArgsWith(*p.Defaults.Animate, "move", 7, "tmp/anim_s1_init.png", p.RawPath, Inputs{EndImage: "tmp/anim_s1_end.png"})
	if err != nil {
		t.Fatal(err)
	}
	if argValue(t, args, "-i") != "tmp/anim_s1_init.png" || argValue(t, args, "--end-img") != "tmp/anim_s1_end.png" {
		t.Errorf("args = %q", args)
	}
	if strings.Contains(argValue(t, args, "-p"), "<Picture") {
		t.Errorf("a first-frame clip has no picture numbering: %q", args)
	}
	plain, _ := BuildSDArgs(*p.Defaults.Animate, "move", 7, "tmp/anim_s1_init.png", p.RawPath)
	for _, a := range plain {
		if a == "--end-img" {
			t.Errorf("no last picture, no --end-img: %q", plain)
		}
	}
}

func TestBuildSDArgs_LTXLastPicture(t *testing.T) {
	p := testParams()
	p.Defaults.Animate = &config.Animate{Engine: config.EngineLTX, DiffusionModel: "d", VAE: "v", AudioVAE: "a", LLM: "l", Connectors: "c"}
	args, err := BuildSDArgsWith(*p.Defaults.Animate, "move", 7, "tmp/init.png", p.RawPath, Inputs{EndImage: "tmp/end.png"})
	if err != nil {
		t.Fatal(err)
	}
	if argValue(t, args, "-i") != "tmp/init.png" || argValue(t, args, "--end-img") != "tmp/end.png" {
		t.Errorf("args = %q", args)
	}
}

func TestBuildSDArgs_H3ReferencesInOrderWithNumberedPrompt(t *testing.T) {
	p := miniMaxParams()
	in := Inputs{RefImages: []string{"r/image-1.png", "r/image-2.png"}, RefVideos: []string{"r/video-1", "r/video-2"}, RefAudios: []string{"r/audio-1.wav"}}
	args, err := BuildSDArgsWith(*p.Defaults.Animate, "move", 7, "tmp/init.png", p.RawPath, in)
	if err != nil {
		t.Fatal(err)
	}
	var seq []string
	for i, a := range args {
		if a == "-r" || a == "--ref-video" || a == "--ref-audio" {
			seq = append(seq, a+"="+args[i+1])
		}
	}
	want := "-r=tmp/init.png -r=r/image-1.png -r=r/image-2.png --ref-video=r/video-1 --ref-video=r/video-2 --ref-audio=r/audio-1.wav"
	if strings.Join(seq, " ") != want {
		t.Errorf("order:\n got  %v\n want %s", seq, want)
	}
	if got := argValue(t, args, "-p"); !strings.HasPrefix(got, "Use the images <Picture 1>, <Picture 2>, <Picture 3> as references") || !strings.HasSuffix(got, "move") {
		t.Errorf("prompt = %q", got)
	}
	one, _ := BuildSDArgsWith(*p.Defaults.Animate, "move", 7, "tmp/init.png", p.RawPath, Inputs{})
	if argValue(t, one, "-p") != MiniMaxPromptPrefix+"move" {
		t.Errorf("one picture keeps today's sentence: %q", one)
	}
}

func inputScene() config.Scene {
	return config.Scene{ID: "s1", Prompt: "p", Animate: true, Image: "a.png"}
}

func TestPrepareInputs_WhatFfmpegIsAskedFor(t *testing.T) {
	dir := t.TempDir()
	raw := filepath.Join(dir, "anim_s1.avi")
	s := inputScene()
	s.References = &config.SceneReferences{Images: []string{"b.png"}, Videos: []string{"v.mp4"}, Audios: []string{"s.m4a"}}
	fr := runner.NewFakeRunner()
	a := config.Animate{Engine: config.EngineMiniMaxH3, Resolution: "768x416"}.Resolved()
	in, err := PrepareInputs(context.Background(), fr, s, a, "", raw)
	if err != nil {
		t.Fatal(err)
	}
	calls := fr.Calls()
	if len(calls) != 3 {
		t.Fatalf("calls = %+v", calls)
	}
	if !strings.Contains(argValue(t, calls[0].Args, "-vf"), "768*416") || len(in.RefImages) != 1 {
		t.Errorf("picture: %v", calls[0].Args)
	}
	if got := argValue(t, calls[1].Args, "-vf"); got != "fps=24,scale=-2:416" || len(in.RefVideos) != 1 {
		t.Errorf("video: %v", calls[1].Args)
	}
	if _, err := os.Stat(in.RefVideos[0]); err != nil {
		t.Errorf("the frames folder must exist: %v", err)
	}
	if calls[2].Args[len(calls[2].Args)-1] != in.RefAudios[0] || argValue(t, calls[2].Args, "-ar") != "32000" || argValue(t, calls[2].Args, "-ac") != "2" {
		t.Errorf("audio: %v", calls[2].Args)
	}
	for _, c := range calls[:1] {
		if !strings.Contains(strings.Join(c.Args, " "), filepath.Join(config.SourcesSubdir, "b.png")) {
			t.Errorf("sources are read from the project: %v", c.Args)
		}
	}
	none, _ := PrepareInputs(context.Background(), runner.NewFakeRunner(), inputScene(), a, "", raw)
	if none.EndImage != "" || len(none.RefImages) != 0 {
		t.Errorf("a scene with none prepares none: %+v", none)
	}
}

func TestRun_LastPictureIsFittedPassedAndNotPingPonged(t *testing.T) {
	p := firstFrameParams()
	p.Scene = inputScene()
	p.Scene.EndImage = "end.png"
	fr := runner.NewFakeRunner()
	if _, err := Run(context.Background(), fr, p, nil); err != nil {
		t.Fatal(err)
	}
	calls := fr.Calls()
	if len(calls) != 4 || calls[0].Name != "ffmpeg" || calls[1].Name != "ffmpeg" || calls[2].Name != "sd" || calls[3].Name != "ffmpeg" {
		t.Fatalf("calls = %+v", calls)
	}
	if argValue(t, calls[2].Args, "--end-img") != "tmp/anim_s1_end.png" {
		t.Errorf("sd args: %v", calls[2].Args)
	}
	if strings.Contains(strings.Join(calls[3].Args, " "), "reverse") {
		t.Errorf("a clip that ends on a picture is not played back: %v", calls[3].Args)
	}
}

func TestRun_ASceneWithNoneIsAsBefore(t *testing.T) {
	fr := runner.NewFakeRunner()
	if _, err := Run(context.Background(), fr, miniMaxParams(), nil); err != nil {
		t.Fatal(err)
	}
	calls := fr.Calls()
	if len(calls) != 3 || !strings.Contains(strings.Join(calls[2].Args, " "), "reverse") {
		t.Errorf("calls = %+v", calls)
	}
}

func TestPrepareInputs_FoldersAreMadeInTheProjectNotTheWorkingDirectory(t *testing.T) {
	project := t.TempDir()
	s := inputScene()
	s.References = &config.SceneReferences{Images: []string{"b.png"}, Videos: []string{"v.mp4"}}
	a := config.Animate{Engine: config.EngineMiniMaxH3, Resolution: "768x416"}.Resolved()
	in, err := PrepareInputs(context.Background(), runner.NewFakeRunner(), s, a, project, "tmp/anim_s1.avi")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(project, in.RefVideos[0])); err != nil {
		t.Errorf("the frames folder must be inside the project: %v", err)
	}
	if _, err := os.Stat(RefsDir("tmp/anim_s1.avi")); err == nil {
		t.Error("nothing may be created in the working directory")
		os.RemoveAll("tmp")
	}
}

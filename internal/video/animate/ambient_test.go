package animate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"veritmo/internal/platform/runner"
)

func TestBuildAmbientArgs_ForwardPartWithFadeOut(t *testing.T) {
	args := BuildAmbientArgs("tmp/anim_s1.avi", AmbientPath("tmp/anim_s1.avi"), 49, 24)
	if args[len(args)-1] != "tmp/anim_s1.ambient.wav" {
		t.Errorf("output = %q", args[len(args)-1])
	}
	if argValue(t, args, "-t") != "2.042" {
		t.Errorf("-t = %q, want the forward length 49/24", argValue(t, args, "-t"))
	}
	if af := argValue(t, args, "-af"); af != "afade=t=out:st=1.742:d=0.300" {
		t.Errorf("-af = %q", af)
	}
	if argValue(t, args, "-ar") != "44100" || argValue(t, args, "-ac") != "2" {
		t.Errorf("args = %v", args)
	}
}

func TestRun_ModelAudioAddsOneExtractionCall(t *testing.T) {
	p := ltxParams()
	p.Defaults.Animate.ModelAudio = true
	fr := runner.NewFakeRunner()
	if _, err := Run(context.Background(), fr, p, nil); err != nil {
		t.Fatal(err)
	}
	calls := fr.Calls()
	if len(calls) != 4 || calls[3].Name != "ffmpeg" || !strings.HasSuffix(calls[3].Args[len(calls[3].Args)-1], ".ambient.wav") {
		t.Fatalf("calls = %+v", calls)
	}
	if strings.Contains(strings.Join(calls[2].Args, " "), "ambient") {
		t.Errorf("the ping-pong call must not change: %v", calls[2].Args)
	}
}

func TestRun_WithoutModelAudioNoExtraction(t *testing.T) {
	fr := runner.NewFakeRunner()
	if _, err := Run(context.Background(), fr, ltxParams(), nil); err != nil {
		t.Fatal(err)
	}
	if len(fr.Calls()) != 3 {
		t.Errorf("calls = %+v", fr.Calls())
	}
}

func TestRun_ModelAudioFailureNamesTheScene(t *testing.T) {
	p := ltxParams()
	p.Defaults.Animate.ModelAudio = true
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if inv.Name == "ffmpeg" && strings.HasSuffix(inv.Args[len(inv.Args)-1], ".ambient.wav") {
			return runner.ScriptedResult{Err: errors.New("matches no streams")}
		}
		return runner.ScriptedResult{}
	})
	_, err := Run(context.Background(), fr, p, nil)
	if err == nil || !strings.Contains(err.Error(), `scene "s1"`) || !strings.Contains(err.Error(), "audio") {
		t.Errorf("error = %v", err)
	}
}

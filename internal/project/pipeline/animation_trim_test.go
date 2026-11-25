package pipeline

import (
	"os"
	"strings"
	"testing"

	"veritmo/internal/platform/cache"
)

func TestAnimationTrim_CutsThePartOfTheForwardAnimationWithoutMakingItAgain(t *testing.T) {
	cfg := animateProject(t)
	start, end := 0.5, 1.5
	cfg.Scenes[0].ClipStart, cfg.Scenes[0].ClipEnd = &start, &end
	fr := animateRunner(false)
	m := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
	if _, err := runAnimateVideo(t, cfg, fr, m); err != nil {
		t.Fatal(err)
	}
	var cut, clip string
	vid := 0
	for _, c := range fr.Calls() {
		line := strings.Join(c.Args, " ")
		if c.Name == "sd" && strings.Contains(line, "vid_gen") {
			vid++
		}
		switch {
		case strings.HasSuffix(line, "tmp/scene_moving.cut.mp4"):
			cut = line
		case strings.HasSuffix(line, "tmp/scene_moving.mp4"):
			clip = line
		}
	}
	if !strings.Contains(cut, "-ss 0.500 -t 1.000 -i tmp/scene_moving.anim.mp4") {
		t.Fatalf("the part is cut from the animation: %s", cut)
	}
	if !strings.Contains(clip, "-stream_loop -1 -i tmp/scene_moving.cut.mp4") {
		t.Fatalf("the scene is built from the cut part: %s", clip)
	}
	if vid != 1 {
		t.Fatalf("one animation made, got %d", vid)
	}

	// only the trim changes: the animation stays cached
	start = 0.8
	fr2 := animateRunner(false)
	buf, err := runAnimateVideo(t, cfg, fr2, m)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range fr2.Calls() {
		if c.Name == "sd" && strings.Contains(strings.Join(c.Args, " "), "vid_gen") {
			t.Fatalf("the animation must not be made again:\n%s", buf)
		}
	}
	if got := strings.Join(phasesOf(buf, "moving"), " "); !strings.Contains(got, "clip") || strings.Contains(got, "animate") {
		t.Fatalf("phases = %q: only the clip phase runs", got)
	}
}

func TestAnimationTrim_PastTheForwardPartStopsTheRun(t *testing.T) {
	cfg := animateProject(t)
	end := 30.0
	cfg.Scenes[0].ClipEnd = &end
	_, err := runAnimateVideo(t, cfg, animateRunner(false), &cache.Manifest{Scenes: map[string]cache.SceneEntry{}})
	if err == nil || !strings.Contains(err.Error(), "beyond the end of the animation") {
		t.Fatalf("err = %v", err)
	}
}

func TestAnimationSpeed_OnlyTheClipPhaseRunsAgain(t *testing.T) {
	cfg := animateProject(t)
	fr := animateRunner(false)
	m := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
	if _, err := runAnimateVideo(t, cfg, fr, m); err != nil {
		t.Fatal(err)
	}
	speed := 0.5
	cfg.Scenes[0].ClipSpeed = &speed
	fr2 := animateRunner(false)
	buf, err := runAnimateVideo(t, cfg, fr2, m)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range fr2.Calls() {
		if c.Name == "sd" {
			t.Fatalf("the animation must not be made again:\n%s", buf)
		}
	}
	found := false
	for _, c := range fr2.Calls() {
		if strings.Contains(strings.Join(c.Args, " "), "setpts=2.000000*PTS") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the clip is rebuilt at half speed: %v", fr2.Calls())
	}
}

func TestKeptAnimation_KeptWithTheClipAndPrunedWhenTheSceneStopsAnimating(t *testing.T) {
	cfg := animateProject(t)
	// the fake runner writes no files: give the run the animation and the clip it would have made
	if err := os.MkdirAll("tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"scene_moving.anim.mp4": "animation", "scene_moving.mp4": "clip", "scene_still.mp4": "still clip"} {
		if err := os.WriteFile("tmp/"+name, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
	if _, err := runAnimateVideo(t, cfg, animateRunner(false), m); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("out/clips/moving.anim.mp4")
	if err != nil || string(got) != "animation" {
		t.Fatalf("the complete animation is kept: %v %q", err, got)
	}
	if _, err := os.Stat("out/clips/still.anim.mp4"); !os.IsNotExist(err) {
		t.Fatalf("a scene that does not animate has no animation to keep: %v", err)
	}
	if _, err := os.Stat("out/clips/moving.mp4"); err != nil {
		t.Fatalf("the finished clip is kept too: %v", err)
	}

	// the scene stops animating: its animation is pruned, its clip stays
	cfg.Scenes[0].Animate = false
	cfg.Scenes[0].MotionPrompt = ""
	if _, err := runAnimateVideo(t, cfg, animateRunner(false), &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("out/clips/moving.anim.mp4"); !os.IsNotExist(err) {
		t.Fatalf("pruned when the scene no longer animates: %v", err)
	}
	if _, err := os.Stat("out/clips/moving.mp4"); err != nil {
		t.Fatalf("the clip stays: %v", err)
	}
}

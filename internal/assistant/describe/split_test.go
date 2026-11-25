package describe

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"veritmo/internal/platform/runner"
)

func TestParseCuts(t *testing.T) {
	out := "frame:0    pts:25088   pts_time:2.041667\nlavfi.scene_score=0.46\nframe:1    pts:49664   pts_time:4.5\nlavfi.scene_score=0.52\nnoise\n"
	got := parseCuts(out)
	if !reflect.DeepEqual(got, []float64{2.041667, 4.5}) {
		t.Fatalf("got %v", got)
	}
	if len(parseCuts("")) != 0 {
		t.Fatal("no output, no cuts")
	}
}

func TestBuildShots(t *testing.T) {
	// 12 s with cuts at 4 and 8: three shots.
	got := buildShots([]float64{4, 8}, 12)
	want := []ShotRange{{0, 4}, {4, 8}, {8, 12}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	// A cut that leaves 0.8 s is merged into the previous shot.
	got = buildShots([]float64{4, 4.8}, 12)
	if !reflect.DeepEqual(got, []ShotRange{{0, 4.8}, {4.8, 12}}) && !reflect.DeepEqual(got, []ShotRange{{0, 4}, {4, 12}}) {
		t.Fatalf("short shot not merged: %v", got)
	}
	for _, s := range got {
		if s.End-s.Start < MinShotSeconds {
			t.Fatalf("a shot under %v s remains: %v", MinShotSeconds, got)
		}
	}
	// A short first shot merges into the next one.
	got = buildShots([]float64{1, 6}, 12)
	if !reflect.DeepEqual(got, []ShotRange{{0, 6}, {6, 12}}) {
		t.Fatalf("short first shot: %v", got)
	}
	// Cuts outside the clip are ignored; no cut is one shot.
	if got := buildShots([]float64{20}, 12); !reflect.DeepEqual(got, []ShotRange{{0, 12}}) {
		t.Fatalf("got %v", got)
	}
	if got := buildShots(nil, 7); !reflect.DeepEqual(got, []ShotRange{{0, 7}}) {
		t.Fatalf("got %v", got)
	}
}

func TestCapShots(t *testing.T) {
	var shots []ShotRange
	for i := 0; i < 30; i++ {
		shots = append(shots, ShotRange{float64(i) * 2, float64(i)*2 + 2})
	}
	shots[7].End = shots[7].Start + 1.6 // a shorter one: merged first
	shots[8].Start = shots[7].End
	shots = capShots(shots, MaxShots)
	if len(shots) != MaxShots {
		t.Fatalf("want %d shots, got %d", MaxShots, len(shots))
	}
	if shots[0].Start != 0 {
		t.Fatalf("the first shot must start at 0: %v", shots[0])
	}
	for i := 1; i < len(shots); i++ {
		if shots[i].Start != shots[i-1].End {
			t.Fatalf("shots must be contiguous: %v", shots)
		}
	}
}

// splitDescriber makes a Describer whose clip has the given shots.
func splitDescriber(t *testing.T, f *fakeOllama, ranges []ShotRange) *Describer {
	d := f.server(t)
	d.Shots = func(ctx context.Context, path string) ([]ShotRange, [][]byte, error) {
		var frames [][]byte
		for i := range ranges {
			frames = append(frames, []byte{byte(i + 1)})
		}
		return ranges, frames, nil
	}
	return d
}

func TestSplitDescribesEveryShot(t *testing.T) {
	f := newFake()
	d := splitDescriber(t, f, []ShotRange{{0, 4}, {4, 8}, {8, 12}})
	res, err := d.Describe(context.Background(), file(t, "c.mp4"), Options{Split: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Shots) != 3 || res.Prompt != res.Shots[0].Prompt || res.Frames != 3 || res.Model != DefaultModel {
		t.Fatalf("unexpected result %+v", res)
	}
	for i, s := range res.Shots {
		if s.Prompt != goodText || s.Motion == "" || s.End <= s.Start {
			t.Fatalf("shot %d incomplete: %+v", i, s)
		}
	}
	if res.Shots[1].Start != 4 || res.Shots[1].End != 8 {
		t.Fatalf("shot ranges lost: %+v", res.Shots[1])
	}
	ages := 0
	for _, c := range f.chats {
		if strings.HasPrefix(c["messages"].([]any)[0].(map[string]any)["content"].(string), "You check a picture") {
			ages++
		}
	}
	if ages != 3 {
		t.Fatalf("every shot needs an age check, got %d", ages)
	}
	if len(f.unloaded) == 0 {
		t.Fatal("the model was not unloaded")
	}
}

func TestSplitMinorShotRefusesAll(t *testing.T) {
	f := newFake()
	f.age = `{"people":1,"minor":true,"unclear":false}`
	d := splitDescriber(t, f, []ShotRange{{0, 4}, {4, 8}})
	res, err := d.Describe(context.Background(), file(t, "c.mp4"), Options{Split: true})
	if !errors.Is(err, ErrMinor) || len(res.Shots) != 0 || res.Prompt != "" {
		t.Fatalf("want ErrMinor and no shots, got %v %+v", err, res)
	}
	for _, c := range f.chats {
		if strings.HasPrefix(c["messages"].([]any)[0].(map[string]any)["content"].(string), "You write prompts") {
			t.Fatal("nothing may be described when a shot is refused")
		}
	}
	if len(f.unloaded) == 0 {
		t.Fatal("the model was not unloaded after refusing")
	}
}

func TestSplitRefusalHasNoShots(t *testing.T) {
	f := newFake()
	f.describeReply = "I'm sorry, but I can't describe this."
	d := splitDescriber(t, f, []ShotRange{{0, 4}, {4, 8}})
	res, err := d.Describe(context.Background(), file(t, "c.mp4"), Options{Split: true})
	var re *RefusedError
	if !errors.As(err, &re) || len(res.Shots) != 0 {
		t.Fatalf("want a RefusedError and no shots, got %v %+v", err, res)
	}
}

func TestSplitOneShotWhenNoCut(t *testing.T) {
	f := newFake()
	d := splitDescriber(t, f, []ShotRange{{0, 9}})
	res, err := d.Describe(context.Background(), file(t, "c.mp4"), Options{Split: true})
	if err != nil || len(res.Shots) != 1 || res.Shots[0].End != 9 {
		t.Fatalf("a clip with no cut is one shot: %v %+v", err, res)
	}
}

func TestSplitIgnoredForAPicture(t *testing.T) {
	f := newFake()
	d := splitDescriber(t, f, []ShotRange{{0, 4}, {4, 8}})
	res, err := d.Describe(context.Background(), file(t, "a.png"), Options{Split: true})
	if err != nil || len(res.Shots) != 0 || res.Kind != KindImage || res.Prompt != goodText {
		t.Fatalf("a picture is one scene: %v %+v", err, res)
	}
}

func TestSplitWithRealFFmpeg(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	mp4 := filepath.Join(dir, "cuts.mp4")
	// Three 3-second shots of very different content: red, a test pattern, blue.
	args := []string{"-y", "-v", "error",
		"-f", "lavfi", "-i", "color=c=red:size=320x240:rate=10:d=3",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=10:d=3",
		"-f", "lavfi", "-i", "color=c=blue:size=320x240:rate=10:d=3",
		"-filter_complex", "[0][1][2]concat=n=3:v=1:a=0", "-pix_fmt", "yuv420p", mp4}
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	ranges, frames, err := extractShots(context.Background(), runner.NewExecRunner(), mp4)
	if err != nil {
		t.Fatal(err)
	}
	if len(ranges) != 3 || len(frames) != 3 {
		t.Fatalf("want 3 shots, got %v", ranges)
	}
	if ranges[0].Start != 0 || ranges[2].End < 8.9 {
		t.Fatalf("the shots must cover the clip: %v", ranges)
	}
	for _, fr := range frames {
		if len(fr) == 0 {
			t.Fatal("empty frame")
		}
	}
	if left, _ := filepath.Glob(filepath.Join(os.TempDir(), "describe-*")); len(left) != 0 {
		t.Fatalf("temporary frames remain: %v", left)
	}
}

package assemble

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"veritmo/internal/project/config"
)

func TestBuildCrossfadeArgs_ChainsFadesAndJoinsRunsSeparatedByCuts(t *testing.T) {
	clips := []JoinClip{
		{Path: "a.mp4", Duration: 2},
		{Path: "b.mp4", Duration: 2, Fade: 0.5},
		{Path: "c.mp4", Duration: 2, Fade: 0}, // a cut
		{Path: "d.mp4", Duration: 2, Fade: 0.25},
	}
	if !HasFades(clips) {
		t.Fatal("HasFades")
	}
	args := BuildCrossfadeArgs(clips, 24, "out.mp4")
	line := strings.Join(args, " ")
	for _, want := range []string{
		"[0:v][1:v]xfade=transition=fade:duration=0.500:offset=1.500[v1]",
		"[0:a][1:a]acrossfade=d=0.500[a1]",
		"[2:v][3:v]xfade=transition=fade:duration=0.250:offset=1.750[v3]",
		"[v1][a1][v3][a3]concat=n=2:v=1:a=1[vc][aout]",
		"[vc]fps=24,format=yuv420p[vout]",
		"-map [vout] -map [aout]",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("missing %q in\n%s", want, line)
		}
	}
	if HasFades([]JoinClip{{Path: "a"}, {Path: "b", Fade: 0}}) || HasFades([]JoinClip{{Path: "a", Fade: 1}}) {
		t.Fatal("no join fades: HasFades must be false (the first clip's fade is ignored)")
	}
}

func makeClip(t *testing.T, path string, seconds float64, color string) {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-y", "-v", "error",
		"-f", "lavfi", "-i", "color=c="+color+":s=160x90:r=24:d="+strconv.FormatFloat(seconds, 'f', 2, 64),
		"-f", "lavfi", "-i", "sine=f=440:r=44100:d="+strconv.FormatFloat(seconds, 'f', 2, 64),
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "192k", "-ar", "44100", "-ac", "2", "-shortest", path).CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
}

func duration(t *testing.T, path string) float64 {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	d, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	return d
}

type fadeRunner struct{}

func (fadeRunner) Run(ctx context.Context, args []string) (string, string, error) {
	out, err := exec.CommandContext(ctx, "ffmpeg", args...).CombinedOutput()
	return "", string(out), err
}

func TestJoinWithFades_RealFfmpegOverlapsTheScenes(t *testing.T) {
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	dir := t.TempDir()
	var clips []JoinClip
	for i, color := range []string{"red", "green", "blue"} {
		p := filepath.Join(dir, "c"+strconv.Itoa(i)+".mp4")
		makeClip(t, p, 2, color)
		clips = append(clips, JoinClip{Path: p, Duration: 2})
	}
	clips[1].Fade, clips[2].Fade = 0.5, 0.5
	out := filepath.Join(dir, "all.mp4")
	if stderr, err := JoinWithFades(context.Background(), fadeRunner{}, clips, 24, out); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if d := duration(t, out); d < 4.95 || d > 5.1 {
		t.Fatalf("three 2 s scenes with two 0.5 s fades last 5 s, got %.2f", d)
	}

	// a cut in the middle: only one fade
	clips[2].Fade = 0
	out2 := filepath.Join(dir, "mixed.mp4")
	if stderr, err := JoinWithFades(context.Background(), fadeRunner{}, clips, 24, out2); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if d := duration(t, out2); d < 5.45 || d > 5.6 {
		t.Fatalf("one fade and one cut last 5.5 s, got %.2f", d)
	}
}

func TestFinalVideoArgs_PutTheIndexFirstAndAnimatedClipsGetTheNormalColourRange(t *testing.T) {
	has := func(args []string, a, b string) bool {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == a && args[i+1] == b {
				return true
			}
		}
		return false
	}
	if !has(BuildConcatArgs("l.txt", "o.mp4"), "-movflags", "+faststart") {
		t.Error("the joined video has its index first, so a browser can play it before it has all arrived")
	}
	if !has(BuildCrossfadeArgs([]JoinClip{{Path: "a", Duration: 2}, {Path: "b", Duration: 2, Fade: 0.5}}, 24, "o.mp4"), "-movflags", "+faststart") {
		t.Error("so does the crossfaded one")
	}
	if !has(BuildMixArgs("v.mp4", "m.mp3", "o.mp4", MixOptions{}), "-movflags", "+faststart") {
		t.Error("and the one with background music")
	}
	vf := BuildVideoFilter(Scene{AnimatedPath: "a.mp4", AnimatedDuration: 2, Duration: 2, Width: 640, Height: 360}, 24)
	if !strings.Contains(vf, "scale=out_range=tv") {
		t.Errorf("an animation (JPEG-range) is brought to the normal range: %s", vf)
	}
	if vf := BuildVideoFilter(Scene{ImagePath: "i.png", Duration: 2, Width: 640, Height: 360}, 24); strings.Contains(vf, "out_range") {
		t.Errorf("a still needs no range conversion: %s", vf)
	}
}

func TestConcatJoin_RealFfmpegGivesContinuousAudio(t *testing.T) {
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	dir := t.TempDir()
	var list strings.Builder
	for i, color := range []string{"red", "green", "blue", "white"} {
		p := filepath.Join(dir, "c"+strconv.Itoa(i)+".mp4")
		makeClip(t, p, 2.02, color)
		list.WriteString("file '" + p + "'\n")
	}
	listFile := filepath.Join(dir, "list.txt")
	if err := os.WriteFile(listFile, []byte(list.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "all.mp4")
	if stderr, err := ConcatClips(context.Background(), fadeRunner{}, listFile, out); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	pk, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "a", "-show_entries", "packet=pts_time", "-of", "csv=p=0", out).Output()
	if err != nil {
		t.Fatal(err)
	}
	var prev float64
	for i, l := range strings.Fields(string(pk)) {
		v, _ := strconv.ParseFloat(strings.Trim(l, ","), 64)
		if i > 0 && (v-prev < 0.015 || v-prev > 0.03) {
			t.Fatalf("audio packet %d is %.4f s after the previous one: a repeated or overlapping timestamp at a join", i, v-prev)
		}
		prev = v
	}
	// one libx264 stream of normal colour, whose frames can all be decoded
	vi, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v", "-show_entries", "stream=codec_name,pix_fmt,nb_frames", "-of", "csv=p=0", out).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(vi)); !strings.HasPrefix(got, "h264,yuv420p,") {
		t.Fatalf("the join is encoded as H.264 yuv420p: %q", got)
	}
	if dec, err := exec.Command("ffmpeg", "-v", "error", "-i", out, "-f", "null", "-").CombinedOutput(); err != nil || len(dec) != 0 {
		t.Fatalf("the whole joined video decodes without errors: %v %s", err, dec)
	}
	// the index comes first
	head, _ := os.ReadFile(out)
	if i, j := strings.Index(string(head), "moov"), strings.Index(string(head), "mdat"); i < 0 || j < 0 || i > j {
		t.Fatalf("the index (moov at %d) must come before the data (mdat at %d)", i, j)
	}
}

func TestBuildCrossfadeArgs_UsesTheKindOfEachTransition(t *testing.T) {
	args := strings.Join(BuildCrossfadeArgs([]JoinClip{{Path: "a", Duration: 2}, {Path: "b", Duration: 2, Fade: 0.5, Kind: "wipeleft"}, {Path: "c", Duration: 2, Fade: 0.5}}, 24, "o.mp4"), " ")
	if !strings.Contains(args, "xfade=transition=wipeleft:") || !strings.Contains(args, "xfade=transition=fade:") {
		t.Fatal(args)
	}
}

func TestJoinWithFades_EveryKindIsAnXfadeFfmpegKnows(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.mp4"), filepath.Join(dir, "b.mp4")
	makeClip(t, a, 2, "red")
	makeClip(t, b, 2, "blue")
	for _, kind := range config.Transitions {
		out := filepath.Join(dir, kind+".mp4")
		clips := []JoinClip{{Path: a, Duration: 2}, {Path: b, Duration: 2, Fade: 0.5, Kind: kind}}
		if stderr, err := JoinWithFades(context.Background(), fadeRunner{}, clips, 24, out); err != nil {
			t.Errorf("%s: %v\n%s", kind, err, stderr)
		}
	}
}

func TestAShaderJoinReplacesTheXfadePictureAndKeepsTheSoundCrossfade(t *testing.T) {
	clips := []JoinClip{
		{Path: "a.mp4", Duration: 6},
		{Path: "b.mp4", Duration: 5, Fade: 0.5, Kind: "shader:glitch", ShaderVideo: "tmp/t1.mp4"},
		{Path: "c.mp4", Duration: 4, Fade: 0.4, Kind: "wipeleft"},
	}
	joined := strings.Join(BuildCrossfadeArgs(clips, 30, "out.mp4"), " ")
	for _, want := range []string{
		"-i a.mp4 -i b.mp4 -i c.mp4 -i tmp/t1.mp4",
		"[0:v]trim=end=5.500,setpts=PTS-STARTPTS[h1]",
		"[1:v]trim=start=0.500,setpts=PTS-STARTPTS[b1]",
		"[h1][3:v][b1]concat=n=3:v=1:a=0[v1]",
		"[0:a][1:a]acrossfade=d=0.500[a1]",
		// the next join is an ordinary xfade: the run is 6 + 5 - 0.5 = 10.5 s long
		"[v1][2:v]xfade=transition=wipeleft:duration=0.400:offset=10.100[v2]",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "transition=shader") || strings.Contains(joined, "xfade=transition=glitch") {
		t.Error("a shader kind is never an xfade name")
	}
}

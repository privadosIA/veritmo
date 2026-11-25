package assemble

import (
	"fmt"
	"math"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"veritmo/internal/project/config"
)

func f(v float64) *float64 { return &v }

func graphOf(t *testing.T, plan MixdownPlan) string {
	t.Helper()
	for i, a := range plan.Args {
		if a == "-filter_complex" {
			return plan.Args[i+1]
		}
	}
	t.Fatal("no filter graph")
	return ""
}

func TestBuildMixdownGoldenLayers(t *testing.T) {
	plan, err := BuildMixdown([]MixdownInput{
		{Path: "out/s/base.mp3", Duration: 20, Clip: config.Clip{Track: "base"}},
		{Path: "sources/voz.mp3", Duration: 12, Clip: config.Clip{File: "voz.mp3", Start: f(10), Gain: f(0.8), FadeIn: f(1), FadeOut: f(2), TrimStart: f(1.7), TrimEnd: f(11.7)}},
	}, "out/s/final.mp3", config.MusicFormatMP3)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-y", "-i", "out/s/base.mp3", "-i", "sources/voz.mp3", "-filter_complex",
		"[0:a]atrim=start=0:end=20,asetpts=PTS-STARTPTS,aresample=44100,aformat=sample_fmts=fltp:channel_layouts=stereo[c0];" +
			"[1:a]atrim=start=1.7:end=11.7,asetpts=PTS-STARTPTS,aresample=44100,aformat=sample_fmts=fltp:channel_layouts=stereo," +
			"afade=t=in:st=0:d=1,afade=t=out:st=8:d=2,volume=0.8,adelay=delays=10000:all=1[c1];" +
			"[c0][c1]amix=inputs=2:normalize=0:duration=longest:dropout_transition=0,alimiter=limit=0.95:level=disabled[out]",
		"-map", "[out]", "-ar", "44100", "-ac", "2", "-c:a", "libmp3lame", "-q:a", "2", "out/s/final.mp3"}
	if strings.Join(plan.Args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args:\n got  %q\n want %q", plan.Args, want)
	}
	if plan.Seconds != 20 {
		t.Fatalf("seconds = %v, want 20 (the base ends last)", plan.Seconds)
	}
}

func TestBuildMixdownSequenceDurationAndWav(t *testing.T) {
	plan, err := BuildMixdown([]MixdownInput{
		{Path: "a.wav", Duration: 30, Clip: config.Clip{TrimStart: f(1.7), TrimEnd: f(24.9)}},
		{Path: "b.wav", Duration: 30, Clip: config.Clip{Start: f(22.1), TrimStart: f(1.6), TrimEnd: f(26.2)}},
	}, "o.wav", config.MusicFormatWAV)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(plan.Seconds-(22.1+24.6)) > 1e-9 {
		t.Fatalf("seconds = %v, want 46.7", plan.Seconds)
	}
	if !strings.Contains(strings.Join(plan.Args, " "), "-c:a pcm_s16le") {
		t.Fatalf("a wav mix needs the pcm codec: %q", plan.Args)
	}
}

func TestBuildMixdownMutedClipsAreNeitherReadNorCounted(t *testing.T) {
	plan, err := BuildMixdown([]MixdownInput{
		{Path: "a.mp3", Duration: 10, Clip: config.Clip{}},
		{Path: "muted.mp3", Duration: 100, Clip: config.Clip{Mute: true, Start: f(50)}},
	}, "o.mp3", config.MusicFormatMP3)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(plan.Args, " "), "muted.mp3") || plan.Seconds != 10 {
		t.Fatalf("muted clip leaked: %q %v", plan.Args, plan.Seconds)
	}
	g := graphOf(t, plan)
	if strings.Contains(g, "amix") || !strings.Contains(g, "[c0]anull,alimiter") {
		t.Fatalf("a single clip is not summed: %s", g)
	}
	if _, err := BuildMixdown([]MixdownInput{{Path: "m.mp3", Duration: 5, Clip: config.Clip{Mute: true}}}, "o.mp3", config.MusicFormatMP3); err == nil ||
		!strings.Contains(err.Error(), "every clip is muted") {
		t.Fatalf("all muted must fail clearly: %v", err)
	}
}

func TestBuildMixdownTrimsAreClampedAndOutsideFails(t *testing.T) {
	plan, err := BuildMixdown([]MixdownInput{{Path: "a.mp3", Duration: 10, Clip: config.Clip{TrimEnd: f(99)}}}, "o.mp3", config.MusicFormatMP3)
	if err != nil || plan.Seconds != 10 || !strings.Contains(graphOf(t, plan), "atrim=start=0:end=10") {
		t.Fatalf("trim_end past the file must clamp: %v %v", plan.Seconds, err)
	}
	_, err = BuildMixdown([]MixdownInput{{Path: "a.mp3", Duration: 10, Clip: config.Clip{TrimStart: f(12)}}}, "o.mp3", config.MusicFormatMP3)
	if err == nil || !strings.Contains(err.Error(), "clip 1") || !strings.Contains(err.Error(), "outside the file") {
		t.Fatalf("a trim outside the file must name the clip: %v", err)
	}
}

func TestBuildMixdownFadesLongerThanTheClipAreClamped(t *testing.T) {
	plan, err := BuildMixdown([]MixdownInput{{Path: "a.mp3", Duration: 4, Clip: config.Clip{FadeIn: f(10), FadeOut: f(10)}}}, "o.mp3", config.MusicFormatMP3)
	if err != nil {
		t.Fatal(err)
	}
	g := graphOf(t, plan)
	if !strings.Contains(g, "afade=t=in:st=0:d=4") || !strings.Contains(g, "afade=t=out:st=0:d=4") {
		t.Fatalf("fades must not exceed the clip: %s", g)
	}
}

// --- real ffmpeg ------------------------------------------------------------

func tone(t *testing.T, dir, name string, hz int, seconds float64, vol float64) string {
	t.Helper()
	p := filepath.Join(dir, name)
	cmd := exec.Command("ffmpeg", "-y", "-v", "error", "-f", "lavfi", "-i",
		"sine=frequency="+strconv.Itoa(hz)+":duration="+num(seconds)+":sample_rate=44100", "-af", "volume="+num(vol*8), "-ac", "2", p) // lavfi sine peaks at 1/8: x8 makes vol 1 full scale
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make tone: %v %s", err, out)
	}
	return p
}

func probeSeconds(t *testing.T, path string) float64 {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	v, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	return v
}

// peakDB returns the highest sample level (dBFS) found in the window.
func peakDB(t *testing.T, path string, from, to float64) float64 {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-v", "error", "-ss", num(from), "-t", num(to-from), "-i", path, "-af",
		"astats=metadata=1:reset=0,ametadata=print:key=lavfi.astats.Overall.Peak_level:file=-", "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("astats: %v %s", err, out)
	}
	last := math.Inf(-1)
	for _, line := range strings.Split(string(out), "\n") {
		if i := strings.Index(line, "Peak_level="); i >= 0 {
			if v, err := strconv.ParseFloat(strings.TrimSpace(line[i+len("Peak_level="):]), 64); err == nil && v > last {
				last = v
			}
		}
	}
	return last
}

func TestMixdownWithRealFFmpegDurationOverlapAndNoClipping(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	loud1 := tone(t, dir, "a.wav", 440, 8, 1.0)
	loud2 := tone(t, dir, "b.wav", 660, 6, 1.0)
	plan, err := BuildMixdown([]MixdownInput{
		{Path: loud1, Duration: 8, Clip: config.Clip{}},
		{Path: loud2, Duration: 6, Clip: config.Clip{Start: f(5), FadeIn: f(1)}},
	}, filepath.Join(dir, "out.mp3"), config.MusicFormatMP3)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("ffmpeg", plan.Args...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	got := probeSeconds(t, filepath.Join(dir, "out.mp3"))
	if math.Abs(got-plan.Seconds) > 0.15 || math.Abs(plan.Seconds-11) > 1e-9 {
		t.Fatalf("duration = %.2f s, planned %.2f s, want 11", got, plan.Seconds)
	}
	// two full-scale tones overlap between 6 s and 8 s: the limiter must hold the ceiling
	if p := peakDB(t, filepath.Join(dir, "out.mp3"), 6, 8); p > -0.2 {
		t.Fatalf("peak in the overlap = %.2f dBFS: the mix clips", p)
	}
}

func TestMixdownWithRealFFmpegDelaysAndTrims(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	a := tone(t, dir, "a.wav", 440, 10, 0.5)
	plan, err := BuildMixdown([]MixdownInput{
		{Path: a, Duration: 10, Clip: config.Clip{Start: f(3), TrimStart: f(2), TrimEnd: f(6)}},
	}, filepath.Join(dir, "o.wav"), config.MusicFormatWAV)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("ffmpeg", plan.Args...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	if got := probeSeconds(t, filepath.Join(dir, "o.wav")); math.Abs(got-7) > 0.1 {
		t.Fatalf("a 4 s clip delayed by 3 s lasts %.2f s, want 7", got)
	}
	if p := peakDB(t, filepath.Join(dir, "o.wav"), 0, 2.5); p > -60 {
		t.Fatalf("the first 3 s must be silent, peak = %.1f dBFS", p)
	}
	if p := peakDB(t, filepath.Join(dir, "o.wav"), 3.5, 6); p < -12 {
		t.Fatalf("the clip must sound after its start, peak = %.1f dBFS", p)
	}
}

func TestMixdownMasterGraph(t *testing.T) {
	in := []MixdownInput{{Path: "a.mp3", Duration: 10, Clip: config.Clip{}}, {Path: "b.mp3", Duration: 10, Clip: config.Clip{}}}
	graph := func(m Master) string {
		plan, err := BuildMixdownMaster(in, "o.mp3", config.MusicFormatMP3, m)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(plan.Args, " ")
	}
	plain := graph(Master{Gain: 1})
	if strings.Contains(plain, "loudnorm") || strings.Contains(plain, "dropout_transition=0,volume") {
		t.Fatalf("no master: %s", plain)
	}
	if got := graph(Master{Gain: 1}); got != plain {
		t.Fatal("BuildMixdown must equal a neutral master")
	}
	g := graph(Master{Gain: 0.5, Normalize: true})
	want := "amix=inputs=2:normalize=0:duration=longest:dropout_transition=0,volume=0.5,loudnorm=I=-14:TP=-1.5:LRA=11,aresample=44100,alimiter="
	if !strings.Contains(g, want) {
		t.Fatalf("graph = %s, want it to contain %s", g, want)
	}
}

// loudnessLUFS is the integrated loudness of a file.
func loudnessLUFS(t *testing.T, path string) float64 {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-v", "info", "-nostats", "-i", path, "-af", "ebur128=peak=true", "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("ebur128: %v %s", err, out)
	}
	text := string(out)
	i := strings.LastIndex(text, "Integrated loudness:")
	if i < 0 {
		t.Fatalf("no loudness in %s", text)
	}
	var v float64
	if _, err := fmt.Sscanf(strings.TrimSpace(strings.SplitN(text[i:], "I:", 2)[1]), "%f", &v); err != nil {
		t.Fatalf("parse: %v in %q", err, text[i:])
	}
	return v
}

func TestMixdownMasterWithRealFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	quiet := tone(t, dir, "q.wav", 440, 12, 0.1)
	render := func(name string, m Master) string {
		out := filepath.Join(dir, name)
		plan, err := BuildMixdownMaster([]MixdownInput{{Path: quiet, Duration: 12, Clip: config.Clip{}}}, out, config.MusicFormatWAV, m)
		if err != nil {
			t.Fatal(err)
		}
		if o, err := exec.Command("ffmpeg", plan.Args...).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg: %v\n%s", err, o)
		}
		return out
	}
	base := render("base.wav", Master{Gain: 1})
	half := render("half.wav", Master{Gain: 0.5})
	if d := peakDB(t, base, 1, 11) - peakDB(t, half, 1, 11); math.Abs(d-6.02) > 0.3 {
		t.Fatalf("a master gain of 0.5 lowers the peak by %.2f dB, want 6", d)
	}
	norm := render("norm.wav", Master{Gain: 1, Normalize: true})
	if got, before := loudnessLUFS(t, norm), loudnessLUFS(t, base); math.Abs(got-config.NormalizeLUFS) > 2 || got <= before {
		t.Fatalf("normalised loudness = %.1f LUFS (was %.1f), want about %d", got, before, config.NormalizeLUFS)
	}
	if got := probeSeconds(t, norm); math.Abs(got-12) > 0.2 {
		t.Fatalf("normalising changed the length: %.2f s", got)
	}
}

package music

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"veritmo/internal/platform/runner"
)

type fakeFFmpeg struct{ measure, loud string }

func (f fakeFFmpeg) Run(_ context.Context, _ string, args []string, _ ...runner.Option) ([]byte, error) {
	if strings.Contains(strings.Join(args, " "), "ebur128") {
		return []byte(f.loud), nil
	}
	return []byte(f.measure), nil
}

func TestParseMeasure(t *testing.T) {
	out := "frame:0 pts:0\nlavfi.astats.Overall.Peak_level=-12.4\nlavfi.astats.Overall.Peak_level=-0.1\nlavfi.astats.Overall.Peak_level=-0.1\n" +
		"lavfi.silence_start=10.5\nlavfi.silence_end=18.0\nlavfi.silence_start=100.2\n"
	peak, sil := parseMeasure(out, 120)
	if peak == nil || *peak != -0.1 {
		t.Fatalf("peak %v", peak)
	}
	if len(sil) != 2 || sil[0] != (Silence{10.5, 18}) || sil[1] != (Silence{100.2, 120}) {
		t.Fatalf("silences %v", sil)
	}
}

func TestParseMeasureSilentFile(t *testing.T) {
	peak, sil := parseMeasure("lavfi.astats.Overall.Peak_level=-inf\n", 30)
	if peak != nil || len(sil) != 0 {
		t.Fatalf("%v %v", peak, sil)
	}
}

func f(v float64) *float64 { return &v }

func TestCheck(t *testing.T) {
	cases := []struct {
		name    string
		a       Analysis
		target  float64
		seconds float64
		want    []string
	}{
		{"healthy", Analysis{PeakDB: f(-1.5), LUFS: f(-14)}, 120, 121, nil},
		{"clipping", Analysis{PeakDB: f(-0.1), LUFS: f(-14)}, 0, 120, []string{IssueClipping}},
		{"silence", Analysis{Silences: []Silence{{50, 60}}}, 0, 120, []string{IssueSilence}},
		{"quiet", Analysis{LUFS: f(-30)}, 0, 120, []string{IssueQuiet}},
		{"loud", Analysis{LUFS: f(-6)}, 0, 120, []string{IssueLoud}},
		{"short", Analysis{}, 120, 80, []string{IssueDuration}},
		{"several", Analysis{PeakDB: f(0), LUFS: f(-5)}, 0, 60, []string{IssueClipping, IssueLoud}},
	}
	for _, c := range cases {
		var got []string
		for _, i := range Check(c.a, c.target, c.seconds) {
			got = append(got, i.Code)
		}
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestAnalyze(t *testing.T) {
	r := fakeFFmpeg{
		measure: "lavfi.astats.Overall.Peak_level=-2.0\nlavfi.silence_start=5\nlavfi.silence_end=12\n",
		loud:    "lavfi.r128.I=-15.04\n",
	}
	a := Analyze(context.Background(), r, "x.mp3", 60)
	if a.PeakDB == nil || *a.PeakDB != -2 || a.LUFS == nil || *a.LUFS != -15 || len(a.Silences) != 1 {
		t.Fatalf("%+v", a)
	}
}

func TestNormalize(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{Base: dir}
	song := filepath.Join(dir, "a.mp3")
	os.WriteFile(song, []byte("old"), 0o644)
	var calls [][]string
	r := runnerFunc(func(args []string) ([]byte, error) {
		calls = append(calls, args)
		if strings.Contains(strings.Join(args, " "), "ebur128") {
			return []byte("lavfi.r128.I=-20.0\n"), nil
		}
		os.WriteFile(filepath.Join(dir, args[len(args)-1]), []byte("new"), 0o644)
		return nil, nil
	})
	from, gain, err := Normalize(context.Background(), r, ws, "a.mp3", "mp3", 192, -14)
	if err != nil || from != -20 || gain != 6 {
		t.Fatalf("from=%v gain=%v err=%v", from, gain, err)
	}
	got, _ := os.ReadFile(song)
	if string(got) != "new" {
		t.Fatalf("file = %q", got)
	}
	enc := strings.Join(calls[1], " ")
	if !strings.Contains(enc, "volume=6.00dB") || !strings.Contains(enc, "alimiter") || !strings.Contains(enc, "libmp3lame -b:a 192k") {
		t.Fatalf("encode args = %s", enc)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.mp3.norm.mp3")); !os.IsNotExist(err) {
		t.Fatal("the temporary file must not stay")
	}
}

func TestNormalizeLeavesAnAlreadyRightSongAndCapsTheGain(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{Base: dir}
	os.WriteFile(filepath.Join(dir, "a.mp3"), []byte("old"), 0o644)
	level := "-14.1"
	n := 0
	r := runnerFunc(func(args []string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "ebur128") {
			return []byte("lavfi.r128.I=" + level + "\n"), nil
		}
		n++
		os.WriteFile(filepath.Join(dir, args[len(args)-1]), []byte("new"), 0o644)
		return nil, nil
	})
	if _, gain, _ := Normalize(context.Background(), r, ws, "a.mp3", "mp3", 0, -14); gain != 0 || n != 0 {
		t.Fatalf("gain %v, encodes %d", gain, n)
	}
	level = "-45.0"
	if _, gain, _ := Normalize(context.Background(), r, ws, "a.mp3", "mp3", 0, -14); gain != MaxLoudnessGainDB {
		t.Fatalf("gain %v", gain)
	}
}

type runnerFunc func(args []string) ([]byte, error)

func (f runnerFunc) Run(_ context.Context, _ string, args []string, _ ...runner.Option) ([]byte, error) {
	return f(args)
}

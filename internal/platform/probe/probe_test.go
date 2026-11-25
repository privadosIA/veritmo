package probe

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"veritmo/internal/platform/runner"
)

func TestBuildArgs(t *testing.T) {
	args := BuildArgs("/tmp/out/output.wav")

	want := []string{
		Flags.LogLevel, "error",
		Flags.ShowEntries, "format=duration",
		Flags.OutputFmt, "json",
		"/tmp/out/output.wav",
	}

	if len(args) != len(want) {
		t.Fatalf("BuildArgs() = %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("BuildArgs()[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}

func TestDurationSuccess(t *testing.T) {
	fr := runner.NewFakeRunner()
	fr.Script(runner.ScriptedResult{
		Stdout: []byte(`{"format": {"duration": "3.500000"}}`),
	})

	got, err := Duration(context.Background(), fr, "/tmp/out/output.wav")
	if err != nil {
		t.Fatalf("Duration() error = %v, want nil", err)
	}

	want := 3500 * time.Millisecond
	if got != want {
		t.Fatalf("Duration() = %v, want %v", got, want)
	}

	calls := fr.Calls()
	if len(calls) != 1 {
		t.Fatalf("got %d invocations, want 1", len(calls))
	}
	if calls[0].Name != "ffprobe" {
		t.Fatalf("invocation name = %q, want %q", calls[0].Name, "ffprobe")
	}
}

func TestDurationPropagatesRunnerError(t *testing.T) {
	fr := runner.NewFakeRunner()
	fr.Script(runner.ScriptedResult{Err: fmt.Errorf("exit status 1 (stderr: no such file)")})

	_, err := Duration(context.Background(), fr, "/missing.wav")
	if err == nil {
		t.Fatal("Duration() error = nil, want non-nil")
	}
}

func TestDurationInvalidJSON(t *testing.T) {
	fr := runner.NewFakeRunner()
	fr.Script(runner.ScriptedResult{Stdout: []byte("not json")})

	_, err := Duration(context.Background(), fr, "/tmp/out/output.wav")
	if err == nil {
		t.Fatal("Duration() error = nil, want non-nil")
	}
}

func TestLoudnessReadsTheLastValueTheFilterPrints(t *testing.T) {
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		return runner.ScriptedResult{Stdout: []byte("frame:0 pts:0 pts_time:0\nlavfi.r128.I=-70.0\nframe:1 pts:1 pts_time:0.1\nlavfi.r128.I=-14.3\nframe:2 pts:2 pts_time:0.2\nlavfi.r128.I=-13.9\n")}
	})
	got, err := Loudness(context.Background(), fr, "a.mp3")
	if err != nil || got != -13.9 {
		t.Fatalf("%v %v", got, err)
	}
	call := fr.Calls()[0]
	if call.Name != "ffmpeg" || call.Args[4] != "a.mp3" {
		t.Fatalf("call = %+v", call)
	}
}

func TestLoudnessOfSilenceOrNothing(t *testing.T) {
	for out, want := range map[string]error{"lavfi.r128.I=-inf\n": ErrSilent, "lavfi.r128.I=-70.0\n": ErrSilent, "": nil} {
		fr := runner.NewFakeRunner()
		fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult { return runner.ScriptedResult{Stdout: []byte(out)} })
		_, err := Loudness(context.Background(), fr, "a.mp3")
		if err == nil || (want != nil && !errors.Is(err, want)) {
			t.Errorf("%q: err = %v", out, err)
		}
	}
}

func TestLoudnessWithRealFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	tone := func(name string, amp float64) string {
		p := filepath.Join(dir, name)
		if out, err := exec.Command("ffmpeg", "-y", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=6:sample_rate=44100", "-af", fmt.Sprintf("volume=%v", amp), "-ac", "2", p).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
		return p
	}
	r := runner.NewExecRunner()
	loud, err := Loudness(context.Background(), r, tone("loud.wav", 8))
	if err != nil {
		t.Fatal(err)
	}
	quiet, err := Loudness(context.Background(), r, tone("quiet.wav", 4))
	if err != nil {
		t.Fatal(err)
	}
	if d := loud - quiet; d < 5.8 || d > 6.2 {
		t.Fatalf("half the amplitude must be 6 dB quieter: %v vs %v", loud, quiet)
	}
	if loud > 0 || loud < -20 {
		t.Fatalf("a loud tone should be around -4 LUFS, got %v", loud)
	}
}

func TestImageSize_ReadsTheWidthAndHeightOfAPicture(t *testing.T) {
	fr := runner.NewFakeRunner()
	fr.Script(runner.ScriptedResult{Stdout: []byte(`{"streams": [{"width": 960, "height": 1367}]}`)})
	w, h, err := ImageSize(context.Background(), fr, "sources/foto.jpg")
	if err != nil || w != 960 || h != 1367 {
		t.Fatalf("got %dx%d, %v", w, h, err)
	}
	args := strings.Join(fr.Calls()[0].Args, " ")
	if !strings.Contains(args, "stream=width,height") || !strings.HasSuffix(args, "sources/foto.jpg") {
		t.Errorf("args = %s", args)
	}
}

func TestImageSize_RefusesAnAnswerWithoutASize(t *testing.T) {
	fr := runner.NewFakeRunner()
	fr.Script(runner.ScriptedResult{Stdout: []byte(`{"streams": []}`)})
	if _, _, err := ImageSize(context.Background(), fr, "x.jpg"); err == nil {
		t.Error("a file without a picture stream must be an error")
	}
}

func TestImageSize_SwapsTheSidesOfAPhotoTurnedAQuarter(t *testing.T) {
	// What ffprobe says of a vertical photo of a phone: stored 4032x3024 and marked to be turned.
	for rotation, want := range map[string][2]int{"-90": {3024, 4032}, "90": {3024, 4032}, "270": {3024, 4032}, "180": {4032, 3024}, "0": {4032, 3024}} {
		fr := runner.NewFakeRunner()
		fr.Script(runner.ScriptedResult{Stdout: []byte(`{"frames": [{"side_data_list": [{}, {"rotation": ` + rotation + `}, {}]}], "streams": [{"width": 4032, "height": 3024}]}`)})
		w, h, err := ImageSize(context.Background(), fr, "sources/movil.jpg")
		if err != nil || w != want[0] || h != want[1] {
			t.Errorf("rotation %s: got %dx%d, %v; want %dx%d", rotation, w, h, err, want[0], want[1])
		}
	}
}

func TestImageSize_AFrameWithoutARotationKeepsTheStreamSize(t *testing.T) {
	fr := runner.NewFakeRunner()
	fr.Script(runner.ScriptedResult{Stdout: []byte(`{"frames": [{}], "streams": [{"width": 1280, "height": 720}]}`)})
	w, h, err := ImageSize(context.Background(), fr, "x.png")
	if err != nil || w != 1280 || h != 720 {
		t.Fatalf("got %dx%d, %v", w, h, err)
	}
}

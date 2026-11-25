package music

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/platform/runner"
)

// separateSetup points the weights at a temp folder (with a file when asked) and fakes the programs: ffmpeg writes its last
// argument, demucs writes its four stems.
func separateSetup(t *testing.T, s *testServer, withWeights bool) *runner.FakeRunner {
	t.Helper()
	dir := t.TempDir()
	s.demucsDir = dir
	if withWeights {
		if err := os.WriteFile(filepath.Join(dir, "ggml-model-htdemucs-4s-f16.bin"), []byte("weights"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s.memAvailable = func() (int64, error) { return 16 << 30, nil }
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		switch inv.Name {
		case "ffmpeg":
			return runner.ScriptedResult{Err: os.WriteFile(inv.Args[len(inv.Args)-1], []byte("WAV"), 0o644)}
		case "/fake/demucs":
			for _, n := range []string{"target_0_drums.wav", "target_1_bass.wav", "target_2_other.wav", "target_3_vocals.wav"} {
				if err := os.WriteFile(filepath.Join(inv.Args[2], n), []byte("WAV"), 0o644); err != nil {
					return runner.ScriptedResult{Err: err}
				}
			}
		}
		return runner.ScriptedResult{}
	})
	s.MediaRunner = fr
	s.Look = func(name string) (string, error) { return "/fake/" + name, nil }
	return fr
}

func TestSeparateReady(t *testing.T) {
	s, _ := newProjectsServer(t)
	separateSetup(t, s, false)
	if _, _, p := s.mHandler.separateReady(); p == nil || p.status != http.StatusServiceUnavailable || !strings.Contains(p.msg, "get-demucs.sh") {
		t.Fatalf("no weights: %+v", p)
	}
	separateSetup(t, s, true)
	s.Look = func(name string) (string, error) {
		if name == "demucs" {
			return "", os.ErrNotExist
		}
		return "/fake/" + name, nil
	}
	if _, _, p := s.mHandler.separateReady(); p == nil || !strings.Contains(p.msg, "get-demucs.sh") {
		t.Fatalf("no program: %+v", p)
	}
	s.Look = func(name string) (string, error) {
		if name == "ffmpeg" {
			return "", os.ErrNotExist
		}
		return "/fake/" + name, nil
	}
	if _, _, p := s.mHandler.separateReady(); p == nil || !strings.Contains(p.msg, "ffmpeg") {
		t.Fatalf("no ffmpeg: %+v", p)
	}
	s.Look = func(name string) (string, error) { return "/fake/" + name, nil }
	bin, model, p := s.mHandler.separateReady()
	if p != nil || bin != "/fake/demucs" || filepath.Base(model) != "ggml-model-htdemucs-4s-f16.bin" {
		t.Fatalf("ready: %q %q %+v", bin, model, p)
	}
}

func TestSeparateFits_SaysHowMuchMemoryIsMissing(t *testing.T) {
	s, _ := newProjectsServer(t)
	separateSetup(t, s, true)
	if p := s.mHandler.separateFits(); p != nil {
		t.Fatalf("16 GB free must be enough: %+v", p)
	}
	s.memAvailable = func() (int64, error) { return 3 << 30, nil }
	p := s.mHandler.separateFits()
	if p == nil || p.status != http.StatusServiceUnavailable || !strings.Contains(p.msg, "voice separator") || !strings.Contains(p.msg, "missing") {
		t.Fatalf("3 GB free must not be enough: %+v", p)
	}
}

func TestSeparateVoice_MakesTheWavWhisperReads(t *testing.T) {
	s, _ := newProjectsServer(t)
	fr := separateSetup(t, s, true)
	dir := t.TempDir()
	got, err := s.mHandler.separateVoice(context.Background(), "/fake/demucs", filepath.Join(s.demucsDir, "ggml-model-htdemucs-4s-f16.bin"), "song.mp3", dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(dir, "vocals16k.wav") {
		t.Errorf("vocals = %q", got)
	}
	// the 44.1 kHz copy and the stems are gone: only the 16 kHz voice stays
	left, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(left) != 1 || left[0] != got {
		t.Errorf("left in the folder = %v, want only %s", left, got)
	}
	var names []string
	for _, c := range fr.Calls() {
		names = append(names, c.Name)
	}
	if strings.Join(names, " ") != "ffmpeg /fake/demucs ffmpeg" {
		t.Errorf("programs run = %v, want ffmpeg, demucs, ffmpeg", names)
	}
}

func TestSeparateVoice_UnreadableAudioIsAnError(t *testing.T) {
	s, _ := newProjectsServer(t)
	fr := separateSetup(t, s, true)
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		return runner.ScriptedResult{Err: os.ErrInvalid}
	})
	if _, err := s.mHandler.separateVoice(context.Background(), "/fake/demucs", "m.bin", "bad.mp3", t.TempDir()); err == nil {
		t.Fatal("ffmpeg failing must fail the separation")
	}
}

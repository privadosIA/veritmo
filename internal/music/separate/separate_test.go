package separate

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"

	"veritmo/internal/platform/runner"
)

// wavBytes makes a mono 16-bit wav of the samples.
func wavBytes(samples []int16, rate int) []byte {
	data := make([]byte, len(samples)*2)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(data[i*2:], uint16(s))
	}
	h := make([]byte, 44)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+len(data)))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], 1)
	binary.LittleEndian.PutUint32(h[24:], uint32(rate))
	binary.LittleEndian.PutUint32(h[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(h[32:], 2)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(len(data)))
	return append(h, data...)
}

// tone is seconds of a sine of the given amplitude (0 is silence) at 16 kHz.
func tone(seconds, amp float64) []float32 {
	n := int(seconds * 16000)
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(amp * math.Sin(2*math.Pi*220*float64(i)/16000))
	}
	return out
}

func cat(parts ...[]float32) []float32 {
	var out []float32
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestEnvelope_FindsTheVoiceBetweenSilences(t *testing.T) {
	// 1 s of silence, 2 s of voice, 1 s of silence
	env := EnvelopeOf(cat(tone(1, 0), tone(2, 0.3), tone(1, 0)), 16000)
	if got := env.Share(1.1, 2.9); got < 0.99 {
		t.Errorf("share of the sung part = %.2f, want ~1", got)
	}
	if got := env.Share(0, 0.9); got > 0.01 {
		t.Errorf("share of the silence = %.2f, want 0", got)
	}
	if got := env.VoicedShare(); math.Abs(got-0.5) > 0.05 {
		t.Errorf("voiced share = %.2f, want ~0.5", got)
	}
	on := env.Onsets()
	if len(on) != 1 || math.Abs(on[0]-1.0) > 0.12 {
		t.Errorf("onsets = %v, want one at ~1.0 s", on)
	}
}

func TestEnvelope_BridgesShortGapsButNotLongOnes(t *testing.T) {
	// a 100 ms gap inside a phrase is bridged; a 400 ms one is a real pause
	env := EnvelopeOf(cat(tone(0.5, 0.3), tone(0.1, 0), tone(0.5, 0.3), tone(1, 0), tone(0.5, 0.3)), 16000)
	if got := len(env.Onsets()); got != 2 {
		t.Errorf("onsets = %d (%v), want 2: the short gap bridged and the long one kept", got, env.Onsets())
	}
	if got := env.Share(0.55, 0.65); got < 0.9 {
		t.Errorf("the bridged gap has share %.2f, want voiced", got)
	}
}

func TestEnvelope_SteadyNoiseIsNotVoice(t *testing.T) {
	// a very quiet hiss (under -45 dBFS) is never voice, whatever its noise floor
	env := EnvelopeOf(tone(3, 0.002), 16000)
	if got := env.VoicedShare(); got != 0 {
		t.Errorf("voiced share of a hiss = %.2f, want 0", got)
	}
}

func TestEnvelope_Empty(t *testing.T) {
	env := EnvelopeOf(nil, 16000)
	if env.VoicedShare() != 0 || env.Share(0, 1) != 0 || len(env.Onsets()) != 0 {
		t.Errorf("an empty envelope must say nothing: %+v", env)
	}
	if (Env{Hop: Hop, Voiced: []bool{true}}).Share(5, 6) != 0 {
		t.Error("a span outside the song has share 0")
	}
}

func TestEnvelopeFile_ReadsAMonoWav(t *testing.T) {
	var s []int16
	for _, v := range cat(tone(0.5, 0), tone(1, 0.3), tone(0.5, 0)) {
		s = append(s, int16(v*32767))
	}
	path := filepath.Join(t.TempDir(), "v.wav")
	if err := os.WriteFile(path, wavBytes(s, 16000), 0o644); err != nil {
		t.Fatal(err)
	}
	env, err := EnvelopeFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := env.Share(0.6, 1.4); got < 0.99 {
		t.Errorf("share = %.2f, want ~1", got)
	}
	if _, err := EnvelopeFile(filepath.Join(t.TempDir(), "none.wav")); err == nil {
		t.Error("a missing file must fail")
	}
	bad := filepath.Join(t.TempDir(), "bad.wav")
	_ = os.WriteFile(bad, []byte("not a wav at all, nope"), 0o644)
	if _, err := EnvelopeFile(bad); err == nil {
		t.Error("a file that is not a wav must fail")
	}
}

func TestLookup(t *testing.T) {
	bin, models := t.TempDir(), t.TempDir()
	if _, _, _, err := Lookup(bin, models); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("nothing installed: err = %v", err)
	}
	// the program, not yet the weights
	exe := filepath.Join(bin, "demucs")
	_ = os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755)
	if _, _, _, err := Lookup(bin, models); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("no weights: err = %v", err)
	}
	_ = os.WriteFile(filepath.Join(models, "ggml-model-htdemucs-6s-f16.bin"), []byte("six"), 0o644)
	_ = os.WriteFile(filepath.Join(models, "ggml-model-htdemucs-4s-f16.bin"), []byte("four"), 0o644)
	_ = os.WriteFile(filepath.Join(models, "notes.txt"), []byte("x"), 0o644)
	gotBin, model, size, err := Lookup(bin, models)
	if err != nil {
		t.Fatal(err)
	}
	if gotBin != exe || filepath.Base(model) != "ggml-model-htdemucs-4s-f16.bin" || size != 4 {
		t.Errorf("Lookup = %q %q %d, want the 4-source weights first", gotBin, model, size)
	}
}

func TestRun_KeepsOnlyTheVocals(t *testing.T) {
	out := filepath.Join(t.TempDir(), "stems")
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		for _, n := range []string{"target_0_drums.wav", "target_1_bass.wav", "target_2_other.wav", "target_3_vocals.wav"} {
			_ = os.WriteFile(filepath.Join(inv.Args[2], n), []byte("RIFF....WAVE"), 0o644)
		}
		return runner.ScriptedResult{}
	})
	got, err := Tool{Runner: fr, Binary: "bin/demucs", Model: "m.bin"}.Run(context.Background(), "in.wav", out)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(out, "target_3_vocals.wav") {
		t.Errorf("vocals = %q", got)
	}
	left, _ := filepath.Glob(filepath.Join(out, "*.wav"))
	if len(left) != 1 {
		t.Errorf("stems left = %v, want only the vocals", left)
	}
	calls := fr.Calls()
	if len(calls) != 1 || calls[0].Name != "bin/demucs" ||
		len(calls[0].Args) != 4 || calls[0].Args[0] != "m.bin" || calls[0].Args[1] != "in.wav" || calls[0].Args[3] != "4" {
		t.Errorf("call = %+v, want demucs <model> <wav> <out> 4", calls)
	}
}

func TestRun_Failures(t *testing.T) {
	out := t.TempDir()
	fr := runner.NewFakeRunner()
	fr.Script(runner.ScriptedResult{Err: errors.New("boom")})
	if _, err := (Tool{Runner: fr, Binary: "d", Model: "m"}).Run(context.Background(), "in.wav", out); err == nil {
		t.Error("a failing demucs must fail")
	}
	fr = runner.NewFakeRunner() // exits 0 and writes nothing
	if _, err := (Tool{Runner: fr, Binary: "d", Model: "m"}).Run(context.Background(), "in.wav", out); err == nil {
		t.Error("no vocal stem must fail")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fr = runner.NewFakeRunner()
	fr.Script(runner.ScriptedResult{Err: errors.New("killed")})
	if _, err := (Tool{Runner: fr, Binary: "d", Model: "m"}).Run(ctx, "in.wav", out); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled run must return the context error, got %v", err)
	}
}

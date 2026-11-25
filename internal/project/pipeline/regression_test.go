package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
)

// legacyAudioHash and legacyImageHash replicate the pre-change hash
// algorithms verbatim, so the golden comparison does not depend on the
// current cache package.
func legacyAudioHash(text string, speed float64, voice string) string {
	h := sha256.New()
	fmt.Fprintf(h, "tts_text=%s\x00", text)
	fmt.Fprintf(h, "speed=%v\x00", speed)
	fmt.Fprintf(h, "voice=%s\x00", voice)
	return hex.EncodeToString(h.Sum(nil))
}

func legacyImageHash(prompt, negativePrompt string, seed int64, model, resolution, sampler string, steps int) string {
	h := sha256.New()
	fmt.Fprintf(h, "prompt=%s\x00", prompt)
	fmt.Fprintf(h, "negative_prompt=%s\x00", negativePrompt)
	fmt.Fprintf(h, "seed=%d\x00", seed)
	fmt.Fprintf(h, "model=%s\x00", model)
	fmt.Fprintf(h, "resolution=%s\x00", resolution)
	fmt.Fprintf(h, "sampler=%s\x00", sampler)
	fmt.Fprintf(h, "steps=%d\x00", steps)
	return hex.EncodeToString(h.Sum(nil))
}

// goldenEvent is one event with the non-deterministic fields stripped.
type goldenEvent struct {
	Type     string
	SceneID  string
	Phase    string
	Artifact string
}

// goldenCall is one subprocess invocation (binary + full argument list).
type goldenCall struct {
	Name string
	Args []string
}

// TestRegressionVideo pins that a scenes.yaml WITHOUT output.mode (legacy
// top-level output_file only, no new field used) drives the exact
// pre-change video pipeline: same phase sequence and event stream, same
// subprocess invocation list (byte-for-byte arguments), same cache hashes,
// and same output path. The golden values below are the pre-change fixture,
// written out from the behaviour of the orchestrator before this change.
func TestRegressionVideo(t *testing.T) {
	chdirTemp(t)
	if err := os.WriteFile("m.safetensors", []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("tmp", 0o755); err != nil {
		t.Fatal(err)
	}

	seed := 42
	cfg := &config.Config{
		Project:    "demo",
		OutputFile: "out/demo.mp4", // legacy alias only — no output block, no mode
		Defaults: config.Defaults{
			Model:      "m.safetensors",
			Voice:      writeVoice(t),
			Resolution: "1280x720",
			Sampler:    "euler",
			Steps:      30,
			FPS:        30,
		},
		Scenes: []config.Scene{
			{ID: "intro", Prompt: "a city", NegativePrompt: "blurry", Seed: &seed, TTSText: "Welcome to the city.", Speed: 1.0},
			{ID: "outro", Prompt: "a sunset", TTSText: "Goodbye.", Speed: 0.5},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cfg.Output.Mode != config.ModeVideo {
		t.Fatalf("absent output.mode must default to video, got %q", cfg.Output.Mode)
	}

	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if inv.Name == "ffprobe" {
			return runner.ScriptedResult{Stdout: []byte(`{"format":{"duration":"2.5"}}`)}
		}
		if inv.Name == "piper" {
			return runner.ScriptedResult{Stdout: []byte("RIFF....WAVE")}
		}
		return runner.ScriptedResult{}
	})

	var buf bytes.Buffer
	emitter := events.New(&buf, true)
	manifest := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}

	final, _ := runVideo(context.Background(), cfg, emitter, fr,
		ttsRunnerAdapter{r: fr}, assembleRunnerAdapter{r: fr},
		manifest, cache.NewOnlyFilter(""), false, nil, "tmp", "", nil, nil)

	// --- Same output path ---
	if final != "out/demo.mp4" {
		t.Errorf("final artifact = %q, want out/demo.mp4", final)
	}

	// --- Same invocation list (pre-change fixture, byte-for-byte) ---
	sceneCalls := func(id, lengthScale, prompt string, negative []string, seedArg, wav, png, mp4 string) []goldenCall {
		sdArgs := []string{"-m", "m.safetensors", "-p", prompt}
		sdArgs = append(sdArgs, negative...)
		sdArgs = append(sdArgs,
			"-s", seedArg,
			"--sampling-method", "euler",
			"--steps", "30",
			"-W", "1280", "-H", "720",
			"-o", png,
		)
		return []goldenCall{
			{"piper", []string{"--length-scale", lengthScale, "--model", "v.onnx", "--output_file", "-"}},
			{"ffprobe", []string{"-v", "error", "-show_entries", "format=duration", "-of", "json", wav}},
			{"sd", sdArgs},
			{"ffmpeg", []string{
				"-y", "-loop", "1", "-framerate", "30",
				"-i", png, "-i", wav,
				"-t", "2.500000",
				"-c:v", "libx264", "-tune", "stillimage", "-pix_fmt", "yuv420p",
				"-c:a", "aac", "-b:a", "192k", "-ar", "44100", "-ac", "2",
				mp4,
			}},
		}
	}
	wantCalls := append(
		sceneCalls("intro", "1", "a city", []string{"-n", "blurry"}, "42",
			"tmp/scene_intro.wav", "tmp/scene_intro.png", "tmp/scene_intro.mp4"),
		sceneCalls("outro", "2", "a sunset", nil, "-1",
			"tmp/scene_outro.wav", "tmp/scene_outro.png", "tmp/scene_outro.mp4")...,
	)
	wantCalls = append(wantCalls, goldenCall{"ffmpeg", []string{
		"-y", "-f", "concat", "-safe", "0", "-i", "tmp/concat_list.txt", "-c:v", "libx264", "-preset", "medium", "-crf", "18", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "192k", "-ar", "44100", "-ac", "2", "-movflags", "+faststart", "out/demo.mp4",
	}})

	calls := fr.Calls()
	if len(calls) != len(wantCalls) {
		t.Fatalf("got %d invocations, want %d", len(calls), len(wantCalls))
	}
	for i, want := range wantCalls {
		if calls[i].Name != want.Name {
			t.Errorf("call %d binary = %q, want %q", i, calls[i].Name, want.Name)
			continue
		}
		if !reflect.DeepEqual(calls[i].Args, want.Args) {
			t.Errorf("call %d (%s) args =\n  %q\nwant\n  %q", i, want.Name, calls[i].Args, want.Args)
		}
	}
	// Narration text still flows over piper's stdin.
	if got := string(calls[0].Stdin); got != "Welcome to the city." {
		t.Errorf("piper stdin = %q", got)
	}

	// --- Same event stream (pre-change fixture, order and fields) ---
	wantEvents := []goldenEvent{
		{"phase_start", "intro", "tts", ""},
		{"phase_end", "intro", "tts", "tmp/scene_intro.wav"},
		{"phase_start", "intro", "probe", ""},
		{"phase_end", "intro", "probe", ""},
		{"phase_start", "intro", "render", ""},
		{"phase_end", "intro", "render", "tmp/scene_intro.png"},
		{"phase_start", "intro", "clip", ""},
		{"phase_end", "intro", "clip", "tmp/scene_intro.mp4"},
		{"phase_start", "outro", "tts", ""},
		{"phase_end", "outro", "tts", "tmp/scene_outro.wav"},
		{"phase_start", "outro", "probe", ""},
		{"phase_end", "outro", "probe", ""},
		{"phase_start", "outro", "render", ""},
		{"phase_end", "outro", "render", "tmp/scene_outro.png"},
		{"phase_start", "outro", "clip", ""},
		{"phase_end", "outro", "clip", "tmp/scene_outro.mp4"},
		{"phase_start", "_global", "concat", ""},
		{"phase_end", "_global", "concat", "out/demo.mp4"},
	}
	var gotEvents []goldenEvent
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var ev events.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("bad event line %q: %v", line, err)
		}
		gotEvents = append(gotEvents, goldenEvent{string(ev.Type), ev.SceneID, ev.Phase, ev.Artifact})
	}
	if !reflect.DeepEqual(gotEvents, wantEvents) {
		t.Errorf("event stream =\n  %v\nwant\n  %v", gotEvents, wantEvents)
	}

	// --- Same cache hashes (pre-change algorithms) ---
	wantHashes := map[string]struct{ audio, image string }{
		"intro": {
			legacyAudioHash("Welcome to the city.", 1.0, "v.onnx"),
			legacyImageHash("a city", "blurry", 42, "m.safetensors", "1280x720", "euler", 30),
		},
		"outro": {
			legacyAudioHash("Goodbye.", 0.5, "v.onnx"),
			legacyImageHash("a sunset", "", -1, "m.safetensors", "1280x720", "euler", 30),
		},
	}
	for id, want := range wantHashes {
		entry, ok := manifest.Scenes[id]
		if !ok {
			t.Errorf("manifest missing scene %q", id)
			continue
		}
		if entry.AudioHash != want.audio {
			t.Errorf("scene %q audio hash = %s, want legacy %s", id, entry.AudioHash, want.audio)
		}
		if entry.ImageHash != want.image {
			t.Errorf("scene %q image hash = %s, want legacy %s", id, entry.ImageHash, want.image)
		}
	}
}

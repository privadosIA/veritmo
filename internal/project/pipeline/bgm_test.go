package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
)

func TestBGMDuration(t *testing.T) {
	for in, want := range map[float64]float64{1: 10, 10: 10, 10.1: 20, 59: 60, 61: 70, 239: 240, 241: 240, 900: 240} {
		if got := BGMDuration(time.Duration(in * float64(time.Second))); got != want {
			t.Errorf("%v s -> %v, want %v", in, got, want)
		}
	}
}

type bgmRun struct {
	fr       *runner.FakeRunner
	events   []events.Event
	manifest *cache.Manifest
	// synthReq is the request the synthesis program was given, when it ran.
	synthReq map[string]any
}

// runBGMProject runs a one-scene video project with the given soundtrack.
func runBGMProject(t *testing.T, bgm *config.BGM, manifest *cache.Manifest, videoSeconds string) bgmRun {
	t.Helper()
	seed := 7
	cfg := &config.Config{
		Project:  "demo",
		Output:   config.Output{File: "out/demo.mp4"},
		BGM:      bgm,
		Defaults: config.Defaults{Model: "m.safetensors", Voice: "v.onnx", Resolution: "1280x720", Sampler: "euler", Steps: 30, FPS: 30},
		Scenes:   []config.Scene{{ID: "talk", Prompt: "a city", TTSText: "Hello.", Speed: 1.0, Seed: &seed}},
	}
	fr := runner.NewFakeRunner()
	var synthReq map[string]any
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		switch inv.Name {
		case "ffprobe":
			return runner.ScriptedResult{Stdout: []byte(`{"format":{"duration":"` + videoSeconds + `"}}`)}
		case "piper":
			return runner.ScriptedResult{Stdout: []byte("RIFF....WAVE")}
		case "ace-synth":
			for i, a := range inv.Args {
				if a == "--request" {
					stem := strings.TrimSuffix(inv.Args[i+1], ".json")
					raw, _ := os.ReadFile(inv.Args[i+1])
					json.Unmarshal(raw, &synthReq)
					os.WriteFile(stem+"0.mp3", []byte("music"), 0o644)
				}
			}
		}
		return runner.ScriptedResult{}
	})
	var buf bytes.Buffer
	if manifest == nil {
		manifest = &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
	}
	final, err := runVideo(context.Background(), cfg, events.New(&buf, true), fr,
		ttsRunnerAdapter{r: fr}, assembleRunnerAdapter{r: fr},
		manifest, cache.NewOnlyFilter(""), false, nil, "tmp", "", nil, nil)
	if err != nil || final != "out/demo.mp4" {
		t.Fatalf("%q %v\n%s", final, err, buf.String())
	}
	var evs []events.Event
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var ev events.Event
		if json.Unmarshal([]byte(line), &ev) == nil {
			evs = append(evs, ev)
		}
	}
	return bgmRun{fr: fr, events: evs, manifest: manifest, synthReq: synthReq}
}

func bgmSetup(t *testing.T) {
	t.Helper()
	chdirTemp(t)
	os.WriteFile("m.safetensors", []byte("stub"), 0o644)
	os.WriteFile("v.onnx", []byte("stub"), 0o644)
	os.WriteFile("v.onnx.json", []byte("{}"), 0o644)
	os.MkdirAll("tmp", 0o755)
	os.MkdirAll("sources", 0o755)
	os.WriteFile(filepath.Join("sources", "theme.mp3"), []byte("x"), 0o644)
}

func (r bgmRun) names() string {
	var n []string
	for _, c := range r.fr.Calls() {
		n = append(n, c.Name)
	}
	return strings.Join(n, " ")
}

func (r bgmRun) globalPhases() string {
	var out []string
	for _, e := range r.events {
		if e.SceneID == "_global" && e.Phase != "" {
			out = append(out, string(e.Type)+":"+e.Phase)
		}
	}
	return strings.Join(out, ",")
}

func lastFFmpeg(t *testing.T, r bgmRun) string {
	t.Helper()
	calls := r.fr.Calls()
	return strings.Join(calls[len(calls)-1].Args, " ")
}

func TestBGM_FileIsMixedAfterTheConcat(t *testing.T) {
	bgmSetup(t)
	r := runBGMProject(t, &config.BGM{File: "theme.mp3"}, nil, "3.0")
	if got := r.names(); got != "piper ffprobe sd ffmpeg ffmpeg ffprobe ffmpeg" {
		t.Fatalf("invocations = %s", got)
	}
	if got := r.globalPhases(); got != "phase_start:concat,phase_end:concat,phase_start:mix,phase_end:mix" {
		t.Fatalf("phases = %s", got)
	}
	concat := strings.Join(r.fr.Calls()[4].Args, " ")
	if !strings.HasSuffix(concat, "tmp/video_nobgm.mp4") {
		t.Fatalf("the joined video is an intermediate: %s", concat)
	}
	mix := lastFFmpeg(t, r)
	for _, want := range []string{"-i tmp/video_nobgm.mp4", "-stream_loop -1 -i sources/theme.mp3", "-c:v copy", "volume=0.25", "loudnorm", "sidechaincompress"} {
		if !strings.Contains(mix, want) {
			t.Errorf("missing %q in %s", want, mix)
		}
	}
	if !strings.HasSuffix(mix, "out/demo.mp4") {
		t.Fatalf("the mix writes the final file: %s", mix)
	}
}

func TestBGM_PromptGeneratesTheSoundtrackOnceAndReusesIt(t *testing.T) {
	bgmSetup(t)
	b := &config.BGM{Prompt: "calm piano"}
	b0 := b
	first := runBGMProject(t, b, nil, "23.0")
	if got := first.names(); got != "piper ffprobe sd ffmpeg ffmpeg ffprobe ace-synth ffmpeg" {
		t.Fatalf("invocations = %s", got)
	}
	if got := first.globalPhases(); got != "phase_start:concat,phase_end:concat,phase_start:bgm,phase_end:bgm,phase_start:mix,phase_end:mix" {
		t.Fatalf("phases = %s", got)
	}
	// Instrumental, the length of the video rounded up to 30 s, no language model.
	synth := first.fr.Calls()[6]
	if synth.Name != "ace-synth" {
		t.Fatal(synth.Name)
	}
	q := first.synthReq
	if q["lyrics"] != "[Instrumental]" || q["duration"] != 30.0 || q["caption"] != "calm piano" {
		t.Fatalf("request = %v", q)
	}
	for _, k := range []string{"lm_model", "lm_seed", "task_type"} {
		if _, ok := q[k]; ok {
			t.Fatalf("the soundtrack request must not carry %s: %v", k, q)
		}
	}
	if q["seed"] != float64(b0.ResolvedSeed()) {
		t.Fatalf("seed = %v, want the one derived from the prompt", q["seed"])
	}
	if !strings.Contains(lastFFmpeg(t, first), "tmp/track_bgm_soundtrack.mp3") {
		t.Fatalf("the mix must use the generated file: %s", lastFFmpeg(t, first))
	}

	// Same prompt, same length class: nothing is generated, the mix still runs.
	second := runBGMProject(t, b, first.manifest, "21.0")
	if strings.Contains(second.names(), "ace-synth") {
		t.Fatalf("soundtrack regenerated: %s", second.names())
	}
	if !strings.Contains(second.globalPhases(), "skipped:bgm") || !strings.Contains(second.globalPhases(), "phase_end:mix") {
		t.Fatalf("phases = %s", second.globalPhases())
	}

	// A new prompt generates again.
	third := runBGMProject(t, &config.BGM{Prompt: "rock"}, second.manifest, "21.0")
	if !strings.Contains(third.names(), "ace-synth") {
		t.Fatal("a new prompt must regenerate")
	}
	// A video that now needs a longer soundtrack generates again.
	fourth := runBGMProject(t, &config.BGM{Prompt: "rock"}, third.manifest, "35.0")
	if !strings.Contains(fourth.names(), "ace-synth") {
		t.Fatal("a longer length class must regenerate")
	}
}

func TestBGM_MixOptionsReachTheFilter(t *testing.T) {
	bgmSetup(t)
	off := false
	vol, fade := 0.6, 0.0
	r := runBGMProject(t, &config.BGM{File: "theme.mp3", Volume: &vol, FadeSeconds: &fade, Ducking: &off}, nil, "3.0")
	mix := lastFFmpeg(t, r)
	if !strings.Contains(mix, "volume=0.6") || strings.Contains(mix, "sidechaincompress") || strings.Contains(mix, "afade") {
		t.Fatalf("%s", mix)
	}
}

func TestBGM_AProjectWithoutItIsUnchanged(t *testing.T) {
	bgmSetup(t)
	r := runBGMProject(t, nil, nil, "3.0")
	if got := r.names(); got != "piper ffprobe sd ffmpeg ffmpeg" {
		t.Fatalf("invocations = %s", got)
	}
	if strings.Contains(r.globalPhases(), "mix") || strings.Contains(r.globalPhases(), "bgm") {
		t.Fatal(r.globalPhases())
	}
	if !strings.HasSuffix(lastFFmpeg(t, r), "out/demo.mp4") || strings.Contains(lastFFmpeg(t, r), "video_nobgm") {
		t.Fatalf("%s", lastFFmpeg(t, r))
	}
}

func TestBGM_PreflightNeedsTheSynthesisProgramOnlyForAGeneratedSoundtrack(t *testing.T) {
	has := func(cfg *config.Config) bool {
		for _, b := range RequiredBinariesFor(cfg, "") {
			if b == "ace-synth" {
				return true
			}
		}
		return false
	}
	video := func(b *config.BGM) *config.Config {
		return &config.Config{Output: config.Output{Mode: config.ModeVideo}, BGM: b}
	}
	if has(video(nil)) || has(video(&config.BGM{File: "a.mp3"})) {
		t.Fatal("not needed without a generated soundtrack")
	}
	if !has(video(&config.BGM{Prompt: "p"})) {
		t.Fatal("needed for a prompt")
	}
}

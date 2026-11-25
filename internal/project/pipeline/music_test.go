package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/music/music"
	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
	"veritmo/internal/project/export"
)

const enrichedJSON = `{"caption":"rich caption","lyrics":"[Verse]\nsung by the model","bpm":96,"duration":30,"keyscale":"G minor","timesignature":"4","vocal_language":"en","seed":%d,"lm_seed":%d,"audio_codes":"1,2,3","inference_steps":8,"output_format":"mp3","synth_model":"acestep-v15-turbo-Q8_0.gguf","lm_model":"acestep-5Hz-lm-1.7B-Q8_0.gguf"}`

type musicEnv struct {
	dir string
	fr  *runner.FakeRunner
	// failSD makes the fake picture program fail (a cover that cannot be made).
	failSD *bool
	// peak is what the fake ffmpeg measures as the peak of a finished song (default -3 dBFS).
	peak *string
}

// newMusicEnv builds a project directory with stub model files and a fake
// engine that behaves like the real programs: it reads the file named by
// --request and writes its outputs next to it, named after its stem.
func newMusicEnv(t *testing.T) *musicEnv {
	t.Helper()
	dir := t.TempDir()
	models := filepath.Join(dir, "models", "music")
	os.MkdirAll(models, 0o755)
	for _, f := range []string{config.DefaultMusicLM, config.DefaultMusicSynth, "Qwen3-Embedding-0.6B-Q8_0.gguf", "vae-BF16.gguf"} {
		os.WriteFile(filepath.Join(models, f), []byte("stub"), 0o644)
	}
	fr := runner.NewFakeRunner()
	failSD := new(bool)
	peak := new(string)
	*peak = "-3.0"
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		switch inv.Name {
		case "ffprobe": // every audio lasts 20 s
			if strings.Contains(inv.Args[len(inv.Args)-1], dirOf(inv)) {
				return runner.ScriptedResult{Err: fmt.Errorf("ffprobe got a path already inside the project dir: %s", inv.Args[len(inv.Args)-1])}
			}
			return runner.ScriptedResult{Stdout: []byte(`{"format":{"duration":"20.000000"}}`)}
		case "ffmpeg": // a mix: the output is the last argument, relative to the project dir like the runner works
			if strings.Contains(strings.Join(inv.Args, " "), "ebur128") { // measuring a mix's loudness
				return runner.ScriptedResult{Stdout: []byte("frame:0 pts:0 pts_time:0\nlavfi.r128.I=-14.23\n")}
			}
			if strings.Contains(strings.Join(inv.Args, " "), "astats") { // the checks of a finished song
				return runner.ScriptedResult{Stdout: []byte("lavfi.astats.Overall.Peak_level=" + *peak + "\nlavfi.silence_start=4\nlavfi.silence_end=12\n")}
			}
			os.WriteFile(filepath.Join(inv.Dir, inv.Args[len(inv.Args)-1]), []byte("MIX:"+strings.Join(inv.Args, " ")), 0o644)
			return runner.ScriptedResult{}
		}
		if inv.Name == "sd" { // a cover: the picture goes where -o says
			if *failSD {
				return runner.ScriptedResult{Err: fmt.Errorf("sd: out of memory")}
			}
			for i, a := range inv.Args {
				if a == "-o" && i+1 < len(inv.Args) {
					os.WriteFile(filepath.Join(inv.Dir, inv.Args[i+1]), []byte("\x89PNGcover"), 0o644)
				}
			}
			return runner.ScriptedResult{}
		}
		in := filepath.Join(inv.Dir, requestArg(inv.Args))
		stem := strings.TrimSuffix(filepath.Base(in), ".json")
		raw, _ := os.ReadFile(in)
		var req map[string]any
		json.Unmarshal(raw, &req)
		switch inv.Name {
		case "ace-lm":
			seed := int64(req["seed"].(float64))
			os.WriteFile(filepath.Join(filepath.Dir(in), stem+"0.json"), []byte(enriched(seed, req["caption"].(string))), 0o644)
		case "ace-synth":
			ext := "mp3"
			if req["output_format"] == "wav16" {
				ext = "wav"
			}
			os.WriteFile(filepath.Join(filepath.Dir(in), stem+"0."+ext), bytes.Repeat([]byte("a"), 16000), 0o644)
		}
		return runner.ScriptedResult{}
	})
	return &musicEnv{dir: dir, fr: fr, failSD: failSD, peak: peak}
}

// enriched is what the fake language model writes: the caption it was given
// enriched, so a different caption gives different content.
// dirOf is the project directory the program runs in.
func dirOf(inv runner.Invocation) string { return inv.Dir }

func enriched(seed int64, caption string) string {
	out := strings.Replace(enrichedJSON, "rich caption", "rich "+caption, 1)
	out = strings.Replace(out, "%d", itoa(seed), 1)
	return strings.Replace(out, "%d", itoa(seed+1000), 1)
}

func itoa(v int64) string { b, _ := json.Marshal(v); return string(b) }

func (e *musicEnv) run(t *testing.T, cfg *config.Config, opts Options) (string, []events.Event, error) {
	t.Helper()
	opts.Dir, opts.KeepTmp, opts.Runner = e.dir, true, e.fr
	n := int64(1000)
	if opts.Rand == nil {
		opts.Rand = func() int64 { n++; return n }
	}
	var buf bytes.Buffer
	out, err := Run(context.Background(), cfg, opts, events.New(&buf, true))
	var evs []events.Event
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var ev events.Event
		if json.Unmarshal([]byte(line), &ev) == nil {
			evs = append(evs, ev)
		}
	}
	return out, evs, err
}

func musicConfig(tracks ...config.Track) *config.Config {
	return &config.Config{Project: "songs", Output: config.Output{Mode: config.ModeMusic}, Tracks: tracks}
}

func phases(evs []events.Event, scene string) []string {
	var out []string
	for _, ev := range evs {
		if ev.SceneID == scene && ev.Phase != "" {
			out = append(out, string(ev.Type)+":"+ev.Phase)
		}
	}
	return out
}

func engineCalls(fr *runner.FakeRunner) []string {
	var names []string
	for _, c := range fr.Calls() {
		if strings.HasPrefix(c.Name, "ace-") { // ffmpeg encodes the mp3s, ffprobe measures: not the engine
			names = append(names, c.Name)
		}
	}
	return names
}

func readMusicManifest(t *testing.T, dir string) export.MusicManifest {
	t.Helper()
	var m export.MusicManifest
	b, err := os.ReadFile(filepath.Join(dir, "out", "songs", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMusic_OwnLyricsSkipComposeAndAreSungVerbatim(t *testing.T) {
	env := newMusicEnv(t)
	cfg := musicConfig(config.Track{ID: "a", Prompt: "folk", Lyrics: "[Verse]\nmy words", VocalLanguage: "es"})
	out, evs, err := env.run(t, cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := engineCalls(env.fr); strings.Join(got, ",") != "ace-synth" {
		t.Fatalf("engine calls = %v", got)
	}
	want := "phase_start:synth,phase_end:synth,phase_start:export,phase_end:export"
	if got := strings.Join(phases(evs, "a"), ","); got != want {
		t.Fatalf("events = %s", got)
	}
	if out != filepath.Join("out", "songs", "manifest.json") {
		t.Fatalf("artifact = %s", out)
	}
	if _, err := os.Stat(filepath.Join(env.dir, "out", "songs", "a.mp3")); err != nil {
		t.Fatal(err)
	}
	e := readMusicManifest(t, env.dir).Tracks[0]
	if e.UseLM || e.LyricsSung != "[Verse]\nmy words" || e.LyricsRequested != e.LyricsSung || e.Caption != "folk" || e.VocalLanguage != "es" {
		t.Fatalf("entry = %+v", e)
	}
	if e.EffectiveSeed != 1001 || e.AudioSeconds <= 0 || e.File != filepath.Join("out", "songs", "a.mp3") {
		t.Fatalf("entry = %+v", e)
	}
	if m := readMusicManifest(t, env.dir); m.Mode != "music" || m.Project != "songs" {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestMusic_CaptionOnlyComposesFirstAndRecordsBothLyrics(t *testing.T) {
	env := newMusicEnv(t)
	cfg := musicConfig(config.Track{ID: "a", Prompt: "lofi beat"})
	_, evs, err := env.run(t, cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(engineCalls(env.fr), ","); got != "ace-lm,ace-synth" {
		t.Fatalf("engine calls = %s", got)
	}
	want := "phase_start:compose,phase_end:compose,phase_start:synth,phase_end:synth,phase_start:export,phase_end:export"
	if got := strings.Join(phases(evs, "a"), ","); got != want {
		t.Fatalf("events = %s", got)
	}
	e := readMusicManifest(t, env.dir).Tracks[0]
	if !e.UseLM || e.Caption != "rich lofi beat" || e.LyricsSung != "[Verse]\nsung by the model" || e.LyricsRequested != "" {
		t.Fatalf("entry = %+v", e)
	}
	if e.BPM != 96 || e.KeyScale != "G minor" || e.LMSeed == 0 || e.LMModel != config.DefaultMusicLM {
		t.Fatalf("entry = %+v", e)
	}
	if _, leaked := e.Params["audio_codes"]; leaked {
		t.Fatal("audio codes must not be in the manifest")
	}
}

func TestMusic_UnchangedRunIsFullyCached(t *testing.T) {
	env := newMusicEnv(t)
	cfg := musicConfig(config.Track{ID: "a", Prompt: "lofi", Lyrics: "x"}, config.Track{ID: "b", Prompt: "jazz"})
	seed := 5
	cfg.Tracks[0].Seed, cfg.Tracks[1].Seed = &seed, &seed
	if _, _, err := env.run(t, cfg, Options{}); err != nil {
		t.Fatal(err)
	}
	env.fr.Reset()
	env.fr = newMusicEnv(t).fr // fresh recorder with the same fake engine behaviour
	_, evs, err := env.run(t, cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(env.fr.Calls()) != 0 {
		t.Fatalf("a cached run must not call the engine: %v", engineCalls(env.fr))
	}
	if got := strings.Join(phases(evs, "b"), ","); got != "skipped:compose,skipped:synth,phase_start:export,phase_end:export" {
		t.Fatalf("events = %s", got)
	}
}

func TestMusic_SynthesisOnlyChangeKeepsCompose(t *testing.T) {
	env := newMusicEnv(t)
	seed := 5
	cfg := musicConfig(config.Track{ID: "a", Prompt: "jazz", Seed: &seed})
	if _, _, err := env.run(t, cfg, Options{}); err != nil {
		t.Fatal(err)
	}
	env.fr = newMusicEnv(t).fr
	cfg.Defaults.Music = &config.Music{InferenceSteps: 20}
	_, evs, err := env.run(t, cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(engineCalls(env.fr), ","); got != "ace-synth" {
		t.Fatalf("engine calls = %s, the LM must not run again", got)
	}
	if got := strings.Join(phases(evs, "a"), ","); !strings.HasPrefix(got, "skipped:compose,phase_start:synth") {
		t.Fatalf("events = %s", got)
	}
	m := readMusicManifest(t, env.dir)
	if m.Tracks[0].Params["inference_steps"].(float64) != 20 {
		t.Fatalf("new steps not applied: %v", m.Tracks[0].Params)
	}
	if m.Tracks[0].Caption != "rich jazz" {
		t.Fatal("the cached composition must be reused")
	}
}

func TestMusic_ChangingTheStylePrefixRerunsTheTracks(t *testing.T) {
	env := newMusicEnv(t)
	seed := 5
	cfg := musicConfig(config.Track{ID: "a", Prompt: "jazz", Seed: &seed})
	if _, _, err := env.run(t, cfg, Options{}); err != nil {
		t.Fatal(err)
	}
	// nothing changed: fully cached
	env.fr = newMusicEnv(t).fr
	env.run(t, cfg, Options{})
	if len(env.fr.Calls()) != 0 {
		t.Fatalf("unchanged project must be cached, got %v", engineCalls(env.fr))
	}
	// a prefix changes the caption, so compose and synth run again
	cfg.Defaults.Music = &config.Music{StylePrefix: "warm analog"}
	env.fr = newMusicEnv(t).fr
	if _, _, err := env.run(t, cfg, Options{}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(engineCalls(env.fr), ","); got != "ace-lm,ace-synth" {
		t.Fatalf("engine calls = %s", got)
	}
	m := readMusicManifest(t, env.dir)
	if !strings.Contains(m.Tracks[0].Caption, "warm analog") {
		t.Fatalf("manifest caption = %q, must record the composed caption", m.Tracks[0].Caption)
	}
}

func TestMusic_CaptionChangeRunsBothPhases(t *testing.T) {
	env := newMusicEnv(t)
	seed := 5
	cfg := musicConfig(config.Track{ID: "a", Prompt: "jazz", Seed: &seed})
	env.run(t, cfg, Options{})
	env.fr = newMusicEnv(t).fr
	cfg.Tracks[0].Prompt = "blues"
	if _, _, err := env.run(t, cfg, Options{}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(engineCalls(env.fr), ","); got != "ace-lm,ace-synth" {
		t.Fatalf("engine calls = %s", got)
	}
}

func TestMusic_VariantsUseConsecutiveSeedsAndSuffixedFiles(t *testing.T) {
	env := newMusicEnv(t)
	seed, two := 100, 2
	cfg := musicConfig(config.Track{ID: "a", Prompt: "p", Lyrics: "x", Seed: &seed, Variants: &two})
	_, evs, err := env.run(t, cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	m := readMusicManifest(t, env.dir)
	if len(m.Tracks) != 2 || m.Tracks[0].EffectiveSeed != 100 || m.Tracks[1].EffectiveSeed != 101 {
		t.Fatalf("tracks = %+v", m.Tracks)
	}
	for _, f := range []string{"a_v1.mp3", "a_v2.mp3"} {
		if _, err := os.Stat(filepath.Join(env.dir, "out", "songs", f)); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(strings.Join(phases(evs, "a"), ","), "phase_start:synth"); n != 1 {
		t.Fatalf("one synth event pair per track, got %d", n)
	}
	if len(engineCalls(env.fr)) != 2 {
		t.Fatalf("one engine run per variant, got %d", len(engineCalls(env.fr)))
	}
}

func TestMusic_KeepingOneVariantReusesItsSongAndRemovesTheOthers(t *testing.T) {
	env := newMusicEnv(t)
	seed, two, one := 100, 2, 1
	cfg := musicConfig(config.Track{ID: "a", Prompt: "p", Lyrics: "x", Seed: &seed, Variants: &two}, config.Track{ID: "b", Prompt: "q", Lyrics: "y", Seed: &seed})
	if _, _, err := env.run(t, cfg, Options{}); err != nil {
		t.Fatal(err)
	}
	engineRuns := len(engineCalls(env.fr))
	out := filepath.Join(env.dir, "out", "songs")

	// keep the second variant: its seed (101) as the track's seed, one variant
	kept := 101
	cfg.Tracks[0].Seed, cfg.Tracks[0].Variants = &kept, &one
	if _, _, err := env.run(t, cfg, Options{}); err != nil {
		t.Fatal(err)
	}
	if got := len(engineCalls(env.fr)); got != engineRuns {
		t.Fatalf("the kept song must come from the cache, but the engine ran %d more time(s)", got-engineRuns)
	}
	for f, want := range map[string]bool{"a.mp3": true, "a_v1.mp3": false, "a_v2.mp3": false, "b.mp3": true} {
		if _, err := os.Stat(filepath.Join(out, f)); (err == nil) != want {
			t.Errorf("%s exists = %v, want %v", f, err == nil, want)
		}
	}
	if m := readMusicManifest(t, env.dir); len(m.Tracks) != 2 || m.Tracks[0].File != "out/songs/a.mp3" || m.Tracks[0].EffectiveSeed != 101 {
		t.Fatalf("manifest = %+v", m.Tracks)
	}
}

func TestMusic_KeepingOneVariantOfALanguageModelTrackDoesNotComposeOrSynthesizeAgain(t *testing.T) {
	env := newMusicEnv(t)
	seed, two, one := 100, 2, 1
	cfg := musicConfig(config.Track{ID: "a", Prompt: "lofi beat", Seed: &seed, Variants: &two})
	if _, _, err := env.run(t, cfg, Options{}); err != nil {
		t.Fatal(err)
	}
	before := len(engineCalls(env.fr))
	kept := 101
	cfg.Tracks[0].Seed, cfg.Tracks[0].Variants = &kept, &one
	_, evs, err := env.run(t, cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(engineCalls(env.fr)); got != before {
		t.Fatalf("the engine ran %d more time(s): %v", got-before, engineCalls(env.fr)[before:])
	}
	if got := strings.Join(phases(evs, "a"), ","); got != "skipped:compose,skipped:synth,phase_start:export,phase_end:export" {
		t.Fatalf("events = %s", got)
	}
	if _, err := os.Stat(filepath.Join(env.dir, "out", "songs", "a.mp3")); err != nil {
		t.Fatal(err)
	}
}

func TestMusic_OnlyOneTrackNeverRemovesTheSongsOfTheOthers(t *testing.T) {
	env := newMusicEnv(t)
	seed, two, one := 100, 2, 1
	cfg := musicConfig(config.Track{ID: "a", Prompt: "p", Lyrics: "x", Seed: &seed, Variants: &two}, config.Track{ID: "b", Prompt: "q", Lyrics: "y", Seed: &seed, Variants: &two})
	if _, _, err := env.run(t, cfg, Options{}); err != nil {
		t.Fatal(err)
	}
	cfg.Tracks[0].Variants, cfg.Tracks[1].Variants = &one, &one // both changed, but only "a" is run
	if _, _, err := env.run(t, cfg, Options{Only: "a"}); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(env.dir, "out", "songs")
	for f, want := range map[string]bool{"a.mp3": true, "a_v1.mp3": false, "b_v1.mp3": true, "b_v2.mp3": true} {
		if _, err := os.Stat(filepath.Join(out, f)); (err == nil) != want {
			t.Errorf("%s exists = %v, want %v", f, err == nil, want)
		}
	}
}

func TestMusic_WavFormatUsesTheEngineNameAndExtension(t *testing.T) {
	env := newMusicEnv(t)
	cfg := musicConfig(config.Track{ID: "a", Prompt: "p", Lyrics: "x"})
	cfg.Defaults.Music = &config.Music{OutputFormat: "wav"}
	if _, _, err := env.run(t, cfg, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(env.dir, "out", "songs", "a.wav")); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(env.fr.Calls()[0].Args, " "), "wav16") {
		t.Fatal("the format belongs in the request, not the arguments")
	}
}

func TestMusic_OnlyProcessesOneTrackAndForceBypassesCache(t *testing.T) {
	env := newMusicEnv(t)
	cfg := musicConfig(config.Track{ID: "a", Prompt: "p", Lyrics: "x"}, config.Track{ID: "b", Prompt: "q", Lyrics: "y"})
	if _, _, err := env.run(t, cfg, Options{Only: "b"}); err != nil {
		t.Fatal(err)
	}
	if len(engineCalls(env.fr)) != 1 {
		t.Fatalf("calls = %v", engineCalls(env.fr))
	}
	if _, err := os.Stat(filepath.Join(env.dir, "out", "songs", "a.mp3")); err == nil {
		t.Fatal("track a must not be exported")
	}
	// nothing was exported for a before, so the manifest has only b
	m := readMusicManifest(t, env.dir)
	if len(m.Tracks) != 1 || m.Tracks[0].TrackID != "b" {
		t.Fatalf("manifest = %+v", m.Tracks)
	}
	seed := 1
	cfg.Tracks[1].Seed = &seed
	env.run(t, cfg, Options{Only: "b"})
	env.fr = newMusicEnv(t).fr
	env.run(t, cfg, Options{Only: "b", Force: true})
	if len(engineCalls(env.fr)) != 1 {
		t.Fatalf("--force must run the track again: %v", engineCalls(env.fr))
	}
}

// exportAll runs a full two-track project and returns its config.
func exportAll(t *testing.T, env *musicEnv) *config.Config {
	t.Helper()
	sa, sb := 10, 20
	cfg := musicConfig(
		config.Track{ID: "a", Prompt: "p", Lyrics: "x", Seed: &sa},
		config.Track{ID: "b", Prompt: "q", Lyrics: "y", Seed: &sb},
	)
	if _, _, err := env.run(t, cfg, Options{}); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func trackSeeds(m export.MusicManifest) []string {
	var out []string
	for _, e := range m.Tracks {
		out = append(out, e.TrackID+":"+itoa(e.EffectiveSeed))
	}
	return out
}

func TestMusic_OnlyKeepsTheOtherTracksInTheManifest(t *testing.T) {
	env := newMusicEnv(t)
	cfg := exportAll(t, env)
	newSeed := 99
	cfg.Tracks[1].Seed = &newSeed
	if _, _, err := env.run(t, cfg, Options{Only: "b"}); err != nil {
		t.Fatal(err)
	}
	m := readMusicManifest(t, env.dir)
	if got := strings.Join(trackSeeds(m), ","); got != "a:10,b:99" {
		t.Fatalf("manifest = %s, want a untouched and b regenerated, in project order", got)
	}
	// and a's audio was not touched
	if _, err := os.Stat(filepath.Join(env.dir, "out", "songs", "a.mp3")); err != nil {
		t.Fatal(err)
	}
}

func TestMusic_OnlyKeepsVariantsOfTheOtherTracksInOrder(t *testing.T) {
	env := newMusicEnv(t)
	sa, sb, three := 10, 20, 3
	cfg := musicConfig(
		config.Track{ID: "a", Prompt: "p", Lyrics: "x", Seed: &sa, Variants: &three},
		config.Track{ID: "b", Prompt: "q", Lyrics: "y", Seed: &sb},
	)
	if _, _, err := env.run(t, cfg, Options{}); err != nil {
		t.Fatal(err)
	}
	env.fr = newMusicEnv(t).fr
	if _, _, err := env.run(t, cfg, Options{Only: "b", Force: true}); err != nil {
		t.Fatal(err)
	}
	m := readMusicManifest(t, env.dir)
	if got := strings.Join(trackSeeds(m), ","); got != "a:10,a:11,a:12,b:20" {
		t.Fatalf("manifest = %s", got)
	}
	for i, e := range m.Tracks[:3] {
		if e.Variant != i {
			t.Fatalf("variants out of order: %+v", m.Tracks)
		}
	}
}

func TestMusic_OnlyDropsEntriesOfTracksThatAreGone(t *testing.T) {
	env := newMusicEnv(t)
	cfg := exportAll(t, env)
	cfg.Tracks = cfg.Tracks[1:] // a was removed from the project
	if _, _, err := env.run(t, cfg, Options{Only: "b"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(trackSeeds(readMusicManifest(t, env.dir)), ","); got != "b:20" {
		t.Fatalf("manifest = %s", got)
	}
}

func TestMusic_OnlyDropsEntriesWhoseAudioIsGone(t *testing.T) {
	env := newMusicEnv(t)
	cfg := exportAll(t, env)
	if err := os.Remove(filepath.Join(env.dir, "out", "songs", "a.mp3")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := env.run(t, cfg, Options{Only: "b", Force: true}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(trackSeeds(readMusicManifest(t, env.dir)), ","); got != "b:20" {
		t.Fatalf("manifest = %s, a's file is gone so it must not be claimed", got)
	}
}

func TestMusic_AnUnreadablePreviousManifestIsIgnored(t *testing.T) {
	for name, content := range map[string]string{
		"garbage":       "{not json",
		"images mode":   `{"project":"songs","mode":"images","images":[]}`,
		"empty content": "",
	} {
		t.Run(name, func(t *testing.T) {
			env := newMusicEnv(t)
			cfg := exportAll(t, env)
			path := filepath.Join(env.dir, "out", "songs", "manifest.json")
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, _, err := env.run(t, cfg, Options{Only: "b", Force: true}); err != nil {
				t.Fatalf("an old file must not fail the run: %v", err)
			}
			if got := strings.Join(trackSeeds(readMusicManifest(t, env.dir)), ","); got != "b:20" {
				t.Fatalf("manifest = %s", got)
			}
		})
	}
}

func TestMusic_AFullRunRewritesTheManifestFromScratch(t *testing.T) {
	env := newMusicEnv(t)
	cfg := exportAll(t, env)
	cfg.Tracks = cfg.Tracks[1:] // a removed; no --only
	if _, _, err := env.run(t, cfg, Options{}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(trackSeeds(readMusicManifest(t, env.dir)), ","); got != "b:20" {
		t.Fatalf("manifest = %s", got)
	}
}

func TestMusic_EngineFailureStopsTheRunWithAnErrorEvent(t *testing.T) {
	env := newMusicEnv(t)
	env.fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		return runner.ScriptedResult{Err: os.ErrInvalid}
	})
	cfg := musicConfig(config.Track{ID: "a", Prompt: "p", Lyrics: "x"})
	_, evs, err := env.run(t, cfg, Options{})
	if err == nil {
		t.Fatal("expected an error")
	}
	var sawError bool
	for _, ev := range evs {
		if ev.Type == events.TypeError && ev.SceneID == "a" && strings.Contains(ev.Message, "synth failed") {
			sawError = true
		}
	}
	if !sawError {
		t.Fatalf("no synth error event: %+v", evs)
	}
}

func TestMusic_TwoGPUsRunTracksAndKeepProjectOrder(t *testing.T) {
	env := newMusicEnv(t)
	cfg := musicConfig(config.Track{ID: "a", Prompt: "p", Lyrics: "x"}, config.Track{ID: "b", Prompt: "q", Lyrics: "y"})
	_, _, err := env.run(t, cfg, Options{GPU: "0,1", VRAMWarnMB: 1, MemAvailable: func() (int64, error) { return 64 << 30, nil }})
	if err != nil {
		t.Fatal(err)
	}
	gpus := map[string]bool{}
	for _, c := range env.fr.Calls() {
		for _, e := range c.Env {
			if strings.HasPrefix(e, "CUDA_VISIBLE_DEVICES=") {
				gpus[e] = true
			}
		}
	}
	if len(gpus) == 0 {
		t.Fatal("tracks must be pinned to a GPU")
	}
	m := readMusicManifest(t, env.dir)
	if m.Tracks[0].TrackID != "a" || m.Tracks[1].TrackID != "b" {
		t.Fatalf("manifest order = %+v", m.Tracks)
	}
}

func TestMusic_PreflightNeedsAceLMOnlyForComposedTracks(t *testing.T) {
	own := musicConfig(config.Track{ID: "a", Prompt: "p", Lyrics: "x"})
	cap := musicConfig(config.Track{ID: "a", Prompt: "p"})
	only := func(cfg *config.Config, id string) []string { return RequiredBinariesFor(cfg, id) }
	if got := strings.Join(only(own, ""), ","); got != "ace-synth" {
		t.Fatalf("own lyrics: %s", got)
	}
	if got := strings.Join(only(cap, ""), ","); got != "ace-synth,ace-lm" {
		t.Fatalf("caption only: %s", got)
	}
	mixed := musicConfig(config.Track{ID: "a", Prompt: "p", Lyrics: "x"}, config.Track{ID: "b", Prompt: "p"})
	if got := strings.Join(only(mixed, "a"), ","); got != "ace-synth" {
		t.Fatalf("--only a: %s", got)
	}
	look := func(bin string) (string, error) {
		if bin == "ace-synth" {
			return "/bin/ace-synth", nil
		}
		return "", os.ErrNotExist
	}
	if err := PreflightFor(own, "", look); err != nil {
		t.Fatalf("own lyrics need only ace-synth: %v", err)
	}
	if err := PreflightFor(cap, "", look); err == nil || !strings.Contains(err.Error(), "ace-lm") {
		t.Fatalf("err = %v", err)
	}
	for _, bin := range RequiredBinaries(config.ModeMusic, true) {
		if bin == "sd" || bin == "piper" || bin == "ffmpeg" || bin == "ffprobe" {
			t.Fatalf("music must not need %s", bin)
		}
	}
}

func TestMusic_OnlyUnknownTrackAndModeOverride(t *testing.T) {
	cfg := musicConfig(config.Track{ID: "a", Prompt: "p"})
	if err := CheckOnlyScene(cfg, "nope"); err == nil || !strings.Contains(err.Error(), `track "nope"`) {
		t.Fatalf("err = %v", err)
	}
	if err := CheckOnlyScene(cfg, "a"); err != nil {
		t.Fatal(err)
	}
	v := &config.Config{}
	if err := ApplyModeOverride(v, "music"); err != nil || v.Output.Mode != "music" {
		t.Fatalf("%v %q", err, v.Output.Mode)
	}
	if err := ApplyModeOverride(v, "audio"); err == nil || !strings.Contains(err.Error(), `"music"`) {
		t.Fatalf("err = %v", err)
	}
	if plan, concat := PhasePlan(config.ModeMusic); strings.Join(plan, ",") != "compose,synth,export" || concat {
		t.Fatalf("plan = %v %v", plan, concat)
	}
}

func TestMusic_OversizedReservationsRunAloneWithoutDeadlock(t *testing.T) {
	env := newMusicEnv(t)
	cfg := musicConfig(config.Track{ID: "a", Prompt: "p"}, config.Track{ID: "b", Prompt: "q"})
	// Less memory than the budget's safety margin: every reservation is
	// larger than the whole budget, so processes must run alone, one at a
	// time, and the run still has to finish.
	_, _, err := env.run(t, cfg, Options{GPU: "0,1", VRAMWarnMB: 1, MemAvailable: func() (int64, error) { return 1 << 30, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(engineCalls(env.fr)); got != 4 {
		t.Fatalf("expected a compose and a synth per track, got %d engine runs", got)
	}
}

// requestArg returns the value of --request among the engine's arguments,
// wherever the other flags put it.
func requestArg(args []string) string {
	for i, a := range args {
		if a == "--request" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func installAdapter(t *testing.T, env *musicEnv, name string, size int) {
	t.Helper()
	dir := filepath.Join(env.dir, "models", "music", "adapters")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMusic_AnAdapterReachesOnlyTheSynthesisProcess(t *testing.T) {
	env := newMusicEnv(t)
	installAdapter(t, env, "style.safetensors", 100)
	half := 0.5
	seed := 5
	cfg := musicConfig(config.Track{ID: "a", Prompt: "jazz", Seed: &seed}) // no lyrics: the language model runs too
	cfg.Defaults.Music = &config.Music{Adapter: "style.safetensors", AdapterScale: &half}
	if _, _, err := env.run(t, cfg, Options{}); err != nil {
		t.Fatal(err)
	}
	var calls []runner.Invocation
	for _, c := range env.fr.Calls() {
		if strings.HasPrefix(c.Name, "ace-") {
			calls = append(calls, c)
		}
	}
	if len(calls) != 2 || calls[0].Name != "ace-lm" || calls[1].Name != "ace-synth" {
		t.Fatalf("calls = %v", engineCalls(env.fr))
	}
	if strings.Contains(strings.Join(calls[0].Args, " "), "--adapters") {
		t.Errorf("the language model must not receive the adapters directory: %v", calls[0].Args)
	}
	synthArgs := strings.Join(calls[1].Args, " ")
	if !strings.Contains(synthArgs, "--adapters models/music/adapters") {
		t.Errorf("synthesis must receive the adapters directory: %s", synthArgs)
	}
	e := readMusicManifest(t, env.dir).Tracks[0]
	if e.Params["adapter"] != "style.safetensors" || e.Params["adapter_scale"].(float64) != 0.5 {
		t.Fatalf("the manifest must record the adapter among its parameters: %v", e.Params)
	}
}

func TestMusic_NoAdapterMeansNoAdaptersArgument(t *testing.T) {
	env := newMusicEnv(t)
	cfg := musicConfig(config.Track{ID: "a", Prompt: "p", Lyrics: "x"})
	if _, _, err := env.run(t, cfg, Options{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(env.fr.Calls()[0].Args, " "), "--adapters") {
		t.Fatalf("args = %v", env.fr.Calls()[0].Args)
	}
}

func TestMusic_ChangingTheAdapterReSynthesizesWithoutComposingAgain(t *testing.T) {
	env := newMusicEnv(t)
	installAdapter(t, env, "one.safetensors", 100)
	installAdapter(t, env, "two.safetensors", 100)
	seed := 5
	cfg := musicConfig(config.Track{ID: "a", Prompt: "jazz", Seed: &seed})
	cfg.Defaults.Music = &config.Music{Adapter: "one.safetensors"}
	if _, _, err := env.run(t, cfg, Options{}); err != nil {
		t.Fatal(err)
	}
	env.fr = newMusicEnv(t).fr
	cfg.Defaults.Music = &config.Music{Adapter: "two.safetensors"}
	if _, _, err := env.run(t, cfg, Options{}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(engineCalls(env.fr), ","); got != "ace-synth" {
		t.Fatalf("engine calls = %s: another adapter re-synthesizes and the composition is reused", got)
	}
}

func TestMusic_ChangingTheAdapterStrengthReSynthesizes(t *testing.T) {
	env := newMusicEnv(t)
	installAdapter(t, env, "one.safetensors", 100)
	seed := 5
	cfg := musicConfig(config.Track{ID: "a", Prompt: "p", Lyrics: "x", Seed: &seed})
	cfg.Defaults.Music = &config.Music{Adapter: "one.safetensors"}
	env.run(t, cfg, Options{})
	env.fr = newMusicEnv(t).fr
	env.run(t, cfg, Options{})
	if len(env.fr.Calls()) != 0 {
		t.Fatal("unchanged: must be cached")
	}
	weak := 0.3
	cfg.Tracks[0].AdapterScale = &weak
	env.run(t, cfg, Options{})
	if got := strings.Join(engineCalls(env.fr), ","); got != "ace-synth" {
		t.Fatalf("engine calls = %s", got)
	}
}

func TestMusic_ReplacingTheAdapterFilesReSynthesizes(t *testing.T) {
	env := newMusicEnv(t)
	installAdapter(t, env, "one.safetensors", 100)
	seed := 5
	cfg := musicConfig(config.Track{ID: "a", Prompt: "p", Lyrics: "x", Seed: &seed})
	cfg.Defaults.Music = &config.Music{Adapter: "one.safetensors"}
	env.run(t, cfg, Options{})
	env.fr = newMusicEnv(t).fr
	installAdapter(t, env, "one.safetensors", 250) // same name, other file
	env.run(t, cfg, Options{})
	if got := strings.Join(engineCalls(env.fr), ","); got != "ace-synth" {
		t.Fatalf("engine calls = %s", got)
	}
}

func installSource(t *testing.T, env *musicEnv, name, content string) {
	t.Helper()
	os.MkdirAll(filepath.Join(env.dir, "sources"), 0o755)
	if err := os.WriteFile(filepath.Join(env.dir, "sources", name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func argAfter(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestMusic_ACoverSkipsComposeAndGivesTheEngineItsSource(t *testing.T) {
	env := newMusicEnv(t)
	installSource(t, env, "base.mp3", "original")
	seed, strength := 5, 0.6
	cfg := musicConfig(config.Track{ID: "c", Prompt: "jazz piano", Seed: &seed, Task: config.TaskCover, Source: "base.mp3", CoverStrength: &strength})
	_, evs, err := env.run(t, cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(engineCalls(env.fr), ","); got != "ace-synth" {
		t.Fatalf("engine calls = %s: a cover never composes", got)
	}
	call := env.fr.Calls()[0]
	if argAfter(call.Args, "--src-audio") != "sources/base.mp3" || argAfter(call.Args, "--ref-audio") != "" {
		t.Fatalf("args = %v", call.Args)
	}
	for _, p := range phases(evs, "c") {
		if strings.HasSuffix(p, ":compose") {
			t.Fatalf("compose event for a cover: %v", phases(evs, "c"))
		}
	}
	m := readMusicManifest(t, env.dir)
	e := m.Tracks[0]
	if e.Task != "cover" || e.Source != "base.mp3" || e.UseLM || e.Params["task_type"] != "cover" || e.Params["audio_cover_strength"] != 0.6 {
		t.Fatalf("%+v", e)
	}
}

func TestMusic_ACloseCoverAlsoGivesTheSourceAsReference(t *testing.T) {
	env := newMusicEnv(t)
	installSource(t, env, "base.mp3", "original")
	cfg := musicConfig(config.Track{ID: "c", Prompt: "p", Task: config.TaskCoverNoFSQ, Source: "base.mp3"})
	if _, _, err := env.run(t, cfg, Options{}); err != nil {
		t.Fatal(err)
	}
	call := env.fr.Calls()[0]
	if argAfter(call.Args, "--src-audio") != "sources/base.mp3" || argAfter(call.Args, "--ref-audio") != "sources/base.mp3" {
		t.Fatalf("args = %v", call.Args)
	}
}

func TestMusic_ChangingTheSourceContentReSynthesizes(t *testing.T) {
	env := newMusicEnv(t)
	installSource(t, env, "base.mp3", "original")
	seed := 5
	cfg := musicConfig(config.Track{ID: "c", Prompt: "p", Seed: &seed, Task: config.TaskCover, Source: "base.mp3"})
	env.run(t, cfg, Options{})
	env.fr = newMusicEnv(t).fr
	env.run(t, cfg, Options{})
	if len(env.fr.Calls()) != 0 {
		t.Fatal("unchanged cover must be cached")
	}
	installSource(t, env, "base.mp3", "other content") // same name
	env.run(t, cfg, Options{})
	if got := strings.Join(engineCalls(env.fr), ","); got != "ace-synth" {
		t.Fatalf("engine calls = %s", got)
	}
}

func TestMusic_ChangingTheRepaintRegionReSynthesizes(t *testing.T) {
	env := newMusicEnv(t)
	installSource(t, env, "base.mp3", "original")
	seed, a, b := 5, 10.0, 20.0
	cfg := musicConfig(config.Track{ID: "r", Prompt: "p", Seed: &seed, Task: config.TaskRepaint, Source: "base.mp3", RepaintStart: &a, RepaintEnd: &b})
	env.run(t, cfg, Options{})
	env.fr = newMusicEnv(t).fr
	c := 25.0
	cfg.Tracks[0].RepaintEnd = &c
	env.run(t, cfg, Options{})
	if got := strings.Join(engineCalls(env.fr), ","); got != "ace-synth" {
		t.Fatalf("engine calls = %s", got)
	}
	e := readMusicManifest(t, env.dir).Tracks[0]
	if e.Params["repainting_start"] != 10.0 || e.Params["repainting_end"] != 25.0 {
		t.Fatalf("%v", e.Params)
	}
}

func TestMusic_NewSongsKeepTheirHashAndHaveNoTaskInTheManifest(t *testing.T) {
	env := newMusicEnv(t)
	seed := 5
	cfg := musicConfig(config.Track{ID: "a", Prompt: "p", Lyrics: "x", Seed: &seed})
	env.run(t, cfg, Options{})
	e := readMusicManifest(t, env.dir).Tracks[0]
	if e.Task != "" || e.Source != "" {
		t.Fatalf("%+v", e)
	}
	if _, ok := e.Params["task_type"]; ok {
		t.Fatalf("a new song must not carry task params: %v", e.Params)
	}
}

func TestMusic_AMissingSourceFailsTheTrack(t *testing.T) {
	env := newMusicEnv(t)
	seed := 5
	cfg := musicConfig(config.Track{ID: "c", Prompt: "p", Seed: &seed, Task: config.TaskCover, Source: "gone.mp3"})
	_, _, err := env.run(t, cfg, Options{})
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestMusic_AReferenceGoesToSynthOnlyAndItsContentIsCached(t *testing.T) {
	env := newMusicEnv(t)
	installSource(t, env, "sound.mp3", "timbre one")
	seed := 5
	cfg := musicConfig(config.Track{ID: "a", Prompt: "p", Seed: &seed, Reference: "sound.mp3"})
	if _, _, err := env.run(t, cfg, Options{}); err != nil {
		t.Fatal(err)
	}
	calls := env.fr.Calls()
	if got := strings.Join(engineCalls(env.fr), ","); got != "ace-lm,ace-synth" {
		t.Fatalf("engine calls = %s", got)
	}
	if lm := strings.Join(calls[0].Args, " "); strings.Contains(lm, "ref-audio") {
		t.Fatalf("the language model never gets the reference: %s", lm)
	}
	if argAfter(calls[1].Args, "--ref-audio") != "sources/sound.mp3" || argAfter(calls[1].Args, "--src-audio") != "" {
		t.Fatalf("args = %v", calls[1].Args)
	}
	if e := readMusicManifest(t, env.dir).Tracks[0]; e.Reference != "sound.mp3" || e.Task != "" {
		t.Fatalf("%+v", e)
	}

	env.fr = newMusicEnv(t).fr
	env.run(t, cfg, Options{})
	if len(env.fr.Calls()) != 0 {
		t.Fatal("unchanged must be cached")
	}
	installSource(t, env, "sound.mp3", "timbre two")
	env.run(t, cfg, Options{})
	if got := strings.Join(engineCalls(env.fr), ","); got != "ace-synth" {
		t.Fatalf("a new reference re-synthesizes without composing: %s", got)
	}
}

func TestMusic_DeducedLanguageIsPartOfTheComposeKey(t *testing.T) {
	useLM := true
	cfg := musicConfig(config.Track{ID: "a", Prompt: "folk", UseLM: &useLM,
		Lyrics: "El amor que siento por ti no se acaba en la noche, mi corazón es tuyo"})
	tr := cfg.Tracks[0]
	seed := int64(5)
	es := ComposeParamsOf(music.BuildRequest(cfg, tr, seed), tr)
	if es.VocalLanguage != "es" {
		t.Fatalf("deduced language = %q, want es", es.VocalLanguage)
	}
	// same everything, but the language is fixed to English instead: another key
	tr.VocalLanguage = "en"
	en := ComposeParamsOf(music.BuildRequest(cfg, tr, seed), tr)
	if cache.ComposeHash(es) == cache.ComposeHash(en) {
		t.Fatal("changing the language must change the compose key")
	}
	if again := ComposeParamsOf(music.BuildRequest(cfg, cfg.Tracks[0], seed), cfg.Tracks[0]); cache.ComposeHash(again) != cache.ComposeHash(es) {
		t.Fatal("the same track must keep the same key")
	}
}

func TestMusic_ChecksAreRecordedAndProblemsWarned(t *testing.T) {
	env := newMusicEnv(t)
	*env.peak = "-0.1"
	cfg := musicConfig(config.Track{ID: "a", Prompt: "lofi", Lyrics: "x"})
	_, evs, err := env.run(t, cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	c := readMusicManifest(t, env.dir).Tracks[0].Check
	if c == nil || c.PeakDB == nil || *c.PeakDB != -0.1 || c.LUFS == nil || *c.LUFS != -14.2 {
		t.Fatalf("check = %+v", c)
	}
	var codes []string
	for _, i := range c.Issues {
		codes = append(codes, i.Code)
	}
	if got := strings.Join(codes, ","); got != "clipping,silence" {
		t.Fatalf("issues = %s", got)
	}
	warns := 0
	for _, ev := range evs {
		if ev.Type == events.TypeWarn && strings.Contains(ev.Message, "dBFS") {
			warns++
		}
	}
	if warns != 1 {
		t.Fatalf("clipping warnings = %d", warns)
	}
}

func TestMusic_LoudnessIsEvenedAtExportAndRecorded(t *testing.T) {
	env := newMusicEnv(t)
	cfg := musicConfig(config.Track{ID: "a", Prompt: "lofi", Lyrics: "x"})
	target := -20.0
	if cfg.Defaults.Music == nil {
		cfg.Defaults.Music = &config.Music{}
	}
	cfg.Defaults.Music.LoudnessLUFS = &target
	if _, _, err := env.run(t, cfg, Options{}); err != nil {
		t.Fatal(err)
	}
	e := readMusicManifest(t, env.dir).Tracks[0]
	if e.LoudnessFrom == nil || *e.LoudnessFrom != -14.2 || e.LoudnessGain == nil || *e.LoudnessGain != -5.8 {
		t.Fatalf("from=%v gain=%v", e.LoudnessFrom, e.LoudnessGain)
	}
	data, err := os.ReadFile(filepath.Join(env.dir, e.File))
	if err != nil || !strings.Contains(string(data), "volume=-5.77dB") {
		t.Fatalf("the exported file was not re-encoded with the gain: %v %q", err, data)
	}
	// the cached synthesis stays as the engine made it
	if _, err := os.Stat(filepath.Join(env.dir, e.File+".norm.mp3")); !os.IsNotExist(err) {
		t.Fatal("temporary file left behind")
	}
}

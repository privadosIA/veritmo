package music

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
)

func f64(v float64) *float64 { return &v }
func intp(v int) *int        { return &v }

func cfgWith(m *config.Music) *config.Config {
	return &config.Config{Project: "songs", Output: config.Output{Mode: config.ModeMusic}, Defaults: config.Defaults{Music: m}}
}

func TestBuildRequestOwnLyricsSendsOnlyDefinedFields(t *testing.T) {
	tr := config.Track{ID: "a", Prompt: "calm piano", Lyrics: "[Verse]\nhola", VocalLanguage: "es"}
	got, _ := json.Marshal(BuildRequest(cfgWith(&config.Music{}), tr, 7))
	want := `{"caption":"calm piano","lyrics":"[Verse]\nhola","vocal_language":"es","seed":7,"lm_seed":7,"output_format":"mp3","synth_model":"acestep-v15-xl-turbo-Q8_0.gguf"}`
	if string(got) != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestBuildRequestCaptionOnlyNamesTheLanguageModelAndParameters(t *testing.T) {
	tr := config.Track{ID: "a", Prompt: "lofi", DurationSeconds: f64(30), BPM: intp(90), KeyScale: "C minor", TimeSignature: "4"}
	m := &config.Music{InferenceSteps: 8, GuidanceScale: f64(1), Shift: f64(3), Solver: "euler", LMTopK: intp(0),
		LMTemperature: f64(0.7), OutputFormat: "wav", VocalLanguage: "en", MP3Bitrate: 192, LMNegativePrompt: "noise"}
	req := BuildRequest(cfgWith(m), tr, 1)
	if req.LMModel != config.DefaultMusicLM {
		t.Fatalf("lm_model = %q", req.LMModel)
	}
	if req.OutputFormat != "wav16" {
		t.Fatalf("output_format = %q, the engine rejects plain wav", req.OutputFormat)
	}
	if req.VocalLanguage != "en" || req.BPM != 90 || req.Duration != 30 || req.KeyScale != "C minor" || req.TimeSignature != "4" {
		t.Fatalf("%+v", req)
	}
	if req.LMTopK == nil || *req.LMTopK != 0 {
		t.Fatal("an explicit zero must survive")
	}
	raw, _ := json.Marshal(req)
	for _, k := range []string{"lm_temperature", "lm_negative_prompt", "mp3_bitrate", "inference_steps", "shift", "guidance_scale", "solver"} {
		if !strings.Contains(string(raw), `"`+k+`"`) {
			t.Errorf("missing %s in %s", k, raw)
		}
	}
}

func TestBuildRequestInstrumentalUsesTheEngineTag(t *testing.T) {
	tr := config.Track{ID: "a", Prompt: "lofi", Instrumental: true, Lyrics: "ignored", VocalLanguage: "es"}
	req := BuildRequest(cfgWith(&config.Music{}), tr, 1)
	if req.Lyrics != "[Instrumental]" || req.VocalLanguage != "" {
		t.Fatalf("lyrics %q lang %q", req.Lyrics, req.VocalLanguage)
	}
}

func TestBuildRequestOwnLyricsDoNotNameTheLanguageModel(t *testing.T) {
	req := BuildRequest(cfgWith(nil), config.Track{ID: "a", Prompt: "p", Lyrics: "x"}, 1)
	if req.LMModel != "" {
		t.Fatalf("lm_model = %q", req.LMModel)
	}
}

func TestBuildRequestPutsTheStylePrefixInFrontOfEveryCaption(t *testing.T) {
	m := &config.Music{StylePrefix: "  warm analog production "}
	for _, tr := range []config.Track{
		{ID: "a", Prompt: "piano ballad", Lyrics: "[Verse]\nla"},
		{ID: "b", Prompt: "lofi beat", Instrumental: true},
	} {
		req := BuildRequest(cfgWith(m), tr, 1)
		if want := "warm analog production, " + tr.Prompt; req.Caption != want {
			t.Errorf("%s: caption = %q, want %q", tr.ID, req.Caption, want)
		}
	}
}

func TestBuildRequestStylePrefixNeverTouchesTheLyrics(t *testing.T) {
	tr := config.Track{ID: "a", Prompt: "ballad", Lyrics: "[Verse]\nhola"}
	req := BuildRequest(cfgWith(&config.Music{StylePrefix: "warm"}), tr, 1)
	if req.Lyrics != tr.Lyrics {
		t.Fatalf("lyrics = %q", req.Lyrics)
	}
}

func TestBuildRequestBlankStylePrefixChangesNothing(t *testing.T) {
	tr := config.Track{ID: "a", Prompt: "ballad", Lyrics: "x"}
	plain, _ := json.Marshal(BuildRequest(cfgWith(&config.Music{}), tr, 1))
	for _, prefix := range []string{"", "   ", "\n"} {
		got, _ := json.Marshal(BuildRequest(cfgWith(&config.Music{StylePrefix: prefix}), tr, 1))
		if string(got) != string(plain) {
			t.Errorf("prefix %q changed the request:\n%s\n%s", prefix, got, plain)
		}
	}
	if got, _ := json.Marshal(BuildRequest(cfgWith(nil), tr, 1)); string(got) != string(plain) {
		t.Error("no music block must give the same request")
	}
}

func TestVariantSeeds(t *testing.T) {
	seed := 100
	got := VariantSeeds(config.Track{Seed: &seed}, 3, func() int64 { t.Fatal("rng must not be used"); return 0 })
	if got[0] != 100 || got[1] != 101 || got[2] != 102 {
		t.Fatalf("%v", got)
	}
	n := int64(0)
	rnd := VariantSeeds(config.Track{}, 2, func() int64 { n += 10; return n })
	if rnd[0] != 10 || rnd[1] != 20 {
		t.Fatalf("%v", rnd)
	}
	zero := 0
	if z := VariantSeeds(config.Track{Seed: &zero}, 2, nil); z[0] != 0 || z[1] != 1 {
		t.Fatalf("explicit zero seed: %v", z)
	}
}

// fakeEngine imitates the real programs: it reads the input named by
// --request and writes the outputs next to it, named after its stem.
func fakeEngine(t *testing.T, ws Workspace, enriched string) *runner.FakeRunner {
	t.Helper()
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		in := requestArg(inv.Args)
		dir, stem := filepath.Dir(ws.abs(in)), strings.TrimSuffix(filepath.Base(in), ".json")
		switch inv.Name {
		case BinaryLM:
			os.WriteFile(filepath.Join(dir, stem+"0.json"), []byte(enriched), 0o644)
		case BinarySynth:
			var req Request
			b, _ := os.ReadFile(ws.abs(in))
			json.Unmarshal(b, &req)
			ext := "mp3"
			if req.OutputFormat == "wav16" {
				ext = "wav"
			}
			os.WriteFile(filepath.Join(dir, stem+"0."+ext), []byte("audio"), 0o644)
		}
		return runner.ScriptedResult{}
	})
	return fr
}

func TestComposeRunsAceLMAndReturnsTheEnrichedRequest(t *testing.T) {
	ws := Workspace{Base: t.TempDir()}
	fr := fakeEngine(t, ws, `{"caption":"rich","lyrics":"la","bpm":90}`)
	req := Request{Caption: "p", LMModel: "lm.gguf"}
	out, err := Compose(context.Background(), fr, ws, "models/music", "tmp/track_a.work", req, []string{"CUDA_VISIBLE_DEVICES=1"})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"caption":"rich","lyrics":"la","bpm":90}` {
		t.Fatalf("out = %s", out)
	}
	call := fr.Calls()[0]
	want := []string{"--models", "models/music", "--request", "tmp/track_a.work/req.json"}
	if call.Name != "ace-lm" || strings.Join(call.Args, " ") != strings.Join(want, " ") {
		t.Fatalf("call = %s %v", call.Name, call.Args)
	}
	if len(call.Env) != 1 || call.Env[0] != "CUDA_VISIBLE_DEVICES=1" {
		t.Fatalf("env = %v", call.Env)
	}
	if _, err := os.Stat(ws.abs("tmp/track_a.work")); !os.IsNotExist(err) {
		t.Fatal("scratch directory must be removed")
	}
}

func TestComposeEngineFailureIsReported(t *testing.T) {
	ws := Workspace{Base: t.TempDir()}
	fr := runner.NewFakeRunner()
	fr.Script(runner.ScriptedResult{Err: errors.New("exit status 1 (stderr: lm_model not found in registry)")})
	_, err := Compose(context.Background(), fr, ws, "m", "tmp/w", Request{Caption: "p"}, nil)
	if err == nil || !strings.Contains(err.Error(), "not found in registry") {
		t.Fatalf("err = %v", err)
	}
}

func TestComposeMissingOutputIsAnError(t *testing.T) {
	ws := Workspace{Base: t.TempDir()}
	_, err := Compose(context.Background(), runner.NewFakeRunner(), ws, "m", "tmp/w", Request{Caption: "p"}, nil)
	if err == nil || !strings.Contains(err.Error(), "did not write") {
		t.Fatalf("err = %v", err)
	}
}

func TestSynthMovesTheAudioToItsCachePath(t *testing.T) {
	for _, tc := range []struct{ format, engineFmt string }{{"mp3", "mp3"}, {"wav", "wav16"}} {
		ws := Workspace{Base: t.TempDir()}
		fr := fakeEngine(t, ws, "")
		req, _ := json.Marshal(Request{Caption: "p", OutputFormat: tc.engineFmt})
		dst := "tmp/track_a." + tc.format
		if err := Synth(context.Background(), fr, ws, "models/music", "", "tmp/track_a.work", req, tc.format, dst, nil, nil); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(ws.abs(dst)); string(b) != "audio" {
			t.Fatalf("%s: %q", tc.format, b)
		}
		if fr.Calls()[0].Name != "ace-synth" {
			t.Fatal("wrong program")
		}
	}
}

func TestSynthRequestOverlaysCurrentSettingsOnTheEnrichedRequest(t *testing.T) {
	enriched := []byte(`{"caption":"rich","lyrics":"la","audio_codes":"1,2,3","seed":5,"lm_seed":9,"inference_steps":8,"shift":3.0,"solver":"euler","output_format":"mp3"}`)
	base := Request{Caption: "p", InferenceSteps: 20, Shift: f64(1), OutputFormat: "wav16", SynthModel: "dit.gguf"}
	out, err := SynthRequest(enriched, base)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	json.Unmarshal(out, &doc)
	if doc["inference_steps"].(float64) != 20 || doc["shift"].(float64) != 1 || doc["output_format"] != "wav16" || doc["synth_model"] != "dit.gguf" {
		t.Fatalf("overlay: %s", out)
	}
	if _, ok := doc["solver"]; ok {
		t.Fatal("a setting no longer set must fall back to the engine default")
	}
	if doc["audio_codes"] != "1,2,3" || doc["lyrics"] != "la" || doc["caption"] != "rich" || doc["lm_seed"].(float64) != 9 {
		t.Fatalf("composed content lost: %s", out)
	}
	again, _ := SynthRequest(enriched, base)
	if string(again) != string(out) {
		t.Fatal("not deterministic")
	}
}

func TestSynthRequestWithoutLanguageModelIsTheBaseRequest(t *testing.T) {
	out, err := SynthRequest(nil, Request{Caption: "p", Lyrics: "x"})
	if err != nil || !strings.Contains(string(out), `"lyrics": "x"`) {
		t.Fatalf("%s %v", out, err)
	}
}

func TestParseResolvedAndParams(t *testing.T) {
	b := []byte(`{"caption":"c","lyrics":"l","bpm":120,"duration":30.5,"keyscale":"D major","timesignature":"4","vocal_language":"es","seed":3,"lm_seed":4,"audio_codes":"1,2","inference_steps":8}`)
	r, err := ParseResolved(b)
	if err != nil || r.BPM != 120 || r.Duration != 30.5 || r.KeyScale != "D major" || r.Seed != 3 || r.LMSeed != 4 || r.Lyrics != "l" {
		t.Fatalf("%+v %v", r, err)
	}
	p, _ := Params(b)
	if _, ok := p["audio_codes"]; ok {
		t.Fatal("audio codes must not be copied to the manifest")
	}
	if _, ok := p["lyrics"]; ok {
		t.Fatal("lyrics are recorded apart")
	}
	if p["inference_steps"].(float64) != 8 {
		t.Fatalf("%v", p)
	}
}

func TestAudioSeconds(t *testing.T) {
	dir := t.TempDir()
	mp3 := filepath.Join(dir, "a.mp3")
	os.WriteFile(mp3, make([]byte, 477696), 0o644)
	if s, _ := AudioSeconds(mp3, "mp3", 0); s < 29.8 || s > 29.9 {
		t.Fatalf("mp3 seconds = %v", s)
	}
	wav := filepath.Join(dir, "a.wav")
	os.WriteFile(wav, make([]byte, 44+48000*4*2), 0o644)
	if s, _ := AudioSeconds(wav, "wav", 0); s != 2 {
		t.Fatalf("wav seconds = %v", s)
	}
	if _, err := AudioSeconds(filepath.Join(dir, "none"), "mp3", 0); err == nil {
		t.Fatal("missing file must fail")
	}
}

func TestBuildRequestNamesTheResolvedAdapter(t *testing.T) {
	half := 0.5
	cfg := cfgWith(&config.Music{Adapter: "style.safetensors", AdapterScale: &half})
	req := BuildRequest(cfg, config.Track{ID: "a", Prompt: "p", Lyrics: "x"}, 1)
	if req.Adapter != "style.safetensors" || req.AdapterScale == nil || *req.AdapterScale != 0.5 {
		t.Fatalf("%+v", req)
	}
	raw, _ := json.Marshal(req)
	if !strings.Contains(string(raw), `"adapter":"style.safetensors"`) || !strings.Contains(string(raw), `"adapter_scale":0.5`) {
		t.Fatalf("%s", raw)
	}
	own := BuildRequest(cfg, config.Track{ID: "b", Prompt: "p", Lyrics: "x", Adapter: "chinese-new-year"}, 1)
	if own.Adapter != "chinese-new-year" {
		t.Fatalf("track adapter must win: %+v", own)
	}
}

func TestBuildRequestWithoutAdapterIsUnchanged(t *testing.T) {
	tr := config.Track{ID: "a", Prompt: "p", Lyrics: "x"}
	raw, _ := json.Marshal(BuildRequest(cfgWith(&config.Music{}), tr, 1))
	if strings.Contains(string(raw), "adapter") {
		t.Fatalf("no adapter must add no field: %s", raw)
	}
}

func TestSynthRequestAppliesTheCurrentAdapterOverTheComposedRequest(t *testing.T) {
	enriched := []byte(`{"caption":"rich","lyrics":"la","audio_codes":"1,2","adapter":"old","adapter_scale":1}`)
	half := 0.5
	out, err := SynthRequest(enriched, Request{Caption: "p", Adapter: "new", AdapterScale: &half})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	json.Unmarshal(out, &doc)
	if doc["adapter"] != "new" || doc["adapter_scale"].(float64) != 0.5 || doc["audio_codes"] != "1,2" {
		t.Fatalf("%s", out)
	}
	// and when the adapter is dropped from the project, it leaves the request too
	out, _ = SynthRequest(enriched, Request{Caption: "p"})
	doc = map[string]any{}
	json.Unmarshal(out, &doc)
	if _, ok := doc["adapter"]; ok {
		t.Fatalf("a removed adapter must not linger in the composed request: %s", out)
	}
}

func TestSynthGivesTheProgramTheAdaptersDirectoryOnlyWhenAskedTo(t *testing.T) {
	ws := Workspace{Base: t.TempDir()}
	fr := fakeEngine(t, ws, "")
	req, _ := json.Marshal(Request{Caption: "p", OutputFormat: "mp3", Adapter: "style"})
	if err := Synth(context.Background(), fr, ws, "models/music", "models/music/adapters", "tmp/w1", req, "mp3", "tmp/a.mp3", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(fr.Calls()[0].Args, " "); got != "--models models/music --adapters models/music/adapters --request tmp/w1/req.json" {
		t.Fatalf("args = %s", got)
	}
	plain, _ := json.Marshal(Request{Caption: "p", OutputFormat: "mp3"})
	if err := Synth(context.Background(), fr, ws, "models/music", "", "tmp/w2", plain, "mp3", "tmp/b.mp3", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(fr.Calls()[1].Args, " "); got != "--models models/music --request tmp/w2/req.json" {
		t.Fatalf("a song without adapter must not change its arguments: %s", got)
	}
}

func TestAdapterBytes(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{Base: dir}
	os.MkdirAll(filepath.Join(dir, "ad", "folder"), 0o755)
	os.WriteFile(filepath.Join(dir, "ad", "single.safetensors"), make([]byte, 100), 0o644)
	os.WriteFile(filepath.Join(dir, "ad", "folder", "adapter_model.safetensors"), make([]byte, 300), 0o644)
	os.WriteFile(filepath.Join(dir, "ad", "folder", "adapter_config.json"), make([]byte, 7), 0o644)
	if got := AdapterBytes(ws, "ad", "single.safetensors"); got != 100 {
		t.Errorf("file: %d", got)
	}
	if got := AdapterBytes(ws, "ad", "folder"); got != 307 {
		t.Errorf("folder: %d", got)
	}
	if AdapterBytes(ws, "ad", "missing") != 0 || AdapterBytes(ws, "ad", "") != 0 {
		t.Error("a missing or unnamed adapter counts as 0")
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

func TestBuildRequestOfATaskCarriesItsFieldsAndNoDuration(t *testing.T) {
	cfg := &config.Config{Output: config.Output{Mode: config.ModeMusic}}
	start, end, strength := 10.0, 25.0, 0.6
	rep := BuildRequest(cfg, config.Track{ID: "a", Prompt: "p", Task: config.TaskRepaint, Source: "b.mp3", RepaintStart: &start, RepaintEnd: &end}, 1)
	if rep.TaskType != "repaint" || *rep.RepaintStart != 10 || *rep.RepaintEnd != 25 || rep.Duration != 0 || rep.LMModel != "" {
		t.Fatalf("%+v", rep)
	}
	cov := BuildRequest(cfg, config.Track{ID: "a", Prompt: "p", Task: config.TaskCover, Source: "b.mp3", CoverStrength: &strength}, 1)
	b, _ := json.Marshal(cov)
	if !strings.Contains(string(b), `"task_type":"cover"`) || !strings.Contains(string(b), `"audio_cover_strength":0.6`) || strings.Contains(string(b), "repainting") {
		t.Fatalf("%s", b)
	}
	ext := BuildRequest(cfg, config.Track{ID: "a", Prompt: "p", Task: config.TaskExtract, Source: "b.mp3", Stem: "vocals"}, 1)
	if ext.Track != "vocals" || ext.TaskType != "extract" {
		t.Fatalf("%+v", ext)
	}
	plain, _ := json.Marshal(BuildRequest(cfg, config.Track{ID: "a", Prompt: "p"}, 1))
	if strings.Contains(string(plain), "task_type") || strings.Contains(string(plain), `"track"`) {
		t.Fatalf("a new song must not carry task fields: %s", plain)
	}
}

func TestSynthGivesTheProgramTheSourceOfATask(t *testing.T) {
	ws := Workspace{Base: t.TempDir()}
	fr := fakeEngine(t, ws, "")
	req, _ := json.Marshal(Request{Caption: "p", OutputFormat: "mp3", TaskType: "cover"})
	src := SourceOf(config.Track{Task: config.TaskCover, Source: "base.mp3"})
	if err := Synth(context.Background(), fr, ws, "models/music", "", "tmp/w1", req, "mp3", "tmp/a.mp3", nil, src); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(fr.Calls()[0].Args, " "); got != "--models models/music --src-audio sources/base.mp3 --request tmp/w1/req.json" {
		t.Fatalf("args = %s", got)
	}
	close := SourceOf(config.Track{Task: config.TaskCoverNoFSQ, Source: "base.mp3"})
	if err := Synth(context.Background(), fr, ws, "models/music", "", "tmp/w2", req, "mp3", "tmp/b.mp3", nil, close); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(fr.Calls()[1].Args, " "); got != "--models models/music --src-audio sources/base.mp3 --ref-audio sources/base.mp3 --request tmp/w2/req.json" {
		t.Fatalf("args = %s", got)
	}
	if SourceOf(config.Track{}) != nil {
		t.Fatal("a new song has no source")
	}
}

func TestSourceOfAReferenceIsTimbreOnly(t *testing.T) {
	src := SourceOf(config.Track{Reference: "sound.mp3"})
	if src == nil || src.Path != "" || src.Ref != "sources/sound.mp3" {
		t.Fatalf("%+v", src)
	}
	ws := Workspace{Base: t.TempDir()}
	fr := fakeEngine(t, ws, "")
	req, _ := json.Marshal(Request{Caption: "p", OutputFormat: "mp3"})
	if err := Synth(context.Background(), fr, ws, "models/music", "", "tmp/w", req, "mp3", "tmp/a.mp3", nil, src); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(fr.Calls()[0].Args, " "); got != "--models models/music --ref-audio sources/sound.mp3 --request tmp/w/req.json" {
		t.Fatalf("args = %s", got)
	}
	if SourceOf(config.Track{Task: config.TaskCover, Source: "b.mp3"}).Ref != "" {
		t.Fatal("a plain cover has no reference")
	}
}

func TestBuildRequestDeducesTheLanguageFromTheLyrics(t *testing.T) {
	es := "[Verse]\nEl amor que siento por ti no se acaba en la noche, mi corazón es tuyo"
	got := BuildRequest(cfgWith(&config.Music{}), config.Track{ID: "a", Prompt: "pop", Lyrics: es}, 1)
	if got.VocalLanguage != "es" {
		t.Fatalf("vocal_language = %q, want es", got.VocalLanguage)
	}
}

func TestBuildRequestWithoutLyricsSingsEnglishNeverLeavesItEmpty(t *testing.T) {
	// a Spanish style prompt must not decide the language: English by default
	got := BuildRequest(cfgWith(&config.Music{}), config.Track{ID: "a", Prompt: "Balada pop con piano y voz cálida"}, 1)
	if got.VocalLanguage != "en" {
		t.Fatalf("vocal_language = %q, want en", got.VocalLanguage)
	}
	raw, _ := json.Marshal(got)
	if !strings.Contains(string(raw), `"vocal_language":"en"`) {
		t.Fatalf("the request must name the language: %s", raw)
	}
}

func TestBuildRequestExplicitLanguageBeatsTheDeducedOne(t *testing.T) {
	es := "El amor que siento por ti no se acaba en la noche, mi corazón es tuyo"
	cases := []struct {
		name string
		m    *config.Music
		tr   config.Track
	}{
		{"track", &config.Music{}, config.Track{ID: "a", Prompt: "p", Lyrics: es, VocalLanguage: "fr"}},
		{"defaults", &config.Music{VocalLanguage: "fr"}, config.Track{ID: "a", Prompt: "p", Lyrics: es}},
		{"track over defaults", &config.Music{VocalLanguage: "de"}, config.Track{ID: "a", Prompt: "p", Lyrics: es, VocalLanguage: "fr"}},
	}
	for _, c := range cases {
		if got := BuildRequest(cfgWith(c.m), c.tr, 1).VocalLanguage; got != "fr" {
			t.Errorf("%s: vocal_language = %q, want fr", c.name, got)
		}
	}
}

func TestBuildRequestTaskTracksKeepTheirLanguageFieldsAsBefore(t *testing.T) {
	tr := config.Track{ID: "a", Prompt: "p", Task: "cover", Lyrics: "El amor que siento por ti no se acaba en la noche"}
	if got := BuildRequest(cfgWith(&config.Music{}), tr, 1).VocalLanguage; got != "" {
		t.Fatalf("a task track must not get a deduced language, got %q", got)
	}
}

func TestBuildRequestAsksForTheChosenVoiceAtTheEndOfTheCaption(t *testing.T) {
	m := &config.Music{StylePrefix: "warm", Vocals: "male"}
	cases := []struct {
		name string
		tr   config.Track
		want string
	}{
		{"default voice", config.Track{ID: "a", Prompt: "piano ballad,"}, "warm, piano ballad, male vocals"},
		{"track overrides the default", config.Track{ID: "b", Prompt: "piano ballad", Vocals: "female"}, "warm, piano ballad, female vocals"},
		{"instrumental has no voice", config.Track{ID: "c", Prompt: "piano ballad", Instrumental: true}, "warm, piano ballad"},
		{"extract has no voice", config.Track{ID: "d", Prompt: "piano ballad", Task: config.TaskExtract}, "warm, piano ballad"},
	}
	for _, c := range cases {
		if got := BuildRequest(cfgWith(m), c.tr, 1).Caption; got != c.want {
			t.Errorf("%s: caption = %q, want %q", c.name, got, c.want)
		}
	}
	if got := BuildRequest(cfgWith(nil), config.Track{ID: "e", Prompt: "p", Vocals: "duet"}, 1).Caption; got != "p, male and female duet vocals" {
		t.Errorf("no music defaults: caption = %q", got)
	}
}

func TestWhyNoOutputNamesTheFatalLinesAndHintsAtMemory(t *testing.T) {
	out := "[VAE] allocating\nggml_backend_cuda_buffer_type_alloc_buffer: cudaMalloc failed: out of memory\n[VAE] FATAL: graph alloc failed\n[Ace-Synth] All done\n"
	got := whyNoOutput(out)
	for _, want := range []string{"FATAL: graph alloc failed", "out of memory", "ran out of memory"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing from %q", want, got)
		}
	}
	if whyNoOutput("[Ace-Synth] All done\n") != "" {
		t.Error("a clean output explains nothing")
	}
}

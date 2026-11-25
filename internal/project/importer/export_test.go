package importer

import (
	"strings"
	"testing"

	"veritmo/internal/music/musicmotion"

	"veritmo/internal/project/config"
)

func ip(v int) *int         { return &v }
func fp(v float64) *float64 { return &v }

func videoProject() *config.Config {
	return &config.Config{
		Project: "demo",
		Output:  config.Output{Mode: config.ModeVideo, File: "out/demo.mp4"},
		Defaults: config.Defaults{
			SubjectPrompt:  "photo of two friends, sunny park,",
			NegativePrompt: "blurry, watermark",
		},
		Scenes: []config.Scene{
			{ID: "talk", Prompt: "sitting on a bench", TTSText: "She says \"hi\" and\nwaves", Speed: 1.2, Seed: ip(7)},
			{ID: "quiet", Prompt: "wide shot of the park", SilenceSeconds: fp(3), NegativePrompt: "people", Animate: true, MotionPrompt: "leaves move in the wind, static camera"},
			{ID: "plain", Prompt: "a close-up", SilenceSeconds: fp(2), Animate: true},
		},
	}
}

func TestExportDataset_RoundTripsThroughTheImporter(t *testing.T) {
	d, err := ExportDataset(videoProject())
	if err != nil {
		t.Fatal(err)
	}
	if d.Exported != 3 {
		t.Fatalf("exported = %d", d.Exported)
	}
	entries, rejected, err := LoadBytes(d.JSON)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("%v %v", err, rejected)
	}
	res, err := Map(entries, nil, Options{Project: "demo", SilenceSeconds: 2})
	if err != nil {
		t.Fatal(err)
	}
	if res.Rejected != 0 || len(res.Config.Scenes) != 3 {
		t.Fatalf("%+v", res.Reports)
	}
	talk, quiet, plain := res.Config.Scenes[0], res.Config.Scenes[1], res.Config.Scenes[2]
	// the shared prompt is folded into every scene, and the narration keeps its words (quotes become single)
	if talk.Prompt != "photo of two friends, sunny park, sitting on a bench" || talk.TTSText != "She says 'hi' and waves" {
		t.Errorf("talk = %+v", talk)
	}
	if talk.Speed != 1.2 || talk.Seed == nil || *talk.Seed != 7 || talk.SilenceSeconds != nil {
		t.Errorf("talk settings = %+v", talk)
	}
	if quiet.SilenceSeconds == nil || *quiet.SilenceSeconds != 3 || !quiet.Animate || quiet.MotionPrompt != "leaves move in the wind, static camera" {
		t.Errorf("quiet = %+v", quiet)
	}
	// without its own motion text, the scene prompt moves
	if !plain.Animate || !strings.Contains(plain.MotionPrompt, "a close-up") {
		t.Errorf("plain = %+v", plain)
	}
	// the negative the scenes share goes to the defaults only when every scene has the same one
	if res.Config.Defaults.NegativePrompt != "" {
		t.Errorf("the scenes do not share one negative: %q", res.Config.Defaults.NegativePrompt)
	}
	if quiet.NegativePrompt != "people blurry, watermark" || talk.NegativePrompt != "blurry, watermark" {
		t.Errorf("negatives: %q / %q", talk.NegativePrompt, quiet.NegativePrompt)
	}
	if err := res.Validate(); err != nil {
		t.Fatalf("the imported project must validate: %v", err)
	}
	yaml := string(res.EmitYAML())
	for _, want := range []string{"animate: true", "motion_prompt: leaves move in the wind, static camera", "seed: 7", "speed: 1.2", "silence_seconds: 3", "# animate:"} {
		if !strings.Contains(yaml, want) {
			t.Errorf("the YAML lacks %q:\n%s", want, yaml)
		}
	}
}

func TestExportDataset_NotesWhatTheFormatCannotCarry(t *testing.T) {
	cfg := videoProject()
	cfg.Scenes[0].Look = &config.Look{Grain: 0.3}
	cfg.Scenes = append(cfg.Scenes, config.Scene{ID: "pic", Image: "mine.png", SilenceSeconds: fp(2)}, config.Scene{ID: "empty", SilenceSeconds: fp(2)})
	d, err := ExportDataset(cfg)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(d.Notes, "|")
	for _, want := range []string{"look of some scenes", `"pic" uses its own picture mine.png`, `"empty" has no prompt`, "models, voice"} {
		if !strings.Contains(joined, want) {
			t.Errorf("notes lack %q: %v", want, d.Notes)
		}
	}
	if d.Exported != 4 { // the three scenes with something to say, and the one with a picture
		t.Errorf("exported = %d", d.Exported)
	}
}

func TestExportDataset_MusicRoundTrips(t *testing.T) {
	bpm, dur := 96, 40.0
	cfg := &config.Config{
		Project:  "songs",
		Output:   config.Output{Mode: config.ModeMusic},
		Defaults: config.Defaults{Music: &config.Music{StylePrefix: "warm analog", Vocals: "female", VocalLanguage: "es"}},
		Tracks: []config.Track{
			{ID: "balada", Prompt: "slow piano ballad", Lyrics: "[Verse]\nCamino por la calle\n\n[Chorus]\nVuelvo a casa", BPM: &bpm, DurationSeconds: &dur, KeyScale: "C minor", TimeSignature: "4", Seed: ip(7), Variants: ip(2)},
			{ID: "base", Prompt: "lofi beat", Instrumental: true},
			{ID: "remix", Prompt: "jazz cover", Task: config.TaskCover, Source: "balada.mp3"},
		},
	}
	d, err := ExportDataset(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if d.Exported != 2 || !strings.Contains(strings.Join(d.Notes, "|"), `track "remix" (cover)`) {
		t.Fatalf("exported %d, notes %v", d.Exported, d.Notes)
	}
	entries, _, err := LoadBytes(d.JSON)
	if err != nil {
		t.Fatal(err)
	}
	res, err := MapMusic(entries, nil, Options{Project: "songs", SilenceSeconds: 2})
	if err != nil || res.Rejected != 0 {
		t.Fatalf("%v %+v", err, res.Reports)
	}
	b, base := res.Config.Tracks[0], res.Config.Tracks[1]
	if b.Prompt != "warm analog, slow piano ballad, female vocals" || b.Lyrics != "[Verse]\nCamino por la calle\n\n[Chorus]\nVuelvo a casa" {
		t.Errorf("balada = %+v", b)
	}
	if b.BPM == nil || *b.BPM != 96 || b.DurationSeconds == nil || *b.DurationSeconds != 40 || b.KeyScale != "C minor" || b.TimeSignature != "4" || b.Seed == nil || *b.Seed != 7 || b.Variants == nil || *b.Variants != 2 || b.VocalLanguage != "es" {
		t.Errorf("balada settings = %+v", b)
	}
	if !base.Instrumental || base.Prompt != "warm analog, lofi beat" {
		t.Errorf("base = %+v", base)
	}
}

func TestExportDataset_NothingToExportIsAnError(t *testing.T) {
	if _, err := ExportDataset(&config.Config{Project: "p", Output: config.Output{Mode: config.ModeVideo}}); err == nil {
		t.Fatal("an empty project has nothing to export")
	}
}

func TestExportDataset_TheNegativePromptTravelsAndSharedOnesGoBackToTheDefaults(t *testing.T) {
	cfg := &config.Config{
		Project:  "demo",
		Output:   config.Output{Mode: config.ModeVideo},
		Defaults: config.Defaults{NegativePrompt: "blurry, watermark"},
		Scenes: []config.Scene{
			{ID: "a", Prompt: "one", SilenceSeconds: fp(2)},
			{ID: "b", Prompt: "two", SilenceSeconds: fp(2)},
		},
	}
	d, err := ExportDataset(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(d.JSON), "Negative: blurry, watermark") != 2 {
		t.Fatalf("every scene must carry the negative prompt:\n%s", d.JSON)
	}
	entries, _, _ := LoadBytes(d.JSON)
	res, err := Map(entries, nil, Options{Project: "demo", SilenceSeconds: 2})
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.Defaults.NegativePrompt != "blurry, watermark" || res.Config.Scenes[0].NegativePrompt != "" {
		t.Errorf("a negative shared by every scene is the project's again: defaults %q, scene %q", res.Config.Defaults.NegativePrompt, res.Config.Scenes[0].NegativePrompt)
	}
	// a scene with its own negative keeps it in front of the project's
	cfg.Scenes[1].NegativePrompt = "people"
	d, _ = ExportDataset(cfg)
	if !strings.Contains(string(d.JSON), "Negative: people blurry, watermark") {
		t.Errorf("scene-level negative first, then the project's:\n%s", d.JSON)
	}
}

func TestExportDataset_AnimatedGraphicsAndTitlesRoundTrip(t *testing.T) {
	two, five, one := 2.0, 5.0, 1.0
	silent := 4.0
	cfg := &config.Config{
		Project: "mix",
		Output:  config.Output{Mode: config.ModeVideo},
		Scenes: []config.Scene{
			{ID: "intro", MotionGraphic: "Opening title over blue waves", SilenceSeconds: &silent, Overlay: &config.Overlay{Text: "Ana Ruiz, director", Position: "top", Start: &one, End: &five}},
			{ID: "street", Prompt: "A quiet street", SilenceSeconds: &two, Overlay: &config.Overlay{Text: "Madrid, 1990"}},
		},
	}
	d, err := ExportDataset(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if d.Exported != 2 {
		t.Fatalf("both scenes must be exported: %d notes=%v", d.Exported, d.Notes)
	}
	if !strings.Contains(strings.Join(d.Notes, "|"), "pages of the animated graphics") {
		t.Errorf("notes: %v", d.Notes)
	}
	entries, rej, err := LoadBytes(d.JSON)
	if err != nil || len(rej) != 0 {
		t.Fatalf("%v %v", err, rej)
	}
	res, err := Map(entries, nil, Options{Project: "mix", SilenceSeconds: 2})
	if err != nil || res.Rejected != 0 {
		t.Fatalf("%v %+v", err, res.Reports)
	}
	a, b := res.Config.Scenes[0], res.Config.Scenes[1]
	if a.MotionGraphic != "Opening title over blue waves" || *a.SilenceSeconds != 4 || a.Overlay == nil || a.Overlay.Position != "top" || *a.Overlay.End != 5 {
		t.Errorf("graphic: %+v %+v", a, a.Overlay)
	}
	if b.Prompt != "A quiet street" || b.Overlay == nil || b.Overlay.Text != "Madrid, 1990" {
		t.Errorf("scene: %+v", b)
	}
	// a graphics-only dataset reads back through the motion mode too
	only, err := ExportDataset(&config.Config{Project: "g", Output: config.Output{Mode: config.ModeVideo}, Scenes: cfg.Scenes[:1]})
	if err != nil {
		t.Fatal(err)
	}
	ents, _, _ := LoadBytes(only.JSON)
	m, err := MapMotion(ents, nil, Options{Project: "g", SilenceSeconds: 2, Parts: musicmotion.Parts{}})
	if err != nil || m.Imported != 1 || m.Config.Scenes[0].MotionGraphic != "Opening title over blue waves" {
		t.Fatalf("%v %+v", err, m.Reports)
	}
}

func TestExportDataset_TheAdultLabelRoundTrips(t *testing.T) {
	for name, tc := range map[string]struct {
		set  func(c *config.Config)
		want string
	}{
		"for adults": {func(c *config.Config) { v := true; c.Adult = &v }, "true"},
		"reviewed":   {func(c *config.Config) { v := false; c.Adult = &v }, "false"},
		"not looked": {func(c *config.Config) {}, ""},
	} {
		cfg := videoProject()
		tc.set(cfg)
		d, err := ExportDataset(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if has := strings.Contains(string(d.JSON), "Adult:"); has != (tc.want != "") {
			t.Errorf("%s: the Adult line is %v in %s", name, has, d.JSON)
		}
		entries, rejected, err := LoadBytes(d.JSON)
		if err != nil || len(rejected) != 0 {
			t.Fatalf("%s: %v %v", name, err, rejected)
		}
		res, err := Map(entries, nil, Options{Project: "demo", SilenceSeconds: 2})
		if err != nil || res.Rejected != 0 {
			t.Fatalf("%s: %v %+v", name, err, res.Reports)
		}
		got := ""
		if res.Config.Adult != nil {
			got = map[bool]string{true: "true", false: "false"}[*res.Config.Adult]
		}
		if got != tc.want {
			t.Errorf("%s: imported %q, want %q", name, got, tc.want)
		}
	}
}

func TestImportRefusesABadAdultLine(t *testing.T) {
	entries := []Entry{{ID: "a", Title: "a", FullPrompt: "a photo\nAdult: maybe"}}
	res, err := Map(entries, nil, Options{Project: "p", SilenceSeconds: 2, Parts: musicmotion.Parts{}})
	if err != nil || res.Rejected != 1 {
		t.Fatalf("%v %+v", err, res)
	}
}

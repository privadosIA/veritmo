package importer

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/project/config"
)

const (
	htmlFixture = "../../../testdata/demo_prompts.html"
	jsonFixture = "../../../testdata/demo_prompts.json"
)

var fixtureIDs = []string{"intro", "bronze_a", "bronze_b", "silver_a", "silver_b", "outro"}

func defaultOpts() Options {
	return Options{Project: "demo_prompts", SilenceSeconds: 2.0}
}

func TestLoad_JSONFixture(t *testing.T) {
	entries, rejected, err := Load(jsonFixture)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(rejected) != 0 {
		t.Fatalf("rejected = %v, want none", rejected)
	}
	if len(entries) != 6 {
		t.Fatalf("len(entries) = %d, want 6", len(entries))
	}
	for i, id := range fixtureIDs {
		if entries[i].ID != id {
			t.Errorf("entries[%d].ID = %q, want %q", i, entries[i].ID, id)
		}
	}
}

func TestLoad_HTMLFixtureMatchesJSON(t *testing.T) {
	htmlEntries, rejected, err := Load(htmlFixture)
	if err != nil {
		t.Fatalf("Load html: %v", err)
	}
	if len(rejected) != 0 {
		t.Fatalf("rejected = %v, want none", rejected)
	}
	jsonEntries, _, err := Load(jsonFixture)
	if err != nil {
		t.Fatalf("Load json: %v", err)
	}
	if len(htmlEntries) != len(jsonEntries) {
		t.Fatalf("html %d entries, json %d", len(htmlEntries), len(jsonEntries))
	}
	for i := range htmlEntries {
		if htmlEntries[i] != jsonEntries[i] {
			t.Errorf("entry %d differs:\nhtml: %+v\njson: %+v", i, htmlEntries[i], jsonEntries[i])
		}
	}
	// ${NEG} resolved: no placeholder survives.
	for _, e := range htmlEntries {
		if strings.Contains(e.FullPrompt, "${") {
			t.Errorf("entry %s keeps an unresolved placeholder", e.ID)
		}
	}
}

func TestLoad_ConvergenceByteIdenticalYAML(t *testing.T) {
	emit := func(path string) []byte {
		t.Helper()
		entries, rejected, err := Load(path)
		if err != nil || len(rejected) != 0 {
			t.Fatalf("Load %s: %v (rejected %v)", path, err, rejected)
		}
		res, err := Map(entries, rejected, defaultOpts())
		if err != nil {
			t.Fatalf("Map: %v", err)
		}
		return res.EmitYAML()
	}
	if !bytes.Equal(emit(htmlFixture), emit(jsonFixture)) {
		t.Error("HTML and JSON fixtures must produce byte-identical scenes.yaml")
	}
}

func TestLoad_UnresolvedPlaceholderRejectsOnlyThatEntry(t *testing.T) {
	html := "<html><body><script>\n" +
		`const NEG = "Negative: blurry.";` + "\n" +
		"const data = [\n" +
		"  [\"good\", \"t\", \"\", `A castle.\n${NEG}`],\n" +
		"  [\"bad\", \"t\", \"\", `A ship.\n${UNKNOWN}`]\n" +
		"];\n</script></body></html>"
	p := filepath.Join(t.TempDir(), "in.html")
	if err := os.WriteFile(p, []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, rejected, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != "good" {
		t.Errorf("entries = %+v, want only 'good'", entries)
	}
	if len(rejected) != 1 || rejected[0].ID != "bad" || !strings.Contains(rejected[0].Reason, "UNKNOWN") {
		t.Errorf("rejected = %+v, want 'bad' naming UNKNOWN", rejected)
	}
}

func mapFixture(t *testing.T) *Result {
	t.Helper()
	entries, rejected, err := Load(htmlFixture)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Map(entries, rejected, defaultOpts())
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestMap_FixturePerSpec(t *testing.T) {
	res := mapFixture(t)
	if res.Imported != 6 || res.Silent != 2 || res.Rejected != 0 {
		t.Fatalf("totals = %d/%d/%d, want 6 imported, 2 silent, 0 rejected", res.Imported, res.Silent, res.Rejected)
	}
	for i, id := range fixtureIDs {
		if res.Config.Scenes[i].ID != id {
			t.Errorf("scene %d id = %q, want %q (dataset order)", i, res.Config.Scenes[i].ID, id)
		}
	}
	byID := map[string]config.Scene{}
	for _, s := range res.Config.Scenes {
		byID[s.ID] = s
	}

	wantIntro := "Nobody chooses the night shift at Saltmarsh Point. The night shift chooses you."
	if byID["intro"].TTSText != wantIntro {
		t.Errorf("intro.tts_text = %q, want %q", byID["intro"].TTSText, wantIntro)
	}
	for _, id := range []string{"bronze_b", "outro"} {
		s := byID[id]
		if s.TTSText != "" || s.SilenceSeconds == nil || *s.SilenceSeconds != 2.0 {
			t.Errorf("%s should be silent with silence_seconds 2.0, got %+v", id, s)
		}
	}
	wantNeg := "studio lighting, glamour, perfect composition, cartoon, CGI, 3d render, text, watermark, blurry."
	if res.Config.Defaults.NegativePrompt != wantNeg {
		t.Errorf("defaults.negative_prompt = %q", res.Config.Defaults.NegativePrompt)
	}
	for _, s := range res.Config.Scenes {
		if s.NegativePrompt != "" {
			t.Errorf("scene %s must not repeat the shared negative", s.ID)
		}
		for _, banned := range []string{"Dialogue", "Negative:", "Camera/style:"} {
			if strings.Contains(s.Prompt, banned) {
				t.Errorf("scene %s prompt still contains %q: %s", s.ID, banned, s.Prompt)
			}
		}
		if s.Prompt == "" {
			t.Errorf("scene %s has an empty prompt", s.ID)
		}
	}
}

func TestMap_DivergentNegativesStaySceneLocal(t *testing.T) {
	entries := []Entry{
		{ID: "a", FullPrompt: "A castle.\nNegative: blurry."},
		{ID: "b", FullPrompt: "A ship.\nNegative: cartoonish."},
	}
	res, err := Map(entries, nil, Options{Project: "p", SilenceSeconds: 2})
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.Defaults.NegativePrompt != "" {
		t.Errorf("defaults.negative_prompt must stay empty on divergence, got %q", res.Config.Defaults.NegativePrompt)
	}
	if res.Config.Scenes[0].NegativePrompt != "blurry." || res.Config.Scenes[1].NegativePrompt != "cartoonish." {
		t.Errorf("scene negatives = %q / %q", res.Config.Scenes[0].NegativePrompt, res.Config.Scenes[1].NegativePrompt)
	}
}

func TestMap_DuplicateIDsAbort(t *testing.T) {
	entries := []Entry{
		{ID: "dup", FullPrompt: "A."},
		{ID: "dup", FullPrompt: "B."},
	}
	_, err := Map(entries, nil, Options{Project: "p", SilenceSeconds: 2})
	if err == nil || !strings.Contains(err.Error(), "dup") || !strings.Contains(err.Error(), "0") {
		t.Fatalf("expected duplicate-id error naming the id and positions, got: %v", err)
	}
}

func TestMap_EntryRejections(t *testing.T) {
	cases := []struct {
		name   string
		prompt string
		reason string
	}{
		{"double dialogue", "A.\nDialogue: \"x\"\nDialogue: \"y\"", "second Dialogue"},
		{"dialogue without quotes", "A.\nDialogue: hello there", "without quoted text"},
		{"empty prompt", "Dialogue: \"only speech\"", "empty prompt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Map([]Entry{{ID: "e", FullPrompt: tc.prompt}}, nil, Options{Project: "p", SilenceSeconds: 2})
			if err != nil {
				t.Fatal(err)
			}
			if res.Rejected != 1 || res.Imported != 0 {
				t.Fatalf("totals = %+v, want 1 rejected", res)
			}
			if !strings.Contains(res.Reports[0].Rejected, tc.reason) {
				t.Errorf("reason = %q, want contains %q", res.Reports[0].Rejected, tc.reason)
			}
		})
	}
}

func TestReport_CoversMappedAndDiscarded(t *testing.T) {
	res := mapFixture(t)
	report := res.Report()
	for _, id := range fixtureIDs {
		if !strings.Contains(report, "["+id+"]") {
			t.Errorf("report missing entry block for %s", id)
		}
	}
	if !strings.Contains(report, "Camera/style") {
		t.Error("report must list discarded Camera/style text")
	}
	if !strings.Contains(report, "imported 6, silent 2, rejected 0") {
		t.Errorf("report summary wrong:\n%s", report)
	}
}

func TestReport_LoraInfoWarned(t *testing.T) {
	entries := []Entry{{ID: "e", LoraInfo: "<lora:style:0.8>", FullPrompt: "A castle."}}
	res, err := Map(entries, nil, Options{Project: "p", SilenceSeconds: 2})
	if err != nil {
		t.Fatal(err)
	}
	report := res.Report()
	if !strings.Contains(report, "lora_info") || !strings.Contains(report, "not active in v1") {
		t.Errorf("report must warn about discarded lora_info:\n%s", report)
	}
}

func TestEmit_RoundTripThroughConfigLoad(t *testing.T) {
	res := mapFixture(t)
	out := filepath.Join(t.TempDir(), "scenes.yaml")
	if err := res.WriteYAML(out, false); err != nil {
		t.Fatalf("WriteYAML: %v", err)
	}

	cfg, err := config.Load(out)
	if err != nil {
		t.Fatalf("generated YAML does not parse: %v", err)
	}
	if len(cfg.Scenes) != 6 || cfg.Project != "demo_prompts" {
		t.Fatalf("round-trip lost content: %d scenes, project %q", len(cfg.Scenes), cfg.Project)
	}

	// Fill the placeholders the dataset cannot know, then full validation
	// must pass with no other edits.
	dir := t.TempDir()
	model, voice := "m.safetensors", "v.onnx"
	for _, p := range []string{model, voice, voice + ".json"} {
		if err := os.WriteFile(filepath.Join(dir, p), []byte("stub"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg.Output = config.Output{Mode: config.ModeVideo, File: "out/demo.mp4"}
	cfg.Defaults.Model = model
	cfg.Defaults.Voice = voice
	cfg.Defaults.Resolution = "1280x720"
	cfg.Defaults.Sampler = "euler"
	cfg.Defaults.Steps = 30
	cfg.Defaults.FPS = 30
	if err := cfg.ValidateInDir(dir); err != nil {
		t.Fatalf("filled config must validate cleanly: %v", err)
	}
}

func TestEmit_RefusesExistingOutput(t *testing.T) {
	res := mapFixture(t)
	out := filepath.Join(t.TempDir(), "scenes.yaml")
	if err := os.WriteFile(out, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := res.WriteYAML(out, false)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected overwrite refusal, got: %v", err)
	}
	if got, _ := os.ReadFile(out); string(got) != "keep me" {
		t.Error("existing file must be unchanged")
	}
	if err := res.WriteYAML(out, true); err != nil {
		t.Fatalf("--force-overwrite should allow replacing: %v", err)
	}
}

func TestEmit_NothingWrittenOnInvalidConfig(t *testing.T) {
	res := &Result{Config: &config.Config{
		Project: "p",
		Scenes: []config.Scene{
			{ID: "dup", Prompt: "a", TTSText: "t"},
			{ID: "dup", Prompt: "b", TTSText: "t"},
		},
	}}
	out := filepath.Join(t.TempDir(), "scenes.yaml")
	err := res.WriteYAML(out, false)
	if err == nil {
		t.Fatal("expected validation failure")
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Error("nothing must be written when validation fails")
	}
}

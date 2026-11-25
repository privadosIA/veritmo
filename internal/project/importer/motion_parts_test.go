package importer

import (
	"reflect"
	"strings"
	"testing"

	"veritmo/internal/music/musicmotion" // the catalog of parts: it checks the parts a dataset names
	"veritmo/internal/project/config"
)

func TestMapMotion_APartWithItsParametersTextAndTempo(t *testing.T) {
	res := mapGraphics(t, songEntry("count", "Part: counter\nParam from: 10\nParam step: 5\nParam unit: beats\nText: ten to zero\nBPM: 100\nDuration: 6"))
	if res.Imported != 1 || res.Rejected != 0 {
		t.Fatalf("%+v", res.Reports)
	}
	s := res.Config.Scenes[0]
	if s.MotionPart != "counter" || s.MotionGraphic != "" || s.Prompt != "" || s.MotionText != "ten to zero" || s.MotionBPM == nil || *s.MotionBPM != 100 {
		t.Fatalf("scene = %+v", s)
	}
	if s.MotionParams["from"] != int64(10) || s.MotionParams["step"] != int64(5) || s.MotionParams["unit"] != "beats" {
		t.Fatalf("the parameters are typed as the part declares them: %#v", s.MotionParams)
	}
	if s.SilenceSeconds == nil || *s.SilenceSeconds != 6 {
		t.Fatalf("duration: %v", s.SilenceSeconds)
	}
	if err := res.Validate(); err != nil {
		t.Fatalf("a scene made from a part is valid: %v", err)
	}
	yaml := string(res.EmitYAML())
	for _, want := range []string{"motion_part: counter", "motion_params:", "      from: 10", "      unit: beats", `motion_text: ten to zero`, "motion_bpm: 100"} {
		if !strings.Contains(yaml, want) {
			t.Errorf("yaml lacks %q:\n%s", want, yaml)
		}
	}
	if strings.Contains(yaml, "motion_graphic:") || strings.Contains(yaml, "prompt:") {
		t.Errorf("a part needs no description:\n%s", yaml)
	}
	back, err := config.LoadBytes(res.EmitYAML())
	if err != nil || back.Scenes[0].MotionPart != "counter" || back.Scenes[0].MotionParams["unit"] != "beats" {
		t.Fatalf("the YAML reads back as the same scene: %v %+v", err, back)
	}
	if !strings.Contains(res.Report(), "motion_part") || !strings.Contains(res.Report(), "motion_params.from") {
		t.Errorf("the report says how each marker was mapped: %s", res.Report())
	}
}

func TestMapMotion_AutoUsesTheTextAsTheDescription(t *testing.T) {
	res := mapGraphics(t, songEntry("g", "A table of risks with one row in red\nPart: auto\nDuration: 5"))
	s := res.Config.Scenes[0]
	if res.Imported != 1 || s.MotionPart != "auto" || s.MotionGraphic != "A table of risks with one row in red" {
		t.Fatalf("%+v %+v", s, res.Reports)
	}
	if err := res.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMapMotion_AnEntryWithASomethingWrongIsRejectedWithTheReasonAndTheOthersAreImported(t *testing.T) {
	cases := map[string]struct{ entry, want string }{
		"a part that is not":            {"Part: hologram", "hologram"},
		"a parameter not its own":       {"Part: toggle\nParam colour: red", `"colour"`},
		"a parameter of the wrong type": {"Part: counter\nParam from: ten", "whole number"},
		"a parameter out of limits":     {"Part: toggle\nParam count: 99", `"count"`},
		"parameters with auto":          {"a thing\nPart: auto\nParam count: 2", "Part: auto"},
		"parameters with no part":       {"a graphic\nParam count: 2", "Param lines need a Part line"},
		"text with no part":             {"a graphic\nText: hello", "Text needs a Part line"},
		"a tempo with no part":          {"a graphic\nBPM: 100", "BPM needs a Part line"},
		"a tempo out of the range":      {"Part: counter\nBPM: 400", "BPM"},
		"a parameter given twice":       {"Part: counter\nParam from: 1\nParam from: 2", "twice"},
	}
	for name, c := range cases {
		res := mapGraphics(t, songEntry("good", "Part: counter"), songEntry("bad", c.entry+"\nDuration: 3"))
		if res.Imported != 1 || res.Rejected != 1 || res.Config.Scenes[0].ID != "good" {
			t.Errorf("%s: imported %d rejected %d", name, res.Imported, res.Rejected)
			continue
		}
		joined := ""
		for _, r := range res.Reports {
			joined += r.Rejected
		}
		if !strings.Contains(joined, c.want) {
			t.Errorf("%s: the reason must say %q, it says %q", name, c.want, joined)
		}
	}
}

func TestMapMotion_TheFirstDesignGivesThePaletteOfTheProject(t *testing.T) {
	res := mapGraphics(t, songEntry("a", "Part: counter\nDesign: electronic, code\nDuration: 3"), songEntry("b", "Part: toggle\nDesign: pastel, pop\nDuration: 3"))
	if res.Config.Defaults.SubjectPrompt != "electronic, code" {
		t.Fatalf("subject_prompt = %q", res.Config.Defaults.SubjectPrompt)
	}
	if !strings.Contains(res.Report(), "palette") {
		t.Errorf("the report says what the words are for: %s", res.Report())
	}
	if !strings.Contains(string(res.EmitYAML()), `subject_prompt: electronic, code`) {
		t.Errorf("the project's words are written:\n%s", res.EmitYAML())
	}
}

func TestMapMotion_TheNewMarkersAreOnlyReadInGraphicsDatasets(t *testing.T) {
	res, err := Map([]Entry{songEntry("s", "A park at dawn\nText: not a marker here\nBPM: 90\nSilence: 2")}, nil, Options{Project: "scenes", SilenceSeconds: 2})
	if err != nil || res.Imported != 1 {
		t.Fatalf("%v %+v", err, res)
	}
	if !strings.Contains(res.Config.Scenes[0].Prompt, "Text: not a marker here") {
		t.Fatalf("in a video dataset they stay in the prompt: %q", res.Config.Scenes[0].Prompt)
	}
}

// Exporting the scenes of a project made from parts and importing the export gives the same scenes.
func TestExportDataset_APartsProjectRoundTrips(t *testing.T) {
	two := 60.0 / 100 * 100
	bpm := 90.0
	cfg := &config.Config{Project: "kit", Output: config.Output{Mode: config.ModeVideo, File: "out/kit.mp4"}, Defaults: config.Defaults{SubjectPrompt: "electronic, code"},
		Scenes: []config.Scene{
			{ID: "count", MotionPart: "roll-counter", MotionParams: map[string]any{"from": int64(10), "to": int64(0), "label": "years", "glitch": true}, MotionText: "closer than it looks", MotionBPM: &bpm, SilenceSeconds: &two},
			{ID: "chosen", MotionPart: "auto", MotionGraphic: "a search box that types a question", SilenceSeconds: &two},
		}}
	d, err := ExportDataset(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Part: roll-counter", "Param from: 10", "Param glitch: true", "Text: closer than it looks", "BPM: 90", "Design: electronic, code", "Graphic: a search box", "Part: auto"} {
		if !strings.Contains(string(d.JSON), want) {
			t.Errorf("the export lacks %q:\n%s", want, d.JSON)
		}
	}
	entries, rejected, err := LoadBytes(d.JSON)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("%v %v", err, rejected)
	}
	back, err := MapMotion(entries, nil, Options{Project: "kit", SilenceSeconds: 2, Parts: musicmotion.Parts{}})
	if err != nil || back.Imported != 2 || back.Rejected != 0 {
		t.Fatalf("%v %+v", err, back.Reports)
	}
	a, b := cfg.Scenes[0], back.Config.Scenes[0]
	if b.MotionPart != a.MotionPart || b.MotionText != a.MotionText || *b.MotionBPM != *a.MotionBPM || *b.SilenceSeconds != *a.SilenceSeconds {
		t.Fatalf("the scene comes back as it was: %+v", b)
	}
	if !reflect.DeepEqual(b.MotionParams, a.MotionParams) {
		t.Fatalf("the parameters come back as they were: %#v against %#v", b.MotionParams, a.MotionParams)
	}
	c := back.Config.Scenes[1]
	if c.MotionPart != "auto" || c.MotionGraphic != "a search box that types a question" || back.Config.Defaults.SubjectPrompt != "electronic, code" {
		t.Fatalf("%+v %q", c, back.Config.Defaults.SubjectPrompt)
	}
}

func TestMapMotion_ThePartIsReportedOnce(t *testing.T) {
	for _, line := range []string{"Part: counter", "Part: auto"} {
		res := mapGraphics(t, songEntry("g", "ten to zero\n"+line+"\nDuration: 6"))
		n := 0
		for _, m := range res.Reports[0].Mapped {
			if strings.HasPrefix(m, "motion_part") {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s: want one motion_part line, got %d: %v", line, n, res.Reports[0].Mapped)
		}
	}
}

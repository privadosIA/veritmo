package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"veritmo/internal/project/config"
)

func modelEntry(id, text string) Entry { return Entry{ID: id, Title: "t " + id, FullPrompt: text} }

func mapModels(t *testing.T, entries ...Entry) *Result {
	t.Helper()
	res, err := MapModels3D(entries, nil, Options{Project: "objetos"})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestMapModels3D_ThreePromptsBecomeThreeEntries(t *testing.T) {
	res := mapModels(t,
		modelEntry("patin", "a retro roller skate, glossy pink"),
		modelEntry("copa", "a golden trophy cup\non a plinth"),
		modelEntry("gato", "a white ceramic cat"))
	if res.Imported != 3 || res.Rejected != 0 || res.Config.Output.Mode != config.ModeModels3D || len(res.Config.Scenes) != 0 || len(res.Config.Tracks) != 0 {
		t.Fatalf("%+v", res)
	}
	got := res.Config.Models
	if got[0].ID != "patin" || got[0].Prompt != "a retro roller skate, glossy pink" || got[1].Prompt != "a golden trophy cup on a plinth" || got[2].ID != "gato" {
		t.Errorf("entries: %+v", got)
	}
	if err := res.Validate(); err != nil {
		t.Errorf("an imported project validates as a hand-written one: %v", err)
	}
	if rep := res.Report(); !strings.Contains(rep, "imported 3 models, rejected 0") {
		t.Errorf("report:\n%s", rep)
	}
}

func TestMapModels3D_TheSettingLinesSetTheirFields(t *testing.T) {
	res := mapModels(t, modelEntry("copa", "a golden trophy cup\nDetail: high\nVariants: 2\nengine: SF3D\nSeed: 7\nAdult: true"))
	if res.Imported != 1 {
		t.Fatalf("%+v", res.Reports)
	}
	m := res.Config.Models[0]
	if m.Prompt != "a golden trophy cup" || m.Detail != "high" || m.Engine != "sf3d" || m.EntryVariants() != 2 || *m.Seed != 7 || !m.Adult {
		t.Errorf("%+v", m)
	}
	if err := res.Validate(); err != nil {
		t.Error(err)
	}
}

func TestMapModels3D_AnEntryWithItsOwnPicture(t *testing.T) {
	res := mapModels(t, modelEntry("gato", "Image: gato.png\nDetail: low"))
	if res.Imported != 1 || res.Config.Models[0].Image != "gato.png" || res.Config.Models[0].Prompt != "" {
		t.Fatalf("%+v %+v", res.Reports, res.Config.Models)
	}
	// the preview says the picture is to be uploaded: a dataset cannot carry it
	if d := strings.Join(res.Reports[0].Discarded, "|"); !strings.Contains(d, "gato.png has to be in the project's sources") {
		t.Errorf("warnings: %v", res.Reports[0])
	}
	// the check stubs the picture, as it does for the description files of the 3D scenes
	if err := res.Validate(); err != nil {
		t.Errorf("%v", err)
	}
}

func TestMapModels3D_Refusals(t *testing.T) {
	for _, tc := range []struct{ name, text, want string }{
		{"an unknown engine", "a lamp\nEngine: dall-e", `line "Engine: dall-e": Engine must be one of triposr, sf3d`},
		{"an unknown detail", "a lamp\nDetail: ultra", `line "Detail: ultra": Detail must be low, normal or high`},
		{"too many variants", "a lamp\nVariants: 9", `Variants must be a whole number from 1 to 4`},
		{"a seed that is not one", "a lamp\nSeed: -3", `Seed must be a whole number, 0 or more`},
		{"adult that is not a boolean", "a lamp\nAdult: maybe", `Adult must be true or false`},
		{"a picture that is a path", "Image: ../x.png", `Image must be the name of a .png`},
		{"nothing", "Detail: low", `no prompt`},
		{"empty", "", `no prompt`},
		{"a prompt and a picture", "a lamp\nImage: l.png", `not both`},
		{"a prompt that is too long", strings.Repeat("x", 1001), `the most is 1000`},
	} {
		res := mapModels(t, modelEntry("a", tc.text), modelEntry("b", "a good one"))
		if res.Rejected != 1 || res.Imported != 1 || res.Config.Models[0].ID != "b" {
			t.Errorf("%s: the bad entry is rejected and the other imports: %+v", tc.name, res.Reports)
			continue
		}
		if r := res.Reports[0]; r.ID != "a" || !strings.Contains(r.Rejected, tc.want) {
			t.Errorf("%s: %+v, want %q", tc.name, r, tc.want)
		}
	}
}

func TestMapModels3D_IdsAreMadePlainAndUniqueWithAWarning(t *testing.T) {
	res := mapModels(t,
		modelEntry("Mi Patín Retro", "a roller skate"),
		modelEntry("copa", "a trophy"),
		modelEntry("copa", "another trophy"),
		Entry{ID: "", Title: "Lámpara", FullPrompt: "a lamp"},
		modelEntry(strings.Repeat("a", 40), "a long one"))
	var ids []string
	for _, m := range res.Config.Models {
		ids = append(ids, m.ID)
	}
	if ids[0] != "mi-patin-retro" || ids[1] != "copa" || ids[2] != "copa-2" || ids[3] != "lampara" || ids[4] != strings.Repeat("a", 32) {
		t.Errorf("ids: %v", ids)
	}
	for _, i := range []int{0, 2, 4} {
		if d := strings.Join(res.Reports[i].Discarded, "|"); !strings.Contains(d, "was changed to") {
			t.Errorf("entry %d does not say its id changed: %v", i, res.Reports[i])
		}
	}
	if d := strings.Join(res.Reports[1].Discarded, "|"); strings.Contains(d, "was changed") {
		t.Errorf("an id that was fine is not reported: %v", res.Reports[1])
	}
	if err := res.Validate(); err != nil {
		t.Errorf("%v", err)
	}
}

func TestMapModels3D_AnEmptyIdTakesItsTitle(t *testing.T) {
	res := mapModels(t, Entry{ID: "", Title: "Lámpara de pie", FullPrompt: "a floor lamp"})
	if res.Config.Models[0].ID != "lampara-de-pie" {
		t.Errorf("id = %q", res.Config.Models[0].ID)
	}
}

func TestMapModels3D_LinesAfterTheSettingsAreWarnedNotKept(t *testing.T) {
	res := mapModels(t, modelEntry("a", "a lamp\nstill a lamp\nDetail: high\nthis is not a setting"), Entry{ID: "b", Title: "t", LoraInfo: "adapter", FullPrompt: "a vase"})
	if res.Config.Models[0].Prompt != "a lamp still a lamp" {
		t.Errorf("prompt = %q", res.Config.Models[0].Prompt)
	}
	if d := strings.Join(res.Reports[0].Discarded, "|"); !strings.Contains(d, `"this is not a setting" is after the settings`) {
		t.Errorf("warnings: %v", res.Reports[0])
	}
	if d := strings.Join(res.Reports[1].Discarded, "|"); !strings.Contains(d, "lora_info") {
		t.Errorf("warnings: %v", res.Reports[1])
	}
}

func TestMapModels3D_TheYAMLIsAProjectOfTheModeThatLoads(t *testing.T) {
	res := mapModels(t, modelEntry("patin", "a roller skate\nDetail: high"), modelEntry("gato", "Image: gato.png"))
	y := res.EmitYAML()
	for _, want := range []string{"mode: models3d", "# model: models/<checkpoint>.safetensors", "# resolution: 768x768", "models:"} {
		if !strings.Contains(string(y), want) {
			t.Errorf("yaml lacks %q:\n%s", want, y)
		}
	}
	cfg, err := config.LoadBytes(y)
	if err != nil || len(cfg.Models) != 2 || cfg.Models[0].Detail != "high" || cfg.Models[1].Image != "gato.png" {
		t.Fatalf("%v %+v\n%s", err, cfg, y)
	}
	path := filepath.Join(t.TempDir(), "scenes.yaml")
	if err := res.WriteYAML(path, false); err != nil {
		t.Fatalf("a valid import is written: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Error(err)
	}
}

func TestExportModels3D_ARoundTripMakesTheSameEntries(t *testing.T) {
	src := `project: objetos
output: {mode: models3d}
defaults:
  models3d: {engine: sf3d, detail: high}
  model: models/m.safetensors
models:
  - {id: patin, prompt: "a retro roller skate, glossy pink"}
  - {id: copa, prompt: "a golden trophy", engine: triposr, detail: normal, variants: 3, seed: 7}
  - {id: gato, image: gato.png, adult: true}
`
	cfg, err := config.LoadBytes([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	ds, err := ExportDataset(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if ds.Exported != 3 {
		t.Errorf("exported %d", ds.Exported)
	}
	notes := strings.Join(ds.Notes, "|")
	for _, want := range []string{"gato.png", "folded into each entry", "the models themselves are not carried"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes lack %q: %v", want, ds.Notes)
		}
	}
	// short: the built-in engine and detail are not written
	if strings.Contains(string(ds.JSON), "Engine: triposr") || strings.Contains(string(ds.JSON), "Detail: normal") {
		t.Errorf("the built-in values are written:\n%s", ds.JSON)
	}
	entries, rejected, err := LoadBytes(ds.JSON)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("%v %v\n%s", err, rejected, ds.JSON)
	}
	res := mapModels(t, entries...)
	if res.Rejected != 0 || res.Imported != 3 {
		t.Fatalf("%+v", res.Reports)
	}
	for i, e := range cfg.Models {
		got := res.Config.Models[i]
		// what a run would do is the same: compare the effective values (the export folded the project's defaults in)
		if got.ID != e.ID || got.Prompt != e.Prompt || got.Image != e.Image || res.Config.EntryEngine(got) != cfg.EntryEngine(e) || res.Config.EntryDetail(got) != cfg.EntryDetail(e) ||
			got.EntryVariants() != e.EntryVariants() || got.Adult != e.Adult || (e.Seed == nil) != (got.Seed == nil) || (e.Seed != nil && *e.Seed != *got.Seed) {
			t.Errorf("entry %d: %+v, was %+v", i, got, e)
		}
	}
	// and the YAML of the import is what a hand edit would start from
	var probe map[string]any
	if err := yaml.Unmarshal(res.EmitYAML(), &probe); err != nil {
		t.Error(err)
	}
}

func TestExportModels3D_ANothingProjectSaysSo(t *testing.T) {
	cfg := &config.Config{Project: "p", Output: config.Output{Mode: config.ModeModels3D}}
	if _, err := ExportDataset(cfg); err == nil || !strings.Contains(err.Error(), "nothing to export") {
		t.Errorf("err = %v", err)
	}
}

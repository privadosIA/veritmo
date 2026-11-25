package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// models3DProject loads and validates a project from its YAML with the files named in its sources folder.
func models3DProject(t *testing.T, yml string, sources ...string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, SourcesSubdir), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, s := range sources {
		if err := os.WriteFile(filepath.Join(dir, SourcesSubdir, s), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := LoadBytes([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	return cfg, cfg.ValidateInDir(linked(dir))
}

func TestModels3D_AValidProject(t *testing.T) {
	cfg, err := models3DProject(t, `
project: objetos
output: {mode: models3d}
defaults:
  models3d: {engine: triposr, detail: low}
models:
  - {id: patin, prompt: "a retro roller skate, glossy pink"}
  - {id: gato, image: gato.png, detail: high}
  - {id: copa, prompt: "a golden trophy", engine: sf3d, variants: 3, seed: 7, adult: true}
`, "gato.png")
	if err != nil {
		t.Fatalf("does not validate: %v", err)
	}
	if cfg.Output.Mode != ModeModels3D || len(cfg.Models) != 3 {
		t.Fatalf("cfg = %+v", cfg)
	}
	p, g, c := cfg.Models[0], cfg.Models[1], cfg.Models[2]
	if cfg.EntryEngine(p) != "triposr" || cfg.EntryDetail(p) != "low" || p.EntryVariants() != 1 {
		t.Errorf("defaults: %s %s %d", cfg.EntryEngine(p), cfg.EntryDetail(p), p.EntryVariants())
	}
	if cfg.EntryDetail(g) != "high" {
		t.Errorf("the entry's own detail: %s", cfg.EntryDetail(g))
	}
	if cfg.EntryEngine(c) != "sf3d" || c.EntryVariants() != 3 || !c.Adult || *c.Seed != 7 {
		t.Errorf("an entry with everything: %+v", c)
	}
}

func TestModels3D_NoDefaultsAtAll(t *testing.T) {
	cfg, err := models3DProject(t, "project: p\noutput: {mode: models3d}\nmodels: [{id: a, prompt: x}]\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EntryEngine(cfg.Models[0]) != "triposr" || cfg.EntryDetail(cfg.Models[0]) != "normal" {
		t.Errorf("the defaults of the defaults: %s %s", cfg.EntryEngine(cfg.Models[0]), cfg.EntryDetail(cfg.Models[0]))
	}
}

func TestModels3D_Refusals(t *testing.T) {
	for _, tc := range []struct {
		name, yml, want string
		sources         []string
	}{
		{"a prompt and an image", "output: {mode: models3d}\nmodels: [{id: a, prompt: x, image: a.png}]", `model "a": prompt and image cannot both be set`, []string{"a.png"}},
		{"neither", "output: {mode: models3d}\nmodels: [{id: a}]", `model "a": needs a prompt or an image`, nil},
		{"too many variants", "output: {mode: models3d}\nmodels: [{id: a, prompt: x, variants: 9}]", `model "a": variants must be between 1 and 4`, nil},
		{"zero variants", "output: {mode: models3d}\nmodels: [{id: a, prompt: x, variants: 0}]", `variants must be between 1 and 4`, nil},
		{"unknown engine", `output: {mode: models3d}` + "\n" + `models: [{id: a, prompt: x, engine: "x"}]`, `model "a": engine "x" must be one of triposr, sf3d`, nil},
		{"unknown detail", "output: {mode: models3d}\nmodels: [{id: a, prompt: x, detail: ultra}]", `model "a": detail "ultra" must be low, normal or high`, nil},
		{"unknown default engine", "output: {mode: models3d}\ndefaults: {models3d: {engine: z}}\nmodels: [{id: a, prompt: x}]", `defaults.models3d.engine "z"`, nil},
		{"unknown default detail", "output: {mode: models3d}\ndefaults: {models3d: {detail: z}}\nmodels: [{id: a, prompt: x}]", `defaults.models3d.detail "z"`, nil},
		{"a repeated id", "output: {mode: models3d}\nmodels: [{id: a, prompt: x}, {id: a, prompt: y}]", `the id is repeated`, nil},
		{"an id that is not plain", "output: {mode: models3d}\nmodels: [{id: Mi Modelo, prompt: x}]", `id "Mi Modelo" must be a plain lower-case name`, nil},
		{"an id with a path", "output: {mode: models3d}\nmodels: [{id: ../a, prompt: x}]", `must be a plain lower-case name`, nil},
		{"an image that is not there", "output: {mode: models3d}\nmodels: [{id: a, image: falta.png}]", `image "falta.png" not found in sources`, nil},
		{"an image that is a path", "output: {mode: models3d}\nmodels: [{id: a, image: ../x.png}]", `must be the name of a .png`, nil},
		{"an image that is not a picture", "output: {mode: models3d}\nmodels: [{id: a, image: a.txt}]", `must be the name of a .png`, []string{"a.txt"}},
		{"a prompt that is too long", "output: {mode: models3d}\nmodels: [{id: a, prompt: \"" + strings.Repeat("x", 1001) + "\"}]", `prompt is longer than 1000 characters`, nil},
		{"no entries", "output: {mode: models3d}", `needs at least one entry in models`, nil},
		{"scenes in this mode", "output: {mode: models3d}\nmodels: [{id: a, prompt: x}]\nscenes: [{id: s, prompt: y, tts_text: z}]", `uses models, not scenes`, nil},
		{"tracks in this mode", "output: {mode: models3d}\nmodels: [{id: a, prompt: x}]\ntracks: [{id: t, prompt: y}]", `uses models, not tracks`, nil},
		{"models in a video project", "output: {mode: video, file: o.mp4}\nmodels: [{id: a, prompt: x}]\nscenes: [{id: s, prompt: y, tts_text: z}]", `models is only valid with output.mode "models3d"`, nil},
		{"models in an images project", "output: {mode: images}\nmodels: [{id: a, prompt: x}]\nscenes: [{id: s, prompt: y}]", `models is only valid with output.mode "models3d"`, nil},
		{"defaults in another mode", "output: {mode: images}\ndefaults: {models3d: {engine: triposr}}\nscenes: [{id: s, prompt: y}]", `defaults.models3d is only valid with output.mode "models3d"`, nil},
		{"two entries one file", "output: {mode: models3d}\nmodels: [{id: a, prompt: x, variants: 2}, {id: a-2, prompt: y}]", `both make the file "a-2.glb"`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := models3DProject(t, "project: p\n"+tc.yml, tc.sources...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to say %q", err, tc.want)
			}
		})
	}
}

func TestModels3D_TheModeIsListedWhenItIsInvalid(t *testing.T) {
	_, err := models3DProject(t, "project: p\noutput: {mode: sculpture}\n")
	if err == nil || !strings.Contains(err.Error(), `"models3d"`) {
		t.Errorf("the refusal lists the modes: %v", err)
	}
}

func TestModels3D_AnImageEntryMakesOneModelAndSaysSo(t *testing.T) {
	cfg, err := models3DProject(t, "project: p\noutput: {mode: models3d}\nmodels: [{id: a, image: a.png, variants: 3}]", "a.png")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Models[0].EntryVariants() != 1 {
		t.Errorf("variants with an image: %d", cfg.Models[0].EntryVariants())
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "variants is ignored") {
		t.Errorf("warnings = %v", cfg.Warnings)
	}
}

func TestModels3D_FileNames(t *testing.T) {
	for i, want := range []string{"patin.glb", "patin-2.glb", "patin-3.glb"} {
		if got := ModelFileName("patin", i); got != want {
			t.Errorf("variant %d: %s, want %s", i, got, want)
		}
	}
	if ModelName("patin", 0) != "patin" || ModelName("patin", 1) != "patin-2" {
		t.Errorf("names: %s %s", ModelName("patin", 0), ModelName("patin", 1))
	}
}

func TestModels3D_RoundTrip(t *testing.T) {
	cfg, err := models3DProject(t, "project: p\noutput: {mode: models3d}\ndefaults: {models3d: {detail: high}}\nmodels: [{id: a, prompt: x, variants: 2, adult: true}]")
	if err != nil {
		t.Fatal(err)
	}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	back, err := LoadBytes(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(back.Models) != 1 || back.Models[0].EntryVariants() != 2 || !back.Models[0].Adult || back.Defaults.Models3D.Detail != "high" {
		t.Errorf("after the round trip: %+v\n%s", back, out)
	}
}

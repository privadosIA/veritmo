package workspace

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/project/config"
)

const modelsDataset = `[
 ["patin","t","","a retro roller skate, glossy pink"],
 ["copa","t","","a golden trophy cup\nDetail: high\nVariants: 2\nSeed: 7"],
 ["gato","t","","Image: gato.png"],
 ["mala","t","","a lamp\nEngine: dall-e"],
 ["Mi Copa","t","","another trophy\nthis line is after nothing and is a prompt line\nDetail: low\nand this is not a setting"]
]`

type modelsPreview struct {
	YAML     string   `json:"yaml"`
	Report   string   `json:"report"`
	Warnings []string `json:"warnings"`
	Imported int      `json:"imported"`
	Rejected int      `json:"rejected"`
	Entries  []struct {
		ID        string   `json:"id"`
		Mapped    []string `json:"mapped"`
		Discarded []string `json:"discarded"`
		Rejected  string   `json:"rejected"`
	} `json:"entries"`
}

func previewModels(t *testing.T, s *testServer, body map[string]any) (modelsPreview, int) {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/import/preview", body)
	var p modelsPreview
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	return p, rec.Code
}

func TestImport_Models3DPreviewShowsTheProjectAndItsWarningsAndConfirmCreatesIt(t *testing.T) {
	s, projects := newProjectsServer(t)
	p, code := previewModels(t, s, map[string]any{"content": modelsDataset, "project": "objetos", "mode": "models3d"})
	if code != http.StatusOK || p.Imported != 4 || p.Rejected != 1 {
		t.Fatalf("%d %+v", code, p)
	}
	for _, e := range p.Entries {
		if e.ID == "mala" && !strings.Contains(e.Rejected, `line "Engine: dall-e"`) {
			t.Errorf("the bad entry is refused naming the line: %+v", e)
		}
	}
	w := strings.Join(p.Warnings, "|")
	for _, want := range []string{"Mi Copa: id \"Mi Copa\" was changed to \"mi-copa\"", "gato: Image gato.png has to be in the project's sources", `"and this is not a setting" is after the settings`} {
		if !strings.Contains(w, want) {
			t.Errorf("warnings lack %q: %v", want, p.Warnings)
		}
	}
	if !strings.Contains(p.YAML, "mode: models3d") || !strings.Contains(p.YAML, "model: models/") || !strings.Contains(p.Report, "imported 4 models, rejected 1") {
		t.Errorf("yaml and report:\n%s\n%s", p.YAML, p.Report)
	}
	// the confirmation refuses a project whose own picture is not in the sources, as a hand-written one is
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/import/confirm", map[string]any{"id": "objetos", "yaml": p.YAML})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `image \"gato.png\" not found in sources`) {
		t.Fatalf("confirm without the picture: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(projects, "objetos")); err == nil {
		t.Fatal("a refused confirmation leaves nothing behind")
	}
	// without that entry it is created, validated, with the entries the dataset gave
	p2, _ := previewModels(t, s, map[string]any{"content": `[["patin","t","","a retro roller skate"],["copa","t","","a trophy\nDetail: high\nVariants: 2\nSeed: 7"]]`, "project": "objetos", "mode": "models3d"})
	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/import/confirm", map[string]any{"id": "objetos", "yaml": p2.YAML})
	if rec.Code != http.StatusCreated {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body)
	}
	raw, err := os.ReadFile(filepath.Join(projects, "objetos", "scenes.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadBytes(raw)
	if err != nil || cfg.Output.Mode != config.ModeModels3D || len(cfg.Models) != 2 || cfg.Models[1].Detail != "high" || cfg.Models[1].EntryVariants() != 2 || *cfg.Models[1].Seed != 7 {
		t.Fatalf("%v %+v", err, cfg)
	}
}

func TestImport_Models3DModeAndOutputModeAreChecked(t *testing.T) {
	s, _ := newProjectsServer(t)
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/import/preview", map[string]any{"content": modelsDataset, "mode": "audio"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "models3d") {
		t.Errorf("an unknown mode lists the accepted ones: %d %s", rec.Code, rec.Body)
	}
	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/import/preview", map[string]any{"content": modelsDataset, "mode": "models3d", "output_mode": "images"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("output_mode images is about scenes: %d %s", rec.Code, rec.Body)
	}
}

func TestExport_Models3DDatasetRoundTrips(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "objetos")
	yml := `project: objetos
output: {mode: models3d}
defaults:
  model: models/m.safetensors
  models3d: {engine: sf3d}
models:
  - {id: patin, prompt: "a retro roller skate, glossy pink"}
  - {id: copa, prompt: "a golden trophy", detail: high, variants: 2, seed: 7}
`
	if err := s.SaveProjectDefinition("objetos", []byte(yml), "save"); err != nil {
		t.Fatal(err)
	}
	rec := call(s.Handler(), http.MethodGet, "/api/projects/objetos/export?format=dataset", "")
	if rec.Code != http.StatusOK || rec.Header().Get("X-Export-Count") != "2" {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	notes, _ := url.QueryUnescape(rec.Header().Get("X-Export-Notes"))
	if !strings.Contains(notes, "folded into each entry") || !strings.Contains(notes, "models themselves are not carried") {
		t.Errorf("notes: %s", notes)
	}
	p, code := previewModels(t, s, map[string]any{"content": rec.Body.String(), "project": "otra", "mode": "models3d"})
	if code != http.StatusOK || p.Imported != 2 || p.Rejected != 0 {
		t.Fatalf("%d %+v", code, p)
	}
	cfg, err := config.LoadBytes([]byte(p.YAML))
	if err != nil || len(cfg.Models) != 2 || cfg.Models[0].Engine != "sf3d" || cfg.Models[1].Detail != "high" || cfg.Models[1].EntryVariants() != 2 || *cfg.Models[1].Seed != 7 {
		t.Fatalf("%v %+v\n%s", err, cfg, p.YAML)
	}
}

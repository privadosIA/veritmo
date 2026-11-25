package workspace

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/project/config"
)

const models3DYAML = `project: objetos
output: {mode: models3d}
defaults:
  model: models/m.safetensors
  sampler: euler
  steps: 20
  resolution: 768x768
  models3d: {engine: triposr, detail: normal}
models:
  - {id: patin, prompt: "a retro roller skate, glossy pink"}
  - {id: copa, prompt: "a golden trophy", engine: sf3d}
  - {id: gato, image: gato.png}
`

// models3DProject creates a project in the mode models3d with its YAML.
func models3DProject(t *testing.T, s *testServer, id string) {
	t.Helper()
	createProject(t, s, id)
	if err := s.SaveProjectDefinition(id, []byte(strings.Replace(models3DYAML, "project: objetos", "project: "+id, 1)), "save"); err != nil {
		t.Fatal(err)
	}
	if rec := putSource(t, s, id, "gato.png", []byte("PNG")); rec.Code >= 300 {
		t.Fatalf("source: %d %s", rec.Code, rec.Body)
	}
}

func TestModels3DMode_TheProjectIsListedAndSavedAsThatMode(t *testing.T) {
	s, _ := newProjectsServer(t)
	models3DProject(t, s, "objetos")
	rec := call(s.Handler(), http.MethodGet, "/api/projects", "")
	var list []ProjectSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	var got *ProjectSummary
	for i := range list {
		if list[i].ID == "objetos" {
			got = &list[i]
		}
	}
	if got == nil || got.Mode != config.ModeModels3D || got.Error != "" {
		t.Fatalf("the project listing: %+v", got)
	}
	// saving the configuration validates it as a run does: an entry with neither a prompt nor an image is refused, saying which
	createProject(t, s, "malo")
	rec = doJSON(t, s.Handler(), http.MethodPut, "/api/projects/malo/config", map[string]string{"yaml": "project: malo\noutput: {mode: models3d}\nmodels: [{id: a}]\n"})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `model \"a\": needs a prompt or an image`) {
		t.Errorf("an entry with nothing to make a model from: %d %s", rec.Code, rec.Body)
	}
	rec = doJSON(t, s.Handler(), http.MethodPut, "/api/projects/malo/config", map[string]string{"yaml": models3DYAML})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `image \"gato.png\" not found in sources`) {
		t.Errorf("an own picture that is not in the sources: %d %s", rec.Code, rec.Body)
	}
}

func TestModels3DMode_TheHistoryAndTheStatisticsFilterByTheMode(t *testing.T) {
	s, _ := newProjectsServer(t)
	models3DProject(t, s, "objetos")
	createProject(t, s, "vid")
	addRun(t, s, "r-m3d", "objetos", "out/objetos/manifest.json")
	addRun(t, s, "r-vid", "vid", "out/vid.mp4")
	rec := call(s.Handler(), http.MethodGet, "/api/runs?mode=models3d", "")
	var runs []RunView
	if err := json.Unmarshal(rec.Body.Bytes(), &runs); err != nil || len(runs) != 1 || runs[0].ID != "r-m3d" {
		t.Fatalf("runs of the mode: %v %s", err, rec.Body)
	}
	rec = call(s.Handler(), http.MethodGet, "/api/runs?mode=models3d,video", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &runs); err != nil || len(runs) != 2 {
		t.Errorf("a list of modes: %v %d", err, len(runs))
	}
	st, _ := getStats(t, s, wide+"&mode=models3d")
	if st.Totals.Runs != 1 || st.Mode != "models3d" || len(st.ByMode) != 1 || st.ByMode[0].Name != "models3d" {
		t.Errorf("stats by mode: %d %q %+v", st.Totals.Runs, st.Mode, st.ByMode)
	}
	// the modes are written out in the 400 of a mode that is none
	for _, path := range []string{"/api/runs?mode=lava", "/api/stats" + wide + "&mode=lava"} {
		rec := call(s.Handler(), http.MethodGet, path, "")
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "models3d") {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
}

func TestModels3DMode_AResultIsAModelFileOfTheOutputFolder(t *testing.T) {
	s, dir := newProjectsServer(t)
	models3DProject(t, s, "objetos")
	out := filepath.Join(dir, "objetos", "out", "objetos")
	os.MkdirAll(out, 0o755)
	os.WriteFile(filepath.Join(out, "manifest.json"), []byte("{}"), 0o644)
	addRun(t, s, "r-m3d", "objetos", "out/objetos/manifest.json")
	if available(t, s, "r-m3d") {
		t.Error("a manifest with no model is not a result")
	}
	os.WriteFile(filepath.Join(out, "patin.glb"), []byte("glTF"), 0o644)
	if !available(t, s, "r-m3d") {
		t.Error("the model is there")
	}
	os.Remove(filepath.Join(out, "patin.glb"))
	if available(t, s, "r-m3d") {
		t.Error("the model was removed")
	}
}

func TestModels3DMode_TheStorageCountsTheModelsWithTheOutput(t *testing.T) {
	s, dir := newProjectsServer(t)
	models3DProject(t, s, "objetos")
	out := filepath.Join(dir, "objetos", "out", "objetos")
	os.MkdirAll(out, 0o755)
	os.WriteFile(filepath.Join(out, "patin.glb"), bytes.Repeat([]byte("x"), 2000), 0o644)
	os.WriteFile(filepath.Join(out, "copa.glb"), bytes.Repeat([]byte("x"), 3000), 0o644)
	rec := call(s.Handler(), http.MethodGet, "/api/projects/objetos/storage", "")
	var body struct {
		Parts []struct {
			ID    string `json:"id"`
			Bytes int64  `json:"bytes"`
			Files int    `json:"files"`
		} `json:"parts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%v %s", err, rec.Body)
	}
	for _, p := range body.Parts {
		if p.ID == "final" && (p.Bytes != 5000 || p.Files != 2) {
			t.Errorf("the output of the project: %+v", p)
		}
	}
}

func TestModels3DMode_TheRecipeNamesTheEnginesAndTheirWeights(t *testing.T) {
	s, projects := newProjectsServer(t)
	models3DProject(t, s, "objetos")
	if err := s.MaterializeProject("objetos"); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteProjectPackage(catalogForTest(t), &buf, filepath.Join(projects, "objetos"), s.assetsRoot, "en"); err != nil {
		t.Fatal(err)
	}
	pc, err := readPackage(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil || pc.recipe == nil {
		t.Fatalf("recipe: %v", err)
	}
	r := pc.recipe
	m := r.Settings.Models3D
	if r.Mode != config.ModeModels3D || r.Models != 3 || m == nil || len(m.Engines) != 2 || m.Engines[0].ID != "triposr" || m.Engines[1].ID != "sf3d" || !m.Engines[1].Gated || m.Engines[0].Licence != "MIT" {
		t.Fatalf("the engines: %+v mode %s models %d", m, r.Mode, r.Models)
	}
	files := map[string]recipeFile{}
	for _, f := range r.Files {
		files[f.Path] = f
	}
	for _, want := range []string{"models/model3d/triposr/model.ckpt", "models/model3d/sf3d/model.safetensors", "models/model3d/sf3d/dinov2-large-config.json", "models/model3d/rembg/u2net.onnx"} {
		f, ok := files[want]
		if !ok || f.Installed || f.Note == "" {
			t.Errorf("%s: %+v", want, f)
		}
	}
	cutouts := 0
	for _, f := range r.Files {
		if f.Path == "models/model3d/rembg/u2net.onnx" {
			cutouts++
		}
	}
	if cutouts != 1 {
		t.Errorf("the cut-out model is shared by the engines and listed once: %d", cutouts)
	}
	md := string(r.markdown("en"))
	for _, want := range []string{"3D engine", "TripoSR · MIT", "Stable Fast 3D", "licence", "POST /api/models3d/engines/sf3d/install", "models/model3d/triposr/model.ckpt"} {
		if !strings.Contains(md, want) {
			t.Errorf("RECIPE.md lacks %q:\n%s", want, md)
		}
	}
}

func TestModels3DMode_ThePackageComesBackWithItsEntriesAndPictures(t *testing.T) {
	s, projects := newProjectsServer(t)
	models3DProject(t, s, "objetos")
	rec := call(s.Handler(), http.MethodGet, "/api/projects/objetos/export?format=zip&lang=en", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d %s", rec.Code, rec.Body)
	}
	pkg := rec.Body.Bytes()
	pre := post(s.Handler(), "/api/import/package/preview?lang=en", pkg)
	var rep packageReport
	if pre.Code != http.StatusOK || json.Unmarshal(pre.Body.Bytes(), &rep) != nil || rep.Sources != 1 {
		t.Fatalf("preview: %d %s", pre.Code, pre.Body)
	}
	conf := post(s.Handler(), "/api/import/package/confirm?id=otra", pkg)
	if conf.Code != http.StatusCreated {
		t.Fatalf("confirm: %d %s", conf.Code, conf.Body)
	}
	raw, err := os.ReadFile(filepath.Join(projects, "otra", "scenes.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadBytes(raw)
	if err != nil || cfg.Output.Mode != config.ModeModels3D || len(cfg.Models) != 3 || cfg.Models[2].Image != "gato.png" || cfg.Defaults.Models3D == nil {
		t.Fatalf("the imported project: %v %+v", err, cfg)
	}
	if b, err := os.ReadFile(filepath.Join(projects, "otra", "sources", "gato.png")); err != nil || string(b) != "PNG" {
		t.Errorf("the picture of an entry travels with the package: %v", err)
	}
}

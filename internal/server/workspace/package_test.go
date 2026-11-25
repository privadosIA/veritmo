package workspace

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"veritmo/internal/video/motion"
)

const packageYAML = `project: Chicas Patines
output: {mode: video, file: out/chicas.mp4}
defaults:
  voice: voices/v.onnx
  resolution: 1024x576
  sampler: euler
  steps: 6
  fps: 24
  model_set:
    diffusion_model: models/krea2_turbo-Q4_K_M.gguf
    llm: models/Qwen3VL-4B-Instruct-Q4_K_M.gguf
    vae: models/wan_2.1_vae.safetensors
    cfg_scale: 1
  animate:
    engine: minimax-h3
    diffusion_model: models/video/minimax_h3_ref2va_pruned-Q4_K_M.gguf
    llm: models/video/qwen3vl_32b_minimax_h3-Q4_K_M.gguf
    vae: models/video/minimax_h3_video_vae_fp16.safetensors
    audio_vae: models/video/minimax_h3_audio_vae_fp32.safetensors
    lora: models/video/minimax_h3_ref2v_turbo_4step.safetensors
scenes:
  - id: s1
    prompt: two friends
    silence_seconds: 2
    animate: true
    image: me.png
`

// packageZip builds a package the way a client uploads it.
func packageZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range files {
		f, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(data))
	}
	zw.Close()
	return buf.Bytes()
}

func post(h http.Handler, path string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRecipe_ListsEveryFileTheCatalogKnowsAndComparesThePreset(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "demo")
	if err := s.SaveProjectDefinition("demo", []byte(packageYAML), "save"); err != nil {
		t.Fatal(err)
	}
	if err := s.MaterializeProject("demo"); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteProjectPackage(catalogForTest(t), &buf, filepath.Join(projects, "demo"), s.assetsRoot, "en"); err != nil {
		t.Fatal(err)
	}
	pc, err := readPackage(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil || pc.recipe == nil {
		t.Fatalf("recipe missing: %v", err)
	}
	r := pc.recipe
	got := map[string]recipeFile{}
	for _, f := range r.Files {
		got[f.Key] = f
	}
	for key, name := range map[string]string{
		"defaults.model_set.diffusion_model": "krea2_turbo-Q4_K_M.gguf",
		"defaults.animate.lora":              "minimax_h3_ref2v_turbo_4step.safetensors",
		"defaults.animate.audio_vae":         "minimax_h3_audio_vae_fp32.safetensors",
		"defaults.voice":                     "v.onnx",
	} {
		if got[key].Name != name {
			t.Errorf("%s = %q, want %q (files %v)", key, got[key].Name, name, sortedFileNames(r.Files))
		}
	}
	if got["defaults.animate.lora"].CatalogID != "video-minimax-h3-ref2va-q4" || got["defaults.animate.lora"].URL == "" {
		t.Errorf("the LoRA should come from the MiniMax bundle: %+v", got["defaults.animate.lora"])
	}
	if !got["defaults.voice"].Installed || got["defaults.model_set.diffusion_model"].Installed {
		t.Errorf("installed flags wrong: voice %v krea %v", got["defaults.voice"].Installed, got["defaults.model_set.diffusion_model"].Installed)
	}
	// 6 steps against the 8 of Krea 2 Turbo's preset, and the animation's turbo LoRA (4 steps against 20)
	var image, anim *recipePreset
	for i := range r.Presets {
		switch r.Presets[i].For {
		case "Image model":
			image = &r.Presets[i]
		case "Animation":
			anim = &r.Presets[i]
		}
	}
	if image == nil || image.Matches || !strings.Contains(strings.Join(image.Differences, ","), "steps 8 → 6") {
		t.Errorf("image preset = %+v", image)
	}
	if anim == nil || anim.Used.Steps != 4 || anim.Matches {
		t.Errorf("animation preset = %+v", anim)
	}
	if !r.Settings.Animate.Turbo || r.Settings.Animate.Engine != "minimax-h3" || r.Settings.Steps != 6 {
		t.Errorf("settings = %+v", r.Settings)
	}
	md := string(r.markdown("en"))
	for _, want := range []string{"# Project recipe: Chicas Patines", "krea2_turbo-Q4_K_M.gguf", "turbo LoRA", "steps 8 → 6"} {
		if !strings.Contains(md, want) {
			t.Errorf("RECIPE.md lacks %q:\n%s", want, md)
		}
	}
	if !strings.Contains(string(r.markdown("es")), "# Receta del proyecto") {
		t.Error("Spanish recipe not written")
	}
}

func TestPackageImport_ReportsWhatIsMissingAndCreatesTheProject(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "demo")
	if err := s.SaveProjectDefinition("demo", []byte(packageYAML), "save"); err != nil {
		t.Fatal(err)
	}
	putSource(t, s, "demo", "me.png", []byte("PNG"))
	rec := call(s.Handler(), http.MethodGet, "/api/projects/demo/export?format=zip&lang=es", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d", rec.Code)
	}
	pkg := rec.Body.Bytes()

	pre := post(s.Handler(), "/api/import/package/preview?lang=es", pkg)
	if pre.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", pre.Code, pre.Body)
	}
	var rep packageReport
	if err := json.Unmarshal(pre.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.SuggestedID != "Chicas-Patines" || rep.IDTaken || rep.Sources != 1 || rep.Scenes != 1 {
		t.Errorf("report = %+v", rep)
	}
	if rep.Ready || len(rep.Install) != 2 {
		t.Fatalf("expected the Krea 2 and MiniMax bundles to install: %+v", rep.Install)
	}
	for _, g := range rep.Install {
		if g.CatalogID == "video-minimax-h3-ref2va-q4" && len(g.Missing) != 5 {
			t.Errorf("MiniMax missing = %v", g.Missing)
		}
	}

	conf := post(s.Handler(), "/api/import/package/confirm?id=chicas", pkg)
	if conf.Code != http.StatusCreated {
		t.Fatalf("confirm: %d %s", conf.Code, conf.Body)
	}
	if b, err := os.ReadFile(filepath.Join(projects, "chicas", "scenes.yaml")); err != nil || !strings.Contains(string(b), "minimax-h3") {
		t.Errorf("scenes.yaml not written: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(projects, "chicas", "sources", "me.png")); err != nil || string(b) != "PNG" {
		t.Errorf("source not written: %v", err)
	}
	if code := post(s.Handler(), "/api/import/package/confirm?id=chicas", pkg).Code; code != http.StatusConflict {
		t.Errorf("second import: %d", code)
	}
}

func TestPackageImport_RefusesWhatIsNotAPackageAndIgnoresUnsafePaths(t *testing.T) {
	s, projects := newProjectsServer(t)
	if code := post(s.Handler(), "/api/import/package/preview", []byte("not a zip")).Code; code != http.StatusUnprocessableEntity {
		t.Errorf("not a zip: %d", code)
	}
	if code := post(s.Handler(), "/api/import/package/preview", packageZip(t, map[string]string{"README.txt": "x"})).Code; code != http.StatusUnprocessableEntity {
		t.Errorf("no scenes.yaml: %d", code)
	}
	evil := packageZip(t, map[string]string{"scenes.yaml": exportYAML, "../evil.txt": "x"})
	if code := post(s.Handler(), "/api/import/package/preview", evil).Code; code != http.StatusUnprocessableEntity {
		t.Errorf("unsafe path: %d", code)
	}
	// files outside scenes.yaml, recipe.json and sources/ are not written
	ok := packageZip(t, map[string]string{"scenes.yaml": exportYAML, "other/x.txt": "x", "sources/nested/y.png": "y", "sources/z.png": "z"})
	if code := post(s.Handler(), "/api/import/package/confirm?id=ok", ok).Code; code != http.StatusCreated {
		t.Fatalf("confirm: %d", code)
	}
	if _, err := os.Stat(filepath.Join(projects, "ok", "other")); err == nil {
		t.Error("an unlisted folder was written")
	}
	if _, err := os.Stat(filepath.Join(projects, "ok", "sources", "z.png")); err != nil {
		t.Error("sources/z.png missing")
	}
	if _, err := os.Stat(filepath.Join(projects, "ok", "sources", "nested")); err == nil {
		t.Error("a nested sources folder was written")
	}
}

func TestPackageImport_ReportsASceneFileThePackageDoesNotCarry(t *testing.T) {
	s, _ := newProjectsServer(t)
	yaml := "project: lacking\noutput: {mode: video, file: out/l.mp4}\ndefaults:\n  voice: voices/v.onnx\n  model: models/m.safetensors\n  resolution: 512x512\n  sampler: euler\n  steps: 4\n  fps: 24\nscenes:\n  - id: a\n    image: mine.png\n    silence_seconds: 1\n"
	pre := post(s.Handler(), "/api/import/package/preview", packageZip(t, map[string]string{"scenes.yaml": yaml}))
	var rep packageReport
	if pre.Code != http.StatusOK || json.Unmarshal(pre.Body.Bytes(), &rep) != nil {
		t.Fatalf("%d %s", pre.Code, pre.Body)
	}
	if rep.Ready || len(rep.Problems) == 0 || !strings.Contains(strings.Join(rep.Problems, " "), "mine.png") {
		t.Errorf("a scene picture missing from the package must be reported: %+v", rep.Problems)
	}
	// with the picture inside, nothing is left to do
	pre = post(s.Handler(), "/api/import/package/preview", packageZip(t, map[string]string{"scenes.yaml": yaml, "sources/mine.png": "PNG"}))
	json.Unmarshal(pre.Body.Bytes(), &rep)
	if !rep.Ready || len(rep.Problems) != 0 {
		t.Errorf("complete package: ready %v problems %v install %v unknown %v", rep.Ready, rep.Problems, rep.Install, rep.Unknown)
	}
}

func TestPackage_CarriesThePagesOfMotionGraphicsAndTheirDetails(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "demo")
	yaml := "project: Motion\noutput: {mode: video, file: out/m.mp4}\ndefaults: {voice: voices/v.onnx, resolution: 1280x720, sampler: euler, steps: 6, fps: 24}\n" +
		"scenes:\n  - {id: gfx, motion_graphic: tres ideas, silence_seconds: 2}\n"
	if err := s.SaveProjectDefinition("demo", []byte(yaml), "save"); err != nil {
		t.Fatal(err)
	}
	putPage(t, s, "gfx", "<html>page</html>", &motion.Meta{Kind: "motion_graphic", Model: "writer:7b"})
	os.MkdirAll(filepath.Join(projects, "demo", "motion"), 0o755)
	os.WriteFile(filepath.Join(projects, "demo", "motion", "notes.txt"), []byte("not a page"), 0o644)

	rec := call(s.Handler(), http.MethodGet, "/api/projects/demo/export?format=zip&lang=en", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d", rec.Code)
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "motion/gfx.html") || !strings.Contains(joined, "motion/gfx.meta.json") || strings.Contains(joined, "notes.txt") {
		t.Fatalf("the package carries the pages and their details and nothing else of motion/: %v", names)
	}

	conf := post(s.Handler(), "/api/import/package/confirm?id=copy", rec.Body.Bytes())
	if conf.Code != http.StatusCreated {
		t.Fatalf("confirm: %d %s", conf.Code, conf.Body)
	}
	if b, err := os.ReadFile(filepath.Join(projects, "copy", "motion", "gfx.html")); err != nil || string(b) != "<html>page</html>" {
		t.Errorf("page not imported: %v %q", err, b)
	}
	if b, err := os.ReadFile(filepath.Join(projects, "copy", "motion", "gfx.meta.json")); err != nil || !strings.Contains(string(b), "writer:7b") {
		t.Errorf("details not imported: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projects, "copy", "motion", "notes.txt")); err == nil {
		t.Error("only pages and details are imported from motion/")
	}
	// a package cannot put anything outside motion/ through that folder
	evil := packageZip(t, map[string]string{"scenes.yaml": yaml, "motion/../scenes.yaml.bak": "x", "motion/sub/dir.html": "x"})
	if rec := post(s.Handler(), "/api/import/package/confirm?id=evil", evil); rec.Code != http.StatusCreated {
		t.Fatalf("an unsafe entry is ignored, not fatal: %d %s", rec.Code, rec.Body)
	}
	if entries, _ := os.ReadDir(filepath.Join(projects, "evil", "motion")); len(entries) != 0 {
		t.Errorf("nothing from the unsafe entries is written: %v", entries)
	}
}

// A page that declares a timeline travels in the package as any other page: byte for byte, with its
// timeline block, and the dataset format (prompts only) keeps leaving motion scenes out with a note.
func TestPackage_APageWithATimelineIsImportedUnchanged(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "demo")
	yaml := "project: Motion\noutput: {mode: video, file: out/m.mp4}\ndefaults: {voice: voices/v.onnx, resolution: 1280x720, sampler: euler, steps: 6, fps: 24}\n" +
		"scenes:\n  - {id: gfx, motion_graphic: tres ideas, silence_seconds: 3}\n  - {id: foto, prompt: a street, tts_text: hola}\n"
	page := "<!doctype html><html><body><h1>Hola</h1>\n<script type=\"application/json\" id=\"motion-timeline\">\n" +
		`{"version":1,"tracks":[` + "\n" + `{"id":"title","label":"Título","target":"h1","start":0.5,"duration":1,"ease":"out","from":{"opacity":0,"y":30},"to":{"opacity":1,"y":0}}` + "\n]}\n</script></body></html>\n"
	if err := s.SaveProjectDefinition("demo", []byte(yaml), "save"); err != nil {
		t.Fatal(err)
	}
	putPage(t, s, "gfx", page, nil)

	rec := call(s.Handler(), http.MethodGet, "/api/projects/demo/export?format=zip&lang=en", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d", rec.Code)
	}
	if conf := post(s.Handler(), "/api/import/package/confirm?id=copy", rec.Body.Bytes()); conf.Code != http.StatusCreated {
		t.Fatalf("confirm: %d %s", conf.Code, conf.Body)
	}
	got, err := os.ReadFile(filepath.Join(projects, "copy", "motion", "gfx.html"))
	if err != nil || string(got) != page {
		t.Fatalf("the imported page must be identical (error %v):\n%q", err, got)
	}
	if !strings.Contains(string(got), `id="motion-timeline"`) || !strings.Contains(string(got), `"label":"Título"`) {
		t.Error("the timeline block must still be there")
	}

	ds := call(s.Handler(), http.MethodGet, "/api/projects/demo/export?format=dataset", "")
	if ds.Code != http.StatusOK {
		t.Fatalf("dataset: %d %s", ds.Code, ds.Body)
	}
	// The dataset carries the description of the graphic, never its page.
	if strings.Contains(ds.Body.String(), "motion-timeline") || !strings.Contains(ds.Body.String(), "Graphic: tres ideas") {
		t.Errorf("the dataset carries the description of the graphic, not its page: %s", ds.Body)
	}
	if notes, _ := url.QueryUnescape(ds.Header().Get("X-Export-Notes")); !strings.Contains(notes, "pages of the animated graphics") {
		t.Errorf("the notes say the pages are not carried: %q", notes)
	}
}

// A package of 100 MB is read without holding it in memory: the upload is spooled to a file and the sources stay in
// the zip until they are copied to the project.
func TestPackage_BigSourcesAreNotHeldInMemory(t *testing.T) {
	s, projects := newProjectsServer(t)
	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	f, _ := zw.Create("scenes.yaml")
	f.Write([]byte(packageYAML))
	w, _ := zw.CreateHeader(&zip.FileHeader{Name: "sources/base.mp3", Method: zip.Store})
	chunk := bytes.Repeat([]byte{3}, 1<<20)
	for i := 0; i < 100; i++ {
		w.Write(chunk)
	}
	zw.Close()
	body := zbuf.Bytes()

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	rec := post(s.Handler(), "/api/import/package/confirm?id=big", body)
	runtime.ReadMemStats(&after)
	if rec.Code != http.StatusCreated {
		t.Fatalf("confirm: status %d (%s)", rec.Code, rec.Body.String())
	}
	if grew := int64(after.TotalAlloc - before.TotalAlloc); grew > 60<<20 {
		t.Errorf("the import allocated %d MB for a 100 MB package, want far less than its size", grew>>20)
	}
	got, err := os.Stat(filepath.Join(projects, "big", "sources", "base.mp3"))
	if err != nil || got.Size() != 100<<20 {
		t.Fatalf("the source was not copied whole: %v %v", got, err)
	}
}

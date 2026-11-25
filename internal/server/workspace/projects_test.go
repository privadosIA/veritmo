package workspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"veritmo/internal/platform/cache"
	"veritmo/internal/project/config"
	"veritmo/internal/project/pipeline"
)

// newProjectsServer builds the workspace over a temp projects root and chdirs into a temp "repo root" holding
// models/ and voices/ so shared-asset linking and validation behave as in production.
func newProjectsServer(t *testing.T) (*testServer, string) {
	t.Helper()
	return newProjectsServerSampling(t, SamplerConfig{})
}

// newProjectsServerSampling is newProjectsServer with the sampler of the runs given (a field left out is the quiet
// default: no card, a fixed memory).
func newProjectsServerSampling(t *testing.T, sc SamplerConfig) (*testServer, string) {
	t.Helper()
	repo := t.TempDir()
	for _, d := range []string{"models", "voices"} {
		if err := os.Mkdir(filepath.Join(repo, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "models", "m.safetensors"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"v.onnx", "v.onnx.json"} { // Piper needs the voice's companion config
		if err := os.WriteFile(filepath.Join(repo, "voices", v), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	catalog := catalogForTest(t) // read before the working directory changes
	t.Chdir(repo)

	projects := filepath.Join(repo, "projects")
	s := newTestServerWith(t, Config{ProjectsDir: projects, DBPath: filepath.Join(repo, "o.db"), Sampler: sc})
	s.catalog = catalog
	return s, projects
}

func createProject(t *testing.T, s *testServer, id string) {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects", map[string]string{"id": id})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %s: status %d: %s", id, rec.Code, rec.Body.String())
	}
}

func TestProjects_ListIgnoresNonProjects(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "demo")
	createProject(t, s, "trailer")
	if err := os.MkdirAll(filepath.Join(projects, "not-a-project"), 0o755); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/projects", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var list []ProjectSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d projects, want 2: %+v", len(list), list)
	}
	for _, p := range list {
		if p.Mode != "video" || p.SceneCount != 1 {
			t.Errorf("project %s: mode=%s scenes=%d, want video/1", p.ID, p.Mode, p.SceneCount)
		}
	}
}

func TestProjects_CreateStarterPassesValidation(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "demo")

	dir := filepath.Join(projects, "demo")
	cfg, err := config.Load(filepath.Join(dir, "scenes.yaml"))
	if err != nil {
		t.Fatalf("starter does not parse: %v", err)
	}
	if err := cfg.ValidateInDir(dir); err != nil {
		t.Fatalf("starter does not validate: %v", err)
	}
	// Shared assets are linked so project-relative model/voice paths work.
	if _, err := os.Stat(filepath.Join(dir, "models", "m.safetensors")); err != nil {
		t.Errorf("models link missing: %v", err)
	}

	// Collision rejected.
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects", map[string]string{"id": "demo"})
	if rec.Code != http.StatusConflict {
		t.Errorf("duplicate create: status %d, want 409", rec.Code)
	}
	// Unsafe id rejected.
	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/projects", map[string]string{"id": "../evil"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unsafe id: status %d, want 400", rec.Code)
	}
}

func TestProjects_DuplicateSkipsWorkDirs(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "demo")
	// Populate work dirs on the source.
	for _, d := range []string{"tmp", "out"} {
		if err := os.MkdirAll(filepath.Join(projects, "demo", d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(projects, "demo", d, "x"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/demo/duplicate", map[string]string{"new_id": "copy"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("duplicate: %d %s", rec.Code, rec.Body.String())
	}
	src, _ := os.ReadFile(filepath.Join(projects, "demo", "scenes.yaml"))
	dst, _ := os.ReadFile(filepath.Join(projects, "copy", "scenes.yaml"))
	if !bytes.Equal(src, dst) {
		t.Error("duplicated scenes.yaml differs from source")
	}
	for _, d := range []string{"tmp", "out"} {
		if _, err := os.Stat(filepath.Join(projects, "copy", d)); !os.IsNotExist(err) {
			t.Errorf("%s/ was copied, want absent", d)
		}
	}
}

func TestProjects_DeleteBlockedWhileBusy(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "demo")

	s.busy = func(id string) (string, bool) {
		if id == "demo" {
			return "run-42", true
		}
		return "", false
	}
	rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/projects/demo", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("busy delete: status %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "run-42") {
		t.Errorf("busy error should name the run: %s", rec.Body.String())
	}

	s.busy = nil
	rec = doJSON(t, s.Handler(), http.MethodDelete, "/api/projects/demo", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: status %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(projects, "demo")); !os.IsNotExist(err) {
		t.Error("project dir still exists after delete")
	}
}

func TestProjects_ConfigPut_InvalidLeavesFileUntouched(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "demo")
	before, err := os.ReadFile(filepath.Join(projects, "demo", "scenes.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	// Duplicate ids + tts/silence conflict + empty prompt.
	bad := `
project: demo
output: {mode: video, file: out/demo.mp4}
scenes:
  - id: a
    prompt: ok
    tts_text: hola
    silence_seconds: 2.0
  - id: a
    prompt: ""
    tts_text: x
`
	rec := doJSON(t, s.Handler(), http.MethodPut, "/api/projects/demo/config", map[string]string{"yaml": bad})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid save: status %d, want 422 (%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, frag := range []string{"duplicate scene id", "mutually exclusive", "empty prompt"} {
		if !strings.Contains(body, frag) {
			t.Errorf("validation message should mention %q: %s", frag, body)
		}
	}
	after, _ := os.ReadFile(filepath.Join(projects, "demo", "scenes.yaml"))
	if !bytes.Equal(before, after) {
		t.Error("scenes.yaml changed on an invalid save")
	}
}

func TestProjects_ConfigPut_ValidRoundTrips(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "demo")

	good := `project: demo
output:
  mode: video
  file: out/demo.mp4
defaults:
  model: models/m.safetensors
  voice: voices/v.onnx
  resolution: 1280x720
  sampler: euler
  steps: 30
  fps: 30
scenes:
  - id: intro
    prompt: una ciudad al amanecer
    tts_text: Bienvenidos
    speed: 1.0
`
	rec := doJSON(t, s.Handler(), http.MethodPut, "/api/projects/demo/config", map[string]string{"yaml": good})
	if rec.Code != http.StatusOK {
		t.Fatalf("valid save: status %d (%s)", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s.Handler(), http.MethodGet, "/api/projects/demo/config", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get config: status %d", rec.Code)
	}
	var resp struct {
		YAML   string        `json:"yaml"`
		Valid  bool          `json:"valid"`
		Parsed config.Config `json:"parsed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.YAML != good {
		t.Errorf("returned YAML differs from what was saved")
	}
	if !resp.Valid {
		t.Error("saved config reported invalid")
	}
	if len(resp.Parsed.Scenes) != 1 || resp.Parsed.Scenes[0].ID != "intro" {
		t.Errorf("parsed form wrong: %+v", resp.Parsed)
	}
}

func TestProjects_CacheReport_MotionChangeInvalidatesOnlyClip(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "demo")
	dir := filepath.Join(projects, "demo")

	yamlTmpl := `project: demo
output: {mode: video, file: out/demo.mp4}
defaults:
  model: models/m.safetensors
  voice: voices/v.onnx
  resolution: 1280x720
  sampler: euler
  steps: 30
  fps: 30
scenes:
  - id: intro
    prompt: ciudad
    tts_text: hola
    speed: 1.0
%s`
	base := fmt.Sprintf(yamlTmpl, "")
	withMotion := fmt.Sprintf(yamlTmpl, "    motion: {type: zoom_in, intensity: 0.5}\n")

	rec := doJSON(t, s.Handler(), http.MethodPut, "/api/projects/demo/config", map[string]string{"yaml": base})
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}

	// Simulate a kept cache manifest from a completed run of `base`.
	cfg, err := config.LoadBytes([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateInDir(dir); err != nil {
		t.Fatal(err)
	}
	scene := cfg.Scenes[0]
	wav, png, mp4 := cache.ArtifactPaths(scene.ID)
	manifest := &cache.Manifest{Scenes: map[string]cache.SceneEntry{
		scene.ID: {
			AudioHash: cache.AudioHash(cache.AudioParams{Text: scene.TTSText, Speed: scene.Speed, Voice: cfg.Defaults.Voice}),
			ImageHash: cache.ImageHash(pipeline.SceneImageParams(cfg, scene, scene.ResolvedSeed(), 0)),
			ClipHash:  cache.ClipHash(pipeline.SceneClipParams(cfg, scene, "")),
			WavPath:   wav, PngPath: png, Mp4Path: mp4,
		},
	}}
	if err := os.MkdirAll(filepath.Join(dir, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveManifest(filepath.Join(dir, cache.ManifestPath()), manifest); err != nil {
		t.Fatal(err)
	}

	// Unchanged config: everything cached.
	rec = doJSON(t, s.Handler(), http.MethodGet, "/api/projects/demo/cache", nil)
	var rep CacheReport
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Cold {
		t.Error("report says cold with a manifest present")
	}
	ph := rep.Scenes[0].Phases
	if ph["tts"] != PhaseHit || ph["render"] != PhaseHit || ph["clip"] != PhaseHit {
		t.Errorf("unchanged config: phases = %v, want all hit", ph)
	}

	// Motion-only edit: clip re-runs, tts and render stay cached.
	rec = doJSON(t, s.Handler(), http.MethodPut, "/api/projects/demo/config", map[string]string{"yaml": withMotion})
	if rec.Code != http.StatusOK {
		t.Fatalf("save motion: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s.Handler(), http.MethodGet, "/api/projects/demo/cache", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	ph = rep.Scenes[0].Phases
	if ph["tts"] != PhaseHit || ph["render"] != PhaseHit {
		t.Errorf("motion edit: tts/render = %s/%s, want hit/hit", ph["tts"], ph["render"])
	}
	if ph["clip"] != PhaseRun {
		t.Errorf("motion edit: clip = %s, want run", ph["clip"])
	}
}

func TestProjects_CacheReport_NoManifestMeansCold(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "demo")

	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/projects/demo/cache", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var rep CacheReport
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if !rep.Cold {
		t.Error("report should be cold without a manifest")
	}
	for _, sc := range rep.Scenes {
		for phase, state := range sc.Phases {
			if state == PhaseHit {
				t.Errorf("cold report has a hit (%s/%s)", sc.SceneID, phase)
			}
		}
	}
}

func TestProjects_CacheReport_AnimatedSceneReportsAnimatePhase(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "demo")
	dir := filepath.Join(projects, "demo")
	for _, f := range []string{"wan.gguf", "umt5.gguf", "vae.safetensors"} {
		if err := os.WriteFile(filepath.Join(dir, "models", f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	yamlTmpl := `project: demo
output: {mode: video, file: out/demo.mp4}
defaults:
  model: models/m.safetensors
  voice: voices/v.onnx
  resolution: 1280x720
  sampler: euler
  steps: 30
  fps: 30
  animate:
    diffusion_model: models/wan.gguf
    t5xxl: models/umt5.gguf
    vae: models/vae.safetensors
scenes:
  - id: intro
    prompt: ciudad
    tts_text: hola
    speed: 1.0
    animate: true
    motion_prompt: %s
  - id: plain
    prompt: mar
    tts_text: adios
    speed: 1.0
`
	save := func(motion string) {
		rec := doJSON(t, s.Handler(), http.MethodPut, "/api/projects/demo/config", map[string]string{"yaml": fmt.Sprintf(yamlTmpl, motion)})
		if rec.Code != http.StatusOK {
			t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
		}
	}
	save("coches")

	cfg, err := config.LoadBytes([]byte(fmt.Sprintf(yamlTmpl, "coches")))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateInDir(dir); err != nil {
		t.Fatal(err)
	}
	manifest := &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
	for _, scene := range cfg.Scenes {
		wav, png, mp4 := cache.ArtifactPaths(scene.ID)
		e := cache.SceneEntry{
			AudioHash: cache.AudioHash(cache.AudioParams{Text: scene.TTSText, Speed: scene.Speed, Voice: cfg.Defaults.Voice}),
			ImageHash: cache.ImageHash(pipeline.SceneImageParams(cfg, scene, scene.ResolvedSeed(), 0)),
			ClipHash:  cache.ClipHash(pipeline.SceneClipParams(cfg, scene, "")),
			WavPath:   wav, PngPath: png, Mp4Path: mp4,
		}
		if scene.Animate {
			e.AnimateHash = cache.AnimateHash(pipeline.SceneAnimateParams(cfg, scene, ""))
		}
		manifest.Scenes[scene.ID] = e
	}
	if err := os.MkdirAll(filepath.Join(dir, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveManifest(filepath.Join(dir, cache.ManifestPath()), manifest); err != nil {
		t.Fatal(err)
	}

	report := func() map[string]map[string]string {
		rec := doJSON(t, s.Handler(), http.MethodGet, "/api/projects/demo/cache", nil)
		var rep CacheReport
		if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
			t.Fatal(err)
		}
		out := map[string]map[string]string{}
		for _, sc := range rep.Scenes {
			out[sc.SceneID] = sc.Phases
		}
		return out
	}

	r := report()
	if r["intro"]["animate"] != PhaseHit || r["intro"]["clip"] != PhaseHit {
		t.Errorf("unchanged: intro = %v, want animate and clip hit", r["intro"])
	}
	if _, ok := r["plain"]["animate"]; ok {
		t.Errorf("non-animated scene must not report animate: %v", r["plain"])
	}

	save("pajaros")
	r = report()
	if r["intro"]["render"] != PhaseHit || r["intro"]["animate"] != PhaseRun || r["intro"]["clip"] != PhaseRun {
		t.Errorf("motion prompt edit: intro = %v, want render hit, animate and clip run", r["intro"])
	}
}

func TestProjects_DeleteTakesItsRunHistoryWithIt(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "gone")
	createProject(t, s, "stays")
	when := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	seedRun(t, s, "run-gone-1", "gone", StateSucceeded, when, 3)
	seedRun(t, s, "run-gone-2", "gone", StateFailed, when, 2)
	seedRun(t, s, "run-stays", "stays", StateSucceeded, when, 4)

	if rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/projects/gone", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	for _, id := range []string{"run-gone-1", "run-gone-2"} {
		if runExists(s, id) || eventCount(t, s, id) != 0 {
			t.Errorf("%s and its events must be gone with the project", id)
		}
	}
	if !runExists(s, "run-stays") || eventCount(t, s, "run-stays") != 4 {
		t.Error("another project's history must stay")
	}
	// the Statistics no longer count the deleted project
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/stats?since=2026-09-01T00:00:00Z&until=2026-11-01T00:00:00Z", nil)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"gone"`) {
		t.Errorf("stats still mention the deleted project: %d %s", rec.Code, rec.Body)
	}
}

func TestProjects_ConfigPut_TooLargeIsRefusedAndNothingIsWritten(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "demo")
	before, _ := os.ReadFile(filepath.Join(projects, "demo", "scenes.yaml"))

	huge := "project: demo\n# " + strings.Repeat("x", 2<<20) + "\n"
	rec := doJSON(t, s.Handler(), http.MethodPut, "/api/projects/demo/config", map[string]string{"yaml": huge})
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "larger than 1 MB") {
		t.Fatalf("status %d (%s), want 413 naming the limit", rec.Code, rec.Body.String())
	}
	after, _ := os.ReadFile(filepath.Join(projects, "demo", "scenes.yaml"))
	if !bytes.Equal(before, after) {
		t.Error("scenes.yaml changed on an oversized save")
	}
}

func TestProjects_ConfigPut_APathOutsideTheProjectIsRefused(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "demo")
	before, _ := os.ReadFile(filepath.Join(projects, "demo", "scenes.yaml"))

	for name, yaml := range map[string]string{
		"output outside":  "project: demo\noutput: {mode: video, file: ../../../tmp/pwned.mp4}\nscenes:\n  - {id: a, prompt: p, tts_text: t}\n",
		"absolute output": "project: demo\noutput: {mode: video, file: /tmp/pwned.mp4}\nscenes:\n  - {id: a, prompt: p, tts_text: t}\n",
		"scene id":        "project: demo\noutput: {mode: video, file: out/demo.mp4}\nscenes:\n  - {id: ../../x, prompt: p, tts_text: t}\n",
	} {
		rec := doJSON(t, s.Handler(), http.MethodPut, "/api/projects/demo/config", map[string]string{"yaml": yaml})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want 422 (%s)", name, rec.Code, rec.Body.String())
		}
	}
	after, _ := os.ReadFile(filepath.Join(projects, "demo", "scenes.yaml"))
	if !bytes.Equal(before, after) {
		t.Error("scenes.yaml changed on a refused save")
	}
}

func TestProjectsList_IsAQueryPagedSortedAndSearched(t *testing.T) {
	s, projects := newProjectsServer(t)
	for _, id := range []string{"alpha", "bravo", "charlie", "delta"} {
		createProject(t, s, id)
	}
	// the list is the table: the folders are not read, so a projects folder that is gone still lists the rows
	if err := os.RemoveAll(projects); err != nil {
		t.Fatal(err)
	}
	type listed struct {
		ids   []string
		total string
		code  int
		link  string
	}
	get := func(query string) listed {
		rec := doJSON(t, s.Handler(), http.MethodGet, "/api/projects"+query, nil)
		var out []ProjectSummary
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		l := listed{total: rec.Header().Get("X-Total-Count"), code: rec.Code, link: rec.Header().Get("Link")}
		for _, p := range out {
			l.ids = append(l.ids, p.ID)
		}
		return l
	}
	if l := get(""); strings.Join(l.ids, ",") != "alpha,bravo,charlie,delta" || l.total != "" {
		t.Fatalf("no parameter: all of them in the order of the id: %+v", l)
	}
	if l := get("?limit=2&offset=1"); strings.Join(l.ids, ",") != "bravo,charlie" || l.total != "4" || !strings.Contains(l.link, "next") || !strings.Contains(l.link, "prev") {
		t.Fatalf("a page: %+v", l)
	}
	if l := get("?sort=-id&limit=2"); strings.Join(l.ids, ",") != "delta,charlie" {
		t.Fatalf("descending: %+v", l)
	}
	if l := get("?q=ARL"); strings.Join(l.ids, ",") != "charlie" || l.total != "1" {
		t.Fatalf("q in the id without regard to case: %+v", l)
	}
	// the project changed last comes first by updated_at
	if _, err := s.projects.Save("bravo", []byte(strings.Replace(starterOf(t, s, "bravo"), "bravo", "bravo two", 1)), "save"); err != nil {
		t.Fatal(err)
	}
	if l := get("?sort=-updated_at&limit=1"); strings.Join(l.ids, ",") != "bravo" {
		t.Fatalf("by update: %+v", l)
	}
	if l := get("?q=two&sort=name"); strings.Join(l.ids, ",") != "bravo" {
		t.Fatalf("q in the name: %+v", l)
	}
	for _, bad := range []string{"?sort=colour", "?limit=0", "?limit=x", "?offset=-1"} {
		if l := get(bad); l.code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, l.code)
		}
	}
}

func starterOf(t *testing.T, s *testServer, id string) string {
	t.Helper()
	raw, err := s.ProjectDefinition(id)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

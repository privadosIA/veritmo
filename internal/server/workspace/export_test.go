package workspace

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/project/importer"
)

const exportYAML = `project: demo
output: {mode: video, file: out/demo.mp4}
defaults:
  subject_prompt: photo of two friends,
scenes:
  - id: talk
    prompt: sitting on a bench
    tts_text: Hello there
    seed: 5
  - id: quiet
    prompt: the park
    silence_seconds: 2
    animate: true
    motion_prompt: leaves move
`

func TestExport_DatasetIsWhatTheImporterReadsBack(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "demo")
	if err := s.SaveProjectDefinition("demo", []byte(exportYAML), "save"); err != nil {
		t.Fatal(err)
	}
	rec := call(s.Handler(), http.MethodGet, "/api/projects/demo/export", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Disposition"), `filename="demo.dataset.json"`) {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	notes, _ := url.QueryUnescape(rec.Header().Get("X-Export-Notes"))
	if !strings.Contains(notes, "models, voice") || rec.Header().Get("X-Export-Count") != "2" {
		t.Fatalf("notes %q count %q", notes, rec.Header().Get("X-Export-Count"))
	}
	entries, rejected, err := importer.LoadBytes(rec.Body.Bytes())
	if err != nil || len(rejected) != 0 || len(entries) != 2 {
		t.Fatalf("%v %v %d", err, rejected, len(entries))
	}
	res, err := importer.Map(entries, nil, importer.Options{Project: "back", SilenceSeconds: 2})
	if err != nil || res.Rejected != 0 {
		t.Fatalf("%v %+v", err, res.Reports)
	}
	if got := res.Config.Scenes[0]; got.Prompt != "photo of two friends, sitting on a bench" || got.TTSText != "Hello there" || got.Seed == nil || *got.Seed != 5 {
		t.Errorf("talk = %+v", got)
	}
	if got := res.Config.Scenes[1]; !got.Animate || got.MotionPrompt != "leaves move" {
		t.Errorf("quiet = %+v", got)
	}
}

func TestExport_ZipHasTheYAMLTheSourcesAndNothingElse(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "demo")
	dir := filepath.Join(projects, "demo")
	if err := s.SaveProjectDefinition("demo", []byte(exportYAML), "save"); err != nil {
		t.Fatal(err)
	}
	putSource(t, s, "demo", "me.png", []byte("PNG"))
	os.MkdirAll(filepath.Join(dir, "out", "demo"), 0o755)
	os.WriteFile(filepath.Join(dir, "out", "demo", "a.mp3"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(dir, "tmp"), 0o755)
	os.WriteFile(filepath.Join(dir, "tmp", "cache.png"), []byte("x"), 0o644)

	rec := call(s.Handler(), http.MethodGet, "/api/projects/demo/export?format=zip", "")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/zip" || !strings.Contains(rec.Header().Get("Content-Disposition"), "demo.zip") {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	if strings.Join(names, ",") != "scenes.yaml,README.txt,RECIPE.md,recipe.json,sources/me.png" {
		t.Fatalf("files = %v", names)
	}
}

func TestExport_RefusalsAndMissing(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "demo")
	if code := call(s.Handler(), http.MethodGet, "/api/projects/demo/export?format=pdf", "").Code; code != http.StatusBadRequest {
		t.Errorf("unknown format: %d", code)
	}
	if code := call(s.Handler(), http.MethodGet, "/api/projects/nope/export", "").Code; code != http.StatusNotFound {
		t.Errorf("unknown project: %d", code)
	}
	// a project with no scenes has nothing to put in a dataset
	if err := s.SaveProjectDefinition("demo", []byte("project: demo\noutput: {mode: video, file: out/demo.mp4}\nscenes: []\n"), "save"); err != nil {
		t.Fatal(err)
	}
	rec := call(s.Handler(), http.MethodGet, "/api/projects/demo/export", "")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "nothing to export") {
		t.Errorf("empty: %d %s", rec.Code, rec.Body)
	}
	// but its package is fine
	if code := call(s.Handler(), http.MethodGet, "/api/projects/demo/export?format=zip", "").Code; code != http.StatusOK {
		t.Errorf("zip of an empty project: %d", code)
	}
}

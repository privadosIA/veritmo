package workspace

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/platform/runner"
	"veritmo/internal/video/blender"
)

// a description in seconds with explicit colours: an import has no design and no tempo to resolve names and beats.
const importedScene = `{"version":1,"camera":{"keys":[{"t":0,"location":[0,-9,1.2],"target":[0,0,0.6]},{"t":4,"location":[0,-7,1],"target":[0,0,0.6]}]},` +
	`"materials":{"m":{"color":"#ff3b30"}},"objects":[{"id":"title","type":"text","text":"Hola","size":1,"material":"m","keys":[{"t":0,"scale":1},{"t":2,"scale":1.2,"ease":"back"}]}]}`

const blenderDataset = `[["hola","Hola","","Dialogue: \"Hola\"\nBlender: hola.scene.json"],["plain","Calle","","a quiet street"]]`

func importDescriptionsServer(t *testing.T) (*testServer, string) {
	t.Helper()
	isolateSettings(t)
	s, projects := newProjectsServer(t)
	return s, projects
}

func previewDataset(t *testing.T, s *testServer, body map[string]any) (int, map[string]any) {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/import/preview", body)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestImport_PreviewWarnsOfTheDescriptionsOfThe3DScenes(t *testing.T) {
	s, _ := importDescriptionsServer(t)
	warnings := func(body map[string]any) string {
		code, out := previewDataset(t, s, body)
		if code != http.StatusOK {
			t.Fatalf("%d %v", code, out)
		}
		raw, _ := json.Marshal(out["warnings"])
		return string(raw)
	}
	if w := warnings(map[string]any{"content": blenderDataset}); !strings.Contains(w, "hola.scene.json, which is not in descriptions") {
		t.Errorf("a line without its file must warn: %s", w)
	}
	if w := warnings(map[string]any{"content": blenderDataset, "descriptions": map[string]any{"hola.scene.json": json.RawMessage(importedScene)}}); w != "[]" {
		t.Errorf("a good description must not warn: %s", w)
	}
	bad := map[string]any{"hola.scene.json": map[string]any{"version": 1}, "other.scene.json": json.RawMessage(importedScene)}
	w := warnings(map[string]any{"content": blenderDataset, "descriptions": bad})
	for _, want := range []string{"does not pass the checks: hola.scene.json", "other.scene.json is not named by any scene"} {
		if !strings.Contains(w, want) {
			t.Errorf("warnings lack %q: %s", want, w)
		}
	}
	for name, d := range map[string]map[string]any{
		"a path":     {"../x.scene.json": json.RawMessage(importedScene)},
		"not a name": {"x.json": json.RawMessage(importedScene)},
	} {
		if code, out := previewDataset(t, s, map[string]any{"content": blenderDataset, "descriptions": d}); code != http.StatusBadRequest {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
}

func TestImport_ConfirmWritesTheDescriptionsBeforeValidating(t *testing.T) {
	s, projects := importDescriptionsServer(t)
	_, out := previewDataset(t, s, map[string]any{"content": blenderDataset, "descriptions": map[string]any{"hola.scene.json": json.RawMessage(importedScene)}})
	yml, _ := out["yaml"].(string)
	if !strings.Contains(yml, "build: hola.scene.json") {
		t.Fatalf("yaml lacks the 3D scene:\n%s", yml)
	}
	confirm := func(id string, d map[string]any) (int, string) {
		rec := doJSON(t, s.Handler(), http.MethodPost, "/api/import/confirm", map[string]any{"id": id, "yaml": yml, "descriptions": d})
		return rec.Code, rec.Body.String()
	}
	// no file: refused, nothing left behind
	if code, body := confirm("nofile", nil); code != http.StatusUnprocessableEntity || !strings.Contains(body, "hola.scene.json") {
		t.Fatalf("a missing description must be refused: %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(projects, "nofile")); err == nil || s.ProjectExists("nofile") {
		t.Errorf("a refused import must leave no project")
	}
	// a description that does not pass
	if code, body := confirm("broken", map[string]any{"hola.scene.json": map[string]any{"version": 1}}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "hola.scene.json") {
		t.Fatalf("a broken description must be refused: %d %s", code, body)
	}
	if s.ProjectExists("broken") {
		t.Errorf("a refused import must leave no project")
	}
	// a file nobody names
	if code, body := confirm("extra", map[string]any{"hola.scene.json": json.RawMessage(importedScene), "x.scene.json": json.RawMessage(importedScene)}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "no scene names") {
		t.Fatalf("an unused description must be refused: %d %s", code, body)
	}
	// the good one (the project's validation asks for Blender, as it does for any 3D scene)
	if _, err := blender.Detect(context.Background(), runner.NewExecRunner()); err != nil {
		t.Skipf("Blender is not usable here: %v", err)
	}
	if code, body := confirm("three-d", map[string]any{"hola.scene.json": json.RawMessage(importedScene)}); code != http.StatusCreated {
		t.Fatalf("%d %s", code, body)
	}
	raw, err := os.ReadFile(filepath.Join(projects, "three-d", "sources", "hola.scene.json"))
	if err != nil || !strings.Contains(string(raw), `"Hola"`) {
		t.Errorf("the description must be in the sources: %v %s", err, raw)
	}
}

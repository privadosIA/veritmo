package workspace

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/assets3d"
)

// libraryWith is a library with a pack "mine": a character that needs the pack's atlas and a prop that needs nothing.
func libraryWith(t *testing.T) *assets3d.Library {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range map[string][]byte{
		"GLB/char-a.glb":            glbBytes(characterGLB),
		"GLB/prop.glb":              glbBytes(`{"accessors":[{"count":30}],"meshes":[{"primitives":[{"attributes":{"POSITION":0},"indices":0}]}]}`),
		"GLB/Textures/colormap.png": []byte("atlas"),
	} {
		w, _ := zw.Create(name)
		w.Write(data)
	}
	zw.Close()
	lib := assets3d.New(t.TempDir(), nil)
	if _, err := lib.Import(context.Background(), assets3d.ImportMeta{ID: "mine", Licence: "CC0"}, &buf); err != nil {
		t.Fatal(err)
	}
	return lib
}

func postJSON(t *testing.T, s *testServer, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return doJSON(t, s.Handler(), http.MethodPost, path, body)
}

func TestSources_AddAModelOfTheLibrary(t *testing.T) {
	s, projects := newProjectsServer(t)
	s.SetModelLibrary(libraryWith(t))
	createProject(t, s, "rink")
	rec := postJSON(t, s, "/api/projects/rink/sources/model", map[string]any{"from": "mine/char-a"})
	var res struct {
		Name     string   `json:"name"`
		Pictures []string `json:"pictures"`
	}
	json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != http.StatusCreated || res.Name != "char-a.glb" || strings.Join(res.Pictures, ",") != "mine--colormap.png" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for _, name := range []string{"char-a.glb", "mine--colormap.png"} {
		if _, err := os.Stat(filepath.Join(projects, "rink", "sources", name)); err != nil {
			t.Errorf("%s is in the sources: %v", name, err)
		}
	}
	// an ordinary source from then on, with its texture found by name
	list := doJSON(t, s.Handler(), http.MethodGet, "/api/projects/rink/sources", nil)
	var out struct {
		Sources []map[string]any `json:"sources"`
	}
	json.Unmarshal(list.Body.Bytes(), &out)
	var model map[string]any
	for _, x := range out.Sources {
		if x["name"] == "char-a.glb" {
			model = x
		}
	}
	if model == nil || model["kind"] != "model" || strings.Join(toStrings(model["needs"]), ",") != "mine--colormap.png" || strings.Join(toStrings(model["clips"]), ",") != "idle,sprint,walk" {
		t.Errorf("the listing: %v", model)
	}
	// again: already there; a prop with no picture; another file with the name taken; and the refusals
	if rec := postJSON(t, s, "/api/projects/rink/sources/model", map[string]any{"from": "mine/char-a"}); rec.Code != http.StatusOK {
		t.Errorf("the same model again: %d %s", rec.Code, rec.Body)
	}
	if rec := postJSON(t, s, "/api/projects/rink/sources/model", map[string]any{"from": "mine/prop", "name": "lamp.glb"}); rec.Code != http.StatusCreated {
		t.Errorf("a prop: %d %s", rec.Code, rec.Body)
	}
	if rec := postJSON(t, s, "/api/projects/rink/sources/model", map[string]any{"from": "mine/prop", "name": "char-a.glb"}); rec.Code != http.StatusConflict {
		t.Errorf("a name taken by another file: %d %s", rec.Code, rec.Body)
	}
	for name, tc := range map[string]struct {
		body map[string]any
		want int
	}{"unknown model": {map[string]any{"from": "mine/nope"}, http.StatusNotFound}, "unknown pack": {map[string]any{"from": "x/y"}, http.StatusNotFound},
		"not a pack/model": {map[string]any{"from": "nope"}, http.StatusBadRequest}, "a path as name": {map[string]any{"from": "mine/prop", "name": "../x.glb"}, http.StatusBadRequest}} {
		if rec := postJSON(t, s, "/api/projects/rink/sources/model", tc.body); rec.Code != tc.want {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := postJSON(t, s, "/api/projects/nope/sources/model", map[string]any{"from": "mine/prop"}); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown project: %d", rec.Code)
	}
}

func TestSources_AddAModelWithNoLibraryIsAnExplainedRefusal(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "rink")
	if rec := postJSON(t, s, "/api/projects/rink/sources/model", map[string]any{"from": "mine/prop"}); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "library") {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}

func TestSources_AModelAScenePlacesIsNotDeleted(t *testing.T) {
	s, projects := newProjectsServer(t)
	s.SetModelLibrary(libraryWith(t))
	createProject(t, s, "rink")
	if rec := postJSON(t, s, "/api/projects/rink/sources/model", map[string]any{"from": "mine/char-a"}); rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	desc := `{"version":1,"camera":{"keys":[{"t":0,"location":[0,-6,1]}]},"objects":[{"id":"ana","type":"model","file":"char-a.glb","size":1.7}]}`
	if err := os.WriteFile(filepath.Join(projects, "rink", "sources", "rink.scene.json"), []byte(desc), 0o644); err != nil {
		t.Fatal(err)
	}
	yaml := "project: rink\noutput: {mode: video, file: out/rink.mp4}\ndefaults: {resolution: 640x360, fps: 30}\nscenes:\n  - id: a\n    silence_seconds: 2\n    blender: {build: rink.scene.json}\n"
	if err := s.SaveProjectDefinition("rink", []byte(yaml), "save"); err != nil {
		t.Fatalf("the project with a 3D scene: %v", err)
	}
	for _, name := range []string{"char-a.glb", "mine--colormap.png"} { // the picture is in use through the model that needs it
		rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/projects/rink/sources/"+name, nil)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `scene \"a\" places`) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
		if _, err := os.Stat(filepath.Join(projects, "rink", "sources", name)); err != nil {
			t.Errorf("%s must still be there: %v", name, err)
		}
	}
	// a model no scene places can go
	if rec := postJSON(t, s, "/api/projects/rink/sources/model", map[string]any{"from": "mine/prop"}); rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/projects/rink/sources/prop.glb", nil); rec.Code != http.StatusNoContent {
		t.Errorf("a model no scene places: %d %s", rec.Code, rec.Body)
	}
}

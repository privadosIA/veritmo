package workspace

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"veritmo/internal/video/blender"
)

// glbBytes is a binary glTF whose JSON chunk is js.
func glbBytes(js string) []byte {
	for len(js)%4 != 0 {
		js += " "
	}
	out := []byte("glTF")
	out = binary.LittleEndian.AppendUint32(out, 2)
	out = binary.LittleEndian.AppendUint32(out, uint32(20+len(js)))
	out = binary.LittleEndian.AppendUint32(out, uint32(len(js)))
	out = binary.LittleEndian.AppendUint32(out, 0x4E4F534A)
	return append(out, js...)
}

const characterGLB = `{"animations":[{"name":"walk"},{"name":"sprint"},{"name":"idle"}],"accessors":[{"count":300}],` +
	`"meshes":[{"primitives":[{"attributes":{"POSITION":0},"indices":0}]}],"images":[{"uri":"Textures/colormap.png"}]}`

func TestSources_ModelUploadAndListing(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "rink")
	if rec := putSource(t, s, "rink", "ana.glb", glbBytes(characterGLB)); rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(projects, "rink", "sources", "ana.glb")); err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/projects/rink/sources", nil)
	var out struct {
		Sources []map[string]any `json:"sources"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Sources) != 1 {
		t.Fatalf("%v %s", err, rec.Body)
	}
	m := out.Sources[0]
	if m["kind"] != "model" || m["triangles"] != float64(100) {
		t.Errorf("listing: %v", m)
	}
	if got := strings.Join(toStrings(m["clips"]), ","); got != "idle,sprint,walk" {
		t.Errorf("clips = %q", got)
	}
	if got := strings.Join(toStrings(m["needs"]), ","); got != "colormap.png" {
		t.Errorf("needs = %q", got)
	}
	// the picture it needs is an ordinary source
	if rec := putSource(t, s, "rink", "colormap.png", []byte("png")); rec.Code != http.StatusCreated {
		t.Fatalf("the texture: %d %s", rec.Code, rec.Body)
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestSources_ANotModelIsRefusedAndNothingIsStored(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "rink")
	for name, body := range map[string][]byte{
		"text.glb":    []byte("this is a text file"),
		"outside.glb": glbBytes(`{"images":[{"uri":"../../secret.png"}]}`),
	} {
		rec := putSource(t, s, "rink", name, body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "not a usable model") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
		if _, err := os.Stat(filepath.Join(projects, "rink", "sources", name)); err == nil {
			t.Errorf("%s was stored", name)
		}
	}
	for _, name := range []string{"x.gltf", "x.fbx", "x.obj"} {
		if rec := putSource(t, s, "rink", name, []byte("x")); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400 (not an accepted name)", name, rec.Code)
		}
	}
}

func TestSources_AModelCanBeDeleted(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "rink")
	putSource(t, s, "rink", "ana.glb", glbBytes(`{}`))
	if rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/projects/rink/sources/ana.glb", nil); rec.Code != http.StatusNoContent && rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if got := listSourceNames(t, s, "rink"); len(got) != 0 {
		t.Errorf("%v", got)
	}
}

func TestDescriptions_APreviewDrawsTheModelsOfTheProjectAndRefusesAClipItDoesNotHave(t *testing.T) {
	s, dir, _ := writerServer(t, validBeats)
	src := filepath.Join(dir, "sources")
	os.MkdirAll(src, 0o755)
	os.WriteFile(filepath.Join(src, "ana.glb"), glbBytes(characterGLB), 0o644)
	os.WriteFile(filepath.Join(src, "colormap.png"), []byte("png"), 0o644)
	doc := func(clip string) string {
		return `{"version":1,"camera":{"keys":[{"t":0,"location":[0,-6,1],"target":[0,0,0.8]}]},` +
			`"objects":[{"id":"ana","type":"model","file":"ana.glb","size":1.7,"animation":{"clip":"` + clip + `"}}]}`
	}
	os.WriteFile(filepath.Join(src, "m.scene.json"), []byte(doc("sprint")), 0o644)
	s.Overrides.BlenderInfo = func(context.Context) (blender.Info, error) {
		return blender.Info{Path: "/fake/blender", Version: "5.3.0"}, nil
	}
	var drew *blender.Spec
	s.Overrides.BlenderPreview = func(_ context.Context, _ string, spec *blender.Spec, _ blender.PreviewOptions) ([]byte, error) {
		drew = spec
		return []byte("\x89PNG fake"), nil
	}
	preview := func() *httptest.ResponseRecorder {
		return doJSON(t, s.Handler(), http.MethodPost, "/api/projects/demo/blender/descriptions/m.scene.json/preview", map[string]any{"t": 2, "seconds": 5})
	}
	if rec := preview(); rec.Code != http.StatusOK || drew == nil || drew.Objects[0].File != "ana.glb" || !*drew.Objects[0].Animation.Loop {
		t.Fatalf("a model is previewed: %d %s %+v", rec.Code, rec.Body, drew)
	}
	os.WriteFile(filepath.Join(src, "m.scene.json"), []byte(doc("skate")), 0o644)
	if rec := preview(); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `is not in ana.glb (clips: idle, sprint, walk)`) {
		t.Errorf("a clip the file does not have: %d %s", rec.Code, rec.Body)
	}
	os.Remove(filepath.Join(src, "colormap.png"))
	os.WriteFile(filepath.Join(src, "m.scene.json"), []byte(doc("sprint")), 0o644)
	if rec := preview(); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "needs the picture colormap.png") {
		t.Errorf("a missing texture: %d %s", rec.Code, rec.Body)
	}
}

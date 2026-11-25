package music

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/assets3d"
	"veritmo/internal/music/musicmotion"
	"veritmo/internal/platform/events"
	"veritmo/internal/server/workspace"
)

func glbWith(js string) []byte {
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

const characterGLB = `{"animations":[{"name":"idle"},{"name":"sprint"},{"name":"walk"}],"accessors":[{"count":300}],` +
	`"meshes":[{"primitives":[{"attributes":{"POSITION":0},"indices":0}]}],"images":[{"uri":"Textures/colormap.png"}]}`

func putRaw(t *testing.T, s *testServer, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPut, path, bytes.NewReader(body)))
	return rec
}

// sceneWithCharacter is a description in seconds that places ana.glb for the whole of a short piece.
const sceneWithCharacter = `{"version":1,"camera":{"keys":[{"t":0,"location":[0,-9,1.2],"target":[0,0,0.8]},{"t":0.8,"location":[0,-7,1],"target":[0,0,0.8]}]},` +
	`"objects":[{"id":"ana","type":"model","file":"ana.glb","size":1.7,"animation":{"clip":"sprint"},"keys":[{"t":0,"location":[-3,0,0]},{"t":0.8,"location":[3,0,0]}]}]}`

func putModels(t *testing.T, s *testServer, id string) {
	t.Helper()
	base := "/api/music-motion/pieces/" + id + "/models/"
	if rec := putRaw(t, s, base+"ana.glb", glbWith(characterGLB)); rec.Code != http.StatusCreated {
		t.Fatalf("ana.glb: %d %s", rec.Code, rec.Body)
	}
	if rec := putRaw(t, s, base+"colormap.png", []byte("png bytes")); rec.Code != http.StatusCreated {
		t.Fatalf("colormap.png: %d %s", rec.Code, rec.Body)
	}
}

func scriptWithCharacter(v PieceView, idx int, desc string) musicmotion.Script {
	script := *v.Script
	script.Pieces = append([]musicmotion.Piece(nil), script.Pieces...)
	script.Pieces[idx].MakeBlender(json.RawMessage(desc))
	return script
}

func TestPieceModels_UploadListAndDelete(t *testing.T) {
	s := mmServer(t)
	v := newPiece(t, s, nil)
	base := "/api/music-motion/pieces/" + v.ID + "/models"
	putModels(t, s, v.ID)
	rec := doJSON(t, s.Handler(), http.MethodGet, base, nil)
	var out struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Models) != 2 {
		t.Fatalf("%v %s", err, rec.Body)
	}
	ana := out.Models[0]
	if ana["kind"] != "model" || ana["triangles"] != float64(100) || strings.Join(toStr(ana["clips"]), ",") != "idle,sprint,walk" || strings.Join(toStr(ana["needs"]), ",") != "colormap.png" {
		t.Errorf("the character: %v", ana)
	}
	if out.Models[1]["kind"] != "picture" {
		t.Errorf("the texture: %v", out.Models[1])
	}
	// the files are in the sources of the piece's project, where the render looks
	if _, err := os.Stat(filepath.Join(s.pieceDir(v.ID), "sources", "ana.glb")); err != nil {
		t.Error(err)
	}
	// never replaced, never anything else
	if rec := putRaw(t, s, base+"/ana.glb", glbWith(`{}`)); rec.Code != http.StatusConflict {
		t.Errorf("an existing name: %d", rec.Code)
	}
	for name, body := range map[string][]byte{"text.glb": []byte("not a model"), "far.glb": glbWith(`{"images":[{"uri":"../../x.png"}]}`)} {
		if rec := putRaw(t, s, base+"/"+name, body); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "not a usable model") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
		if _, err := os.Stat(filepath.Join(s.pieceDir(v.ID), "sources", name)); err == nil {
			t.Errorf("%s was stored", name)
		}
	}
	for _, name := range []string{"x.fbx", "x.gltf", "song.mp3", "a.scene.json"} {
		if rec := putRaw(t, s, base+"/"+name, []byte("x")); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, rec.Code)
		}
	}
	if rec := putRaw(t, s, base+"/empty.png", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("an empty file: %d", rec.Code)
	}
	if rec := doJSON(t, s.Handler(), http.MethodDelete, base+"/colormap.png", nil); rec.Code != http.StatusNoContent {
		t.Errorf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodDelete, base+"/colormap.png", nil); rec.Code != http.StatusNotFound {
		t.Errorf("delete again: %d", rec.Code)
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/music-motion/pieces/nope/models", nil); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown piece: %d", rec.Code)
	}
}

func toStr(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestPieceModels_AModelTheScriptUsesIsNotDeleted(t *testing.T) {
	s := mmServer(t)
	v := newPiece(t, s, nil)
	putModels(t, s, v.ID)
	script := scriptWithCharacter(v, 0, sceneWithCharacter)
	if rec := doJSON(t, s.Handler(), http.MethodPut, "/api/music-motion/pieces/"+v.ID+"/script", script); rec.Code != http.StatusOK {
		t.Fatalf("a script that places a model of its piece: %d %s", rec.Code, rec.Body)
	}
	base := "/api/music-motion/pieces/" + v.ID + "/models/"
	for _, name := range []string{"ana.glb", "colormap.png"} { // the picture is in use through the model that needs it
		rec := doJSON(t, s.Handler(), http.MethodDelete, base+name, nil)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), v.Script.Pieces[0].ID) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
		if _, err := os.Stat(filepath.Join(s.pieceDir(v.ID), "sources", name)); err != nil {
			t.Errorf("%s must still be there: %v", name, err)
		}
	}
	// the script puts a plain scene in its place: now they can go
	plain := scriptWithCharacter(v, 0, pieceScene3D)
	if rec := doJSON(t, s.Handler(), http.MethodPut, "/api/music-motion/pieces/"+v.ID+"/script", plain); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodDelete, base+"ana.glb", nil); rec.Code != http.StatusNoContent {
		t.Errorf("a model no piece places: %d %s", rec.Code, rec.Body)
	}
}

func TestPieceModels_AScriptCanOnlyPlaceTheModelsOfItsPiece(t *testing.T) {
	s := mmServer(t)
	v := newPiece(t, s, nil)
	put := func(desc string) *httptest.ResponseRecorder {
		return doJSON(t, s.Handler(), http.MethodPut, "/api/music-motion/pieces/"+v.ID+"/script", scriptWithCharacter(v, 0, desc))
	}
	// no model uploaded yet: refused with the piece and the place in the JSON
	if rec := put(sceneWithCharacter); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "objects[0].file") || !strings.Contains(rec.Body.String(), v.Script.Pieces[0].ID) {
		t.Errorf("a model the piece does not have: %d %s", rec.Code, rec.Body)
	}
	putModels(t, s, v.ID)
	if rec := put(strings.Replace(sceneWithCharacter, `"sprint"`, `"skate"`, 1)); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "(clips: idle, sprint, walk)") {
		t.Errorf("a clip the file does not have: %d %s", rec.Code, rec.Body)
	}
	font := `{"version":1,"camera":{"keys":[{"t":0,"location":[0,-9,1.2]}]},"objects":[{"id":"t","type":"text","text":"Hi","size":1,"font":"x.ttf"}]}`
	if rec := put(font); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "takes its fonts from the design") {
		t.Errorf("a font file: %d %s", rec.Code, rec.Body)
	}
	if rec := put(sceneWithCharacter); rec.Code != http.StatusOK {
		t.Errorf("a model of the piece: %d %s", rec.Code, rec.Body)
	}
}

func TestPieceModels_NothingChangesWhileTheRenderRuns(t *testing.T) {
	s := mmServer(t)
	v := newPiece(t, s, nil)
	putModels(t, s, v.ID)
	defer holdRenders(s)() // a render that does not end until the test returns
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces/"+v.ID+"/render", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("render: %d %s", rec.Code, rec.Body)
	}
	base := "/api/music-motion/pieces/" + v.ID + "/models/"
	if rec := putRaw(t, s, base+"bea.glb", glbWith(`{}`)); rec.Code != http.StatusConflict {
		t.Errorf("an upload during a render: %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodDelete, base+"colormap.png", nil); rec.Code != http.StatusConflict {
		t.Errorf("a delete during a render: %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/music-motion/pieces/"+v.ID+"/models", nil); rec.Code != http.StatusOK {
		t.Errorf("the list can be read: %d", rec.Code)
	}
}

func libraryForPieces(t *testing.T) *assets3d.Library {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range map[string][]byte{"GLB/ana.glb": glbWith(characterGLB), "GLB/Textures/colormap.png": []byte("atlas")} {
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

func TestPieceModels_AddAModelOfTheLibraryAndPlaceItInTheScript(t *testing.T) {
	s := mmServer(t)
	s.SetModelLibrary(libraryForPieces(t))
	v := newPiece(t, s, nil)
	base := "/api/music-motion/pieces/" + v.ID + "/models"
	rec := doJSON(t, s.Handler(), http.MethodPost, base, map[string]any{"from": "mine/ana"})
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), "mine--colormap.png") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodPost, base, map[string]any{"from": "mine/ana"}); rec.Code != http.StatusOK {
		t.Errorf("again: %d", rec.Code)
	}
	list := doJSON(t, s.Handler(), http.MethodGet, base, nil)
	if !strings.Contains(list.Body.String(), `"needs":["mine--colormap.png"]`) {
		t.Errorf("the list: %s", list.Body)
	}
	// the script places it like a model that was uploaded
	script := scriptWithCharacter(v, 0, strings.Replace(sceneWithCharacter, "ana.glb", "ana.glb", 1))
	if rec := doJSON(t, s.Handler(), http.MethodPut, "/api/music-motion/pieces/"+v.ID+"/script", script); rec.Code != http.StatusOK {
		t.Fatalf("a script that places a model of the library: %d %s", rec.Code, rec.Body)
	}
	for name, tc := range map[string]struct {
		body map[string]any
		want int
	}{"unknown": {map[string]any{"from": "mine/nope"}, http.StatusNotFound}, "bad": {map[string]any{"from": "x"}, http.StatusBadRequest}} {
		if rec := doJSON(t, s.Handler(), http.MethodPost, base, tc.body); rec.Code != tc.want {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	// nothing is added while the piece renders
	defer holdRenders(s)()
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces/"+v.ID+"/render", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("render: %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodPost, base, map[string]any{"from": "mine/ana", "name": "ana2.glb"}); rec.Code != http.StatusConflict {
		t.Errorf("during a render: %d %s", rec.Code, rec.Body)
	}
}

// holdRenders makes every render of the test server wait until the returned function is called, so that a test that needs a render in
// progress does not race with one that finishes at once.
func holdRenders(s *testServer) (release func()) {
	ch := make(chan struct{})
	s.Workspace.Execute = func(ctx context.Context, rn workspace.ExecRun, em *events.Emitter) (string, error) {
		em.PipelineStart()
		select {
		case <-ch:
		case <-ctx.Done():
		}
		out := "out/" + rn.Rec.Project + ".mp4"
		_ = os.MkdirAll(rn.Dir+"/out", 0o755)
		_ = os.WriteFile(rn.Dir+"/"+out, []byte("video"), 0o644)
		em.Done(out)
		return out, nil
	}
	return func() { close(ch) }
}

package musicmotion

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// glbOf is a binary glTF whose JSON chunk is js.
func glbOf(js string) []byte {
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

const sceneWithModel = `{"version":1,"camera":{"keys":[{"beat":0,"location":[0,-9,1.2],"target":[0,0,0.6]},{"beat":16,"location":[0,-7,1],"target":[0,0,0.6]}]},` +
	`"objects":[{"id":"ana","type":"model","file":"ana.glb","size":1.7,"animation":{"clip":"sprint"},"keys":[{"beat":0,"location":[-4,0,0]},{"beat":16,"location":[4,0,0]}]}]}`

// pieceSources is a sources folder with a character (two clips, a texture) and its texture.
func pieceSources(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "ana.glb"), glbOf(`{"animations":[{"name":"idle"},{"name":"sprint"}],"images":[{"uri":"Textures/map.png"}]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "map.png"), []byte("png"), 0o644)
	return dir
}

func scriptWithModel(desc string) Script {
	s := blenderScript()
	s.Pieces[1].Blender.Description = json.RawMessage(desc)
	return s
}

func TestA3DPieceCanPlaceTheModelsOfItsPiece(t *testing.T) {
	cat := Catalog{Builtin: BuiltinParts()}
	s := scriptWithModel(sceneWithModel)
	// without the piece's folder a model is refused with the reason (the dataset writer has none)
	if err := s.Validate(cat, testMap(), good()); err == nil || !strings.Contains(err.Error(), "files of a project's sources") {
		t.Fatalf("no folder: %v", err)
	}
	dir := pieceSources(t)
	if err := s.WithSources(dir).Validate(cat, testMap(), good()); err != nil {
		t.Fatalf("a script that places a model of its piece: %v", err)
	}
	if s.sources != "" {
		t.Error("WithSources must not change the script it is called on")
	}
	// what it does not accept is named by the piece and the place in the JSON
	for name, tc := range map[string]struct{ desc, want string }{
		"a file the piece does not have": {strings.Replace(sceneWithModel, "ana.glb", "skater.glb", 1), "objects[0].file"},
		"a clip the file lacks":          {strings.Replace(sceneWithModel, `"sprint"`, `"skate"`, 1), `"skate" is not in ana.glb (clips: idle, sprint)`},
	} {
		err := scriptWithModel(tc.desc).WithSources(dir).Validate(cat, testMap(), good())
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "p2") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// a picture the model needs is missing
	os.Remove(filepath.Join(dir, "map.png"))
	if err := s.WithSources(dir).Validate(cat, testMap(), good()); err == nil || !strings.Contains(err.Error(), "needs the picture map.png") {
		t.Errorf("a missing texture: %v", err)
	}
}

func TestA3DPieceStillHasNoFontFiles(t *testing.T) {
	cat := Catalog{Builtin: BuiltinParts()}
	dir := pieceSources(t)
	os.WriteFile(filepath.Join(dir, "x.ttf"), []byte("font"), 0o644)
	desc := `{"version":1,"camera":{"keys":[{"beat":0,"location":[0,-9,1.2]}]},"objects":[{"id":"t","type":"text","text":"Hi","size":1,"font":"x.ttf","keys":[{"beat":0,"scale":1}]}]}`
	err := scriptWithModel(desc).WithSources(dir).Validate(cat, testMap(), good())
	if err == nil || !strings.Contains(err.Error(), "takes its fonts from the design") {
		t.Errorf("a font file in a piece: %v", err)
	}
}

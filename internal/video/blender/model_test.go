package blender

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// glb builds the bytes of a binary glTF whose JSON chunk is js (and a short binary chunk).
func glb(js string) []byte {
	for len(js)%4 != 0 {
		js += " "
	}
	bin := []byte{0, 0, 0, 0}
	total := 12 + 8 + len(js) + 8 + len(bin)
	out := make([]byte, 0, total)
	u := func(v uint32) { out = binary.LittleEndian.AppendUint32(out, v) }
	u(glbMagic)
	u(2)
	u(uint32(total))
	u(uint32(len(js)))
	u(glbJSONChunk)
	out = append(out, js...)
	u(uint32(len(bin)))
	u(0x004E4942)
	out = append(out, bin...)
	return out
}

const goodJSON = `{"asset":{"version":"2.0"},
 "animations":[{"name":"walk"},{"name":"idle"},{"name":"walk"},{"name":""}],
 "accessors":[{"count":300},{"count":120},{"count":30}],
 "meshes":[{"primitives":[{"attributes":{"POSITION":1},"indices":0},{"attributes":{"POSITION":1},"mode":5,"indices":2}]}],
 "buffers":[{"byteLength":4}],"images":[{"uri":"data:image/png;base64,AAAA"},{"bufferView":0},{"uri":"Textures/colormap.png"},{"uri":"Textures%2Fcolormap.png"},{"uri":"other/atlas.jpg"}]}`

func TestParseModel(t *testing.T) {
	m, err := ParseModel(glb(goodJSON))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(m.Clips, ","); got != "idle,walk" {
		t.Errorf("clips = %q, want idle,walk (sorted, no repeats, no unnamed)", got)
	}
	// 300 indices -> 100 triangles; a strip of 30 indices -> 28
	if m.Triangles != 128 {
		t.Errorf("triangles = %d, want 128", m.Triangles)
	}
	if !m.HasClip("idle") || m.HasClip("run") {
		t.Error("HasClip is wrong")
	}
	if m.Bytes == 0 {
		t.Error("Bytes is empty")
	}
	if got := strings.Join(m.Needs, ","); got != "atlas.jpg,colormap.png" {
		t.Errorf("needs = %q, want atlas.jpg,colormap.png (last names, sorted, no repeats)", got)
	}
	if len(m.Refs) != 2 || m.Refs[0].Path != "Textures/colormap.png" || m.Refs[1].Path != "other/atlas.jpg" {
		t.Errorf("refs = %+v", m.Refs)
	}
}

func TestParseModelWithoutIndices(t *testing.T) {
	m, err := ParseModel(glb(`{"accessors":[{"count":90}],"meshes":[{"primitives":[{"attributes":{"POSITION":0}}]}]}`))
	if err != nil || m.Triangles != 30 || len(m.Clips) != 0 {
		t.Fatalf("got %+v, %v", m, err)
	}
}

func TestParseModelRefusals(t *testing.T) {
	good := glb(goodJSON)
	cut := good[:len(good)-30]
	badVersion := append([]byte(nil), good...)
	binary.LittleEndian.PutUint32(badVersion[4:], 1)
	bigChunk := append([]byte(nil), good...)
	binary.LittleEndian.PutUint32(bigChunk[12:], 1<<20)
	notJSON := glb("not json at all")
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"text renamed", []byte("this is just a text file renamed"), "glTF header"},
		{"too short", []byte("glTF"), "too short"},
		{"cut", cut, "cut short"},
		{"version", badVersion, "version 1"},
		{"chunk beyond the file", bigChunk, "cut short"},
		{"json", notJSON, "cannot be read"},
		{"image outside", glb(`{"images":[{"uri":"../../secret.png"}]}`), "relative path of plain names"},
		{"image absolute", glb(`{"images":[{"uri":"/etc/passwd"}]}`), "relative path of plain names"},
		{"image encoded outside", glb(`{"images":[{"uri":"..%2F..%2Fsecret.png"}]}`), "relative path of plain names"},
		{"image with a scheme", glb(`{"images":[{"uri":"http://x/y.png"}]}`), "relative path of plain names"},
		{"image with a backslash", glb(`{"images":[{"uri":"a\\b.png"}]}`), "relative path of plain names"},
		{"image with a query", glb(`{"images":[{"uri":"a.png?x=1"}]}`), "relative path of plain names"},
		{"external buffer", glb(`{"buffers":[{"uri":"model.bin"}]}`), "keeps its data in another file"},
	}
	for _, c := range cases {
		_, err := ParseModel(c.data)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error = %v, want it to contain %q", c.name, err, c.want)
		}
	}
	if _, err := ParseModel(make([]byte, MaxModelBytes+1)); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Errorf("a file over the limit: %v", err)
	}
}

func TestReadModel(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.glb")
	if err := os.WriteFile(p, glb(goodJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := ReadModel(p)
	if err != nil || m.Triangles != 128 {
		t.Fatalf("got %+v, %v", m, err)
	}
	if _, err := ReadModel(filepath.Join(dir, "missing.glb")); err == nil {
		t.Error("a missing file must fail")
	}
}

func TestStageModels(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "plain.glb"), glb(`{"animations":[{"name":"a"}]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "skin.glb"), glb(`{"images":[{"uri":"Textures/colormap.png"}]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "colormap.png"), []byte("pixels"), 0o644)
	doc := `{"version":1,"camera":{"keys":[{"t":0,"location":[0,-5,1]}]},"objects":[
	{"id":"a","type":"model","file":"plain.glb","size":1},{"id":"b","type":"model","file":"skin.glb","size":1}]}`
	spec, err := Parse([]byte(doc), 5, testEnv(dir))
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), ModelsDir)
	got, err := StageModels(spec, dir, dst)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "plain.glb"); got["plain.glb"] != want {
		t.Errorf("a model that needs nothing is imported from the sources: %q, want %q", got["plain.glb"], want)
	}
	staged := got["skin.glb"]
	if !strings.HasPrefix(staged, dst) {
		t.Fatalf("a model that needs a picture is staged under %s: %q", dst, staged)
	}
	if b, err := os.ReadFile(filepath.Join(filepath.Dir(staged), "Textures", "colormap.png")); err != nil || string(b) != "pixels" {
		t.Errorf("the picture is next to the model where the file names it: %q, %v", b, err)
	}
	// the picture is gone: refused with its name, nothing is read from elsewhere
	os.Remove(filepath.Join(dir, "colormap.png"))
	if _, err := StageModels(spec, dir, dst); err == nil || !strings.Contains(err.Error(), "colormap.png") {
		t.Errorf("a missing picture: %v", err)
	}
}

func TestSettingsCarryTheStagedModels(t *testing.T) {
	spec, _ := Resolve([]byte(minimal), testEnv(""))
	b, err := Params{Spec: spec, Dir: "/p", Sources: "sources", FPS: 30, ModelFiles: map[string]string{"a.glb": "/p/x/a.glb"}}.Settings("/p/out")
	if err != nil || !strings.Contains(string(b), `"model_files":{"a.glb":"/p/x/a.glb"}`) {
		t.Errorf("settings %s, %v", b, err)
	}
	b, _ = Params{Spec: spec, Dir: "/p", FPS: 30}.Settings("/p/out")
	if strings.Contains(string(b), "model_files") {
		t.Errorf("no models, no model_files: %s", b)
	}
}

func TestRewriteImages(t *testing.T) {
	src := glb(`{"asset":{"version":"2.0"},"animations":[{"name":"walk"}],"accessors":[{"count":300}],"meshes":[{"primitives":[{"attributes":{"POSITION":0},"indices":0}]}],` +
		`"images":[{"uri":"Textures/colormap.png"},{"uri":"data:image/png;base64,AAAA"},{"bufferView":0},{"uri":"Textures/colormap.png"}],"extras":{"big":9007199254740993}}`)
	out, err := RewriteImages(src, func(r ImageRef) string { return "pack--" + r.Name })
	if err != nil {
		t.Fatal(err)
	}
	m, err := ParseModel(out)
	if err != nil {
		t.Fatalf("the copy must still be a valid glb: %v", err)
	}
	if len(m.Refs) != 1 || m.Refs[0].Path != "pack--colormap.png" || strings.Join(m.Needs, ",") != "pack--colormap.png" {
		t.Errorf("the picture is renamed: %+v", m.Refs)
	}
	if strings.Join(m.Clips, ",") != "walk" || m.Triangles != 100 {
		t.Errorf("the rest is kept: %+v", m)
	}
	if !strings.Contains(string(out), "9007199254740993") || !strings.Contains(string(out), "data:image/png;base64,AAAA") {
		t.Error("numbers and embedded images are kept exactly")
	}
	if got := binary.LittleEndian.Uint32(out[8:12]); int(got) != len(out) {
		t.Errorf("the length in the header is %d, the file has %d", got, len(out))
	}
	if len(out)%4 != 0 {
		t.Errorf("chunks are aligned to four bytes: %d", len(out))
	}
	// a binary chunk after the JSON one survives byte for byte
	withBin := append(append([]byte(nil), src...), []byte{8, 0, 0, 0, 'B', 'I', 'N', 0, 1, 2, 3, 4, 5, 6, 7, 8}...)
	binary.LittleEndian.PutUint32(withBin[8:12], uint32(len(withBin)))
	out, err = RewriteImages(withBin, func(r ImageRef) string { return "x-" + r.Name })
	if err != nil || !bytes.HasSuffix(out, []byte{1, 2, 3, 4, 5, 6, 7, 8}) {
		t.Errorf("the binary chunk is untouched: %v %v", err, out[len(out)-8:])
	}
	if _, err := RewriteImages([]byte("not a glb"), nil); err == nil {
		t.Error("a file that is not a glb must fail")
	}
}

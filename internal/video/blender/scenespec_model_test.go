package blender

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// modelFile is a .glb with two clips and 100 triangles.
const modelJSON = `{"animations":[{"name":"idle"},{"name":"sprint"}],"accessors":[{"count":300}],"meshes":[{"primitives":[{"attributes":{"POSITION":0},"indices":0}]}]}`

// sourcesWithModel is a sources folder with ana.glb in it.
func sourcesWithModel(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ana.glb"), glb(modelJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// withModel is a description whose only object is the model given as JSON members.
func withModel(members string) string {
	return `{"version":1,"camera":{"keys":[{"t":0,"location":[0,-5,1]}]},"materials":{"m":{"color":"fg"}},"objects":[{"id":"ana","type":"model",` + members + `}]}`
}

func parseIn(t *testing.T, dir, doc string) (*Spec, error) {
	t.Helper()
	return Parse([]byte(doc), 5, testEnv(dir))
}

func TestModel_AValidOneComesOutResolved(t *testing.T) {
	dir := sourcesWithModel(t)
	s, err := parseIn(t, dir, withModel(`"file":"ana.glb","size":1.7,"animation":{"clip":"sprint"},"keys":[{"t":0,"location":[-4,0,0]},{"t":5,"location":[4,0,0]}]`))
	if err != nil {
		t.Fatal(err)
	}
	a := s.Objects[0].Animation
	if a == nil || *a.Speed != 1 || !*a.Loop || *a.Offset != 0 {
		t.Errorf("the defaults of a clip come out resolved: %+v", a)
	}
	if got := s.Models(); len(got) != 1 || got[0] != "ana.glb" {
		t.Errorf("Models = %v", got)
	}
	// a model without a clip is valid too (it keeps the pose it is saved in)
	if _, err := parseIn(t, dir, withModel(`"file":"ana.glb","size":1.7`)); err != nil {
		t.Errorf("a model with no animation: %v", err)
	}
	// and so is a material that replaces its own, keyed with an emission
	if _, err := parseIn(t, dir, withModel(`"file":"ana.glb","size":1,"material":"m","keys":[{"t":1,"emission":4}]`)); err != nil {
		t.Errorf("a model with a material: %v", err)
	}
}

func TestModel_Refusals(t *testing.T) {
	dir := sourcesWithModel(t)
	os.WriteFile(filepath.Join(dir, "text.glb"), []byte("just a text"), 0o644)
	os.WriteFile(filepath.Join(dir, "far.glb"), glb(`{"images":[{"uri":"../../secret.png"}]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "noclips.glb"), glb(`{"accessors":[{"count":3}]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "heavy.glb"), glb(`{"accessors":[{"count":3000000}],"meshes":[{"primitives":[{"attributes":{"POSITION":0},"indices":0}]}]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "textured.glb"), glb(`{"images":[{"uri":"Textures/colormap.png"}]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "odd.glb"), glb(`{"images":[{"uri":"x/atlas.exe"}]}`), 0o644)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "o.glb"), glb(modelJSON), 0o644)
	if err := os.Symlink(filepath.Join(outside, "o.glb"), filepath.Join(dir, "link.glb")); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct{ members, want string }{
		"no file":         {`"size":1`, "objects[0].file: is required"},
		"a path":          {`"file":"../ana.glb","size":1`, "objects[0].file"},
		"an absolute":     {`"file":"/etc/passwd","size":1`, "objects[0].file"},
		"another format":  {`"file":"ana.fbx","size":1`, "must be the plain name of a .glb"},
		"not there":       {`"file":"gone.glb","size":1`, `objects[0].file: the model "gone.glb" was not found`},
		"not a glb":       {`"file":"text.glb","size":1`, "not a valid binary glTF"},
		"points away":     {`"file":"far.glb","size":1`, "relative path of plain names"},
		"needs a picture": {`"file":"textured.glb","size":1`, "textured.glb needs the picture colormap.png, which is not in the sources"},
		"needs a bad one": {`"file":"odd.glb","size":1`, `odd.glb needs "atlas.exe", which is not a .png`},
		"a link out":      {`"file":"link.glb","size":1`, "resolves outside the sources"},
		"too heavy":       {`"file":"heavy.glb","size":1`, "1000000 triangles, at most 500000"},
		"unknown clip":    {`"file":"ana.glb","size":1,"animation":{"clip":"run"}`, `objects[0].animation.clip: "run" is not in ana.glb (clips: idle, sprint)`},
		"no clip name":    {`"file":"ana.glb","size":1,"animation":{}`, "objects[0].animation.clip: is required"},
		"file with none":  {`"file":"noclips.glb","size":1,"animation":{"clip":"walk"}`, "noclips.glb has no clips"},
		"slow":            {`"file":"ana.glb","size":1,"animation":{"clip":"idle","speed":0.01}`, "animation.speed"},
		"fast":            {`"file":"ana.glb","size":1,"animation":{"clip":"idle","speed":9}`, "animation.speed"},
		"offset":          {`"file":"ana.glb","size":1,"animation":{"clip":"idle","offset":6}`, "animation.offset: 6 is outside the scene (0 to 5 s)"},
		"no size":         {`"file":"ana.glb"`, "objects[0].size: is required"},
		"a text field":    {`"file":"ana.glb","size":1,"text":"hi"`, "only for objects of type text"},
		"a font":          {`"file":"ana.glb","size":1,"font":"display"`, "only for objects of type text"},
		"a curve":         {`"file":"ana.glb","size":1,"curve":1`, "curve is only for objects of type backdrop"},
		"unknown field":   {`"file":"ana.glb","size":1,"animations":[]`, `unknown field "animations"`},
	} {
		_, err := parseIn(t, dir, withModel(tc.members))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want it to contain %q", name, err, tc.want)
		}
	}
}

func TestModel_FileAndAnimationAreOnlyForAModel(t *testing.T) {
	dir := sourcesWithModel(t)
	for name, members := range map[string]string{
		"file":      `"type":"cube","size":1,"file":"ana.glb"`,
		"animation": `"type":"cube","size":1,"animation":{"clip":"idle"}`,
	} {
		doc := `{"version":1,"camera":{"keys":[{"t":0,"location":[0,-5,1]}]},"objects":[{"id":"a",` + members + `}]}`
		if _, err := parseIn(t, dir, doc); err == nil || !strings.Contains(err.Error(), "file and animation are only for objects of type model") {
			t.Errorf("%s on a cube: %v", name, err)
		}
	}
}

func TestModel_FitIsOnlyForAModelAndDefaultsToHeight(t *testing.T) {
	dir := sourcesWithModel(t)
	s, err := parseIn(t, dir, withModel(`"file":"ana.glb","size":1`))
	if err != nil || s.Objects[0].Fit != FitHeight {
		t.Fatalf("an absent fit is resolved to height: %v %+v", err, s)
	}
	s, err = parseIn(t, dir, withModel(`"file":"ana.glb","size":1,"fit":"largest"`))
	if err != nil || s.Objects[0].Fit != FitLargest {
		t.Fatalf("largest: %v", err)
	}
	doc := `{"version":1,"camera":{"keys":[{"t":0,"location":[0,-5,1]}]},"objects":[{"id":"a","type":"cube","size":1,"fit":"largest"}]}`
	if _, err := parseIn(t, dir, doc); err == nil || !strings.Contains(err.Error(), "objects[0].fit: fit is only for objects of type model") {
		t.Errorf("fit on a cube: %v", err)
	}
	// two ways of saying the same thing give the same resolved description (and so the same cache key)
	a, _ := parseIn(t, dir, withModel(`"file":"ana.glb","size":1`))
	b, _ := parseIn(t, dir, withModel(`"file":"ana.glb","size":1,"fit":"height"`))
	ja, _ := a.JSON()
	jb, _ := b.JSON()
	if string(ja) != string(jb) {
		t.Errorf("no fit and height must resolve the same:\n%s\n%s", ja, jb)
	}
}

func TestModel_AtMostSixteen(t *testing.T) {
	dir := sourcesWithModel(t)
	one := func(i int) string {
		return `{"id":"m` + strings.Repeat("x", i) + `","type":"model","file":"ana.glb","size":1}`
	}
	build := func(n int) string {
		var items []string
		for i := 0; i < n; i++ {
			items = append(items, one(i))
		}
		return `{"version":1,"camera":{"keys":[{"t":0,"location":[0,-5,1]}]},"objects":[` + strings.Join(items, ",") + `]}`
	}
	if _, err := parseIn(t, dir, build(16)); err != nil {
		t.Errorf("16 models: %v", err)
	}
	if _, err := parseIn(t, dir, build(17)); err == nil || !strings.Contains(err.Error(), "at most 16 models") {
		t.Errorf("17 models: %v", err)
	}
}

func TestModel_WithNoProjectItIsRefusedWithTheReason(t *testing.T) {
	env := testEnv("")
	env.NoFiles = true
	_, err := Parse([]byte(withModel(`"file":"ana.glb","size":1`)), 5, env)
	if err == nil || !strings.Contains(err.Error(), "files of a project's sources") {
		t.Errorf("no project: %v", err)
	}
}

func TestModel_AClipOfTheSameFileIsReadOnce(t *testing.T) {
	dir := sourcesWithModel(t)
	doc := `{"version":1,"camera":{"keys":[{"t":0,"location":[0,-5,1]}]},"objects":[
	{"id":"a","type":"model","file":"ana.glb","size":1,"animation":{"clip":"sprint"}},
	{"id":"b","type":"model","file":"ana.glb","size":1,"animation":{"clip":"sprint","offset":0.2}}]}`
	s, err := parseIn(t, dir, doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Models()) != 1 {
		t.Errorf("Models lists a file once: %v", s.Models())
	}
}

func TestModel_ModelsHashFollowsTheContentNotTheName(t *testing.T) {
	dir := sourcesWithModel(t)
	s, err := parseIn(t, dir, withModel(`"file":"ana.glb","size":1`))
	if err != nil {
		t.Fatal(err)
	}
	h1 := s.ModelsHash(dir)
	if h1 == "" || h1 != s.ModelsHash(dir) {
		t.Fatalf("hash %q must be stable", h1)
	}
	os.WriteFile(filepath.Join(dir, "other.glb"), glb(modelJSON), 0o644) // a file the scene does not use
	if s.ModelsHash(dir) != h1 {
		t.Error("a file the scene does not use must not change the hash")
	}
	os.WriteFile(filepath.Join(dir, "ana.glb"), glb(strings.Replace(modelJSON, "sprint", "run", 1)), 0o644)
	if s.ModelsHash(dir) == h1 {
		t.Error("a file replaced by another with the same name must change the hash")
	}
	if none, _ := parseIn(t, dir, minimal); none.ModelsHash(dir) != "" {
		t.Error("a description with no model has no models hash")
	}
}

func TestModel_ItIsNotInTheTableOfPrimitives(t *testing.T) {
	for _, ty := range ObjectTypes() {
		if ty == ModelType {
			t.Error("the writer's prompt is made from ObjectTypes: a model must not be in it when the project has none")
		}
	}
}

func TestModel_APictureItNeedsIsFoundInTheSourcesByItsLastName(t *testing.T) {
	dir := sourcesWithModel(t)
	os.WriteFile(filepath.Join(dir, "textured.glb"), glb(`{"animations":[{"name":"idle"}],"images":[{"uri":"Textures/colormap.png"}]}`), 0o644)
	if _, err := parseIn(t, dir, withModel(`"file":"textured.glb","size":1`)); err == nil {
		t.Fatal("without colormap.png it must be refused")
	}
	os.WriteFile(filepath.Join(dir, "colormap.png"), []byte("png bytes"), 0o644)
	s, err := parseIn(t, dir, withModel(`"file":"textured.glb","size":1,"animation":{"clip":"idle"}`))
	if err != nil {
		t.Fatalf("with colormap.png: %v", err)
	}
	h1 := s.ModelsHash(dir)
	os.WriteFile(filepath.Join(dir, "colormap.png"), []byte("another picture"), 0o644)
	if s.ModelsHash(dir) == h1 {
		t.Error("a texture replaced by another must change the hash")
	}
}

func TestModel_TheExampleOfTheProjectIsValid(t *testing.T) {
	s, err := Load(modelSources+"/patinadoras.scene.json", 5, testEnv(modelSources))
	if err != nil {
		t.Fatalf("the example of testdata/blender-models: %v", err)
	}
	if got := strings.Join(s.Models(), ","); got != "character-female-a.glb,character-female-c.glb" {
		t.Errorf("models of the example: %s", got)
	}
}

func TestModel_UsesSource(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "ana.glb"), glb(`{"images":[{"uri":"Textures/map.png"}]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "map.png"), []byte("png"), 0o644)
	s, err := Resolve([]byte(withModel(`"file":"ana.glb","size":1`)), testEnv(dir))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"ana.glb": true, "map.png": true, "other.glb": false, "other.png": false} {
		if got := s.UsesSource(dir, name); got != want {
			t.Errorf("UsesSource(%s) = %v, want %v", name, got, want)
		}
	}
}

package blender

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writerSources is a sources folder with a character that has clips and a texture, one whose texture is missing and a
// file that is not a model.
func writerSources(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "ana.glb"), glb(`{"animations":[{"name":"idle"},{"name":"sprint"}],"images":[{"uri":"Textures/map.png"}]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "map.png"), []byte("png"), 0o644)
	os.WriteFile(filepath.Join(dir, "lost.glb"), glb(`{"images":[{"uri":"Textures/gone.png"}]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "fake.glb"), []byte("not a glb"), 0o644)
	return dir
}

func TestWriter_ATellsTheModelWhichModelsTheProjectHas(t *testing.T) {
	env := writeEnv(120, true)
	env.Sources = writerSources(t)
	w := &scripted{answers: []string{`{"version":1,"camera":{"keys":[{"t":0,"location":[0,-6,1],"target":[0,0,0.8]},{"t":5,"location":[0,-5,1],"target":[0,0,0.8]}]},` +
		`"objects":[{"id":"girl","type":"model","file":"ana.glb","size":1.7,"animation":{"clip":"sprint"},"keys":[{"beat":0,"location":[-4,0,0]},{"beat":10,"location":[4,0,0]}]}]}`}}
	res, err := WriteDescription(context.Background(), w, request("a girl running across a rink", env))
	if err != nil {
		t.Fatal(err)
	}
	if res.Attempts != 1 || res.Spec.Objects[0].File != "ana.glb" {
		t.Errorf("%+v", res)
	}
	user := w.user[0]
	for _, want := range []string{"MODELS:", "- ana.glb: clips idle, sprint", `"type":"model"`, "HEIGHT in metres", "IN PLACE", `"fit":"largest"`, "LONGEST dimension", "a plate, a rug, food"} {
		if !strings.Contains(user, want) {
			t.Errorf("the prompt does not say %q:\n%s", want, user)
		}
	}
	for _, not := range []string{"lost.glb", "fake.glb"} {
		if strings.Contains(user, not) {
			t.Errorf("the prompt lists %s, which cannot be used", not)
		}
	}
}

func TestWriter_AProjectWithoutModelsGetsThePromptItAlwaysHad(t *testing.T) {
	plain := request("a spinning cube", writeEnv(120, true))
	withEmptySources := plain
	withEmptySources.Env.Sources = t.TempDir()
	withOnlyBroken := plain
	withOnlyBroken.Env.Sources = t.TempDir()
	os.WriteFile(filepath.Join(withOnlyBroken.Env.Sources, "x.glb"), []byte("nope"), 0o644)
	want := writerUserPrompt(plain)
	for name, r := range map[string]WriteRequest{"empty sources": withEmptySources, "only a broken glb": withOnlyBroken} {
		if got := writerUserPrompt(r); got != want {
			t.Errorf("%s: the prompt changed:\n%s", name, got)
		}
	}
	if strings.Contains(want, "model") {
		t.Error("the prompt of a project with no models must not mention models")
	}
	if strings.Contains(writerSystemPrompt(ExampleFor(120, 5)), "fit") || strings.Contains(want, "fit") {
		t.Error("neither the fixed part nor the prompt of a project without models mentions fit")
	}
	if strings.Contains(writerSystemPrompt(ExampleFor(120, 5)), "model") {
		t.Error("the fixed part of the prompt does not mention models: only the project part does")
	}
}

func TestWriter_ANoProjectSceneHasNoModels(t *testing.T) {
	env := writeEnv(0, false)
	env.NoFiles = true
	env.Sources = writerSources(t) // even if a folder were given
	if got := ProjectModels(env); len(got) != 0 {
		t.Errorf("no project, no models: %v", got)
	}
}

func TestWriter_AnInventedFileOrClipIsAskedForAgain(t *testing.T) {
	env := writeEnv(0, false)
	env.Sources = writerSources(t)
	bad := `{"version":1,"camera":{"keys":[{"t":0,"location":[0,-6,1]},{"t":5,"location":[0,-5,1]}]},"objects":[{"id":"g","type":"model","file":"skater.glb","size":1.7}]}`
	badClip := `{"version":1,"camera":{"keys":[{"t":0,"location":[0,-6,1]},{"t":5,"location":[0,-5,1]}]},"objects":[{"id":"g","type":"model","file":"ana.glb","size":1.7,"animation":{"clip":"skate"}}]}`
	good := `{"version":1,"camera":{"keys":[{"t":0,"location":[0,-6,1]},{"t":5,"location":[0,-5,1]}]},"objects":[{"id":"g","type":"model","file":"ana.glb","size":1.7,"animation":{"clip":"idle"}}]}`
	w := &scripted{answers: []string{bad, badClip, good}}
	res, err := WriteDescription(context.Background(), w, request("a girl", env))
	if err != nil || res.Attempts != 3 {
		t.Fatalf("attempts %d, %v", res.Attempts, err)
	}
	if !strings.Contains(w.user[1], `objects[0].file: the model "skater.glb" was not found`) {
		t.Errorf("the first repair does not name the file:\n%s", w.user[1])
	}
	if !strings.Contains(w.user[2], `"skate" is not in ana.glb (clips: idle, sprint)`) {
		t.Errorf("the second repair does not list the clips:\n%s", w.user[2])
	}
}

func TestWriter_AFlatModelWithFitLargestPassesTheChecks(t *testing.T) {
	env := writeEnv(0, false)
	env.Sources = writerSources(t)
	doc := `{"version":1,"camera":{"keys":[{"t":0,"location":[0,-6,1]},{"t":5,"location":[0,-5,1]}]},"objects":[` +
		`{"id":"plate","type":"model","file":"ana.glb","size":0.3,"fit":"largest","keys":[{"t":0,"location":[0,0,0]},{"t":5,"rotation":[0,0,360]}]}]}`
	w := &scripted{answers: []string{doc}}
	res, err := WriteDescription(context.Background(), w, request("a plate turning", env))
	if err != nil || res.Attempts != 1 || res.Spec.Objects[0].Fit != FitLargest {
		t.Fatalf("%+v %v", res, err)
	}
}

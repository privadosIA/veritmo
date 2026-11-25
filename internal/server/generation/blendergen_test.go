package generation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/project/config"
	"veritmo/internal/video/blender"
)

// a description in seconds with explicit colours: a generation has no design and no tempo
const genScene = `{"version":1,"camera":{"keys":[{"t":0,"location":[0,-9,1.2],"target":[0,0,0.6]},{"t":4,"location":[0,-7,1],"target":[0,0,0.6]}]},` +
	`"materials":{"m":{"color":"#ff3b30"}},"objects":[{"id":"title","type":"text","text":"Hola","size":1,"material":"m","keys":[{"t":0,"scale":1},{"t":2,"scale":1.2,"ease":"back"}]}]}`

func blenderServer(t *testing.T) *testServer {
	t.Helper()
	s := genServer(t, fullCatalog())
	s.blenderProbeFn = func(context.Context) blenderProbe {
		return blenderProbe{Info: blender.Info{Path: "/usr/bin/blender", Version: "5.2.3"}}
	}
	return s
}

func getOptions(t *testing.T, s *testServer) generationOptions {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/generation/options", nil)
	var o generationOptions
	if err := json.Unmarshal(rec.Body.Bytes(), &o); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	return o
}

func TestBlenderOption_IsListedAfterTheOthersAndNeverTheDefault(t *testing.T) {
	s := blenderServer(t)
	var b *videoOptionView
	defaults := 0
	for i, o := range getOptions(t, s).Video {
		if o.Default {
			defaults++
		}
		if o.ID == optionBlender {
			b = &getOptions(t, s).Video[i]
		} else if o.Inputs != nil && o.Inputs.Scene {
			t.Errorf("option %s must not take a scene", o.ID)
		}
	}
	if b == nil || !b.Available || b.Default || b.Reason != "" || b.Inputs == nil || !b.Inputs.Scene || b.Inputs.StartImage || b.Inputs.EndImage || b.Inputs.ReferenceImages != nil ||
		b.Duration.Min != 1 || b.Duration.Max != 30 || b.Duration.Default != 5 || defaults != 1 {
		t.Fatalf("blender option = %+v (defaults %d)", b, defaults)
	}
	// without Blender the option says why, and the others do not change
	s.blenderProbeFn = func(context.Context) blenderProbe {
		return blenderProbe{Err: fmt.Errorf("%w: %s", blender.ErrNotInstalled, blender.InstallHint)}
	}
	for _, o := range getOptions(t, s).Video {
		if o.ID == optionBlender && (o.Available || !strings.Contains(o.Reason, "pacman -S blender")) {
			t.Errorf("blender without Blender = %+v", o)
		}
	}
	rec := postGeneration(t, s, map[string]any{"option": "blender", "duration": 4, "blender": map[string]any{"description": json.RawMessage(genScene)}})
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("an unavailable option is 503: %d %s", rec.Code, rec.Body)
	}
}

func TestPlanBlender_TheAspectAndTheQualityShapeTheScene(t *testing.T) {
	want := map[string]string{"16:9": "1280x720", "9:16": "720x1280", "1:1": "1280x1280", "4:3": "1280x960", "3:4": "960x1280"}
	for aspect, res := range want {
		p, err := planBlender("gen-x", "gen-x.scene.json", generationRequest{Duration: 6, AspectRatio: aspect, Quality: "high"}, "voice.onnx")
		if err != nil {
			t.Fatal(err)
		}
		sc := p.Config.Scenes[0]
		if p.Config.Defaults.Resolution != res || sc.Blender == nil || sc.Blender.Build != "gen-x.scene.json" || *sc.SilenceSeconds != 6 || p.ClipSeconds != 6 || p.Config.Defaults.FPS != 30 {
			t.Errorf("%s: %+v %+v", aspect, p.Config.Defaults, sc)
		}
	}
	draft, _ := planBlender("g", "g.scene.json", generationRequest{Duration: 2, AspectRatio: "16:9", Quality: "draft"}, "")
	high, _ := planBlender("g", "g.scene.json", generationRequest{Duration: 2, AspectRatio: "16:9", Quality: "high"}, "")
	if draft.Config.Defaults.Resolution != "854x480" || draft.Config.Scenes[0].Blender.Samples >= high.Config.Scenes[0].Blender.Samples {
		t.Errorf("draft %+v high %+v", draft.Config.Scenes[0].Blender, high.Config.Scenes[0].Blender)
	}
	if _, err := planBlender("g", "g.scene.json", generationRequest{AspectRatio: "21:9", Quality: "high"}, ""); err == nil {
		t.Error("an aspect that is not one of the list is refused")
	}
	// the project validates with its description in the sources
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "sources"), 0o755)
	os.WriteFile(filepath.Join(dir, "sources", "g.scene.json"), []byte(genScene), 0o644)
	p, _ := planBlender("g", "g.scene.json", generationRequest{Duration: 4, AspectRatio: "16:9", Quality: "normal"}, "")
	p.Config.SkipBlenderCheck = true
	p.Config.Defaults.Voice = ""
	if err := p.Config.ValidateInDir(dir); err != nil {
		t.Errorf("the plan must validate: %v", err)
	}
	_ = config.ModeVideo
}

func TestGenerations_ABlenderSceneBecomesAVideoAndIsAnInput(t *testing.T) {
	s := blenderServer(t)
	rec := postGeneration(t, s, map[string]any{"option": "blender", "duration": 4, "aspect_ratio": "9:16", "quality": "normal", "blender": map[string]any{"description": json.RawMessage(genScene)}})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	g := decodeGeneration(t, rec)
	if strings.Contains(rec.Body.String(), `"Hola"`) || g.Request.Blender == nil || len(g.Request.Blender.Description) != 0 || g.ClipSeconds != 4 {
		t.Errorf("the description is not part of the request that is reported: %s", rec.Body)
	}
	scene := g.ID + ".scene.json"
	if len(g.Inputs) != 1 || g.Inputs[0].Kind != "scene" || g.Inputs[0].Name != scene || !strings.HasSuffix(g.Inputs[0].Address, "/sources/"+scene) {
		t.Fatalf("inputs = %+v", g.Inputs)
	}
	stored, err := os.ReadFile(filepath.Join(s.Workspace.ProjectDir(g.ID), "sources", scene))
	if err != nil || !strings.Contains(string(stored), `"Hola"`) {
		t.Fatalf("the description is in the sources: %v %s", err, stored)
	}
	yml, _ := os.ReadFile(filepath.Join(s.Workspace.ProjectDir(g.ID), "scenes.yaml"))
	if !strings.Contains(string(yml), "build: "+scene) || !strings.Contains(string(yml), "720x1280") {
		t.Errorf("scenes.yaml:\n%s", yml)
	}
	// the address answers the JSON, with the access of the video
	got := doJSON(t, s.Handler(), http.MethodGet, g.Inputs[0].Address, nil)
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"Hola"`) {
		t.Errorf("address: %d %s", got.Code, got.Body)
	}
	waitGeneration(t, s, g.ID, GenDone)
}

func TestGenerations_ABlenderRequestThatCannotBeCreatesNothing(t *testing.T) {
	s := blenderServer(t)
	count := func() int {
		var l struct{ Items []json.RawMessage }
		rec := doJSON(t, s.Handler(), http.MethodGet, "/api/generations", nil)
		var raw json.RawMessage = rec.Body.Bytes()
		if err := json.Unmarshal(raw, &l.Items); err != nil {
			t.Fatalf("%v %s", err, raw)
		}
		return len(l.Items)
	}
	late := strings.Replace(genScene, `{"t":4,"location"`, `{"t":9,"location"`, 1)
	cases := map[string]struct {
		body map[string]any
		want string
	}{
		"a key beyond the duration": {map[string]any{"option": "blender", "duration": 4, "blender": map[string]any{"description": json.RawMessage(late)}}, "camera"},
		"not JSON for the contract": {map[string]any{"option": "blender", "duration": 4, "blender": map[string]any{"description": map[string]any{"version": 1}}}, "does not pass"},
		"another option":            {map[string]any{"option": "ltx", "prompt": "x", "blender": map[string]any{"description": json.RawMessage(genScene)}}, "only the option"},
		"no description":            {map[string]any{"option": "blender", "duration": 4}, "needs a 3D scene"},
		"too short":                 {map[string]any{"option": "blender", "duration": 0.5, "blender": map[string]any{"description": json.RawMessage(genScene)}}, "outside what option blender accepts"},
		"too long":                  {map[string]any{"option": "blender", "duration": 31, "blender": map[string]any{"description": json.RawMessage(genScene)}}, "outside what option blender accepts"},
		"with audio":                {map[string]any{"option": "blender", "duration": 4, "audio": true, "blender": map[string]any{"description": json.RawMessage(genScene)}}, "cannot write audio"},
	}
	for name, c := range cases {
		rec := postGeneration(t, s, c.body)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	pic := postParts(t, s, map[string]any{"option": "blender", "duration": 4, "blender": map[string]any{"description": json.RawMessage(genScene)}}, testPart{"image", "a.png", "x"})
	if pic.Code != http.StatusUnprocessableEntity || !strings.Contains(pic.Body.String(), "no pictures") {
		t.Errorf("with a picture: %d %s", pic.Code, pic.Body)
	}
	if n := count(); n != 0 {
		t.Errorf("no generation may exist after the refusals: %d", n)
	}
}

func TestGenerations_ReusingAScene(t *testing.T) {
	s := blenderServer(t)
	first := decodeGeneration(t, postGeneration(t, s, map[string]any{"option": "blender", "duration": 4, "blender": map[string]any{"description": json.RawMessage(genScene)}}))
	// as it was
	again := decodeGeneration(t, postGeneration(t, s, map[string]any{"option": "blender", "duration": 4, "reuse_inputs_of": first.ID}))
	if len(again.Inputs) != 1 || again.Inputs[0].Kind != "scene" || again.Inputs[0].Name != again.ID+".scene.json" {
		t.Fatalf("inputs = %+v", again.Inputs)
	}
	copied, err := os.ReadFile(filepath.Join(s.Workspace.ProjectDir(again.ID), "sources", again.Inputs[0].Name))
	if err != nil || !strings.Contains(string(copied), `"Hola"`) {
		t.Errorf("the scene is copied: %v %s", err, copied)
	}
	// with a changed scene: the one sent wins
	changed := strings.Replace(genScene, `"Hola"`, `"Adios"`, 1)
	third := decodeGeneration(t, postGeneration(t, s, map[string]any{"option": "blender", "duration": 4, "reuse_inputs_of": first.ID, "blender": map[string]any{"description": json.RawMessage(changed)}}))
	got, _ := os.ReadFile(filepath.Join(s.Workspace.ProjectDir(third.ID), "sources", third.ID+".scene.json"))
	if len(third.Inputs) != 1 || !strings.Contains(string(got), `"Adios"`) {
		t.Errorf("the description sent wins: %+v %s", third.Inputs, got)
	}
	if orig, _ := os.ReadFile(filepath.Join(s.Workspace.ProjectDir(first.ID), "sources", first.ID+".scene.json")); !strings.Contains(string(orig), `"Hola"`) {
		t.Errorf("the first generation is untouched: %s", orig)
	}
	// reused for a duration the scene does not fit: refused naming the fault
	if rec := postGeneration(t, s, map[string]any{"option": "blender", "duration": 1, "reuse_inputs_of": first.ID}); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "camera") {
		t.Errorf("a reused scene is checked for the new duration: %d %s", rec.Code, rec.Body)
	}
	// another option cannot use it
	if rec := postGeneration(t, s, map[string]any{"option": "ltx", "reuse_inputs_of": first.ID, "prompt": "x"}); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "3D scene") {
		t.Errorf("another option: %d %s", rec.Code, rec.Body)
	}
}

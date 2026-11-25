package musicmotion

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/project/config"
)

// a description of a 3D scene in beats of a 120 bpm song (2 beats are 1 s), with explicit colours
const pieceScene = `{"version":1,"camera":{"keys":[{"beat":0,"location":[0,-9,1.2],"target":[0,0,0.6]},{"beat":16,"location":[0,-7,1],"target":[0,0,0.6]}]},` +
	`"materials":{"m":{"color":"#ff3b30"}},"objects":[{"id":"ball","type":"sphere","size":1,"material":"m","keys":[{"beat":0,"location":[0,0,2]},{"beat":8,"location":[0,0,0.5],"ease":"in"}]}]}`

func blenderScript() Script {
	s := testScript()
	s.Pieces[1] = Piece{ID: "p2", Start: 10, End: 20, Section: "Chorus", Blender: &PieceBlender{Description: json.RawMessage(pieceScene)}}
	return s
}

func TestABlenderPieceIsAValidPieceOfTheScript(t *testing.T) {
	cat := Catalog{Builtin: BuiltinParts()}
	s := blenderScript()
	if err := s.Validate(cat, testMap(), good()); err != nil {
		t.Fatalf("a 3D chorus in a 120 bpm song: %v", err)
	}
	if !s.Pieces[1].IsBlender() || s.Pieces[0].IsBlender() {
		t.Error("IsBlender")
	}
	// it survives the file
	raw, _ := json.Marshal(s)
	var back Script
	if err := json.Unmarshal(raw, &back); err != nil || back.Pieces[1].Blender == nil || !strings.Contains(string(back.Pieces[1].Blender.Description), `"sphere"`) {
		t.Fatalf("round trip: %v %s", err, raw)
	}
	if err := s.CheckSceneParts(cat, "api-mm-x"); err != nil {
		t.Errorf("a 3D piece has no scene part: %v", err)
	}
}

func TestEachBadBlenderPieceIsRefusedAndNamed(t *testing.T) {
	cat := Catalog{Builtin: BuiltinParts()}
	zero := 0
	cases := []struct {
		name string
		edit func(*Piece)
		want string
	}{
		{"a part too", func(p *Piece) { p.Part = "stamp" }, "not both"},
		{"params", func(p *Piece) { p.Params = map[string]any{"a": 1} }, "cannot have params"},
		{"lines", func(p *Piece) { p.Lines = []ScriptLine{{Line: &zero}} }, "cannot have lines"},
		{"a hit", func(p *Piece) { p.Hit = &ScriptHit{Word: "x", At: 12, Length: 1} }, "cannot have hit"},
		{"a mode", func(p *Piece) { p.Mode = ModeLight }, "cannot have mode"},
		{"a design", func(p *Piece) { p.Design = &DesignOverride{} }, "cannot have design"},
		{"a stage", func(p *Piece) { p.Stage = &Stage{} }, "cannot have stage"},
		{"extras", func(p *Piece) { p.With = []PartRef{{Part: "stamp"}} }, "cannot have with"},
		{"no description", func(p *Piece) { p.Blender.Description = nil }, "no description"},
		{"not a scene", func(p *Piece) { p.Blender.Description = json.RawMessage(`{"version":1}`) }, "does not pass"},
		{"a key beyond the piece", func(p *Piece) {
			p.Blender.Description = json.RawMessage(strings.Replace(pieceScene, `"beat":16`, `"beat":30`, 1)) // 15 s in a piece of 10
		}, "camera"},
		{"a font file", func(p *Piece) {
			p.Blender.Description = json.RawMessage(`{"version":1,"camera":{"keys":[{"t":0,"location":[0,-9,1],"target":[0,0,0]},{"t":2,"location":[0,-7,1],"target":[0,0,0]}]},` +
				`"materials":{"m":{"color":"#ffffff"}},"objects":[{"id":"t","type":"text","text":"x","size":1,"font":"x.ttf","material":"m","keys":[{"t":0,"scale":1},{"t":1,"scale":2}]}]}`)
		}, "no project"},
	}
	for _, c := range cases {
		s := blenderScript()
		c.edit(&s.Pieces[1])
		err := s.Validate(cat, testMap(), good())
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), `"p2"`) {
			t.Errorf("%s: got %v, want an error naming p2 with %q", c.name, err, c.want)
		}
	}
	// a beat needs the song's tempo
	s := blenderScript()
	m := testMap()
	m.BPM = 0
	if err := s.Validate(cat, m, good()); err == nil || !strings.Contains(err.Error(), "beat") {
		t.Errorf("a beat key in a song with no tempo: %v", err)
	}
}

// mixedSpec is a project whose second piece is a 3D scene, in a song of 20 s at 120 bpm.
func mixedSpec(t *testing.T) ProjectSpec {
	t.Helper()
	dir := t.TempDir()
	song := filepath.Join(dir, "in.wav")
	if err := os.WriteFile(song, []byte("RIFF....WAVE"), 0o644); err != nil {
		t.Fatal(err)
	}
	return ProjectSpec{Dir: filepath.Join(dir, "proj"), Name: "piece-3d", SongFile: song, Map: testMap(), Design: good(), Script: blenderScript(),
		Cat: Catalog{Builtin: BuiltinParts()}, FPS: 30, Width: 640, Height: 360}
}

func TestEveryOperationOverAMixedScriptKnowsA3DPiece(t *testing.T) {
	spec := mixedSpec(t)
	cat := spec.Cat
	// pages: only the piece made from a part has one, and asking for the 3D one is an error that says why
	pages, err := BuildPages(spec, nil)
	if err != nil || len(pages) != 1 || pages[0].ID != "p1" {
		t.Fatalf("pages = %v %v", pages, err)
	}
	if _, err := BuildPages(spec, []string{"p2"}); err == nil || !strings.Contains(err.Error(), "3D scene") {
		t.Errorf("a page of a 3D piece: %v", err)
	}
	if _, err := SceneOfPiece(spec.Script.Pieces[1], spec); err == nil {
		t.Error("a 3D piece has no scene of a page")
	}
	// crossfades and the estimate count it by its length
	if f := Crossfades(spec.Script, spec.Design); len(f) != 2 {
		t.Errorf("crossfades = %v", f)
	}
	parts := map[string]Part{}
	for _, p := range cat.List() {
		parts[p.Name] = p
	}
	sec, frames := raw(spec.Script, parts, 30, 1280, 720)
	if frames != 600 || sec < 10*30*DefaultBlenderCostMS/1000.0 {
		t.Errorf("estimate = %v s for %d frames", sec, frames)
	}
	// parts in use, scene parts, fallback pieces, drafts and layers do not meet an empty part
	if spec.Script.UsesPart("stamp") || !spec.Script.UsesPart("toggle") {
		t.Error("the parts in use are those of the pieces made from parts")
	}
	if f := FallbackPiecesWith(spec.Script, parts); len(f) != 1 || f[0] != "p1" {
		t.Errorf("a 3D piece is not a fallback: %v", f)
	}
	if err := spec.Script.validateLayers(cat); err != nil {
		t.Errorf("layers: %v", err)
	}
	in, err := BuildSceneInput(spec, SceneRef{Piece: "x", ScriptPiece: "p1"}, "")
	if err != nil || len(in.Neighbours) != 1 || in.Neighbours[0].Part != "3D scene" {
		t.Errorf("a draft of the piece next to a 3D one: %v %+v", err, in.Neighbours)
	}
	if _, err := BuildSceneInput(spec, SceneRef{Piece: "x", ScriptPiece: "p2"}, ""); err == nil {
		t.Error("a scene part is not written for a 3D piece")
	}
	// the beat phase: 120 bpm beats every half second, so a piece that starts on a beat has no phase
	if p := BeatPhase(spec.Map, 10); p != 0 {
		t.Errorf("phase on a beat = %v", p)
	}
}

func TestTheProjectOfAMixedPieceHasTheSceneItsFileAndTheTempo(t *testing.T) {
	spec := mixedSpec(t)
	files, err := WriteProject(spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[1].Blender != "p2.scene.json" || files[1].Part != "" || files[1].PagePath != "" || files[1].Seconds != 10 {
		t.Fatalf("files = %+v", files)
	}
	yml, _ := os.ReadFile(filepath.Join(spec.Dir, "scenes.yaml"))
	for _, want := range []string{"blender: {build: p2.scene.json}", "motion:", "bpm: 120", "design:", "motion_graphic:"} {
		if !strings.Contains(string(yml), want) {
			t.Errorf("scenes.yaml lacks %q:\n%s", want, yml)
		}
	}
	stored, err := os.ReadFile(filepath.Join(spec.Dir, "sources", "p2.scene.json"))
	if err != nil || !strings.Contains(string(stored), `"sphere"`) {
		t.Fatalf("the description is in the sources: %v %s", err, stored)
	}
	cfg, err := config.LoadBytes(yml)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Scenes[1].Blender == nil || cfg.Scenes[1].Blender.Build != "p2.scene.json" || cfg.Defaults.Motion == nil || *cfg.Defaults.Motion.BPM != 120 {
		t.Errorf("scenes = %+v motion = %+v", cfg.Scenes[1], cfg.Defaults.Motion)
	}
	// the piece stops being 3D: its description is removed from the sources, and there is no defaults.motion
	spec.Script = testScript()
	if _, err := WriteProject(spec); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(spec.Dir, "sources", "p2.scene.json")); err == nil {
		t.Error("a stale description must be removed")
	}
	yml, _ = os.ReadFile(filepath.Join(spec.Dir, "scenes.yaml"))
	if strings.Contains(string(yml), "motion:") || strings.Contains(string(yml), "blender") {
		t.Errorf("a project with no 3D piece does not change:\n%s", yml)
	}
}

func TestBeatPhaseSaysWhereTheFirstBeatFallsInThePiece(t *testing.T) {
	m := testMap()
	m.Beats = []float64{9.5, 10.0, 10.5, 11.0}
	if p := BeatPhase(m, 10.2); p != 0.3 {
		t.Errorf("a piece that starts between beats: %v", p)
	}
	if p := BeatPhase(m, 10.0); p != 0 {
		t.Errorf("a piece on a beat: %v", p)
	}
	if p := BeatPhase(m, 12); p != 0 {
		t.Errorf("no beat after the start: %v", p)
	}
	m.Beats = nil
	if p := BeatPhase(m, 10.2); p != 0 {
		t.Errorf("a song with no beats: %v", p)
	}
}

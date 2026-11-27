package musicmotion

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeClip writes a short video with ffmpeg into the sources folder of a project.
func makeClip(t *testing.T, sources, name string, seconds string) {
	t.Helper()
	if err := os.MkdirAll(sources, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc=duration="+seconds+":size=320x180:rate=16", "-pix_fmt", "yuv420p", filepath.Join(sources, name))
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
}

func TestAPieceOverAnItemOfTheTrackIsALayerAndTheTrackIsInTheProject(t *testing.T) {
	needTools(t)
	dir := t.TempDir()
	spec := shortSpec(t, filepath.Join(dir, "proj"), makeSongOrFake(t, dir))
	makeClip(t, filepath.Join(spec.Dir, "sources"), "a.mp4", "4")
	makeClip(t, filepath.Join(spec.Dir, "sources"), "b.mp4", "4")
	// a clip over the first piece and the start of the second, and another over the rest, with a fade between them
	spec.Script.Pieces[0].Part, spec.Script.Pieces[1].Part = "blank", "blank"
	spec.Script.Video = []VideoItem{
		{ID: "v1", Clip: "a.mp4", Start: 0, End: 5, ClipStart: f64(0.5), ClipEnd: f64(3), ClipSpeed: f64(0.5), Scrim: f64(0.4)},
		{ID: "v2", Clip: "b.mp4", Start: 5, End: 8, TransitionSeconds: f64(0.5)},
	}
	files, err := WriteProject(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !files[0].Layer || !files[1].Layer {
		t.Fatalf("both pieces are over the track: %+v", files)
	}
	cfg := loadProject(t, spec.Dir)
	if err := cfg.ValidateInDir(spec.Dir); err != nil {
		t.Fatalf("the project must be valid: %v", err)
	}
	if len(cfg.VideoTrack) != 2 || cfg.VideoTrack[0].ID != "v1" || *cfg.VideoTrack[0].ClipStart != 0.5 || *cfg.VideoTrack[0].ClipSpeed != 0.5 || *cfg.VideoTrack[0].Scrim != 0.4 ||
		cfg.VideoTrack[1].Start != 5 || *cfg.VideoTrack[1].TransitionSeconds != 0.5 {
		t.Fatalf("the track: %+v", cfg.VideoTrack)
	}
	for i, sc := range cfg.Scenes {
		if !sc.Layer || sc.MotionGraphic == "" || sc.Clip != "" || sc.Overlay != nil || !sc.IsSilent() || sc.SilenceDuration() != files[i].Seconds {
			t.Errorf("scene %s is a layer made from a page: %+v", sc.ID, sc)
		}
		page, err := os.ReadFile(filepath.Join(spec.Dir, "motion", sc.ID+".html"))
		if err != nil || !strings.Contains(string(page), "html,body{background:transparent}") || strings.Contains(string(page), `id="scrim"`) {
			t.Errorf("the page of a layer is transparent and has no scrim (the item has it): %v", err)
		}
		if _, err := os.Stat(filepath.Join(spec.Dir, "motion", sc.ID+".overlay.html")); err == nil {
			t.Errorf("no overlay page for %s", sc.ID)
		}
	}
	if cfg.Defaults.Motion == nil || cfg.Defaults.Motion.Design == nil {
		t.Error("the colour of the gaps and the scrim comes from the song's design")
	}
}

func TestAPieceOverNoItemIsAPlainSceneEvenWithATrack(t *testing.T) {
	dir := t.TempDir()
	spec := shortSpec(t, filepath.Join(dir, "proj"), makeSongOrFake(t, dir))
	spec.Script.Pieces[0].Part = "blank"
	spec.Script.Video = []VideoItem{{ID: "v1", Clip: "a.mp4", Start: 0, End: 3}} // only over the first piece
	files, err := WriteProject(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !files[0].Layer || files[1].Layer {
		t.Fatalf("%+v", files)
	}
	raw, _ := os.ReadFile(filepath.Join(spec.Dir, "scenes.yaml"))
	y := string(raw)
	if strings.Count(y, "layer: true") != 1 || !strings.Contains(y, "video_track:\n  - {id: v1, clip: a.mp4, start: 0, end: 3}") {
		t.Fatalf("%s", y)
	}
	page, _ := os.ReadFile(filepath.Join(spec.Dir, "motion", "p2.html"))
	if strings.Contains(string(page), "html,body{background:transparent}") {
		t.Fatal("a piece in a gap keeps the design's own background")
	}
}

func TestTheBackgroundOfAPieceIsWrittenAsTheItemItBecomes(t *testing.T) {
	dir := t.TempDir()
	spec := shortSpec(t, filepath.Join(dir, "proj"), makeSongOrFake(t, dir))
	spec.Script.Pieces[0].Part = "blank"
	spec.Script.Pieces[0].Background = &PieceBackground{Clip: "a.mp4", Scrim: f64(0.2)}
	if _, err := WriteProject(spec); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(spec.Dir, "scenes.yaml"))
	if !strings.Contains(string(raw), "- {id: bg-p1, clip: a.mp4, start: 0, end: 4, scrim: 0.2}") || strings.Count(string(raw), "layer: true") != 1 {
		t.Fatalf("%s", raw)
	}
	if spec.Script.Pieces[0].Background == nil {
		t.Fatal("the spec that was given is not changed")
	}
}

func TestTakingTheTrackOffGivesTheOpaquePageBack(t *testing.T) {
	dir := t.TempDir()
	spec := shortSpec(t, filepath.Join(dir, "proj"), makeSongOrFake(t, dir))
	spec.Script.Pieces[0].Part = "blank"
	spec.Script.Video = []VideoItem{{ID: "v1", Clip: "a.mp4", Start: 0, End: 4}}
	if _, err := WriteProject(spec); err != nil {
		t.Fatal(err)
	}
	page := filepath.Join(spec.Dir, "motion", "p1.html")
	if raw, _ := os.ReadFile(page); !strings.Contains(string(raw), "html,body{background:transparent}") {
		t.Fatal("a layer is transparent")
	}
	// a page an earlier version wrote as an overlay, and the metadata a run left, go
	over := filepath.Join(spec.Dir, "motion", "p1.overlay.html")
	meta := filepath.Join(spec.Dir, "motion", "p1.overlay.meta.json")
	os.WriteFile(over, []byte("old"), 0o644)
	os.WriteFile(meta, []byte("{}"), 0o644)
	spec.Script.Video = nil
	if _, err := WriteProject(spec); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(page); strings.Contains(string(raw), "html,body{background:transparent}") {
		t.Error("without the track the page paints its own background again")
	}
	for _, gone := range []string{over, meta} {
		if _, err := os.Stat(gone); err == nil {
			t.Errorf("%s is still there", gone)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(spec.Dir, "scenes.yaml"))
	if strings.Contains(string(raw), "video_track") || strings.Contains(string(raw), "layer: true") {
		t.Fatalf("no track, no layer:\n%s", raw)
	}
}

func TestAMixedScriptWritesLayersAMotionSceneAndA3DScene(t *testing.T) {
	dir := t.TempDir()
	spec := shortSpec(t, filepath.Join(dir, "proj"), makeSongOrFake(t, dir))
	third := spec.Script.Pieces[1]
	spec.Script.Pieces[1].End = 6
	third.ID, third.Start = "p3", 6
	third.MakeBlender(json.RawMessage(pieceScene))
	spec.Script.Pieces = append(spec.Script.Pieces, third)
	spec.Script.Pieces[2].End = 8
	spec.Script.Pieces[0].Part, spec.Script.Pieces[0].Params = "blank", nil
	spec.Script.Video = []VideoItem{{ID: "v1", Clip: "a.mp4", Start: 0, End: 4}}
	if _, err := WriteProject(spec); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(spec.Dir, "scenes.yaml"))
	y := string(raw)
	for _, want := range []string{"video_track:", "- id: p1\n    motion_graphic:", "    layer: true\n", "- id: p2\n    motion_graphic:", "- id: p3\n    blender: {build: p3.scene.json}", "  motion:\n"} {
		if !strings.Contains(y, want) {
			t.Errorf("scenes.yaml lacks %q:\n%s", want, y)
		}
	}
}

// The local scrims an item was given are written into its item of scenes.yaml, where the pipeline reads them, and an item with none has no field.
func TestTheLocalScrimsOfAnItemAreWrittenIntoTheProject(t *testing.T) {
	needTools(t)
	dir := t.TempDir()
	spec := shortSpec(t, filepath.Join(dir, "proj"), makeSongOrFake(t, dir))
	makeClip(t, filepath.Join(spec.Dir, "sources"), "a.mp4", "4")
	makeClip(t, filepath.Join(spec.Dir, "sources"), "b.mp4", "4")
	spec.Script.Pieces[0].Part, spec.Script.Pieces[1].Part = "blank", "blank"
	spec.Script.Video = []VideoItem{
		{ID: "v1", Clip: "a.mp4", Start: 0, End: 5, Local: []LocalScrim{{Band: "bottom", Scrim: 0.55}, {Band: "top", Scrim: 0.3}}},
		{ID: "v2", Clip: "b.mp4", Start: 5, End: 8},
	}
	if _, err := WriteProject(spec); err != nil {
		t.Fatal(err)
	}
	cfg := loadProject(t, spec.Dir)
	if err := cfg.ValidateInDir(spec.Dir); err != nil {
		t.Fatalf("the project must be valid: %v", err)
	}
	got := cfg.VideoTrack[0].LocalScrims
	if len(got) != 2 || got[0].Band != "bottom" || got[0].Scrim != 0.55 || got[1].Band != "top" || got[1].Scrim != 0.3 || len(cfg.VideoTrack[1].LocalScrims) != 0 {
		t.Fatalf("%+v", cfg.VideoTrack)
	}
	raw, _ := os.ReadFile(filepath.Join(spec.Dir, "scenes.yaml"))
	if strings.Count(string(raw), "local_scrims") != 1 {
		t.Errorf("only the item that has them says so:\n%s", raw)
	}
}

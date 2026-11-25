package importer

import (
	"os"
	"path/filepath"
	"testing"

	"veritmo/internal/music/musicmotion" // the catalog of parts: it checks the parts a dataset names
)

// The sample datasets of docs/examples are the ones the documentation walks through: they must keep importing
// as the documentation says.
func TestDocsExamplesImport(t *testing.T) {
	load := func(name string) ([]Entry, []Rejection) {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "examples", name))
		if err != nil {
			t.Fatal(err)
		}
		e, r, err := LoadBytes(b)
		if err != nil {
			t.Fatal(err)
		}
		return e, r
	}
	opts := Options{Project: "example", SilenceSeconds: 2, Parts: musicmotion.Parts{}}

	e, r := load("scenes.json")
	res, err := Map(e, r, opts)
	if err != nil || res.Imported != 3 || res.Silent != 1 || res.Rejected != 0 {
		t.Fatalf("scenes: %v %+v", err, res.Reports)
	}
	if err := res.Validate(); err != nil {
		t.Fatal(err)
	}

	e, r = load("graphics.json")
	res, err = MapMotion(e, r, opts)
	if err != nil || res.Imported != 3 || res.Silent != 3 || res.Rejected != 0 {
		t.Fatalf("graphics: %v %+v", err, res.Reports)
	}
	if err := res.Validate(); err != nil {
		t.Fatal(err)
	}
	if res.Config.Scenes[0].Overlay == nil || res.Config.Scenes[2].MotionGraphic == "" {
		t.Fatalf("graphics: %+v", res.Config.Scenes)
	}

	e, r = load("graphics-parts.json")
	res, err = MapMotion(e, r, opts)
	if err != nil || res.Imported != 5 || res.Silent != 5 || res.Rejected != 0 {
		t.Fatalf("graphics made from parts: %v %+v", err, res.Reports)
	}
	if err := res.Validate(); err != nil {
		t.Fatal(err)
	}
	if res.Config.Scenes[0].MotionPart != "roll-counter" || res.Config.Scenes[4].MotionPart != "auto" || res.Config.Defaults.SubjectPrompt == "" {
		t.Fatalf("graphics made from parts: %+v", res.Config.Scenes)
	}

	e, r = load("songs.json")
	res, err = MapMusic(e, r, opts)
	if err != nil || res.Imported != 2 || res.Instrumental != 1 || res.Rejected != 0 {
		t.Fatalf("songs: %v %+v", err, res.Reports)
	}
}

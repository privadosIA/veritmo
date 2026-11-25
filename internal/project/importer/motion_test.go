package importer

import (
	"strings"
	"testing"

	"veritmo/internal/music/musicmotion"
)

func mapGraphics(t *testing.T, entries ...Entry) *Result {
	t.Helper()
	res, err := MapMotion(entries, nil, Options{Project: "graphics", SilenceSeconds: 2, Parts: musicmotion.Parts{}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestMapMotion_DescriptionAndDuration(t *testing.T) {
	res := mapGraphics(t, songEntry("grafico", "A bar chart growing\nfrom 2 to 9\nDuration: 4"))
	if res.Imported != 1 || res.Rejected != 0 {
		t.Fatalf("%+v", res.Reports)
	}
	s := res.Config.Scenes[0]
	if s.MotionGraphic != "A bar chart growing from 2 to 9" || s.Prompt != "" {
		t.Errorf("scene = %+v", s)
	}
	if s.SilenceSeconds == nil || *s.SilenceSeconds != 4 {
		t.Errorf("silence = %v", s.SilenceSeconds)
	}
	if err := res.Validate(); err != nil {
		t.Fatalf("a graphic needs neither Chrome nor a model to import: %v", err)
	}
	yaml := string(res.EmitYAML())
	if !strings.Contains(yaml, "motion_graphic: A bar chart growing from 2 to 9") || strings.Contains(yaml, "prompt:") {
		t.Errorf("yaml:\n%s", yaml)
	}
	if !strings.Contains(res.Report(), "imported 1 graphics, silent 1, rejected 0") {
		t.Errorf("report: %s", res.Report())
	}
}

func TestMapMotion_NarratedGraphicDiscardsDuration(t *testing.T) {
	res := mapGraphics(t, songEntry("g", "Title card\nDialogue: \"Hello\"\nDuration: 5"))
	s := res.Config.Scenes[0]
	if s.TTSText != "Hello" || s.SilenceSeconds != nil {
		t.Fatalf("%+v", s)
	}
	if !strings.Contains(strings.Join(res.Reports[0].Discarded, "|"), "dialogue") {
		t.Errorf("discarded: %v", res.Reports[0].Discarded)
	}
}

func TestMapMotion_GraphicLineWinsOverTheText(t *testing.T) {
	res := mapGraphics(t, songEntry("g", "ignored text\nGraphic: A rotating logo"))
	if got := res.Config.Scenes[0].MotionGraphic; got != "A rotating logo" {
		t.Errorf("graphic = %q", got)
	}
	if len(res.Reports[0].Discarded) == 0 {
		t.Error("the text before the markers must be reported as discarded")
	}
}

func TestMapMotion_OverlayLines(t *testing.T) {
	res := mapGraphics(t, songEntry("g", "Waves\nDuration: 6\nOverlay: Ana Ruiz, director\nOverlay position: top\nOverlay start: 1\nOverlay end: 5"))
	o := res.Config.Scenes[0].Overlay
	if o == nil || o.Text != "Ana Ruiz, director" || o.Position != "top" || *o.Start != 1 || *o.End != 5 {
		t.Fatalf("overlay = %+v", o)
	}
	if err := res.Validate(); err != nil {
		t.Fatal(err)
	}
	if y := string(res.EmitYAML()); !strings.Contains(y, "overlay:\n      text: Ana Ruiz, director\n      position: top\n      start: 1\n      end: 5") {
		t.Errorf("yaml:\n%s", y)
	}
}

func TestMapMotion_OverlayWithoutTextIsDiscarded(t *testing.T) {
	res := mapGraphics(t, songEntry("g", "Waves\nOverlay position: top"))
	if res.Config.Scenes[0].Overlay != nil {
		t.Fatal("no overlay without text")
	}
	if !strings.Contains(strings.Join(res.Reports[0].Discarded, "|"), "Overlay position") {
		t.Errorf("discarded: %v", res.Reports[0].Discarded)
	}
}

func TestMapMotion_RejectsOneEntryAtATime(t *testing.T) {
	res := mapGraphics(t,
		songEntry("empty", "Duration: 3"),
		songEntry("badnum", "Waves\nDuration: soon"),
		songEntry("badpos", "Waves\nOverlay: x\nOverlay position: middle"),
		songEntry("ok", "Waves"),
	)
	if res.Imported != 1 || res.Rejected != 3 {
		t.Fatalf("%+v", res.Reports)
	}
}

func TestMapMotion_DuplicateIdsAbort(t *testing.T) {
	if _, err := MapMotion([]Entry{songEntry("a", "x"), songEntry("a", "y")}, nil, Options{Project: "p", SilenceSeconds: 2, Parts: musicmotion.Parts{}}); err == nil {
		t.Fatal("duplicate ids must abort")
	}
}

func TestMap_SceneDatasetMarksAGraphicAndTakesAnOverlay(t *testing.T) {
	res, err := Map([]Entry{
		songEntry("intro", "Graphic: Opening title over blue waves\nSilence: 3"),
		songEntry("plain", "A quiet street\nOverlay: Madrid, 1990\nOverlay position: bottom"),
	}, nil, Options{Project: "mixed", SilenceSeconds: 2})
	if err != nil {
		t.Fatal(err)
	}
	if res.Rejected != 0 || res.Imported != 2 {
		t.Fatalf("%+v", res.Reports)
	}
	a, b := res.Config.Scenes[0], res.Config.Scenes[1]
	if a.MotionGraphic != "Opening title over blue waves" || a.Prompt != "" {
		t.Errorf("a = %+v", a)
	}
	if b.Prompt != "A quiet street" || b.Overlay == nil || b.Overlay.Text != "Madrid, 1990" {
		t.Errorf("b = %+v", b)
	}
	if err := res.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMap_DurationIsOnlyForGraphics(t *testing.T) {
	res, err := Map([]Entry{songEntry("s", "A street\nDuration: 4")}, nil, Options{Project: "p", SilenceSeconds: 2, Parts: musicmotion.Parts{}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Config.Scenes[0].Prompt, "Duration: 4") {
		t.Errorf("in a scene dataset it stays part of the text: %q", res.Config.Scenes[0].Prompt)
	}
}

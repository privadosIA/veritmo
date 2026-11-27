package musicmotion

import (
	"context"
	"math"
	"os/exec"
	"path/filepath"
	"testing"
)

// A clip of one flat colour (white, black or mid grey), a few seconds long, in the folder dir.
func flatClip(t *testing.T, dir, name, colour string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if out, err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "color=c="+colour+":s=320x180:r=10:d=4", "-pix_fmt", "yuv420p", path).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	return path
}

func TestClipBandLuminanceReadsTheBrightnessBehindTheVerse(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	dir := t.TempDir()
	white := flatClip(t, dir, "white.mp4", "white")
	black := flatClip(t, dir, "black.mp4", "black")
	w, err := ClipBandLuminance(context.Background(), white, 0, nil, VersePositionBottom)
	if err != nil || w < 230 {
		t.Fatalf("white: %v %v", w, err)
	}
	b, err := ClipBandLuminance(context.Background(), black, 0.5, nil, VersePositionTop)
	if err != nil || b > 25 {
		t.Fatalf("black: %v %v", b, err)
	}
	end := 2.0
	if _, err := ClipBandLuminance(context.Background(), white, 1, &end, VersePositionCenter); err != nil {
		t.Fatal(err)
	}
	if _, err := ClipBandLuminance(context.Background(), filepath.Join(dir, "nope.mp4"), 0, nil, VersePositionBottom); err == nil {
		t.Error("a clip that is not there is measured")
	}
}

// A design whose accent is orange on a cream background (the one of the real run), and a script with a verse over one item.
func scrimDesign() Design {
	return Design{Dark: "#1d1b19", Light: "#f5f0e6", Accent: "#b8411f", Display: "serif", Body: "serif", DefaultMode: ModeLight}
}

func scrimScript(item VideoItem) Script {
	return Script{Version: 1,
		Pieces: []Piece{{ID: "p1", Start: 0, End: 10, Section: "verse", Part: "blank", Lines: []ScriptLine{{Keyword: "road"}}}},
		Video:  []VideoItem{item}}
}

// a measure that answers one number whatever the clip
func grey(v float64) LuminanceFunc {
	return func(context.Context, string, float64, *float64, string) (float64, error) { return v, nil }
}

func localOf(it VideoItem, band string) (float64, bool) {
	for _, l := range it.Local {
		if l.Band == band {
			return l.Scrim, true
		}
	}
	return 0, false
}

func TestAnItemWithNoScrimGetsTheLeastLocalScrimThatLetsTheAccentRead(t *testing.T) {
	s := scrimScript(VideoItem{ID: "v1", Clip: "a.mp4", Start: 0, End: 10})
	d := scrimDesign()

	// a clip the accent already reads on: nothing is laid over it and the item is as it was
	if got, notes := EffectiveScrims(context.Background(), s, d, "", grey(200)); got.Video[0].Scrim != nil || len(got.Video[0].Local) != 0 || len(notes) != 0 {
		t.Fatalf("a clip that needs no more: %+v %v", got.Video[0], notes)
	}
	// a clip as dark as the accent: the global scrim stays, and a local one over the bottom band (where the verse is) lets it read
	got, notes := EffectiveScrims(context.Background(), s, d, "", grey(110))
	local, ok := localOf(got.Video[0], "bottom")
	if got.Video[0].Scrim != nil || len(got.Video[0].Local) != 1 || !ok || local < scrimStep || local > MaxLocalScrim || len(notes) != 0 {
		t.Fatalf("a clip that needs more: %+v %v", got.Video[0], notes)
	}
	ground := func(l float64) string {
		g := (1-DefaultScrim)*110 + DefaultScrim*greyOf(d.Light)
		return groundHex(g, greyOf(d.Light), l)
	}
	if c := Contrast(d.Accent, ground(local)); c < MinVerseContrast {
		t.Errorf("at %v the contrast is %.2f", local, c)
	}
	if local-scrimStep > 0 {
		if c := Contrast(d.Accent, ground(local-scrimStep)); c >= MinVerseContrast {
			t.Errorf("it is not the least step: %v already gave %.2f", local-scrimStep, c)
		}
	}
	if s.Video[0].Scrim != nil || len(s.Video[0].Local) != 0 {
		t.Error("the script it was given was changed")
	}
}

func TestAnItemWithAScrimOfItsOwnKeepsItGetsNoStripAndAGetsANote(t *testing.T) {
	low := 0.1
	s := scrimScript(VideoItem{ID: "v1", Clip: "a.mp4", Start: 0, End: 10, Scrim: &low})
	got, notes := EffectiveScrims(context.Background(), s, scrimDesign(), "", grey(110))
	if *got.Video[0].Scrim != 0.1 || len(got.Video[0].Local) != 0 || len(notes) != 1 || notes[0].Item != "v1" || notes[0].Band != "bottom" || notes[0].Contrast <= 0 || notes[0].Contrast >= MinVerseContrast {
		t.Fatalf("%v %+v %+v", *got.Video[0].Scrim, got.Video[0].Local, notes)
	}
}

func TestAnItemThatStillReadsUnderTheContrastAtTheMaximumKeepsItAndGetsANote(t *testing.T) {
	// an accent that is nearly the colour of the scrim never reads on it
	d := scrimDesign()
	d.Accent = "#efe9dd"
	got, notes := EffectiveScrims(context.Background(), scrimScript(VideoItem{ID: "v1", Clip: "a.mp4", Start: 0, End: 10}), d, "", grey(240))
	local, ok := localOf(got.Video[0], "bottom")
	if !ok || math.Abs(local-MaxLocalScrim) > 1e-9 || len(notes) != 1 || notes[0].Item != "v1" || notes[0].Contrast >= MinVerseContrast {
		t.Fatalf("%+v %+v", got.Video[0].Local, notes)
	}
}

func TestEffectiveScrimsLeavesWhatItCannotMeasureOrNeedNotAlone(t *testing.T) {
	d := scrimDesign()
	item := VideoItem{ID: "v1", Clip: "a.mp4", Start: 0, End: 10}
	failing := func(context.Context, string, float64, *float64, string) (float64, error) { return 0, context.Canceled }
	if got, notes := EffectiveScrims(context.Background(), scrimScript(item), d, "", failing); len(got.Video[0].Local) != 0 || len(notes) != 0 {
		t.Errorf("a clip that cannot be measured: %+v %v", got.Video[0].Local, notes)
	}
	if got, _ := EffectiveScrims(context.Background(), scrimScript(item), d, "", nil); len(got.Video[0].Local) != 0 {
		t.Error("no measure: the script changed")
	}
	noVerse := scrimScript(item)
	noVerse.Pieces[0].Lines = nil
	if got, notes := EffectiveScrims(context.Background(), noVerse, d, "", grey(110)); len(got.Video[0].Local) != 0 || len(notes) != 0 {
		t.Errorf("an item with no verse over it: %+v %v", got.Video[0].Local, notes)
	}
	if got, notes := EffectiveScrims(context.Background(), Script{Version: 1, Pieces: noVerse.Pieces}, d, "", grey(110)); len(got.Video) != 0 || len(notes) != 0 {
		t.Error("a script with no track")
	}
}

func TestTheWorstPlaceTheVerseFallsOnDecidesTheScrim(t *testing.T) {
	s := scrimScript(VideoItem{ID: "v1", Clip: "a.mp4", Start: 0, End: 10})
	s.Pieces[0].End = 5
	s.Pieces = append(s.Pieces, Piece{ID: "p2", Start: 5, End: 10, Section: "verse", Part: "blank", Lines: []ScriptLine{{Keyword: "road"}},
		Design: &DesignOverride{VerseLayout: &VerseLayout{Position: VersePositionTop}}})
	// bright at the bottom (where p1's verse is) and dark at the top (p2's)
	byBand := func(_ context.Context, _ string, _ float64, _ *float64, band string) (float64, error) {
		if band == VersePositionTop {
			return 110, nil
		}
		return 250, nil
	}
	got, _ := EffectiveScrims(context.Background(), s, scrimDesign(), "", byBand)
	if _, ok := localOf(got.Video[0], "top"); !ok {
		t.Fatalf("the dark band was not looked at: %+v", got.Video[0].Local)
	}
	if _, ok := localOf(got.Video[0], "bottom"); ok || len(got.Video[0].Local) != 1 {
		t.Errorf("the bright band got a strip it does not need: %+v", got.Video[0].Local)
	}
}

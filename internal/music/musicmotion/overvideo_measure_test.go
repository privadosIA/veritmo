package musicmotion

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"testing"
	"time"
)

// TestMeasureTheBuiltinParts measures every built-in part over a transparent page and, with MEASURE_OVER_VIDEO=file, writes the table
// (it is how the list of parts marked over_video was decided).
func TestMeasureTheBuiltinParts(t *testing.T) {
	browser := browserOrSkip(t)
	if os.Getenv("MEASURE_OVER_VIDEO") == "" {
		t.Skip("set MEASURE_OVER_VIDEO to a file to measure every part")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	table := map[string]OverVideoMeasure{}
	for _, p := range (Catalog{Builtin: BuiltinParts()}).List() {
		m, err := MeasureOverVideo(ctx, browser, p, nil)
		if err != nil {
			t.Errorf("%s: %v", p.Name, err)
			continue
		}
		table[p.Name] = m
		t.Logf("%-20s %5.1f %%  at %.2f s  ok=%v", p.Name, m.Coverage*100, m.At, m.OK())
	}
	raw, _ := json.MarshalIndent(table, "", "  ")
	_ = os.WriteFile(os.Getenv("MEASURE_OVER_VIDEO"), raw, 0o644)
}

// A part marked over_video must leave the video visible when it is drawn on a transparent page: this repeats the measure the marks
// came from, so a part that is changed to fill the frame, or a mark that was put by hand, is caught.
func TestThePartsMarkedOverVideoLeaveTheVideoVisible(t *testing.T) {
	browser := browserOrSkip(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	marked := 0
	for _, p := range (Catalog{Builtin: BuiltinParts()}).List() {
		if !p.OverVideo {
			continue
		}
		marked++
		m, err := MeasureOverVideo(ctx, browser, p, nil)
		if err != nil {
			t.Errorf("%s: %v", p.Name, err)
			continue
		}
		if !m.OK() {
			t.Errorf("%s is marked over_video but paints %.0f %% of the frame opaque at %.2f s", p.Name, m.Coverage*100, m.At)
		}
	}
	if marked < 20 {
		t.Errorf("only %d built-in parts are marked over_video", marked)
	}
}

// The parts that fill the frame are not marked, and say so by their measure.
func TestThePartsThatFillTheFrameAreNotMarkedOverVideo(t *testing.T) {
	browser := browserOrSkip(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, name := range []string{"sunburst", "synth-sun"} {
		p, ok := (Catalog{Builtin: BuiltinParts()}).Get(name)
		if !ok {
			t.Fatalf("no part %s", name)
		}
		if p.OverVideo {
			t.Errorf("%s fills the frame and is marked over_video", name)
		}
		m, err := MeasureOverVideo(ctx, browser, p, nil)
		if err != nil {
			t.Fatal(err)
		}
		if m.OK() {
			t.Errorf("%s was measured at %.0f %% and passes: its mark would be allowed", name, m.Coverage*100)
		}
	}
}

func TestOpaqueShareCountsTheSolidPixels(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 10, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			switch {
			case x < 3: // 30 %: solid
				img.Set(x, y, color.NRGBA{255, 0, 0, 255})
			case x < 5: // 20 %: a soft glow, under half
				img.Set(x, y, color.NRGBA{255, 0, 0, 100})
			}
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	got, err := opaqueShare(b.Bytes())
	if err != nil || got < 0.299 || got > 0.301 {
		t.Fatalf("share %v, %v: want 0.3", got, err)
	}
}

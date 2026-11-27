package musicmotion

import (
	"bytes"
	"context"
	"flag"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/video/motion"
)

var updateGolden = flag.Bool("update-golden", false, "write the golden pages")

// TestPageWithoutBackgroundIsUnchanged pins the page of a piece with no background to the bytes it had before the background
// existed: a render made before must keep its cache.
func TestPageWithoutBackgroundIsUnchanged(t *testing.T) {
	html, err := BuildPage(testScene())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "page_nobackground.golden.html")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(html), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no golden page (run with -update-golden once): %v", err)
	}
	if html != string(want) {
		t.Fatal("the page of a piece with no background changed")
	}
}

// The verse of a layer has a halo of the design's background colour for every verse style, and a page that is not a layer has none.
func TestTheVerseOfALayerHasAHaloAndAPlainPageHasNone(t *testing.T) {
	for _, style := range []string{"", VerseStylePlain, VerseStyleHighlight, VerseStyleKaraoke, VerseStyleGlow} {
		sc := testScene()
		sc.Design.VerseStyle = style
		sc.Background = &VideoBackground{}
		html, err := BuildPage(sc)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(html, "#verse .w{text-shadow:0 0 .08em color-mix(in srgb,var(--bg) 90%,transparent)") {
			t.Errorf("style %q: a layer has no halo", style)
		}
		if style == VerseStyleGlow && !strings.Contains(html, "#verse.verse-glow .w.k{text-shadow:0 0 .1em var(--accent)") {
			t.Error("glow: the halo replaced the glow of the key word")
		}
		if !strings.Contains(html, "#verse.verse-karaoke .w.sung{color:var(--accent)}") {
			t.Errorf("style %q: the colour of the sung words is gone", style)
		}
		plain := testScene()
		plain.Design.VerseStyle = style
		page, err := BuildPage(plain)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(page, "var(--bg) 90%,transparent") {
			t.Errorf("style %q: a page that is not a layer has the halo", style)
		}
	}
}

func TestAPageOverAVideoPaintsNothingOverTheFrame(t *testing.T) {
	sc := testScene()
	sc.Background = &VideoBackground{Scrim: 0.4}
	html, err := BuildPage(sc)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"html,body{background:transparent}", `<div id="scrim"></div>`, "color-mix(in srgb,var(--bg) 40%,transparent)", `<div id="hitbg">`} {
		if !strings.Contains(html, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	// the scrim is the first thing in the body, so every part is drawn over it
	if i, j := strings.Index(html, `<div id="scrim">`), strings.Index(html, `<div id="stage">`); i < 0 || j < 0 || i > j {
		t.Error("the scrim is not before the stage")
	}
	// the hit's backdrop is drawn at most at 60 % of its opacity, so the video stays visible behind a hit
	if !strings.Contains(html, "hitbg.style.opacity = String(0.6 * Math.min(a, out))") {
		t.Error("the backdrop of a hit is no longer translucent")
	}
	plain, _ := BuildPage(testScene())
	if strings.Contains(plain, `id="scrim"`) {
		t.Error("a page with no background has a scrim")
	}
}

func TestTheScrimHasLimits(t *testing.T) {
	for _, v := range []float64{-0.1, 0.81, 2} {
		sc := testScene()
		sc.Background = &VideoBackground{Scrim: v}
		if _, err := BuildPage(sc); err == nil {
			t.Errorf("a scrim of %v was accepted", v)
		}
	}
	for _, v := range []float64{0, 0.3, MaxScrim} {
		sc := testScene()
		sc.Background = &VideoBackground{Scrim: v}
		if _, err := BuildPage(sc); err != nil {
			t.Errorf("a scrim of %v was refused: %v", v, err)
		}
	}
}

// pixelAlpha draws the page transparent at t and returns the alpha of the pixel at (x, y).
func pixelAlpha(t *testing.T, sc Scene, at float64, x, y int) uint32 {
	t.Helper()
	html, err := BuildPage(sc)
	if err != nil {
		t.Fatal(err)
	}
	s, err := motion.Open(context.Background(), browserOrSkip(t), motion.Page{HTML: html, Width: sc.Width, Height: sc.Height, Duration: sc.Duration, Transparent: true}, motion.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	raw, err := s.Frame(context.Background(), at)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, a := img.At(x, y).RGBA()
	return a >> 8
}

func TestAPageOverAVideoIsTransparentWhereItDrawsNothing(t *testing.T) {
	browserOrSkip(t)
	sc := testScene()
	sc.Lines, sc.Hit = nil, nil
	// a corner the part does not touch: the page without a background is opaque there, with one and no scrim it is transparent
	if a := pixelAlpha(t, sc, 1, 5, 5); a != 255 {
		t.Errorf("the page of its own has alpha %d at the corner, want 255", a)
	}
	sc.Background = &VideoBackground{}
	if a := pixelAlpha(t, sc, 1, 5, 5); a != 0 {
		t.Errorf("a page over a video with no scrim has alpha %d at the corner, want 0", a)
	}
	sc.Background = &VideoBackground{Scrim: 0.5}
	if a := pixelAlpha(t, sc, 1, 5, 5); a < 120 || a > 135 {
		t.Errorf("a scrim of 0.5 gives alpha %d at the corner, want about 128", a)
	}
}

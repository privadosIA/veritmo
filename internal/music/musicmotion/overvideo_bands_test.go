package musicmotion

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

// The thirds of a frame are counted by the rows they hold, and the share of the whole picture is the same one opaqueShare always gave.
func TestOpaqueBandsCountsEachThirdOfAPicture(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 10, 9))
	for y := 0; y < 9; y++ {
		for x := 0; x < 10; x++ {
			switch {
			case y < 3: // the top third, all opaque
				img.Set(x, y, color.NRGBA{255, 0, 0, 255})
			case y >= 3 && y < 6 && x < 5: // half of the centre third
				img.Set(x, y, color.NRGBA{255, 0, 0, 255})
			case y >= 6 && x == 0: // a text edge below half opacity does not count
				img.Set(x, y, color.NRGBA{255, 0, 0, 100})
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	bands, share, err := opaqueBands(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if bands.Top != 1 || bands.Center != 0.5 || bands.Bottom != 0 {
		t.Errorf("bands %+v", bands)
	}
	if want := 45.0 / 90.0; share != want {
		t.Errorf("the whole picture is %v, want %v", share, want)
	}
	if only, _ := opaqueShare(buf.Bytes()); only != share {
		t.Errorf("opaqueShare changed: %v", only)
	}
}

func TestBandsShareIsTheOneOfTheBandTheVerseIsIn(t *testing.T) {
	b := Bands{Top: 0.9, Center: 0.5, Bottom: 0.1}
	if b.Share(VersePositionTop) != 0.9 || b.Share(VersePositionCenter) != 0.5 || b.Share(VersePositionBottom) != 0.1 || b.Share("") != 0.1 {
		t.Fatal("the shares do not follow the position")
	}
}

func TestABandsClaimIsValidatedAndRoundTrips(t *testing.T) {
	m := strings.Replace(testManifest, `"version":1,`, `"version":1,"bands":{"top":0.4,"center":0,"bottom":0.05},`, 1)
	p, err := parseManifest([]byte(m), "probe")
	if err != nil || p.Bands == nil || p.Bands.Top != 0.4 || p.Bands.Bottom != 0.05 {
		t.Fatalf("%v %+v", err, p.Bands)
	}
	if _, err := parseManifest([]byte(strings.Replace(testManifest, `"version":1,`, `"version":1,"bands":{"top":1.2},`, 1)), "probe"); err == nil || !strings.Contains(err.Error(), "bands.top") {
		t.Errorf("a share over 1 is accepted: %v", err)
	}
	if p, _ := parseManifest([]byte(testManifest), "probe"); p.Bands != nil {
		t.Error("a manifest with no bands has bands")
	}
	raw, err := WithBands([]byte(testManifest), Bands{Top: 0.123456, Center: 0, Bottom: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	if q, err := parseManifest(raw, "probe"); err != nil || q.Bands == nil || q.Bands.Top != 0.12 || q.Name != "probe" || len(q.Levels) != 1 {
		t.Fatalf("%v %+v", err, q)
	}
}

// A part that paints only the top third, and moves on the beat so it passes every other check.
const topBarFragment = `<style>#b{position:absolute;left:0;top:0;width:100vw;height:30vh;background:var(--accent)}</style><div id="b"></div>
<script>window.__partSeek=function(t,b){document.getElementById('b').style.opacity=String(0.8+0.2*Math.pow(1-b.position,2));};</script>`

func TestTheBandsOfAPartAreMeasuredOnATransparentPage(t *testing.T) {
	browser := browserOrSkip(t)
	p, probs := ParsePart([]byte(testManifest), topBarFragment)
	if len(probs) != 0 {
		t.Fatal(probs)
	}
	m, err := MeasureOverVideo(context.Background(), browser, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Bands.Top < 0.9 || m.Bands.Center > 0.05 || m.Bands.Bottom > 0.05 {
		t.Errorf("a bar over the top 30 %% measured %+v", m.Bands)
	}
	if m.Coverage < 0.25 || m.Coverage > 0.35 {
		t.Errorf("the coverage is %v", m.Coverage)
	}
}

func TestAManifestWhoseBandsAreNotWhatWasMeasuredFailsTheCheck(t *testing.T) {
	browser := browserOrSkip(t)
	claim := func(bands string) Part {
		p, probs := ParsePart([]byte(strings.Replace(testManifest, `"version":1,`, `"version":1,"bands":`+bands+`,`, 1)), topBarFragment)
		if len(probs) != 0 {
			t.Fatal(probs)
		}
		return p
	}
	probs, err := CheckPart(context.Background(), browser, claim(`{"top":0,"center":0,"bottom":0}`), CheckOptions{})
	if err != nil || len(probs) != 1 || !strings.Contains(probs[0], "the top band differs") {
		t.Fatalf("%v %v", probs, err)
	}
	if probs, err := CheckPart(context.Background(), browser, claim(`{"top":0.95,"center":0,"bottom":0}`), CheckOptions{}); err != nil || len(probs) != 0 {
		t.Fatalf("what was measured is refused: %v %v", probs, err)
	}
}

func TestAnApprovedDraftCarriesTheBandsThatWereMeasured(t *testing.T) {
	e := draftEngine(t, &scriptedWriter{answers: []string{answerOf(testManifest, topBarFragment)}}, nil)
	d, _ := e.Store.Create("a bar", "", nil)
	e.Run(context.Background(), d.ID)
	part, probs, err := e.Approve(context.Background(), d.ID)
	if err != nil || len(probs) != 0 {
		t.Fatalf("%v %v", err, probs)
	}
	got, ok := e.Cat.Get("probe")
	if !ok || got.Bands == nil || got.Bands.Top < 0.9 || got.Bands.Bottom > 0.05 || part.Bands == nil {
		t.Fatalf("bands: part %+v, catalog %+v (found %v)", part.Bands, got.Bands, ok)
	}
}

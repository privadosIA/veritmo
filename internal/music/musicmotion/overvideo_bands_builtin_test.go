package musicmotion

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

var reBandsInManifest = regexp.MustCompile(`"bands":\{[^}]*\},`)

// TestTheBuiltinPartsCarryTheirBands measures every built-in part on a transparent page and checks that its manifest says what was
// measured (within the tolerance the check allows). With WRITE_BANDS=1 it writes the measures into the manifests instead, which is how
// they were put there and how they are put again when a part changes: the program decides them, not the one who wrote the part.
func TestTheBuiltinPartsCarryTheirBands(t *testing.T) {
	browser := browserOrSkip(t)
	write := os.Getenv("WRITE_BANDS") != ""
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	for _, p := range (Catalog{Builtin: BuiltinParts()}).List() {
		m, err := MeasureOverVideo(ctx, browser, p, nil)
		if err != nil {
			t.Errorf("%s: %v", p.Name, err)
			continue
		}
		if write {
			path := filepath.Join("parts", p.Name, "manifest.json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Errorf("%s: %v", p.Name, err)
				continue
			}
			text := reBandsInManifest.ReplaceAllString(string(raw), "")
			b := Bands{Top: roundBand(m.Bands.Top), Center: roundBand(m.Bands.Center), Bottom: roundBand(m.Bands.Bottom)}
			at := regexp.MustCompile(`("version":\s*\d+,)`)
			if !at.MatchString(text) {
				t.Errorf("%s: the manifest has no version to put the bands after", p.Name)
				continue
			}
			text = at.ReplaceAllString(text, fmt.Sprintf(`${1}"bands":{"top":%g,"center":%g,"bottom":%g},`, b.Top, b.Center, b.Bottom))
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
				t.Errorf("%s: %v", p.Name, err)
			}
			continue
		}
		if p.Bands == nil {
			t.Errorf("%s: the manifest has no bands (WRITE_BANDS=1 go test -run TestTheBuiltinPartsCarryTheirBands writes them)", p.Name)
			continue
		}
		if band := p.Bands.differsFrom(m.Bands); band != "" {
			t.Errorf("%s: %s", p.Name, BandsProblem(*p.Bands, m.Bands, band))
		}
	}
}

// The two anchors the verse notes are tuned against: flat bars at the top, and a part that draws nothing of its own.
func TestFlatSlabsOccupiesTheTopAndBlankNothing(t *testing.T) {
	cat := Catalog{Builtin: BuiltinParts()}
	slabs, ok := cat.Get("flat-slabs")
	if !ok || slabs.Bands == nil || slabs.Bands.Top < BandOccupied {
		t.Fatalf("flat-slabs: %+v", slabs.Bands)
	}
	blank, ok := cat.Get("blank")
	if !ok || blank.Bands == nil || blank.Bands.Top >= BandClear || blank.Bands.Center >= BandClear || blank.Bands.Bottom >= BandClear {
		t.Fatalf("blank: %+v", blank.Bands)
	}
}

package motionref

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"veritmo/internal/music/musicmotion"
)

// TestRealReferences_DistancesBetweenStyles prints how far apart the styles drawn from a real library of references are,
// to tune the thresholds of a learning. It runs only when REFERENCES_DIR (and optionally PRESETS_DIR) is set.
//
//	REFERENCES_DIR=~/.config/veritmo/references go test ./internal/music/motionref -run RealReferences -v
func TestRealReferences_DistancesBetweenStyles(t *testing.T) {
	dir := os.Getenv("REFERENCES_DIR")
	if dir == "" {
		t.Skip("REFERENCES_DIR is not set")
	}
	refs := NewStore(dir).List()
	var styles []musicmotion.Preset
	for _, r := range refs {
		if r.State != StateReady {
			continue
		}
		p, err := FromReference(context.Background(), nil, musicmotion.NewCatalog(nil), r)
		if err != nil {
			t.Logf("%s: %v", r.Title, err)
			continue
		}
		p.ID = r.ID
		styles = append(styles, p)
	}
	var all []float64
	nearest := map[string]float64{}
	for i, a := range styles {
		nearest[a.Name] = 2
		for j, b := range styles {
			if i == j {
				continue
			}
			d, _ := musicmotion.StyleDistance(a, b)
			if i < j {
				all = append(all, d)
			}
			if d < nearest[a.Name] {
				nearest[a.Name] = d
			}
		}
	}
	sort.Float64s(all)
	if len(all) == 0 {
		t.Skip("fewer than two references")
	}
	q := func(p float64) float64 { return all[int(p*float64(len(all)-1))] }
	fmt.Printf("styles: %d, pairs: %d\ndistance min %.3f  p10 %.3f  p25 %.3f  median %.3f  p75 %.3f  max %.3f\n", len(styles), len(all), all[0], q(.1), q(.25), q(.5), q(.75), all[len(all)-1])
	for _, th := range []float64{0.15, 0.25, 0.40} {
		n := 0
		for _, d := range all {
			if d < th {
				n++
			}
		}
		fmt.Printf("pairs closer than %.2f: %d of %d\n", th, n, len(all))
	}
	names := make([]string, 0, len(nearest))
	for n := range nearest {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Printf("  %-40s nearest %.3f\n", strings.TrimSpace(n), nearest[n])
	}
	if pd := os.Getenv("PRESETS_DIR"); pd != "" {
		fmt.Println("against the styles made by hand:")
		hand := NewPresetStore(pd).List()
		for _, s := range styles {
			c, ok := musicmotion.ClosestStyle(s, hand)
			if ok {
				fmt.Printf("  %-40s closest %-30s %.3f\n", strings.TrimSpace(s.Name), strings.TrimSpace(c.Name), c.Distance)
			}
		}
	}
}

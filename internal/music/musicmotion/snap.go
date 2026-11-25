package musicmotion

// snap.go: cuts and entrances land on the beat. A boundary between two pieces moves to the nearest beat when one is
// within half a beat of it, and keeps its own time otherwise; the word hit of a piece moves to the nearest half beat
// (a beat or the middle between two). See specs/music-motion ("Cuts and entrances land on the beat").

import (
	"math"
	"sort"

	"veritmo/internal/music/songmap"
)

// minWordHit is how much of a piece a word hit that was pushed back to its end keeps, in seconds.
const minWordHit = 0.05

// minPieceSeconds is the shortest piece the snapping leaves: a cut that would leave less is not moved.
const minPieceSeconds = 1.0

// nearest returns the value of sorted that is closest to t and how far it is.
func nearest(t float64, sorted []float64) (float64, float64) {
	if len(sorted) == 0 {
		return t, math.Inf(1)
	}
	i := sort.SearchFloat64s(sorted, t)
	best, d := sorted[min(i, len(sorted)-1)], math.Abs(sorted[min(i, len(sorted)-1)]-t)
	if i > 0 && math.Abs(sorted[i-1]-t) < d {
		best, d = sorted[i-1], math.Abs(sorted[i-1]-t)
	}
	return best, d
}

func period(m songmap.Map) float64 {
	if m.BPM > 0 {
		return 60 / m.BPM
	}
	if len(m.Beats) > 1 {
		return m.Beats[1] - m.Beats[0]
	}
	return 0
}

// SnapBoundary is the nearest beat to t when it is within half a beat, and t otherwise.
func SnapBoundary(t float64, m songmap.Map) float64 {
	p := period(m)
	if p <= 0 {
		return t
	}
	b, d := nearest(t, m.Beats)
	if d <= p/2+1e-9 {
		return b
	}
	return t
}

// SnapSubdivision is the nearest half beat (a beat or the middle between two) to t, when t is within a quarter
// of a beat of it (always, on a regular grid), and t otherwise.
func SnapSubdivision(t float64, m songmap.Map) float64 {
	p := period(m)
	if p <= 0 || len(m.Beats) == 0 {
		return t
	}
	var grid []float64
	for _, b := range m.Beats {
		grid = append(grid, b, b+p/2)
	}
	sort.Float64s(grid)
	g, d := nearest(t, grid)
	if d <= p/4+1e-9 {
		return g
	}
	return t
}

// SnapScript moves the boundaries between the pieces of a script to the beat and the word hits to the half beat. A
// boundary that would leave a piece shorter than a second is left where it is. It returns how many boundaries
// and hits moved. The script's first start and last end are the song's and never move.
func SnapScript(s *Script, m songmap.Map) (boundaries, hits int) {
	for i := 1; i < len(s.Pieces); i++ {
		prev, cur := &s.Pieces[i-1], &s.Pieces[i]
		t := SnapBoundary(cur.Start, m)
		if math.Abs(t-cur.Start) < 1e-9 {
			continue
		}
		next := cur.End
		if i+1 < len(s.Pieces) {
			next = s.Pieces[i+1].Start // the next boundary is snapped on its own turn; keep room until then
		}
		if t-prev.Start < minPieceSeconds || next-t < minPieceSeconds {
			continue
		}
		cur.Start, prev.End = t, t
		boundaries++
	}
	for i := range s.Pieces {
		p := &s.Pieces[i]
		if p.Hit == nil {
			continue
		}
		at := SnapSubdivision(p.Hit.At, m)
		// The piece itself may have moved to a beat: the hit stays inside it, whatever the snapping did.
		if at < p.Start {
			at = p.Start
		}
		if at >= p.End {
			at = math.Max(p.Start, p.End-minWordHit)
		}
		if math.Abs(at-p.Hit.At) < 1e-9 {
			continue
		}
		p.Hit.At = at
		hits++
	}
	return boundaries, hits
}

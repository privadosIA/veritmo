package musicmotion

import (
	"math"
	"sort"

	"veritmo/internal/music/songmap"
)

// SectionEnergy is how loud a section of the song is: the mean of the map's loudness over its span (0 to 1, the RMS of the audio)
// and its rank among the sections (1 is the loudest; ties keep the order of the song).
type SectionEnergy struct {
	Tag        string
	Start, End float64
	Loudness   float64
	Rank       int
}

// Energy classes of a section, from its rank: the top third of the ranks is loud, the bottom third quiet.
const (
	EnergyLoud   = "loud"
	EnergyNormal = "normal"
	EnergyQuiet  = "quiet"
)

// SectionEnergies reads the loudness of each section of the map; nil when the map has no loudness or no sections.
func SectionEnergies(m songmap.Map) []SectionEnergy {
	if len(m.Loudness) == 0 || m.LoudnessHop <= 0 || len(m.Sections) == 0 {
		return nil
	}
	out := make([]SectionEnergy, len(m.Sections))
	for i, s := range m.Sections {
		lo := int(math.Floor(s.Start / m.LoudnessHop))
		hi := int(math.Ceil(s.End / m.LoudnessHop))
		lo, hi = max(lo, 0), min(hi, len(m.Loudness))
		var sum float64
		for _, v := range m.Loudness[lo:max(lo, hi)] {
			sum += v
		}
		mean := 0.0
		if hi > lo {
			mean = math.Min(sum/float64(hi-lo), 1)
		}
		out[i] = SectionEnergy{Tag: s.Tag, Start: s.Start, End: s.End, Loudness: mean}
	}
	idx := make([]int, len(out))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return out[idx[a]].Loudness > out[idx[b]].Loudness })
	for rank, i := range idx {
		out[i].Rank = rank + 1
	}
	return out
}

// EnergyClass is loud for the top third of the ranks, quiet for the bottom third and normal in between (two sections: the louder is
// loud and the other quiet; one section is normal).
func EnergyClass(rank, n int) string {
	if n < 2 {
		return EnergyNormal
	}
	third := int(math.Ceil(float64(n) / 3))
	switch {
	case rank <= third:
		return EnergyLoud
	case rank > n-third:
		return EnergyQuiet
	}
	return EnergyNormal
}

// IsBreakdown says whether section i is the quietest of the song and comes right after a louder section: the one place the cut
// density asks for a single held picture.
func IsBreakdown(e []SectionEnergy, i int) bool {
	return i > 0 && i < len(e) && e[i].Rank == len(e) && e[i-1].Loudness > e[i].Loudness
}

// BarSeconds is how long a bar of four beats lasts at the map's tempo (2 s when the tempo is unknown).
func BarSeconds(m songmap.Map) float64 {
	if m.BPM >= 20 {
		return 4 * 60 / m.BPM
	}
	return 2
}

const (
	quietPieceSeconds     = 12.0
	breakdownPieceSeconds = 15.0
	minLoudPieceSeconds   = 3.0
)

// energyLongest is the longest a piece of section i may be for the fixed rule: quiet sections get pieces of 8 to 15 s (12), a loud
// section one piece per two bars (never under 3 s), a breakdown one held piece (up to 15 s) and the rest the usual length.
func energyLongest(m songmap.Map, e []SectionEnergy, i int, usual float64) float64 {
	if i >= len(e) {
		return usual
	}
	switch {
	case IsBreakdown(e, i):
		return breakdownPieceSeconds
	case EnergyClass(e[i].Rank, len(e)) == EnergyLoud:
		return math.Max(2*BarSeconds(m), minLoudPieceSeconds)
	case EnergyClass(e[i].Rank, len(e)) == EnergyQuiet:
		return quietPieceSeconds
	}
	return usual
}

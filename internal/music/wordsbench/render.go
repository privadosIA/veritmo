package wordsbench

import (
	"fmt"
	"strings"

	"veritmo/internal/music/songmap"
)

// Table renders a report as text: one table per song and the summary with the medians per way and the decision.
func Table(r Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Timing of the words against the attacks of the voice: %s, model %s", r.Date, r.Model)
	if r.Machine != "" {
		fmt.Fprintf(&b, ", %s", r.Machine)
	}
	b.WriteString("\n")
	for _, n := range r.Notes {
		fmt.Fprintf(&b, "NOTE: %s\n", n)
	}
	for _, s := range r.Songs {
		fmt.Fprintf(&b, "\n%s %s v%d (%.0f s", s.Project, s.TrackID, s.Variant+1, s.Seconds)
		if s.SeparationSeconds > 0 {
			fmt.Fprintf(&b, ", voice separated in %.0f s, heard in %.0f %% of the song, %d attacks, %.0f bpm", s.SeparationSeconds, 100*s.VoicedShare, len(s.Attacks), s.BPM)
		}
		b.WriteString(")")
		if s.SameAs != "" {
			fmt.Fprintf(&b, " [same lyrics as %s: not counted]", s.SameAs)
		}
		b.WriteString("\n")
		if s.Error != "" {
			fmt.Fprintf(&b, "  failed: %s\n", s.Error)
			continue
		}
		rows := make([]WayMedian, len(s.Ways))
		for i, w := range s.Ways {
			rows[i] = WayMedian{Way: w.Way, Timing: w.Timing, Seconds: w.Seconds, Metrics: w.Metrics}
		}
		writeWays(&b, rows)
		if f := s.Fallback; f != nil {
			if f.Triggered() {
				fmt.Fprintf(&b, "  fallback: voice matched %d/%d, mix %d/%d → %s\n", f.Voice, f.Written, f.Mix, f.Written, f.Kept)
			} else {
				fmt.Fprintf(&b, "  fallback: not needed (voice %d/%d)\n", f.Voice, f.Written)
			}
		}
	}
	if len(r.Songs) > 0 {
		fmt.Fprintf(&b, "\nMedian over %d distinct songs (%d measured)\n", r.Summary.Distinct, r.Summary.Songs)
		writeWays(&b, r.Summary.Medians)
		if r.Summary.GainPoints != nil {
			fmt.Fprintf(&b, "  moving the words onto the attacks: %+.1f points within 50 ms on the median song; the voice way took %.2f times the length of the song\n", *r.Summary.GainPoints, *r.Summary.VoiceTimes)
		}
	}
	if len(r.Songs) > 0 {
		fmt.Fprintf(&b, "fallback to the mix (the voice matched under %.0f %% of the written words): %d of %d distinct songs\n", 100*songmap.VoiceLossRatio, r.Summary.Fallbacks, r.Summary.Distinct)
	}
	fmt.Fprintf(&b, "\ndecision: %s. %s\n", r.Summary.Decision, r.Summary.Reason)
	return b.String()
}

func writeWays(b *strings.Builder, ways []WayMedian) {
	fmt.Fprintf(b, "  %-21s %-8s %6s %7s %7s %7s %7s %8s %7s %12s %8s\n", "way", "timing", "heard", "<=50ms", "<=100", "<=150", "|off|ms", "lag ms", "orphans", "beat/chance", "seconds")
	for _, w := range ways {
		m := w.Metrics
		abs, lag := "-", "-"
		if m.MedianAbsMs != nil {
			abs, lag = fmt.Sprintf("%.0f", *m.MedianAbsMs), fmt.Sprintf("%+.0f", *m.SignedMedianMs)
		}
		fmt.Fprintf(b, "  %-21s %-8s %6d %6.1f%% %6.1f%% %6.1f%% %7s %8s %7d %5.1f/%5.1f%% %8.1f\n", w.Way, w.Timing, m.Heard, m.Within50, m.Within100, m.Within150, abs, lag, m.OrphanAttacks, m.BeatHit, m.BeatChance, w.Seconds)
	}
}

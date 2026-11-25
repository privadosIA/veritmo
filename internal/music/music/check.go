package music

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	"veritmo/internal/platform/probe"
	"veritmo/internal/platform/runner"
)

// Thresholds of the automatic checks of a finished song.
const (
	// ClipPeakDB is the sample peak above which a song is reported as clipping
	// (or about to): the export limiter normally keeps it well below.
	ClipPeakDB = -0.3
	// SilenceFloorDB and SilenceMinSeconds define a long silence: audio under
	// the floor for at least that long.
	SilenceFloorDB    = -50.0
	SilenceMinSeconds = 4.0
	// QuietLUFS and LoudLUFS bound the integrated loudness of a normal song.
	QuietLUFS = -24.0
	LoudLUFS  = -8.0
	// DurationTolerance is how far the length may stray from the requested one
	// (a fraction of it) before it is reported.
	DurationTolerance = 0.15
)

// Issue codes of Check.
const (
	IssueClipping = "clipping"
	IssueSilence  = "silence"
	IssueQuiet    = "quiet"
	IssueLoud     = "loud"
	IssueDuration = "duration"
)

// Silence is a stretch of audio under SilenceFloorDB. End is the length of
// the file when the silence runs to its end.
type Silence struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// Analysis is what ffmpeg measures in a song.
type Analysis struct {
	PeakDB   *float64
	LUFS     *float64
	Silences []Silence
}

// Issue is one finding of the checks; Detail is a short English description
// with the measured value (the dashboard translates by Code).
type Issue struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// Analyze measures the peak, the long silences and the integrated loudness of
// the audio file at path. Anything ffmpeg cannot report is left out of the
// result, so a missing ffmpeg gives an empty Analysis and no error.
func Analyze(ctx context.Context, r runner.CommandRunner, path string, seconds float64) Analysis {
	var a Analysis
	out, err := r.Run(ctx, "ffmpeg", []string{
		"-v", "error", "-nostats", "-i", path, "-vn",
		"-af", fmt.Sprintf("silencedetect=n=%gdB:d=%g,astats=metadata=1:reset=0,"+
			"ametadata=mode=print:key=lavfi.astats.Overall.Peak_level:file=-,"+
			"ametadata=mode=print:key=lavfi.silence_start:file=-,"+
			"ametadata=mode=print:key=lavfi.silence_end:file=-", SilenceFloorDB, SilenceMinSeconds),
		"-f", "null", "-",
	})
	if err == nil {
		a.PeakDB, a.Silences = parseMeasure(string(out), seconds)
	}
	if v, err := probe.Loudness(ctx, r, path); err == nil {
		v = math.Round(v*10) / 10
		a.LUFS = &v
	}
	return a
}

// parseMeasure reads the metadata lines of the analysis pass: the peak only
// grows (astats runs without reset), so the largest is the whole file's.
func parseMeasure(out string, seconds float64) (*float64, []Silence) {
	var peak *float64
	var sil []Silence
	open := -1.0
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "lavfi.astats.Overall.Peak_level="):
			v, err := strconv.ParseFloat(strings.TrimPrefix(line, "lavfi.astats.Overall.Peak_level="), 64)
			if err == nil && !math.IsNaN(v) && (peak == nil || v > *peak) {
				p := v
				peak = &p
			}
		case strings.HasPrefix(line, "lavfi.silence_start="):
			if v, err := strconv.ParseFloat(strings.TrimPrefix(line, "lavfi.silence_start="), 64); err == nil {
				open = v
			}
		case strings.HasPrefix(line, "lavfi.silence_end="):
			if v, err := strconv.ParseFloat(strings.TrimPrefix(line, "lavfi.silence_end="), 64); err == nil && open >= 0 {
				sil = append(sil, Silence{Start: open, End: v})
				open = -1
			}
		}
	}
	if open >= 0 && seconds > open {
		sil = append(sil, Silence{Start: open, End: seconds})
	}
	if peak != nil && math.IsInf(*peak, -1) {
		peak = nil
	}
	if peak != nil {
		v := math.Round(*peak*10) / 10
		peak = &v
	}
	return peak, sil
}

// Check turns an analysis into findings. target is the requested length (0
// when unknown) and seconds the real one; instrumental or not makes no
// difference to any check.
func Check(a Analysis, target, seconds float64) []Issue {
	var out []Issue
	if a.PeakDB != nil && *a.PeakDB > ClipPeakDB {
		out = append(out, Issue{IssueClipping, fmt.Sprintf("peak %.1f dBFS", *a.PeakDB)})
	}
	for _, s := range a.Silences {
		out = append(out, Issue{IssueSilence, fmt.Sprintf("silence of %.0f s at %s", s.End-s.Start, clock(s.Start))})
	}
	if a.LUFS != nil {
		switch {
		case *a.LUFS < QuietLUFS:
			out = append(out, Issue{IssueQuiet, fmt.Sprintf("%.1f LUFS", *a.LUFS)})
		case *a.LUFS > LoudLUFS:
			out = append(out, Issue{IssueLoud, fmt.Sprintf("%.1f LUFS", *a.LUFS)})
		}
	}
	if target > 0 && seconds > 0 && math.Abs(seconds-target) > target*DurationTolerance {
		out = append(out, Issue{IssueDuration, fmt.Sprintf("%.0f s instead of %.0f s", seconds, target)})
	}
	return out
}

func clock(s float64) string {
	return fmt.Sprintf("%d:%02d", int(s)/60, int(s)%60)
}

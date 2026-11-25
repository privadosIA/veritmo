package describe

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/platform/probe"
	"veritmo/internal/platform/runner"
)

// Shot detection limits: a shot shorter than MinShotSeconds is merged into its
// neighbour, and a clip gets at most MaxShots shots.
const (
	MinShotSeconds = 1.5
	MaxShots       = 12
)

// cutThresholds are the sensitivities of ffmpeg's scene-change filter, tried in
// order: the next one only when the previous found more than MaxShots shots.
var cutThresholds = []float64{0.3, 0.4, 0.5}

// ShotRange is the part of a clip a shot covers, in seconds.
type ShotRange struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// Shot is one shot of a split clip with the prompt that describes it.
type Shot struct {
	Start  float64 `json:"start"`
	End    float64 `json:"end"`
	Prompt string  `json:"prompt"`
	// Motion is a short movement prompt for the shot; empty when the model
	// did not write one.
	Motion string `json:"motion,omitempty"`
}

const motionSystem = "You write motion prompts to animate a still image. Reply with ONE line in English, " +
	"at most 25 words, describing only subtle movement of the people and of the camera. No preamble, no quotes."

// describeSplit describes a clip as one scene per shot. The age check runs on
// the middle frame of every shot before any description is made, so one
// minor refuses the whole clip; the model is unloaded when it ends.
func (d *Describer) describeSplit(ctx context.Context, path, model string, models []Model) (Result, error) {
	ranges, frames, err := d.shots(ctx, path)
	if err != nil {
		return Result{}, err
	}
	if len(ranges) == 0 || len(ranges) != len(frames) {
		return Result{}, fmt.Errorf("%w: no picture could be read from the file", ErrInvalidInput)
	}
	// Up to three calls per shot: the age check, the description and the movement prompt.
	if err := d.checkCalls(3 * len(ranges)); err != nil {
		return Result{}, err
	}
	defer d.unload(model)

	for i, f := range frames {
		if err := d.checkAdults(ctx, model, f, models, i+1, len(frames)); err != nil {
			return Result{}, err
		}
	}
	shots := make([]Shot, len(ranges))
	for i, f := range frames {
		out, err := d.chat(ctx, model, describeSystem, "Describe this picture as a prompt.", f, false, 400)
		if err != nil {
			return Result{}, err
		}
		text := assist.Clean(out, false)
		if isNotADescription(text) {
			return Result{}, d.refusal(ctx, model, models)
		}
		shots[i] = Shot{Start: ranges[i].Start, End: ranges[i].End, Prompt: text}
	}
	// A movement prompt per shot from its own description; a failure leaves it empty.
	for i := range shots {
		if out, err := d.chat(ctx, model, motionSystem, "Scene: "+shots[i].Prompt, nil, false, 80); err == nil {
			shots[i].Motion = assist.Clean(out, true)
		}
	}
	return Result{Prompt: shots[0].Prompt, Model: model, Provider: d.providerID(), Kind: KindVideo, Frames: len(shots), Shots: shots}, nil
}

func (d *Describer) shots(ctx context.Context, path string) ([]ShotRange, [][]byte, error) {
	if d.Shots != nil {
		return d.Shots(ctx, path)
	}
	r := d.Runner
	if r == nil {
		r = runner.NewExecRunner()
	}
	return extractShots(ctx, r, path)
}

// parseCuts reads the times of the cuts from the output of ffmpeg's
// `metadata=print` filter, whose lines look like `frame:0 pts:25088 pts_time:2.04`.
func parseCuts(out string) []float64 {
	var cuts []float64
	for _, line := range strings.Split(out, "\n") {
		i := strings.Index(line, "pts_time:")
		if i < 0 {
			continue
		}
		field := strings.Fields(line[i+len("pts_time:"):])
		if len(field) == 0 {
			continue
		}
		if t, err := strconv.ParseFloat(field[0], 64); err == nil && t > 0 {
			cuts = append(cuts, t)
		}
	}
	sort.Float64s(cuts)
	return cuts
}

// buildShots turns cut times into shots over a clip of dur seconds. Cuts
// outside the clip are ignored and a shot shorter than MinShotSeconds is merged
// into its neighbour (the previous one, or the next for the first shot).
func buildShots(cuts []float64, dur float64) []ShotRange {
	bounds := []float64{0}
	for _, c := range cuts {
		if c > bounds[len(bounds)-1] && c < dur {
			bounds = append(bounds, c)
		}
	}
	bounds = append(bounds, dur)
	var shots []ShotRange
	for i := 0; i+1 < len(bounds); i++ {
		s := ShotRange{bounds[i], bounds[i+1]}
		if n := len(shots); n > 0 && s.End-s.Start < MinShotSeconds {
			shots[n-1].End = s.End
			continue
		}
		shots = append(shots, s)
	}
	if len(shots) > 1 && shots[0].End-shots[0].Start < MinShotSeconds {
		shots[1].Start = 0
		shots = shots[1:]
	}
	return shots
}

// capShots merges the shortest shot into its shorter neighbour until at most n remain.
func capShots(shots []ShotRange, n int) []ShotRange {
	for len(shots) > n && len(shots) > 1 {
		short := 0
		for i, s := range shots {
			if s.End-s.Start < shots[short].End-shots[short].Start {
				short = i
			}
		}
		into := short - 1
		switch {
		case short == 0:
			into = 1
		case short < len(shots)-1:
			prev, next := shots[short-1], shots[short+1]
			if next.End-next.Start < prev.End-prev.Start {
				into = short + 1
			}
		}
		lo, hi := into, short
		if hi < lo {
			lo, hi = hi, lo
		}
		shots[lo].End = shots[hi].End
		shots = append(shots[:hi], shots[hi+1:]...)
	}
	return shots
}

// extractShots finds the shots of a clip with ffmpeg's scene-change filter and
// grabs the frame in the middle of each. The frames live in a temp dir that is
// removed before return.
func extractShots(ctx context.Context, r runner.CommandRunner, path string) ([]ShotRange, [][]byte, error) {
	dur, err := probe.Duration(ctx, r, path)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: cannot read the length of %q: %w", ErrInvalidInput, filepath.Base(path), err)
	}
	secs := dur.Seconds()
	if secs > MaxVideoSeconds {
		return nil, nil, fmt.Errorf("%w: the clip lasts %d s and the limit is %d s", ErrInvalidInput, int(secs), MaxVideoSeconds)
	}
	var shots []ShotRange
	for _, thr := range cutThresholds {
		filter := fmt.Sprintf("select='gt(scene,%.2f)',metadata=print:file=-", thr)
		out, err := r.Run(ctx, "ffmpeg", []string{"-v", "error", "-i", path, "-vf", filter, "-an", "-f", "null", "-"})
		if err != nil {
			return nil, nil, fmt.Errorf("%w: ffmpeg could not read %q: %w", ErrInvalidInput, filepath.Base(path), err)
		}
		shots = buildShots(parseCuts(string(out)), secs)
		if len(shots) <= MaxShots {
			break
		}
	}
	shots = capShots(shots, MaxShots)

	dir, err := os.MkdirTemp("", "describe-*")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(dir)
	var frames [][]byte
	for i, s := range shots {
		b, err := grabFrame(ctx, r, path, (s.Start+s.End)/2, filepath.Join(dir, fmt.Sprintf("shot-%02d.png", i)))
		if err != nil {
			return nil, nil, err
		}
		frames = append(frames, b)
	}
	return shots, frames, nil
}

// grabFrame saves the frame at second seek (the first one when seek < 0), scaled
// to the size sent to the model, and returns its PNG.
func grabFrame(ctx context.Context, r runner.CommandRunner, path string, seek float64, out string) ([]byte, error) {
	scale := fmt.Sprintf("scale='min(%d,iw)':'min(%d,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2", imageSide, imageSide)
	args := []string{"-y", "-v", "error"}
	if seek >= 0 {
		args = append(args, "-ss", fmt.Sprintf("%.3f", seek))
	}
	args = append(args, "-i", path, "-frames:v", "1", "-vf", scale, out)
	if _, err := r.Run(ctx, "ffmpeg", args); err != nil {
		return nil, fmt.Errorf("%w: ffmpeg could not read %q: %w", ErrInvalidInput, filepath.Base(path), err)
	}
	return os.ReadFile(out)
}

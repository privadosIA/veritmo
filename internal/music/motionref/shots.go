package motionref

// shots.go: the shots of a reference. A shot is what lies between two cuts; each is measured without a model (how much it
// moves, its dominant colour, whether its cut falls on a beat) and, when a vision model is there, read (see readShots).
// See specs motion-references, "The analysis says what the video is like".

import (
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"math"
	"sort"
	"strconv"
	"strings"

	"veritmo/internal/music/audiomeasure"
	"veritmo/internal/music/beatgrid"
)

// The settings of the shots are constants: the same video gives the same shots.
const (
	// minShot is the length under which a shot is a flash and is merged into the one before.
	minShot = 0.4
	// maxShots bounds the shots kept; above it the threshold of the scene filter is raised through cutThresholds.
	maxShots = 600
	// shotFrames is how many pictures of a shot are decoded to measure its motion (at least two are needed).
	shotFrames = 4
	// shotW and shotH are the size those pictures are scaled to.
	shotW, shotH = 64, 36
	// StaticMotion is the motion (0 to 1) under which a shot counts as static.
	StaticMotion = 0.02
	// beatTolerance is how close (seconds) a cut must be to a beat to be on it.
	beatTolerance = 0.06
	// beatSeconds is how much of the sound the beats are followed over: the shots after it say nothing of the beat.
	beatSeconds = audioSeconds
	// changeRate is how many small grey pictures a second the change pass compares to find a change.
	changeRate = 2
	// fineRate is how many pictures a second are decoded to put a change at its frame: the change is somewhere between two
	// of the changeRate pictures, and the beat is measured to 60 ms, so the time of a change is refined to 1/fineRate s.
	// It is a multiple of changeRate. A video longer than maxFineSeconds is not refined (its changes stay at the middle of
	// their interval, a quarter of a second at most from the truth): at fineRate its pictures would be 70 kB a second.
	fineRate, maxFineSeconds = 30, 1800
	// changeFloor is the least difference (0 to 1) between two consecutive pictures that can be a change, however quiet the
	// video is: codec noise sits well under it. changeK is how many times the video's own noise (the median difference) a
	// change must reach: a text swap on a plain ground is far above it, a slow pan stays near it. Calibrated on four
	// references of the owner's library (docs/MUSIC_MODE_MOTION.md, "Calibration of the change pass"): at 2 the live-action
	// one grows from 97 to 122 shots (+26 %, over the 20 % the design allows), at 3 to 110 (+13 %) with the typographic ones
	// going from 2, 28 and 42 shots to 86, 65 and 109; at 4 it is +5 % and LGTM and Permission keep 24 s and 51 s holds that
	// are not holds (the swaps hide in the motion of the picture).
	changeFloor = 0.06
	changeK     = 3
	// changeKStep and changeKMax raise changeK when the video has more than maxShots boundaries, before the pass is dropped.
	changeKStep, changeKMax = 2, 10
	// sameEvent is how close (seconds) a change must be to a hard cut to be that same event seen twice: the cut makes the peak
	// of the change pass, which then puts the change within a quarter of a second of it.
	sameEvent = 0.25
	// phaseMinBoundaries is how many boundaries the tracked span needs for the beat phase to be chosen from them.
	phaseMinBoundaries = 8
)

// Kinds of boundary, which a shot carries for the boundary that starts it.
const (
	// KindCut is a hard cut found by the scene filter.
	KindCut = "cut"
	// KindChange is a change of the picture without a hard cut (a word swapped on the same ground).
	KindChange = "change"
)

// beatPhases are the shifts of the tracked grid, in beats, the phase is chosen among.
var beatPhases = []float64{0, 0.25, 0.5, 0.75}

// cutThresholds are the scene scores (ffmpeg, 0 to 1) above which there is a cut: the first one, raised in steps when the
// video has more than maxShots shots.
var cutThresholds = []float64{0.3, 0.4, 0.5}

// Shot is one shot of a video.
type Shot struct {
	Index int `json:"index"`
	// Start and End are seconds from the start of the video.
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	// Motion is how much the picture moves inside the shot, from 0 (still) to 1.
	Motion float64 `json:"motion"`
	// Kind is how the shot starts: "cut" (a hard cut) or "change" (the picture changed without a cut); empty for the first
	// shot and for shots measured before the kinds existed.
	Kind string `json:"kind,omitempty"`
	// Colour is the dominant colour of the shot (#rrggbb).
	Colour string `json:"colour,omitempty"`
	// BeatCut says whether the cut that starts the shot falls within beatTolerance of a beat; nil for the first shot and
	// when the sound has no clear tempo.
	BeatCut *bool `json:"beat_cut,omitempty"`
	// Read is what the vision model saw in it, when it was asked.
	Read *ShotReading `json:"read,omitempty"`
}

// Length is the length of the shot in seconds.
func (s Shot) Length() float64 { return s.End - s.Start }

// Static says whether the shot barely moves.
func (s Shot) Static() bool { return s.Motion < StaticMotion }

// ShotReading is the answer of the vision model about one shot: closed vocabularies, "unknown" when it could not say.
type ShotReading struct {
	// Subject is what is on screen: typography, figure, chart, ui, photo, abstract or empty.
	Subject string `json:"subject"`
	// Text is how much text there is: none, word, line or block; TextSize how big: small, medium or huge.
	Text     string `json:"text"`
	TextSize string `json:"text_size"`
	// Entrance is how the text or the picture came in: cut, fade, slide, scale, letter, word or unknown.
	Entrance string `json:"entrance"`
	// Camera is static, zoom, pan or shake.
	Camera string `json:"camera"`
	// Note says why the reading is unknown (an answer that was not the JSON asked for, a refusal).
	Note string `json:"note,omitempty"`
}

// TextLed says whether the shot is made of the text filling the frame.
func (r *ShotReading) TextLed() bool {
	return r != nil && (r.Subject == "typography" || (r.Text != "none" && r.Text != "" && r.Text != "unknown" && r.TextSize == "huge"))
}

// Unknown says whether the model could not read the shot.
func (r *ShotReading) Unknown() bool { return r == nil || r.Subject == "unknown" }

// cutTimes are the seconds at which the scene score of the video passes the threshold.
func (a *Analyser) cutTimes(ctx context.Context, video string, threshold float64) ([]float64, error) {
	out, err := a.run().Run(ctx, "ffmpeg", []string{"-v", "error", "-i", video, "-vf",
		fmt.Sprintf("select='gt(scene,%.2f)',metadata=print:file=-", threshold), "-an", "-f", "null", "-"})
	if err != nil {
		return nil, fmt.Errorf("cannot look for the cuts: %w", err)
	}
	return parseCutTimes(string(out)), nil
}

// parseCutTimes reads the pts_time of every frame the filter let through.
func parseCutTimes(out string) []float64 {
	var times []float64
	for _, line := range strings.Split(out, "\n") {
		if i := strings.Index(line, "pts_time:"); i >= 0 {
			if f := strings.Fields(line[i+len("pts_time:"):]); len(f) > 0 {
				if t, err := strconv.ParseFloat(f[0], 64); err == nil && t > 0 {
					times = append(times, t)
				}
			}
		}
	}
	sort.Float64s(times)
	return times
}

// boundary is a moment the picture changes: a hard cut or a content change.
type boundary struct {
	At   float64
	Kind string
}

// changeSignal is what the change pass measures of a video: the difference (0 to 1) between each small grey picture and
// the next one half a second later (Coarse, element j between the pictures at j/changeRate and (j+1)/changeRate seconds)
// and, when the video was decoded finely, the difference between each picture and the next at fineRate (Fine, element i
// between i/fineRate and (i+1)/fineRate).
type changeSignal struct {
	Coarse, Fine []float64
}

// changeSignal decodes the video as small grey pictures: at fineRate a second when it is not longer than maxFineSeconds
// (the coarse samples are every (fineRate/changeRate)th of those), at changeRate otherwise.
func (a *Analyser) changeSignal(ctx context.Context, video string, duration float64) (changeSignal, error) {
	rate := fineRate
	if duration > maxFineSeconds {
		rate = changeRate
	}
	raw, err := a.run().Run(ctx, "ffmpeg", []string{"-v", "error", "-i", video, "-an", "-vf",
		fmt.Sprintf("fps=%d,scale=%d:%d:flags=area,format=gray", rate, shotW, shotH), "-f", "rawvideo", "-"})
	if err != nil {
		return changeSignal{}, fmt.Errorf("cannot look for the changes of the picture: %w", err)
	}
	size := shotW * shotH
	n := len(raw) / size
	frame := func(i int) []byte { return raw[i*size : (i+1)*size] }
	step := rate / changeRate // pictures between two coarse samples
	var sig changeSignal
	for i := 0; i+step < n; i += step {
		sig.Coarse = append(sig.Coarse, grayDifference(frame(i), frame(i+step)))
	}
	if rate == fineRate {
		for i := 0; i+1 < n; i++ {
			sig.Fine = append(sig.Fine, grayDifference(frame(i), frame(i+1)))
		}
	}
	return sig, nil
}

// grayDifference is the mean absolute difference between two grey pictures, from 0 to 1.
func grayDifference(a, b []byte) float64 {
	n := len(a)
	if n == 0 || len(b) < n {
		return 0
	}
	sum := 0
	for i := 0; i < n; i++ {
		d := int(a[i]) - int(b[i])
		if d < 0 {
			d = -d
		}
		sum += d
	}
	return float64(sum) / float64(n) / 255
}

// changesFromDiffs are the indexes of the coarse samples where the picture changed: the difference reaches max(changeFloor,
// k times the video's own noise, the median difference) and is a peak (above the one before, not under the one after).
// k of 0 or less is no change pass.
func changesFromDiffs(diffs []float64, k float64) []int {
	if k <= 0 || len(diffs) == 0 {
		return nil
	}
	threshold := math.Max(changeFloor, k*median(diffs))
	var at []int
	for j, d := range diffs {
		if d < threshold {
			continue
		}
		if j > 0 && d <= diffs[j-1] {
			continue
		}
		if j+1 < len(diffs) && d < diffs[j+1] {
			continue
		}
		at = append(at, j)
	}
	return at
}

// times are the seconds at which the picture changed, at threshold k. The change is somewhere between two coarse pictures:
// with the fine differences it is put where they jump most (the first of the ties), at the first picture that shows the new
// content, which is where a hard cut is put too; without them, at the middle of the interval.
func (s changeSignal) times(k float64) []float64 {
	var times []float64
	perCoarse := fineRate / changeRate
	for _, j := range changesFromDiffs(s.Coarse, k) {
		t := (float64(j) + 0.5) / changeRate
		if lo := j * perCoarse; len(s.Fine) > lo {
			hi := lo + perCoarse
			if hi > len(s.Fine) {
				hi = len(s.Fine)
			}
			best := lo
			for i := lo; i < hi; i++ {
				if s.Fine[i] > s.Fine[best] {
					best = i
				}
			}
			if s.Fine[best] > 0 {
				t = float64(best+1) / fineRate
			}
		}
		times = append(times, t)
	}
	return times
}

// changeTimes are the seconds at which the picture of the video changed, at the calibrated threshold.
func (a *Analyser) changeTimes(ctx context.Context, video string, duration float64) ([]float64, error) {
	sig, err := a.changeSignal(ctx, video, duration)
	if err != nil {
		return nil, err
	}
	return sig.times(changeK), nil
}

// boundaries joins the hard cuts and the changes into one sorted list; a change within sameEvent of a cut is the cut.
func boundaries(cuts, changes []float64) []boundary {
	out := make([]boundary, 0, len(cuts)+len(changes))
	for _, c := range cuts {
		out = append(out, boundary{c, KindCut})
	}
	for _, ch := range changes {
		same := false
		for _, c := range cuts {
			if math.Abs(c-ch) <= sameEvent {
				same = true
				break
			}
		}
		if !same {
			out = append(out, boundary{ch, KindChange})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out
}

// shotsFromBoundaries makes the shots of a video of the given length from its boundaries: a shot shorter than minShot is
// merged into the one before it (its boundary is dropped, and the shot keeps the kind of the earlier boundary), and a last
// shot shorter than that into the one before.
func shotsFromBoundaries(bounds []boundary, duration float64) []Shot {
	starts := []boundary{{0, ""}}
	for _, b := range bounds {
		if b.At >= duration {
			continue
		}
		starts = append(starts, b)
	}
	// Merge the short shots: dropping the start of a shot makes it part of the one before.
	for {
		at := -1
		for i := 1; i < len(starts); i++ {
			end := duration
			if i+1 < len(starts) {
				end = starts[i+1].At
			}
			if end-starts[i].At < minShot {
				at = i
				break
			}
		}
		if at < 0 {
			break
		}
		starts = append(starts[:at], starts[at+1:]...)
	}
	shots := make([]Shot, len(starts))
	for i, s := range starts {
		end := duration
		if i+1 < len(starts) {
			end = starts[i+1].At
		}
		shots[i] = Shot{Index: i, Start: round3(s.At), End: round3(end), Kind: s.Kind}
	}
	return shots
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// shotPass is one try at the shots: the scene threshold and the change threshold (0: no change pass).
type shotPass struct{ scene, k float64 }

// shotPasses are the tries in order when a video has more than maxShots shots: the change threshold raised first, then
// the change pass dropped, then the scene threshold raised in steps.
func shotPasses() []shotPass {
	var passes []shotPass
	for k := float64(changeK); k <= changeKMax; k += changeKStep {
		passes = append(passes, shotPass{cutThresholds[0], k})
	}
	for _, th := range cutThresholds {
		passes = append(passes, shotPass{th, 0})
	}
	return passes
}

// shotsOf finds the shots of a video: the hard cuts of the scene filter and the content changes, raised while there are
// more than maxShots (see shotPasses).
func (a *Analyser) shotsOf(ctx context.Context, video string, duration float64) ([]Shot, error) {
	sig, err := a.changeSignal(ctx, video, duration)
	if err != nil {
		return nil, err
	}
	scene := map[float64][]float64{}
	var shots []Shot
	for _, p := range shotPasses() {
		cuts, ok := scene[p.scene]
		if !ok {
			if cuts, err = a.cutTimes(ctx, video, p.scene); err != nil {
				return nil, err
			}
			scene[p.scene] = cuts
		}
		shots = shotsFromBoundaries(boundaries(cuts, sig.times(p.k)), duration)
		if len(shots) <= maxShots {
			break
		}
	}
	if len(shots) > maxShots {
		shots = shots[:maxShots] // a video that still has more at the last threshold: the rest is one shot's worth of noise
		shots[maxShots-1].End = round3(duration)
	}
	return shots, nil
}

// measureShot decodes some pictures of a shot and fills its motion and its dominant colour. The pictures are taken from
// the inside of the shot (the first and last tenth are left out, where the cut itself is) and are not kept.
func (a *Analyser) measureShot(ctx context.Context, video string, s *Shot) error {
	length := s.Length()
	inner := length * 0.8
	if inner <= 0 {
		return nil
	}
	from := s.Start + length*0.1
	rate := float64(shotFrames) / inner
	raw, err := a.run().Run(ctx, "ffmpeg", []string{"-v", "error", "-ss", strconv.FormatFloat(from, 'f', 3, 64), "-t", strconv.FormatFloat(inner, 'f', 3, 64), "-i", video,
		"-an", "-vf", fmt.Sprintf("fps=%.6f,scale=%d:%d:flags=area,format=rgb24", rate, shotW, shotH), "-frames:v", strconv.Itoa(shotFrames), "-f", "rawvideo", "-"})
	if err != nil {
		return fmt.Errorf("cannot read the pictures of a shot: %w", err)
	}
	size := shotW * shotH * 3
	n := len(raw) / size
	if n == 0 {
		return nil
	}
	frames := make([][]byte, n)
	for i := range frames {
		frames[i] = raw[i*size : (i+1)*size]
	}
	if n >= 2 {
		sum, count := 0.0, 0
		for i := 1; i < n; i++ {
			sum += meanDifference(frames[i-1], frames[i])
			count++
		}
		s.Motion = round3(sum / float64(count))
	}
	img := image.NewRGBA(image.Rect(0, 0, shotW, shotH))
	mid := frames[n/2]
	for y := 0; y < shotH; y++ {
		for x := 0; x < shotW; x++ {
			o := (y*shotW + x) * 3
			img.SetRGBA(x, y, color.RGBA{mid[o], mid[o+1], mid[o+2], 255})
		}
	}
	if cols := dominant(pixelsOf(img), DominantColours); len(cols) > 0 {
		s.Colour = cols[0].Hex
	}
	return nil
}

// meanDifference is the mean absolute difference between the luma of two pictures, from 0 to 1.
func meanDifference(a, b []byte) float64 {
	luma := func(p []byte, i int) float64 {
		return 0.299*float64(p[i*3]) + 0.587*float64(p[i*3+1]) + 0.114*float64(p[i*3+2])
	}
	n := len(a) / 3
	if n == 0 || len(b) < len(a) {
		return 0
	}
	sum := 0.0
	for i := 0; i < n; i++ {
		sum += math.Abs(luma(a, i) - luma(b, i))
	}
	return sum / float64(n) / 255
}

// markBeatCuts says, for the shots that start within the first beatSeconds of the sound, whether their boundary falls on a
// beat. The grid is the tracked one shifted by the phase (0, a quarter, a half or three quarters of a beat) that puts most
// boundaries on the beat, chosen only when at least phaseMinBoundaries boundaries fall in the tracked span; the phase used is
// returned. It does nothing when there is no sound or no clear tempo.
func (a *Analyser) markBeatCuts(ctx context.Context, video string, an *Analysis, shots []Shot) (float64, error) {
	if an.Tempo == nil || len(shots) < 2 {
		return 0, nil
	}
	raw, err := a.run().Run(ctx, "ffmpeg", []string{"-v", "error", "-i", video, "-vn", "-t", strconv.Itoa(beatSeconds), "-ac", "1",
		"-ar", strconv.Itoa(audiomeasure.Rate), "-f", "f32le", "-"})
	if err != nil {
		return 0, fmt.Errorf("cannot read the sound of the video: %w", err)
	}
	if len(raw) < 4*audiomeasure.Rate*5 {
		return 0, nil
	}
	samples := make([]float32, len(raw)/4)
	for i := range samples {
		samples[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	tracked, ok := beatgrid.Track(samples, audiomeasure.Rate, *an.Tempo)
	if !ok || len(tracked.Beats) == 0 {
		return 0, nil
	}
	covered := float64(len(samples)) / float64(audiomeasure.Rate)
	var times []float64 // the boundaries inside the tracked span
	for i := 1; i < len(shots); i++ {
		if shots[i].Start > covered {
			break
		}
		times = append(times, shots[i].Start)
	}
	phase, grid := 0.0, tracked.Beats
	if len(times) >= phaseMinBoundaries {
		best := -1
		for _, p := range beatPhases {
			g := shiftBeats(tracked.Beats, p)
			on := 0
			for _, t := range times {
				if nearBeat(g, t) {
					on++
				}
			}
			if on > best { // strictly more: a tie keeps the earlier phase, the tracked grid first
				best, phase, grid = on, p, g
			}
		}
	}
	for i, t := range times {
		on := nearBeat(grid, t)
		shots[i+1].BeatCut = &on
	}
	return phase, nil
}

// shiftBeats moves every beat by the fraction of its local beat gap (the gap to the next beat, the last one with the gap
// before it), so the shifted grid follows the tracker's drift.
func shiftBeats(beats []float64, phase float64) []float64 {
	out := make([]float64, len(beats))
	for i, b := range beats {
		gap := 0.0
		switch {
		case i+1 < len(beats):
			gap = beats[i+1] - b
		case i > 0:
			gap = b - beats[i-1]
		}
		out[i] = b + phase*gap
	}
	return out
}

// nearBeat says whether t is within beatTolerance of one of the (sorted) beats.
func nearBeat(beats []float64, t float64) bool {
	i := sort.SearchFloat64s(beats, t)
	for _, j := range []int{i - 1, i} {
		if j >= 0 && j < len(beats) && math.Abs(beats[j]-t) <= beatTolerance {
			return true
		}
	}
	return false
}

// Breakdown measures the shots of a video and writes them, the rhythm sheet and its text in the analysis, with the number
// of cuts and the mean shot length (one less than the shots, and the video over the shots, so the three agree). It does
// not touch the key frames, the colours, the tempo or the look, so it can be run again for a reference analysed before it
// existed. The video has to be the one the analysis is of.
func (a *Analyser) Breakdown(ctx context.Context, video string, an *Analysis) error {
	shots, err := a.shotsOf(ctx, video, an.Duration)
	if err != nil {
		return err
	}
	for i := range shots {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := a.measureShot(ctx, video, &shots[i]); err != nil {
			return err
		}
	}
	phase, err := a.markBeatCuts(ctx, video, an, shots)
	if err != nil {
		return err
	}
	an.Shots = shots
	an.Cuts = len(shots) - 1
	an.MeanShot = round2(an.Duration / float64(len(shots)))
	an.Rhythm = buildRhythm(shots, an.Duration)
	an.Rhythm.BeatPhase = phase
	an.RhythmText = RhythmText(an.Rhythm)
	// The new shots are unread: the reading is still to do, or there is no model to do it.
	if a.Reader == nil {
		an.BreakdownState, an.BreakdownNote = BreakdownSkipped, "no shots were read: no vision model is available"
	} else {
		an.BreakdownState, an.BreakdownNote = BreakdownPending, ""
	}
	return nil
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

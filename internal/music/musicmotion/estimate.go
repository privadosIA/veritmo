package musicmotion

// estimate.go: how long a render will take, before it starts. It starts from the measured cost of a frame of each
// part (the manifest's cost_ms) plus what starting the browser for a scene takes, and is corrected by what the
// renders of this machine actually took before. The answer says it is an estimate.

import (
	"math"

	"veritmo/internal/platform/fsutil"
)

// perSceneOverheadSeconds is the browser start and the encode of one scene, measured.
const perSceneOverheadSeconds = 1.2

// A recorded time is believed only when it is within this range of what was forecast.
const (
	minRatio = 0.1
	maxRatio = 20.0
)

// historyKeep is how many past renders correct the estimate.
const historyKeep = 10

// Estimate is the expected time of a render.
type Estimate struct {
	Seconds float64 `json:"seconds"`
	// Estimate is always true: the number is a forecast, not a promise.
	Estimate bool `json:"estimate"`
	Frames   int  `json:"frames"`
	// Corrected says the number was scaled by the renders this machine has done.
	Corrected bool    `json:"corrected"`
	Factor    float64 `json:"factor"`
}

// DefaultBackgroundCostMS is the milliseconds a frame of a piece over a video adds to the cost of its page at 1280x720: the overlay is
// encoded with its alpha and composited over the clip in the clip phase. Measured on 2026-11-26 (RTX 5060 Ti machine, 30 fps, scenes of the
// same pieces drawn over a clip and alone): +18 ms a frame for `flat-slabs` (a part that costs little itself), +2 ms for `nebula`; 10 is their
// mean. The estimate of a whole render of 26 pieces, 3 of them over a video, was 329 s and the render took 328 s.
const DefaultBackgroundCostMS = 10

// The cost of the video track, per second of video, on this machine's measure (see the design of add-video-track): an item is cut, retimed and
// encoded once (TrackItemRate seconds of work for each second of item) and the stack lays the whole song over the track and encodes it once
// (StackRate for each second of the video). They are estimates until task 7.2 of that change measures them on a real song.
const (
	TrackItemRate = 0.075
	StackRate     = 0.05
)

// DefaultBlenderCostMS is the milliseconds Cycles takes per frame of a 3D piece at 1280x720 with the default samples (measured on
// 2026-11-24: a 9 s piece, 270 frames, took about 100 s).
const DefaultBlenderCostMS = 370

// raw is the uncorrected time of a script at a frame rate and size.
func raw(s Script, parts map[string]Part, fps, width, height int) (seconds float64, frames int) {
	area := float64(width*height) / float64(1280*720)
	if area <= 0 {
		area = 1
	}
	s, _ = s.WithTrack() // a piece's `background` counts as the item it becomes
	for _, p := range s.Pieces {
		n := int(math.Round((p.End - p.Start) * float64(fps)))
		if p.Blender != nil {
			// Cycles draws a frame of a 3D piece in a time that does not depend on a part (see DefaultBlenderCostMS).
			seconds += float64(n)*DefaultBlenderCostMS*area/1000 + perSceneOverheadSeconds
			frames += n
			continue
		}
		cost := DefaultCostMS
		if part, ok := parts[p.Part]; ok && part.CostMS > 0 {
			cost = part.CostMS
		}
		// the parts of the layers that show in the piece are drawn on every frame of it too
		for _, it := range s.layerItemsFor(p) {
			ic := DefaultCostMS
			if part, ok := parts[it.Part]; ok && part.CostMS > 0 {
				ic = part.CostMS
			}
			cost += ic
		}
		if len(s.ItemsOver(p)) > 0 {
			// a layer: the page is drawn the same, and each frame is also encoded with its alpha
			cost += DefaultBackgroundCostMS
		}
		seconds += float64(n)*float64(cost)*area/1000 + perSceneOverheadSeconds
		frames += n
	}
	if len(s.Video) > 0 {
		// each item built once, and the stack over the whole song
		for _, it := range s.Video {
			seconds += it.Seconds()*TrackItemRate*area + perSceneOverheadSeconds
		}
		seconds += s.Pieces[len(s.Pieces)-1].End * StackRate * area
	}
	return seconds, frames
}

// EstimateRender forecasts the render of a script. historyDir is the folder that keeps the times of the
// renders already done on this machine (nil for none); the forecast is scaled by their median.
func EstimateRender(s Script, cat Catalog, fps, width, height int, history HistoryStore) Estimate {
	return EstimateRenderWith(s, cat.GetMany(s.PartNames()), fps, width, height, history)
}

// EstimateRenderWith is EstimateRender for a caller that has already read the parts of the script (Catalog.GetMany of Script.PartNames): the
// view of a piece reads them once and gives them to everything that needs them.
func EstimateRenderWith(s Script, parts map[string]Part, fps, width, height int, history HistoryStore) Estimate {
	sec, frames := raw(s, parts, fps, width, height)
	e := Estimate{Seconds: math.Round(sec*10) / 10, Estimate: true, Frames: frames, Factor: 1}
	if history != nil {
		if ratios := history.Ratios(); len(ratios) > 0 {
			e.Factor = math.Round(medianOrOne(ratios)*100) / 100
			e.Seconds = math.Round(sec*e.Factor*10) / 10
			e.Corrected = true
		}
	}
	return e
}

// RecordRender stores how long a render really took against what the uncorrected forecast said, so the next
// forecast on this machine is closer. Renders that did nothing (everything cached) say nothing about speed and are
// not recorded: pass framesRendered, the frames actually drawn.
func RecordRender(s Script, cat Catalog, fps, width, height int, framesRendered int, took float64, history HistoryStore) error {
	if history == nil || framesRendered <= 0 || took <= 0 {
		return nil
	}
	sec, frames := raw(s, cat.GetMany(s.PartNames()), fps, width, height)
	if frames <= 0 || sec <= 0 {
		return nil
	}
	// Only the share of the script that was drawn counts: the forecast is scaled to it.
	forecast := sec * float64(framesRendered) / float64(frames)
	ratio := took / forecast
	if ratio < minRatio || ratio > maxRatio {
		return nil // a time that far from the forecast is not this machine's speed (a render that was cut short, a stalled one)
	}
	return history.Add(ratio)
}

// medianOrOne is the median of the ratios, or 1 (no correction) when there are none.
func medianOrOne(v []float64) float64 {
	if len(v) == 0 {
		return 1
	}
	return fsutil.Median(v)
}

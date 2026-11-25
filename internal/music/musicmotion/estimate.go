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

// DefaultBlenderCostMS is the milliseconds Cycles takes per frame of a 3D piece at 1280x720 with the default samples (measured on
// 2026-11-24: a 9 s piece, 270 frames, took about 100 s).
const DefaultBlenderCostMS = 370

// raw is the uncorrected time of a script at a frame rate and size.
func raw(s Script, parts map[string]Part, fps, width, height int) (seconds float64, frames int) {
	area := float64(width*height) / float64(1280*720)
	if area <= 0 {
		area = 1
	}
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
		seconds += float64(n)*float64(cost)*area/1000 + perSceneOverheadSeconds
		frames += n
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

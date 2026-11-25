package workspace

// phaseduration.go answers, from the runs this machine has made, how long a phase takes for a model, a size and a
// card: the median and the 95th percentile of the phases that were executed (not reused from the cache) in runs that
// ended well, over the latest months. The estimate of a project is built from it where there is enough history, and
// from the measured table of the catalog where there is not (see specs/run-statistics and project-estimate).

import (
	"math"
	"net/http"
	"path"
	"sort"
	"strconv"
	"time"

	"veritmo/internal/project/config"
	"veritmo/internal/server/api"
)

const (
	// historyMinExecutions is how many executions of a phase it takes before the history is trusted over the table.
	historyMinExecutions = 3
	// historyPeriod is how far back the history counts: older runs may have used other versions of sd or other settings.
	historyPeriod = 90 * 24 * time.Hour
)

// phaseKey is what a phase was made with. Resolution, steps and frames are part of it because a render of the same
// model at another size or step count is another duration, not a variation of this one.
type phaseKey struct {
	Phase      string // render | animate
	Model      string
	Resolution string
	Steps      int
	Frames     int // animate only
	Units      int // render: the pictures it made (the variants of an images-mode scene)
}

// phaseKeyOf is the key a scene's phase has in a configuration.
func phaseKeyOf(cfg *config.Config, sc config.Scene, phase string) phaseKey {
	d := cfg.Defaults
	switch phase {
	case "render":
		file := path.Base(d.Model)
		if d.ModelSet != nil {
			file = path.Base(d.ModelSet.DiffusionModel)
		}
		eff := sc.EffectiveRender(d)
		return phaseKey{Phase: phase, Model: file, Resolution: eff.Resolution, Steps: eff.Steps}
	case "animate":
		a := d.Animate
		if a == nil {
			return phaseKey{Phase: phase}
		}
		r := a.Resolved()
		res, frames, steps := r.Resolution, r.Frames, r.Steps
		if sc.Animation != nil {
			if sc.Animation.Resolution != "" {
				res = sc.Animation.Resolution
			}
			if sc.Animation.Frames != 0 {
				frames = sc.Animation.Frames
			}
			if sc.Animation.Steps != 0 {
				steps = sc.Animation.Steps
			}
		}
		file := path.Base(r.DiffusionModel)
		switch {
		case r.IsAnimateDiff():
			file = path.Base(r.MotionModule)
		case r.IsMiniMaxH3() && r.LoRA != "":
			file += "+" + path.Base(r.LoRA) // the turbo LoRA changes the steps and the time: it is another configuration
		}
		return phaseKey{Phase: phase, Model: file, Resolution: res, Steps: steps, Frames: frames}
	}
	return phaseKey{Phase: phase}
}

// phaseStat is what the history says about one phase key on one card.
type phaseStat struct {
	Executions int
	Median     float64 // seconds per picture or clip
	P95        float64
}

// Enough says the history is enough to be used instead of the table.
func (p phaseStat) Enough() bool { return p.Executions >= historyMinExecutions }

// phaseDuration reads the duration of a phase for a card from the history. It counts only the executions of runs
// that succeeded, in the last historyPeriod, with the same model, resolution, steps and frames; each duration is per
// picture (a phase that made several variants is divided by them).
func (h *historyStore) phaseDuration(gpu string, k phaseKey) phaseStat {
	if gpu == "" || k.Model == "" || k.Resolution == "" {
		return phaseStat{}
	}
	var rows []CalibrationSample
	err := h.db.Where("server = ? AND gpu = ? AND kind = ? AND model = ? AND resolution = ? AND steps = ? AND frames = ? AND succeeded = ? AND at >= ?",
		h.server, gpu, k.Phase, k.Model, k.Resolution, k.Steps, k.Frames, true, time.Now().UTC().Add(-historyPeriod)).Find(&rows).Error
	if err != nil || len(rows) == 0 {
		return phaseStat{}
	}
	secs := make([]float64, 0, len(rows))
	for _, r := range rows {
		secs = append(secs, r.Actual/float64(max(r.Units, 1)))
	}
	sort.Float64s(secs)
	return phaseStat{Executions: len(secs), Median: round1(percentile(secs, 0.5)), P95: round1(percentile(secs, 0.95))}
}

// cpuDuration reads, from the history, how long the rest of a project takes on this machine: kindScene is the
// narration, probe, clip and export of one scene and kindJoin the final join. Same rules as phaseDuration: runs that
// succeeded in the last historyPeriod, and enough executions or nothing.
func (h *historyStore) cpuDuration(gpu, kind string) phaseStat {
	if gpu == "" {
		return phaseStat{}
	}
	var secs []float64
	if err := h.db.Model(&CalibrationSample{}).Where("server = ? AND gpu = ? AND kind = ? AND succeeded = ? AND at >= ?",
		h.server, gpu, kind, true, time.Now().UTC().Add(-historyPeriod)).Pluck("actual", &secs).Error; err != nil || len(secs) == 0 {
		return phaseStat{}
	}
	sort.Float64s(secs)
	return phaseStat{Executions: len(secs), Median: round1(percentile(secs, 0.5)), P95: round1(percentile(secs, 0.95))}
}

// percentile reads the p-th quantile (0 to 1) of sorted values by linear interpolation.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	pos := p * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	return sorted[lo] + (sorted[hi]-sorted[lo])*(pos-float64(lo))
}

// PhaseDurationView is the answer of GET /api/stats/phase-duration.
type PhaseDurationView struct {
	Phase         string  `json:"phase"`
	Model         string  `json:"model"`
	Resolution    string  `json:"resolution"`
	GPU           string  `json:"gpu"`
	Steps         int     `json:"steps,omitempty"`
	Frames        int     `json:"frames,omitempty"`
	Executions    int     `json:"executions"`
	MinExecutions int     `json:"min_executions"`
	Enough        bool    `json:"enough"`
	MedianSeconds float64 `json:"median_seconds,omitempty"`
	P95Seconds    float64 `json:"p95_seconds,omitempty"`
	PeriodDays    int     `json:"period_days"`
}

// GET /api/stats/phase-duration?phase=render|animate&model=&resolution=&gpu=[&steps=][&frames=]
func (h *Handler) handlePhaseDuration(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	phase := q.Get("phase")
	if phase != "render" && phase != "animate" {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "phase must be render or animate")
		return
	}
	for _, name := range []string{"model", "resolution", "gpu"} {
		if q.Get(name) == "" {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, name+" is required")
			return
		}
	}
	num := func(name string) (int, bool) {
		v := q.Get(name)
		if v == "" {
			return 0, true
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, name+" must be a whole number")
			return 0, false
		}
		return n, true
	}
	steps, ok := num("steps")
	if !ok {
		return
	}
	frames, ok := num("frames")
	if !ok {
		return
	}
	k := phaseKey{Phase: phase, Model: q.Get("model"), Resolution: q.Get("resolution"), Steps: steps, Frames: frames}
	st := h.rm.hist.phaseDuration(q.Get("gpu"), k)
	out := PhaseDurationView{Phase: phase, Model: k.Model, Resolution: k.Resolution, GPU: q.Get("gpu"), Steps: steps, Frames: frames,
		Executions: st.Executions, MinExecutions: historyMinExecutions, Enough: st.Enough(), PeriodDays: int(historyPeriod / (24 * time.Hour))}
	if st.Enough() {
		out.MedianSeconds, out.P95Seconds = st.Median, st.P95
	}
	api.WriteJSON(w, http.StatusOK, out)
}

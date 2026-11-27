package workspace

// stats.go aggregates the run history into the figures the Statistics page
// shows (see specs/run-statistics). Everything is computed on read from the
// stored runs and their events, so runs that predate any sampling count too.

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"veritmo/internal/platform/fsutil"
	"veritmo/internal/project/config"
	"veritmo/internal/server/api"
)

const (
	defaultStatsWindow = 30 * 24 * time.Hour
	slowestRuns        = 10
	durationSeriesCap  = 200
)

type statsPeriod struct {
	Since time.Time `json:"since"`
	Until time.Time `json:"until"`
}

type statsTotals struct {
	Runs        int            `json:"runs"`
	ByState     map[string]int `json:"by_state"`
	SuccessRate *float64       `json:"success_rate,omitempty"` // succeeded / finished runs
	TotalSecs   float64        `json:"total_seconds"`
	QueueWaitS  *float64       `json:"queue_wait_mean_seconds,omitempty"`
}

type statsDuration struct {
	Runs   int      `json:"runs"` // succeeded runs with a duration
	Mean   *float64 `json:"mean_seconds,omitempty"`
	Median *float64 `json:"median_seconds,omitempty"`
	P95    *float64 `json:"p95_seconds,omitempty"`
	Total  float64  `json:"total_seconds"`
}

type statsDay struct {
	Date      string  `json:"date"`
	Succeeded int     `json:"succeeded"`
	Failed    int     `json:"failed"`
	Other     int     `json:"other"`
	Seconds   float64 `json:"seconds"`
}

type statsPhase struct {
	Phase      string   `json:"phase"`
	Executions int      `json:"executions"`
	Reused     int      `json:"reused"`
	TotalSecs  float64  `json:"total_seconds"`
	MeanSecs   *float64 `json:"mean_seconds,omitempty"`
	P95Secs    *float64 `json:"p95_seconds,omitempty"`
}

type statsCache struct {
	Executed   int      `json:"executed"`
	Reused     int      `json:"reused"`
	ReuseShare *float64 `json:"reuse_share,omitempty"`
}

type statsGroup struct {
	Name        string   `json:"name"`
	Runs        int      `json:"runs"`
	Seconds     float64  `json:"seconds"`
	SuccessRate *float64 `json:"success_rate,omitempty"`
	// GPU rows only: VRAM peaks (percent of the GPU's total) over the runs
	// that have samples; absent when none does.
	PeakVRAMPct     *float64 `json:"peak_vram_pct,omitempty"`
	MeanPeakVRAMPct *float64 `json:"mean_peak_vram_pct,omitempty"`
	SampledRuns     int      `json:"sampled_runs,omitempty"`
}

type statsRunPoint struct {
	ID        string   `json:"id"`
	Project   string   `json:"project"`
	State     RunState `json:"state"`
	CreatedAt string   `json:"created_at"`
	Seconds   float64  `json:"seconds"`
	// Mode is the kind of the run's project (video, images, music or animation); empty when the project no longer exists.
	Mode string `json:"mode,omitempty"`
	// ResultAvailable says whether the run still has a result to link (see resultavail.go).
	ResultAvailable bool `json:"result_available"`
}

type statsResult struct {
	Period  statsPeriod `json:"period"`
	Project string      `json:"project,omitempty"`
	// Mode is the filter the statistics were computed with, when there was one.
	Mode      string        `json:"mode,omitempty"`
	Totals    statsTotals   `json:"totals"`
	Duration  statsDuration `json:"duration"`
	ByDay     []statsDay    `json:"by_day"`
	Phases    []statsPhase  `json:"phases"`
	Cache     statsCache    `json:"cache"`
	ByProject []statsGroup  `json:"by_project"`
	ByGPU     []statsGroup  `json:"by_gpu"`
	// ByMode groups the runs by the kind of their project: video, images, music or animation.
	ByMode []statsGroup `json:"by_mode"`
	// ByClient groups the runs by the client that asked for them: a dashboard of the generation API by its own
	// name, unknown, or embedded for the ordinary projects.
	ByClient     []statsGroup    `json:"by_client"`
	RunDurations []statsRunPoint `json:"run_durations"`
	Slowest      []statsRunPoint `json:"slowest"`
}

func ptr(f float64) *float64 { return &f }

func mean(v []float64) float64 {
	t := 0.0
	for _, x := range v {
		t += x
	}
	return t / float64(len(v))
}

// percentile95 is the nearest-rank 95th percentile of a sorted slice.
func percentile95(sorted []float64) float64 {
	i := int(math.Ceil(0.95*float64(len(sorted)))) - 1
	return sorted[max(0, i)]
}

type statEvent struct {
	Type       string `json:"type"`
	Phase      string `json:"phase"`
	DurationMs int64  `json:"duration_ms"`
}

// phaseOrder lists the usual phases in execution order; others follow
// alphabetically.
var phaseOrder = []string{"tts", "probe", "render", "animate", "clip", "track", "stack", "concat", "compose", "synth", "export"}

func phaseRank(p string) int {
	for i, x := range phaseOrder {
		if x == p {
			return i
		}
	}
	return len(phaseOrder)
}

// rate returns succeeded / (succeeded + failed + cancelled + interrupted), or
// nil with no finished run.
func rate(by map[string]int) *float64 {
	fin := 0
	for _, st := range terminalStates {
		fin += by[string(st)]
	}
	if fin == 0 {
		return nil
	}
	return ptr(float64(by[string(StateSucceeded)]) / float64(fin))
}

func runSeconds(r Run) (float64, bool) {
	if r.StartedAt == nil || r.FinishedAt == nil || r.FinishedAt.Before(*r.StartedAt) {
		return 0, false
	}
	return r.FinishedAt.Sub(*r.StartedAt).Seconds(), true
}

// gpuNames lists the GPUs a run option occupies; a run without GPUs is
// reported under "" (shown as "not pinned").
func gpuNames(gpu string) []string {
	var out []string
	for _, p := range strings.Split(gpu, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

// mobileProjectPrefix is the start of the name of the projects the Create page makes: those are the "mobile" ones,
// whichever device made them. Every other project is a "desktop" one.
const mobileProjectPrefix = "mobile-"

func (h *historyStore) stats(project string, modes, origins, servers, clients []string, since, until time.Time) (*statsResult, error) {
	q := h.db.Where("created_at >= ? AND created_at <= ?", since.UTC(), until.UTC())
	if project != "" {
		q = q.Where("project = ?", project)
	}
	if len(servers) > 0 {
		q = q.Where("server IN ?", servers)
	}
	var runs []Run
	if err := q.Order("created_at ASC, id ASC").Find(&runs).Error; err != nil {
		return nil, err
	}
	if h.visible != nil {
		runs = h.onlyVisible(runs)
	}
	modeOf := map[string]string{} // the kind of each project, read once
	kindOf := func(project string) string {
		m, ok := modeOf[project]
		if IsAnimationProject(project) {
			m, ok = string(ModeAnimation), true
		}
		if !ok && h.modeOf != nil {
			m = h.modeOf(project)
		}
		modeOf[project] = m
		return m
	}
	if len(origins) > 0 {
		kept := runs[:0:0]
		for _, r := range runs {
			origin := "desktop"
			if strings.HasPrefix(r.Project, mobileProjectPrefix) {
				origin = "mobile"
			}
			if slices.Contains(origins, origin) {
				kept = append(kept, r)
			}
		}
		runs = kept
	}
	if len(clients) > 0 {
		kept := runs[:0:0]
		for _, r := range runs {
			if slices.Contains(clients, clientOf(r)) {
				kept = append(kept, r)
			}
		}
		runs = kept
	}
	// Without a mode the animations are left out (they are a separate world); asking for the mode brings them in.
	kept := runs[:0:0]
	for _, r := range runs {
		if k := kindOf(r.Project); (len(modes) == 0 && k != string(ModeAnimation)) || slices.Contains(modes, k) {
			kept = append(kept, r)
		}
	}
	runs = kept
	res := &statsResult{
		Period:  statsPeriod{Since: since.UTC(), Until: until.UTC()},
		Project: project,
		Mode:    strings.Join(modes, ","),
		Totals:  statsTotals{ByState: map[string]int{}},
		ByDay:   []statsDay{}, Phases: []statsPhase{}, ByProject: []statsGroup{}, ByGPU: []statsGroup{}, ByMode: []statsGroup{}, ByClient: []statsGroup{},
		RunDurations: []statsRunPoint{}, Slowest: []statsRunPoint{},
	}
	res.Totals.Runs = len(runs)

	var waits, okDurations []float64
	days := map[string]*statsDay{}
	projects := map[string]*statsGroup{}
	projectStates := map[string]map[string]int{}
	gpus := map[string]*statsGroup{}
	gpuStates := map[string]map[string]int{}
	kinds := map[string]*statsGroup{}
	kindStates := map[string]map[string]int{}
	clientGroups := map[string]*statsGroup{}
	clientStates := map[string]map[string]int{}
	var points []statsRunPoint

	group := func(m map[string]*statsGroup, sm map[string]map[string]int, name string) (*statsGroup, map[string]int) {
		g := m[name]
		if g == nil {
			g = &statsGroup{Name: name}
			m[name] = g
			sm[name] = map[string]int{}
		}
		return g, sm[name]
	}

	for _, r := range runs {
		res.Totals.ByState[string(r.State)]++
		secs, hasDur := runSeconds(r)
		if hasDur {
			res.Totals.TotalSecs += secs
		}
		if r.StartedAt != nil && !r.StartedAt.Before(r.CreatedAt) {
			waits = append(waits, r.StartedAt.Sub(r.CreatedAt).Seconds())
		}
		if r.State == StateSucceeded && hasDur {
			okDurations = append(okDurations, secs)
			res.Duration.Total += secs
		}
		day := r.CreatedAt.UTC().Format("2006-01-02")
		d := days[day]
		if d == nil {
			d = &statsDay{Date: day}
			days[day] = d
		}
		switch r.State {
		case StateSucceeded:
			d.Succeeded++
		case StateFailed:
			d.Failed++
		default:
			d.Other++
		}
		if hasDur {
			d.Seconds += secs
			points = append(points, statsRunPoint{ID: r.ID, Project: r.Project, State: r.State,
				CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339), Seconds: secs, Mode: kindOf(r.Project)})
		}
		kg, ks := group(kinds, kindStates, kindOf(r.Project))
		kg.Runs++
		ks[string(r.State)]++
		if hasDur {
			kg.Seconds += secs
		}
		cg, cs := group(clientGroups, clientStates, clientOf(r))
		cg.Runs++
		cs[string(r.State)]++
		if hasDur {
			cg.Seconds += secs
		}
		if !IsManagedProject(r.Project) { // a generation of the API is not a user's project
			pg, ps := group(projects, projectStates, r.Project)
			pg.Runs++
			ps[string(r.State)]++
			if hasDur {
				pg.Seconds += secs
			}
		}
		for _, name := range gpuNames(r.GPU) {
			gg, gs := group(gpus, gpuStates, name)
			gg.Runs++
			gs[string(r.State)]++
			if hasDur {
				gg.Seconds += secs
			}
		}
	}
	res.Totals.SuccessRate = rate(res.Totals.ByState)
	if len(waits) > 0 {
		res.Totals.QueueWaitS = ptr(mean(waits))
	}
	if n := len(okDurations); n > 0 {
		sort.Float64s(okDurations)
		res.Duration.Runs = n
		res.Duration.Mean = ptr(mean(okDurations))
		res.Duration.Median = ptr(fsutil.Median(okDurations))
		res.Duration.P95 = ptr(percentile95(okDurations))
	}

	for _, d := range days {
		res.ByDay = append(res.ByDay, *d)
	}
	sort.Slice(res.ByDay, func(i, j int) bool { return res.ByDay[i].Date < res.ByDay[j].Date })

	flush := func(m map[string]*statsGroup, sm map[string]map[string]int) []statsGroup {
		out := make([]statsGroup, 0, len(m))
		for name, g := range m {
			g.SuccessRate = rate(sm[name])
			out = append(out, *g)
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Runs != out[j].Runs {
				return out[i].Runs > out[j].Runs
			}
			return out[i].Name < out[j].Name
		})
		return out
	}
	res.ByProject = flush(projects, projectStates)
	res.ByGPU = flush(gpus, gpuStates)
	res.ByMode = flush(kinds, kindStates)
	res.ByClient = flush(clientGroups, clientStates)

	// run durations over time (most recent window) and the slowest runs
	series := points
	if len(series) > durationSeriesCap {
		series = series[len(series)-durationSeriesCap:]
	}
	res.RunDurations = append(res.RunDurations, series...)
	slow := append([]statsRunPoint(nil), points...)
	sort.SliceStable(slow, func(i, j int) bool { return slow[i].Seconds > slow[j].Seconds })
	if len(slow) > slowestRuns {
		slow = slow[:slowestRuns]
	}
	res.Slowest = append(res.Slowest, slow...)

	// phases and cache, from the stored events
	ids := make([]string, len(runs))
	for i, r := range runs {
		ids[i] = r.ID
	}
	type acc struct {
		exec, reused int
		durs         []float64
	}
	phases := map[string]*acc{}
	for i := 0; i < len(ids); i += deleteChunk {
		end := min(i+deleteChunk, len(ids))
		var evs []RunEvent
		if err := h.db.Select("line").Where("run_id IN ?", ids[i:end]).Find(&evs).Error; err != nil {
			return nil, err
		}
		for _, e := range evs {
			var ev statEvent
			if json.Unmarshal([]byte(e.Line), &ev) != nil || ev.Phase == "" {
				continue
			}
			a := phases[ev.Phase]
			if a == nil {
				a = &acc{}
				phases[ev.Phase] = a
			}
			switch ev.Type {
			case "phase_end":
				a.exec++
				a.durs = append(a.durs, float64(ev.DurationMs)/1000)
			case "skipped":
				a.reused++
			}
		}
	}
	for name, a := range phases {
		p := statsPhase{Phase: name, Executions: a.exec, Reused: a.reused}
		res.Cache.Executed += a.exec
		res.Cache.Reused += a.reused
		if n := len(a.durs); n > 0 {
			sort.Float64s(a.durs)
			for _, d := range a.durs {
				p.TotalSecs += d
			}
			p.MeanSecs = ptr(p.TotalSecs / float64(n))
			p.P95Secs = ptr(percentile95(a.durs))
		}
		res.Phases = append(res.Phases, p)
	}
	sort.Slice(res.Phases, func(i, j int) bool {
		ri, rj := phaseRank(res.Phases[i].Phase), phaseRank(res.Phases[j].Phase)
		if ri != rj {
			return ri < rj
		}
		return res.Phases[i].Phase < res.Phases[j].Phase
	})
	if all := res.Cache.Executed + res.Cache.Reused; all > 0 {
		res.Cache.ReuseShare = ptr(float64(res.Cache.Reused) / float64(all))
	}

	if err := h.addGPUPeaks(res, ids); err != nil {
		return nil, err
	}
	return res, nil
}

// addGPUPeaks fills the VRAM peaks of the per-GPU rows from the stored samples:
// for each run, the highest percentage each GPU reached; per GPU, the highest
// of those and their mean.
func (h *historyStore) addGPUPeaks(res *statsResult, ids []string) error {
	type peak struct {
		RunID string
		GPU   int
		Used  int
		Total int
	}
	perRunGPU := map[string]map[string]float64{} // gpu -> run -> pct
	for i := 0; i < len(ids); i += deleteChunk {
		end := min(i+deleteChunk, len(ids))
		var rows []peak
		err := h.db.Model(&RunSample{}).
			Select("run_id, gpu, max(vram_used_mb) as used, max(vram_total_mb) as total").
			Where("run_id IN ? AND gpu >= 0", ids[i:end]).
			Group("run_id, gpu").Scan(&rows).Error
		if err != nil {
			return err
		}
		for _, p := range rows {
			if p.Total <= 0 {
				continue
			}
			name := strconv.Itoa(p.GPU)
			if perRunGPU[name] == nil {
				perRunGPU[name] = map[string]float64{}
			}
			perRunGPU[name][p.RunID] = 100 * float64(p.Used) / float64(p.Total)
		}
	}
	for i := range res.ByGPU {
		runs := perRunGPU[res.ByGPU[i].Name]
		if len(runs) == 0 {
			continue
		}
		top, sum := 0.0, 0.0
		for _, pct := range runs {
			top = math.Max(top, pct)
			sum += pct
		}
		res.ByGPU[i].PeakVRAMPct = ptr(round1(top))
		res.ByGPU[i].MeanPeakVRAMPct = ptr(round1(sum / float64(len(runs))))
		res.ByGPU[i].SampledRuns = len(runs)
	}
	return nil
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }

// parseStatsTime accepts an RFC 3339 timestamp or a plain date (UTC midnight).
func parseStatsTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02", s)
}

// GET /api/stats?project=&mode=&origin=&server=&client=&since=&until=
func (h *Handler) handleStats(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	until := time.Now().UTC()
	if v := q.Get("until"); v != "" {
		t, err := parseStatsTime(v)
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid until %q: use an RFC 3339 timestamp or a YYYY-MM-DD date", v))
			return
		}
		until = t
	}
	since := until.Add(-defaultStatsWindow)
	if v := q.Get("since"); v != "" {
		t, err := parseStatsTime(v)
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid since %q: use an RFC 3339 timestamp or a YYYY-MM-DD date", v))
			return
		}
		since = t
	}
	if since.After(until) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "since must not be after until")
		return
	}
	var modes []string
	for _, m := range strings.Split(q.Get("mode"), ",") {
		if m = strings.TrimSpace(m); m == "" {
			continue
		}
		if !validRunMode(m) {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid mode %q: use %s (comma separated)", m, RunModesText))
			return
		}
		modes = append(modes, m)
	}
	var origins []string
	for _, o := range strings.Split(q.Get("origin"), ",") {
		if o = strings.TrimSpace(o); o == "" {
			continue
		}
		if o != "desktop" && o != "mobile" {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid origin %q: use desktop or mobile (comma separated)", o))
			return
		}
		origins = append(origins, o)
	}
	var servers []string
	for _, n := range strings.Split(q.Get("server"), ",") {
		if n = strings.TrimSpace(n); n != "" {
			servers = append(servers, n)
		}
	}
	var clients []string
	for _, c := range strings.Split(q.Get("client"), ",") {
		if c = strings.TrimSpace(c); c == "" {
			continue
		}
		if !config.ValidClient(c) {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid client %q: use client names (%s), comma separated", c, config.ClientPatternText))
			return
		}
		clients = append(clients, c)
	}
	res, err := h.rm.hist.stats(q.Get("project"), modes, origins, servers, clients, since, until)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, "could not compute the statistics")
		return
	}
	h.markResults(res)
	api.WriteJSON(w, http.StatusOK, res)
}

// markResults sets result_available on the runs of the tables.
func (h *Handler) markResults(res *statsResult) {
	c := h.newResultCheck()
	mark := func(points []statsRunPoint) {
		for i, p := range points {
			if rec, err := h.rm.hist.getRun(p.ID); err == nil {
				points[i].ResultAvailable = c.available(*rec)
			}
		}
	}
	mark(res.Slowest)
	mark(res.RunDurations)
}

// --- per-run resources ------------------------------------------------------

type resourcePoint struct {
	TMs     int64 `json:"t_ms"`
	UsedMB  int   `json:"used_mb"`
	UtilPct *int  `json:"util_pct,omitempty"`
}

type resourceGPU struct {
	Index   int             `json:"index"`
	TotalMB int             `json:"total_mb"`
	Samples []resourcePoint `json:"samples"`
}

type resourceRAM struct {
	TotalMB int             `json:"total_mb"`
	Samples []resourcePoint `json:"samples"`
}

type vramPeak struct {
	GPU int     `json:"gpu"`
	MB  int     `json:"mb"`
	Pct float64 `json:"pct"`
}

type ramPeak struct {
	MB  int     `json:"mb"`
	Pct float64 `json:"pct"`
}

type resourcePeaks struct {
	VRAM []vramPeak `json:"vram"`
	RAM  *ramPeak   `json:"ram"`
}

type resourcesResult struct {
	IntervalMs int64         `json:"interval_ms"`
	Truncated  bool          `json:"truncated"`
	GPUs       []resourceGPU `json:"gpus"`
	RAM        resourceRAM   `json:"ram"`
	Peaks      resourcePeaks `json:"peaks"`
}

func (h *historyStore) resources(r *Run, interval time.Duration, maxRows int) (*resourcesResult, error) {
	var rows []RunSample
	if err := h.db.Where("run_id = ?", r.ID).Order("ts ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	res := &resourcesResult{
		IntervalMs: interval.Milliseconds(),
		Truncated:  len(rows) >= maxRows,
		GPUs:       []resourceGPU{},
		RAM:        resourceRAM{Samples: []resourcePoint{}},
		Peaks:      resourcePeaks{VRAM: []vramPeak{}},
	}
	if len(rows) == 0 {
		return res, nil
	}
	base := rows[0].TS
	if r.StartedAt != nil && r.StartedAt.Before(base) {
		base = *r.StartedAt
	}
	rel := func(t time.Time) int64 { return t.Sub(base).Milliseconds() }
	byGPU := map[int]*resourceGPU{}
	var order []int
	lastRAM := time.Time{}
	ramMax := 0
	for _, s := range rows {
		if s.GPU >= 0 {
			g := byGPU[s.GPU]
			if g == nil {
				g = &resourceGPU{Index: s.GPU, Samples: []resourcePoint{}}
				byGPU[s.GPU] = g
				order = append(order, s.GPU)
			}
			if s.VRAMTotalMB > g.TotalMB {
				g.TotalMB = s.VRAMTotalMB
			}
			u := s.UtilPct
			g.Samples = append(g.Samples, resourcePoint{TMs: rel(s.TS), UsedMB: s.VRAMUsedMB, UtilPct: &u})
		}
		// RAM is stored on every row of a tick: keep one point per instant.
		if !s.TS.Equal(lastRAM) {
			lastRAM = s.TS
			if s.RAMTotalMB > res.RAM.TotalMB {
				res.RAM.TotalMB = s.RAMTotalMB
			}
			res.RAM.Samples = append(res.RAM.Samples, resourcePoint{TMs: rel(s.TS), UsedMB: s.RAMUsedMB})
			ramMax = max(ramMax, s.RAMUsedMB)
		}
	}
	sort.Ints(order)
	for _, idx := range order {
		g := byGPU[idx]
		top := 0
		for _, p := range g.Samples {
			top = max(top, p.UsedMB)
		}
		pct := 0.0
		if g.TotalMB > 0 {
			pct = round1(100 * float64(top) / float64(g.TotalMB))
		}
		res.GPUs = append(res.GPUs, *g)
		res.Peaks.VRAM = append(res.Peaks.VRAM, vramPeak{GPU: idx, MB: top, Pct: pct})
	}
	if res.RAM.TotalMB > 0 {
		res.Peaks.RAM = &ramPeak{MB: ramMax, Pct: round1(100 * float64(ramMax) / float64(res.RAM.TotalMB))}
	} else {
		res.RAM.Samples = []resourcePoint{} // RAM unreadable on this machine: nothing to draw
	}
	return res, nil
}

// GET /api/runs/{id}/resources
func (h *Handler) handleRunResources(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rec, err := h.rm.hist.getRun(id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("run %s not found", id))
		return
	}
	h.rm.sampler.mu.Lock()
	interval, maxRows := h.rm.sampler.interval, h.rm.sampler.maxRows
	h.rm.sampler.mu.Unlock()
	res, err := h.rm.hist.resources(rec, interval, maxRows)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, "could not read the run's resources")
		return
	}
	api.WriteJSON(w, http.StatusOK, res)
}

// projectMode is the kind of a project (video, images or music) read from its scenes.yaml; empty when the
// project is gone or its file cannot be read.
func (h *Handler) projectMode(project string) config.OutputMode {
	cfg, err := h.ProjectConfig(project)
	if err != nil {
		return ""
	}
	if cfg.Output.Mode == "" {
		return config.ModeVideo
	}
	return cfg.Output.Mode
}

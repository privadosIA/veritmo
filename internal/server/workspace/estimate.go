package workspace

// estimate.go builds the estimated time of a project (spec project-estimate): the phases it will run, each with the
// time this machine has measured for it where the history has enough (phaseduration.go) and with the catalog's table,
// scaled by the card's speed, where it has not; the answer says which. A phase the cache will reuse adds nothing.

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"veritmo/internal/server/modelcat"

	"veritmo/internal/platform/cache"
	"veritmo/internal/project/config"
	"veritmo/internal/project/pipeline"
	"veritmo/internal/server/api"
)

// Time that is not the models': narration, the clip of each scene, joining (the table of the dashboard's planner).
const (
	estimatePerSceneSeconds = 4
	estimateFixedSeconds    = 15
)

// Where an estimate comes from.
const (
	sourceHistory = "history"
	sourceTable   = "table"
	sourceMixed   = "mixed"
)

// EstimateRequest is the body of POST /api/estimate: the scenes.yaml to estimate and, optionally, the project whose cache it reuses.
type EstimateRequest struct {
	// YAML is the scenes.yaml to estimate (a draft that is not a project yet, or the project's own).
	YAML string `json:"yaml"`
	// Project, when given, is a project of the server whose cache is read: the phases it will reuse add nothing.
	Project string `json:"project,omitempty"`
	// GPU is the card the run would use (an index); empty is the one the server would choose.
	GPU string `json:"gpu,omitempty"`
}

// EstimateView is the forecast of a run: its seconds and where they come from (this machine's history, the table of timings or both).
type EstimateView struct {
	Seconds int    `json:"seconds"`
	Source  string `json:"source"`
	// Executions is the fewest executions any history figure of the estimate rests on (absent without history).
	Executions int `json:"executions,omitempty"`
	// HistoryPhases, TablePhases and CachedPhases count the render and animate phases by where their time comes
	// from: this machine's history, the table, or nowhere (the cache reuses them).
	HistoryPhases int    `json:"history_phases"`
	TablePhases   int    `json:"table_phases"`
	CachedPhases  int    `json:"cached_phases"`
	GPU           string `json:"gpu,omitempty"`
}

func zeroAsMax(n int) int {
	if n == 0 {
		return int(^uint(0) >> 1)
	}
	return n
}

// estimator holds what an estimate reads.
type estimator struct {
	cat      []modelcat.Entry
	hist     *historyStore
	gpu      string
	speed    float64
	manifest *cache.Manifest
	dir      string
}

// tablePicture is the seconds of one picture by the table, scaled by the card (the dashboard's own rule when the
// catalog has no timing for the model: 20 s at 1024x576 and 8 steps).
func (e *estimator) tablePicture(cfg *config.Config, sc config.Scene, units int) float64 {
	if t := ExpectedPicture(e.cat, cfg, sc, units); t > 0 {
		return t * e.speed
	}
	eff := sc.EffectiveRender(cfg.Defaults)
	px := pixelsOf(eff.Resolution)
	if px == 0 || eff.Steps == 0 {
		return 20 * float64(max(units, 1)) * e.speed
	}
	return 20 * (px / pixelsOf("1024x576")) * (float64(eff.Steps) / 8) * float64(max(units, 1)) * e.speed
}

func (e *estimator) tableClip(cfg *config.Config, sc config.Scene) float64 {
	if t := ExpectedClip(e.cat, cfg, sc); t > 0 {
		return t * e.speed
	}
	return 85 * e.speed
}

func (e *estimator) estimate(cfg *config.Config) EstimateView {
	out := EstimateView{GPU: e.gpu}
	var seconds float64
	minExec := 0
	add := func(phase string, key phaseKey, units int, table float64) {
		st := e.hist.phaseDuration(e.gpu, key)
		if st.Enough() {
			seconds += st.Median * float64(units)
			out.HistoryPhases++
			if minExec == 0 || st.Executions < minExec {
				minExec = st.Executions
			}
			return
		}
		seconds += table
		out.TablePhases++
	}
	images := cfg.Output.Mode == config.ModeImages
	scenes := 0
	for _, sc := range cfg.Scenes {
		scenes++
		if sc.Image == "" && sc.Clip == "" && !sc.IsMotionGraphic() && !sc.IsBlender() {
			units := 1
			if images {
				units = sc.EffectiveVariants()
			}
			if e.cachedRender(cfg, sc) {
				out.CachedPhases++
			} else {
				add("render", phaseKeyOf(cfg, sc, "render"), units, e.tablePicture(cfg, sc, units))
			}
		}
		if !images {
			if sc.Animate && sc.Clip == "" && cfg.Defaults.Animate != nil {
				if e.cachedAnimate(cfg, sc) {
					out.CachedPhases++
				} else {
					add("animate", phaseKeyOf(cfg, sc, "animate"), 1, e.tableClip(cfg, sc))
				}
			}
		}
	}
	// The rest (narration, probe, clip, join): the history of this machine when it has enough of both parts, else the
	// table. It is the work of the CPU and ffmpeg, so the speed of the card does not scale it.
	scene, join := e.hist.cpuDuration(e.gpu, kindScene), e.hist.cpuDuration(e.gpu, kindJoin)
	if scene.Enough() && join.Enough() {
		seconds += scene.Median*float64(scenes) + join.Median
		out.HistoryPhases++
		minExec = min(zeroAsMax(minExec), scene.Executions, join.Executions)
	} else {
		if !images {
			seconds += estimatePerSceneSeconds * float64(scenes)
		}
		seconds += estimateFixedSeconds
		out.TablePhases++
	}
	out.Seconds = int(math.Round(seconds))
	switch {
	case out.HistoryPhases > 0 && out.TablePhases == 0:
		out.Source = sourceHistory
	case out.HistoryPhases > 0:
		out.Source = sourceMixed
	default:
		out.Source = sourceTable
	}
	if out.HistoryPhases > 0 {
		out.Executions = minExec
	}
	return out
}

// cachedRender says the cache holds this scene's picture (the project's manifest has its image hash).
func (e *estimator) cachedRender(cfg *config.Config, sc config.Scene) bool {
	if e.manifest == nil || cfg.Output.Mode == config.ModeImages {
		return false
	}
	hash, err := pipeline.VideoImageHash(cfg, sc, e.dir)
	entry, ok := e.manifest.Scenes[sc.ID]
	return err == nil && ok && entry.ImageHash == hash
}

// cachedAnimate says the cache holds this scene's animation: its picture and its animate hash match.
func (e *estimator) cachedAnimate(cfg *config.Config, sc config.Scene) bool {
	if e.manifest == nil || !e.cachedRender(cfg, sc) {
		return sc.Image != "" && e.manifest != nil && e.animateHit(cfg, sc)
	}
	return e.animateHit(cfg, sc)
}

func (e *estimator) animateHit(cfg *config.Config, sc config.Scene) bool {
	entry, ok := e.manifest.Scenes[sc.ID]
	return ok && entry.AnimateHash != "" && entry.AnimateHash == cache.AnimateHash(pipeline.SceneAnimateParams(cfg, sc, e.dir))
}

// estimateGPU is the name of the card an estimate is for: the one asked for, or the one the server would choose.
func (h *Handler) estimateGPU(requested string) string {
	if name := h.runGPUName(h.defaultGPU(requested)); name != "" {
		return name
	}
	gpus, err := h.QueryGPUs()
	if err != nil || len(gpus) == 0 {
		return ""
	}
	best := gpus[0]
	for _, g := range gpus[1:] {
		if g.TotalVRAMMB > best.TotalVRAMMB {
			best = g
		}
	}
	return best.Name
}

// POST /api/estimate {yaml, project?, gpu?}
func (h *Handler) handleEstimate(w http.ResponseWriter, r *http.Request) {
	var req EstimateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	cfg, err := config.LoadBytes([]byte(req.YAML))
	if err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
		return
	}
	if cfg.Output.Mode == config.ModeMusic {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, "the estimate covers video and image projects, not songs")
		return
	}
	e := &estimator{cat: h.host.Catalog(), hist: h.rm.hist, gpu: h.estimateGPU(req.GPU), speed: 1}
	if gpus, err := h.QueryGPUs(); err == nil {
		if vram, speed := h.host.MachineOf(h.withMeasuredSpeed(gpus)); vram > 0 {
			e.speed = speed
		}
	}
	if req.Project != "" {
		if !api.ProjectIDPattern.MatchString(req.Project) || !h.ProjectExists(req.Project) || !h.host.ProjectVisible(req.Project) {
			api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("project %q not found", req.Project))
			return
		}
		e.dir = h.ProjectDir(req.Project)
		if m, err := cache.LoadManifest(filepath.Join(e.dir, cache.ManifestPath())); err == nil {
			if _, statErr := os.Stat(filepath.Join(e.dir, cache.ManifestPath())); statErr == nil {
				e.manifest = m
			}
		}
	}
	api.WriteJSON(w, http.StatusOK, e.estimate(cfg))
}

// withMeasuredSpeed gives each card the speed its own runs measured (the environment report does the same).
func (h *Handler) withMeasuredSpeed(gpus []GPUStatus) []GPUStatus {
	for i := range gpus {
		if f, n, ok := h.rm.hist.speedOf(gpus[i].Name); ok {
			gpus[i].SpeedFactor, gpus[i].SpeedSamples = f, n
		}
	}
	return gpus
}

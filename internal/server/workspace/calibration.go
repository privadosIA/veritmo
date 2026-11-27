package workspace

// calibration.go learns how fast this machine is. After a run, every render and animate phase it executed is
// compared with what the catalog's timing (measured on the reference machine, an RTX 5060 Ti) says it should
// have taken at those settings: the ratio of the two is a sample of the card's speed. The environment report
// gives each card the median of its recent samples, and the dashboard's planners use it instead of the table of
// card families (see specs/environment-status).

import (
	"encoding/json"
	"fmt"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"veritmo/internal/server/modelcat"

	"gorm.io/gorm"

	"veritmo/internal/project/config"
)

// CalibrationSample is one comparison of a phase with its expected time.
type CalibrationSample struct {
	ID    uint   `gorm:"primaryKey" json:"-"`
	RunID string `gorm:"size:64;index" json:"run_id"`
	// Server is the server whose card was measured: the speed of a machine is that machine's, so a history that
	// several share keeps each one's samples apart.
	Server string `gorm:"size:100;index" json:"-"`
	// GPU is the card's name as nvidia-smi gives it.
	GPU string `gorm:"size:191;index" json:"gpu"`
	// Kind is the phase: "render" or "animate".
	Kind     string    `gorm:"size:32" json:"kind"`
	Expected float64   `json:"expected"`
	Actual   float64   `json:"actual"`
	Ratio    float64   `json:"ratio"`
	At       time.Time `json:"at"`

	// What the phase was made with, so the history can say how long that took on this card (see phaseduration.go):
	// the model file (the diffusion model, or for an animation the engine's), the size, the steps and, for an
	// animation, the frames. Units is how many pictures the phase made (the variants of an images-mode scene), so
	// a duration is read per picture. Succeeded says the run ended well: only those count as history. Samples
	// stored before these columns existed have them empty and never match a history question.
	Model      string `gorm:"size:191;index" json:"-"`
	Resolution string `gorm:"size:32" json:"-"`
	Steps      int    `json:"-"`
	Frames     int    `json:"-"`
	Units      int    `json:"-"`
	Succeeded  bool   `json:"-"`
}

const (
	// calibrationKeep is how many samples of a card are kept; the median reads the latest calibrationWindow.
	calibrationKeep   = 400
	calibrationWindow = 20
	// calibrationMinSamples is how many samples it takes before the measured speed replaces the table's.
	calibrationMinSamples = 2
	// A phase shorter than this says nothing about speed (it is dominated by loading).
	calibrationMinExpected = 2.0
	// Ratios outside these bounds are not about speed: something else was going on.
	calibrationMinRatio = 0.15
	calibrationMaxRatio = 25.0
)

func entryOfFile(cat []modelcat.Entry, name string, roles ...string) (modelcat.Entry, bool) {
	for _, e := range cat {
		for _, f := range e.Files {
			if f.Name != name {
				continue
			}
			if len(roles) == 0 {
				return e, true
			}
			for _, r := range roles {
				if f.Role == r {
					return e, true
				}
			}
		}
	}
	return modelcat.Entry{}, false
}

// entryWithRoleWithout is entryWithRole skipping the entries that also have a file with the role `not`: the
// MiniMax H3 bundles and the LTX one both have an audio VAE, and only LTX has connectors.
func entryWithRoleWithout(cat []modelcat.Entry, role, not string) (modelcat.Entry, bool) {
	for _, e := range cat {
		has, excluded := false, false
		for _, f := range e.Files {
			has = has || f.Role == role
			excluded = excluded || f.Role == not
		}
		if has && !excluded {
			return e, true
		}
	}
	return modelcat.Entry{}, false
}

func entryWithRole(cat []modelcat.Entry, role string) (modelcat.Entry, bool) {
	for _, e := range cat {
		for _, f := range e.Files {
			if f.Role == role {
				return e, true
			}
		}
	}
	return modelcat.Entry{}, false
}

func pixelsOf(res string) float64 {
	w, h, err := parseWxHServer(res)
	if err != nil {
		return 0
	}
	return float64(w * h)
}

func parseWxHServer(s string) (int, int, error) {
	parts := strings.SplitN(s, "x", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("%q is not WIDTHxHEIGHT", s)
	}
	w, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, err
	}
	h, err := strconv.Atoi(parts[1])
	return w, h, err
}

// ExpectedPicture is how long the picture of a scene takes on the reference machine: the entry's time scaled by
// pixels and steps (the same rule the dashboard's planner uses). 0 when the catalog has no timing for the model.
func ExpectedPicture(cat []modelcat.Entry, cfg *config.Config, sc config.Scene, variants int) float64 {
	d := cfg.Defaults
	file := path.Base(d.Model)
	if d.ModelSet != nil {
		file = path.Base(d.ModelSet.DiffusionModel)
	}
	e, ok := entryOfFile(cat, file, "model", "")
	if !ok || e.Timing == nil || e.Timing.Steps == 0 {
		return 0
	}
	eff := sc.EffectiveRender(d)
	px, base := pixelsOf(eff.Resolution), pixelsOf(e.Timing.Resolution)
	if px == 0 || base == 0 || eff.Steps == 0 {
		return 0
	}
	return float64(e.Timing.Seconds) * (px / base) * (float64(eff.Steps) / float64(e.Timing.Steps)) * float64(max(variants, 1))
}

// ExpectedClip is how long the animation of a scene takes on the reference machine: the time of the engine's
// entry scaled by pixels, frames and steps, with the turbo basis when the LoRA is set.
func ExpectedClip(cat []modelcat.Entry, cfg *config.Config, sc config.Scene) float64 {
	a := cfg.Defaults.Animate
	if a == nil {
		return 0
	}
	r := a.Resolved()
	var e modelcat.Entry
	var ok bool
	switch {
	case a.IsAnimateDiff():
		e, ok = entryWithRole(cat, "motion_module")
	case a.IsMiniMaxH3():
		e, ok = entryWithRoleWithout(cat, "audio_vae", "connectors")
	case a.IsLTX():
		e, ok = entryWithRole(cat, "connectors")
	default:
		e, ok = entryOfFile(cat, path.Base(a.DiffusionModel), "model")
	}
	if !ok || e.Timing == nil || e.Timing.Frames == 0 || e.Timing.Steps == 0 {
		return 0
	}
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
	base, baseSteps := float64(e.Timing.Seconds), float64(e.Timing.Steps)
	if a.IsMiniMaxH3() && a.LoRA != "" && e.Timing.TurboSeconds > 0 {
		base, baseSteps = float64(e.Timing.TurboSeconds), float64(e.Timing.TurboSteps)
	}
	px, bpx := pixelsOf(res), pixelsOf(e.Timing.Resolution)
	if px == 0 || bpx == 0 || baseSteps == 0 {
		return 0
	}
	return base * (px / bpx) * (float64(frames) / float64(e.Timing.Frames)) * (float64(steps) / baseSteps)
}

// CalibrationSamples reads the stored events of a run and compares every render and animate phase it executed
// with its expected time. gpu is the card's name.
func CalibrationSamples(cat []modelcat.Entry, runID, gpu string, cfg *config.Config, lines []string, succeeded bool) []CalibrationSample {
	scenes := map[string]config.Scene{}
	for _, s := range cfg.Scenes {
		scenes[s.ID] = s
	}
	var out []CalibrationSample
	for _, line := range lines {
		var ev struct {
			Type       string `json:"type"`
			SceneID    string `json:"scene_id"`
			Phase      string `json:"phase"`
			DurationMS int64  `json:"duration_ms"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil || ev.Type != "phase_end" || ev.DurationMS <= 0 {
			continue
		}
		sc, ok := scenes[ev.SceneID]
		if !ok {
			continue
		}
		var expected float64
		key := phaseKeyOf(cfg, sc, ev.Phase)
		switch ev.Phase {
		case "render":
			variants := 1
			if cfg.Output.Mode == config.ModeImages {
				variants = sc.EffectiveVariants()
			}
			key.Units = variants
			expected = ExpectedPicture(cat, cfg, sc, variants)
		case "animate":
			expected = ExpectedClip(cat, cfg, sc)
		default:
			continue
		}
		actual := float64(ev.DurationMS) / 1000
		if expected < calibrationMinExpected {
			continue
		}
		ratio := actual / expected
		if ratio < calibrationMinRatio || ratio > calibrationMaxRatio {
			continue
		}
		out = append(out, CalibrationSample{RunID: runID, GPU: gpu, Kind: ev.Phase, Expected: expected, Actual: actual, Ratio: ratio, At: time.Now().UTC(),
			Model: key.Model, Resolution: key.Resolution, Steps: key.Steps, Frames: key.Frames, Units: max(key.Units, 1), Succeeded: succeeded})
	}
	return append(out, cpuSamples(runID, gpu, cfg, lines, succeeded)...)
}

// The kinds of sample that are not a phase of the card: the narration, probe, clip and export of one scene, added up,
// and the final join. They say how long the rest of a project takes on this machine; they say nothing about the speed
// of a card, so speedOf never reads them (and they have no expected time).
const (
	kindScene = "scene"
	kindJoin  = "join"
)

// cpuSamples is one sample per scene that ran any of its CPU phases (what they took together) and one for the final
// join, from the phases the run executed.
func cpuSamples(runID, gpu string, cfg *config.Config, lines []string, succeeded bool) []CalibrationSample {
	known := map[string]bool{}
	for _, s := range cfg.Scenes {
		known[s.ID] = true
	}
	perScene := map[string]float64{}
	var order []string
	var join float64
	for _, line := range lines {
		var ev struct {
			Type       string `json:"type"`
			SceneID    string `json:"scene_id"`
			Phase      string `json:"phase"`
			DurationMS int64  `json:"duration_ms"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil || ev.Type != "phase_end" || ev.DurationMS <= 0 {
			continue
		}
		secs := float64(ev.DurationMS) / 1000
		switch {
		case ev.SceneID == "_global" && (ev.Phase == "concat" || ev.Phase == "stack" || ev.Phase == "export"):
			join += secs
		case known[ev.SceneID] && (ev.Phase == "tts" || ev.Phase == "probe" || ev.Phase == "clip" || ev.Phase == "export"):
			if _, seen := perScene[ev.SceneID]; !seen {
				order = append(order, ev.SceneID)
			}
			perScene[ev.SceneID] += secs
		}
	}
	var out []CalibrationSample
	now := time.Now().UTC()
	for _, id := range order {
		out = append(out, CalibrationSample{RunID: runID, GPU: gpu, Kind: kindScene, Actual: perScene[id], Units: 1, Succeeded: succeeded, At: now})
	}
	if join > 0 {
		out = append(out, CalibrationSample{RunID: runID, GPU: gpu, Kind: kindJoin, Actual: join, Units: 1, Succeeded: succeeded, At: now})
	}
	return out
}

// addCalibration stores samples and keeps only the latest calibrationKeep of each card.
func (h *historyStore) addCalibration(samples []CalibrationSample) error {
	if len(samples) == 0 {
		return nil
	}
	for i := range samples {
		samples[i].Server = h.server
	}
	return h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&samples).Error; err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, s := range samples {
			if seen[s.GPU] {
				continue
			}
			seen[s.GPU] = true
			var old []uint
			if err := tx.Model(&CalibrationSample{}).Where("gpu = ? AND server = ?", s.GPU, h.server).Order("id DESC").Offset(calibrationKeep).Limit(1_000_000).Pluck("id", &old).Error; err != nil {
				return err
			}
			if len(old) > 0 {
				if err := tx.Delete(&CalibrationSample{}, old).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// speedOf is the measured speed of a card: the median ratio of its latest samples (2 means twice as slow as
// the reference machine) and how many samples that is; ok is false until there are enough.
func (h *historyStore) speedOf(gpu string) (factor float64, samples int, ok bool) {
	var ratios []float64
	if err := h.db.Model(&CalibrationSample{}).Where("gpu = ? AND server = ? AND kind NOT IN ?", gpu, h.server, []string{kindScene, kindJoin}).Order("id DESC").Limit(calibrationWindow).Pluck("ratio", &ratios).Error; err != nil || len(ratios) < calibrationMinSamples {
		return 0, len(ratios), false
	}
	sort.Float64s(ratios)
	m := ratios[len(ratios)/2]
	if len(ratios)%2 == 0 {
		m = (ratios[len(ratios)/2-1] + ratios[len(ratios)/2]) / 2
	}
	return math.Round(m*100) / 100, len(ratios), true
}

// runGPUName is the name of the card a run used: the one its GPU option names, or the only card of the machine
// when none was chosen; "" when it cannot be told (several cards, or none readable).
func (h *Handler) runGPUName(gpu string) string {
	gpus, err := h.QueryGPUs()
	if err != nil || len(gpus) == 0 {
		return ""
	}
	if gpu == "" {
		if len(gpus) == 1 {
			return gpus[0].Name
		}
		return ""
	}
	idx, err := strconv.Atoi(gpu)
	if err != nil {
		return "" // several cards in one run: nothing to attribute
	}
	for _, g := range gpus {
		if g.Index == idx {
			return g.Name
		}
	}
	return ""
}

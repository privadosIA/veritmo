package generation

// plan.go is the planning behind the generation API (spec video-generation-api and image-edit-api):
// which ways of making a video the machine has, which models can edit a picture, and how a request
// becomes the scenes.yaml of a project. It is the Go twin of web/src/app/editor/auto-plan.ts and
// quality-tiers.ts: the identifiers of the options are the dashboard's, so both agree.

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"

	"veritmo/internal/platform/sched"
	"veritmo/internal/project/config"
)

// --- the machine ---------------------------------------------------------------------------------

// planMachine is what the planner knows about the machine: the card that will do the work and the memory.
type planMachine struct {
	VRAMMB int
	// RAMGB is 0 when the memory is not known (no entry is then ruled out by its RAM).
	RAMGB int
	// Speed is the multiple of the reference machine's times (RTX 5060 Ti 16 GB = 1): 2 is twice as slow.
	Speed float64
}

// gpuSpeed is the time of a card against the reference one (an approximation by card family, not a
// measure: it only scales the estimates). The first pattern that matches wins.
var gpuSpeed = []struct {
	re *regexp.Regexp
	f  float64
}{
	{regexp.MustCompile(`5090`), 0.25}, {regexp.MustCompile(`4090`), 0.33}, {regexp.MustCompile(`5080`), 0.45},
	{regexp.MustCompile(`4080`), 0.55}, {regexp.MustCompile(`(?i)5070 ?Ti`), 0.65}, {regexp.MustCompile(`3090`), 0.7},
	{regexp.MustCompile(`(?i)4070 ?Ti`), 0.8}, {regexp.MustCompile(`5070`), 0.8}, {regexp.MustCompile(`3080`), 0.8},
	{regexp.MustCompile(`4070`), 0.95}, {regexp.MustCompile(`(?i)5060 ?Ti`), 1}, {regexp.MustCompile(`(?i)4060 ?Ti`), 1.05},
	{regexp.MustCompile(`3070`), 1.1}, {regexp.MustCompile(`5060`), 1.15}, {regexp.MustCompile(`(?i)3060 ?Ti`), 1.2},
	{regexp.MustCompile(`4060`), 1.3}, {regexp.MustCompile(`2080`), 1.3}, {regexp.MustCompile(`3060`), 1.5},
	{regexp.MustCompile(`2070`), 1.6}, {regexp.MustCompile(`(?i)1080 ?Ti`), 2}, {regexp.MustCompile(`2060`), 2},
	{regexp.MustCompile(`1660|1650`), 3},
}

// MachineOf describes the machine from its cards and its memory: the biggest card by its total memory.
func MachineOf(gpus []gpuStatus, ramTotalBytes int64) planMachine {
	mem := func(g gpuStatus) int {
		if g.TotalVRAMMB > 0 {
			return g.TotalVRAMMB
		}
		return g.FreeVRAMMB
	}
	m := planMachine{Speed: 20}
	if ramTotalBytes > 0 {
		m.RAMGB = int(math.Round(float64(ramTotalBytes) / (1 << 30)))
	}
	if len(gpus) == 0 {
		return m
	}
	best := gpus[0]
	for _, g := range gpus[1:] {
		if mem(g) > mem(best) {
			best = g
		}
	}
	m.VRAMMB = mem(best)
	switch {
	case best.SpeedFactor > 0 && best.SpeedSamples > 0:
		m.Speed = best.SpeedFactor // what the machine did in its own runs beats a table of card families
	default:
		m.Speed = 1.5
		for _, g := range gpuSpeed {
			if g.re.MatchString(best.Name) {
				m.Speed = g.f
				break
			}
		}
	}
	return m
}

// fitsMachine says whether an entry fits the card and the memory.
func fitsMachine(e catalogEntry, m planMachine) bool {
	return e.VRAMMB <= m.VRAMMB && (e.MinRAMGB == 0 || m.RAMGB == 0 || e.MinRAMGB <= m.RAMGB)
}

func needsText(e catalogEntry) string {
	ram := ""
	if e.MinRAMGB > 0 {
		ram = fmt.Sprintf(" and %d GB of RAM", e.MinRAMGB)
	}
	return fmt.Sprintf("%s does not fit this machine: it needs %d GB of VRAM%s", e.Name, int(math.Round(float64(e.VRAMMB)/1024)), ram)
}

// --- catalog lookups ---------------------------------------------------------------------------

func fileOf(e catalogEntry, role string) string {
	for _, f := range e.Files {
		if f.Role == role {
			return f.Name
		}
	}
	if role == "model" { // a checkpoint that is the entry's only file has no role (SD 1.5, SDXL)
		for _, f := range e.Files {
			if f.Role == "" {
				return f.Name
			}
		}
	}
	return ""
}

func hasRole(e catalogEntry, role string) bool {
	for _, f := range e.Files {
		if f.Role == role {
			return true
		}
	}
	return false
}

func isLTXEntry(e catalogEntry) bool { return e.Kind == "video" && hasRole(e, "connectors") }

func isFirstFrameEntry(e catalogEntry) bool {
	for _, f := range e.Files {
		if f.Role == "model" && strings.Contains(strings.ToLower(f.Name), "fl2va") {
			return true
		}
	}
	return false
}

func isMiniMaxEntry(e catalogEntry) bool {
	return e.Kind == "video" && hasRole(e, "audio_vae") && !isLTXEntry(e)
}

func findEntry(cat []catalogEntry, ok func(catalogEntry) bool) *catalogEntry {
	for i := range cat {
		if ok(cat[i]) {
			return &cat[i]
		}
	}
	return nil
}

func ltxEntryOf(cat []catalogEntry) *catalogEntry { return findEntry(cat, isLTXEntry) }
func miniMaxEntryOf(cat []catalogEntry) *catalogEntry {
	return findEntry(cat, func(e catalogEntry) bool { return isMiniMaxEntry(e) && !isFirstFrameEntry(e) })
}
func miniMaxFirstFrameEntryOf(cat []catalogEntry) *catalogEntry {
	return findEntry(cat, func(e catalogEntry) bool { return isMiniMaxEntry(e) && isFirstFrameEntry(e) })
}
func animateDiffEntryOf(cat []catalogEntry) *catalogEntry {
	return findEntry(cat, func(e catalogEntry) bool { return e.Kind == "video" && hasRole(e, "motion_module") })
}

// installedSD15 is the project path of the installed SD 1.5 checkpoint (the AnimateDiff base); "" when it is not.
func installedSD15(cat []catalogEntry) string {
	for _, e := range cat {
		if e.ID == "sd-1-5" && e.Installed {
			if f := fileOf(e, "model"); f != "" {
				return "models/" + f
			}
		}
	}
	return ""
}

// pickStyleEntry is the installed image model that fits for a style, the one with the lowest style_rank.
func pickStyleEntry(cat []catalogEntry, m planMachine, style string) *catalogEntry {
	var best *catalogEntry
	for i := range cat {
		e := &cat[i]
		if e.Kind != "model" || e.Edit || !e.Installed || !fitsMachine(*e, m) || !slices.Contains(e.Styles, style) {
			continue
		}
		if best == nil || rankOf(*e) < rankOf(*best) {
			best = e
		}
	}
	return best
}

func rankOf(e catalogEntry) int {
	if e.StyleRank == 0 {
		return 99
	}
	return e.StyleRank
}

// stillEntry is the image model the first picture of a video is made with: photo, else general, else anime.
func stillEntry(cat []catalogEntry, m planMachine) *catalogEntry {
	for _, style := range []string{"photo", "general", "anime"} {
		if e := pickStyleEntry(cat, m, style); e != nil {
			return e
		}
	}
	return nil
}

// --- resolutions -------------------------------------------------------------------------------

func parseRes(res string) (w, h int, ok bool) {
	if _, err := fmt.Sscanf(res, "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}

func resPixels(res string) int {
	w, h, ok := parseRes(res)
	if !ok {
		return 0
	}
	return w * h
}

// scaleRes scales both sides of WIDTHxHEIGHT and rounds each to a multiple (at least one multiple).
func scaleRes(res string, factor float64, multiple int) string {
	w, h, ok := parseRes(res)
	if !ok {
		return res
	}
	round := func(n int) int {
		v := int(math.Round(float64(n)*factor/float64(multiple))) * multiple
		if v < multiple {
			v = multiple
		}
		return v
	}
	return fmt.Sprintf("%dx%d", round(w), round(h))
}

// fitRes is the biggest resolution of the same shape, in multiples of 64, that stays within the pixels.
func fitRes(res string, maxPx int) string {
	if resPixels(res) <= maxPx {
		return res
	}
	factor := math.Sqrt(float64(maxPx) / float64(resPixels(res)))
	out := scaleRes(res, factor, 64)
	for resPixels(out) > maxPx && factor > 0.2 {
		factor -= 0.02
		out = scaleRes(res, factor, 64)
	}
	return out
}

// shapeRes gives a resolution the shape of an aspect ratio with about the pixels of base scaled by the quality,
// in multiples of `multiple` and with no side under 256.
func shapeRes(base string, aw, ah int, scale float64, multiple int) string {
	px := float64(resPixels(base)) * scale * scale
	if px <= 0 {
		px = 512 * 512
	}
	round := func(v float64) int {
		n := int(math.Round(v/float64(multiple))) * multiple
		minimum := ((256 + multiple - 1) / multiple) * multiple
		if n < minimum {
			n = minimum
		}
		return n
	}
	h := math.Sqrt(px * float64(ah) / float64(aw))
	w := h * float64(aw) / float64(ah)
	return fmt.Sprintf("%dx%d", round(w), round(h))
}

// maxPicturePixels is the most pixels a picture of a model can have on this machine (0: no limit to speak of).
// Measured with IntoRealism fp8 on a 16 GB card: 13.8 GB at 1024x576, 15.2 GB at 1280x704.
func maxPicturePixels(e *catalogEntry, m planMachine) int {
	if e == nil || e.VRAMMB == 0 || m.VRAMMB <= 0 {
		return 0
	}
	base := ""
	if e.Preset != nil {
		base = e.Preset.Resolution
	}
	if base == "" && e.Timing != nil {
		base = e.Timing.Resolution
	}
	if base == "" {
		base = "1024x576"
	}
	return resPixels(base) + int((float64(m.VRAMMB)*0.95-float64(e.VRAMMB))/4.6*1000)
}

// --- quality -----------------------------------------------------------------------------------

// The qualities of a request: they scale the sides of the picture and of the clip.
var (
	qualityNames = []string{"draft", "normal", "high"}
	qualityScale = map[string]float64{"draft": 0.5, "normal": 0.75, "high": 1}
	// qualityTier is the level of the picture model a quality asks for (quality-tiers).
	qualityTier = map[string]string{"draft": "low", "normal": "medium", "high": "high"}
)

type tierFactor struct {
	res, steps float64
	frames     int
}

var tierFactors = map[string]tierFactor{
	"low":    {0.75, 0.5, 17},
	"medium": {1, 1, 33},
	"high":   {1.25, 1.5, 49},
}

// animateDiffLevels are AnimateDiff's levels: 16 frames is what the motion module was trained on.
var animateDiffLevels = map[string]struct {
	res           string
	frames, steps int
}{
	"low":    {"384x384", 8, 12},
	"medium": {"512x512", 16, 25},
	"high":   {"640x640", 24, 38},
}

// aspectRatios are the shapes a request can ask for.
var aspectRatios = []string{"16:9", "9:16", "1:1", "4:3", "3:4"}

func parseAspect(s string) (w, h int, ok bool) {
	if _, err := fmt.Sscanf(s, "%d:%d", &w, &h); err != nil || w <= 0 || h <= 0 || !slices.Contains(aspectRatios, s) {
		return 0, 0, false
	}
	return w, h, true
}

// --- the video choices -------------------------------------------------------------------------

// videoChoice is one way of making a video, with whether this machine can do it.
type videoChoice struct {
	ID          string
	Name        string
	Description string
	Engine      string
	Tier        string // Wan and AnimateDiff level
	Turbo       bool
	FirstFrame  bool
	// References marks the options made for references (MiniMax H3 with its Ref2VA bundle): they take pictures,
	// videos and audios, and neither a first nor a last picture.
	References bool
	// LowRes is the lower resolution MiniMax-H3 runs at on a card with less memory than its preset needs.
	LowRes    string
	Entry     *catalogEntry
	Available bool
	Reason    string
}

// videoOrder is from best to worst: what the measured shots said (identity, faces, how well it follows the prompt).
var videoOrder = []string{"h3-full", "wan-high", "ltx", "h3-draft", "wan-medium", "wan-low", "ad-high", "ad-medium", "ad-low", "h3-ref", "h3-ref-draft"}

func videoChoices(cat []catalogEntry, m planMachine) []videoChoice {
	by := map[string]videoChoice{}
	put := func(c videoChoice) { by[c.ID] = c }

	// MiniMax-H3: the first-frame bundle is preferred when it is installed.
	ff := miniMaxFirstFrameEntryOf(cat)
	h3 := miniMaxEntryOf(cat)
	if ff != nil && ff.Installed {
		h3 = ff
	}
	for _, id := range []string{"h3-full", "h3-draft"} {
		c := videoChoice{ID: id, Engine: config.EngineMiniMaxH3, Turbo: id == "h3-draft"}
		c.Name, c.Description = "MiniMax H3", "The best quality: a picture or a prompt becomes a shot with its own sound. Slow."
		if c.Turbo {
			c.Name, c.Description = "MiniMax H3 (draft)", "MiniMax H3 with a turbo LoRA: much faster, a little less faithful. Good for trying a prompt."
		}
		switch {
		case h3 == nil || !h3.Installed:
			c.Reason = "MiniMax H3 is not installed"
		case c.Turbo && !hasRole(*h3, "lora"):
			c.Reason = "the MiniMax H3 bundle has no turbo LoRA"
		case !fitsMachine(*h3, m) && !(h3.LowVRAM != nil && fitsMachine(withVRAM(*h3, h3.LowVRAM.VRAMMB), m)):
			c.Reason = needsText(*h3)
		default:
			c.Available, c.Entry, c.FirstFrame = true, h3, h3 == ff
			if !fitsMachine(*h3, m) {
				c.LowRes = h3.LowVRAM.Resolution
			}
		}
		put(c)
	}

	// MiniMax-H3 with its references bundle (Ref2VA), always at the resolution its limits were measured at. They are
	// listed after the rest so the default option does not change.
	ref := miniMaxEntryOf(cat)
	for _, id := range []string{"h3-ref", "h3-ref-draft"} {
		c := videoChoice{ID: id, Engine: config.EngineMiniMaxH3, Turbo: id == "h3-ref-draft", References: true, LowRes: referenceResolution}
		c.Name, c.Description = "MiniMax H3 (references)", "MiniMax H3 guided by reference pictures, videos and audios instead of a first picture: the people and the place of the references appear in the shot."
		if c.Turbo {
			c.Name, c.Description = "MiniMax H3 (references, draft)", "The references option with a turbo LoRA: much faster, a little less faithful."
		}
		switch {
		case ref == nil || !ref.Installed:
			c.Reason = "the MiniMax H3 references bundle is not installed"
		case c.Turbo && !hasRole(*ref, "lora"):
			c.Reason = "the MiniMax H3 references bundle has no turbo LoRA"
		case ref.LowVRAM == nil || !fitsMachine(withVRAM(*ref, ref.LowVRAM.VRAMMB), m):
			need := *ref
			if ref.LowVRAM != nil {
				need = withVRAM(*ref, ref.LowVRAM.VRAMMB)
			}
			c.Reason = needsText(need)
		default:
			c.Available, c.Entry = true, ref
		}
		put(c)
	}

	ltx := ltxEntryOf(cat)
	c := videoChoice{ID: "ltx", Engine: config.EngineLTX, Name: "LTX 2.3", Description: "A fast, good all-rounder that fits a 12 GB card; it can write the sound of the shot, speech included."}
	switch {
	case ltx == nil || !ltx.Installed:
		c.Reason = "LTX 2.3 is not installed"
	case !fitsMachine(*ltx, m):
		c.Reason = needsText(*ltx)
	default:
		c.Available, c.Entry = true, ltx
	}
	put(c)

	for _, tier := range []string{"high", "medium", "low"} {
		c := videoChoice{ID: "wan-" + tier, Engine: config.EngineWan, Tier: tier, Name: "Wan (" + tier + ")"}
		c.Description = map[string]string{
			"high":   "Wan 2.2 A14B: the best of the Wan family, much slower than the others.",
			"medium": "Wan 2.2 5B at 8-bit: a balance between quality and time.",
			"low":    "Wan 2.2 5B at 4-bit: the lightest Wan, for small cards.",
		}[tier]
		var first *catalogEntry
		for i := range cat {
			e := &cat[i]
			if e.Kind != "video" || e.Quality != tier || hasRole(*e, "motion_module") {
				continue
			}
			if first == nil {
				first = e
			}
			if e.Installed && fitsMachine(*e, m) {
				c.Available, c.Entry = true, e
				break
			}
		}
		if !c.Available {
			switch {
			case first == nil:
				c.Reason = "no Wan bundle of this level is in the catalog"
			case !first.Installed:
				c.Reason = first.Name + " is not installed"
			default:
				c.Reason = needsText(*first)
			}
		}
		put(c)
	}

	ad := animateDiffEntryOf(cat)
	for _, tier := range []string{"high", "medium", "low"} {
		c := videoChoice{ID: "ad-" + tier, Engine: config.EngineAnimateDiff, Tier: tier, Name: "AnimateDiff (" + tier + ")",
			Description: "SD 1.5 with a motion module: the lightest way to move a picture, for very small cards; the result is rougher."}
		switch {
		case ad == nil || !ad.Installed:
			c.Reason = "the AnimateDiff motion module is not installed"
		case installedSD15(cat) == "":
			c.Reason = "SD 1.5 (the AnimateDiff base) is not installed"
		case !fitsMachine(*ad, m):
			c.Reason = needsText(*ad)
		default:
			c.Available, c.Entry = true, ad
		}
		put(c)
	}

	out := make([]videoChoice, 0, len(videoOrder))
	for _, id := range videoOrder {
		out = append(out, by[id])
	}
	return out
}

func withVRAM(e catalogEntry, mb int) catalogEntry {
	e.VRAMMB = mb
	return e
}

func findChoice(choices []videoChoice, id string) (videoChoice, bool) {
	for _, c := range choices {
		if c.ID == id {
			return c, true
		}
	}
	return videoChoice{}, false
}

// --- the request -------------------------------------------------------------------------------

// Limits of the duration of a video: the scene lasts that long and the motion loops (design decision 4).
const (
	minGenerationSeconds     = 2.0
	maxGenerationSeconds     = 30.0
	defaultGenerationSeconds = 5.0
)

// videoRequest is a video request once the defaults are filled in.
type videoRequest struct {
	Prompt      string
	Option      string
	Duration    float64
	AspectRatio string
	Quality     string
	Audio       bool
	// Picture is the name of the start picture inside sources/ ("" = none).
	Picture string
	// EndPicture is the last picture of the clip, in sources/ ("" = none); needs Picture.
	EndPicture string
	// RefImages, RefVideos and RefAudios are the references, in sources/: the first picture is the scene's own and
	// takes the place of Picture (a request has one or the other).
	RefImages, RefVideos, RefAudios []string
}

// videoPlan is what a request becomes.
type videoPlan struct {
	Config      *config.Config
	Choice      videoChoice
	ClipSeconds float64
	// Estimate is the seconds the generation takes on this machine.
	Estimate int
}

// clipFPS and clipFrames give the clip an engine makes by itself.
func clipFrames(a *config.Animate) int {
	if a.Frames > 0 {
		return a.Frames
	}
	switch a.Engine {
	case config.EngineAnimateDiff:
		return config.DefaultAnimateDiffFrames
	case config.EngineMiniMaxH3:
		return config.DefaultMiniMaxFrames
	case config.EngineLTX:
		return config.DefaultLTXFrames
	}
	return config.DefaultAnimateFrames
}

func clipFPS(a *config.Animate) int {
	switch a.Engine {
	case config.EngineAnimateDiff:
		return config.DefaultAnimateDiffFPS
	case config.EngineMiniMaxH3:
		return config.MiniMaxFPS
	case config.EngineLTX:
		return config.LTXFPS
	}
	return config.DefaultAnimateFPS
}

// clipSecondsOf is the length of the clip as it is stored: played forward and back (ping-pong), or once when the
// model writes the sound of the shot.
func clipSecondsOf(a *config.Animate) float64 {
	s := float64(clipFrames(a)) / float64(clipFPS(a))
	if !a.ModelAudio {
		s *= 2
	}
	return math.Round(s*10) / 10
}

func fptr(v float64) *float64 { return &v }

// planVideo turns a request into the configuration of a project. voice and the model files come from the
// install; the caller has validated the request against the option's limits.
func planVideo(id string, req videoRequest, ch videoChoice, cat []catalogEntry, m planMachine, voice string) (*videoPlan, error) {
	aw, ah, ok := parseAspect(req.AspectRatio)
	if !ok {
		return nil, fmt.Errorf("aspect ratio %q is not one of %s", req.AspectRatio, strings.Join(aspectRatios, ", "))
	}
	scale, ok := qualityScale[req.Quality]
	if !ok {
		return nil, fmt.Errorf("quality %q is not one of %s", req.Quality, strings.Join(qualityNames, ", "))
	}
	if !ch.Available || ch.Entry == nil {
		return nil, fmt.Errorf("the option %s cannot run now: %s", ch.ID, ch.Reason)
	}
	entry := *ch.Entry
	a := &config.Animate{Engine: ch.Engine}
	multiple := 16
	preset := func() {
		if entry.Preset != nil {
			a.Resolution = entry.Preset.Resolution
			a.Steps = entry.Preset.Steps
			a.CfgScale = entry.Preset.CfgScale
		}
	}
	path := func(role string) string {
		if f := fileOf(entry, role); f != "" {
			return "models/video/" + f
		}
		return ""
	}
	tf := tierFactors[ch.Tier]
	switch ch.Engine {
	case config.EngineWan:
		a.DiffusionModel, a.T5XXL, a.VAE = path("model"), path("text_encoder"), path("vae")
		wanSteps := 20.0
		if hn := path("high_noise_model"); hn != "" {
			a.HighNoiseDiffusionModel = hn
			wanSteps = 10
		}
		a.Resolution = scaleRes("832x480", tf.res, 16)
		a.Frames = tf.frames
		a.Steps = int(math.Max(4, math.Round(wanSteps*tf.steps)))
	case config.EngineAnimateDiff:
		multiple = 8
		lv := animateDiffLevels[ch.Tier]
		a.MotionModule, a.Checkpoint = path("motion_module"), installedSD15(cat)
		a.Resolution, a.Frames, a.Steps = lv.res, lv.frames, lv.steps
		if entry.Preset != nil {
			a.CfgScale = entry.Preset.CfgScale
		}
	case config.EngineMiniMaxH3:
		multiple = config.MiniMaxSideMultiple
		a.DiffusionModel, a.LLM, a.VAE, a.AudioVAE = path("model"), path("text_encoder"), path("vae"), path("audio_vae")
		a.FirstFrame = ch.FirstFrame
		preset()
		if ch.LowRes != "" {
			a.Resolution = ch.LowRes
		}
		if ch.Turbo {
			a.LoRA = path("lora")
			a.Steps = config.DefaultMiniMaxTurboSteps
			if ch.FirstFrame {
				a.Steps = config.DefaultMiniMaxFirstFrameTurboSteps
			}
			if entry.Timing != nil && entry.Timing.TurboSteps > 0 {
				a.Steps = entry.Timing.TurboSteps
			}
		}
	case config.EngineLTX:
		multiple = config.LTXSideMultiple
		a.DiffusionModel, a.LLM, a.VAE, a.AudioVAE, a.Connectors = path("model"), path("text_encoder"), path("vae"), path("audio_vae"), path("connectors")
		preset()
	}
	if req.Audio {
		a.ModelAudio = true
	}
	// The clip has the shape asked for, in about the pixels its level has, scaled by the quality.
	baseRes := a.Resolution
	if baseRes == "" {
		baseRes = config.DefaultAnimateResolution
	}
	a.Resolution = shapeRes(baseRes, aw, ah, scale, multiple)

	cfg := &config.Config{
		Project: id,
		Output:  config.Output{Mode: config.ModeVideo, File: "out/" + id + ".mp4"},
		Defaults: config.Defaults{
			Voice: voice, Sampler: "euler", FPS: 30, Animate: a,
			Resolution: "1280x720", Steps: 30,
		},
	}
	scene := config.Scene{ID: "escena_1", Speed: 1, SilenceSeconds: fptr(req.Duration), Animate: true}
	if len(req.RefImages) > 0 {
		req.Picture = req.RefImages[0]
	}
	picture := req.Picture != ""
	still := stillEntry(cat, m)
	var stillSeconds float64
	switch {
	case picture:
		scene.Image = req.Picture
		scene.EndImage = req.EndPicture
		if len(req.RefImages) > 1 || len(req.RefVideos) > 0 || len(req.RefAudios) > 0 {
			scene.References = &config.SceneReferences{Images: append([]string(nil), req.RefImages[1:]...), Videos: req.RefVideos, Audios: req.RefAudios}
		}
		scene.MotionPrompt = strings.TrimSpace(req.Prompt)
		if scene.MotionPrompt == "" {
			scene.Prompt = "the picture"
		}
	default:
		if still == nil {
			return nil, fmt.Errorf("no image model is installed that fits this machine: the first picture of the video cannot be made")
		}
		scene.Prompt = strings.TrimSpace(req.Prompt)
	}
	if still != nil {
		applyStyleEntryTo(cfg, *still)
		tier := qualityTier[req.Quality]
		pf := tierFactors[tier]
		base := "512x512"
		steps := 20
		if still.Preset != nil {
			if still.Preset.Resolution != "" {
				base = still.Preset.Resolution
			}
			if still.Preset.Steps > 0 {
				steps = still.Preset.Steps
			}
		}
		res := shapeRes(scaleRes(base, pf.res, 64), aw, ah, 1, 64)
		if mp := maxPicturePixels(still, m); mp > 0 {
			res = fitRes(res, mp)
		}
		cfg.Defaults.Resolution = res
		cfg.Defaults.Steps = int(math.Max(4, math.Round(float64(steps)*pf.steps)))
		if !picture {
			stillSeconds = pictureSeconds(still, res, cfg.Defaults.Steps)
		}
	}
	cfg.Scenes = []config.Scene{scene}

	clip := clipSeconds(entry, ch.Turbo, a.Resolution, clipFrames(a), a.Steps, clipSteps(entry, ch.Turbo))
	estimate := int(math.Round((stillSeconds + 4 + clip + 15) * m.Speed))
	clipSecs := clipSecondsOf(a)
	if req.EndPicture != "" {
		// A clip that ends on a picture plays once, forward (see assemble.Scene.AnimatedHold).
		clipSecs = math.Round(float64(clipFrames(a))/float64(clipFPS(a))*10) / 10
	}
	return &videoPlan{Config: cfg, Choice: ch, ClipSeconds: clipSecs, Estimate: estimate}, nil
}

// applyStyleEntryTo writes the model of a catalog entry (a model set when it has a text encoder and a VAE, a single
// checkpoint otherwise) and its recommended settings.
func applyStyleEntryTo(cfg *config.Config, e catalogEntry) {
	model, llm, vae := fileOf(e, "model"), fileOf(e, "text_encoder"), fileOf(e, "vae")
	if llm != "" && vae != "" {
		cfg.Defaults.Model = ""
		cfg.Defaults.ModelSet = &config.ModelSet{DiffusionModel: "models/" + model, LLM: "models/" + llm, VAE: "models/" + vae}
		if e.Preset != nil && e.Preset.CfgScale != nil {
			cfg.Defaults.ModelSet.CfgScale = e.Preset.CfgScale
		}
	} else {
		cfg.Defaults.Model = "models/" + model
		cfg.Defaults.ModelSet = nil
	}
	if e.Preset != nil {
		if e.Preset.Steps > 0 {
			cfg.Defaults.Steps = e.Preset.Steps
		}
		if e.Preset.Resolution != "" {
			cfg.Defaults.Resolution = e.Preset.Resolution
		}
	}
}

// --- time --------------------------------------------------------------------------------------

// pictureSeconds is the seconds one picture takes: the entry's time scaled by pixels and steps.
func pictureSeconds(e *catalogEntry, res string, steps int) float64 {
	if e == nil || e.Timing == nil || resPixels(e.Timing.Resolution) == 0 || e.Timing.Steps == 0 {
		return 20 * (float64(resPixels(res)) / float64(resPixels("1024x576"))) * (float64(steps) / 8)
	}
	t := e.Timing
	return float64(t.Seconds) * float64(resPixels(res)) / float64(resPixels(t.Resolution)) * float64(steps) / float64(t.Steps)
}

func clipSteps(e catalogEntry, turbo bool) int {
	if e.Timing != nil && turbo && e.Timing.TurboSteps > 0 {
		return e.Timing.TurboSteps
	}
	if e.Timing != nil {
		return e.Timing.Steps
	}
	return 0
}

// clipSeconds is the seconds one clip takes: the entry's time scaled by pixels, frames and steps.
func clipSeconds(e catalogEntry, turbo bool, res string, frames, steps, baseSteps int) float64 {
	t := e.Timing
	if t == nil || t.Frames == 0 || resPixels(t.Resolution) == 0 || t.Steps == 0 || baseSteps == 0 {
		return 85
	}
	base := float64(t.Seconds)
	if turbo && t.TurboSeconds > 0 {
		base = float64(t.TurboSeconds)
	}
	return base * float64(resPixels(res)) / float64(resPixels(t.Resolution)) * float64(frames) / float64(t.Frames) * float64(steps) / float64(baseSteps)
}

// --- the options ------------------------------------------------------------------------------

type durationRange struct {
	Min     float64 `json:"min"`
	Max     float64 `json:"max"`
	Default float64 `json:"default"`
}

// videoOptionView is one way of making a video, as the options list shows it.
type videoOptionView struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Engine      string        `json:"engine"`
	Available   bool          `json:"available"`
	Reason      string        `json:"reason,omitempty"`
	ClipSeconds float64       `json:"clip_seconds"`
	Duration    durationRange `json:"duration"`
	Audio       bool          `json:"audio"`
	// Estimate is the seconds a generation takes on this machine at the best quality, 16:9 and with no start
	// picture; it does not grow with the duration (the clip loops).
	Estimate int  `json:"estimate,omitempty"`
	Default  bool `json:"default"`
	// Inputs is what the option takes besides the prompt, and its limits.
	Inputs *generationInputs `json:"inputs,omitempty"`
}

// imageModelView is one model a picture can be edited with.
type imageModelView struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	Kind           string `json:"kind"` // img2img | instruction
	Available      bool   `json:"available"`
	Reason         string `json:"reason,omitempty"`
	Strength       bool   `json:"strength"`
	NegativePrompt bool   `json:"negative_prompt"`
	MaxVariants    int    `json:"max_variants"`
	Default        bool   `json:"default"`
}

// generationOptions is the answer of GET /api/generation/options.
type generationOptions struct {
	Video        []videoOptionView `json:"video"`
	ImageModels  []imageModelView  `json:"image_models"`
	AspectRatios []string          `json:"aspect_ratios"`
	Qualities    []string          `json:"qualities"`
}

// Image edits: what each kind of model takes (design decision 8 and the open question about variants).
const (
	img2imgMaxVariants     = 8
	instructionMaxVariants = 1
	editKindImg2img        = "img2img"
	editKindInstruction    = "instruction"
)

func buildVideoOptions(cat []catalogEntry, m planMachine, voice string) []videoOptionView {
	choices := videoChoices(cat, m)
	out := make([]videoOptionView, 0, len(choices))
	defaulted := false
	for _, ch := range choices {
		v := videoOptionView{ID: ch.ID, Name: ch.Name, Description: ch.Description, Engine: ch.Engine, Available: ch.Available, Reason: ch.Reason,
			Duration: durationRange{Min: minGenerationSeconds, Max: maxGenerationSeconds, Default: defaultGenerationSeconds},
			Audio:    ch.Engine == config.EngineMiniMaxH3 || ch.Engine == config.EngineLTX}
		v.Inputs = inputsOf(ch, m)
		if ch.Available {
			if p, err := planVideo("estimate", videoRequest{Option: ch.ID, Duration: defaultGenerationSeconds, AspectRatio: "16:9", Quality: "high"}, ch, cat, m, voice); err == nil {
				v.ClipSeconds, v.Estimate = p.ClipSeconds, p.Estimate
			} else {
				// A choice that cannot be planned (no image model for its first picture) cannot run.
				v.Available, v.Reason = false, err.Error()
			}
		}
		if v.Available && !defaulted {
			v.Default, defaulted = true, true
		}
		out = append(out, v)
	}
	return out
}

func buildImageModels(cat []catalogEntry, m planMachine) []imageModelView {
	var out []imageModelView
	add := func(e catalogEntry, kind string) {
		v := imageModelView{ID: e.ID, Name: e.Name, Description: e.Description, Kind: kind}
		switch kind {
		case editKindInstruction:
			v.MaxVariants = instructionMaxVariants
		default:
			v.Strength, v.NegativePrompt, v.MaxVariants = true, true, img2imgMaxVariants
		}
		switch {
		case !e.Installed:
			v.Reason = e.Name + " is not installed"
		case !fitsMachine(e, m):
			v.Reason = needsText(e)
		default:
			v.Available = true
		}
		out = append(out, v)
	}
	// Editing models first, the fast one (FLUX.1 Kontext) before the precise one (Qwen-Image-Edit).
	var edits []catalogEntry
	for _, e := range cat {
		if e.Kind == "model" && e.Edit {
			edits = append(edits, e)
		}
	}
	sort.SliceStable(edits, func(i, j int) bool { return !hasRole(edits[i], "llm_vision") && hasRole(edits[j], "llm_vision") })
	for _, e := range edits {
		add(e, editKindInstruction)
	}
	for _, e := range cat {
		if e.Kind == "model" && !e.Edit && len(e.Styles) > 0 {
			add(e, editKindImg2img)
		}
	}
	for i := range out {
		if out[i].Available {
			out[i].Default = true
			break
		}
	}
	return out
}

// machineNow reads the machine now: its cards and its memory.
func (h *Handler) machineNow() planMachine {
	if h.GenMachine != nil {
		return h.GenMachine()
	}
	gpus, _ := h.ws.QueryGPUs()
	var ram int64
	if total, _, err := sched.MemInfo(); err == nil {
		ram = total
	}
	return MachineOf(gpus, ram)
}

// catalogNow is the catalog with what is installed now.
func (h *Handler) catalogNow() []catalogEntry {
	if h.GenCatalog != nil {
		return h.models.WithoutHiddenAdult(h.GenCatalog())
	}
	return h.models.WithoutHiddenAdult(h.models.InstalledCatalog())
}

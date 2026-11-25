// Package render invokes the stable-diffusion.cpp CLI (bin/sd, built by
// scripts/build-sd.sh) to turn a scene's prompt into a still PNG image.
// Looping that image into a timed video clip is the clip phase's job (see
// internal/video/assemble), not this package's.
package render

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
	"veritmo/internal/video/sdlog"
)

// Flags centralizes every sd CLI flag in one place so they are easy to
// correct later (see design.md).
var Flags = struct {
	Model          string
	DiffusionModel string
	LLM            string
	VAE            string
	CfgScale       string
	Prompt         string
	NegativePrompt string
	Seed           string
	SamplingMethod string
	Steps          string
	Width          string
	Height         string
	Output         string
	InitImage      string
	Strength       string
	ClipL          string
	T5XXL          string
	RefImage       string
	DiffusionFA    string
	LLMVision      string
	FlowShift      string
	Mmap           string
	OffloadToCPU   string
	VAETiling      string
}{
	Model:          "-m",
	DiffusionModel: "--diffusion-model",
	LLM:            "--llm",
	VAE:            "--vae",
	CfgScale:       "--cfg-scale",
	Prompt:         "-p",
	NegativePrompt: "-n",
	Seed:           "-s",
	SamplingMethod: "--sampling-method",
	Steps:          "--steps",
	Width:          "-W",
	Height:         "-H",
	Output:         "-o",
	InitImage:      "-i",
	Strength:       "--strength",
	ClipL:          "--clip_l",
	T5XXL:          "--t5xxl",
	RefImage:       "-r",
	DiffusionFA:    "--diffusion-fa",
	LLMVision:      "--llm_vision",
	FlowShift:      "--flow-shift",
	Mmap:           "--mmap",
	OffloadToCPU:   "--offload-to-cpu",
	VAETiling:      "--vae-tiling",
}

// Binary is the stable-diffusion.cpp executable name, installed at bin/sd
// by scripts/build-sd.sh.
const Binary = "sd"

// UnsetSeed is the value sd.cpp treats as "pick a random seed".
const UnsetSeed int64 = -1

// ParseResolution splits a "WIDTHxHEIGHT" string (e.g. "1280x720") into its
// two integer components.
func ParseResolution(resolution string) (width, height int, err error) {
	parts := strings.SplitN(resolution, "x", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("render: resolution %q must be WIDTHxHEIGHT", resolution)
	}
	width, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("render: resolution %q: invalid width: %w", resolution, err)
	}
	height, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("render: resolution %q: invalid height: %w", resolution, err)
	}
	return width, height, nil
}

// Model selects what sd loads: a single checkpoint (-m) or, when Set is
// non-nil, a diffusion model with its LLM and VAE (Set wins over Checkpoint).
type Model struct {
	Checkpoint string
	Set        *config.ModelSet
	// CfgScale, when set, is the cfg scale to pass (a scene's own, or the one
	// resolved for it); it wins over the model set's. Nil keeps the model set's,
	// and with a single checkpoint no --cfg-scale is passed (sd uses its own).
	CfgScale *float64
}

// ModelOf returns the model the defaults select.
func ModelOf(d config.Defaults) Model {
	return Model{Checkpoint: d.Model, Set: d.ModelSet}
}

func (m Model) args() []string {
	cfg := m.CfgScale
	if cfg == nil && m.Set != nil {
		cfg = m.Set.CfgScale
	}
	var args []string
	switch {
	case m.Set == nil:
		args = []string{Flags.Model, m.Checkpoint}
	case m.Set.QwenEdit():
		// An editing model of the Qwen family (Qwen-Image-Edit): its LLM with the vision projector, no CLIP-L or
		// T5-XXL. The weights are read from disk and offloaded to the CPU and the VAE runs in tiles: measured on a
		// 12 GB card with 30 GB of RAM, that is what lets 19 GB of weights fit.
		args = []string{
			Flags.DiffusionModel, m.Set.DiffusionModel,
			Flags.VAE, m.Set.VAE,
			Flags.LLM, m.Set.LLM,
			Flags.LLMVision, m.Set.LLMVision,
			Flags.FlowShift, "3",
			Flags.Mmap, Flags.OffloadToCPU, Flags.DiffusionFA, Flags.VAETiling,
		}
	case m.Set.Edit:
		// An editing model (FLUX.1 Kontext): its own text encoders, no LLM; flash attention keeps it inside 12 GB.
		args = []string{
			Flags.DiffusionModel, m.Set.DiffusionModel,
			Flags.VAE, m.Set.VAE,
			Flags.ClipL, m.Set.ClipL,
			Flags.T5XXL, m.Set.T5XXL,
			Flags.DiffusionFA,
		}
	default:
		args = []string{
			Flags.DiffusionModel, m.Set.DiffusionModel,
			Flags.LLM, m.Set.LLM,
			Flags.VAE, m.Set.VAE,
		}
	}
	if cfg != nil {
		args = append(args, Flags.CfgScale, strconv.FormatFloat(*cfg, 'g', -1, 64))
	}
	return args
}

// BuildArgs constructs the sd CLI argument slice that renders prompt (and
// optional negativePrompt) with the given model/sampler/steps/seed into a
// PNG at outPath.
func BuildArgs(model Model, prompt, negativePrompt string, seed int64, resolution, sampler string, steps int, outPath string) ([]string, error) {
	width, height, err := ParseResolution(resolution)
	if err != nil {
		return nil, err
	}

	args := append(model.args(), Flags.Prompt, prompt)
	if negativePrompt != "" {
		args = append(args, Flags.NegativePrompt, negativePrompt)
	}
	args = append(args,
		Flags.Seed, strconv.FormatInt(seed, 10),
		Flags.SamplingMethod, sampler,
		Flags.Steps, strconv.Itoa(steps),
		Flags.Width, strconv.Itoa(width),
		Flags.Height, strconv.Itoa(height),
		Flags.Output, outPath,
	)
	return args, nil
}

// ComposePositive returns the effective positive prompt: the global subject
// prompt prepended to the scene prompt, or the scene prompt alone when the
// subject prompt is unset.
func ComposePositive(subjectPrompt, scenePrompt string) string {
	if subjectPrompt == "" {
		return scenePrompt
	}
	return subjectPrompt + " " + scenePrompt
}

// ComposeNegative returns the effective negative prompt: the scene-level
// negative prompt followed by the global contribution, either alone when the
// other is unset, or "" when both are unset.
func ComposeNegative(sceneNegative, globalNegative string) string {
	switch {
	case sceneNegative == "":
		return globalNegative
	case globalNegative == "":
		return sceneNegative
	default:
		return sceneNegative + " " + globalNegative
	}
}

// VariantResult records what one sd invocation actually received, so the
// export phase can write a manifest entry that reproduces the image.
type VariantResult struct {
	VariantIndex   int
	EffectiveSeed  int64
	Prompt         string
	NegativePrompt string
	OutPath        string
	// Resolution is the size the picture was made at: the one asked for, or a smaller one when the program ran
	// out of video memory and was run again (see IsOutOfMemory).
	Resolution string
	// LoadMs is the milliseconds sd said it spent loading model weights, over every attempt (0: it did not say).
	LoadMs int
}

// MaxMemoryRetries is how many times a picture that ran out of video memory is made again, each time smaller.
const MaxMemoryRetries = 2

// memoryTraces are what the diffusion program says when the card cannot hold the picture.
var memoryTraces = []string{"out of memory", "failed during weight preparation", "cudamalloc", "cuda error: out of memory", "failed to allocate"}

// IsOutOfMemory says whether an error of the diffusion program is the card running out of memory: the program's own
// messages ("failed during weight preparation", "out of memory"...), which the runner puts in the error.
func IsOutOfMemory(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, t := range memoryTraces {
		if strings.Contains(msg, t) {
			return true
		}
	}
	return false
}

// minRetryPixels is the smallest picture worth retrying: below it the picture would be too poor to be of any use.
const minRetryPixels = 512 * 512

// ShrinkResolution is the next smaller size to try: about three quarters of the pixels, the same shape, in
// multiples of 64. It returns the same size when it cannot go smaller without being too poor.
func ShrinkResolution(res string) string {
	w, h, err := ParseResolution(res)
	if err != nil {
		return res
	}
	factor := math.Sqrt(0.75)
	nw := int(math.Round(float64(w)*factor/64)) * 64
	nh := int(math.Round(float64(h)*factor/64)) * 64
	if nw >= w && nh >= h {
		nw, nh = w-64, h // rounding kept the size: take a step
	}
	if nw*nh < minRetryPixels || nw < 64 || nh < 64 || nw*nh >= w*h {
		return res
	}
	return fmt.Sprintf("%dx%d", nw, nh)
}

// VariantSeeds resolves the effective seed for each of n variants: an
// explicit scene seed yields base+i (deterministic), while an unset seed
// draws one random 32-bit value per variant from rng. The orchestrator (not
// sd's internal -1 draw) owns random seeds so they can be recorded in the
// manifest and hashed into the cache key.
func VariantSeeds(scene config.Scene, n int, rng func() int64) []int64 {
	seeds := make([]int64, n)
	base := scene.ResolvedSeed()
	for i := range seeds {
		if base == UnsetSeed {
			seeds[i] = rng()
		} else {
			seeds[i] = base + int64(i)
		}
	}
	return seeds
}

// RunVariant invokes sd once with an explicit effective seed and the
// composed prompts, writing the PNG to exactly outPath. It returns the
// metadata the manifest needs for that variant.
func RunVariant(ctx context.Context, r runner.CommandRunner, scene config.Scene, defaults config.Defaults, variantIndex int, effectiveSeed int64, outPath string, env []string) (VariantResult, error) {
	prompt := ComposePositive(defaults.SubjectPrompt, scene.Prompt)
	negative := ComposeNegative(scene.NegativePrompt, defaults.NegativePrompt)

	// The scene's own resolution, steps and cfg scale win over the project's.
	eff := scene.EffectiveRender(defaults)
	model := ModelOf(defaults)
	model.CfgScale = eff.CfgScale

	var opts []runner.Option
	if len(env) > 0 {
		opts = append(opts, runner.WithEnv(env...))
	}
	loadMs := 0 // what sd says it spent loading models, over every attempt

	// A picture that does not fit the card is made again a little smaller instead of failing the run; an
	// img2img one keeps the size it was fitted to.
	resolution := eff.Resolution
	for attempt := 0; ; attempt++ {
		args, err := BuildArgs(model, prompt, negative, effectiveSeed, resolution, defaults.Sampler, eff.Steps, outPath)
		if err != nil {
			return VariantResult{}, err
		}
		if scene.InitImage != "" {
			// The picture was fitted to the resolution by the pipeline beforehand. An editing model takes it as
			// the reference to edit (the prompt is the instruction); any other starts a generation from it.
			if model.Set != nil && model.Set.Edit {
				args = append(args, Flags.RefImage, cache.InitImagePath(scene.ID))
			} else {
				args = append(args, Flags.InitImage, cache.InitImagePath(scene.ID),
					Flags.Strength, strconv.FormatFloat(scene.ResolvedStrength(), 'g', -1, 64))
			}
		}
		var out []byte
		out, err = r.Run(ctx, Binary, args, opts...)
		if ms, ok := sdlog.LoadMillis(out); ok {
			loadMs += ms
		}
		if err == nil {
			break
		}
		next := ShrinkResolution(resolution)
		if scene.InitImage != "" || !IsOutOfMemory(err) || attempt >= MaxMemoryRetries || next == resolution || ctx.Err() != nil {
			return VariantResult{}, fmt.Errorf("render: scene %q: %w", scene.ID, err)
		}
		resolution = next
	}
	return VariantResult{
		VariantIndex:   variantIndex,
		EffectiveSeed:  effectiveSeed,
		Prompt:         prompt,
		NegativePrompt: negative,
		OutPath:        outPath,
		Resolution:     resolution,
		LoadMs:         loadMs,
	}, nil
}

// RenderVariants invokes sd once per variant, strictly sequentially (never
// in parallel, to bound VRAM on the render GPU). seeds and outPaths must
// have one entry per variant; seeds[i] is the effective seed for variant i.
func RenderVariants(ctx context.Context, r runner.CommandRunner, scene config.Scene, defaults config.Defaults, seeds []int64, outPaths []string, env []string) ([]VariantResult, error) {
	if len(seeds) != len(outPaths) {
		return nil, fmt.Errorf("render: scene %q: %d seeds for %d output paths", scene.ID, len(seeds), len(outPaths))
	}
	results := make([]VariantResult, 0, len(seeds))
	for i := range seeds {
		res, err := RunVariant(ctx, r, scene, defaults, i, seeds[i], outPaths[i], env)
		if err != nil {
			return results, err
		}
		results = append(results, res)
	}
	return results, nil
}

// Run invokes sd via r to render scene's prompt (using defaults for model,
// resolution, sampler and step count) into a PNG at outPath. env carries
// per-invocation overrides such as CUDA_VISIBLE_DEVICES, letting the caller
// pin generation to a specific GPU.
func Run(ctx context.Context, r runner.CommandRunner, scene config.Scene, defaults config.Defaults, outPath string, env []string) error {
	_, err := RunVariant(ctx, r, scene, defaults, 0, scene.ResolvedSeed(), outPath, env)
	return err
}

// ShrunkNote says that a picture was made smaller than asked because the card ran out of memory; "" when it was not.
func ShrunkNote(asked string, res VariantResult) string {
	if res.Resolution == "" || res.Resolution == asked {
		return ""
	}
	return fmt.Sprintf("the card ran out of memory at %s: the picture was made again at %s (lower the resolution of the project to avoid the first attempt)", asked, res.Resolution)
}

// EditResolution is the size an edit is made at: the shape of the picture being edited in about the pixels of the
// resolution asked for (the memory the card was sized for), in multiples of 16 and no side under 256 or over 2048.
func EditResolution(pictureW, pictureH int, asked string) (string, error) {
	aw, ah, err := ParseResolution(asked)
	if err != nil {
		return "", err
	}
	if pictureW <= 0 || pictureH <= 0 {
		return asked, nil
	}
	ratio := float64(pictureW) / float64(pictureH)
	budget := float64(aw * ah)
	snap := func(v float64) int {
		n := int(math.Round(v/16)) * 16
		return min(2048, max(256, n))
	}
	h := snap(math.Sqrt(budget / ratio))
	w := snap(float64(h) * ratio)
	return fmt.Sprintf("%dx%d", w, h), nil
}

// Package animate turns a scene's rendered still into a short clip by driving
// sd in video mode (Wan 2.2, or AnimateDiff on SD 1.5), then converts sd's AVI
// into a forward-and-back MP4 so the clip phase can loop it without a visible
// jump.
package animate

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
	"veritmo/internal/video/render"
	"veritmo/internal/video/sdlog"
)

// SDMode is sd's video-generation run mode.
const SDMode = "vid_gen"

// FFmpegBinary converts sd's AVI into the ping-pong MP4.
const FFmpegBinary = "ffmpeg"

// Flags are the sd flags specific to video generation; the shared ones
// (prompt, seed, sampler, output, size) come from the render package.
var Flags = struct {
	Mode, DiffusionModel, T5XXL, VAE, InitImg, Frames, FPS, Steps, CfgScale string
	// Two-expert (Wan A14B) flags, added only when a high-noise expert is set.
	HighNoiseModel, HighNoiseSteps, HighNoiseCfgScale, HighNoiseSampler, FlowShift string
	// AnimateDiff flags: the SD 1.5 checkpoint, the motion module and the
	// scheduler SD 1.5 needs (sd's own default produces noise-like output).
	Checkpoint, MotionModule, Scheduler string
	// MiniMax-H3 flags: the audio VAE, the Qwen3-VL text encoder, the
	// reference image and the CPU random generator sd documents for it.
	AudioVAE, LLM, RefImage, RNG, LoRADir string
	// EndImg is the last picture (MiniMax-H3 first/last frame, LTX-2.3); RefVideo and RefAudio are references.
	EndImg, RefVideo, RefAudio string
	// LTX-2.3 flag: the embeddings-connectors file.
	Connectors string
}{
	Mode:           "-M",
	DiffusionModel: "--diffusion-model",
	T5XXL:          "--t5xxl",
	VAE:            "--vae",
	InitImg:        "-i",
	Frames:         "--video-frames",
	FPS:            "--fps",
	Steps:          "--steps",
	CfgScale:       "--cfg-scale",

	HighNoiseModel:    "--high-noise-diffusion-model",
	HighNoiseSteps:    "--high-noise-steps",
	HighNoiseCfgScale: "--high-noise-cfg-scale",
	HighNoiseSampler:  "--high-noise-sampling-method",
	FlowShift:         "--flow-shift",

	Checkpoint:   "--model",
	MotionModule: "--motion-module",
	Scheduler:    "--scheduler",

	AudioVAE: "--audio-vae",
	LLM:      "--llm",
	RefImage: "-r",
	EndImg:   "--end-img",
	RefVideo: "--ref-video",
	RefAudio: "--ref-audio",
	RNG:      "--rng",
	LoRADir:  "--lora-model-dir",

	Connectors: "--embeddings-connectors",
}

// Memory-saving flags measured to work on a 12-16 GB GPU with Wan 5B.
var memoryFlags = []string{"--offload-to-cpu", "--vae-tiling", "--diffusion-fa"}

// Prompt returns the video prompt: the scene's motion_prompt when set,
// otherwise its composed positive prompt.
func Prompt(scene config.Scene, defaults config.Defaults) string {
	if scene.MotionPrompt != "" {
		return scene.MotionPrompt
	}
	return render.ComposePositive(defaults.SubjectPrompt, scene.Prompt)
}

// BuildSDArgs returns the sd arguments that generate the clip from image
// into rawPath. rawPath must end in .avi: sd only writes .avi, .webm or
// .webp for video and appends .avi to any other extension.
func BuildSDArgs(a config.Animate, prompt string, seed int64, image, rawPath string) ([]string, error) {
	return BuildSDArgsWith(a, prompt, seed, image, rawPath, Inputs{})
}

// BuildSDArgsWith is BuildSDArgs with the last picture and the references of the scene (MiniMax H3 and LTX only: the
// other engines take none).
func BuildSDArgsWith(a config.Animate, prompt string, seed int64, image, rawPath string, in Inputs) ([]string, error) {
	a = a.Resolved()
	w, h, err := render.ParseResolution(a.Resolution)
	if err != nil {
		return nil, err
	}
	if a.IsAnimateDiff() {
		return buildAnimateDiffArgs(a, w, h, prompt, seed, image, rawPath), nil
	}
	if a.IsMiniMaxH3() {
		return buildMiniMaxArgs(a, w, h, prompt, seed, image, rawPath, in), nil
	}
	if a.IsLTX() {
		return buildLTXArgs(a, w, h, prompt, seed, image, rawPath, in), nil
	}
	args := []string{
		Flags.Mode, SDMode,
		Flags.DiffusionModel, a.DiffusionModel,
		Flags.VAE, a.VAE,
		Flags.T5XXL, a.T5XXL,
		Flags.InitImg, image,
		render.Flags.Prompt, prompt,
		render.Flags.Seed, strconv.FormatInt(seed, 10),
		render.Flags.SamplingMethod, "euler",
		render.Flags.Width, strconv.Itoa(w),
		render.Flags.Height, strconv.Itoa(h),
		Flags.Frames, strconv.Itoa(a.Frames),
		Flags.FPS, strconv.Itoa(a.FPS),
		Flags.Steps, strconv.Itoa(a.Steps),
		Flags.CfgScale, strconv.FormatFloat(*a.CfgScale, 'g', -1, 64),
	}
	if a.TwoExpert() {
		// DiffusionModel is the low-noise expert; the high-noise one has its
		// own steps and cfg. Without it none of this is added.
		args = append(args,
			Flags.HighNoiseModel, a.HighNoiseDiffusionModel,
			Flags.HighNoiseSteps, strconv.Itoa(a.HighNoiseSteps),
			Flags.HighNoiseCfgScale, strconv.FormatFloat(*a.HighNoiseCfgScale, 'g', -1, 64),
			Flags.HighNoiseSampler, "euler",
			Flags.FlowShift, config.AnimateTwoExpertFlowShift,
		)
	}
	args = append(args, memoryFlags...)
	return append(args, render.Flags.Output, rawPath), nil
}

// MiniMaxPromptPrefix tells the Ref2VA model that the still is the subject to
// keep; the reference image is addressed as <Picture 1> in the prompt.
const MiniMaxPromptPrefix = "Use the image <Picture 1> as the reference: keep its subject, appearance and style unchanged. "

// buildMiniMaxArgs is the sd command of the MiniMax-H3 engine (Ref2VA: the
// still is a reference image, not an init image). It follows sd.cpp's
// docs/minimax_h3.md: cfg 1, flash attention, offload to CPU and the CPU
// random generator. The model also writes an audio track into the AVI, which
// the ping-pong conversion drops. --mmap is what makes the Q4 weights (~35 GB)
// fit a 30 GB machine: sd maps the files instead of copying them to RAM
// (measured 2026-10-13: 864x480, 56 frames, 258 s, 13.5 GB of VRAM, RAM
// available never under 28 GB; without it Q4 is killed at 24 GB).
func buildMiniMaxArgs(a config.Animate, w, h int, prompt string, seed int64, image, rawPath string, in Inputs) []string {
	// Ref2VA: the still is a reference the prompt must name. First frame (FL2VA): the still is the clip's first
	// frame (--init-img) and the prompt only says what moves.
	imageFlag := Flags.RefImage
	var extra []string // the last picture (first frame) or the other references, after the scene's picture
	if a.FirstFrame {
		imageFlag = Flags.InitImg
		if in.EndImage != "" {
			extra = []string{Flags.EndImg, in.EndImage}
		}
	} else {
		prompt = miniMaxReferencesPrompt(in.Pictures()) + prompt
		extra = refArgs(in)
	}
	var lora []string
	if a.LoRA != "" {
		// sd takes a LoRA as a tag in the prompt, found in --lora-model-dir by file name.
		name := strings.TrimSuffix(filepath.Base(a.LoRA), filepath.Ext(a.LoRA))
		prompt += " <lora:" + name + ":1>"
		lora = []string{Flags.LoRADir, filepath.Dir(a.LoRA)}
	}
	return append([]string{
		Flags.Mode, SDMode,
		Flags.DiffusionModel, a.DiffusionModel,
		Flags.VAE, a.VAE,
		Flags.AudioVAE, a.AudioVAE,
		Flags.LLM, a.LLM,
		imageFlag, image,
	}, append(extra, append([]string{
		render.Flags.Prompt, prompt,
		render.Flags.Seed, strconv.FormatInt(seed, 10),
		render.Flags.Width, strconv.Itoa(w),
		render.Flags.Height, strconv.Itoa(h),
		Flags.Frames, strconv.Itoa(a.Frames),
		Flags.FPS, strconv.Itoa(a.FPS),
		Flags.Steps, strconv.Itoa(a.Steps),
		Flags.CfgScale, strconv.FormatFloat(*a.CfgScale, 'g', -1, 64),
		"--diffusion-fa", "--offload-to-cpu", "--mmap", Flags.RNG, "cpu",
	}, append(lora, render.Flags.Output, rawPath)...)...)...)
}

// buildLTXArgs is the sd command of the LTX-2.3 engine (distilled model): the still is the clip's first frame
// (--init-img) and the prompt only says what moves. It is the command measured on a 12 GB card (768x448, 49
// frames, 118 s, 11.35 GB of VRAM): euler, the CPU offload and --mmap, which maps the ~25 GB of weights instead
// of copying them to RAM. The model also writes an audio track into the AVI, which the ping-pong conversion
// drops (it is kept only with model_audio, see BuildAmbientArgs).
func buildLTXArgs(a config.Animate, w, h int, prompt string, seed int64, image, rawPath string, in Inputs) []string {
	var end []string // the last picture, when the clip ends on one
	if in.EndImage != "" {
		end = []string{Flags.EndImg, in.EndImage}
	}
	return append(append([]string{
		Flags.Mode, SDMode,
		Flags.DiffusionModel, a.DiffusionModel,
		Flags.VAE, a.VAE,
		Flags.AudioVAE, a.AudioVAE,
		Flags.LLM, a.LLM,
		Flags.Connectors, a.Connectors,
		Flags.InitImg, image,
	}, end...), []string{
		render.Flags.Prompt, prompt,
		render.Flags.Seed, strconv.FormatInt(seed, 10),
		render.Flags.SamplingMethod, "euler",
		render.Flags.Width, strconv.Itoa(w),
		render.Flags.Height, strconv.Itoa(h),
		Flags.Frames, strconv.Itoa(a.Frames),
		Flags.FPS, strconv.Itoa(a.FPS),
		Flags.Steps, strconv.Itoa(a.Steps),
		Flags.CfgScale, strconv.FormatFloat(*a.CfgScale, 'g', -1, 64),
		"--offload-to-cpu", "--mmap",
		render.Flags.Output, rawPath,
	}...)
}

// buildAnimateDiffArgs is the sd command of the AnimateDiff engine, as
// measured: euler with the karras scheduler, the init image at --strength,
// the memory flags sd documents for it and no Wan options. image must already be at the animation resolution (see
// BuildFitArgs): sd would stretch any other size.
func buildAnimateDiffArgs(a config.Animate, w, h int, prompt string, seed int64, image, rawPath string) []string {
	return []string{
		Flags.Mode, SDMode,
		Flags.Checkpoint, a.Checkpoint,
		Flags.MotionModule, a.MotionModule,
		Flags.InitImg, image,
		render.Flags.Strength, strconv.FormatFloat(*a.Strength, 'g', -1, 64),
		render.Flags.Prompt, prompt,
		render.Flags.Seed, strconv.FormatInt(seed, 10),
		render.Flags.SamplingMethod, "euler",
		Flags.Scheduler, "karras",
		render.Flags.Width, strconv.Itoa(w),
		render.Flags.Height, strconv.Itoa(h),
		Flags.Frames, strconv.Itoa(a.Frames),
		Flags.FPS, strconv.Itoa(a.FPS),
		Flags.Steps, strconv.Itoa(a.Steps),
		Flags.CfgScale, strconv.FormatFloat(*a.CfgScale, 'g', -1, 64),
		// Without these it runs out of memory on the 12 GB GPU (peak 11.3 GB);
		// with them it peaks at 5.6 GB there and is no slower.
		"--offload-to-cpu", "--diffusion-fa",
		render.Flags.Output, rawPath,
	}
}

// FittedPath is where the cover-cropped copy of the still goes, next to the
// scene's raw sd output (tmp/anim_<scene>.avi -> tmp/anim_<scene>_init.png).
func FittedPath(rawPath string) string {
	return strings.TrimSuffix(rawPath, ".avi") + "_init.png"
}

// BuildFitArgs returns the ffmpeg arguments that scale image to cover w x h
// and centre-crop it to exactly that size, so sd animates the picture without
// stretching it. The still itself is not modified.
func BuildFitArgs(image, outPath string, w, h int) []string {
	return []string{
		"-y", "-i", image,
		"-vf", fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d", w, h, w, h),
		"-frames:v", "1",
		outPath,
	}
}

// BuildPingPongArgs returns the ffmpeg arguments that play rawPath forward
// and then in reverse into an MP4. The reversed half drops its first and
// last frames (they equal the forward half's last and first), so looping the
// result repeats no frame: 33 frames become 33+31 = 64. frames is the number
// of frames sd generated.
func BuildPingPongArgs(rawPath, outPath string, frames int) []string {
	graph := fmt.Sprintf("[0:v]split[a][b];"+
		"[b]reverse,select='gt(n,0)*lt(n,%d)',setpts=N/FR/TB[r];"+
		"[a][r]concat=n=2:v=1:a=0,format=yuv420p[v]", frames-1)
	return []string{
		"-y", "-i", rawPath,
		"-filter_complex", graph,
		"-map", "[v]",
		"-c:v", "libx264", "-crf", "12", "-preset", "fast",
		outPath,
	}
}

// AmbientFadeSeconds is the fade out at the end of the model's audio.
const AmbientFadeSeconds = 0.3

// AmbientPath is where the model's audio goes: tmp/anim_<scene>.avi -> tmp/anim_<scene>.ambient.wav.
func AmbientPath(rawPath string) string {
	return strings.TrimSuffix(rawPath, ".avi") + ".ambient.wav"
}

// BuildAmbientArgs returns the ffmpeg arguments that write the audio of rawPath as a stereo 44100 Hz WAV: only
// the forward part (frames / fps seconds, never the reversed half of the ping-pong) with a short fade out.
func BuildAmbientArgs(rawPath, outPath string, frames, fps int) []string {
	length := float64(frames) / float64(fps)
	return []string{
		"-y", "-i", rawPath,
		"-vn",
		"-t", strconv.FormatFloat(length, 'f', 3, 64),
		"-af", fmt.Sprintf("afade=t=out:st=%s:d=%s", strconv.FormatFloat(length-AmbientFadeSeconds, 'f', 3, 64), strconv.FormatFloat(AmbientFadeSeconds, 'f', 3, 64)),
		"-ar", "44100", "-ac", "2",
		outPath,
	}
}

// Params identifies one animation.
type Params struct {
	Scene    config.Scene
	Defaults config.Defaults
	Seed     int64
	Image    string // rendered PNG
	RawPath  string // sd's .avi output
	OutPath  string // ping-pong MP4
	// Dir is the project folder, for what animate creates on disk itself (the runner already runs in it); empty is
	// the working directory.
	Dir string
}

// Run generates the clip with sd and converts it. env carries per-run
// overrides such as CUDA_VISIBLE_DEVICES. Failures name the scene and keep
// the captured stderr; there is no fallback to the still image. loadMs is the milliseconds sd said it spent loading
// model weights (0 when it did not say), also when the run fails after sd ran.
func Run(ctx context.Context, r runner.CommandRunner, p Params, env []string) (loadMs int, err error) {
	var opts []runner.Option
	if len(env) > 0 {
		opts = append(opts, runner.WithEnv(env...))
	}
	// The scene's own resolution, frames and steps win over the project's.
	a := p.Defaults.Animate.ForScene(p.Scene)
	image := p.Image
	if a.IsAnimateDiff() || a.IsMiniMaxH3() || a.IsLTX() {
		// They take the still at the animation resolution.
		w, h, err := render.ParseResolution(a.Resolution)
		if err != nil {
			return 0, fmt.Errorf("animate: scene %q: %w", p.Scene.ID, err)
		}
		image = FittedPath(p.RawPath)
		if _, err := r.Run(ctx, FFmpegBinary, BuildFitArgs(p.Image, image, w, h)); err != nil {
			return 0, fmt.Errorf("animate: scene %q: fitting the picture: %w", p.Scene.ID, err)
		}
	}
	in, err := PrepareInputs(ctx, r, p.Scene, a.Resolved(), p.Dir, p.RawPath)
	if err != nil {
		return 0, fmt.Errorf("animate: scene %q: %w", p.Scene.ID, err)
	}
	args, err := BuildSDArgsWith(a, Prompt(p.Scene, p.Defaults), p.Seed, image, p.RawPath, in)
	if err != nil {
		return 0, fmt.Errorf("animate: scene %q: %w", p.Scene.ID, err)
	}
	out, err := r.Run(ctx, render.Binary, args, opts...)
	if ms, ok := sdlog.LoadMillis(out); ok {
		loadMs = ms
	}
	if err != nil {
		return loadMs, fmt.Errorf("animate: scene %q: sd: %w", p.Scene.ID, err)
	}
	// A clip that ends on a given picture plays once, forward: the ping-pong would end on the first one.
	convert := BuildPingPongArgs(p.RawPath, p.OutPath, a.Frames)
	if in.EndImage != "" {
		convert = BuildForwardArgs(p.RawPath, p.OutPath)
	}
	if _, err := r.Run(ctx, FFmpegBinary, convert); err != nil {
		return loadMs, fmt.Errorf("animate: scene %q: ffmpeg: %w", p.Scene.ID, err)
	}
	if a.ModelAudio && (a.IsMiniMaxH3() || a.IsLTX()) {
		if _, err := r.Run(ctx, FFmpegBinary, BuildAmbientArgs(p.RawPath, AmbientPath(p.RawPath), a.Frames, a.FPS)); err != nil {
			return loadMs, fmt.Errorf("animate: scene %q: the model's audio: %w", p.Scene.ID, err)
		}
	}
	return loadMs, nil
}

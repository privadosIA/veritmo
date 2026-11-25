// Package pipeline is the shared entry point that drives a full
// scenes.yaml run: Piper (TTS) -> ffprobe (duration) -> sd
// (stable-diffusion.cpp image render) -> ffmpeg (per-scene clip + final
// concat), or render -> export in images mode.
//
// It is the wiring formerly private to cmd/veritmo, extracted so the
// HTTP server can run the pipeline in-process against a project directory
// (Options.Dir) while the CLI keeps its exact pre-extraction behavior by
// passing an empty Dir (process cwd). Fatal conditions emit the same
// `error` event as before and are returned as errors instead of calling
// os.Exit, so a long-lived caller survives a failed run.
package pipeline

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"veritmo/internal/platform/media"

	"veritmo/internal/music/music"
	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/platform/probe"
	"veritmo/internal/platform/runner"
	"veritmo/internal/platform/sched"
	"veritmo/internal/project/config"
	"veritmo/internal/project/export"
	"veritmo/internal/speech/tts"
	"veritmo/internal/video/animate"
	"veritmo/internal/video/assemble"
	"veritmo/internal/video/motion"
	"veritmo/internal/video/render"
	"veritmo/internal/video/videoedit"
)

// Options configures one pipeline run. The zero value reproduces a bare
// `orchestrator` invocation from the current working directory.
type Options struct {
	// Dir is the project directory every relative path (tmp/, out/,
	// model/voice paths, output.file) resolves against. Empty means the
	// process cwd, which is the CLI's behavior.
	Dir string
	// KeepTmp preserves tmp/ (and its cache manifest) after the run.
	KeepTmp bool
	// GPU is the index exported as CUDA_VISIBLE_DEVICES for the sd phases;
	// empty leaves the environment untouched. A comma-separated list ("0,1")
	// runs one scene at a time on each listed GPU (see ParseGPUs).
	GPU string
	// VRAMWarnMB is the advisory free-VRAM threshold checked when GPU is
	// set.
	VRAMWarnMB int
	// FreeVRAMMB reports the free memory of one GPU for that check; nil asks nvidia-smi. A field so a test
	// answers without it.
	FreeVRAMMB func(gpuID string) (int, error)
	// VideoEdit is the catalog of the video-edit tools a video-edit project runs with; nil builds the one of this build with
	// the real programs and no service (the command line).
	VideoEdit *videoedit.Catalog
	// MotionParts is the catalog of parts the scenes made from a part are built from (see MotionParts); nil has none.
	MotionParts MotionParts
	// MusicServices says where the .NET services of the music generators are: the same host the server shows in
	// Environment, so a run finds the service that is running there. Nil has none.
	MusicServices *musicsvc.ServiceHost
	// Models3D is the 3D service and the library a models3d-mode run makes its models with; nil fails such a run, saying so.
	Models3D *Models3DServices
	// Force bypasses the cache for every phase of every scene.
	Force bool
	// Only restricts (re)processing to a single scene by ID.
	Only string
	// Runner executes subprocesses; nil uses the production exec runner.
	Runner runner.CommandRunner
	// MemAvailable reads the memory available for the RAM budget of a
	// multi-GPU run; nil reads /proc/meminfo.
	MemAvailable sched.AvailableFunc
	// Rand draws effective seeds for scenes without an explicit seed; nil
	// uses RandomSeed.
	Rand func() int64
	// NoContainerStart: a music generator that runs in a container is never started by the run; a stopped one fails
	// with the reason not_running. The server sets it (the user starts the containers); the CLI leaves it off.
	NoContainerStart bool
	// Overlap, when above 0, lets a video run with one GPU or none do the narration and probe of up to that many scenes
	// ahead, and the clip of a drawn scene, while the card works on another scene. 0 (the default) is the sequential
	// pipeline. It is ignored (with a warning) with several GPUs and in a project with motion graphics or overlays.
	Overlap int
}

// ApplyModeOverride applies the --mode flag onto the config before Validate
// runs (precedence: flag > YAML). An empty mode leaves the YAML value
// untouched.
func ApplyModeOverride(cfg *config.Config, mode config.OutputMode) error {
	if mode == "" {
		return nil
	}
	if mode != config.ModeVideo && mode != config.ModeImages && mode != config.ModeMusic && mode != config.ModeVideoEdit && mode != config.ModeModels3D {
		return fmt.Errorf("--mode must be %q, %q, %q, %q or %q, got %q", config.ModeVideo, config.ModeImages, config.ModeMusic, config.ModeVideoEdit, config.ModeModels3D, mode)
	}
	cfg.Output.Mode = mode
	return nil
}

// PhasePlan returns the per-scene phase sequence for a mode and whether the
// global concat phase runs after all scenes. In video mode an "animate" phase
// additionally runs between render and clip for scenes with animate: true.
func PhasePlan(mode config.OutputMode) (scenePhases []string, concat bool) {
	if mode == config.ModeImages {
		return []string{"render", "export"}, false
	}
	if mode == config.ModeModels3D {
		// the picture (an entry with its own image has none) and the model
		return []string{"render", "model3d"}, false
	}
	if mode == config.ModeVideoEdit {
		return []string{"edit"}, false
	}
	if mode == config.ModeMusic {
		// compose is skipped for tracks that bring their own lyrics.
		return []string{"compose", "synth", "export"}, false
	}
	return []string{"tts", "probe", "render", "clip"}, true
}

// RequiredBinaries lists the external binaries the pre-flight check must
// find for the selected mode. Images mode never needs piper or ffprobe, and
// needs ffmpeg only for the optional contact sheet.
func RequiredBinaries(mode config.OutputMode, contactSheet bool) []string {
	if mode == config.ModeMusic {
		return []string{music.BinarySynth}
	}
	if mode == config.ModeVideoEdit {
		return []string{export.FFmpegBinary, "ffprobe"}
	}
	if mode == config.ModeModels3D {
		return []string{render.Binary} // RequiredBinariesFor leaves it out when every entry brings its own picture
	}
	if mode == config.ModeImages {
		bins := []string{render.Binary}
		if contactSheet {
			bins = append(bins, export.FFmpegBinary)
		}
		return bins
	}
	return []string{tts.Binary, render.Binary, "ffmpeg", "ffprobe"}
}

// usesInitImage reports whether any scene starts from a picture.
func usesInitImage(cfg *config.Config) bool {
	for _, s := range cfg.Scenes {
		if s.InitImage != "" {
			return true
		}
	}
	return false
}

// needsRender reports whether any scene is generated by the diffusion
// program; when every scene brings its own picture it is not needed.
func needsRender(cfg *config.Config) bool {
	for _, s := range cfg.Scenes {
		if s.Image == "" && s.Clip == "" && !s.IsMotionGraphic() && !s.IsBlender() {
			return true
		}
	}
	return len(cfg.Scenes) == 0
}

// modelsNeedPictures reports whether a models3d run draws any picture: an entry with a prompt that the run will make.
func modelsNeedPictures(cfg *config.Config, only string) bool {
	for _, e := range cfg.Models {
		if e.Image == "" && (only == "" || e.ID == only) {
			return true
		}
	}
	return false
}

// RequiredBinariesFor is RequiredBinaries for a loaded config: in music mode
// it adds ace-lm only when a track that will run uses the language model
// (all tracks, or just the one selected with --only).
func RequiredBinariesFor(cfg *config.Config, only string) []string {
	bins := RequiredBinaries(cfg.Output.Mode, cfg.Output.ContactSheet)
	if cfg.Output.Mode == config.ModeModels3D && !modelsNeedPictures(cfg, only) {
		return nil
	}
	if cfg.Output.Mode == config.ModeVideo && !needsRender(cfg) {
		bins = slices.DeleteFunc(bins, func(b string) bool { return b == render.Binary })
	}
	if cfg.Output.Mode == config.ModeImages && usesInitImage(cfg) && !slices.Contains(bins, "ffmpeg") {
		bins = append(bins, "ffmpeg") // the starting picture is fitted with ffmpeg
	}
	server := cfg.Defaults.Music.ResolvedEngine() == config.EngineServer
	if cfg.Output.Mode == config.ModeMusic {
		return musicBinaries(cfg, only, bins, server)
	}
	if cfg.Output.Mode == config.ModeVideo && cfg.BGM.Generated() {
		if server {
			bins = append(bins, music.BinaryServer)
		} else {
			bins = append(bins, music.BinarySynth)
		}
	}
	return bins
}

// IsMixID reports whether id names a mix of the project (not a track).
func IsMixID(cfg *config.Config, id string) bool {
	if id == "" {
		return false
	}
	for _, m := range cfg.Mixes {
		if m.ID == id {
			return true
		}
	}
	return false
}

// musicBinaries lists what a music run needs: the engine program(s) unless
// the run is restricted to one mix, and ffmpeg and ffprobe when mixes are
// going to be rendered (all of them, or the selected one).
func musicBinaries(cfg *config.Config, only string, bins []string, server bool) []string {
	mixOnly := IsMixID(cfg, only)
	var out []string
	switch {
	case mixOnly:
	case server:
		out = []string{music.BinaryServer} // one program replaces ace-lm and ace-synth
	default:
		out = bins
		for _, t := range cfg.Tracks {
			if (only == "" || t.ID == only) && t.EffectiveUseLM() {
				out = append(slices.Clone(bins), music.BinaryLM)
				break
			}
		}
	}
	if len(cfg.Mixes) > 0 && (only == "" || mixOnly) {
		out = append(out, export.FFmpegBinary, "ffprobe")
	}
	return out
}

// PreflightFor verifies the binaries RequiredBinariesFor lists.
func PreflightFor(cfg *config.Config, only string, look func(string) (string, error)) error {
	for _, bin := range RequiredBinariesFor(cfg, only) {
		if _, err := look(bin); err != nil {
			return fmt.Errorf("required binary %q not found on PATH (output mode %q): %w", bin, cfg.Output.Mode, err)
		}
	}
	return nil
}

// Preflight verifies every required binary is present via look
// (exec.LookPath in production) before any scene is processed.
func Preflight(mode config.OutputMode, contactSheet bool, look func(string) (string, error)) error {
	for _, bin := range RequiredBinaries(mode, contactSheet) {
		if _, err := look(bin); err != nil {
			return fmt.Errorf("required binary %q not found on PATH (output mode %q): %w", bin, mode, err)
		}
	}
	return nil
}

// CheckOnlyScene verifies that a non-empty --only scene ID exists in the
// config.
func CheckOnlyScene(cfg *config.Config, onlyID string) error {
	if onlyID == "" {
		return nil
	}
	if cfg.Output.Mode == config.ModeMusic {
		for _, t := range cfg.Tracks {
			if t.ID == onlyID {
				return nil
			}
		}
		if IsMixID(cfg, onlyID) {
			return nil
		}
		if len(cfg.Mixes) > 0 {
			return fmt.Errorf("track or mix %q not found in config", onlyID)
		}
		return fmt.Errorf("track %q not found in config", onlyID)
	}
	if cfg.Output.Mode == config.ModeModels3D {
		for _, e := range cfg.Models {
			if e.ID == onlyID {
				return nil
			}
		}
		return fmt.Errorf("model %q not found in config", onlyID)
	}
	for _, s := range cfg.Scenes {
		if s.ID == onlyID {
			return nil
		}
	}
	return fmt.Errorf("scene %q not found in config", onlyID)
}

// RandomSeed draws one 32-bit effective seed for a variant of a scene that
// declares no explicit seed (the orchestrator, not sd, owns random seeds so
// they can be recorded in the manifest and hashed into the cache key).
func RandomSeed() int64 {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // the system has no randomness: nothing sensible to do
	}
	return int64(binary.BigEndian.Uint32(b[:]))
}

// Run executes the pipeline for an already loaded and validated config. It
// emits the full event stream on emitter (starting with pipeline_start and
// ending with pipeline_end on success), returns the final artifact path, and
// on a fatal condition emits the `error` event and returns a non-nil error
// without cleaning tmp/ — exactly the pre-extraction CLI semantics, where
// the process exited immediately after the error event.
func Run(ctx context.Context, cfg *config.Config, opts Options, emitter *events.Emitter) (string, error) {
	base := opts.Runner
	if base == nil {
		base = runner.NewExecRunner()
	}
	applyMotionSong(cfg)
	execRunner := base
	if opts.Dir != "" {
		execRunner = dirRunner{r: base, dir: opts.Dir}
	}
	rng := opts.Rand
	if rng == nil {
		rng = RandomSeed
	}

	gpus, err := ParseGPUs(opts.GPU)
	if err != nil {
		return "", fail(emitter, "", err.Error())
	}

	emitter.PipelineStart()

	// --- GPU / VRAM check ---
	var renderEnv []string
	if len(gpus) == 1 {
		renderEnv = append(renderEnv, "CUDA_VISIBLE_DEVICES="+gpus[0])
	}
	for _, gpu := range gpus {
		if err := checkVRAM(opts.FreeVRAMMB, gpu, opts.VRAMWarnMB, emitter); err != nil {
			emitter.Warn("_global", fmt.Sprintf("VRAM check failed on GPU %s: %v", gpu, err))
		}
	}

	// --- Working directories ---
	tmpDir := filepath.Join("tmp")
	if err := os.MkdirAll(joinDir(opts.Dir, tmpDir), 0o755); err != nil {
		return "", fail(emitter, "", fmt.Sprintf("cannot create tmp dir: %v", err))
	}
	if cfg.Output.Mode == config.ModeVideo {
		if dir := filepath.Dir(cfg.Output.File); dir != "." && dir != "" {
			if err := os.MkdirAll(joinDir(opts.Dir, dir), 0o755); err != nil {
				return "", fail(emitter, "", fmt.Sprintf("cannot create output dir: %v", err))
			}
		}
	}

	synthRunner := ttsRunnerAdapter{r: execRunner}
	ffmpegRunner := assembleRunnerAdapter{r: execRunner}

	// --- Cache manifest (persists across runs only when keep-tmp is used) ---
	manifest, err := cache.LoadManifest(joinDir(opts.Dir, cache.ManifestPath()))
	if err != nil {
		return "", fail(emitter, "", fmt.Sprintf("cannot load cache manifest: %v", err))
	}

	onlyFilter := cache.NewOnlyFilter(opts.Only)

	// Scene-level parallelism (several GPUs) shares a RAM budget between the
	// concurrent sd processes; with 0 or 1 GPU there is nothing to share.
	var budget *sched.Budget
	if len(gpus) > 1 {
		avail := opts.MemAvailable
		if avail == nil {
			avail = sched.MemAvailable
		}
		budget = sched.NewBudgetFromAvailable(avail)
	}

	// --- Per-scene pipeline (phase selection by output.mode) ---
	var finalArtifact string
	switch cfg.Output.Mode {
	case config.ModeVideoEdit:
		finalArtifact, err = runVideoEdit(ctx, cfg, opts.VideoEdit, emitter, opts.Dir, gpus)
	case config.ModeMusic:
		finalArtifact, err = runMusic(ctx, cfg, emitter, execRunner, manifest, onlyFilter, opts.Force, renderEnv, rng, opts.Dir, gpus, budget, opts.NoContainerStart, opts.MusicServices)
	case config.ModeModels3D:
		if len(gpus) > 1 {
			renderEnv = append(renderEnv, "CUDA_VISIBLE_DEVICES="+gpus[0]) // one picture at a time: the first card
		}
		finalArtifact, err = runModels3D(ctx, cfg, emitter, execRunner, manifest, onlyFilter, opts.Force, renderEnv, opts.Dir, opts.Models3D)
	case config.ModeImages:
		finalArtifact, err = runImages(ctx, cfg, emitter, execRunner, manifest, onlyFilter, opts.Force, renderEnv, rng, opts.Dir, gpus, budget)
	default:
		finalArtifact, err = runVideoOverlap(ctx, cfg, emitter, execRunner, synthRunner, ffmpegRunner, manifest, onlyFilter, opts.Force, renderEnv, tmpDir, opts.Dir, gpus, budget, opts.Overlap, opts.MotionParts)
	}
	if err != nil {
		return "", err
	}

	// --- Cleanup ---
	if !opts.KeepTmp {
		if err := media.RemoveTree(joinDir(opts.Dir, tmpDir)); err != nil {
			emitter.Warn("_global", fmt.Sprintf("cleanup failed: %v", err))
		}
	}

	emitter.Done(finalArtifact)
	return finalArtifact, nil
}

// runVideo drives the video pipeline (tts → probe → render → clip per
// scene, then the global concat) and returns the final video path. Its
// event stream and subprocess invocations are byte-for-byte identical to
// the pre-extraction orchestrator (see the regression test). base is the
// project directory for Go-side file access; subprocess arguments and event
// artifacts stay project-relative.
func runVideo(ctx context.Context, cfg *config.Config, emitter *events.Emitter, execRunner runner.CommandRunner, synthRunner ttsRunnerAdapter, ffmpegRunner assembleRunnerAdapter, manifest *cache.Manifest, onlyFilter cache.OnlyFilter, force bool, renderEnv []string, tmpDir, base string, gpus []string, budget *sched.Budget) (string, error) {
	return runVideoOverlap(ctx, cfg, emitter, execRunner, synthRunner, ffmpegRunner, manifest, onlyFilter, force, renderEnv, tmpDir, base, gpus, budget, 0, nil)
}

// runVideoOverlap is runVideo with the overlap option: with overlap > 0, one GPU or none and nothing in the project that
// needs another program next to the card (motion graphics and overlays), the narration and probe of up to `overlap`
// scenes ahead and the clip of the scenes already drawn run while the card works on another scene (see runStaged).
// With 0 it is the sequential pipeline.
func runVideoOverlap(ctx context.Context, cfg *config.Config, emitter *events.Emitter, execRunner runner.CommandRunner, synthRunner ttsRunnerAdapter, ffmpegRunner assembleRunnerAdapter, manifest *cache.Manifest, onlyFilter cache.OnlyFilter, force bool, renderEnv []string, tmpDir, base string, gpus []string, budget *sched.Budget, overlap int, parts MotionParts) (string, error) {
	width, height, err := render.ParseResolution(cfg.Defaults.Resolution)
	if err != nil {
		return "", fail(emitter, "", fmt.Sprintf("render failed: %v", err))
	}

	st := &runState{emitter: emitter, manifest: manifest, budget: budget}
	store := newArtifactStore(base)
	mo := &motionRun{parts: parts}
	renderBytes := sched.Estimate(renderFootprint(cfg, base))
	animateBytes := sched.Estimate(animateFootprint(cfg, base))
	clipPathBy := make([]string, len(cfg.Scenes))
	clipLenBy := make([]float64, len(cfg.Scenes))

	// A scene goes through three stages: prepare (the hashes, the cache decision, tts and probe: CPU), gpu (render and
	// animate: the card) and finish (the clip, ffmpeg on the CPU, and the manifest). One after another they are the
	// sequential pipeline; with the overlap option the CPU stages of other scenes run while the card works.
	prepare := func(ctx context.Context, idx int, scene config.Scene) (*videoJob, error) {
		audioHash := cache.AudioHash(cache.AudioParams{
			Text:    scene.TTSText,
			Speed:   scene.Speed,
			Voice:   cfg.VoiceFor(scene),
			Speaker: cfg.SpeakerFor(scene),
		})
		// The pages of a motion graphic or an overlay are written first (the model is asked only for
		// the ones the project does not have yet): their content is part of the cache keys.
		if _, err := mo.ensureMotionPages(ctx, cfg, scene, base, width, height, emitter); err != nil {
			return nil, st.fail(scene.ID, fmt.Sprintf("motion failed: %v", err))
		}
		var imageHash string
		var err error
		if scene.IsMotionGraphic() {
			imageHash = MotionImageHash(cfg, scene, base, audioHash)
		} else if scene.IsBlender() {
			imageHash = BlenderImageHash(cfg, scene, base, audioHash)
		} else if imageHash, err = VideoImageHash(cfg, scene, base); err != nil {
			return nil, fail(emitter, scene.ID, fmt.Sprintf("render failed: %v", err))
		}
		clipHash := SceneClipHash(cfg, scene, base)
		// The size this scene's picture is drawn (or fitted) at; the video keeps the project's size.
		renderW, renderH, err := sceneRenderSize(cfg, scene)
		if err != nil {
			return nil, fail(emitter, scene.ID, fmt.Sprintf("render failed: %v", err))
		}
		animated := isAnimated(cfg, scene)
		var animateHash string
		var decision cache.Decision
		st.mu.Lock()
		if animated {
			animateHash = cache.AnimateHash(SceneAnimateParams(cfg, scene, base))
			decision = cache.DecideAnimated(manifest, onlyFilter, scene.ID, audioHash, imageHash, clipHash, animateHash, force)
		} else {
			decision = cache.DecideWithFilter(manifest, onlyFilter, scene.ID, audioHash, imageHash, clipHash, force)
		}
		st.mu.Unlock()
		if BlendMissing(base, scene) {
			// A built scene whose kept .blend is gone is rendered again, and its clip made again with it.
			decision.Phases[cache.PhaseRender], decision.Phases[cache.PhaseClip] = false, false
		}
		wavPath, pngPath, mp4Path := cache.ArtifactPaths(scene.ID)
		animRawPath, animPath := cache.AnimatePaths(scene.ID)

		// Phases: TTS + Probe. A silent scene skips both entirely — no
		// subprocess and no events (phases that do not run are silent in
		// the stream) — and takes its duration from silence_seconds.
		var durationSeconds float64
		if scene.IsSilent() {
			durationSeconds = scene.SilenceDuration()
		} else {
			// Phase: TTS
			if decision.Phases[cache.PhaseTTS] {
				emitter.Skipped(scene.ID, "tts")
				keepInStore(store, emitter, scene.ID, audioHash, ".wav", joinDir(base, wavPath)) // a run before the store had it
			} else if !force && store.Get(audioHash, ".wav", joinDir(base, wavPath)) {
				// The same narration (text, voice, speed) was already made, by this scene before or by another one.
				emitter.Skipped(scene.ID, "tts")
			} else {
				emitter.PhaseStart(scene.ID, "tts")
				// Synthesize creates the output file itself (a Go-side
				// write), so it gets the project-resolved path; the event
				// artifact and manifest keep the project-relative one.
				if err := tts.SynthesizeSpeaker(ctx, synthRunner, scene.TTSText, scene.Speed, cfg.VoiceFor(scene), cfg.SpeakerFor(scene), joinDir(base, wavPath)); err != nil {
					return nil, st.fail(scene.ID, fmt.Sprintf("tts failed: %v", err))
				}
				emitter.PhaseEnd(scene.ID, "tts", wavPath)
				keepInStore(store, emitter, scene.ID, audioHash, ".wav", joinDir(base, wavPath))
			}

			// Phase: Probe (always run; cheap, and clip generation always needs duration)
			emitter.PhaseStart(scene.ID, "probe")
			duration, err := probe.Duration(ctx, execRunner, wavPath)
			if err != nil {
				return nil, st.fail(scene.ID, fmt.Sprintf("probe failed: %v", err))
			}
			emitter.PhaseEnd(scene.ID, "probe", "")
			durationSeconds = duration.Seconds()
		}

		return &videoJob{idx: idx, scene: scene, audioHash: audioHash, imageHash: imageHash, clipHash: clipHash, animateHash: animateHash,
			renderW: renderW, renderH: renderH, animated: animated, decision: decision, durationSeconds: durationSeconds,
			wavPath: wavPath, pngPath: pngPath, mp4Path: mp4Path, animRawPath: animRawPath, animPath: animPath}, nil
	}
	gpuStage := func(ctx context.Context, job *videoJob, renderEnv []string) error {
		scene, decision, animated := job.scene, job.decision, job.animated
		renderW, renderH, durationSeconds := job.renderW, job.renderH, job.durationSeconds
		pngPath, animRawPath, animPath := job.pngPath, job.animRawPath, job.animPath
		// Only a picture the diffusion program draws is worth keeping by its hash: a given picture, a clip or a motion
		// graphic is a cheap fit, not a render.
		drawn := scene.Image == "" && scene.Clip == "" && !scene.IsMotionGraphic() && !scene.IsBlender()
		// Phase: Render
		if decision.Phases[cache.PhaseRender] {
			emitter.Skipped(scene.ID, "render")
			if drawn {
				keepInStore(store, emitter, scene.ID, job.imageHash, ".png", joinDir(base, pngPath)) // a run before the store had it
			}
		} else if drawn && !force && store.Get(job.imageHash, ".png", joinDir(base, pngPath)) {
			// The same picture (prompt, seed, model, size...) was already drawn, by this scene before or by another one.
			emitter.Skipped(scene.ID, "render")
		} else {
			need := renderBytes
			if scene.Image != "" || scene.Clip != "" || scene.IsMotionGraphic() {
				need = 0 // no model is loaded for a given picture or clip, or a motion graphic
			}
			if scene.IsBlender() {
				need = sched.Estimate(blenderBytes(base, scene)) // no model either, but Cycles needs memory of its own
			}
			release, err := st.reserve(ctx, need)
			if err != nil {
				return err
			}
			emitter.PhaseStart(scene.ID, "render")
			renderLoadMs := 0 // what sd says it spent loading models in this phase (0: it did not say)
			if scene.IsMotionGraphic() {
				// A motion graphic: the page is played to a video as long as the scene; its first
				// frame is the scene picture.
				err = renderMotionScene(ctx, mo, execRunner, base, scene, width, height, cfg.Defaults.FPS, durationSeconds, pngPath)
			} else if scene.IsBlender() {
				// A Blender scene: rendered on the card this stage holds (or the CPU) to a video as long as the scene.
				err = renderBlenderScene(ctx, execRunner, emitter, cfg, base, scene, width, height, cfg.Defaults.FPS, durationSeconds, renderEnv, pngPath)
			} else if scene.Clip != "" {
				// The scene's own clip: its first frame is the scene picture.
				err = fitSceneClipFrame(ctx, execRunner, scene, renderW, renderH, pngPath)
			} else if scene.Image != "" {
				// The scene's own picture: no diffusion, just fit it to the resolution.
				err = fitSceneImage(ctx, execRunner, scene, renderW, renderH, pngPath)
			} else {
				if scene.InitImage != "" {
					err = fitInitImage(ctx, execRunner, scene, renderW, renderH)
				}
				if err == nil {
					var res render.VariantResult
					res, err = render.RunVariant(ctx, execRunner, scene, cfg.Defaults, 0, scene.ResolvedSeed(), pngPath, renderEnv)
					renderLoadMs = res.LoadMs
					if err == nil {
						if note := render.ShrunkNote(scene.EffectiveRender(cfg.Defaults).Resolution, res); note != "" {
							emitter.Warn(scene.ID, note)
						}
					}
				}
			}
			release()
			if err != nil {
				return st.fail(scene.ID, fmt.Sprintf("render failed: %v", err))
			}
			emitter.PhaseEndLoad(scene.ID, "render", pngPath, renderLoadMs)
			if drawn {
				keepInStore(store, emitter, scene.ID, job.imageHash, ".png", joinDir(base, pngPath))
			}
		}

		// Phase: Animate (animated scenes only): Wan turns the rendered image
		// into a short ping-pong clip. A failure stops the run: there is no
		// silent fallback to the still image.
		if animated {
			if decision.Phases[cache.PhaseAnimate] {
				emitter.Skipped(scene.ID, "animate")
			} else {
				release, err := st.reserve(ctx, animateBytes)
				if err != nil {
					return err
				}
				emitter.PhaseStart(scene.ID, "animate")
				var animateLoadMs int
				animateLoadMs, err = animate.Run(ctx, execRunner, animate.Params{
					Scene:    scene,
					Defaults: cfg.Defaults,
					Seed:     scene.ResolvedSeed(),
					Image:    pngPath,
					RawPath:  animRawPath,
					OutPath:  animPath,
					Dir:      base,
				}, renderEnv)
				release()
				if err != nil {
					return st.fail(scene.ID, fmt.Sprintf("animate failed: %v", err))
				}
				emitter.PhaseEndLoad(scene.ID, "animate", animPath, animateLoadMs)
			}
		}

		if animated {
			if err := keepAnimation(base, scene.ID, animPath); err != nil {
				emitter.Warn(scene.ID, fmt.Sprintf("cannot keep the animation: %v", err))
			}
		}
		return nil
	}
	finishStage := func(ctx context.Context, job *videoJob) error {
		scene, idx, decision, animated := job.scene, job.idx, job.decision, job.animated
		audioHash, imageHash, clipHash, animateHash := job.audioHash, job.imageHash, job.clipHash, job.animateHash
		renderW, renderH, durationSeconds := job.renderW, job.renderH, job.durationSeconds
		wavPath, pngPath, mp4Path, animRawPath, animPath := job.wavPath, job.pngPath, job.mp4Path, job.animRawPath, job.animPath
		// Phase: Clip (mux rendered image + synthesized audio into a timed clip)
		if decision.Phases[cache.PhaseClip] {
			emitter.Skipped(scene.ID, "clip")
		} else {
			emitter.PhaseStart(scene.ID, "clip")
			clipScene := assemble.Scene{
				ID:              scene.ID,
				ImagePath:       pngPath,
				AudioPath:       wavPath,
				Duration:        durationSeconds,
				MotionType:      scene.MotionType(),
				MotionIntensity: scene.MotionIntensity(),
				Grain:           scene.LookGrain(),
				WhiteBalance:    scene.LookWhiteBalance(),
				Seed:            scene.ResolvedSeed(),
				Silent:          scene.IsSilent(),
				Width:           width,
				Height:          height,
				SourceWidth:     renderW,
				SourceHeight:    renderH,
			}
			if animated {
				clipScene.AnimatedPath = animPath
				clipScene.AnimatedDuration = animatedDuration(cfg.Defaults.Animate, scene)
				clipScene.AnimatedHold = scene.EndImage != ""
				if cfg.Defaults.Animate.ModelAudio && scene.Clip == "" && !scene.IsMotionGraphic() && !scene.ClipTrimmed() {
					clipScene.AmbientPath = animate.AmbientPath(animRawPath)
					clipScene.AmbientSpeech = cfg.Defaults.Animate.ModelAudioSpeech
				}
			}
			if scene.Clip != "" {
				own := filepath.Join(config.SourcesSubdir, scene.Clip)
				length, err := probe.Duration(ctx, execRunner, own)
				if err != nil {
					return st.fail(scene.ID, fmt.Sprintf("probe failed: cannot read the clip %s: %v", scene.Clip, err))
				}
				clipScene.AnimatedPath = own
				clipScene.AnimatedDuration = length.Seconds()
			}
			if scene.IsMotionGraphic() {
				// The page was drawn as long as the scene: it plays at its own speed.
				clipScene.AnimatedPath = MotionPath(scene.ID)
				clipScene.AnimatedDuration = durationSeconds
			}
			if scene.IsBlender() {
				// The render was made as long as the scene: it plays at its own speed.
				clipScene.AnimatedPath = BlenderPath(scene.ID)
				clipScene.AnimatedDuration = durationSeconds
			}
			if clipScene.AnimatedPath != "" {
				clipScene.AnimatedSpeed = clipSpeedKey(scene)
				if scene.IsMotionGraphic() || scene.IsBlender() {
					clipScene.AnimatedSpeed = 1
				}
			}
			if o := scene.Overlay; o != nil {
				start, end := overlayWindow(*o, durationSeconds)
				if start >= end-0.05 {
					emitter.Warn(scene.ID, fmt.Sprintf("the overlay starts at %.2f s but the scene lasts %.2f s: it is not drawn", start, durationSeconds))
				} else {
					ext, err := motion.OverlayExt()
					if err != nil {
						return st.fail(scene.ID, fmt.Sprintf("clip failed: %v", err))
					}
					ovPath := OverlayVideoPath(scene.ID, ext)
					if err := mo.renderPage(ctx, base, scene.ID, true, width, height, cfg.Defaults.FPS, end-start, ovPath); err != nil {
						return st.fail(scene.ID, fmt.Sprintf("clip failed: cannot draw the overlay: %v", err))
					}
					clipScene.OverlayPath, clipScene.OverlayStart, clipScene.OverlayEnd = ovPath, start, end
				}
			}
			if scene.ClipTrimmed() && clipScene.AnimatedPath != "" {
				// Only part of the video: an own clip counts from the start of the file; an animation,
				// from the start of its forward half (the file is the ping-pong, twice that long).
				source, total, label := clipScene.AnimatedPath, clipScene.AnimatedDuration, scene.Clip
				if scene.Clip == "" {
					total, label = total/2, "the animation"
				}
				start, end := scene.ResolvedClipStart(), total
				if scene.ClipEnd != nil {
					end = *scene.ClipEnd
				}
				if start >= total || end > total+0.05 {
					return st.fail(scene.ID, fmt.Sprintf("clip failed: the part %.2f-%.2f s is beyond the end of %s, which lasts %.2f s", start, end, label, total))
				}
				cut := filepath.Join("tmp", fmt.Sprintf("scene_%s.cut.mp4", scene.ID))
				if _, err := execRunner.Run(ctx, "ffmpeg", assemble.BuildCutArgs(source, start, end-start, cut)); err != nil {
					return st.fail(scene.ID, fmt.Sprintf("clip failed: cannot cut the part of %s: %v", label, err))
				}
				clipScene.AnimatedPath = cut
				clipScene.AnimatedDuration = end - start
			}
			if stderr, err := assemble.GenerateClip(ctx, ffmpegRunner, clipScene, cfg.Defaults.FPS, mp4Path); err != nil {
				return st.fail(scene.ID, fmt.Sprintf("clip failed: %v (stderr: %s)", err, stderr))
			}
			emitter.PhaseEnd(scene.ID, "clip", mp4Path)
		}
		if err := keepClip(base, scene.ID, mp4Path); err != nil {
			emitter.Warn(scene.ID, fmt.Sprintf("cannot keep the clip: %v", err))
		}

		entry := cache.SceneEntry{
			AudioHash: audioHash,
			ImageHash: imageHash,
			ClipHash:  clipHash,
			WavPath:   wavPath,
			PngPath:   pngPath,
			Mp4Path:   mp4Path,
		}
		if animated {
			entry.AnimateHash = animateHash
			entry.AnimPath = animPath
		}
		// A scene --only leaves out is assumed to have its artifacts from an earlier run, so its
		// entry stays as that run left it: recording the current hashes would make a later run
		// treat as cached a scene that was never produced, or was produced from an older prompt.
		if onlyFilter.ShouldProcess(scene.ID) {
			st.mu.Lock()
			manifest.UpdateEntry(scene.ID, entry)
			// Saved scene by scene, not only at the end: a run that is interrupted (a cancel, the
			// server restarting, a failure in a later scene) keeps what it already made.
			if err := cache.SaveManifest(joinDir(base, cache.ManifestPath()), manifest); err != nil {
				emitter.Warn(scene.ID, fmt.Sprintf("cannot save cache manifest: %v", err))
			}
			st.mu.Unlock()
		}
		// The concat demuxer resolves relative paths in the list file
		// against the list file's own directory (tmp/), not the cwd, so
		// list entries must be absolute to avoid a doubled "tmp/" prefix.
		absMp4Path, err := filepath.Abs(joinDir(base, mp4Path))
		if err != nil {
			return st.fail(scene.ID, fmt.Sprintf("cannot resolve absolute path for %s: %v", mp4Path, err))
		}
		clipPathBy[idx] = absMp4Path
		clipLenBy[idx] = durationSeconds
		return nil
	}
	sequential := func(ctx context.Context, idx int, scene config.Scene, renderEnv []string) error {
		job, err := prepare(ctx, idx, scene)
		if err != nil {
			return err
		}
		if err := gpuStage(ctx, job, renderEnv); err != nil {
			return err
		}
		return finishStage(ctx, job)
	}
	switch reason := overlapBlocker(cfg, gpus); {
	case overlap > 0 && reason == "":
		err = runStaged(ctx, len(cfg.Scenes), overlap, renderEnv,
			func(ctx context.Context, idx int) (*videoJob, error) { return prepare(ctx, idx, cfg.Scenes[idx]) },
			gpuStage, finishStage)
	default:
		if overlap > 0 {
			emitter.Warn("_global", "overlap ignored: "+reason)
		}
		err = runScenes(ctx, cfg.Scenes, gpus, renderEnv, sequential)
	}
	if err != nil {
		return "", st.runError(err)
	}
	if err != nil {
		return "", st.runError(err)
	}
	clipPaths := clipPathBy
	pruneKeptClips(base, cfg.Scenes)
	PruneLeftovers(base, cfg.Scenes)

	if err := cache.SaveManifest(joinDir(base, cache.ManifestPath()), manifest); err != nil {
		emitter.Warn("_global", fmt.Sprintf("cannot save cache manifest: %v", err))
	}

	// --- Final concat ---
	emitter.PhaseStart("_global", "concat")
	join := joinClips(cfg, clipPaths, clipLenBy, emitter)
	renderShaderJoins(ctx, cfg, join, ffmpegRunner, base, tmpDir, emitter)
	fades := assemble.HasFades(join)
	if !fades {
		if _, err := assemble.WriteConcatList(joinDir(base, tmpDir), clipPaths); err != nil {
			return "", fail(emitter, "_global", fmt.Sprintf("concat failed: %v", err))
		}
	}
	// The list is written through the Go-side path (joined with the project
	// directory), but ffmpeg runs with the project directory as its cwd, so
	// it must be given the project-relative path.
	listFile := filepath.Join(tmpDir, "concat_list.txt")
	// With background music the joined video is an intermediate; the mix writes the final file.
	joined := cfg.Output.File
	if cfg.BGM != nil {
		joined = filepath.Join(tmpDir, "video_nobgm.mp4")
	}
	if fades {
		if stderr, err := assemble.JoinWithFades(ctx, ffmpegRunner, join, cfg.Defaults.FPS, joined); err != nil {
			return "", fail(emitter, "_global", fmt.Sprintf("concat failed: %v (stderr: %s)", err, stderr))
		}
	} else if stderr, err := assemble.ConcatClips(ctx, ffmpegRunner, listFile, joined); err != nil {
		return "", fail(emitter, "_global", fmt.Sprintf("concat failed: %v (stderr: %s)", err, stderr))
	}
	emitter.PhaseEnd("_global", "concat", joined)

	if cfg.BGM != nil {
		env := renderEnv
		if len(gpus) > 1 {
			env = []string{"CUDA_VISIBLE_DEVICES=" + gpus[0]}
		}
		if err := mixBGM(ctx, cfg, emitter, execRunner, ffmpegRunner, manifest, force, env, base, joined, cfg.Output.File); err != nil {
			return "", err
		}
	}

	return cfg.Output.File, nil
}

// runImages drives the images-mode pipeline: render (× variants, strictly
// sequential) → per-scene export, then the global manifest write and
// optional contact sheet. It returns the final artifact path for the
// pipeline_end event. base is the project directory for Go-side file
// access; manifest entries, subprocess arguments and event artifacts stay
// project-relative.
func runImages(ctx context.Context, cfg *config.Config, emitter *events.Emitter, execRunner runner.CommandRunner, manifest *cache.Manifest, onlyFilter cache.OnlyFilter, force bool, renderEnv []string, rng func() int64, base string, gpus []string, budget *sched.Budget) (string, error) {
	exportDir := cfg.ExportDir()
	if _, _, err := render.ParseResolution(cfg.Defaults.Resolution); err != nil {
		return "", fail(emitter, "", fmt.Sprintf("render failed: %v", err))
	}

	st := &runState{emitter: emitter, manifest: manifest, budget: budget}
	renderBytes := sched.Estimate(renderFootprint(cfg, base))

	// Random seeds are drawn here, in project order, before any scene starts,
	// so the same random source gives the same seeds however the scenes are
	// scheduled.
	seedsBy := make([][]int64, len(cfg.Scenes))
	for i, scene := range cfg.Scenes {
		if onlyFilter.ShouldProcess(scene.ID) {
			seedsBy[i] = render.VariantSeeds(scene, scene.EffectiveVariants(), rng)
		}
	}
	entriesBy := make([][]export.Entry, len(cfg.Scenes))

	err := runScenes(ctx, cfg.Scenes, gpus, renderEnv, func(ctx context.Context, idx int, scene config.Scene, renderEnv []string) error {
		if !onlyFilter.ShouldProcess(scene.ID) {
			return nil
		}
		n := scene.EffectiveVariants()
		seeds := seedsBy[idx]
		// An edit keeps the shape of its picture: a portrait photo is not cropped to the project's landscape.
		scene = withEditSize(ctx, execRunner, cfg, scene)
		sceneW, sceneH, err := sceneRenderSize(cfg, scene)
		if err != nil {
			return fail(emitter, scene.ID, fmt.Sprintf("render failed: %v", err))
		}

		keys := make([]string, n)
		pngs := make([]string, n)
		hashes := make([]string, n)
		toRender := make([]bool, n)
		// madeAt is the size a picture was made at when it is not the one asked for (the card ran out of memory).
		madeAt := make([][2]int, n)
		allCached := true
		for i := 0; i < n; i++ {
			keys[i] = cache.VariantKey(scene.ID, n, i)
			_, pngs[i], _ = cache.ArtifactPaths(keys[i])
			ip, err := SceneImageParamsAt(cfg, scene, seeds[i], i, base)
			if err != nil {
				return fail(emitter, scene.ID, fmt.Sprintf("render failed: %v", err))
			}
			hashes[i] = cache.ImageHash(ip)
			st.mu.Lock()
			toRender[i] = !cache.DecideImages(manifest, keys[i], hashes[i], force)
			st.mu.Unlock()
			if toRender[i] {
				allCached = false
			}
		}

		// Phase: Render (one event pair per scene, covering all variants)
		if allCached {
			emitter.Skipped(scene.ID, "render")
		} else {
			release, err := st.reserve(ctx, renderBytes)
			if err != nil {
				return err
			}
			emitter.PhaseStart(scene.ID, "render")
			if scene.InitImage != "" {
				if err := fitInitImage(ctx, execRunner, scene, sceneW, sceneH); err != nil {
					release()
					return st.fail(scene.ID, fmt.Sprintf("render failed: %v", err))
				}
			}
			loadMs := 0
			for i := 0; i < n; i++ {
				if !toRender[i] {
					continue
				}
				res, err := render.RunVariant(ctx, execRunner, scene, cfg.Defaults, i, seeds[i], pngs[i], renderEnv)
				loadMs += res.LoadMs
				if err != nil {
					release()
					return st.fail(scene.ID, fmt.Sprintf("render failed: %v", err))
				}
				if note := render.ShrunkNote(scene.EffectiveRender(cfg.Defaults).Resolution, res); note != "" {
					emitter.Warn(scene.ID, note)
					if w, h, perr := render.ParseResolution(res.Resolution); perr == nil {
						madeAt[i] = [2]int{w, h}
					}
				}
			}
			release()
			emitter.PhaseEndLoad(scene.ID, "render", pngs[n-1], loadMs)
		}

		st.mu.Lock()
		for i := 0; i < n; i++ {
			entry := manifest.Scenes[keys[i]]
			entry.ImageHash = hashes[i]
			entry.PngPath = pngs[i]
			manifest.UpdateEntry(keys[i], entry)
		}
		st.mu.Unlock()

		// Phase: Export (copy this scene's PNGs into out/<project>/)
		emitter.PhaseStart(scene.ID, "export")
		items := make([]export.Item, 0, n)
		for i := 0; i < n; i++ {
			file := filepath.Join(exportDir, config.VariantPNGName(scene.ID, n, i))
			resolution := [2]int{sceneW, sceneH}
			if madeAt[i] != ([2]int{}) {
				resolution = madeAt[i]
			}
			items = append(items, export.Item{
				Src: joinDir(base, pngs[i]),
				Dst: joinDir(base, file),
				Entry: export.Entry{
					SceneID:        scene.ID,
					Variant:        i,
					EffectiveSeed:  seeds[i],
					Prompt:         render.ComposePositive(cfg.Defaults.SubjectPrompt, scene.Prompt),
					NegativePrompt: render.ComposeNegative(scene.NegativePrompt, cfg.Defaults.NegativePrompt),
					Model:          cfg.Defaults.Model,
					ModelSet:       cfg.Defaults.ModelSet,
					Resolution:     resolution,
					Sampler:        cfg.Defaults.Sampler,
					Steps:          scene.EffectiveRender(cfg.Defaults).Steps,
					File:           file,
					InitImage:      scene.InitImage,
					Strength:       initStrength(scene),
				},
			})
		}
		if err := export.CopyItems(items); err != nil {
			return st.fail(scene.ID, fmt.Sprintf("export failed: %v", err))
		}
		for _, it := range items {
			entriesBy[idx] = append(entriesBy[idx], it.Entry)
		}
		emitter.PhaseEnd(scene.ID, "export", items[n-1].Entry.File)
		return nil
	})
	if err != nil {
		return "", st.runError(err)
	}
	// Assemble in project order, whatever order the scenes finished in.
	var entries []export.Entry
	var exportedPNGs []string
	for _, es := range entriesBy {
		for _, e := range es {
			entries = append(entries, e)
			exportedPNGs = append(exportedPNGs, e.File)
		}
	}

	if err := cache.SaveManifest(joinDir(base, cache.ManifestPath()), manifest); err != nil {
		emitter.Warn("_global", fmt.Sprintf("cannot save cache manifest: %v", err))
	}

	// Global export step: mandatory manifest.json, optional contact sheet.
	emitter.PhaseStart("_global", "export")
	manifestPath := filepath.Join(exportDir, "manifest.json")
	m := export.Manifest{
		Project:     cfg.Project,
		Mode:        "images",
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Images:      entries,
	}
	if err := export.WriteManifest(joinDir(base, manifestPath), m); err != nil {
		return "", fail(emitter, "_global", fmt.Sprintf("export failed: %v", err))
	}
	if cfg.Output.ContactSheet {
		if err := export.ContactSheet(ctx, execRunner, exportedPNGs, filepath.Join(exportDir, "contact_sheet.png")); err != nil {
			return "", fail(emitter, "_global", fmt.Sprintf("%v", err))
		}
	}
	emitter.PhaseEnd("_global", "export", manifestPath)

	return manifestPath, nil
}

// newArtifactStore is the store of the artifacts of a video project by the hash of their inputs: `tmp/store/` of the
// project, so it goes with the rest of tmp/ (and lasts as long as the manifest does: with --keep-tmp across runs, and
// otherwise within the run). Its size limit comes from ORCHESTRATOR_STORE_MB (default 2048; 0 turns it off).
func newArtifactStore(base string) *cache.Store {
	limit, ok := cache.StoreMaxBytes(os.Getenv("ORCHESTRATOR_STORE_MB"))
	if !ok {
		return nil
	}
	return &cache.Store{Dir: joinDir(base, filepath.Join("tmp", "store")), MaxBytes: limit}
}

// keepInStore puts the artifact of a phase in the store under the hash of its inputs, when it is not there yet. A
// store that cannot be written only warns: it is an optimisation, never a reason to stop a run.
func keepInStore(store *cache.Store, emitter *events.Emitter, sceneID, hash, ext, path string) {
	if store == nil || store.Has(hash, ext) {
		return
	}
	if err := store.Put(hash, ext, path); err != nil {
		emitter.Warn(sceneID, fmt.Sprintf("cannot keep the artifact in the store: %v", err))
	}
}

// VideoImageHash is the cache key of a video scene's picture: the generation
// inputs, or for a scene with its own picture the content of the file and the
// resolution it is fitted to. base is the project directory.
func VideoImageHash(cfg *config.Config, scene config.Scene, base string) (string, error) {
	if scene.Clip != "" {
		sha, err := cache.FileSHA256(joinDir(base, filepath.Join(config.SourcesSubdir, scene.Clip)))
		if err != nil {
			return "", fmt.Errorf("cannot read the clip %s: %w", scene.Clip, err)
		}
		return cache.ImageHash(cache.ImageParams{Resolution: scene.EffectiveRender(cfg.Defaults).Resolution, SourceImage: "clip:" + sha + clipTrimKey(scene)}), nil
	}
	if scene.Image == "" {
		p, err := SceneImageParamsAt(cfg, scene, scene.ResolvedSeed(), 0, base)
		if err != nil {
			return "", err
		}
		return cache.ImageHash(p), nil
	}
	sha, err := cache.FileSHA256(joinDir(base, filepath.Join(config.SourcesSubdir, scene.Image)))
	if err != nil {
		return "", fmt.Errorf("cannot read the image %s: %w", scene.Image, err)
	}
	return cache.ImageHash(cache.ImageParams{Resolution: scene.EffectiveRender(cfg.Defaults).Resolution, SourceImage: sha}), nil
}

// SceneImageParamsAt is SceneImageParams plus the starting picture of an
// img2img scene (its content and strength), read from the project directory.
func SceneImageParamsAt(cfg *config.Config, scene config.Scene, seed int64, variantIndex int, base string) (cache.ImageParams, error) {
	p := SceneImageParams(cfg, scene, seed, variantIndex)
	if scene.InitImage == "" {
		return p, nil
	}
	sha, err := cache.FileSHA256(joinDir(base, filepath.Join(config.SourcesSubdir, scene.InitImage)))
	if err != nil {
		return p, fmt.Errorf("cannot read the init image %s: %w", scene.InitImage, err)
	}
	p.InitImage, p.InitStrength = sha, scene.ResolvedStrength()
	return p, nil
}

// fitInitImage writes the starting picture of an img2img scene, fitted to the
// project resolution, where the diffusion program reads it.
func fitInitImage(ctx context.Context, r runner.CommandRunner, scene config.Scene, width, height int) error {
	args := assemble.BuildFitImageArgs(filepath.Join(config.SourcesSubdir, scene.InitImage), width, height, cache.InitImagePath(scene.ID))
	if _, err := r.Run(ctx, "ffmpeg", args); err != nil {
		return fmt.Errorf("scene %q: cannot fit the init image: %w", scene.ID, err)
	}
	return nil
}

func initStrength(scene config.Scene) *float64 {
	if scene.InitImage == "" {
		return nil
	}
	v := scene.ResolvedStrength()
	return &v
}

// joinClips pairs each scene clip with the crossfade into it: the scene's own value or the project's,
// limited to half of the shorter of the two scenes it joins (with a warning when it is shortened).
func joinClips(cfg *config.Config, paths []string, lengths []float64, emitter *events.Emitter) []assemble.JoinClip {
	out := make([]assemble.JoinClip, len(paths))
	for i, p := range paths {
		out[i] = assemble.JoinClip{Path: p, Duration: lengths[i]}
		if i == 0 {
			continue
		}
		fade := cfg.Scenes[i].ResolvedTransition(cfg.Defaults)
		if limit := math.Min(lengths[i-1], lengths[i]) / 2; fade > limit {
			emitter.Warn(cfg.Scenes[i].ID, fmt.Sprintf("the %.2f s fade into this scene was shortened to %.2f s: a fade cannot be longer than half of the shorter scene it joins", fade, limit))
			fade = limit
		}
		out[i].Fade = fade
		out[i].Kind = cfg.Scenes[i].ResolvedTransitionKind(cfg.Defaults)
		out[i].Accent = cfg.Scenes[i].ResolvedTransitionAccent(cfg.Defaults)
	}
	return out
}

// KeptClipsDir is where each scene's clip is kept (project-relative), outside
// tmp/ so cleaning tmp/ does not remove it.
const KeptClipsDir = "out/clips"

// keepClip copies a scene's finished clip to out/clips/<scene>.mp4, atomically.
// A clip that is not there (a scene --only left out and never made) is not an error.
func keepClip(base, sceneID, mp4Path string) error {
	return keepFile(base, mp4Path, sceneID+".mp4")
}

// keepAnimation copies a scene's complete animation (the ping-pong file) to out/clips/<scene>.anim.mp4.
func keepAnimation(base, sceneID, animPath string) error {
	return keepFile(base, animPath, sceneID+".anim.mp4")
}

// keepFile copies the project-relative file src to out/clips/<name>, atomically; a missing source is not an error.
func keepFile(base, srcPath, name string) error {
	src, err := os.Open(joinDir(base, srcPath))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer src.Close()
	dir := joinDir(base, KeptClipsDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".clip-*")
	if err != nil {
		return err
	}
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}

// pruneKeptClips removes from out/clips/ the clips of scenes that are no longer
// in the project, the complete animations of scenes that no longer animate, and
// any half-written copy, so no clip outlives its scene.
func pruneKeptClips(base string, scenes []config.Scene) {
	dir := joinDir(base, KeptClipsDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	live := make(map[string]bool, len(scenes))
	for _, sc := range scenes {
		live[sc.ID+".mp4"] = true
		if sc.Animate {
			live[sc.ID+".anim.mp4"] = true // the complete animation, only for a scene that animates
		}
	}
	for _, e := range entries {
		if !live[e.Name()] {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}

// fitSceneClipFrame writes the first frame of the scene's own clip as the scene image.
func fitSceneClipFrame(ctx context.Context, r runner.CommandRunner, scene config.Scene, width, height int, out string) error {
	args := assemble.BuildPosterArgs(filepath.Join(config.SourcesSubdir, scene.Clip), width, height, scene.ResolvedClipStart(), out)
	if _, err := r.Run(ctx, "ffmpeg", args); err != nil {
		return fmt.Errorf("scene %q: cannot take the first frame of the clip: %w", scene.ID, err)
	}
	return nil
}

// fitSceneImage writes the scene image from the scene's own picture.
func fitSceneImage(ctx context.Context, r runner.CommandRunner, scene config.Scene, width, height int, out string) error {
	args := assemble.BuildFitImageArgs(filepath.Join(config.SourcesSubdir, scene.Image), width, height, out)
	if _, err := r.Run(ctx, "ffmpeg", args); err != nil {
		return fmt.Errorf("scene %q: cannot fit the image: %w", scene.ID, err)
	}
	return nil
}

// SceneImageParams builds the cache-key inputs for one variant.
func SceneImageParams(cfg *config.Config, scene config.Scene, seed int64, variantIndex int) cache.ImageParams {
	eff := scene.EffectiveRender(cfg.Defaults)
	p := cache.ImageParams{
		Prompt:               scene.Prompt,
		NegativePrompt:       scene.NegativePrompt,
		Seed:                 seed,
		Model:                cfg.Defaults.Model,
		Resolution:           eff.Resolution,
		Sampler:              cfg.Defaults.Sampler,
		Steps:                eff.Steps,
		SubjectPrompt:        cfg.Defaults.SubjectPrompt,
		GlobalNegativePrompt: cfg.Defaults.NegativePrompt,
		Lora:                 scene.Lora,
		VariantIndex:         variantIndex,
	}
	if ms := cfg.Defaults.ModelSet; ms != nil {
		p.DiffusionModel, p.LLM, p.VAE = ms.DiffusionModel, ms.LLM, ms.VAE
		p.Edit, p.EditClipL, p.EditT5XXL, p.EditLLMVision = ms.Edit, ms.ClipL, ms.T5XXL, ms.LLMVision
	}
	if eff.CfgScale != nil {
		p.CfgScale = *eff.CfgScale
	}
	return p
}

// isAnimated reports whether the scene runs the animate phase.
func isAnimated(cfg *config.Config, scene config.Scene) bool {
	return scene.Animate && cfg.Defaults.Animate != nil
}

// SceneAnimateParams builds the animate-phase cache-key inputs for one
// animated scene, with every unset setting resolved to its default.
func SceneAnimateParams(cfg *config.Config, scene config.Scene, base string) cache.AnimateParams {
	a := cfg.Defaults.Animate.ForScene(scene)
	p := cache.AnimateParams{
		Prompt:         animate.Prompt(scene, cfg.Defaults),
		Seed:           scene.ResolvedSeed(),
		DiffusionModel: a.DiffusionModel,
		T5XXL:          a.T5XXL,
		VAE:            a.VAE,
		Resolution:     a.Resolution,
		Frames:         a.Frames,
		FPS:            a.FPS,
		Steps:          a.Steps,
		CfgScale:       *a.CfgScale,
	}
	if a.IsAnimateDiff() {
		// The Wan models are not inputs of an AnimateDiff clip.
		p.Engine = config.EngineAnimateDiff
		p.DiffusionModel, p.T5XXL, p.VAE = "", "", ""
		p.Checkpoint = a.Checkpoint
		p.MotionModule = a.MotionModule
		p.Strength = *a.Strength
		return p
	}
	if a.IsMiniMaxH3() {
		p.Engine = config.EngineMiniMaxH3
		p.T5XXL = ""
		p.AudioVAE, p.LLM, p.LoRA, p.FirstFrame = a.AudioVAE, a.LLM, a.LoRA, a.FirstFrame
		p.ModelAudio = a.ModelAudio
		p.EndImage, p.References = sceneInputHashes(base, scene)
		return p
	}
	if a.IsLTX() {
		p.Engine = config.EngineLTX
		p.T5XXL = ""
		p.AudioVAE, p.LLM, p.Connectors = a.AudioVAE, a.LLM, a.Connectors
		p.ModelAudio, p.ModelAudioSpeech = a.ModelAudio, a.ModelAudioSpeech
		p.EndImage, p.References = sceneInputHashes(base, scene)
		return p
	}
	if a.TwoExpert() {
		p.HighNoiseModel = a.HighNoiseDiffusionModel
		p.HighNoiseSteps = a.HighNoiseSteps
		p.HighNoiseCfgScale = *a.HighNoiseCfgScale
		p.FlowShift = config.AnimateTwoExpertFlowShift
	}
	return p
}

// sceneInputHashes are the content hashes of a scene's last picture and references, in the order they go to the
// engine: pictures, videos, then audios. A file that cannot be read hashes to a marker that never matches a stored key.
func sceneInputHashes(base string, scene config.Scene) (end string, refs []string) {
	hash := func(name string) string {
		data, err := os.ReadFile(filepath.Join(base, config.SourcesSubdir, name))
		if err != nil {
			return "unreadable:" + name
		}
		sum := sha256.Sum256(data)
		return hex.EncodeToString(sum[:])
	}
	if scene.EndImage != "" {
		end = hash(scene.EndImage)
	}
	if r := scene.References; r != nil {
		for _, n := range r.Images {
			refs = append(refs, "image:"+hash(n))
		}
		for _, n := range r.Videos {
			refs = append(refs, "video:"+hash(n))
		}
		for _, n := range r.Audios {
			refs = append(refs, "audio:"+hash(n))
		}
	}
	return end, refs
}

// animatedDuration is the length in seconds of the ping-pong clip: the
// forward frames plus the reversed ones minus the two shared endpoints.
func animatedDuration(a *config.Animate, scene config.Scene) float64 {
	r := a.ForScene(scene)
	if scene.EndImage != "" {
		return float64(r.Frames) / float64(r.FPS) // played once, not back and forth
	}
	return float64(2*r.Frames-2) / float64(r.FPS)
}

// sceneRenderSize is the size a scene's picture is drawn at: its own
// resolution when it sets one, the project's otherwise.
func sceneRenderSize(cfg *config.Config, scene config.Scene) (int, int, error) {
	return render.ParseResolution(scene.EffectiveRender(cfg.Defaults).Resolution)
}

// SceneClipParams builds the clip-phase cache-key inputs for one scene.
func SceneClipParams(cfg *config.Config, scene config.Scene, base string) cache.ClipParams {
	var animateHash string
	if isAnimated(cfg, scene) {
		animateHash = cache.AnimateHash(SceneAnimateParams(cfg, scene, base))
	}
	return cache.ClipParams{
		AnimateHash:     animateHash,
		MotionType:      scene.MotionType(),
		MotionIntensity: scene.MotionIntensity(),
		Grain:           scene.LookGrain(),
		WhiteBalance:    scene.LookWhiteBalance(),
		FPS:             cfg.Defaults.FPS,
		SilenceSeconds:  scene.SilenceDuration(),
		Seed:            scene.ResolvedSeed(),
		ClipTrim:        clipTrimKey(scene),
		ClipSpeed:       clipSpeedKey(scene),
		Assembly:        videoClipAssembly(cfg, scene),
	}
}

// videoClipAssembly is the revision of the assembly of a scene made from a video (an animation or its own clip); 0 for a still.
func videoClipAssembly(cfg *config.Config, scene config.Scene) int {
	if scene.Clip != "" || scene.IsMotionGraphic() || scene.IsBlender() || isAnimated(cfg, scene) {
		return 2
	}
	return 0
}

// clipSpeedKey is the speed of a scene's video for the cache key (0 when automatic or when the scene has no video of its own).
func clipSpeedKey(scene config.Scene) float64 {
	if scene.ClipSpeed == nil || (scene.Clip == "" && !scene.Animate) {
		return 0
	}
	return *scene.ClipSpeed
}

// clipTrimKey is the cache-key form of the part of a clip a scene uses ("" for the whole clip).
func clipTrimKey(scene config.Scene) string {
	if !scene.ClipTrimmed() || (scene.Clip == "" && !scene.Animate) {
		return ""
	}
	end := "end"
	if scene.ClipEnd != nil {
		end = fmt.Sprintf("%.3f", *scene.ClipEnd)
	}
	return fmt.Sprintf("%.3f-%s", scene.ResolvedClipStart(), end)
}

// queryFreeVRAM shells out to nvidia-smi for the free memory of gpuID.
func queryFreeVRAM(gpuID string) (int, error) {
	cmd := exec.Command("nvidia-smi",
		"--query-gpu=memory.free",
		"--format=csv,noheader,nounits",
		"--id="+gpuID,
	)
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr = string(exitErr.Stderr)
		}
		return 0, fmt.Errorf("nvidia-smi failed: %w (stderr: %s)", err, strings.TrimSpace(stderr))
	}

	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return 0, fmt.Errorf("nvidia-smi returned no data")
	}

	freeMB, err := strconv.Atoi(strings.TrimSpace(fields[0]))
	if err != nil {
		return 0, fmt.Errorf("cannot parse VRAM value %q: %w", fields[0], err)
	}
	return freeMB, nil
}

// checkVRAM warns if free memory on gpuID is below threshold. freeVRAMMB reports the free memory of one GPU; nil asks nvidia-smi.
func checkVRAM(freeVRAMMB func(gpuID string) (int, error), gpuID string, thresholdMB int, emitter *events.Emitter) error {
	if freeVRAMMB == nil {
		freeVRAMMB = queryFreeVRAM
	}
	freeMB, err := freeVRAMMB(gpuID)
	if err != nil {
		return err
	}
	if freeMB < thresholdMB {
		emitter.Warn("_global", fmt.Sprintf("free VRAM on GPU %s is %d MB (threshold: %d MB)", gpuID, freeMB, thresholdMB))
	}
	return nil
}

// fail emits the error event (exactly as the pre-extraction fatal did) and
// returns the same message as an error for the caller to act on.
func fail(emitter *events.Emitter, sceneID, msg string) error {
	emitter.Error(sceneID, msg)
	return errors.New(msg)
}

// joinDir resolves a project-relative path against the project directory;
// an empty base keeps the path as-is (cwd semantics).
func joinDir(base, p string) string {
	if base == "" {
		return p
	}
	return filepath.Join(base, p)
}

// dirRunner decorates a CommandRunner so every subprocess runs with the
// project directory as its working directory.
type dirRunner struct {
	r   runner.CommandRunner
	dir string
}

func (d dirRunner) Run(ctx context.Context, name string, args []string, opts ...runner.Option) ([]byte, error) {
	return d.r.Run(ctx, name, args, append(opts, runner.WithDir(d.dir))...)
}

// ---------------------------------------------------------------------------
// Adapters: bridge the production runner.CommandRunner (internal/platform/runner) to
// the narrower, independently-defined Runner interfaces that internal/speech/tts
// and internal/video/assemble each accept.
// ---------------------------------------------------------------------------

// ttsRunnerAdapter satisfies tts.Runner by delegating to runner.CommandRunner.
type ttsRunnerAdapter struct{ r runner.CommandRunner }

func (a ttsRunnerAdapter) Run(ctx context.Context, name string, args []string, stdin io.Reader, stdout io.Writer) (string, error) {
	var opts []runner.Option
	if stdin != nil {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return "", err
		}
		opts = append(opts, runner.WithStdin(data))
	}
	out, err := a.r.Run(ctx, name, args, opts...)
	if err != nil {
		return "", err
	}
	if stdout != nil {
		if _, err := stdout.Write(out); err != nil {
			return "", err
		}
	}
	return "", nil
}

// assembleRunnerAdapter satisfies assemble.CommandRunner by invoking ffmpeg
// through runner.CommandRunner.
type assembleRunnerAdapter struct{ r runner.CommandRunner }

func (a assembleRunnerAdapter) Run(ctx context.Context, args []string) (string, string, error) {
	out, err := a.r.Run(ctx, "ffmpeg", args)
	if err != nil {
		return "", "", err
	}
	return string(out), "", nil
}

// withEditSize gives a scene of an editing project the resolution of its picture's shape (see render.EditResolution),
// so the picture is edited whole instead of cropped to the project's. Any problem leaves the scene as it is.
func withEditSize(ctx context.Context, r runner.CommandRunner, cfg *config.Config, scene config.Scene) config.Scene {
	if ms := cfg.Defaults.ModelSet; ms == nil || !ms.Edit || scene.InitImage == "" {
		return scene
	}
	w, h, err := probe.ImageSize(ctx, r, filepath.Join(config.SourcesSubdir, scene.InitImage))
	if err != nil {
		return scene
	}
	res, err := render.EditResolution(w, h, scene.EffectiveRender(cfg.Defaults).Resolution)
	if err != nil {
		return scene
	}
	own := config.SceneRender{}
	if scene.Render != nil {
		own = *scene.Render
	}
	own.Resolution = res
	scene.Render = &own
	return scene
}

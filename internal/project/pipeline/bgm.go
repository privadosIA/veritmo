package pipeline

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"time"

	"veritmo/internal/music/music"
	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/probe"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
	"veritmo/internal/video/assemble"
)

// bgmKey is the manifest key of the generated soundtrack.
const bgmKey = "bgm_soundtrack"

// Lengths a generated soundtrack is asked for, in seconds: the video length
// rounded up to a step (so small edits reuse the cache), within what the engine
// does comfortably. A longer video loops the track.
const (
	bgmStep = 10.0
	bgmMin  = 10.0
	bgmMax  = 240.0
)

// BGMDuration returns the length to generate for a video of the given length.
func BGMDuration(video time.Duration) float64 {
	d := math.Ceil(video.Seconds()/bgmStep) * bgmStep
	return math.Min(math.Max(d, bgmMin), bgmMax)
}

// bgmTrack is the track the soundtrack is generated as: instrumental, without
// the language model.
func bgmTrack(b *config.BGM, seconds float64) config.Track {
	seed := int(b.ResolvedSeed())
	useLM := false
	return config.Track{ID: "bgm", Prompt: b.Prompt, Instrumental: true, DurationSeconds: &seconds, Seed: &seed, UseLM: &useLM}
}

// mixBGM puts the project's background music under the narration of the
// joined video and writes the final file. It generates the soundtrack first
// when the project asks for one (cached). joined and the music paths are
// project-relative, like every argument handed to a program.
func mixBGM(ctx context.Context, cfg *config.Config, emitter *events.Emitter, execRunner runner.CommandRunner, ffmpegRunner assembleRunnerAdapter, manifest *cache.Manifest, force bool, env []string, base, joined, out string) error {
	b := cfg.BGM
	total, err := probe.Duration(ctx, execRunner, joined)
	if err != nil {
		return fail0(emitter, fmt.Sprintf("mix failed: cannot measure the video: %v", err))
	}

	musicPath := filepath.Join(config.SourcesSubdir, b.File)
	if b.Generated() {
		musicPath, err = generateBGM(ctx, cfg, emitter, execRunner, manifest, force, env, base, total)
		if err != nil {
			return err
		}
	}

	emitter.PhaseStart("_global", "mix")
	opts := assemble.MixOptions{Volume: b.ResolvedVolume(), Fade: b.ResolvedFade(), Ducking: b.ResolvedDucking(), Duration: total.Seconds()}
	if stderr, err := assemble.MixMusic(ctx, ffmpegRunner, joined, musicPath, out, opts); err != nil {
		return fail0(emitter, fmt.Sprintf("mix failed: %v (stderr: %s)", err, stderr))
	}
	emitter.PhaseEnd("_global", "mix", out)
	return nil
}

// generateBGM makes (or reuses) the soundtrack for a video of length total and
// returns its project-relative path.
func generateBGM(ctx context.Context, cfg *config.Config, emitter *events.Emitter, execRunner runner.CommandRunner, manifest *cache.Manifest, force bool, env []string, base string, total time.Duration) (string, error) {
	m := cfg.Defaults.Music
	ws := music.Workspace{Base: base}
	format := m.ResolvedOutputFormat()
	modelsDir := m.ResolvedModelsDir()
	adaptersDir := m.AdaptersDir()
	synthModel := m.ResolvedSynth()
	vae := music.FindSupport(ws, modelsDir, config.MusicVAEGlob)

	track := bgmTrack(cfg.BGM, BGMDuration(total))
	req := music.BuildRequest(cfg, track, cfg.BGM.ResolvedSeed())
	req.LMModel, req.LMSeed = "", nil // the soundtrack never composes
	body, err := music.SynthRequest(nil, req)
	if err != nil {
		return "", fail0(emitter, fmt.Sprintf("bgm failed: %v", err))
	}
	sp, err := SynthParamsOf(ws, adaptersDir, track, req, body, synthModel, vae)
	if err != nil {
		return "", fail0(emitter, fmt.Sprintf("bgm failed: %v", err))
	}
	hash := cache.SynthHash(sp)
	_, audio := cache.MusicPaths(bgmKey, format)
	if cache.DecideSynth(manifest, base, bgmKey, hash, force) {
		emitter.Skipped("_global", "bgm")
		return audio, nil
	}

	emitter.PhaseStart("_global", "bgm")
	useAdapters := ""
	if req.Adapter != "" {
		useAdapters = adaptersDir
	}
	engine := newACEEngine(cfg, execRunner, base)
	defer func() { _ = engine.Close() }()
	if err := engine.Synth(ctx, ws, modelsDir, useAdapters, cache.MusicWorkDir(bgmKey), body, format, audio, env, nil); err != nil {
		return "", fail0(emitter, fmt.Sprintf("bgm failed: %v", err))
	}
	e := manifest.Scenes[bgmKey]
	e.SynthHash, e.AudioPath = hash, audio
	manifest.UpdateEntry(bgmKey, e)
	if err := cache.SaveManifest(joinDir(base, cache.ManifestPath()), manifest); err != nil {
		emitter.Warn("_global", fmt.Sprintf("cannot save cache manifest: %v", err))
	}
	emitter.PhaseEnd("_global", "bgm", audio)
	return audio, nil
}

// fail0 reports a global failure.
func fail0(emitter *events.Emitter, msg string) error {
	return fail(emitter, "_global", msg)
}

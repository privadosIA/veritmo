package generation

// extendclip.go makes the new clip of the `extend` video-edit tool: it is a generation with a start picture (the
// last frame of the video), planned like POST /api/generations plans one and run in the folder of the edit.

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"veritmo/internal/server/api"

	"veritmo/internal/platform/events"
	"veritmo/internal/platform/fsutil"
	"veritmo/internal/project/config"
	"veritmo/internal/project/pipeline"
	"veritmo/internal/video/videoedit"
)

// clipOptions maps the engines of the API to the generation options that take a first frame.
var clipOptions = []struct{ engine, option string }{
	{videoedit.EngineMiniMaxH3, "h3-full"},
	{videoedit.EngineLTX, "ltx"},
}

// extensionQuality is the quality of the new clip: the best, as the new picture should match the source's.
const extensionQuality = "high"

type clipBridge struct{ h *Handler }

func (b clipBridge) Engines(ctx context.Context) []videoedit.ClipEngine {
	choices := videoChoices(b.h.catalogNow(), b.h.machineNow())
	var out []videoedit.ClipEngine
	for _, co := range clipOptions {
		ch, ok := findChoice(choices, co.option)
		e := videoedit.ClipEngine{ID: co.engine, Available: ok && ch.Available, Reason: ch.Reason}
		switch {
		case !ok:
			e.Reason = "this build does not have the engine"
		case ch.Available && co.engine == videoedit.EngineMiniMaxH3 && !ch.FirstFrame:
			e.Available, e.Reason = false, "MiniMax H3 is installed without its first-frame bundle"
		}
		out = append(out, e)
	}
	return out
}

// nearestAspect is the listed aspect ratio closest to the shape of the video.
func nearestAspect(w, h int) string {
	want := math.Log(float64(w) / float64(h))
	best, bestD := aspectRatios[0], math.MaxFloat64
	for _, a := range aspectRatios {
		aw, ah, _ := parseAspect(a)
		if d := math.Abs(math.Log(float64(aw)/float64(ah)) - want); d < bestD {
			best, bestD = a, d
		}
	}
	return best
}

// forwardSeconds is the length of the clip an engine makes by itself, forward only: the pipeline makes a ping-pong
// of it (forward and back) and the first half is what the engine made.
func forwardSeconds(a *config.Animate) float64 {
	return float64(clipFrames(a)) / float64(clipFPS(a))
}

// prepareClip plans the new clip as a generation with a start picture and writes its project into req.Dir: the
// scenes.yaml, the picture in sources/ and the links to the shared models. It returns the configuration, validated,
// and the seconds the clip will last.
func (b clipBridge) prepareClip(req videoedit.ClipRequest) (*config.Config, float64, error) {
	var option string
	for _, co := range clipOptions {
		if co.engine == req.Engine {
			option = co.option
		}
	}
	if option == "" {
		return nil, 0, fmt.Errorf("the engine %q cannot make the new clip", req.Engine)
	}
	cat, m := b.h.catalogNow(), b.h.machineNow()
	choice, ok := findChoice(videoChoices(cat, m), option)
	if !ok || !choice.Available {
		return nil, 0, fmt.Errorf("the engine %s cannot run now: %s", req.Engine, choice.Reason)
	}
	const picture = "inicio.png"
	vr := videoRequest{Prompt: req.Prompt, Option: option, Duration: req.Seconds, AspectRatio: nearestAspect(req.Width, req.Height), Quality: extensionQuality, Picture: picture}
	plan, err := planVideo("extension", vr, choice, cat, m, b.h.ws.FirstAsset("voices"))
	if err != nil {
		return nil, 0, err
	}
	// The pipeline makes a ping-pong of the engine's clip and cuts the scene from its start, so the length of the
	// scene, up to what the engine makes, gives the forward clip alone.
	seconds := math.Min(req.Seconds, forwardSeconds(plan.Config.Defaults.Animate))
	plan.Config.Scenes[0].SilenceSeconds = fptr(math.Round(seconds*100) / 100)

	if err := os.MkdirAll(filepath.Join(req.Dir, config.SourcesSubdir), 0o755); err != nil {
		return nil, 0, err
	}
	if err := fsutil.CopyFile(req.Picture, filepath.Join(req.Dir, config.SourcesSubdir, picture)); err != nil {
		return nil, 0, err
	}
	linkAbsolute(req.Dir)
	yml, err := scenesYAML(plan.Config)
	if err != nil {
		return nil, 0, err
	}
	if err := os.WriteFile(filepath.Join(req.Dir, "scenes.yaml"), yml, 0o644); err != nil {
		return nil, 0, err
	}
	// the new definition is the saved one (the text it replaces is a version)
	if err := b.h.ws.IngestProject(filepath.Base(req.Dir), "extend"); err != nil {
		return nil, 0, err
	}
	cfg, err := config.LoadBytes(yml)
	if err != nil {
		return nil, 0, err
	}
	cfg.Parts = b.h.ws.Parts()
	if err := cfg.ValidateInDir(req.Dir); err != nil {
		return nil, 0, fmt.Errorf("the new clip cannot be planned: %w", err)
	}
	return cfg, seconds, nil
}

func (b clipBridge) MakeClip(ctx context.Context, req videoedit.ClipRequest) (videoedit.ClipResult, error) {
	cfg, seconds, err := b.prepareClip(req)
	if err != nil {
		return videoedit.ClipResult{}, err
	}
	// Its events are not the edit's: the edit has one phase.
	out, err := pipeline.Run(ctx, cfg, pipeline.Options{Dir: req.Dir, GPU: req.GPU, NoContainerStart: true}, events.New(io.Discard, true))
	if err != nil {
		if ctx.Err() != nil {
			return videoedit.ClipResult{}, ctx.Err()
		}
		return videoedit.ClipResult{}, fmt.Errorf("the engine %s could not make the clip: %w", req.Engine, err)
	}
	return videoedit.ClipResult{File: filepath.Join(req.Dir, filepath.FromSlash(out)), Seconds: seconds}, nil
}

// linkAbsolute links the shared models and voices into a folder with absolute links. A project's own folder uses
// relative ones, but the folder of a clip is inside a project that the media directory may have moved to another disk,
// and a relative link is read from where the folder really is.
func linkAbsolute(dir string) {
	for _, name := range []string{"models", "voices"} {
		src, err := filepath.Abs(name)
		if err != nil || !api.DirExists(src) {
			continue
		}
		dst := filepath.Join(dir, name)
		if _, err := os.Lstat(dst); err == nil {
			continue
		}
		_ = os.Symlink(src, dst)
	}
}

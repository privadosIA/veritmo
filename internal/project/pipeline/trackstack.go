package pipeline

// trackstack.go: the video track and the stack (spec assemble and cache). A project with a video_track draws its motion scenes as transparent
// layers; here each item of the track is built from its clip, the items are joined into one video (gaps in the design's background colour) and
// the layers (and the scenes that are not layers) are laid over it, with a silent track of sound for the soundtrack to be mixed under.

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"

	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/probe"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
	"veritmo/internal/video/assemble"
	"veritmo/internal/video/motion"
)

// LayerPath is where the transparent video of a layer scene is written, relative to the project.
func LayerPath(sceneID, ext string) string {
	return filepath.Join("tmp", fmt.Sprintf("scene_%s.layer.%s", sceneID, ext))
}

// TrackItemPath is where an item of the video track is built, relative to the project; TrackPath is the joined track.
func TrackItemPath(id string) string { return filepath.Join("tmp", "track_"+id+".mp4") }

// TrackPath is where the joined video track is written, relative to the project.
func TrackPath() string { return filepath.Join("tmp", "track.mp4") }

// trackColour is the colour of the gaps of the track and of the scrim: the design's background of its default mode, black without a design.
func trackColour(cfg *config.Config) string {
	m := cfg.Defaults.Motion
	if m == nil || m.Design == nil {
		return "#000000"
	}
	if m.Design.DefaultMode == "light" && m.Design.Light != "" {
		return m.Design.Light
	}
	if m.Design.Dark != "" {
		return m.Design.Dark
	}
	return "#000000"
}

// trackBuildSeconds is how long item i is built: its own length plus the crossfade into the next one when the two touch, so that the items land
// where they start once the track is joined.
func trackBuildSeconds(items []config.VideoTrackItem, i int) float64 {
	secs := items[i].Seconds()
	if i+1 < len(items) {
		if f := trackFade(items, i+1); f > 0 {
			secs += f
		}
	}
	return secs
}

// trackFade is the crossfade into item i from the one before it: what it says, when the two touch, never longer than the shorter of them.
func trackFade(items []config.VideoTrackItem, i int) float64 {
	if i == 0 || items[i].TransitionSeconds == nil || *items[i].TransitionSeconds <= 0 || items[i].Start > items[i-1].End+0.001 {
		return 0
	}
	return math.Min(*items[i].TransitionSeconds, math.Min(items[i-1].Seconds(), items[i].Seconds()))
}

// renderLayerPage plays the stored page of a layer scene to a transparent video as long as the scene: the page of the scene (not an overlay's)
// with its song map, on a transparent background.
func (m *motionRun) renderLayerPage(ctx context.Context, base, sceneID string, width, height, fps int, seconds float64, out string) error {
	br, err := m.Browser()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(motion.PagePath(base, sceneID, false))
	if err != nil {
		return err
	}
	page := motion.Page{HTML: string(data), Width: width, Height: height, Duration: seconds, Transparent: true, Song: motion.LoadSong(base, sceneID)}
	_, _, err = renderMotion(ctx, motion.Job{Page: page, FPS: fps, Browser: br.Path, Out: joinDir(base, out)})
	return err
}

// renderLayerScene draws a layer scene: its page to a transparent video, and its first frame (with its transparency) as the scene picture.
func renderLayerScene(ctx context.Context, mo *motionRun, r runner.CommandRunner, base string, scene config.Scene, width, height, fps int, seconds float64, out, pngPath string) error {
	if err := mo.renderLayerPage(ctx, base, scene.ID, width, height, fps, seconds, out); err != nil {
		return err
	}
	args := []string{"-y"}
	if filepath.Ext(out) == ".webm" {
		args = append(args, "-c:v", "libvpx-vp9") // the native VP9 decoder drops the alpha channel
	}
	args = append(args, "-i", out, "-frames:v", "1", pngPath)
	if _, err := r.Run(ctx, "ffmpeg", args); err != nil {
		return fmt.Errorf("scene %q: cannot take the first frame of the layer: %w", scene.ID, err)
	}
	return nil
}

// trackStackInput is what the track and the stack are made from.
type trackStackInput struct {
	cfg      *config.Config
	emitter  *events.Emitter
	run      runner.CommandRunner
	manifest *cache.Manifest
	force    bool
	base     string
	width    int
	height   int
	// clipPaths and clipLens are the video of each scene (a layer's transparent video, or a scene's opaque clip) and its length, in scene
	// order; join says how each scene is joined to the one before it.
	clipPaths []string
	clipLens  []float64
	join      []assemble.JoinClip
	// layerHash is the hash of what each scene's video was made from (a layer's image hash, an opaque scene's clip hash).
	layerHash []string
	out       string
}

// buildTrackAndStack makes the track from the items of video_track, then lays the scenes over it and writes the video out (with a silent
// track of sound). It writes nothing the manifest already has made from the same inputs.
func buildTrackAndStack(ctx context.Context, in trackStackInput) error {
	cfg, em := in.cfg, in.emitter
	fps := cfg.Defaults.FPS
	colour := trackColour(cfg)
	items := cfg.VideoTrack

	// where each scene starts in the output, and how long the video is
	starts := make([]float64, len(in.clipPaths))
	total := 0.0
	for i := range in.clipPaths {
		if i == 0 {
			starts[i] = 0
		} else {
			starts[i] = total - in.join[i].Fade
		}
		total = starts[i] + in.clipLens[i]
	}

	// --- the items ---
	itemHash := make([]string, len(items))
	itemPath := make([]string, len(items))
	for i, it := range items {
		src := filepath.Join(config.SourcesSubdir, it.Clip)
		content, err := cache.FileSHA256(joinDir(in.base, src))
		if err != nil {
			return fail(em, "track:"+it.ID, fmt.Sprintf("track failed: cannot read the clip %s: %v", it.Clip, err))
		}
		build := trackBuildSeconds(items, i)
		speed, from, to := "auto", "start", "end"
		if it.ClipSpeed != nil {
			speed = strconv.FormatFloat(*it.ClipSpeed, 'f', -1, 64)
		}
		if it.ClipStart != nil {
			from = strconv.FormatFloat(*it.ClipStart, 'f', -1, 64)
		}
		if it.ClipEnd != nil {
			to = strconv.FormatFloat(*it.ClipEnd, 'f', -1, 64)
		}
		parts := []string{"item", content, from, to, speed, fmt.Sprintf("%.3f", build), fmt.Sprintf("%.3f", it.ScrimValue()), colour, fmt.Sprintf("%dx%d", in.width, in.height), strconv.Itoa(fps)}
		for _, l := range it.LocalScrims { // an item with none keeps the hash it always had
			parts = append(parts, fmt.Sprintf("local:%s:%.3f", l.Band, l.Scrim))
		}
		itemHash[i] = cache.TrackHash(parts...)
		itemPath[i] = TrackItemPath(it.ID)
		key := cache.TrackItemKey(it.ID)
		if cache.DecideTrack(in.manifest, in.base, key, itemHash[i], in.force) {
			em.Skipped(key, "track")
			continue
		}
		em.PhaseStart(key, "track")
		if err := in.buildItem(ctx, it, src, build, colour, fps, itemPath[i]); err != nil {
			return fail(em, key, fmt.Sprintf("track failed: %v", err))
		}
		in.manifest.UpdateTrack(key, itemHash[i], itemPath[i])
		em.PhaseEnd(key, "track", itemPath[i])
	}

	// --- the joined track: the items in order, the colour in the gaps, as long as the video ---
	var segs []assemble.TrackSegment
	cursor := 0.0
	gap := func(until float64) {
		if until-cursor > 0.001 {
			segs = append(segs, assemble.TrackSegment{Lavfi: assemble.GapSource(colour, in.width, in.height, fps, until-cursor), Duration: until - cursor})
			cursor = until
		}
	}
	joinParts := []string{colour, fmt.Sprintf("%.3f", total), fmt.Sprintf("%dx%d", in.width, in.height), strconv.Itoa(fps)}
	for i, it := range items {
		fade := trackFade(items, i)
		if fade == 0 {
			gap(it.Start)
		}
		segs = append(segs, assemble.TrackSegment{Path: itemPath[i], Duration: trackBuildSeconds(items, i), Fade: fade})
		cursor = it.End
		joinParts = append(joinParts, itemHash[i], fmt.Sprintf("%.3f/%.3f/%.3f", it.Start, it.End, fade))
	}
	gap(total)
	joinHash := cache.TrackHash(append([]string{"join"}, joinParts...)...)
	if cache.DecideTrack(in.manifest, in.base, cache.TrackJoinKey, joinHash, in.force) {
		em.Skipped(cache.TrackJoinKey, "track")
	} else {
		em.PhaseStart(cache.TrackJoinKey, "track")
		if _, err := in.run.Run(ctx, "ffmpeg", assemble.BuildTrackJoinArgs(segs, fps, TrackPath())); err != nil {
			return fail(em, cache.TrackJoinKey, fmt.Sprintf("track failed: cannot join the items: %v", err))
		}
		in.manifest.UpdateTrack(cache.TrackJoinKey, joinHash, TrackPath())
		em.PhaseEnd(cache.TrackJoinKey, "track", TrackPath())
	}

	// --- the stack ---
	layers := make([]assemble.StackLayer, len(in.clipPaths))
	stackParts := []string{joinHash, fmt.Sprintf("%.3f", total), strconv.Itoa(fps)}
	for i := range in.clipPaths {
		fadeOut := 0.0
		if i+1 < len(in.join) {
			fadeOut = in.join[i+1].Fade
		}
		layers[i] = assemble.StackLayer{Path: in.clipPaths[i], Start: starts[i], Seconds: in.clipLens[i], FadeIn: in.join[i].Fade, FadeOut: fadeOut, Alpha: cfg.Scenes[i].Layer}
		stackParts = append(stackParts, in.layerHash[i], fmt.Sprintf("%.3f/%.3f/%.3f/%.3f/%v", starts[i], in.clipLens[i], layers[i].FadeIn, layers[i].FadeOut, layers[i].Alpha))
	}
	stackHash := cache.TrackHash(append([]string{"stack"}, stackParts...)...)
	if cache.DecideTrack(in.manifest, in.base, cache.StackKey, stackHash, in.force) {
		em.Skipped("_global", "stack")
		return nil
	}
	em.PhaseStart("_global", "stack")
	if _, err := in.run.Run(ctx, "ffmpeg", assemble.BuildStackArgs(assemble.StackSpec{Track: TrackPath(), Layers: layers, Seconds: total, FPS: fps}, in.out)); err != nil {
		return fail(em, "_global", fmt.Sprintf("stack failed: %v", err))
	}
	in.manifest.UpdateTrack(cache.StackKey, stackHash, in.out)
	em.PhaseEnd("_global", "stack", in.out)
	return nil
}

// buildItem cuts the part of the clip the item plays (when it says one), and builds the item from it.
func (in trackStackInput) buildItem(ctx context.Context, it config.VideoTrackItem, src string, build float64, colour string, fps int, out string) error {
	length, err := probe.Duration(ctx, in.run, src)
	if err != nil {
		return fmt.Errorf("cannot read the clip %s: %w", it.Clip, err)
	}
	part, partSeconds := src, length.Seconds()
	if it.ClipStart != nil || it.ClipEnd != nil {
		start, end := 0.0, length.Seconds()
		if it.ClipStart != nil {
			start = *it.ClipStart
		}
		if it.ClipEnd != nil && *it.ClipEnd < end {
			end = *it.ClipEnd
		}
		if start >= length.Seconds() || end <= start {
			return fmt.Errorf("the part %.2f-%.2f s is beyond the end of %s, which lasts %.2f s", start, end, it.Clip, length.Seconds())
		}
		cut := filepath.Join("tmp", fmt.Sprintf("track_%s.cut.mp4", it.ID))
		if _, err := in.run.Run(ctx, "ffmpeg", assemble.BuildCutArgs(src, start, end-start, cut)); err != nil {
			return fmt.Errorf("cannot cut the part of %s: %w", it.Clip, err)
		}
		part, partSeconds = cut, end-start
	}
	spec := assemble.TrackItemSpec{Part: part, PartSeconds: partSeconds, Seconds: build, Width: in.width, Height: in.height, Scrim: it.ScrimValue(), ScrimColour: colour, LocalScrims: it.LocalScrims}
	if it.ClipSpeed != nil {
		spec.Speed = *it.ClipSpeed
	}
	if _, err := in.run.Run(ctx, "ffmpeg", assemble.BuildTrackItemArgs(spec, fps, out)); err != nil {
		return fmt.Errorf("cannot build the item %q: %w", it.ID, err)
	}
	return nil
}

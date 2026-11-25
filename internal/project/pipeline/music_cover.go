package pipeline

import (
	"context"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"

	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/runner"
	"veritmo/internal/platform/sched"
	"veritmo/internal/project/config"
	"veritmo/internal/video/render"
)

// coverNegative is what a cover must not have: text, logos and frames, which image models draw badly. The models of
// the turbo family run at cfg 1 and ignore it, so the positive prompt does not mention them either (naming a
// thing in a prompt tends to draw it).
const coverNegative = "text, letters, words, caption, watermark, logo, signature, typography, advisory label, sticker, border, frame, blurry, low quality"

// coverPrompt is the prompt of a cover: the style the song was asked for, as album art.
func coverPrompt(caption string) string {
	return "Music album artwork, " + caption + ". Cinematic lighting, highly detailed, full-bleed composition."
}

// coverSeed is the seed of a track's cover: the track's own, or one made from its id, so the same track always
// asks for the same picture and the cache holds.
func coverSeed(t config.Track) int64 {
	if t.Seed != nil {
		return int64(*t.Seed)
	}
	h := fnv.New32a()
	h.Write([]byte(t.ID))
	return int64(h.Sum32() & 0x7fffffff)
}

// coverConfig is the project with what covers need filled in: square album art, a sampler and some steps when the
// project (which is a music one, with no pictures of its own) sets none. The prompt prefixes of videos do not apply.
func coverConfig(cfg *config.Config) *config.Config {
	c := *cfg
	d := c.Defaults
	if d.Resolution == "" {
		d.Resolution = config.DefaultCoverResolution
	}
	if d.Sampler == "" {
		d.Sampler = "euler"
	}
	if d.Steps == 0 {
		d.Steps = config.DefaultCoverSteps
	}
	d.SubjectPrompt, d.NegativePrompt = "", ""
	c.Defaults = d
	return &c
}

// ensureCover makes (or finds in the cache) the cover of a track made with the project's image model, and returns
// its path inside the project. It never fails the song: when the picture cannot be made the answer is "" and a
// warning says why, so the song is exported without a cover.
func ensureCover(ctx context.Context, st *runState, cfg *config.Config, track config.Track, force bool, execRunner runner.CommandRunner, emitter *events.Emitter, env []string, base string) string {
	ccfg := coverConfig(cfg)
	caption := track.Prompt
	if m := cfg.Defaults.Music; m != nil {
		caption = m.Caption(track.Prompt)
	}
	scene := config.Scene{ID: "cover_" + track.ID, Prompt: coverPrompt(caption), NegativePrompt: coverNegative}
	key := scene.ID
	_, png, _ := cache.ArtifactPaths(key)
	hash := cache.ImageHash(SceneImageParams(ccfg, scene, coverSeed(track), 0))
	st.mu.Lock()
	e0, known := st.manifest.Scenes[key]
	st.mu.Unlock()
	if _, err := os.Stat(joinDir(base, png)); !force && known && e0.ImageHash == hash && e0.PngPath == png && err == nil {
		emitter.Skipped(track.ID, "cover")
		return png
	}
	release, err := st.reserve(ctx, sched.Estimate(renderFootprint(ccfg, base)))
	if err != nil {
		emitter.Warn(track.ID, fmt.Sprintf("cover skipped: %v", err))
		return ""
	}
	defer release()
	emitter.PhaseStart(track.ID, "cover")
	res, err := render.RunVariant(ctx, execRunner, scene, ccfg.Defaults, 0, coverSeed(track), png, env)
	if err != nil {
		emitter.Warn(track.ID, fmt.Sprintf("cover skipped: %v", err))
		return ""
	}
	if note := render.ShrunkNote(scene.EffectiveRender(ccfg.Defaults).Resolution, res); note != "" {
		emitter.Warn(track.ID, note)
	}
	st.mu.Lock()
	e := st.manifest.Scenes[key]
	e.ImageHash, e.PngPath = hash, png
	st.manifest.UpdateEntry(key, e)
	st.mu.Unlock()
	emitter.PhaseEnd(track.ID, "cover", png)
	return png
}

// coverExportName is where a track's cover is copied in out/<project>/ next to its songs.
func coverExportName(exportDir, trackID string) string {
	return filepath.Join(exportDir, trackID+".png")
}

package pipeline

// blenderscene.go: the scenes made from a .blend file (spec blender-engine). Blender renders the scene's frames on
// the card the run gives it (or the CPU), ffmpeg joins them into a video as long as the scene, and from there the
// scene is a clip like any other.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"veritmo/internal/platform/legacyname"

	"veritmo/internal/music/musicmotion/fonts"
	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
	"veritmo/internal/video/assemble"
	"veritmo/internal/video/blender"
)

// Seams for the tests, which have no Blender to hand.
var (
	detectBlender = blender.Cached
	renderBlender = blender.Render
	joinBlender   = blender.Join
)

// blenderRAMExtra is what Cycles needs on top of the size of the file: its memory is not predictable from the file
// alone, so a generous fixed amount is reserved.
const blenderRAMExtra = 2 << 30

// BlenderPath is where the video of a Blender scene is written, relative to the project.
func BlenderPath(sceneID string) string {
	return filepath.Join("tmp", fmt.Sprintf("scene_%s.blender.mp4", sceneID))
}

// BlenderFramesDir is where the frames of a Blender scene are written, relative to the project.
func BlenderFramesDir(sceneID string) string {
	return filepath.Join("tmp", fmt.Sprintf("scene_%s.blender", sceneID))
}

// BlendPath is where a scene built from a description is kept as a .blend, relative to the project: next to the scene's
// clip, and downloadable through the artifacts of the project.
func BlendPath(sceneID string) string {
	return filepath.Join("tmp", fmt.Sprintf("scene_%s.blend", sceneID))
}

// blenderFile is the file of the sources the scene starts from: its .blend, or its description.
func blenderFile(scene config.Scene) string {
	if scene.Blender.IsBuild() {
		return filepath.Join(config.SourcesSubdir, scene.Blender.Build)
	}
	return filepath.Join(config.SourcesSubdir, scene.Blender.File)
}

// blenderPalette is the bg, fg and accent a description can name: the project's design (its two tones and its accent),
// or the fixed palette of the engine when the project has none.
func blenderPalette(cfg *config.Config) blender.Palette {
	if m := cfg.Defaults.Motion; m != nil && m.Design != nil {
		return blender.PaletteFromDesign(m.Design.Dark, m.Design.Light, m.Design.Accent)
	}
	return blender.DefaultPalette
}

// blenderFontDir is where the fonts of the design are kept once decoded to TrueType for Blender (a variable for the tests).
var blenderFontDir = func() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(legacyname.Dir(dir), "fonts")
}

// blenderFaces are the files of the font pack that the project's design stands for: the display font at weight 700 when
// the pack has it (else 400) and the body font at 400. A role is missing when the design has none or its font is a system
// stack, which has no file.
func blenderFaces(cfg *config.Config) map[string]fonts.Face {
	out := map[string]fonts.Face{}
	m := cfg.Defaults.Motion
	if m == nil || m.Design == nil {
		return out
	}
	if f, ok := fonts.Pick(m.Design.Display, 700); ok {
		out[blender.FontDisplay] = f
	}
	if f, ok := fonts.Pick(m.Design.Body, 400); ok {
		out[blender.FontBody] = f
	}
	return out
}

// blenderEnv is what a description is checked against in a project that is being run: the design's colours and fonts, the
// project's tempo (defaults.motion.bpm, none when absent) and the sources folder under base (the current folder when base
// is empty: the command line runs a scenes.yaml in place, and it has sources like any project).
func blenderEnv(cfg *config.Config, base string) blender.Env {
	env := blender.Env{Palette: blenderPalette(cfg), Sources: joinDir(base, config.SourcesSubdir), DesignFonts: map[string]bool{}}
	if m := cfg.Defaults.Motion; m != nil && m.BPM != nil {
		env.BPM = *m.BPM
	}
	for role := range blenderFaces(cfg) {
		env.DesignFonts[role] = true
	}
	return env
}

// BlenderEnv is what a scene description is checked against in this project (see blenderEnv): the design's colours and
// fonts, the project's tempo and the sources folder under base, the project's folder. An empty base here means there is NO
// project (a scene written or checked on its own: the one-call generation, the stateless writer, a piece of a music motion
// script): it has no sources, so a font or a model file is refused.
func BlenderEnv(cfg *config.Config, base string) blender.Env {
	env := blenderEnv(cfg, base)
	env.NoFiles = base == ""
	return env
}

// BlenderFamilies names the family of each font role the design has in the pack ("display": "Playfair Display"), for
// what is said to a model about the project.
func BlenderFamilies(cfg *config.Config) map[string]string {
	out := map[string]string{}
	for role, face := range blenderFaces(cfg) {
		out[role] = face.Family
	}
	return out
}

// BlenderFontPaths decodes to TrueType, in the font cache, the fonts of the design that a description uses (display and
// body) and returns the file of each by role.
func BlenderFontPaths(cfg *config.Config, spec *blender.Spec) (map[string]string, error) {
	faces := blenderFaces(cfg)
	out := map[string]string{}
	for _, role := range spec.FontRoles() {
		path, err := blender.EnsureTTF(blenderFontDir(), faces[role].WOFF2)
		if err != nil {
			return nil, fmt.Errorf("the %s font: %w", role, err)
		}
		out[role] = path
	}
	return out, nil
}

// BlendMissing says that a scene built from a description has no kept .blend: its render is not reused then, because
// the .blend is part of what the scene gives.
func BlendMissing(base string, scene config.Scene) bool {
	if scene.Blender == nil || !scene.Blender.IsBuild() {
		return false
	}
	info, err := os.Stat(joinDir(base, BlendPath(scene.ID)))
	return err != nil || !info.Mode().IsRegular()
}

// BlenderImageHash is the image hash of a Blender scene: it follows what decides the render (see cache.BlenderParams)
// and nothing else, so an unchanged scene is not rendered again.
func BlenderImageHash(cfg *config.Config, scene config.Scene, base, audioHash string) string {
	b := scene.Blender
	length := audioHash
	if scene.IsSilent() {
		length = fmt.Sprintf("silence=%v", scene.SilenceDuration())
	}
	inject, _ := json.Marshal(b.Inject) // a map is encoded with its keys sorted
	version := "unavailable"
	if info, err := detectBlender(context.Background()); err == nil {
		version = info.Version
	}
	p := cache.BlenderParams{
		FileSHA256: fileSHA(joinDir(base, blenderFile(scene))),
		Inject:     string(inject),
		Samples:    b.ResolvedSamples(),
		Frames:     b.Frames,
		Resolution: cfg.Defaults.Resolution,
		FPS:        cfg.Defaults.FPS,
		Length:     length,
		Version:    version,
	}
	if b.IsBuild() {
		// A built scene is what its RESOLVED description says (beats in seconds with the project's tempo, colours as
		// #rrggbb with the design's) and the fonts it uses. A description that cannot be read or resolved still gets
		// a key, from its file (the run then fails naming the fault).
		p.Build, p.FileSHA256 = p.FileSHA256, ""
		if data, err := os.ReadFile(joinDir(base, blenderFile(scene))); err == nil {
			if spec, err := blender.Resolve(data, blenderEnv(cfg, base)); err == nil {
				if resolved, err := spec.JSON(); err == nil {
					sum := sha256.Sum256(resolved)
					p.Build = hex.EncodeToString(sum[:])
				}
				p.Font = blenderFontKey(spec, cfg, base)
				p.Models = spec.ModelsHash(joinDir(base, config.SourcesSubdir))
			}
		}
	}
	return cache.ImageHash(cache.ImageParams{Resolution: cfg.Defaults.Resolution, SourceImage: "blender:" + cache.BlenderHash(p)})
}

// blenderFontKey is the identity of the fonts a description uses: the content of the font files of the sources it names
// and the WOFF2 of the design's fonts it takes with display and body ("" when it uses none).
func blenderFontKey(spec *blender.Spec, cfg *config.Config, base string) string {
	files := spec.FontsHash(joinDir(base, config.SourcesSubdir))
	faces := blenderFaces(cfg)
	var parts []string
	for _, role := range spec.FontRoles() {
		parts = append(parts, role+"="+blender.FontSHA(faces[role].WOFF2))
	}
	if files == "" && len(parts) == 0 {
		return ""
	}
	return files + "|" + strings.Join(parts, ";")
}

// fileSHA is the SHA-256 of a file's content, or "unreadable" when it cannot be read.
func fileSHA(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return "unreadable"
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "unreadable"
	}
	return hex.EncodeToString(h.Sum(nil))
}

// blenderBytes is the RAM a Blender scene reserves: its file and a fixed margin.
func blenderBytes(base string, scene config.Scene) int64 {
	var size int64
	if info, err := os.Stat(joinDir(base, blenderFile(scene))); err == nil {
		size = info.Size()
	}
	return size + blenderRAMExtra
}

// gpuPinned says whether the run's environment gives the render a card: CUDA_VISIBLE_DEVICES set to something.
func gpuPinned(env []string) bool {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "CUDA_VISIBLE_DEVICES="); ok {
			return strings.TrimSpace(v) != ""
		}
	}
	return false
}

// renderBlenderScene renders a Blender scene to a video exactly as long as the scene and writes the video's first
// frame as the scene's picture.
func renderBlenderScene(ctx context.Context, r runner.CommandRunner, emitter *events.Emitter, cfg *config.Config, base string, scene config.Scene, width, height, fps int, seconds float64, env []string, pngPath string) error {
	info, err := detectBlender(ctx)
	if err != nil {
		return err
	}
	b := scene.Blender
	frames := BlenderFramesDir(scene.ID)
	params := blender.Params{
		Out: frames, Dir: base, Samples: b.ResolvedSamples(), Device: b.ResolvedDevice(), GPU: gpuPinned(env),
		Width: width, Height: height, Count: int(math.Ceil(seconds * float64(fps))),
	}
	if b.IsBuild() {
		// The description is checked here, before Blender starts: a fault is named by its path in the JSON. What
		// comes back is the resolved description, which is what Blender builds.
		spec, err := blender.Load(joinDir(base, blenderFile(scene)), seconds, blenderEnv(cfg, base))
		if err != nil {
			return fmt.Errorf("scene %q: %s: %w", scene.ID, b.Build, err)
		}
		fonts, err := BlenderFontPaths(cfg, spec)
		if err != nil {
			return fmt.Errorf("scene %q: %w", scene.ID, err)
		}
		params.Fonts = fonts
		params.Spec, params.Sources, params.BlendOut = spec, config.SourcesSubdir, BlendPath(scene.ID)
		params.FPS = fps
		_ = os.Remove(joinDir(base, params.BlendOut)) // the one kept is the one of this render
	} else {
		params.File, params.Inject, params.Frames = blenderFile(scene), b.Inject, b.Frames
	}
	res, err := renderBlender(ctx, r, info.Path, params, env)
	if err != nil {
		if b.IsBuild() && !BlendMissing(base, scene) {
			return fmt.Errorf("the render failed (the built scene was kept as %s): %w", BlendPath(scene.ID), err)
		}
		return err
	}
	if res.Device == "CPU" && b.ResolvedDevice() == config.BlenderAuto {
		emitter.Warn(scene.ID, "Blender rendered on the CPU because the run gave it no GPU: it is slower")
	}
	out := BlenderPath(scene.ID)
	if err := joinBlender(ctx, r, base, frames, fps, seconds, out); err != nil {
		return err
	}
	if _, err := r.Run(ctx, "ffmpeg", assemble.BuildPosterArgs(out, width, height, 0, pngPath)); err != nil {
		return fmt.Errorf("scene %q: cannot take the first frame of the Blender render: %w", scene.ID, err)
	}
	_ = os.RemoveAll(joinDir(base, frames)) // the video is what is kept
	return nil
}

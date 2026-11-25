package pipeline

// shadertransition.go: the joins of a project whose transition is a shader one (`transition: shader:domain-warp`, see internal/video/shadertrans).
// The end of the clip before and the start of the clip after are cut to pictures, Chrome mixes them with the shader and the video of the
// transition goes to the join, where it takes the place of the xfade picture. A transition that cannot be drawn is a crossfade, with a warning.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"veritmo/internal/platform/events"
	"veritmo/internal/project/config"
	"veritmo/internal/video/assemble"
	"veritmo/internal/video/render"
	"veritmo/internal/video/shadertrans"
)

// renderShaderJoins draws the shader transitions of the join list and sets their videos. tmpDir is project-relative; the clips are too (ffmpeg runs
// in the project folder), base is the project folder for what Go reads.
func renderShaderJoins(ctx context.Context, cfg *config.Config, join []assemble.JoinClip, ff assemble.CommandRunner, base, tmpDir string, emitter *events.Emitter) {
	width, height, err := render.ParseResolution(cfg.Defaults.Resolution)
	if err != nil {
		return
	}
	fps := cfg.Defaults.FPS
	if fps <= 0 {
		fps = 30
	}
	for i := 1; i < len(join); i++ {
		shader, ok := shadertrans.ShaderOf(join[i].Kind)
		if !ok || join[i].Fade <= 0 {
			continue
		}
		sceneID := cfg.Scenes[i].ID
		video, err := drawShaderJoin(ctx, join[i-1], join[i], shader, width, height, fps, ff, base, tmpDir, i)
		if err != nil {
			emitter.Warn(sceneID, fmt.Sprintf("the shader transition %q into this scene could not be drawn (%v): a crossfade is used", shader, err))
			join[i].Kind = "fade"
			continue
		}
		join[i].ShaderVideo = video
	}
}

func drawShaderJoin(ctx context.Context, from, to assemble.JoinClip, shader string, width, height, fps int, ff assemble.CommandRunner, base, tmpDir string, i int) (string, error) {
	br, err := findBrowser()
	if err != nil {
		return "", err
	}
	n := int(to.Fade*float64(fps) + 0.5)
	if n < 2 {
		return "", fmt.Errorf("a transition of %.2f s has fewer than two frames at %d fps", to.Fade, fps)
	}
	cut := func(label, clip string, start float64) ([][]byte, error) {
		rel := filepath.Join(tmpDir, fmt.Sprintf("shader_%d_%s", i, label))
		abs := joinDir(base, rel)
		_ = os.RemoveAll(abs)
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return nil, err
		}
		vf := fmt.Sprintf("fps=%d,scale=%d:%d", fps, width, height)
		if _, stderr, err := ff.Run(ctx, []string{"-y", "-ss", fmt.Sprintf("%.3f", start), "-t", fmt.Sprintf("%.3f", to.Fade+0.2), "-i", clip, "-an", "-vf", vf, "-frames:v", fmt.Sprint(n), filepath.Join(rel, "%04d.png")}); err != nil {
			return nil, fmt.Errorf("cutting the frames of %s: %w (%s)", clip, err, strings.TrimSpace(stderr))
		}
		files, _ := filepath.Glob(filepath.Join(abs, "*.png"))
		sort.Strings(files)
		var out [][]byte
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				return nil, err
			}
			out = append(out, b)
		}
		_ = os.RemoveAll(abs)
		return out, nil
	}
	a, err := cut("a", from.Path, from.Duration-to.Fade)
	if err != nil {
		return "", err
	}
	b, err := cut("b", to.Path, 0)
	if err != nil {
		return "", err
	}
	if len(a) < n || len(b) < n {
		return "", fmt.Errorf("the clips gave %d and %d frames of the %d the transition needs", len(a), len(b), n)
	}
	rel := filepath.Join(tmpDir, fmt.Sprintf("shader_%d.mp4", i))
	if _, _, err := shadertrans.Render(ctx, shadertrans.Job{Browser: br.Path, Shader: shader, From: a[:n], To: b[:n], Width: width, Height: height, FPS: fps, Accent: to.Accent, Out: joinDir(base, rel)}); err != nil {
		return "", err
	}
	return rel, nil
}

package generation

// picture.go: one picture made from a prompt with the best image model installed on this machine, for what needs a picture before it can do its
// work and has no project to take the model from (the generation of a 3D model, spec model-generation).

import (
	"context"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strconv"

	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
	"veritmo/internal/server/modelcat"
	"veritmo/internal/server/workspace"
	"veritmo/internal/video/render"
)

// PictureMaker returns a function that makes a square picture of a prompt and gives it as PNG bytes. The model is the one the planner would make the
// first picture of a video with (photo, else general, else anime; the best ranked that is installed in modelsRoot/models and fits the biggest card);
// the prompt is used as it comes (the caller completes it) with the negative prompt asked. The same prompt gives the same picture. sd is run by r in
// the working folder, where models/ is, on the card with most free memory.
func PictureMaker(r runner.CommandRunner, cat []modelcat.Entry, modelsRoot string, gpus []workspace.GPUStatus, ramBytes int64, negative string) func(ctx context.Context, prompt string) ([]byte, error) {
	return func(ctx context.Context, prompt string) ([]byte, error) {
		m := MachineOf(gpus, ramBytes)
		installed := make([]catalogEntry, 0, len(cat))
		for _, e := range cat {
			e.Installed = len(e.Files) > 0 && filesExist(filepath.Join(modelsRoot, "models"), e)
			installed = append(installed, e)
		}
		defaults, err := pictureDefaults(installed, m)
		if err != nil {
			return nil, err
		}
		cfg := &config.Config{Defaults: defaults}
		dir, err := os.MkdirTemp("", "vt-picture-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(dir)
		out := filepath.Join(dir, "picture.png")
		var env []string
		if len(gpus) > 0 {
			best := gpus[0]
			for _, g := range gpus[1:] {
				if g.FreeVRAMMB > best.FreeVRAMMB {
					best = g
				}
			}
			env = append(env, "CUDA_VISIBLE_DEVICES="+strconv.Itoa(best.Index))
		}
		scene := config.Scene{ID: "picture", Prompt: prompt, NegativePrompt: negative}
		if _, err := render.RunVariant(ctx, r, scene, cfg.Defaults, 0, int64(crc32.ChecksumIEEE([]byte(prompt))), out, env); err != nil {
			return nil, err
		}
		return os.ReadFile(out)
	}
}

// pictureDefaults are the settings (model, size, sampler, steps) to draw a square picture with the best image model of the installed entries that fits the
// machine: the one the planner would make the first picture of a video with.
func pictureDefaults(installed []catalogEntry, m planMachine) (config.Defaults, error) {
	still := stillEntry(installed, m)
	if still == nil {
		return config.Defaults{}, fmt.Errorf("no image model is installed that fits this machine")
	}
	cfg := &config.Config{Defaults: config.Defaults{Sampler: "euler", Resolution: "1024x1024", Steps: 20}}
	applyStyleEntryTo(cfg, *still)
	res := shapeRes(cfg.Defaults.Resolution, 1, 1, 1, 64)
	if mp := maxPicturePixels(still, m); mp > 0 {
		res = fitRes(res, mp)
	}
	cfg.Defaults.Resolution = res
	return cfg.Defaults, nil
}

// PictureDefaults are the image settings a project takes to draw the pictures of its prompts with the best image model installed now (the 3D model
// generation API makes its projects with them).
func (h *Handler) PictureDefaults() (config.Defaults, error) {
	return pictureDefaults(h.catalogNow(), h.machineNow())
}

// filesExist says whether every file of an entry is in the folder.
func filesExist(dir string, e catalogEntry) bool {
	for _, f := range e.Files {
		if fi, err := os.Stat(filepath.Join(dir, f.Name)); err != nil || !fi.Mode().IsRegular() {
			return false
		}
	}
	return true
}

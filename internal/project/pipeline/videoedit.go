package pipeline

// videoedit.go is the pipeline of a video-edit project (spec video-edit-api): one phase, `edit`, that hands the
// source to the tool the project names and writes its result and a result.json next to it.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"veritmo/internal/platform/events"
	"veritmo/internal/project/config"
	"veritmo/internal/video/videoedit"
)

// VideoEditScene is the identifier the single phase of a video-edit run goes by in the events.
const VideoEditScene = "edit"

// VideoEditResultFile is the description of the result a tool made, written in the project's export folder.
const VideoEditResultFile = "result.json"

func runVideoEdit(ctx context.Context, cfg *config.Config, tools *videoedit.Catalog, emitter *events.Emitter, base string, gpus []string) (string, error) {
	ve := cfg.VideoEdit
	if ve == nil {
		return "", fail(emitter, VideoEditScene, "the project has no video_edit block")
	}
	if tools == nil {
		tools = videoedit.NewCatalog()
	}
	impl, ok := tools.ImplOf(ve.Tool)
	if !ok {
		return "", fail(emitter, VideoEditScene, fmt.Sprintf("the tool %s is not available in this build", ve.Tool))
	}
	settings, err := videoedit.ValidateSettings(ve.Tool, ve.Settings)
	if err != nil {
		return "", fail(emitter, VideoEditScene, err.Error())
	}
	dir, err := filepath.Abs(joinDir(base, ""))
	if err != nil {
		return "", fail(emitter, VideoEditScene, err.Error())
	}
	outDir := filepath.Join(dir, cfg.ExportDir())
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", fail(emitter, VideoEditScene, fmt.Sprintf("cannot create the output folder: %v", err))
	}
	job := videoedit.Job{
		Dir: dir, OutDir: outDir, Motion: ve.Motion, Settings: settings,
		Warn: func(msg string) { emitter.Warn(VideoEditScene, msg) },
	}
	if len(gpus) == 1 {
		job.GPU = gpus[0]
	}
	if ve.Source != "" {
		job.Video = filepath.Join(dir, config.SourcesSubdir, ve.Source)
	}
	if ve.Image != "" {
		job.Image = filepath.Join(dir, config.SourcesSubdir, ve.Image)
	}

	emitter.PhaseStart(VideoEditScene, "edit")
	res, err := impl.Run(ctx, job)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fail(emitter, VideoEditScene, fmt.Sprintf("%s failed: %v", ve.Tool, err))
	}
	if res.File == "" {
		return "", fail(emitter, VideoEditScene, fmt.Sprintf("%s finished without a result", ve.Tool))
	}
	if _, err := os.Stat(filepath.Join(outDir, res.File)); err != nil {
		return "", fail(emitter, VideoEditScene, fmt.Sprintf("%s did not leave its result: %v", ve.Tool, err))
	}
	raw, _ := json.MarshalIndent(res, "", "  ")
	if err := os.WriteFile(filepath.Join(outDir, VideoEditResultFile), raw, 0o644); err != nil {
		return "", fail(emitter, VideoEditScene, fmt.Sprintf("cannot write the description of the result: %v", err))
	}
	artifact := filepath.ToSlash(filepath.Join(cfg.ExportDir(), res.File))
	emitter.PhaseEnd(VideoEditScene, "edit", artifact)
	return artifact, nil
}

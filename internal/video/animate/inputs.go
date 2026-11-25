package animate

// inputs.go: the files besides the scene's picture that go to sd (spec video-generation-api): the last picture of a
// clip (MiniMax H3 first/last frame, LTX 2.3) and the references (MiniMax H3 Ref2VA). The pipeline's files are
// prepared here with ffmpeg into what the engine reads, next to the clip's raw output.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
	"veritmo/internal/video/render"
)

// RefVideoFPS is the rate sd takes the frames of a reference video as.
const RefVideoFPS = 24

// RefAudioRate is the sample rate sd brings reference audio to (it reads any WAV; this is what it uses).
const RefAudioRate = 32000

// Inputs are the prepared files of a clip besides its first picture: paths ready to give to sd.
type Inputs struct {
	EndImage  string   // the last picture, fitted to the canvas
	RefImages []string // reference pictures after the scene's own (which is reference picture 1)
	RefVideos []string // folders of PNG frames, 24 fps
	RefAudios []string // WAV files
}

// Pictures is how many reference pictures the prompt names: the scene's own and the others.
func (in Inputs) Pictures() int { return 1 + len(in.RefImages) }

// EndFittedPath is where the last picture goes once fitted: tmp/anim_<scene>.avi -> tmp/anim_<scene>_end.png.
func EndFittedPath(rawPath string) string { return strings.TrimSuffix(rawPath, ".avi") + "_end.png" }

// RefsDir is the folder of a clip's prepared references.
func RefsDir(rawPath string) string { return strings.TrimSuffix(rawPath, ".avi") + "_refs" }

// BuildRefImageArgs scales a reference picture down to fit the canvas w x h by area, keeping its aspect (sd only
// downscales the ones larger than the canvas, and never crops them).
func BuildRefImageArgs(src, out string, w, h int) []string {
	f := fmt.Sprintf("min(1,sqrt(%d*%d/(iw*ih)))", w, h)
	return []string{
		"-y", "-i", src,
		"-vf", fmt.Sprintf("scale=w='trunc(iw*%s/2)*2':h='trunc(ih*%s/2)*2'", f, f),
		"-frames:v", "1", out,
	}
}

// BuildRefVideoArgs writes the frames of a reference video as PNG files at 24 fps, scaled to the canvas height.
func BuildRefVideoArgs(src, dir string, h int) []string {
	return []string{
		"-y", "-i", src, "-an",
		"-vf", fmt.Sprintf("fps=%d,scale=-2:%d", RefVideoFPS, h),
		filepath.Join(dir, "%05d.png"),
	}
}

// BuildRefAudioArgs brings a reference audio to a stereo 32 kHz WAV.
func BuildRefAudioArgs(src, out string) []string {
	return []string{"-y", "-i", src, "-vn", "-ac", "2", "-ar", strconv.Itoa(RefAudioRate), "-c:a", "pcm_s16le", out}
}

// BuildForwardArgs converts rawPath into an MP4 played once, forward: a clip that ends on a given picture must not be
// played back in reverse.
func BuildForwardArgs(rawPath, outPath string) []string {
	return []string{
		"-y", "-i", rawPath,
		"-vf", "format=yuv420p", "-an",
		"-c:v", "libx264", "-crf", "12", "-preset", "fast",
		outPath,
	}
}

// at is a path of the project as the file system sees it from the server: inside dir unless it is absolute.
func at(dir, p string) string {
	if dir == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(dir, p)
}

// PrepareInputs makes the files of the scene's last picture and references for sd, with ffmpeg through r, at the
// canvas of the animation resolution. sources are resolved against the working directory like the scene's own clip.
//
// dir is the project's folder, where the runner runs ffmpeg: the folders this makes are created inside it (an empty dir
// is the working directory), not wherever the server happens to run.
func PrepareInputs(ctx context.Context, r runner.CommandRunner, scene config.Scene, a config.Animate, dir, rawPath string) (Inputs, error) {
	var in Inputs
	if scene.EndImage == "" && scene.References.Empty() {
		return in, nil
	}
	w, h, err := render.ParseResolution(a.Resolution)
	if err != nil {
		return in, err
	}
	src := func(name string) string { return filepath.Join(config.SourcesSubdir, name) }
	run := func(what string, args []string) error {
		if _, err := r.Run(ctx, FFmpegBinary, args); err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		return nil
	}
	if scene.EndImage != "" {
		in.EndImage = EndFittedPath(rawPath)
		if err := run("fitting the last picture", BuildFitArgs(src(scene.EndImage), in.EndImage, w, h)); err != nil {
			return in, err
		}
	}
	refs := scene.References
	if refs.Empty() {
		return in, nil
	}
	refs0 := RefsDir(rawPath)
	if err := os.RemoveAll(at(dir, refs0)); err != nil {
		return in, err
	}
	if err := os.MkdirAll(at(dir, refs0), 0o755); err != nil {
		return in, err
	}
	for i, name := range refs.Images {
		out := filepath.Join(refs0, fmt.Sprintf("image-%d.png", i+1))
		if err := run("preparing reference picture "+name, BuildRefImageArgs(src(name), out, w, h)); err != nil {
			return in, err
		}
		in.RefImages = append(in.RefImages, out)
	}
	for i, name := range refs.Videos {
		frames := filepath.Join(refs0, fmt.Sprintf("video-%d", i+1))
		if err := os.MkdirAll(at(dir, frames), 0o755); err != nil {
			return in, err
		}
		if err := run("preparing reference video "+name, BuildRefVideoArgs(src(name), frames, h)); err != nil {
			return in, err
		}
		in.RefVideos = append(in.RefVideos, frames)
	}
	for i, name := range refs.Audios {
		out := filepath.Join(refs0, fmt.Sprintf("audio-%d.wav", i+1))
		if err := run("preparing reference audio "+name, BuildRefAudioArgs(src(name), out)); err != nil {
			return in, err
		}
		in.RefAudios = append(in.RefAudios, out)
	}
	return in, nil
}

// miniMaxReferencesPrompt is the sentence that names the reference pictures the engine reads as <Picture N>; one
// picture is the sentence the engine always had.
func miniMaxReferencesPrompt(pictures int) string {
	if pictures <= 1 {
		return MiniMaxPromptPrefix
	}
	names := make([]string, pictures)
	for i := range names {
		names[i] = fmt.Sprintf("<Picture %d>", i+1)
	}
	return "Use the images " + strings.Join(names, ", ") + " as references: keep their subjects, appearance and style unchanged. "
}

// refArgs are the sd arguments of the references, in the order the engine presents them: pictures, videos, audios.
func refArgs(in Inputs) []string {
	var out []string
	for _, p := range in.RefImages {
		out = append(out, Flags.RefImage, p)
	}
	for _, v := range in.RefVideos {
		out = append(out, Flags.RefVideo, v)
	}
	for _, a := range in.RefAudios {
		out = append(out, Flags.RefAudio, a)
	}
	return out
}

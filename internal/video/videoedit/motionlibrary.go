package videoedit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"veritmo/internal/video/motionlib"
)

// Library is the library of motions as motion control uses it: it gives a window of the control of a ready motion.
type Library interface {
	Window(id string, start float64) (motionlib.Window, error)
}

// peopleSamples is how many frames of a video are looked at to say whether anybody is in it.
const peopleSamples = 8

// CheckPeople looks at a few frames spread over a video and says whether anybody is in it: ErrNoPeople when nobody
// is. It is the quick check a request is refused by; the whole video is only processed afterwards, in the background.
func CheckPeople(ctx context.Context, v Vision, video string, duration float64, work string) error {
	if v == nil {
		return fmt.Errorf("the vision service is not set up")
	}
	frames := filepath.Join(work, "check-frames")
	if err := os.RemoveAll(frames); err != nil {
		return err
	}
	if err := os.MkdirAll(frames, 0o755); err != nil {
		return err
	}
	rate := float64(peopleSamples) / duration
	if duration <= 0 {
		rate = 1
	}
	if _, err := extractFrames(ctx, video, frames, rate); err != nil {
		return err
	}
	sum, err := v.Mask(ctx, frames, filepath.Join(work, "check-masks"))
	if err != nil {
		return err
	}
	if sum.FramesWithoutPeople != nil && *sum.FramesWithoutPeople >= sum.Frames {
		return ErrNoPeople
	}
	return nil
}

// MotionMade is what preparing a motion gives.
type MotionMade struct {
	Frames        int
	People        int
	Width, Height int
}

// libraryFramesArgs extracts every frame of a video at 16 per second, fitted into w x h with black bars, up to the
// longest a motion may be.
func libraryFramesArgs(video string, w, h int, dir string) []string {
	filter := fmt.Sprintf("fps=%d,scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1", MotionFPS, w, h, w, h)
	return []string{"-hide_banner", "-loglevel", "error", "-y", "-i", video, "-an", "-t", trimFloat(float64(motionlib.MaxSeconds) + 0.5),
		"-vf", filter, "-frames:v", fmt.Sprint(motionlib.MaxSeconds * MotionFPS), filepath.Join(dir, "%04d.png")}
}

// previewArgs makes the small preview of a motion: the picture of the video, low, without sound.
func previewArgs(video, out string) []string {
	return []string{"-hide_banner", "-loglevel", "error", "-y", "-i", video, "-an", "-t", trimFloat(float64(motionlib.MaxSeconds) + 0.5),
		"-vf", "scale=-2:240,fps=16", "-c:v", "libx264", "-preset", "veryfast", "-crf", "28", "-pix_fmt", "yuv420p", "-movflags", "+faststart", out}
}

// median is the middle value of the counts (the lower one when there is an even number).
func median(v []int) int {
	if len(v) == 0 {
		return 0
	}
	c := append([]int(nil), v...)
	sort.Ints(c)
	return c[(len(c)-1)/2]
}

// PrepareMotion makes what a motion keeps: the control of the whole video (the depth of the people, black elsewhere,
// at 16 per second, in the shape of the video) in controlDir and a small preview. It fails when nobody is found, or
// when the control is not complete. work is a folder of the caller's for what is not kept (the frames of the video).
func PrepareMotion(ctx context.Context, v Vision, video string, info Info, controlDir, previewPath, work string) (MotionMade, error) {
	if v == nil {
		return MotionMade{}, fmt.Errorf("the vision service is not set up")
	}
	w, h := ShapeFor(info.Width, info.Height)
	frames := filepath.Join(work, "frames")
	for _, d := range []string{frames, controlDir} {
		if err := os.RemoveAll(d); err != nil {
			return MotionMade{}, err
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			return MotionMade{}, err
		}
	}
	if err := runFFmpeg(ctx, libraryFramesArgs(video, w, h, frames)); err != nil {
		return MotionMade{}, err
	}
	made, _ := filepath.Glob(filepath.Join(frames, "*.png"))
	if len(made) < MotionFrames {
		return MotionMade{}, fmt.Errorf("the video gives %d frames at %d per second: a motion needs at least %d (%.1f s)", len(made), MotionFPS, MotionFrames, MotionSeconds)
	}
	sum, err := v.Control(ctx, frames, controlDir, ControlDilate)
	if err != nil {
		return MotionMade{}, err
	}
	if sum.FramesWithoutPeople != nil && *sum.FramesWithoutPeople >= sum.Frames {
		return MotionMade{}, ErrNoPeople
	}
	if err := checkControlFrames(controlDir, len(made)); err != nil {
		return MotionMade{}, err
	}
	var counts []int
	for _, m := range sum.Masks {
		if m.People > 0 {
			counts = append(counts, m.People)
		}
	}
	if err := runFFmpeg(ctx, previewArgs(video, previewPath)); err != nil {
		return MotionMade{}, err
	}
	return MotionMade{Frames: len(made), People: median(counts), Width: w, Height: h}, nil
}

// refitArgs cuts the window out of the stored control and fits it into w x h with black bars: the bars are the colour of
// the control's background, so nothing is added to it.
func refitArgs(win motionlib.Window, w, h int, dir string) []string {
	filter := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease:flags=bicubic,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black,format=rgb24", w, h, w, h)
	return []string{"-hide_banner", "-loglevel", "error", "-y", "-start_number", fmt.Sprint(win.First + 1), "-framerate", fmt.Sprint(MotionFPS),
		"-i", filepath.Join(win.Dir, "%04d.png"), "-frames:v", fmt.Sprint(MotionFrames), "-vf", filter, filepath.Join(dir, "%04d.png")}
}

// RefitControl writes into dir the 33 frames of a window of a motion's control, fitted to w x h, and checks them.
func RefitControl(ctx context.Context, win motionlib.Window, w, h int, dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := runFFmpeg(ctx, refitArgs(win, w, h, dir)); err != nil {
		return err
	}
	return CheckControlDir(dir)
}

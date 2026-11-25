// Package probe asks ffprobe about a media file: how long it is, how loud it is, how big a picture is. Every
// function has a twin that only builds the ffprobe arguments, so a test checks the command line without running the
// program; the calls go through a [runner.CommandRunner], never through os/exec directly.
package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"veritmo/internal/platform/runner"
)

// Flags centralizes every ffprobe CLI flag in one place so they are easy to
// correct later.
var Flags = struct {
	LogLevel    string
	ShowEntries string
	OutputFmt   string
}{
	LogLevel:    "-v",
	ShowEntries: "-show_entries",
	OutputFmt:   "-of",
}

// BuildArgs constructs the ffprobe CLI argument slice that reports a media
// file's duration (in seconds) as JSON.
func BuildArgs(path string) []string {
	return []string{
		Flags.LogLevel, "error",
		Flags.ShowEntries, "format=duration",
		Flags.OutputFmt, "json",
		path,
	}
}

// probeFormat mirrors the subset of ffprobe's JSON output used here.
type probeFormat struct {
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

// Duration invokes ffprobe via the supplied CommandRunner and returns the
// duration of the media file at path. CommandRunner errors (including
// captured stderr) are propagated unchanged.
func Duration(ctx context.Context, r runner.CommandRunner, path string) (time.Duration, error) {
	args := BuildArgs(path)

	stdout, err := r.Run(ctx, "ffprobe", args)
	if err != nil {
		return 0, err
	}

	var parsed probeFormat
	if err := json.Unmarshal(stdout, &parsed); err != nil {
		return 0, fmt.Errorf("probe: parsing ffprobe output: %w", err)
	}

	seconds, err := strconv.ParseFloat(parsed.Format.Duration, 64)
	if err != nil {
		return 0, fmt.Errorf("probe: parsing duration %q: %w", parsed.Format.Duration, err)
	}

	return time.Duration(seconds * float64(time.Second)), nil
}

// LoudnessArgs returns the ffmpeg arguments that measure the integrated
// loudness (EBU R128) of a file and print it, as filter metadata, on stdout.
func LoudnessArgs(path string) []string {
	return []string{
		"-v", "error", "-nostats", "-i", path, "-vn",
		"-af", "ebur128=metadata=1,ametadata=mode=print:key=lavfi.r128.I:file=-",
		"-f", "null", "-",
	}
}

// ErrSilent is returned by Loudness for audio with no measurable loudness.
var ErrSilent = errors.New("probe: the audio is silent")

// Loudness measures the integrated loudness of the media file at path in LUFS.
// It reads the whole file. Audio that is silent (or too quiet to pass the
// loudness gate) gives ErrSilent.
func Loudness(ctx context.Context, r runner.CommandRunner, path string) (float64, error) {
	out, err := r.Run(ctx, "ffmpeg", LoudnessArgs(path))
	if err != nil {
		return 0, err
	}
	last := ""
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "lavfi.r128.I="); ok {
			last = v // the filter reports the loudness so far each time: the last one is the whole file
		}
	}
	if last == "" {
		return 0, fmt.Errorf("probe: ffmpeg reported no loudness")
	}
	v, err := strconv.ParseFloat(last, 64)
	if err != nil || math.IsInf(v, 0) || math.IsNaN(v) || v <= -70 {
		return 0, ErrSilent
	}
	return v, nil
}

// ImageSizeArgs returns the ffprobe arguments that report the width and height of the first video stream (a
// picture counts as one) and how the first frame is rotated: a phone stores a vertical photo sideways with a
// rotation mark (EXIF), which only the frame reports.
func ImageSizeArgs(path string) []string {
	return []string{
		Flags.LogLevel, "error",
		"-select_streams", "v:0",
		"-read_intervals", "%+#1",
		Flags.ShowEntries, "stream=width,height:frame_side_data=rotation",
		Flags.OutputFmt, "json",
		path,
	}
}

// ImageSize is the width and height of a picture as it is seen: a photo marked as turned a quarter (90 or 270
// degrees) has its sides swapped, so a vertical photo of a phone is vertical here too.
func ImageSize(ctx context.Context, r runner.CommandRunner, path string) (width, height int, err error) {
	stdout, err := r.Run(ctx, "ffprobe", ImageSizeArgs(path))
	if err != nil {
		return 0, 0, err
	}
	var parsed struct {
		Streams []struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"streams"`
		Frames []struct {
			SideData []struct {
				Rotation *float64 `json:"rotation"`
			} `json:"side_data_list"`
		} `json:"frames"`
	}
	if err := json.Unmarshal(stdout, &parsed); err != nil {
		return 0, 0, fmt.Errorf("probe: parsing ffprobe output: %w", err)
	}
	if len(parsed.Streams) == 0 || parsed.Streams[0].Width <= 0 || parsed.Streams[0].Height <= 0 {
		return 0, 0, fmt.Errorf("probe: %s has no picture size", path)
	}
	width, height = parsed.Streams[0].Width, parsed.Streams[0].Height
	for _, f := range parsed.Frames {
		for _, sd := range f.SideData {
			if sd.Rotation != nil {
				if int(math.Abs(*sd.Rotation))%180 == 90 {
					width, height = height, width
				}
				return width, height, nil
			}
		}
	}
	return width, height, nil
}

package motionref

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"veritmo/internal/music/audiomeasure"
	"veritmo/internal/platform/fsutil"
	"veritmo/internal/platform/runner"
)

// The settings of the analysis are constants: the same video gives the same analysis.
const (
	// MaxFrames and MinFrames bound the key frames kept; Sampled is how many are taken before the almost equal ones
	// are dropped.
	MaxFrames = 24
	MinFrames = 12
	sampled   = MaxFrames
	// frameWidth is the width the key frames are scaled to.
	frameWidth = 640
	// sameFrame is the mean difference (of 0 to 1, on a 32x32 grey copy) under which two frames are almost equal.
	sameFrame = 0.012
)

// Analyser measures videos. The zero value runs ffmpeg and ffprobe for real and describes nothing.
type Analyser struct {
	Runner runner.CommandRunner
	// Looker describes the look of the video; nil leaves the description out and says why.
	Looker Looker
	// Reader opens the vision model to read the shots (see ReadShots); nil leaves the shots unread and says why.
	Reader Reader
}

func (a *Analyser) run() runner.CommandRunner {
	if a.Runner != nil {
		return a.Runner
	}
	return runner.NewExecRunner()
}

// Analyse measures a video and writes its key frames in framesDir (00.jpg, 01.jpg…).
func (a *Analyser) Analyse(ctx context.Context, video, framesDir string) (*Analysis, error) {
	an := &Analysis{}
	if err := a.probe(ctx, video, an); err != nil {
		return nil, err
	}
	if an.Duration > MaxSeconds {
		return nil, fmt.Errorf("the video lasts %d minutes: the limit is %d", int(an.Duration/60), MaxSeconds/60)
	}
	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(framesDir, ".take-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	takes, err := a.take(ctx, video, tmp, an.Duration)
	if err != nil {
		return nil, err
	}
	an.Colours, an.Thirds = coloursOf(takes)
	an.Frames, err = keep(takes, framesDir)
	if err != nil {
		return nil, err
	}
	if err := a.audio(ctx, video, an); err != nil {
		return nil, err
	}
	// The shots, the cuts, the mean shot and the rhythm sheet (the beat of the cuts needs the tempo).
	if err := a.Breakdown(ctx, video, an); err != nil {
		return nil, err
	}
	if err := a.describeLook(ctx, framesDir, an); err != nil {
		return nil, err
	}
	return an, nil
}

// audioSeconds is how much of the audio is measured: the tempo and the key do not need more.
const audioSeconds = 180

// audio estimates the tempo and the key of the sound of the video. With no sound, or no clear pulse or key, the
// fields stay absent.
func (a *Analyser) audio(ctx context.Context, video string, an *Analysis) error {
	out, err := a.run().Run(ctx, "ffprobe", []string{"-v", "error", "-select_streams", "a:0", "-show_entries", "stream=codec_type", "-of", "default=nw=1", video})
	if err != nil || !strings.Contains(string(out), "codec_type=audio") {
		return nil // no sound
	}
	raw, err := a.run().Run(ctx, "ffmpeg", []string{"-v", "error", "-i", video, "-vn", "-t", strconv.Itoa(audioSeconds), "-ac", "1",
		"-ar", strconv.Itoa(audiomeasure.Rate), "-f", "f32le", "-"})
	if err != nil {
		return fmt.Errorf("cannot read the sound of the video: %w", err)
	}
	if len(raw) < 4*audiomeasure.Rate*5 {
		return nil // under five seconds of sound says nothing
	}
	samples := make([]float32, len(raw)/4)
	for i := range samples {
		samples[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	m := audiomeasure.Measure(samples)
	if m.BPM != nil {
		v := math.Round(*m.BPM*10) / 10
		an.Tempo = &v
	}
	an.Key = m.Key
	return nil
}

// probe reads the size, the frame rate and the duration.
func (a *Analyser) probe(ctx context.Context, video string, an *Analysis) error {
	out, err := a.run().Run(ctx, "ffprobe", []string{"-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height,r_frame_rate:format=duration", "-of", "default=nw=1", video})
	if err != nil {
		return fmt.Errorf("the file is not a video: %w", err)
	}
	for _, l := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
		if !ok {
			continue
		}
		switch k {
		case "width":
			an.Width, _ = strconv.Atoi(v)
		case "height":
			an.Height, _ = strconv.Atoi(v)
		case "duration":
			an.Duration, _ = strconv.ParseFloat(v, 64)
		case "r_frame_rate":
			n, d, _ := strings.Cut(v, "/")
			num, _ := strconv.ParseFloat(n, 64)
			den, _ := strconv.ParseFloat(d, 64)
			if den > 0 {
				an.FPS = math.Round(num/den*100) / 100
			}
		}
	}
	if an.Width == 0 || an.Height == 0 || an.Duration <= 0 {
		return errors.New("the file has no picture or no length")
	}
	return nil
}

// taken is a picture sampled from the video.
type taken struct {
	path string
	sig  []float64
}

// take samples MaxFrames pictures at even intervals into dir, in time order.
func (a *Analyser) take(ctx context.Context, video, dir string, duration float64) ([]taken, error) {
	var all []taken
	for i := 0; i < sampled; i++ {
		at := duration * (float64(i) + 0.5) / float64(sampled)
		path := filepath.Join(dir, fmt.Sprintf("t%02d.jpg", i))
		if _, err := a.run().Run(ctx, "ffmpeg", []string{"-v", "error", "-y", "-ss", strconv.FormatFloat(at, 'f', 3, 64), "-i", video,
			"-frames:v", "1", "-vf", fmt.Sprintf("scale=%d:-2,format=yuvj420p", frameWidth), "-q:v", "3", path}); err != nil {
			return nil, fmt.Errorf("cannot read a picture of the video: %w", err)
		}
		sig, err := signature(path)
		if err != nil {
			continue // a position past the last picture: the video is a little shorter than it says
		}
		all = append(all, taken{path, sig})
	}
	if len(all) == 0 {
		return nil, errors.New("no picture could be read from the video")
	}
	return all, nil
}

// keep drops the pictures almost equal to a neighbour (down to MinFrames at least, when there are that many) and
// writes the rest, in order, in dir.
func keep(all []taken, dir string) ([]string, error) {
	all = append([]taken(nil), all...)
	// Drop the later of the two neighbours that differ least, while they are almost equal and there are more than
	// MinFrames.
	for len(all) > MinFrames {
		worst, at := math.Inf(1), -1
		for i := 1; i < len(all); i++ {
			if d := distance(all[i-1].sig, all[i].sig); d < worst {
				worst, at = d, i
			}
		}
		if at < 0 || worst >= sameFrame {
			break
		}
		all = append(all[:at], all[at+1:]...)
	}
	names := make([]string, 0, len(all))
	for i, t := range all {
		name := fmt.Sprintf("%02d.jpg", i)
		if err := fsutil.CopyFile(t.path, filepath.Join(dir, name)); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, nil
}

const sigSide = 32

// signature is a 32x32 grey copy of a picture (values of 0 to 1) made by averaging blocks.
func signature(path string) ([]float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err := jpeg.Decode(f)
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	sig := make([]float64, sigSide*sigSide)
	counts := make([]float64, sigSide*sigSide)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			grey := (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(bl)) / 65535
			cell := (y*sigSide/h)*sigSide + x*sigSide/w
			sig[cell] += grey
			counts[cell]++
		}
	}
	for i := range sig {
		if counts[i] > 0 {
			sig[i] /= counts[i]
		}
	}
	return sig, nil
}

func distance(a, b []float64) float64 {
	s := 0.0
	for i := range a {
		s += math.Abs(a[i] - b[i])
	}
	return s / float64(len(a))
}

// decode reads a key frame.
func decode(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return jpeg.Decode(f)
}

package videoedit

import (
	"context"
	"fmt"
	"image"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"strings"

	"veritmo/internal/video/videovision"
)

// The window of a crop follows the people by the boxes the person mask gives at a few frames per second, smoothed by
// a moving average over about a second so the picture does not shake, and moved by ffmpeg at the video's own rate.
const (
	followFPS    = 4.0
	followWindow = 4 // samples in the moving average: one second
)

// boxCentres is the centre, along the axis the window moves on, of the box of the people of every sample; a sample
// with nobody in it is NaN. scale brings the sample's pixels to the video's.
func boxCentres(masks []videovision.FrameMask, horizontal bool, scale float64) []float64 {
	out := make([]float64, len(masks))
	for i, m := range masks {
		if len(m.Box) != 4 {
			out[i] = math.NaN()
			continue
		}
		lo, hi := m.Box[1], m.Box[3]
		if horizontal {
			lo, hi = m.Box[0], m.Box[2]
		}
		out[i] = float64(lo+hi) / 2 * scale
	}
	return out
}

// fillGaps replaces the NaN samples by the nearest sample that has a value; false when there is none at all.
func fillGaps(v []float64) bool {
	any := false
	for _, x := range v {
		if !math.IsNaN(x) {
			any = true
		}
	}
	if !any {
		return false
	}
	out := make([]float64, len(v))
	for i := range v {
		if !math.IsNaN(v[i]) {
			out[i] = v[i]
			continue
		}
		for d := 1; d < len(v); d++ {
			if i-d >= 0 && !math.IsNaN(v[i-d]) {
				out[i] = v[i-d]
				break
			}
			if i+d < len(v) && !math.IsNaN(v[i+d]) {
				out[i] = v[i+d]
				break
			}
		}
	}
	copy(v, out)
	return true
}

// smooth is a centred moving average of n samples (shorter at the ends).
func smooth(v []float64, n int) []float64 {
	out := make([]float64, len(v))
	half := n / 2
	for i := range v {
		lo, hi := i-half, i+half
		if lo < 0 {
			lo = 0
		}
		if hi > len(v)-1 {
			hi = len(v) - 1
		}
		sum := 0.0
		for k := lo; k <= hi; k++ {
			sum += v[k]
		}
		out[i] = sum / float64(hi-lo+1)
	}
	return out
}

// windowPositions turns the centres into the position of the window's start, kept inside the picture.
func windowPositions(centres []float64, window, size int) []float64 {
	out := make([]float64, len(centres))
	for i, c := range centres {
		p := c - float64(window)/2
		out[i] = math.Max(0, math.Min(p, float64(size-window)))
	}
	return out
}

// interpolate gives the position at time t from samples taken every 1/rate seconds, linearly between them.
func interpolate(pos []float64, rate, t float64) float64 {
	f := t * rate
	i := int(math.Floor(f))
	if i < 0 {
		return pos[0]
	}
	if i >= len(pos)-1 {
		return pos[len(pos)-1]
	}
	w := f - float64(i)
	return pos[i]*(1-w) + pos[i+1]*w
}

// sendcmdScript is the file sendcmd reads: at each frame of the output, the window's start.
func sendcmdScript(pos []float64, fps, duration float64, horizontal bool) string {
	prop := "y"
	if horizontal {
		prop = "x"
	}
	var b strings.Builder
	frames := int(math.Ceil(duration*fps)) + 1
	for k := 0; k < frames; k++ {
		t := float64(k) / fps
		fmt.Fprintf(&b, "%.4f crop %s %d;\n", t, prop, int(math.Round(interpolate(pos, followFPS, t))))
	}
	return b.String()
}

// filterEscape escapes a value inside a filter graph.
func filterEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `:`, `\:`, `'`, `\'`, `,`, `\,`, `;`, `\;`, `[`, `\[`, `]`, `\]`)
	return r.Replace(s)
}

// followFilter builds the crop filter whose window follows the people. It returns "" and, when the reason is worth
// telling, a note: the vision service is not usable, or nobody is in the video.
func (c *Catalog) followFilter(ctx context.Context, job Job, info Info, ratio float64) (filter, note string, err error) {
	cw, ch := cropWindow(info.Width, info.Height, ratio)
	horizontal := cw < info.Width
	if !horizontal && ch >= info.Height {
		return "", "", nil // the window is the whole picture: there is nothing to follow
	}
	v := c.VisionOf()
	if v == nil {
		return "", "the window is centred: the vision service is not set up", nil
	}
	if ok, why := v.Ready(ctx); !ok {
		return "", "the window is centred: " + why, nil
	}
	frames, err := workDir(job, "follow-frames")
	if err != nil {
		return "", "", err
	}
	masks, err := workDir(job, "follow-masks")
	if err != nil {
		return "", "", err
	}
	if _, err := extractFrames(ctx, job.Video, frames, followFPS); err != nil {
		return "", "", err
	}
	first, _ := filepath.Glob(filepath.Join(frames, "f_*.png"))
	reduced, err := imageWidth(first[0])
	if err != nil {
		return "", "", err
	}
	sum, err := v.Mask(ctx, frames, masks)
	if err != nil {
		return "", "", err
	}
	// the reduced frames keep the video's shape, so one factor serves both axes
	centres := boxCentres(sum.Masks, horizontal, float64(info.Width)/float64(reduced))
	if !fillGaps(centres) {
		return "", "the window is centred: no person was found in the video", nil
	}
	size, window := info.Height, ch
	if horizontal {
		size, window = info.Width, cw
	}
	pos := windowPositions(smooth(centres, followWindow), window, size)
	script := filepath.Join(job.Dir, "tmp", "follow.cmd")
	if err := os.WriteFile(script, []byte(sendcmdScript(pos, info.FPS, info.Duration, horizontal)), 0o644); err != nil {
		return "", "", err
	}
	x0, y0 := 0, int(pos[0])
	if horizontal {
		x0, y0 = int(pos[0]), 0
	}
	return fmt.Sprintf("sendcmd=f=%s,crop=%d:%d:%d:%d", filterEscape(script), cw, ch, x0, y0), "", nil
}

func imageWidth(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, err
	}
	return cfg.Width, nil
}

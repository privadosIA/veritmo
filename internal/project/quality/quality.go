// Package quality checks the animated clips of a video project once they are
// made: it samples frames of each clip, measures what can be measured (colour
// drift, loss of sharpness) and, when a vision model is available through
// Ollama, asks it whether the people in the frame are deformed. From the
// first bad frame it says how much of the clip is clean, so a bad tail can be
// cut or the scene made again with another seed (see specs/quality-check).
package quality

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"veritmo/internal/platform/runner"
)

// Verdicts of a scene.
const (
	VerdictOK    = "ok"    // nothing wrong was found
	VerdictTrim  = "trim"  // the clip is clean until GoodUntil: cut it there
	VerdictRetry = "retry" // too little is clean: make the scene again with another seed
)

// Options tune the check; the zero value of every field means its default.
type Options struct {
	// FPS is how many frames per second of clip are sampled (default 4).
	FPS float64
	// ColorDrift is the shift of the mean colour (0-255, any channel) from the
	// first frame above which a frame is flagged (default 20).
	ColorDrift float64
	// SharpDrop is the fraction of the first frame's sharpness a frame may lose
	// before it is flagged (default 0.30).
	SharpDrop float64
	// MinSeverity is the lowest vision severity (0-10) that flags a frame (default 7).
	MinSeverity int
	// MinKeep is the least clean seconds worth keeping: a clip with less is
	// retried instead of trimmed (default 0.8).
	MinKeep float64
	// MaxSeconds limits the check to the first seconds of the clip: the part a
	// scene really uses. Zero checks the whole clip.
	MaxSeconds float64
}

func (o Options) fps() float64 {
	if o.FPS <= 0 {
		return 4
	}
	return o.FPS
}
func (o Options) colorDrift() float64 {
	if o.ColorDrift <= 0 {
		return 20
	}
	return o.ColorDrift
}
func (o Options) sharpDrop() float64 {
	if o.SharpDrop <= 0 {
		return 0.30
	}
	return o.SharpDrop
}
func (o Options) minSeverity() int {
	if o.MinSeverity <= 0 {
		return 7
	}
	return o.MinSeverity
}
func (o Options) minKeep() float64 {
	if o.MinKeep <= 0 {
		return 0.8
	}
	return o.MinKeep
}

// Judgement is what a vision model says about one frame.
type Judgement struct {
	Defect   bool   `json:"defect"`
	Severity int    `json:"severity"`
	Issue    string `json:"issue"`
}

// Vision judges a frame (a PNG). A nil Vision means the check uses only what it can measure.
type Vision interface {
	Judge(ctx context.Context, framePNG []byte) (Judgement, error)
}

// FaceFinder is a Vision that can also locate faces. Small faces in a wide shot
// are too few pixels for the frame-level judgement, so each face is cropped,
// enlarged and judged on its own.
type FaceFinder interface {
	Faces(ctx context.Context, framePNG []byte) ([]Box, error)
	JudgeFace(ctx context.Context, facePNG []byte) (Judgement, error)
}

// Frame is the result for one sampled frame.
type Frame struct {
	T      float64    `json:"t"`               // seconds into the clip
	Color  float64    `json:"color_drift"`     // mean colour shift from the first frame
	Sharp  float64    `json:"sharpness_ratio"` // sharpness over the first frame's
	Flag   string     `json:"flag,omitempty"`  // "color", "blur" or "vision": why it counts as bad
	Vision *Judgement `json:"vision,omitempty"`
}

// SceneReport is the verdict on one clip.
type SceneReport struct {
	ID        string   `json:"id"`
	Seconds   float64  `json:"seconds"`    // length of the clip checked
	GoodUntil float64  `json:"good_until"` // seconds of clean clip from its start
	Verdict   string   `json:"verdict"`
	Issues    []string `json:"issues,omitempty"`
	Frames    []Frame  `json:"frames"`
	// Kind says what the first counted bad frame was flagged for: "color",
	// "blur" or "vision" ("" when the scene is ok).
	Kind string `json:"kind,omitempty"`
	// BadAt is the second of the first counted bad frame, and Evidence that
	// frame as a PNG (not part of the JSON report).
	BadAt    float64 `json:"bad_at,omitempty"`
	Evidence []byte  `json:"-"`
}

// CheckClip samples the clip at path, measures and (with vision) judges every
// frame, and decides the verdict. vision may be nil.
func CheckClip(ctx context.Context, r runner.CommandRunner, vision Vision, id, path string, o Options) (*SceneReport, error) {
	dir, err := os.MkdirTemp("", "vt-quality-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	fps := o.fps()
	args := []string{"-v", "error", "-y"}
	if o.MaxSeconds > 0 {
		args = append(args, "-t", strconv.FormatFloat(o.MaxSeconds, 'f', -1, 64))
	}
	width := 768
	finder, _ := vision.(FaceFinder)
	if finder != nil {
		width = 1280 // faces need the pixels
	}
	args = append(args, "-i", path, "-vf", fmt.Sprintf("fps=%s,scale=%d:-2", strconv.FormatFloat(fps, 'f', -1, 64), width), filepath.Join(dir, "f_%04d.png"))
	if _, err := r.Run(ctx, "ffmpeg", args); err != nil {
		return nil, fmt.Errorf("quality: cannot read frames of %s: %w", path, err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "f_*.png"))
	sort.Strings(files)
	if len(files) < 2 {
		return nil, fmt.Errorf("quality: %s has too few frames to check (%d)", path, len(files))
	}

	rep := &SceneReport{ID: id, Seconds: float64(len(files)) / fps}
	frameData := make([][]byte, 0, len(files))
	var first *metrics
	for i, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("quality: %s: %w", f, err)
		}
		m := measure(img)
		if first == nil {
			first = &m
		}
		// A frame with no detail to begin with (a flat colour) has no sharpness to lose.
		ratio := 1.0
		if first.sharp >= minDetail {
			ratio = m.sharp / first.sharp
		}
		fr := Frame{T: float64(i) / fps, Color: round2(m.colorShift(*first)), Sharp: round2(ratio)}
		switch {
		case i > 0 && fr.Color > o.colorDrift():
			fr.Flag = "color"
		case i > 0 && fr.Sharp < 1-o.sharpDrop():
			fr.Flag = "blur"
		}
		if vision != nil {
			j, err := vision.Judge(ctx, data)
			if err != nil {
				rep.Issues = append(rep.Issues, fmt.Sprintf("vision failed at %.2fs: %v", fr.T, err))
			} else {
				if finder != nil {
					if fj, ferr := judgeFaces(ctx, finder, img, data); ferr != nil {
						rep.Issues = append(rep.Issues, fmt.Sprintf("face check failed at %.2fs: %v", fr.T, ferr))
					} else if fj != nil && fj.Defect && (!j.Defect || fj.Severity > j.Severity) {
						j = *fj
					}
				}
				fr.Vision = &j
				if fr.Flag == "" && j.Defect && j.Severity >= o.minSeverity() {
					fr.Flag = "vision"
				}
			}
		}
		rep.Frames = append(rep.Frames, fr)
		frameData = append(frameData, data)
	}
	if bad := decide(rep, fps, o); bad >= 0 {
		rep.Kind, rep.BadAt, rep.Evidence = rep.Frames[bad].Flag, rep.Frames[bad].T, frameData[bad]
	}
	return rep, nil
}

// decide sets GoodUntil and the verdict from the flagged frames. A frame the
// measurements flag counts on its own once two are flagged; one doubtful frame
// of the vision model alone is ignored unless it is severe (9 or more).
// It returns the index of the first counted bad frame, or -1.
func decide(rep *SceneReport, fps float64, o Options) int {
	flagged := 0
	for _, f := range rep.Frames {
		if f.Flag != "" {
			flagged++
		}
	}
	firstBad := -1
	for i, f := range rep.Frames {
		if f.Flag == "" {
			continue
		}
		severe := f.Vision != nil && f.Vision.Severity >= 9 && f.Flag == "vision"
		if flagged >= 2 || severe || f.Flag != "vision" {
			firstBad = i
			break
		}
	}
	if firstBad < 0 {
		if flagged == 1 {
			rep.Issues = append(rep.Issues, "one doubtful frame, ignored")
		}
		rep.GoodUntil, rep.Verdict = rep.Seconds, VerdictOK
		return -1
	}
	bad := rep.Frames[firstBad]
	reason := bad.Flag
	if bad.Flag == "vision" && bad.Vision != nil {
		reason = "vision: " + bad.Vision.Issue
	}
	rep.Issues = append(rep.Issues, fmt.Sprintf("from %.2fs (%s)", bad.T, reason))
	step := 1 / fps
	rep.GoodUntil = math.Max(0, math.Floor((bad.T-step)*10)/10)
	if rep.GoodUntil >= o.minKeep() {
		rep.Verdict = VerdictTrim
	} else {
		rep.GoodUntil, rep.Verdict = 0, VerdictRetry
	}
	return firstBad
}

// minDetail is the mean luma gradient below which a frame counts as having no detail.
const minDetail = 1.0

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// metrics are the measurements of one frame.
type metrics struct {
	r, g, b float64 // mean colour
	sharp   float64 // mean absolute luma gradient
}

func (m metrics) colorShift(from metrics) float64 {
	return math.Max(math.Abs(m.r-from.r), math.Max(math.Abs(m.g-from.g), math.Abs(m.b-from.b)))
}

// measure reads every 4th pixel of the frame.
func measure(img image.Image) metrics {
	b := img.Bounds()
	const stride = 4
	var sr, sg, sb, grad, n, gn float64
	var prevRow []float64
	for y := b.Min.Y; y < b.Max.Y; y += stride {
		row := make([]float64, 0, b.Dx()/stride+1)
		for x := b.Min.X; x < b.Max.X; x += stride {
			r, g, bl, _ := img.At(x, y).RGBA()
			rf, gf, bf := float64(r>>8), float64(g>>8), float64(bl>>8)
			sr, sg, sb, n = sr+rf, sg+gf, sb+bf, n+1
			row = append(row, 0.299*rf+0.587*gf+0.114*bf)
		}
		for i := 1; i < len(row); i++ {
			grad += math.Abs(row[i] - row[i-1])
			gn++
		}
		if prevRow != nil {
			for i := range row {
				grad += math.Abs(row[i] - prevRow[i])
				gn++
			}
		}
		prevRow = row
	}
	if n == 0 || gn == 0 {
		return metrics{}
	}
	return metrics{r: sr / n, g: sg / n, b: sb / n, sharp: grad / gn}
}

// Summary is a one-line account of a report.
func (r *SceneReport) Summary() string {
	switch r.Verdict {
	case VerdictOK:
		return fmt.Sprintf("%s: ok (%.1fs clean)", r.ID, r.Seconds)
	case VerdictTrim:
		return fmt.Sprintf("%s: trim to %.1fs of %.1fs — %s", r.ID, r.GoodUntil, r.Seconds, strings.Join(r.Issues, "; "))
	default:
		return fmt.Sprintf("%s: retry (bad from the start) — %s", r.ID, strings.Join(r.Issues, "; "))
	}
}

// MarshalReports renders the reports as the quality.json file.
func MarshalReports(project string, reports []*SceneReport, model string) ([]byte, error) {
	return json.MarshalIndent(map[string]any{"project": project, "vision_model": model, "scenes": reports}, "", "  ")
}

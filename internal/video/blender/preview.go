package blender

// preview.go: one frame of a description, small and on the CPU, to look at before a clip is rendered (spec
// blender-scene-writer). It never takes a card: a run pins one for its whole length, and a preview that took it would wait
// for the run or break it.

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"veritmo/internal/platform/runner"
)

// Preview limits and defaults.
const (
	DefaultPreviewWidth = 480
	MaxPreviewWidth     = 960
	previewSamples      = 8
)

// PreviewOptions says which frame of which scene to draw.
type PreviewOptions struct {
	// T is the second to draw, inside the scene; Seconds is the length of the scene and FPS its frame rate (they decide
	// the frame, and where the keys fall).
	T, Seconds float64
	FPS        int
	// Width is the width of the picture (DefaultPreviewWidth when 0, at most MaxPreviewWidth); Height follows the aspect
	// of AspectW:AspectH (16:9 when they are 0).
	Width            int
	AspectW, AspectH int
	// Sources is the sources folder (for the fonts of the files of the project) and Fonts the TrueType file of each font
	// role of the design.
	Sources string
	Fonts   map[string]string
}

// PreviewFrame is the 1-based frame that second t is: the same rule the script uses to place a key.
func PreviewFrame(t, seconds float64, fps int) int {
	count := int(math.Ceil(seconds * float64(fps)))
	f := int(math.Floor(t*float64(fps)+0.5)) + 1
	if f > count {
		f = count
	}
	if f < 1 {
		f = 1
	}
	return f
}

// Preview draws one frame of the description with Blender at bin and returns the PNG.
func Preview(ctx context.Context, r runner.CommandRunner, bin string, spec *Spec, o PreviewOptions) ([]byte, error) {
	if o.FPS <= 0 || o.Seconds <= 0 || o.T < 0 || o.T > o.Seconds+1e-9 {
		return nil, fmt.Errorf("blender: t must be inside the scene (0 to %v s)", o.Seconds)
	}
	width := o.Width
	if width <= 0 {
		width = DefaultPreviewWidth
	}
	if width > MaxPreviewWidth {
		width = MaxPreviewWidth
	}
	aw, ah := o.AspectW, o.AspectH
	if aw <= 0 || ah <= 0 {
		aw, ah = 16, 9
	}
	height := int(math.Round(float64(width) * float64(ah) / float64(aw)))
	if height < 2 {
		height = 2
	}
	dir, err := os.MkdirTemp("", "preview-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	frame := PreviewFrame(o.T, o.Seconds, o.FPS)
	// the render runs in a folder of its own: a relative sources folder (the server's projects folder is relative) would be read from it
	sources := o.Sources
	if sources != "" && !filepath.IsAbs(sources) {
		if abs, err := filepath.Abs(sources); err == nil {
			sources = abs
		}
	}
	// CUDA_VISIBLE_DEVICES is emptied and the device is cpu: whatever the environment says, no card is touched
	_, err = Render(ctx, r, bin, Params{
		Dir: dir, Out: "f", Samples: previewSamples, Device: "cpu", Width: width, Height: height,
		Count: int(math.Ceil(o.Seconds * float64(o.FPS))), FPS: o.FPS, Frame: frame,
		Spec: spec, Sources: sources, Fonts: o.Fonts,
	}, []string{"CUDA_VISIBLE_DEVICES="})
	if err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(dir, "f", fmt.Sprintf("f_%06d.png", frame)))
}

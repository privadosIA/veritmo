package motionref

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"veritmo/internal/assistant/describe"
)

// lookFrames is how many key frames the vision model reads to describe the look.
const lookFrames = 4

// Looker describes the look of a video from some of its key frames (pictures). The server gives one that goes through
// the descriptor that describes references, with its safeguards; nil means there is no vision model to ask.
type Looker func(ctx context.Context, frames [][]byte) (string, error)

// describeLook fills the description of the analysis, or says why there is none. A frame that shows a person that is
// or may be a minor is deleted and stops the description; a model that declines is reported, never replaced.
func (a *Analyser) describeLook(ctx context.Context, framesDir string, an *Analysis) error {
	if a.Looker == nil {
		an.DescriptionNote = "no description: no vision model is available"
		return nil
	}
	if len(an.Frames) == 0 {
		return nil
	}
	n := lookFrames
	if n > len(an.Frames) {
		n = len(an.Frames)
	}
	var chosen []int
	var pics [][]byte
	for i := 0; i < n; i++ {
		idx := (2*i + 1) * len(an.Frames) / (2 * n)
		b, err := os.ReadFile(filepath.Join(framesDir, an.Frames[idx]))
		if err != nil {
			return err
		}
		chosen = append(chosen, idx)
		pics = append(pics, b)
	}
	text, err := a.Looker(ctx, pics)
	if err == nil {
		an.Description = text
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var minor *describe.MinorFrameError
	switch {
	case errors.As(err, &minor):
		name := an.Frames[chosen[minor.Frame]]
		_ = os.Remove(filepath.Join(framesDir, name))
		kept := an.Frames[:0:0]
		for _, f := range an.Frames {
			if f != name {
				kept = append(kept, f)
			}
		}
		an.Frames = kept
		an.DescriptionNote = fmt.Sprintf("no description: a person who is, or may be, a minor appears in frame %s, which was deleted", name)
	case errors.Is(err, describe.ErrMinor):
		an.DescriptionNote = "no description: a person who is, or may be, a minor appears in the video"
	case errors.Is(err, describe.ErrRefused):
		an.DescriptionNote = "no description: the vision model declined to describe the look"
	default:
		an.DescriptionNote = "no description: " + err.Error()
	}
	return nil
}

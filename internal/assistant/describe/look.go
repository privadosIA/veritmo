package describe

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"veritmo/internal/assistant/assist"
)

// lookSystem asks for the look of a video (what a motion designer needs), not a prompt for a picture generator.
const lookSystem = "You describe the visual look of a video from one of its key frames, for a motion designer. " +
	"Reply with ONE paragraph in English, 40 to 70 words, about the colours (name the dominant ones), the typography, " +
	"the kind of screen or layout, the graphic style and, if text is visible, how it is set. " +
	"Do not describe people beyond saying that a person appears. No preamble, no quotes, no lists."

const lookMergeSystem = "You combine the looks of several key frames of ONE video into a single description of its look " +
	"(50 to 90 words, English, one paragraph): colours, typography, the kind of screens and the graphic style. " +
	"Do not describe people. Answer with the paragraph only."

// MinorFrameError says which of the frames given to DescribeLook shows a person that is, or may be, a minor. It is an
// ErrMinor.
type MinorFrameError struct {
	// Frame is the position, from 0, of the frame in the list given.
	Frame int
	Err   error
}

func (e *MinorFrameError) Error() string { return e.Err.Error() }
func (e *MinorFrameError) Unwrap() error { return ErrMinor }

// Look is the description of the look of a video.
type Look struct {
	Text     string
	Model    string
	Provider string
}

// DescribeLook describes the look of a video from some of its key frames (pictures: JPEG or PNG). It has the same
// safeguards as Describe: every frame goes through the age check first and a person that is or may be a minor stops
// everything (a MinorFrameError, nothing described); a model that declines is reported (ErrRefused), never replaced
// with an invented text.
func (d *Describer) DescribeLook(ctx context.Context, frames [][]byte, opts Options) (Look, error) {
	if len(frames) == 0 {
		return Look{}, fmt.Errorf("%w: no picture to describe", ErrInvalidInput)
	}
	timeout := d.Timeout
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	model, models, err := d.pickModel(ctx, opts)
	if err != nil {
		return Look{}, err
	}
	// One age check and one description per frame, and the merge.
	if err := d.checkCalls(2*len(frames) + 1); err != nil {
		return Look{}, err
	}
	defer d.unload(model)
	for i, f := range frames {
		if err := d.checkAdults(ctx, model, f, models, i+1, len(frames)); err != nil {
			if errors.Is(err, ErrMinor) {
				return Look{}, &MinorFrameError{Frame: i, Err: err}
			}
			return Look{}, err
		}
	}
	var texts []string
	for _, f := range frames {
		out, err := d.chat(ctx, model, lookSystem, "Describe the look of this frame.", f, false, 300)
		if err != nil {
			return Look{}, err
		}
		text := assist.Clean(out, false)
		if isNotADescription(text) {
			return Look{}, d.refusal(ctx, model, models)
		}
		texts = append(texts, text)
	}
	text := texts[0]
	if len(texts) > 1 {
		var b strings.Builder
		for i, t := range texts {
			fmt.Fprintf(&b, "Frame %d of %d: %s\n", i+1, len(texts), t)
		}
		if out, err := d.chat(ctx, model, lookMergeSystem, b.String(), nil, false, 400); err == nil {
			if merged := assist.Clean(out, false); !isNotADescription(merged) {
				text = merged
			}
		}
	}
	return Look{Text: text, Model: model, Provider: d.providerID()}, nil
}

// DescribePicture describes the look of one picture (JPEG or PNG) after the age check: the same safeguards as
// DescribeLook with a single frame. A minor, or an age that cannot be confirmed, is a MinorFrameError.
func (d *Describer) DescribePicture(ctx context.Context, picture []byte, opts Options) (Look, error) {
	return d.DescribeLook(ctx, [][]byte{picture}, opts)
}

package models

// vision.go: the vision service of the video-edit tools (dotnet/video-vision). It appears in Environment next
// to the music generators, with the same start and stop, and the tools that need it (remove-background, depth,
// motion-control) are available only while it runs and its model files are there.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/video/videovision"
)

// Vision is the vision service as the video-edit tools use it: it checks the engine without starting it and,
// when it runs, calls it.
type Vision struct{ host Host }

func (b Vision) engine() *musicsvc.Engine {
	return b.host.VisionEngine()
}

// Ready says whether the service can do a job now: installed, its model files there (named when not) and running.
func (b Vision) Ready(ctx context.Context) (bool, string) {
	a := b.engine().Availability(ctx)
	b.host.OverlayVision(&a)
	if a.Available {
		return true, ""
	}
	if a.Reason == musicsvc.ReasonNoWeights && len(a.Missing) > 0 {
		return false, fmt.Sprintf("the model file %s is missing from %s", strings.Join(a.Missing, ", "), videovision.WeightsDir)
	}
	if a.Detail != "" {
		return false, a.Detail
	}
	return false, "the vision service cannot be used now (" + a.Reason + ")"
}

func (b Vision) client(ctx context.Context) (*musicsvc.Client, error) {
	c, err := b.engine().Open(ctx)
	if err != nil {
		var ue *musicsvc.UnavailableError
		if errors.As(err, &ue) {
			return nil, errors.New(ue.Detail)
		}
		return nil, err
	}
	return c, nil
}

// Mask asks the vision service for the mask of the people in the frames of a folder, written to out.
func (b Vision) Mask(ctx context.Context, frames, out string) (videovision.Summary, error) {
	c, err := b.client(ctx)
	if err != nil {
		return videovision.Summary{}, err
	}
	return videovision.Mask(ctx, c, frames, out)
}

// Depth asks the vision service for the depth of the frames of a folder, written to out.
func (b Vision) Depth(ctx context.Context, frames, out string) (videovision.Summary, error) {
	c, err := b.client(ctx)
	if err != nil {
		return videovision.Summary{}, err
	}
	return videovision.Depth(ctx, c, frames, out)
}

// Control asks the vision service for the control frames of a folder (the depth of the people, black elsewhere), the mask
// grown by dilate pixels, written to out.
func (b Vision) Control(ctx context.Context, frames, out string, dilate int) (videovision.Summary, error) {
	c, err := b.client(ctx)
	if err != nil {
		return videovision.Summary{}, err
	}
	return videovision.Control(ctx, c, frames, out, dilate)
}

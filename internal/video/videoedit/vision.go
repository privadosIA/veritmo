package videoedit

import (
	"context"
	"time"

	"veritmo/internal/video/videovision"
)

// Vision is the service the tools that read a picture with a network talk to (the person mask, the depth). The
// server installs the real one; a tool that needs it asks whether it is ready before it says it is available.
type Vision interface {
	// Ready says whether the service can do a job now and, when it cannot, why: not installed, not running, a model
	// file missing (named).
	Ready(ctx context.Context) (bool, string)
	Mask(ctx context.Context, framesDir, outDir string) (videovision.Summary, error)
	Depth(ctx context.Context, framesDir, outDir string) (videovision.Summary, error)
	Control(ctx context.Context, framesDir, outDir string, dilate int) (videovision.Summary, error)
}

// NeedsVision is the availability of a tool that needs the vision service: it is available while the service is
// installed, has its model files and runs; the reason says which of them is not so.
func (c *Catalog) NeedsVision() (bool, string) {
	v := c.VisionOf()
	if v == nil {
		return false, "the vision service is not set up in this build"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return v.Ready(ctx)
}

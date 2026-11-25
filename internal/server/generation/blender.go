package generation

// blender.go: the Blender engine in the environment report (spec blender-engine). Blender is optional: when it is
// missing or too old the report says so and why, and nothing else changes.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"veritmo/internal/video/blender"
)

// blenderProbe is what asking Blender gives: who it is and what Cycles can render on.
type blenderProbe struct {
	Info    blender.Info
	Devices []blender.Device
	Err     error
}

// blenderCache keeps the answer for a short while: listing the devices starts Blender.
type blenderCache struct {
	mu   sync.Mutex
	at   time.Time
	last blenderProbe
}

const blenderCacheTTL = time.Minute

// probeBlender asks Blender (through the server's read-only runner), at most once per blenderCacheTTL.
func (h *Handler) probeBlender(ctx context.Context) blenderProbe {
	if h.blenderProbeFn != nil {
		return h.blenderProbeFn(ctx)
	}
	c := &h.blenderMemo
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.at.IsZero() && time.Since(c.at) < blenderCacheTTL {
		return c.last
	}
	p := blenderProbe{}
	p.Info, p.Err = blender.Detect(ctx, h.host.EnvRunner())
	if p.Err == nil {
		p.Devices, _ = blender.Devices(ctx, h.host.EnvRunner(), p.Info.Path)
	}
	c.at, c.last = time.Now(), p
	return p
}

// blenderStatus is Blender as an optional binary of the environment report: present when it can be used, with its
// version and the devices Cycles sees; otherwise the reason and how to fix it.
func (h *Handler) blenderStatus() binaryStatus {
	p := h.probeBlender(context.Background())
	st := binaryStatus{Name: "blender", Required: false, Path: p.Info.Path, Version: p.Info.Version}
	if p.Err != nil {
		st.Fix = p.Err.Error()
		if errors.Is(p.Err, blender.ErrNotInstalled) {
			st.Fix = blender.ErrNotInstalled.Error() + ": " + blender.InstallHint
		}
		return st
	}
	st.Present = true
	for _, d := range p.Devices {
		st.Devices = append(st.Devices, strings.TrimSpace(d.Type+" "+d.Name))
	}
	return st
}

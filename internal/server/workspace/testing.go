package workspace

// testing.go: the few hooks the tests of package server need to drive the runs of the workspace without a pipeline.
// They are not used by the server.

import (
	"context"
	"time"

	"veritmo/internal/platform/events"
	"veritmo/internal/project/config"
)

// ExecRun is a run as the test double of the pipeline sees it.
type ExecRun struct {
	Rec Run
	Cfg *config.Config
	Dir string
}

// SimulateRun makes the manager believe a run with that identifier is active, until release is called.
func (h *Handler) SimulateRun(id string, cfg *config.Config) (release func()) {
	h.rm.mu.Lock()
	h.rm.runs[id] = &run{cfg: cfg}
	h.rm.mu.Unlock()
	return func() {
		h.rm.mu.Lock()
		delete(h.rm.runs, id)
		h.rm.mu.Unlock()
	}
}

// SeedRun stores a run record in the history as it is.
func (h *Handler) SeedRun(r *Run) error { return h.rm.hist.createRun(r) }

// SeedRow stores any record of the history database (a resource sample, a calibration sample).
func (h *Handler) SeedRow(row any) error { return h.rm.hist.db.Create(row).Error }

// RunsOf lists the stored runs of a project (all of them for "").
func (h *Handler) RunsOf(project string) ([]Run, error) { return h.rm.hist.listRuns(project, 0) }

// SetAvailabilityTTL changes how long the availability of the tasks stays cached.
func (h *Handler) SetAvailabilityTTL(d time.Duration) { h.avail.ttl = d }

// executeWith is the executor of a run when the tests replace the pipeline with Execute.
func (h *Handler) executeWith(ctx context.Context, rn *run, em *events.Emitter) (string, error) {
	return h.Execute(ctx, ExecRun{Rec: rn.rec, Cfg: rn.cfg, Dir: rn.dir}, em)
}

// SeedEvent stores one event line of a run in the history.
func (h *Handler) SeedEvent(runID string, seq int, line string) error {
	return h.rm.hist.appendEvent(runID, seq, line)
}

// SetPipelineLookPath replaces how the runs look for the programs of the pipeline (ffmpeg, sd, piper).
func (h *Handler) SetPipelineLookPath(look func(string) (string, error)) { h.rm.lookPath = look }

package workspace

// events.go implements the run-event-streaming capability: one SSE stream
// per run in which every pipeline event is delivered verbatim as a
// default-type message, while server-originated notifications use the
// distinct event types `run-state` and `stream-end` and never touch the
// pipeline objects (see specs/run-event-streaming and design D2/D3).

import (
	"fmt"
	"net/http"

	"veritmo/internal/server/api"
)

// runStatePayload is the body of `run-state` notifications.
type runStatePayload struct {
	ID            string   `json:"id"`
	State         RunState `json:"state"`
	QueuePosition int      `json:"queue_position,omitempty"`
	Artifact      string   `json:"artifact,omitempty"`
	Error         string   `json:"error,omitempty"`
}

func (h *Handler) runStateOf(id string) (runStatePayload, bool) {
	rec, err := h.rm.hist.getRun(id)
	if err != nil {
		return runStatePayload{}, false
	}
	return runStatePayload{
		ID:            rec.ID,
		State:         rec.State,
		QueuePosition: h.rm.queuePosition(id),
		Artifact:      rec.Artifact,
		Error:         rec.Error,
	}, true
}

// GET /api/runs/{id}/events — live stream (replay then tail) for queued
// and running runs, stored replay for finished ones; both end with an
// explicit stream-end notification.
func (h *Handler) handleRunEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	state, ok := h.runStateOf(id)
	if !ok {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("run %s not found", id))
		return
	}

	rn, live := h.rm.liveRun(id)

	sse, ok := api.NewSSEWriter(w)
	if !ok {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, "response writer does not support streaming")
		return
	}
	sse.Notify("run-state", state)

	if live {
		ctx := r.Context()
		rn.log.WatchCancel(ctx)
		for i := 0; ; i++ {
			line, over := rn.log.Next(ctx, i)
			if line != nil {
				sse.Event(line)
				continue
			}
			if over {
				if ctx.Err() != nil {
					return // client went away; no stream-end
				}
				break
			}
		}
	} else {
		lines, err := h.rm.hist.listEvents(id)
		if err != nil {
			sse.Notify("stream-end", map[string]string{"error": err.Error()})
			return
		}
		for _, line := range lines {
			sse.Event([]byte(line))
		}
	}

	// Terminal: report the final state and close explicitly so clients
	// can distinguish completion from a dropped connection.
	if final, ok := h.runStateOf(id); ok {
		sse.Notify("run-state", final)
	}
	sse.Notify("stream-end", map[string]string{"id": id})
}

package music

// loudness.go measures how loud a project audio is, so the music studio can
// preview what normalising a mix would do (see specs/project-management).

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"

	"veritmo/internal/platform/probe"
	"veritmo/internal/server/api"
)

type loudnessAnswer struct {
	// LUFS is the integrated loudness (EBU R128); null for silent audio.
	LUFS *float64 `json:"lufs"`
}

type loudnessKey struct {
	path    string
	size    int64
	mtimeNS int64
}

// loudnessCache is the answers of the loudness endpoint kept per file version.
type loudnessCache struct {
	mu    sync.Mutex
	store map[loudnessKey]loudnessAnswer
}

// GET /api/projects/{id}/loudness?file=<path>
func (h *Handler) handleLoudness(w http.ResponseWriter, r *http.Request) {
	id, ok := h.ws.RequireProject(w, r)
	if !ok {
		return
	}
	rel := r.URL.Query().Get("file")
	if !peaksFileOK(rel) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "file must be an .mp3 or .wav in sources/ (sources/<name>) or in out/ (out/<project>/<name>)")
		return
	}
	full, ok := h.ws.ConfinedArtifactPath(id, rel)
	if !ok {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "invalid file path")
		return
	}
	fi, err := os.Stat(full)
	if err != nil || !fi.Mode().IsRegular() {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("%q does not exist in the project", rel))
		return
	}
	key := loudnessKey{path: full, size: fi.Size(), mtimeNS: fi.ModTime().UnixNano()}
	h.loudness.mu.Lock()
	cached, hit := h.loudness.store[key]
	h.loudness.mu.Unlock()
	if hit {
		api.WriteJSON(w, http.StatusOK, cached)
		return
	}
	if _, err := h.host.LookPath("ffmpeg"); err != nil {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, "ffmpeg is not installed: it is needed to read the audio")
		return
	}
	lufs, err := probe.Loudness(r.Context(), h.host.Runner(), full)
	var ans loudnessAnswer
	switch {
	case err == nil:
		ans.LUFS = &lufs
	case errors.Is(err, probe.ErrSilent):
		// a silent file is a valid answer: it has no loudness
	default:
		if r.Context().Err() != nil {
			return
		}
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, fmt.Sprintf("cannot read this audio: %s", api.LastLines(err.Error(), 3)))
		return
	}
	h.loudness.mu.Lock()
	if h.loudness.store == nil {
		h.loudness.store = map[loudnessKey]loudnessAnswer{}
	}
	if len(h.loudness.store) >= peaksMaxEntries {
		for old := range h.loudness.store {
			delete(h.loudness.store, old)
			break
		}
	}
	h.loudness.store[key] = ans
	h.loudness.mu.Unlock()
	api.WriteJSON(w, http.StatusOK, ans)
}

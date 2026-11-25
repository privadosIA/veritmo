package music

// songs.go deletes one exported song of a music project: its audio file, its
// cover when no other song shares it, and its entry in the manifest.

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"veritmo/internal/project/config"
	"veritmo/internal/project/export"
	"veritmo/internal/server/api"
)

// DELETE /api/projects/{id}/songs/{track}/{variant}   (variant is 0-based)
func (h *Handler) handleSongDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := h.ws.RequireProject(w, r)
	if !ok {
		return
	}
	track := r.PathValue("track")
	variant, err := strconv.Atoi(r.PathValue("variant"))
	if err != nil || variant < 0 {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "variant must be a non-negative integer")
		return
	}
	cfg, err := h.ws.ProjectConfig(id)
	if err != nil || cfg.Output.Mode != config.ModeMusic {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeInvalid, fmt.Sprintf("project %q is not a music project", id))
		return
	}
	if runID, busy := h.ws.ProjectBusy(id); busy {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("project %q has an active or queued run (%s)", id, runID))
		return
	}
	dir := h.ws.ProjectDir(id)
	manifestPath := filepath.Join(dir, cfg.ExportDir(), "manifest.json")
	m, err := export.ReadMusicManifest(manifestPath)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, "the project has no exported songs")
		return
	}
	at := -1
	for i, e := range m.Tracks {
		if e.TrackID == track && e.Variant == variant {
			at = i
			break
		}
	}
	if at < 0 {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("song %q variant %d does not exist", track, variant))
		return
	}
	gone := m.Tracks[at]
	rest := append(append([]export.MusicEntry{}, m.Tracks[:at]...), m.Tracks[at+1:]...)
	for _, rel := range []string{gone.File, gone.Cover} {
		if rel == "" {
			continue
		}
		if shared(rest, rel) {
			continue
		}
		full, ok := h.ws.ConfinedArtifactPath(id, rel)
		if !ok {
			continue
		}
		if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, fmt.Sprintf("cannot delete %s: %v", rel, err))
			return
		}
	}
	m.Tracks = rest
	if err := export.WriteMusicManifest(manifestPath, m); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, fmt.Sprintf("cannot update the manifest: %v", err))
		return
	}
	// the library follows the manifest: the song, its stars and its place in the playlists go with it
	if _, err := h.ws.SyncLibrary(r.Context(), id); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, fmt.Sprintf("the song is deleted but the library could not be updated: %v", err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// shared reports whether another entry uses the same file or cover.
func shared(entries []export.MusicEntry, rel string) bool {
	for _, e := range entries {
		if e.File == rel || e.Cover == rel {
			return true
		}
	}
	return false
}

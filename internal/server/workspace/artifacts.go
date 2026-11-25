package workspace

// artifacts.go implements the artifact-access capability: read-only HTTP
// access to a project's pipeline artifacts (tmp/ intermediates, out/
// exports, the final video), strictly confined to the project directory
// (see specs/artifact-access). There is deliberately no write or delete
// endpoint.

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"veritmo/internal/platform/media"
	"veritmo/internal/server/api"
)

// GET /api/projects/{id}/artifacts/{path...}
func (h *Handler) handleArtifact(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	// Artifacts are rewritten in place (a regenerated song, an updated
	// manifest): the browser must revalidate them (a 304 when unchanged) or
	// its heuristic cache shows the previous run's file.
	w.Header().Set("Cache-Control", "no-cache")
	id, ok := h.RequireProject(w, r)
	if !ok {
		return
	}
	rel := r.PathValue("path")

	full, ok := h.ConfinedArtifactPath(id, rel)
	if !ok {
		// Rejected without filesystem detail (traversal, absolute path,
		// or a symlink escaping the project).
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "invalid artifact path")
		return
	}

	fi, err := os.Stat(full)
	if err != nil || !fi.Mode().IsRegular() {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("artifact %q not produced yet", rel))
		return
	}
	f, err := os.Open(full)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("artifact %q not produced yet", rel))
		return
	}
	defer f.Close()
	// ServeContent sets the media type from the file extension and
	// honours Range requests, so audio/video seeking works in-browser.
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
}

// ConfinedArtifactPath resolves rel inside the project directory and
// reports whether the fully resolved target (symlinks included) stays
// within it.
func (h *Handler) ConfinedArtifactPath(projectID, rel string) (string, bool) {
	if rel == "" || strings.HasPrefix(rel, "/") || filepath.IsAbs(filepath.FromSlash(rel)) {
		return "", false
	}
	// Lexical check first: a cleaned path must not climb out.
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", false
	}

	projAbs, err := filepath.Abs(h.ProjectDir(projectID))
	if err != nil {
		return "", false
	}
	projResolved, err := filepath.EvalSymlinks(projAbs)
	if err != nil {
		return "", false
	}
	full := filepath.Join(projAbs, clean)

	// Resolve the target's symlinks; a missing file resolves its parent
	// so the not-found answer still goes through the confinement check.
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", false
		}
		parent, errp := filepath.EvalSymlinks(filepath.Dir(full))
		if errp != nil {
			// Parent missing too: lexically confined, let Stat 404 it.
			return full, within(full, projResolved) || within(full, projAbs)
		}
		resolved = filepath.Join(parent, filepath.Base(full))
	}
	if !within(resolved, projResolved) && !h.withinMediaFolder(projectID, resolved) {
		return "", false
	}
	return resolved, true
}

// withinMediaFolder reports whether a resolved path is inside <media>/<project>: the links of a project kept in
// the media directory lead there, and nowhere else is allowed.
func (h *Handler) withinMediaFolder(projectID, resolved string) bool {
	if h.cfg.MediaDir == "" || !media.ValidID(projectID) {
		return false
	}
	root, err := filepath.EvalSymlinks(media.Folder(h.cfg.MediaDir, projectID))
	return err == nil && within(resolved, root)
}

// within reports whether path is inside root (both absolute, resolved).
func within(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

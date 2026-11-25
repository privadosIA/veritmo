package workspace

// export.go downloads a project: as a dataset in the format `orchestrator
// import` reads, or as a package with its scenes.yaml and sources (see
// specs/project-management).

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	"veritmo/internal/project/importer"
	"veritmo/internal/server/api"
)

// GET /api/projects/{id}/export?format=dataset|zip&lang=es|en (lang: the language of the recipe inside the zip)
func (h *Handler) handleProjectExport(w http.ResponseWriter, r *http.Request) {
	id, ok := h.RequireProject(w, r)
	if !ok {
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "dataset"
	}
	switch format {
	case "dataset":
		cfg, err := h.ProjectConfig(id)
		if err != nil {
			api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
			return
		}
		ds, err := importer.ExportDataset(cfg)
		if err != nil {
			api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
			return
		}
		notes, _ := json.Marshal(ds.Notes)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", id+".dataset.json"))
		w.Header().Set("X-Export-Notes", url.QueryEscape(string(notes)))
		w.Header().Set("X-Export-Count", fmt.Sprint(ds.Exported))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(ds.JSON)
	case "zip":
		// Written to a temporary file first, not to memory: a failure must not leave a half-written download, and a
		// project with big sources must not need that much memory.
		tmp, err := os.CreateTemp("", "project-export-*.zip")
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
			return
		}
		defer func() { tmp.Close(); os.Remove(tmp.Name()) }()
		if err := h.MaterializeProject(id); err != nil { // the package is made from the folder
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
			return
		}
		if err := WriteProjectPackage(h.host.Catalog(), tmp, h.ProjectDir(id), h.host.AssetsRoot(), r.URL.Query().Get("lang")); err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
			return
		}
		size, err := tmp.Seek(0, io.SeekEnd)
		if err == nil {
			_, err = tmp.Seek(0, io.SeekStart)
		}
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", id+".zip"))
		w.Header().Set("Content-Length", fmt.Sprint(size))
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, tmp)
	default:
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("format must be dataset or zip, got %q", format))
	}
}

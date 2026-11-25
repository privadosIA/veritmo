package workspace

// data.go: the tools for the data of the server (spec data-store): its status, the import of the state kept in files, the export and the
// cleaning of the files left behind. The work is done by package store.

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"veritmo/internal/platform/store"
	"veritmo/internal/server/api"
)

// GET /api/data/status -> store.Status
func (h *Handler) handleDataStatus(w http.ResponseWriter, r *http.Request) {
	st, err := h.cfg.Store.Status()
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, st)
}

// DataImportRequest is the body of POST /api/data/import.
type DataImportRequest struct {
	Entity string `json:"entity"`
}

// POST /api/data/import {entity?} -> {imports: [record]}
//
// Runs the import of one entity (whatever its state) or of all that have not been imported. Files that have no row become rows; rows
// that exist are untouched.
func (h *Handler) handleDataImport(w http.ResponseWriter, r *http.Request) {
	var req DataImportRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "invalid JSON body: "+err.Error())
		return
	}
	recs, err := h.cfg.Store.RunImports(r.Context(), req.Entity)
	switch {
	case errors.Is(err, store.ErrUnknownEntity):
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, err.Error())
		return
	case errors.Is(err, store.ErrImportRunning):
		api.WriteError(w, http.StatusConflict, api.CodeConflict, err.Error())
		return
	case err != nil:
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	if recs == nil {
		recs = []store.ImportRecord{}
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"imports": recs})
}

// GET /api/data/export -> a .zip
func (h *Handler) handleDataExport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="veritmo-data-`+time.Now().UTC().Format("20060102-150405")+`.zip"`)
	if err := h.cfg.Store.Export(w); err != nil {
		// The zip may be half written: the status cannot change any more, so the connection is cut and the error is logged.
		slog.Error("data export failed", "err", err)
		panic(http.ErrAbortHandler)
	}
}

// DataCleanRequest is the body of POST /api/data/legacy/clean.
type DataCleanRequest struct {
	Entities []string `json:"entities"`
}

// POST /api/data/legacy/clean {entities} -> {cleaned: [{entity, deleted, bytes}]}
func (h *Handler) handleDataLegacyClean(w http.ResponseWriter, r *http.Request) {
	var req DataCleanRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "invalid JSON body: "+err.Error())
		return
	}
	if len(req.Entities) == 0 {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "name the entities whose files are cleaned")
		return
	}
	res, err := h.cfg.Store.CleanLegacy(req.Entities)
	switch {
	case errors.Is(err, store.ErrUnknownEntity):
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, err.Error())
		return
	case errors.Is(err, store.ErrLegacyRefused):
		api.WriteError(w, http.StatusConflict, api.CodeConflict, err.Error())
		return
	case err != nil:
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	if res == nil {
		res = []store.CleanResult{}
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"cleaned": res})
}

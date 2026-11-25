//go:build !private

package models

import (
	"net/http"

	"veritmo/internal/server/api"
)

// GET /api/civitai/resolve: the public edition has no Civitai lookup.
func (h *Handler) handleCivitaiResolve(w http.ResponseWriter, r *http.Request) {
	api.WriteError(w, http.StatusNotFound, api.CodeNotFound, "this edition does not resolve Civitai addresses")
}

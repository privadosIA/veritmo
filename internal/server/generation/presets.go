package generation

// presets.go is the API of the styles (spec motion-presets): a design, a rhythm and a list of parts drawn from a
// reference. A style is kept when its reference is deleted. Making a piece with one is in music_musicmotion.go.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"veritmo/internal/platform/store"

	"veritmo/internal/music/motionref"
	"veritmo/internal/music/musicmotion"
	"veritmo/internal/platform/settings"
	"veritmo/internal/server/api"
)

// PresetStore is the folder of the styles, under the settings (where they are when it is first used).
func (h *Handler) PresetStore() *motionref.PresetStore {
	h.presetsMu.Lock()
	defer h.presetsMu.Unlock()
	dir := filepath.Join(settings.Dir(), "music-motion", "presets")
	if h.presets == nil || h.presets.Dir != dir {
		h.presets = motionref.NewPresetStoreDB(h.ws.Store())
		h.presets.Dir = dir
	}
	return h.presets
}

type presetRequest struct {
	// ReferenceID is the reference the style is drawn from.
	ReferenceID string `json:"reference_id"`
	// Name is the name of the style; empty takes the title of the reference.
	Name string `json:"name"`
	// Provider replaces the routing of the assistant that chooses the parts, for this call only.
	Provider string `json:"provider"`
	// Design, Rhythm and Parts make a style from scratch (no ReferenceID); they are validated as a replacement is.
	Design *musicmotion.Design `json:"design"`
	Rhythm *float64            `json:"rhythm"`
	Parts  []string            `json:"parts"`
}

type presetPut struct {
	Name   string             `json:"name"`
	Design musicmotion.Design `json:"design"`
	Rhythm float64            `json:"rhythm"`
	Parts  []string           `json:"parts"`
}

// POST /api/music-motion/presets
func (h *Handler) handlePresetCreate(w http.ResponseWriter, r *http.Request) {
	var req presetRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if req.ReferenceID == "" {
		h.createPresetFromScratch(w, req)
		return
	}
	if req.Design != nil || req.Rhythm != nil || req.Parts != nil {
		api.Refuse(w, "send either reference_id or design, rhythm and parts, not both")
		return
	}
	ref, err := h.assistant.RefStore().Get(req.ReferenceID)
	if err != nil {
		api.Refuse(w, "the reference %q does not exist", req.ReferenceID)
		return
	}
	if name := strings.TrimSpace(req.Name); name != "" {
		ref.Title = name
	}
	wr, ctx := h.host.MotionWriter(r.Context(), req.Provider)
	p, err := motionref.FromReference(ctx, wr, h.host.MotionCatalog(), ref)
	if err != nil {
		api.Refuse(w, "%v", err)
		return
	}
	p, err = h.PresetStore().Add(p)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	api.WriteJSON(w, http.StatusCreated, p)
}

// createPresetFromScratch keeps a style made of the fields of the request, with the validation of a replacement.
func (h *Handler) createPresetFromScratch(w http.ResponseWriter, req presetRequest) {
	if req.Design == nil || req.Rhythm == nil {
		api.Refuse(w, "without reference_id a style needs name, design and rhythm")
		return
	}
	p := musicmotion.Preset{Name: strings.TrimSpace(req.Name), Design: *req.Design, Rhythm: *req.Rhythm, Parts: req.Parts, Adjustments: []string{}}
	if p.Parts == nil {
		p.Parts = []string{}
	}
	if err := p.Validate(h.host.MotionCatalog()); err != nil {
		api.Refuse(w, "%v", err)
		return
	}
	p, err := h.PresetStore().Add(p)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	api.WriteJSON(w, http.StatusCreated, p)
}

// GET /api/music-motion/presets
//
// With q, sort, limit or offset it is a searched or paged list (X-Total-Count and Link say where the others are); without them it answers
// every style as it always did.
func (h *Handler) handlePresetList(w http.ResponseWriter, r *http.Request) {
	params, err := store.ParseList(r, motionref.PresetsListSpec)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	if !params.Paged {
		api.WriteJSON(w, http.StatusOK, map[string]any{"presets": h.PresetStore().List()})
		return
	}
	list, total, err := h.PresetStore().Page(params)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	store.WriteListHeaders(w, r, total, params)
	api.WriteJSON(w, http.StatusOK, map[string]any{"presets": list})
}

func (h *Handler) presetFromPath(w http.ResponseWriter, r *http.Request) (musicmotion.Preset, bool) {
	p, err := h.PresetStore().Get(r.PathValue("id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("style %q not found", r.PathValue("id")))
		return musicmotion.Preset{}, false
	}
	return p, true
}

// GET /api/music-motion/presets/{id}
func (h *Handler) handlePresetGet(w http.ResponseWriter, r *http.Request) {
	if p, ok := h.presetFromPath(w, r); ok {
		api.WriteJSON(w, http.StatusOK, p)
	}
}

// PUT /api/music-motion/presets/{id}
func (h *Handler) handlePresetPut(w http.ResponseWriter, r *http.Request) {
	p, ok := h.presetFromPath(w, r)
	if !ok {
		return
	}
	var req presetPut
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	p.Name, p.Design, p.Rhythm, p.Parts = strings.TrimSpace(req.Name), req.Design, req.Rhythm, req.Parts
	if p.Parts == nil {
		p.Parts = []string{}
	}
	if err := p.Validate(h.host.MotionCatalog()); err != nil {
		api.Refuse(w, "%v", err)
		return
	}
	if err := h.PresetStore().Replace(p); err != nil {
		if errors.Is(err, motionref.ErrPresetNotFound) {
			api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("style %q not found", p.ID))
			return
		}
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, p)
}

// DELETE /api/music-motion/presets/{id}
func (h *Handler) handlePresetDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := h.presetFromPath(w, r)
	if !ok {
		return
	}
	if err := h.PresetStore().Delete(p.ID); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

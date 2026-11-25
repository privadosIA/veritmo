// Package prompts is the store of prompt presets: prompts of a house style that people pick from and that the assistant receives as examples. They
// are rows of the database (see internal/prompts), one per preset, that the command `veritmo prompts fetch` writes to the same database while the
// server runs, so a list asked after it sees them with no restart.
package prompts

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	pstore "veritmo/internal/platform/store"
	store "veritmo/internal/prompts"
	"veritmo/internal/server/api"
)

// Preset is one prompt (see internal/prompts).
type Preset = store.Preset

const (
	maxImportItems = 20000
	maxImportBody  = 32 << 20
	defaultLimit   = 50
	maxLimit       = 200
)

// BundleFormat and BundleVersion identify a bundle of presets.
const (
	BundleFormat  = "prompt-presets"
	BundleVersion = 1
)

// Host is what the store needs from the server.
type Host interface {
	// Presets is where the presets are kept.
	Presets() store.Store
	// ShowsAdult says whether the server shows adult content (a censored one hides the presets marked nsfw).
	ShowsAdult() bool
}

// Handler serves the store.
type Handler struct {
	host Host
}

// New makes the store.
func New(h Host) *Handler { return &Handler{host: h} }

// Register registers the endpoints.
func (h *Handler) Register(handle api.HandleFunc) {
	handle("GET /api/prompts", h.handleList)
	handle("POST /api/prompts", h.handleCreate)
	handle("POST /api/prompts/import", h.handleImport)
	handle("GET /api/prompts/{id}", h.handleGet)
	handle("PUT /api/prompts/{id}", h.handlePut)
	handle("DELETE /api/prompts/{id}", h.handleDelete)
}

// visible says whether the server shows a preset: a censored one does not show the presets marked nsfw.
func (h *Handler) visible(p *Preset) bool { return !p.NSFW || h.host.ShowsAdult() }

// fail answers a failure of the store as a 500.
func fail(w http.ResponseWriter, err error) {
	api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
}

// listParams reads limit (default 50, at most 200: a bigger one is 200), offset, sort (title or created_at, with a - for descending order) and q.
func listParams(r *http.Request) (pstore.ListParams, error) {
	query := r.URL.Query()
	limit := defaultLimit
	if v := query.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return pstore.ListParams{}, errors.New("limit must be a positive integer")
		}
		limit = min(n, maxLimit)
	}
	// offset, sort and q are the ones of every list (api-listing): the limit is read above because this one clamps instead of refusing
	rest := r.Clone(r.Context())
	u := *r.URL
	q := u.Query()
	q.Del("limit")
	u.RawQuery = q.Encode()
	rest.URL = &u
	p, err := pstore.ParseList(rest, store.ListSpec)
	if err != nil {
		return p, err
	}
	p.Limit = limit
	p.Paged = p.Paged || query.Has("limit")
	return p, nil
}

// GET /api/prompts?kind=&q=&tags=&limit=&offset=&sort= -> {items, total}; X-Total-Count and Link when a page parameter is given.
func (h *Handler) handleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kind := q.Get("kind")
	if kind != "" && !store.ValidKind(kind) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("kind must be one of %s", strings.Join(store.Kinds, ", ")))
		return
	}
	params, err := listParams(r)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	var want []string
	for _, t := range strings.Split(q.Get("tags"), ",") {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			want = append(want, t)
		}
	}
	items, total, err := h.host.Presets().List(store.Filter{Kind: kind, Tags: want, HideNSFW: !h.host.ShowsAdult()}, params)
	if err != nil {
		fail(w, err)
		return
	}
	pstore.WriteListHeaders(w, r, total, params)
	api.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

// GET /api/prompts/{id}: one preset.
func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request) {
	p, ok := h.find(w, r.PathValue("id"))
	if !ok {
		return
	}
	api.WriteJSON(w, http.StatusOK, p)
}

// find reads a preset the server shows; it writes the 404 itself.
func (h *Handler) find(w http.ResponseWriter, id string) (*Preset, bool) {
	p, err := h.host.Presets().Get(id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !h.visible(p)) {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("prompt %q not found", id))
		return nil, false
	}
	if err != nil {
		fail(w, err)
		return nil, false
	}
	return p, true
}

// POST /api/prompts {kind, title?, prompt, negative?, tags?, engine?, language?, nsfw?} -> 201 Preset
func (h *Handler) handleCreate(w http.ResponseWriter, r *http.Request) {
	var p Preset
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&p); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if why := store.Clean(&p); why != "" {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, why)
		return
	}
	ids, err := h.host.Presets().FreshIDs(1)
	if err != nil {
		fail(w, err)
		return
	}
	p.ID, p.Own, p.Source, p.SourceID, p.Licence = ids[0], true, "user", "", ""
	p.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := h.host.Presets().Save([]*Preset{&p}); err != nil {
		fail(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, p)
}

// PUT /api/prompts/{id} {kind?, title?, prompt, negative?, tags?, engine?, language?, nsfw?}: replaces the fields of a preset of the person's own with the
// validation a new preset passes; its id, its origin and its date stay. 403 for a preset that is not the person's own.
func (h *Handler) handlePut(w http.ResponseWriter, r *http.Request) {
	old, ok := h.find(w, r.PathValue("id"))
	if !ok {
		return
	}
	if !old.Own {
		api.WriteError(w, http.StatusForbidden, api.CodeForbidden, "a bundled preset is read-only: only your own can be changed")
		return
	}
	var p Preset
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&p); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if p.Kind == "" {
		p.Kind = old.Kind
	}
	if p.Kind != old.Kind {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "the kind of a preset cannot be changed")
		return
	}
	if why := store.Clean(&p); why != "" {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, why)
		return
	}
	p.ID, p.Own, p.Source, p.SourceID, p.Licence, p.CreatedAt = old.ID, true, old.Source, old.SourceID, old.Licence, old.CreatedAt
	if err := h.host.Presets().Save([]*Preset{&p}); err != nil {
		fail(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, p)
}

// DELETE /api/prompts/{id}: the person's own presets only.
func (h *Handler) handleDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := h.find(w, r.PathValue("id"))
	if !ok {
		return
	}
	if !p.Own {
		api.WriteError(w, http.StatusForbidden, api.CodeForbidden, "a bundled preset is read-only: only your own can be deleted")
		return
	}
	if err := h.host.Presets().Remove(p.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type refusal struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}

// POST /api/prompts/import {format, version, items} -> {added, already, refused}
func (h *Handler) handleImport(w http.ResponseWriter, r *http.Request) {
	var bundle struct {
		Format  string   `json:"format"`
		Version int      `json:"version"`
		Items   []Preset `json:"items"`
	}
	api.RaiseBodyLimit(w, r, maxImportBody)
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxImportBody)).Decode(&bundle); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if bundle.Format != BundleFormat || bundle.Version != BundleVersion {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("the bundle must be format %q version %d", BundleFormat, BundleVersion))
		return
	}
	if len(bundle.Items) > maxImportItems {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("a bundle has at most %d items", maxImportItems))
		return
	}
	presets := h.host.Presets()
	have, err := presets.Keys()
	if err != nil {
		fail(w, err)
		return
	}
	already, refused := 0, []refusal{}
	var accepted []*Preset
	for i := range bundle.Items {
		p := bundle.Items[i]
		p.Source, p.Licence = strings.TrimSpace(p.Source), strings.TrimSpace(p.Licence)
		if p.Source == "" || p.Licence == "" {
			refused = append(refused, refusal{i, "source and licence are required"})
			continue
		}
		if why := store.Clean(&p); why != "" {
			refused = append(refused, refusal{i, why})
			continue
		}
		if k := store.Key(&p); k != "" {
			if _, dup := have[k]; dup {
				already++
				continue
			}
			have[k] = ""
		}
		accepted = append(accepted, &p)
	}
	ids, err := presets.FreshIDs(len(accepted))
	if err != nil {
		fail(w, err)
		return
	}
	created := time.Now().UTC().Format(time.RFC3339)
	for i, p := range accepted {
		p.ID, p.Own, p.CreatedAt = ids[i], false, created
	}
	if err := presets.Save(accepted); err != nil {
		fail(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"added": len(accepted), "already": already, "refused": refused})
}

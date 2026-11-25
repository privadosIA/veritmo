package music

// partslist.go: the list of the parts of the catalog and the creation of a part of the user's (spec music-motion-parts, "The parts can be
// listed in pages and created").

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"veritmo/internal/music/musicmotion"
	"veritmo/internal/platform/store"
	"veritmo/internal/server/api"
)

// partsListSpec says what the list of parts can be sorted by.
var partsListSpec = store.ListSpec{
	Sortable: map[string]string{"name": "name", "updated_at": "updated_at"},
	Default:  "name",
}

// GET /api/music-motion/parts?source=&level=&scene=&piece=&q=&sort=&limit=&offset=
//
// The parts of the catalog, a list that can be paged. By default the scenes written for a piece are left out; scene=true lists only them and
// piece=<id> only those of that piece (it implies scene=true).
func (h *Handler) handleMusicMotionPartsList(w http.ResponseWriter, r *http.Request) {
	p, err := store.ParseList(r, partsListSpec)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	q := r.URL.Query()
	source := q.Get("source")
	if source != "" && source != "user" && source != "builtin" {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "source must be user or builtin")
		return
	}
	scene := q.Get("scene")
	if scene != "" && scene != "true" && scene != "false" {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "scene must be true or false")
		return
	}
	level, piece, text := q.Get("level"), q.Get("piece"), strings.ToLower(p.Q)
	cat := h.mmCatalog()
	var parts []musicmotion.Part
	if scene == "true" || piece != "" {
		parts = cat.ListScenes(piece)
	} else {
		parts = cat.List()
	}
	items := make([]musicmotion.Part, 0, len(parts))
	for _, part := range parts {
		if source != "" && part.Source != source {
			continue
		}
		if level != "" && !part.Supports(level) {
			continue
		}
		if text != "" && !strings.Contains(strings.ToLower(part.Name+" "+part.Description), text) {
			continue
		}
		items = append(items, part)
	}
	sortBy, desc := p.Sort, p.Desc
	if sortBy == "" {
		sortBy, desc = "name", false
	}
	sort.SliceStable(items, func(i, j int) bool {
		var less bool
		if sortBy == "updated_at" {
			if !items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
				less = items[i].UpdatedAt.Before(items[j].UpdatedAt)
				return less != desc
			}
		} else if items[i].Name != items[j].Name {
			less = items[i].Name < items[j].Name
			return less != desc
		}
		return items[i].Name < items[j].Name
	})
	total := int64(len(items))
	out := make([]CatalogPart, 0, len(items))
	for _, part := range store.Slice(items, p) {
		out = append(out, catalogPartOf(part))
	}
	store.WriteListHeaders(w, r, total, p)
	api.WriteJSON(w, http.StatusOK, out)
}

// POST /api/music-motion/parts {manifest, fragment}: keeps a new part of the user's after the checks an approval makes.
func (h *Handler) handleMusicMotionPartPost(w http.ResponseWriter, r *http.Request) {
	var req DraftPut
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	e, status, msg := h.draftCheckEngine(r.Context())
	if e == nil {
		api.WriteError(w, status, api.CodeUnavailable, msg)
		return
	}
	part, probs, err := e.CreateUserPart(r.Context(), req.Manifest, req.Fragment)
	switch {
	case errors.Is(err, musicmotion.ErrNameTaken):
		name := ""
		var head struct {
			Name string `json:"name"`
		}
		if json.Unmarshal([]byte(req.Manifest), &head) == nil {
			name = head.Name
		}
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("a part called %q already is in the catalog: choose another name", name))
	case err != nil:
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
	case len(probs) > 0:
		api.Refuse(w, "the part does not pass the checks: %s", strings.Join(probs, "; "))
	default:
		api.WriteJSON(w, http.StatusCreated, catalogPartOf(part))
	}
}

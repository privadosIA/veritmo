package music

// libraryitems.go: the user's characters and backgrounds through the API (spec motion-characters, "The user's characters and backgrounds are
// managed through the API"). The checks of a posted item are the ones the library applies to a folder.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"veritmo/internal/music/musicmotion"
	"veritmo/internal/platform/store"
	"veritmo/internal/server/api"
)

// libraryListSpec says what the lists of characters and backgrounds can be sorted and searched by; they are filtered in memory (the ones of the
// program and the user's are merged).
var libraryListSpec = store.ListSpec{Sortable: map[string]string{"name": "name"}, Searchable: []string{"name", "description"}, Default: "name"}

// LibraryItemPut is the body that creates or replaces a character or a background: its manifest and its drawings by file name.
type LibraryItemPut struct {
	Manifest json.RawMessage   `json:"manifest"`
	Files    map[string]string `json:"files"`
}

func matchesQuery(q string, texts ...string) bool {
	if q == "" {
		return true
	}
	for _, t := range texts {
		if strings.Contains(strings.ToLower(t), strings.ToLower(q)) {
			return true
		}
	}
	return false
}

func libraryParams(w http.ResponseWriter, r *http.Request) (store.ListParams, bool) {
	p, err := store.ParseList(r, libraryListSpec)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return p, false
	}
	return p, true
}

// GET /api/music-motion/library/characters
func (h *Handler) handleLibraryCharacters(w http.ResponseWriter, r *http.Request) {
	p, ok := libraryParams(w, r)
	if !ok {
		return
	}
	out := []CatalogCharacter{}
	for _, c := range h.mmLibrary().Characters() { // by name; a descending sort reverses it below
		if matchesQuery(p.Q, c.Name, c.Description) {
			ex := c.Expressions
			if ex == nil {
				ex = []string{}
			}
			out = append(out, CatalogCharacter{Name: c.Name, Description: c.Description, Poses: c.PoseNames(), Expressions: ex, Source: c.Source})
		}
	}
	if p.Desc {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	total := int64(len(out))
	store.WriteListHeaders(w, r, total, p)
	api.WriteJSON(w, http.StatusOK, map[string]any{"characters": store.Slice(out, p)})
}

// GET /api/music-motion/library/backgrounds
func (h *Handler) handleLibraryBackgrounds(w http.ResponseWriter, r *http.Request) {
	p, ok := libraryParams(w, r)
	if !ok {
		return
	}
	out := []CatalogBackground{}
	for _, b := range h.mmLibrary().Backgrounds() {
		if matchesQuery(p.Q, b.Name, b.Description) {
			out = append(out, CatalogBackground{Name: b.Name, Description: b.Description, Source: b.Source})
		}
	}
	if p.Desc {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	store.WriteListHeaders(w, r, int64(len(out)), p)
	api.WriteJSON(w, http.StatusOK, map[string]any{"backgrounds": store.Slice(out, p)})
}

// libraryKind is what the routes of one kind of item share.
type libraryKind struct {
	kind   string
	noun   string
	exists func(l musicmotion.Library, name string) (source string, ok bool)
}

func characterKind() libraryKind {
	return libraryKind{kind: musicmotion.KindCharacter, noun: "character",
		exists: func(l musicmotion.Library, name string) (string, bool) {
			c, ok := l.Character(name)
			return c.Source, ok
		}}
}

func backgroundKind() libraryKind {
	return libraryKind{kind: musicmotion.KindBackground, noun: "background",
		exists: func(l musicmotion.Library, name string) (string, bool) {
			b, ok := l.Background(name)
			return b.Source, ok
		}}
}

func writeLibraryCharacter(h *Handler, w http.ResponseWriter, status int, name string) {
	c, ok := h.mmLibrary().Character(name)
	if !ok {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("character %q not found", name))
		return
	}
	ex := c.Expressions
	if ex == nil {
		ex = []string{}
	}
	poses := map[string]string{}
	for pose := range c.Poses {
		poses[pose] = c.SVG[pose]
	}
	api.WriteJSON(w, status, LibraryCharacter{Name: c.Name, Width: c.Width, Height: c.Height, Anchor: c.Anchor, DefaultPose: c.DefaultPose,
		DefaultExpression: c.DefaultExpression, Expressions: ex, Poses: poses, Source: c.Source})
}

func writeLibraryBackground(h *Handler, w http.ResponseWriter, status int, name string) {
	b, ok := h.mmLibrary().Background(name)
	if !ok {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("background %q not found", name))
		return
	}
	api.WriteJSON(w, status, LibraryBackground{Name: b.Name, SVG: b.SVG, Source: b.Source})
}

func (h *Handler) readLibraryBody(w http.ResponseWriter, r *http.Request) (LibraryItemPut, bool) {
	var req LibraryItemPut
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return req, false
	}
	if len(req.Manifest) == 0 {
		api.Refuse(w, "the manifest is missing")
		return req, false
	}
	if req.Files == nil {
		req.Files = map[string]string{}
	}
	return req, true
}

// put checks and keeps an item: 422 with the problems, nothing changed.
func (h *Handler) putLibraryItem(w http.ResponseWriter, k libraryKind, name string, req LibraryItemPut) bool {
	if probs := musicmotion.CheckLibraryItem(k.kind, name, req.Manifest, req.Files); len(probs) > 0 {
		api.Refuse(w, "the %s does not pass the checks: %s", k.noun, strings.Join(probs, "; "))
		return false
	}
	if err := musicmotion.DBLibrary(h.ws.Store()).Put(k.kind, name, req.Manifest, req.Files); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return false
	}
	return true
}

func (h *Handler) libraryPost(k libraryKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := h.readLibraryBody(w, r)
		if !ok {
			return
		}
		var head struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(req.Manifest, &head) != nil || head.Name == "" {
			api.Refuse(w, "the manifest has no name")
			return
		}
		if _, exists := k.exists(h.mmLibrary(), head.Name); exists {
			api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("a %s called %q already is in the library: choose another name", k.noun, head.Name))
			return
		}
		if h.putLibraryItem(w, k, head.Name, req) {
			k.showStatus(h, w, http.StatusCreated, head.Name)
		}
	}
}

func (k libraryKind) showStatus(h *Handler, w http.ResponseWriter, status int, name string) {
	if k.kind == musicmotion.KindCharacter {
		writeLibraryCharacter(h, w, status, name)
		return
	}
	writeLibraryBackground(h, w, status, name)
}

// ownItem answers 404 for a name that is not in the library and 403 for one that comes with the program; it says whether the item is the user's.
func (h *Handler) ownItem(w http.ResponseWriter, k libraryKind, name string) bool {
	source, ok := k.exists(h.mmLibrary(), name)
	switch {
	case !ok:
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("%s %q not found", k.noun, name))
	case source != "user":
		api.WriteError(w, http.StatusForbidden, api.CodeForbidden, fmt.Sprintf("the %s %q comes with the program and cannot be changed or deleted", k.noun, name))
	default:
		return true
	}
	return false
}

func (h *Handler) libraryPut(k libraryKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if !h.ownItem(w, k, name) {
			return
		}
		req, ok := h.readLibraryBody(w, r)
		if !ok {
			return
		}
		if h.putLibraryItem(w, k, name, req) {
			k.showStatus(h, w, http.StatusOK, name)
		}
	}
}

func (h *Handler) libraryDelete(k libraryKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if !h.ownItem(w, k, name) {
			return
		}
		if _, err := musicmotion.DBLibrary(h.ws.Store()).Delete(k.kind, name); err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

package music

// musicmotion_models.go: the 3D models of a piece (spec music-motion, "A piece keeps 3D models"). They are files of the
// sources of the piece's project, where the render already looks: a .glb and the pictures its textures name. The 3D pieces of
// the script place them with objects of type model (the contract of blender-engine).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"veritmo/internal/assets3d"
	"veritmo/internal/music/musicmotion"
	"veritmo/internal/project/config"
	"veritmo/internal/server/api"
	"veritmo/internal/server/workspace"
	"veritmo/internal/video/blender"
)

// modelFile is one entry of the list of a piece's models.
type modelFile struct {
	Name string `json:"name"`
	// Kind is "model" (a .glb) or "picture" (what a model's textures name).
	Kind string `json:"kind"`
	Size int64  `json:"size"`
	// A model says what its file holds.
	Clips     *[]string `json:"clips,omitempty"`
	Triangles *int      `json:"triangles,omitempty"`
	Needs     *[]string `json:"needs,omitempty"`
}

// modelKind says what a name of the models of a piece is: "model", "picture" or "" when it is neither.
func modelKind(name string) string {
	switch {
	case config.ValidModelName(name):
		return "model"
	case config.ValidImageName(name):
		return "picture"
	}
	return ""
}

func badModelName(w http.ResponseWriter, name string) {
	api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid name %q: a model is a plain file name ending in .glb, and a picture for its textures ends in .png, .jpg, .jpeg or .webp (letters, digits, '.', '_' or '-')", name))
}

// PUT /api/music-motion/pieces/{id}/models/{name}  (raw body: a .glb, or a picture)
func (h *Handler) handlePieceModelPut(w http.ResponseWriter, r *http.Request) {
	info, ok := h.pieceFromPath(w, r)
	if !ok || h.refuseIfRendering(w, info) {
		return
	}
	name := r.PathValue("name")
	kind := modelKind(name)
	if kind == "" {
		badModelName(w, name)
		return
	}
	api.RaiseBodyLimit(w, r, blender.MaxModelBytes+1)
	data, err := io.ReadAll(io.LimitReader(r.Body, blender.MaxModelBytes+1))
	switch {
	case err != nil:
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("cannot read the upload: %v", err))
		return
	case len(data) == 0:
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "the file is empty")
		return
	case len(data) > blender.MaxModelBytes:
		api.WriteError(w, http.StatusRequestEntityTooLarge, api.CodeInvalid, fmt.Sprintf("the file is larger than %d MB", blender.MaxModelBytes>>20))
		return
	}
	if kind == "model" {
		if _, err := blender.ParseModel(data); err != nil {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("%q is not a usable model: %v", name, err))
			return
		}
	}
	dir := h.pieceSources(info.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	dst := filepath.Join(dir, name)
	if _, err := os.Lstat(dst); err == nil {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("%q already exists in this piece: choose another name", name))
		return
	}
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	defer os.Remove(tmp.Name())
	_, werr := io.Copy(tmp, bytes.NewReader(data))
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), dst)
	}
	if werr != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, werr.Error())
		return
	}
	api.WriteJSON(w, http.StatusCreated, map[string]string{"name": name})
}

// GET /api/music-motion/pieces/{id}/models
func (h *Handler) handlePieceModelsList(w http.ResponseWriter, r *http.Request) {
	info, ok := h.pieceFromPath(w, r)
	if !ok {
		return
	}
	out := []modelFile{}
	entries, _ := os.ReadDir(h.pieceSources(info.ID))
	for _, e := range entries {
		kind := modelKind(e.Name())
		if kind == "" || !e.Type().IsRegular() {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		f := modelFile{Name: e.Name(), Kind: kind, Size: fi.Size()}
		if kind == "model" {
			if m, err := blender.ReadModel(filepath.Join(h.pieceSources(info.ID), e.Name())); err == nil {
				clips, needs := append([]string{}, m.Clips...), append([]string{}, m.Needs...)
				f.Clips, f.Triangles, f.Needs = &clips, &m.Triangles, &needs
			}
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	api.WriteJSON(w, http.StatusOK, map[string]any{"models": out})
}

// DELETE /api/music-motion/pieces/{id}/models/{name}
func (h *Handler) handlePieceModelDelete(w http.ResponseWriter, r *http.Request) {
	info, ok := h.pieceFromPath(w, r)
	if !ok || h.refuseIfRendering(w, info) {
		return
	}
	name := r.PathValue("name")
	if modelKind(name) == "" {
		badModelName(w, name)
		return
	}
	path := filepath.Join(h.pieceSources(info.ID), name)
	if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("%q is not in this piece", name))
		return
	}
	if script, has := musicmotion.LoadScriptAt(h.place(info.ID)); has {
		if piece := h.pieceUsing(script, h.pieceSources(info.ID), name); piece != "" {
			api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("the 3D piece %q of the script places %q: change or remove it first", piece, name))
			return
		}
	}
	if err := os.Remove(path); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// pieceUsing is the id of the first 3D piece of the script whose description places the model name, or whose model needs the
// picture name; "" when none does.
func (h *Handler) pieceUsing(script musicmotion.Script, sources, name string) string {
	for _, p := range script.Pieces {
		if p.Blender == nil {
			continue
		}
		if spec, err := blender.Decode(p.Blender.Description); err == nil && spec.UsesSource(sources, name) {
			return p.ID
		}
	}
	return ""
}

// SetModelLibrary gives the handler the library of 3D model packs: a model of it can be added to a piece.
func (h *Handler) SetModelLibrary(lib *assets3d.Library) { h.models3d = lib }

// POST /api/music-motion/pieces/{id}/models  {"from": "<pack>/<model>", "name"?}
//
// Copies a model of the library into the piece's models with the pictures it needs, as the route of a project does.
func (h *Handler) handlePieceModelAdd(w http.ResponseWriter, r *http.Request) {
	info, ok := h.pieceFromPath(w, r)
	if !ok || h.refuseIfRendering(w, info) {
		return
	}
	var req struct {
		From string `json:"from"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if h.models3d == nil {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeEngineNotRunning, "the library of 3D models is not available")
		return
	}
	res, err := h.models3d.CopyModel(req.From, h.pieceSources(info.ID), req.Name)
	if err != nil {
		workspace.WriteModelCopyError(w, err)
		return
	}
	status := http.StatusCreated
	if res.Already {
		status = http.StatusOK
	}
	api.WriteJSON(w, status, res)
}

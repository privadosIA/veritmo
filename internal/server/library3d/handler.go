// Package library3d serves the library of 3D model packs (spec model-library): the packs the server knows and the ones a person
// imported, their installation, the catalog of their models and its thumbnails. The work is in internal/assets3d.
package library3d

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"veritmo/internal/assets3d"
	"veritmo/internal/assets3d/generate"
	"veritmo/internal/platform/store"
	"veritmo/internal/project/config"
	"veritmo/internal/server/api"
	"veritmo/internal/server/workspace"
)

// Host is what the handler asks of the server around it.
type Host interface {
	// ShowsAdult says whether the server shows content that is marked for people of 18 or older (it does not by default).
	ShowsAdult() bool
	// RefuseAdultConfig answers the 422 of a request that would make content for adults on a server that does not keep it, and says whether it did.
	RefuseAdultConfig(w http.ResponseWriter, adult *bool) bool
	// ProjectVisible says whether the content mode lets a caller see a project (a generation is one).
	ProjectVisible(id string) bool
	// PictureDefaults are the image settings a project draws the picture of a prompt with: the best image model installed here.
	PictureDefaults() (config.Defaults, error)
}

// Handler serves the routes under /api/models3d/.
type Handler struct {
	lib      *assets3d.Library
	svc      *generate.Service
	host     Host
	ws       Workspace
	installs installs
	// Fetcher downloads the weights of TripoSR for the .NET service (the zero value goes to Hugging Face and GitHub).
	Fetcher generate.Fetcher
}

// SetHost gives the handler the server's answers; without one it behaves as a server that hides what is for adults.
func (h *Handler) SetHost(host Host) { h.host = host }

func (h *Handler) showsAdult() bool { return h.host != nil && h.host.ShowsAdult() }

// New makes the handler of a library.
func New(lib *assets3d.Library) *Handler { return &Handler{lib: lib} }

// Library is the library the handler serves.
func (h *Handler) Library() *assets3d.Library { return h.lib }

// Register adds the routes.
func (h *Handler) Register(handle api.HandleFunc) {
	handle("GET /api/models3d/packs", h.handlePacks)
	handle("POST /api/models3d/packs/import", h.handleImport)
	handle("GET /api/models3d/packs/{id}", h.handlePack)
	handle("POST /api/models3d/packs/{id}/install", h.handleInstall)
	handle("POST /api/models3d/packs/{id}/cancel", h.handleCancel)
	handle("DELETE /api/models3d/packs/{id}", h.handleRemove)
	handle("GET /api/models3d/models", h.handleModels)
	handle("GET /api/models3d/models/{pack}/{name}", h.handleModel)
	handle("GET /api/models3d/models/{pack}/{name}/thumbnail", h.handleThumbnail)
	handle("GET /api/models3d/models/{pack}/{name}/file", h.handleFile)
	h.registerEngines(handle)
	handle("POST /api/models3d/generations", h.handleGenerationCreate)
	handle("GET /api/models3d/generations", h.handleGenerationList)
	handle("GET /api/models3d/generations/{id}", h.handleGenerationGet)
	handle("POST /api/models3d/generations/{id}/cancel", h.handleGenerationCancel)
	handle("DELETE /api/models3d/generations/{id}", h.handleGenerationDelete)
}

// fail answers an error of the library with the status of its kind.
func fail(w http.ResponseWriter, err error) {
	var unavailable *workspace.BlenderUnavailableError
	switch {
	case errors.Is(err, assets3d.ErrNotFound):
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, err.Error())
	case errors.Is(err, assets3d.ErrConflict):
		api.WriteError(w, http.StatusConflict, api.CodeConflict, err.Error())
	case errors.Is(err, assets3d.ErrInvalid):
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
	case errors.Is(err, assets3d.ErrNoModels):
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
	case errors.As(err, &unavailable):
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeEngineNotRunning, err.Error())
	case errors.Is(err, assets3d.ErrNoPreview):
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeEngineNotRunning, "the model has no preview picture in its pack and Blender cannot draw one here: install Blender (see the Environment screen)")
	default:
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
	}
}

// GET /api/models3d/packs
func (h *Handler) handlePacks(w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, http.StatusOK, map[string]any{"packs": h.lib.Packs()})
}

// GET /api/models3d/packs/{id}
func (h *Handler) handlePack(w http.ResponseWriter, r *http.Request) {
	p, err := h.lib.Pack(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, p)
}

// POST /api/models3d/packs/{id}/install
func (h *Handler) handleInstall(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.lib.Install(id); err != nil {
		fail(w, err)
		return
	}
	p, _ := h.lib.Pack(id)
	api.WriteJSON(w, http.StatusAccepted, p)
}

// POST /api/models3d/packs/{id}/cancel
func (h *Handler) handleCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.lib.Cancel(id); err != nil {
		fail(w, err)
		return
	}
	h.lib.Wait(id)
	p, _ := h.lib.Pack(id)
	api.WriteJSON(w, http.StatusOK, p)
}

// DELETE /api/models3d/packs/{id}
func (h *Handler) handleRemove(w http.ResponseWriter, r *http.Request) {
	if err := h.lib.Remove(r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/models3d/packs/import?id=&name=&author=&licence=&source_url=   (body: the zip)
func (h *Handler) handleImport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	meta := assets3d.ImportMeta{ID: q.Get("id"), Name: q.Get("name"), Author: q.Get("author"), Licence: q.Get("licence"), SourceURL: q.Get("source_url")}
	api.RaiseBodyLimit(w, r, assets3d.MaxZipBytes+1)
	p, err := h.lib.Import(r.Context(), meta, r.Body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			api.WriteError(w, http.StatusRequestEntityTooLarge, api.CodeInvalid, fmt.Sprintf("the zip is larger than %d MB", assets3d.MaxZipBytes>>20))
			return
		}
		fail(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, p)
}

// GET /api/models3d/models?pack=&q=&tag=&has_clips=&source=&engine=&project=&limit=&offset=
func (h *Handler) handleModels(w http.ResponseWriter, r *http.Request) {
	p, err := store.ParseList(r, store.ListSpec{})
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	q := r.URL.Query()
	hasClips := false
	if v := strings.ToLower(q.Get("has_clips")); v != "" {
		switch v {
		case "true":
			hasClips = true
		case "false":
		default:
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "has_clips must be true or false")
			return
		}
	}
	source, engine := q.Get("source"), q.Get("engine")
	if source != "" && source != "catalog" && source != "user" && source != "generated" {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("source must be catalog, user or generated, got %q", source))
		return
	}
	if _, ok := generate.Info(engine); engine != "" && !ok {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("engine must be one of %s, got %q", strings.Join(generate.Engines(), ", "), engine))
		return
	}
	models, total := h.lib.Models(assets3d.Query{Pack: q.Get("pack"), Q: p.Q, Tag: q.Get("tag"), HasClips: hasClips, Source: source, Engine: engine,
		Project: q.Get("project"), HideAdult: !h.showsAdult(), Limit: p.Limit, Offset: p.Offset})
	store.WriteListHeaders(w, r, int64(total), p)
	api.WriteJSON(w, http.StatusOK, map[string]any{"models": models, "total": total})
}

// GET /api/models3d/models/{pack}/{name}
func (h *Handler) handleModel(w http.ResponseWriter, r *http.Request) {
	m, err := h.lib.Model(r.PathValue("pack"), r.PathValue("name"))
	if err != nil {
		fail(w, err)
		return
	}
	if m.Adult && !h.showsAdult() {
		fail(w, fmt.Errorf("%w: model %q", assets3d.ErrNotFound, r.PathValue("pack")+"/"+r.PathValue("name")))
		return
	}
	api.WriteJSON(w, http.StatusOK, m)
}

// GET /api/models3d/models/{pack}/{name}/thumbnail
func (h *Handler) handleThumbnail(w http.ResponseWriter, r *http.Request) {
	if m, err := h.lib.Model(r.PathValue("pack"), r.PathValue("name")); err == nil && m.Adult && !h.showsAdult() {
		fail(w, fmt.Errorf("%w: model %q", assets3d.ErrNotFound, r.PathValue("pack")+"/"+r.PathValue("name")))
		return
	}
	b, ct, err := h.lib.Thumbnail(r.Context(), r.PathValue("pack"), r.PathValue("name"))
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(b)
}

// GET /api/models3d/models/{pack}/{name}/file
//
// The binary glTF as the library keeps it. A model does not change until it is removed or replaced, so the answer carries an ETag (its size
// and modification time) and http.ServeContent answers If-None-Match with 304 and a Range with 206.
func (h *Handler) handleFile(w http.ResponseWriter, r *http.Request) {
	pack, name := r.PathValue("pack"), r.PathValue("name")
	if m, err := h.lib.Model(pack, name); err == nil && m.Adult && !h.showsAdult() {
		fail(w, fmt.Errorf("%w: model %q", assets3d.ErrNotFound, pack+"/"+name))
		return
	}
	path, err := h.lib.ModelFile(pack, name)
	if err != nil {
		fail(w, err)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		fail(w, fmt.Errorf("%w: the file of model %q is gone", assets3d.ErrNotFound, pack+"/"+name))
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		fail(w, fmt.Errorf("%w: the file of model %q is gone", assets3d.ErrNotFound, pack+"/"+name))
		return
	}
	w.Header().Set("Content-Type", "model/gltf-binary")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("ETag", `"`+strconv.FormatInt(fi.Size(), 16)+"-"+strconv.FormatInt(fi.ModTime().UnixNano(), 16)+`"`)
	http.ServeContent(w, r, name+".glb", fi.ModTime(), f)
}

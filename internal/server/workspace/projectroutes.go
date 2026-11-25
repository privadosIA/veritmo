package workspace

// projectroutes.go: one project and its versions through the API (spec project-management, "One project can be read and its details edited" and
// "The definition of a project keeps its versions").

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"gopkg.in/yaml.v3"

	"veritmo/internal/platform/store"
	"veritmo/internal/project/config"
	"veritmo/internal/server/api"
)

// GET /api/projects/{id}: the project as the list shows it.
func (h *Handler) handleProjectGet(w http.ResponseWriter, r *http.Request) {
	id, ok := h.RequireProject(w, r)
	if !ok {
		return
	}
	row, err := h.projects.Get(id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("project %q not found", id))
		return
	}
	api.WriteJSON(w, http.StatusOK, summaryOf(row))
}

// PATCH /api/projects/{id} {name?, client?, adult?}: edits those details of the definition as a save does (the text it replaces is a version).
// The three are written into the text itself, so that its comments and its order stay; any other field is a 400.
func (h *Handler) handleProjectPatch(w http.ResponseWriter, r *http.Request) {
	id, ok := h.RequireProject(w, r)
	if !ok {
		return
	}
	var raw map[string]json.RawMessage
	if !api.DecodeJSONLimited(w, r, &raw, api.MaxConfigBody) {
		return
	}
	for k := range raw {
		if k != "name" && k != "client" && k != "adult" {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("the field %q cannot be changed this way: only name, client and adult", k))
			return
		}
	}
	if len(raw) == 0 {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "nothing to change: send name, client or adult")
		return
	}
	if runID, busy := h.ProjectBusy(id); busy {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("project %q has an active or queued run (%s)", id, runID))
		return
	}
	definition, err := h.ProjectDefinition(id)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(definition, &doc); err != nil || doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, "the definition of the project is not valid, so its details cannot be edited: fix it with PUT /api/projects/{id}/config")
		return
	}
	m := doc.Content[0]
	if v, ok := raw["name"]; ok {
		var name string
		if json.Unmarshal(v, &name) != nil || name == "" {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "name must be a non-empty text")
			return
		}
		setNode(m, "project", name, "!!str")
	}
	if v, ok := raw["client"]; ok {
		var client *string
		if json.Unmarshal(v, &client) != nil {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "client must be a text or null")
			return
		}
		switch {
		case client == nil || *client == "":
			removeNode(m, "client")
		case !config.ValidClient(*client):
			api.Refuse(w, "client %q is not a client name: use %s, one to 32 lowercase letters, digits or hyphens", *client, config.ClientPatternText)
			return
		default:
			setNode(m, "client", *client, "!!str")
		}
	}
	if v, ok := raw["adult"]; ok {
		var adult *bool
		if json.Unmarshal(v, &adult) != nil {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "adult must be true, false or null")
			return
		}
		if adult == nil {
			removeNode(m, "adult")
		} else {
			setNode(m, "adult", strconv.FormatBool(*adult), "!!bool")
		}
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	if !h.checkDefinition(w, id, out) {
		return
	}
	if err := h.SaveProjectDefinition(id, out, "patch"); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	row, _ := h.projects.Get(id)
	api.WriteJSON(w, http.StatusOK, summaryOf(row))
}

// checkDefinition validates a text as any save does (the loader of the pipeline, the content mode, the files of the folder) and writes the 422 itself.
func (h *Handler) checkDefinition(w http.ResponseWriter, id string, text []byte) bool {
	cfg, err := config.LoadBytes(text)
	if err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
		return false
	}
	if h.host.RefuseAdultConfig(w, cfg.Adult) {
		return false
	}
	if err := h.validateInDir(cfg, h.ProjectDir(id)); err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
		return false
	}
	return true
}

func setNode(m *yaml.Node, key, value, tag string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			v := m.Content[i+1]
			v.Kind, v.Tag, v.Value, v.Style, v.Content = yaml.ScalarNode, tag, value, 0, nil
			return
		}
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value})
}

func removeNode(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

// versionView is a version as the API lists it.
type versionView struct {
	Version   int    `json:"version"`
	CreatedAt string `json:"created_at"`
	Reason    string `json:"reason"`
	Bytes     int    `json:"bytes"`
}

func viewOfVersion(v ProjectVersion) versionView {
	return versionView{Version: v.Version, CreatedAt: v.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"), Reason: v.Reason, Bytes: v.Bytes}
}

// VersionsListSpec says how the versions can be sorted: by version, newest first by default.
var VersionsListSpec = store.ListSpec{Sortable: map[string]string{"version": "version"}, Default: "-version"}

// GET /api/projects/{id}/versions[?limit=&offset=&sort=]: the versions of the definition, newest first.
func (h *Handler) handleProjectVersions(w http.ResponseWriter, r *http.Request) {
	id, ok := h.RequireProject(w, r)
	if !ok {
		return
	}
	params, err := store.ParseList(r, VersionsListSpec)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	all, err := h.projects.Versions(id) // newest first
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	if params.Sort == "version" && !params.Desc {
		for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
			all[i], all[j] = all[j], all[i]
		}
	}
	out := []versionView{}
	for _, v := range store.Slice(all, params) {
		out = append(out, viewOfVersion(v))
	}
	store.WriteListHeaders(w, r, int64(len(all)), params)
	api.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) versionFromPath(w http.ResponseWriter, r *http.Request) (string, ProjectVersion, bool) {
	id, ok := h.RequireProject(w, r)
	if !ok {
		return "", ProjectVersion{}, false
	}
	n, err := strconv.Atoi(r.PathValue("version"))
	if err != nil || n < 1 {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("version %q does not exist", r.PathValue("version")))
		return "", ProjectVersion{}, false
	}
	v, err := h.projects.Version(id, n)
	switch {
	case errors.Is(err, ErrNoVersion), errors.Is(err, ErrNoProject):
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("version %d of project %q does not exist", n, id))
		return "", ProjectVersion{}, false
	case err != nil:
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return "", ProjectVersion{}, false
	}
	return id, v, true
}

// GET /api/projects/{id}/versions/{version}: the text of a version.
func (h *Handler) handleProjectVersionGet(w http.ResponseWriter, r *http.Request) {
	_, v, ok := h.versionFromPath(w, r)
	if !ok {
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"version": v.Version, "created_at": viewOfVersion(v).CreatedAt, "reason": v.Reason, "bytes": v.Bytes, "yaml": string(v.Definition)})
}

// POST /api/projects/{id}/versions/{version}/restore: makes a version the definition, after the validation any save passes; the text it
// replaces is kept as a version. 409 while the project has an active run.
func (h *Handler) handleProjectVersionRestore(w http.ResponseWriter, r *http.Request) {
	id, v, ok := h.versionFromPath(w, r)
	if !ok {
		return
	}
	if runID, busy := h.ProjectBusy(id); busy {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("project %q has an active or queued run (%s)", id, runID))
		return
	}
	if !h.checkDefinition(w, id, []byte(v.Definition)) {
		return
	}
	if err := h.projects.Restore(id, v.Version); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	h.syncLibraryQuietly(id)
	row, _ := h.projects.Get(id)
	api.WriteJSON(w, http.StatusOK, map[string]any{"restored": v.Version, "project": summaryOf(row)})
}

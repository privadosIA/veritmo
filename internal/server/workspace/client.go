package workspace

// client.go: who made a project, and the two query parameters the listings share (spec add-project-client-and-managed):
// `managed=include` lists the projects of the generation API too, `client=a,b` keeps only those clients.

import (
	"fmt"
	"net/http"
	"strings"

	"veritmo/internal/project/config"
	"veritmo/internal/server/api"
)

// listedManaged says whether a managed project is one `managed=include` lists: a generation, an image edit, a video
// edit or a 3D model generation. The animations of the music motion engine (`api-mm-`) keep their own screen and are never listed.
func listedManaged(id string) bool { return managedKindOfID(id) != "" }

// managedKindOfID is the kind of a managed project read from its name; "" for any other (and for an animation).
func managedKindOfID(id string) ManagedKind {
	switch {
	case strings.HasPrefix(id, GenPrefix):
		return KindGeneration
	case strings.HasPrefix(id, EditPrefix):
		return KindImageEdit
	case strings.HasPrefix(id, VideoPrefix):
		return KindVideoEdit
	case strings.HasPrefix(id, ModelGenPrefix):
		return KindModelGeneration
	}
	return ""
}

// projectClient is who made a project, as its row says (the `client:` of its definition, else the client stored in the request of a
// managed project, else `embedded` for an ordinary project and `unknown` for a managed one). kind is the managed kind, empty for an
// ordinary project.
func (h *Handler) projectClient(id string) (client string, kind ManagedKind) {
	kind = managedKindOfID(id)
	var row ProjectRow
	if err := h.projects.db.DB.Select("id", "client").Where("id = ?", id).Take(&row).Error; err == nil {
		return row.Client, kind
	}
	if kind == "" {
		return clientEmbedded, kind
	}
	return ClientUnknown, kind
}

// listFilter is what the listings take from the request: whether the managed projects are included and which clients
// are kept (none listed: all).
type listFilter struct {
	Include bool
	Clients map[string]bool
}

// parseListFilter reads `managed` and `client`. A bad value is an error whose text names what is accepted (a 400).
func parseListFilter(r *http.Request) (listFilter, error) {
	var f listFilter
	switch v := r.URL.Query().Get("managed"); v {
	case "":
	case "include":
		f.Include = true
	default:
		return f, fmt.Errorf("managed %q is not accepted: the only value is include", v)
	}
	if v := r.URL.Query().Get("client"); v != "" {
		f.Clients = map[string]bool{}
		for _, name := range strings.Split(v, ",") {
			name = strings.TrimSpace(name)
			if name != clientEmbedded && name != ClientUnknown && !config.ValidClient(name) {
				return f, fmt.Errorf("client %q is not a client name: use a comma list of names (%s), embedded or unknown", name, config.ClientPatternText)
			}
			f.Clients[name] = true
		}
	}
	return f, nil
}

// shows says whether a project is in the listing: a managed project only when included, an animation never, and only the
// clients asked for.
func (f listFilter) shows(id, client string) bool {
	if IsManagedProject(id) && !(f.Include && listedManaged(id)) {
		return false
	}
	return f.Clients == nil || f.Clients[client]
}

// listFilterOf parses the request and answers the 400 itself.
func listFilterOf(w http.ResponseWriter, r *http.Request) (listFilter, bool) {
	f, err := parseListFilter(r)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return f, false
	}
	return f, true
}

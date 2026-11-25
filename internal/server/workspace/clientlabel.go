package workspace

// clientlabel.go: who asked for a run. The generation API lets a dashboard name itself (`client`, see
// config.ValidClient); the statistics and the metrics group by it. See spec add-client-label.

import (
	"net/http"
	"strings"

	"veritmo/internal/project/config"
	"veritmo/internal/server/api"
)

// Client names that are not a dashboard's own.
const (
	ClientUnknown  = "unknown"  // an API request that gave none, or was made before clients existed
	clientEmbedded = "embedded" // an ordinary project: the dashboard of the server itself
	clientOther    = "other"    // the clients beyond the cap of the metrics
)

// Kinds of run: the way it was asked for, not the type of the project.
const (
	kindVideoRun   = "video"
	KindEditRun    = "edit"
	kindProjectRun = "project"
	// kindModelsRun is the run of a 3D model generation.
	kindModelsRun = "models3d"
)

// RunKindOf is the kind of the run of a project, read from its name.
func RunKindOf(project string) string {
	switch {
	case strings.HasPrefix(project, GenPrefix), strings.HasPrefix(project, PiecePrefix):
		return kindVideoRun
	case strings.HasPrefix(project, EditPrefix), strings.HasPrefix(project, VideoPrefix):
		return KindEditRun
	case strings.HasPrefix(project, ModelGenPrefix):
		return kindModelsRun
	}
	return kindProjectRun
}

// clientOf is the client of a run: the stored one, else unknown for a run of the API and embedded for any other.
func clientOf(r Run) string { return effectiveClient(r.Client, RunKindOf(r.Project)) }

// effectiveClient is clientOf for a stored client and a kind of run.
func effectiveClient(stored, kind string) string {
	if stored != "" {
		return stored
	}
	if kind == kindProjectRun {
		return clientEmbedded
	}
	return ClientUnknown
}

// RequestClient is the client a request names: unknown when it names none. A name that is not one (it is empty,
// has capitals, spaces or more than 32 characters) is refused with a 422 and ok is false.
func RequestClient(w http.ResponseWriter, client *string) (name string, ok bool) {
	if client == nil {
		return ClientUnknown, true
	}
	if !config.ValidClient(*client) {
		api.Refuse(w, "client %q is not a client name: use %s, one to 32 lowercase letters, digits or hyphens", *client, config.ClientPatternText)
		return "", false
	}
	return *client, true
}

// ClientShown is the client a stored request reports: unknown for one made before clients existed.
func ClientShown(client *string) string {
	if client == nil || *client == "" {
		return ClientUnknown
	}
	return *client
}

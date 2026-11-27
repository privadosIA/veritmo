package workspace

// clients.go: the client labels in use and the colour of each (spec client-labels). A label is a visual category: it says where something came
// from. The counts come from the tables, grouped by label, with the visibility the content mode gives every listing.

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"veritmo/internal/platform/settings"
	"veritmo/internal/project/config"
	"veritmo/internal/server/api"
)

// ClientCounts is how many items of each kind a label has.
type ClientCounts struct {
	Projects         int `json:"projects"`
	Animations       int `json:"animations"`
	Generations      int `json:"generations"`
	ImageEdits       int `json:"image_edits"`
	VideoEdits       int `json:"video_edits"`
	ModelGenerations int `json:"model_generations"`
}

// ClientLabel is a label in use, with what it has and its colour.
type ClientLabel struct {
	Name     string       `json:"name"`
	Color    string       `json:"color"`
	Counts   ClientCounts `json:"counts"`
	Total    int          `json:"total"`
	LastUsed string       `json:"last_used"`
}

// validClientName says whether a name can be a label: the reserved ones or the pattern of the clients.
func validClientName(name string) bool {
	return name == clientEmbedded || name == ClientUnknown || config.ValidClient(name)
}

// clientLabels lists the labels the items the server shows have, newest use first.
func (h *Handler) clientLabels() []ClientLabel {
	type acc struct {
		counts ClientCounts
		last   time.Time
	}
	labels := map[string]*acc{}
	add := func(client string, kind ManagedKind, animation bool, used time.Time) {
		a := labels[client]
		if a == nil {
			a = &acc{}
			labels[client] = a
		}
		switch {
		case animation:
			a.counts.Animations++
		case kind == KindGeneration:
			a.counts.Generations++
		case kind == KindImageEdit:
			a.counts.ImageEdits++
		case kind == KindVideoEdit:
			a.counts.VideoEdits++
		case kind == KindModelGeneration:
			a.counts.ModelGenerations++
		default:
			a.counts.Projects++
		}
		if used.After(a.last) {
			a.last = used
		}
	}

	// the label a call of the generation API was recorded with, for a project row that has none
	recorded := map[string]string{}
	var recs []ManagedRow
	if err := h.projects.db.DB.Select("id", "client").Find(&recs).Error; err == nil {
		for _, r := range recs {
			recorded[r.ID] = r.Client
		}
	}
	var rows []ProjectRow
	if err := h.projects.db.DB.Select("id", "client", "created_at", "updated_at").Find(&rows).Error; err == nil {
		for _, r := range rows {
			if IsAnimationProject(r.ID) || !h.host.ProjectVisible(r.ID) {
				continue // an animation is counted from its own table
			}
			kind := managedKindOfID(r.ID)
			client := r.Client
			if client == "" {
				client = recorded[r.ID]
			}
			if client == "" {
				client = clientEmbedded
				if kind != "" {
					client = ClientUnknown
				}
			}
			add(client, kind, false, latest(r.UpdatedAt, r.CreatedAt))
		}
	}
	var pieces []struct {
		ID        string
		Client    string
		CreatedAt time.Time
		UpdatedAt time.Time
	}
	if err := h.projects.db.DB.Table("mm_pieces").Select("id", "client", "created_at", "updated_at").Find(&pieces).Error; err == nil {
		for _, p := range pieces {
			if !h.host.ProjectVisible(p.ID) {
				continue
			}
			client := p.Client
			if client == "" {
				client = ClientUnknown
			}
			add(client, "", true, latest(p.UpdatedAt, p.CreatedAt))
		}
	}

	out := make([]ClientLabel, 0, len(labels))
	for name, a := range labels {
		c := a.counts
		l := ClientLabel{Name: name, Color: settings.ClientColour(name), Counts: c, Total: c.Projects + c.Animations + c.Generations + c.ImageEdits + c.VideoEdits + c.ModelGenerations}
		if !a.last.IsZero() {
			l.LastUsed = a.last.UTC().Format(time.RFC3339)
		}
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastUsed != out[j].LastUsed {
			return out[i].LastUsed > out[j].LastUsed
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func latest(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// GET /api/clients
func (h *Handler) handleClientsList(w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, http.StatusOK, h.clientLabels())
}

// PUT /api/clients/{name} {color}: the colour of a label, or null to go back to the default one. A label with no items may have one.
func (h *Handler) handleClientColorPut(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if !validClientName(name) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "the client name must be embedded, unknown or "+config.ClientPatternText)
		return
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "invalid JSON body: "+err.Error())
		return
	}
	raw, sent := body["color"]
	if !sent {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "send color: #rrggbb, or null for the default one")
		return
	}
	var color *string
	if err := json.Unmarshal(raw, &color); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "color must be text written #rrggbb, or null")
		return
	}
	colour := ""
	if color != nil {
		colour = strings.TrimSpace(*color)
		if !settings.ValidClientColour(colour) {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "color must be written #rrggbb, or null for the default one")
			return
		}
	}
	if err := settings.SetClientColour(name, colour); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]string{"name": name, "color": settings.ClientColour(name)})
}

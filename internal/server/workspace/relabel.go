package workspace

// relabel.go: change the client label of a generation, an edit or a model generation (spec client-labels, "A generation, an edit or a model
// generation can be relabelled"). The label is a visual category: nothing but where the item says it came from changes.

import (
	"encoding/json"
	"fmt"
	"net/http"

	"gopkg.in/yaml.v3"

	"veritmo/internal/project/config"
	"veritmo/internal/server/api"
)

// DecodeRelabel reads the body of a relabel, `{client}` and nothing else: a name, or null or an empty text to remove the label. It writes the
// 400 itself when the body is not that.
func DecodeRelabel(w http.ResponseWriter, r *http.Request) (*string, bool) {
	var raw map[string]json.RawMessage
	if !api.DecodeJSONLimited(w, r, &raw, 4<<10) {
		return nil, false
	}
	for k := range raw {
		if k != "client" {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("the field %q cannot be changed this way: only client", k))
			return nil, false
		}
	}
	v, sent := raw["client"]
	if !sent {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "nothing to change: send client (a name, or null to remove it)")
		return nil, false
	}
	var client *string
	if json.Unmarshal(v, &client) != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "client must be a text or null")
		return nil, false
	}
	if client != nil && *client != "" && !config.ValidClient(*client) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("client %q is not a client name: use %s", *client, config.ClientPatternText))
		return nil, false
	}
	return client, true
}

// RelabelManaged writes the label of a managed item wherever it keeps one: the `client` of its record (the request it was made with, whose copy is
// the column of the records table) and the `client:` of its definition (which the project row and the listings read). A nil or empty client removes
// it, and the label the server gives by default (`unknown`) applies again. It is refused with 409 while the item has an active or queued run. The
// runs already made keep the label they had: they are a record of who asked.
func (h *Handler) RelabelManaged(id string, client *string) *LaunchError {
	info, ok := h.ReadManaged(id)
	if !ok {
		return &LaunchError{Status: http.StatusNotFound, Code: api.CodeNotFound, Msg: fmt.Sprintf("%q not found", id)}
	}
	if runID, busy := h.ProjectBusy(id); busy {
		return &LaunchError{Status: http.StatusConflict, Code: api.CodeConflict, Msg: fmt.Sprintf("%q has an active or queued run (%s): change its label when it ends", id, runID)}
	}
	label := ""
	if client != nil {
		label = *client
	}

	if lerr := h.SetProjectClient(id, client); lerr != nil {
		return lerr
	}

	// the record: the client of the request it was made with
	req := map[string]json.RawMessage{}
	if len(info.Request) > 0 {
		_ = json.Unmarshal(info.Request, &req)
	}
	if label == "" {
		delete(req, "client")
	} else {
		raw, _ := json.Marshal(label)
		req["client"] = raw
	}
	request, err := json.Marshal(req)
	if err != nil {
		return internalError(err)
	}
	info.Request = request
	if err := h.PutManaged(info); err != nil {
		return internalError(err)
	}
	return nil
}

// SetProjectClient writes the `client:` of the definition of a project (and so its row, which the listings read): a nil or empty client removes it.
// It is refused with 409 while the project has an active or queued run. It serves the items that keep a label of their own besides the project's,
// such as an animation, whose label changes in its own row first.
func (h *Handler) SetProjectClient(id string, client *string) *LaunchError {
	if runID, busy := h.ProjectBusy(id); busy {
		return &LaunchError{Status: http.StatusConflict, Code: api.CodeConflict, Msg: fmt.Sprintf("%q has an active or queued run (%s): change its label when it ends", id, runID)}
	}
	label := ""
	if client != nil {
		label = *client
	}
	definition, err := h.ProjectDefinition(id)
	if err != nil {
		return internalError(err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(definition, &doc); err != nil || doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return &LaunchError{Status: http.StatusConflict, Code: api.CodeConflict, Msg: "the definition of the project is not valid, so its label cannot be changed"}
	}
	if label == "" {
		removeNode(doc.Content[0], "client")
	} else {
		setNode(doc.Content[0], "client", label, "!!str")
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return internalError(err)
	}
	if _, err := config.LoadBytes(out); err != nil {
		return &LaunchError{Status: http.StatusUnprocessableEntity, Code: api.CodeValidation, Msg: err.Error()}
	}

	if err := h.SaveProjectDefinition(id, out, "patch"); err != nil {
		return internalError(err)
	}
	return nil
}

func internalError(err error) *LaunchError {
	return &LaunchError{Status: http.StatusInternalServerError, Code: api.CodeInternal, Msg: err.Error()}
}

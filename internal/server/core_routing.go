package server

// core_routing.go: which provider runs each task, the consent to send data to a remote one,
// and the cap on calls to it. Providers never switch by themselves: a call can name
// another provider, and only the user's action does. See specs/provider-routing.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"veritmo/internal/assistant/llm"
	"veritmo/internal/platform/edition"
	"veritmo/internal/platform/providers"
	"veritmo/internal/platform/settings"
	"veritmo/internal/server/api"
	"veritmo/internal/server/assistant"
)

type routedTask struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
	// Remote is true when the provider is not this machine.
	Remote bool `json:"remote"`
	// Consented says whether sending data to it was acknowledged (always true for a local one).
	Consented bool   `json:"consented"`
	Model     string `json:"model"`
}

// suggestion names the external provider that fits a creative task; it changes nothing by itself.
type suggestion struct {
	ProviderID string `json:"provider_id"`
	Name       string `json:"name"`
	Reason     string `json:"reason"`
}

type routingAnswer struct {
	Routing  map[string]routedTask `json:"routing"`
	MaxCalls int                   `json:"max_calls"`
	// Suggested holds, for the creative tasks, the external provider that fits them (absent when there is none).
	Suggested map[string]suggestion `json:"suggested,omitempty"`
}

// suggestedReasons are the fixed sentences of the suggestion, per task.
var suggestedReasons = map[string]string{
	settings.TaskMotionGraphics: "the local model reaches its ceiling on the metaphor; an external one scores higher on the same rubric",
	settings.TaskQuality:        "the vision reading of frames is more reliable on an external model",
}

// suggestExternal picks, for the creative tasks, the first saved provider created from a preset, tested with success,
// not blocked in this edition and with the model the task needs, in the presets' order of preference.
func suggestExternal() map[string]suggestion {
	out := map[string]suggestion{}
	for _, task := range []string{settings.TaskMotionGraphics, settings.TaskQuality} {
		best, bestRank := settings.Provider{}, -1
		for _, p := range settings.Providers() {
			rank := providers.Rank(p.Preset)
			if p.Type != settings.TypeOpenAI || rank < 0 || !p.TestOK || !edition.ProviderHostAllowed(p.BaseURL) || settings.ModelFor(p, task) == "" {
				continue
			}
			if bestRank < 0 || rank < bestRank {
				best, bestRank = p, rank
			}
		}
		if bestRank >= 0 {
			out[task] = suggestion{ProviderID: best.ID, Name: best.Name, Reason: suggestedReasons[task]}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func routingView() routingAnswer {
	out := routingAnswer{Routing: map[string]routedTask{}, MaxCalls: settings.MaxCalls(), Suggested: suggestExternal()}
	for _, task := range settings.Tasks {
		id := settings.RoutedProvider(task)
		if id == settings.BuiltinID {
			b := assistant.BuiltinView()
			rt := routedTask{Provider: id, Name: b.Name, Remote: b.Remote, Consented: true}
			if task == settings.TaskMotionGraphics {
				rt.Model = settings.MotionModel()
			}
			out.Routing[task] = rt
			continue
		}
		p, ok := settings.FindProvider(id)
		if !ok { // a hand-edited file: say so rather than hide it
			out.Routing[task] = routedTask{Provider: id, Name: id}
			continue
		}
		remote := settings.Remote(p.BaseURL)
		model := settings.ModelFor(p, task)
		if m := settings.MotionModel(); task == settings.TaskMotionGraphics && m != "" {
			model = m
		}
		out.Routing[task] = routedTask{
			Provider: id, Name: p.Name, Remote: remote,
			Consented: !remote || settings.Acknowledged(id, p.BaseURL), Model: model,
		}
	}
	return out
}

// GET /api/routing -> {"routing": {task: {provider, name, remote, consented, model}}, "max_calls": N}
func (s *Server) handleRoutingGet(w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, http.StatusOK, routingView())
}

// PUT /api/routing {"routing": {task: provider id}, "acknowledge_remote"?: bool, "max_calls"?: N}
//
// Routing a task to a remote provider needs the user's acknowledgement, given here with
// acknowledge_remote or before with POST /api/providers/{id}/acknowledge; without it the
// answer is 400 consent_required. Routing to "ollama" puts the task back on the built-in.
func (s *Server) handleRoutingPut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Routing           map[string]string `json:"routing"`
		AcknowledgeRemote bool              `json:"acknowledge_remote"`
		MaxCalls          *int              `json:"max_calls"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if req.Routing == nil && req.MaxCalls == nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "give routing (a provider per task) or max_calls")
		return
	}
	if req.MaxCalls != nil && *req.MaxCalls < 1 {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "max_calls must be at least 1")
		return
	}
	// Validate everything before saving anything.
	var toAcknowledge []string
	for task, id := range req.Routing {
		if !settings.ValidTask(task) {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("%q is not a task: use %v", task, settings.Tasks))
			return
		}
		if id == "" || id == settings.BuiltinID {
			continue
		}
		p, ok := settings.FindProvider(id)
		if !ok {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("there is no provider %q", id))
			return
		}
		if p.Type == settings.TypeOpenAI && settings.ModelFor(p, task) == "" {
			api.WriteRoutingError(w, &llm.NoModelError{ProviderID: p.ID, Name: p.Name, Task: task})
			return
		}
		if settings.Remote(p.BaseURL) && !settings.Acknowledged(id, p.BaseURL) {
			if !req.AcknowledgeRemote {
				api.WriteRoutingError(w, &llm.ConsentError{ProviderID: p.ID, Name: p.Name, Task: task, Address: p.BaseURL})
				return
			}
			toAcknowledge = append(toAcknowledge, id)
		}
	}
	for _, id := range toAcknowledge {
		if err := settings.Acknowledge(id); err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
			return
		}
	}
	if req.Routing != nil {
		if err := settings.SetRouting(req.Routing); err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
			return
		}
	}
	if req.MaxCalls != nil {
		if err := settings.SetMaxCalls(*req.MaxCalls); err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
			return
		}
	}
	api.WriteJSON(w, http.StatusOK, routingView())
}

// POST /api/providers/{id}/acknowledge: records that the user accepted sending data to the
// provider at its current address. It stops counting if the address changes.
func (s *Server) handleProviderAcknowledge(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := settings.Acknowledge(id); err != nil {
		if errors.Is(err, settings.ErrNoSuchProvider) || id == settings.BuiltinID {
			api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("there is no provider %q to acknowledge", id))
			return
		}
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	p, _ := settings.FindProvider(id)
	api.WriteJSON(w, http.StatusOK, assistant.ProviderViewOf(p))
}

package workspace

// availability.go: which model tasks can run right now. For each routed task the
// provider is resolved (internal/assistant/llm) and checked: an Ollama answers with models, an
// OpenAI-compatible provider has the model declared, the consent given and a key it
// accepts. Nothing of the user's is sent and no model is loaded. The answer is kept 30
// seconds and dropped whenever settings, providers, keys or routing change. See
// specs/model-availability.

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/assistant/llm"
	"veritmo/internal/platform/settings"
	"veritmo/internal/server/api"
)

const (
	// AvailabilityTTL is how long the availability of a task is kept before it is asked again.
	AvailabilityTTL     = 30 * time.Second
	availabilityTimeout = 3 * time.Second
)

// Why a task cannot run.
const (
	reasonUnreachable   = "unreachable"
	reasonNoModels      = "no_models"
	reasonNoVisionModel = "no_vision_model"
	reasonNoModel       = "no_model"
	reasonNoConsent     = "no_consent"
	reasonKeyRejected   = "key_rejected"
)

type availProvider struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// TaskAvailability says whether a task of the assistant can run now and which provider it would use.
type TaskAvailability struct {
	Available bool          `json:"available"`
	Provider  availProvider `json:"provider"`
	Reason    string        `json:"reason,omitempty"`
}

type availabilityAnswer struct {
	Tasks map[string]TaskAvailability `json:"tasks"`
}

// availabilityCache keeps the last answer for a short time.
type availabilityCache struct {
	mu      sync.Mutex
	at      time.Time
	version uint64
	body    *availabilityAnswer
	// ttl overrides AvailabilityTTL when set (tests).
	ttl time.Duration
}

func (c *availabilityCache) bump() {
	c.mu.Lock()
	c.version++
	c.body = nil
	c.mu.Unlock()
}

// DropAvailability forgets the cached availability: every call that can change the settings, a provider, a key or the
// routing does it afterwards.
func (h *Handler) DropAvailability() { h.avail.bump() }

// GET /api/models/availability -> {"tasks": {task: {available, provider: {id, name, type}, reason?}}}
func (h *Handler) handleModelAvailability(w http.ResponseWriter, r *http.Request) {
	ttl := h.avail.ttl
	if ttl == 0 {
		ttl = AvailabilityTTL
	}
	h.avail.mu.Lock()
	if h.avail.body != nil && time.Since(h.avail.at) < ttl {
		body := h.avail.body
		h.avail.mu.Unlock()
		api.WriteJSON(w, http.StatusOK, body)
		return
	}
	version := h.avail.version
	h.avail.mu.Unlock()

	body := computeAvailability(r.Context())

	h.avail.mu.Lock()
	if h.avail.version == version { // nothing changed while it was computed
		h.avail.body, h.avail.at = body, time.Now()
	}
	h.avail.mu.Unlock()
	api.WriteJSON(w, http.StatusOK, body)
}

// modelProbe is what one provider answered when asked for its models, shared by the tasks that use it.
type modelProbe struct {
	models []llm.Model
	err    error
}

func computeAvailability(ctx context.Context) *availabilityAnswer {
	out := &availabilityAnswer{Tasks: map[string]TaskAvailability{}}
	type resolved struct {
		task string
		res  llm.Resolved
		ta   TaskAvailability
		ok   bool // resolved without error and still to be probed
	}
	items := make([]*resolved, 0, len(settings.Tasks))
	for _, task := range settings.Tasks {
		it := &resolved{task: task}
		res, err := llm.For(task, "")
		it.res = res
		if err == nil {
			it.ok = true
			it.ta.Provider = availProvider{ID: res.ID, Name: res.Name, Type: res.Type}
		} else {
			it.ta.Provider, it.ta.Reason = providerOfError(task, err)
		}
		items = append(items, it)
	}
	// One probe per distinct provider, in parallel.
	probes := map[string]*modelProbe{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, it := range items {
		if !it.ok {
			continue
		}
		key := it.res.ID
		mu.Lock()
		_, seen := probes[key]
		if !seen {
			probes[key] = &modelProbe{}
		}
		p := probes[key]
		mu.Unlock()
		if seen {
			continue
		}
		wg.Add(1)
		go func(res llm.Resolved, p *modelProbe) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, availabilityTimeout)
			defer cancel()
			p.models, p.err = res.Provider.Models(cctx)
		}(it.res, p)
	}
	wg.Wait()
	for _, it := range items {
		if it.ok {
			it.ta = judge(it.task, it.res, probes[it.res.ID])
		}
		out.Tasks[it.task] = it.ta
	}
	return out
}

// judge says whether a task can run on a provider, from what the provider answered.
func judge(task string, res llm.Resolved, p *modelProbe) TaskAvailability {
	ta := TaskAvailability{Provider: availProvider{ID: res.ID, Name: res.Name, Type: res.Type}}
	if p.err != nil {
		if errors.Is(p.err, llm.ErrKeyRejected) {
			ta.Reason = reasonKeyRejected
		} else {
			ta.Reason = reasonUnreachable
		}
		return ta
	}
	if res.Type == settings.TypeOpenAI {
		ta.Available = true // the model is declared (llm.For checked it) and the provider answered
		return ta
	}
	// An Ollama: the models it has.
	if !settings.NeedsVision(task) {
		for _, m := range p.models {
			if assist.IsChatModel(m.Name) {
				ta.Available = true
				return ta
			}
		}
		ta.Reason = reasonNoModels
		return ta
	}
	for _, m := range p.models {
		if m.Vision != nil && *m.Vision {
			ta.Available = true
			return ta
		}
	}
	ta.Reason = reasonNoVisionModel
	return ta
}

// providerOfError names the provider and the reason when a task could not even be resolved.
func providerOfError(task string, err error) (availProvider, string) {
	var ce *llm.ConsentError
	var nm *llm.NoModelError
	switch {
	case errors.As(err, &ce):
		return availProvider{ID: ce.ProviderID, Name: ce.Name, Type: providerType(ce.ProviderID)}, reasonNoConsent
	case errors.As(err, &nm):
		return availProvider{ID: nm.ProviderID, Name: nm.Name, Type: providerType(nm.ProviderID)}, reasonNoModel
	}
	id := settings.RoutedProvider(task)
	return availProvider{ID: id, Name: id, Type: providerType(id)}, reasonUnreachable
}

func providerType(id string) string {
	if p, ok := settings.FindProvider(id); ok {
		return p.Type
	}
	return settings.TypeOllama
}

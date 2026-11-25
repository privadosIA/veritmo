package assistant

// assistant/providers.go: the model providers the user registered (the built-in Ollama plus
// OpenAI-compatible servers and extra Ollamas) and their API keys. A key is only
// ever written to keys.json by internal/platform/settings; nothing here reads one back out
// to a client: providers are returned through ProviderView, which has no key field.
// See specs/model-providers.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"veritmo/internal/assistant/llm"
	"veritmo/internal/platform/edition"
	"veritmo/internal/platform/providers"
	"veritmo/internal/platform/settings"
	"veritmo/internal/server/api"
)

// ProviderView is what the API says about a provider: never its key, only whether one is set.
type ProviderView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	BaseURL     string `json:"base_url"`
	TextModel   string `json:"text_model"`
	VisionModel string `json:"vision_model"`
	// Builtin marks the Ollama of the Ollama setting: it always exists and is not editable here.
	Builtin bool `json:"builtin"`
	// Remote is true when the address is not this machine.
	Remote bool `json:"remote"`
	KeySet bool `json:"key_set"`
	// Blocked is true when this edition does not use the provider's address (it was saved by
	// another edition); no task is run on it.
	Blocked bool `json:"blocked,omitempty"`
	// Preset is the preset the provider was created from, if any.
	Preset string `json:"preset,omitempty"`
	// ListsModels says whether the provider's model list is asked for when testing it.
	ListsModels bool `json:"lists_models"`
	// TestedAt and TestOK are the last test of the provider (absent if never tested or changed since).
	TestedAt string `json:"tested_at,omitempty"`
	TestOK   bool   `json:"test_ok,omitempty"`
}

// listsModels says whether to ask a provider for its models: always, unless its preset says it cannot.
func listsModels(p settings.Provider) bool {
	if pr, ok := providers.Find(p.Preset); ok {
		return pr.ListsModels
	}
	return true
}

// ProviderViewOf is the view of a saved provider: what the page shows, never its key (only whether one is set).
func ProviderViewOf(p settings.Provider) ProviderView {
	return ProviderView{
		ID: p.ID, Name: p.Name, Type: p.Type, BaseURL: p.BaseURL, TextModel: p.TextModel, VisionModel: p.VisionModel,
		Remote: settings.Remote(p.BaseURL), KeySet: settings.HasKey(p.ID), Blocked: !edition.ProviderHostAllowed(p.BaseURL),
		Preset: p.Preset, ListsModels: listsModels(p), TestedAt: p.TestedAt, TestOK: p.TestOK,
	}
}

// BuiltinView is the view of the built-in Ollama, which is always there and cannot be removed.
func BuiltinView() ProviderView {
	r := settings.Ollama()
	return ProviderView{ID: settings.BuiltinID, Name: "Ollama", Type: settings.TypeOllama, BaseURL: r.Host, Builtin: true, Remote: r.Remote, ListsModels: true}
}

func allProviders() []ProviderView {
	out := []ProviderView{BuiltinView()}
	for _, p := range settings.Providers() {
		out = append(out, ProviderViewOf(p))
	}
	return out
}

// providerRequest is the body of POST and PUT /api/providers. Key: absent leaves it
// unchanged (on PUT), "" removes it, anything else sets it.
type providerRequest struct {
	ID          string  `json:"id"`
	Preset      string  `json:"preset"`
	Name        string  `json:"name"`
	Type        string  `json:"type"`
	BaseURL     string  `json:"base_url"`
	TextModel   string  `json:"text_model"`
	VisionModel string  `json:"vision_model"`
	Key         *string `json:"key"`
}

func decodeProviderRequest(w http.ResponseWriter, r *http.Request) (providerRequest, bool) {
	var req providerRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return req, false
	}
	return req, true
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// freeProviderID derives an id from a name that no provider has.
func freeProviderID(name string) string {
	base := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(base) > 36 {
		base = strings.Trim(base[:36], "-")
	}
	if base == "" || base == settings.BuiltinID {
		base = "provider"
	}
	taken := map[string]bool{}
	for _, p := range settings.Providers() {
		taken[p.ID] = true
	}
	id := base
	for i := 2; taken[id]; i++ {
		id = fmt.Sprintf("%s-%d", base, i)
	}
	return id
}

// GET /api/providers -> {"providers": [...]}: the built-in Ollama first, then the saved ones.
func (h *Handler) handleProvidersList(w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, http.StatusOK, map[string]any{"providers": allProviders()})
}

// GET /api/providers/{id}: one provider as the list shows it (the built-in Ollama included), with has_key and never the key; 404 for an unknown id.
func (h *Handler) handleProviderGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	for _, p := range allProviders() {
		if p.ID == id {
			api.WriteJSON(w, http.StatusOK, p)
			return
		}
	}
	api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("there is no provider %q", id))
}

// fillFromPreset fills what the request left empty with what the preset gives.
func fillFromPreset(req *providerRequest, pr providers.Preset) {
	fill := func(dst *string, v string) {
		if strings.TrimSpace(*dst) == "" {
			*dst = v
		}
	}
	first := func(l []string) string {
		if len(l) == 0 {
			return ""
		}
		return l[0]
	}
	fill(&req.Type, settings.TypeOpenAI)
	fill(&req.BaseURL, pr.BaseURL)
	fill(&req.Name, pr.Name)
	fill(&req.TextModel, first(pr.TextModels))
	fill(&req.VisionModel, first(pr.VisionModels))
}

// GET /api/providers/presets -> {"presets": [...]}: the providers the program knows, in order of preference.
func (h *Handler) handleProviderPresets(w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, http.StatusOK, map[string]any{"presets": providers.Presets(), "models_checked": providers.ModelsDate})
}

// POST /api/providers {id?, preset?, name, type, base_url, text_model?, vision_model?, key?}
func (h *Handler) handleProviderCreate(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeProviderRequest(w, r)
	if !ok {
		return
	}
	if req.Preset != "" {
		pr, found := providers.Find(req.Preset)
		if !found {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("there is no preset %q: use one of %s", req.Preset, strings.Join(providers.IDs(), ", ")))
			return
		}
		fillFromPreset(&req, pr)
	}
	if strings.TrimSpace(req.ID) == "" {
		req.ID = freeProviderID(req.Name)
	}
	p := settings.Provider{ID: req.ID, Name: req.Name, Type: req.Type, BaseURL: req.BaseURL, TextModel: req.TextModel, VisionModel: req.VisionModel, Preset: req.Preset}
	norm, err := settings.ValidateProvider(p)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	key := ""
	if req.Key != nil {
		key = *req.Key
	}
	if key != "" {
		if err := settings.ValidateKey(key); err != nil {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
			return
		}
	}
	if err := settings.CheckKeyTransport(norm.BaseURL, key != ""); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	saved, err := settings.AddProvider(norm)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	if key != "" {
		if err := settings.SetKey(saved.ID, key); err != nil {
			_ = settings.DeleteProvider(saved.ID)
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, "cannot save the key")
			return
		}
	}
	api.WriteJSON(w, http.StatusCreated, ProviderViewOf(saved))
}

// PUT /api/providers/{id} {name, type, base_url, text_model?, vision_model?, key?}
func (h *Handler) handleProviderUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == settings.BuiltinID {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "the built-in provider cannot be edited here: change its address in the Ollama settings")
		return
	}
	if _, found := settings.FindProvider(id); !found {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("there is no provider %q", id))
		return
	}
	req, ok := decodeProviderRequest(w, r)
	if !ok {
		return
	}
	p := settings.Provider{ID: id, Name: req.Name, Type: req.Type, BaseURL: req.BaseURL, TextModel: req.TextModel, VisionModel: req.VisionModel}
	norm, err := settings.ValidateProvider(p)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	hasKey := settings.HasKey(id)
	newKey := ""
	if req.Key != nil {
		if *req.Key == "" {
			hasKey = false
		} else {
			if err := settings.ValidateKey(*req.Key); err != nil {
				api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
				return
			}
			newKey, hasKey = *req.Key, true
		}
	}
	if err := settings.CheckKeyTransport(norm.BaseURL, hasKey); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	saved, err := settings.UpdateProvider(id, norm)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	if newKey != "" || (req.Key != nil && *req.Key == "") {
		_ = settings.ClearTest(id)
		saved.TestedAt, saved.TestOK = "", false
	}
	switch {
	case newKey != "":
		if err := settings.SetKey(id, newKey); err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, "cannot save the key")
			return
		}
	case req.Key != nil && *req.Key == "":
		if err := settings.DeleteKey(id); err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, "cannot remove the key")
			return
		}
	}
	api.WriteJSON(w, http.StatusOK, ProviderViewOf(saved))
}

// DELETE /api/providers/{id}: removes the provider and its key.
func (h *Handler) handleProviderDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == settings.BuiltinID {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "the built-in provider cannot be removed: change its address in the Ollama settings")
		return
	}
	if err := settings.DeleteProvider(id); err != nil {
		var inUse settings.ErrProviderInUse
		if errors.As(err, &inUse) {
			api.WriteError(w, http.StatusConflict, api.CodeConflict, err.Error())
			return
		}
		if errors.Is(err, settings.ErrNoSuchProvider) {
			api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("there is no provider %q", id))
			return
		}
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type providerTestModel struct {
	Name   string `json:"name"`
	Vision *bool  `json:"vision,omitempty"`
}

// ProviderTestResult is the answer of the test of a provider: whether it answers and the models it offers.
type ProviderTestResult struct {
	OK     bool                `json:"ok"`
	Host   string              `json:"host"`
	Remote bool                `json:"remote"`
	Models []providerTestModel `json:"models,omitempty"`
	// ChatMS is how long the one short chat took, in milliseconds (0 when no chat was sent).
	ChatMS int64 `json:"chat_ms,omitempty"`
	// ModelsNote says why there are no models when the provider does not list them or its list failed.
	ModelsNote string `json:"models_note,omitempty"`
	Error      string `json:"error,omitempty"`
}

// POST /api/providers/{id}/test -> {ok, host, remote, models?: [{name}], error?}
//
// Lists the provider's models within 5 seconds, without following redirects, reading
// at most 4 MiB and returning at most 200 names. Nothing of the user's is sent; the
// key goes only to the provider itself, as a Bearer token. A provider that cannot be
// reached or rejects the key is a normal answer (ok false), not an error status.
func (h *Handler) handleProviderTest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var p settings.Provider
	key := ""
	if id == settings.BuiltinID {
		b := BuiltinView()
		p = settings.Provider{ID: id, Type: settings.TypeOllama, BaseURL: b.BaseURL}
	} else {
		saved, found := settings.FindProvider(id)
		if !found {
			api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("there is no provider %q", id))
			return
		}
		p, key = saved, settings.Key(id)
	}
	if err := settings.CheckKeyTransport(p.BaseURL, key != ""); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	res := testProvider(r.Context(), p, key)
	if id != settings.BuiltinID {
		_ = settings.RecordTest(id, res.OK, time.Now())
	}
	api.WriteJSON(w, http.StatusOK, res)
}

const (
	providerTestMaxModels = 200
	// providerChatTimeout is how long the one short chat of a test waits: a first token on a large model takes seconds.
	providerChatTimeout = 10 * time.Second
	providerTestPrompt  = "Reply with OK"
	// providerTestMaxTokens leaves room for a model that thinks before it answers (Gemini 2.5 spends the whole budget thinking at 5).
	providerTestMaxTokens = 512
)

func testProvider(parent context.Context, p settings.Provider, key string) ProviderTestResult {
	res := ProviderTestResult{Host: p.BaseURL, Remote: settings.Remote(p.BaseURL)}
	if p.Type == settings.TypeOllama {
		t := api.ProbeOllama(parent, p.BaseURL)
		res.OK, res.Error = t.OK, t.Error
		for _, m := range t.Models {
			v := m.Vision
			res.Models = append(res.Models, providerTestModel{Name: m.Name, Vision: &v})
		}
		return res
	}
	client := llm.NewOpenAI(p.BaseURL, key)
	// One short chat proves the key and the model; the model list proves nothing (some providers
	// do not serve it, others serve it without a key). Nothing of the user's is sent.
	model := p.TextModel
	if model == "" {
		model = p.VisionModel
	}
	chatFailed := false
	if model != "" {
		ctx, cancel := context.WithTimeout(parent, providerChatTimeout)
		start := time.Now()
		out, err := client.Chat(ctx, llm.Request{Model: model, User: providerTestPrompt, MaxTokens: providerTestMaxTokens})
		cancel()
		res.ChatMS = time.Since(start).Milliseconds()
		switch {
		case err != nil:
			res.Error, chatFailed = providerFailure(ctx, p.BaseURL, key, err, providerChatTimeout), true
		case strings.TrimSpace(out.Text) == "":
			res.Error, chatFailed = fmt.Sprintf("%s answered the chat with no content: check the model name %q", p.BaseURL, model), true
		}
	}
	if listsModels(p) {
		ctx, cancel := context.WithTimeout(parent, api.OllamaTestTimeout)
		models, err := client.Models(ctx)
		cancel()
		switch {
		case err != nil:
			if model == "" {
				res.Error = providerFailure(ctx, p.BaseURL, key, err, api.OllamaTestTimeout)
			} else {
				res.ModelsNote = "the model list could not be read; the models are typed by hand"
			}
		default:
			if len(models) > providerTestMaxModels {
				models = models[:providerTestMaxModels]
			}
			for _, m := range models {
				res.Models = append(res.Models, providerTestModel{Name: strings.TrimPrefix(m.Name, "models/")})
			}
		}
	} else {
		res.ModelsNote = "this provider does not list models; type the model names by hand"
	}
	switch {
	case model != "":
		res.OK = !chatFailed
	case res.Error == "" && len(res.Models) > 0:
		// No model to chat with: the list is all there is to go by, as it was before presets.
		res.OK = true
		res.ModelsNote = "No text model is set, so no chat was sent: set one to test the key with a chat."
	case res.Error == "":
		res.Error = "set a text model to test this provider: it does not list models and no chat can be sent without one"
	}
	return res
}

// providerFailure explains a failed test without echoing anything the other side
// sent, and with the key redacted in any case.
func providerFailure(ctx context.Context, host, key string, err error, timeout time.Duration) string {
	var msg string
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		msg = fmt.Sprintf("no answer from %s (timed out after %d s)", host, int(timeout.Seconds()))
	case errors.Is(err, llm.ErrKeyRejected):
		msg = fmt.Sprintf("the key was rejected by %s", host)
	case errors.Is(err, llm.ErrRateLimited):
		msg = fmt.Sprintf("%s asked to slow down: try again in a moment", host)
	case errors.Is(err, llm.ErrUnreachable):
		msg = fmt.Sprintf("no provider answers at %s (%s)", host, api.FailureKind(ctx, err))
	default:
		msg = fmt.Sprintf("%s answered with an error: %s", host, err.Error())
	}
	if key != "" {
		msg = strings.ReplaceAll(msg, key, "***")
	}
	return msg
}

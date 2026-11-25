package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/assistant/describe"
	"veritmo/internal/assistant/llm"
	"veritmo/internal/platform/edition"
	"veritmo/internal/platform/settings"
)

// WriteRoutingError answers the errors of resolving a provider (no consent, no model,
// unknown provider, too many calls). It reports whether it wrote one.
func WriteRoutingError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, llm.ErrConsentRequired):
		WriteError(w, http.StatusBadRequest, CodeConsentRequired, err.Error())
	case errors.Is(err, edition.ErrModelNotAllowed):
		WriteError(w, http.StatusBadRequest, CodeModelNotAllowed, err.Error())
	case errors.Is(err, llm.ErrProviderBlocked):
		WriteError(w, http.StatusServiceUnavailable, CodeUnavailable, err.Error())
	case errors.Is(err, llm.ErrNoModel):
		WriteError(w, http.StatusBadRequest, CodeNoModel, err.Error())
	case errors.Is(err, llm.ErrUnknownProvider):
		WriteError(w, http.StatusBadRequest, CodeInvalid, err.Error())
	case errors.Is(err, describe.ErrTooManyCalls):
		WriteError(w, http.StatusBadRequest, CodeTooManyCalls, err.Error())
	case errors.Is(err, llm.ErrNoVision):
		WriteError(w, http.StatusUnprocessableEntity, CodeInvalid, err.Error())
	case errors.Is(err, llm.ErrKeyRejected), errors.Is(err, llm.ErrRateLimited), errors.Is(err, llm.ErrModelNotFound):
		WriteError(w, http.StatusBadGateway, CodeInternal, err.Error())
	default:
		return false
	}
	return true
}

// NeedsLocalMemory says whether the task, on the provider it resolves to, loads a model
// into this machine's memory (an Ollama on this machine): only then must it wait for a
// run in progress. A remote provider, or an Ollama on another machine, needs none of it.
func NeedsLocalMemory(ctx context.Context, task string) bool {
	res, err := llm.ForContext(ctx, task)
	if err != nil {
		return true // the call will fail on its own terms; keep the safe behaviour
	}
	return res.Type == settings.TypeOllama && !res.Remote
}

// WriteAssistError answers each failure of the assistant with its own status.
func WriteAssistError(w http.ResponseWriter, err error) {
	if WriteRoutingError(w, err) {
		return
	}
	switch {
	case errors.Is(err, assist.ErrInvalidInput):
		WriteError(w, http.StatusBadRequest, CodeInvalid, err.Error())
	case errors.Is(err, assist.ErrRefused):
		WriteError(w, http.StatusUnprocessableEntity, CodeAssistRefused, err.Error())
	case errors.Is(err, assist.ErrUnreachable), errors.Is(err, assist.ErrNotInstalled), errors.Is(err, assist.ErrNoMemory):
		WriteError(w, http.StatusServiceUnavailable, CodeUnavailable, err.Error())
	default:
		WriteError(w, http.StatusBadGateway, CodeInternal, err.Error())
	}
}

// CatchAll is the handler of /api/ on the mux: unknown API paths are a JSON 404 and, because a method-less pattern
// beats the mux's built-in 405 for wrong-method requests, it probes the other methods itself to answer 405 with an
// Allow header when the path exists under a different method.
func CatchAll(mux *http.ServeMux) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var allowed []string
		for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
			if m == r.Method {
				continue
			}
			probe := r.Clone(r.Context())
			probe.Method = m
			if _, pattern := mux.Handler(probe); pattern != "" && pattern != "/api/" && pattern != "/" {
				allowed = append(allowed, m)
			}
		}
		if len(allowed) > 0 {
			w.Header().Set("Allow", strings.Join(allowed, ", "))
			WriteError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, fmt.Sprintf("method %s not allowed for %s", r.Method, r.URL.Path))
			return
		}
		WriteError(w, http.StatusNotFound, CodeNotFound, fmt.Sprintf("no such endpoint: %s %s", r.Method, r.URL.Path))
	}
}

package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"veritmo/internal/server/api"
)

// jsonErrorWriter rewrites plain-text 404/405 responses produced below the
// handlers (by http.ServeMux's own routing) into the structured JSON error
// shape. Handler-written JSON errors pass through untouched because they
// set an application/json content type before writing the header.
type jsonErrorWriter struct {
	http.ResponseWriter
	r        *http.Request
	wrote    bool
	suppress bool
}

func (jw *jsonErrorWriter) WriteHeader(status int) {
	if jw.wrote {
		return
	}
	jw.wrote = true
	ct := jw.Header().Get("Content-Type")
	if (status == http.StatusNotFound || status == http.StatusMethodNotAllowed) && !strings.HasPrefix(ct, "application/json") {
		jw.suppress = true
		jw.Header().Set("Content-Type", "application/json")
		jw.ResponseWriter.WriteHeader(status)
		code, msg := api.CodeNotFound, fmt.Sprintf("no such endpoint: %s %s", jw.r.Method, jw.r.URL.Path)
		if status == http.StatusMethodNotAllowed {
			code, msg = api.CodeMethodNotAllowed, fmt.Sprintf("method %s not allowed for %s", jw.r.Method, jw.r.URL.Path)
		}
		body, _ := json.Marshal(api.APIError{Error: api.APIErrorBody{Code: code, Message: msg}})
		_, _ = jw.ResponseWriter.Write(append(body, '\n'))
		return
	}
	jw.ResponseWriter.WriteHeader(status)
}

func (jw *jsonErrorWriter) Write(p []byte) (int, error) {
	if !jw.wrote {
		jw.WriteHeader(http.StatusOK)
	}
	if jw.suppress {
		return len(p), nil
	}
	return jw.ResponseWriter.Write(p)
}

// Flush keeps SSE streaming working through the wrapper.
func (jw *jsonErrorWriter) Flush() {
	if f, ok := jw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

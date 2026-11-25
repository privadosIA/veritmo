package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/platform/edition"
)

// A model the edition does not use is a 400 with its own code, for the assistant
// and for describe alike.
func TestModelNotAllowedIs400(t *testing.T) {
	s, _ := newProjectsServer(t)
	err := fmt.Errorf("task: %w", &edition.NotAllowedError{Model: "x-abliterated"})
	s.assistant.AssistRun = func(context.Context, assist.Task, string, string) (string, error) { return "", err }
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist", map[string]string{"task": "image", "text": "x"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"code":"model_not_allowed"`) {
		t.Fatalf("assist: %d %s", rec.Code, rec.Body)
	}
}

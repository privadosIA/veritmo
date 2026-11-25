package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/server/api"
)

const svgTestDrawing = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1920 1080"><circle id="ring" cx="960" cy="300" r="120"/></svg>`

func svgServer(t *testing.T) (*Server, *atomic.Int32, *atomic.Value) {
	t.Helper()
	s, _ := newProjectsServer(t)
	var calls atomic.Int32
	var last atomic.Value
	last.Store("")
	s.assistant.AssistSVGRun = func(ctx context.Context, text, model string) (string, error) {
		calls.Add(1)
		last.Store(text + "|" + model)
		return svgTestDrawing, nil
	}
	return s, &calls, &last
}

func TestAssistSVG_ReturnsTheDrawing(t *testing.T) {
	s, calls, last := svgServer(t)
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist/svg", map[string]string{"text": "  un círculo  ", "model": "qwen3:8b"})
	var out struct{ SVG, Model string }
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusOK || out.SVG != svgTestDrawing || out.Model != "qwen3:8b" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if calls.Load() != 1 || last.Load() != "un círculo|qwen3:8b" {
		t.Errorf("calls %d, last %v", calls.Load(), last.Load())
	}
}

func TestAssistSVG_InvalidInputIs400AndNeverRuns(t *testing.T) {
	s, calls, _ := svgServer(t)
	for name, body := range map[string]any{
		"empty":     map[string]string{"text": "  "},
		"missing":   map[string]string{},
		"too long":  map[string]string{"text": strings.Repeat("a", assist.MaxTextLen+1)},
		"not JSON":  "texto",
		"bad field": map[string]int{"text": 3},
	} {
		rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist/svg", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if calls.Load() != 0 {
		t.Errorf("the assistant ran %d times for invalid input", calls.Load())
	}
}

func TestAssistSVG_FailuresUseTheStandardErrorBody(t *testing.T) {
	s, _ := newProjectsServer(t)
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{fmt.Errorf("%w: it said no", assist.ErrRefused), http.StatusUnprocessableEntity, api.CodeAssistRefused},
		{fmt.Errorf("%w: no viewBox", assist.ErrBadSVG), http.StatusUnprocessableEntity, api.CodeValidation},
		{fmt.Errorf("%w: down", assist.ErrUnreachable), http.StatusServiceUnavailable, api.CodeUnavailable},
		{fmt.Errorf("%w: gone", assist.ErrNotInstalled), http.StatusServiceUnavailable, api.CodeUnavailable},
		{fmt.Errorf("%w: full", assist.ErrNoMemory), http.StatusServiceUnavailable, api.CodeUnavailable},
	} {
		err := tc.err
		s.assistant.AssistSVGRun = func(context.Context, string, string) (string, error) { return "", err }
		rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist/svg", map[string]string{"text": "algo"})
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), `"code":"`+tc.code+`"`) || !strings.Contains(rec.Body.String(), err.Error()) {
			t.Errorf("%v: %d %s", err, rec.Code, rec.Body)
		}
	}
}

func TestAssistSVG_RefusedWhileARunIsActive(t *testing.T) {
	s, fe := launchServer(t)
	var calls atomic.Int32
	s.assistant.AssistSVGRun = func(context.Context, string, string) (string, error) {
		calls.Add(1)
		return svgTestDrawing, nil
	}
	id, _ := launchRun(t, s, "demo", map[string]any{"gpu": "1"})
	waitStart(t, fe)
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist/svg", map[string]string{"text": "un círculo"})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), id) || calls.Load() != 0 {
		t.Fatalf("%d %s calls %d", rec.Code, rec.Body, calls.Load())
	}
	fe.chanFor(id) <- true
	waitState(t, s, id, stateSucceeded)
	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/assist/svg", map[string]string{"text": "un círculo"})
	if rec.Code != http.StatusOK || calls.Load() != 1 {
		t.Fatalf("after the run: %d calls %d", rec.Code, calls.Load())
	}
}

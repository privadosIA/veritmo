package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/assistant/describe"
)

// describeServer is a server with a project "p" holding sources/ref.png and a fake describer.
func describeServer(t *testing.T, run func(path string, opts describe.Options) (describe.Result, error)) (*Server, string) {
	t.Helper()
	s, projects := newProjectsServer(t)
	createProject(t, s, "p")
	src := filepath.Join(projects, "p", "sources")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(src, "ref.png")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.assistant.DescribeRun = func(ctx context.Context, path string, opts describe.Options) (describe.Result, error) {
		return run(path, opts)
	}
	return s, file
}

func TestDescribe_ReturnsPromptAndModel(t *testing.T) {
	var gotPath string
	var gotOpts describe.Options
	s, file := describeServer(t, func(path string, opts describe.Options) (describe.Result, error) {
		gotPath, gotOpts = path, opts
		return describe.Result{Prompt: "Two adults on a bench", Model: "gemma4:26b", Kind: describe.KindImage, Frames: 1}, nil
	})
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/describe", map[string]any{"project": "p", "source": "ref.png", "detail": true, "model": "x:1"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"prompt":"Two adults on a bench"`) || !strings.Contains(rec.Body.String(), `"model":"gemma4:26b"`) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if gotPath != file || !gotOpts.Detail || gotOpts.Model != "x:1" {
		t.Errorf("path=%q opts=%+v", gotPath, gotOpts)
	}
}

func TestDescribe_InvalidRequestsNeverRun(t *testing.T) {
	ran := 0
	s, _ := describeServer(t, func(string, describe.Options) (describe.Result, error) { ran++; return describe.Result{}, nil })
	for name, body := range map[string]map[string]any{
		"no project":    {"source": "ref.png"},
		"traversal":     {"project": "p", "source": "../ref.png"},
		"audio source":  {"project": "p", "source": "a.mp3"},
		"unknown":       {"project": "p", "source": "other.png"},
		"other project": {"project": "nope", "source": "ref.png"},
	} {
		rec := doJSON(t, s.Handler(), http.MethodPost, "/api/describe", body)
		if rec.Code < 400 || rec.Code > 404 {
			t.Errorf("%s: status %d: %s", name, rec.Code, rec.Body)
		}
	}
	if ran != 0 {
		t.Errorf("describe ran %d times for invalid requests", ran)
	}
}

func TestDescribe_RefusedIs422WithAlternatives(t *testing.T) {
	s, file := describeServer(t, func(string, describe.Options) (describe.Result, error) {
		return describe.Result{}, &describe.RefusedError{Model: "gemma4:26b", Alternatives: []string{"detail-model:30b"}}
	})
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/describe", map[string]any{"project": "p", "source": "ref.png"})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "describe_refused") || !strings.Contains(rec.Body.String(), "detail-model:30b") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(file); err != nil {
		t.Error("a refusal by the model must not delete the file")
	}
}

func TestDescribe_MinorIs422AndDeletesTheFile(t *testing.T) {
	s, file := describeServer(t, func(string, describe.Options) (describe.Result, error) {
		return describe.Result{}, describe.ErrMinor
	})
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/describe", map[string]any{"project": "p", "source": "ref.png"})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "describe_minor") || strings.Contains(rec.Body.String(), `"prompt"`) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Error("the file of a refused picture must be deleted")
	}
}

func TestDescribe_UnavailableIs503(t *testing.T) {
	s, _ := describeServer(t, func(string, describe.Options) (describe.Result, error) {
		return describe.Result{}, describe.ErrNotInstalled
	})
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/describe", map[string]any{"project": "p", "source": "ref.png"})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestDescribe_Models(t *testing.T) {
	s, _ := newProjectsServer(t)
	s.assistant.DescribeModels = func(ctx context.Context) (string, string, []describe.Model, error) {
		return "gemma4:26b", "detail-model:30b", []describe.Model{{Name: "gemma4:26b", SizeBytes: 1, Fits: true}}, nil
	}
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/describe/models", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"default":"gemma4:26b"`) || !strings.Contains(rec.Body.String(), `"detail":"detail-model:30b"`) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestDescribe_SplitIsPassedAndShotsReturned(t *testing.T) {
	var got describe.Options
	s, _ := describeServer(t, func(path string, opts describe.Options) (describe.Result, error) {
		got = opts
		return describe.Result{Prompt: "first", Model: "gemma4:26b", Kind: describe.KindVideo, Frames: 2, Shots: []describe.Shot{
			{Start: 0, End: 4, Prompt: "first", Motion: "slow push in"},
			{Start: 4, End: 9.5, Prompt: "second"},
		}}, nil
	})
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/describe", map[string]any{"project": "p", "source": "ref.png", "split": true})
	if rec.Code != http.StatusOK || !got.Split {
		t.Fatalf("status %d split=%v: %s", rec.Code, got.Split, rec.Body)
	}
	for _, want := range []string{`"shots":[`, `"start":4`, `"end":9.5`, `"motion":"slow push in"`, `"prompt":"first"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("missing %s in %s", want, rec.Body)
		}
	}
	// Not asked: no split reaches the describer.
	doJSON(t, s.Handler(), http.MethodPost, "/api/describe", map[string]any{"project": "p", "source": "ref.png"})
	if got.Split {
		t.Error("split must default to false")
	}
}

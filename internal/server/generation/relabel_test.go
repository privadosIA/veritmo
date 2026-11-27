package generation

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"veritmo/internal/platform/events"
	"veritmo/internal/server/workspace"
)

// The label of a generation, an image edit and a video edit can be changed afterwards: the item, the listings and the label list show the new
// one at once, and the run already made keeps the label it was made with.
func TestRelabel_AGenerationAnImageEditAndAVideoEditChangeTheirLabel(t *testing.T) {
	for _, kind := range []struct {
		name, path string
		make       func(t *testing.T) (s *testServer, id, runID string)
	}{
		{"generation", "/api/generations/", func(t *testing.T) (*testServer, string, string) {
			s := genServer(t, fullCatalog())
			g := decodeGeneration(t, postGeneration(t, s, map[string]any{"prompt": "p", "option": "ltx", "client": "first-dash"}))
			waitGeneration(t, s, g.ID, GenDone)
			return s, g.ID, g.RunID
		}},
		{"image edit", "/api/image-edits/", func(t *testing.T) (*testServer, string, string) {
			s := editServer(t)
			e := decodeEdit(t, postEdit(t, s, map[string]any{"model": "kontext", "prompt": "a red cap", "client": "first-dash"}, pngBytes(160, 90)))
			waitEdit(t, s, e.ID, GenDone)
			return s, e.ID, e.RunID
		}},
		{"video edit", "/api/video-edits/", func(t *testing.T) (*testServer, string, string) {
			s := videoEditServer(t)
			v := decodeVideoEdit(t, postVideoEdit(t, s, map[string]any{"tool": "scale", "client": "first-dash"}, clip, nil))
			waitVideoEdit(t, s, v.ID, GenDone)
			return s, v.ID, v.RunID
		}},
	} {
		t.Run(kind.name, func(t *testing.T) {
			s, id, runID := kind.make(t)
			patch := func(body any) (int, string) {
				rec := doJSON(t, s.Handler(), http.MethodPatch, kind.path+id, body)
				return rec.Code, rec.Body.String()
			}
			if code, body := patch(map[string]any{"client": "second-dash"}); code != http.StatusOK || !contains(body, `"client":"second-dash"`) {
				t.Fatalf("relabel: %d %s", code, body)
			}
			if rec := doJSON(t, s.Handler(), http.MethodGet, kind.path+id, nil); !contains(rec.Body.String(), `"client":"second-dash"`) {
				t.Errorf("the item still says the old label: %s", rec.Body)
			}
			if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/projects?managed=include&client=second-dash", nil); !contains(rec.Body.String(), id) {
				t.Errorf("the project listing does not show it under the new label: %s", rec.Body)
			}
			if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/projects?managed=include&client=first-dash", nil); contains(rec.Body.String(), id) {
				t.Errorf("the project listing still shows it under the old label")
			}
			if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/clients", nil); !contains(rec.Body.String(), `"name":"second-dash"`) || contains(rec.Body.String(), `"name":"first-dash"`) {
				t.Errorf("the list of labels: %s", rec.Body)
			}
			if run, err := s.Workspace.RunRecord(runID); err != nil || run.Client != "first-dash" {
				t.Errorf("the run made earlier keeps its label: %q (%v)", run.Client, err)
			}
			// null removes it: the label the server gives by default applies again
			if code, body := patch(map[string]any{"client": nil}); code != http.StatusOK || !contains(body, `"client":"unknown"`) {
				t.Errorf("remove: %d %s", code, body)
			}
			for name, bad := range map[string]any{"another field": map[string]any{"client": "x", "name": "y"}, "a bad name": map[string]any{"client": "Bad Name"}, "no field": map[string]any{}, "a number": map[string]any{"client": 3}} {
				if code, _ := patch(bad); code != http.StatusBadRequest {
					t.Errorf("%s: %d", name, code)
				}
			}
			if rec := doJSON(t, s.Handler(), http.MethodPatch, kind.path+"api-gen-nope", map[string]any{"client": "x"}); rec.Code != http.StatusNotFound {
				t.Errorf("an unknown item: %d", rec.Code)
			}
		})
	}
}

// While the item has a run in progress the label does not change.
func TestRelabel_WhileItRunsIsA409(t *testing.T) {
	s := genServer(t, fullCatalog())
	started := make(chan struct{}, 1)
	s.Workspace.Execute = func(ctx context.Context, rn workspace.ExecRun, em *events.Emitter) (string, error) {
		em.PipelineStart()
		started <- struct{}{}
		<-ctx.Done()
		return "", ctx.Err()
	}
	g := decodeGeneration(t, postGeneration(t, s, map[string]any{"prompt": "p", "option": "ltx", "client": "first-dash"}))
	<-started
	if rec := doJSON(t, s.Handler(), http.MethodPatch, "/api/generations/"+g.ID, map[string]any{"client": "second-dash"}); rec.Code != http.StatusConflict {
		t.Fatalf("relabel while it runs: %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/generations/"+g.ID, nil); !contains(rec.Body.String(), `"client":"first-dash"`) {
		t.Errorf("the label changed: %s", rec.Body)
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}

package server

// generation_http_test.go: the tests of the generation API that need the whole server: its authentication, the
// content mode and the contract in openapi.yaml.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"veritmo/internal/platform/edition"
	"veritmo/internal/platform/events"
	"veritmo/internal/project/config"
	"veritmo/internal/server/generation"
	"veritmo/internal/server/workspace"
)

func TestImageEdits_ATokenIsNeeded(t *testing.T) {
	s := editServer(t)
	s.auth = newAuthenticator(testToken)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/image-edits"}, {http.MethodGet, "/api/image-edits/x"}, {http.MethodDelete, "/api/image-edits/x"},
	} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}")))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a token: %d", tc.method, tc.path, rec.Code)
		}
	}
}

func TestContract_AnImageEditFitsItsDescription(t *testing.T) {
	c := newContractChecker(t)
	s := editServer(t)
	e := decodeEdit(t, postEdit(t, s, map[string]any{"model": "into", "prompt": "p", "strength": 0.5, "variants": 2, "negative_prompt": "x"}, pngBytes(16, 9)))
	done := waitEdit(t, s, e.ID, generation.GenDone)
	body := get(t, s, "/api/image-edits/"+e.ID)
	expectFits(t, c, "/api/image-edits/{id}", body)
	if len(done.Results) == 0 || !strings.Contains(body, `"results"`) {
		t.Errorf("the case must carry the results: %.300s", body)
	}
}

func TestGenerationOptions_AskForTheTokenAndListTheOptions(t *testing.T) {
	s := genServer(t, fullCatalog())
	s.auth = newAuthenticator(testToken)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/generation/options", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("without a token: %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/generation/options", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("with the token: %d %s", rec.Code, rec.Body)
	}
	var got generation.GenerationOptions
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Video) != 12 || len(got.ImageModels) == 0 || len(got.AspectRatios) == 0 || len(got.Qualities) != 3 {
		t.Errorf("options = %+v", got)
	}
	if !got.Video[0].Default || !got.ImageModels[0].Default {
		t.Errorf("the first available of each list is the default: %+v / %+v", got.Video[0], got.ImageModels[0])
	}
}

func TestContract_AGenerationFitsItsDescription(t *testing.T) {
	c := newContractChecker(t)
	s := genServer(t, fullCatalog())
	g := decodeGeneration(t, postGeneration(t, s, map[string]any{"prompt": "p", "option": "ltx"}))
	done := waitGeneration(t, s, g.ID, generation.GenDone)
	expectFits(t, c, "/api/generations/{id}", get(t, s, "/api/generations/"+g.ID))
	expectFits(t, c, "/api/generations", get(t, s, "/api/generations"))
	if done.Video == "" {
		t.Error("the case must carry the video")
	}
}

func TestContract_TheGenerationOptionsFitTheirDescription(t *testing.T) {
	c := newContractChecker(t)
	// An option that cannot run carries its reason; the others carry their estimate.
	s := genServer(t, baseCatalog())
	body := get(t, s, "/api/generation/options")
	expectFits(t, c, "/api/generation/options", body)
	for _, field := range []string{`"reason"`, `"estimate"`} {
		if !strings.Contains(body, field) {
			t.Errorf("the case must carry %s, which is optional: %.400s", field, body)
		}
	}
}

func TestGenerations_AdultContentIsAllowedWhereTheServerKeepsIt(t *testing.T) {
	if !edition.ContentAll() {
		t.Skip("this build cannot start in the mode that keeps adult content")
	}
	s, _ := newProjectsServerMode(t, ContentAll)
	s.generation.GenCatalog = func() []catalogEntry { return fullCatalog() }
	s.generation.GenMachine = func() generation.Machine { return generation.Machine{VRAMMB: 16384, RAMGB: 32, Speed: 1} }
	touchFiles(t, ".", fullCatalog())
	s.workspace.SetPipelineLookPath(fakeLook)
	s.workspace.Execute = func(ctx context.Context, rn workspace.ExecRun, em *events.Emitter) (string, error) {
		return "out/x.mp4", nil
	}
	t.Cleanup(s.workspace.Shutdown)
	rec := postGeneration(t, s, map[string]any{"prompt": "p", "option": "ltx", "adult": true})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	cfg, _ := config.Load(s.workspace.ProjectConfigPath(decodeGeneration(t, rec).ID))
	if cfg.Adult == nil || !*cfg.Adult {
		t.Errorf("the label was not kept: %+v", cfg.Adult)
	}
}

func TestGenerations_AGenerationIsLabelledSoAStrictServerShowsIt(t *testing.T) {
	s, _ := newProjectsServerMode(t, ContentStrict)
	s.generation.GenCatalog = func() []catalogEntry { return fullCatalog() }
	s.generation.GenMachine = func() generation.Machine { return generation.Machine{VRAMMB: 16384, RAMGB: 32, Speed: 1} }
	touchFiles(t, ".", fullCatalog())
	s.workspace.SetPipelineLookPath(fakeLook)
	s.workspace.Execute = func(ctx context.Context, rn workspace.ExecRun, em *events.Emitter) (string, error) {
		return "out/x.mp4", nil
	}
	t.Cleanup(s.workspace.Shutdown)
	rec := postGeneration(t, s, map[string]any{"prompt": "p", "option": "ltx"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	g := decodeGeneration(t, rec)
	if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/generations/"+g.ID, nil); rec.Code != http.StatusOK {
		t.Errorf("a strict server hides a generation: %d", rec.Code)
	}
}

func TestGenerations_ATokenIsNeeded(t *testing.T) {
	s := genServer(t, fullCatalog())
	s.auth = newAuthenticator(testToken)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/generations"}, {http.MethodGet, "/api/generations"},
		{http.MethodGet, "/api/generations/x"}, {http.MethodDelete, "/api/generations/x"},
	} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}")))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a token: %d", tc.method, tc.path, rec.Code)
		}
	}
}

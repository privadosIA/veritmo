package server

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"veritmo/internal/server/generation"
)

// scenesClient is the `client` of the scenes.yaml of a managed project.
func scenesClient(t *testing.T, s *Server, id string) string {
	t.Helper()
	cfg, err := s.workspace.ProjectConfig(id)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Client
}

func TestClient_AGenerationCarriesItEverywhere(t *testing.T) {
	s := genServer(t, fullCatalog())
	rec := postGeneration(t, s, map[string]any{"prompt": "a cat", "option": "ltx", "client": "video-studio"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	g := decodeGeneration(t, rec)
	if g.Client != "video-studio" || scenesClient(t, s, g.ID) != "video-studio" {
		t.Errorf("answer client %q, scenes.yaml %q", g.Client, scenesClient(t, s, g.ID))
	}
	done := waitGeneration(t, s, g.ID, generation.GenDone)
	if done.Client != "video-studio" {
		t.Errorf("read client %q", done.Client)
	}
	if r, _ := s.workspace.RunRecord(g.RunID); r.Client != "video-studio" {
		t.Errorf("run client %q", r.Client)
	}
	var list []generationView
	if err := json.Unmarshal(doJSON(t, s.Handler(), http.MethodGet, "/api/generations", nil).Body.Bytes(), &list); err != nil || len(list) != 1 || list[0].Client != "video-studio" {
		t.Errorf("list = %+v, %v", list, err)
	}
}

func TestClient_WithoutOneItIsUnknownAndAnOldRecordToo(t *testing.T) {
	s := genServer(t, fullCatalog())
	g := decodeGeneration(t, postGeneration(t, s, map[string]any{"prompt": "a cat", "option": "ltx"}))
	if g.Client != "unknown" || scenesClient(t, s, g.ID) != "unknown" {
		t.Errorf("client %q, scenes.yaml %q", g.Client, scenesClient(t, s, g.ID))
	}
	waitGeneration(t, s, g.ID, generation.GenDone)
	if r, _ := s.workspace.RunRecord(g.RunID); r.Client != "unknown" {
		t.Errorf("run client %q", r.Client)
	}
	// A record from before clients existed: no client in its request.
	info, ok := s.workspace.ReadManaged(g.ID)
	if !ok {
		t.Fatal("the generation has no record")
	}
	var req map[string]any
	_ = json.Unmarshal(info.Request, &req)
	delete(req, "client")
	info.Request, _ = json.Marshal(req)
	if err := s.workspace.PutManaged(info); err != nil {
		t.Fatal(err)
	}
	if got := decodeGeneration(t, doJSON(t, s.Handler(), http.MethodGet, "/api/generations/"+g.ID, nil)); got.Client != "unknown" {
		t.Errorf("an old generation reads client %q", got.Client)
	}
}

func TestClient_ANameThatDoesNotFitRefusesAndCreatesNothing(t *testing.T) {
	s := genServer(t, fullCatalog())
	for _, bad := range []string{"Video Studio", "VIDEO", "", "a_b", strings.Repeat("a", 33)} {
		rec := postGeneration(t, s, map[string]any{"prompt": "a cat", "option": "ltx", "client": bad})
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "client") {
			t.Errorf("generation %q: status %d: %s", bad, rec.Code, rec.Body)
		}
	}
	es := editServer(t)
	for _, bad := range []string{"Video Studio", "", strings.Repeat("a", 33)} {
		rec := postEdit(t, es, map[string]any{"model": "into", "prompt": "night", "client": bad}, pngBytes(160, 90))
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "client") {
			t.Errorf("edit %q: status %d: %s", bad, rec.Code, rec.Body)
		}
	}
	for _, srv := range []*Server{s, es} {
		entries, _ := os.ReadDir(srv.cfg.ProjectsDir)
		for _, e := range entries {
			if isManagedProject(e.Name()) {
				t.Errorf("a refused request left %s behind", e.Name())
			}
		}
	}
}

func TestClient_AnEditCarriesItInTheMultipartRequestPart(t *testing.T) {
	s := editServer(t)
	rec := postEdit(t, s, map[string]any{"model": "into", "prompt": "night", "client": "photo-lab"}, pngBytes(160, 90))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	e := decodeEdit(t, rec)
	if e.Client != "photo-lab" || scenesClient(t, s, e.ID) != "photo-lab" {
		t.Errorf("answer client %q, scenes.yaml %q", e.Client, scenesClient(t, s, e.ID))
	}
	if done := waitEdit(t, s, e.ID, generation.GenDone); done.Client != "photo-lab" {
		t.Errorf("read client %q", done.Client)
	}
	if r, _ := s.workspace.RunRecord(e.RunID); r.Client != "photo-lab" {
		t.Errorf("run client %q", r.Client)
	}
	plain := decodeEdit(t, postEdit(t, s, map[string]any{"model": "into", "prompt": "day"}, pngBytes(160, 90)))
	if plain.Client != "unknown" {
		t.Errorf("an edit without client reads %q", plain.Client)
	}
}

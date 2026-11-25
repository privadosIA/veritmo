package generation

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"veritmo/internal/project/config"
)

func adultCatalog() []catalogEntry {
	m := tSD15()
	m.ID, m.Adult, m.Styles, m.StyleRank = "adult-sd", true, []string{"x"}, 1
	m.Files = files("", "adult.safetensors")
	return append(fullCatalog(), m)
}

func imageModelIDs(t *testing.T, s *testServer) []string {
	t.Helper()
	var out generationOptions
	if err := json.Unmarshal(doJSON(t, s.Handler(), http.MethodGet, "/api/generation/options", nil).Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, m := range out.ImageModels {
		ids = append(ids, m.ID)
	}
	return ids
}

// A server that hides adult content does not offer a model made for adults to a generation or an edit, and refuses
// it by name as one that does not exist; the one that shows everything offers it and labels what it makes adult.
func TestGenerationAPI_ModelsMadeForAdultsFollowTheContentMode(t *testing.T) {
	s := editServer(t)
	s.GenCatalog = func() []catalogEntry { return adultCatalog() }
	touchFiles(t, ".", adultCatalog())
	if slices.Contains(imageModelIDs(t, s), "adult-sd") {
		t.Errorf("a censored server offers the model: %v", imageModelIDs(t, s))
	}
	rec := postEdit(t, s, map[string]any{"model": "adult-sd", "prompt": "night"}, pngBytes(160, 90))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "unknown") {
		t.Errorf("a censored server takes the model: %d %s", rec.Code, rec.Body)
	}

	s.Adult = true
	if !slices.Contains(imageModelIDs(t, s), "adult-sd") {
		t.Fatalf("the server that shows everything does not offer it: %v", imageModelIDs(t, s))
	}
	rec = postEdit(t, s, map[string]any{"model": "adult-sd", "prompt": "night"}, pngBytes(160, 90))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	e := decodeEdit(t, rec)
	cfg, err := config.Load(filepath.Join(s.Workspace.ProjectDir(e.ID), "scenes.yaml"))
	if err != nil || cfg.Adult == nil || !*cfg.Adult {
		t.Errorf("the edit is not labelled adult: %+v, %v", cfg.Adult, err)
	}
	other := decodeEdit(t, postEdit(t, s, map[string]any{"model": "sd-1-5", "prompt": "night"}, pngBytes(160, 90)))
	if c, _ := config.Load(filepath.Join(s.Workspace.ProjectDir(other.ID), "scenes.yaml")); c.Adult == nil || *c.Adult {
		t.Errorf("an edit with an ordinary model must stay labelled fine: %+v", c.Adult)
	}
}

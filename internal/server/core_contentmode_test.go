package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"veritmo/internal/platform/edition"
)

// labelled makes the projects a (adult: true), b (adult: false) and c (not looked at), each with a
// finished run and a kept clip.
func labelled(t *testing.T, s *Server, dir string) {
	t.Helper()
	for id, label := range map[string]string{"a": "adult: true\n", "b": "adult: false\n", "c": ""} {
		createProject(t, s, id)
		raw, err := s.workspace.ProjectDefinition(id)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.workspace.SaveProjectDefinition(id, []byte(label+string(raw)), "save"); err != nil {
			t.Fatal(err)
		}
		clips := filepath.Join(dir, id, "out", "clips")
		if err := os.MkdirAll(clips, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(clips, "s1.mp4"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		run := &Run{ID: "run-" + id, Project: id, State: "succeeded", CreatedAt: now, StartedAt: &now, FinishedAt: &now}
		if err := s.workspace.SeedRun(run); err != nil {
			t.Fatal(err)
		}
	}
}

func idsOf(t *testing.T, rec interface{ Bytes() []byte }, key string) []string {
	t.Helper()
	var got []map[string]any
	var wrapped map[string][]map[string]any
	if json.Unmarshal(rec.Bytes(), &got) != nil {
		if err := json.Unmarshal(rec.Bytes(), &wrapped); err != nil {
			t.Fatalf("not a list: %s", rec.Bytes())
		}
		got = wrapped[key]
	}
	var out []string
	for _, m := range got {
		for _, k := range []string{"project", "id"} {
			if v, ok := m[k].(string); ok {
				out = append(out, v)
				break
			}
		}
	}
	return out
}

func sameSet(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	m := map[string]bool{}
	for _, g := range got {
		m[g] = true
	}
	for _, w := range want {
		if !m[w] {
			return false
		}
	}
	return true
}

func TestContentMode_ListsLeaveOutWhatIsHidden(t *testing.T) {
	for mode, want := range map[ContentMode][]string{ContentCensored: {"b", "c"}, ContentStrict: {"b"}, "": {"b", "c"}} {
		s, dir := newProjectsServerMode(t, mode)
		labelled(t, s, dir)
		h := s.Handler()
		if got := idsOf(t, doJSON(t, h, http.MethodGet, "/api/projects", nil).Body, ""); !sameSet(got, want...) {
			t.Errorf("%q projects: %v want %v", mode, got, want)
		}
		if got := idsOf(t, doJSON(t, h, http.MethodGet, "/api/runs", nil).Body, ""); !sameSet(got, want...) {
			t.Errorf("%q runs: %v want %v", mode, got, want)
		}
		if got := idsOf(t, doJSON(t, h, http.MethodGet, "/api/clips", nil).Body, "clips"); !sameSet(got, want...) {
			t.Errorf("%q clips: %v want %v", mode, got, want)
		}
		var st struct {
			Totals struct{ Runs int } `json:"totals"`
		}
		if err := json.Unmarshal(doJSON(t, h, http.MethodGet, "/api/stats", nil).Body.Bytes(), &st); err != nil || st.Totals.Runs != len(want) {
			t.Errorf("%q stats count %d want %d (%v)", mode, st.Totals.Runs, len(want), err)
		}
		var store struct {
			Projects []struct{ ID string } `json:"projects"`
		}
		_ = json.Unmarshal(doJSON(t, h, http.MethodGet, "/api/storage", nil).Body.Bytes(), &store)
		var got []string
		for _, p := range store.Projects {
			got = append(got, p.ID)
		}
		if !sameSet(got, want...) {
			t.Errorf("%q storage: %v want %v", mode, got, want)
		}
	}
}

// A hidden project answers exactly like one that does not exist.
func TestContentMode_AHiddenProjectIsAnUnknownOne(t *testing.T) {
	s, dir := newProjectsServerMode(t, ContentCensored)
	labelled(t, s, dir)
	h := s.Handler()
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/projects/%s/config"},
		{http.MethodGet, "/api/projects/%s/cache"},
		{http.MethodGet, "/api/projects/%s/sources"},
		{http.MethodGet, "/api/projects/%s/artifacts/out/clips/s1.mp4"},
		{http.MethodGet, "/api/projects/%s/storage"},
		{http.MethodGet, "/api/projects/%s/export"},
		{http.MethodPost, "/api/projects/%s/runs"},
		{http.MethodPost, "/api/projects/%s/duplicate"},
		{http.MethodGet, "/api/projects/%s/reviews"},
		{http.MethodDelete, "/api/projects/%s"},
	} {
		hidden := doJSON(t, h, tc.method, strings.Replace(tc.path, "%s", "a", 1), nil)
		missing := doJSON(t, h, tc.method, strings.Replace(tc.path, "%s", "nope", 1), nil)
		if hidden.Code != http.StatusNotFound {
			t.Errorf("%s %s: %d %s", tc.method, tc.path, hidden.Code, hidden.Body)
			continue
		}
		want := strings.ReplaceAll(missing.Body.String(), "nope", "a")
		if hidden.Body.String() != want {
			t.Errorf("%s %s: the body differs from the unknown project's:\n%s\n%s", tc.method, tc.path, hidden.Body, want)
		}
	}
	// the visible ones still work
	if rec := doJSON(t, h, http.MethodGet, "/api/projects/b/config", nil); rec.Code != http.StatusOK {
		t.Errorf("b: %d", rec.Code)
	}
	// its runs too
	for _, path := range []string{"/api/runs/run-a", "/api/runs/run-a/events", "/api/runs/run-a/resources"} {
		hidden := doJSON(t, h, http.MethodGet, path, nil)
		missing := doJSON(t, h, http.MethodGet, strings.Replace(path, "run-a", "run-nope", 1), nil)
		if hidden.Code != http.StatusNotFound || hidden.Body.String() != strings.ReplaceAll(missing.Body.String(), "run-nope", "run-a") {
			t.Errorf("%s: %d %s", path, hidden.Code, hidden.Body)
		}
	}
	if rec := doJSON(t, h, http.MethodGet, "/api/runs/run-b", nil); rec.Code != http.StatusOK {
		t.Errorf("run-b: %d", rec.Code)
	}
	// a bulk delete ignores the hidden run
	rec := doJSON(t, h, http.MethodPost, "/api/runs/delete", map[string]any{"ids": []string{"run-a", "run-b"}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"deleted":1`) {
		t.Errorf("bulk delete: %d %s", rec.Code, rec.Body)
	}
	if _, err := s.workspace.RunRecord("run-a"); err != nil {
		t.Errorf("the hidden run must survive: %v", err)
	}
	// and so does a filter
	doJSON(t, h, http.MethodPost, "/api/runs/delete", map[string]any{"states": []string{"succeeded"}})
	if _, err := s.workspace.RunRecord("run-a"); err != nil {
		t.Errorf("the hidden run must survive a filter: %v", err)
	}
}

func TestContentMode_NothingForAdultsIsCreatedOrSaved(t *testing.T) {
	s, dir := newProjectsServerMode(t, ContentCensored)
	labelled(t, s, dir)
	h := s.Handler()
	raw, _ := s.workspace.ProjectDefinition("b")
	adult := strings.Replace(string(raw), "adult: false", "adult: true", 1)
	rec := doJSON(t, h, http.MethodPut, "/api/projects/b/config", map[string]string{"yaml": adult})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"content_not_allowed"`) {
		t.Fatalf("saving the label: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "strict") || strings.Contains(rec.Body.String(), "mode") {
		t.Errorf("the refusal must not say how the mode changes: %s", rec.Body)
	}
	after, _ := s.workspace.ProjectDefinition("b")
	if string(after) != string(raw) {
		t.Error("the saved definition must be unchanged")
	}
	rec = doJSON(t, h, http.MethodPost, "/api/import/confirm", map[string]string{"id": "imp", "yaml": adult})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("import: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(dir, "imp")); err == nil {
		t.Error("no project may be created")
	}
	// marking it as reviewed is fine
	if rec := doJSON(t, h, http.MethodPut, "/api/projects/b/config", map[string]string{"yaml": string(raw)}); rec.Code != http.StatusOK {
		t.Errorf("saving as it was: %d %s", rec.Code, rec.Body)
	}
}

func TestContentMode_TheLabelFollowsTheSavedDefinition(t *testing.T) {
	s, dir := newProjectsServerMode(t, ContentCensored)
	labelled(t, s, dir)
	if !s.projectVisible("c") {
		t.Fatal("c is shown")
	}
	raw, err := s.workspace.ProjectDefinition("c")
	if err != nil {
		t.Fatal(err)
	}
	// a file edited by hand has no effect: the label is the one of the saved definition
	path := filepath.Join(dir, "c", "scenes.yaml")
	if err := os.WriteFile(path, []byte("adult: true\n"+string(raw)), 0o644); err != nil {
		t.Fatal(err)
	}
	if !s.projectVisible("c") {
		t.Error("the file on disk is not what the server reads")
	}
	// saving it as the definition changes the label at once
	if err := s.workspace.SaveProjectDefinition("c", []byte("adult: true\n"+string(raw)), "save"); err != nil {
		t.Fatal(err)
	}
	if s.projectVisible("c") {
		t.Error("c was marked for adults in its definition: it must be hidden now")
	}
}

func TestContentMode_TheEnvironmentSaysIt(t *testing.T) {
	for mode, want := range map[ContentMode]string{"": "censored", ContentStrict: "strict"} {
		s, _ := newProjectsServerMode(t, mode)
		rec := doJSON(t, s.Handler(), http.MethodGet, "/api/environment", nil)
		if !strings.Contains(rec.Body.String(), `"content_mode":"`+want+`"`) {
			t.Errorf("%q: %s", mode, rec.Body.String()[:200])
		}
	}
}

// Every route is classified, so a new one cannot forget the gate.
func TestEveryRouteIsClassified(t *testing.T) {
	s, _ := newProjectsServer(t)
	for _, p := range s.patterns {
		if routeClass(p) == "" {
			t.Errorf("route %q has a project, run or review value that the content gate does not know", p)
		}
	}
	for p := range listRoutes {
		found := false
		for _, q := range s.patterns {
			found = found || p == q
		}
		if !found {
			t.Errorf("listRoutes names %q, which is not a route", p)
		}
	}
}

func TestContentMode_ADatasetOfAdultModelsIsRefusedByACensoredServer(t *testing.T) {
	s, dir := newProjectsServerMode(t, ContentCensored)
	h := s.Handler()
	adult := `[["patin","t","","a roller skate\nAdult: true"]]`
	rec := doJSON(t, h, http.MethodPost, "/api/import/preview", map[string]any{"content": adult, "project": "objetos", "mode": "models3d"})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"content_not_allowed"`) {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "strict") {
		t.Errorf("the refusal must not say how the mode changes: %s", rec.Body)
	}
	// a dataset that marks nothing as adult imports; so does one that says Adult: false
	for _, ok := range []string{`[["patin","t","","a roller skate"]]`, `[["patin","t","","a roller skate\nAdult: false"]]`} {
		if rec := doJSON(t, h, http.MethodPost, "/api/import/preview", map[string]any{"content": ok, "project": "objetos", "mode": "models3d"}); rec.Code != http.StatusOK {
			t.Errorf("%s: %d %s", ok, rec.Code, rec.Body)
		}
	}
	// the confirmation refuses the same entry in a project file
	yml := "project: objetos\noutput: {mode: models3d}\nmodels:\n  - {id: patin, prompt: a roller skate, adult: true}\n"
	rec = doJSON(t, h, http.MethodPost, "/api/import/confirm", map[string]string{"id": "objetos", "yaml": yml})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"content_not_allowed"`) {
		t.Errorf("confirm: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(dir, "objetos")); err == nil {
		t.Error("no project may be created")
	}
	// a server that shows everything takes it (only a build that allows that mode can start one)
	if edition.ContentAll() {
		open, _ := newProjectsServerMode(t, ContentAll)
		if rec := doJSON(t, open.Handler(), http.MethodPost, "/api/import/preview", map[string]any{"content": adult, "project": "objetos", "mode": "models3d"}); rec.Code != http.StatusOK {
			t.Errorf("an open server: %d %s", rec.Code, rec.Body)
		}
	}
}

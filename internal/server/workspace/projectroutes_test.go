package workspace

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestProjectGet_AnswersTheProjectAsTheListShowsIt(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "demo")
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/projects/demo", nil)
	var one ProjectSummary
	_ = json.Unmarshal(rec.Body.Bytes(), &one)
	var list []ProjectSummary
	_ = json.Unmarshal(doJSON(t, s.Handler(), http.MethodGet, "/api/projects", nil).Body.Bytes(), &list)
	if rec.Code != 200 || len(list) != 1 || one != list[0] || one.ID != "demo" || one.Mode == "" {
		t.Fatalf("%d %+v vs %+v", rec.Code, one, list)
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/projects/nope", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("%d", rec.Code)
	}
}

func TestProjectPatch_ChangesNameClientAndAdultKeepingTheTextAndTheVersion(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "demo")
	commented := "# my notes stay\n" + starterOf(t, s, "demo")
	if err := s.SaveProjectDefinition("demo", []byte(commented), "save"); err != nil {
		t.Fatal(err)
	}
	patch := func(body any) (int, ProjectSummary) {
		rec := doJSON(t, s.Handler(), http.MethodPatch, "/api/projects/demo", body)
		var p ProjectSummary
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		return rec.Code, p
	}
	before, _ := s.projects.Versions("demo")
	code, p := patch(map[string]any{"client": "lab"})
	if code != 200 || p.Client != "lab" {
		t.Fatalf("%d %+v", code, p)
	}
	def := starterOf(t, s, "demo")
	if !strings.Contains(def, "client: lab") || !strings.Contains(def, "# my notes stay") {
		t.Fatalf("the client is in the definition and the comment stays:\n%s", def)
	}
	after, _ := s.projects.Versions("demo")
	if len(after) != len(before)+1 || after[0].Reason != "patch" {
		t.Fatalf("the previous definition is a version: %+v", after)
	}
	if v, _ := s.projects.Version("demo", after[0].Version); string(v.Definition) != commented {
		t.Fatal("the version is the text before the patch")
	}
	// the name and the label
	if code, p := patch(map[string]any{"name": "new-name", "adult": false}); code != 200 || p.Name != "new-name" || p.Adult == nil || *p.Adult {
		t.Fatalf("%d %+v", code, p)
	}
	// null removes them
	if code, p := patch(map[string]any{"client": nil}); code != 200 || p.Client != "embedded" || strings.Contains(starterOf(t, s, "demo"), "client:") {
		t.Fatalf("%d %+v", code, p)
	}
	// the same value again: no new version
	n := len(mustVersions(t, s))
	patch(map[string]any{"name": "new-name"})
	if len(mustVersions(t, s)) != n {
		t.Fatal("a patch that changes nothing adds no version")
	}
	// refusals
	if code, _ := patch(map[string]any{"scenes": []int{}}); code != http.StatusBadRequest {
		t.Fatalf("a field that cannot be changed: %d", code)
	}
	if code, _ := patch(map[string]any{}); code != http.StatusBadRequest {
		t.Fatalf("nothing: %d", code)
	}
	if code, _ := patch(map[string]any{"name": "not a valid name"}); code != http.StatusUnprocessableEntity {
		t.Fatalf("a name the loader refuses: %d", code)
	}
	if code, _ := patch(map[string]any{"client": "Not A Client"}); code != http.StatusUnprocessableEntity {
		t.Fatalf("a client that is not valid: %d", code)
	}
	if rec := doJSON(t, s.Handler(), http.MethodPatch, "/api/projects/nope", map[string]any{"client": "lab"}); rec.Code != http.StatusNotFound {
		t.Fatalf("%d", rec.Code)
	}
	s.busy = func(string) (string, bool) { return "run-1", true }
	if code, _ := patch(map[string]any{"client": "other"}); code != http.StatusConflict {
		t.Fatalf("during a run: %d", code)
	}
	s.busy = nil
}

func mustVersions(t *testing.T, s *testServer) []ProjectVersion {
	t.Helper()
	vs, err := s.projects.Versions("demo")
	if err != nil {
		t.Fatal(err)
	}
	return vs
}

func TestProjectVersions_ListReadAndRestore(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "demo")
	base := starterOf(t, s, "demo")
	one, two := "# one\n"+base, "# two\n"+base
	_ = s.SaveProjectDefinition("demo", []byte(one), "save")
	_ = s.SaveProjectDefinition("demo", []byte(two), "fix")
	type vv struct {
		Version   int    `json:"version"`
		Reason    string `json:"reason"`
		Bytes     int    `json:"bytes"`
		CreatedAt string `json:"created_at"`
		YAML      string `json:"yaml"`
	}
	var list []vv
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/projects/demo/versions", nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if rec.Code != 200 || len(list) != 2 || list[0].Version != 2 || list[0].Reason != "fix" || list[1].Version != 1 || list[0].CreatedAt == "" || list[1].Bytes != len(base) {
		t.Fatalf("newest first: %d %+v", rec.Code, list)
	}
	var page []vv
	rec = doJSON(t, s.Handler(), http.MethodGet, "/api/projects/demo/versions?limit=1&offset=1", nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	if len(page) != 1 || page[0].Version != 1 || rec.Header().Get("X-Total-Count") != "2" {
		t.Fatalf("%+v %v", page, rec.Header())
	}
	var got vv
	_ = json.Unmarshal(doJSON(t, s.Handler(), http.MethodGet, "/api/projects/demo/versions/1", nil).Body.Bytes(), &got)
	if got.YAML != base {
		t.Fatalf("the text of the version: %q", got.YAML)
	}
	for _, bad := range []string{"/api/projects/demo/versions/9", "/api/projects/demo/versions/zero", "/api/projects/nope/versions/1"} {
		if rec := doJSON(t, s.Handler(), http.MethodGet, bad, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
	// restore version 1: it is the definition and the text it replaced is the newest version
	s.busy = func(string) (string, bool) { return "run-1", true }
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/demo/versions/1/restore", nil); rec.Code != http.StatusConflict {
		t.Fatalf("during a run: %d", rec.Code)
	}
	s.busy = nil
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/demo/versions/9/restore", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("%d", rec.Code)
	}
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/demo/versions/1/restore", nil); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var cfg struct {
		YAML string `json:"yaml"`
	}
	_ = json.Unmarshal(doJSON(t, s.Handler(), http.MethodGet, "/api/projects/demo/config", nil).Body.Bytes(), &cfg)
	if cfg.YAML != base {
		t.Fatalf("the restored text is the definition: %q", cfg.YAML)
	}
	vs := mustVersions(t, s)
	if top, _ := s.projects.Version("demo", vs[0].Version); vs[0].Reason != "restore" || string(top.Definition) != two {
		t.Fatalf("the text it replaced is the newest version: %+v", vs[0])
	}
}

// A project of the generation API or of the music mode keeps its label in its own record too: the project route refuses to change only the project's
// copy, names the route that changes both, and leaves everything as it was. The other fields, and the ordinary projects, work as always.
func TestProjectPatch_AManagedProjectIsRelabelledByItsOwnRoute(t *testing.T) {
	s, _ := newProjectsServer(t)
	seedMixed(t, s)
	for id, want := range map[string]string{
		"api-gen-20261107-000000-aaaaaa":  "PATCH /api/generations/{id}",
		"api-edit-20261107-000000-bbbbbb": "PATCH /api/image-edits/{id}",
		"api-vid-20261107-000000-cccccc":  "PATCH /api/video-edits/{id}",
		"api-mm-20261107-000000-dddddd":   "PATCH /api/music-motion/pieces/{id}",
		"api-m3d-20261107-000000-eeeeee":  "PATCH /api/models3d/generations/{id}",
	} {
		if !s.ProjectExists(id) {
			seedProjectClient(t, s, id, "", "")
		}
		before, _ := s.projectClient(id)
		rec := doJSON(t, s.Handler(), http.MethodPatch, "/api/projects/"+id, map[string]any{"client": "other-dash"})
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %d %s", id, rec.Code, rec.Body)
		}
		if after, _ := s.projectClient(id); after != before {
			t.Errorf("%s: the label changed from %q to %q", id, before, after)
		}
	}
	// the null that removes a label is a relabel too
	if rec := doJSON(t, s.Handler(), http.MethodPatch, "/api/projects/api-gen-20261107-000000-aaaaaa", map[string]any{"client": nil}); rec.Code != http.StatusBadRequest {
		t.Errorf("client null: %d", rec.Code)
	}
	// name and adult still go through the project route, which is their only one: they are not refused by the guard (the seeded project points to
	// files that are not there, so it is the loader that answers 422, which shows the request went past the guard)
	if rec := doJSON(t, s.Handler(), http.MethodPatch, "/api/projects/api-gen-20261107-000000-aaaaaa", map[string]any{"adult": false}); rec.Code == http.StatusBadRequest {
		t.Errorf("adult on a managed project is refused by the guard: %d %s", rec.Code, rec.Body)
	}
	// an ordinary project is relabelled as always
	createProject(t, s, "demo")
	if rec := doJSON(t, s.Handler(), http.MethodPatch, "/api/projects/demo", map[string]any{"client": "other-dash"}); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"client":"other-dash"`) {
		t.Errorf("an ordinary project: %d %s", rec.Code, rec.Body)
	}
}

package workspace

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedProjectClient writes a project folder: its scenes.yaml (with the client line when given) and, for a managed one, its
// generation.json with the request's client.
func seedProjectClient(t *testing.T, s *testServer, id, yamlClient, RequestClient string) {
	t.Helper()
	dir := s.ProjectDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	y := "project: " + id + "\noutput:\n  mode: video\n  file: out/x.mp4\n"
	if yamlClient != "" {
		y = "client: " + yamlClient + "\n" + y
	}
	y += "defaults:\n  model: m.safetensors\n  voice: v.onnx\n  resolution: 1280x720\n  sampler: euler\n  steps: 30\n  fps: 30\nscenes:\n  - id: a\n    prompt: p\n    tts_text: t\n    speed: 1.0\n"
	if err := os.WriteFile(filepath.Join(dir, "scenes.yaml"), []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	if kind := managedKindOfID(id); kind != "" || IsManagedProject(id) {
		req := map[string]any{}
		if RequestClient != "" {
			req["client"] = RequestClient
		}
		raw, _ := json.Marshal(req)
		info, _ := json.Marshal(ManagedInfo{ID: id, Kind: kind, Request: raw})
		if err := os.WriteFile(filepath.Join(dir, ManagedRecord), info, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ImportFolders(); err != nil {
		t.Fatal(err)
	}
}

func TestProjectClient(t *testing.T) {
	s, _ := newProjectsServer(t)
	seedProjectClient(t, s, "plain", "", "")
	seedProjectClient(t, s, "mine", "video-studio", "")
	seedProjectClient(t, s, "api-gen-20261107-000000-aaaaaa", "", "")
	seedProjectClient(t, s, "api-edit-20261107-000000-bbbbbb", "", "dash-b")
	seedProjectClient(t, s, "api-vid-20261107-000000-cccccc", "dash-c", "ignored")
	for id, want := range map[string]struct {
		client string
		kind   ManagedKind
	}{
		"plain":                           {"embedded", ""},
		"mine":                            {"video-studio", ""},
		"api-gen-20261107-000000-aaaaaa":  {"unknown", KindGeneration},
		"api-edit-20261107-000000-bbbbbb": {"dash-b", KindImageEdit},
		"api-vid-20261107-000000-cccccc":  {"dash-c", KindVideoEdit},
	} {
		if c, k := s.projectClient(id); c != want.client || k != want.kind {
			t.Errorf("%s: client=%q kind=%q, want %q %q", id, c, k, want.client, want.kind)
		}
	}
	if listedManaged("api-mm-x") || !IsManagedProject("api-mm-x") {
		t.Error("an animation is managed but never listed")
	}
}

func TestParseListFilter(t *testing.T) {
	parse := func(q string) (listFilter, error) {
		return parseListFilter(httptest.NewRequest("GET", "/api/x"+q, nil))
	}
	if f, err := parse(""); err != nil || f.Include || f.Clients != nil {
		t.Errorf("default: %+v %v", f, err)
	}
	if f, err := parse("?managed=include&client=a-b,embedded"); err != nil || !f.Include || !f.Clients["a-b"] || !f.Clients["embedded"] {
		t.Errorf("full: %+v %v", f, err)
	}
	for _, q := range []string{"?managed=everything", "?managed=true", "?client=BAD", "?client=a,,b", "?client=" + "x123456789012345678901234567890123"} {
		if _, err := parse(q); err == nil {
			t.Errorf("%s must be refused", q)
		}
	}
	f, _ := parse("?client=embedded")
	if !f.shows("plain", "embedded") || f.shows("plain", "x") || f.shows("api-gen-1", "embedded") {
		t.Error("shows")
	}
	g, _ := parse("?managed=include")
	if !g.shows("api-gen-1", "unknown") || g.shows("api-mm-1", "unknown") {
		t.Error("include lists generations but not animations")
	}
	a, err := parse("?managed=all&client=dash-b")
	if err != nil || !a.All || !a.Include || !a.shows("api-mm-1", "dash-b") || !a.shows("api-gen-1", "dash-b") || a.shows("api-mm-1", "other") || !a.shows("plain", "dash-b") {
		t.Errorf("all lists the generations and the animations of the clients asked for: %+v %v", a, err)
	}
	if _, err := parse("?managed=nope"); err == nil || !strings.Contains(err.Error(), "include") || !strings.Contains(err.Error(), "all") {
		t.Errorf("a bad value names what is accepted: %v", err)
	}
}

func TestProjectCreate_WithAClient(t *testing.T) {
	s, _ := newProjectsServer(t)
	h := s.Handler()
	if rec := doJSON(t, h, "POST", "/api/projects", map[string]any{"id": "con", "client": "video-studio"}); rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if c, _ := s.projectClient("con"); c != "video-studio" {
		t.Errorf("client = %q", c)
	}
	raw, _ := s.ProjectDefinition("con")
	if !strings.Contains(string(raw), "client: video-studio") {
		t.Errorf("scenes.yaml: %s", raw)
	}
	if rec := doJSON(t, h, "POST", "/api/projects", map[string]any{"id": "sin"}); rec.Code != 201 {
		t.Fatalf("create: %d", rec.Code)
	}
	raw, _ = s.ProjectDefinition("sin")
	if strings.Contains(string(raw), "client:") {
		t.Errorf("no client given, none stored: %s", raw)
	}
	if c, _ := s.projectClient("sin"); c != "embedded" {
		t.Errorf("client = %q", c)
	}
	rec := doJSON(t, h, "POST", "/api/projects", map[string]any{"id": "mal", "client": "Bad Name"})
	if rec.Code != 422 || s.ProjectExists("mal") {
		t.Errorf("a bad client is a 422 and creates nothing: %d %s", rec.Code, rec.Body)
	}
}

func getJSON(t *testing.T, s *testServer, path string, into any) int {
	t.Helper()
	rec := doJSON(t, s.Handler(), "GET", path, nil)
	if into != nil && rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
			t.Fatalf("%s: %v\n%s", path, err, rec.Body)
		}
	}
	return rec.Code
}

// seedMixed makes one project of every origin.
func seedMixed(t *testing.T, s *testServer) {
	t.Helper()
	seedProjectClient(t, s, "plain", "", "")
	seedProjectClient(t, s, "mine", "video-studio", "")
	seedProjectClient(t, s, "api-gen-20261107-000000-aaaaaa", "video-studio", "video-studio")
	seedProjectClient(t, s, "api-edit-20261107-000000-bbbbbb", "", "dash-b")
	seedProjectClient(t, s, "api-vid-20261107-000000-cccccc", "", "")
	seedProjectClient(t, s, "api-mm-20261107-000000-dddddd", "", "")
}

func TestProjectsList_ClientKindAndManaged(t *testing.T) {
	s, _ := newProjectsServer(t)
	seedMixed(t, s)
	ids := func(q string) (map[string]ProjectSummary, int) {
		var list []ProjectSummary
		code := getJSON(t, s, "/api/projects"+q, &list)
		m := map[string]ProjectSummary{}
		for _, p := range list {
			m[p.ID] = p
		}
		return m, code
	}
	def, _ := ids("")
	if len(def) != 2 || def["plain"].Client != "embedded" || def["mine"].Client != "video-studio" || def["plain"].Kind != "" {
		t.Errorf("default: %+v", def)
	}
	inc, _ := ids("?managed=include")
	if len(inc) != 5 {
		t.Fatalf("included: %d projects %+v", len(inc), inc)
	}
	if g := inc["api-gen-20261107-000000-aaaaaa"]; g.Client != "video-studio" || g.Kind != KindGeneration {
		t.Errorf("generation: %+v", g)
	}
	if g := inc["api-edit-20261107-000000-bbbbbb"]; g.Client != "dash-b" || g.Kind != KindImageEdit {
		t.Errorf("edit: %+v", g)
	}
	if g := inc["api-vid-20261107-000000-cccccc"]; g.Client != "unknown" || g.Kind != KindVideoEdit {
		t.Errorf("video edit: %+v", g)
	}
	if _, ok := inc["api-mm-20261107-000000-dddddd"]; ok {
		t.Error("managed=include does not list an animation")
	}
	all, code := ids("?managed=all")
	if code != 200 || len(all) != 6 {
		t.Fatalf("managed=all: %d, %d projects %+v", code, len(all), all)
	}
	if a := all["api-mm-20261107-000000-dddddd"]; a.Kind != KindMusicMotion || a.Client != "unknown" {
		t.Errorf("animation: %+v", a)
	}
	if only, _ := ids("?managed=all&client=unknown"); len(only) != 2 || only["api-mm-20261107-000000-dddddd"].ID == "" || only["api-vid-20261107-000000-cccccc"].ID == "" {
		t.Errorf("the client filter on animations: %+v", only)
	}
	only, _ := ids("?managed=include&client=video-studio,embedded")
	if len(only) != 3 {
		t.Errorf("two clients: %+v", only)
	}
	if _, code := ids("?managed=everything"); code != 400 {
		t.Errorf("a bad managed value: %d", code)
	}
	if _, code := ids("?client=Bad"); code != 400 {
		t.Errorf("client=Bad: %d", code)
	}
}

func keepClip(t *testing.T, s *testServer, id string) {
	t.Helper()
	dir := filepath.Join(s.ProjectDir(id), "out", "clips")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.mp4"), []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestClipsList_ClientProjectKindAndManaged(t *testing.T) {
	s, _ := newProjectsServer(t)
	seedMixed(t, s)
	for _, id := range []string{"plain", "mine", "api-gen-20261107-000000-aaaaaa", "api-mm-20261107-000000-dddddd"} {
		keepClip(t, s, id)
	}
	var def struct{ Clips []keptClip }
	getJSON(t, s, "/api/clips", &def)
	if len(def.Clips) != 2 || def.Clips[0].Client == "" || def.Clips[0].ProjectKind != "" {
		t.Errorf("default: %+v", def.Clips)
	}
	var inc struct{ Clips []keptClip }
	getJSON(t, s, "/api/clips?managed=include", &inc)
	by := map[string]keptClip{}
	for _, c := range inc.Clips {
		by[c.Project] = c
	}
	if len(by) != 3 || by["api-gen-20261107-000000-aaaaaa"].ProjectKind != KindGeneration || by["api-gen-20261107-000000-aaaaaa"].Client != "video-studio" || by["plain"].Client != "embedded" {
		t.Errorf("included: %+v", by)
	}
	var only struct{ Clips []keptClip }
	getJSON(t, s, "/api/clips?managed=include&client=video-studio", &only)
	if len(only.Clips) != 2 {
		t.Errorf("client filter: %+v", only.Clips)
	}
	var all struct{ Clips []keptClip }
	getJSON(t, s, "/api/clips?managed=all", &all)
	byAll := map[string]keptClip{}
	for _, c := range all.Clips {
		byAll[c.Project] = c
	}
	if len(byAll) != 4 || byAll["api-mm-20261107-000000-dddddd"].ProjectKind != KindMusicMotion || byAll["api-mm-20261107-000000-dddddd"].Client != "unknown" {
		t.Errorf("managed=all: %+v", byAll)
	}
	if _, ok := by["api-mm-20261107-000000-dddddd"]; ok {
		t.Error("managed=include lists an animation's clip")
	}
	if code := getJSON(t, s, "/api/clips?managed=x", nil); code != 400 {
		t.Errorf("managed=x: %d", code)
	}
}

func TestLibrary_ClientAndFilters(t *testing.T) {
	s, _ := newProjectsServer(t)
	songProject(t, s, "balada", "", [2]any{"one", int64(11)})
	songProject(t, s, "cancion", "", [2]any{"two", int64(22)})
	raw, _ := s.ProjectDefinition("cancion")
	if err := s.SaveProjectDefinition("cancion", append([]byte("client: music-studio\n"), raw...), "save"); err != nil {
		t.Fatal(err)
	}
	lib := getLibrary(t, s)
	by := map[string]string{}
	for _, sg := range lib.Songs {
		by[sg.Project] = sg.Client
	}
	if by["balada"] != "embedded" || by["cancion"] != "music-studio" {
		t.Errorf("clients: %v", by)
	}
	var only LibraryResponse
	getJSON(t, s, "/api/library?client=music-studio", &only)
	if len(only.Songs) != 1 || only.Songs[0].Project != "cancion" {
		t.Errorf("filtered: %+v", only.Songs)
	}
	var inc LibraryResponse
	if code := getJSON(t, s, "/api/library?managed=include", &inc); code != 200 || len(inc.Songs) != 2 {
		t.Errorf("include: %d %d", code, len(inc.Songs))
	}
	if code := getJSON(t, s, "/api/library?managed=no", nil); code != 400 {
		t.Errorf("managed=no: %d", code)
	}
}

func TestStorage_ClientManagedAndDiskTotalsUnchanged(t *testing.T) {
	s, _ := newProjectsServer(t)
	seedMixed(t, s)
	type body struct {
		Projects []struct {
			ID         string      `json:"id"`
			TotalBytes int64       `json:"total_bytes"`
			Client     string      `json:"client"`
			Kind       ManagedKind `json:"kind"`
		} `json:"projects"`
		Disk map[string]any `json:"disk"`
	}
	var def, inc body
	getJSON(t, s, "/api/storage", &def)
	getJSON(t, s, "/api/storage?managed=include", &inc)
	if len(def.Projects) != 2 || len(inc.Projects) != 5 {
		t.Fatalf("projects: %d default, %d included", len(def.Projects), len(inc.Projects))
	}
	kinds := map[string]ManagedKind{}
	clients := map[string]string{}
	for _, p := range inc.Projects {
		kinds[p.ID], clients[p.ID] = p.Kind, p.Client
	}
	if kinds["api-gen-20261107-000000-aaaaaa"] != KindGeneration || clients["api-gen-20261107-000000-aaaaaa"] != "video-studio" || clients["plain"] != "embedded" {
		t.Errorf("kinds=%v clients=%v", kinds, clients)
	}
	if def.Disk["total_bytes"] != inc.Disk["total_bytes"] {
		t.Errorf("the disk total is the disk's, not a sum: %v vs %v", def.Disk["total_bytes"], inc.Disk["total_bytes"])
	}
	var all body
	getJSON(t, s, "/api/storage?managed=all", &all)
	var anim bool
	for _, p := range all.Projects {
		if p.ID == "api-mm-20261107-000000-dddddd" {
			anim = p.Kind == KindMusicMotion && p.Client == "unknown"
		}
	}
	if len(all.Projects) != 6 || !anim {
		t.Errorf("managed=all: %d projects, animation listed with its kind: %v", len(all.Projects), anim)
	}
	if def.Disk["total_bytes"] != all.Disk["total_bytes"] {
		t.Errorf("the disk total is the disk's, not a sum: %v vs %v", def.Disk["total_bytes"], all.Disk["total_bytes"])
	}
	var only body
	getJSON(t, s, "/api/storage?managed=include&client=dash-b", &only)
	if len(only.Projects) != 1 || only.Projects[0].Kind != KindImageEdit {
		t.Errorf("client filter: %+v", only.Projects)
	}
	if code := getJSON(t, s, "/api/storage?managed=2", nil); code != 400 {
		t.Errorf("managed=2: %d", code)
	}
	var one map[string]any
	if code := getJSON(t, s, "/api/projects/api-gen-20261107-000000-aaaaaa/storage", &one); code != 200 {
		t.Errorf("the storage of a managed project: %d", code)
	}
}

func TestProjectDelete_AManagedProjectPointsToItsOwnEndpoint(t *testing.T) {
	s, _ := newProjectsServer(t)
	seedMixed(t, s)
	for id, want := range map[string]string{
		"api-gen-20261107-000000-aaaaaa":  "DELETE /api/generations/{id}",
		"api-edit-20261107-000000-bbbbbb": "DELETE /api/image-edits/{id}",
		"api-vid-20261107-000000-cccccc":  "DELETE /api/video-edits/{id}",
		"api-mm-20261107-000000-dddddd":   "DELETE /api/music-motion/pieces/{id}",
	} {
		rec := doJSON(t, s.Handler(), "DELETE", "/api/projects/"+id, nil)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %d %s", id, rec.Code, rec.Body)
		}
		if !s.ProjectExists(id) {
			t.Errorf("%s was deleted", id)
		}
	}
	if rec := doJSON(t, s.Handler(), "DELETE", "/api/projects/plain", nil); rec.Code != 204 {
		t.Errorf("an ordinary project is still deleted: %d", rec.Code)
	}
}

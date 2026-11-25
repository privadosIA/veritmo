package workspace

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/platform/media"
)

// newMediaServer is a projects server whose projects are kept in a temporary media directory (never the real media directory).
func newMediaServer(t *testing.T) (*testServer, string, string) {
	t.Helper()
	s, projects := newProjectsServer(t)
	m := t.TempDir()
	s.cfg.MediaDir = m
	return s, projects, m
}

func TestMedia_NewProjectLivesInTheMediaFolder(t *testing.T) {
	s, projects, m := newMediaServer(t)
	createProject(t, s, "p")
	for _, part := range media.Parts {
		if !media.IsLinked(filepath.Join(projects, "p"), m, "p", part) {
			t.Fatalf("%s is not a link into the media folder", part.Name)
		}
	}
	if _, err := os.Stat(filepath.Join(m, "p", "config", "scenes.yaml")); err != nil {
		t.Fatal(err)
	}
	// Saving the scenes keeps the link and writes the real file.
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/projects/p/config", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get config: %d", rec.Code)
	}
	var cfg struct {
		YAML string `json:"yaml"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &cfg)
	rec = doJSON(t, s.Handler(), http.MethodPut, "/api/projects/p/config", map[string]string{"yaml": cfg.YAML + "\n# edited\n"})
	if rec.Code != http.StatusOK {
		t.Fatalf("put config: %d %s", rec.Code, rec.Body)
	}
	if !media.IsLinked(filepath.Join(projects, "p"), m, "p", media.Parts[0]) {
		t.Fatal("saving the scenes replaced the link")
	}
	// The definition lives in the database; the file in the media folder is written when something needs it (a run, an export).
	if err := s.MaterializeProject("p"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(m, "p", "config", "scenes.yaml"))
	if !strings.Contains(string(b), "# edited") {
		t.Fatal("the real file was not updated")
	}
}

func TestMedia_StorageCountsTheMediaFolderAndArtifactsAreServed(t *testing.T) {
	s, projects, m := newMediaServer(t)
	createProject(t, s, "p")
	put(t, projects, "p", "out/p.mp4", 5000)
	put(t, projects, "p", "out/clips/c.anim.mp4", 400)
	put(t, projects, "p", "tmp/manifest.json", 5)
	put(t, projects, "p", "sources/photo.png", 7)
	if _, err := os.Stat(filepath.Join(m, "p", "out", "p.mp4")); err != nil {
		t.Fatal("the result did not land in the media folder: ", err)
	}
	a := getStorage(t, s, "p")
	for part, want := range map[string]int64{"final": 5000, "animations": 400, "cache": 5, "sources": 7} {
		if got := partBytes(a, part); got != want {
			t.Errorf("%s = %d, want %d", part, got, want)
		}
	}
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/projects/p/artifacts/out/p.mp4", nil)
	if rec.Code != http.StatusOK || rec.Body.Len() != 5000 {
		t.Fatalf("artifact: %d (%d bytes)", rec.Code, rec.Body.Len())
	}
	// A link that leads outside the project's media folder is still refused.
	other := t.TempDir()
	os.WriteFile(filepath.Join(other, "secret"), []byte("x"), 0o644)
	os.Symlink(filepath.Join(other, "secret"), filepath.Join(m, "p", "out", "leak"))
	rec = doJSON(t, s.Handler(), http.MethodGet, "/api/projects/p/artifacts/out/leak", nil)
	if rec.Code == http.StatusOK {
		t.Fatal("a link out of the media folder was served")
	}
	// Cleaning animations works through the links.
	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/projects/p/storage/clean", map[string]any{"parts": []string{"animations"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("clean: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(m, "p", "out", "clips", "c.anim.mp4")); err == nil {
		t.Fatal("the animation was not removed")
	}
}

func TestMedia_StorageEndpointReportsTheMediaDiskAndPath(t *testing.T) {
	s, _, m := newMediaServer(t)
	createProject(t, s, "p")
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/storage", nil)
	var body struct {
		Media struct {
			Dir  string `json:"dir"`
			Disk struct {
				Free  int64 `json:"free_bytes"`
				Total int64 `json:"total_bytes"`
			} `json:"disk"`
			Projects []struct{ ID, Folder string } `json:"projects"`
		} `json:"media"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Media.Dir != m || body.Media.Disk.Total == 0 || body.Media.Disk.Free == 0 {
		t.Fatalf("media: %+v", body.Media)
	}
	if len(body.Media.Projects) != 1 || body.Media.Projects[0].Folder != filepath.Join(m, "p") {
		t.Fatalf("projects: %+v", body.Media.Projects)
	}
}

func TestMedia_NoSettingNoMediaField(t *testing.T) {
	s, _ := newProjectsServer(t)
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/storage", nil)
	if strings.Contains(rec.Body.String(), `"media"`) {
		t.Fatalf("media present without a setting: %s", rec.Body)
	}
}

func TestMedia_DeleteRemovesTheMediaFolderOnly(t *testing.T) {
	s, projects, m := newMediaServer(t)
	createProject(t, s, "a")
	createProject(t, s, "b")
	put(t, projects, "a", "out/a.mp4", 10)
	put(t, projects, "b", "out/b.mp4", 10)
	rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/projects/a", nil)
	if rec.Code != http.StatusNoContent && rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(m, "a")); err == nil {
		t.Fatal("the media folder of a is still there")
	}
	if _, err := os.Stat(filepath.Join(m, "b", "out", "b.mp4")); err != nil {
		t.Fatal("b was touched")
	}
}

func TestMedia_RunRefusedWhenTheDiskIsGone(t *testing.T) {
	s, _, m := newMediaServer(t)
	createProject(t, s, "p")
	if err := os.RemoveAll(m); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m, []byte("a file where the disk was"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/p/runs", nil)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "media directory") {
		t.Fatalf("run: %d %s", rec.Code, rec.Body)
	}
}

func TestMedia_ExportAndImportFollowTheLinks(t *testing.T) {
	s, projects, m := newMediaServer(t)
	createProject(t, s, "demo")
	put(t, projects, "demo", "sources/mine.png", 4)
	put(t, projects, "demo", "motion/gfx.html", 6)
	rec := call(s.Handler(), http.MethodGet, "/api/projects/demo/export?format=zip&lang=en", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "mine.png") {
		t.Fatal("the sources behind the link are not in the package")
	}
	conf := post(s.Handler(), "/api/import/package/confirm?id=copy", rec.Body.Bytes())
	if conf.Code != http.StatusCreated {
		t.Fatalf("confirm: %d %s", conf.Code, conf.Body)
	}
	if _, err := os.Stat(filepath.Join(m, "copy", "config", "sources", "mine.png")); err != nil {
		t.Fatalf("the imported source is not in the media folder: %v", err)
	}
	if !media.Linked(filepath.Join(projects, "copy"), m, "copy") {
		t.Fatal("the imported project is not linked")
	}
}

func newModelsServer(t *testing.T) (*testServer, string) {
	t.Helper()
	s, _ := newProjectsServer(t)
	m := t.TempDir()
	s.cfg.ModelsDir = m
	return s, m
}

func TestModels_StorageAndEnvironmentReportTheDirectoryAndItsDisk(t *testing.T) {
	s, m := newModelsServer(t)
	for _, path := range []string{"/api/storage", "/api/environment"} {
		rec := doJSON(t, s.Handler(), http.MethodGet, path, nil)
		var body map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		key := "models"
		if path == "/api/environment" {
			key = "models_dir"
		}
		var md struct {
			Dir       string `json:"dir"`
			Available bool   `json:"available"`
			Linked    bool   `json:"linked"`
			Disk      struct {
				Free  int64 `json:"free_bytes"`
				Total int64 `json:"total_bytes"`
			} `json:"disk"`
		}
		if err := json.Unmarshal(body[key], &md); err != nil {
			t.Fatalf("%s %s: %v (%s)", path, key, err, rec.Body)
		}
		if md.Dir != m || !md.Available || md.Linked || md.Disk.Total == 0 || md.Disk.Free == 0 {
			t.Fatalf("%s: %+v", path, md)
		}
	}
}

func TestModels_NoSettingNoField(t *testing.T) {
	s, _ := newProjectsServer(t)
	for _, path := range []string{"/api/storage", "/api/environment"} {
		rec := doJSON(t, s.Handler(), http.MethodGet, path, nil)
		if strings.Contains(rec.Body.String(), `"models_dir"`) || (path == "/api/storage" && strings.Contains(rec.Body.String(), `"models"`)) {
			t.Fatalf("%s carries the models directory without a setting: %s", path, rec.Body)
		}
	}
}

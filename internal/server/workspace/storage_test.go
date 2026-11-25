package workspace

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"veritmo/internal/platform/store"
)

type storageAnswer struct {
	Parts []storagePart `json:"parts"`
	Total int64         `json:"total_bytes"`
}

func partBytes(a storageAnswer, id string) int64 {
	for _, p := range a.Parts {
		if p.ID == id {
			return p.Bytes
		}
	}
	return -1
}

func getStorage(t *testing.T, s *testServer, project string) storageAnswer {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/projects/"+project+"/storage", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("storage: %d %s", rec.Code, rec.Body)
	}
	var a storageAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	return a
}

func put(t *testing.T, projects, project, rel string, n int) {
	t.Helper()
	p := filepath.Join(projects, project, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

// storageProject is a video project with one scene ("intro") and a file in every part.
func storageProject(t *testing.T) (*testServer, string) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "p")
	put(t, projects, "p", "voices-ignored.txt", 1) // a file of no part
	put(t, projects, "p", "tmp/scene_escena_1.png", 10)
	put(t, projects, "p", "tmp/manifest.json", 5)
	put(t, projects, "p", "tmp/scene_old.anim.avi", 1000) // a leftover: the scene is gone
	put(t, projects, "p", "out/clips/escena_1.mp4", 100)
	put(t, projects, "p", "out/clips/escena_1.anim.mp4", 400)
	put(t, projects, "p", "out/p.mp4", 5000)
	put(t, projects, "p", "sources/photo.png", 7)
	put(t, projects, "p", ".reviews/rev-1/f1.png", 30)
	// the backups of the definition are its versions: two, of 60 and 20 bytes (cleaning keeps the newest)
	for n, size := range []int{60, 20} {
		v := ProjectVersion{ProjectID: "p", Version: n + 1, Reason: "save", Bytes: size, CreatedAt: time.Now().UTC(), Definition: store.Text(strings.Repeat("x", size))}
		if err := s.Projects().db.DB.Create(&v).Error; err != nil {
			t.Fatal(err)
		}
	}
	return s, projects
}

func TestStorage_BreakdownByPartAndSymbolicLinksAreNotFollowed(t *testing.T) {
	s, projects := storageProject(t)
	// the models link of a project points at a large folder that is not the project's
	big := t.TempDir()
	if err := os.WriteFile(filepath.Join(big, "model.gguf"), make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(filepath.Join(projects, "p", "models"))
	if err := os.Symlink(big, filepath.Join(projects, "p", "models")); err != nil {
		t.Fatal(err)
	}
	a := getStorage(t, s, "p")
	want := map[string]int64{"cache": 15, "leftovers": 1000, "clips": 100, "animations": 400, "final": 5000, "sources": 7, "reviews": 30, "backups": 80}
	var sum int64
	for id, w := range want {
		if got := partBytes(a, id); got != w {
			t.Errorf("%s = %d, want %d", id, got, w)
		}
		sum += w
	}
	if a.Total != sum {
		t.Fatalf("total = %d, the parts add up to %d (the linked folder and scenes.yaml itself are not counted)", a.Total, sum)
	}
	for _, p := range a.Parts {
		if p.Cleanable != (p.ID != "final" && p.ID != "sources") {
			t.Errorf("%s cleanable = %v", p.ID, p.Cleanable)
		}
	}
}

func TestStorage_CleanARefusesTheResultsAndRemovesWhatWasAsked(t *testing.T) {
	s, projects := storageProject(t)
	for _, bad := range []string{"final", "sources", "nonsense"} {
		rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/p/storage/clean", map[string]any{"parts": []string{bad}})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", bad, rec.Code)
		}
	}
	if _, err := os.Stat(filepath.Join(projects, "p", "out", "p.mp4")); err != nil {
		t.Fatal("the final video is untouched")
	}

	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/p/storage/clean", map[string]any{"parts": []string{"animations", "leftovers"}})
	var res struct {
		Freed int64 `json:"freed_bytes"`
	}
	json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != http.StatusOK || res.Freed != 1400 {
		t.Fatalf("clean: %d %s", rec.Code, rec.Body)
	}
	for rel, want := range map[string]bool{
		"out/clips/escena_1.anim.mp4": false, "tmp/scene_old.anim.avi": false,
		"out/clips/escena_1.mp4": true, "tmp/scene_escena_1.png": true, "out/p.mp4": true, "sources/photo.png": true,
	} {
		if _, err := os.Stat(filepath.Join(projects, "p", filepath.FromSlash(rel))); (err == nil) != want {
			t.Errorf("%s present = %v, want %v", rel, err == nil, want)
		}
	}
	// reviews: the frames and the folders they leave empty go; the backups go but the newest version
	doJSON(t, s.Handler(), http.MethodPost, "/api/projects/p/storage/clean", map[string]any{"parts": []string{"reviews", "backups", "clips", "cache"}})
	a := getStorage(t, s, "p")
	if partBytes(a, "backups") != 20 {
		t.Errorf("backups = %d, want 20 (the newest version stays)", partBytes(a, "backups"))
	}
	for _, id := range []string{"reviews", "clips", "cache", "animations", "leftovers"} {
		if partBytes(a, id) != 0 {
			t.Errorf("%s still has %d bytes", id, partBytes(a, id))
		}
	}
	if _, err := os.Stat(filepath.Join(projects, "p", ".reviews", "rev-1")); !os.IsNotExist(err) {
		t.Error("the empty review folder is removed")
	}
	if partBytes(a, "final") != 5000 || partBytes(a, "sources") != 7 {
		t.Error("the results and the inputs stay")
	}
}

func TestStorage_AProjectWithAnActiveRunIsNotCleaned(t *testing.T) {
	s, fe := launchServer(t)
	runID, _ := launchRun(t, s, "demo", map[string]any{})
	<-fe.started // the run is in progress
	defer func() { fe.chanFor(runID) <- true }()
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/demo/storage/clean", map[string]any{"parts": []string{"cache"}})
	if rec.Code != http.StatusConflict {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestStorage_AllProjectsAndTheDisk(t *testing.T) {
	s, projects := storageProject(t)
	createProject(t, s, "q")
	put(t, projects, "q", "out/x.mp4", 123)
	// the list answers with the size kept with each project: it is measured when its own breakdown is asked for (or when a run ends...)
	getStorage(t, s, "p")
	getStorage(t, s, "q")
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/storage", nil)
	var a struct {
		Projects []struct {
			ID    string `json:"id"`
			Total int64  `json:"total_bytes"`
		} `json:"projects"`
		Disk diskReport `json:"disk"`
	}
	json.Unmarshal(rec.Body.Bytes(), &a)
	got := map[string]int64{}
	for _, p := range a.Projects {
		got[p.ID] = p.Total
	}
	if got["q"] != 123 || got["p"] != 6632 {
		t.Fatalf("sizes = %v", got)
	}
	if a.Disk.FreeBytes <= 0 || a.Disk.TotalBytes < a.Disk.FreeBytes || a.Disk.WarnBelowBytes != 20_000_000_000 {
		t.Fatalf("disk = %v", a.Disk)
	}
}

func TestLowDisk_ThresholdFromTheEnvironmentAndReportedEverywhere(t *testing.T) {
	// the machine has some free space: a huge threshold makes it low, 0 disables the warning
	t.Setenv("VERITMO_MIN_FREE_GB", "100000000")
	s, fe := launchServer(t)

	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/storage", nil)
	var a struct {
		Disk diskReport `json:"disk"`
	}
	json.Unmarshal(rec.Body.Bytes(), &a)
	if !a.Disk.Low || a.Disk.WarnBelowBytes != 100_000_000*1_000_000_000 {
		t.Fatalf("storage overview: %+v", a.Disk)
	}
	rec = doJSON(t, s.Handler(), http.MethodGet, "/api/environment", nil)
	var env struct {
		Disk *diskReport `json:"disk"`
	}
	json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Disk == nil || !env.Disk.Low {
		t.Fatalf("environment report: %s", rec.Body)
	}

	// launching is never blocked, and its answer carries the warning
	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/projects/demo/runs", map[string]any{})
	var launched struct {
		Warnings []string `json:"warnings"`
	}
	json.Unmarshal(rec.Body.Bytes(), &launched)
	if rec.Code != http.StatusAccepted || len(launched.Warnings) != 1 || !strings.Contains(launched.Warnings[0], "GB are free") {
		t.Fatalf("launch: %d %s", rec.Code, rec.Body)
	}
	runID := <-fe.started
	fe.chanFor(runID) <- true
}

func TestLowDisk_DisabledAndPlentyOfSpaceGiveNoWarning(t *testing.T) {
	for _, v := range []string{"0", "0.001"} { // disabled; a threshold of one megabyte
		t.Setenv("VERITMO_MIN_FREE_GB", v)
		s, fe := launchServer(t)
		rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/demo/runs", map[string]any{})
		if rec.Code != http.StatusAccepted || strings.Contains(rec.Body.String(), "warnings") {
			t.Fatalf("%s: %d %s", v, rec.Code, rec.Body)
		}
		runID := <-fe.started
		fe.chanFor(runID) <- true
	}
}

func TestStorage_CleaningTheBackupsKeepsTheNewestVersionAndTheDefinition(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "p")
	base := starterOf(t, s, "p")
	for i := 1; i <= 8; i++ {
		if _, err := s.projects.Save("p", []byte(base+strings.Repeat("# edit\n", i)), "save"); err != nil {
			t.Fatal(err)
		}
	}
	current, _ := s.ProjectDefinition("p")
	if vs, _ := s.projects.Versions("p"); len(vs) != 8 {
		t.Fatalf("%d versions", len(vs))
	}
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/p/storage/clean", map[string]any{"parts": []string{"backups"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	vs, _ := s.projects.Versions("p")
	if len(vs) != 1 || vs[0].Version != 8 {
		t.Fatalf("the newest stays: %+v", vs)
	}
	after, _ := s.ProjectDefinition("p")
	if string(after) != string(current) {
		t.Fatal("the definition is unchanged")
	}
}

func TestStorage_TheListShowsTheKeptSizeAndMeasuringOneProjectUpdatesIt(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "p")
	sizeOf := func() int64 {
		var body struct {
			Projects []struct {
				ID    string `json:"id"`
				Total int64  `json:"total_bytes"`
			} `json:"projects"`
		}
		if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/storage", nil); rec.Code != 200 {
			t.Fatalf("%d", rec.Code)
		} else if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		for _, p := range body.Projects {
			if p.ID == "p" {
				return p.Total
			}
		}
		t.Fatal("p is not listed")
		return 0
	}
	small := sizeOf()
	// something writes a big file behind the server's back: the list does not walk the folder, so it does not know yet
	put(t, projects, "p", "out/final.mp4", 5000)
	if got := sizeOf(); got != small {
		t.Fatalf("the list shows the size kept with the project (%d), not the disk (%d)", small, got)
	}
	// asking for the storage of the project measures it and keeps the value
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/projects/p/storage", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"total_bytes":`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if got := sizeOf(); got < small+5000 {
		t.Fatalf("after measuring: %d", got)
	}
}

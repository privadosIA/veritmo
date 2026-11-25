package music

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"veritmo/internal/project/export"
)

func TestSongDelete_RemovesTheFileAndItsManifestEntryOnly(t *testing.T) {
	s, _ := newProjectsServer(t)
	songProject(t, s, "one", "", [2]any{"a", int64(7)}, [2]any{"b", int64(8)})
	if rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/projects/one/songs/ballad/0", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	dir := filepath.Join(s.Workspace.ProjectDir("one"), "out", "one")
	if _, err := os.Stat(filepath.Join(dir, "a.mp3")); !os.IsNotExist(err) {
		t.Fatal("the audio is still there")
	}
	if _, err := os.Stat(filepath.Join(dir, "b.mp3")); err != nil {
		t.Fatal("the other song was removed")
	}
	m, err := export.ReadMusicManifest(filepath.Join(dir, "manifest.json"))
	if err != nil || len(m.Tracks) != 1 || m.Tracks[0].Variant != 1 {
		t.Fatalf("manifest = %+v, %v", m.Tracks, err)
	}
	if lib := getLibrary(t, s); len(lib.Songs) != 1 {
		t.Fatalf("library = %d songs", len(lib.Songs))
	}
}

func TestSongDelete_TakesItsStarsAndItsPlaceInThePlaylistsWithIt(t *testing.T) {
	s, _ := newProjectsServer(t)
	songProject(t, s, "one", "", [2]any{"a", int64(7)}, [2]any{"b", int64(8)}, [2]any{"c", int64(9)})
	put := map[string]any{
		"ratings":   map[string]int{"one/ballad/7": 5, "one/ballad/8": 4},
		"playlists": []map[string]any{{"id": "mix", "name": "Mix", "songs": []string{"one/ballad/7", "one/ballad/8", "one/ballad/9"}}},
	}
	if rec := doJSON(t, s.Handler(), http.MethodPut, "/api/library/meta", put); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/projects/one/songs/ballad/1", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	lib := getLibrary(t, s)
	if len(lib.Songs) != 2 || len(lib.Ratings) != 1 || lib.Ratings["one/ballad/7"] != 5 {
		t.Fatalf("library = %+v", lib)
	}
	if got := lib.Playlists[0].Songs; len(got) != 2 || got[0] != "one/ballad/7" || got[1] != "one/ballad/9" {
		t.Fatalf("playlist = %v", got)
	}
}

func TestSongDelete_UnknownSongAndBadVariant(t *testing.T) {
	s, _ := newProjectsServer(t)
	songProject(t, s, "one", "", [2]any{"a", int64(7)})
	for path, want := range map[string]int{
		"/api/projects/one/songs/ballad/5":  http.StatusNotFound,
		"/api/projects/one/songs/other/0":   http.StatusNotFound,
		"/api/projects/one/songs/ballad/-1": http.StatusBadRequest,
		"/api/projects/one/songs/ballad/x":  http.StatusBadRequest,
		"/api/projects/nope/songs/ballad/0": http.StatusNotFound,
	} {
		if rec := doJSON(t, s.Handler(), http.MethodDelete, path, nil); rec.Code != want {
			t.Errorf("%s: %d, want %d", path, rec.Code, want)
		}
	}
}

func TestSongDelete_NotAMusicProject(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "video")
	if rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/projects/video/songs/ballad/0", nil); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d", rec.Code)
	}
}

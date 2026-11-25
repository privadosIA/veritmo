package workspace

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/music/audiomeasure"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/export"
)

// songProject writes a music project with a manifest listing the given songs (track, seed) and the audio of
// those whose file should exist.
func songProject(t *testing.T, s *testServer, id string, favorite string, songs ...[2]any) {
	t.Helper()
	createProject(t, s, id)
	y := "project: " + id + "\noutput:\n  mode: music\ntracks:\n  - id: ballad\n    prompt: p\n"
	if favorite != "" {
		y += "favorites:\n  - { track: ballad, seed: " + favorite + " }\n"
	}
	if err := s.SaveProjectDefinition(id, []byte(y), "save"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.ProjectDir(id), "out", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	m := export.MusicManifest{Project: id, Mode: "music", GeneratedAt: "2026-10-13T10:00:00Z"}
	for i, sg := range songs {
		file := filepath.Join("out", id, sg[0].(string)+".mp3")
		if sg[1].(int64) >= 0 {
			os.WriteFile(filepath.Join(s.ProjectDir(id), file), []byte("mp3"), 0o644)
		}
		seed := sg[1].(int64)
		if seed < 0 {
			seed = -seed
		}
		m.Tracks = append(m.Tracks, export.MusicEntry{TrackID: "ballad", Variant: i, EffectiveSeed: seed, Title: "Song " + sg[0].(string), Caption: "pop", File: file, AudioSeconds: 120})
	}
	if err := export.WriteMusicManifest(filepath.Join(dir, "manifest.json"), m); err != nil {
		t.Fatal(err)
	}
	// what the end of a run does: the library follows what the run exported
	if _, err := s.SyncLibrary(context.Background(), id); err != nil {
		t.Fatal(err)
	}
}

func getLibrary(t *testing.T, s *testServer) LibraryResponse {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/library", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("library: %d %s", rec.Code, rec.Body.String())
	}
	var out LibraryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestLibrary_ListsTheSongsOfEveryMusicProjectThatAreStillOnDisk(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "video") // not a music project
	songProject(t, s, "one", "7", [2]any{"a", int64(7)}, [2]any{"b", int64(8)}, [2]any{"gone", int64(-9)})
	songProject(t, s, "two", "", [2]any{"c", int64(1)})
	lib := getLibrary(t, s)
	var keys []string
	for _, sg := range lib.Songs {
		keys = append(keys, sg.Key)
	}
	if got := strings.Join(keys, ","); got != "one/ballad/7,one/ballad/8,two/ballad/1" {
		t.Fatalf("keys = %s", got)
	}
	if !lib.Songs[0].Favorite || lib.Songs[1].Favorite {
		t.Fatalf("favorites = %v %v", lib.Songs[0].Favorite, lib.Songs[1].Favorite)
	}
	if lib.Songs[0].Title != "Song a" || lib.Songs[0].Seconds != 120 {
		t.Fatalf("song = %+v", lib.Songs[0])
	}
	if len(lib.Ratings) != 0 || len(lib.Playlists) != 0 {
		t.Fatalf("meta = %+v", lib.libraryMeta)
	}
}

func TestLibrary_RatingsAndPlaylistsArePersisted(t *testing.T) {
	s, projects := newProjectsServer(t)
	songProject(t, s, "one", "", [2]any{"a", int64(7)})
	songProject(t, s, "two", "", [2]any{"c", int64(1)})
	body := map[string]any{
		"ratings":   map[string]int{"one/ballad/7": 5},
		"playlists": []map[string]any{{"id": "running", "name": " Para correr ", "songs": []string{"one/ballad/7", "two/ballad/1"}}},
	}
	if rec := doJSON(t, s.Handler(), http.MethodPut, "/api/library/meta", body); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	lib := getLibrary(t, s)
	if lib.Ratings["one/ballad/7"] != 5 || len(lib.Playlists) != 1 || lib.Playlists[0].Name != "Para correr" || len(lib.Playlists[0].Songs) != 2 {
		t.Fatalf("meta = %+v", lib.libraryMeta)
	}
	// they are kept in the database, not in a file next to the projects
	if _, err := os.Stat(filepath.Join(projects, libraryMetaFile)); err == nil {
		t.Fatal("the metadata was written to a file")
	}
	// and the other PUT replaces them: no stars, the playlist reordered
	body = map[string]any{"playlists": []map[string]any{{"id": "running", "name": "Para correr", "songs": []string{"two/ballad/1", "one/ballad/7"}}}}
	if rec := doJSON(t, s.Handler(), http.MethodPut, "/api/library/meta", body); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	lib = getLibrary(t, s)
	if len(lib.Ratings) != 0 || strings.Join(lib.Playlists[0].Songs, ",") != "two/ballad/1,one/ballad/7" {
		t.Fatalf("meta = %+v", lib.libraryMeta)
	}
	// the dotfile is not a project
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/projects", nil)
	if strings.Contains(rec.Body.String(), "library") {
		t.Fatalf("projects = %s", rec.Body.String())
	}
}

func TestLibrary_AStarOrAPlaceForASongThatIsNotInTheLibraryIsLeftOut(t *testing.T) {
	s, _ := newProjectsServer(t)
	songProject(t, s, "one", "", [2]any{"a", int64(7)})
	body := map[string]any{
		"ratings":   map[string]int{"one/ballad/7": 4, "gone/ballad/1": 5},
		"playlists": []map[string]any{{"id": "mix", "name": "Mix", "songs": []string{"gone/ballad/1", "one/ballad/7"}}},
	}
	rec := doJSON(t, s.Handler(), http.MethodPut, "/api/library/meta", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	var kept libraryMeta
	if err := json.Unmarshal(rec.Body.Bytes(), &kept); err != nil {
		t.Fatal(err)
	}
	if len(kept.Ratings) != 1 || kept.Ratings["one/ballad/7"] != 4 || strings.Join(kept.Playlists[0].Songs, ",") != "one/ballad/7" {
		t.Fatalf("kept = %+v", kept)
	}
	if lib := getLibrary(t, s); len(lib.Ratings) != 1 || strings.Join(lib.Playlists[0].Songs, ",") != "one/ballad/7" {
		t.Fatalf("meta = %+v", lib.libraryMeta)
	}
}

func TestLibrary_RejectsWhatIsNotValid(t *testing.T) {
	s, _ := newProjectsServer(t)
	good := []map[string]any{{"id": "a", "name": "A", "songs": []string{}}}
	cases := map[string]map[string]any{
		"zero stars":         {"ratings": map[string]int{"p/t/1": 0}},
		"six stars":          {"ratings": map[string]int{"p/t/1": 6}},
		"bad key":            {"ratings": map[string]int{"nonsense": 3}},
		"bad playlist id":    {"playlists": []map[string]any{{"id": "Bad Id", "name": "x", "songs": []string{}}}},
		"empty name":         {"playlists": []map[string]any{{"id": "a", "name": "  ", "songs": []string{}}}},
		"repeated id":        {"playlists": append(good, good...)},
		"bad song in a list": {"playlists": []map[string]any{{"id": "a", "name": "A", "songs": []string{"nope"}}}},
	}
	for name, body := range cases {
		if rec := doJSON(t, s.Handler(), http.MethodPut, "/api/library/meta", body); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want 422: %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestLibrary_SkipsTheSongsOfATrackThatIsNoLongerInTheProject(t *testing.T) {
	s, _ := newProjectsServer(t)
	songProject(t, s, "one", "", [2]any{"a", int64(7)})
	// The track is deleted from the project; its audio and its manifest entry stay behind.
	y := "project: one\noutput:\n  mode: music\ntracks:\n  - id: other\n    prompt: p\n"
	if err := s.SaveProjectDefinition("one", []byte(y), "save"); err != nil {
		t.Fatal(err)
	}
	if lib := getLibrary(t, s); len(lib.Songs) != 0 {
		t.Fatalf("songs of a deleted track listed: %+v", lib.Songs)
	}
}

func TestLibrary_TellsTheGeneratorOfEachSong(t *testing.T) {
	for model, want := range map[string]string{"heartmula": "heartmula", "diffrhythm2": "diffrhythm2", "acestep-v15-turbo-Q8_0.gguf": "ace-step", "": "ace-step"} {
		if got := songGenerator(model); got != want {
			t.Errorf("songGenerator(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestLibrary_ASongSaysHowLongItsSongMapIsAndItIsMeasuredOnce(t *testing.T) {
	s := newTestServer(t)
	s.look = func(name string) (string, error) { return "/fake/" + name, nil }
	calls := 0
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		calls++
		// 135.04 s of mono audio at the rate of the song map, four bytes a sample.
		return runner.ScriptedResult{Stdout: make([]byte, 4*int(135.04*float64(audiomeasure.Rate)))}
	})
	s.mediaRunner = fr
	songProject(t, s, "ballad", "", [2]any{"one", int64(7)})
	for range 2 {
		songs := getLibrary(t, s).Songs
		if len(songs) != 1 || songs[0].Duration != 135.04 || songs[0].Seconds != 120 {
			t.Fatalf("%+v", songs)
		}
	}
	// a sync of an audio that did not change does not decode it again
	if _, err := s.SyncLibrary(context.Background(), "ballad"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("decoded %d times, want once", calls)
	}
	// Without ffmpeg the song is listed all the same, with no duration.
	s.look = func(string) (string, error) { return "", os.ErrNotExist }
	songProject(t, s, "other", "", [2]any{"two", int64(8)})
	for _, sg := range getLibrary(t, s).Songs {
		if sg.Project == "other" && sg.Duration != 0 {
			t.Fatalf("%+v", sg)
		}
	}
}

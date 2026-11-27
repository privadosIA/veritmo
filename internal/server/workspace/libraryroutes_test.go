package workspace

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	return out
}

func keysOf(songs []songView) string {
	var keys []string
	for _, s := range songs {
		keys = append(keys, s.Key)
	}
	return strings.Join(keys, ",")
}

func getSongs(t *testing.T, s *testServer, query string) ([]songView, *httptest.ResponseRecorder) {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/library/songs"+query, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("songs%s: %d %s", query, rec.Code, rec.Body.String())
	}
	return decode[[]songView](t, rec), rec
}

func rateSong(t *testing.T, s *testServer, key string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return doJSON(t, s.Handler(), http.MethodPut, "/api/library/songs/"+strings.ReplaceAll(key, "/", "%2F")+"/rating", body)
}

// libraryOf makes two music projects: demo (songs a, b and c; b is a favourite) and other (song d).
func libraryOf(t *testing.T) *testServer {
	s, _ := newProjectsServer(t)
	songProject(t, s, "demo", "8", [2]any{"a", int64(7)}, [2]any{"b", int64(8)}, [2]any{"c", int64(9)})
	songProject(t, s, "other", "", [2]any{"d", int64(1)})
	return s
}

func TestLibrary_TheListingDoesNotTouchTheDisk(t *testing.T) {
	s, projects := newProjectsServer(t)
	songProject(t, s, "one", "", [2]any{"a", int64(7)})
	if err := os.RemoveAll(projects); err != nil { // the folder cannot be read
		t.Fatal(err)
	}
	if lib := getLibrary(t, s); len(lib.Songs) != 1 || lib.Songs[0].Key != "one/ballad/7" {
		t.Fatalf("library = %+v", lib.Songs)
	}
	if songs, _ := getSongs(t, s, ""); len(songs) != 1 {
		t.Fatalf("songs = %+v", songs)
	}
}

func TestLibrarySongs_TheBestOfAProject(t *testing.T) {
	s := libraryOf(t)
	for key, stars := range map[string]int{"demo/ballad/7": 3, "demo/ballad/8": 4, "demo/ballad/9": 5, "other/ballad/1": 5} {
		if rec := rateSong(t, s, key, map[string]int{"stars": stars}); rec.Code != http.StatusOK {
			t.Fatalf("rate %s: %d %s", key, rec.Code, rec.Body.String())
		}
	}
	songs, _ := getSongs(t, s, "?project=demo&min_rating=4&sort=-rating")
	if got := keysOf(songs); got != "demo/ballad/9,demo/ballad/8" || songs[0].Rating != 5 || songs[1].Rating != 4 {
		t.Fatalf("songs = %s %+v", got, songs)
	}
}

func TestLibrarySongs_PagesSearchFiltersAndOrders(t *testing.T) {
	s := libraryOf(t)
	all, rec := getSongs(t, s, "")
	if len(all) != 4 || rec.Header().Get("X-Total-Count") != "" {
		t.Fatalf("an unpaged list answers everything with no header: %d %q", len(all), rec.Header().Get("X-Total-Count"))
	}
	// the page: two songs, the total of all of them in the header
	page, rec := getSongs(t, s, "?limit=2&offset=1&sort=title")
	if keysOf(page) != "demo/ballad/8,demo/ballad/9" || rec.Header().Get("X-Total-Count") != "4" {
		t.Fatalf("page = %s, total %q", keysOf(page), rec.Header().Get("X-Total-Count"))
	}
	if got, _ := getSongs(t, s, "?q=SONG+d"); keysOf(got) != "other/ballad/1" {
		t.Fatalf("search = %s", keysOf(got))
	}
	if got, _ := getSongs(t, s, "?favorite=true"); keysOf(got) != "demo/ballad/8" || !got[0].Favorite {
		t.Fatalf("favourites = %s", keysOf(got))
	}
	if got, _ := getSongs(t, s, "?client=embedded&project=other"); keysOf(got) != "other/ballad/1" {
		t.Fatalf("client = %s", keysOf(got))
	}
	if got, _ := getSongs(t, s, "?client=lab"); len(got) != 0 {
		t.Fatalf("client lab = %s", keysOf(got))
	}
	// two of the songs were made by the services: the generator filters them
	for key, gen := range map[string]string{"demo/ballad/8": "heartmula", "other/ballad/1": "diffrhythm2"} {
		if err := s.songs.db.DB.Model(&SongRow{}).Where("song_key = ?", key).Update("model", gen).Error; err != nil {
			t.Fatal(err)
		}
	}
	// the generator: the songs it made, counted in the total
	for gen, want := range map[string]string{"ace-step": "demo/ballad/7,demo/ballad/9", "heartmula": "demo/ballad/8", "diffrhythm2": "other/ballad/1"} {
		got, rec := getSongs(t, s, "?generator="+gen+"&sort=title&limit=10")
		if keysOf(got) != want || rec.Header().Get("X-Total-Count") != strconv.Itoa(len(strings.Split(want, ","))) {
			t.Errorf("generator %s: %s, total %q, want %s", gen, keysOf(got), rec.Header().Get("X-Total-Count"), want)
		}
	}
	if got, _ := getSongs(t, s, "?generator=diffrhythm2&project=other"); keysOf(got) != "other/ballad/1" {
		t.Errorf("generator and project together: %s", keysOf(got))
	}
	if got, _ := getSongs(t, s, "?generator=heartmula&project=other"); len(got) != 0 {
		t.Errorf("another generator in that project: %s", keysOf(got))
	}
	for _, bad := range []string{"?generator=bogus", "?generator=ACE-STEP", "?sort=bogus", "?limit=0", "?min_rating=6", "?min_rating=x", "?favorite=no", "?managed=everything", "?client=Bad%20Client"} {
		if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/library/songs"+bad, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", bad, rec.Code)
		}
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/library/songs?playlist=nope", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown playlist: %d, want 404", rec.Code)
	}
}

func TestLibrarySongs_APlaylistComesInItsOrderUnlessSorted(t *testing.T) {
	s := libraryOf(t)
	body := map[string]any{"id": "night", "name": "Night", "songs": []string{"other/ballad/1", "demo/ballad/9", "demo/ballad/7"}}
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/library/playlists", body); rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if got, rec := getSongs(t, s, "?playlist=night"); keysOf(got) != "other/ballad/1,demo/ballad/9,demo/ballad/7" || rec.Header().Get("X-Total-Count") != "" {
		t.Fatalf("in order = %s", keysOf(got))
	}
	if got, _ := getSongs(t, s, "?playlist=night&sort=title"); keysOf(got) != "demo/ballad/7,demo/ballad/9,other/ballad/1" {
		t.Fatalf("sorted = %s", keysOf(got))
	}
	if got, _ := getSongs(t, s, "?playlist=night&limit=1&offset=1"); keysOf(got) != "demo/ballad/9" {
		t.Fatalf("paged = %s", keysOf(got))
	}
}

func TestLibrarySong_ReadAndRatedOneByOne(t *testing.T) {
	s := libraryOf(t)
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/library/songs/demo%2Fballad%2F7", nil)
	if one := decode[songView](t, rec); rec.Code != http.StatusOK || one.Key != "demo/ballad/7" || one.Title != "Song a" || one.Rating != 0 {
		t.Fatalf("%d %+v", rec.Code, one)
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/library/songs/demo%2Fballad%2F99", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown song: %d", rec.Code)
	}
	// one rating changes that song only
	_ = rateSong(t, s, "other/ballad/1", map[string]int{"stars": 2})
	if rec := rateSong(t, s, "demo/ballad/7", map[string]int{"stars": 5}); rec.Code != http.StatusOK || decode[songView](t, rec).Rating != 5 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	lib := getLibrary(t, s)
	if len(lib.Ratings) != 2 || lib.Ratings["demo/ballad/7"] != 5 || lib.Ratings["other/ballad/1"] != 2 {
		t.Fatalf("ratings = %v", lib.Ratings)
	}
	// 0 removes the stars
	if rec := rateSong(t, s, "demo/ballad/7", map[string]int{"stars": 0}); rec.Code != http.StatusOK {
		t.Fatalf("%d", rec.Code)
	}
	if lib := getLibrary(t, s); len(lib.Ratings) != 1 {
		t.Fatalf("ratings = %v", lib.Ratings)
	}
	for name, body := range map[string]any{"six": map[string]int{"stars": 6}, "negative": map[string]int{"stars": -1}, "none": map[string]int{}, "text": map[string]string{"stars": "5"}} {
		want := http.StatusUnprocessableEntity
		if name == "text" {
			want = http.StatusBadRequest
		}
		if rec := rateSong(t, s, "demo/ballad/7", body); rec.Code != want {
			t.Errorf("%s: %d, want %d", name, rec.Code, want)
		}
	}
	if rec := rateSong(t, s, "demo/ballad/99", map[string]int{"stars": 3}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown song: %d", rec.Code)
	}
}

func TestLibrary_AnAudioRemovedByHandIsGoneAfterARescan(t *testing.T) {
	s := libraryOf(t)
	_ = rateSong(t, s, "demo/ballad/7", map[string]int{"stars": 5})
	if err := os.Remove(filepath.Join(s.ProjectDir("demo"), "out", "demo", "a.mp3")); err != nil {
		t.Fatal(err)
	}
	if len(getLibrary(t, s).Songs) != 4 {
		t.Fatal("the listing looked at the disk")
	}
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/library/rescan", nil)
	if got := decode[map[string]int](t, rec); rec.Code != http.StatusOK || got["removed"] != 1 || got["added"] != 0 || got["updated"] != 0 {
		t.Fatalf("%d %v", rec.Code, got)
	}
	lib := getLibrary(t, s)
	if len(lib.Songs) != 3 || len(lib.Ratings) != 0 {
		t.Fatalf("library = %+v", lib)
	}
	// audio put back by hand is added
	if err := os.WriteFile(filepath.Join(s.ProjectDir("demo"), "out", "demo", "a.mp3"), []byte("mp3"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := decode[map[string]int](t, doJSON(t, s.Handler(), http.MethodPost, "/api/library/rescan", nil)); got["added"] != 1 {
		t.Fatalf("%v", got)
	}
}

func createPlaylist(t *testing.T, s *testServer, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	return doJSON(t, s.Handler(), http.MethodPost, "/api/library/playlists", body)
}

func TestLibraryPlaylists_ANewOneGetsItsIdFromItsName(t *testing.T) {
	s := libraryOf(t)
	rec := createPlaylist(t, s, map[string]any{"name": "Night drive", "songs": []string{"demo/ballad/9", "demo/ballad/7"}})
	if p := decode[libraryPlaylist](t, rec); rec.Code != http.StatusCreated || p.ID != "night-drive" || strings.Join(p.Songs, ",") != "demo/ballad/9,demo/ballad/7" {
		t.Fatalf("%d %+v", rec.Code, p)
	}
	if rec := createPlaylist(t, s, map[string]any{"name": "Música ñoña!", "id": "mine"}); rec.Code != http.StatusCreated || decode[libraryPlaylist](t, rec).Songs == nil {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	list := decode[[]playlistSummary](t, doJSON(t, s.Handler(), http.MethodGet, "/api/library/playlists", nil))
	if len(list) != 2 || list[0] != (playlistSummary{ID: "night-drive", Name: "Night drive", Songs: 2}) || list[1].ID != "mine" || list[1].Songs != 0 {
		t.Fatalf("list = %+v", list)
	}
	if got := playlistID("Música ñoña!"); got != "musica-nona" {
		t.Fatalf("id = %q", got)
	}
}

func TestLibraryPlaylists_ASongThatDoesNotExistIsRefusedAndNothingChanges(t *testing.T) {
	s := libraryOf(t)
	_ = createPlaylist(t, s, map[string]any{"id": "a", "name": "A", "songs": []string{"demo/ballad/7"}})
	rec := createPlaylist(t, s, map[string]any{"id": "b", "name": "B", "songs": []string{"demo/ballad/7", "nope/x/1"}})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "nope/x/1") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/library/playlists/b", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("a refused playlist exists: %d", rec.Code)
	}
	rec = doJSON(t, s.Handler(), http.MethodPut, "/api/library/playlists/a", map[string]any{"name": "A2", "songs": []string{"gone/x/1"}})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "gone/x/1") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if p := decode[libraryPlaylist](t, doJSON(t, s.Handler(), http.MethodGet, "/api/library/playlists/a", nil)); p.Name != "A" || len(p.Songs) != 1 {
		t.Fatalf("the playlist changed: %+v", p)
	}
}

func TestLibraryPlaylists_ReplaceDeleteAndTheirErrors(t *testing.T) {
	s := libraryOf(t)
	_ = createPlaylist(t, s, map[string]any{"id": "a", "name": "A"})
	if rec := createPlaylist(t, s, map[string]any{"id": "a", "name": "Again"}); rec.Code != http.StatusConflict {
		t.Fatalf("repeated id: %d", rec.Code)
	}
	rec := doJSON(t, s.Handler(), http.MethodPut, "/api/library/playlists/a", map[string]any{"name": " Renamed ", "songs": []string{"other/ballad/1", "demo/ballad/7"}})
	if p := decode[libraryPlaylist](t, rec); rec.Code != http.StatusOK || p.Name != "Renamed" || strings.Join(p.Songs, ",") != "other/ballad/1,demo/ballad/7" {
		t.Fatalf("%d %+v", rec.Code, p)
	}
	if p := decode[libraryPlaylist](t, doJSON(t, s.Handler(), http.MethodGet, "/api/library/playlists/a", nil)); strings.Join(p.Songs, ",") != "other/ballad/1,demo/ballad/7" {
		t.Fatalf("%+v", p)
	}
	long := strings.Repeat("x", maxPlaylistName+1)
	for name, c := range map[string]struct {
		method, path string
		body         map[string]any
		want         int
	}{
		"empty name":      {http.MethodPost, "/api/library/playlists", map[string]any{"id": "z", "name": "  "}, 422},
		"long name":       {http.MethodPost, "/api/library/playlists", map[string]any{"id": "z", "name": long}, 422},
		"bad id":          {http.MethodPost, "/api/library/playlists", map[string]any{"id": "Bad Id", "name": "Z"}, 422},
		"no id from name": {http.MethodPost, "/api/library/playlists", map[string]any{"name": "!!!"}, 422},
		"put long name":   {http.MethodPut, "/api/library/playlists/a", map[string]any{"name": long}, 422},
		"put unknown":     {http.MethodPut, "/api/library/playlists/nope", map[string]any{"name": "N"}, 404},
		"get unknown":     {http.MethodGet, "/api/library/playlists/nope", nil, 404},
		"delete unknown":  {http.MethodDelete, "/api/library/playlists/nope", nil, 404},
	} {
		if rec := doJSON(t, s.Handler(), c.method, c.path, c.body); rec.Code != c.want {
			t.Errorf("%s: %d, want %d: %s", name, rec.Code, c.want, rec.Body.String())
		}
	}
	if rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/library/playlists/a", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/library/playlists/a", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("after delete: %d", rec.Code)
	}
}

func TestLibraryPlaylists_TooManyPlaylistsAndTooManySongs(t *testing.T) {
	s := libraryOf(t)
	var lists []map[string]any
	for i := range maxPlaylists {
		lists = append(lists, map[string]any{"id": fmt.Sprintf("p%d", i), "name": "P", "songs": []string{}})
	}
	if rec := doJSON(t, s.Handler(), http.MethodPut, "/api/library/meta", map[string]any{"playlists": lists}); rec.Code != http.StatusOK {
		t.Fatalf("meta: %d %s", rec.Code, rec.Body.String())
	}
	if rec := createPlaylist(t, s, map[string]any{"id": "one-more", "name": "One more"}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("201st playlist: %d", rec.Code)
	}
	many := make([]string, maxPlaylistSongs+1)
	for i := range many {
		many[i] = "demo/ballad/7"
	}
	if rec := doJSON(t, s.Handler(), http.MethodPut, "/api/library/playlists/p0", map[string]any{"name": "P", "songs": many}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("too many songs: %d", rec.Code)
	}
}

func TestLibrary_WhatTheContentModeHidesIsNotListedNotReadAndNotErased(t *testing.T) {
	s := libraryOf(t)
	_ = createPlaylist(t, s, map[string]any{"id": "mix", "name": "Mix", "songs": []string{"demo/ballad/7", "other/ballad/1", "demo/ballad/8"}})
	_ = rateSong(t, s, "other/ballad/1", map[string]int{"stars": 4})
	s.hidden["other"] = true
	if got, _ := getSongs(t, s, ""); keysOf(got) == "" || strings.Contains(keysOf(got), "other") {
		t.Fatalf("songs = %s", keysOf(got))
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/library/songs/other%2Fballad%2F1", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("hidden song: %d", rec.Code)
	}
	if rec := rateSong(t, s, "other/ballad/1", map[string]int{"stars": 1}); rec.Code != http.StatusNotFound {
		t.Fatalf("rating a hidden song: %d", rec.Code)
	}
	if rec := createPlaylist(t, s, map[string]any{"id": "x", "name": "X", "songs": []string{"other/ballad/1"}}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a hidden song in a playlist: %d", rec.Code)
	}
	if p := decode[libraryPlaylist](t, doJSON(t, s.Handler(), http.MethodGet, "/api/library/playlists/mix", nil)); strings.Join(p.Songs, ",") != "demo/ballad/7,demo/ballad/8" {
		t.Fatalf("playlist = %+v", p)
	}
	if list := decode[[]playlistSummary](t, doJSON(t, s.Handler(), http.MethodGet, "/api/library/playlists", nil)); list[0].Songs != 2 {
		t.Fatalf("list = %+v", list)
	}
	// what the client saves does not erase what it never saw: the hidden song stays in the playlist and keeps its stars
	_ = doJSON(t, s.Handler(), http.MethodPut, "/api/library/playlists/mix", map[string]any{"name": "Mix", "songs": []string{"demo/ballad/8"}})
	_ = doJSON(t, s.Handler(), http.MethodPut, "/api/library/meta", map[string]any{
		"ratings":   map[string]int{},
		"playlists": []map[string]any{{"id": "mix", "name": "Mix", "songs": []string{"demo/ballad/8"}}},
	})
	delete(s.hidden, "other")
	if p := decode[libraryPlaylist](t, doJSON(t, s.Handler(), http.MethodGet, "/api/library/playlists/mix", nil)); strings.Join(p.Songs, ",") != "demo/ballad/8,other/ballad/1" {
		t.Fatalf("playlist = %+v", p)
	}
	if lib := getLibrary(t, s); lib.Ratings["other/ballad/1"] != 4 {
		t.Fatalf("ratings = %v", lib.Ratings)
	}
}

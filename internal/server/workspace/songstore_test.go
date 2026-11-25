package workspace

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"veritmo/internal/platform/store"
	"veritmo/internal/project/export"
)

func memSongs(t *testing.T) *songStore {
	t.Helper()
	reg := store.NewRegistry()
	RegisterStore(reg)
	st, err := store.OpenMemory(reg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	return newSongStore(st)
}

// source is a song of project p whose audio file exists in dir.
func source(t *testing.T, dir, p, track string, seed int64, title string) songSource {
	t.Helper()
	path := filepath.Join(dir, p+"-"+track+".mp3")
	if err := os.WriteFile(path, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	return songSource{path: path, createdAt: time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC), song: librarySong{
		Key: libraryKey(p, track, seed), Project: p, TrackID: track, Seed: seed, Title: title, File: "out/" + p + "/" + track + ".mp3", Generator: "ace-step", Client: "lab",
	}}
}

func TestSongStore_SyncAddsUpdatesKeepingTheStarsAndRemoves(t *testing.T) {
	ss := memSongs(t)
	dir := t.TempDir()
	a, b := source(t, dir, "one", "a", 1, "A"), source(t, dir, "one", "b", 2, "B")
	rep, err := ss.SyncProject(context.Background(), "one", []songSource{a, b})
	if err != nil || rep != (SyncReport{Added: 2}) {
		t.Fatalf("%+v %v", rep, err)
	}
	if err := ss.SetRating(a.song.Key, 5); err != nil {
		t.Fatal(err)
	}
	// the same list changes nothing
	if rep, _ := ss.SyncProject(context.Background(), "one", []songSource{a, b}); rep != (SyncReport{}) {
		t.Fatalf("%+v", rep)
	}
	// a new title updates one song and its stars stay; a song left out is removed
	a.song.Title = "A renamed"
	rep, err = ss.SyncProject(context.Background(), "one", []songSource{a})
	if err != nil || rep != (SyncReport{Updated: 1, Removed: 1}) {
		t.Fatalf("%+v %v", rep, err)
	}
	row, err := ss.Get(a.song.Key)
	if err != nil || row.Title != "A renamed" || row.Rating != 5 {
		t.Fatalf("%+v %v", row, err)
	}
	if _, err := ss.Get(b.song.Key); !errors.Is(err, ErrNoSong) {
		t.Fatalf("%v", err)
	}
	// another project is not touched, and an empty list removes the rows of the project
	c := source(t, dir, "two", "c", 3, "C")
	if _, err := ss.SyncProject(context.Background(), "two", []songSource{c}); err != nil {
		t.Fatal(err)
	}
	rep, _ = ss.SyncProject(context.Background(), "one", nil)
	if rep.Removed != 1 {
		t.Fatalf("%+v", rep)
	}
	if rows, _ := ss.All(); len(rows) != 1 || rows[0].Key != c.song.Key {
		t.Fatalf("%+v", rows)
	}
}

func TestSongStore_RemovingASongClosesTheGapInItsPlaylists(t *testing.T) {
	ss := memSongs(t)
	dir := t.TempDir()
	songs := []songSource{source(t, dir, "p", "a", 1, "A"), source(t, dir, "p", "b", 2, "B"), source(t, dir, "p", "c", 3, "C"), source(t, dir, "q", "d", 4, "D")}
	for _, p := range []string{"p", "q"} {
		var mine []songSource
		for _, s := range songs {
			if s.song.Project == p {
				mine = append(mine, s)
			}
		}
		if _, err := ss.SyncProject(context.Background(), p, mine); err != nil {
			t.Fatal(err)
		}
	}
	keys := func(i ...int) []string {
		var out []string
		for _, n := range i {
			out = append(out, songs[n].song.Key)
		}
		return out
	}
	if err := ss.CreatePlaylist(libraryPlaylist{ID: "mix", Name: "Mix", Songs: keys(0, 3, 1, 2)}); err != nil {
		t.Fatal(err)
	}
	if err := ss.CreatePlaylist(libraryPlaylist{ID: "other", Name: "Other", Songs: keys(1)}); err != nil {
		t.Fatal(err)
	}
	// song b leaves the manifest of p
	if _, err := ss.SyncProject(context.Background(), "p", []songSource{songs[0], songs[2]}); err != nil {
		t.Fatal(err)
	}
	mix, _ := ss.Playlist("mix")
	if got := strings.Join(mix.Songs, ","); got != strings.Join(keys(0, 3, 2), ",") {
		t.Fatalf("mix = %s", got)
	}
	if other, _ := ss.Playlist("other"); len(other.Songs) != 0 {
		t.Fatalf("other = %+v", other)
	}
	// the places are 0..n-1 with no gap, so the next change does not collide
	var places []PlaylistSong
	ss.db.DB.Where("playlist_id = ?", "mix").Order("position").Find(&places)
	for i, p := range places {
		if p.Position != i {
			t.Fatalf("places = %+v", places)
		}
	}
	// a project deleted takes its songs out of the playlists, in the transaction of the delete
	if err := ss.db.Tx(func(tx *store.Store) error { return ss.deleteProject(tx, "q") }); err != nil {
		t.Fatal(err)
	}
	if mix, _ = ss.Playlist("mix"); strings.Join(mix.Songs, ",") != strings.Join(keys(0, 2), ",") {
		t.Fatalf("mix = %+v", mix)
	}
}

func TestSongStore_AnAudioThatDidNotChangeIsNotMeasuredAgain(t *testing.T) {
	ss := memSongs(t)
	calls := 0
	ss.measure = func(_ context.Context, _ string) float64 { calls++; return 61.5 }
	dir := t.TempDir()
	a := source(t, dir, "one", "a", 1, "A")
	for range 3 {
		if _, err := ss.SyncProject(context.Background(), "one", []songSource{a}); err != nil {
			t.Fatal(err)
		}
	}
	if row, _ := ss.Get(a.song.Key); calls != 1 || row.Duration != 61.5 {
		t.Fatalf("calls = %d, row = %+v", calls, row)
	}
	// the file changes (another size): it is measured once more
	if err := os.WriteFile(a.path, []byte("a longer audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ss.SyncProject(context.Background(), "one", []songSource{a}); err != nil || calls != 2 {
		t.Fatalf("calls = %d, err = %v", calls, err)
	}
	// a song that could not be measured (0) is tried again at the next sync
	ss.measure = func(_ context.Context, _ string) float64 { calls++; return 0 }
	b := source(t, dir, "one", "b", 2, "B")
	for range 2 {
		_, _ = ss.SyncProject(context.Background(), "one", []songSource{a, b})
	}
	if calls != 4 {
		t.Fatalf("calls = %d, want 4 (the new audio measured twice)", calls)
	}
}

func TestSongStore_ReplaceMetaIsOneChangeAndLeavesOutWhatIsNotASong(t *testing.T) {
	ss := memSongs(t)
	dir := t.TempDir()
	a, b := source(t, dir, "one", "a", 1, "A"), source(t, dir, "one", "b", 2, "B")
	_, _ = ss.SyncProject(context.Background(), "one", []songSource{a, b})
	_ = ss.SetRating(b.song.Key, 2)
	kept, err := ss.ReplaceMeta(libraryMeta{
		Ratings:   map[string]int{a.song.Key: 4, "x/y/9": 3},
		Playlists: []libraryPlaylist{{ID: "p1", Name: "P1", Songs: []string{b.song.Key, "x/y/9", a.song.Key}}, {ID: "p2", Name: "P2", Songs: []string{}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(kept.Ratings) != 1 || strings.Join(kept.Playlists[0].Songs, ",") != b.song.Key+","+a.song.Key {
		t.Fatalf("kept = %+v", kept)
	}
	m, _ := ss.Meta()
	if m.Ratings[a.song.Key] != 4 || m.Ratings[b.song.Key] != 0 || len(m.Playlists) != 2 || m.Playlists[0].ID != "p1" || m.Playlists[1].Songs == nil {
		t.Fatalf("meta = %+v", m)
	}
}

func TestSongStore_Playlists(t *testing.T) {
	ss := memSongs(t)
	dir := t.TempDir()
	a := source(t, dir, "one", "a", 1, "A")
	_, _ = ss.SyncProject(context.Background(), "one", []songSource{a})
	if err := ss.CreatePlaylist(libraryPlaylist{ID: "x", Name: "X", Songs: []string{a.song.Key}}); err != nil {
		t.Fatal(err)
	}
	if err := ss.CreatePlaylist(libraryPlaylist{ID: "x", Name: "X again"}); !errors.Is(err, ErrPlaylistExists) {
		t.Fatalf("%v", err)
	}
	var unknown *ErrUnknownSongs
	if err := ss.CreatePlaylist(libraryPlaylist{ID: "y", Name: "Y", Songs: []string{a.song.Key, "no/pe/1"}}); !errors.As(err, &unknown) || unknown.Keys[0] != "no/pe/1" {
		t.Fatalf("%v", err)
	}
	if _, err := ss.Playlist("y"); !errors.Is(err, ErrNoPlaylist) {
		t.Fatalf("a refused playlist was kept: %v", err)
	}
	if err := ss.ReplacePlaylist("x", "Renamed", nil); err != nil {
		t.Fatal(err)
	}
	if p, _ := ss.Playlist("x"); p.Name != "Renamed" || len(p.Songs) != 0 {
		t.Fatalf("%+v", p)
	}
	if err := ss.ReplacePlaylist("nope", "N", nil); !errors.Is(err, ErrNoPlaylist) {
		t.Fatalf("%v", err)
	}
	if err := ss.DeletePlaylist("x"); err != nil {
		t.Fatal(err)
	}
	if err := ss.DeletePlaylist("x"); !errors.Is(err, ErrNoPlaylist) {
		t.Fatalf("%v", err)
	}
}

// --- the library as the server keeps it (spec music-library) --------------------------------------------------

func TestLibrary_AfterARunTheNewSongsAreListedWithTheirDuration(t *testing.T) {
	s, _ := newProjectsServer(t)
	s.look = func(name string) (string, error) { return "/fake/" + name, nil }
	songProject(t, s, "one", "", [2]any{"a", int64(7)})
	if len(getLibrary(t, s).Songs) != 1 {
		t.Fatal("the first song is not listed")
	}
	// a run exports two more songs, writing the manifest and the audio; the end of the run is what the library is told about
	dir := filepath.Join(s.ProjectDir("one"), "out", "one")
	for _, name := range []string{"b", "c"} {
		if err := os.WriteFile(filepath.Join(dir, name+".mp3"), []byte("mp3"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	appendSongs(t, s, "one", [2]any{"b", int64(8)}, [2]any{"c", int64(9)})
	if len(getLibrary(t, s).Songs) != 1 {
		t.Fatal("the listing looked at the disk before the run ended")
	}
	s.rm.afterRun("one")
	if got := len(getLibrary(t, s).Songs); got != 3 {
		t.Fatalf("songs after the run = %d, want 3", got)
	}
}

func TestLibrary_ADeletedProjectTakesItsSongsOutOfThePlaylists(t *testing.T) {
	s, _ := newProjectsServer(t)
	songProject(t, s, "one", "", [2]any{"a", int64(7)}, [2]any{"b", int64(8)})
	songProject(t, s, "two", "", [2]any{"c", int64(1)}, [2]any{"d", int64(2)})
	put := map[string]any{
		"ratings":   map[string]int{"one/ballad/7": 5, "two/ballad/1": 4},
		"playlists": []map[string]any{{"id": "mix", "name": "Mix", "songs": []string{"two/ballad/1", "one/ballad/7", "two/ballad/2", "one/ballad/8"}}},
	}
	if rec := doJSON(t, s.Handler(), http.MethodPut, "/api/library/meta", put); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/projects/one", nil); rec.Code != http.StatusNoContent && rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	lib := getLibrary(t, s)
	for _, sg := range lib.Songs {
		if sg.Project == "one" {
			t.Fatalf("a song of the deleted project is listed: %+v", sg)
		}
	}
	if len(lib.Songs) != 2 || len(lib.Ratings) != 1 || lib.Ratings["two/ballad/1"] != 4 {
		t.Fatalf("library = %+v", lib)
	}
	if got := strings.Join(lib.Playlists[0].Songs, ","); got != "two/ballad/1,two/ballad/2" {
		t.Fatalf("playlist = %s", got)
	}
}

// appendSongs adds entries to the manifest of a music project, as a run that exports more songs does (their audio is the caller's).
func appendSongs(t *testing.T, s *testServer, id string, songs ...[2]any) {
	t.Helper()
	path := filepath.Join(s.ProjectDir(id), "out", id, "manifest.json")
	m, err := export.ReadMusicManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, sg := range songs {
		m.Tracks = append(m.Tracks, export.MusicEntry{TrackID: "ballad", Variant: len(m.Tracks) + i, EffectiveSeed: sg[1].(int64), Title: "Song " + sg[0].(string), File: filepath.Join("out", id, sg[0].(string)+".mp3")})
	}
	if err := export.WriteMusicManifest(path, m); err != nil {
		t.Fatal(err)
	}
}

func TestProjectsAccess_ARunOfTheCommandLineIsToldToTheLibrary(t *testing.T) {
	ps, st := memProjects(t)
	_ = ps
	dir := t.TempDir()
	access := OpenProjects(st, dir)
	access.songs.measure = func(context.Context, string) float64 { return 12.5 }
	def := "project: one\noutput: {mode: music}\ntracks:\n  - {id: ballad, prompt: p}\n"
	if err := access.Create("one", []byte(def)); err != nil {
		t.Fatal(err)
	}
	if err := access.Materialize("one"); err != nil {
		t.Fatal(err)
	}
	// the run exports a song into the folder
	writeFile(t, filepath.Join(dir, "one", "out", "one", "a.mp3"), "mp3", time.Time{})
	m := export.MusicManifest{Project: "one", Mode: "music", GeneratedAt: "2026-11-19T10:00:00Z", Tracks: []export.MusicEntry{{TrackID: "ballad", EffectiveSeed: 7, Title: "A", File: "out/one/a.mp3"}}}
	if err := export.WriteMusicManifest(filepath.Join(dir, "one", "out", "one", "manifest.json"), m); err != nil {
		t.Fatal(err)
	}
	if err := access.Ingest("one", "run"); err != nil {
		t.Fatal(err)
	}
	row, err := newSongStore(st).Get("one/ballad/7")
	if err != nil || row.Title != "A" || row.Duration != 12.5 {
		t.Fatalf("%+v %v", row, err)
	}
}

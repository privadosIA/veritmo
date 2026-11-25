package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"veritmo/internal/platform/store"
	"veritmo/internal/project/export"
)

// libraryFolders writes, in dir, two music projects with songs on disk (and one in the manifest whose audio is gone) and the file of stars and playlists.
func libraryFolders(t *testing.T, dir string) {
	t.Helper()
	def := func(project string) string {
		return "project: " + project + "\noutput: {mode: music}\ntracks:\n  - {id: ballad, prompt: p}\nfavorites:\n  - {track: ballad, seed: 7}\n"
	}
	for _, p := range []struct {
		id    string
		seeds []int64
	}{{"one", []int64{7, 8, -9}}, {"two", []int64{1}}} {
		writeFile(t, filepath.Join(dir, p.id, "scenes.yaml"), def(p.id), time.Time{})
		m := export.MusicManifest{Project: p.id, Mode: "music", GeneratedAt: "2026-10-13T10:00:00Z"}
		for i, seed := range p.seeds {
			file := filepath.Join("out", p.id, "s"+string(rune('a'+i))+".mp3")
			if seed > 0 {
				writeFile(t, filepath.Join(dir, p.id, file), "mp3", time.Time{})
			}
			if seed < 0 {
				seed = -seed
			}
			m.Tracks = append(m.Tracks, export.MusicEntry{TrackID: "ballad", Variant: i, EffectiveSeed: seed, Title: "Song", File: file, AudioSeconds: 60})
		}
		if err := export.WriteMusicManifest(filepath.Join(dir, p.id, "out", p.id, "manifest.json"), m); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(dir, libraryMetaFile), `{
  "ratings": {"one/ballad/7": 5, "two/ballad/1": 3, "gone/ballad/1": 4, "one/ballad/8": 9},
  "playlists": [
    {"id": "mix", "name": "Mix", "songs": ["two/ballad/1", "gone/ballad/1", "one/ballad/7"]},
    {"id": "empty", "name": "Empty", "songs": []}
  ]
}`, time.Time{})
}

func TestLibraryImporter_SongsStarsAndPlaylistsBecomeRows(t *testing.T) {
	dir := t.TempDir()
	libraryFolders(t, dir)
	reg := store.NewRegistry()
	RegisterStore(reg)
	RegisterProjectsImporter(reg, dir)
	measured := 0
	reg.AddImporter(LibraryImporter{Dir: dir, Measure: func(context.Context, string) float64 { measured++; return 61.25 }})
	st, err := store.OpenMemory(reg)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	recs, err := st.RunImports(context.Background(), "")
	if err != nil || len(recs) != 2 || recs[0].Entity != "projects" || recs[1].Entity != "library" || recs[1].State != store.ImportDone {
		t.Fatalf("the library is imported after the projects: %+v %v", recs, err)
	}
	// three songs (the one whose audio is gone is not a song), two playlists
	if rec := recs[1]; rec.Created != 5 {
		t.Fatalf("%+v", rec)
	}
	ss := newSongStore(st)
	rows, _ := ss.All()
	if len(rows) != 3 || measured != 3 {
		t.Fatalf("%d songs, %d measured", len(rows), measured)
	}
	one, err := ss.Get("one/ballad/7")
	if err != nil || one.Rating != 5 || !one.Favorite || one.Duration != 61.25 || one.Doc.V.Duration != 61.25 || one.Client != clientEmbedded || one.FileSize != 3 {
		t.Fatalf("%+v %v", one, err)
	}
	// a rating out of range is left out too
	if eight, _ := ss.Get("one/ballad/8"); eight.Rating != 0 {
		t.Fatalf("%+v", eight)
	}
	mix, _ := ss.Playlist("mix")
	if strings.Join(mix.Songs, ",") != "two/ballad/1,one/ballad/7" {
		t.Fatalf("%+v", mix)
	}
	if empty, err := ss.Playlist("empty"); err != nil || empty.Songs == nil || len(empty.Songs) != 0 {
		t.Fatalf("%+v %v", empty, err)
	}
	var skipped []string
	for _, s := range recs[1].Skipped {
		skipped = append(skipped, s.Reason)
	}
	all := strings.Join(skipped, "\n")
	if len(skipped) != 3 || !strings.Contains(all, "stars of gone/ballad/1 are left out") || !strings.Contains(all, "stars of one/ballad/8 are left out") ||
		!strings.Contains(all, "place of gone/ballad/1 in the playlist mix is dropped") {
		t.Fatalf("skipped = %q", skipped)
	}
	// a second run creates nothing and changes nothing, even if the stars changed meanwhile
	_ = ss.SetRating("one/ballad/7", 2)
	recs, _ = st.RunImports(context.Background(), "library")
	if recs[0].Created != 0 || measured != 3 {
		t.Fatalf("%+v measured %d", recs[0], measured)
	}
	if again, _ := ss.Get("one/ballad/7"); again.Rating != 2 {
		t.Fatalf("an import overwrote the stars: %+v", again)
	}
	// no file was changed or removed; the file of the stars is what stays behind
	for _, f := range []string{libraryMetaFile, "one/out/one/sa.mp3", "one/out/one/manifest.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	files, _ := LibraryImporter{Dir: dir}.LegacyFiles()
	if len(files) != 1 || filepath.Base(files[0].Path) != libraryMetaFile {
		t.Fatalf("%+v", files)
	}
	if missing, _ := st.CheckSchema(); len(missing) > 0 {
		t.Fatal(missing)
	}
}

func TestLibraryImporter_NoFileOrAFileThatCannotBeReadIsNotAFailure(t *testing.T) {
	dir := t.TempDir()
	libraryFolders(t, dir)
	_ = os.Remove(filepath.Join(dir, libraryMetaFile))
	reg := store.NewRegistry()
	RegisterStore(reg)
	RegisterProjectsImporter(reg, dir)
	reg.AddImporter(LibraryImporter{Dir: dir})
	st, _ := store.OpenMemory(reg)
	defer st.Close()
	_ = st.Migrate()
	recs, err := st.RunImports(context.Background(), "")
	if err != nil || recs[1].State != store.ImportDone || recs[1].Created != 3 || len(recs[1].Skipped) != 0 {
		t.Fatalf("%+v %v", recs, err)
	}
	if files, _ := (LibraryImporter{Dir: dir}).LegacyFiles(); len(files) != 0 {
		t.Fatalf("%+v", files)
	}
	// a damaged file is reported, and the songs are there all the same
	writeFile(t, filepath.Join(dir, libraryMetaFile), "{not json", time.Time{})
	recs, _ = st.RunImports(context.Background(), "library")
	if recs[0].State != store.ImportDone || len(recs[0].Skipped) != 1 || filepath.Base(recs[0].Skipped[0].Path) != libraryMetaFile {
		t.Fatalf("%+v", recs[0])
	}
}

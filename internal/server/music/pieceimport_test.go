package music

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"veritmo/internal/music/musicmotion"
	"veritmo/internal/platform/store"
)

func writePieceFolder(t *testing.T, root string, info pieceInfo, docs map[string]string) string {
	t.Helper()
	dir := filepath.Join(root, info.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.MarshalIndent(info, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "piece.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	for kind, body := range docs {
		if err := os.WriteFile(filepath.Join(dir, musicmotion.DocFile(kind)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// media beside the state files
	for _, n := range []string{"song.mp3", "vocals-16k.wav", "scenes.yaml"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("media"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestThePiecesKeptAsFoldersBecomeRowsWithTheirDocumentsAndAnInvalidOneIsSkipped(t *testing.T) {
	root := t.TempDir()
	created := time.Date(2026, 11, 10, 9, 30, 0, 0, time.UTC)
	good := pieceInfo{ID: piecePrefix + "20261110-093000-aaaaaa", CreatedAt: created, Project: "songs", TrackID: "night", Title: "Neon Pulse", Style: "typography", Level: "medium",
		FPS: 30, Width: 1280, Height: 720, Client: "video-studio", SongFile: "song.mp3", Lyrics: "we are neon", ScriptSource: "assistant", Renders: 2, RunID: "run-1"}
	script := `{"version":1,"pieces":[{"id":"p1","start":0,"end":4,"part":"blank"}]}`
	writePieceFolder(t, root, good, map[string]string{musicmotion.DocScript: script, musicmotion.DocDesign: `{"design":{"dark":"#000"},"source":"user","edited":true}`, musicmotion.DocSongMap: `{"version":3}`})
	writePieceFolder(t, root, pieceInfo{ID: piecePrefix + "20261111-100000-bbbbbb", CreatedAt: created.Add(time.Hour)}, nil)
	// a folder whose piece.json names another piece, one that is not JSON and one with no piece.json
	writePieceFolder(t, root, pieceInfo{ID: piecePrefix + "other"}, nil)
	if err := os.Rename(filepath.Join(root, piecePrefix+"other"), filepath.Join(root, piecePrefix+"20261112-000000-cccccc")); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(root, piecePrefix+"20261113-000000-dddddd")
	_ = os.MkdirAll(broken, 0o755)
	_ = os.WriteFile(filepath.Join(broken, "piece.json"), []byte("{not json"), 0o644)
	_ = os.MkdirAll(filepath.Join(root, piecePrefix+"20261114-000000-eeeeee"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "songs"), 0o755) // a project that is not a piece

	reg := store.NewRegistry()
	RegisterStore(reg, root)
	st, err := store.OpenMemory(reg)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	recs, err := st.RunImports(context.Background(), "")
	if err != nil || len(recs) != 1 {
		t.Fatalf("%+v %v", recs, err)
	}
	r := recs[0]
	if r.Entity != "music-motion/pieces" || r.State != store.ImportDone || r.Created != 2 || len(r.Skipped) != 3 {
		t.Fatalf("%+v", r)
	}
	ps := pieceStore{st: st}
	got, ok := ps.get(good.ID)
	if !ok || got.Title != "Neon Pulse" || got.Client != "video-studio" || got.Renders != 2 || got.RunID != "run-1" || !got.CreatedAt.Equal(created) || got.SizeBytes != nil {
		t.Fatalf("%+v %v", got, ok)
	}
	if body, ok := ps.doc(good.ID, musicmotion.DocScript); !ok || string(body) != script {
		t.Fatalf("the document is the file as it was: %q %v", body, ok)
	}
	if _, ok := ps.doc(good.ID, musicmotion.DocReview); ok {
		t.Fatal("a document the folder does not have is not made up")
	}
	for _, p := range []string{"song.mp3", "piece.json"} {
		if _, err := os.Stat(filepath.Join(root, good.ID, p)); err != nil {
			t.Fatalf("the import never touches a file: %v", err)
		}
	}
	files, _ := pieceImporter{projects: root}.LegacyFiles()
	for _, f := range files {
		base := filepath.Base(f.Path)
		if strings.HasSuffix(base, ".mp3") || strings.HasSuffix(base, ".wav") || base == "scenes.yaml" {
			t.Fatalf("media is never listed: %s", f.Path)
		}
	}
	// a second run imports nothing
	recs, err = st.RunImports(context.Background(), "music-motion/pieces")
	if err != nil || recs[0].Created != 0 || recs[0].Existing != 2 {
		t.Fatalf("%+v %v", recs, err)
	}
	if missing, _ := st.CheckSchema(); len(missing) > 0 {
		t.Fatal(missing)
	}
}

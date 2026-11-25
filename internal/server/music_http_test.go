package server

// music_http_test.go: the tests of the music family that need the whole server: the content mode that hides a project.

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/platform/runner"
)

func TestSongAnalysis_AHiddenProjectIsUnknown(t *testing.T) {
	s, dir := newProjectsServerMode(t, ContentCensored)
	labelled(t, s, dir)
	putSource(t, s, "a", "x.mp3", []byte("MP3"))
	whisperDir := t.TempDir()
	s.music.SetWhisperDir(whisperDir)
	if err := os.WriteFile(filepath.Join(whisperDir, "ggml-large-v3-turbo-q5_0.bin"), []byte("model"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.memAvailable = func() (int64, error) { return 8 << 30, nil }
	s.mediaRunner = runner.NewFakeRunner()
	s.look = func(name string) (string, error) { return "/fake/" + name, nil }
	for _, path := range []string{"/api/projects/a/transcribe", "/api/projects/a/measure"} {
		rec := call(s.Handler(), http.MethodPost, path, `{"source":"x.mp3"}`)
		unknown := call(s.Handler(), http.MethodPost, strings.Replace(path, "/a/", "/zzz/", 1), `{"source":"x.mp3"}`)
		if rec.Code != http.StatusNotFound || strings.ReplaceAll(rec.Body.String(), `\"a\"`, `\"zzz\"`) != unknown.Body.String() {
			t.Errorf("%s: %d %s vs %s", path, rec.Code, rec.Body.String(), unknown.Body.String())
		}
	}
}

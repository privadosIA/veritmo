package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWords_WithNoArgumentsPrintsTheUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runWords(nil, &out, &errb); code != 2 || !strings.Contains(errb.String(), "veritmo words bench") {
		t.Fatalf("%d %q", code, errb.String())
	}
	errb.Reset()
	if code := runWords([]string{"bench"}, &out, &errb); code != 2 {
		t.Fatalf("a folder is needed: %d", code)
	}
}

func TestWords_AFolderWithNoSongsIsAnError(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runWords([]string{"bench", t.TempDir()}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "no song with written lyrics") {
		t.Fatalf("%d %q", code, errb.String())
	}
}

func TestWords_WithoutWhisperTheCommandSaysSo(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "p", "out", "p")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, "p", "out", "p", "a.mp3"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"tracks":[{"track_id":"a","variant":0,"file":"out/p/a.mp3","lyrics_sung":"one two three four five"}]}`), 0o644)
	t.Setenv("PATH", t.TempDir()) // no whisper-cli, no ffmpeg
	var out, errb bytes.Buffer
	if code := runWords([]string{"bench", "--bin-dir", t.TempDir(), root}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "whisper-cli") {
		t.Fatalf("%d %q", code, errb.String())
	}
}

func TestWhisperModel_PrefersTheTurbo(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"ggml-base.bin", "ggml-large-v3-turbo-q5_0.bin", "notes.txt"} {
		_ = os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644)
	}
	if got := whisperModel(dir); filepath.Base(got) != "ggml-large-v3-turbo-q5_0.bin" {
		t.Fatalf("%q", got)
	}
	if whisperModel(t.TempDir()) != "" {
		t.Fatal("no model, no path")
	}
}

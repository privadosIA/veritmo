package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/project/importer"
)

const exportCLIYAML = `project: demo
output: {mode: video, file: out/demo.mp4}
defaults:
  negative_prompt: blurry
scenes:
  - id: a
    prompt: a bench
    tts_text: Hello
    seed: 3
  - id: b
    prompt: the park
    silence_seconds: 2
    animate: true
    motion_prompt: leaves move
`

func TestExportCLI_DatasetReadsBackAndNeverOverwritesBeforeAsked(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "scenes.yaml")
	os.WriteFile(in, []byte(exportCLIYAML), 0o644)
	out := filepath.Join(dir, "out", "demo.json")
	var stdout, stderr bytes.Buffer
	if code := runExport([]string{"-o", out, in}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "exported 2") || !strings.Contains(stdout.String(), "note: ") {
		t.Fatalf("stdout = %s", stdout.String())
	}
	entries, rejected, err := importer.Load(out)
	if err != nil || len(rejected) != 0 || len(entries) != 2 {
		t.Fatalf("%v %v %d", err, rejected, len(entries))
	}
	if !strings.Contains(entries[1].FullPrompt, "Motion: leaves move") || !strings.Contains(entries[0].FullPrompt, "Negative: blurry") {
		t.Errorf("entries = %+v", entries)
	}
	stderr.Reset()
	if code := runExport([]string{"-o", out, in}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "already exists") {
		t.Fatalf("an existing output must be refused: %d %s", code, stderr.String())
	}
	if code := runExport([]string{"--force-overwrite", "-o", out, in}, &stdout, &stderr); code != 0 {
		t.Fatalf("--force-overwrite: %d", code)
	}
}

func TestExportCLI_ZipAndBadUsage(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "scenes.yaml")
	os.WriteFile(in, []byte(exportCLIYAML), 0o644)
	os.MkdirAll(filepath.Join(dir, "sources"), 0o755)
	os.WriteFile(filepath.Join(dir, "sources", "me.png"), []byte("PNG"), 0o644)
	out := filepath.Join(dir, "demo.zip")
	var stdout, stderr bytes.Buffer
	if code := runExport([]string{"--format", "zip", "-o", out, in}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	if strings.Join(names, ",") != "scenes.yaml,README.txt,RECIPE.md,recipe.json,sources/me.png" {
		t.Fatalf("files = %v", names)
	}
	for _, args := range [][]string{{in}, {"-o", out}, {"--format", "pdf", "-o", out, in}} {
		if code := runExport(args, &stdout, &stderr); code == 0 {
			t.Errorf("%v must be refused", args)
		}
	}
}

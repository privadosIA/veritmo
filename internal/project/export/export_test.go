package export

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"veritmo/internal/platform/runner"
)

func writePNG(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("fake png "+name), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func fixedNow() time.Time {
	return time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
}

func sampleItems(t *testing.T, srcDir, outDir string) []Item {
	t.Helper()
	src1 := writePNG(t, srcDir, "scene_hero_v1.png")
	src2 := writePNG(t, srcDir, "scene_hero_v2.png")
	src3 := writePNG(t, srcDir, "scene_intro.png")
	return []Item{
		{Src: src1, Entry: Entry{
			SceneID: "hero", Variant: 0, EffectiveSeed: 100,
			Prompt: "cinematic a red car", NegativePrompt: "text blurry",
			Model: "m.safetensors", Resolution: [2]int{1024, 768}, Sampler: "euler", Steps: 30,
			File: filepath.Join(outDir, "hero_v1.png"),
		}},
		{Src: src2, Entry: Entry{
			SceneID: "hero", Variant: 1, EffectiveSeed: 101,
			Prompt: "cinematic a red car", NegativePrompt: "text blurry",
			Model: "m.safetensors", Resolution: [2]int{1024, 768}, Sampler: "euler", Steps: 30,
			File: filepath.Join(outDir, "hero_v2.png"),
		}},
		{Src: src3, Entry: Entry{
			SceneID: "intro", Variant: 0, EffectiveSeed: 7,
			Prompt: "a city", NegativePrompt: "",
			Model: "m.safetensors", Resolution: [2]int{1024, 768}, Sampler: "euler", Steps: 30,
			File: filepath.Join(outDir, "intro.png"),
		}},
	}
}

func TestExport_FileLayoutAndManifest(t *testing.T) {
	srcDir := t.TempDir()
	outDir := filepath.Join(t.TempDir(), "out", "proj")
	items := sampleItems(t, srcDir, outDir)

	if err := Run(outDir, "proj", items, fixedNow); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	for _, name := range []string{"hero_v1.png", "hero_v2.png", "intro.png"} {
		if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
			t.Errorf("expected exported file %s: %v", name, err)
		}
	}

	data, err := os.ReadFile(filepath.Join(outDir, "manifest.json"))
	if err != nil {
		t.Fatalf("manifest.json: %v", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("manifest.json is not valid JSON: %v", err)
	}
	if m.Project != "proj" || m.Mode != "images" {
		t.Errorf("manifest header = %q/%q, want proj/images", m.Project, m.Mode)
	}
	if m.GeneratedAt != "2026-01-15T10:30:00Z" {
		t.Errorf("generated_at = %q, want fixed RFC3339 timestamp", m.GeneratedAt)
	}
	if len(m.Images) != 3 {
		t.Fatalf("len(images) = %d, want 3", len(m.Images))
	}
	first := m.Images[0]
	if first.SceneID != "hero" || first.Variant != 0 || first.EffectiveSeed != 100 {
		t.Errorf("first entry = %+v", first)
	}
	if first.Prompt != "cinematic a red car" || first.NegativePrompt != "text blurry" {
		t.Errorf("first entry prompts = %q / %q", first.Prompt, first.NegativePrompt)
	}
	if first.Resolution != [2]int{1024, 768} || first.Sampler != "euler" || first.Steps != 30 {
		t.Errorf("first entry params = %+v", first)
	}
	if m.Images[1].EffectiveSeed != 101 || m.Images[1].Variant != 1 {
		t.Errorf("second entry = %+v", m.Images[1])
	}
}

func TestExport_CollisionRejectedBeforeAnyWrite(t *testing.T) {
	srcDir := t.TempDir()
	outDir := filepath.Join(t.TempDir(), "out", "proj")
	src := writePNG(t, srcDir, "a.png")

	items := []Item{
		{Src: src, Entry: Entry{SceneID: "a", Variant: 0, File: filepath.Join(outDir, "a_v1.png")}},
		{Src: src, Entry: Entry{SceneID: "a_v1", Variant: 0, File: filepath.Join(outDir, "a_v1.png")}},
	}

	err := Run(outDir, "proj", items, fixedNow)
	if err == nil {
		t.Fatal("expected collision error")
	}
	if !strings.Contains(err.Error(), "a_v1.png") {
		t.Errorf("error should identify the colliding path, got: %v", err)
	}
	if _, statErr := os.Stat(outDir); !os.IsNotExist(statErr) {
		entries, _ := os.ReadDir(outDir)
		if len(entries) != 0 {
			t.Errorf("no file should be created on collision, found %d entries", len(entries))
		}
	}
}

func TestExport_NoPartialManifestOnFailure(t *testing.T) {
	srcDir := t.TempDir()
	outDir := filepath.Join(t.TempDir(), "out", "proj")
	items := sampleItems(t, srcDir, outDir)

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// manifest.json.tmp cannot be created when the directory is read-only
	// after the PNG copies; simulate by pre-creating manifest.json as a
	// directory so the atomic rename fails.
	if err := os.MkdirAll(filepath.Join(outDir, "manifest.json"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := Run(outDir, "proj", items, fixedNow)
	if err == nil {
		t.Fatal("expected manifest write failure")
	}
	// The failed write must leave no partial manifest: the tmp file is
	// cleaned up and the (directory) target is untouched.
	if _, statErr := os.Stat(filepath.Join(outDir, "manifest.json.tmp")); !os.IsNotExist(statErr) {
		t.Error("manifest.json.tmp should not be left behind")
	}
	fi, statErr := os.Stat(filepath.Join(outDir, "manifest.json"))
	if statErr != nil || !fi.IsDir() {
		t.Error("failed write must not replace the rename target")
	}
}

func TestContactSheet_ExactFFmpegArgs(t *testing.T) {
	fr := runner.NewFakeRunner()
	pngs := []string{"out/p/a.png", "out/p/b.png", "out/p/c.png"}

	if err := ContactSheet(context.Background(), fr, pngs, "out/p/contact_sheet.png"); err != nil {
		t.Fatalf("ContactSheet() error = %v", err)
	}

	calls := fr.Calls()
	if len(calls) != 1 {
		t.Fatalf("got %d invocations, want 1", len(calls))
	}
	if calls[0].Name != FFmpegBinary {
		t.Errorf("binary = %q, want ffmpeg", calls[0].Name)
	}
	want := []string{
		"-y",
		"-i", "out/p/a.png",
		"-i", "out/p/b.png",
		"-i", "out/p/c.png",
		"-filter_complex", "concat=n=3,tile=2x2",
		"out/p/contact_sheet.png",
	}
	if len(calls[0].Args) != len(want) {
		t.Fatalf("args = %v, want %v", calls[0].Args, want)
	}
	for i := range want {
		if calls[0].Args[i] != want[i] {
			t.Errorf("args[%d] = %q, want %q", i, calls[0].Args[i], want[i])
		}
	}
}

func TestContactSheet_SkippedWhenDisabled(t *testing.T) {
	// The orchestrator only calls ContactSheet when output.contact_sheet is
	// true; Run itself must never invoke ffmpeg.
	srcDir := t.TempDir()
	outDir := filepath.Join(t.TempDir(), "out", "proj")
	items := sampleItems(t, srcDir, outDir)

	if err := Run(outDir, "proj", items, fixedNow); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "contact_sheet.png")); !os.IsNotExist(err) {
		t.Error("Run must not produce a contact sheet")
	}
}

func TestContactSheet_FailureLeavesArtifactsIntact(t *testing.T) {
	srcDir := t.TempDir()
	outDir := filepath.Join(t.TempDir(), "out", "proj")
	items := sampleItems(t, srcDir, outDir)

	if err := Run(outDir, "proj", items, fixedNow); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	fr := runner.NewFakeRunner()
	fr.Script(runner.ScriptedResult{Err: errBoom})
	var pngs []string
	for _, it := range items {
		pngs = append(pngs, it.Entry.File)
	}
	err := ContactSheet(context.Background(), fr, pngs, filepath.Join(outDir, "contact_sheet.png"))
	if err == nil {
		t.Fatal("expected contact-sheet failure")
	}
	if !strings.Contains(err.Error(), "contact sheet") {
		t.Errorf("error should be wrapped with contact sheet context, got: %v", err)
	}
	for _, p := range append(pngs, filepath.Join(outDir, "manifest.json")) {
		if _, statErr := os.Stat(p); statErr != nil {
			t.Errorf("artifact %s must remain intact after sheet failure: %v", p, statErr)
		}
	}
}

func TestTileLayout(t *testing.T) {
	cases := []struct{ n, cols, rows int }{
		{1, 1, 1}, {2, 2, 1}, {3, 2, 2}, {4, 2, 2}, {5, 3, 2}, {9, 3, 3}, {10, 4, 3},
	}
	for _, tc := range cases {
		cols, rows := TileLayout(tc.n)
		if cols != tc.cols || rows != tc.rows {
			t.Errorf("TileLayout(%d) = %dx%d, want %dx%d", tc.n, cols, rows, tc.cols, tc.rows)
		}
	}
}

var errBoom = &boomError{}

type boomError struct{}

func (*boomError) Error() string { return "exit status 1 (stderr: boom)" }

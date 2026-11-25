package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/project/config"
)

// fixturePath resolves a testdata file from the repo root (tests in
// cmd/veritmo may run after chdirTemp, so resolve it up front).
func fixturePath(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return p
}

func TestImportCmd_MissingOutputFlagErrors(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runImport([]string{fixturePath(t, "demo_prompts.html")}, &out, &errBuf)
	if code == 0 {
		t.Fatal("missing -o must exit non-zero")
	}
	if !strings.Contains(errBuf.String(), "-o") {
		t.Errorf("stderr should mention -o: %s", errBuf.String())
	}
}

func TestImportCmd_InvalidSilenceSecondsErrors(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runImport([]string{"--silence-seconds", "0", "-o", "x.yaml", fixturePath(t, "demo_prompts.html")}, &out, &errBuf)
	if code == 0 || !strings.Contains(errBuf.String(), "silence-seconds") {
		t.Fatalf("zero silence-seconds must fail, got code %d stderr %s", code, errBuf.String())
	}
}

func TestImportE2E_HTMLFixture(t *testing.T) {
	html := fixturePath(t, "demo_prompts.html")
	dir := chdirTemp(t)
	outPath := filepath.Join(dir, "scenes.yaml")

	var out, errBuf bytes.Buffer
	code := runImport([]string{"-o", outPath, html}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errBuf.String())
	}

	// Report on stdout with the expected summary.
	if !strings.Contains(out.String(), "imported 6, silent 2, rejected 0") {
		t.Errorf("stdout report missing summary:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "wrote "+outPath) {
		t.Errorf("stdout should confirm the written path:\n%s", out.String())
	}

	// YAML written and loadable; silent scenes carry silence_seconds.
	cfg, err := config.Load(outPath)
	if err != nil {
		t.Fatalf("generated YAML: %v", err)
	}
	if len(cfg.Scenes) != 6 {
		t.Fatalf("scenes = %d, want 6", len(cfg.Scenes))
	}
	silent := 0
	for _, s := range cfg.Scenes {
		if s.IsSilent() {
			silent++
		}
	}
	if silent != 2 {
		t.Errorf("silent scenes = %d, want 2", silent)
	}

	// No pipeline artifacts: a pure conversion creates no tmp/ or out/.
	for _, d := range []string{"tmp", "out"} {
		if _, err := os.Stat(filepath.Join(dir, d)); !os.IsNotExist(err) {
			t.Errorf("import must not create %s/", d)
		}
	}
}

func TestImportE2E_RefusesOverwriteWithoutFlag(t *testing.T) {
	html := fixturePath(t, "demo_prompts.html")
	dir := chdirTemp(t)
	outPath := filepath.Join(dir, "scenes.yaml")
	if err := os.WriteFile(outPath, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errBuf bytes.Buffer
	if code := runImport([]string{"-o", outPath, html}, &out, &errBuf); code == 0 {
		t.Fatal("existing output without --force-overwrite must fail")
	}
	if got, _ := os.ReadFile(outPath); string(got) != "keep" {
		t.Error("existing file must be unchanged")
	}

	out.Reset()
	errBuf.Reset()
	if code := runImport([]string{"-o", outPath, "--force-overwrite", html}, &out, &errBuf); code != 0 {
		t.Fatalf("--force-overwrite run failed: %s", errBuf.String())
	}
}

const songsJSON = `[["balada","t","","Spanish pop ballad\nBPM: 90\nLyrics:\n[Verse]\nhola"],["lofi","t","","Lo-fi\nInstrumental"]]`

func TestImportCmd_MusicModeWritesATracksProject(t *testing.T) {
	dir := chdirTemp(t)
	in := filepath.Join(dir, "songs.json")
	if err := os.WriteFile(in, []byte(songsJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "scenes.yaml")
	var out, errBuf bytes.Buffer
	if code := runImport([]string{"--mode", "music", "-o", outPath, in}, &out, &errBuf); code != 0 {
		t.Fatalf("exit %d: %s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "imported 2, instrumental 1, rejected 0") {
		t.Errorf("summary: %s", out.String())
	}
	b, _ := os.ReadFile(outPath)
	cfg, err := config.LoadBytes(b)
	if err != nil || cfg.Output.Mode != "music" || len(cfg.Tracks) != 2 || len(cfg.Scenes) != 0 {
		t.Fatalf("%v %+v", err, cfg)
	}
}

func TestImportCmd_UnknownModeErrors(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runImport([]string{"--mode", "audio", "-o", "x.yaml", fixturePath(t, "demo_prompts.html")}, &out, &errBuf)
	if code == 0 || !strings.Contains(errBuf.String(), "--mode") {
		t.Fatalf("code %d stderr %s", code, errBuf.String())
	}
}

func TestImportCmd_MotionModeWritesGraphics(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "g.json")
	if err := os.WriteFile(in, []byte(`[["a","t","","A bar chart growing\nDuration: 3"],["b","t","","Title\nOverlay: Hola"]]`), 0o644); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "scenes.yaml")
	var out, errBuf bytes.Buffer
	if code := runImport([]string{"--mode", "motion", "-o", outPath, in}, &out, &errBuf); code != 0 {
		t.Fatalf("code %d: %s", code, errBuf.String())
	}
	b, _ := os.ReadFile(outPath)
	if !strings.Contains(string(b), "motion_graphic: A bar chart growing") || !strings.Contains(out.String(), "imported 2 graphics") {
		t.Fatalf("%s\n%s", b, out.String())
	}
}

func TestImportCmd_Models3DModeImportsAndExportsBack(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "objetos.json")
	os.WriteFile(in, []byte(`[["patin","t","","a retro roller skate, glossy pink"],["copa","t","","a golden trophy cup\nDetail: high\nVariants: 2"],["mala","t","","a lamp\nEngine: dall-e"]]`), 0o644)
	outPath := filepath.Join(dir, "scenes.yaml")
	var out, errBuf bytes.Buffer
	if code := runImport([]string{"--mode", "models3d", "-o", outPath, in}, &out, &errBuf); code != 0 {
		t.Fatalf("exit %d: %s %s", code, out.String(), errBuf.String())
	}
	if !strings.Contains(out.String(), "imported 2 models, rejected 1") || !strings.Contains(out.String(), `line "Engine: dall-e"`) {
		t.Errorf("report:\n%s", out.String())
	}
	cfg, err := config.Load(outPath)
	if err != nil || cfg.Output.Mode != config.ModeModels3D || len(cfg.Models) != 2 || cfg.Models[1].Detail != "high" || cfg.Models[1].EntryVariants() != 2 {
		t.Fatalf("%v %+v", err, cfg)
	}
	// and back
	back := filepath.Join(dir, "back.json")
	out.Reset()
	if code := runExport([]string{"-o", back, outPath}, &out, &errBuf); code != 0 || !strings.Contains(out.String(), "exported 2") {
		t.Fatalf("export: %d %s %s", code, out.String(), errBuf.String())
	}
	if raw, _ := os.ReadFile(back); !strings.Contains(string(raw), "Detail: high") || !strings.Contains(string(raw), "Variants: 2") {
		t.Errorf("the exported dataset:\n%s", raw)
	}
	// a wrong mode lists the accepted ones
	out.Reset()
	errBuf.Reset()
	if code := runImport([]string{"--mode", "audio", "-o", filepath.Join(dir, "x.yaml"), in}, &out, &errBuf); code != 2 || !strings.Contains(errBuf.String(), "models3d") {
		t.Errorf("a wrong mode: %d %s", code, errBuf.String())
	}
}

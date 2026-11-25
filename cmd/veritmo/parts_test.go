package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/music/musicmotion"
	"veritmo/internal/platform/settings"
	"veritmo/internal/project/pipeline"
	"veritmo/internal/video/motion"
)

// A scene made from a part of the catalog needs the engine of the music motion pieces: the pipeline builds its page with it
// and the configuration checks the part with it. The program hands both over (it no longer happens when the engine is
// linked in), and this is the test that says it does.
func TestTheProgramHandsTheCatalogOfPartsToTheConfigurationAndTheRun(t *testing.T) {
	var _ pipeline.MotionEngine = musicmotion.Parts{}
	path := filepath.Join(t.TempDir(), "scenes.yaml")
	if err := os.WriteFile(path, []byte("project: p\nscenes:\n  - id: a\n    motion_part: no-such-part\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Parts == nil {
		t.Fatal("the configuration was not given the catalog of parts")
	}
	if err := cfg.Parts.Check("no-such-part", nil); err == nil || !strings.Contains(err.Error(), "not in the catalog") {
		t.Errorf("the catalog does not know the parts: %v", err)
	}
	if err := cfg.Parts.Check("counter", nil); err != nil {
		t.Errorf("a part that comes with the engine is refused: %v", err)
	}
}

const cleanHyperframesBlock = `<!doctype html><html><head><script src="https://cdn.jsdelivr.net/npm/gsap@3.14.2/dist/gsap.min.js"></script>
<style>#root .h{position:absolute;left:160px;top:120px;font-family:"Inter";font-size:120px;color:#fff}</style></head><body>
<div id="root" data-composition-id="clean" data-width="1920" data-height="1080" data-start="0" data-duration="4"><div class="clip" data-start="0" data-duration="4"><div id="h" class="h">Hello</div></div></div>
<script>window.__timelines = window.__timelines || {}; var tl = gsap.timeline({paused:true}); tl.from('#h',{y:80,opacity:0,duration:0.8,ease:'power3.out'},0.2); tl.to('#h',{x:300,duration:2,ease:'none'},1); window.__timelines['clean'] = tl;</script></body></html>`

// partsEnvironment points the page builder at the repository's GSAP and the program at an empty parts folder; it skips without a browser or GSAP.
func partsEnvironment(t *testing.T) string {
	t.Helper()
	gsap, err := filepath.Abs(filepath.Join("..", "..", "third_party", "gsap", "gsap.min.js"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(gsap); err != nil {
		t.Skip("GSAP is not installed (bash scripts/get-gsap.sh)")
	}
	if _, err := motion.Find(settings.Browser()); err != nil {
		t.Skip("no Chrome or Chromium on this machine")
	}
	old := musicmotion.GSAPFile
	musicmotion.GSAPFile = gsap
	t.Cleanup(func() { musicmotion.GSAPFile = old })
	return filepath.Join(t.TempDir(), "parts")
}

func TestPartsImportHyperframesWritesWhatPassesAndExitsByWhetherAnyWasWritten(t *testing.T) {
	parts := partsEnvironment(t)
	dir := filepath.Join(t.TempDir(), "compositions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "clean.html"), []byte(cleanHyperframesBlock), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "movie.html"), []byte(strings.Replace(cleanHyperframesBlock, `<div id="h"`, `<video src="a.mp4"></video><div id="h"`, 1)), 0o644)

	var out, errb strings.Builder
	if code := runParts([]string{"import-hyperframes", "--dry-run", "--parts-dir", parts, filepath.Dir(dir)}, &out, &errb); code != 0 {
		t.Fatalf("dry run: exit %d\n%s\n%s", code, out.String(), errb.String())
	}
	if _, err := os.Stat(parts); err == nil {
		t.Fatal("--dry-run writes nothing")
	}
	if !strings.Contains(out.String(), "would be written as hf-clean") || !strings.Contains(out.String(), "movie") || !strings.Contains(out.String(), "refused: it mounts media") {
		t.Fatalf("lines:\n%s", out.String())
	}

	out.Reset()
	if code := runParts([]string{"import-hyperframes", "--json", "--parts-dir", parts, filepath.Dir(dir)}, &out, &errb); code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out.String(), errb.String())
	}
	var rep struct {
		Blocks  []struct{ Block, Part, Refused string }
		Written int
	}
	if err := json.Unmarshal([]byte(out.String()), &rep); err != nil || rep.Written != 1 || len(rep.Blocks) != 2 {
		t.Fatalf("json: %v\n%s", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(parts, "hf-clean", "manifest.json")); err != nil {
		t.Fatalf("the part was not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(parts, "hf-clean", "origin.json")); err != nil {
		t.Fatalf("its origin was not written: %v", err)
	}

	// nothing usable: exit 1, nothing written, every line says why
	only := filepath.Join(t.TempDir(), "compositions")
	_ = os.MkdirAll(only, 0o755)
	_ = os.WriteFile(filepath.Join(only, "movie.html"), []byte(strings.Replace(cleanHyperframesBlock, `<div id="h"`, `<video src="a.mp4"></video><div id="h"`, 1)), 0o644)
	out.Reset()
	if code := runParts([]string{"import-hyperframes", "--parts-dir", parts, filepath.Dir(only)}, &out, &errb); code != 1 || !strings.Contains(out.String(), "refused") {
		t.Fatalf("every block refused: want exit 1, got %d\n%s", code, out.String())
	}
	if code := runParts([]string{"import-hyperframes"}, &out, &errb); code != 2 {
		t.Fatalf("no folder: usage, exit 2, got %d", code)
	}
}

func TestPartsImportHyperframesEmbedsThePicturesOfAssetsAndSaysSo(t *testing.T) {
	parts := partsEnvironment(t)
	root := filepath.Join(t.TempDir(), "blocks")
	dir := filepath.Join(root, "compositions")
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><path d="M0 5 L10 5" stroke="#fff"/></svg>`
	_ = os.WriteFile(filepath.Join(root, "assets", "stroke.svg"), []byte(svg), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "inked.html"), []byte(strings.Replace(cleanHyperframesBlock, `<div id="h" class="h">Hello</div>`, `<div id="h" class="h">Hello</div><img class="ink" src="assets/stroke.svg" style="position:absolute;left:160px;top:300px;width:600px">`, 1)), 0o644)

	var out, errb strings.Builder
	if code := runParts([]string{"import-hyperframes", "--json", "--parts-dir", parts, root}, &out, &errb); code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out.String(), errb.String())
	}
	var rep struct {
		Blocks []struct {
			Block    string
			Pictures []struct {
				Name, Kind string
				Bytes      int
			}
		}
	}
	if err := json.Unmarshal([]byte(out.String()), &rep); err != nil || len(rep.Blocks) != 1 || len(rep.Blocks[0].Pictures) != 1 || rep.Blocks[0].Pictures[0].Name != "stroke.svg" || rep.Blocks[0].Pictures[0].Kind != "svg" {
		t.Fatalf("the JSON entry carries the pictures: %v\n%s", err, out.String())
	}
	html, err := os.ReadFile(filepath.Join(parts, "hf-inked", "part.html"))
	if err != nil || !strings.Contains(string(html), "data:image/svg+xml;base64,") || strings.Contains(string(html), "assets/stroke.svg") {
		t.Fatalf("the part carries the drawing inside: %v", err)
	}
	origin, _ := os.ReadFile(filepath.Join(parts, "hf-inked", "origin.json"))
	if !strings.Contains(string(origin), `"stroke.svg"`) {
		t.Errorf("origin.json lists the embedded file: %s", origin)
	}
	manifest, _ := os.ReadFile(filepath.Join(parts, "hf-inked", "manifest.json"))
	if !strings.Contains(string(manifest), `"pictures": 1`) {
		t.Errorf("the manifest counts the pictures: %s", manifest)
	}

	// the text output names them with their sizes
	out.Reset()
	if code := runParts([]string{"import-hyperframes", "--dry-run", "--parts-dir", filepath.Join(t.TempDir(), "p2"), root}, &out, &errb); code != 0 || !strings.Contains(out.String(), "pictures: stroke.svg 1 KB") {
		t.Fatalf("the text line: exit %d\n%s", code, out.String())
	}
}

// Without --parts-dir the command writes through the catalog of the database: rows and no folder, and a project that names the part is checked
// against the same database while the parts folder of the settings does not exist.
func TestPartsImportHyperframesWritesRowsAndACheckedProjectFindsThePartThere(t *testing.T) {
	_ = partsEnvironment(t)
	isolate(t)
	work := t.TempDir()
	t.Chdir(work) // defaultDB is relative to the folder the command runs in
	if code, out, errOut := data(t, "migrate"); code != 0 {
		t.Fatalf("migrate: %d\n%s\n%s", code, out, errOut)
	}
	blocks := filepath.Join(t.TempDir(), "compositions")
	if err := os.MkdirAll(blocks, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(blocks, "clean.html"), []byte(cleanHyperframesBlock), 0o644)
	var out, errb strings.Builder
	if code := runParts([]string{"import-hyperframes", filepath.Dir(blocks)}, &out, &errb); code != 0 || !strings.Contains(out.String(), "written as hf-clean") {
		t.Fatalf("exit %d\n%s\n%s", code, out.String(), errb.String())
	}
	if _, err := os.Stat(filepath.Join(settings.Dir(), "music-motion", "parts")); !os.IsNotExist(err) {
		t.Fatalf("no folder of parts is made: %v", err)
	}
	yaml := filepath.Join(work, "scenes.yaml")
	if err := os.WriteFile(yaml, []byte("project: p\nscenes:\n  - id: a\n    motion_part: hf-clean\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(yaml)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Parts.Check("hf-clean", nil); err != nil {
		t.Fatalf("the part is in the database the command opens: %v", err)
	}
	if err := cfg.Parts.Check("no-such-part", nil); err == nil {
		t.Fatal("and a part that is not there is still refused")
	}
}

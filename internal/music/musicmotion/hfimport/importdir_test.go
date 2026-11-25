package hfimport

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/music/musicmotion"
	"veritmo/internal/video/motion"
)

// gsapOrSkip points the page builder at the repository's GSAP file and a browser, or skips: both are needed to check a part.
func gsapOrSkip(t *testing.T) string {
	t.Helper()
	b, err := motion.Find("")
	if err != nil {
		t.Skip("no Chrome or Chromium on this machine")
	}
	dir, _ := os.Getwd()
	for {
		p := filepath.Join(dir, "third_party", "gsap", "gsap.min.js")
		if _, err := os.Stat(p); err == nil {
			old := musicmotion.GSAPFile
			musicmotion.GSAPFile = p
			t.Cleanup(func() { musicmotion.GSAPFile = old })
			return b.Path
		}
		if up := filepath.Dir(dir); up != dir {
			dir = up
			continue
		}
		t.Skip("GSAP is not installed (bash scripts/get-gsap.sh)")
	}
}

// folder is a compositions/ folder as `npx hyperframes add` leaves it: a clean block, one with a video and a vertical one.
func folder(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "compositions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	put := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name+".html"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("kinetic", strings.Replace(kineticBlock, "top: 380px", "top: 120px", 1))
	put("with-video", strings.Replace(kineticBlock, `<div id="k-sub"`, `<video id="v" src="clip.mp4"></video><div id="k-sub"`, 1))
	put("tall", strings.Replace(strings.Replace(kineticBlock, `data-width="1920"`, `data-width="1080"`, 1), `data-height="1080"`, `data-height="1920"`, 1))
	return filepath.Dir(dir)
}

func TestImportWritesTheBlockThatPassesAndRefusesTheRestSayingWhy(t *testing.T) {
	browser := gsapOrSkip(t)
	userDir := filepath.Join(t.TempDir(), "parts")
	lines, err := Import(context.Background(), Options{Dir: folder(t), Browser: browser, UserDir: userDir, Version: "0.9.53"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Line{}
	for _, l := range lines {
		got[l.Block] = l
	}
	if len(lines) != 3 || !got["kinetic"].Written || got["with-video"].Written || got["tall"].Written {
		t.Fatalf("want only the clean block written: %+v", lines)
	}
	if !strings.Contains(got["with-video"].Refused, "parts carry no media") || !strings.Contains(got["tall"].Refused, "vertical") {
		t.Fatalf("the reasons: %+v", lines)
	}
	// the part is where the server reads it, with its origin, and the catalog lists it as a timeline part
	cat := musicmotion.Catalog{Builtin: musicmotion.BuiltinParts(), UserDir: userDir}
	p, ok := cat.Get("hf-kinetic")
	if !ok || p.Source != "user" || !p.Timeline() {
		t.Fatalf("the part is not in the catalog as a timeline part: %v %+v", ok, p.Manifest)
	}
	if p.Origin == nil || p.Origin.Source != "hyperframes" || p.Origin.Block != "kinetic" || p.Origin.Licence != "Apache-2.0" || p.Origin.Version != "0.9.53" || p.Origin.URL == "" {
		t.Fatalf("origin: %+v", p.Origin)
	}
	if _, err := os.Stat(filepath.Join(userDir, "hf-with-video")); err == nil {
		t.Fatal("a refused block must leave nothing behind")
	}
	// a second run does not replace the part
	again, err := Import(context.Background(), Options{Dir: folder(t), Only: []string{"kinetic"}, Browser: browser, UserDir: userDir})
	if err != nil || len(again) != 1 || again[0].Written || !strings.Contains(again[0].Refused, "already exists") {
		t.Fatalf("%+v %v", again, err)
	}
}

func TestImportWithADryRunWritesNothingAndOnlyPicksTheNamedBlocks(t *testing.T) {
	browser := gsapOrSkip(t)
	userDir := filepath.Join(t.TempDir(), "parts")
	lines, err := Import(context.Background(), Options{Dir: folder(t), Only: []string{"kinetic", "tall"}, DryRun: true, Browser: browser, UserDir: userDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || !lines[0].Written || lines[1].Written {
		t.Fatalf("a dry run says what would pass: %+v", lines)
	}
	if _, err := os.Stat(userDir); err == nil {
		t.Fatal("a dry run writes nothing")
	}
	if _, err := Import(context.Background(), Options{Dir: folder(t), Only: []string{"nope"}, Browser: browser, UserDir: userDir}); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("an unknown block is an error naming it: %v", err)
	}
}

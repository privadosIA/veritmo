package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"veritmo/internal/video/motion"
)

func syncFixture(t *testing.T) (*projectStore, string) {
	t.Helper()
	ps, _ := memProjects(t)
	if err := ps.Create("demo", []byte(videoYAML)); err != nil {
		t.Fatal(err)
	}
	return ps, filepath.Join(t.TempDir(), "demo")
}

func modTime(t *testing.T, path string) time.Time {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.ModTime()
}

func TestMaterialize_WritesTheDefinitionAndThePagesOnlyWhenTheyDiffer(t *testing.T) {
	ps, dir := syncFixture(t)
	_ = ps.PutPage("demo", "a", false, "<p>page</p>", &motion.Meta{Kind: "motion_graphic", Model: "gemma"})
	if err := ps.Materialize("demo", dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "scenes.yaml")); string(b) != videoYAML {
		t.Fatalf("%q", b)
	}
	if b, _ := os.ReadFile(motion.PagePath(dir, "a", false)); string(b) != "<p>page</p>" {
		t.Fatalf("%q", b)
	}
	if m, ok := motion.LoadMeta(dir, "a", false); !ok || m.Model != "gemma" {
		t.Fatalf("%+v %v", m, ok)
	}
	// nothing differs: nothing is rewritten
	old := time.Now().Add(-time.Hour)
	for _, f := range []string{filepath.Join(dir, "scenes.yaml"), motion.PagePath(dir, "a", false)} {
		_ = os.Chtimes(f, old, old)
	}
	if err := ps.Materialize("demo", dir); err != nil {
		t.Fatal(err)
	}
	if modTime(t, filepath.Join(dir, "scenes.yaml")).After(old.Add(time.Minute)) {
		t.Fatal("an equal definition is not written again")
	}
	if modTime(t, motion.PagePath(dir, "a", false)).After(old.Add(time.Minute)) {
		t.Fatal("an equal page is not written again")
	}
	// a hand edit of the file is overwritten by the saved text
	_ = os.WriteFile(filepath.Join(dir, "scenes.yaml"), []byte("project: by hand\n"), 0o644)
	_ = os.WriteFile(motion.PagePath(dir, "a", false), []byte("<p>by hand</p>"), 0o644)
	if err := ps.Materialize("demo", dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "scenes.yaml")); string(b) != videoYAML {
		t.Fatalf("%q", b)
	}
	if b, _ := os.ReadFile(motion.PagePath(dir, "a", false)); string(b) != "<p>page</p>" {
		t.Fatalf("%q", b)
	}
	// a page with no row goes, and a file of motion/ that is not a page stays
	_ = os.WriteFile(motion.PagePath(dir, "gone", false), []byte("<p>stale</p>"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "motion", "a.song.json"), []byte("{}"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "motion", "a.choice.json"), []byte("{}"), 0o644)
	if err := ps.Materialize("demo", dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(motion.PagePath(dir, "gone", false)); !os.IsNotExist(err) {
		t.Fatal("a page the API does not have is removed")
	}
	for _, keep := range []string{"a.song.json", "a.choice.json"} {
		if _, err := os.Stat(filepath.Join(dir, "motion", keep)); err != nil {
			t.Fatalf("%s must stay: %v", keep, err)
		}
	}
	if err := ps.Materialize("nope", dir); err == nil {
		t.Fatal("an unknown project cannot be written")
	}
}

func TestIngest_AnUnchangedFolderSavesNothingAndAChangedFileAddsAVersionWithItsReason(t *testing.T) {
	ps, dir := syncFixture(t)
	_ = ps.Materialize("demo", dir)
	if err := ps.Ingest("demo", dir, "run"); err != nil {
		t.Fatal(err)
	}
	if vs, _ := ps.Versions("demo"); len(vs) != 0 {
		t.Fatalf("%d versions", len(vs))
	}
	fixed := strings.Replace(videoYAML, "a street", "a fixed street", 1)
	_ = os.WriteFile(filepath.Join(dir, "scenes.yaml"), []byte(fixed), 0o644) // what a fix does
	if err := ps.Ingest("demo", dir, "fix"); err != nil {
		t.Fatal(err)
	}
	row, _ := ps.Get("demo")
	vs, _ := ps.Versions("demo")
	if string(row.Definition) != fixed || len(vs) != 1 || vs[0].Reason != "fix" {
		t.Fatalf("%q %+v", row.Definition, vs)
	}
	if v, _ := ps.Version("demo", 1); string(v.Definition) != videoYAML {
		t.Fatal("the version is the text before the fix")
	}
	// taken once: a second ingest of the same file adds nothing
	_ = ps.Ingest("demo", dir, "fix")
	if vs, _ := ps.Versions("demo"); len(vs) != 1 {
		t.Fatalf("%d versions", len(vs))
	}
}

func TestIngest_APageTheRunWroteBecomesARowAndAPageFileThatIsGoneIsNotADeletion(t *testing.T) {
	ps, dir := syncFixture(t)
	_ = ps.Materialize("demo", dir)
	if err := motion.Store(dir, "a", false, "<p>written by the run</p>"); err != nil {
		t.Fatal(err)
	}
	if err := motion.StoreMeta(dir, "a", false, motion.Meta{Kind: "motion_graphic", Provider: "ollama", Model: "gemma"}); err != nil {
		t.Fatal(err)
	}
	if err := motion.Store(dir, "a", true, "<p>overlay</p>"); err != nil {
		t.Fatal(err)
	}
	if err := ps.Ingest("demo", dir, "run"); err != nil {
		t.Fatal(err)
	}
	html, meta, hasMeta, found := ps.Page("demo", "a", false)
	if !found || html != "<p>written by the run</p>" || !hasMeta || meta.Provider != "ollama" {
		t.Fatalf("%q %+v %v %v", html, meta, hasMeta, found)
	}
	if _, _, _, found := ps.Page("demo", "a", true); !found {
		t.Fatal("the overlay too")
	}
	// the run changes the page: the row follows
	_ = motion.Store(dir, "a", false, "<p>v2</p>")
	_ = ps.Ingest("demo", dir, "run")
	if html, _, _, _ := ps.Page("demo", "a", false); html != "<p>v2</p>" {
		t.Fatalf("%q", html)
	}
	// the file is removed by the run: the row stays
	_ = os.Remove(motion.PagePath(dir, "a", false))
	_ = ps.Ingest("demo", dir, "run")
	if html, _, _, found := ps.Page("demo", "a", false); !found || html != "<p>v2</p>" {
		t.Fatalf("only the API deletes a page: %q %v", html, found)
	}
}

func TestIngest_ADefinitionSavedWhileTheRunWorkedStaysAndTheRunsTextIsAVersion(t *testing.T) {
	ps, dir := syncFixture(t)
	_ = ps.Materialize("demo", dir) // the run starts with the saved text
	// meanwhile the user saves another text
	mine := strings.Replace(videoYAML, "Demo", "Demo by the user", 1)
	if _, err := ps.Save("demo", []byte(mine), "save"); err != nil {
		t.Fatal(err)
	}
	// and the run leaves its fix in the file
	runs := strings.Replace(videoYAML, "a street", "the run's street", 1)
	_ = os.WriteFile(filepath.Join(dir, "scenes.yaml"), []byte(runs), 0o644)
	if err := ps.Ingest("demo", dir, "fix"); err != nil {
		t.Fatal(err)
	}
	row, _ := ps.Get("demo")
	if string(row.Definition) != mine {
		t.Fatalf("the user's text stays: %q", row.Definition)
	}
	vs, _ := ps.Versions("demo")
	var found bool
	for _, v := range vs {
		full, _ := ps.Version("demo", v.Version)
		if string(full.Definition) == runs && v.Reason == "fix" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the run's text is a version: %+v", vs)
	}
}

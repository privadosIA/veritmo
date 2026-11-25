package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/platform/store"
	"veritmo/internal/server/workspace"
)

const projectYAML = "project: demo\noutput: {mode: images}\ndefaults:\n  model: m.safetensors\nscenes:\n  - id: one\n    prompt: a kite\n"

// projectsFixture makes a migrated SQLite database with the project demo and returns the flags that point at it.
func projectsFixture(t *testing.T) (db, projects string, args []string) {
	t.Helper()
	isolate(t)
	db = filepath.Join(t.TempDir(), "o.db")
	projects = filepath.Join(t.TempDir(), "projects")
	if code, out, errOut := data(t, "migrate", "--db", db); code != 0 {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
	access, st, err := openProjectStore(db, projects)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := access.Create("demo", []byte(projectYAML)); err != nil {
		t.Fatal(err)
	}
	// the files a definition names, in the folder of the project (a save is validated against them)
	if err := os.MkdirAll(filepath.Join(projects, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(projects, "demo", "m.safetensors"), []byte("stub"), 0o644)
	return db, projects, []string{"--db", db, "--projects-dir", projects}
}

func TestProject_ResolveWritesTheSavedDefinitionToItsFolderAndTakesBackAFix(t *testing.T) {
	db, projects, _ := projectsFixture(t)
	p := projectArgs{ID: "demo", ProjectsDir: projects, DBPath: db}
	var errb bytes.Buffer
	path, done, ok := p.resolve(&errb)
	if !ok {
		t.Fatalf("%s", errb.String())
	}
	if got, _ := os.ReadFile(path); string(got) != projectYAML || path != filepath.Join(projects, "demo", "scenes.yaml") {
		t.Fatalf("the run gets the saved definition in the folder of the project: %q %s", got, path)
	}
	// what the work changes in the folder is the definition afterwards, and the text before is a version
	fixed := strings.Replace(projectYAML, "a kite", "a fixed kite", 1)
	_ = os.WriteFile(path, []byte(fixed), 0o644)
	_ = os.WriteFile(path+".bak", []byte("the backup the fix leaves"), 0o644)
	done("fix")
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatal("the backup file goes: the version is the backup")
	}
	access, st, err := openProjectStore(db, projects)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if def, _ := access.Definition("demo"); string(def) != fixed {
		t.Fatalf("%q", def)
	}
}

func TestProject_AnUnknownProjectExitsBeforeAnythingRunsAndNamesIt(t *testing.T) {
	_, _, args := projectsFixture(t)
	var out, errb bytes.Buffer
	if code := runExport(append([]string{"--project", "nope", "-o", filepath.Join(t.TempDir(), "x.json")}, args...), &out, &errb); code == 0 || !strings.Contains(errb.String(), `"nope"`) {
		t.Fatalf("export: %d %s", code, errb.String())
	}
	errb.Reset()
	if code := runCheck(append([]string{"--project", "nope"}, args...), &out, &errb); code == 0 || !strings.Contains(errb.String(), `"nope"`) {
		t.Fatalf("check: %d %s", code, errb.String())
	}
	// both a project and a file is a usage error
	if code := runCheck(append([]string{"--project", "demo", "scenes.yaml"}, args...), &out, &errb); code != 2 {
		t.Fatalf("%d", code)
	}
}

func TestProject_ALooseFileOpensNoDatabase(t *testing.T) {
	old := openProjectStore
	openProjectStore = func(string, string) (*workspace.ProjectsAccess, *store.Store, error) {
		t.Fatal("a loose scenes.yaml must not open the database")
		return nil, nil, nil
	}
	defer func() { openProjectStore = old }()
	dir := t.TempDir()
	path := filepath.Join(dir, "scenes.yaml")
	_ = os.WriteFile(path, []byte(projectYAML), 0o644)
	var out, errb bytes.Buffer
	out2 := filepath.Join(t.TempDir(), "x.json")
	if code := runExport([]string{"-o", out2, path}, &out, &errb); code != 0 {
		t.Fatalf("%d %s", code, errb.String())
	}
	if _, err := os.Stat(out2); err != nil {
		t.Fatal(err)
	}
}

func TestProject_ExportOfAProjectOfTheDatabase(t *testing.T) {
	_, _, args := projectsFixture(t)
	outFile := filepath.Join(t.TempDir(), "demo.dataset.json")
	var out, errb bytes.Buffer
	if code := runExport(append([]string{"--project", "demo", "-o", outFile}, args...), &out, &errb); code != 0 {
		t.Fatalf("%d %s", code, errb.String())
	}
	if b, _ := os.ReadFile(outFile); !strings.Contains(string(b), "a kite") {
		t.Fatalf("%s", b)
	}
}

func TestProject_PullEditAndPush(t *testing.T) {
	_, projects, args := projectsFixture(t)
	file := filepath.Join(t.TempDir(), "edit.yaml")
	var out, errb bytes.Buffer
	if code := runProject(append([]string{"pull", "demo", file}, args...), &out, &errb); code != 0 {
		t.Fatalf("%d %s", code, errb.String())
	}
	if b, _ := os.ReadFile(file); string(b) != projectYAML {
		t.Fatalf("%q", b)
	}
	// the default file is the scenes.yaml of its folder
	if code := runProject(append([]string{"pull", "demo"}, args...), &out, &errb); code != 0 {
		t.Fatalf("%d %s", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(projects, "demo", "scenes.yaml")); err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(projectYAML, "a kite", "a red kite", 1)
	_ = os.WriteFile(file, []byte(edited), 0o644)
	out.Reset()
	if code := runProject(append([]string{"push", "demo", file}, args...), &out, &errb); code != 0 || !strings.Contains(out.String(), "saved") {
		t.Fatalf("%d %s %s", code, out.String(), errb.String())
	}
	access, st, _ := openProjectStore(args[1], projects)
	defer st.Close()
	if def, _ := access.Definition("demo"); string(def) != edited {
		t.Fatalf("the edited text is the definition: %q", def)
	}
	// pushing the same text again changes nothing
	out.Reset()
	if code := runProject(append([]string{"push", "demo", file}, args...), &out, &errb); code != 0 || !strings.Contains(out.String(), "nothing changed") {
		t.Fatalf("%d %s", code, out.String())
	}
}

func TestProject_AnInvalidFileIsNotPushed(t *testing.T) {
	_, projects, args := projectsFixture(t)
	file := filepath.Join(t.TempDir(), "bad.yaml")
	_ = os.WriteFile(file, []byte("project: demo\nscenes: nope\n"), 0o644)
	var out, errb bytes.Buffer
	if code := runProject(append([]string{"push", "demo", file}, args...), &out, &errb); code == 0 || errb.Len() == 0 {
		t.Fatalf("%d %s", code, errb.String())
	}
	access, st, _ := openProjectStore(args[1], projects)
	defer st.Close()
	if def, _ := access.Definition("demo"); string(def) != projectYAML {
		t.Fatalf("the definition is unchanged: %q", def)
	}
	// an unknown project, and a usage error
	if code := runProject(append([]string{"pull", "nope"}, args...), &out, &errb); code == 0 {
		t.Fatal("an unknown project")
	}
	if code := runProject([]string{"frobnicate"}, &out, &errb); code != 2 {
		t.Fatalf("%d", code)
	}
}

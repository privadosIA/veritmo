package media

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"veritmo/internal/platform/fsutil"
)

func repoWithModels(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "models", "a.gguf"), "aaaa")
	write(t, filepath.Join(root, "models", "music", "m.gguf"), "mmmmmm")
	os.MkdirAll(filepath.Join(root, "models", "music", "adapters"), 0o755)
	os.Symlink("a.gguf", filepath.Join(root, "models", "alias.gguf"))
	return root
}

func TestMigrateModelsMovesAndLinks(t *testing.T) {
	root, dest := repoWithModels(t), filepath.Join(t.TempDir(), "models")
	rep, err := MigrateModels(root, dest, false, nil)
	if err != nil || !rep.Linked || rep.Copied != 3 {
		t.Fatalf("%+v %v", rep, err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "models", "music", "m.gguf")); string(b) != "mmmmmm" {
		t.Fatalf("read through the link: %q", b)
	}
	if fi, err := os.Stat(filepath.Join(dest, "music", "adapters")); err != nil || !fi.IsDir() {
		t.Fatal("the empty folder was not kept")
	}
	if l, _ := os.Readlink(filepath.Join(dest, "alias.gguf")); l != "a.gguf" {
		t.Fatalf("inner link: %q", l)
	}
	if _, err := os.Lstat(filepath.Join(root, "models.migrated")); err == nil {
		t.Fatal("the old copy was not deleted")
	}
	if st := CheckModels(root, dest); !st.Available || !st.Linked {
		t.Fatalf("state: %+v", st)
	}
	rep, err = MigrateModels(root, dest, false, nil)
	if err != nil || !rep.Done {
		t.Fatalf("repeat: %+v %v", rep, err)
	}
}

func TestMigrateModelsDryRunChangesNothing(t *testing.T) {
	root, dest := repoWithModels(t), filepath.Join(t.TempDir(), "models")
	rep, err := MigrateModels(root, dest, true, nil)
	if err != nil || rep.Needed != 10 || rep.Linked {
		t.Fatalf("%+v %v", rep, err)
	}
	if fi, _ := os.Lstat(filepath.Join(root, "models")); fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("dry run linked models")
	}
	if _, err := os.Stat(dest); err == nil {
		t.Fatal("dry run created the destination")
	}
}

func TestMigrateModelsResumesAfterACut(t *testing.T) {
	root, dest := repoWithModels(t), t.TempDir()
	// An earlier run copied one file completely and left a short .partial of another.
	if _, err := MigrateModels(root, dest, true, nil); err != nil {
		t.Fatal(err)
	}
	if err := fsutil.CopyFile(filepath.Join(root, "models", "a.gguf"), filepath.Join(dest, "a.gguf")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dest, "music", "m.gguf.partial"), "mm")
	rep, err := MigrateModels(root, dest, false, nil)
	if err != nil || rep.Skipped < 1 || !rep.Linked {
		t.Fatalf("%+v %v", rep, err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "models", "music", "m.gguf")); string(b) != "mmmmmm" {
		t.Fatalf("content: %q", b)
	}
}

func TestMigrateModelsRefusesALinkElsewhere(t *testing.T) {
	root, other := t.TempDir(), t.TempDir()
	os.Symlink(other, filepath.Join(root, "models"))
	if _, err := MigrateModels(root, t.TempDir(), false, nil); err == nil || !strings.Contains(err.Error(), "elsewhere") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckModelsStates(t *testing.T) {
	root := repoWithModels(t)
	dest := t.TempDir()
	if st := CheckModels(root, dest); !st.Available || st.Linked || !strings.Contains(st.Reason, "migrate") {
		t.Fatalf("real folder: %+v", st)
	}
	if st := CheckModels(root, filepath.Join(dest, "nope")); st.Available {
		t.Fatalf("missing directory is available: %+v", st)
	}
}

func TestResolveModelsDirPrecedence(t *testing.T) {
	cfg := t.TempDir()
	os.MkdirAll(filepath.Join(cfg, "veritmo"), 0o755)
	os.WriteFile(filepath.Join(cfg, "veritmo", ModelsSettingFile), []byte("/from/file\nignored\n"), 0o644)
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for _, c := range []struct {
		flag string
		env  map[string]string
		want string
	}{
		{"/flag", map[string]string{"MODELS_DIR": "/env"}, "/flag"},
		{"", map[string]string{"MODELS_DIR": "/env"}, "/env"},
		{"", nil, "/from/file"},
		{"", map[string]string{"MODELS_DIR": "off"}, ""},
	} {
		if got := ResolveModelsDir(c.flag, env(c.env), cfg); got != c.want {
			t.Errorf("%+v = %q, want %q", c, got, c.want)
		}
	}
	if got := ResolveModelsDir("", env(nil), t.TempDir()); got != "" {
		t.Errorf("no setting = %q", got)
	}
}

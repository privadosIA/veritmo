package legacyname

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// logs captures what the package logs while f runs.
func logs(t *testing.T, f func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)
	f()
	return buf.String()
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A server updated in place: the files of the old directory are in the new one, the old one is gone, and one line says what moved.
func TestDir_AnOldDirectoryIsMovedOnce(t *testing.T) {
	reset()
	base := t.TempDir()
	write(t, filepath.Join(base, "mediaorquestator", "token"), "secret")
	write(t, filepath.Join(base, "mediaorquestator", "settings.json"), "{}")
	var got string
	out := logs(t, func() { got = Dir(base) })
	if got != filepath.Join(base, "veritmo") {
		t.Fatalf("dir = %s", got)
	}
	if b, err := os.ReadFile(filepath.Join(got, "token")); err != nil || string(b) != "secret" {
		t.Errorf("the token did not move: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(base, "mediaorquestator")); err == nil {
		t.Errorf("the old directory is still there")
	}
	if !strings.Contains(out, "moved "+filepath.Join(base, "mediaorquestator")+" to "+got) {
		t.Errorf("the log does not say what moved: %q", out)
	}
	if again := logs(t, func() { Dir(base) }); again != "" {
		t.Errorf("a second call logs again: %q", again)
	}
}

// Both directories: the new one is used, the old one is left untouched and a warning names it.
func TestDir_BothDirectoriesKeepTheNewOneAndWarn(t *testing.T) {
	reset()
	base := t.TempDir()
	write(t, filepath.Join(base, "mediaorquestator", "token"), "old")
	write(t, filepath.Join(base, "veritmo", "token"), "new")
	var got string
	out := logs(t, func() { got = Dir(base) })
	if b, _ := os.ReadFile(filepath.Join(got, "token")); string(b) != "new" {
		t.Errorf("the new directory is not the one used: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(base, "mediaorquestator", "token")); string(b) != "old" {
		t.Errorf("the old directory was touched: %q", b)
	}
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, filepath.Join(base, "mediaorquestator")) {
		t.Errorf("no warning that names the old directory: %q", out)
	}
}

// A fresh machine: nothing is created, nothing is logged.
func TestDir_AFreshMachineLogsNothingAndCreatesNothing(t *testing.T) {
	reset()
	base := t.TempDir()
	var got string
	out := logs(t, func() { got = Dir(base) })
	if got != filepath.Join(base, "veritmo") || out != "" {
		t.Errorf("dir %s, log %q", got, out)
	}
	if entries, _ := os.ReadDir(base); len(entries) != 0 {
		t.Errorf("something was created: %v", entries)
	}
}

// The old variable answers when the new one is unset (with one warning, once), the new one wins when both are set, and neither set is empty.
func TestGetenv_TheOldNameAnswersWithAWarningAndTheNewOneWins(t *testing.T) {
	resetEnv()
	t.Setenv("VERITMO_X_TEST", "")
	t.Setenv("MO_X_TEST", "")
	if got := Getenv("X_TEST"); got != "" {
		t.Errorf("nothing set: %q", got)
	}
	t.Setenv("MO_X_TEST", "old")
	out := logs(t, func() {
		if got := Getenv("X_TEST"); got != "old" {
			t.Errorf("old only: %q", got)
		}
		Getenv("X_TEST")
	})
	if strings.Count(out, "MO_X_TEST is obsolete, use VERITMO_X_TEST") != 1 {
		t.Errorf("the warning must come once and name the new variable: %q", out)
	}
	t.Setenv("VERITMO_X_TEST", "new")
	if out := logs(t, func() {
		if got := Getenv("X_TEST"); got != "new" {
			t.Errorf("both set: %q", got)
		}
	}); out != "" {
		t.Errorf("the new variable wins without a warning: %q", out)
	}
	resetEnv()
	t.Setenv("MO_X_TEST", "")
	t.Setenv("VERITMO_X_TEST", "only-new")
	if got := Getenv("X_TEST"); got != "only-new" {
		t.Errorf("new only: %q", got)
	}
}

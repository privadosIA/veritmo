package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/music/musicmotion"
	"veritmo/internal/platform/settings"
	"veritmo/internal/platform/settingsdb"
)

func TestRunCORS(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := runCORS(args, &out, &errb)
		return code, out.String(), errb.String()
	}
	if code, out, _ := run("add", "http://192.168.1.50:4200/"); code != 0 || !strings.Contains(out, "allowed: http://192.168.1.50:4200") {
		t.Fatalf("add: %d %q", code, out)
	}
	if code, out, _ := run("add", "--network", "192.168.1.0/24"); code != 0 || !strings.Contains(out, "192.168.1.0/24") {
		t.Fatalf("add network: %d %q", code, out)
	}
	if _, out, _ := run("add", "http://192.168.1.50:4200"); !strings.Contains(out, "already allowed") {
		t.Fatalf("repeat: %q", out)
	}
	_, out, _ := run("list")
	if !strings.Contains(out, "origins (1)") || !strings.Contains(out, "networks (1)") {
		t.Fatalf("list: %q", out)
	}
	if code, _, errs := run("add", "nope"); code != 1 || !strings.Contains(errs, "not an origin") {
		t.Fatalf("bad origin: %d %q", code, errs)
	}
	if code, out, _ := run("remove", "http://192.168.1.50:4200"); code != 0 || !strings.Contains(out, "removed") {
		t.Fatalf("remove: %d %q", code, out)
	}
	if code, _, _ := run(); code != 2 {
		t.Fatalf("no args: %d", code)
	}
}

func TestRunCORS_TheOriginIsKeptInTheDatabaseAndTheServerSeesIt(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("HOME", cfg)
	work := t.TempDir()
	t.Chdir(work) // defaultDB is relative to the folder the command runs in
	db := filepath.Join(work, defaultDB)
	st, err := openStore(db, "projects", settingsZero())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	prevBlocks := musicmotion.BlockStore
	useDatabaseSettings(defaultDB, "projects", &warn)
	t.Cleanup(func() { settings.UseSource(nil); musicmotion.BlockStore = prevBlocks })

	var out, errb bytes.Buffer
	if code := runCORS([]string{"add", "http://192.168.1.50:4200"}, &out, &errb); code != 0 {
		t.Fatalf("add: %d %s %s", code, out.String(), errb.String())
	}
	// the server, with its own handle on the same database, finds it; the settings file was not written
	got, err := settingsdb.New(st).Load()
	if err != nil || len(got.CORSOrigins) != 1 || got.CORSOrigins[0] != "http://192.168.1.50:4200" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := os.Stat(settings.Path()); !os.IsNotExist(err) {
		t.Fatalf("settings.json was written: %v", err)
	}
	if warn.Len() != 0 {
		t.Fatalf("warning: %s", warn.String())
	}
	// an origin the server saved is listed by the command
	f, _ := settingsdb.New(st).Load()
	f.CORSNetworks = []string{"100.64.0.0/10"}
	if err := settingsdb.New(st).Save(f); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := runCORS([]string{"list"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "100.64.0.0/10") {
		t.Fatalf("list: %d %q", code, out.String())
	}
}

func TestSettingsSource_WithoutADatabaseTheSettingsAreTheFileAndAnUnusableOneIsToldOnceAndNeverSavedOver(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("HOME", cfg)
	t.Chdir(t.TempDir())
	var warn bytes.Buffer
	prevBlocks := musicmotion.BlockStore
	useDatabaseSettings(defaultDB, "projects", &warn)
	t.Cleanup(func() { settings.UseSource(nil); musicmotion.BlockStore = prevBlocks })
	// no database file: the settings are the file, and nothing is created
	if err := settings.Save(settings.File{OllamaHost: "http://h:11434"}); err != nil {
		t.Fatal(err)
	}
	if settings.Load().OllamaHost != "http://h:11434" {
		t.Fatal("the settings file is not used while there is no database")
	}
	if _, err := os.Stat(defaultDB); !os.IsNotExist(err) {
		t.Fatalf("a database was created: %v", err)
	}
	// a database that exists and is not migrated cannot be used: readers get nothing and are told once, writers fail
	settings.UseSource(nil)
	if err := os.MkdirAll(filepath.Dir(defaultDB), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := openStore(defaultDB, "projects", settingsZero())
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	useDatabaseSettings(defaultDB, "projects", &warn)
	if got := settings.Load(); got.OllamaHost != "" {
		t.Fatalf("%+v", got)
	}
	_ = settings.Load()
	if n := strings.Count(warn.String(), "warning:"); n != 1 || !strings.Contains(warn.String(), "data migrate") {
		t.Fatalf("warnings: %q", warn.String())
	}
	if err := settings.SetRouting(map[string]string{"assistant": "x"}); err == nil {
		t.Fatal("a writer saved over settings it could not read")
	}
}

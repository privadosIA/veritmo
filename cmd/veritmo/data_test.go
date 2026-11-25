package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"veritmo/internal/platform/settings"
	"veritmo/internal/platform/store"
	"veritmo/internal/server"
)

// isolate keeps a test away from the real settings (and so from the real database the server of the machine uses).
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return home
}

func data(t *testing.T, args ...string) (code int, out, errOut string) {
	t.Helper()
	var o, e bytes.Buffer
	code = runData(args, &o, &e)
	return code, o.String(), e.String()
}

func TestData_StatusDoesNotMigrateAndMigrateDoes(t *testing.T) {
	isolate(t)
	db := filepath.Join(t.TempDir(), "o.db")
	code, out, errOut := data(t, "status", "--db", db)
	if code != 0 || !strings.Contains(out, "engine: sqlite") || !strings.Contains(out, "migrations pending (") || !strings.Contains(out, "migrations applied (0)") {
		t.Fatalf("%d\n%s\n%s", code, out, errOut)
	}
	if code, out, errOut := data(t, "migrate", "--db", db); code != 0 || !strings.Contains(out, fmt.Sprintf("applied %d migration(s)", len(server.DataRegistry("").Migrations()))) {
		t.Fatalf("%d\n%s\n%s", code, out, errOut)
	}
	if code, out, errOut := data(t, "migrate", "--db", db); code != 0 || !strings.Contains(out, "up to date") {
		t.Fatalf("a second migrate has nothing to do: %d\n%s\n%s", code, out, errOut)
	}
	code, out, _ = data(t, "status", "--db", db)
	if code != 0 || !strings.Contains(out, "migrations pending: none") || !strings.Contains(out, "runs") || !strings.Contains(out, "data_imports") {
		t.Fatalf("%d\n%s", code, out)
	}
	code, out, _ = data(t, "status", "--json", "--db", db)
	if code != 0 || !strings.Contains(out, `"engine": "sqlite"`) || !strings.Contains(out, `"legacy_files"`) {
		t.Fatalf("%d\n%s", code, out)
	}
}

func TestData_ExportThenRestoreIntoAnEmptyDatabase(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	a, b, zipFile := filepath.Join(dir, "a.db"), filepath.Join(dir, "b.db"), filepath.Join(dir, "x.zip")
	st, err := store.Open(a, settingsZero(), server.DataRegistry(""))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.Create(&store.DataImport{Entity: "test/e", State: "done", Created: 7}).Error; err != nil {
		t.Fatal(err)
	}
	st.Close()
	if code, out, errOut := data(t, "export", "--db", a, zipFile); code != 0 || !strings.Contains(out, "exported to") {
		t.Fatalf("%d\n%s\n%s", code, out, errOut)
	}
	if code, _, e := data(t, "migrate", "--db", b); code != 0 {
		t.Fatal(e)
	}
	if code, out, errOut := data(t, "restore", "--db", b, zipFile); code != 0 || !strings.Contains(out, "restored") {
		t.Fatalf("%d\n%s\n%s", code, out, errOut)
	}
	_, out, _ := data(t, "status", "--db", b)
	if !strings.Contains(out, "test/e") || !strings.Contains(out, "created 7") {
		t.Fatalf("the restored database must hold the record of the export:\n%s", out)
	}
	if _, err := os.Stat(zipFile + ".part"); !os.IsNotExist(err) {
		t.Fatal("no temporary file may be left")
	}
}

func TestData_RestoreIntoADatabaseWithRowsExitsNonZeroAndChangesNothing(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	a, zipFile := filepath.Join(dir, "a.db"), filepath.Join(dir, "x.zip")
	if code, _, e := data(t, "migrate", "--db", a); code != 0 {
		t.Fatal(e)
	}
	if code, _, e := data(t, "export", "--db", a, zipFile); code != 0 {
		t.Fatal(e)
	}
	// the same database now has a row in a table that is not the records of the imports
	st, _ := store.Open(a, settingsZero(), server.DataRegistry(""))
	var runs int64
	st.DB.Exec("INSERT INTO runs (id, project, state, created_at) VALUES ('r1', 'p', 'succeeded', datetime('now'))")
	st.DB.Table("runs").Count(&runs)
	st.Close()
	code, _, errOut := data(t, "restore", "--db", a, zipFile)
	if code == 0 || !strings.Contains(errOut, "already has rows") {
		t.Fatalf("%d %q", code, errOut)
	}
	st, _ = store.Open(a, settingsZero(), server.DataRegistry(""))
	defer st.Close()
	var n int64
	st.DB.Table("runs").Count(&n)
	if n != runs {
		t.Fatalf("the restore changed %d rows to %d", runs, n)
	}
}

func TestData_RollbackUndoesTheLastMigrationAndRefusesWhatCannotBeUndone(t *testing.T) {
	isolate(t)
	db := migrated(t)
	migs := server.DataRegistry("").Migrations()
	if len(migs) < 3 {
		t.Fatalf("the program has %d migrations", len(migs))
	}
	code, _, errOut := data(t, "rollback", "--db", db, migs[0].ID)
	if code == 0 || !strings.Contains(errOut, "not the last") {
		t.Fatalf("a migration that is not the last: %d %q", code, errOut)
	}
	// every migration but the first (the history, which cannot be undone) is undone from the last one back
	for i := len(migs) - 1; i >= 1; i-- {
		code, out, errOut := data(t, "rollback", "--db", db, migs[i].ID)
		if code != 0 || !strings.Contains(out, "undone") {
			t.Fatalf("%s: %d\n%s\n%s", migs[i].ID, code, out, errOut)
		}
		_, status, _ := data(t, "status", "--db", db)
		if !strings.Contains(status, migs[i].ID) || !strings.Contains(status, fmt.Sprintf("migrations pending (%d)", len(migs)-i)) {
			t.Fatalf("the next start applies it again:\n%s", status)
		}
	}
	// the history migration is now the last one applied and cannot be undone
	code, _, errOut = data(t, "rollback", "--db", db, migs[0].ID)
	if code == 0 || !strings.Contains(errOut, "cannot be undone") {
		t.Fatalf("%d %q", code, errOut)
	}
}

func TestData_RollbackIsRefusedWhileAServerRuns(t *testing.T) {
	home := isolate(t)
	db := filepath.Join(t.TempDir(), "o.db")
	if code, _, e := data(t, "migrate", "--db", db); code != 0 {
		t.Fatal(e)
	}
	migs := server.DataRegistry("").Migrations()
	lastID := migs[len(migs)-1].ID
	dir := filepath.Join(home, ".config", "veritmo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// the pid of this very process stands for a running server
	if err := os.WriteFile(filepath.Join(dir, "serve-4666.pid"), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := data(t, "rollback", "--db", db, lastID)
	if code == 0 || !strings.Contains(errOut, "server is running") {
		t.Fatalf("%d %q", code, errOut)
	}
	if err := os.WriteFile(filepath.Join(dir, "serve-4666.pid"), []byte("999999999"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := data(t, "rollback", "--db", db, lastID); code != 0 {
		t.Fatalf("a pid file of a process that is gone does not count: %d %q", code, errOut)
	}
}

func TestData_ADatabaseThatCannotBeOpenedFailsBeforeAnyWork(t *testing.T) {
	isolate(t)
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(file, "x.db") // its folder is a file
	for _, args := range [][]string{{"status"}, {"import"}, {"export", filepath.Join(t.TempDir(), "x.zip")}, {"rollback", "202611181210_data_imports"}} {
		code, out, errOut := data(t, append([]string{args[0], "--db", bad}, args[1:]...)...)
		if code == 0 || out != "" || !strings.Contains(errOut, "data "+args[0]) {
			t.Errorf("%v: %d %q %q", args, code, out, errOut)
		}
	}
}

func TestData_UsageErrorsExitWithTwo(t *testing.T) {
	isolate(t)
	for _, args := range [][]string{{}, {"nope"}, {"status", "extra"}, {"export"}, {"restore"}, {"rollback"}, {"import", "a", "b"}} {
		if code, _, _ := data(t, args...); code != 2 {
			t.Errorf("%v: exit %d", args, code)
		}
	}
	if code, _, errOut := data(t, "import", "--db", migrated(t), "no-such-entity"); code != 1 || !strings.Contains(errOut, "no such entity") {
		t.Errorf("an unknown entity: %d %q", code, errOut)
	}
}

func settingsZero() settings.Database { return settings.Database{} }

// migrated is a SQLite file with every migration applied.
func migrated(t *testing.T) string {
	t.Helper()
	db := filepath.Join(t.TempDir(), "o.db")
	if code, _, e := data(t, "migrate", "--db", db); code != 0 {
		t.Fatal(e)
	}
	return db
}

func TestData_ImportExportAndRestoreNeedAMigratedDatabaseAndSayWhatToDo(t *testing.T) {
	isolate(t)
	db := filepath.Join(t.TempDir(), "o.db")
	for _, args := range [][]string{{"import"}, {"export", filepath.Join(t.TempDir(), "x.zip")}, {"restore", filepath.Join(t.TempDir(), "x.zip")}} {
		code, out, errOut := data(t, append([]string{args[0], "--db", db}, args[1:]...)...)
		if code == 0 || out != "" || !strings.Contains(errOut, "veritmo data migrate") {
			t.Errorf("%v: %d %q %q", args, code, out, errOut)
		}
	}
	// and none of them migrated anything
	_, out, _ := data(t, "status", "--db", db)
	if !strings.Contains(out, "migrations applied (0)") {
		t.Fatalf("only migrate changes the schema:\n%s", out)
	}
}

func TestData_ConnectionFlagsAreCheckedAndThePasswordComesFromTheEnvironment(t *testing.T) {
	isolate(t)
	for _, args := range [][]string{
		{"status", "--host", "db"},                         // flags without --driver
		{"status", "--driver", "sqlite"},                   // SQLite is the --db file
		{"status", "--driver", "mysql", "--database", "x"}, // no host
		{"status", "--driver", "oracle", "--host", "h"},    // unknown engine
	} {
		if code, _, errOut := data(t, args...); code != 2 || errOut == "" {
			t.Errorf("%v: %d %q", args, code, errOut)
		}
	}
	d, err := databaseFor("postgres", "db.example", 0, "app", "me")
	if err != nil || d.Port != 5432 || d.Host != "db.example" {
		t.Fatalf("%+v %v", d, err)
	}
	t.Setenv("VERITMO_DB_PASSWORD", "s3cret")
	d, _ = databaseFor("mysql", "db.example", 3306, "app", "me")
	if d.Password != "s3cret" {
		t.Fatal("the password is read from VERITMO_DB_PASSWORD")
	}
	// no flag takes a password
	if code, _, _ := data(t, "status", "--password", "x"); code != 2 {
		t.Fatalf("there must be no --password flag: %d", code)
	}
}

func TestServe_RefusesToStartWhileMigrationsArePendingAndNamesTheCommand(t *testing.T) {
	isolate(t)
	t.Setenv("ORCHESTRATOR_TOKEN", "")
	var out, errb bytes.Buffer
	db := filepath.Join(t.TempDir(), "o.db")
	code := runServe([]string{"--addr", "127.0.0.1:0", "--token", "t", "--projects-dir", t.TempDir(), "--db", db}, &out, &errb)
	if code != 1 || !strings.Contains(errb.String(), "veritmo data migrate") || out.Len() != 0 {
		t.Fatalf("%d %q %q", code, errb.String(), out.String())
	}
	_, status, _ := data(t, "status", "--db", db)
	if !strings.Contains(status, "migrations applied (0)") {
		t.Fatalf("the server must not have migrated:\n%s", status)
	}
}

func TestData_ADatabaseAskedForByTheEnvironmentThatIsNotValidIsAnErrorNotAFile(t *testing.T) {
	isolate(t)
	db := filepath.Join(t.TempDir(), "o.db")
	t.Setenv("VERITMO_DB_DRIVER", "mysql")
	t.Setenv("VERITMO_DB_HOST", "")
	t.Setenv("VERITMO_DB_PASSWORD", "secret-in-env")
	code, out, errOut := data(t, "status", "--db", db)
	if code == 0 || !strings.Contains(errOut, "VERITMO_DB_") || strings.Contains(errOut+out, "secret-in-env") {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
	if _, err := os.Stat(db); err == nil {
		t.Fatal("a SQLite file was made although the environment asked for a MySQL")
	}
	// with the variables unset the SQLite file works as before
	t.Setenv("VERITMO_DB_DRIVER", "")
	if code, _, e := data(t, "migrate", "--db", db); code != 0 {
		t.Fatalf("%s", e)
	}
}

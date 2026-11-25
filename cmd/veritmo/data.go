package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"veritmo/internal/platform/legacyname"

	"veritmo/internal/platform/settings"
	"veritmo/internal/platform/store"
	"veritmo/internal/server"
)

const dataUsage = `Usage: veritmo data <command> [connection flags] [arguments]

  status [--json]            The engine, the migrations applied and pending, the rows of each table, the imports and the state
                             files left behind. It changes nothing.
  migrate                    Applies the migrations the database has not applied, in order. THIS is how the schema changes: the
                             server never migrates, it refuses to start while migrations are pending. The deployment workflow
                             (.gitea/workflows/migrar.yml) runs it; run it by hand on a local database.
  import [entity]            Imports the state that was kept in files into the database (every entity not imported yet, or the
                             one named, again). Files that have no row become rows; rows that exist are never changed.
  export <file>              Writes the data as a .zip (one JSON-lines file per table and a manifest). No secret, no media.
  restore <file>             Loads an export into an empty database of any engine. It refuses a database that has rows and an
                             export made by a newer version.
  rollback <migration>       Undoes the last migration applied (it must be named, and be one that can be undone). Refused while a
                             server runs on a SQLite database.

import, export and restore need a migrated database and say so when it is not (run migrate first).

Connection: the database saved in Settings (SQLite, MySQL or Postgres) unless --driver names another: then
--driver mysql|postgres --host H [--port P] --database NAME --user U, with the password in the environment variable VERITMO_DB_PASSWORD
(never as a flag: a command line is visible to every user of the machine). --db is the SQLite file when no other database applies
(default .orchestrator/orchestrator.db, the one serve uses). The commands work while a server runs on the same SQLite file.`

// runData implements `veritmo data` (spec cli, "The command line manages the data").
func runData(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, dataUsage)
		return 2
	}
	cmd := args[0]
	switch cmd {
	case "status", "migrate", "import", "export", "restore", "rollback":
	default:
		fmt.Fprintf(stderr, "data: unknown command %q\n\n%s\n", cmd, dataUsage)
		return 2
	}
	fs := flag.NewFlagSet("data "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", ".orchestrator/orchestrator.db", "SQLite file (used when no other database is saved in Settings)")
	projectsDir := fs.String("projects-dir", "projects", "Folder of the projects: where the state of the pieces is imported from")
	asJSON := fs.Bool("json", false, "Print the status as JSON")
	driver := fs.String("driver", "", "mysql or postgres: connect to this database instead of the one saved in Settings")
	host := fs.String("host", "", "Host of the database server (with --driver)")
	port := fs.Int("port", 0, "Port of the database server (with --driver; the engine's usual one by default)")
	dbName := fs.String("database", "", "Name of the database (with --driver)")
	dbUser := fs.String("user", "", "User of the database (with --driver); the password is VERITMO_DB_PASSWORD")
	fs.Usage = func() { fmt.Fprintln(stderr, dataUsage); fs.PrintDefaults() }
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	rest := fs.Args()
	switch cmd {
	case "status", "migrate":
		if len(rest) != 0 {
			fs.Usage()
			return 2
		}
	case "import":
		if len(rest) > 1 {
			fs.Usage()
			return 2
		}
	default:
		if len(rest) != 1 {
			fs.Usage()
			return 2
		}
	}
	d, err := databaseFor(*driver, *host, *port, *dbName, *dbUser)
	if err != nil {
		fmt.Fprintf(stderr, "data %s: %v\n", cmd, err)
		return 2
	}
	// Only migrate (and rollback) change the schema. The others need the schema to be there, and say so when it is not.
	st, err := openStore(*dbPath, *projectsDir, d)
	if err != nil {
		fmt.Fprintf(stderr, "data %s: %v\n", cmd, err)
		return 1
	}
	if cmd == "import" || cmd == "export" || cmd == "restore" {
		if err := st.CheckMigrated(); err != nil {
			_ = st.Close()
			fmt.Fprintf(stderr, "data %s: %v\n", cmd, err)
			return 1
		}
	}
	defer func() { _ = st.Close() }()
	switch cmd {
	case "status":
		return dataStatus(st, *asJSON, stdout, stderr)
	case "migrate":
		return dataMigrate(st, stdout, stderr)
	case "import":
		entity := ""
		if len(rest) == 1 {
			entity = rest[0]
		}
		return dataImport(st, entity, stdout, stderr)
	case "export":
		return dataExport(st, rest[0], stdout, stderr)
	case "restore":
		return dataRestore(st, rest[0], stdout, stderr)
	default:
		return dataRollback(st, rest[0], stdout, stderr)
	}
}

// databaseFor is the database a command works on: the one the flags name (the password from VERITMO_DB_PASSWORD), or the one saved in Settings.
func databaseFor(driver, host string, port int, name, user string) (settings.Database, error) {
	if driver == "" {
		if host != "" || port != 0 || name != "" || user != "" {
			return settings.Database{}, errors.New("--host, --port, --database and --user go with --driver")
		}
		return settings.GetDatabase(), nil
	}
	d, err := settings.Database{Driver: driver, Host: host, Port: port, Name: name, User: user, Password: legacyname.Getenv("DB_PASSWORD")}.Validate()
	if err != nil {
		return d, err
	}
	if !d.IsRemote() {
		return d, errors.New("--driver is mysql or postgres: SQLite is the file of --db")
	}
	return d, nil
}

// openStore opens the database a command works on: d when it is a MySQL or Postgres, or the SQLite file at dbPath. It creates and changes no
// table (see dataMigrate). A database that cannot be opened is an error that names it, before any work is done.
func openStore(dbPath, projectsDir string, d settings.Database, opts ...server.DataOption) (*store.Store, error) {
	if err := settings.CheckEnvDatabase(); err != nil { // a database asked for in the environment that is not valid is an error, never a silent SQLite file
		return nil, err
	}
	if !d.IsRemote() {
		if dir := filepath.Dir(dbPath); dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("cannot create the folder of the database %s: %w", dbPath, err)
			}
		}
	}
	return store.Open(dbPath, d, server.DataRegistry(projectsDir, opts...))
}

func dataMigrate(st *store.Store, stdout, stderr io.Writer) int {
	_, before, err := st.Migrations()
	if err != nil {
		fmt.Fprintf(stderr, "data migrate: %v\n", err)
		return 1
	}
	if err := st.Migrate(); err != nil {
		fmt.Fprintf(stderr, "data migrate: %v\n", err)
		return 1
	}
	if len(before) == 0 {
		fmt.Fprintf(stdout, "the %s database is up to date: no migration to apply\n", st.Driver)
		return 0
	}
	fmt.Fprintf(stdout, "applied %d migration(s) to the %s database:\n", len(before), st.Driver)
	for _, id := range before {
		fmt.Fprintf(stdout, "  %s\n", id)
	}
	return 0
}

func dataStatus(st *store.Store, asJSON bool, stdout, stderr io.Writer) int {
	status, err := st.Status()
	if err != nil {
		fmt.Fprintf(stderr, "data status: %v\n", err)
		return 1
	}
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(status)
		return 0
	}
	fmt.Fprintf(stdout, "engine: %s\n", status.Engine)
	fmt.Fprintf(stdout, "migrations applied (%d):\n", len(status.Migrations.Applied))
	for _, m := range status.Migrations.Applied {
		fmt.Fprintf(stdout, "  %s  %s\n", m.ID, m.AppliedAt.Format("2006-01-02 15:04:05"))
	}
	if len(status.Migrations.Pending) == 0 {
		fmt.Fprintln(stdout, "migrations pending: none")
	} else {
		fmt.Fprintf(stdout, "migrations pending (%d): %s\n", len(status.Migrations.Pending), strings.Join(status.Migrations.Pending, ", "))
	}
	fmt.Fprintln(stdout, "tables:")
	for _, t := range status.Tables {
		fmt.Fprintf(stdout, "  %-32s %d rows\n", t.Name, t.Rows)
	}
	fmt.Fprintln(stdout, "imports:")
	if len(status.Imports) == 0 {
		fmt.Fprintln(stdout, "  none yet")
	}
	for _, r := range status.Imports {
		fmt.Fprintf(stdout, "  %-32s %-6s created %d, existing %d, skipped %d\n", r.Entity, r.State, r.Created, r.Existing, len(r.Skipped))
		for _, s := range r.Skipped {
			fmt.Fprintf(stdout, "      skipped %s: %s\n", s.Path, s.Reason)
		}
		if r.Error != "" {
			fmt.Fprintf(stdout, "      error: %s\n", r.Error)
		}
	}
	fmt.Fprintln(stdout, "state files left behind:")
	if len(status.LegacyFiles) == 0 {
		fmt.Fprintln(stdout, "  none")
	}
	for _, l := range status.LegacyFiles {
		fmt.Fprintf(stdout, "  %-32s %d files, %d bytes\n", l.Entity, l.Files, l.Bytes)
	}
	return 0
}

func dataImport(st *store.Store, entity string, stdout, stderr io.Writer) int {
	recs, err := st.RunImports(context.Background(), entity)
	if err != nil {
		fmt.Fprintf(stderr, "data import: %v\n", err)
		return 1
	}
	if len(recs) == 0 {
		fmt.Fprintln(stdout, "nothing to import: every entity has been imported (name one to run it again)")
		return 0
	}
	code := 0
	for _, r := range recs {
		fmt.Fprintf(stdout, "%s: %s, created %d, existing %d, skipped %d\n", r.Entity, r.State, r.Created, r.Existing, len(r.Skipped))
		for _, s := range r.Skipped {
			fmt.Fprintf(stdout, "  skipped %s: %s\n", s.Path, s.Reason)
		}
		if r.State != store.ImportDone {
			fmt.Fprintf(stderr, "%s: %s\n", r.Entity, r.Error)
			code = 1
		}
	}
	return code
}

func dataExport(st *store.Store, file string, stdout, stderr io.Writer) int {
	tmp := file + ".part"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		fmt.Fprintf(stderr, "data export: %v\n", err)
		return 1
	}
	err = st.Export(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, file)
	}
	if err != nil {
		_ = os.Remove(tmp)
		fmt.Fprintf(stderr, "data export: %v\n", err)
		return 1
	}
	fi, _ := os.Stat(file)
	fmt.Fprintf(stdout, "exported to %s (%d bytes)\n", file, fi.Size())
	return 0
}

func dataRestore(st *store.Store, file string, stdout, stderr io.Writer) int {
	f, err := os.Open(file)
	if err != nil {
		fmt.Fprintf(stderr, "data restore: %v\n", err)
		return 1
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		fmt.Fprintf(stderr, "data restore: %v\n", err)
		return 1
	}
	if err := st.Restore(f, fi.Size()); err != nil {
		fmt.Fprintf(stderr, "data restore: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "restored from %s\n", file)
	return 0
}

func dataRollback(st *store.Store, id string, stdout, stderr io.Writer) int {
	if st.Driver == settings.DBSQLite {
		if pid, ok := serverRunning(); ok {
			fmt.Fprintf(stderr, "data rollback: a server is running (pid %d): stop it before undoing a migration of the SQLite database\n", pid)
			return 1
		}
	}
	if err := st.Rollback(id); err != nil {
		fmt.Fprintf(stderr, "data rollback: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "undone: %s (it is applied again the next time a program opens the database)\n", id)
	return 0
}

// serverRunning says whether a server of this machine is running, from the pid files scripts/serve.sh writes next to the settings.
func serverRunning() (int, bool) {
	files, _ := filepath.Glob(filepath.Join(settings.Dir(), "serve-*.pid"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil || pid <= 0 {
			continue
		}
		if err := syscall.Kill(pid, 0); err == nil || errors.Is(err, syscall.EPERM) {
			return pid, true
		}
	}
	return 0, false
}

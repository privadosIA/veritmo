package store

// schema.go: the schema changes only through versioned migrations (what Flyway does in Java), run with gormigrate. A migration
// has an id that orders it by date, is applied once, in order, when the program opens the database, is recorded in the table
// schema_migrations with the time it was applied, and can be undone with Rollback (only `veritmo data rollback` does it).
// AutoMigrate is never called to create or change a table of the data layer.

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// Migration is one change of the schema. It is gormigrate's own type.
type Migration = gormigrate.Migration

// ErrCannotUndo is what the Rollback of a migration that cannot be undone returns: one that transforms data, or that
// creates tables that already existed in the databases deployed before it.
var ErrCannotUndo = errors.New("this migration cannot be undone")

// ErrUnknownMigrations means the database has applied migrations this program does not have: it was changed by a newer
// version.
var ErrUnknownMigrations = errors.New("the database has migrations this program does not know")

// CannotUndo is the Rollback of a migration that cannot be undone.
func CannotUndo(*gorm.DB) error { return ErrCannotUndo }

const migrationsTable = "schema_migrations"

var reMigrationID = regexp.MustCompile(`^[0-9]{12}_[a-z0-9_]+$`)

func addMigration(list []*Migration, m *Migration) []*Migration {
	if m == nil || !reMigrationID.MatchString(m.ID) {
		id := ""
		if m != nil {
			id = m.ID
		}
		panic(fmt.Sprintf("store: the migration id %q must be YYYYMMDDHHMM_short_name", id))
	}
	if m.Migrate == nil {
		panic(fmt.Sprintf("store: the migration %s has no Migrate", m.ID))
	}
	for _, o := range list {
		if o.ID == m.ID {
			panic(fmt.Sprintf("store: the migration %s is registered twice", m.ID))
		}
	}
	return append(list, m)
}

// schemaMigration is the table gormigrate writes its ids to. gormigrate inserts only the id: applied_at is filled by stampApplied
// right after (a column default such as CURRENT_TIMESTAMP is not the same in the three engines: MySQL refuses it on datetime(3)).
type schemaMigration struct {
	ID        string `gorm:"primaryKey;size:255"`
	AppliedAt *time.Time
}

func (schemaMigration) TableName() string { return migrationsTable }

// AppliedMigration is a migration the database has applied.
type AppliedMigration struct {
	ID        string    `json:"id"`
	AppliedAt time.Time `json:"applied_at"`
}

// Migrate applies, in order, the migrations the database has not applied, all or nothing where the engine allows it (SQLite
// and Postgres; MySQL commits each DDL statement). It refuses a database that has applied migrations the program does not have.
// Only `veritmo data migrate` (which the deployment workflow runs) calls it in the program: the server never migrates, it
// checks with CheckMigrated. Two migrations at once, in this process or (MySQL, Postgres) in two, take turns: the second finds
// nothing left to apply.
func (s *Store) Migrate() error { return s.migrateWith(s.reg.Migrations()) }

// CheckMigrated says whether the database is ready for this program: nothing pending and nothing applied that the program does not know.
// The error tells what to do. It changes nothing.
func (s *Store) CheckMigrated() error { return s.checkMigratedWith(s.reg.Migrations()) }

func (s *Store) checkMigratedWith(list []*Migration) error {
	unknown, err := s.unknown(list)
	if err != nil {
		return err
	}
	if len(unknown) > 0 {
		return fmt.Errorf("%w: %s (the database was changed by a newer version: update this program)", ErrUnknownMigrations, strings.Join(unknown, ", "))
	}
	_, pending, err := s.migrationsWith(list)
	if err != nil {
		return err
	}
	if len(pending) > 0 {
		return fmt.Errorf("%w: %d pending (%s): run `veritmo data migrate` (the deployment workflow does) before starting", ErrNotMigrated, len(pending), strings.Join(pending, ", "))
	}
	return nil
}

// ErrNotMigrated is what a database with migrations not applied yet answers to CheckMigrated.
var ErrNotMigrated = errors.New("the database is not migrated")

// migrationLockName is the name of the lock two programs migrating the same MySQL or Postgres database take turns with.
const (
	migrationLockName = "veritmo_migrations"
	migrationLockKey  = 727274915 // the same lock for Postgres, which takes a number
)

// withMigrationLock runs fn while it holds the lock of the database: a named lock of MySQL or an advisory lock of Postgres, which belong to
// a connection (held here until fn ends). SQLite has the lock of its file.
func (s *Store) withMigrationLock(fn func() error) error {
	s.migrating.Lock()
	defer s.migrating.Unlock()
	switch s.Driver {
	case "mysql", "postgres":
		return s.DB.Connection(func(c *gorm.DB) error {
			if s.Driver == "mysql" {
				var got int
				if err := c.Raw("SELECT GET_LOCK(?, 120)", migrationLockName).Scan(&got).Error; err != nil || got != 1 {
					return fmt.Errorf("cannot take the migration lock (another program is migrating): %w", err)
				}
				defer c.Exec("SELECT RELEASE_LOCK(?)", migrationLockName)
			} else {
				if err := c.Exec("SELECT pg_advisory_lock(?)", migrationLockKey).Error; err != nil {
					return fmt.Errorf("cannot take the migration lock: %w", err)
				}
				defer c.Exec("SELECT pg_advisory_unlock(?)", migrationLockKey)
			}
			return fn()
		})
	}
	return fn()
}

func (s *Store) migrateWith(list []*Migration) error {
	return s.withMigrationLock(func() error { return s.migrateLocked(list) })
}

func (s *Store) migrateLocked(list []*Migration) error {
	if err := s.DB.AutoMigrate(&schemaMigration{}); err != nil {
		return fmt.Errorf("cannot create %s: %w", migrationsTable, err)
	}
	if unknown, err := s.unknown(list); err != nil {
		return err
	} else if len(unknown) > 0 {
		return fmt.Errorf("%w: %s (the database was changed by a newer version: update this program)", ErrUnknownMigrations, strings.Join(unknown, ", "))
	}
	m := gormigrate.New(s.DB, &gormigrate.Options{TableName: migrationsTable, UseTransaction: true, ValidateUnknownMigrations: true}, list)
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("cannot migrate the database: %w", err)
	}
	now := time.Now().UTC()
	if err := s.DB.Model(&schemaMigration{}).Where("applied_at IS NULL").Update("applied_at", now).Error; err != nil {
		return fmt.Errorf("cannot record when the migrations were applied: %w", err)
	}
	return nil
}

// unknown lists the applied migrations that are not in list.
func (s *Store) unknown(list []*Migration) ([]string, error) {
	applied, err := s.applied()
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, m := range list {
		known[m.ID] = true
	}
	var out []string
	for _, a := range applied {
		if !known[a.ID] {
			out = append(out, a.ID)
		}
	}
	return out, nil
}

func (s *Store) applied() ([]AppliedMigration, error) {
	if !s.DB.Migrator().HasTable(&schemaMigration{}) {
		return nil, nil
	}
	var rows []schemaMigration
	if err := s.DB.Order("id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", migrationsTable, err)
	}
	out := make([]AppliedMigration, len(rows))
	for i, r := range rows {
		out[i] = AppliedMigration{ID: r.ID}
		if r.AppliedAt != nil {
			out[i].AppliedAt = r.AppliedAt.UTC()
		}
	}
	return out, nil
}

// Migrations says which migrations the database has applied, oldest first, and the ids of the ones it has not (the program has
// them and the database does not).
func (s *Store) Migrations() (applied []AppliedMigration, pending []string, err error) {
	return s.migrationsWith(s.reg.Migrations())
}

func (s *Store) migrationsWith(list []*Migration) ([]AppliedMigration, []string, error) {
	applied, err := s.applied()
	if err != nil {
		return nil, nil, err
	}
	done := map[string]bool{}
	for _, a := range applied {
		done[a.ID] = true
	}
	var pending []string
	for _, m := range list {
		if !done[m.ID] {
			pending = append(pending, m.ID)
		}
	}
	return applied, pending, nil
}

// Rollback undoes the migration id, which must be the last one applied and one that can be undone, and removes its record.
func (s *Store) Rollback(id string) error { return s.rollbackWith(s.reg.Migrations(), id) }

func (s *Store) rollbackWith(list []*Migration, id string) error {
	applied, err := s.applied()
	if err != nil {
		return err
	}
	if len(applied) == 0 {
		return errors.New("no migration has been applied: there is nothing to undo")
	}
	if unknown, err := s.unknown(list); err != nil {
		return err
	} else if len(unknown) > 0 {
		return fmt.Errorf("%w: %s", ErrUnknownMigrations, strings.Join(unknown, ", "))
	}
	last := applied[len(applied)-1].ID
	if id != last {
		return fmt.Errorf("%s is not the last migration applied (that is %s): only the last one can be undone", id, last)
	}
	var target *Migration
	for _, m := range list {
		if m.ID == id {
			target = m
		}
	}
	if target == nil {
		return fmt.Errorf("the program has no migration %s", id)
	}
	if target.Rollback == nil {
		return fmt.Errorf("%s: %w", id, ErrCannotUndo)
	}
	m := gormigrate.New(s.DB, &gormigrate.Options{TableName: migrationsTable, UseTransaction: true}, list)
	if err := m.RollbackMigration(target); err != nil {
		if errors.Is(err, ErrCannotUndo) {
			return fmt.Errorf("%s: %w", id, ErrCannotUndo)
		}
		return fmt.Errorf("cannot undo %s: %w", id, err)
	}
	return nil
}

// CheckSchema says what the registered models have that the database does not: a table, a column or an index. It is empty when
// every migration needed has been written. The tests of each area call it after Migrate: a model changed without a migration fails.
func (s *Store) CheckSchema() ([]string, error) { return checkSchema(s.DB, s.reg.Models()) }

func checkSchema(db *gorm.DB, models []any) ([]string, error) {
	var missing []string
	mig := db.Migrator()
	for _, model := range models {
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(model); err != nil {
			return nil, fmt.Errorf("cannot read the model %T: %w", model, err)
		}
		table := stmt.Schema.Table
		if !mig.HasTable(model) {
			missing = append(missing, "table "+table)
			continue
		}
		for _, f := range stmt.Schema.Fields {
			if f.DBName != "" && !mig.HasColumn(model, f.DBName) {
				missing = append(missing, "column "+table+"."+f.DBName)
			}
		}
		for _, idx := range stmt.Schema.ParseIndexes() {
			if !mig.HasIndex(model, idx.Name) {
				missing = append(missing, "index "+table+"."+idx.Name)
			}
		}
	}
	return missing, nil
}

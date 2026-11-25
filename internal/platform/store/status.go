package store

import (
	"fmt"
)

// TableRows is a table and how many rows it has.
type TableRows struct {
	Name string `json:"name"`
	Rows int64  `json:"rows"`
}

// MigrationsStatus says which migrations the database has applied and which it has not.
type MigrationsStatus struct {
	Applied []AppliedMigration `json:"applied"`
	Pending []string           `json:"pending"`
}

// Status is the answer of GET /api/data/status and of `veritmo data status`.
type Status struct {
	Engine      string           `json:"engine"`
	Migrations  MigrationsStatus `json:"migrations"`
	Tables      []TableRows      `json:"tables"`
	Imports     []ImportRecord   `json:"imports"`
	LegacyFiles []LegacyStatus   `json:"legacy_files"`
}

// TableCounts counts the rows of every registered table.
func (s *Store) TableCounts() ([]TableRows, error) {
	tables, err := tablesOf(s.DB, s.reg.Models())
	if err != nil {
		return nil, err
	}
	out := make([]TableRows, 0, len(tables))
	for _, t := range tables {
		var n int64
		if !s.DB.Migrator().HasTable(t.model) {
			// a migration that has not been applied yet (or was undone): the table is not there, which the migrations show
			out = append(out, TableRows{Name: t.name()})
			continue
		}
		if err := s.DB.Model(t.model).Count(&n).Error; err != nil {
			return nil, fmt.Errorf("cannot count %s: %w", t.name(), err)
		}
		out = append(out, TableRows{Name: t.name(), Rows: n})
	}
	return out, nil
}

// Status reads the engine, the migrations, the rows of each table, the record of each import and the state files left behind.
func (s *Store) Status() (*Status, error) {
	applied, pending, err := s.Migrations()
	if err != nil {
		return nil, err
	}
	if applied == nil {
		applied = []AppliedMigration{}
	}
	if pending == nil {
		pending = []string{}
	}
	tables, err := s.TableCounts()
	if err != nil {
		return nil, err
	}
	imports, err := s.ImportRecords()
	if err != nil {
		return nil, err
	}
	if imports == nil {
		imports = []ImportRecord{}
	}
	legacy, err := s.LegacyStatuses()
	if err != nil {
		return nil, err
	}
	return &Status{Engine: s.Driver, Migrations: MigrationsStatus{Applied: applied, Pending: pending}, Tables: tables, Imports: imports, LegacyFiles: legacy}, nil
}

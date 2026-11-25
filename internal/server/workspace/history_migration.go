package workspace

// history_migration.go: the migration that creates the tables of the run history. The structs are copied here as they were when it
// was written (design D4 of add-data-layer): the migration must do the same whatever Run and the others become, and a later change
// of a table is a migration of its own. The tables already exist in every database deployed before the data layer, so it creates
// only those that are missing, and it cannot be undone (it would drop history it did not create).

import (
	"time"

	"gorm.io/gorm"

	"veritmo/internal/platform/store"
)

func historyMigration() *store.Migration {
	return &store.Migration{
		ID:       "202611181200_history",
		Migrate:  migrateHistory,
		Rollback: store.CannotUndo,
	}
}

func migrateHistory(tx *gorm.DB) error {
	type Run struct {
		ID         string `gorm:"primaryKey;size:64"`
		Project    string `gorm:"size:191;index"`
		State      string `gorm:"size:32;index"`
		Server     string `gorm:"size:100;index"`
		GPU        string `gorm:"size:191"`
		KeepTmp    bool
		Force      bool
		Only       string `gorm:"size:191"`
		VRAMWarnMB int
		Overlap    int
		Mode       string `gorm:"size:32"`
		Client     string `gorm:"size:32;index"`
		CreatedAt  time.Time
		StartedAt  *time.Time
		FinishedAt *time.Time
		Artifact   string `gorm:"size:255"`
		Error      string
	}
	type RunEvent struct {
		ID    uint   `gorm:"primaryKey"`
		RunID string `gorm:"size:64;index:idx_run_seq,unique,priority:1"`
		Seq   int    `gorm:"index:idx_run_seq,unique,priority:2"`
		Line  string
	}
	type RunSample struct {
		ID          uint   `gorm:"primaryKey"`
		RunID       string `gorm:"size:64;index"`
		TS          time.Time
		GPU         int
		VRAMUsedMB  int `gorm:"column:vram_used_mb"`
		VRAMTotalMB int `gorm:"column:vram_total_mb"`
		UtilPct     int
		RAMUsedMB   int
		RAMTotalMB  int
	}
	type Review struct {
		ID         string `gorm:"primaryKey;size:64"`
		Project    string `gorm:"size:191;index"`
		State      string `gorm:"size:32;index"`
		Server     string `gorm:"size:100;index"`
		Model      string `gorm:"size:191"`
		Total      int
		Done       int
		Notes      string
		Error      string
		CreatedAt  time.Time
		FinishedAt *time.Time
	}
	type Finding struct {
		ID          string `gorm:"primaryKey;size:64"`
		ReviewID    string `gorm:"size:64;index"`
		Scene       string
		Kind        string
		Source      string
		FromSeconds float64
		Severity    int
		Description string
		HasFrame    bool
		FixKind     string
		FixParams   string
		Status      string
		RunID       string
		Message     string
		CreatedAt   time.Time
		Seq         int
	}
	type CalibrationSample struct {
		ID         uint   `gorm:"primaryKey"`
		RunID      string `gorm:"size:64;index"`
		Server     string `gorm:"size:100;index"`
		GPU        string `gorm:"size:191;index"`
		Kind       string `gorm:"size:32"`
		Expected   float64
		Actual     float64
		Ratio      float64
		At         time.Time
		Model      string `gorm:"size:191;index"`
		Resolution string `gorm:"size:32"`
		Steps      int
		Frames     int
		Units      int
		Succeeded  bool
	}
	m := tx.Migrator()
	for _, model := range []any{&Run{}, &RunEvent{}, &RunSample{}, &Review{}, &Finding{}, &CalibrationSample{}} {
		if m.HasTable(model) {
			continue
		}
		if err := m.CreateTable(model); err != nil {
			return err
		}
	}
	return nil
}

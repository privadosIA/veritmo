package musicmotion

// history.go: what the renders of this machine took against what was forecast (spec motion-presets, "The times of the renders are kept in the
// database"). The estimate of a piece starts from it. Two implementations: the file the program used to keep, and the database.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"gorm.io/gorm"

	"veritmo/internal/platform/store"
)

// HistoryStore keeps the ratios between the time a render took and what the uncorrected forecast said, the last historyKeep of them.
type HistoryStore interface {
	// Ratios are the kept ratios, the oldest first.
	Ratios() []float64
	// Add keeps one more ratio and forgets the oldest ones beyond historyKeep.
	Add(ratio float64) error
}

// HistoryPath is the file that keeps the history in a folder.
func HistoryPath(dir string) string { return filepath.Join(dir, "render-times.json") }

// DirHistory is the history kept in a file of dir; nil for an empty dir (no history).
func DirHistory(dir string) HistoryStore {
	if dir == "" {
		return nil
	}
	return dirHistory{dir: dir}
}

type dirHistory struct{ dir string }

// history is the content of the file.
type history struct {
	Ratios []float64 `json:"ratios"`
}

func (d dirHistory) load() history {
	var h history
	if raw, err := os.ReadFile(HistoryPath(d.dir)); err == nil {
		_ = json.Unmarshal(raw, &h)
	}
	return h
}

func (d dirHistory) Ratios() []float64 { return d.load().Ratios }

func (d dirHistory) Add(ratio float64) error {
	h := d.load()
	h.Ratios = append(h.Ratios, ratio)
	if len(h.Ratios) > historyKeep {
		h.Ratios = h.Ratios[len(h.Ratios)-historyKeep:]
	}
	data, err := json.Marshal(h)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d.dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(HistoryPath(d.dir), data, 0o644)
}

// ratioRow is one ratio as the database keeps it.
type ratioRow struct {
	ID        uint `gorm:"primaryKey;autoIncrement"`
	Ratio     float64
	CreatedAt time.Time
}

// TableName is the table of the ratios.
func (ratioRow) TableName() string { return "mm_render_ratios" }

func historyMigration() *store.Migration {
	return &store.Migration{
		ID: "202611191400_mm_render_ratios",
		Migrate: func(tx *gorm.DB) error {
			type ratioRow struct {
				ID        uint `gorm:"primaryKey;autoIncrement"`
				Ratio     float64
				CreatedAt time.Time
			}
			if tx.Migrator().HasTable("mm_render_ratios") {
				return nil
			}
			return tx.Table("mm_render_ratios").Migrator().CreateTable(&ratioRow{})
		},
		Rollback: func(tx *gorm.DB) error { return tx.Migrator().DropTable("mm_render_ratios") },
	}
}

// DBHistory is the history kept in the database.
func DBHistory(st *store.Store) HistoryStore { return dbHistory{st: st} }

type dbHistory struct{ st *store.Store }

func (d dbHistory) Ratios() []float64 {
	var rows []ratioRow
	if err := d.st.DB.Order("id DESC").Limit(historyKeep).Find(&rows).Error; err != nil {
		return nil
	}
	out := make([]float64, len(rows))
	for i, r := range rows {
		out[len(rows)-1-i] = r.Ratio // the oldest first
	}
	return out
}

func (d dbHistory) Add(ratio float64) error {
	return d.st.Tx(func(tx *store.Store) error {
		if err := tx.DB.Create(&ratioRow{Ratio: ratio, CreatedAt: time.Now().UTC()}).Error; err != nil {
			return err
		}
		var keep []uint
		if err := tx.DB.Model(&ratioRow{}).Order("id DESC").Limit(historyKeep).Pluck("id", &keep).Error; err != nil {
			return err
		}
		return tx.DB.Where("id NOT IN ?", keep).Delete(&ratioRow{}).Error
	})
}

// historyImporter reads render-times.json from the folder of the music motion state: entity music-motion/render-times. The ratios are kept only
// when the database has none yet (nothing is overwritten).
type historyImporter struct{ dir string }

func (historyImporter) Entity() string { return "music-motion/render-times" }

func (i historyImporter) Import(ctx context.Context, tx *store.Store) (store.Report, error) {
	var rep store.Report
	var n int64
	if err := tx.DB.Model(&ratioRow{}).Count(&n).Error; err != nil {
		return rep, err
	}
	if n > 0 {
		rep.Existing = int(n)
		return rep, nil
	}
	path := HistoryPath(i.dir)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return rep, nil
		}
		return rep, err
	}
	var h history
	if json.Unmarshal(raw, &h) != nil {
		rep.Skipped = append(rep.Skipped, store.Skipped{Path: path, Reason: "render-times.json is not valid"})
		return rep, nil
	}
	if len(h.Ratios) > historyKeep {
		h.Ratios = h.Ratios[len(h.Ratios)-historyKeep:]
	}
	now := time.Now().UTC()
	for k, r := range h.Ratios { // in order, so that the ids keep the age
		if err := tx.DB.Create(&ratioRow{Ratio: r, CreatedAt: now.Add(time.Duration(k) * time.Millisecond)}).Error; err != nil {
			return rep, err
		}
		rep.Created++
	}
	return rep, nil
}

func (i historyImporter) LegacyFiles() ([]store.LegacyFile, error) {
	st, err := os.Stat(HistoryPath(i.dir))
	if err != nil || !st.Mode().IsRegular() {
		return nil, nil
	}
	return []store.LegacyFile{{Path: HistoryPath(i.dir), Bytes: st.Size()}}, nil
}

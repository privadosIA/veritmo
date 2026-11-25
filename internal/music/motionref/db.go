package motionref

// db.go: the references, the learnings and the styles kept in the database (spec motion-references, motion-learning and motion-presets). Each is a
// row with what a list filters by as columns and the record as a document; the key frames of a reference stay files under its folder.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"gorm.io/gorm"

	"veritmo/internal/music/musicmotion"
	"veritmo/internal/platform/store"
)

// Folders are the folders the three stores were kept in before the database: what the importers read.
type Folders struct {
	References, Learnings, Presets string
}

// UserFolders are the folders under the settings folder (settings.Dir) where the stores kept their state.
func UserFolders(settingsDir string) Folders {
	return Folders{References: filepath.Join(settingsDir, "references"), Learnings: filepath.Join(settingsDir, "music-motion", "learnings"),
		Presets: filepath.Join(settingsDir, "music-motion", "presets")}
}

type referenceRow struct {
	ID        string    `gorm:"primaryKey;size:64"`
	State     string    `gorm:"size:32;index"`
	Title     string    `gorm:"size:255"`
	Video     string    `gorm:"size:1000"`
	CreatedAt time.Time `gorm:"index"`
	UpdatedAt time.Time
	Doc       store.Text
}

func (referenceRow) TableName() string { return "mm_references" }

type learningRow struct {
	ID          string    `gorm:"primaryKey;size:64"`
	ReferenceID string    `gorm:"size:64;index"`
	State       string    `gorm:"size:32;index"`
	CreatedAt   time.Time `gorm:"index"`
	UpdatedAt   time.Time
	Doc         store.Text
}

func (learningRow) TableName() string { return "mm_learnings" }

type presetRow struct {
	ID        string    `gorm:"primaryKey;size:64"`
	Name      string    `gorm:"size:255"`
	CreatedAt time.Time `gorm:"index"`
	UpdatedAt time.Time
	Doc       store.Text
}

func (presetRow) TableName() string { return "mm_presets" }

func motionrefMigration() *store.Migration {
	return &store.Migration{
		ID: "202611191300_mm_motionref",
		Migrate: func(tx *gorm.DB) error {
			type referenceRow struct {
				ID        string    `gorm:"primaryKey;size:64"`
				State     string    `gorm:"size:32;index"`
				Title     string    `gorm:"size:255"`
				Video     string    `gorm:"size:1000"`
				CreatedAt time.Time `gorm:"index"`
				UpdatedAt time.Time
				Doc       store.Text
			}
			type learningRow struct {
				ID          string    `gorm:"primaryKey;size:64"`
				ReferenceID string    `gorm:"size:64;index"`
				State       string    `gorm:"size:32;index"`
				CreatedAt   time.Time `gorm:"index"`
				UpdatedAt   time.Time
				Doc         store.Text
			}
			type presetRow struct {
				ID        string    `gorm:"primaryKey;size:64"`
				Name      string    `gorm:"size:255"`
				CreatedAt time.Time `gorm:"index"`
				UpdatedAt time.Time
				Doc       store.Text
			}
			for table, model := range map[string]any{"mm_references": &referenceRow{}, "mm_learnings": &learningRow{}, "mm_presets": &presetRow{}} {
				if tx.Migrator().HasTable(table) {
					continue
				}
				if err := tx.Table(table).Migrator().CreateTable(model); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			for _, t := range []string{"mm_references", "mm_learnings", "mm_presets"} {
				if err := tx.Migrator().DropTable(t); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// RegisterStore tells a registry what the references, the learnings and the styles own in the database (server.DataRegistry calls it).
func RegisterStore(r *store.Registry, dirs Folders) {
	r.AddModels(&referenceRow{}, &learningRow{}, &presetRow{})
	r.AddMigration(motionrefMigration())
	r.AddImporter(referencesImporter{dir: dirs.References})
	r.AddImporter(learningsImporter{dir: dirs.Learnings})
	r.AddImporter(presetsImporter{dir: dirs.Presets})
}

// --- references -----------------------------------------------------------------------------------

// NewStoreDB makes the library of references on the database; dir is where their key frames are kept, a folder each.
func NewStoreDB(dir string, st *store.Store) *Store { return &Store{Dir: dir, db: st} }

func referenceDoc(ref Reference) (store.Text, error) {
	b, err := json.MarshalIndent(ref, "", "  ")
	return store.Text(b), err
}

func (s *Store) dbCreate(ref Reference) error {
	doc, err := referenceDoc(ref)
	if err != nil {
		return err
	}
	row := referenceRow{ID: ref.ID, State: ref.State, Title: clipTo(ref.Title, 255), Video: clipTo(ref.Source, 1000), CreatedAt: ref.CreatedAt, Doc: doc}
	return s.db.DB.Create(&row).Error
}

func (s *Store) dbSave(ref Reference) error {
	doc, err := referenceDoc(ref)
	if err != nil {
		return err
	}
	res := s.db.DB.Model(&referenceRow{}).Where("id = ?", ref.ID).Updates(map[string]any{"state": ref.State, "title": clipTo(ref.Title, 255), "video": clipTo(ref.Source, 1000), "doc": doc})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound // a deleted reference is not brought back
	}
	return nil
}

func referenceOfRow(r referenceRow) (Reference, error) {
	var ref Reference
	if err := json.Unmarshal([]byte(r.Doc), &ref); err != nil {
		return Reference{}, ErrNotFound
	}
	if ref.Tags == nil {
		ref.Tags = []string{}
	}
	return ref, nil
}

func (s *Store) dbGet(id string) (Reference, error) {
	var r referenceRow
	if err := s.db.DB.Where("id = ?", id).Take(&r).Error; err != nil {
		return Reference{}, ErrNotFound
	}
	return referenceOfRow(r)
}

func (s *Store) dbList() []Reference {
	var rows []referenceRow
	if err := s.db.DB.Order("created_at DESC, id DESC").Find(&rows).Error; err != nil {
		return []Reference{}
	}
	out := make([]Reference, 0, len(rows))
	for _, r := range rows {
		if ref, err := referenceOfRow(r); err == nil {
			out = append(out, ref)
		}
	}
	return out
}

func (s *Store) dbDelete(id string) error {
	res := s.db.DB.Where("id = ?", id).Delete(&referenceRow{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// --- learnings ------------------------------------------------------------------------------------

// NewLearningStoreDB makes the store of learnings on the database.
func NewLearningStoreDB(st *store.Store) *LearningStore { return &LearningStore{db: st} }

func (s *LearningStore) dbSave(l Learning) error {
	l.fill()
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	row := learningRow{ID: l.ID, ReferenceID: l.ReferenceID, State: l.State, CreatedAt: l.CreatedAt, Doc: store.Text(b)}
	return s.db.DB.Save(&row).Error
}

func (s *LearningStore) dbGet(id string) (Learning, error) {
	var r learningRow
	if err := s.db.DB.Where("id = ?", id).Take(&r).Error; err != nil {
		return Learning{}, ErrLearningNotFound
	}
	var l Learning
	if json.Unmarshal([]byte(r.Doc), &l) != nil {
		return Learning{}, ErrLearningNotFound
	}
	l.fill()
	return l, nil
}

func (s *LearningStore) dbList(referenceID string) []Learning {
	q := s.db.DB.Order("created_at DESC, id DESC")
	if referenceID != "" {
		q = q.Where("reference_id = ?", referenceID)
	}
	var rows []learningRow
	if err := q.Find(&rows).Error; err != nil {
		return []Learning{}
	}
	out := make([]Learning, 0, len(rows))
	for _, r := range rows {
		var l Learning
		if json.Unmarshal([]byte(r.Doc), &l) == nil {
			l.fill()
			out = append(out, l)
		}
	}
	return out
}

// Delete removes a learning (never the style or the part drafts it proposed).
func (s *LearningStore) Delete(id string) error {
	if s.db == nil {
		if _, err := s.Get(id); err != nil {
			return err
		}
		return os.Remove(s.path(id))
	}
	res := s.db.DB.Where("id = ?", id).Delete(&learningRow{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrLearningNotFound
	}
	return nil
}

// --- styles ---------------------------------------------------------------------------------------

// NewPresetStoreDB makes the store of styles on the database.
func NewPresetStoreDB(st *store.Store) *PresetStore { return &PresetStore{db: st} }

func (s *PresetStore) dbWrite(p musicmotion.Preset) error {
	if p.Parts == nil {
		p.Parts = []string{}
	}
	if p.Adjustments == nil {
		p.Adjustments = []string{}
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	row := presetRow{ID: p.ID, Name: clipTo(p.Name, 255), CreatedAt: p.CreatedAt, Doc: store.Text(b)}
	return s.db.DB.Save(&row).Error
}

func presetOfRow(r presetRow) (musicmotion.Preset, error) {
	var p musicmotion.Preset
	if json.Unmarshal([]byte(r.Doc), &p) != nil {
		return musicmotion.Preset{}, ErrPresetNotFound
	}
	if p.Parts == nil {
		p.Parts = []string{}
	}
	if p.Adjustments == nil {
		p.Adjustments = []string{}
	}
	return p, nil
}

func (s *PresetStore) dbGet(id string) (musicmotion.Preset, error) {
	var r presetRow
	if err := s.db.DB.Where("id = ?", id).Take(&r).Error; err != nil {
		return musicmotion.Preset{}, ErrPresetNotFound
	}
	return presetOfRow(r)
}

func (s *PresetStore) dbList() []musicmotion.Preset {
	var rows []presetRow
	if err := s.db.DB.Order("created_at DESC, id DESC").Find(&rows).Error; err != nil {
		return []musicmotion.Preset{}
	}
	out := make([]musicmotion.Preset, 0, len(rows))
	for _, r := range rows {
		if p, err := presetOfRow(r); err == nil {
			out = append(out, p)
		}
	}
	return out
}

func (s *PresetStore) dbDelete(id string) error {
	res := s.db.DB.Where("id = ?", id).Delete(&presetRow{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrPresetNotFound
	}
	return nil
}

func clipTo(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

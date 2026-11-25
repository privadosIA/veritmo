package motionlib

// db.go: the record of each motion in the database (change move-settings-and-catalogs-to-database). A motion is a row with the columns a list filters
// and sorts by and the Motion itself as a document; its control frames and its preview stay files in its folder.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"veritmo/internal/platform/store"
)

// Row is a motion as the database keeps it. Tags holds the tags between commas (",walk,slow,") so that the search finds a part of one.
type Row struct {
	ID        string `gorm:"primaryKey;size:191"`
	State     string `gorm:"size:16;index"`
	Title     string `gorm:"size:255"`
	Tags      store.Text
	CreatedAt time.Time `gorm:"index;autoCreateTime:false"`
	Doc       store.JSON[Motion]
}

// TableName is the table of the motions.
func (Row) TableName() string { return "motions" }

// rowOf is the row a motion is kept as: the columns a list filters and sorts by, and the motion itself.
func rowOf(m Motion) Row {
	tags := ""
	if len(m.Tags) > 0 {
		tags = "," + strings.ToLower(strings.Join(m.Tags, ",")) + ","
	}
	return Row{ID: m.ID, State: m.State, Title: m.Title, Tags: store.Text(tags), CreatedAt: m.CreatedAt.UTC(), Doc: store.JSON[Motion]{V: m}}
}

// motionOf is the motion a row holds (never with nil tags).
func motionOf(r Row) Motion {
	m := r.Doc.V
	if m.Tags == nil {
		m.Tags = []string{}
	}
	return m
}

// migration creates the table of the motions (the struct is copied here: it does the same whatever the model becomes).
func migration() *store.Migration {
	return &store.Migration{
		ID: "202611192100_motions",
		Migrate: func(tx *gorm.DB) error {
			type row struct {
				ID        string `gorm:"primaryKey;size:191"`
				State     string `gorm:"size:16;index"`
				Title     string `gorm:"size:255"`
				Tags      store.Text
				CreatedAt time.Time `gorm:"index"`
				Doc       store.Text
			}
			if tx.Migrator().HasTable("motions") {
				return nil
			}
			return tx.Table("motions").Migrator().CreateTable(&row{})
		},
		Rollback: func(tx *gorm.DB) error { return tx.Migrator().DropTable("motions") },
	}
}

// RegisterStore tells a registry what the motions own: their table, its migration and the importer of the motions kept as folders under dir
// (server.DataRegistry calls it).
func RegisterStore(r *store.Registry, dir string) {
	r.AddModels(&Row{})
	r.AddMigration(migration())
	r.AddImporter(Importer{Dir: dir})
}

// NewStoreDB is the library that keeps the record of each motion in the database st and its files under dir.
func NewStoreDB(dir string, st *store.Store) *Store { return &Store{Dir: dir, db: st} }

// saveRow writes the record of a motion, replacing the one it has.
func (s *Store) saveRow(m Motion) error {
	if m.Tags == nil {
		m.Tags = []string{}
	}
	return s.db.DB.Clauses(clause.OnConflict{UpdateAll: true}).Create(ptr(rowOf(m))).Error
}

// ptr is the address of a value: what a Create call takes.
func ptr[T any](v T) *T { return &v }

// getRow reads the record of a motion from the database.
func (s *Store) getRow(id string) (Motion, error) {
	var r Row
	if err := s.db.DB.Where("id = ?", id).Take(&r).Error; err != nil {
		return Motion{}, ErrNotFound
	}
	return motionOf(r), nil
}

// listRows reads every record, the most recent first.
func (s *Store) listRows() []Motion {
	var rows []Row
	_ = s.db.DB.Order("created_at DESC").Order("id DESC").Find(&rows).Error
	out := make([]Motion, 0, len(rows))
	for _, r := range rows {
		out = append(out, motionOf(r))
	}
	return out
}

// ListSpec says what the list of motions can be sorted and searched by: the newest first unless asked otherwise; q is searched in the title and the tags.
var ListSpec = store.ListSpec{
	Sortable:   map[string]string{"created_at": "created_at", "title": "title"},
	Searchable: []string{"title", "tags"},
	Default:    "-created_at",
	Tie:        "id",
}

// ListPage reads the motions of a state ("" for all) that the parameters keep, and how many that is in all. It needs the database.
func (s *Store) ListPage(state string, p store.ListParams) ([]Motion, int64, error) {
	q := s.db.DB.Model(&Row{})
	if state != "" {
		q = q.Where("state = ?", state)
	}
	q = store.Filter(q, p, ListSpec)
	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	q = store.Order(q, p, ListSpec)
	if p.Limit > 0 {
		q = q.Limit(p.Limit)
	}
	if p.Offset > 0 {
		q = q.Offset(p.Offset)
	}
	var rows []Row
	if err := q.Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	out := make([]Motion, 0, len(rows))
	for _, r := range rows {
		out = append(out, motionOf(r))
	}
	return out, total, nil
}

// Importer reads the motions kept as folders: <Dir>/<id>/motion.json.
type Importer struct{ Dir string }

// Entity is the name of the entity.
func (Importer) Entity() string { return "motions" }

// files are the motion.json files under the folder of the motions, by path.
func (i Importer) files() []string {
	files, _ := filepath.Glob(filepath.Join(i.Dir, IDPrefix+"*", "motion.json"))
	sort.Strings(files)
	return files
}

// Import creates the rows of the motions that have none. The control frames and the previews are not touched.
func (i Importer) Import(_ context.Context, tx *store.Store) (store.Report, error) {
	var rep store.Report
	var rows []Row
	for _, f := range i.files() {
		raw, err := os.ReadFile(f)
		if err != nil {
			rep.Skipped = append(rep.Skipped, store.Skipped{Path: f, Reason: err.Error()})
			continue
		}
		var m Motion
		if err := json.Unmarshal(raw, &m); err != nil || !ValidID(m.ID) || m.ID != filepath.Base(filepath.Dir(f)) {
			rep.Skipped = append(rep.Skipped, store.Skipped{Path: f, Reason: "it is not the motion.json of the motion it is in"})
			continue
		}
		rows = append(rows, rowOf(m))
	}
	var err error
	rep.Created, rep.Existing, err = store.InsertMissing(tx, rows)
	return rep, err
}

// LegacyFiles are the motion.json files, and only them: the control frames and the previews are what a run reads, not state.
func (i Importer) LegacyFiles() ([]store.LegacyFile, error) {
	var out []store.LegacyFile
	for _, f := range i.files() {
		if fi, err := os.Lstat(f); err == nil && fi.Mode().IsRegular() {
			out = append(out, store.LegacyFile{Path: f, Bytes: fi.Size()})
		}
	}
	return out, nil
}

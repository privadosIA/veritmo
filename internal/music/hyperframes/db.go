package hyperframes

// db.go: the record of each block in the database (change move-settings-and-catalogs-to-database). A block is a row with the columns a list filters
// and sorts by and the Block itself as a document; the files of the block (its HTML and what it loads, its thumbnail, the clips) stay in its folder,
// because the page loads them by relative path.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"veritmo/internal/platform/store"
)

// BlockRow is a block as the database keeps it. Tags holds the tags between commas (",neon,city,") so that one tag is matched whole.
type BlockRow struct {
	Name        string `gorm:"primaryKey;size:191"`
	Kind        string `gorm:"size:32;index"`
	Family      string `gorm:"size:64;index"`
	State       string `gorm:"size:32;index"`
	Title       string `gorm:"size:255"`
	Description store.Text
	Tags        store.Text
	UpdatedAt   time.Time `gorm:"autoUpdateTime:false"`
	Doc         store.JSON[Block]
}

// TableName is the table of the blocks.
func (BlockRow) TableName() string { return "hf_blocks" }

// rowOf is the row a block is kept as; its state (the result of the last checks) is worked out here so that a list can filter and count by it.
func rowOf(b Block) BlockRow {
	tags := ""
	if len(b.Tags) > 0 {
		tags = "," + strings.ToLower(strings.Join(b.Tags, ",")) + ","
	}
	return BlockRow{Name: b.Name, Kind: b.Kind, Family: strings.ToLower(b.Family), State: StateOf(b), Title: b.Title, Description: store.Text(b.Description),
		Tags: store.Text(tags), UpdatedAt: time.Now().UTC(), Doc: store.JSON[Block]{V: b}}
}

// migration creates the table of the blocks (the struct is copied here: it does the same whatever the model becomes).
func migration() *store.Migration {
	return &store.Migration{
		ID: "202611191900_hf_blocks",
		Migrate: func(tx *gorm.DB) error {
			type row struct {
				Name        string `gorm:"primaryKey;size:191"`
				Kind        string `gorm:"size:32;index"`
				Family      string `gorm:"size:64;index"`
				State       string `gorm:"size:32;index"`
				Title       string `gorm:"size:255"`
				Description store.Text
				Tags        store.Text
				UpdatedAt   time.Time
				Doc         store.Text
			}
			if tx.Migrator().HasTable("hf_blocks") {
				return nil
			}
			return tx.Table("hf_blocks").Migrator().CreateTable(&row{})
		},
		Rollback: func(tx *gorm.DB) error { return tx.Migrator().DropTable("hf_blocks") },
	}
}

// RegisterStore tells a registry what the blocks own: their table, its migration and the importer of the blocks kept as folders under dir
// (server.DataRegistry calls it).
func RegisterStore(r *store.Registry, dir string) {
	r.AddModels(&BlockRow{})
	r.AddMigration(migration())
	r.AddImporter(Importer{Dir: dir})
}

// NewStoreDB is the store of the blocks that keeps their records in the database st and their files under dir.
func NewStoreDB(dir string, st *store.Store) Store { return Store{Dir: dir, db: st} }

// getRow reads the record of a block from the database.
func (s Store) getRow(name string) (Block, error) {
	if !ValidName(name) {
		return Block{}, errors.New(name + " is not a block name")
	}
	var r BlockRow
	if err := s.db.DB.Where("name = ?", name).Take(&r).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Block{}, ErrNotFound
		}
		return Block{}, err
	}
	return r.Doc.V, nil
}

// listRows reads every record, by name.
func (s Store) listRows() ([]Block, error) {
	var rows []BlockRow
	if err := s.db.DB.Order("name").Find(&rows).Error; err != nil {
		return nil, err
	}
	var out []Block
	for _, r := range rows {
		out = append(out, r.Doc.V)
	}
	return out, nil
}

// saveRow writes the record of a block, replacing the one it has.
func (s Store) saveRow(b Block) error {
	return s.db.DB.Clauses(clause.OnConflict{UpdateAll: true}).Create(ptr(rowOf(b))).Error
}

// ptr is the address of a value: what a Create call takes.
func ptr[T any](v T) *T { return &v }

// Filter is what a list of blocks keeps: the search text (name, title, description and tags, without regard to case), a tag, the state of the last
// checks, the kind ("block", "component" or empty for both) and the family.
type Filter struct {
	Q, Tag, State, Kind, Family string
}

// ListSpec says what the list of blocks can be sorted by (the name by default).
var ListSpec = store.ListSpec{
	Sortable:   map[string]string{"name": "name", "title": "title"},
	Searchable: []string{"name", "title", "description", "tags"},
	Default:    "name",
	Tie:        "name",
}

// apply adds the conditions of the filter (all but the search text, which store.Filter adds) to a query.
func (f Filter) apply(q *gorm.DB) *gorm.DB {
	if f.Tag != "" {
		q = q.Where("tags LIKE ? ESCAPE '"+store.LikeEscape+"'", "%,"+strings.TrimSuffix(strings.TrimPrefix(store.LikePattern(f.Tag), "%"), "%")+",%")
	}
	if f.State != "" {
		q = q.Where("state = ?", f.State)
	}
	switch f.Kind {
	case "component":
		q = q.Where("kind = ?", "component")
	case "block":
		q = q.Where("kind = ?", "")
	}
	if f.Family != "" {
		q = q.Where("family = ?", strings.ToLower(f.Family))
	}
	return q
}

// Page is a page of the blocks, the total that match and how many of each state (over everything that matches, not only the page).
type Page struct {
	Blocks []Block
	Total  int64
	States map[string]int
}

// ListPage reads the blocks the filter and the parameters keep. It needs the database.
func (s Store) ListPage(f Filter, p store.ListParams) (Page, error) {
	base := f.apply(s.db.DB.Model(&BlockRow{}))
	base = store.Filter(base, store.ListParams{Q: f.Q}, ListSpec)
	var out Page
	out.States = map[string]int{}
	if err := base.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		return out, err
	}
	var counts []struct {
		State string
		N     int
	}
	if err := base.Session(&gorm.Session{}).Select("state, COUNT(*) AS n").Group("state").Scan(&counts).Error; err != nil {
		return out, err
	}
	for _, c := range counts {
		out.States[c.State] = c.N
	}
	q := store.Order(base, p, ListSpec)
	if p.Limit > 0 {
		q = q.Limit(p.Limit)
	}
	if p.Offset > 0 {
		q = q.Offset(p.Offset)
	}
	var rows []BlockRow
	if err := q.Find(&rows).Error; err != nil {
		return out, err
	}
	for _, r := range rows {
		out.Blocks = append(out.Blocks, r.Doc.V)
	}
	return out, nil
}

// Delete deletes the record of a block and its folder. A part that was made from the block is a part of its own and is never touched.
func (s Store) Delete(name string) error {
	dir, err := s.Path(name)
	if err != nil {
		return err
	}
	if _, err := s.Get(name); err != nil {
		return err
	}
	if s.db != nil {
		if err := s.db.DB.Where("name = ?", name).Delete(&BlockRow{}).Error; err != nil {
			return err
		}
	}
	return os.RemoveAll(dir)
}

// Importer reads the blocks kept as folders: <Dir>/blocks/<name>/block.json.
type Importer struct{ Dir string }

// Entity is the name of the entity.
func (Importer) Entity() string { return "hyperframes/blocks" }

// Import creates the rows of the blocks that have none. The files of a block are not touched.
func (i Importer) Import(_ context.Context, tx *store.Store) (store.Report, error) {
	var rep store.Report
	files := i.blockFiles()
	var rows []BlockRow
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			rep.Skipped = append(rep.Skipped, store.Skipped{Path: f, Reason: err.Error()})
			continue
		}
		var b Block
		if err := json.Unmarshal(raw, &b); err != nil || !ValidName(b.Name) || b.Name != filepath.Base(filepath.Dir(f)) {
			rep.Skipped = append(rep.Skipped, store.Skipped{Path: f, Reason: "it is not the block.json of the block it is in"})
			continue
		}
		r := rowOf(b)
		if fi, err := os.Stat(f); err == nil {
			r.UpdatedAt = fi.ModTime().UTC()
		}
		rows = append(rows, r)
	}
	created, existing, err := store.InsertMissing(tx, rows)
	rep.Created, rep.Existing = created, existing
	return rep, err
}

// blockFiles are the block.json files under the folder of the blocks, by path.
func (i Importer) blockFiles() []string {
	files, _ := filepath.Glob(filepath.Join(i.Dir, "blocks", "*", "block.json"))
	sort.Strings(files)
	return files
}

// LegacyFiles are the block.json files, and only them: the files of the blocks are not state, they are what the page loads.
func (i Importer) LegacyFiles() ([]store.LegacyFile, error) {
	var out []store.LegacyFile
	for _, f := range i.blockFiles() {
		if fi, err := os.Lstat(f); err == nil && fi.Mode().IsRegular() {
			out = append(out, store.LegacyFile{Path: f, Bytes: fi.Size()})
		}
	}
	return out, nil
}

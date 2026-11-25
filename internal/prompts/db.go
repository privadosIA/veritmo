package prompts

// db.go: the presets in the database (change move-settings-and-catalogs-to-database). A preset is a row of the table prompt_presets: the columns a list
// filters and sorts by, the text the search looks in, and the preset itself as a document, so that what is listed is exactly what was saved. The file
// functions of prompts.go stay for the importer (the presets that were kept as files) and for the tests.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"veritmo/internal/platform/store"
)

// ErrNotFound is a preset that is not in the store.
var ErrNotFound = errors.New("the preset does not exist")

// Row is a preset as the database keeps it. Tags holds the tags between commas (",neon,city,") so that one tag is matched exactly and the search finds
// a part of one; CreatedAt is the RFC 3339 text the preset carries, which sorts as the time does.
type Row struct {
	ID        string `gorm:"primaryKey;size:191"`
	Kind      string `gorm:"size:16;index;index:idx_prompt_source,priority:1"`
	Title     string `gorm:"size:255"`
	Engine    string `gorm:"size:64;index"`
	Language  string `gorm:"size:32"`
	Source    string `gorm:"size:64;index:idx_prompt_source,priority:2"`
	SourceID  string `gorm:"size:191;index:idx_prompt_source,priority:3"`
	NSFW      bool   `gorm:"index"`
	Own       bool   `gorm:"index"`
	CreatedAt string `gorm:"size:40;index"`
	Prompt    store.Text
	Negative  store.Text
	Tags      store.Text
	Doc       store.JSON[Preset]
}

// TableName is the table of the presets.
func (Row) TableName() string { return "prompt_presets" }

// tagsColumn is the text the tags of a preset are stored as: ",a,b," (nothing for none), so that one tag is matched whole with a LIKE.
func tagsColumn(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	return "," + strings.Join(tags, ",") + ","
}

// rowOf is the row a preset is kept as: the columns a list filters and sorts by, the text the search looks in, and the preset itself.
func rowOf(p *Preset) Row {
	return Row{
		ID: p.ID, Kind: p.Kind, Title: p.Title, Engine: p.Engine, Language: p.Language, Source: p.Source, SourceID: p.SourceID, NSFW: p.NSFW, Own: p.Own,
		CreatedAt: p.CreatedAt, Prompt: store.Text(p.Prompt), Negative: store.Text(p.Negative), Tags: store.Text(tagsColumn(p.Tags)), Doc: store.JSON[Preset]{V: *p},
	}
}

// presetOf is the preset a row holds (never with nil tags).
func presetOf(r Row) *Preset {
	p := r.Doc.V
	if p.Tags == nil {
		p.Tags = []string{}
	}
	return &p
}

// Migration creates the table of the presets.
func migration() *store.Migration {
	return &store.Migration{
		ID: "202611191800_prompt_presets",
		Migrate: func(tx *gorm.DB) error {
			type row struct {
				ID        string `gorm:"primaryKey;size:191"`
				Kind      string `gorm:"size:16;index;index:idx_prompt_source,priority:1"`
				Title     string `gorm:"size:255"`
				Engine    string `gorm:"size:64;index"`
				Language  string `gorm:"size:32"`
				Source    string `gorm:"size:64;index:idx_prompt_source,priority:2"`
				SourceID  string `gorm:"size:191;index:idx_prompt_source,priority:3"`
				NSFW      bool   `gorm:"index"`
				Own       bool   `gorm:"index"`
				CreatedAt string `gorm:"size:40;index"`
				Prompt    store.Text
				Negative  store.Text
				Tags      store.Text
				Doc       store.Text
			}
			if tx.Migrator().HasTable("prompt_presets") {
				return nil
			}
			return tx.Table("prompt_presets").Migrator().CreateTable(&row{})
		},
		Rollback: func(tx *gorm.DB) error { return tx.Migrator().DropTable("prompt_presets") },
	}
}

// RegisterStore tells a registry what the presets own: their table, its migration and the importer of the presets kept as files under dir
// (<media dir>/prompts); server.DataRegistry calls it.
func RegisterStore(r *store.Registry, dir string) {
	r.AddModels(&Row{})
	r.AddMigration(migration())
	r.AddImporter(Importer{Dir: dir})
}

// Store is what the server and the command that fills it use of the presets: the database implements it.
type Store interface {
	Save(ps []*Preset) error
	Remove(id string) error
	Get(id string) (*Preset, error)
	Keys() (map[string]string, error)
	FreshIDs(n int) ([]string, error)
	List(f Filter, p store.ListParams) ([]*Preset, int64, error)
}

// DB is the store of presets of a database.
type DB struct{ st *store.Store }

var _ Store = (*DB)(nil)

// NewDB makes the store of the presets of an open, migrated database.
func NewDB(st *store.Store) *DB { return &DB{st: st} }

// Save keeps presets, replacing the ones whose id is already there, as one change (in batches of 500 inside one transaction).
func (d *DB) Save(ps []*Preset) error {
	if len(ps) == 0 {
		return nil
	}
	return d.st.Tx(func(tx *store.Store) error {
		for start := 0; start < len(ps); start += 500 {
			end := min(start+500, len(ps))
			rows := make([]Row, 0, end-start)
			for _, p := range ps[start:end] {
				rows = append(rows, rowOf(p))
			}
			if err := tx.DB.Clauses(clause.OnConflict{UpdateAll: true}).Create(&rows).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// Remove deletes a preset; ErrNotFound when there is none.
func (d *DB) Remove(id string) error {
	res := d.st.DB.Where("id = ?", id).Delete(&Row{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// Get reads a preset; ErrNotFound when there is none.
func (d *DB) Get(id string) (*Preset, error) {
	var r Row
	if err := d.st.DB.Where("id = ?", id).Take(&r).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return presetOf(r), nil
}

// Keys are the Key of every preset that has one (a source and a source id), for the callers that skip what is already kept.
func (d *DB) Keys() (map[string]string, error) {
	var rows []Row
	if err := d.st.DB.Select("id", "kind", "source", "source_id").Where("source <> '' AND source_id <> ''").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.Kind+"\x00"+r.Source+"\x00"+r.SourceID] = r.ID
	}
	return out, nil
}

// FreshIDs makes n ids that no preset has and that differ from each other (an id is 8 hexadecimal digits: with thousands of presets a repeat is
// not unlikely, and a repeated id would replace another preset).
func (d *DB) FreshIDs(n int) ([]string, error) {
	out := make([]string, 0, n)
	seen := make(map[string]bool, n)
	for len(out) < n {
		need := n - len(out)
		batch := make([]string, 0, need)
		for len(batch) < need {
			if id := NewID(); !seen[id] {
				seen[id] = true
				batch = append(batch, id)
			}
		}
		taken := map[string]bool{}
		for start := 0; start < len(batch); start += 500 {
			var found []string
			if err := d.st.DB.Model(&Row{}).Where("id IN ?", batch[start:min(start+500, len(batch))]).Pluck("id", &found).Error; err != nil {
				return nil, err
			}
			for _, id := range found {
				taken[id] = true
			}
		}
		for _, id := range batch {
			if !taken[id] {
				out = append(out, id)
			}
		}
	}
	return out, nil
}

// Filter is what a list keeps besides the search: a kind, the tags every preset must have, and whether the presets marked nsfw are hidden.
type Filter struct {
	Kind     string
	Tags     []string
	HideNSFW bool
}

// ListSpec says what the list of presets can be sorted and searched by: `q` is searched in the title, the prompt and the tags.
var ListSpec = store.ListSpec{
	Sortable:   map[string]string{"title": "title", "created_at": "created_at"},
	Searchable: []string{"title", "prompt", "tags"},
	Tie:        "id",
}

// List reads a page of presets and how many there are in all. With no sort the newest come first, and with a search the ones that match best come
// first (the title weighs 3, a tag 2 and the prompt 1), as the list has always been ordered.
func (d *DB) List(f Filter, p store.ListParams) ([]*Preset, int64, error) {
	q := d.st.DB.Model(&Row{})
	if f.Kind != "" {
		q = q.Where("kind = ?", f.Kind)
	}
	if f.HideNSFW {
		q = q.Where("nsfw = ?", false)
	}
	for _, t := range f.Tags {
		q = q.Where("tags LIKE ? ESCAPE '"+store.LikeEscape+"'", "%,"+strings.TrimSuffix(strings.TrimPrefix(store.LikePattern(t), "%"), "%")+",%")
	}
	q = store.Filter(q, p, ListSpec)
	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	switch {
	case p.Sort != "":
		q = store.Order(q, p, ListSpec)
	case p.Q != "":
		pat := store.LikePattern(p.Q)
		esc := " LIKE ? ESCAPE '" + store.LikeEscape + "'"
		q = q.Order(clause.OrderBy{Expression: clause.Expr{
			SQL:  "(CASE WHEN LOWER(title)" + esc + " THEN 3 ELSE 0 END + CASE WHEN LOWER(tags)" + esc + " THEN 2 ELSE 0 END + CASE WHEN LOWER(prompt)" + esc + " THEN 1 ELSE 0 END) DESC, created_at DESC, id ASC",
			Vars: []any{pat, pat, pat}, WithoutParentheses: true,
		}}) // one expression: the clause drops the columns added after it
	default:
		q = q.Order("created_at DESC").Order("id ASC")
	}
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
	out := make([]*Preset, 0, len(rows))
	for _, r := range rows {
		out = append(out, presetOf(r))
	}
	return out, total, nil
}

// Importer reads the presets kept as files under Dir/<kind>/<id>.json (the folder .cache is not a kind and is never read).
type Importer struct{ Dir string }

// Entity is the name of the entity.
func (Importer) Entity() string { return "prompts" }

// Import creates the presets that are missing, a transaction for all the kinds and batches of 500. A file that cannot be read is named in the record.
func (i Importer) Import(_ context.Context, tx *store.Store) (store.Report, error) {
	var rep store.Report
	for _, kind := range Kinds {
		presets, skipped := readKindReport(i.Dir, kind)
		rep.Skipped = append(rep.Skipped, skipped...)
		rows := make([]Row, 0, len(presets))
		for _, p := range presets {
			rows = append(rows, rowOf(p))
		}
		created, existing, err := store.InsertMissing(tx, rows)
		if err != nil {
			return rep, fmt.Errorf("the presets of kind %s: %w", kind, err)
		}
		rep.Created += created
		rep.Existing += existing
	}
	return rep, nil
}

// LegacyFiles are the preset files of every kind folder. The .cache folder is never listed.
func (i Importer) LegacyFiles() ([]store.LegacyFile, error) {
	var out []store.LegacyFile
	for _, kind := range Kinds {
		files, _ := filepath.Glob(filepath.Join(i.Dir, kind, "*.json"))
		sort.Strings(files)
		for _, f := range files {
			if fi, err := lstat(f); err == nil && fi.Mode().IsRegular() {
				out = append(out, store.LegacyFile{Path: f, Bytes: fi.Size()})
			}
		}
	}
	return out, nil
}

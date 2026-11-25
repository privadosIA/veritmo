package musicmotion

// draftsdb.go: the drafts of parts kept in the database (spec motion-part-drafts, "Drafts are kept in the database"). A draft is a row with what a
// list filters by as columns, the record of the draft as a document and the text of its files (the part it holds, and the one a rewrite starts
// from) as columns of their own. The picture and the preview frames stay files under the folder of the store.

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

	"veritmo/internal/platform/store"
)

// draftRow is a draft as the database keeps it.
type draftRow struct {
	ID               string `gorm:"primaryKey;size:64"`
	State            string `gorm:"size:32;index"`
	Name             string `gorm:"size:64"`
	Source           string `gorm:"size:32"`
	Replaces         string `gorm:"size:64"`
	ScenePiece       string `gorm:"size:191;index"`
	SceneScriptPiece string `gorm:"size:64"`
	Request          string `gorm:"size:500"`
	Attempts         int
	CreatedAt        time.Time `gorm:"index"`
	UpdatedAt        time.Time
	Doc              store.Text // the record of the draft (draft.json), without the text of its files
	ManifestJSON     store.Text
	FragmentHTML     store.Text
	BaseManifestJSON store.Text
	BaseFragmentHTML store.Text
}

// TableName is the table of the drafts.
func (draftRow) TableName() string { return "mm_part_drafts" }

func draftsMigration() *store.Migration {
	return &store.Migration{
		ID: "202611191200_mm_part_drafts",
		Migrate: func(tx *gorm.DB) error {
			type draftRow struct {
				ID               string `gorm:"primaryKey;size:64"`
				State            string `gorm:"size:32;index"`
				Name             string `gorm:"size:64"`
				Source           string `gorm:"size:32"`
				Replaces         string `gorm:"size:64"`
				ScenePiece       string `gorm:"size:191;index"`
				SceneScriptPiece string `gorm:"size:64"`
				Request          string `gorm:"size:500"`
				Attempts         int
				CreatedAt        time.Time `gorm:"index"`
				UpdatedAt        time.Time
				Doc              store.Text
				ManifestJSON     store.Text
				FragmentHTML     store.Text
				BaseManifestJSON store.Text
				BaseFragmentHTML store.Text
			}
			if tx.Migrator().HasTable("mm_part_drafts") {
				return nil
			}
			return tx.Table("mm_part_drafts").Migrator().CreateTable(&draftRow{})
		},
		Rollback: func(tx *gorm.DB) error { return tx.Migrator().DropTable("mm_part_drafts") },
	}
}

// NewDraftStoreDB makes a store of drafts that keeps them in the database: their records and the text of their files. dir is where the
// pictures and the preview frames of the drafts are kept, a folder each.
func NewDraftStoreDB(dir string, st *store.Store) *DraftStore { return &DraftStore{Dir: dir, db: st} }

// On is the same store working inside the transaction tx: what it writes is applied or undone with it. The media files are not part of a
// transaction: delete them with RemoveMedia once it is committed.
func (s *DraftStore) On(tx *store.Store) *DraftStore { return &DraftStore{Dir: s.Dir, db: tx} }

// RemoveMedia deletes the picture and the preview frames of a draft.
func (s *DraftStore) RemoveMedia(id string) {
	if ValidDraftID(id) {
		_ = os.RemoveAll(s.path(id))
	}
}

// DeleteRecord removes a draft from the database, leaving its media (see RemoveMedia). ErrDraftNotFound when there is none.
func (s *DraftStore) DeleteRecord(id string) error {
	if s.db == nil {
		return s.Delete(id)
	}
	res := s.db.DB.Where("id = ?", id).Delete(&draftRow{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrDraftNotFound
	}
	return nil
}

// docOf is the record of a draft as it is kept: without the text of its files.
func docOf(d Draft) (store.Text, error) {
	d.Manifest, d.Fragment = "", ""
	if d.Problems == nil {
		d.Problems = []string{}
	}
	if d.Preview == nil {
		d.Preview = []string{}
	}
	raw, err := json.MarshalIndent(d, "", "  ")
	return store.Text(raw), err
}

func draftColumns(d Draft) map[string]any {
	cols := map[string]any{"state": d.State, "name": d.Name, "source": d.Source, "replaces": d.Replaces, "request": clipRunes(d.Request, 500), "attempts": d.Attempts, "scene_piece": "", "scene_script_piece": ""}
	if d.Scene != nil {
		cols["scene_piece"], cols["scene_script_piece"] = d.Scene.Piece, d.Scene.ScriptPiece
	}
	return cols
}

func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func (s *DraftStore) dbCreate(d Draft) error {
	doc, err := docOf(d)
	if err != nil {
		return err
	}
	cols := draftColumns(d)
	row := draftRow{ID: d.ID, State: d.State, Name: d.Name, Source: d.Source, Replaces: d.Replaces, Request: cols["request"].(string), Attempts: d.Attempts, CreatedAt: d.CreatedAt, Doc: doc}
	if d.Scene != nil {
		row.ScenePiece, row.SceneScriptPiece = d.Scene.Piece, d.Scene.ScriptPiece
	}
	return s.db.DB.Create(&row).Error
}

func (s *DraftStore) dbSave(d Draft) error {
	doc, err := docOf(d)
	if err != nil {
		return err
	}
	cols := draftColumns(d)
	cols["doc"] = doc
	res := s.db.DB.Model(&draftRow{}).Where("id = ?", d.ID).Updates(cols)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrDraftNotFound // a deleted draft is not brought back
	}
	return nil
}

func (s *DraftStore) dbSetFiles(id string, cols map[string]any) error {
	res := s.db.DB.Model(&draftRow{}).Where("id = ?", id).Updates(cols)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrDraftNotFound
	}
	return nil
}

func draftOfRow(r draftRow) (Draft, error) {
	var d Draft
	if err := json.Unmarshal([]byte(r.Doc), &d); err != nil {
		return Draft{}, ErrDraftNotFound
	}
	d.Manifest, d.Fragment = string(r.ManifestJSON), string(r.FragmentHTML)
	if d.Problems == nil {
		d.Problems = []string{}
	}
	if d.Preview == nil {
		d.Preview = []string{}
	}
	return d, nil
}

func (s *DraftStore) dbGet(id string) (Draft, error) {
	var r draftRow
	if err := s.db.DB.Where("id = ?", id).Take(&r).Error; err != nil {
		return Draft{}, ErrDraftNotFound
	}
	return draftOfRow(r)
}

func (s *DraftStore) dbList() []Draft {
	var rows []draftRow
	if err := s.db.DB.Order("created_at DESC, id DESC").Find(&rows).Error; err != nil {
		return []Draft{}
	}
	out := make([]Draft, 0, len(rows))
	for _, r := range rows {
		if d, err := draftOfRow(r); err == nil {
			out = append(out, d)
		}
	}
	return out
}

func (s *DraftStore) dbExists(id string) bool {
	var n int64
	s.db.DB.Model(&draftRow{}).Where("id = ?", id).Count(&n)
	return n > 0
}

func (s *DraftStore) dbLoadBase(id string) (string, string, error) {
	var r draftRow
	if err := s.db.DB.Where("id = ?", id).Take(&r).Error; err != nil {
		return "", "", ErrDraftNotFound
	}
	if r.BaseManifestJSON == "" && r.BaseFragmentHTML == "" {
		return "", "", errors.New("the draft has no base")
	}
	return string(r.BaseManifestJSON), string(r.BaseFragmentHTML), nil
}

// draftsImporter reads the drafts that were kept as folders (draft.json and the text of the files beside it): entity music-motion/part-drafts.
// The picture and the preview frames stay where they are: the store keeps serving them from the same folders.
type draftsImporter struct{ dir string }

func (draftsImporter) Entity() string { return "music-motion/part-drafts" }

// draftFiles are the state files of a draft folder (never frame.jpg or preview/).
var draftFiles = []string{"draft.json", "manifest.json", "part.html", "base-manifest.json", "base-part.html"}

func (i draftsImporter) Import(ctx context.Context, tx *store.Store) (store.Report, error) {
	var rep store.Report
	entries, err := os.ReadDir(i.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return rep, nil
		}
		return rep, err
	}
	var rows []draftRow
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(i.dir, e.Name())
		if !ValidDraftID(e.Name()) {
			rep.Skipped = append(rep.Skipped, store.Skipped{Path: dir, Reason: "the folder is not named like a draft"})
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, "draft.json"))
		if err != nil {
			rep.Skipped = append(rep.Skipped, store.Skipped{Path: dir, Reason: "the folder has no draft.json"})
			continue
		}
		var d Draft
		if json.Unmarshal(raw, &d) != nil || d.ID != e.Name() {
			rep.Skipped = append(rep.Skipped, store.Skipped{Path: filepath.Join(dir, "draft.json"), Reason: "draft.json is not valid or names another draft"})
			continue
		}
		doc, err := docOf(d)
		if err != nil {
			return rep, err
		}
		cols := draftColumns(d)
		row := draftRow{ID: d.ID, State: d.State, Name: d.Name, Source: d.Source, Replaces: d.Replaces, Request: cols["request"].(string), Attempts: d.Attempts,
			CreatedAt: d.CreatedAt, Doc: doc, ManifestJSON: readText(dir, "manifest.json"), FragmentHTML: readText(dir, "part.html"),
			BaseManifestJSON: readText(dir, "base-manifest.json"), BaseFragmentHTML: readText(dir, "base-part.html")}
		if d.Scene != nil {
			row.ScenePiece, row.SceneScriptPiece = d.Scene.Piece, d.Scene.ScriptPiece
		}
		rows = append(rows, row)
	}
	rep.Created, rep.Existing, err = store.InsertMissing(tx, rows)
	return rep, err
}

func readText(dir, name string) store.Text {
	b, _ := os.ReadFile(filepath.Join(dir, name))
	return store.Text(b)
}

func (i draftsImporter) LegacyFiles() ([]store.LegacyFile, error) {
	entries, err := os.ReadDir(i.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []store.LegacyFile
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		for _, f := range draftFiles {
			p := filepath.Join(i.dir, e.Name(), f)
			if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
				out = append(out, store.LegacyFile{Path: p, Bytes: st.Size()})
			}
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Path < out[b].Path })
	return out, nil
}

// DraftFilter narrows a list of drafts.
type DraftFilter struct {
	// State keeps the drafts in that state (writing, ready or failed).
	State string
	// Piece keeps the drafts of the scenes written for that piece.
	Piece string
}

// DraftsListSpec says what the list of drafts can be sorted and searched by.
var DraftsListSpec = store.ListSpec{
	Sortable:   map[string]string{"created_at": "created_at", "name": "name"},
	Searchable: []string{"request", "name"},
	Default:    "-created_at",
	Tie:        "id",
}

// Page reads the drafts that match the filter and the parameters (search, sort and page), newest first by default, and how many match in all.
// A draft comes without the text of its files, as in a list.
func (s *DraftStore) Page(f DraftFilter, p store.ListParams) ([]Draft, int64, error) {
	if s.db == nil { // a store of folders: filter and page in memory
		var all []Draft
		for _, d := range s.List() {
			if (f.State == "" || d.State == f.State) && (f.Piece == "" || (d.Scene != nil && d.Scene.Piece == f.Piece)) &&
				(p.Q == "" || strings.Contains(strings.ToLower(d.Request+" "+d.Name), strings.ToLower(p.Q))) {
				d.Manifest, d.Fragment = "", ""
				all = append(all, d)
			}
		}
		return store.Slice(all, p), int64(len(all)), nil
	}
	base := s.db.DB.Model(&draftRow{})
	if f.State != "" {
		base = base.Where("state = ?", f.State)
	}
	if f.Piece != "" {
		base = base.Where("scene_piece = ?", f.Piece)
	}
	base = store.Filter(base, p, DraftsListSpec)
	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	q := store.Order(base, p, DraftsListSpec).Select("id", "doc")
	if p.Limit > 0 {
		q = q.Limit(p.Limit)
	}
	if p.Offset > 0 {
		q = q.Offset(p.Offset)
	}
	var rows []draftRow
	if err := q.Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	out := make([]Draft, 0, len(rows))
	for _, r := range rows {
		if d, err := draftOfRow(r); err == nil {
			out = append(out, d)
		}
	}
	return out, total, nil
}

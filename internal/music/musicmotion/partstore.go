package musicmotion

// partstore.go: where the parts of the user's are kept (spec music-motion-parts, "The user's parts are kept in the database"). PartStore is
// the interface the catalog reads and writes them through; dirParts (partsdir.go) keeps them in a folder and dbParts in the database.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"veritmo/internal/platform/store"
)

// ErrPartExists is a part that cannot be created because one of the user's has its name.
var ErrPartExists = errors.New("the part already exists")

// ScenesMode says which parts a list holds with respect to the scenes written for a piece.
type ScenesMode int

// The three ways to list.
const (
	NoScenes   ScenesMode = iota // only the parts people choose from
	OnlyScenes                   // only the scene parts
	AllParts                     // both
)

// SceneFilter narrows a list of parts.
type SceneFilter struct {
	Scenes ScenesMode
	// Piece, with OnlyScenes, keeps the scenes written for that piece.
	Piece string
}

func (f SceneFilter) matches(p Part) bool {
	switch f.Scenes {
	case NoScenes:
		return !p.IsScene()
	case OnlyScenes:
		return p.IsScene() && (f.Piece == "" || p.Scene.Piece == f.Piece)
	}
	return true
}

// PartStore is where the user's parts (the scene parts written for a piece included) are kept. The parts that come with the program are not in
// it. Every method reads or writes only the parts it names.
type PartStore interface {
	Get(name string) (Part, bool)
	// GetMany reads each part once: one query for all of them.
	GetMany(names []string) map[string]Part
	List(f SceneFilter) []Part
	// Create keeps a new part; ErrPartExists when one of the user's has its name.
	Create(manifest []byte, fragment string, o *PartOrigin) error
	// Replace changes the manifest and the fragment of a part (its origin stays); ErrPartNotFound when there is none.
	Replace(name string, manifest []byte, fragment string) error
	// Delete removes a part; ErrPartNotFound when there is none.
	Delete(name string) error
	// DeleteScenes removes the scene parts written for a piece and says how many.
	DeleteScenes(piece string) (int, error)
}

// partRow is a part of the user's as the database keeps it: the manifest and the fragment as the files had them, byte for byte, and what a
// list filters by as columns of their own.
type partRow struct {
	Name             string `gorm:"primaryKey;size:64"`
	IsScene          bool   `gorm:"index"`
	ScenePiece       string `gorm:"size:191;index"`
	SceneScriptPiece string `gorm:"size:64"`
	Description      string `gorm:"size:1000"`
	Levels           string `gorm:"size:100"` // comma-joined
	Manual           bool
	FixedCopy        bool
	OriginSource     string `gorm:"size:64"`
	ManifestJSON     store.Text
	FragmentHTML     store.Text
	Origin           store.JSON[*PartOrigin]
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// TableName is the table of the user's parts.
func (partRow) TableName() string { return "mm_parts" }

func partsMigration() *store.Migration {
	return &store.Migration{
		ID: "202611191000_mm_parts",
		Migrate: func(tx *gorm.DB) error {
			type partRow struct {
				Name             string `gorm:"primaryKey;size:64"`
				IsScene          bool   `gorm:"index"`
				ScenePiece       string `gorm:"size:191;index"`
				SceneScriptPiece string `gorm:"size:64"`
				Description      string `gorm:"size:1000"`
				Levels           string `gorm:"size:100"`
				Manual           bool
				FixedCopy        bool
				OriginSource     string `gorm:"size:64"`
				ManifestJSON     store.Text
				FragmentHTML     store.Text
				Origin           store.JSON[*PartOrigin]
				CreatedAt        time.Time
				UpdatedAt        time.Time
			}
			if tx.Migrator().HasTable("mm_parts") {
				return nil
			}
			return tx.Table("mm_parts").Migrator().CreateTable(&partRow{})
		},
		Rollback: func(tx *gorm.DB) error { return tx.Migrator().DropTable("mm_parts") },
	}
}

// RegisterStore tells a registry what the music motion engine owns in the database. The server calls it when it builds the registry of its
// database (server.DataRegistry).
func RegisterStore(r *store.Registry, dirs Folders) {
	r.AddModels(&partRow{}, &draftRow{}, &ratioRow{}, &libraryItemRow{}, &libraryFileRow{})
	r.AddMigration(partsMigration())
	r.AddMigration(draftsMigration())
	r.AddMigration(historyMigration())
	r.AddMigration(libraryMigration())
	r.AddImporter(partsImporter{dir: dirs.Parts})
	r.AddImporter(draftsImporter{dir: dirs.Drafts})
	r.AddImporter(historyImporter{dir: dirs.State})
	r.AddImporter(libraryImporter{dir: dirs.Library})
}

// dbParts keeps the user's parts in the database.
type dbParts struct{ st *store.Store }

// NewDBParts is the store of the user's parts in the database.
func NewDBParts(st *store.Store) PartStore { return dbParts{st: st} }

func (d dbParts) partOf(r partRow) (Part, bool) {
	p, err := parsePart(r.Name, []byte(r.ManifestJSON), string(r.FragmentHTML), r.Origin.V, "user")
	if err != nil {
		warnOnce("user/"+r.Name+"#"+err.Error(), fmt.Sprintf("the part %q is left out: %v", "user/"+r.Name, err))
		return Part{}, false
	}
	p.UpdatedAt = r.UpdatedAt.UTC()
	return p, true
}

func (d dbParts) Get(name string) (Part, bool) {
	var r partRow
	if err := d.st.DB.Where("name = ?", name).Take(&r).Error; err != nil {
		return Part{}, false
	}
	return d.partOf(r)
}

func (d dbParts) GetMany(names []string) map[string]Part {
	out := map[string]Part{}
	if len(names) == 0 {
		return out
	}
	var rows []partRow
	if err := d.st.DB.Where("name IN ?", names).Find(&rows).Error; err != nil {
		return out
	}
	for _, r := range rows {
		if p, ok := d.partOf(r); ok {
			out[r.Name] = p
		}
	}
	return out
}

func (d dbParts) List(f SceneFilter) []Part {
	q := d.st.DB.Order("name")
	switch f.Scenes {
	case NoScenes:
		q = q.Where("is_scene = ?", false)
	case OnlyScenes:
		q = q.Where("is_scene = ?", true)
		if f.Piece != "" {
			q = q.Where("scene_piece = ?", f.Piece)
		}
	}
	var rows []partRow
	if err := q.Find(&rows).Error; err != nil {
		return nil
	}
	out := make([]Part, 0, len(rows))
	for _, r := range rows {
		if p, ok := d.partOf(r); ok {
			out = append(out, p)
		}
	}
	return out
}

// rowOf makes the row of a part from its manifest and fragment, checked as the folder reader checks them.
func rowOf(manifest []byte, fragment string, o *PartOrigin) (partRow, error) {
	var head struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(manifest, &head); err != nil || !reName.MatchString(head.Name) {
		return partRow{}, errors.New("the manifest has no valid name")
	}
	p, err := parsePart(head.Name, manifest, fragment, o, "user")
	if err != nil {
		return partRow{}, err
	}
	r := partRow{Name: p.Name, Description: p.Description, Levels: strings.Join(p.Levels, ","), Manual: p.Manual, FixedCopy: p.HasFixedCopy,
		ManifestJSON: store.Text(manifest), FragmentHTML: store.Text(fragment)}
	r.Origin.V = o
	if o != nil {
		r.OriginSource = o.sourceKind()
	}
	if p.Scene != nil {
		r.IsScene, r.ScenePiece, r.SceneScriptPiece = true, p.Scene.Piece, p.Scene.ScriptPiece
	}
	return r, nil
}

func (d dbParts) Create(manifest []byte, fragment string, o *PartOrigin) error {
	r, err := rowOf(manifest, fragment, o)
	if err != nil {
		return err
	}
	if err := d.st.DB.Create(&r).Error; err != nil {
		var n int64
		d.st.DB.Model(&partRow{}).Where("name = ?", r.Name).Count(&n)
		if n > 0 {
			return fmt.Errorf("%w: the part %q is already there", ErrPartExists, r.Name)
		}
		return err
	}
	return nil
}

func (d dbParts) Replace(name string, manifest []byte, fragment string) error {
	var old partRow
	if err := d.st.DB.Where("name = ?", name).Take(&old).Error; err != nil {
		return ErrPartNotFound
	}
	r, err := rowOf(manifest, fragment, old.Origin.V) // the origin stays
	if err != nil {
		return err
	}
	if r.Name != name {
		return fmt.Errorf("%w: %q instead of %q", ErrPartRenamed, r.Name, name)
	}
	r.CreatedAt = old.CreatedAt
	return d.st.DB.Save(&r).Error
}

func (d dbParts) Delete(name string) error {
	res := d.st.DB.Where("name = ?", name).Delete(&partRow{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrPartNotFound
	}
	return nil
}

func (d dbParts) DeleteScenes(piece string) (int, error) {
	res := d.st.DB.Where("is_scene = ? AND scene_piece = ?", true, piece).Delete(&partRow{})
	return int(res.RowsAffected), res.Error
}

// sourceKind says where a part came from, as a word a list can show: the project of the block, a learning, a reference or none.
func (o PartOrigin) sourceKind() string {
	switch {
	case o.Source != "":
		return o.Source
	case o.Learned:
		return "learned"
	case o.ReferenceID != "" || o.Video != "":
		return "reference"
	}
	return ""
}

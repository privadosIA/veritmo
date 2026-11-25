package musicmotion

// librarystore.go: where the user's characters and backgrounds are kept (spec motion-characters). A LibraryStore gives them as a tree of folders
// (characters/<name>/manifest.json and its drawings, the same as the folder the library used to read), so the checks of an item are always the
// ones of the readers; the database implementation keeps the manifest and the drawings as text.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing/fstest"
	"time"

	"gorm.io/gorm"

	"veritmo/internal/platform/store"
)

// The two kinds of item of the library.
const (
	KindCharacter  = "character"
	KindBackground = "background"
)

// LibraryStore keeps the user's characters and backgrounds.
type LibraryStore interface {
	// Tree is the user's items as folders: characters/<name>/... and backgrounds/<name>/....
	Tree() fs.FS
	// Put keeps an item (its manifest and its drawings by file name), replacing the one of that name.
	Put(kind, name string, manifest []byte, files map[string]string) error
	// Delete removes an item; false when there was none.
	Delete(kind, name string) (bool, error)
}

// NewLibrary is the library the programs use: what comes with the program and the user's items from the store (nil for none).
func NewLibrary(items LibraryStore) Library { return Library{Builtin: BuiltinLibrary(), Items: items} }

// libraryItemRow is an item of the user's library as the database keeps it.
type libraryItemRow struct {
	Kind         string `gorm:"primaryKey;size:16"`
	Name         string `gorm:"primaryKey;size:64"`
	Description  string `gorm:"size:500"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
	ManifestJSON store.Text
}

// TableName is the table of the items.
func (libraryItemRow) TableName() string { return "mm_library_items" }

// libraryFileRow is a drawing of an item: its text, or the path of a file when it is an image that is not SVG.
type libraryFileRow struct {
	Kind string `gorm:"primaryKey;size:16"`
	Name string `gorm:"primaryKey;size:64"`
	File string `gorm:"primaryKey;size:191"`
	Body store.Text
	Path string `gorm:"size:1000"`
}

// TableName is the table of the drawings.
func (libraryFileRow) TableName() string { return "mm_library_files" }

func libraryMigration() *store.Migration {
	return &store.Migration{
		ID: "202611191500_mm_library",
		Migrate: func(tx *gorm.DB) error {
			type libraryItemRow struct {
				Kind         string `gorm:"primaryKey;size:16"`
				Name         string `gorm:"primaryKey;size:64"`
				Description  string `gorm:"size:500"`
				CreatedAt    time.Time
				UpdatedAt    time.Time
				ManifestJSON store.Text
			}
			type libraryFileRow struct {
				Kind string `gorm:"primaryKey;size:16"`
				Name string `gorm:"primaryKey;size:64"`
				File string `gorm:"primaryKey;size:191"`
				Body store.Text
				Path string `gorm:"size:1000"`
			}
			for table, model := range map[string]any{"mm_library_items": &libraryItemRow{}, "mm_library_files": &libraryFileRow{}} {
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
			for _, t := range []string{"mm_library_files", "mm_library_items"} {
				if err := tx.Migrator().DropTable(t); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// DBLibrary is the user's library kept in the database.
func DBLibrary(st *store.Store) LibraryStore { return dbLibrary{st: st} }

type dbLibrary struct{ st *store.Store }

func folderOf(kind string) string {
	if kind == KindBackground {
		return "backgrounds"
	}
	return "characters"
}

func (d dbLibrary) Tree() fs.FS {
	m := fstest.MapFS{}
	var items []libraryItemRow
	var files []libraryFileRow
	if d.st.DB.Find(&items).Error != nil || d.st.DB.Find(&files).Error != nil {
		return m
	}
	for _, it := range items {
		m[folderOf(it.Kind)+"/"+it.Name+"/manifest.json"] = &fstest.MapFile{Data: []byte(it.ManifestJSON)}
	}
	for _, f := range files {
		m[folderOf(f.Kind)+"/"+f.Name+"/"+f.File] = &fstest.MapFile{Data: []byte(f.Body)}
	}
	return m
}

func (d dbLibrary) Put(kind, name string, manifest []byte, files map[string]string) error {
	desc := descriptionOf(manifest)
	return d.st.Tx(func(tx *store.Store) error {
		now := time.Now().UTC()
		var old libraryItemRow
		created := now
		if tx.DB.Where("kind = ? AND name = ?", kind, name).Take(&old).Error == nil {
			created = old.CreatedAt
		}
		if err := tx.DB.Where("kind = ? AND name = ?", kind, name).Delete(&libraryFileRow{}).Error; err != nil {
			return err
		}
		if err := tx.DB.Save(&libraryItemRow{Kind: kind, Name: name, Description: clipRunes(desc, 500), CreatedAt: created, UpdatedAt: now, ManifestJSON: store.Text(manifest)}).Error; err != nil {
			return err
		}
		for f, body := range files {
			if err := tx.DB.Create(&libraryFileRow{Kind: kind, Name: name, File: f, Body: store.Text(body)}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (d dbLibrary) Delete(kind, name string) (bool, error) {
	found := false
	err := d.st.Tx(func(tx *store.Store) error {
		res := tx.DB.Where("kind = ? AND name = ?", kind, name).Delete(&libraryItemRow{})
		if res.Error != nil {
			return res.Error
		}
		found = res.RowsAffected > 0
		return tx.DB.Where("kind = ? AND name = ?", kind, name).Delete(&libraryFileRow{}).Error
	})
	return found, err
}

func descriptionOf(manifest []byte) string {
	var m struct {
		Description string `json:"description"`
	}
	_ = json.Unmarshal(manifest, &m)
	return m.Description
}

// --- what a posted item must be ---------------------------------------------------------------

var reLibraryFile = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ErrLibraryItem is an item that is not valid: its message says what is wrong.
var ErrLibraryItem = errors.New("the item is not valid")

// CheckLibraryItem checks a posted item the way the library checks a folder: it writes the item as a folder and runs the same reader on it. It
// returns the manifest as it will be kept, or the problems (each a sentence) that leave it out.
func CheckLibraryItem(kind, name string, manifest []byte, files map[string]string) (problems []string) {
	if !reName.MatchString(name) {
		return []string{fmt.Sprintf("the name %q is not valid (lower case letters, digits and dashes)", name)}
	}
	m := fstest.MapFS{}
	dir := folderOf(kind) + "/" + name + "/"
	m[dir+"manifest.json"] = &fstest.MapFile{Data: manifest}
	var names []string
	for f, body := range files {
		if !reLibraryFile.MatchString(f) || strings.Contains(f, "..") {
			problems = append(problems, fmt.Sprintf("the file name %q is not valid", f))
			continue
		}
		m[dir+f] = &fstest.MapFile{Data: []byte(body)}
		names = append(names, f)
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return problems
	}
	var bad string
	var used map[string]bool
	if kind == KindBackground {
		var bg Background
		bg, bad = checkBackground(m, name, "user")
		used = map[string]bool{bg.File: true}
	} else {
		var c Character
		c, bad = checkCharacter(m, name, "user")
		used = map[string]bool{}
		for _, f := range c.Poses {
			used[f] = true
		}
	}
	if bad != "" {
		return []string{bad}
	}
	sort.Strings(names)
	for _, f := range names {
		if !used[f] {
			problems = append(problems, fmt.Sprintf("the file %q is not used by the manifest", f))
		}
	}
	return problems
}

// --- import ------------------------------------------------------------------------------------

// libraryImporter reads the folders of the user's library (characters/<name>/ and backgrounds/<name>/): entity music-motion/library. An item whose
// manifest is invalid or whose file is missing is left out with one entry in the record.
type libraryImporter struct{ dir string }

func (libraryImporter) Entity() string { return "music-motion/library" }

func (i libraryImporter) Import(ctx context.Context, tx *store.Store) (store.Report, error) {
	var rep store.Report
	if i.dir == "" {
		return rep, nil
	}
	fsys := os.DirFS(i.dir)
	var items []libraryItemRow
	var files []libraryFileRow
	add := func(kind, name string, files2 map[string]string, manifest []byte, desc string) {
		items = append(items, libraryItemRow{Kind: kind, Name: name, Description: clipRunes(desc, 500), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), ManifestJSON: store.Text(manifest)})
		for f, body := range files2 {
			files = append(files, libraryFileRow{Kind: kind, Name: name, File: f, Body: store.Text(body)})
		}
	}
	for _, kind := range []string{KindCharacter, KindBackground} {
		entries, _ := fs.ReadDir(fsys, folderOf(kind))
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			path := filepath.Join(i.dir, folderOf(kind), name)
			var bad string
			var used []string
			var desc string
			if kind == KindBackground {
				var bg Background
				bg, bad = checkBackground(fsys, name, "user")
				used, desc = []string{bg.File}, bg.Description
			} else {
				var c Character
				c, bad = checkCharacter(fsys, name, "user")
				desc = c.Description
				for _, f := range c.Poses {
					used = append(used, f)
				}
			}
			if bad != "" {
				warnOnce(folderOf(kind)+"/user/"+name+"#"+bad, fmt.Sprintf("the %s %q is left out: %s", kind, path, bad))
				rep.Skipped = append(rep.Skipped, store.Skipped{Path: path, Reason: bad})
				continue
			}
			manifest, _ := fs.ReadFile(fsys, folderOf(kind)+"/"+name+"/manifest.json")
			body := map[string]string{}
			for _, f := range used {
				b, _ := fs.ReadFile(fsys, folderOf(kind)+"/"+name+"/"+f)
				body[f] = string(b)
			}
			add(kind, name, body, manifest, desc)
		}
	}
	var err error
	if rep.Created, rep.Existing, err = store.InsertMissing(tx, items); err != nil {
		return rep, err
	}
	if _, _, err := store.InsertMissing(tx, files); err != nil { // never overwrites a drawing either
		return rep, err
	}
	return rep, nil
}

func (i libraryImporter) LegacyFiles() ([]store.LegacyFile, error) {
	if i.dir == "" {
		return nil, nil
	}
	var out []store.LegacyFile
	for _, kind := range []string{"characters", "backgrounds"} {
		_ = filepath.WalkDir(filepath.Join(i.dir, kind), func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() {
				if st, err := d.Info(); err == nil {
					out = append(out, store.LegacyFile{Path: p, Bytes: st.Size()})
				}
			}
			return nil
		})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Path < out[b].Path })
	return out, nil
}

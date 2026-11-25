package workspace

// projectstore.go: the projects in the database (change move-projects-to-database). A project is a row with its definition (the text of its
// scenes.yaml, byte for byte) and what the listings say about it, derived from the text every time it is saved; the versions of the definition,
// the pages of its motion scenes and the records of the calls of the generation API are rows too. The folder of a project keeps its sources, its
// tmp/ and out/ and the working copy of the definition, which Materialize writes before a run and Ingest takes back (see projectsync.go).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"veritmo/internal/platform/store"
	"veritmo/internal/project/config"
	"veritmo/internal/video/motion"
)

// KeepVersions is how many versions of a definition a project keeps.
const KeepVersions = 20

// The kinds a project can be (the column kind): an ordinary project, the four the programs make for themselves.
const (
	kindProject   = "project"
	kindAnimation = "animation"
)

// ErrProjectExists is a project that already is in the database; ErrNoProject one that is not; ErrNoVersion a version that is not.
var (
	ErrProjectExists = errors.New("the project already exists")
	ErrNoProject     = errors.New("the project does not exist")
	ErrNoVersion     = errors.New("the version does not exist")
)

// ProjectRow is a project as the database keeps it.
type ProjectRow struct {
	ID         string `gorm:"primaryKey;size:191"`
	Name       string `gorm:"size:255"`
	Mode       string `gorm:"size:16;index"`
	Kind       string `gorm:"size:16;index"`
	Client     string `gorm:"size:64;index"`
	Adult      *bool
	SceneCount int
	TrackCount int
	Valid      bool
	Error      store.Text
	// SizeBytes is what the disk held for the project when it was last measured; nil before the first measure.
	SizeBytes     *int64
	DefinitionSHA string `gorm:"size:64"`
	// CreatedAt and UpdatedAt are always set here, in UTC: the database must not stamp them in the local zone (a text column of mixed zones does not sort).
	CreatedAt  time.Time `gorm:"autoCreateTime:false"`
	UpdatedAt  time.Time `gorm:"index;autoUpdateTime:false"`
	Definition store.Text
}

// TableName is the table of the projects.
func (ProjectRow) TableName() string { return "projects" }

// ProjectVersion is a definition a save replaced.
type ProjectVersion struct {
	ID        uint   `gorm:"primaryKey;autoIncrement"`
	ProjectID string `gorm:"size:191;uniqueIndex:idx_project_version,priority:1"`
	// Version is the number of the version inside its project, from 1.
	Version    int    `gorm:"uniqueIndex:idx_project_version,priority:2"`
	Reason     string `gorm:"size:16"`
	Bytes      int
	CreatedAt  time.Time `gorm:"autoCreateTime:false"`
	Definition store.Text
}

// TableName is the table of the versions.
func (ProjectVersion) TableName() string { return "project_versions" }

// ProjectPage is the page of a motion scene (or of its overlay) and its provenance.
type ProjectPage struct {
	ProjectID string    `gorm:"primaryKey;size:191"`
	Scene     string    `gorm:"primaryKey;size:191"`
	Overlay   bool      `gorm:"primaryKey"`
	SHA       string    `gorm:"size:64"`
	UpdatedAt time.Time `gorm:"autoUpdateTime:false"`
	HTML      store.Text
	Meta      store.Text // motion.Meta as JSON; empty for a page with no provenance file
}

// TableName is the table of the pages.
func (ProjectPage) TableName() string { return "project_pages" }

// ManagedRow is the record of a call of the generation API (generation.json).
type ManagedRow struct {
	ID        string    `gorm:"primaryKey;size:191"`
	Kind      string    `gorm:"size:16;index"`
	RunID     string    `gorm:"size:64"`
	Client    string    `gorm:"size:64;index"`
	CreatedAt time.Time `gorm:"index"`
	Doc       store.Text
}

// TableName is the table of the records.
func (ManagedRow) TableName() string { return "managed_records" }

func projectsMigration() *store.Migration {
	return &store.Migration{
		ID: "202611192000_projects",
		Migrate: func(tx *gorm.DB) error {
			type projectRow struct {
				ID            string `gorm:"primaryKey;size:191"`
				Name          string `gorm:"size:255"`
				Mode          string `gorm:"size:16;index"`
				Kind          string `gorm:"size:16;index"`
				Client        string `gorm:"size:64;index"`
				Adult         *bool
				SceneCount    int
				TrackCount    int
				Valid         bool
				Error         store.Text
				SizeBytes     *int64
				DefinitionSHA string `gorm:"size:64"`
				CreatedAt     time.Time
				UpdatedAt     time.Time `gorm:"index"`
				Definition    store.Text
			}
			type projectVersion struct {
				ID         uint   `gorm:"primaryKey;autoIncrement"`
				ProjectID  string `gorm:"size:191;uniqueIndex:idx_project_version,priority:1"`
				Version    int    `gorm:"uniqueIndex:idx_project_version,priority:2"`
				Reason     string `gorm:"size:16"`
				Bytes      int
				CreatedAt  time.Time
				Definition store.Text
			}
			type projectPage struct {
				ProjectID string `gorm:"primaryKey;size:191"`
				Scene     string `gorm:"primaryKey;size:191"`
				Overlay   bool   `gorm:"primaryKey"`
				SHA       string `gorm:"size:64"`
				UpdatedAt time.Time
				HTML      store.Text
				Meta      store.Text
			}
			for table, model := range map[string]any{"projects": &projectRow{}, "project_versions": &projectVersion{}, "project_pages": &projectPage{}} {
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
			for _, t := range []string{"project_pages", "project_versions", "projects"} {
				if err := tx.Migrator().DropTable(t); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func managedRecordsMigration() *store.Migration {
	return &store.Migration{
		ID: "202611192010_managed_records",
		Migrate: func(tx *gorm.DB) error {
			type managedRecord struct {
				ID        string    `gorm:"primaryKey;size:191"`
				Kind      string    `gorm:"size:16;index"`
				RunID     string    `gorm:"size:64"`
				Client    string    `gorm:"size:64;index"`
				CreatedAt time.Time `gorm:"index"`
				Doc       store.Text
			}
			if tx.Migrator().HasTable("managed_records") {
				return nil
			}
			return tx.Table("managed_records").Migrator().CreateTable(&managedRecord{})
		},
		Rollback: func(tx *gorm.DB) error { return tx.Migrator().DropTable("managed_records") },
	}
}

// sha is the hash of a text, as the columns and the comparisons with the files use it.
func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// projectKindOf is the kind of a project read from its id.
func projectKindOf(id string) string {
	switch {
	case IsAnimationProject(id):
		return kindAnimation
	case managedKindOfID(id) != "":
		return string(managedKindOfID(id))
	}
	return kindProject
}

// deriveProject fills the columns the listings use from the text of a definition: it parses it with the loader of the pipeline and, when that
// fails, marks the row invalid with the message and reads the client with a lenient reader (what the list always did for an invalid file).
// managedClient is the client the request of a managed project stored; it is used when the text names none.
func deriveProject(id string, definition []byte, managedClient string) ProjectRow {
	row := ProjectRow{ID: id, Kind: projectKindOf(id), Definition: store.Text(definition), DefinitionSHA: sha(definition)}
	var head struct {
		Client string `yaml:"client"`
	}
	_ = yaml.Unmarshal(definition, &head)
	client := head.Client
	cfg, err := config.LoadBytes(definition)
	if err != nil {
		row.Error = store.Text(err.Error())
	} else {
		mode := cfg.Output.Mode
		if mode == "" {
			mode = config.ModeVideo
		}
		row.Valid, row.Name, row.Mode = true, cfg.Project, string(mode)
		row.SceneCount, row.TrackCount, row.Adult = len(cfg.Scenes), len(cfg.Tracks), cfg.Adult
		if cfg.Client != "" {
			client = cfg.Client
		}
	}
	if client == "" {
		client = managedClient
	}
	if client == "" {
		if row.Kind == kindProject {
			client = clientEmbedded
		} else {
			client = ClientUnknown
		}
	}
	row.Client = client
	return row
}

// projectStore is the projects of the database.
type projectStore struct {
	db *store.Store
	mu sync.Map // project id -> *sync.Mutex: Save, Materialize and Ingest of one project take turns
	// synced is, for each project, the hash of the definition the last Materialize wrote or Ingest took back.
	synced sync.Map
	// onDelete are what other areas delete with a project, in the same transaction (the songs of the library).
	onDelete []func(tx *store.Store, id string) error
}

func newProjectStore(db *store.Store) *projectStore { return &projectStore{db: db} }

// lock takes the turn of a project and returns what gives it back.
func (p *projectStore) lock(id string) func() {
	m, _ := p.mu.LoadOrStore(id, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// Get reads a project with its definition.
func (p *projectStore) Get(id string) (ProjectRow, error) {
	var r ProjectRow
	if err := p.db.DB.Where("id = ?", id).Take(&r).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ProjectRow{}, ErrNoProject
		}
		return ProjectRow{}, err
	}
	return r, nil
}

// Exists says whether the project is in the database.
func (p *projectStore) Exists(id string) bool {
	var n int64
	p.db.DB.Model(&ProjectRow{}).Where("id = ?", id).Count(&n)
	return n > 0
}

// managedClientOf is the client the stored request of a managed project names, "" when there is none.
func (p *projectStore) managedClientOf(id string) string {
	var rec ManagedRow
	if p.db.DB.Select("client").Where("id = ?", id).Take(&rec).Error != nil {
		return ""
	}
	return rec.Client
}

// Create keeps a new project: its row (and no version). ErrProjectExists when the id is taken.
func (p *projectStore) Create(id string, definition []byte) error {
	defer p.lock(id)()
	row := deriveProject(id, definition, p.managedClientOf(id))
	now := time.Now().UTC()
	row.CreatedAt, row.UpdatedAt = now, now
	res := p.db.DB.Create(&row)
	if res.Error != nil {
		var n int64
		if p.db.DB.Model(&ProjectRow{}).Where("id = ?", id).Count(&n); n > 0 {
			return ErrProjectExists
		}
		return res.Error
	}
	return nil
}

// Save makes a text the definition of a project. When it changes it, the text it replaces is kept as a version (with the reason) and the
// derived columns are filled again; the same text changes nothing and adds no version. The project must exist (ErrNoProject).
func (p *projectStore) Save(id string, definition []byte, reason string) (changed bool, err error) {
	defer p.lock(id)()
	return p.save(p.db, id, definition, reason)
}

func (p *projectStore) save(st *store.Store, id string, definition []byte, reason string) (changed bool, err error) {
	err = st.Tx(func(tx *store.Store) error {
		var old ProjectRow
		if e := tx.DB.Where("id = ?", id).Take(&old).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return ErrNoProject
			}
			return e
		}
		if old.DefinitionSHA == sha(definition) {
			return nil
		}
		changed = true
		var last int
		tx.DB.Model(&ProjectVersion{}).Where("project_id = ?", id).Select("COALESCE(MAX(version), 0)").Scan(&last)
		now := time.Now().UTC()
		if e := tx.DB.Create(&ProjectVersion{ProjectID: id, Version: last + 1, Reason: reason, Bytes: len(old.Definition), CreatedAt: now, Definition: old.Definition}).Error; e != nil {
			return e
		}
		if e := tx.DB.Where("project_id = ? AND version <= ?", id, last+1-KeepVersions).Delete(&ProjectVersion{}).Error; e != nil {
			return e
		}
		row := deriveProject(id, definition, p.managedClientOf(id))
		return tx.DB.Model(&ProjectRow{}).Where("id = ?", id).Updates(map[string]any{
			"name": row.Name, "mode": row.Mode, "client": row.Client, "adult": row.Adult, "scene_count": row.SceneCount, "track_count": row.TrackCount,
			"valid": row.Valid, "error": row.Error, "definition_sha": row.DefinitionSHA, "definition": row.Definition, "updated_at": now,
		}).Error
	})
	return changed, err
}

// Delete removes the rows of a project (it and its versions and pages; the record of a managed project too). The folder is the caller's.
func (p *projectStore) Delete(id string) error {
	defer p.lock(id)()
	return p.db.Tx(func(tx *store.Store) error {
		for _, m := range []any{&ProjectVersion{}, &ProjectPage{}} {
			if err := tx.DB.Where("project_id = ?", id).Delete(m).Error; err != nil {
				return err
			}
		}
		if err := tx.DB.Where("id = ?", id).Delete(&ManagedRow{}).Error; err != nil {
			return err
		}
		for _, hook := range p.onDelete {
			if err := hook(tx, id); err != nil {
				return err
			}
		}
		res := tx.DB.Where("id = ?", id).Delete(&ProjectRow{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrNoProject
		}
		return nil
	})
}

// Duplicate makes a project of another's definition and pages (not its versions, its record or its files).
func (p *projectStore) Duplicate(id, newID string) error {
	src, err := p.Get(id)
	if err != nil {
		return err
	}
	return p.db.Tx(func(tx *store.Store) error {
		ps := &projectStore{db: tx}
		if err := ps.Create(newID, []byte(src.Definition)); err != nil {
			return err
		}
		var pages []ProjectPage
		if err := tx.DB.Where("project_id = ?", id).Find(&pages).Error; err != nil {
			return err
		}
		for _, pg := range pages {
			pg.ProjectID = newID
			if err := tx.DB.Create(&pg).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// Versions lists the versions of a project, newest first, without their text.
func (p *projectStore) Versions(id string) ([]ProjectVersion, error) {
	if !p.Exists(id) {
		return nil, ErrNoProject
	}
	var out []ProjectVersion
	err := p.db.DB.Omit("definition").Where("project_id = ?", id).Order("version DESC").Find(&out).Error
	return out, err
}

// Version reads one version with its text.
func (p *projectStore) Version(id string, n int) (ProjectVersion, error) {
	var v ProjectVersion
	if err := p.db.DB.Where("project_id = ? AND version = ?", id, n).Take(&v).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if !p.Exists(id) {
				return v, ErrNoProject
			}
			return v, ErrNoVersion
		}
		return v, err
	}
	return v, nil
}

// Restore makes a version the definition of the project (the text it replaces becomes a version). The caller validates the text first, as any save.
func (p *projectStore) Restore(id string, n int) error {
	v, err := p.Version(id, n)
	if err != nil {
		return err
	}
	_, err = p.Save(id, []byte(v.Definition), "restore")
	return err
}

// SetSize keeps the size measured for a project.
func (p *projectStore) SetSize(id string, bytes int64) error {
	return p.db.DB.Model(&ProjectRow{}).Where("id = ?", id).Update("size_bytes", bytes).Error
}

// TouchClient fills the client of a managed project from its record when the definition names none (the column is derived from the text, and the
// record is written after the project).
func (p *projectStore) TouchClient(id string) error {
	row, err := p.Get(id)
	if err != nil {
		return err
	}
	derived := deriveProject(id, []byte(row.Definition), p.managedClientOf(id))
	return p.db.DB.Model(&ProjectRow{}).Where("id = ?", id).Update("client", derived.Client).Error
}

// --- pages -------------------------------------------------------------------------------------

// Page reads the page of a scene (or of its overlay) and its provenance.
func (p *projectStore) Page(id, scene string, overlay bool) (html string, meta motion.Meta, hasMeta bool, found bool) {
	var pg ProjectPage
	if err := p.db.DB.Where("project_id = ? AND scene = ? AND overlay = ?", id, scene, overlay).Take(&pg).Error; err != nil {
		return "", motion.Meta{}, false, false
	}
	if pg.Meta != "" && json.Unmarshal([]byte(pg.Meta), &meta) == nil {
		hasMeta = true
	}
	return string(pg.HTML), meta, hasMeta, true
}

// PutPage keeps (creates or replaces) the page of a scene. meta nil keeps no provenance.
func (p *projectStore) PutPage(id, scene string, overlay bool, html string, meta *motion.Meta) error {
	var metaText store.Text
	if meta != nil {
		b, err := json.Marshal(meta)
		if err != nil {
			return err
		}
		metaText = store.Text(b)
	}
	pg := ProjectPage{ProjectID: id, Scene: scene, Overlay: overlay, SHA: sha([]byte(html)), UpdatedAt: time.Now().UTC(), HTML: store.Text(html), Meta: metaText}
	return p.db.DB.Clauses(clause.OnConflict{UpdateAll: true}).Create(&pg).Error
}

// DeletePage removes the page of a scene; false when there was none.
func (p *projectStore) DeletePage(id, scene string, overlay bool) (bool, error) {
	res := p.db.DB.Where("project_id = ? AND scene = ? AND overlay = ?", id, scene, overlay).Delete(&ProjectPage{})
	return res.RowsAffected > 0, res.Error
}

// Pages lists the pages of a project.
func (p *projectStore) Pages(id string) ([]ProjectPage, error) {
	var out []ProjectPage
	err := p.db.DB.Where("project_id = ?", id).Order("scene, overlay").Find(&out).Error
	return out, err
}

func (r ProjectRow) String() string { return fmt.Sprintf("%s (%s)", r.ID, strings.TrimSpace(r.Name)) }

// ProjectsListSpec says what the list of projects can be sorted and searched by.
var ProjectsListSpec = store.ListSpec{
	Sortable:   map[string]string{"id": "id", "name": "name", "updated_at": "updated_at"},
	Searchable: []string{"id", "name"},
	Default:    "id",
	Tie:        "id",
}

// ProjectFilter is what a listing keeps: the managed projects only when asked for, an animation never, the clients named, and what the content
// mode of the server shows.
type ProjectFilter struct {
	// IncludeManaged lists the generations and the edits of the generation API too.
	IncludeManaged bool
	// Clients, when not empty, keeps only those clients.
	Clients []string
	// HideAdult leaves out the projects marked for adults; OnlyReviewed keeps only the ones marked fine (the strict mode).
	HideAdult, OnlyReviewed bool
}

// List reads the projects (without their definitions) that the filter and the parameters keep, and how many that is in all.
func (p *projectStore) List(f ProjectFilter, params store.ListParams) ([]ProjectRow, int64, error) {
	base := p.db.DB.Model(&ProjectRow{}).Omit("definition")
	kinds := []string{kindProject}
	if f.IncludeManaged {
		kinds = append(kinds, string(KindGeneration), string(KindImageEdit), string(KindVideoEdit), string(KindModelGeneration))
	}
	base = base.Where("kind IN ?", kinds)
	if len(f.Clients) > 0 {
		base = base.Where("client IN ?", f.Clients)
	}
	switch {
	case f.OnlyReviewed:
		base = base.Where("adult = ?", false)
	case f.HideAdult:
		base = base.Where("(adult IS NULL OR adult = ?)", false)
	}
	base = store.Filter(base, params, ProjectsListSpec)
	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	q := store.Order(base, params, ProjectsListSpec)
	if params.Limit > 0 {
		q = q.Limit(params.Limit)
	}
	if params.Offset > 0 {
		q = q.Offset(params.Offset)
	}
	var rows []ProjectRow
	if err := q.Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// PruneVersions deletes the versions of a project but the keep newest, and says how many bytes that freed.
func (p *projectStore) PruneVersions(id string, keep int) (int64, error) {
	var vs []ProjectVersion
	if err := p.db.DB.Omit("definition").Where("project_id = ?", id).Order("version DESC").Find(&vs).Error; err != nil {
		return 0, err
	}
	var freed int64
	var drop []uint
	for i, v := range vs {
		if i >= keep {
			drop = append(drop, v.ID)
			freed += int64(v.Bytes)
		}
	}
	if len(drop) == 0 {
		return 0, nil
	}
	return freed, p.db.DB.Where("id IN ?", drop).Delete(&ProjectVersion{}).Error
}

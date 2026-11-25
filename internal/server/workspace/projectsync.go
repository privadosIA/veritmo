package workspace

// projectsync.go: the folder of a project and its row (design D3 of move-projects-to-database). The pipeline works on files, so before anything that
// reads the definition or the pages from the folder the server writes them there (Materialize), and after a run that may have changed them it takes
// what changed back into the database (Ingest). Both compare hashes, not times: the media folder may be on a filesystem with coarse times, and a
// copy or a restore changes times.

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"veritmo/internal/platform/store"
	"veritmo/internal/server/api"
	"veritmo/internal/video/motion"
)

const definitionFile = "scenes.yaml"

// fileSHA is the hash of a file, "" when it cannot be read.
func fileSHA(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return sha(b), true
}

// pageName reads a file of motion/ that is the page of a scene: <scene>.html or <scene>.overlay.html.
func pageName(file string) (scene string, overlay bool, ok bool) {
	if !strings.HasSuffix(file, ".html") {
		return "", false, false
	}
	base := strings.TrimSuffix(file, ".html")
	if s, found := strings.CutSuffix(base, ".overlay"); found {
		return s, true, s != ""
	}
	return base, false, base != ""
}

// Materialize writes the definition and the pages of a project to its folder dir, the files that differ from the saved ones: the definition to
// scenes.yaml, each page to motion/ with its provenance, and it removes a page file that has no row. It touches no other file of motion/. A
// scenes.yaml that holds something other than the saved definition or the text it wrote last is overwritten, with one line in the log.
func (p *projectStore) Materialize(id, dir string) error {
	defer p.lock(id)()
	return p.materialize(id, dir)
}

func (p *projectStore) materialize(id, dir string) error {
	row, err := p.Get(id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, definitionFile)
	have, exists := fileSHA(path)
	if !exists || have != row.DefinitionSHA {
		if exists && have != p.lastSeen(id) {
			slog.Warn("scenes.yaml was changed by hand and is overwritten by the saved definition (change a definition with PUT /api/projects/{id}/config or veritmo project push)", "project", id)
		}
		if err := api.AtomicWrite(path, []byte(row.Definition)); err != nil {
			return err
		}
	}
	p.setBase(id, row.DefinitionSHA)

	pages, err := p.Pages(id)
	if err != nil {
		return err
	}
	wanted := map[string]bool{}
	for _, pg := range pages {
		page := motion.PagePath(dir, pg.Scene, pg.Overlay)
		wanted[filepath.Base(page)] = true
		if cur, ok := fileSHA(page); !ok || cur != pg.SHA {
			if err := motion.Store(dir, pg.Scene, pg.Overlay, string(pg.HTML)); err != nil {
				return err
			}
		}
		if pg.Meta != "" {
			var m motion.Meta
			if json.Unmarshal([]byte(pg.Meta), &m) == nil {
				if cur, ok := motion.LoadMeta(dir, pg.Scene, pg.Overlay); !ok || cur != m {
					if err := motion.StoreMeta(dir, pg.Scene, pg.Overlay, m); err != nil {
						return err
					}
				}
			}
		}
	}
	entries, _ := os.ReadDir(filepath.Join(dir, motion.Dir))
	for _, e := range entries {
		if _, _, isPage := pageName(e.Name()); isPage && !e.IsDir() && !wanted[e.Name()] {
			_ = os.Remove(filepath.Join(dir, motion.Dir, e.Name())) // a page the API no longer has
		}
	}
	return nil
}

// Ingest takes back into the database what a run (or a fix, or the extension of a clip) changed in the folder: a scenes.yaml that differs from the
// saved definition becomes the definition (the text it replaces a version, with reason), and a page that differs becomes the page. A page file that
// is gone is not a deletion: only the API deletes pages. When the definition was saved by someone else while the run worked, that text stays the
// definition and the one the run left is kept as a version.
func (p *projectStore) Ingest(id, dir, reason string) error {
	defer p.lock(id)()
	return p.ingest(id, dir, reason)
}

func (p *projectStore) ingest(id, dir, reason string) error {
	row, err := p.Get(id)
	if err != nil {
		return err
	}
	if raw, err := os.ReadFile(filepath.Join(dir, definitionFile)); err == nil && sha(raw) != row.DefinitionSHA {
		if base := p.base(id); base != "" && base != row.DefinitionSHA {
			// the saved definition moved on since the run started: keep the run's text as a version, not as the definition
			if err := p.addVersion(id, raw, reason); err != nil {
				return err
			}
		} else if _, err := p.save(p.db, id, raw, reason); err != nil {
			return err
		}
		p.setBase(id, sha(raw))
	}
	entries, _ := os.ReadDir(filepath.Join(dir, motion.Dir))
	for _, e := range entries {
		scene, overlay, isPage := pageName(e.Name())
		if !isPage || e.IsDir() {
			continue
		}
		html, err := os.ReadFile(filepath.Join(dir, motion.Dir, e.Name()))
		if err != nil {
			continue
		}
		meta, hasMeta := motion.LoadMeta(dir, scene, overlay)
		curHTML, curMeta, curHas, found := p.Page(id, scene, overlay)
		if found && curHTML == string(html) && curHas == hasMeta && (!hasMeta || curMeta == meta) {
			continue
		}
		var m *motion.Meta
		if hasMeta {
			m = &meta
		}
		if err := p.PutPage(id, scene, overlay, string(html), m); err != nil {
			return err
		}
	}
	return nil
}

// addVersion keeps a text as a version without changing the definition.
func (p *projectStore) addVersion(id string, text []byte, reason string) error {
	return p.db.Tx(func(tx *store.Store) error {
		var last int
		tx.DB.Model(&ProjectVersion{}).Where("project_id = ?", id).Select("COALESCE(MAX(version), 0)").Scan(&last)
		if err := tx.DB.Create(&ProjectVersion{ProjectID: id, Version: last + 1, Reason: reason, Bytes: len(text), CreatedAt: time.Now().UTC(), Definition: store.Text(text)}).Error; err != nil {
			return err
		}
		return tx.DB.Where("project_id = ? AND version <= ?", id, last+1-KeepVersions).Delete(&ProjectVersion{}).Error
	})
}

// base is the hash of the definition the last Materialize wrote (or the last Ingest took) for a project; "" when this process has not synced it.
func (p *projectStore) base(id string) string {
	v, _ := p.synced.Load(id)
	s, _ := v.(string)
	return s
}

func (p *projectStore) setBase(id, hash string) { p.synced.Store(id, hash) }

// lastSeen is the same hash, as the log line that reports an overwritten hand edit uses it.
func (p *projectStore) lastSeen(id string) string { return p.base(id) }

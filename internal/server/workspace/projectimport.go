package workspace

// projectimport.go: the import of the projects that were kept as folders into the database (spec data-store; entity projects, design D5 of
// move-projects-to-database): every folder of the projects folder that holds a scenes.yaml becomes a row (an invalid definition too, marked as such),
// each scenes.yaml.* backup a version, each page of motion/ a page, and each generation.json a record. Nothing is overwritten and no file is changed.

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"veritmo/internal/platform/store"
	"veritmo/internal/server/api"
	"veritmo/internal/video/motion"
)

// RegisterProjectsImporter adds to a registry the importer of the projects kept in the folder projectsDir (server.DataRegistry calls it).
func RegisterProjectsImporter(r *store.Registry, projectsDir string) {
	r.AddImporter(ProjectsImporter{Dir: projectsDir})
}

// ProjectsImporter reads the projects folder.
type ProjectsImporter struct{ Dir string }

// Entity is the name of the entity.
func (ProjectsImporter) Entity() string { return "projects" }

func (i ProjectsImporter) projectDirs() []string {
	entries, err := os.ReadDir(i.Dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && api.ProjectIDPattern.MatchString(e.Name()) {
			if _, err := os.Stat(filepath.Join(i.Dir, e.Name(), definitionFile)); err == nil {
				out = append(out, e.Name())
			}
		}
	}
	return out
}

// Import creates the rows that are missing.
func (i ProjectsImporter) Import(ctx context.Context, tx *store.Store) (store.Report, error) {
	var rep store.Report
	var rows []ProjectRow
	var versions []ProjectVersion
	var pages []ProjectPage
	var records []ManagedRow
	for _, id := range i.projectDirs() {
		dir := filepath.Join(i.Dir, id)
		def, err := os.ReadFile(filepath.Join(dir, definitionFile))
		if err != nil {
			rep.Skipped = append(rep.Skipped, store.Skipped{Path: filepath.Join(dir, definitionFile), Reason: err.Error()})
			continue
		}
		var managedClient string
		if rec, ok := readManagedFile(dir, id); ok {
			row, _ := managedRowOf(rec)
			records = append(records, row)
			managedClient = row.Client
		}
		row := deriveProject(id, def, managedClient)
		when := time.Now().UTC()
		if fi, err := os.Stat(filepath.Join(dir, definitionFile)); err == nil {
			when = fi.ModTime().UTC()
		}
		size := walkSize(dir)
		row.CreatedAt, row.UpdatedAt, row.SizeBytes = when, when, &size
		rows = append(rows, row)
		versions = append(versions, backupVersions(dir, id)...)
		pages = append(pages, pagesOf(dir, id)...)
	}
	var err error
	if rep.Created, rep.Existing, err = store.InsertMissing(tx, rows); err != nil {
		return rep, err
	}
	for _, batch := range []func() error{
		func() error { _, _, e := store.InsertMissing(tx, records); return e },
		func() error { _, _, e := store.InsertMissing(tx, versions); return e },
		func() error { _, _, e := store.InsertMissing(tx, pages); return e },
	} {
		if err := batch(); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// readManagedFile reads the generation.json of a managed project.
func readManagedFile(dir, id string) (*ManagedInfo, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, ManagedRecord))
	if err != nil {
		return nil, false
	}
	var info ManagedInfo
	if json.Unmarshal(raw, &info) != nil || info.ID != id {
		return nil, false
	}
	return &info, true
}

// backupVersions are the scenes.yaml.* files of a project as versions, the oldest first (by modification time, then by name).
func backupVersions(dir, id string) []ProjectVersion {
	entries, _ := os.ReadDir(dir)
	type bak struct {
		name string
		mod  time.Time
	}
	var baks []bak
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasPrefix(e.Name(), definitionFile+".") {
			if fi, err := e.Info(); err == nil {
				baks = append(baks, bak{e.Name(), fi.ModTime().UTC()})
			}
		}
	}
	sort.Slice(baks, func(a, b int) bool {
		if !baks[a].mod.Equal(baks[b].mod) {
			return baks[a].mod.Before(baks[b].mod)
		}
		return baks[a].name < baks[b].name
	})
	var out []ProjectVersion
	for n, b := range baks {
		raw, err := os.ReadFile(filepath.Join(dir, b.name))
		if err != nil {
			continue
		}
		out = append(out, ProjectVersion{ProjectID: id, Version: n + 1, Reason: "import", Bytes: len(raw), CreatedAt: b.mod, Definition: store.Text(raw)})
	}
	return out
}

// pagesOf are the pages of motion/ of a project with their provenance.
func pagesOf(dir, id string) []ProjectPage {
	entries, _ := os.ReadDir(filepath.Join(dir, motion.Dir))
	var out []ProjectPage
	for _, e := range entries {
		scene, overlay, ok := pageName(e.Name())
		if !ok || !e.Type().IsRegular() {
			continue
		}
		html, err := os.ReadFile(filepath.Join(dir, motion.Dir, e.Name()))
		if err != nil {
			continue
		}
		pg := ProjectPage{ProjectID: id, Scene: scene, Overlay: overlay, SHA: sha(html), UpdatedAt: time.Now().UTC(), HTML: store.Text(html)}
		if fi, err := e.Info(); err == nil {
			pg.UpdatedAt = fi.ModTime().UTC()
		}
		if meta, ok := motion.LoadMeta(dir, scene, overlay); ok {
			b, _ := json.Marshal(meta)
			pg.Meta = store.Text(b)
		}
		out = append(out, pg)
	}
	return out
}

// walkSize is the bytes of the files of a project folder that count for its storage: the ones that belong to a part (tmp/, out/, sources/, the
// reviews and the backups; not scenes.yaml itself, which the report never counted). A link at its top level (a project kept in the media directory
// has its folders as links) is followed once, no deeper.
func walkSize(dir string) int64 {
	var total int64
	var walk func(root, prefix string, followTop bool)
	count := func(rel string, size int64) {
		if partOf(filepath.ToSlash(rel), nil) != "" {
			total += size
		}
	}
	walk = func(root, prefix string, followTop bool) {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.Join(prefix, rel)
			if d.Type()&fs.ModeSymlink != 0 {
				if followTop && filepath.Dir(p) == root {
					if fi, err := os.Stat(p); err == nil {
						if fi.IsDir() {
							walk(p, rel, false)
						} else {
							count(rel, fi.Size())
						}
					}
				}
				return nil
			}
			if d.Type().IsRegular() {
				if fi, err := d.Info(); err == nil {
					count(rel, fi.Size())
				}
			}
			return nil
		})
	}
	walk(dir, "", true)
	return total
}

// LegacyFiles are the state files of the entity that stay on disk after the import: the backups of the definitions and the records of the
// generation API. The scenes.yaml and the pages stay as the working copy of the folder and are never listed.
func (i ProjectsImporter) LegacyFiles() ([]store.LegacyFile, error) {
	var out []store.LegacyFile
	entries, err := os.ReadDir(i.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(i.Dir, e.Name()))
		for _, f := range files {
			if f.Type().IsRegular() && (strings.HasPrefix(f.Name(), definitionFile+".") || f.Name() == ManagedRecord) {
				if fi, err := f.Info(); err == nil {
					out = append(out, store.LegacyFile{Path: filepath.Join(i.Dir, e.Name(), f.Name()), Bytes: fi.Size()})
				}
			}
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Path < out[b].Path })
	return out, nil
}

// ImportFolders imports the projects of the projects folder that are not in the database yet (what the importer does, for a caller that has no
// registry: the program at start and the tests).
func (h *Handler) ImportFolders() error {
	return h.projects.db.Tx(func(tx *store.Store) error {
		_, err := ProjectsImporter{Dir: h.cfg.ProjectsDir}.Import(context.Background(), tx)
		return err
	})
}

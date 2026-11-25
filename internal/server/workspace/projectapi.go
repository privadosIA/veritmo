package workspace

// projectapi.go: what the handlers of the server (and the other families) use of the projects in the database: the definition of a project is read
// from its row, a save goes through projectStore.Save, and the folder is synced around what reads it (design D3 of move-projects-to-database).

import (
	"errors"
	"fmt"
	"os"

	"veritmo/internal/project/config"
)

// ProjectExists reports whether id names a project of the database.
func (h *Handler) ProjectExists(id string) bool { return h.projects.Exists(id) }

// ProjectDefinition is the saved text of the scenes.yaml of a project.
func (h *Handler) ProjectDefinition(id string) ([]byte, error) {
	row, err := h.projects.Get(id)
	if err != nil {
		return nil, err
	}
	return []byte(row.Definition), nil
}

// ProjectConfig is the saved definition of a project parsed with the loader of the pipeline (no validation against the files of the folder).
func (h *Handler) ProjectConfig(id string) (*config.Config, error) {
	raw, err := h.ProjectDefinition(id)
	if err != nil {
		return nil, fmt.Errorf("config: cannot read the project %q: %w", id, err)
	}
	return config.LoadBytes(raw)
}

// SaveProjectDefinition makes a text the definition of a project (the one it replaces is kept as a version, with the reason). The text must
// already be validated by the caller.
func (h *Handler) SaveProjectDefinition(id string, definition []byte, reason string) error {
	changed, err := h.projects.Save(id, definition, reason)
	if err == nil && changed {
		h.syncLibraryQuietly(id) // the title, the favourites and the client of its songs come from the definition
	}
	return err
}

// MaterializeProject writes the definition and the pages of a project to its folder, for what reads them there (a run, a check, an export).
func (h *Handler) MaterializeProject(id string) error {
	return h.projects.Materialize(id, h.ProjectDir(id))
}

// IngestProject takes back into the database what something changed in the folder of a project (a run, a fix, the extension of a clip) and
// measures its size again.
func (h *Handler) IngestProject(id, reason string) error {
	if !h.projects.Exists(id) {
		return nil
	}
	if err := h.projects.Ingest(id, h.ProjectDir(id), reason); err != nil {
		return err
	}
	return h.refreshProjectSize(id)
}

// refreshProjectSize measures the folder of a project and keeps the size with it.
func (h *Handler) refreshProjectSize(id string) error {
	if _, err := os.Stat(h.ProjectDir(id)); err != nil {
		return nil
	}
	_, total := h.projectStorage(id)
	return h.projects.SetSize(id, total)
}

// Projects is the projects of the database (the other families make their projects through it).
func (h *Handler) Projects() *projectStore { return h.projects }

// ProjectAdult is the adult label of a project (its row): true, false or nil when it was not looked at; exists is false for an unknown project.
func (h *Handler) ProjectAdult(id string) (adult *bool, exists bool) {
	var row ProjectRow
	if err := h.projects.db.DB.Select("id", "adult").Where("id = ?", id).Take(&row).Error; err != nil {
		return nil, false
	}
	return row.Adult, true
}

// keepSize keeps a size just measured with the project.
func (h *Handler) keepSize(id string, total int64) { _ = h.projects.SetSize(id, total) }

// keepSizeOf measures the folder of a project and keeps the size.
func (h *Handler) keepSizeOf(id string) { _ = h.refreshProjectSize(id) }

// SyncProjectFromFolder takes a project that something wrote in its folder (a piece writes its pages and its scenes.yaml itself) into the
// database: the row is made when there is none, and the definition and the pages that differ from the saved ones become the saved ones.
func (h *Handler) SyncProjectFromFolder(id, reason string) error {
	if !h.projects.Exists(id) {
		raw, err := os.ReadFile(h.ProjectConfigPath(id))
		if err != nil {
			return err
		}
		if err := h.projects.Create(id, raw); err != nil && !errors.Is(err, ErrProjectExists) {
			return err
		}
	}
	return h.IngestProject(id, reason)
}

// projectIDs are the ids of every project of the database (managed ones too), in order: what a listing that looks at the files of each project
// goes through, instead of reading the projects folder.
func (h *Handler) projectIDs() []string {
	var ids []string
	h.projects.db.DB.Model(&ProjectRow{}).Order("id").Pluck("id", &ids)
	return ids
}

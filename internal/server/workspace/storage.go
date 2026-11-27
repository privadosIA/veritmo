package workspace

// storage.go shows what takes the space of a project and cleans the parts that
// can be cleaned (spec storage). Symbolic links are never followed or counted:
// a project's models/ and voices/ are links to folders of the whole install.

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"veritmo/internal/platform/legacyname"
	"veritmo/internal/platform/media"
	"veritmo/internal/platform/store"
	"veritmo/internal/server/api"

	"veritmo/internal/project/config"
	"veritmo/internal/project/pipeline"
)

// Parts of a project, in the order they are shown.
const (
	partCache      = "cache"
	partLeftovers  = "leftovers"
	partClips      = "clips"
	partAnimations = "animations"
	partFinal      = "final"
	partSources    = "sources"
	partReviews    = "reviews"
	partBackups    = "backups"
)

var storageParts = []string{partCache, partLeftovers, partClips, partAnimations, partFinal, partSources, partReviews, partBackups}

// cleanable says which parts the user may clean; final and sources are the results and the inputs.
var cleanable = map[string]bool{partCache: true, partLeftovers: true, partClips: true, partAnimations: true, partReviews: true, partBackups: true}

type storagePart struct {
	ID        string `json:"id"`
	Bytes     int64  `json:"bytes"`
	Files     int    `json:"files"`
	Cleanable bool   `json:"cleanable"`
}

// fileRef is one regular file of a project and the part it belongs to.
type fileRef struct {
	path string
	part string
	size int64
}

// projectFiles lists the regular files of a project with their part, never following symbolic links.
func (h *Handler) projectFiles(id string) []fileRef {
	dir := h.ProjectDir(id)
	var ids map[string]bool
	if raw, err := h.ProjectDefinition(id); err == nil {
		if cfg, err := config.LoadBytes(raw); err == nil {
			ids = pipeline.SceneIDs(cfg.Scenes)
		}
	}
	imported := h.versionDigests(id)
	var out []fileRef
	// A project kept in the media directory is counted from there: its config/ folder takes the place of the
	// project folder's own files, so the parts are the same as for a project that is whole inside projects/.
	root, strip := dir, ""
	if h.cfg.MediaDir != "" && media.Linked(dir, h.cfg.MediaDir, id) {
		root, strip = media.Folder(h.cfg.MediaDir, id), "config/"
	}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = strings.TrimPrefix(filepath.ToSlash(rel), strip)
		part := partOf(rel, ids)
		if part == "" {
			return nil
		}
		if part == partBackups && imported[fileDigest(p)] {
			return nil // a backup the import already took as a version: it is counted there
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		out = append(out, fileRef{path: p, part: part, size: fi.Size()})
		return nil
	})
	return out
}

// partOf classifies a file by its path relative to the project; "" is a file that belongs to no part
// (scenes.yaml itself).
func partOf(rel string, ids map[string]bool) string {
	switch {
	case strings.HasPrefix(rel, "tmp/"):
		if ids != nil && pipeline.IsLeftover(strings.TrimPrefix(rel, "tmp/"), ids) {
			return partLeftovers
		}
		return partCache
	case strings.HasPrefix(rel, "out/clips/"):
		if strings.HasSuffix(rel, ".anim.mp4") {
			return partAnimations
		}
		return partClips
	case strings.HasPrefix(rel, "out/"):
		return partFinal
	case strings.HasPrefix(rel, "sources/"):
		return partSources
	case strings.HasPrefix(rel, ".reviews/"):
		return partReviews
	case strings.HasPrefix(rel, "scenes.yaml."):
		return partBackups
	}
	return ""
}

// projectStorage is the breakdown of a project and its total.
func (h *Handler) projectStorage(id string) ([]storagePart, int64) {
	sums := map[string]*storagePart{}
	for _, p := range storageParts {
		sums[p] = &storagePart{ID: p, Cleanable: cleanable[p]}
	}
	var total int64
	for _, f := range h.projectFiles(id) {
		sums[f.part].Bytes += f.size
		sums[f.part].Files++
		total += f.size
	}
	// the backups of a definition are its versions in the database; a scenes.yaml.* file left behind counts with them
	if vs, err := h.projects.Versions(id); err == nil {
		for _, v := range vs {
			sums[partBackups].Bytes += int64(v.Bytes)
			sums[partBackups].Files++
			total += int64(v.Bytes)
		}
	}
	out := make([]storagePart, 0, len(storageParts))
	for _, p := range storageParts {
		out = append(out, *sums[p])
	}
	return out, total
}

// GET /api/projects/{id}/storage
func (h *Handler) handleProjectStorage(w http.ResponseWriter, r *http.Request) {
	id, ok := h.RequireProject(w, r)
	if !ok {
		return
	}
	parts, total := h.projectStorage(id)
	h.keepSize(id, total) // the disk is measured now: the value kept with the project follows
	api.WriteJSON(w, http.StatusOK, map[string]any{"parts": parts, "total_bytes": total})
}

// POST /api/projects/{id}/storage/clean {"parts": ["animations", ...]}
func (h *Handler) handleProjectStorageClean(w http.ResponseWriter, r *http.Request) {
	id, ok := h.RequireProject(w, r)
	if !ok {
		return
	}
	var req struct {
		Parts []string `json:"parts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Parts) == 0 {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, `expected {"parts": ["cache", ...]}`)
		return
	}
	want := map[string]bool{}
	for _, p := range req.Parts {
		if !cleanable[p] {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("the part %q cannot be cleaned: use one of cache, leftovers, clips, animations, reviews, backups", p))
			return
		}
		want[p] = true
	}
	if runID, busy := h.ProjectBusy(id); busy {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("project %q has an active or queued run (%s)", id, runID))
		return
	}
	var freed int64
	cleaned := map[string]int64{}
	for _, f := range h.projectFiles(id) {
		if !want[f.part] {
			continue
		}
		if os.Remove(f.path) == nil {
			freed += f.size
			cleaned[f.part] += f.size
		}
	}
	if want[partBackups] { // the backup files the import took as versions go too
		left, _ := filepath.Glob(media.Real(h.ProjectConfigPath(id)) + ".*")
		for _, f := range left {
			if fi, err := os.Lstat(f); err == nil && fi.Mode().IsRegular() && os.Remove(f) == nil {
				freed += fi.Size()
				cleaned[partBackups] += fi.Size()
			}
		}
	}
	if want[partBackups] { // the versions of the definition but the newest
		if bytes, err := h.projects.PruneVersions(id, 1); err == nil && bytes > 0 {
			freed += bytes
			cleaned[partBackups] += bytes
		}
	}
	h.removeEmptyDirs(id, want)
	h.keepSizeOf(id)
	h.StorageChanged(id)
	api.WriteJSON(w, http.StatusOK, map[string]any{"freed_bytes": freed, "cleaned": cleaned})
}

// removeEmptyDirs drops the folders a clean left empty (out/clips, .reviews/<id>), but never the project's own folders.
func (h *Handler) removeEmptyDirs(id string, parts map[string]bool) {
	dir := h.ProjectDir(id)
	var dirs []string
	for _, root := range []string{".reviews", filepath.Join("out", "clips")} {
		_ = filepath.WalkDir(filepath.Join(dir, root), func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() && d.Type()&fs.ModeSymlink == 0 {
				dirs = append(dirs, p)
			}
			return nil
		})
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, d := range dirs {
		_ = os.Remove(d) // fails when it is not empty
	}
}

// defaultMinFreeGB is the free space below which the disk is reported as low.
const defaultMinFreeGB = 20.0

// minFreeBytes is the low-space threshold: VERITMO_MIN_FREE_GB (0 disables the warning) or 20 GB.
func minFreeBytes() int64 {
	gb := defaultMinFreeGB
	if v := legacyname.Getenv("MIN_FREE_GB"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			gb = f
		}
	}
	return int64(gb * 1e9)
}

// diskReport is the free and total space of the disk that holds the projects.
type diskReport struct {
	FreeBytes      int64 `json:"free_bytes"`
	TotalBytes     int64 `json:"total_bytes"`
	WarnBelowBytes int64 `json:"warn_below_bytes"`
	// Low is true when less than WarnBelowBytes is free (never when the warning is disabled).
	Low bool `json:"low"`
}

// disk reads the space of the disk that holds dir; ok is false when it cannot be read.
func disk(dir string) (diskReport, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return diskReport{}, false
	}
	r := diskReport{
		FreeBytes:      int64(st.Bavail) * int64(st.Bsize),
		TotalBytes:     int64(st.Blocks) * int64(st.Bsize),
		WarnBelowBytes: minFreeBytes(),
	}
	r.Low = r.WarnBelowBytes > 0 && r.FreeBytes < r.WarnBelowBytes
	return r, true
}

// resultsDisk is the directory whose disk receives what the projects write: the media directory when there is one.
func (h *Handler) resultsDisk() string {
	if h.cfg.MediaDir != "" {
		return h.cfg.MediaDir
	}
	return h.cfg.ProjectsDir
}

// lowDiskWarning is the message shown when the disk is low, or "".
func lowDiskWarning(dir string) string {
	d, ok := disk(dir)
	if !ok || !d.Low {
		return ""
	}
	return fmt.Sprintf("Only %.1f GB are free on the disk of the projects (the warning starts below %.0f GB): free some space (the storage of each project, in Projects) before a long run.", float64(d.FreeBytes)/1e9, float64(d.WarnBelowBytes)/1e9)
}

// GET /api/storage: the size of every project and the free space of the disk that holds them.
func (h *Handler) handleStorage(w http.ResponseWriter, r *http.Request) {
	filter, ok := listFilterOf(w, r)
	if !ok {
		return
	}
	type row struct {
		ID         string      `json:"id"`
		TotalBytes int64       `json:"total_bytes"`
		Client     string      `json:"client"`
		Kind       ManagedKind `json:"kind,omitempty"`
	}
	rows := []row{}
	pf := ProjectFilter{IncludeManaged: filter.Include, IncludeAnimations: filter.All}
	pf.HideAdult, pf.OnlyReviewed = h.host.ContentFilter()
	for c := range filter.Clients {
		pf.Clients = append(pf.Clients, c)
	}
	sort.Strings(pf.Clients)
	// the size is the one kept with each project (measured when a run ends, a storage is cleaned or the sources change): no folder is walked
	list, _, err := h.projects.List(pf, store.ListParams{})
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	for _, p := range list {
		var total int64
		if p.SizeBytes != nil {
			total = *p.SizeBytes
		}
		rows = append(rows, row{ID: p.ID, TotalBytes: total, Client: p.Client, Kind: managedKindOfID(p.ID)})
	}
	body := map[string]any{"projects": rows}
	if d, ok := disk(h.cfg.ProjectsDir); ok {
		body["disk"] = d
	} else {
		body["disk"] = map[string]any{}
	}
	if h.cfg.MediaDir != "" {
		// Read on every request: a client that writes into the media directory sees what is left right now.
		m := map[string]any{"dir": h.cfg.MediaDir, "projects": h.mediaProjects(filter)}
		if d, ok := disk(h.cfg.MediaDir); ok {
			m["disk"] = d
		} else {
			m["disk"] = map[string]any{}
		}
		body["media"] = m
	}
	if m := h.modelsReport(); m != nil {
		body["models"] = m
	}
	api.WriteJSON(w, http.StatusOK, body)
}

// ModelsIssue is why nothing may be installed: the models directory is configured and not available (its disk is
// not mounted). nil when there is no models directory or it is fine. A download is never written to the local disk
// in its place.
func (h *Handler) ModelsIssue() error {
	if h.cfg.ModelsDir == "" {
		return nil
	}
	if err := media.Available(h.cfg.ModelsDir); err != nil {
		return fmt.Errorf("the models directory is not available, so nothing is installed: %w", err)
	}
	return nil
}

// modelsReport is the models directory with the space of its disk, read now; nil without a models directory.
func (h *Handler) modelsReport() map[string]any {
	if h.cfg.ModelsDir == "" {
		return nil
	}
	st := media.CheckModels(".", h.cfg.ModelsDir)
	m := map[string]any{"dir": h.cfg.ModelsDir, "available": st.Available, "linked": st.Linked}
	if st.Reason != "" {
		m["reason"] = st.Reason
	}
	if st.Available {
		if d, ok := disk(h.cfg.ModelsDir); ok {
			m["disk"] = d
		}
	}
	return m
}

// mediaProjects lists the folder of every visible project inside the media directory.
func (h *Handler) mediaProjects(filter listFilter) []map[string]string {
	rows := []map[string]string{}
	for _, id := range media.Projects(h.cfg.ProjectsDir) {
		client, _ := h.projectClient(id)
		if !filter.shows(id, client) || !h.host.ProjectVisible(id) {
			continue
		}
		rows = append(rows, map[string]string{"id": id, "folder": media.Folder(h.cfg.MediaDir, id)})
	}
	return rows
}

// OnStorageChanged registers a function the handler calls with the id of a project whose files changed in a way the server did (its storage was
// cleaned): a family that keeps the size of what it makes measures it again there.
func (h *Handler) OnStorageChanged(f func(projectID string)) {
	h.storageMu.Lock()
	defer h.storageMu.Unlock()
	h.storageHooks = append(h.storageHooks, f)
}

// StorageChanged tells the registered functions that the files of a project changed.
func (h *Handler) StorageChanged(projectID string) {
	h.storageMu.Lock()
	hooks := append([]func(string){}, h.storageHooks...)
	h.storageMu.Unlock()
	for _, f := range hooks {
		f(projectID)
	}
}

// versionDigests are the hashes of the texts the versions of a project hold: a scenes.yaml.* file with one of them was imported as a version.
func (h *Handler) versionDigests(id string) map[string]bool {
	out := map[string]bool{}
	var vs []ProjectVersion
	if h.projects.db.DB.Where("project_id = ?", id).Find(&vs).Error == nil {
		for _, v := range vs {
			out[sha([]byte(v.Definition))] = true
		}
	}
	return out
}

func fileDigest(path string) string {
	d, _ := fileSHA(path)
	return d
}

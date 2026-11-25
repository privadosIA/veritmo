package workspace

// clips.go: the video clips a scene can use as its own (the `clip` field) and
// the clips every run keeps in out/clips/. A clip taken from another project is
// copied, so a scene never depends on another project's files; and no clip is
// left loose: a video a saved configuration stops using is removed from
// sources/ (see pruneUnusedClips).

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"veritmo/internal/project/config"
	"veritmo/internal/project/pipeline"
	"veritmo/internal/server/api"
	"veritmo/internal/video/motion"
)

// clipRefs maps each video of sources/ to the scenes of cfg that use it as their clip.
func clipRefs(cfg *config.Config) map[string][]string {
	refs := map[string][]string{}
	if cfg == nil {
		return refs
	}
	for _, sc := range cfg.Scenes {
		if sc.Clip != "" {
			refs[sc.Clip] = append(refs[sc.Clip], sc.ID)
		}
	}
	return refs
}

// clipUsers is clipRefs for the saved configuration of a project (none when it cannot be read).
func (h *Handler) clipUsers(id string) map[string][]string {
	raw, err := h.ProjectDefinition(id)
	if err != nil {
		return map[string][]string{}
	}
	cfg, err := config.LoadBytes(raw)
	if err != nil {
		return map[string][]string{}
	}
	return clipRefs(cfg)
}

// pruneUnusedClips removes from sources/ the videos the previous configuration
// used as a scene's clip and the new one no longer does (a scene was removed or
// given another clip), unless another scene still uses them. A video that was
// never a scene's clip is never removed here. It returns the names removed.
func (h *Handler) pruneUnusedClips(id string, oldRaw []byte, next *config.Config) []string {
	old, err := config.LoadBytes(oldRaw)
	if err != nil {
		return nil
	}
	still := clipRefs(next)
	var gone []string
	for name := range clipRefs(old) {
		if len(still[name]) > 0 || !config.ValidClipName(name) {
			continue
		}
		path := filepath.Join(h.SourcesDir(id), name)
		if fi, err := os.Lstat(path); err == nil && fi.Mode().IsRegular() && os.Remove(path) == nil {
			gone = append(gone, name)
		}
	}
	sort.Strings(gone)
	return gone
}

// animSuffix ends the name of the complete animation a project keeps for a generated scene.
const animSuffix = ".anim.mp4"

// keptClip is the clip a project keeps for a scene.
type keptClip struct {
	Project string `json:"project"`
	Scene   string `json:"scene"`
	// Kind is "clip" (the finished clip of the scene) or "animation" (the complete
	// animation of a generated scene, forward and back).
	Kind     string `json:"kind"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
	// Motion says how a motion-graphic scene's page was made (the model that wrote it, the
	// browser that drew it); absent for any other scene.
	Motion string `json:"motion,omitempty"`
	// Client is who made the project the clip belongs to; ProjectKind is set only for a managed project.
	Client      string      `json:"client"`
	ProjectKind ManagedKind `json:"project_kind,omitempty"`
}

// GET /api/clips?exclude=<project>: the clips kept by every project (out/clips/), to pick one as a scene's own.
func (h *Handler) handleClipsList(w http.ResponseWriter, r *http.Request) {
	filter, ok := listFilterOf(w, r)
	if !ok {
		return
	}
	exclude, only := r.URL.Query().Get("exclude"), r.URL.Query().Get("project")
	out := []keptClip{}
	for _, project := range h.projectIDs() {
		if project == exclude || (only != "" && project != only) {
			continue
		}
		client, pkind := h.projectClient(project)
		if !filter.shows(project, client) {
			continue
		}
		if !h.host.ProjectVisible(project) {
			continue // the content mode hides it
		}
		dir := filepath.Join(h.cfg.ProjectsDir, project, pipeline.KeptClipsDir)
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".mp4") || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			kind, scene := "clip", strings.TrimSuffix(e.Name(), ".mp4")
			if strings.HasSuffix(e.Name(), animSuffix) {
				kind, scene = "animation", strings.TrimSuffix(e.Name(), animSuffix)
			}
			if fi, err := e.Info(); err == nil {
				kc := keptClip{Project: project, Scene: scene, Kind: kind, Size: fi.Size(), Modified: fi.ModTime().UTC().Format(time.RFC3339), Client: client, ProjectKind: pkind}
				if kind == "clip" {
					kc.Motion = motionSummary(filepath.Join(h.cfg.ProjectsDir, project), scene)
				}
				out = append(out, kc)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Project != out[j].Project {
			return out[i].Project < out[j].Project
		}
		if out[i].Scene != out[j].Scene {
			return out[i].Scene < out[j].Scene
		}
		return out[i].Kind < out[j].Kind
	})
	api.WriteJSON(w, http.StatusOK, map[string]any{"clips": out})
}

// motionSummary is the one-line description of a motion-graphic scene's page ("" when the
// scene is not one).
func motionSummary(projectDir, scene string) string {
	meta, ok := motion.LoadMeta(projectDir, scene, false)
	if !ok || meta.Kind != "motion_graphic" {
		return ""
	}
	html, _ := os.ReadFile(motion.PagePath(projectDir, scene, false))
	return meta.Describe(string(html))
}

// POST /api/projects/{id}/sources/clip {"from_project","scene","name"?}: copies the
// clip another project kept for a scene into this project's sources/. The copy
// is the scene's own: it does not depend on the other project afterwards.
func (h *Handler) handleSourceClipCopy(w http.ResponseWriter, r *http.Request) {
	id, ok := h.RequireProject(w, r)
	if !ok {
		return
	}
	var req struct {
		FromProject string `json:"from_project"`
		Scene       string `json:"scene"`
		Kind        string `json:"kind"`
		Name        string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if !api.ProjectIDPattern.MatchString(req.FromProject) || req.Scene == "" || strings.ContainsAny(req.Scene, `/\`) || strings.HasPrefix(req.Scene, ".") {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "from_project and scene must name a project and one of its scenes")
		return
	}
	if _, err := os.Stat(h.ProjectDir(req.FromProject)); err != nil || !h.host.ProjectVisible(req.FromProject) {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("project %q does not exist", req.FromProject))
		return
	}
	file, suffix := req.Scene+".mp4", ""
	switch req.Kind {
	case "", "clip":
	case "animation":
		file, suffix = req.Scene+animSuffix, "-anim"
	default:
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, `kind must be "clip" or "animation"`)
		return
	}
	src := filepath.Join(h.ProjectDir(req.FromProject), pipeline.KeptClipsDir, file)
	f, err := os.Open(src)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("project %q has no kept %s for scene %q", req.FromProject, kindWord(req.Kind), req.Scene))
		return
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, "the clip is not a file")
		return
	}
	name := req.Name
	if name == "" {
		name = h.freeClipName(id, req.FromProject+"-"+req.Scene+suffix+".mp4")
	}
	if !config.ValidClipName(name) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("%q is not a valid clip name: use a plain file name ending in .mp4, .mov, .webm or .mkv", name))
		return
	}
	if !h.writeSource(w, id, name, io.Reader(f)) {
		return
	}
	api.WriteJSON(w, http.StatusCreated, map[string]string{"name": name})
}

// freeClipName returns name, or name with a number added, so that it does not exist in sources/.
func (h *Handler) freeClipName(id, name string) string {
	stem, ext := strings.TrimSuffix(name, filepath.Ext(name)), filepath.Ext(name)
	candidate := name
	for i := 2; ; i++ {
		if _, err := os.Lstat(filepath.Join(h.SourcesDir(id), candidate)); err != nil {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d%s", stem, i, ext)
	}
}

func kindWord(kind string) string {
	if kind == "animation" {
		return "animation"
	}
	return "clip"
}

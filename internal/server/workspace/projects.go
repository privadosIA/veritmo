package workspace

// projects.go implements the project-management capability: discovery and
// CRUD of scenes.yaml projects under the projects root, validated
// read/write of the YAML through the pipeline's own loader, and the
// per-scene cache status report (see specs/project-management).

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"veritmo/internal/platform/media"
	"veritmo/internal/server/api"

	"veritmo/internal/music/music"
	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/store"
	"veritmo/internal/project/config"
	"veritmo/internal/project/pipeline"
)

// ProjectSummary is a project as the listing shows it: its id, mode, size and labels.
type ProjectSummary struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Mode       config.OutputMode `json:"mode"`
	SceneCount int               `json:"scene_count"`
	// TrackCount is the number of songs of a music project.
	TrackCount int `json:"track_count,omitempty"`
	// Adult is the project's label: true (for adults), false (looked at, fine for everyone), absent (not looked at).
	Adult   *bool  `json:"adult,omitempty"`
	Invalid bool   `json:"invalid,omitempty"`
	Error   string `json:"error,omitempty"`
	// Client is who made the project (its scenes.yaml, else embedded or unknown); Kind is set only for a managed project.
	Client string      `json:"client"`
	Kind   ManagedKind `json:"kind,omitempty"`
}

// ProjectDir is the folder of a project.
func (h *Handler) ProjectDir(id string) string {
	return filepath.Join(h.cfg.ProjectsDir, id)
}

// ProjectConfigPath is the scenes.yaml of a project.
func (h *Handler) ProjectConfigPath(id string) string {
	return filepath.Join(h.ProjectDir(id), "scenes.yaml")
}

// RequireProject resolves and checks the {id} path value, writing the
// error response itself when the id is malformed or unknown.
func (h *Handler) RequireProject(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !api.ProjectIDPattern.MatchString(id) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid project id %q", id))
		return "", false
	}
	if !h.ProjectExists(id) {
		// Its scenes.yaml leads into the media directory: when that disk is gone the project is not "not found".
		if h.cfg.MediaDir != "" && api.DirExists(h.ProjectDir(id)) {
			if err := media.Available(h.cfg.MediaDir); err != nil {
				api.WriteError(w, http.StatusServiceUnavailable, api.CodeInternal, fmt.Sprintf("the media directory is not available, so project %q cannot be read: %v", id, err))
				return "", false
			}
		}
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("project %q not found", id))
		return "", false
	}
	return id, true
}

// GET /api/projects[?managed=include&client=a,b&q=&sort=&limit=&offset=]
//
// One query on the table of projects: no directory is read. With q, sort, limit or offset it is a searched or paged list (X-Total-Count and Link
// say where the others are); without them it answers every project, in the order of the id, as it always did.
func (h *Handler) handleProjectsList(w http.ResponseWriter, r *http.Request) {
	filter, ok := listFilterOf(w, r)
	if !ok {
		return
	}
	params, err := store.ParseList(r, ProjectsListSpec)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	pf := ProjectFilter{IncludeManaged: filter.Include, IncludeAnimations: filter.All}
	pf.HideAdult, pf.OnlyReviewed = h.host.ContentFilter()
	for c := range filter.Clients {
		pf.Clients = append(pf.Clients, c)
	}
	sort.Strings(pf.Clients)
	rows, total, err := h.projects.List(pf, params)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, fmt.Sprintf("cannot read the projects: %v", err))
		return
	}
	out := make([]ProjectSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, summaryOf(row))
	}
	store.WriteListHeaders(w, r, total, params)
	api.WriteJSON(w, http.StatusOK, out)
}

// summaryOf is a project as the listing shows it.
func summaryOf(row ProjectRow) ProjectSummary {
	sum := ProjectSummary{ID: row.ID, Client: row.Client, Kind: managedKindOfID(row.ID)}
	if !row.Valid {
		sum.Invalid, sum.Error = true, string(row.Error)
		return sum
	}
	sum.Name, sum.Mode = row.Name, config.OutputMode(row.Mode)
	sum.SceneCount, sum.TrackCount, sum.Adult = row.SceneCount, row.TrackCount, row.Adult
	return sum
}

// POST /api/projects  {"id": "..."}
func (h *Handler) handleProjectCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
		// Client is the dashboard that makes the project (config.ValidClient); stored in its scenes.yaml, nothing when absent.
		Client *string `json:"client"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if req.Client != nil && !config.ValidClient(*req.Client) {
		api.Refuse(w, "client %q is not a client name: use %s, one to 32 lowercase letters, digits or hyphens", *req.Client, config.ClientPatternText)
		return
	}
	if !api.ProjectIDPattern.MatchString(req.ID) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid project id %q: use letters, digits, '._-', starting with a letter or digit", req.ID))
		return
	}
	if IsManagedProject(req.ID) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("project id %q is reserved for the generation API", req.ID))
		return
	}
	if h.ProjectExists(req.ID) || api.DirExists(h.ProjectDir(req.ID)) {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("project %q already exists", req.ID))
		return
	}
	starter := starterYAML(req.ID, h.FirstAsset("models"), h.FirstAsset("voices"))
	if req.Client != nil && *req.Client != "" {
		starter = append([]byte("client: "+*req.Client+"\n"), starter...)
	}
	if err := h.materializeProject(req.ID, starter); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	api.WriteJSON(w, http.StatusCreated, map[string]string{"id": req.ID})
}

// POST /api/projects/{id}/duplicate  {"new_id": "..."}
func (h *Handler) handleProjectDuplicate(w http.ResponseWriter, r *http.Request) {
	id, ok := h.RequireProject(w, r)
	if !ok {
		return
	}
	var req struct {
		NewID string `json:"new_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if !api.ProjectIDPattern.MatchString(req.NewID) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid project id %q", req.NewID))
		return
	}
	if h.ProjectExists(req.NewID) || api.DirExists(h.ProjectDir(req.NewID)) {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("project %q already exists", req.NewID))
		return
	}
	// Copy the definition, the pages and the sources — tmp/ and out/ stay behind by design.
	if err := h.projects.Duplicate(id, req.NewID); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	if err := h.prepareFolder(req.NewID); err != nil {
		_ = h.removeProjectFiles(req.NewID)
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	if err := h.copySources(id, req.NewID); err != nil {
		_ = h.removeProjectFiles(req.NewID)
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, fmt.Sprintf("cannot copy the sources: %v", err))
		return
	}
	api.WriteJSON(w, http.StatusCreated, map[string]string{"id": req.NewID})
}

// DELETE /api/projects/{id}
func (h *Handler) handleProjectDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := h.RequireProject(w, r)
	if !ok {
		return
	}
	if endpoint := managedDeleteEndpoint(id); endpoint != "" {
		owner := "the generation API"
		if IsAnimationProject(id) {
			owner = "the music mode (it is an animation)"
		}
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("project %q belongs to %s: delete it with %s", id, owner, endpoint))
		return
	}
	if runID, busy := h.ProjectBusy(id); busy {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("project %q has an active or queued run (%s); cancel it first", id, runID))
		return
	}
	if err := h.removeProjectFiles(id); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	// The project's run history (runs, events, resource samples) goes with it:
	// left behind it would keep counting in the Statistics and the Runs list for
	// a project that no longer exists. No run of the project is active (409 above).
	f := runDeleteFilter{Project: id, States: terminalStates}
	if _, _, _, err := h.rm.deleteRuns(nil, &f, false); err != nil {
		slog.Warn("cannot delete the run history of a project", "project", id, "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/projects/{id}/config
func (h *Handler) handleProjectConfigGet(w http.ResponseWriter, r *http.Request) {
	id, ok := h.RequireProject(w, r)
	if !ok {
		return
	}
	raw, err := h.ProjectDefinition(id)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	resp := map[string]any{"yaml": string(raw)}
	cfg, err := config.LoadBytes(raw)
	if err != nil {
		resp["valid"] = false
		resp["error"] = err.Error()
		api.WriteJSON(w, http.StatusOK, resp)
		return
	}
	if err := h.validateInDir(cfg, h.ProjectDir(id)); err != nil {
		resp["valid"] = false
		resp["error"] = err.Error()
	} else {
		resp["valid"] = true
	}
	resp["parsed"] = cfg
	resp["warnings"] = cfg.Warnings
	api.WriteJSON(w, http.StatusOK, resp)
}

// PUT /api/projects/{id}/config  {"yaml": "..."}
func (h *Handler) handleProjectConfigPut(w http.ResponseWriter, r *http.Request) {
	id, ok := h.RequireProject(w, r)
	if !ok {
		return
	}
	var req struct {
		YAML string `json:"yaml"`
	}
	if !api.DecodeJSONLimited(w, r, &req, api.MaxConfigBody) {
		return
	}
	cfg, err := config.LoadBytes([]byte(req.YAML))
	if err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
		return
	}
	if h.host.RefuseAdultConfig(w, cfg.Adult) {
		return
	}
	if err := h.validateInDir(cfg, h.ProjectDir(id)); err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
		return
	}
	oldRaw, _ := h.ProjectDefinition(id)
	if err := h.SaveProjectDefinition(id, []byte(req.YAML), "save"); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	resp := map[string]any{"saved": true, "warnings": cfg.Warnings}
	// A clip no scene uses any more does not stay behind in sources/ (not while a run reads it).
	if _, busy := h.ProjectBusy(id); !busy && len(oldRaw) > 0 {
		if gone := h.pruneUnusedClips(id, oldRaw, cfg); len(gone) > 0 {
			resp["removed_clips"] = gone
		}
	}
	api.WriteJSON(w, http.StatusOK, resp)
}

// GET /api/projects/{id}/cache
func (h *Handler) handleProjectCache(w http.ResponseWriter, r *http.Request) {
	id, ok := h.RequireProject(w, r)
	if !ok {
		return
	}
	dir := h.ProjectDir(id)
	if err := h.MaterializeProject(id); err != nil { // the report reads the folder
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	raw, err := h.ProjectDefinition(id)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	cfg, err := config.LoadBytes(raw)
	if err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
		return
	}
	if err := h.validateInDir(cfg, dir); err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
		return
	}
	report, err := buildCacheReport(cfg, dir)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, report)
}

// Cache phase states in the report.
const (
	PhaseHit    = "hit"    // cached, a run would skip it
	PhaseRun    = "run"    // a run would (re-)execute it
	PhaseAlways = "always" // never cached (probe, export manifest)
	PhaseNA     = "n/a"    // does not apply to this scene/mode
)

type sceneCacheReport struct {
	SceneID string            `json:"scene_id"`
	Phases  map[string]string `json:"phases"`
}

// CacheReport is the answer of GET /api/projects/{id}/cache: for each scene, which phases the cache already has.
type CacheReport struct {
	Mode   config.OutputMode  `json:"mode"`
	Cold   bool               `json:"cold"`
	Scenes []sceneCacheReport `json:"scenes"`
}

// buildCacheReport computes, with the pipeline's own hash and decision
// logic, what the next run would re-execute per scene and phase. It never
// mutates the manifest.
func buildCacheReport(cfg *config.Config, dir string) (*CacheReport, error) {
	manifestPath := filepath.Join(dir, cache.ManifestPath())
	cold := false
	if _, err := os.Stat(manifestPath); err != nil {
		cold = true
	}
	manifest, err := cache.LoadManifest(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("cannot load cache manifest: %w", err)
	}

	rep := &CacheReport{Mode: cfg.Output.Mode, Cold: cold}
	state := func(hit bool) string {
		if hit {
			return PhaseHit
		}
		return PhaseRun
	}

	if cfg.Output.Mode == config.ModeMusic {
		for _, track := range cfg.Tracks {
			rep.Scenes = append(rep.Scenes, musicTrackCacheReport(cfg, track, manifest, dir, state))
		}
		return rep, nil
	}

	for _, scene := range cfg.Scenes {
		sr := sceneCacheReport{SceneID: scene.ID, Phases: map[string]string{}}
		switch cfg.Output.Mode {
		case config.ModeImages:
			n := scene.EffectiveVariants()
			allCached := true
			for i := 0; i < n; i++ {
				// Seeded scenes derive variant seeds exactly like the
				// pipeline (seed + i). Unseeded scenes hash the sentinel,
				// which never matches the random-seed hash a run records,
				// so they correctly report as re-run.
				seed := scene.ResolvedSeed()
				if seed != -1 {
					seed += int64(i)
				}
				ip, ierr := pipeline.SceneImageParamsAt(cfg, scene, seed, i, dir)
				h := "unreadable-image"
				if ierr == nil {
					h = cache.ImageHash(ip)
				}
				if !cache.DecideImages(manifest, cache.VariantKey(scene.ID, n, i), h, false) {
					allCached = false
				}
			}
			sr.Phases["render"] = state(allCached)
			sr.Phases["export"] = PhaseAlways
		default: // video
			audioHash := cache.AudioHash(cache.AudioParams{
				Text:  scene.TTSText,
				Speed: scene.Speed,
				Voice: cfg.Defaults.Voice,
			})
			var imageHash string
			var ierr error
			if scene.IsMotionGraphic() {
				imageHash = pipeline.MotionImageHash(cfg, scene, dir, audioHash)
			} else if scene.IsBlender() {
				imageHash = pipeline.BlenderImageHash(cfg, scene, dir, audioHash)
			} else if imageHash, ierr = pipeline.VideoImageHash(cfg, scene, dir); ierr != nil {
				imageHash = "unreadable-image" // never equals a stored hash: the scene would fail, so it is not a hit
			}
			clipHash := pipeline.SceneClipHash(cfg, scene, dir)
			d := cache.Decide(manifest, scene.ID, audioHash, imageHash, clipHash, false)
			if scene.IsBlender() && pipeline.BlendMissing(dir, scene) {
				d.Phases[cache.PhaseRender], d.Phases[cache.PhaseClip] = false, false // a built scene is rendered again without its .blend
			}
			if scene.Animate && cfg.Defaults.Animate != nil {
				animateHash := cache.AnimateHash(pipeline.SceneAnimateParams(cfg, scene, dir))
				d = cache.DecideAnimated(manifest, cache.OnlyFilter{}, scene.ID, audioHash, imageHash, clipHash, animateHash, false)
				sr.Phases["animate"] = state(d.Phases[cache.PhaseAnimate])
			}
			if scene.IsSilent() {
				sr.Phases["tts"] = PhaseNA
				sr.Phases["probe"] = PhaseNA
			} else {
				sr.Phases["tts"] = state(d.Phases[cache.PhaseTTS])
				sr.Phases["probe"] = PhaseAlways
			}
			sr.Phases["render"] = state(d.Phases[cache.PhaseRender])
			sr.Phases["clip"] = state(d.Phases[cache.PhaseClip])
		}
		rep.Scenes = append(rep.Scenes, sr)
	}
	return rep, nil
}

// musicTrackCacheReport says what the next run would re-execute for a track,
// with the pipeline's own hashes: compose (n/a when the track brings its own
// lyrics), synth and export. A track without a seed hashes the sentinel, which
// never matches the random seed a run records, so it correctly reports as
// re-run. The synth state needs the composed request, so it can only be a hit
// when compose is.
func musicTrackCacheReport(cfg *config.Config, track config.Track, manifest *cache.Manifest, dir string, state func(bool) string) sceneCacheReport {
	sr := sceneCacheReport{SceneID: track.ID, Phases: map[string]string{}}
	ws := music.Workspace{Base: dir}
	m := cfg.Defaults.Music
	format := m.ResolvedOutputFormat()
	vae := music.FindSupport(ws, m.ResolvedModelsDir(), config.MusicVAEGlob)
	useLM := track.EffectiveUseLM()
	n := track.EffectiveVariants()
	composeHit, synthHit := true, true
	for i := 0; i < n; i++ {
		seed := track.ResolvedSeed()
		if seed != -1 {
			seed += int64(i)
		}
		req := music.BuildRequest(cfg, track, seed)
		key := cache.VariantKey(track.ID, n, i)
		composePath, _ := cache.MusicPaths(key, format)
		var enriched []byte
		if useLM {
			h := cache.ComposeHash(pipeline.ComposeParamsOf(req, track))
			if !cache.DecideCompose(manifest, dir, key, h, false) {
				composeHit, synthHit = false, false
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, composePath))
			if err != nil {
				composeHit, synthHit = false, false
				continue
			}
			enriched = b
		}
		sreq, err := music.SynthRequest(enriched, req)
		if err != nil {
			synthHit = false
			continue
		}
		sp, err := pipeline.SynthParamsOf(ws, m.AdaptersDir(), track, req, sreq, m.ResolvedSynth(), vae)
		if err != nil {
			synthHit = false
			continue
		}
		sh := cache.SynthHash(sp)
		if !cache.DecideSynth(manifest, dir, key, sh, false) {
			synthHit = false
		}
	}
	if useLM {
		sr.Phases["compose"] = state(composeHit)
	} else {
		sr.Phases["compose"] = PhaseNA
	}
	sr.Phases["synth"] = state(synthHit)
	sr.Phases["export"] = PhaseAlways
	return sr
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// materializeProject creates the project directory, writes its scenes.yaml
// and links the shared asset directories into it.
func (h *Handler) materializeProject(id string, scenesYAML []byte) error {
	if err := h.projects.Create(id, scenesYAML); err != nil {
		if errors.Is(err, ErrProjectExists) {
			return fmt.Errorf("project %q already exists", id)
		}
		return fmt.Errorf("cannot keep the project: %w", err)
	}
	if err := h.prepareFolder(id); err != nil {
		_ = h.projects.Delete(id)
		return err
	}
	h.keepSizeOf(id)
	return nil
}

// prepareFolder makes the folder of a project that is in the database: its definition and pages written, the shared assets linked, the media
// directory adopting it.
func (h *Handler) prepareFolder(id string) error {
	dir := h.ProjectDir(id)
	if err := h.projects.Materialize(id, dir); err != nil {
		return fmt.Errorf("cannot create project dir: %w", err)
	}
	h.linkSharedAssets(dir)
	return h.AdoptProject(id)
}

// AdoptProject keeps a project that was just written in the media directory (a no-op without one): the files it
// has now move there and the project folder keeps links (spec media-directory).
func (h *Handler) AdoptProject(id string) error {
	if h.cfg.MediaDir == "" {
		return nil
	}
	if _, err := media.Adopt(h.ProjectDir(id), h.cfg.MediaDir, id, false); err != nil {
		return fmt.Errorf("cannot keep the project in the media directory: %w", err)
	}
	return nil
}

// removeProjectFiles deletes a project: its rows, its folder and, with a media directory, the folder that holds its files.
func (h *Handler) removeProjectFiles(id string) error {
	if derr := h.projects.Delete(id); derr != nil && !errors.Is(derr, ErrNoProject) {
		return derr
	}
	err := os.RemoveAll(h.ProjectDir(id))
	if h.cfg.MediaDir != "" {
		if e := media.RemoveProject(h.cfg.MediaDir, id); err == nil {
			err = e
		}
	}
	return err
}

// linkSharedAssets symlinks the repo-level models/ and voices/ directories
// into the project directory, so the project-relative paths scenes.yaml
// uses (models/<file>, voices/<file>) resolve both when the pipeline runs
// with the project as working directory and when the server validates
// in-place. Best-effort: a missing source or an unsupported filesystem
// only means the links are absent.
func (h *Handler) linkSharedAssets(projDir string) {
	for _, name := range []string{"models", "voices"} {
		src, err := filepath.Abs(name)
		if err != nil {
			continue
		}
		if !api.DirExists(src) {
			continue
		}
		dst := filepath.Join(projDir, name)
		if _, err := os.Lstat(dst); err == nil {
			continue // already present (link or real dir)
		}
		target := src
		if absProj, err := filepath.Abs(projDir); err == nil {
			if rel, err := filepath.Rel(absProj, src); err == nil {
				target = rel
			}
		}
		_ = os.Symlink(target, dst)
	}
}

// FirstAsset returns the project-relative path of the first regular file
// in the given shared asset directory ("" when none).
func (h *Handler) FirstAsset(dirName string) string {
	entries, err := os.ReadDir(dirName)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.Type().IsRegular() && !strings.HasPrefix(e.Name(), ".") {
			return filepath.Join(dirName, e.Name())
		}
	}
	return ""
}

// starterYAML renders a minimal scenes.yaml that passes the pipeline's
// validation: one narrated example scene, video mode, and the first
// available shared model/voice (empty values are legal and flagged by the
// environment view instead).
func starterYAML(id, model, voice string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "project: %s\n\n", id)
	b.WriteString("output:\n  mode: video\n")
	fmt.Fprintf(&b, "  file: out/%s.mp4\n\n", id)
	b.WriteString("defaults:\n")
	fmt.Fprintf(&b, "  model: %q\n", model)
	fmt.Fprintf(&b, "  voice: %q\n", voice)
	b.WriteString("  resolution: 1280x720\n  sampler: euler\n  steps: 30\n  fps: 30\n\n")
	b.WriteString("scenes:\n")
	b.WriteString("  - id: escena_1\n")
	b.WriteString("    prompt: describe the first scene here\n")
	b.WriteString("    tts_text: narration text\n")
	b.WriteString("    speed: 1.0\n")
	return []byte(b.String())
}

// ProjectBusy reports whether a project has an active or queued run. The
// run queue (run-control) installs the real check; without it nothing is
// ever busy.
func (h *Handler) ProjectBusy(id string) (string, bool) {
	if h.busy == nil {
		return "", false
	}
	return h.busy(id)
}

// managedPath is the path of the route of the kind a managed project belongs to (the one that deletes it and the one that changes its label), "" for
// any other project.
func managedPath(id string) string {
	switch managedKindOfID(id) {
	case KindGeneration:
		return "/api/generations/{id}"
	case KindImageEdit:
		return "/api/image-edits/{id}"
	case KindVideoEdit:
		return "/api/video-edits/{id}"
	case KindModelGeneration:
		return "/api/models3d/generations/{id}"
	case KindMusicMotion:
		return "/api/music-motion/pieces/{id}"
	}
	return ""
}

// managedDeleteEndpoint is the endpoint that deletes a managed project, "" for any other project.
func managedDeleteEndpoint(id string) string {
	if path := managedPath(id); path != "" {
		return "DELETE " + path
	}
	return ""
}

// managedPatchEndpoint is the endpoint that changes the label of a managed project, "" for any other project.
func managedPatchEndpoint(id string) string {
	if path := managedPath(id); path != "" {
		return "PATCH " + path
	}
	return ""
}

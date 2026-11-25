package workspace

// reviews.go implements the project-review capability: an analysis of the
// animated clips of a project (frame measurements and, with a vision model,
// a judgement of the people), stored as a review with findings. Each finding
// has a proposed fix and is run as a task through the run queue; the user can
// add findings of their own, and delete reviews like runs (see
// specs/project-review).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"veritmo/internal/platform/media"

	"gorm.io/gorm"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/assistant/llm"
	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/settings"
	"veritmo/internal/project/config"
	"veritmo/internal/project/quality"
	"veritmo/internal/server/api"
)

// Review states.
const (
	reviewAnalyzing = "analyzing"
	reviewReady     = "ready"
	reviewFailed    = "failed"
)

// Finding statuses and sources.
const (
	findingPending = "pending"
	findingRunning = "running"
	findingDone    = "done"
	findingFailed  = "failed"
	findingSkipped = "skipped"

	sourceAuto   = "auto"
	sourceManual = "manual"
)

// reviewsDir is where a project keeps the evidence frames of its reviews.
const reviewsDir = ".reviews"

// Review is one analysis of a project.
type Review struct {
	ID         string     `gorm:"primaryKey;size:64" json:"id"`
	Project    string     `gorm:"size:191;index" json:"project"`
	State      string     `gorm:"size:32;index" json:"state"`
	Server     string     `gorm:"size:100;index" json:"-"`
	Model      string     `gorm:"size:191" json:"model,omitempty"`
	Total      int        `json:"total"`
	Done       int        `json:"done"`
	Notes      string     `json:"notes,omitempty"`
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// Finding is one thing noticed in a scene, with the fix proposed for it. It is
// also the task that applies the fix.
type Finding struct {
	ID          string    `gorm:"primaryKey;size:64" json:"id"`
	ReviewID    string    `gorm:"size:64;index" json:"review_id"`
	Scene       string    `json:"scene"`
	Kind        string    `json:"kind"`   // deformation, color_drift, blur or observation
	Source      string    `json:"source"` // auto or manual
	FromSeconds float64   `json:"from_seconds"`
	Severity    int       `json:"severity"`
	Description string    `json:"description"`
	HasFrame    bool      `json:"has_frame"`
	FixKind     string    `json:"-"`
	FixParams   string    `json:"-"`
	Status      string    `json:"status"`
	RunID       string    `json:"run_id,omitempty"`
	Message     string    `json:"message,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	Seq         int       `json:"-"`
}

// fixJSON is the fix of a finding as the API shows and accepts it.
type fixJSON struct {
	Kind   string         `json:"kind"`
	Params map[string]any `json:"params,omitempty"`
}

// FindingJSON is a finding with its fix decoded.
type FindingJSON struct {
	Finding
	Fix fixJSON `json:"fix"`
}

func viewOf(f Finding) FindingJSON {
	v := FindingJSON{Finding: f, Fix: fixJSON{Kind: f.FixKind}}
	if f.FixParams != "" {
		_ = json.Unmarshal([]byte(f.FixParams), &v.Fix.Params)
	}
	return v
}

// The kinds of fix. A new one is a case in validateFix and toQualityFix.
const (
	fixTrim   = "trim"          // cut a silent scene to seconds
	fixReseed = "reseed"        // make the scene again with another seed
	fixMotion = "motion_prompt" // rewrite the motion prompt
	fixPrompt = "prompt"        // rewrite the scene's prompt
)

func paramNumber(p map[string]any, key string) (float64, bool) {
	v, ok := p[key]
	if !ok {
		return 0, false
	}
	f, ok := v.(float64)
	return f, ok
}

func paramText(p map[string]any, key string) (string, bool) {
	s, ok := p[key].(string)
	return strings.TrimSpace(s), ok && strings.TrimSpace(s) != ""
}

// validateFix checks a fix as the user or the analysis gave it.
func validateFix(f fixJSON) error {
	switch f.Kind {
	case fixTrim:
		if n, ok := paramNumber(f.Params, "seconds"); !ok || n <= 0 {
			return fmt.Errorf("fix %q needs seconds > 0", f.Kind)
		}
	case fixReseed:
		if n, ok := paramNumber(f.Params, "seed"); !ok || n < 0 || n != float64(int(n)) {
			return fmt.Errorf("fix %q needs a seed that is an integer >= 0", f.Kind)
		}
	case fixMotion:
		if _, ok := paramText(f.Params, "motion_prompt"); !ok {
			return fmt.Errorf("fix %q needs a motion_prompt", f.Kind)
		}
	case fixPrompt:
		if _, ok := paramText(f.Params, "prompt"); !ok {
			return fmt.Errorf("fix %q needs a prompt", f.Kind)
		}
	default:
		return fmt.Errorf("unknown fix %q: use %s, %s, %s or %s", f.Kind, fixTrim, fixReseed, fixMotion, fixPrompt)
	}
	return nil
}

// toQualityFix is the edit of scenes.yaml a fix stands for, for a scene.
func toQualityFix(scene config.Scene, f fixJSON) (quality.Fix, error) {
	out := quality.Fix{ID: scene.ID}
	switch f.Kind {
	case fixTrim:
		if !scene.IsSilent() {
			return out, fmt.Errorf("scene %q has narration, so its clip cannot be cut: make it again or rewrite its prompt instead", scene.ID)
		}
		n, _ := paramNumber(f.Params, "seconds")
		n = float64(int(n*10+0.5)) / 10
		out.SilenceSeconds = &n
	case fixReseed:
		n, _ := paramNumber(f.Params, "seed")
		seed := int(n)
		out.Seed = &seed
	case fixMotion:
		t, _ := paramText(f.Params, "motion_prompt")
		out.MotionPrompt = &t
	case fixPrompt:
		t, _ := paramText(f.Params, "prompt")
		out.Prompt = &t
	default:
		return out, fmt.Errorf("unknown fix %q", f.Kind)
	}
	return out, nil
}

// --- store ------------------------------------------------------------------

func (h *historyStore) createReview(r *Review) error {
	if r.Server == "" {
		r.Server = h.server
	}
	return h.db.Create(r).Error
}
func (h *historyStore) saveReview(r *Review) error { return h.db.Save(r).Error }

func (h *historyStore) getReview(id string) (*Review, error) {
	var r Review
	if err := h.db.First(&r, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &r, nil
}

func (h *historyStore) listReviews(project string) ([]Review, error) {
	var out []Review
	q := h.db.Order("created_at DESC, id DESC")
	if project != "" {
		q = q.Where("project = ?", project)
	}
	return out, q.Find(&out).Error
}

func (h *historyStore) createFinding(f *Finding) error { return h.db.Create(f).Error }
func (h *historyStore) saveFinding(f *Finding) error   { return h.db.Save(f).Error }

func (h *historyStore) getFinding(reviewID, id string) (*Finding, error) {
	var f Finding
	if err := h.db.First(&f, "id = ? AND review_id = ?", id, reviewID).Error; err != nil {
		return nil, err
	}
	return &f, nil
}

func (h *historyStore) findingsOf(reviewID string) ([]Finding, error) {
	var out []Finding
	return out, h.db.Where("review_id = ?", reviewID).Order("seq ASC, created_at ASC, id ASC").Find(&out).Error
}

func (h *historyStore) deleteReviewRows(ids []string) (int, error) {
	n := 0
	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("review_id IN ?", ids).Delete(&Finding{}).Error; err != nil {
			return err
		}
		res := tx.Where("id IN ?", ids).Delete(&Review{})
		n = int(res.RowsAffected)
		return res.Error
	})
	return n, err
}

// --- the manager --------------------------------------------------------------

// reviewManager keeps what is going on: the analyses in progress.
type reviewManager struct {
	mu        sync.Mutex
	analyzing map[string]string // review id -> project
	counter   atomic.Int64
	// idSuffix ends the ids of the reviews (see newInstanceSuffix).
	idSuffix string
	ctx      context.Context
	wg       sync.WaitGroup // the analyses in flight: shutdown waits for them before the database closes
}

func newReviewManager(ctx context.Context) *reviewManager {
	return &reviewManager{analyzing: map[string]string{}, ctx: ctx}
}

func (m *reviewManager) newID(prefix string) string {
	return fmt.Sprintf("%s-%s-%04d%s", prefix, time.Now().UTC().Format("20060102-150405.000"), m.counter.Add(1), m.idSuffix)
}

func (m *reviewManager) projectAnalyzing(project string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, p := range m.analyzing {
		if p == project {
			return id, true
		}
	}
	return "", false
}

// --- the analysis ---------------------------------------------------------------

type analyzeRequest struct {
	// Analyze false creates an empty review for the user's own findings.
	Analyze *bool `json:"analyze"`
	// Model is the Ollama vision model; empty measures only.
	Model string `json:"model"`
}

// animatedClip is a scene of the project whose animation exists.
type animatedClip struct {
	Scene config.Scene
	Path  string
}

func (h *Handler) animatedClips(projectID string) ([]animatedClip, *config.Config, error) {
	cfg, err := h.ProjectConfig(projectID)
	if err != nil {
		return nil, nil, err
	}
	var out []animatedClip
	for _, sc := range cfg.Scenes {
		if !sc.Animate || cfg.Defaults.Animate == nil {
			continue
		}
		raw, _ := cache.AnimatePaths(sc.ID)
		p := filepath.Join(h.ProjectDir(projectID), raw)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			out = append(out, animatedClip{Scene: sc, Path: p})
		}
	}
	return out, cfg, nil
}

// POST /api/projects/{id}/reviews {"model"?: "...", "analyze"?: false}
func (h *Handler) handleReviewStart(w http.ResponseWriter, r *http.Request) {
	id, ok := h.RequireProject(w, r)
	if !ok {
		return
	}
	var req analyzeRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	hist := h.rm.hist
	rev := &Review{ID: h.rv.newID("rev"), Project: id, CreatedAt: time.Now().UTC(), Model: strings.TrimSpace(req.Model)}
	if req.Analyze != nil && !*req.Analyze { // an empty review for the user's own findings
		rev.State = reviewReady
		now := time.Now().UTC()
		rev.FinishedAt = &now
		if err := hist.createReview(rev); err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
			return
		}
		api.WriteJSON(w, http.StatusCreated, rev)
		return
	}
	if other, busy := h.rv.projectAnalyzing(id); busy {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("project %q is already being analyzed (%s)", id, other))
		return
	}
	if runID, busy := h.ProjectBusy(id); busy {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("project %q has an active or queued run (%s): analyze when it finishes", id, runID))
		return
	}
	// The vision judgement runs on the provider routed to the quality check. A provider that is not the
	// built-in Ollama brings its own model; without consent for a remote one the review does not start.
	if res, err := llm.For(settings.TaskQuality, ""); err != nil {
		if api.WriteRoutingError(w, err) {
			return
		}
	} else if !res.Builtin && res.Model != "" {
		rev.Model = res.Model
	}
	clips, _, err := h.animatedClips(id)
	if err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
		return
	}
	if len(clips) == 0 {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, "nothing to analyze: the project has no animated clips yet (run it first, keeping tmp/)")
		return
	}
	rev.State, rev.Total = reviewAnalyzing, len(clips)
	if rev.Model == "" {
		rev.Notes = "no vision model: only colour and sharpness were checked"
	}
	if err := hist.createReview(rev); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	h.rv.mu.Lock()
	h.rv.analyzing[rev.ID] = id
	h.rv.mu.Unlock()
	h.rv.wg.Add(1)
	go h.analyze(rev, clips)
	api.WriteJSON(w, http.StatusAccepted, rev)
}

// analyze runs the check on every clip and records the findings.
func (h *Handler) analyze(rev *Review, clips []animatedClip) {
	hist := h.rm.hist
	defer h.rv.wg.Done()
	defer func() {
		h.rv.mu.Lock()
		delete(h.rv.analyzing, rev.ID)
		h.rv.mu.Unlock()
	}()
	ctx := h.rv.ctx
	var vision quality.Vision
	var routeErr error
	if rev.Model != "" {
		newVision := h.Overrides.ReviewVision
		if newVision == nil {
			newVision = func(m string) quality.Vision {
				res, err := llm.For(settings.TaskQuality, "")
				if err != nil {
					routeErr = err // consent withdrawn or provider gone since the review started: stop, never fall back
					return nil
				}
				if res.Builtin {
					return quality.NewOllama(settings.OllamaURL(), m)
				}
				return quality.FromResolved(res, m)
			}
		}
		vision = newVision(rev.Model)
		if u, ok := vision.(interface{ Unload(context.Context) }); ok {
			defer u.Unload(context.WithoutCancel(ctx)) // the model is big: the next run needs the memory
		}
	}
	fail := func(err error) {
		now := time.Now().UTC()
		rev.State, rev.Error, rev.FinishedAt = reviewFailed, err.Error(), &now
		if serr := saveReviewTwice(hist, rev); serr != nil {
			slog.Warn("a review failed and its state could not be saved", "review", rev.ID, "failure", err, "err", serr)
		}
	}
	if routeErr != nil {
		fail(routeErr)
		return
	}

	seq := 0
	for _, c := range clips {
		o := quality.Options{}
		if c.Scene.IsSilent() {
			o.MaxSeconds = c.Scene.SilenceDuration()
		}
		rep, err := quality.CheckClip(ctx, h.host.Runner(), vision, c.Scene.ID, c.Path, o)
		if err != nil {
			fail(err)
			return
		}
		if fix := quality.Propose(rep, c.Scene.IsSilent(), c.Scene.SilenceDuration(), c.Scene.Seed); fix != nil {
			seq++
			if err := h.recordFinding(rev, c.Scene, rep, fix, seq); err != nil {
				fail(err)
				return
			}
		}
		rev.Done++
		if err := saveReviewTwice(hist, rev); err != nil {
			fail(fmt.Errorf("cannot save the progress of the review: %w", err))
			return
		}
	}
	now := time.Now().UTC()
	rev.State, rev.FinishedAt = reviewReady, &now
	if err := saveReviewTwice(hist, rev); err != nil {
		fail(fmt.Errorf("cannot save the result of the review: %w", err))
	}
}

// saveFindingOrWarn saves a finding whose change cannot fail the request (a status that is recomputed on the next
// read); a failure is reported on stderr instead of vanishing.
func (h *Handler) saveFindingOrWarn(f *Finding) {
	if err := h.rm.hist.saveFinding(f); err != nil {
		slog.Warn("cannot save a finding", "finding", f.ID, "err", err)
	}
}

// saveReviewTwice saves the review and, when that fails, once more a moment later (a busy database): the state of an
// analysis is what the client waits for, and a lost write leaves it "analyzing" for ever.
func saveReviewTwice(hist *historyStore, rev *Review) error {
	err := hist.saveReview(rev)
	if err != nil {
		time.Sleep(200 * time.Millisecond)
		err = hist.saveReview(rev)
	}
	return err
}

// recordFinding stores what a report found, with its evidence frame.
func (h *Handler) recordFinding(rev *Review, scene config.Scene, rep *quality.SceneReport, fix *quality.Fix, seq int) error {
	f := &Finding{
		ID: h.rv.newID("fnd"), ReviewID: rev.ID, Scene: scene.ID, Source: sourceAuto, Status: findingPending,
		FromSeconds: rep.BadAt, CreatedAt: time.Now().UTC(), Seq: seq,
		Description: strings.Join(rep.Issues, "; "),
	}
	switch rep.Kind {
	case "color":
		f.Kind, f.Severity = "color_drift", 6
	case "blur":
		f.Kind, f.Severity = "blur", 5
	default:
		f.Kind, f.Severity = "deformation", 7
		for _, fr := range rep.Frames {
			if fr.T == rep.BadAt && fr.Vision != nil {
				f.Severity = fr.Vision.Severity
			}
		}
	}
	var fj fixJSON
	switch {
	case fix.SilenceSeconds != nil:
		fj = fixJSON{Kind: fixTrim, Params: map[string]any{"seconds": *fix.SilenceSeconds}}
	default:
		fj = fixJSON{Kind: fixReseed, Params: map[string]any{"seed": *fix.Seed}}
	}
	params, _ := json.Marshal(fj.Params)
	f.FixKind, f.FixParams = fj.Kind, string(params)
	if len(rep.Evidence) > 0 {
		dir := filepath.Join(h.ProjectDir(rev.Project), reviewsDir, rev.ID)
		if err := os.MkdirAll(dir, 0o755); err == nil && os.WriteFile(filepath.Join(dir, f.ID+".png"), rep.Evidence, 0o644) == nil {
			f.HasFrame = true
		}
	}
	return h.rm.hist.createFinding(f)
}

// --- reading --------------------------------------------------------------------

// syncFindings brings the status of running tasks up to date with their runs.
func (h *Handler) syncFindings(fs []Finding) {
	for i := range fs {
		f := &fs[i]
		if f.Status != findingRunning || f.RunID == "" {
			continue
		}
		run, err := h.rm.hist.getRun(f.RunID)
		if err != nil {
			f.Status, f.Message = findingFailed, "its run is no longer in the history"
		} else {
			switch run.State {
			case StateSucceeded:
				f.Status = findingDone
			case StateFailed, StateCancelled, StateInterrupted:
				f.Status, f.Message = findingFailed, fmt.Sprintf("the run %s: %s", run.State, run.Error)
			default:
				continue
			}
		}
		h.saveFindingOrWarn(f)
	}
}

type reviewDetail struct {
	Review   *Review       `json:"review"`
	Findings []FindingJSON `json:"findings"`
}

func (h *Handler) reviewDetailOf(rev *Review) (*reviewDetail, error) {
	fs, err := h.rm.hist.findingsOf(rev.ID)
	if err != nil {
		return nil, err
	}
	h.syncFindings(fs)
	out := &reviewDetail{Review: rev, Findings: make([]FindingJSON, len(fs))}
	for i, f := range fs {
		out.Findings[i] = viewOf(f)
	}
	return out, nil
}

// loadReview finds a review by the {rid} of the path.
func (h *Handler) loadReview(w http.ResponseWriter, r *http.Request) (*Review, bool) {
	rev, err := h.rm.hist.getReview(r.PathValue("rid"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("review %q not found", r.PathValue("rid")))
		return nil, false
	}
	return rev, true
}

// GET /api/projects/{id}/reviews
func (h *Handler) handleReviewList(w http.ResponseWriter, r *http.Request) {
	id, ok := h.RequireProject(w, r)
	if !ok {
		return
	}
	revs, err := h.rm.hist.listReviews(id)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	type summary struct {
		Review
		Findings int `json:"findings"`
		Pending  int `json:"pending"`
	}
	out := make([]summary, 0, len(revs))
	for _, rv := range revs {
		fs, _ := h.rm.hist.findingsOf(rv.ID)
		h.syncFindings(fs)
		sm := summary{Review: rv, Findings: len(fs)}
		for _, f := range fs {
			if f.Status == findingPending {
				sm.Pending++
			}
		}
		out = append(out, sm)
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// GET /api/reviews/{rid}
func (h *Handler) handleReviewGet(w http.ResponseWriter, r *http.Request) {
	rev, ok := h.loadReview(w, r)
	if !ok {
		return
	}
	d, err := h.reviewDetailOf(rev)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, d)
}

// GET /api/reviews/{rid}/findings/{fid}/frame
func (h *Handler) handleFindingFrame(w http.ResponseWriter, r *http.Request) {
	rev, ok := h.loadReview(w, r)
	if !ok {
		return
	}
	f, err := h.rm.hist.getFinding(rev.ID, r.PathValue("fid"))
	if err != nil || !f.HasFrame {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, "that finding has no frame")
		return
	}
	data, err := os.ReadFile(filepath.Join(h.ProjectDir(rev.Project), reviewsDir, rev.ID, f.ID+".png"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, "the frame is gone")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(data)
}

// GET /api/review/vision-models -> {"models": ["..."]}
func (h *Handler) handleVisionModels(w http.ResponseWriter, r *http.Request) {
	list := h.Overrides.VisionModels
	if list == nil {
		list = func(ctx context.Context) ([]string, error) {
			return quality.VisionModels(ctx, settings.OllamaURL())
		}
	}
	names, err := list(r.Context())
	if err != nil {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, err.Error())
		return
	}
	if names == nil {
		names = []string{}
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"models": names})
}

// --- findings: the user's own, and edits ---------------------------------------

type findingRequest struct {
	Scene       *string  `json:"scene"`
	FromSeconds *float64 `json:"from_seconds"`
	Severity    *int     `json:"severity"`
	Description *string  `json:"description"`
	Fix         *fixJSON `json:"fix"`
	Skipped     *bool    `json:"skipped"`
}

func (h *Handler) decodeFinding(w http.ResponseWriter, r *http.Request) (findingRequest, bool) {
	var req findingRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 256<<10)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return req, false
	}
	return req, true
}

// POST /api/reviews/{rid}/findings
func (h *Handler) handleFindingCreate(w http.ResponseWriter, r *http.Request) {
	rev, ok := h.loadReview(w, r)
	if !ok {
		return
	}
	if rev.State == reviewAnalyzing {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, "the review is still being analyzed")
		return
	}
	req, ok := h.decodeFinding(w, r)
	if !ok {
		return
	}
	if req.Scene == nil || strings.TrimSpace(*req.Scene) == "" || req.Description == nil || strings.TrimSpace(*req.Description) == "" {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "a finding needs a scene and a description of what was seen")
		return
	}
	cfg, err := h.ProjectConfig(rev.Project)
	if err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
		return
	}
	var scene *config.Scene
	for i := range cfg.Scenes {
		if cfg.Scenes[i].ID == *req.Scene {
			scene = &cfg.Scenes[i]
		}
	}
	if scene == nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("scene %q is not in the project", *req.Scene))
		return
	}
	f := &Finding{
		ID: h.rv.newID("fnd"), ReviewID: rev.ID, Scene: scene.ID, Kind: "observation", Source: sourceManual,
		Description: strings.TrimSpace(*req.Description), Status: findingPending, CreatedAt: time.Now().UTC(), Severity: 5,
	}
	if req.FromSeconds != nil {
		if *req.FromSeconds < 0 {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "from_seconds must be >= 0")
			return
		}
		f.FromSeconds = *req.FromSeconds
	}
	if req.Severity != nil {
		if *req.Severity < 0 || *req.Severity > 10 {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "severity must be from 0 to 10")
			return
		}
		f.Severity = *req.Severity
	}
	fix := fixJSON{Kind: fixReseed, Params: map[string]any{"seed": float64(1000)}}
	if scene.Seed != nil {
		fix.Params["seed"] = float64(*scene.Seed + 1000)
	}
	if req.Fix != nil {
		fix = *req.Fix
	}
	if err := validateFix(fix); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	params, _ := json.Marshal(fix.Params)
	f.FixKind, f.FixParams = fix.Kind, string(params)
	existing, _ := h.rm.hist.findingsOf(rev.ID)
	f.Seq = len(existing) + 1
	if err := h.rm.hist.createFinding(f); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	api.WriteJSON(w, http.StatusCreated, viewOf(*f))
}

// PATCH /api/reviews/{rid}/findings/{fid}
func (h *Handler) handleFindingPatch(w http.ResponseWriter, r *http.Request) {
	rev, ok := h.loadReview(w, r)
	if !ok {
		return
	}
	f, err := h.rm.hist.getFinding(rev.ID, r.PathValue("fid"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, "finding not found")
		return
	}
	h.syncFindings([]Finding{*f})
	if f2, err := h.rm.hist.getFinding(rev.ID, f.ID); err == nil {
		f = f2
	}
	if f.Status == findingRunning {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, "the task is running: it cannot be changed now")
		return
	}
	req, ok := h.decodeFinding(w, r)
	if !ok {
		return
	}
	if req.Fix != nil {
		if err := validateFix(*req.Fix); err != nil {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
			return
		}
		params, _ := json.Marshal(req.Fix.Params)
		f.FixKind, f.FixParams = req.Fix.Kind, string(params)
		if f.Status == findingFailed || f.Status == findingDone {
			f.Status, f.Message, f.RunID = findingPending, "", ""
		}
	}
	if req.Description != nil {
		if strings.TrimSpace(*req.Description) == "" {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "the description cannot be empty")
			return
		}
		f.Description = strings.TrimSpace(*req.Description)
	}
	if req.Severity != nil {
		if *req.Severity < 0 || *req.Severity > 10 {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "severity must be from 0 to 10")
			return
		}
		f.Severity = *req.Severity
	}
	if req.FromSeconds != nil {
		if *req.FromSeconds < 0 {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "from_seconds must be >= 0")
			return
		}
		f.FromSeconds = *req.FromSeconds
	}
	if req.Skipped != nil {
		switch {
		case *req.Skipped && f.Status != findingDone:
			f.Status = findingSkipped
		case !*req.Skipped && f.Status == findingSkipped:
			f.Status = findingPending
		}
	}
	if err := h.rm.hist.saveFinding(f); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, viewOf(*f))
}

// POST /api/reviews/{rid}/findings/{fid}/suggest -> {"motion_prompt": "..."}
// asks the local text model for a gentler motion prompt for the scene, from what
// was noticed. It is a suggestion: nothing is changed.
func (h *Handler) handleFindingSuggest(w http.ResponseWriter, r *http.Request) {
	rev, ok := h.loadReview(w, r)
	if !ok {
		return
	}
	f, err := h.rm.hist.getFinding(rev.ID, r.PathValue("fid"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, "finding not found")
		return
	}
	if runID, busy := h.ProjectBusy(rev.Project); busy {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("project %q has an active or queued run (%s): the assistant needs the memory", rev.Project, runID))
		return
	}
	cfg, err := h.ProjectConfig(rev.Project)
	if err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
		return
	}
	motion := ""
	for _, sc := range cfg.Scenes {
		if sc.ID == f.Scene {
			motion = sc.MotionPrompt
			if motion == "" {
				motion = sc.Prompt
			}
		}
	}
	text := fmt.Sprintf("Current movement: %s. Problem seen in the video: %s. Describe a gentler, simpler movement for the same scene that avoids that problem.", motion, f.Description)
	run := h.host.AssistRun()
	out, err := run(r.Context(), assist.TaskMotion, text, "")
	if err != nil {
		api.WriteAssistError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]string{"motion_prompt": out})
}

// --- tasks -------------------------------------------------------------------

type tasksRequest struct {
	// FindingIDs names the findings to run; empty runs every pending one.
	FindingIDs []string `json:"finding_ids"`
	GPU        string   `json:"gpu"`
}

// POST /api/reviews/{rid}/tasks -> {"started": [{"finding_id","run_id"}], "skipped": [{"finding_id","reason"}]}
// Each task applies its fix to scenes.yaml and queues a run limited to its scene.
func (h *Handler) handleReviewTasks(w http.ResponseWriter, r *http.Request) {
	rev, ok := h.loadReview(w, r)
	if !ok {
		return
	}
	var req tasksRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 256<<10)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if rev.State == reviewAnalyzing {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, "the review is still being analyzed")
		return
	}
	if runID, busy := h.ProjectBusy(rev.Project); busy {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("project %q has an active or queued run (%s): run the tasks when it finishes", rev.Project, runID))
		return
	}
	fs, err := h.rm.hist.findingsOf(rev.ID)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	h.syncFindings(fs)
	want := map[string]bool{}
	for _, id := range req.FindingIDs {
		want[id] = true
	}
	type startedItem struct {
		FindingID string `json:"finding_id"`
		RunID     string `json:"run_id"`
	}
	type skippedItem struct {
		FindingID string `json:"finding_id"`
		Reason    string `json:"reason"`
	}
	started, skipped := []startedItem{}, []skippedItem{}
	for i := range fs {
		f := &fs[i]
		if len(want) > 0 && !want[f.ID] {
			continue
		}
		if f.Status != findingPending && f.Status != findingFailed {
			if want[f.ID] {
				skipped = append(skipped, skippedItem{f.ID, "it is " + f.Status})
			}
			continue
		}
		cfg, err := h.ProjectConfig(rev.Project)
		if err != nil {
			api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
			return
		}
		var scene *config.Scene
		for j := range cfg.Scenes {
			if cfg.Scenes[j].ID == f.Scene {
				scene = &cfg.Scenes[j]
			}
		}
		fail := func(msg string) {
			f.Status, f.Message = findingFailed, msg
			h.saveFindingOrWarn(f)
			skipped = append(skipped, skippedItem{f.ID, msg})
		}
		if scene == nil {
			fail(fmt.Sprintf("scene %q is no longer in the project", f.Scene))
			continue
		}
		fj := viewOf(*f).Fix
		fix, err := toQualityFix(*scene, fj)
		if err != nil {
			fail(err.Error())
			continue
		}
		if err := h.applyFix(rev.Project, fix); err != nil {
			fail("could not edit scenes.yaml: " + err.Error())
			continue
		}
		run, _, lerr := h.rm.launch(rev.Project, RunRequest{GPU: h.defaultGPU(req.GPU), KeepTmp: true, Only: f.Scene})
		if lerr != nil {
			fail("scenes.yaml was edited, but the run could not start: " + lerr.Msg)
			continue
		}
		f.Status, f.RunID, f.Message = findingRunning, run.ID, ""
		h.saveFindingOrWarn(f)
		started = append(started, startedItem{f.ID, run.ID})
	}
	sort.SliceStable(started, func(i, j int) bool { return started[i].RunID < started[j].RunID })
	api.WriteJSON(w, http.StatusAccepted, map[string]any{"started": started, "skipped": skipped})
}

// applyFix edits the definition of a project the way a review asks: the fix is made on the working copy of the folder (what ApplyFixes works on),
// the result is taken into the database as a new version, and the .bak file the function leaves is removed: the version is the backup.
func (h *Handler) applyFix(project string, fix quality.Fix) error {
	if err := h.MaterializeProject(project); err != nil {
		return err
	}
	path := h.ProjectConfigPath(project)
	if err := quality.ApplyFixes(path, []quality.Fix{fix}); err != nil {
		return err
	}
	_ = os.Remove(media.Real(path) + ".bak")
	return h.projects.Ingest(project, h.ProjectDir(project), "fix")
}

// --- deletion ----------------------------------------------------------------

// deleteReviews removes reviews and their evidence. Reviews that are being
// analyzed, or whose tasks are running, are skipped and reported.
func (h *Handler) deleteReviews(ids []string, project string, before *time.Time) (deleted int, skipped []string, err error) {
	hist := h.rm.hist
	skipped = []string{}
	var candidates []Review
	if ids != nil {
		for _, id := range ids {
			if rv, e := hist.getReview(id); e == nil && h.host.RunProjectVisible(rv.Project) {
				candidates = append(candidates, *rv)
			}
		}
	} else {
		all, e := hist.listReviews(project)
		if e != nil {
			return 0, nil, e
		}
		for _, rv := range all {
			if !h.host.RunProjectVisible(rv.Project) {
				continue // the content mode hides it
			}
			if before == nil || rv.CreatedAt.Before(*before) {
				candidates = append(candidates, rv)
			}
		}
	}
	var doomed []Review
	for _, rv := range candidates {
		h.rv.mu.Lock()
		_, live := h.rv.analyzing[rv.ID]
		h.rv.mu.Unlock()
		running := false
		if fs, _ := hist.findingsOf(rv.ID); len(fs) > 0 {
			h.syncFindings(fs)
			for _, f := range fs {
				if f.Status == findingRunning {
					running = true
				}
			}
		}
		if live || rv.State == reviewAnalyzing && live || running {
			skipped = append(skipped, rv.ID)
			continue
		}
		doomed = append(doomed, rv)
	}
	if len(doomed) == 0 {
		return 0, skipped, nil
	}
	rids := make([]string, len(doomed))
	for i, rv := range doomed {
		rids[i] = rv.ID
	}
	if deleted, err = hist.deleteReviewRows(rids); err != nil {
		return 0, nil, err
	}
	for _, rv := range doomed {
		_ = os.RemoveAll(filepath.Join(h.ProjectDir(rv.Project), reviewsDir, rv.ID))
	}
	return deleted, skipped, nil
}

// DELETE /api/reviews/{rid}
func (h *Handler) handleReviewDelete(w http.ResponseWriter, r *http.Request) {
	rev, ok := h.loadReview(w, r)
	if !ok {
		return
	}
	deleted, skipped, err := h.deleteReviews([]string{rev.ID}, "", nil)
	switch {
	case err != nil:
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, "could not delete the review")
	case deleted == 0 && len(skipped) > 0:
		api.WriteError(w, http.StatusConflict, api.CodeConflict, "the review is being analyzed or has running tasks: wait for them or cancel the runs")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// POST /api/reviews/delete {"ids": [...]} or {"project"?, "before"?}
func (h *Handler) handleReviewsDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs     *[]string `json:"ids"`
		Project *string   `json:"project"`
		Before  *string   `json:"before"`
	}
	api.RaiseBodyLimit(w, r, 4<<20)
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&body); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "invalid body: expected {\"ids\": [...]} or a filter {\"project\", \"before\"}")
		return
	}
	hasFilter := body.Project != nil || body.Before != nil
	switch {
	case body.IDs != nil && hasFilter:
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "send either ids or a filter (project, before), not both")
		return
	case body.IDs == nil && !hasFilter:
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "name the reviews to delete: ids, or a filter with project or before")
		return
	}
	var (
		deleted int
		skipped []string
		err     error
	)
	if body.IDs != nil {
		deleted, skipped, err = h.deleteReviews(*body.IDs, "", nil)
	} else {
		project := ""
		if body.Project != nil {
			project = *body.Project
		}
		var before *time.Time
		if body.Before != nil {
			t, perr := time.Parse(time.RFC3339, *body.Before)
			if perr != nil {
				api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid before %q: use an RFC 3339 timestamp", *body.Before))
				return
			}
			before = &t
		}
		deleted, skipped, err = h.deleteReviews(nil, project, before)
	}
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, "could not delete the reviews")
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"deleted": deleted, "skipped_active": skipped})
}

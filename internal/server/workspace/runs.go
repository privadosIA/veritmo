package workspace

// runs.go implements the live half of the run-control capability: the
// per-GPU lanes (one active run per lane, FIFO per lane, a multi-GPU run
// occupies all its lanes; design D5), in-process pipeline
// execution with context-based cancellation, and the marshal-once event
// log every subscriber and the history share (design D2).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"veritmo/internal/platform/media"
	"veritmo/internal/server/api"
	"veritmo/internal/server/modelcat"

	"veritmo/internal/platform/events"
	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/project/config"
	"veritmo/internal/project/pipeline"
	"veritmo/internal/video/videoedit"
)

// ---------------------------------------------------------------------------
// runManager — queue, workers, cancellation
// ---------------------------------------------------------------------------

type run struct {
	rec        Run
	cfg        *config.Config
	dir        string
	log        *api.EventLog
	cancel     context.CancelFunc
	userCancel bool
	lanes      []string // GPU lanes the run occupies while active
}

// lanesOf returns the lanes a GPU option occupies: each listed GPU index, or
// the single shared lane "" for a run without a GPU.
func lanesOf(gpu string) []string {
	gpus, err := pipeline.ParseGPUs(gpu)
	if err != nil || len(gpus) == 0 {
		return []string{""}
	}
	return gpus
}

type runManager struct {
	mu          sync.Mutex
	hist        *historyStore
	projectsDir string
	// projects are the projects of the database: a run gets its definition from there and gives back what it changed; measure is the size of the
	// folder of a project (the run ends, the size is kept with the project).
	projects *projectStore
	measure  func(project string) (int64, bool)
	// afterIngest is called with the project when a run of it ended and what it changed is in the database (the song library follows what the run exported).
	afterIngest func(project string)

	runs   map[string]*run // queued + running only
	queue  []*run          // queued runs in arrival order (one FIFO for all lanes)
	active map[string]*run // lane (GPU index, "" = no GPU) -> running run

	rootCtx      context.Context
	rootCancel   context.CancelFunc
	shuttingDown bool
	wg           sync.WaitGroup

	counter int
	// idSuffix ends the ids of the runs (see newInstanceSuffix).
	idSuffix string

	sampler *resourceSampler

	// videoEdit is the catalog of tools a video-edit run uses (the server's).
	videoEdit *videoedit.Catalog
	// motion and services are the music motion engine and the music services of the server (see Host).
	motion   pipeline.MotionEngine
	services *musicsvc.ServiceHost
	// models3d is the 3D service and the library a run of a models3d project makes its models with (set by the server).
	models3d pipeline.Models3DServices
	// browserCheck says whether a browser exists for the motion graphics of a run (nil: the check of the configuration, which looks on this machine).
	browserCheck func() error

	// test seams
	execute  func(ctx context.Context, rn *run, emitter *events.Emitter) (string, error)
	lookPath func(string) (string, error)
	// gpuName gives the name of the card a run used (its single GPU index, or "" for a run with none chosen); nil or
	// an empty answer means the run says nothing about the speed of a card.
	gpuName func(gpu string) string
	// catalog is the model catalog the calibration compares the times with.
	catalog func() []modelcat.Entry
	// preflight is the server's own check of a launch (a music generator whose container is stopped); nil checks nothing.
	preflight func(cfg *config.Config) *LaunchError
}

func newRunManager(hist *historyStore, projectsDir string, sampler SamplerConfig) *runManager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &runManager{
		hist:        hist,
		projectsDir: projectsDir,
		runs:        make(map[string]*run),
		active:      make(map[string]*run),
		rootCtx:     ctx,
		rootCancel:  cancel,
		lookPath:    exec.LookPath,
	}
	m.execute = m.pipelineExecute
	m.sampler = newResourceSampler(hist, sampler)
	go m.sampleLoop()
	return m
}

// motionParts is the builder of pages the runs get: nil (and not a nil engine in an interface) when there is none.
func (m *runManager) motionParts() pipeline.MotionParts {
	if m.motion == nil {
		return nil
	}
	return m.motion
}

func (m *runManager) pipelineExecute(ctx context.Context, rn *run, emitter *events.Emitter) (string, error) {
	return pipeline.Run(ctx, rn.cfg, pipeline.Options{
		Dir:           rn.dir,
		KeepTmp:       rn.rec.KeepTmp,
		GPU:           rn.rec.GPU,
		VRAMWarnMB:    rn.rec.VRAMWarnMB,
		Force:         rn.rec.Force,
		Only:          rn.rec.Only,
		Overlap:       rn.rec.Overlap,
		VideoEdit:     m.videoEdit,
		MotionParts:   m.motionParts(),
		MusicServices: m.services,
		Models3D:      &m.models3d,

		NoContainerStart: true,
	}, emitter)
}

// RunRequest is the POST body for launching a run; fields mirror the CLI
// flags (see specs/run-control).
type RunRequest struct {
	GPU        string            `json:"gpu"`
	KeepTmp    bool              `json:"keep_tmp"`
	Force      bool              `json:"force"`
	Only       string            `json:"only"`
	VRAMWarnMB int               `json:"vram_warn_mb"`
	Mode       config.OutputMode `json:"mode"`
	// Overlap: see pipeline.Options.Overlap (0 to 8).
	Overlap int `json:"overlap"`
}

// maxOverlap is the most scenes of look-ahead a run can ask for (each one holds its narration and files until the card gets to it).
const maxOverlap = 8

// LaunchError distinguishes validation failures (client's fault) from
// internal ones.
type LaunchError struct {
	Status int
	Code   string
	Msg    string
	// Reason is the availability reason behind the error, when there is one (not_running).
	Reason string
}

func (e *LaunchError) Error() string { return e.Msg }

// launch validates the project and request, records the run as queued and
// schedules its lane. It returns the run snapshot and its queue position
// (0 = running next / already dispatched).
func (m *runManager) launch(projectID string, req RunRequest) (Run, int, *LaunchError) {
	dir := filepath.Join(m.projectsDir, projectID)
	row, err := m.projects.Get(projectID)
	if err != nil {
		return Run{}, 0, &LaunchError{Status: http.StatusNotFound, Code: api.CodeNotFound, Msg: fmt.Sprintf("project %q not found", projectID)}
	}
	if err := m.projects.Materialize(projectID, dir); err != nil { // the run works on the folder: it gets the saved definition and pages
		return Run{}, 0, &LaunchError{Status: http.StatusInternalServerError, Code: api.CodeInternal, Msg: err.Error()}
	}
	cfg, err := config.LoadBytes([]byte(row.Definition))
	if err != nil {
		return Run{}, 0, &LaunchError{Status: http.StatusUnprocessableEntity, Code: api.CodeValidation, Msg: err.Error()}
	}
	if err := pipeline.ApplyModeOverride(cfg, req.Mode); err != nil {
		return Run{}, 0, &LaunchError{Status: http.StatusUnprocessableEntity, Code: api.CodeValidation, Msg: err.Error()}
	}
	if m.motion != nil {
		cfg.Parts = m.motion
	}
	if m.browserCheck != nil {
		cfg.Checks.Browser = m.browserCheck
	}
	if err := cfg.ValidateInDir(dir); err != nil {
		return Run{}, 0, &LaunchError{Status: http.StatusUnprocessableEntity, Code: api.CodeValidation, Msg: err.Error()}
	}
	if err := pipeline.CheckOnlyScene(cfg, req.Only); err != nil {
		return Run{}, 0, &LaunchError{Status: http.StatusUnprocessableEntity, Code: api.CodeValidation, Msg: err.Error()}
	}
	if err := pipeline.PreflightFor(cfg, req.Only, m.lookPath); err != nil {
		return Run{}, 0, &LaunchError{Status: http.StatusUnprocessableEntity, Code: api.CodeValidation, Msg: err.Error()}
	}
	if _, err := pipeline.ParseGPUs(req.GPU); err != nil {
		return Run{}, 0, &LaunchError{Status: http.StatusUnprocessableEntity, Code: api.CodeValidation, Msg: fmt.Sprintf("gpu: %v", err)}
	}
	if m.preflight != nil {
		if lerr := m.preflight(cfg); lerr != nil {
			return Run{}, 0, lerr
		}
	}
	if req.VRAMWarnMB == 0 {
		req.VRAMWarnMB = 2048
	}
	if req.Overlap < 0 || req.Overlap > maxOverlap {
		return Run{}, 0, &LaunchError{Status: http.StatusUnprocessableEntity, Code: api.CodeValidation, Msg: fmt.Sprintf("overlap %d is outside 0 to %d", req.Overlap, maxOverlap)}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.shuttingDown {
		return Run{}, 0, &LaunchError{Status: http.StatusServiceUnavailable, Code: api.CodeConflict, Msg: "server is shutting down"}
	}
	if lerr := m.queueFullLocked(projectID); lerr != nil {
		return Run{}, 0, lerr
	}
	m.counter++
	rec := Run{
		ID:         fmt.Sprintf("run-%s-%04d%s", time.Now().UTC().Format("20060102-150405.000"), m.counter, m.idSuffix),
		Project:    projectID,
		State:      StateQueued,
		GPU:        req.GPU,
		KeepTmp:    req.KeepTmp,
		Force:      req.Force,
		Only:       req.Only,
		VRAMWarnMB: req.VRAMWarnMB,
		Overlap:    req.Overlap,
		Mode:       req.Mode,
		Client:     cfg.Client,
		CreatedAt:  time.Now().UTC(),
	}
	if err := m.hist.createRun(&rec); err != nil {
		return Run{}, 0, &LaunchError{Status: http.StatusInternalServerError, Code: api.CodeInternal, Msg: err.Error()}
	}
	rn := &run{rec: rec, cfg: cfg, dir: dir, lanes: lanesOf(rec.GPU)}
	rn.log = api.NewEventLog(func(seq int, line []byte) {
		if err := m.hist.appendEvent(rn.rec.ID, seq, string(line)); err != nil {
			slog.Warn("cannot persist an event of a run", "run", rn.rec.ID, "seq", seq, "err", err)
		}
	})
	m.runs[rec.ID] = rn
	m.queue = append(m.queue, rn)
	m.scheduleLocked()
	return rn.rec, m.queuePositionLocked(rn), nil
}

// Limits of the run queue. A run waits for the GPU of its lanes; without a limit a client could queue thousands of them.
const (
	maxQueuedRuns           = 20 // runs waiting, all projects together
	maxQueuedRunsPerProject = 5  // runs waiting for the same project (a few GPUs, a few clicks)
)

// queueFullLocked answers 429 queue_full when the queue has no room for one more run of project; m.mu is held.
func (m *runManager) queueFullLocked(project string) *LaunchError {
	perProject := 0
	for _, q := range m.queue {
		if q.rec.Project == project {
			perProject++
		}
	}
	switch {
	case len(m.queue) >= maxQueuedRuns:
		return &LaunchError{Status: http.StatusTooManyRequests, Code: api.CodeQueueFull, Msg: fmt.Sprintf("the queue is full (%d runs are waiting): wait for one to start or cancel one", maxQueuedRuns)}
	case perProject >= maxQueuedRunsPerProject:
		return &LaunchError{Status: http.StatusTooManyRequests, Code: api.CodeQueueFull, Msg: fmt.Sprintf("project %q already has %d runs waiting: wait for one to start or cancel one", project, maxQueuedRunsPerProject)}
	}
	return nil
}

// sharesLane reports whether two lane sets overlap.
func sharesLane(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

// scheduleLocked starts every queued run whose lanes are all free and that
// no earlier queued run is still waiting on: FIFO per lane, so a run for
// GPUs 0,1 waiting on a busy GPU 1 is not overtaken by a later run for GPU 0.
// Caller holds m.mu.
func (m *runManager) scheduleLocked() {
	var waiting []*run
	for _, rn := range m.queue {
		startable := true
		for _, lane := range rn.lanes {
			if m.active[lane] != nil {
				startable = false
			}
		}
		for _, earlier := range waiting {
			if sharesLane(earlier.lanes, rn.lanes) {
				startable = false
			}
		}
		if !startable {
			waiting = append(waiting, rn)
			continue
		}
		for _, lane := range rn.lanes {
			m.active[lane] = rn
		}
		m.startLocked(rn)
	}
	m.queue = waiting
}

// startLocked marks rn running and launches its worker. Caller holds m.mu.
func (m *runManager) startLocked(rn *run) {
	now := time.Now().UTC()
	rn.rec.State = StateRunning
	rn.rec.StartedAt = &now
	if err := m.hist.saveRun(&rn.rec); err != nil {
		slog.Warn("cannot persist a run", "run", rn.rec.ID, "err", err)
	}
	ctx, cancel := context.WithCancel(m.rootCtx)
	rn.cancel = cancel

	// One reference reading when the run starts, before it loads any model.
	go m.sampler.sample(m.rootCtx, []sampleTarget{targetOf(rn.rec.ID, rn.lanes)})

	m.wg.Add(1)
	go m.work(ctx, rn)
}

// sampleLoop samples the resources of the active runs; with none active it
// reads nothing (no nvidia-smi call). It ends with the manager.
func (m *runManager) sampleLoop() {
	for {
		t := time.NewTimer(m.sampler.intervalNow())
		select {
		case <-m.rootCtx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		m.mu.Lock()
		var targets []sampleTarget
		seen := map[*run]bool{}
		for _, rn := range m.active {
			if rn != nil && !seen[rn] {
				seen[rn] = true
				targets = append(targets, targetOf(rn.rec.ID, rn.lanes))
			}
		}
		m.mu.Unlock()
		m.sampler.sample(m.rootCtx, targets)
	}
}

// work executes one run and finishes its lane. Panics inside the pipeline
// are recovered into a failed run so the server survives (design risk 1).
func (m *runManager) work(ctx context.Context, rn *run) {
	defer m.wg.Done()
	var artifact string
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("pipeline panicked: %v", r)
			}
		}()
		emitter := events.New(rn.log, true)
		// a run that waited in the queue starts from what is saved now
		if merr := m.projects.Materialize(rn.rec.Project, filepath.Join(m.projectsDir, rn.rec.Project)); merr != nil && !errors.Is(merr, ErrNoProject) {
			slog.Warn("cannot write the project to its folder before the run", "project", rn.rec.Project, "err", merr)
		}
		artifact, err = m.execute(ctx, rn, emitter)
	}()
	m.afterRun(rn.rec.Project)

	m.mu.Lock()
	now := time.Now().UTC()
	rn.rec.FinishedAt = &now
	switch {
	case err == nil:
		rn.rec.State = StateSucceeded
		rn.rec.Artifact = artifact
	case rn.userCancel:
		rn.rec.State = StateCancelled
	case m.shuttingDown || m.rootCtx.Err() != nil:
		rn.rec.State = StateInterrupted
		rn.rec.Error = "server shut down during the run"
	default:
		rn.rec.State = StateFailed
		rn.rec.Error = err.Error()
	}
	if err := m.hist.saveRun(&rn.rec); err != nil {
		slog.Warn("cannot persist a run", "run", rn.rec.ID, "err", err)
	}
	delete(m.runs, rn.rec.ID)
	for _, lane := range rn.lanes {
		m.active[lane] = nil
	}
	m.sampler.forget(rn.rec.ID)
	rn.log.Close()
	m.scheduleLocked()
	m.mu.Unlock()
	m.recordCalibration(rn)
}

// How many times afterRun tries to take a run's changes into the database, and the pause that grows with each attempt.
const (
	ingestAttempts = 5
	ingestBackoff  = 40 * time.Millisecond
)

// afterRun takes into the database what the run changed in the folder of its project (a fix applied to the definition, a page the model wrote)
// and measures the folder again; whatever the state the run ended in.
func (m *runManager) afterRun(project string) {
	if m.projects == nil || !m.projects.Exists(project) {
		return
	}
	dir := filepath.Join(m.projectsDir, project)
	// What a run changed is taken once, when it ends, and a failure here drops it: the database can be busy for a moment (the run's
	// own records are written at the same time; with the in-memory database of the tests a write waits for no lock, and
	// "database is locked" is the answer), so a few more attempts are made before giving up.
	var err error
	for attempt := 1; attempt <= ingestAttempts; attempt++ {
		if err = m.projects.Ingest(project, dir, "run"); err == nil {
			break
		}
		time.Sleep(time.Duration(attempt) * ingestBackoff)
	}
	if err != nil {
		slog.Warn("cannot take the changes of the run into the database", "project", project, "err", err)
	}
	if m.measure != nil {
		if total, ok := m.measure(project); ok {
			_ = m.projects.SetSize(project, total)
		}
	}
	if m.afterIngest != nil {
		m.afterIngest(project)
	}
}

// recordCalibration compares the phases the run executed with their expected time (see calibration.go). It is
// best effort: a failure only means the card is not calibrated by this run.
func (m *runManager) recordCalibration(rn *run) {
	if m.gpuName == nil || rn.cfg == nil || rn.rec.State == StateInterrupted {
		return
	}
	gpu := m.gpuName(rn.rec.GPU)
	if gpu == "" {
		return
	}
	lines, err := m.hist.listEvents(rn.rec.ID)
	if err != nil {
		return
	}
	if err := m.hist.addCalibration(CalibrationSamples(m.catalog(), rn.rec.ID, gpu, rn.cfg, lines, rn.rec.State == StateSucceeded)); err != nil {
		slog.Warn("cannot store the timings of a run", "run", rn.rec.ID, "err", err)
	}
}

// cancelRun cancels a queued run (dequeue) or a running one (context
// cancellation; the runner kills the subprocess). Terminal runs are a
// conflict.
func (m *runManager) cancelRun(id string) (Run, *LaunchError) {
	m.mu.Lock()
	rn, ok := m.runs[id]
	if !ok {
		m.mu.Unlock()
		// Known but finished? Answer conflict with its state.
		if rec, err := m.hist.getRun(id); err == nil {
			return Run{}, &LaunchError{Status: http.StatusConflict, Code: api.CodeConflict, Msg: fmt.Sprintf("run %s already finished (%s)", id, rec.State)}
		}
		return Run{}, &LaunchError{Status: http.StatusNotFound, Code: api.CodeNotFound, Msg: fmt.Sprintf("run %s not found", id)}
	}
	switch rn.rec.State {
	case StateQueued:
		for i, qr := range m.queue {
			if qr == rn {
				m.queue = append(m.queue[:i:i], m.queue[i+1:]...)
				break
			}
		}
		now := time.Now().UTC()
		rn.rec.State = StateCancelled
		rn.rec.FinishedAt = &now
		if err := m.hist.saveRun(&rn.rec); err != nil {
			slog.Warn("cannot persist a run", "run", rn.rec.ID, "err", err)
		}
		delete(m.runs, id)
		rn.log.Close()
		rec := rn.rec
		// Runs behind the cancelled one may now be able to start.
		m.scheduleLocked()
		m.mu.Unlock()
		return rec, nil
	case StateRunning:
		rn.userCancel = true
		cancel := rn.cancel
		rec := rn.rec
		m.mu.Unlock()
		cancel()
		return rec, nil
	default:
		rec := rn.rec
		m.mu.Unlock()
		return Run{}, &LaunchError{Status: http.StatusConflict, Code: api.CodeConflict, Msg: fmt.Sprintf("run %s is %s", id, rec.State)}
	}
}

// projectBusy reports the first queued or running run of a project.
func (m *runManager) projectBusy(projectID string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, rn := range m.runs {
		if rn.rec.Project == projectID {
			return id, true
		}
	}
	return "", false
}

// anyBusy reports a queued or running run of any project.
func (m *runManager) anyBusy() (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.runs {
		return id, true
	}
	return "", false
}

// liveRun returns the in-memory run (queued or running) for streaming.
func (m *runManager) liveRun(id string) (*run, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rn, ok := m.runs[id]
	return rn, ok
}

// queuePosition returns 1-based position for a queued run, 0 otherwise.
func (m *runManager) queuePosition(id string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	rn, ok := m.runs[id]
	if !ok {
		return 0
	}
	return m.queuePositionLocked(rn)
}

// queuePositionLocked is a queued run's 1-based rank among the queued runs
// that share one of its lanes (itself included); 0 when it is not queued.
// Caller holds m.mu.
func (m *runManager) queuePositionLocked(rn *run) int {
	if rn.rec.State != StateQueued {
		return 0
	}
	pos := 0
	for _, qr := range m.queue {
		if qr == rn {
			return pos + 1
		}
		if sharesLane(qr.lanes, rn.lanes) {
			pos++
		}
	}
	return 0
}

// shutdown cancels every active run (recorded as interrupted) and waits
// for the workers to persist their final state.
func (m *runManager) shutdown() {
	m.mu.Lock()
	m.shuttingDown = true
	m.mu.Unlock()
	m.rootCancel()
	m.wg.Wait()
}

// ---------------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------------

// defaultGPU is the GPU a run uses when none is chosen. With several cards, sd would use the first one, which
// is usually the one that holds the desktop and has the least memory (a 14 GB model fails to load in 12 GB);
// the card with the most FREE memory is chosen instead (a card that other services, such as HeartMuLa, are
// using counts for what is left). With one card, or when the cards cannot be read, nothing
// is pinned, as before.
func (h *Handler) defaultGPU(requested string) string {
	if requested != "" {
		return requested
	}
	gpus, err := h.QueryGPUs()
	if err != nil || len(gpus) < 2 {
		return ""
	}
	mem := func(g GPUStatus) int {
		if g.FreeVRAMMB > 0 {
			return g.FreeVRAMMB
		}
		return g.TotalVRAMMB
	}
	best := gpus[0]
	for _, g := range gpus[1:] {
		if mem(g) > mem(best) {
			best = g
		}
	}
	return strconv.Itoa(best.Index)
}

// musicMinVRAMMB is the memory a card needs to make songs on its own (the synthesis model with its
// decoder; the same figure the studio's Easy mode warns about).
const musicMinVRAMMB = 9000

// GPUForRun is the GPU choice of a run. When none was asked for, a music project with two or more songs to
// make uses every card that can make songs on its own, so two songs are made at the same time; anything else
// gets defaultGPU.
func (h *Handler) GPUForRun(projectID string, req RunRequest) string {
	if req.GPU != "" {
		return req.GPU
	}
	if cards := h.musicGPUs(projectID, req.Only); len(cards) >= 2 {
		return strings.Join(cards, ",")
	}
	return h.defaultGPU("")
}

// musicGPUs lists the indexes of the cards a music run can use in parallel; empty when the project is not a
// music one, fewer than two songs would be made, or the cards cannot be read.
func (h *Handler) musicGPUs(projectID, only string) []string {
	cfg, err := h.ProjectConfig(projectID)
	if err != nil || cfg.Output.Mode != config.ModeMusic {
		return nil
	}
	songs := len(cfg.Tracks)
	if only != "" {
		songs = 0
		for _, t := range cfg.Tracks {
			if t.ID == only {
				songs = 1
			}
		}
	}
	if songs < 2 {
		return nil
	}
	gpus, err := h.QueryGPUs()
	if err != nil {
		return nil
	}
	var out []string
	for _, g := range gpus {
		mem := g.TotalVRAMMB
		if mem == 0 {
			mem = g.FreeVRAMMB
		}
		if mem >= musicMinVRAMMB {
			out = append(out, strconv.Itoa(g.Index))
		}
	}
	return out
}

// POST /api/projects/{id}/runs
func (h *Handler) handleRunLaunch(w http.ResponseWriter, r *http.Request) {
	id, ok := h.RequireProject(w, r)
	if !ok {
		return
	}
	var req RunRequest
	// No body at all is a run with the defaults (the contract does not make the body required); a broken one is a 400.
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if h.cfg.MediaDir != "" {
		// The disk of the media directory may be gone (unmounted): refuse, never fall back to writing locally.
		if err := media.Available(h.cfg.MediaDir); err != nil {
			api.WriteError(w, http.StatusServiceUnavailable, api.CodeInternal, fmt.Sprintf("the media directory is not available, so nothing is written: %v", err))
			return
		}
	}
	req.GPU = h.GPUForRun(id, req)
	rec, pos, lerr := h.rm.launch(id, req)
	if lerr != nil {
		if lerr.Reason != "" {
			api.WriteJSON(w, lerr.Status, api.APIError{Error: api.APIErrorBody{Code: lerr.Code, Message: lerr.Msg, Reason: lerr.Reason}})
			return
		}
		api.WriteError(w, lerr.Status, lerr.Code, lerr.Msg)
		return
	}
	resp := map[string]any{"run": rec, "queue_position": pos}
	// A low disk is a warning, never a reason not to start.
	if msg := lowDiskWarning(h.resultsDisk()); msg != "" {
		resp["warnings"] = []string{msg}
	}
	api.WriteJSON(w, http.StatusAccepted, resp)
}

// GET /api/runs?project=&limit=
func (h *Handler) handleRunsList(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid limit %q", v))
			return
		}
		limit = n
	}
	modes, bad := parseRunModes(r.URL.Query().Get("mode"))
	if bad != "" {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid mode %q: use %s (comma separated)", bad, RunModesText))
		return
	}
	listLimit := limit
	if len(modes) > 0 {
		listLimit = 0 // the limit counts the runs of those modes
	}
	runs, err := h.rm.hist.listRuns(r.URL.Query().Get("project"), listLimit)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	if len(modes) > 0 {
		runs = h.rm.hist.onlyModes(runs, modes)
		if limit > 0 && len(runs) > limit {
			runs = runs[:limit]
		}
	}
	api.WriteJSON(w, http.StatusOK, h.viewsOf(runs))
}

// deleteRuns removes finished runs from the history. ids and f are exclusive:
// with ids, unknown ones are ignored; with a filter, only terminal runs are
// selected. A run that is queued or running, in the database or still held
// live, is never deleted: it is skipped and reported. The whole operation
// holds the manager lock so a run cannot change state in the middle.
//
// With files, the output folder of each deleted run (out/<name>, see
// outputPathOf) and the project's tmp/ (the cache and the scene pictures the
// editor shows as thumbnails, and the clips kept in out/clips/, which would otherwise outlive the files the
// user asked to delete) are removed too, after the history rows; projects with
// a queued or running run keep their files. filesRemoved counts the folders
// (or files) that existed and were removed.
func (m *runManager) deleteRuns(ids []string, f *runDeleteFilter, files bool) (deleted int, skipped []string, filesRemoved int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	skipped = []string{}

	var candidates []string
	if f != nil {
		if candidates, err = m.hist.terminalRunIDs(*f); err != nil {
			return 0, nil, 0, err
		}
	} else {
		states, err := m.hist.runStates(ids)
		if err != nil {
			return 0, nil, 0, err
		}
		for _, id := range ids {
			st, ok := states[id]
			if !ok {
				continue // unknown: ignored
			}
			if !isTerminalState(RunState(st)) {
				skipped = append(skipped, id)
				continue
			}
			candidates = append(candidates, id)
		}
	}
	var doomed []string
	for _, id := range candidates {
		if _, live := m.runs[id]; live {
			skipped = append(skipped, id)
			continue
		}
		doomed = append(doomed, id)
	}
	var outputs map[string]struct{}
	if files {
		outputs = m.outputsOf(doomed)
	}
	if deleted, err = m.hist.deleteRuns(doomed); err != nil {
		return 0, nil, 0, err
	}
	for full := range outputs {
		if _, err := os.Lstat(full); err != nil {
			continue // nothing there: not counted
		}
		if media.RemoveTree(full) == nil {
			filesRemoved++
		}
	}
	return deleted, skipped, filesRemoved, nil
}

// outputPathOf maps a run's artifact (a project-relative slash path such as
// out/<name>/manifest.json) to what deleting the run's files removes: the
// whole out/<name> folder, or the file itself for out/<file>. Anything else
// (empty, absolute, climbing out, outside out/) yields "".
func outputPathOf(artifact string) string {
	if artifact == "" || strings.HasPrefix(artifact, "/") {
		return ""
	}
	parts := strings.Split(artifact, "/")
	if len(parts) < 2 || parts[0] != "out" {
		return ""
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return ""
		}
	}
	if len(parts) == 2 {
		return artifact
	}
	return "out/" + parts[1]
}

// outputsOf resolves the output paths of the given runs, skipping the
// projects that have a queued or running run. The caller holds m.mu.
func (m *runManager) outputsOf(ids []string) map[string]struct{} {
	busy := map[string]bool{}
	for _, rn := range m.runs {
		busy[rn.rec.Project] = true
	}
	out := map[string]struct{}{}
	for _, id := range ids {
		rec, err := m.hist.getRun(id)
		if err != nil || rec.Project == "" || busy[rec.Project] {
			continue
		}
		out[filepath.Join(m.projectsDir, rec.Project, "tmp")] = struct{}{}
		out[filepath.Join(m.projectsDir, rec.Project, pipeline.KeptClipsDir)] = struct{}{}
		rel := outputPathOf(rec.Artifact)
		if rel == "" {
			continue
		}
		out[filepath.Join(m.projectsDir, rec.Project, filepath.FromSlash(rel))] = struct{}{}
	}
	return out
}

// DELETE /api/runs/{id}
func (h *Handler) handleRunDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rec, err := h.rm.hist.getRun(id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("run %s not found", id))
		return
	}
	deleted, skipped, _, err := h.rm.deleteRuns([]string{id}, nil, r.URL.Query().Get("delete_files") == "true")
	switch {
	case err != nil:
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, "could not delete the run")
	case len(skipped) > 0:
		api.WriteError(w, http.StatusConflict, api.CodeConflict,
			fmt.Sprintf("run %s is %s: cancel it first (POST /api/runs/%s/cancel), then delete it", id, rec.State, id))
	case deleted == 0:
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("run %s not found", id))
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// POST /api/runs/delete  {"ids": [...]}  or  {"project"?, "states"?, "before"?}
func (h *Handler) handleRunsDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs     *[]string   `json:"ids"`
		Project *string     `json:"project"`
		States  *[]RunState `json:"states"`
		Before  *string     `json:"before"`
		// DeleteFiles also removes the output folder of each deleted run.
		DeleteFiles bool `json:"delete_files"`
	}
	api.RaiseBodyLimit(w, r, 4<<20)
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&body); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "invalid body: expected {\"ids\": [...]} or a filter {\"project\", \"states\", \"before\"}")
		return
	}
	hasFilter := body.Project != nil || body.States != nil || body.Before != nil
	switch {
	case body.IDs != nil && hasFilter:
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "send either ids or a filter (project, states, before), not both")
		return
	case body.IDs == nil && !hasFilter:
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "name the runs to delete: ids, or a filter with project, states or before")
		return
	}
	var (
		deleted      int
		skipped      []string
		filesRemoved int
		err          error
	)
	if body.IDs != nil {
		ids := h.host.OnlyVisibleRunIDs(*body.IDs)
		deleted, skipped, filesRemoved, err = h.rm.deleteRuns(ids, nil, body.DeleteFiles)
	} else {
		f := runDeleteFilter{}
		if body.Project != nil {
			f.Project = *body.Project
		}
		if body.States != nil {
			for _, st := range *body.States {
				if !isTerminalState(st) {
					api.WriteError(w, http.StatusBadRequest, api.CodeInvalid,
						fmt.Sprintf("state %q cannot be deleted: only finished runs (%s) can", st, joinStates(terminalStates)))
					return
				}
			}
			f.States = *body.States
		}
		if body.Before != nil {
			t, perr := time.Parse(time.RFC3339, *body.Before)
			if perr != nil {
				api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid before %q: use an RFC 3339 timestamp", *body.Before))
				return
			}
			f.Before = &t
		}
		if !h.host.ShowsAdult() {
			// the filter must not reach the runs of a hidden project: it selects the ids first
			var ids []string
			if ids, err = h.rm.hist.terminalRunIDs(f); err == nil {
				deleted, skipped, filesRemoved, err = h.rm.deleteRuns(h.host.OnlyVisibleRunIDs(ids), nil, body.DeleteFiles)
			}
		} else {
			deleted, skipped, filesRemoved, err = h.rm.deleteRuns(nil, &f, body.DeleteFiles)
		}
	}
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, "could not delete the runs")
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"deleted": deleted, "skipped_active": skipped, "files_removed": filesRemoved})
}

// GET /api/runs/{id}
func (h *Handler) handleRunGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rec, err := h.rm.hist.getRun(id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("run %s not found", id))
		return
	}
	resp := map[string]any{"run": h.viewsOf([]Run{*rec})[0]}
	if pos := h.rm.queuePosition(id); pos > 0 {
		resp["queue_position"] = pos
	}
	api.WriteJSON(w, http.StatusOK, resp)
}

// POST /api/runs/{id}/cancel
func (h *Handler) handleRunCancel(w http.ResponseWriter, r *http.Request) {
	rec, lerr := h.rm.cancelRun(r.PathValue("id"))
	if lerr != nil {
		api.WriteError(w, lerr.Status, lerr.Code, lerr.Msg)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"run": rec})
}

// AnyBusy names a run that is queued or active, if there is one.
func (h *Handler) AnyBusy() (string, bool) { return h.rm.anyBusy() }

// Launch validates the project and the request, records the run as queued and schedules it.
func (h *Handler) Launch(projectID string, req RunRequest) (Run, int, *LaunchError) {
	return h.rm.launch(projectID, req)
}

// CancelRun cancels a queued or active run.
func (h *Handler) CancelRun(id string) (Run, *LaunchError) { return h.rm.cancelRun(id) }

// RunRecord is the stored record of a run.
func (h *Handler) RunRecord(id string) (*Run, error) { return h.rm.hist.getRun(id) }

// RunEvents are the stored event lines of a run.
func (h *Handler) RunEvents(id string) ([]string, error) { return h.rm.hist.listEvents(id) }

// ReviewProject is the project of a review; ok is false when there is no such review.
func (h *Handler) ReviewProject(id string) (project string, ok bool) {
	rev, err := h.rm.hist.getReview(id)
	if err != nil {
		return "", false
	}
	return rev.Project, true
}

// Context is the context of the run manager: it ends when the server shuts down.
func (h *Handler) Context() context.Context { return h.rm.rootCtx }

// Shutdown stops the runs and the reviews and waits for them.
func (h *Handler) Shutdown() {
	h.rm.shutdown()
	h.rv.wg.Wait() // the reviews share the context of the run manager, which is cancelled by now
	if h.ownStore {
		_ = h.cfg.Store.Close()
	}
}

// UsesGenerator says whether a queued or running run of music makes its songs with that generator.
func (h *Handler) UsesGenerator(generator string) bool {
	m := h.rm
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, rn := range m.runs {
		if rn.cfg != nil && rn.cfg.Output.Mode == config.ModeMusic && rn.cfg.Defaults.Music.ResolvedGenerator() == generator {
			return true
		}
	}
	return false
}

// UsesModels3D says whether a queued or running run makes 3D models, which is to say that it needs the 3D service to stay as it is.
func (h *Handler) UsesModels3D() bool {
	m := h.rm
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, rn := range m.runs {
		if rn.cfg != nil && rn.cfg.Output.Mode == config.ModeModels3D {
			return true
		}
	}
	return false
}

// ProjectSize is the size in bytes of everything a project keeps on disk.
func (h *Handler) ProjectSize(id string) int64 {
	_, total := h.projectStorage(id)
	return total
}

// validRunMode says whether the history and the statistics take it as a `mode`: the output modes they report (not video_edit, which has its own tools) and
// animation, the music-motion pieces.
func validRunMode(m string) bool {
	switch mode := config.OutputMode(m); mode {
	case config.ModeVideo, config.ModeImages, config.ModeMusic, config.ModeModels3D, ModeAnimation:
		return true
	}
	return false
}

// RunModesText names the modes a run list or the statistics accept, for the message of a 400.
const RunModesText = "video, images, music, models3d or animation"

// parseRunModes reads a comma separated list of modes (video, images, music, models3d, animation); bad is the first one that is none.
func parseRunModes(v string) (modes []string, bad string) {
	for _, m := range strings.Split(v, ",") {
		if m = strings.TrimSpace(m); m == "" {
			continue
		}
		if !validRunMode(m) {
			return nil, m
		}
		modes = append(modes, m)
	}
	return modes, ""
}

// onlyModes keeps the runs whose project is of one of the modes (animation for the music-motion pieces).
func (h *historyStore) onlyModes(runs []Run, modes []string) []Run {
	kinds := map[string]string{}
	out := runs[:0:0]
	for _, r := range runs {
		k, ok := kinds[r.Project]
		if !ok {
			if IsAnimationProject(r.Project) {
				k = string(ModeAnimation)
			} else if h.modeOf != nil {
				k = h.modeOf(r.Project)
			}
			kinds[r.Project] = k
		}
		if slices.Contains(modes, k) {
			out = append(out, r)
		}
	}
	return out
}

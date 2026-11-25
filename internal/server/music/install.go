package music

// install.go: what a music generator lacks, downloaded or built from the page. POST /api/music/engines/{engine}/install answers a plan (what
// would be installed, how big it is and the licences) and, with `confirm: true`, starts a job that runs the parts in order and that can be
// followed (GET /api/music/installs/{id}, its events) and cancelled. See the spec add-music-backend-selection.
//
// Three parts exist, and each is the program the project already uses by hand: `service` is scripts/get-services.sh (the .NET service),
// `image` is `docker build` of docker/<engine> (the container) and `weights` is the catalog download of the weights (the same one the Models
// screen starts; DiffRhythm2's with the Docker backend is `fetch.py` of its own image, which needs no .NET). Nothing in a request is a program
// name, an image name or a path: the request only picks among these parts of the two generators.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/server/api"
	"veritmo/internal/server/models"
	"veritmo/internal/server/workspace"
)

// The parts of an install.
const (
	partService = "service"
	partImage   = "image"
	partWeights = "weights"
)

// The states of an install job and of its parts.
const (
	installRunning   = "running"
	installDone      = "done"
	installFailed    = "failed"
	installCancelled = "cancelled"
	partPending      = "pending"
	partSkipped      = "skipped"   // it was there when its turn came
	partCancelled    = "cancelled" // it was running when the install was cancelled
)

// ReasonNoDisk is the reason of the 422 of an install that does not fit on the disk.
const ReasonNoDisk = "no_disk"

// catalogWeights is the catalog entry that holds the weights of each generator.
var catalogWeights = map[string]string{engineHeartMuLa: "music-heartmula-3b", engineDiffRhythm2: "music-diffrhythm2"}

// weightsLicenses are the licences the person accepts by confirming the download of the weights.
var weightsLicenses = map[string][]string{
	engineHeartMuLa:   {"HeartMuLa weights: Apache-2.0"},
	engineDiffRhythm2: {"DiffRhythm2 weights: Apache-2.0", "MuQ-MuLan weights (style encoder): CC-BY-NC-4.0, non-commercial use only"},
}

type installRequest struct {
	Backend string   `json:"backend"`
	Parts   []string `json:"parts"`
	Confirm bool     `json:"confirm"`
}

// InstallPart is one thing an install fetches or builds.
type InstallPart struct {
	Part      string   `json:"part"`
	NeedBytes int64    `json:"need_bytes,omitempty"`
	Licenses  []string `json:"licenses,omitempty"`
	// State and Error are only in a job: pending, running, done, skipped or failed.
	State string `json:"state,omitempty"`
	Error string `json:"error,omitempty"`
}

// InstallDisk is the disk one place needs for the parts that go there, and what it has free.
type InstallDisk struct {
	Path       string   `json:"path"`
	Parts      []string `json:"parts"`
	NeedBytes  int64    `json:"need_bytes"`
	FreeBytes  int64    `json:"free_bytes"`
	Sufficient bool     `json:"sufficient"`
}

// InstallPlan is what an install would do: the answer to a request without `confirm`.
type InstallPlan struct {
	Engine          string        `json:"engine"`
	Backend         string        `json:"backend"`
	Parts           []InstallPart `json:"parts"`
	TotalBytes      int64         `json:"total_bytes"`
	Disk            []InstallDisk `json:"disk"`
	Licenses        []string      `json:"licenses,omitempty"`
	ConfirmRequired bool          `json:"confirm_required"`
}

// InstallJob is the record of an install that was confirmed.
type InstallJob struct {
	ID         string        `json:"id"`
	Engine     string        `json:"engine"`
	Backend    string        `json:"backend"`
	State      string        `json:"state"`
	Parts      []InstallPart `json:"parts"`
	Error      string        `json:"error,omitempty"`
	CreatedAt  time.Time     `json:"created_at"`
	FinishedAt *time.Time    `json:"finished_at,omitempty"`
}

type installJob struct {
	rec        InstallJob
	log        *api.EventLog
	cancel     context.CancelFunc
	userCancel bool
}

// installJobs keeps the installs of this server: at most one runs at a time, and the last few are remembered.
type installJobs struct {
	mu      sync.Mutex
	jobs    map[string]*installJob
	order   []string
	active  *installJob
	counter int
	wg      sync.WaitGroup
	seams   installSeams
}

const maxInstallHistory = 20

// The seams of an install: nil uses the real thing.
type installSeams struct {
	// exec runs a program, streaming its output lines to logf; dir is empty for the current one.
	exec func(ctx context.Context, dir, name string, args []string, logf func(string)) error
	// weights downloads the weights of an engine for a backend (the catalog download, or fetch.py in the image).
	weights func(ctx context.Context, h *Handler, j *installJob, engine, backend string) error
	// free is the bytes free on the file system of a path.
	free func(path string) (uint64, error)
}

func (j *installJob) emit(typ string, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	line, _ := json.Marshal(map[string]json.RawMessage{"type": json.RawMessage(strconvQuote(typ)), "data": raw})
	_, _ = j.log.Write(append(line, '\n'))
}

func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

func (j *installJob) logf(line string) { j.emit("log", map[string]string{"line": line}) }

// installOrder is the order the parts run in: what the weights step may need (the image, the service) comes first.
var installOrder = []string{partService, partImage, partWeights}

func (h *Handler) installSeam() installSeams {
	s := h.installs.seams
	if s.exec == nil {
		s.exec = realExec
	}
	if s.weights == nil {
		s.weights = installWeights
	}
	if s.free == nil {
		s.free = func(p string) (uint64, error) { return workspace.FreeDiskBytes(nearestExisting(p)) }
	}
	return s
}

// nearestExisting walks up from a path to the first folder that exists (the weights folder may not be made yet).
func nearestExisting(p string) string {
	for p != "" && p != "/" && p != "." {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		p = filepath.Dir(p)
	}
	return "/"
}

// installPlace is where a part writes, for the disk check.
func (h *Handler) installPlace(ctx context.Context, engine, part string) string {
	switch part {
	case partService:
		if s := h.host.Services(); s != nil && s.Dir != "" {
			return s.Dir
		}
		abs, _ := filepath.Abs("bin/services")
		return abs
	case partImage:
		// Docker keeps its images in its own root folder; it may not be readable by this user, so the nearest folder that is does.
		out, err := h.heartMulaRunnerOrDefault().Run(ctx, musicsvc.DockerProgram(), []string{"info", "--format", "{{.DockerRootDir}}"})
		if p := strings.TrimSpace(string(out)); err == nil && p != "" {
			return p
		}
		return "/var/lib/docker"
	default:
		if engine == engineDiffRhythm2 {
			return h.diffRhythm2WeightsDir()
		}
		return h.heartMulaWeightsDir()
	}
}

// planInstall works out what installing would do for an engine and a backend, without changing anything. Parts is what the request asked for
// (empty: everything that is missing); only what is missing is in the plan.
func (h *Handler) planInstall(ctx context.Context, name, backend string, asked []string) (*InstallPlan, *BackendError) {
	eng, ok := h.engineOf(name, false)
	if !ok || (name != engineHeartMuLa && name != engineDiffRhythm2) {
		return nil, &BackendError{Status: http.StatusNotFound, Msg: fmt.Sprintf("no such generator %q: use %s or %s", name, engineHeartMuLa, engineDiffRhythm2)}
	}
	if backend == "" {
		backend = eng.Local.Backend
	}
	if backend == "" {
		backend = musicsvc.BackendDotnet
	}
	if !musicsvc.ValidBackend(backend) {
		return nil, &BackendError{Status: http.StatusUnprocessableEntity, Msg: "the backend must be dotnet or docker"}
	}
	status := eng.Backends(ctx)[backend]
	var cannot string // what the server cannot install by itself
	for _, m := range status.Missing {
		switch m {
		case musicsvc.MissingDocker:
			return nil, &BackendError{Status: http.StatusUnprocessableEntity, Reason: musicsvc.ReasonNoDocker, Msg: "docker is not installed or does not answer: install it first (" + status.Detail + ")"}
		case musicsvc.MissingGPURuntime:
			cannot = "docker has no NVIDIA runtime: install the NVIDIA Container Toolkit first"
		}
	}
	if cannot != "" && len(status.Missing) == 1 {
		return nil, &BackendError{Status: http.StatusUnprocessableEntity, Reason: musicsvc.ReasonNoGPURuntime, Msg: cannot}
	}
	wanted := map[string]bool{}
	for _, m := range status.Missing {
		if m == musicsvc.MissingService || m == musicsvc.MissingImage || m == musicsvc.MissingWeights {
			wanted[m] = true
		}
	}
	if len(asked) > 0 {
		allowed := map[string]bool{partWeights: true}
		if backend == musicsvc.BackendDotnet {
			allowed[partService] = true
		} else {
			allowed[partImage] = true
		}
		keep := map[string]bool{}
		for _, p := range asked {
			if !allowed[p] {
				return nil, &BackendError{Status: http.StatusUnprocessableEntity, Msg: fmt.Sprintf("the part %q does not exist for the %s backend", p, backend)}
			}
			keep[p] = wanted[p]
		}
		wanted = keep
	}
	plan := &InstallPlan{Engine: name, Backend: backend, Parts: []InstallPart{}, Disk: []InstallDisk{}}
	for _, part := range installOrder {
		if !wanted[part] {
			continue
		}
		p := InstallPart{Part: part}
		switch part {
		case partService:
			p.NeedBytes = eng.Spec.Sizes.Service
		case partImage:
			p.NeedBytes = eng.Spec.Sizes.Image
		case partWeights:
			p.NeedBytes = eng.Spec.Sizes.Weights
			p.Licenses = weightsLicenses[name]
			plan.Licenses = append(plan.Licenses, weightsLicenses[name]...)
		}
		plan.TotalBytes += p.NeedBytes
		plan.Parts = append(plan.Parts, p)
	}
	plan.ConfirmRequired = len(plan.Parts) > 0
	// the disk, added up per file system
	seams := h.installSeam()
	groups := map[uint64]*InstallDisk{}
	var keys []uint64
	for _, p := range plan.Parts {
		place := h.installPlace(ctx, name, p.Part)
		dev := deviceOf(nearestExisting(place))
		g := groups[dev]
		if g == nil {
			free, _ := seams.free(place)
			g = &InstallDisk{Path: place, FreeBytes: int64(free)}
			groups[dev] = g
			keys = append(keys, dev)
		}
		g.Parts = append(g.Parts, p.Part)
		g.NeedBytes += p.NeedBytes
	}
	sort.Slice(keys, func(a, b int) bool { return keys[a] < keys[b] })
	for _, k := range keys {
		g := groups[k]
		g.Sufficient = g.FreeBytes >= g.NeedBytes
		plan.Disk = append(plan.Disk, *g)
	}
	return plan, nil
}

// POST /api/music/engines/{engine}/install {"backend"?: "dotnet"|"docker", "parts"?: ["service"|"image"|"weights"], "confirm"?: true}
//
//	-> 200 {plan}            without confirm: what it would download or build, how big, the licences, the disk; nothing changes
//	-> 202 {install: {...}}  with confirm: a job that runs the parts in order (follow it at /api/music/installs/{id})
//
// 409 while the generator runs, a run uses it or another install runs; 422 when the disk lacks the space (reason no_disk, with how much),
// when Docker cannot be used or for a part that does not exist for the backend.
func (h *Handler) handleEngineInstall(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("engine")
	var req installRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil && err != io.EOF {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
			return
		}
	}
	plan, berr := h.planInstall(r.Context(), name, strings.ToLower(strings.TrimSpace(req.Backend)), req.Parts)
	if berr != nil {
		writeBackendError(w, berr)
		return
	}
	if !req.Confirm {
		api.WriteJSON(w, http.StatusOK, plan)
		return
	}
	if len(plan.Parts) == 0 {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, "everything the "+plan.Backend+" backend of "+name+" needs is already there")
		return
	}
	for _, d := range plan.Disk {
		if !d.Sufficient {
			api.WriteJSON(w, http.StatusUnprocessableEntity, api.APIError{Error: api.APIErrorBody{Code: api.CodeValidation, Reason: ReasonNoDisk,
				Message: fmt.Sprintf("%s needs %.1f GB free and has %.1f GB: %.1f GB more are needed", d.Path, gigabytes(d.NeedBytes), gigabytes(d.FreeBytes), gigabytes(d.NeedBytes-d.FreeBytes))}})
			return
		}
	}
	rec, berr := h.startInstall(r.Context(), name, plan)
	if berr != nil {
		writeBackendError(w, berr)
		return
	}
	api.WriteJSON(w, http.StatusAccepted, map[string]any{"install": rec})
}

func gigabytes(b int64) float64 { return float64(b) / (1 << 30) }

func writeBackendError(w http.ResponseWriter, e *BackendError) {
	code := api.CodeValidation
	switch {
	case e.Status == http.StatusConflict:
		code = api.CodeConflict
	case e.Status == http.StatusNotFound:
		code = api.CodeNotFound
	case e.Status >= 500:
		code = api.CodeInternal
	}
	api.WriteJSON(w, e.Status, api.APIError{Error: api.APIErrorBody{Code: code, Message: e.Msg, Reason: e.Reason}})
}

// startInstall refuses what must not run now and starts the job.
func (h *Handler) startInstall(ctx context.Context, name string, plan *InstallPlan) (InstallJob, *BackendError) {
	eng, _ := h.engineOf(name, false)
	if st := eng.Local.State(ctx, false); st.State != musicsvc.StateStopped {
		return InstallJob{}, &BackendError{Status: http.StatusConflict, Msg: "the generator is running: stop it before installing"}
	}
	if _, starting := h.containers.startingSince(name); starting || h.ws.UsesGenerator(name) {
		return InstallJob{}, &BackendError{Status: http.StatusConflict, Msg: "the generator is starting or a run is making songs with it: wait for it"}
	}
	m := &h.installs
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active != nil {
		return InstallJob{}, &BackendError{Status: http.StatusConflict, Msg: "another install is running (" + m.active.rec.ID + "): wait for it or cancel it"}
	}
	m.counter++
	id := fmt.Sprintf("inst-%d-%d", time.Now().Unix(), m.counter)
	j := &installJob{rec: InstallJob{ID: id, Engine: name, Backend: plan.Backend, State: installRunning, CreatedAt: time.Now().UTC()}, log: api.NewEventLog(nil)}
	for _, p := range plan.Parts {
		p.State = partPending
		j.rec.Parts = append(j.rec.Parts, p)
	}
	if m.jobs == nil {
		m.jobs = map[string]*installJob{}
	}
	m.jobs[id] = j
	m.order = append(m.order, id)
	if len(m.order) > maxInstallHistory {
		old := m.order[0]
		m.order = m.order[1:]
		delete(m.jobs, old)
	}
	m.active = j
	jctx, cancel := context.WithCancel(context.WithoutCancel(h.ws.Context()))
	j.cancel = cancel
	j.emit("state", j.rec)
	m.wg.Add(1)
	go h.runInstall(jctx, j)
	return j.rec, nil
}

func (j *installJob) setPart(i int, state, errText string) {
	j.rec.Parts[i].State, j.rec.Parts[i].Error = state, errText
	j.emit("state", j.rec)
}

// stillMissing says whether a part is still lacking now, when its turn comes (a repeat after a failure skips what already worked).
func (h *Handler) stillMissing(ctx context.Context, j *installJob, part string) bool {
	eng, _ := h.engineOf(j.rec.Engine, false)
	for _, m := range eng.Backends(ctx)[j.rec.Backend].Missing {
		if m == part {
			return true
		}
	}
	return false
}

func (h *Handler) runInstall(ctx context.Context, j *installJob) {
	m := &h.installs
	defer m.wg.Done()
	seams := h.installSeam()
	var failure error
	for i := range j.rec.Parts {
		part := j.rec.Parts[i].Part
		if ctx.Err() != nil {
			break
		}
		m.mu.Lock()
		j.setPart(i, "running", "")
		m.mu.Unlock()
		if !h.stillMissing(ctx, j, part) {
			m.mu.Lock()
			j.setPart(i, partSkipped, "")
			m.mu.Unlock()
			continue
		}
		var err error
		switch part {
		case partService:
			err = seams.exec(ctx, "", "bash", []string{"scripts/get-services.sh", "--only", j.rec.Engine}, j.logf)
		case partImage:
			err = seams.exec(ctx, "", musicsvc.DockerProgram(), []string{"build", "-t", h.imageOf(j.rec.Engine), filepath.Join("docker", j.rec.Engine)}, j.logf)
		case partWeights:
			err = seams.weights(ctx, h, j, j.rec.Engine, j.rec.Backend)
		}
		m.mu.Lock()
		if err != nil && ctx.Err() == nil {
			j.setPart(i, installFailed, err.Error())
			failure = fmt.Errorf("%s: %w", part, err)
			m.mu.Unlock()
			break
		}
		if ctx.Err() == nil {
			j.setPart(i, installDone, "")
		}
		m.mu.Unlock()
	}
	m.mu.Lock()
	if ctx.Err() != nil && failure == nil {
		for i := range j.rec.Parts {
			if j.rec.Parts[i].State == "running" {
				j.rec.Parts[i].State = partCancelled
			}
		}
	}
	now := time.Now().UTC()
	j.rec.FinishedAt = &now
	switch {
	case failure != nil:
		j.rec.State, j.rec.Error = installFailed, failure.Error()
	case ctx.Err() != nil:
		j.rec.State = installCancelled
	default:
		j.rec.State = installDone
	}
	m.active = nil
	j.emit("state", j.rec)
	j.log.Close()
	m.mu.Unlock()
	j.cancel()
	h.engines.bump()
}

// imageOf is the image the container backend of a generator runs (its Docker spec).
func (h *Handler) imageOf(engine string) string {
	core, _ := h.engineOf(engine, false)
	return core.Spec.Docker.Image
}

// installWeights downloads the weights: the catalog entry, or fetch.py of the DiffRhythm2 image for the Docker backend.
func installWeights(ctx context.Context, h *Handler, j *installJob, engine, backend string) error {
	if engine == engineDiffRhythm2 && backend == musicsvc.BackendDocker {
		dir := h.diffRhythm2WeightsDir()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		u, err := user.Current()
		if err != nil {
			return err
		}
		return h.installSeam().exec(ctx, "", musicsvc.DockerProgram(), []string{"run", "--rm", "--user", u.Uid + ":" + u.Gid, "-e", "HOME=/tmp", "-v", dir + ":/ckpt", h.imageOf(engineDiffRhythm2), "python", "/opt/fetch.py"}, j.logf)
	}
	rec, err := h.models.StartCatalog(catalogWeights[engine])
	if errors.Is(err, models.ErrAlreadyInstalled) {
		return nil
	}
	if err != nil {
		return err
	}
	j.logf("downloading the weights (" + rec.Name + ")")
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	last := ""
	for {
		select {
		case <-ctx.Done():
			h.models.CancelDownload(rec.ID)
			return ctx.Err()
		case <-tick.C:
		}
		cur, ok := h.models.Download(rec.ID)
		if !ok {
			return errors.New("the download is gone")
		}
		if line := fmt.Sprintf("%d of %d bytes", cur.Bytes, cur.Total); line != last && cur.Total > 0 {
			j.logf(line)
			last = line
		}
		switch cur.State {
		case models.DownloadSucceeded:
			return nil
		case models.DownloadFailed:
			return errors.New(cur.Error)
		case models.DownloadCancelled:
			return errors.New("the download was cancelled")
		}
	}
}

// realExec runs a program and sends each line it writes to logf.
func realExec(ctx context.Context, dir, name string, args []string, logf func(string)) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 15 * time.Second
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	logf("$ " + name + " " + strings.Join(args, " "))
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	var tail []string
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			logf(line)
			if tail = append(tail, line); len(tail) > 5 {
				tail = tail[1:]
			}
		}
	}()
	err := cmd.Wait()
	_ = pw.Close()
	<-done
	if err != nil {
		return fmt.Errorf("%s failed: %s", name, strings.Join(tail, " | "))
	}
	return nil
}

// GET /api/music/installs -> {"installs": [...]} newest first
func (h *Handler) handleInstallsList(w http.ResponseWriter, r *http.Request) {
	m := &h.installs
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []InstallJob{}
	for i := len(m.order) - 1; i >= 0; i-- {
		out = append(out, m.jobs[m.order[i]].rec)
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"installs": out})
}

func (h *Handler) installByID(w http.ResponseWriter, id string) *installJob {
	m := &h.installs
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("install %s not found", id))
		return nil
	}
	return j
}

// GET /api/music/installs/{id} -> {"install": {...}}
func (h *Handler) handleInstallGet(w http.ResponseWriter, r *http.Request) {
	j := h.installByID(w, r.PathValue("id"))
	if j == nil {
		return
	}
	h.installs.mu.Lock()
	rec := j.rec
	h.installs.mu.Unlock()
	api.WriteJSON(w, http.StatusOK, map[string]any{"install": rec})
}

// POST /api/music/installs/{id}/cancel -> {"install": {...}}; what ended is left as it is
func (h *Handler) handleInstallCancel(w http.ResponseWriter, r *http.Request) {
	j := h.installByID(w, r.PathValue("id"))
	if j == nil {
		return
	}
	h.installs.mu.Lock()
	if j.rec.State == installRunning {
		j.userCancel = true
		j.cancel()
	}
	rec := j.rec
	h.installs.mu.Unlock()
	api.WriteJSON(w, http.StatusOK, map[string]any{"install": rec})
}

// GET /api/music/installs/{id}/events replays the events of the job and follows it until it ends (`state` with the record, `log` with a line),
// closed by stream-end.
func (h *Handler) handleInstallEvents(w http.ResponseWriter, r *http.Request) {
	j := h.installByID(w, r.PathValue("id"))
	if j == nil {
		return
	}
	sw, ok := api.NewSSEWriter(w)
	if !ok {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, "response writer does not support streaming")
		return
	}
	ctx := r.Context()
	j.log.WatchCancel(ctx)
	for i := 0; ; i++ {
		line, over := j.log.Next(ctx, i)
		if line != nil {
			var env struct {
				Type string          `json:"type"`
				Data json.RawMessage `json:"data"`
			}
			if json.Unmarshal(line, &env) == nil && env.Type != "" {
				sw.Named(env.Type, env.Data)
			}
			continue
		}
		if over {
			if ctx.Err() != nil {
				return
			}
			break
		}
	}
	sw.Notify("stream-end", map[string]string{"id": j.rec.ID})
}

package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"veritmo/internal/project/config"
	"veritmo/internal/server/api"

	"veritmo/internal/platform/events"
	"veritmo/internal/platform/runner"
)

// fakeLook pretends every pipeline binary exists.
func fakeLook(name string) (string, error) { return "/fake/" + name, nil }

// blockingExecute returns an execute seam whose runs block until released
// (or their context is cancelled) and records starts.
type fakeExec struct {
	started chan string          // receives run IDs as they start
	mu      sync.Mutex           // guards release (the test and the worker both touch it)
	release map[string]chan bool // run ID -> succeed(true)/keep error(false)
}

func newFakeExec() *fakeExec {
	return &fakeExec{started: make(chan string, 16), release: map[string]chan bool{}}
}

func (f *fakeExec) chanFor(id string) chan bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.release[id]; !ok {
		f.release[id] = make(chan bool, 1)
	}
	return f.release[id]
}

func (f *fakeExec) execute(ctx context.Context, rn *run, emitter *events.Emitter) (string, error) {
	emitter.PipelineStart()
	f.started <- rn.rec.ID
	select {
	case ok := <-f.chanFor(rn.rec.ID):
		if !ok {
			emitter.Error("_global", "boom")
			return "", fmt.Errorf("boom")
		}
		emitter.Done("out/x.mp4")
		return "out/x.mp4", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// launchServer returns a projects server with a project created, the fake
// executor installed and binary lookups faked.
func launchServer(t *testing.T) (*testServer, *fakeExec) {
	t.Helper()
	return launchServerSampling(t, SamplerConfig{})
}

// launchServerSampling is launchServer with the sampler of the runs given.
func launchServerSampling(t *testing.T, sc SamplerConfig) (*testServer, *fakeExec) {
	t.Helper()
	s, _ := newProjectsServerSampling(t, sc)
	createProject(t, s, "demo")
	fe := newFakeExec()
	s.rm.execute = fe.execute
	s.rm.lookPath = fakeLook
	t.Cleanup(s.rm.shutdown)
	return s, fe
}

func launchRun(t *testing.T, s *testServer, project string, req map[string]any) (string, int) {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/"+project+"/runs", req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("launch: status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Run           Run `json:"run"`
		QueuePosition int `json:"queue_position"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Run.ID, resp.QueuePosition
}

func waitState(t *testing.T, s *testServer, id string, want RunState) Run {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rec, err := s.rm.hist.getRun(id)
		if err == nil && rec.State == want {
			return *rec
		}
		time.Sleep(5 * time.Millisecond)
	}
	rec, _ := s.rm.hist.getRun(id)
	t.Fatalf("run %s never reached %s (last: %+v)", id, want, rec)
	return Run{}
}

func waitStart(t *testing.T, fe *fakeExec) string {
	t.Helper()
	select {
	case id := <-fe.started:
		return id
	case <-time.After(3 * time.Second):
		t.Fatal("no run started")
		return ""
	}
}

func TestHistory_RoundTripAndRecovery(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "h.db")
	h, err := openHistory(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	r := Run{ID: "run-1", Project: "demo", State: StateRunning, GPU: "1", KeepTmp: true, VRAMWarnMB: 2048, CreatedAt: now, StartedAt: &now}
	if err := h.createRun(&r); err != nil {
		t.Fatal(err)
	}
	if err := h.appendEvent("run-1", 0, `{"type":"pipeline_start"}`); err != nil {
		t.Fatal(err)
	}
	if err := h.appendEvent("run-1", 1, `{"type":"phase_start","scene_id":"a","phase":"tts"}`); err != nil {
		t.Fatal(err)
	}
	q := Run{ID: "run-2", Project: "demo", State: StateQueued, CreatedAt: now.Add(time.Second)}
	if err := h.createRun(&q); err != nil {
		t.Fatal(err)
	}

	// Reopen: the running and queued runs become interrupted; data survives.
	h2, err := openHistory(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"run-1", "run-2"} {
		got, err := h2.getRun(id)
		if err != nil {
			t.Fatalf("getRun(%s): %v", id, err)
		}
		if got.State != StateInterrupted {
			t.Errorf("%s state = %s, want interrupted", id, got.State)
		}
	}
	got, _ := h2.getRun("run-1")
	if got.Project != "demo" || got.GPU != "1" || !got.KeepTmp || got.VRAMWarnMB != 2048 {
		t.Errorf("options not persisted: %+v", got)
	}
	lines, err := h2.listEvents("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[1] != `{"type":"phase_start","scene_id":"a","phase":"tts"}` {
		t.Errorf("events not persisted verbatim: %q", lines)
	}

	list, err := h2.listRuns("", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != "run-2" {
		t.Errorf("listRuns order wrong: %+v", list)
	}
}

func TestRuns_InvalidProjectNeverQueues(t *testing.T) {
	s, _ := launchServer(t)
	// Break the project's config.
	bad := "project: demo\noutput: {mode: video}\nscenes: [{id: a, prompt: ''}]\n"
	if err := s.SaveProjectDefinition("demo", []byte(bad), "save"); err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/demo/runs", map[string]any{})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422: %s", rec.Code, rec.Body.String())
	}
	runs, err := s.rm.hist.listRuns("", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Errorf("a run was recorded for an invalid project: %+v", runs)
	}
}

func TestRuns_SameLaneSerializesFIFO(t *testing.T) {
	s, fe := launchServer(t)

	id1, pos1 := launchRun(t, s, "demo", map[string]any{"gpu": "1"})
	started := waitStart(t, fe)
	if started != id1 || pos1 != 0 {
		t.Fatalf("first run should start immediately (pos=%d)", pos1)
	}
	id2, pos2 := launchRun(t, s, "demo", map[string]any{"gpu": "1"})
	if pos2 != 1 {
		t.Errorf("second run queue position = %d, want 1", pos2)
	}
	// Second must NOT start while first is active.
	select {
	case got := <-fe.started:
		t.Fatalf("run %s started while lane busy", got)
	case <-time.After(100 * time.Millisecond):
	}
	// Release the first; the second starts and both succeed.
	fe.chanFor(id1) <- true
	waitState(t, s, id1, StateSucceeded)
	if got := waitStart(t, fe); got != id2 {
		t.Fatalf("expected %s to start next, got %s", id2, got)
	}
	fe.chanFor(id2) <- true
	rec := waitState(t, s, id2, StateSucceeded)
	if rec.Artifact != "out/x.mp4" {
		t.Errorf("artifact = %q", rec.Artifact)
	}
}

func TestRuns_DifferentLanesRunConcurrently(t *testing.T) {
	s, fe := launchServer(t)

	id0, _ := launchRun(t, s, "demo", map[string]any{"gpu": "0"})
	id1, _ := launchRun(t, s, "demo", map[string]any{"gpu": "1"})
	got := map[string]bool{waitStart(t, fe): true, waitStart(t, fe): true}
	if !got[id0] || !got[id1] {
		t.Fatalf("both lanes should start concurrently, started: %v", got)
	}
	fe.chanFor(id0) <- true
	fe.chanFor(id1) <- false
	waitState(t, s, id0, StateSucceeded)
	rec := waitState(t, s, id1, StateFailed)
	if !strings.Contains(rec.Error, "boom") {
		t.Errorf("failed run error = %q", rec.Error)
	}
}

func TestRuns_CancelActive(t *testing.T) {
	s, fe := launchServer(t)
	id1, _ := launchRun(t, s, "demo", map[string]any{"gpu": "1"})
	waitStart(t, fe)
	id2, _ := launchRun(t, s, "demo", map[string]any{"gpu": "1"})

	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/runs/"+id1+"/cancel", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body.String())
	}
	waitState(t, s, id1, StateCancelled)
	// The lane frees and the queued run starts.
	if got := waitStart(t, fe); got != id2 {
		t.Fatalf("queued run should start after cancel, got %s", got)
	}
	fe.chanFor(id2) <- true
	waitState(t, s, id2, StateSucceeded)

	// Cancelling a finished run is a conflict.
	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/runs/"+id1+"/cancel", nil)
	if rec.Code != http.StatusConflict {
		t.Errorf("cancel finished: %d, want 409", rec.Code)
	}
}

func TestRuns_CancelQueuedNeverStarts(t *testing.T) {
	s, fe := launchServer(t)
	id1, _ := launchRun(t, s, "demo", map[string]any{"gpu": "1"})
	waitStart(t, fe)
	id2, _ := launchRun(t, s, "demo", map[string]any{"gpu": "1"})

	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/runs/"+id2+"/cancel", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel queued: %d %s", rec.Code, rec.Body.String())
	}
	waitState(t, s, id2, StateCancelled)
	fe.chanFor(id1) <- true
	waitState(t, s, id1, StateSucceeded)
	// id2 must never have started.
	select {
	case got := <-fe.started:
		t.Fatalf("cancelled queued run started: %s", got)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRuns_HistoryEndpoints(t *testing.T) {
	s, fe := launchServer(t)
	createProject(t, s, "other")
	id1, _ := launchRun(t, s, "demo", map[string]any{"gpu": "1", "keep_tmp": true})
	waitStart(t, fe)
	fe.chanFor(id1) <- true
	waitState(t, s, id1, StateSucceeded)
	id2, _ := launchRun(t, s, "other", map[string]any{"gpu": "1", "only": ""})
	waitStart(t, fe)
	fe.chanFor(id2) <- false
	waitState(t, s, id2, StateFailed)

	// Newest first, filterable.
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/runs", nil)
	var list []Run
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != id2 {
		t.Fatalf("list order wrong: %+v", list)
	}
	rec = doJSON(t, s.Handler(), http.MethodGet, "/api/runs?project=demo", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != id1 || !list[0].KeepTmp {
		t.Fatalf("filtered list wrong: %+v", list)
	}

	// Detail by id.
	rec = doJSON(t, s.Handler(), http.MethodGet, "/api/runs/"+id1, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get run: %d", rec.Code)
	}
	var detail struct {
		Run Run `json:"run"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Run.State != StateSucceeded || detail.Run.Artifact == "" {
		t.Errorf("detail wrong: %+v", detail.Run)
	}
	rec = doJSON(t, s.Handler(), http.MethodGet, "/api/runs/run-nope", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown run: %d, want 404", rec.Code)
	}
}

func TestRuns_ProjectBusyBlocksDelete(t *testing.T) {
	s, fe := launchServer(t)
	id1, _ := launchRun(t, s, "demo", map[string]any{"gpu": "1"})
	waitStart(t, fe)

	rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/projects/demo", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete while running: %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), id1) {
		t.Errorf("conflict should name run %s: %s", id1, rec.Body.String())
	}
	fe.chanFor(id1) <- true
	waitState(t, s, id1, StateSucceeded)
}

func expectNoStart(t *testing.T, fe *fakeExec, what string) {
	t.Helper()
	select {
	case got := <-fe.started:
		t.Fatalf("%s: run %s started but should wait", what, got)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRuns_MultiGPURunWaitsForABusyLane(t *testing.T) {
	s, fe := launchServer(t)

	idBusy, _ := launchRun(t, s, "demo", map[string]any{"gpu": "1"})
	waitStart(t, fe)
	idMulti, pos := launchRun(t, s, "demo", map[string]any{"gpu": "0,1"})
	if pos != 1 {
		t.Errorf("multi-GPU run queue position = %d, want 1", pos)
	}
	expectNoStart(t, fe, "GPU 0,1 while GPU 1 is busy")

	fe.chanFor(idBusy) <- true
	waitState(t, s, idBusy, StateSucceeded)
	if got := waitStart(t, fe); got != idMulti {
		t.Fatalf("multi-GPU run should start once both lanes are free, got %s", got)
	}
	fe.chanFor(idMulti) <- true
	waitState(t, s, idMulti, StateSucceeded)
}

func TestRuns_MultiGPURunOccupiesAllItsLanes(t *testing.T) {
	s, fe := launchServer(t)

	idMulti, _ := launchRun(t, s, "demo", map[string]any{"gpu": "0,1"})
	waitStart(t, fe)
	// Runs on either of its GPUs queue behind it, and a different GPU is free.
	id0, pos0 := launchRun(t, s, "demo", map[string]any{"gpu": "0"})
	id1, pos1 := launchRun(t, s, "demo", map[string]any{"gpu": "1"})
	id2, pos2 := launchRun(t, s, "demo", map[string]any{"gpu": "2"})
	if pos0 != 1 || pos1 != 1 {
		t.Errorf("queue positions on its lanes = %d, %d, want 1, 1", pos0, pos1)
	}
	if pos2 != 0 || waitStart(t, fe) != id2 {
		t.Errorf("a run on a free GPU must start at once (pos=%d)", pos2)
	}
	expectNoStart(t, fe, "runs on GPUs 0 and 1")

	fe.chanFor(idMulti) <- true
	waitState(t, s, idMulti, StateSucceeded)
	got := map[string]bool{waitStart(t, fe): true, waitStart(t, fe): true}
	if !got[id0] || !got[id1] {
		t.Fatalf("both single-GPU runs should start once the multi-GPU run ends, got %v", got)
	}
	for _, id := range []string{id0, id1, id2} {
		fe.chanFor(id) <- true
		waitState(t, s, id, StateSucceeded)
	}
}

func TestRuns_SingleGPURunDoesNotOvertakeAnEarlierMultiGPURun(t *testing.T) {
	s, fe := launchServer(t)

	idBusy, _ := launchRun(t, s, "demo", map[string]any{"gpu": "1"})
	waitStart(t, fe)
	idMulti, _ := launchRun(t, s, "demo", map[string]any{"gpu": "0,1"})
	idLate, posLate := launchRun(t, s, "demo", map[string]any{"gpu": "0"})
	if posLate != 2 {
		t.Errorf("run behind the multi-GPU run has position %d, want 2", posLate)
	}
	// GPU 0 is free, but the earlier 0,1 run is waiting on it: FIFO per lane.
	expectNoStart(t, fe, "GPU 0 run behind a waiting 0,1 run")

	fe.chanFor(idBusy) <- true
	waitState(t, s, idBusy, StateSucceeded)
	if got := waitStart(t, fe); got != idMulti {
		t.Fatalf("expected the 0,1 run first, got %s", got)
	}
	fe.chanFor(idMulti) <- true
	waitState(t, s, idMulti, StateSucceeded)
	if got := waitStart(t, fe); got != idLate {
		t.Fatalf("expected the GPU 0 run last, got %s", got)
	}
	fe.chanFor(idLate) <- true
	waitState(t, s, idLate, StateSucceeded)
}

func TestRuns_CancellingAQueuedMultiGPURunReleasesTheOnesBehindIt(t *testing.T) {
	s, fe := launchServer(t)

	idBusy, _ := launchRun(t, s, "demo", map[string]any{"gpu": "1"})
	waitStart(t, fe)
	idMulti, _ := launchRun(t, s, "demo", map[string]any{"gpu": "0,1"})
	idLate, _ := launchRun(t, s, "demo", map[string]any{"gpu": "0"})
	expectNoStart(t, fe, "GPU 0 run behind the 0,1 run")

	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/runs/"+idMulti+"/cancel", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body.String())
	}
	waitState(t, s, idMulti, StateCancelled)
	if got := waitStart(t, fe); got != idLate {
		t.Fatalf("the GPU 0 run should start once the 0,1 run is cancelled, got %s", got)
	}
	fe.chanFor(idBusy) <- true
	fe.chanFor(idLate) <- true
	waitState(t, s, idBusy, StateSucceeded)
	waitState(t, s, idLate, StateSucceeded)
}

func TestRuns_InvalidGPUListIsRejectedAtLaunch(t *testing.T) {
	s, _ := launchServer(t)
	for _, gpu := range []string{"0,0", "0,x", "-1"} {
		rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/demo/runs", map[string]any{"gpu": gpu})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("gpu %q: status %d, want 422: %s", gpu, rec.Code, rec.Body.String())
		}
	}
	runs, err := s.rm.hist.listRuns("", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Errorf("a run was recorded for an invalid GPU list: %+v", runs)
	}
}

// twoCardsForRuns makes the environment report two cards, the first (the desktop's) with the less memory.
func twoCardsForRuns(s *testServer) {
	s.look = func(name string) (string, error) { return "/fake/" + name, nil }
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if inv.Name == "nvidia-smi" && strings.Contains(inv.Args[0], "memory.free") {
			return runner.ScriptedResult{Stdout: []byte("0, NVIDIA GeForce RTX 4070, 11868, 12282\n1, NVIDIA GeForce RTX 5060 Ti, 15888, 16311\n")}
		}
		return runner.ScriptedResult{Stdout: []byte("")}
	})
	s.envRunner = fr
}

func TestLaunch_WithoutAGPUUsesTheCardWithMostMemory(t *testing.T) {
	s, _ := launchServer(t)
	twoCardsForRuns(s)
	id, _ := launchRun(t, s, "demo", map[string]any{})
	if got := waitState(t, s, id, StateRunning).GPU; got != "1" {
		t.Errorf("run GPU = %q, want the 16 GB card (1): sd would otherwise use card 0 and a 14 GB model does not fit in 12 GB", got)
	}
	// a chosen GPU is respected, and so is an explicit list
	id0, _ := launchRun(t, s, "demo", map[string]any{"gpu": "0"})
	if got := waitState(t, s, id0, StateRunning).GPU; got != "0" {
		t.Errorf("chosen GPU = %q", got)
	}
}

func TestLaunch_WithOneOrNoReadableCardNothingIsPinned(t *testing.T) {
	s, _ := launchServer(t)
	s.look = func(name string) (string, error) { return "", fmt.Errorf("not found") } // no nvidia-smi
	id, _ := launchRun(t, s, "demo", map[string]any{})
	if got := waitState(t, s, id, StateRunning).GPU; got != "" {
		t.Errorf("run GPU = %q, want none pinned", got)
	}
}

// musicProjectForRuns writes a music project with the given songs next to the demo one.
func musicProjectForRuns(t *testing.T, s *testServer, id string, songs ...string) {
	t.Helper()
	createProject(t, s, id)
	y := "project: " + id + "\noutput:\n  mode: music\ntracks:\n"
	for _, n := range songs {
		y += "  - id: " + n + "\n    prompt: lofi\n"
	}
	if err := s.SaveProjectDefinition(id, []byte(y), "save"); err != nil {
		t.Fatal(err)
	}
	models := filepath.Join(filepath.Dir(s.ProjectConfigPath(id)), "models", "music")
	if err := os.MkdirAll(models, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{config.DefaultMusicLM, config.DefaultMusicSynth, "Qwen3-Embedding-0.6B-Q8_0.gguf", "vae-BF16.gguf"} {
		if err := os.WriteFile(filepath.Join(models, f), []byte("stub"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLaunch_AMusicProjectWithSeveralSongsUsesEveryCardThatFitsIt(t *testing.T) {
	s, _ := launchServer(t)
	twoCardsForRuns(s)
	musicProjectForRuns(t, s, "songs", "a", "b")
	id, _ := launchRun(t, s, "songs", map[string]any{})
	if got := waitState(t, s, id, StateRunning).GPU; got != "0,1" {
		t.Errorf("GPU = %q, want both cards (0,1) so two songs are made at the same time", got)
	}
	// one song only (or a run restricted to one): the card with the most memory, as always
	id1, _ := launchRun(t, s, "songs", map[string]any{"only": "a"})
	if got := waitState(t, s, id1, StateQueued).GPU; got != "1" {
		t.Errorf("a single song: GPU = %q, want 1", got)
	}
	// an explicit choice is respected
	idX, _ := launchRun(t, s, "songs", map[string]any{"gpu": "1"})
	if got := waitState(t, s, idX, StateQueued).GPU; got != "1" {
		t.Errorf("chosen GPU = %q", got)
	}
}

func TestLaunch_AMusicProjectKeepsOneCardWhenTheOtherIsTooSmall(t *testing.T) {
	s, _ := launchServer(t)
	s.look = func(name string) (string, error) { return "/fake/" + name, nil }
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		return runner.ScriptedResult{Stdout: []byte("0, GTX 1650, 3900, 4096\n1, NVIDIA GeForce RTX 5060 Ti, 15888, 16311\n")}
	})
	s.envRunner = fr
	musicProjectForRuns(t, s, "songs", "a", "b")
	id, _ := launchRun(t, s, "songs", map[string]any{})
	if got := waitState(t, s, id, StateRunning).GPU; got != "1" {
		t.Errorf("GPU = %q, want only the card that fits", got)
	}
}

func TestRuns_OverlapIsKeptOnTheRunAndOutsideItsRangeRefused(t *testing.T) {
	s, fe := launchServer(t)
	id, _ := launchRun(t, s, "demo", map[string]any{"overlap": 2})
	waitStart(t, fe)
	rec, err := s.rm.hist.getRun(id)
	if err != nil || rec.Overlap != 2 {
		t.Fatalf("run = %+v (%v), want overlap 2", rec, err)
	}
	fe.chanFor(id) <- true
	for _, bad := range []int{-1, maxOverlap + 1} {
		if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/demo/runs", map[string]any{"overlap": bad}); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("overlap %d: status %d, want 422", bad, rec.Code)
		}
	}
}

func TestRunLaunch_NoBodyIsARunWithTheDefaultsAndABrokenOneIsA400(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "nobody")
	h := s.Handler()
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/projects/nobody/runs", strings.NewReader(body)))
		return rec
	}
	if rec := post(""); rec.Code == http.StatusBadRequest && strings.Contains(rec.Body.String(), "invalid JSON body") {
		t.Fatalf("an empty body must not be refused as invalid JSON: %d %s", rec.Code, rec.Body)
	}
	if rec := post("{nope"); rec.Code != http.StatusBadRequest {
		t.Errorf("broken body: %d %s", rec.Code, rec.Body)
	}
}

func TestRuns_AProjectCannotQueueMoreThanItsShare(t *testing.T) {
	s, fe := launchServer(t)
	id1, _ := launchRun(t, s, "demo", map[string]any{"gpu": "1"})
	waitStart(t, fe) // the first one runs; the next ones wait
	id2, _ := launchRun(t, s, "demo", map[string]any{"gpu": "1"})
	for i := 1; i < maxQueuedRunsPerProject; i++ {
		launchRun(t, s, "demo", map[string]any{"gpu": "1"})
	}

	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/demo/runs", map[string]any{"gpu": "1"})
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), api.CodeQueueFull) {
		t.Fatalf("one waiting run too many: status %d (%s), want 429 queue_full", rec.Code, rec.Body.String())
	}
	if n := len(s.rm.queue); n != maxQueuedRunsPerProject {
		t.Errorf("queue holds %d runs, want %d", n, maxQueuedRunsPerProject)
	}

	// Cancelling one frees its place.
	if c := doJSON(t, s.Handler(), http.MethodPost, "/api/runs/"+id2+"/cancel", nil); c.Code != http.StatusOK {
		t.Fatalf("cancel: %d", c.Code)
	}
	launchRun(t, s, "demo", map[string]any{"gpu": "1"})
	fe.chanFor(id1) <- true
	go func() {
		for range fe.started {
		}
	}()
}

func TestRuns_TheQueueHasAGlobalLimit(t *testing.T) {
	s, fe := launchServer(t)
	for i := 0; i < maxQueuedRuns/maxQueuedRunsPerProject+1; i++ {
		createProject(t, s, fmt.Sprintf("p%d", i))
	}
	launchRun(t, s, "demo", map[string]any{"gpu": "1"})
	waitStart(t, fe)
	go func() { // at shutdown every waiting run starts to be cancelled: keep the seam from blocking on its small buffer
		for range fe.started {
		}
	}()
	queued := 0
	var refused *httptest.ResponseRecorder
	for i := 0; queued < maxQueuedRuns+1 && refused == nil; i++ {
		rec := doJSON(t, s.Handler(), http.MethodPost, fmt.Sprintf("/api/projects/p%d/runs", i%(maxQueuedRuns/maxQueuedRunsPerProject+1)), map[string]any{"gpu": "1"})
		if rec.Code == http.StatusAccepted {
			queued++
			continue
		}
		refused = rec
	}
	if refused == nil {
		t.Fatal("the queue never filled up")
	}
	if refused.Code != http.StatusTooManyRequests || queued != maxQueuedRuns {
		t.Fatalf("refused with %d after %d waiting runs (%s), want 429 after %d", refused.Code, queued, refused.Body.String(), maxQueuedRuns)
	}
}

// A run gets the saved definition: whatever the file held before, the scenes.yaml of its folder is the saved text when the run starts.
func TestRuns_ARunGetsTheSavedDefinitionAndItsPages(t *testing.T) {
	s, _ := launchServer(t)
	was, _ := s.ProjectDefinition("demo")
	saved := "# saved\n" + string(was)
	if err := s.SaveProjectDefinition("demo", []byte(saved), "save"); err != nil {
		t.Fatal(err)
	}
	if err := s.Projects().PutPage("demo", "gfx", false, "<p>stored page</p>", nil); err != nil {
		t.Fatal(err)
	}
	// someone edited the file by hand, and left a page the API does not have
	_ = os.WriteFile(s.ProjectConfigPath("demo"), []byte("project: by hand\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(s.ProjectDir("demo"), "motion"), 0o755)
	_ = os.WriteFile(filepath.Join(s.ProjectDir("demo"), "motion", "stale.html"), []byte("<p>stale</p>"), 0o644)

	var seen, page string
	var stale bool
	s.rm.execute = func(ctx context.Context, rn *run, em *events.Emitter) (string, error) {
		b, _ := os.ReadFile(s.ProjectConfigPath("demo"))
		seen = string(b)
		p, _ := os.ReadFile(filepath.Join(s.ProjectDir("demo"), "motion", "gfx.html"))
		page = string(p)
		_, err := os.Stat(filepath.Join(s.ProjectDir("demo"), "motion", "stale.html"))
		stale = err == nil
		em.PipelineStart()
		em.Done("out/x.mp4")
		return "out/x.mp4", nil
	}
	// launched directly: a request through the test handler would import the stale page as the page of the project (the importer takes files with no row)
	rec, _, lerr := s.Launch("demo", RunRequest{})
	if lerr != nil {
		t.Fatal(lerr)
	}
	waitState(t, s, rec.ID, StateSucceeded)
	if seen != saved || page != "<p>stored page</p>" || stale {
		t.Fatalf("the run saw %q, page %q, stale page present %v", seen, page, stale)
	}
	if vs, _ := s.Projects().Versions("demo"); len(vs) != 1 {
		t.Fatalf("only the save made a version, not the run: %+v", vs)
	}
}

// A run that fixes the definition: what the run leaves in the file is the definition afterwards and the text before is a version; a page the
// run wrote is stored too, and the size kept with the project follows.
func TestRuns_ARunThatFixesTheDefinitionLeavesTheFixedOneAndAVersion(t *testing.T) {
	s, _ := launchServer(t)
	before, _ := s.ProjectDefinition("demo")
	fixed := strings.Replace(string(before), "project:", "# fixed by the run\nproject:", 1)
	s.rm.execute = func(ctx context.Context, rn *run, em *events.Emitter) (string, error) {
		_ = os.WriteFile(s.ProjectConfigPath("demo"), []byte(fixed), 0o644)
		_ = os.MkdirAll(filepath.Join(s.ProjectDir("demo"), "motion"), 0o755)
		_ = os.WriteFile(filepath.Join(s.ProjectDir("demo"), "motion", "gfx.html"), []byte("<p>the model wrote it</p>"), 0o644)
		_ = os.MkdirAll(filepath.Join(s.ProjectDir("demo"), "out"), 0o755)
		_ = os.WriteFile(filepath.Join(s.ProjectDir("demo"), "out", "final.mp4"), make([]byte, 2048), 0o644)
		em.PipelineStart()
		em.Done("out/x.mp4")
		return "out/x.mp4", nil
	}
	id, _ := launchRun(t, s, "demo", nil)
	waitState(t, s, id, StateSucceeded)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) { // the run is finished before its ingest is: wait for the row to follow
		if row, _ := s.Projects().Get("demo"); string(row.Definition) == fixed {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	row, _ := s.Projects().Get("demo")
	vs, _ := s.Projects().Versions("demo")
	if string(row.Definition) != fixed || len(vs) != 1 || vs[0].Reason != "run" {
		t.Fatalf("definition %q, versions %+v", row.Definition, vs)
	}
	if v, _ := s.Projects().Version("demo", 1); string(v.Definition) != string(before) {
		t.Fatal("the version is the text before the run")
	}
	if html, _, _, found := s.Projects().Page("demo", "gfx", false); !found || html != "<p>the model wrote it</p>" {
		t.Fatalf("the page the run wrote is stored: %q %v", html, found)
	}
	if row.SizeBytes == nil || *row.SizeBytes < 2048 {
		t.Fatalf("the size kept with the project is the one after the run: %v", row.SizeBytes)
	}
}

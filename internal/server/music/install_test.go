package music

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/platform/settings"
)

// installFixture is a server whose docker is a fake and whose install runs fake programs: it records what was run, in order, and lets a test
// make a program fail or wait.
type installFixture struct {
	s       *testServer
	d       *dockerFake
	mu      sync.Mutex
	ran     []string
	failOn  string        // a program whose run fails (matched as a prefix of "name args")
	block   chan struct{} // when set, a program waits for it to be closed or its context to end
	freeB   uint64
	weights int
}

func newInstallFixture(t *testing.T) *installFixture {
	t.Helper()
	d := &dockerFake{daemon: true, noImage: true}
	f := &installFixture{s: backendServer(t, d), d: d, freeB: 500 << 30}
	f.s.mHandler.installs.seams = installSeams{
		exec: func(ctx context.Context, dir, name string, args []string, logf func(string)) error {
			cmd := name + " " + strings.Join(args, " ")
			f.mu.Lock()
			f.ran = append(f.ran, cmd)
			fail, block := f.failOn != "" && strings.HasPrefix(cmd, f.failOn), f.block
			f.mu.Unlock()
			logf("running " + cmd)
			if block != nil {
				select {
				case <-block:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if fail {
				return errors.New("boom")
			}
			if strings.Contains(cmd, "docker build") || strings.HasPrefix(cmd, "docker build") {
				d.mu.Lock()
				d.noImage = false
				d.mu.Unlock()
			}
			return nil
		},
		weights: func(ctx context.Context, h *Handler, j *installJob, engine, backend string) error {
			f.mu.Lock()
			f.weights++
			fail := f.failOn == "weights"
			f.ran = append(f.ran, "weights "+engine+" "+backend)
			f.mu.Unlock()
			if fail {
				return errors.New("no network")
			}
			return nil
		},
		free: func(string) (uint64, error) { f.mu.Lock(); defer f.mu.Unlock(); return f.freeB, nil },
	}
	return f
}

func (f *installFixture) post(t *testing.T, engine string, body map[string]any) (int, []byte) {
	t.Helper()
	rec := doJSON(t, f.s.Handler(), http.MethodPost, "/api/music/engines/"+engine+"/install", body)
	return rec.Code, rec.Body.Bytes()
}

func (f *installFixture) job(t *testing.T, id string) InstallJob {
	t.Helper()
	rec := doJSON(t, f.s.Handler(), http.MethodGet, "/api/music/installs/"+id, nil)
	var out struct {
		Install InstallJob `json:"install"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Install
}

func (f *installFixture) started(t *testing.T, body []byte) string {
	t.Helper()
	var out struct {
		Install InstallJob `json:"install"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Install.ID == "" {
		t.Fatalf("not a started install: %s", body)
	}
	return out.Install.ID
}

func (f *installFixture) commands() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.ran, "\n")
}

// Without `confirm` the answer is a plan: what is missing, how big, the licences and the disk. Nothing is run or changed.
func TestInstall_APlanSaysWhatWouldBeDownloadedAndRunsNothing(t *testing.T) {
	f := newInstallFixture(t)
	code, body := f.post(t, "diffrhythm2", map[string]any{"backend": "docker"})
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	var plan InstallPlan
	if err := json.Unmarshal(body, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Engine != "diffrhythm2" || plan.Backend != "docker" || !plan.ConfirmRequired {
		t.Errorf("%+v", plan)
	}
	if len(plan.Parts) != 2 || plan.Parts[0].Part != "image" || plan.Parts[1].Part != "weights" {
		t.Fatalf("the image and then the weights: %+v", plan.Parts)
	}
	if plan.Parts[0].NeedBytes != 14_000_000_000 || plan.TotalBytes != 14_000_000_000+9_500_000_000 {
		t.Errorf("sizes: %+v", plan)
	}
	if !strings.Contains(strings.Join(plan.Licenses, " "), "CC-BY-NC-4.0") {
		t.Errorf("the non-commercial licence of the style encoder is shown: %v", plan.Licenses)
	}
	if len(plan.Disk) == 0 || !plan.Disk[0].Sufficient || plan.Disk[0].NeedBytes <= 0 {
		t.Errorf("disk: %+v", plan.Disk)
	}
	if got := f.commands(); got != "" {
		t.Errorf("a plan runs nothing, ran: %s", got)
	}
	if rec := doJSON(t, f.s.Handler(), http.MethodGet, "/api/music/installs", nil); strings.Contains(rec.Body.String(), `"id"`) {
		t.Errorf("a plan makes no job: %s", rec.Body)
	}
}

func TestInstall_ConfirmRunsThePartsInOrderAndTheJobIsFollowed(t *testing.T) {
	f := newInstallFixture(t)
	code, body := f.post(t, "diffrhythm2", map[string]any{"backend": "docker", "confirm": true})
	if code != http.StatusAccepted {
		t.Fatalf("%d %s", code, body)
	}
	id := f.started(t, body)
	waitUntil(t, func() bool { return f.job(t, id).State != installRunning })
	j := f.job(t, id)
	if j.State != installDone || j.Error != "" || j.Parts[0].State != installDone || j.Parts[1].State != installDone || j.FinishedAt == nil {
		t.Fatalf("job: %+v", j)
	}
	want := "docker build -t veritmo/diffrhythm2:1 docker/diffrhythm2\nweights diffrhythm2 docker"
	if got := f.commands(); got != want {
		t.Errorf("the image is built first, then the weights:\n%s", got)
	}
	// the events replay what happened and end
	rec := doJSON(t, f.s.Handler(), http.MethodGet, "/api/music/installs/"+id+"/events", nil)
	if !strings.Contains(rec.Body.String(), "running docker build") || !strings.Contains(rec.Body.String(), "stream-end") {
		t.Errorf("events: %s", rec.Body)
	}
	if rec := doJSON(t, f.s.Handler(), http.MethodGet, "/api/music/installs", nil); !strings.Contains(rec.Body.String(), id) {
		t.Errorf("listed: %s", rec.Body)
	}
}

func TestInstall_RefusesWhenTheDiskIsShort(t *testing.T) {
	f := newInstallFixture(t)
	f.freeB = 3 << 30
	code, body := f.post(t, "diffrhythm2", map[string]any{"backend": "docker", "confirm": true})
	if code != http.StatusUnprocessableEntity || !strings.Contains(string(body), ReasonNoDisk) || !strings.Contains(string(body), "more are needed") {
		t.Errorf("%d %s", code, body)
	}
	if f.commands() != "" {
		t.Error("nothing runs when it does not fit")
	}
	// the plan still answers, and says the disk is not enough
	_, pb := f.post(t, "diffrhythm2", map[string]any{"backend": "docker"})
	var plan InstallPlan
	json.Unmarshal(pb, &plan)
	if len(plan.Disk) == 0 || plan.Disk[0].Sufficient {
		t.Errorf("the plan says it is not enough: %+v", plan.Disk)
	}
}

func TestInstall_OneAtATimeAndItCanBeCancelled(t *testing.T) {
	f := newInstallFixture(t)
	f.block = make(chan struct{})
	code, body := f.post(t, "diffrhythm2", map[string]any{"backend": "docker", "confirm": true})
	if code != http.StatusAccepted {
		t.Fatalf("%d %s", code, body)
	}
	id := f.started(t, body)
	waitUntil(t, func() bool { return f.job(t, id).Parts[0].State == "running" })
	if code, _ := f.post(t, "heartmula", map[string]any{"backend": "docker", "confirm": true}); code != http.StatusConflict {
		t.Errorf("another install while one runs: %d", code)
	}
	rec := doJSON(t, f.s.Handler(), http.MethodPost, "/api/music/installs/"+id+"/cancel", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body)
	}
	waitUntil(t, func() bool { return f.job(t, id).State != installRunning })
	if j := f.job(t, id); j.State != installCancelled || j.Parts[0].State != partCancelled || j.Parts[1].State != partPending {
		t.Errorf("cancelled: the part that ran is cancelled and the next one stays pending: %+v", j)
	}
	if strings.Contains(f.commands(), "weights") {
		t.Error("what comes after the cancelled part does not run")
	}
	if rec := doJSON(t, f.s.Handler(), http.MethodPost, "/api/music/installs/nope/cancel", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown job: %d", rec.Code)
	}
}

// An install that fails half way leaves the job failed with the part and the reason; repeating it does only what is still missing.
func TestInstall_AFailureHalfWayCanBeRepeated(t *testing.T) {
	f := newInstallFixture(t)
	f.failOn = "weights"
	code, body := f.post(t, "diffrhythm2", map[string]any{"backend": "docker", "confirm": true})
	if code != http.StatusAccepted {
		t.Fatalf("%d %s", code, body)
	}
	id := f.started(t, body)
	waitUntil(t, func() bool { return f.job(t, id).State != installRunning })
	j := f.job(t, id)
	if j.State != installFailed || !strings.Contains(j.Error, "weights") || j.Parts[0].State != installDone || j.Parts[1].State != installFailed {
		t.Fatalf("failed job: %+v", j)
	}
	f.mu.Lock()
	f.failOn, f.ran = "", nil
	f.mu.Unlock()
	code, pb := f.post(t, "diffrhythm2", map[string]any{"backend": "docker"})
	var plan InstallPlan
	json.Unmarshal(pb, &plan)
	if code != http.StatusOK || len(plan.Parts) != 1 || plan.Parts[0].Part != "weights" {
		t.Fatalf("the image is there now, only the weights are left: %d %+v", code, plan)
	}
	code, body = f.post(t, "diffrhythm2", map[string]any{"backend": "docker", "confirm": true})
	if code != http.StatusAccepted {
		t.Fatalf("%d %s", code, body)
	}
	id = f.started(t, body)
	waitUntil(t, func() bool { return f.job(t, id).State != installRunning })
	if j := f.job(t, id); j.State != installDone {
		t.Errorf("repeated: %+v", j)
	}
	if got := f.commands(); got != "weights diffrhythm2 docker" {
		t.Errorf("only the weights run again: %s", got)
	}
}

func TestInstall_RefusalsAndTheEnginesItDoesNotKnow(t *testing.T) {
	f := newInstallFixture(t)
	if code, _ := f.post(t, "clap", map[string]any{"backend": "docker"}); code != http.StatusNotFound {
		t.Errorf("LAION-CLAP has no container: %d", code)
	}
	if code, _ := f.post(t, "diffrhythm2", map[string]any{"backend": "docker", "parts": []string{"service"}}); code != http.StatusUnprocessableEntity {
		t.Errorf("a part that does not exist for that backend: %d", code)
	}
	if code, _ := f.post(t, "diffrhythm2", map[string]any{"backend": "podman"}); code != http.StatusUnprocessableEntity {
		t.Errorf("a backend that does not exist: %d", code)
	}
	// docker that does not answer cannot be installed by this: it is Docker itself
	f.d.mu.Lock()
	f.d.daemon = false
	f.d.mu.Unlock()
	code, body := f.post(t, "diffrhythm2", map[string]any{"backend": "docker"})
	if code != http.StatusUnprocessableEntity || !strings.Contains(string(body), musicsvc.ReasonNoDocker) {
		t.Errorf("no docker: %d %s", code, body)
	}
	f.d.mu.Lock()
	f.d.daemon = true
	f.d.mu.Unlock()
	// HeartMuLa has its weights (containerServer writes them) and the image is missing: asking for the weights alone changes nothing
	code, body = f.post(t, "heartmula", map[string]any{"backend": "docker", "parts": []string{"weights"}, "confirm": true})
	if code != http.StatusConflict {
		t.Errorf("nothing of what was asked is missing: %d %s", code, body)
	}
}

func TestInstall_RefusesWhileTheGeneratorRuns(t *testing.T) {
	f := newInstallFixture(t)
	f.d.mu.Lock()
	f.d.running, f.d.gpu = true, "1"
	f.d.mu.Unlock()
	if err := settings.SetMusicBackend("diffrhythm2", "docker"); err != nil {
		t.Fatal(err)
	}
	code, body := f.post(t, "diffrhythm2", map[string]any{"backend": "docker", "confirm": true})
	if code != http.StatusConflict || !strings.Contains(string(body), "running") {
		t.Errorf("the container runs: %d %s", code, body)
	}
}

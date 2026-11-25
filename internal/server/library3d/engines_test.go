package library3d

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"veritmo/internal/assets3d/generate"
	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/platform/runner"
)

// docker is a fake docker that answers the commands the service uses and remembers what it was asked.
type docker struct {
	mu      sync.Mutex
	image   bool
	label   string
	running bool
	calls   [][]string
	envs    [][]string
	fetch   func(args []string) error // what `docker run --rm ... fetch.py` does
	hold    chan struct{}             // when set, the fetch waits for it
}

func (d *docker) handle(inv runner.Invocation) runner.ScriptedResult {
	if inv.Name == "nvidia-smi" {
		return runner.ScriptedResult{Stdout: []byte("0, NVIDIA GeForce RTX 4070, 11000, 12282\n")}
	}
	d.mu.Lock()
	d.calls = append(d.calls, inv.Args)
	d.mu.Unlock()
	switch inv.Args[0] {
	case "info":
		return runner.ScriptedResult{Stdout: []byte(`{"nvidia":{},"runc":{}}`)}
	case "image":
		if !d.image {
			return runner.ScriptedResult{Err: errors.New("No such image")}
		}
		if len(inv.Args) > 2 && inv.Args[2] == "-f" {
			return runner.ScriptedResult{Stdout: []byte(d.label + "\n")}
		}
	case "run":
		if inv.Args[1] == "--rm" {
			if d.hold != nil {
				<-d.hold
			}
			if d.fetch != nil {
				if err := d.fetch(inv.Args); err != nil {
					return runner.ScriptedResult{Stdout: []byte("fetching\n"), Err: err}
				}
			}
			return runner.ScriptedResult{Stdout: []byte("fetching\ndone\n")}
		}
		d.mu.Lock()
		d.running = true
		d.mu.Unlock()
	case "rm":
		d.mu.Lock()
		d.running = false
		d.mu.Unlock()
	case "inspect":
		if !d.running {
			return runner.ScriptedResult{Err: errors.New("No such object")}
		}
		return runner.ScriptedResult{Stdout: []byte("running|2026-11-24T12:00:00Z|0\n")}
	}
	return runner.ScriptedResult{}
}

func (d *docker) ran(prefix ...string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.calls {
		if len(c) >= len(prefix) && strings.Join(c[:len(prefix)], " ") == strings.Join(prefix, " ") {
			return c
		}
	}
	return nil
}

func withService(t *testing.T, d *docker, key string) (*server, string) {
	s := newServer(t, nil)
	fr := runner.NewFakeRunner()
	fr.SetHandler(d.handle)
	dir := filepath.Join(t.TempDir(), "model3d")
	s.h.SetService(&generate.Service{Runner: fr, ModelsDir: dir, NoStart: true, HFKey: func() string { return key }})
	s.h.Acceptances = &fakeAcceptances{} // never the settings of the machine that runs the tests
	return s, dir
}

// fakeAcceptances keeps the acceptances in memory.
type fakeAcceptances struct {
	mu   sync.Mutex
	done map[string]string
}

func (f *fakeAcceptances) Accept(id, client string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.done == nil {
		f.done = map[string]string{}
	}
	f.done[id] = client
	return nil
}

func (f *fakeAcceptances) Accepted(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.done[id]
	return ok
}

func fillWeights(t *testing.T, dir, engine string) {
	for _, rel := range generate.Required(engine) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o644)
	}
}

type errBody struct {
	Error struct{ Code, Message, Reason string }
}

func bodyReason(t *testing.T, rec *httptest.ResponseRecorder) string {
	var e errBody
	json.Unmarshal(rec.Body.Bytes(), &e)
	return e.Error.Reason
}

func TestEnginesWithoutAServiceSayThereIsNone(t *testing.T) {
	s := newServer(t, nil)
	for _, p := range []string{"GET /api/models3d/engines", "POST /api/models3d/engines/triposr/start", "POST /api/models3d/engines/triposr/install"} {
		m, path, _ := strings.Cut(p, " ")
		if rec := s.do(m, path, nil); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: %d %s", p, rec.Code, rec.Body)
		}
	}
}

func TestEnginesListsBothWithTheirReason(t *testing.T) {
	d := &docker{image: true, label: "triposr"}
	s, dir := withService(t, d, "")
	rec := s.do(http.MethodGet, "/api/models3d/engines", nil)
	var out struct {
		Engines map[string]struct {
			Available bool
			Reason    string
			Detail    string
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if e := out.Engines["triposr"]; e.Available || e.Reason != "no_weights" {
		t.Errorf("triposr: %+v", e)
	}
	if e := out.Engines["sf3d"]; e.Available || e.Reason != generate.ReasonNotBuilt || !strings.Contains(e.Detail, "SF3D=1") {
		t.Errorf("sf3d: %+v", e)
	}
	fillWeights(t, dir, generate.EngineTripoSR)
	d.running = true
	rec = s.do(http.MethodGet, "/api/models3d/engines", nil)
	json.Unmarshal(rec.Body.Bytes(), &out)
	if e := out.Engines["triposr"]; !e.Available || e.Reason != "" {
		t.Errorf("running: %+v", e)
	}
}

func TestStartAndStopTheContainer(t *testing.T) {
	d := &docker{image: true, label: "triposr"}
	s, dir := withService(t, d, "")
	if rec := s.do(http.MethodPost, "/api/models3d/engines/dalle/start", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown engine: %d", rec.Code)
	}
	if rec := s.do(http.MethodPost, "/api/models3d/engines/triposr/start", nil); rec.Code != http.StatusConflict || bodyReason(t, rec) != "no_weights" {
		t.Errorf("without weights: %d %s", rec.Code, rec.Body)
	}
	if d.ran("run") != nil {
		t.Error("nothing is run without the weights")
	}
	fillWeights(t, dir, generate.EngineTripoSR)
	rec := s.do(http.MethodPost, "/api/models3d/engines/triposr/start", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"running"`) {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	if run := strings.Join(d.ran("run"), " "); !strings.Contains(run, "-v "+dir+":/ckpt:ro") || !strings.Contains(run, "127.0.0.1:8790:8790") {
		t.Errorf("docker run: %s", run)
	}
	if rec := s.do(http.MethodPost, "/api/models3d/engines/triposr/start", nil); rec.Code != http.StatusOK {
		t.Errorf("starting what runs is not an error: %d", rec.Code)
	}
	if rec := s.do(http.MethodPost, "/api/models3d/engines/triposr/stop", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"stopped"`) {
		t.Errorf("stop: %d %s", rec.Code, rec.Body)
	}
}

func TestInstallShowsThePlanThenRunsTheFetchOfTheImage(t *testing.T) {
	d := &docker{image: true, label: "triposr"}
	s, dir := withService(t, d, "")
	d.fetch = func(args []string) error { fillWeights(t, dir, generate.EngineTripoSR); return nil }
	rec := s.do(http.MethodPost, "/api/models3d/engines/triposr/install", nil)
	var plan struct {
		Plan InstallPlan
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &plan); err != nil || rec.Code != http.StatusOK || len(plan.Plan.Missing) != 4 || plan.Plan.Licence.Name != "MIT" || plan.Plan.Licence.Commercial != "yes" || plan.Plan.Gated || plan.Plan.Path != dir {
		t.Fatalf("plan: %d %s", rec.Code, rec.Body)
	}
	if d.ran("run", "--rm") != nil {
		t.Error("a plan runs nothing")
	}
	rec = s.do(http.MethodPost, "/api/models3d/engines/triposr/install", []byte(`{"confirm":true}`))
	var job struct {
		Install InstallJob
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil || rec.Code != http.StatusAccepted || job.Install.ID == "" {
		t.Fatalf("install: %d %s", rec.Code, rec.Body)
	}
	waitFor(t, func() bool {
		var j InstallJob
		json.Unmarshal(s.do(http.MethodGet, "/api/models3d/installs/"+job.Install.ID, nil).Body.Bytes(), &j)
		return j.State == "done"
	})
	run := strings.Join(d.ran("run", "--rm"), " ")
	for _, want := range []string{"-v " + dir + ":/ckpt", "veritmo/model3d:1", "/opt/fetch.py triposr"} {
		if !strings.Contains(run, want) {
			t.Errorf("fetch lacks %q: %s", want, run)
		}
	}
	if strings.Contains(run, "HF_TOKEN") {
		t.Error("TripoSR needs no key")
	}
	if rec := s.do(http.MethodPost, "/api/models3d/engines/triposr/install", nil); rec.Code != http.StatusConflict {
		t.Errorf("complete weights: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(http.MethodGet, "/api/models3d/installs/nope", nil); rec.Code != http.StatusNotFound {
		t.Errorf("%d", rec.Code)
	}
}

func TestInstallOfAGatedEngineNeedsTheKeyAndNeverPutsItInTheArguments(t *testing.T) {
	hf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusFound) }))
	defer hf.Close()
	d := &docker{image: true, label: "triposr,sf3d"}
	s, dir := withService(t, d, "")
	if rec := s.do(http.MethodPost, "/api/models3d/engines/sf3d/install", nil); rec.Code != http.StatusUnprocessableEntity || bodyReason(t, rec) != generate.ReasonKeyMissing {
		t.Errorf("no key: %d %s", rec.Code, rec.Body)
	}
	svc := s.h.Service()
	svc.HFKey, svc.HFBase = func() string { return "hf_secret_key" }, hf.URL
	d.fetch = func(args []string) error {
		for _, a := range args {
			if strings.Contains(a, "hf_secret_key") {
				t.Errorf("the key is in the arguments: %v", args)
			}
		}
		fillWeights(t, dir, generate.EngineSF3D)
		return nil
	}
	rec := s.do(http.MethodPost, "/api/models3d/engines/sf3d/install", []byte(`{"confirm":true,"accept_licence":"stability-community"}`))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var job struct{ Install InstallJob }
	json.Unmarshal(rec.Body.Bytes(), &job)
	waitFor(t, func() bool {
		var j InstallJob
		json.Unmarshal(s.do(http.MethodGet, "/api/models3d/installs/"+job.Install.ID, nil).Body.Bytes(), &j)
		return j.State == "done"
	})
	if run := strings.Join(d.ran("run", "--rm"), " "); !strings.Contains(run, "-e HF_TOKEN") || !strings.Contains(run, "fetch.py sf3d") {
		t.Errorf("%s", run)
	}
}

func TestInstallFailureAndCancel(t *testing.T) {
	d := &docker{image: true, label: "triposr"}
	d.fetch = func(args []string) error { return errors.New("exit status 1: network is unreachable") }
	s, _ := withService(t, d, "")
	rec := s.do(http.MethodPost, "/api/models3d/engines/triposr/install", []byte(`{"confirm":true}`))
	var job struct{ Install InstallJob }
	json.Unmarshal(rec.Body.Bytes(), &job)
	var j InstallJob
	waitFor(t, func() bool {
		json.Unmarshal(s.do(http.MethodGet, "/api/models3d/installs/"+job.Install.ID, nil).Body.Bytes(), &j)
		return j.State == "failed"
	})
	if !strings.Contains(j.Error, "network is unreachable") || j.FinishedAt == nil {
		t.Errorf("%+v", j)
	}
	// a finished install cannot be cancelled, and another may start
	if rec := s.do(http.MethodPost, "/api/models3d/installs/"+job.Install.ID+"/cancel", nil); rec.Code != http.StatusConflict {
		t.Errorf("cancel of a finished install: %d", rec.Code)
	}
	d.hold = make(chan struct{})
	d.fetch = nil
	rec = s.do(http.MethodPost, "/api/models3d/engines/triposr/install", []byte(`{"confirm":true}`))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("second install: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(http.MethodPost, "/api/models3d/engines/triposr/install", []byte(`{"confirm":true}`)); rec.Code != http.StatusConflict {
		t.Errorf("two at once: %d %s", rec.Code, rec.Body)
	}
	close(d.hold)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("it did not happen in time")
}

func TestAServiceElsewhereIsUsedAsItIsAndNeverManagedFromHere(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Write([]byte(`{"ready":true,"version":"1","busy":false,"queued":0,"missing":[]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer remote.Close()
	d := &docker{}
	s, _ := withService(t, d, "")
	s.h.Service().External.Host, s.h.Service().External.Consented = remote.URL, true
	rec := s.do(http.MethodGet, "/api/models3d/engines", nil)
	var out struct {
		Engines map[string]struct {
			Available bool
			Mode      string
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || !out.Engines["triposr"].Available || out.Engines["triposr"].Mode != "external" {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
	for _, p := range []string{"start", "stop", "install"} {
		rec := s.do(http.MethodPost, "/api/models3d/engines/triposr/"+p, []byte(`{"confirm":true}`))
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "MODEL3D_HOST") {
			t.Errorf("%s: %d %s", p, rec.Code, rec.Body)
		}
	}
	if len(d.calls) != 0 {
		t.Errorf("docker is not touched: %v", d.calls)
	}
}

// withDotnet makes the service one that has the .NET backend (the default) with a host whose `dotnet` is a program that exists.
func withDotnet(t *testing.T, s *server, published bool) {
	t.Helper()
	svc := s.h.Service()
	root := t.TempDir()
	svc.Services = &musicsvc.ServiceHost{Dir: filepath.Join(root, "services"), StateDir: filepath.Join(root, "state"), BinDir: root, Dotnet: "sh"}
	if published {
		for _, e := range generate.Engines() {
			os.MkdirAll(filepath.Join(svc.Services.Dir, generate.Spec(e).Service.Dir), 0o755)
			os.WriteFile(svc.Services.DLLPath(generate.Spec(e).Service), []byte("x"), 0o644)
		}
	}
}

func planOf(t *testing.T, rec *httptest.ResponseRecorder) InstallPlan {
	t.Helper()
	var out struct{ Plan InstallPlan }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("plan: %d %s", rec.Code, rec.Body)
	}
	return out.Plan
}

// The .NET service is the default way: its install is the service and the weights, and needs no Docker or image.
func TestInstallForTheDotnetServicePublishesItWithoutDocker(t *testing.T) {
	d := &docker{} // no image, and the fake docker is never asked for the .NET backend
	s, dir := withService(t, d, "")
	withDotnet(t, s, false)
	fillWeights(t, dir, generate.EngineTripoSR)
	plan := planOf(t, s.do(http.MethodPost, "/api/models3d/engines/triposr/install", nil))
	if plan.Backend != "dotnet" || strings.Join(plan.Parts, ",") != "service" || len(plan.Missing) != 0 {
		t.Fatalf("plan: %+v", plan)
	}
	rec := s.do(http.MethodPost, "/api/models3d/engines/triposr/install", []byte(`{"confirm":true}`))
	var job struct{ Install InstallJob }
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil || rec.Code != http.StatusAccepted {
		t.Fatalf("install: %d %s", rec.Code, rec.Body)
	}
	waitFor(t, func() bool {
		var j InstallJob
		json.Unmarshal(s.do(http.MethodGet, "/api/models3d/installs/"+job.Install.ID, nil).Body.Bytes(), &j)
		return j.State == "done"
	})
	if c := d.ran("run", "--rm"); c != nil {
		t.Errorf("the weights of the .NET service are not fetched with Docker: %v", c)
	}
}

// The container is the other way: it can be installed before it is chosen, by naming the backend; its image is built with docker build.
func TestInstallForTheContainerBuildsTheImageThenFetchesTheWeights(t *testing.T) {
	d := &docker{} // no image yet
	s, dir := withService(t, d, "")
	withDotnet(t, s, true)
	d.fetch = func(args []string) error { fillWeights(t, dir, generate.EngineTripoSR); return nil }
	plan := planOf(t, s.do(http.MethodPost, "/api/models3d/engines/triposr/install", []byte(`{"backend":"docker"}`)))
	if plan.Backend != "docker" || strings.Join(plan.Parts, ",") != "image,weights" {
		t.Fatalf("plan: %+v", plan)
	}
	if only := planOf(t, s.do(http.MethodPost, "/api/models3d/engines/triposr/install", []byte(`{"backend":"docker","parts":["weights"]}`))); strings.Join(only.Parts, ",") != "weights" {
		t.Errorf("parts asked for: %+v", only)
	}
	if rec := s.do(http.MethodPost, "/api/models3d/engines/triposr/install", []byte(`{"backend":"docker","parts":["service"]}`)); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a part that the backend does not have: %d", rec.Code)
	}
	rec := s.do(http.MethodPost, "/api/models3d/engines/triposr/install", []byte(`{"backend":"docker","confirm":true}`))
	var job struct{ Install InstallJob }
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil || rec.Code != http.StatusAccepted {
		t.Fatalf("install: %d %s", rec.Code, rec.Body)
	}
	waitFor(t, func() bool {
		var j InstallJob
		json.Unmarshal(s.do(http.MethodGet, "/api/models3d/installs/"+job.Install.ID, nil).Body.Bytes(), &j)
		return j.State == "done"
	})
	if build := strings.Join(d.ran("build"), " "); !strings.Contains(build, "-t veritmo/model3d:1") || !strings.HasSuffix(build, "docker/model3d") {
		t.Errorf("docker build: %q", build)
	}
	if d.ran("run", "--rm") == nil {
		t.Error("the weights were not fetched in the image")
	}
}

// Stable Fast 3D as a service: its install fetches the weights with the saved key, the grid that only the service reads included; without the key it
// says so before anything is downloaded.
func TestInstallOfStableFastForTheDotnetBackendNeedsTheKeyForItsGatedWeights(t *testing.T) {
	s, dir := withService(t, &docker{image: true, label: "triposr,sf3d"}, "")
	withDotnet(t, s, true)
	// the weights of the container are there; only the grid (a public file) is missing: no key is needed for it
	fillWeights(t, dir, generate.EngineSF3D)
	plan := planOf(t, s.do(http.MethodPost, "/api/models3d/engines/sf3d/install", []byte(`{"backend":"dotnet"}`)))
	if plan.Backend != "dotnet" || strings.Join(plan.Parts, ",") != "weights" || strings.Join(plan.Missing, ",") != "sf3d/160_tets.npz" {
		t.Fatalf("plan: %+v", plan)
	}
	// the plan carries the licence with its conditions, for the client to show before the person confirms
	if l := plan.Licence; l.ID != "stability-community" || !strings.Contains(l.Name, "Stability AI Community License") || l.URL == "" || l.Commercial != "conditions" || l.Summary == "" {
		t.Errorf("licence of the plan: %+v", l)
	}
	// a gated file missing and no key saved
	os.Remove(filepath.Join(dir, "sf3d/model.safetensors"))
	rec := s.do(http.MethodPost, "/api/models3d/engines/sf3d/install", []byte(`{"backend":"dotnet"}`))
	if rec.Code != http.StatusUnprocessableEntity || bodyReason(t, rec) != generate.ReasonKeyMissing {
		t.Errorf("without a key: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(http.MethodPost, "/api/models3d/engines/triposr/install", []byte(`{"backend":"podman"}`)); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("another backend: %d", rec.Code)
	}
}

// The licence of Stable Fast 3D has conditions: confirming the install is not accepting it. The request has to carry the acceptance, the server never gives it,
// and it records each one it receives.
func TestInstallOfStableFastNeedsTheExplicitAcceptanceOfItsLicence(t *testing.T) {
	hf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusFound) }))
	defer hf.Close()
	d := &docker{image: true, label: "triposr,sf3d"}
	s, dir := withService(t, d, "")
	svc := s.h.Service()
	svc.HFKey, svc.HFBase = func() string { return "hf_key" }, hf.URL
	d.fetch = func(args []string) error { fillWeights(t, dir, generate.EngineSF3D); return nil }
	acc := s.h.Acceptances.(*fakeAcceptances)

	// the plan says what the licence is and that nothing was accepted
	plan := planOf(t, s.do(http.MethodPost, "/api/models3d/engines/sf3d/install", nil))
	if plan.Licence.ID != "stability-community" || plan.Licence.Accepted == nil || *plan.Licence.Accepted {
		t.Fatalf("licence of the plan: %+v", plan.Licence)
	}

	// confirming alone, or with another id, is refused with the licence named, and nothing runs or is recorded
	for _, body := range []string{`{"confirm":true}`, `{"confirm":true,"accept_licence":"mit"}`, `{"confirm":true,"accept_licence":true}`} {
		rec := s.do(http.MethodPost, "/api/models3d/engines/sf3d/install", []byte(body))
		if body == `{"confirm":true,"accept_licence":true}` {
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s: a wrong type is an invalid body: %d", body, rec.Code)
			}
			continue
		}
		if rec.Code != http.StatusBadRequest || bodyReason(t, rec) != ReasonLicenceAcceptanceRequired {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
		var refusal struct{ Error struct{ Message string } }
		json.Unmarshal(rec.Body.Bytes(), &refusal)
		for _, want := range []string{"Stability AI Community License", "https://stability.ai/community-license-agreement", `accept_licence: "stability-community"`, "never accepts a licence for anyone"} {
			if !strings.Contains(refusal.Error.Message, want) {
				t.Errorf("%s: the refusal lacks %q: %s", body, want, refusal.Error.Message)
			}
		}
	}
	if d.ran("run", "--rm") != nil || len(acc.done) != 0 {
		t.Fatalf("a refusal runs and records nothing: %v %v", d.ran("run", "--rm"), acc.done)
	}

	// with the acceptance it starts, and the acceptance is recorded with the client that sent it
	rec := s.do(http.MethodPost, "/api/models3d/engines/sf3d/install", []byte(`{"confirm":true,"accept_licence":"stability-community","client":"model-studio"}`))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if acc.done["stability-community"] != "model-studio" {
		t.Errorf("recorded: %v", acc.done)
	}
	var job struct{ Install InstallJob }
	json.Unmarshal(rec.Body.Bytes(), &job)
	waitFor(t, func() bool {
		var j InstallJob
		json.Unmarshal(s.do(http.MethodGet, "/api/models3d/installs/"+job.Install.ID, nil).Body.Bytes(), &j)
		return j.State == "done"
	})
	// and the plan now says so
	d.fetch = nil
	os.Remove(filepath.Join(dir, "sf3d/model.safetensors"))
	if plan := planOf(t, s.do(http.MethodPost, "/api/models3d/engines/sf3d/install", nil)); plan.Licence.Accepted == nil || !*plan.Licence.Accepted {
		t.Errorf("the plan after the acceptance: %+v", plan.Licence)
	}
}

// TripoSR is MIT: its plan carries no acceptance and its install needs none.
func TestInstallOfTripoSRNeedsNoAcceptance(t *testing.T) {
	d := &docker{image: true, label: "triposr"}
	s, dir := withService(t, d, "")
	d.fetch = func(args []string) error { fillWeights(t, dir, generate.EngineTripoSR); return nil }
	plan := planOf(t, s.do(http.MethodPost, "/api/models3d/engines/triposr/install", nil))
	if plan.Licence.ID != "" || plan.Licence.Accepted != nil {
		t.Errorf("TripoSR's plan: %+v", plan.Licence)
	}
	if rec := s.do(http.MethodPost, "/api/models3d/engines/triposr/install", []byte(`{"confirm":true}`)); rec.Code != http.StatusAccepted {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}

// The build of the .NET service is run with the acceptance the request carried, which is the one thing that lets scripts/get-services.sh build it.
func TestInstallOfStableFastPassesTheAcceptanceToTheServiceBuild(t *testing.T) {
	hf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusFound) }))
	defer hf.Close()
	d := &docker{image: true, label: "triposr,sf3d"}
	s, _ := withService(t, d, "hf_key")
	s.h.Service().HFBase = hf.URL
	withDotnet(t, s, false)
	rec := s.do(http.MethodPost, "/api/models3d/engines/sf3d/install", []byte(`{"backend":"dotnet","parts":["service"],"confirm":true,"accept_licence":"stability-community"}`))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var job struct{ Install InstallJob }
	json.Unmarshal(rec.Body.Bytes(), &job)
	waitFor(t, func() bool {
		var j InstallJob
		json.Unmarshal(s.do(http.MethodGet, "/api/models3d/installs/"+job.Install.ID, nil).Body.Bytes(), &j)
		return j.State != "running"
	})
	var build string
	for _, c := range s.h.Service().Runner.(*runner.FakeRunner).Calls() {
		if c.Name == "bash" {
			build = strings.Join(c.Args, " ")
		}
	}
	if !strings.Contains(build, "--only stable-fast-3d") || !strings.Contains(build, "--accept-licence stability-community") {
		t.Errorf("the build runs as %q", build)
	}
}

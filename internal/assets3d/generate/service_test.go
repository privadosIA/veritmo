package generate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/platform/runner"
)

// fakeDocker answers like the real docker for what the availability asks, and says which engines the image was built with.
type fakeDocker struct {
	daemon, image, nvidia bool
	label                 string
	running               bool
	calls                 [][]string
}

func (d *fakeDocker) handle(inv runner.Invocation) runner.ScriptedResult {
	switch inv.Name {
	case "nvidia-smi":
		return runner.ScriptedResult{Stdout: []byte("1, NVIDIA GeForce RTX 5060 Ti, 15900, 16311\n0, NVIDIA GeForce RTX 4070, 11000, 12282\n")}
	case "docker":
	default:
		return runner.ScriptedResult{Err: errors.New("unexpected program " + inv.Name)}
	}
	d.calls = append(d.calls, inv.Args)
	if !d.daemon {
		return runner.ScriptedResult{Err: errors.New("Cannot connect to the Docker daemon")}
	}
	switch inv.Args[0] {
	case "info":
		if d.nvidia {
			return runner.ScriptedResult{Stdout: []byte(`{"nvidia":{},"runc":{}}`)}
		}
		return runner.ScriptedResult{Stdout: []byte(`{"runc":{}}`)}
	case "image":
		if !d.image {
			return runner.ScriptedResult{Err: errors.New("No such image")}
		}
		if len(inv.Args) > 2 && inv.Args[1] == "inspect" && inv.Args[2] == "-f" {
			return runner.ScriptedResult{Stdout: []byte(d.label + "\n")}
		}
	case "run":
		d.running = true
	case "rm":
		d.running = false
	case "inspect":
		if !d.running {
			return runner.ScriptedResult{Err: errors.New("No such object")}
		}
		return runner.ScriptedResult{Stdout: []byte("running|2026-11-24T12:00:00Z|1\n")}
	}
	return runner.ScriptedResult{}
}

func service(t *testing.T, d *fakeDocker, key string, hf *httptest.Server) (*Service, string) {
	fr := runner.NewFakeRunner()
	fr.SetHandler(d.handle)
	dir := t.TempDir()
	s := &Service{Runner: fr, ModelsDir: dir, HFKey: func() string { return key }, NoStart: true}
	if hf != nil {
		s.HFBase = hf.URL
	}
	return s, dir
}

func fakeHF(t *testing.T, code int, seen *string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = r.Method + " " + r.URL.Path + " " + r.Header.Get("Authorization")
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func fill(t *testing.T, dir string, engine string) {
	for _, rel := range Required(engine) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o644)
	}
}

func up() *fakeDocker {
	return &fakeDocker{daemon: true, image: true, nvidia: true, label: "triposr"}
}

func TestAvailabilityInTheOrderAPersonFixesIt(t *testing.T) {
	ctx := context.Background()
	d := up()
	d.daemon = false
	s, dir := service(t, d, "", nil)
	if a := s.Availability(ctx, EngineTripoSR); a.Reason != musicsvc.ReasonNoDocker || a.Available {
		t.Errorf("no docker: %+v", a)
	}
	d.daemon, d.image = true, false
	if a := s.Availability(ctx, EngineTripoSR); a.Reason != musicsvc.ReasonNoImage {
		t.Errorf("no image: %+v", a)
	}
	d.image, d.nvidia = true, false
	if a := s.Availability(ctx, EngineTripoSR); a.Reason != musicsvc.ReasonNoGPURuntime {
		t.Errorf("no runtime: %+v", a)
	}
	d.nvidia = true
	a := s.Availability(ctx, EngineTripoSR)
	if a.Reason != musicsvc.ReasonNoWeights || len(a.Missing) != 4 {
		t.Errorf("no weights: %+v", a)
	}
	fill(t, dir, EngineTripoSR)
	if a := s.Availability(ctx, EngineTripoSR); a.Reason != musicsvc.ReasonNotRunning || a.Available || a.Container == nil {
		t.Errorf("everything there but stopped: %+v", a)
	}
	d.running = true
	if a := s.Availability(ctx, EngineTripoSR); !a.Available || a.Reason != "" {
		t.Errorf("running: %+v", a)
	}
	if a := s.Availability(ctx, "nope"); a.Reason != "unknown_engine" {
		t.Errorf("%+v", a)
	}
}

func TestAnEngineTheImageWasBuiltWithoutIsNotBuilt(t *testing.T) {
	d := up()
	s, dir := service(t, d, "", nil)
	fill(t, dir, EngineSF3D)
	a := s.Availability(context.Background(), EngineSF3D)
	if a.Reason != ReasonNotBuilt || a.Available || !strings.Contains(a.Detail, "SF3D=1") {
		t.Errorf("%+v", a)
	}
	d.label = "triposr,sf3d"
	if a := s.Availability(context.Background(), EngineSF3D); a.Reason == ReasonNotBuilt {
		t.Errorf("built with it: %+v", a)
	}
	d.label = "" // an image with no label is the one with the default engine only
	if a := s.Availability(context.Background(), EngineTripoSR); a.Reason == ReasonNotBuilt {
		t.Errorf("%+v", a)
	}
}

func TestStableFastNeedsAKeyAndTheLicenceAndTheServerOnlySaysSo(t *testing.T) {
	ctx := context.Background()
	d := up()
	d.label = "triposr,sf3d"
	// no key saved
	s, _ := service(t, d, "", nil)
	a := s.Availability(ctx, EngineSF3D)
	if a.Reason != ReasonKeyMissing || !strings.Contains(a.Detail, "PUT /api/tokens/huggingface") || !strings.Contains(a.Detail, "stabilityai/stable-fast-3d") {
		t.Errorf("no key: %+v", a)
	}
	// a key Hugging Face does not know
	var seen string
	s, _ = service(t, d, "hf_wrong", fakeHF(t, http.StatusUnauthorized, &seen))
	if a := s.Availability(ctx, EngineSF3D); a.Reason != musicsvc.ReasonKeyRejected {
		t.Errorf("401: %+v", a)
	}
	if seen != "HEAD /stabilityai/stable-fast-3d/resolve/main/config.yaml Bearer hf_wrong" {
		t.Errorf("it only asks, with the key and a HEAD: %q", seen)
	}
	// a key whose account has not accepted the licence
	s, _ = service(t, d, "hf_ok", fakeHF(t, http.StatusForbidden, nil))
	a = s.Availability(ctx, EngineSF3D)
	if a.Reason != ReasonLicenceNotAccepted || !strings.Contains(a.Detail, "/stabilityai/stable-fast-3d") || !strings.Contains(a.Detail, "accept the licence") {
		t.Errorf("403: %+v", a)
	}
	// a key that may read it: what is missing is the weights, which are installed
	s, _ = service(t, d, "hf_ok", fakeHF(t, http.StatusFound, nil))
	if a := s.Availability(ctx, EngineSF3D); a.Reason != musicsvc.ReasonNoWeights {
		t.Errorf("accepted: %+v", a)
	}
	// Hugging Face is not asked once the weights are there, and TripoSR never asks
	s, dir := service(t, d, "", nil)
	fill(t, dir, EngineSF3D)
	if a := s.Availability(ctx, EngineSF3D); a.Reason == ReasonKeyMissing {
		t.Errorf("weights are there: %+v", a)
	}
	s, _ = service(t, d, "", nil)
	if a := s.Availability(ctx, EngineTripoSR); a.Reason != musicsvc.ReasonNoWeights {
		t.Errorf("TripoSR needs no account: %+v", a)
	}
	// Hugging Face cannot be reached: said, not guessed
	s, _ = service(t, d, "hf_ok", nil)
	s.HFBase = "http://127.0.0.1:1"
	if a := s.Availability(ctx, EngineSF3D); a.Reason != musicsvc.ReasonUnreachable {
		t.Errorf("%+v", a)
	}
}

func TestOpenRefusesWithTheReasonWhenTheEngineCannotBeUsed(t *testing.T) {
	d := up()
	s, dir := service(t, d, "", nil)
	_, _, err := s.Open(context.Background(), EngineTripoSR)
	var ue *musicsvc.UnavailableError
	if !errors.As(err, &ue) || ue.Reason != musicsvc.ReasonNoWeights {
		t.Errorf("%v", err)
	}
	fill(t, dir, EngineTripoSR)
	if _, _, err := s.Open(context.Background(), EngineTripoSR); !errors.As(err, &ue) || ue.Reason != musicsvc.ReasonNotRunning {
		t.Errorf("stopped and the server does not start it: %v", err)
	}
}

func TestStartRunsTheContainerOnALoopbackPortWithTheWeightsReadOnly(t *testing.T) {
	d := up()
	s, dir := service(t, d, "", nil)
	if err := s.Start(context.Background(), EngineTripoSR); err != nil {
		t.Fatal(err)
	}
	var run string
	for _, c := range d.calls {
		if c[0] == "run" {
			run = strings.Join(c, " ")
		}
	}
	for _, want := range []string{"--name vt-model3d", "--gpus device=1", "-p 127.0.0.1:8790:8790", "-v " + dir + ":/ckpt:ro", "veritmo/model3d:1"} {
		if !strings.Contains(run, want) {
			t.Errorf("docker run lacks %q: %s", want, run)
		}
	}
	if st := s.State(context.Background(), EngineTripoSR); st.State != musicsvc.StateRunning {
		t.Errorf("%+v", st)
	}
	if err := s.Stop(context.Background(), EngineTripoSR); err != nil {
		t.Fatal(err)
	}
	if st := s.State(context.Background(), EngineTripoSR); st.State != musicsvc.StateStopped {
		t.Errorf("%+v", st)
	}
}

// What the program says it needs is what the container reads: the list of files, the port and the label are the ones of docker/model3d.
func TestTheContainerFilesAreTheOnesTheProgramExpects(t *testing.T) {
	server, err := os.ReadFile("../../../docker/model3d/server.py")
	if err != nil {
		t.Skip("docker/model3d is not here")
	}
	for _, engine := range Engines() {
		for _, rel := range Required(engine) {
			if !strings.Contains(string(server), `"`+rel+`"`) {
				t.Errorf("server.py does not list %s for %s", rel, engine)
			}
		}
	}
	if !strings.Contains(string(server), `"MODEL3D_PORT", "8790"`) {
		t.Error("the port of the service is not 8790")
	}
	dockerfile, _ := os.ReadFile("../../../docker/model3d/Dockerfile")
	if !strings.Contains(string(dockerfile), "LABEL "+EnginesLabel+"=") || !strings.Contains(string(dockerfile), "EXPOSE 8790") {
		t.Error("the Dockerfile does not carry the label of the engines or the port")
	}
	fetch, _ := os.ReadFile("../../../docker/model3d/fetch.py")
	for _, rel := range Required(EngineTripoSR) {
		if !strings.Contains(string(fetch), rel) {
			t.Errorf("fetch.py does not fetch %s", rel)
		}
	}
}

// Against the real Hugging Face (VERITMO_REAL_HF=1): with no key the engine says so, and a key that is not one is refused with 401 by the real
// repository. Nothing is sent but a HEAD of one file of the repository.
func TestRealHuggingFaceGate(t *testing.T) {
	if os.Getenv("VERITMO_REAL_HF") == "" {
		t.Skip("set VERITMO_REAL_HF=1 to ask the real Hugging Face")
	}
	ctx := context.Background()
	s := &Service{HFKey: func() string { return "" }}
	if reason, detail := s.CheckGate(ctx); reason != ReasonKeyMissing {
		t.Errorf("no key: %q %q", reason, detail)
	}
	s.HFKey = func() string { return "hf_thisIsNotAKeyAtAll0123456789" }
	reason, detail := s.CheckGate(ctx)
	t.Logf("a wrong key: %q: %s", reason, detail)
	if reason != musicsvc.ReasonKeyRejected {
		t.Errorf("a wrong key: %q %q", reason, detail)
	}
}

// withService gives the service a host with both services published (a file each) and a `dotnet` that records its environment and stays up.
func withService(t *testing.T, s *Service, installed bool) (envFile string) {
	t.Helper()
	root := t.TempDir()
	host := &musicsvc.ServiceHost{Dir: filepath.Join(root, "services"), StateDir: filepath.Join(root, "state"), BinDir: root}
	if installed {
		for _, e := range Engines() {
			os.MkdirAll(filepath.Join(host.Dir, Spec(e).Service.Dir), 0o755)
			os.WriteFile(host.DLLPath(Spec(e).Service), []byte("x"), 0o644)
		}
	}
	envFile = filepath.Join(root, "env.txt")
	fake := filepath.Join(root, "dotnet")
	os.WriteFile(fake, []byte("#!/bin/sh\nenv > '"+envFile+"'\nexec sleep 60\n"), 0o755)
	host.Dotnet = fake
	s.Services = host
	return envFile
}

func TestTripoSRRunsAsTheDotnetServiceByDefaultAndSaysWhatEachBackendLacks(t *testing.T) {
	ctx := context.Background()
	d := up()
	s, dir := service(t, d, "", nil)
	withService(t, s, false)
	if got := s.BackendOf(EngineTripoSR); got != musicsvc.BackendDotnet {
		t.Fatalf("the default backend is %q", got)
	}
	a := s.Availability(ctx, EngineTripoSR)
	if a.Reason != musicsvc.ReasonNoService || a.Available {
		t.Errorf("service not published: %+v", a)
	}
	if b := a.Backends[musicsvc.BackendDotnet]; b.Available || len(b.Missing) != 2 || b.Missing[0] != musicsvc.MissingService {
		t.Errorf("dotnet backend: %+v", b)
	}
	if b := a.Backends[musicsvc.BackendDocker]; len(b.Missing) != 1 || b.Missing[0] != musicsvc.MissingWeights {
		t.Errorf("docker backend: %+v", b)
	}
	// the container is not asked about when the service is the backend
	for _, c := range d.calls {
		if c[0] == "image" && len(c) > 2 && c[2] == "-f" {
			t.Errorf("the image label was read for the .NET service: %v", c)
		}
	}
	withService(t, s, true)
	fill(t, dir, EngineTripoSR)
	if a := s.Availability(ctx, EngineTripoSR); a.Reason != musicsvc.ReasonNotRunning || a.Container == nil {
		t.Errorf("published and stopped: %+v", a)
	}
}

func TestTripoSRFallsBackToTheContainerWhenTheSettingSaysSo(t *testing.T) {
	ctx := context.Background()
	d := up()
	s, dir := service(t, d, "", nil)
	withService(t, s, true)
	fill(t, dir, EngineTripoSR)
	s.Backend = func() string { return musicsvc.BackendDocker }
	if got := s.BackendOf(EngineTripoSR); got != musicsvc.BackendDocker {
		t.Fatalf("backend %q", got)
	}
	d.running = true
	if a := s.Availability(ctx, EngineTripoSR); !a.Available {
		t.Errorf("the container runs: %+v", a)
	}
	s.Backend = func() string { return musicsvc.BackendDotnet }
	if a := s.Availability(ctx, EngineTripoSR); a.Available || a.Reason != musicsvc.ReasonNotRunning {
		t.Errorf("the container runs but the backend is the service: %+v", a)
	}
}

func TestStableFastRunsAsItsOwnDotnetServiceAndNeedsTheTetrahedraGridOnlyThere(t *testing.T) {
	ctx := context.Background()
	d := up()
	d.label = "triposr,sf3d"
	s, dir := service(t, d, "", nil)
	withService(t, s, true)
	if !HasService(EngineSF3D) || !HasService(EngineTripoSR) {
		t.Fatal("both engines have a .NET service")
	}
	if got := s.BackendOf(EngineSF3D); got != musicsvc.BackendDotnet {
		t.Errorf("the default backend of Stable Fast 3D is %q", got)
	}
	if a, b := specOn(EngineSF3D, musicsvc.BackendDotnet).Container, specOn(EngineTripoSR, musicsvc.BackendDotnet).Container; a == b {
		t.Errorf("the two services share the name %q: one would see and stop the other", a)
	}
	if a := specOn(EngineSF3D, musicsvc.BackendDocker).Container; a != Container {
		t.Errorf("in the container they are one process: %q", a)
	}
	fill(t, dir, EngineSF3D) // what the container reads: no tetrahedra grid
	s.Backend = func() string { return musicsvc.BackendDocker }
	d.running = true
	if a := s.Availability(ctx, EngineSF3D); a.Reason == musicsvc.ReasonNoWeights {
		t.Errorf("the container does not read the grid: %+v", a)
	}
	s.Backend = nil
	a := s.Availability(ctx, EngineSF3D)
	if a.Reason != musicsvc.ReasonNoWeights || len(a.Missing) != 1 || a.Missing[0] != "sf3d/160_tets.npz" {
		t.Errorf("the service reads the grid: %+v", a)
	}
	if b := a.Backends[musicsvc.BackendDotnet]; b.Available || len(b.Missing) != 1 || b.Missing[0] != musicsvc.MissingWeights {
		t.Errorf("dotnet backend: %+v", b)
	}
	if b := a.Backends[musicsvc.BackendDocker]; slices.Contains(b.Missing, musicsvc.MissingWeights) {
		t.Errorf("docker backend: %+v", b)
	}
	if got := RequiredFor(EngineSF3D, musicsvc.BackendDotnet); !slices.Contains(got, "sf3d/160_tets.npz") || len(got) != len(Required(EngineSF3D))+1 {
		t.Errorf("RequiredFor dotnet: %v", got)
	}
	if got := RequiredFor(EngineTripoSR, musicsvc.BackendDotnet); len(got) != len(Required(EngineTripoSR)) {
		t.Errorf("TripoSR reads the same files in both: %v", got)
	}
}

func TestStartRunsTheStableFastServiceOnItsOwnWithItsEnvironment(t *testing.T) {
	ctx := context.Background()
	d := up()
	s, dir := service(t, d, "", nil)
	envFile := withService(t, s, true)
	fill(t, dir, EngineSF3D)
	os.WriteFile(filepath.Join(dir, "sf3d/160_tets.npz"), []byte("x"), 0o644)
	if err := s.Start(ctx, EngineSF3D); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Stop(ctx, EngineSF3D) })
	st := s.State(ctx, EngineSF3D)
	if st.State != musicsvc.StateRunning || st.Port == 0 {
		t.Fatalf("state %+v", st)
	}
	if other := s.State(ctx, EngineTripoSR); other.State == musicsvc.StateRunning {
		t.Errorf("TripoSR shows the service of Stable Fast 3D as its own: %+v", other)
	}
	var env []byte
	for i := 0; i < 50 && len(env) == 0; i++ {
		env, _ = os.ReadFile(envFile)
		time.Sleep(50 * time.Millisecond)
	}
	for _, want := range []string{"SF3D_CKPT=" + dir, "SF3D_PORT=" + strconv.Itoa(st.Port), "SF3D_DEVICE=cuda:0", "SF3D_HOST=127.0.0.1"} {
		if !strings.Contains("\n"+string(env), "\n"+want+"\n") {
			t.Errorf("the service environment lacks %q:\n%s", want, env)
		}
	}
	if err := s.Stop(ctx, EngineSF3D); err != nil {
		t.Fatal(err)
	}
	if st := s.State(ctx, EngineSF3D); st.State != musicsvc.StateStopped {
		t.Errorf("after stop: %+v", st)
	}
}

func TestStartRunsTheDotnetServiceOnAFreePortWithTheTripoSREnvironment(t *testing.T) {
	ctx := context.Background()
	d := up()
	s, dir := service(t, d, "", nil)
	envFile := withService(t, s, true)
	fill(t, dir, EngineTripoSR)
	if err := s.Start(ctx, EngineTripoSR); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Stop(ctx, EngineTripoSR) })
	st := s.State(ctx, EngineTripoSR)
	if st.State != musicsvc.StateRunning || st.Port == 0 || st.Port == Port {
		t.Errorf("state %+v: it must run on a free port, not the container's", st)
	}
	var env []byte
	for i := 0; i < 50 && len(env) == 0; i++ {
		env, _ = os.ReadFile(envFile)
		time.Sleep(50 * time.Millisecond)
	}
	for _, want := range []string{"TRIPOSR_CKPT=" + dir, "TRIPOSR_PORT=" + strconv.Itoa(st.Port), "TRIPOSR_DEVICE=cuda:0", "TRIPOSR_HOST=127.0.0.1", "CUDA_VISIBLE_DEVICES=1", "VERITMO_SERVICE=" + Container} {
		if !strings.Contains("\n"+string(env), "\n"+want+"\n") {
			t.Errorf("the service environment lacks %q:\n%s", want, env)
		}
	}
	for _, c := range d.calls {
		if c[0] == "run" {
			t.Errorf("a container was started for the .NET service: %v", c)
		}
	}
	if err := s.Stop(ctx, EngineTripoSR); err != nil {
		t.Fatal(err)
	}
	if st := s.State(ctx, EngineTripoSR); st.State != musicsvc.StateStopped {
		t.Errorf("after stop: %+v", st)
	}
}

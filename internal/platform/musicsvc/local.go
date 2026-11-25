package musicsvc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"veritmo/internal/platform/runner"
)

// Spec describes one generator: what its service is called, where its weights live and what it needs.
type Spec struct {
	// Name is how messages call the generator ("HeartMuLa").
	Name string
	// Version is the version of the service this program is made for.
	Version string
	// Container is the name the service is known by (its state and log files carry it).
	Container string
	// WeightsDir is where the weights live, relative to the project root.
	WeightsDir string
	// MinFreeMB is the free GPU memory a song needs.
	MinFreeMB int64
	// Missing returns the weight files the service reads that are not in the weights folder (nothing when it is complete).
	Missing func(dir string) []string
	// ServiceMissing is the same for what only the .NET service reads, on top of Missing (nil: the service reads what the container does).
	ServiceMissing func(dir string) []string
	// Sends says what the user lets go to a service on another machine ("lyrics and tags").
	Sends string
	// Service describes the .NET service of the generator.
	Service ServiceSpec
	// Docker describes its Python container (the image and the port); empty for a generator that has none.
	Docker DockerSpec
	// Sizes is what each way of running it takes on disk (approximate).
	Sizes Sizes
}

// MissingFor returns the weight files that are not in dir for a backend: what Missing says, plus ServiceMissing for the .NET service.
func (sp Spec) MissingFor(backend, dir string) []string {
	var out []string
	if sp.Missing != nil {
		out = sp.Missing(dir)
	}
	if backend != BackendDocker && sp.ServiceMissing != nil {
		out = append(out, sp.ServiceMissing(dir)...)
	}
	return out
}

// Local is the engine as a service on this machine.
type Local struct {
	Spec   Spec
	Runner runner.CommandRunner
	// Models is the absolute path of the weights folder.
	Models string
	// Services says where the .NET service of the engine is installed and runs it as a process.
	Services *ServiceHost
	// Backend is how it runs: BackendDotnet (the default, empty too) or BackendDocker (the container of Spec.Docker).
	Backend string
}

// UsesServices says whether this engine has a .NET service to run.
func (l Local) UsesServices() bool { return l.Services != nil && l.Spec.Service.DLL != "" }

// GPUInfo is a card and the memory it has free now and in all.
type GPUInfo struct {
	Index   int
	Name    string
	FreeMB  int64
	TotalMB int64
}

// GPUs reads the cards and their free memory with nvidia-smi.
func (l Local) GPUs(ctx context.Context) ([]GPUInfo, error) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := l.Runner.Run(cctx, "nvidia-smi", []string{"--query-gpu=index,name,memory.free,memory.total", "--format=csv,noheader,nounits"})
	if err != nil {
		return nil, fmt.Errorf("nvidia-smi: %w", err)
	}
	var gpus []GPUInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Split(line, ",")
		if len(parts) < 4 {
			continue
		}
		idx, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
		free, err2 := strconv.ParseInt(strings.TrimSpace(parts[len(parts)-2]), 10, 64)
		total, err3 := strconv.ParseInt(strings.TrimSpace(parts[len(parts)-1]), 10, 64)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		gpus = append(gpus, GPUInfo{Index: idx, Name: strings.TrimSpace(strings.Join(parts[1:len(parts)-2], ",")), FreeMB: free, TotalMB: total})
	}
	if len(gpus) == 0 {
		return nil, errors.New("nvidia-smi lists no GPU")
	}
	return gpus, nil
}

// ErrNoRoom is returned when no card has enough free memory.
var ErrNoRoom = errors.New("no GPU has enough free memory")

// PickGPU chooses the card with the most free memory among those that have at least spec.MinFreeMB, or the one the
// caller names (a CUDA index as text, empty for the best). When none fits the error says how much each has free.
func PickGPU(spec Spec, gpus []GPUInfo, want string) (GPUInfo, error) {
	var best *GPUInfo
	for i := range gpus {
		g := gpus[i]
		if want != "" && strconv.Itoa(g.Index) != want {
			continue
		}
		if g.FreeMB >= spec.MinFreeMB && (best == nil || g.FreeMB > best.FreeMB) {
			best = &g
		}
	}
	if best != nil {
		return *best, nil
	}
	var each []string
	for _, g := range gpus {
		if want != "" && strconv.Itoa(g.Index) != want {
			continue
		}
		each = append(each, fmt.Sprintf("GPU %d (%s) has %.1f GB free", g.Index, g.Name, float64(g.FreeMB)/1024))
	}
	if len(each) == 0 {
		each = []string{"the GPU asked for is not there"}
	}
	return GPUInfo{}, fmt.Errorf("%w for %s: it needs about %.1f GB and %s", ErrNoRoom, spec.Name, float64(spec.MinFreeMB)/1024, strings.Join(each, ", "))
}

// Running returns the port the service listens on (127.0.0.1) when it is running.
func (l Local) Running(ctx context.Context) (int, bool) {
	if l.UsesDocker() {
		running, _, _, ok := l.dockerInspect(ctx)
		return l.Spec.Docker.Port, ok && running
	}
	if !l.UsesServices() {
		return 0, false
	}
	st, ok := l.Services.readState(l.Spec.Container)
	return st.Port, ok
}

// FreePort asks the system for a port that is free on 127.0.0.1.
func FreePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// Start runs the service on a card and port, replacing one that runs already.
func (l Local) Start(ctx context.Context, gpu GPUInfo, port int) error {
	if l.UsesDocker() {
		return l.dockerStart(ctx, gpu)
	}
	if !l.UsesServices() {
		return fmt.Errorf("the %s service is not installed: run scripts/get-services.sh", l.Spec.Name)
	}
	_ = l.Stop(ctx)
	return l.Services.start(ctx, l, gpu, port)
}

// Stop ends the service (it is not an error that there is none).
func (l Local) Stop(ctx context.Context) error {
	if l.UsesDocker() {
		return l.dockerStop(ctx)
	}
	if !l.UsesServices() {
		return nil
	}
	return l.Services.stop(ctx, l.Spec.Container)
}

// The states of a service.
const (
	StateStopped  = "stopped"
	StateStarting = "starting"
	StateRunning  = "running"
)

// ContainerState is the lifecycle of the service of an engine.
type ContainerState struct {
	State string `json:"state"` // stopped, starting or running
	// GPU is the card it was given; Port the port it listens on (127.0.0.1); Since when it started.
	GPU   *int   `json:"gpu,omitempty"`
	Port  int    `json:"port,omitempty"`
	Since string `json:"since,omitempty"`
	// MemoryMB is what the service uses, when the caller asked.
	MemoryMB int64 `json:"memory_mb,omitempty"`
	// Error is why the last start did not get ready (set by the server).
	Error string `json:"error,omitempty"`
}

// State says whether the service runs, on which card and port, since when and, with stats, how much memory it
// uses. A service that does not run is stopped, whatever the reason.
func (l Local) State(ctx context.Context, stats bool) ContainerState {
	if l.UsesDocker() {
		running, since, gpu, ok := l.dockerInspect(ctx)
		if !ok || !running {
			return ContainerState{State: StateStopped}
		}
		return ContainerState{State: StateRunning, Port: l.Spec.Docker.Port, GPU: &gpu, Since: since}
	}
	if !l.UsesServices() {
		return ContainerState{State: StateStopped}
	}
	st, ok := l.Services.readState(l.Spec.Container)
	if !ok {
		return ContainerState{State: StateStopped}
	}
	gpu := st.GPU
	cs := ContainerState{State: StateRunning, Port: st.Port, GPU: &gpu, Since: st.Since}
	if stats {
		cs.MemoryMB = memoryMB(st.PID)
	}
	return cs
}

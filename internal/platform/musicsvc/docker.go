package musicsvc

// docker.go: the Python container of a generator as the other way to run it on this machine, beside the .NET service. The server runs
// it the way scripts/music-containers.sh does (loopback only, the weights read-only, one card, no restart policy): there is one place
// that knows the image names and ports, and a test compares it with the script. See specs/music-containers.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"veritmo/internal/platform/legacyname"
)

// The two ways to run a generator on this machine.
const (
	BackendDotnet = "dotnet"
	BackendDocker = "docker"
)

// More reasons an engine cannot be used, for the Docker backend.
const (
	// ReasonNoDocker means Docker is not installed or its daemon does not answer.
	ReasonNoDocker = "no_docker"
	// ReasonNoImage means the image of the generator is not built or pulled.
	ReasonNoImage = "no_image"
	// ReasonNoGPURuntime means Docker has no NVIDIA runtime (docker run --gpus would fail).
	ReasonNoGPURuntime = "no_gpu_runtime"
)

// DockerSpec says how the container of a generator is run: the image, built from docker/<engine>/, and the port it listens on.
type DockerSpec struct {
	Image string
	Port  int
}

// ValidBackend reports whether the text is one of the two backends.
func ValidBackend(b string) bool { return b == BackendDotnet || b == BackendDocker }

// dockerProgram is the Docker command line program: VERITMO_DOCKER when set (podman is compatible), docker otherwise.
func dockerProgram() string {
	if p := strings.TrimSpace(legacyname.Getenv("DOCKER")); p != "" {
		return p
	}
	return "docker"
}

// UsesDocker says whether this engine runs as the container of its image on this machine.
func (l Local) UsesDocker() bool { return l.Backend == BackendDocker && l.Spec.Docker.Image != "" }

// DockerState is what Docker says about the container of an engine, and about the programs it needs.
type DockerState struct {
	// Daemon is whether Docker answers; Image whether the image of the engine is there; GPURuntime whether Docker has the NVIDIA runtime.
	Daemon, Image, GPURuntime bool
	// Error says why the daemon does not answer.
	Error string
}

// DockerCheck asks Docker, without changing anything, whether the container of this engine could be run.
func (l Local) DockerCheck(ctx context.Context) DockerState {
	var st DockerState
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := l.Runner.Run(cctx, dockerProgram(), []string{"info", "--format", "{{json .Runtimes}}"})
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.Daemon = true
	st.GPURuntime = strings.Contains(string(out), "nvidia")
	if l.Spec.Docker.Image != "" {
		_, err := l.Runner.Run(cctx, dockerProgram(), []string{"image", "inspect", l.Spec.Docker.Image})
		st.Image = err == nil
	}
	return st
}

// dockerInspect reads the state of the container: whether it runs, since when and the card it was given.
func (l Local) dockerInspect(ctx context.Context) (running bool, since string, gpu int, ok bool) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := l.Runner.Run(cctx, dockerProgram(), []string{"inspect", "-f",
		"{{.State.Status}}|{{.State.StartedAt}}|{{range .HostConfig.DeviceRequests}}{{range .DeviceIDs}}{{.}}{{end}}{{end}}", l.Spec.Container})
	if err != nil {
		return false, "", 0, false
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "|", 3)
	if len(parts) < 3 {
		return false, "", 0, false
	}
	gpu, _ = strconv.Atoi(parts[2])
	return parts[0] == "running", parts[1], gpu, true
}

func (l Local) dockerStart(ctx context.Context, gpu GPUInfo) error {
	if l.Spec.Docker.Image == "" || l.Spec.Docker.Port == 0 {
		return fmt.Errorf("%s has no container", l.Spec.Name)
	}
	if st := l.DockerCheck(ctx); !st.Daemon {
		return fmt.Errorf("docker does not answer: %s", st.Error)
	} else if !st.Image {
		return fmt.Errorf("the image %s of %s is not there: build it from the Environment screen or with scripts/music-containers.sh", l.Spec.Docker.Image, l.Spec.Name)
	}
	_ = l.dockerStop(ctx)
	port := strconv.Itoa(l.Spec.Docker.Port)
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	_, err := l.Runner.Run(cctx, dockerProgram(), []string{"run", "-d", "--name", l.Spec.Container, "--restart", "no",
		"--gpus", "device=" + strconv.Itoa(gpu.Index), "-p", "127.0.0.1:" + port + ":" + port,
		"-v", l.Models + ":/ckpt:ro", l.Spec.Docker.Image})
	if err != nil {
		return fmt.Errorf("docker run: %w", err)
	}
	return nil
}

// dockerStop removes the container (it is not an error that there is none).
func (l Local) dockerStop(ctx context.Context) error {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	_, _ = l.Runner.Run(cctx, dockerProgram(), []string{"rm", "-f", l.Spec.Container})
	return nil
}

// Sizes is what each way of running a generator takes on disk, in bytes, as the install plan and the engines answer say it. They are
// approximate (measured on 2026-11-19 with the images and weights of this machine); zero means not measured.
type Sizes struct {
	// Service is the published .NET service with the native libraries it brings; Image the Docker image; Weights the model folder.
	Service, Image, Weights int64
}

// The backend flags: what each way of running a generator can lack, as the page says it in words.
const (
	MissingService    = "service"
	MissingWeights    = "weights"
	MissingDocker     = "docker"
	MissingImage      = "image"
	MissingGPURuntime = "gpu_runtime"
)

// BackendInfo says whether one way of running a generator on this machine has everything it needs, and what it lacks.
type BackendInfo struct {
	// Available is true when nothing is missing (the generator may still be stopped: that is the state of its container).
	Available bool `json:"available"`
	// Missing lists what is lacking, in the order a person would fix it: `service` (the .NET service, or dotnet), `weights` for
	// dotnet; `docker`, `image`, `weights`, `gpu_runtime` for docker.
	Missing []string `json:"missing,omitempty"`
	// Reason and Detail are the first thing missing as the usual reason code and its sentence.
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
	// NeedBytes is the disk the missing parts take (what an install would download or build); zero when nothing is missing or it is not known.
	NeedBytes int64 `json:"need_bytes,omitempty"`
}

// Backends says, for each way of running this generator on this machine, whether it can be used and what is missing. It starts and changes
// nothing. An engine that has no container (LAION-CLAP, the video vision service) answers nil.
func (e *Engine) Backends(ctx context.Context) map[string]BackendInfo {
	if e.Spec.Docker.Image == "" {
		return nil
	}
	l := e.local()
	weights := len(e.Spec.MissingFor(BackendDocker, l.Models)) > 0
	serviceWeights := len(e.Spec.MissingFor(BackendDotnet, l.Models)) > 0
	out := map[string]BackendInfo{}

	dn := BackendInfo{}
	if l.Services == nil || !l.Services.Installed(e.Spec.Service) {
		dn.Missing = append(dn.Missing, MissingService)
		dn.NeedBytes += e.Spec.Sizes.Service
		dn.Reason, dn.Detail = ReasonNoService, fmt.Sprintf("the %s service is not installed: run scripts/get-services.sh", e.Spec.Name)
	} else if err := l.Services.DotnetFound(); err != nil {
		dn.Missing = append(dn.Missing, MissingService)
		dn.Reason, dn.Detail = ReasonNoService, "dotnet is not installed: the "+e.Spec.Name+" service needs the .NET runtime"
	}
	if serviceWeights {
		dn.Missing = append(dn.Missing, MissingWeights)
		dn.NeedBytes += e.Spec.Sizes.Weights
		if dn.Reason == "" {
			dn.Reason, dn.Detail = ReasonNoWeights, fmt.Sprintf("the weights are not complete in %s", e.Spec.WeightsDir)
		}
	}
	dn.Available = len(dn.Missing) == 0
	out[BackendDotnet] = dn

	dk := BackendInfo{}
	st := l.DockerCheck(ctx)
	if !st.Daemon {
		dk.Missing = append(dk.Missing, MissingDocker)
		dk.Reason, dk.Detail = ReasonNoDocker, "docker does not answer: "+st.Error
	} else {
		if !st.Image {
			dk.Missing = append(dk.Missing, MissingImage)
			dk.NeedBytes += e.Spec.Sizes.Image
			dk.Reason, dk.Detail = ReasonNoImage, fmt.Sprintf("the image %s is not there", e.Spec.Docker.Image)
		}
	}
	if weights {
		dk.Missing = append(dk.Missing, MissingWeights)
		dk.NeedBytes += e.Spec.Sizes.Weights
		if dk.Reason == "" {
			dk.Reason, dk.Detail = ReasonNoWeights, fmt.Sprintf("the weights are not complete in %s", e.Spec.WeightsDir)
		}
	}
	if st.Daemon && !st.GPURuntime {
		dk.Missing = append(dk.Missing, MissingGPURuntime)
		if dk.Reason == "" {
			dk.Reason, dk.Detail = ReasonNoGPURuntime, "docker has no NVIDIA runtime: install the NVIDIA Container Toolkit"
		}
	}
	dk.Available = len(dk.Missing) == 0
	out[BackendDocker] = dk
	return out
}

// DockerProgram is the Docker command line program the container backend uses (docker, or VERITMO_DOCKER), for the callers that run
// `docker build` and the like themselves.
func DockerProgram() string { return dockerProgram() }

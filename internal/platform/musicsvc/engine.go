package musicsvc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Why the engine cannot be used (the `reason` of the availability).
const (
	// ReasonNoService means the .NET service is not installed (scripts/get-services.sh) or dotnet is missing.
	ReasonNoService   = "no_service"
	ReasonNoWeights   = "no_weights"
	ReasonNoGPU       = "no_gpu"
	ReasonUnreachable = "unreachable"
	ReasonNoConsent   = "no_consent"
	ReasonKeyRejected = "key_rejected"
	// ReasonNotRunning means everything is installed, but the service is stopped and this engine does not start it by
	// itself (a run made through the server): the user starts it from the Environment screen.
	ReasonNotRunning = "not_running"
)

// Where the engine runs.
const (
	ModeLocal    = "local"
	ModeExternal = "external"
)

// External is the service on another machine, from the settings.
type External struct {
	Host      string
	Token     string
	Consented bool
}

// Availability says whether a song can be made now, and where, or why not.
type Availability struct {
	Available bool     `json:"available"`
	Mode      string   `json:"mode,omitempty"`
	Reason    string   `json:"reason,omitempty"`
	Detail    string   `json:"detail,omitempty"`
	Missing   []string `json:"missing,omitempty"`
	Version   string   `json:"version,omitempty"`
	// Container is the lifecycle of the local service (nothing for an engine on another machine).
	Container *ContainerState `json:"container,omitempty"`
	// Backends says, for each way of running a generator on this machine (dotnet, docker), whether it has everything it needs and what it lacks.
	// Only the generators that have a container carry it.
	Backends map[string]BackendInfo `json:"backends,omitempty"`
}

// UnavailableError is an engine that cannot be used, with the reason code.
type UnavailableError struct {
	Reason string
	Detail string
}

func (e *UnavailableError) Error() string { return e.Detail }

func unavailable(reason, format string, args ...any) *UnavailableError {
	return &UnavailableError{Reason: reason, Detail: fmt.Sprintf(format, args...)}
}

// Engine decides where the songs are made: the external service when an address is saved, the local service otherwise.
type Engine struct {
	Spec     Spec
	Local    Local
	External External
	// PreferGPU is the CUDA index the run asks for; empty chooses the card with most free memory.
	PreferGPU string
	// StartTimeout is how long a started service has to answer ready (default 180 s); PollEvery how often it is asked (default 1 s).
	StartTimeout time.Duration
	PollEvery    time.Duration
	// NoStart says that this engine never starts the service: with none running, Availability says not_running and
	// Open fails with that reason. The server sets it (the user starts the services); the command line does not.
	NoStart bool
	// WithStats asks for the memory the service uses.
	WithStats bool
	started   bool
}

func (e *Engine) externalClient() *Client {
	return NewClient(e.Spec.Name, e.External.Host, e.External.Token)
}

// local is the local service of this engine with its spec filled in.
func (e *Engine) local() Local {
	l := e.Local
	l.Spec = e.Spec
	return l
}

// healthError turns what a health call said into an unavailable engine, or nil when it is ready.
func healthError(h Health, err error, where string) *UnavailableError {
	switch {
	case errors.Is(err, ErrUnauthorized):
		return unavailable(ReasonKeyRejected, "%s needs a token, or the one saved is wrong", where)
	case err != nil:
		return unavailable(ReasonUnreachable, "%s does not answer: %v", where, err)
	case !h.Ready:
		why := "it is not ready"
		if len(h.Missing) > 0 {
			why = "it lacks " + strings.Join(h.Missing, ", ")
		}
		if h.GPU == nil {
			why += " (it sees no GPU)"
		}
		return unavailable(ReasonUnreachable, "%s answers but %s", where, why)
	}
	return nil
}

// Availability checks, without starting anything, whether the engine can be used: the external service answers
// ready, or the service and the weights are there and a card is big enough.
func (e *Engine) Availability(ctx context.Context) Availability {
	if e.External.Host != "" {
		a := Availability{Mode: ModeExternal}
		if !e.External.Consented {
			a.Reason, a.Detail = ReasonNoConsent, "sending "+e.Spec.Sends+" to "+e.External.Host+" has not been accepted"
			return a
		}
		h, err := e.externalClient().Health(ctx)
		if ue := healthError(h, err, e.External.Host); ue != nil {
			a.Reason, a.Detail = ue.Reason, ue.Detail
			return a
		}
		a.Available, a.Version = true, h.Version
		return a
	}
	a := Availability{Mode: ModeLocal}
	if err := e.localReady(ctx); err != nil {
		var ue *UnavailableError
		if errors.As(err, &ue) {
			a.Reason, a.Detail = ue.Reason, ue.Detail
			if ue.Reason == ReasonNoWeights {
				a.Missing = e.Spec.MissingFor(e.Local.Backend, e.Local.Models)
			}
		}
		return a
	}
	l := e.local()
	state := l.State(ctx, e.WithStats)
	a.Container = &state
	a.Version = e.Spec.Version
	if e.NoStart && state.State != StateRunning {
		a.Reason = ReasonNotRunning
		a.Detail = fmt.Sprintf("the %s service is not running: start it from the Environment screen", e.Spec.Name)
		return a
	}
	a.Available = true
	return a
}

// localReady checks the pieces of the local engine in the order a person would fix them.
func (e *Engine) localReady(ctx context.Context) error {
	l := e.local()
	if l.UsesDocker() {
		st := l.DockerCheck(ctx)
		switch {
		case !st.Daemon:
			return unavailable(ReasonNoDocker, "docker does not answer (%s): the %s container needs it", st.Error, e.Spec.Name)
		case !st.Image:
			return unavailable(ReasonNoImage, "the image %s of %s is not there: build it from the Environment screen or with scripts/music-containers.sh", e.Spec.Docker.Image, e.Spec.Name)
		case !st.GPURuntime:
			return unavailable(ReasonNoGPURuntime, "docker has no NVIDIA runtime: install the NVIDIA Container Toolkit so that %s can use the card", e.Spec.Name)
		}
	} else if !l.UsesServices() || !l.Services.Installed(e.Spec.Service) {
		return unavailable(ReasonNoService, "the %s service is not installed: run scripts/get-services.sh", e.Spec.Name)
	}
	if !l.UsesDocker() {
		if err := l.Services.DotnetFound(); err != nil {
			return unavailable(ReasonNoService, "dotnet is not installed: the %s service needs the .NET runtime", e.Spec.Name)
		}
	}
	if missing := l.Spec.MissingFor(l.Backend, l.Models); len(missing) > 0 {
		return unavailable(ReasonNoWeights, "the weights are not complete in %s (%d files missing)", e.Spec.WeightsDir, len(missing))
	}
	gpus, err := l.GPUs(ctx)
	if err != nil {
		return unavailable(ReasonNoGPU, "no GPU can be read: %v", err)
	}
	for _, g := range gpus {
		if g.TotalMB >= e.Spec.MinFreeMB {
			return nil
		}
	}
	return unavailable(ReasonNoGPU, "no GPU has the %.1f GB %s needs", float64(e.Spec.MinFreeMB)/1024, e.Spec.Name)
}

// Open returns a client of a service that is ready to make a song, starting the service when the engine is local
// and not running. Close stops what Open started.
func (e *Engine) Open(ctx context.Context) (*Client, error) {
	if e.External.Host != "" {
		if !e.External.Consented {
			return nil, unavailable(ReasonNoConsent, "sending %s to %s has not been accepted: accept it in Settings", e.Spec.Sends, e.External.Host)
		}
		c := e.externalClient()
		h, err := c.Health(ctx)
		if ue := healthError(h, err, e.External.Host); ue != nil {
			return nil, ue
		}
		return c, nil
	}
	if err := e.localReady(ctx); err != nil {
		return nil, err
	}
	l := e.local()
	if port, ok := l.Running(ctx); ok {
		c := NewClient(e.Spec.Name, fmt.Sprintf("127.0.0.1:%d", port), "")
		if h, err := c.Health(ctx); err == nil && h.Ready {
			return c, nil
		}
		// A service that does not answer is replaced.
	}
	if e.NoStart {
		return nil, unavailable(ReasonNotRunning, "the %s service is not running: start it from the Environment screen", e.Spec.Name)
	}
	gpus, err := l.GPUs(ctx)
	if err != nil {
		return nil, unavailable(ReasonNoGPU, "no GPU can be read: %v", err)
	}
	gpu, err := PickGPU(e.Spec, gpus, e.PreferGPU)
	if err != nil {
		return nil, err
	}
	port := l.Spec.Docker.Port // the container listens on the port of its image; a .NET service on a free one
	if !l.UsesDocker() {
		if port, err = FreePort(); err != nil {
			return nil, err
		}
	}
	if err := l.Start(ctx, gpu, port); err != nil {
		return nil, err
	}
	e.started = true
	c := NewClient(e.Spec.Name, fmt.Sprintf("127.0.0.1:%d", port), "")
	startTimeout, pollEvery := e.StartTimeout, e.PollEvery
	if startTimeout <= 0 {
		startTimeout = DefaultStartTimeout
	}
	if pollEvery <= 0 {
		pollEvery = DefaultPollEvery
	}
	deadline := time.Now().Add(startTimeout)
	for {
		h, err := c.Health(ctx)
		if err == nil && h.Ready {
			return c, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if time.Now().After(deadline) {
			return nil, unavailable(ReasonUnreachable, "the %s service did not get ready in %s", e.Spec.Name, startTimeout)
		}
		time.Sleep(pollEvery)
	}
}

// The defaults of Engine.StartTimeout and Engine.PollEvery.
const (
	DefaultStartTimeout = 180 * time.Second
	DefaultPollEvery    = time.Second
)

// Close stops the service this Engine started; one that was already running is left alone.
func (e *Engine) Close() {
	if e.started {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		l := e.local()
		_ = l.Stop(ctx)
		e.started = false
	}
}

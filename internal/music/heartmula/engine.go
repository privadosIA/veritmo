package heartmula

import (
	"context"

	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/platform/settings"
)

// Why the engine cannot be used (the `reason` of the availability).
const (
	ReasonNoService   = musicsvc.ReasonNoService
	ReasonNoWeights   = musicsvc.ReasonNoWeights
	ReasonNoGPU       = musicsvc.ReasonNoGPU
	ReasonUnreachable = musicsvc.ReasonUnreachable
	ReasonNoConsent   = musicsvc.ReasonNoConsent
	ReasonKeyRejected = musicsvc.ReasonKeyRejected
	ReasonNotRunning  = musicsvc.ReasonNotRunning
)

// Where the engine runs.
const (
	ModeLocal    = musicsvc.ModeLocal
	ModeExternal = musicsvc.ModeExternal
)

type (
	// External is the service on another machine, from the settings.
	External = musicsvc.External
	// Availability says whether a song can be made now, and where, or why not.
	Availability = musicsvc.Availability
	// UnavailableError is an engine that cannot be used, with the reason code.
	UnavailableError = musicsvc.UnavailableError
	// ContainerState is the lifecycle of the local service.
	ContainerState = musicsvc.ContainerState
)

// ExternalFromSettings reads the saved external address and the acceptance given for it.
func ExternalFromSettings() External {
	host, token := settings.HeartMula()
	return External{Host: host, Token: token, Consented: host != "" && (!settings.Remote(host) || settings.HeartMulaAcknowledged(host))}
}

// StartTimeout is how long a started service has to answer ready; PollEvery how often it is asked.
var (
	StartTimeout = musicsvc.DefaultStartTimeout
	PollEvery    = musicsvc.DefaultPollEvery
)

// Engine decides where the songs are made: the external service when an address is saved, the local service otherwise.
type Engine struct {
	Local    Local
	External External
	// PreferGPU is the CUDA index the run asks for; empty chooses the card with most free memory.
	PreferGPU string
	// NoStart: the engine never starts the service (the server sets it); WithStats asks for its memory.
	NoStart, WithStats bool

	core *musicsvc.Engine
}

// engine returns the shared engine with the current fields; it is made once, because it remembers whether it
// started the service.
func (e *Engine) engine() *musicsvc.Engine {
	if e.core == nil {
		e.core = &musicsvc.Engine{}
	}
	e.core.Spec = spec
	e.core.Local = musicsvc.Local{Spec: spec, Runner: e.Local.Runner, Models: e.Local.Models, Services: e.Local.Services, Backend: e.Local.Backend}
	e.core.External, e.core.PreferGPU = e.External, e.PreferGPU
	e.core.StartTimeout, e.core.PollEvery = StartTimeout, PollEvery
	e.core.NoStart, e.core.WithStats = e.NoStart, e.WithStats
	return e.core
}

// Core is the shared engine, for the server to start and stop the service.
func (e *Engine) Core() *musicsvc.Engine { return e.engine() }

// Availability checks, without starting anything, whether the engine can be used.
func (e *Engine) Availability(ctx context.Context) Availability { return e.engine().Availability(ctx) }

// Open returns a client of a service that is ready to make a song, starting the service when the engine is
// local and not running. Close stops what Open started.
func (e *Engine) Open(ctx context.Context) (*Client, error) { return e.engine().Open(ctx) }

// Close stops the service this Engine started; one that was already running is left alone.
func (e *Engine) Close() { e.engine().Close() }

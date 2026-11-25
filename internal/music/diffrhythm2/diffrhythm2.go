// Package diffrhythm2 talks to the DiffRhythm2 music service (a .NET service), which runs on this
// machine or on another one, and finds out whether there is one to use. The service is optional: nothing here
// starts unless a project asks for the diffrhythm2 generator. What it shares with the other generators that run as
// a service is in internal/platform/musicsvc.
package diffrhythm2

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/platform/runner"
	"veritmo/internal/platform/settings"
)

// Version is the version of the service this program is made for.
const Version = "1"

// What the local engine is made of.
const (
	// Container is the name the service is known by (its state and log files carry it).
	Container = "vt-diffrhythm2"
	// WeightsDir is where the weights live, relative to the project root.
	WeightsDir = "models/music/diffrhythm2"
	// MinFreeMB is the video memory a song needs: 7.8 GB at the peak, measured, and a little room.
	MinFreeMB = 8000
)

// The limits of a song, which are the model's: the longest it was made for, and the length it takes when none is asked.
const (
	MaxSeconds = 210.0
	// Margin is how much longer than the duration asked for a song may be: the model stops by itself at the end of
	// the lyrics, and the duration asked for is the longest it may take.
	Margin = 15.0
)

// Request is what the service takes to make a song.
type Request struct {
	Lyrics      string  `json:"lyrics"`
	Style       string  `json:"style"`
	MaxSecs     float64 `json:"max_secs,omitempty"`
	Seed        int64   `json:"seed"`
	CfgStrength float64 `json:"cfg_strength,omitempty"`
	Steps       int     `json:"steps,omitempty"`
}

// MaxSecsFor is the longest a song may be for the duration the track asks for (0 for none).
func MaxSecsFor(durationSeconds float64) float64 {
	if durationSeconds <= 0 {
		return MaxSeconds
	}
	if d := durationSeconds + Margin; d < MaxSeconds {
		return d
	}
	return MaxSeconds
}

// The answers of the service and the errors the caller tells apart.
type (
	// GPU is one card as the service reports it (see [musicsvc.GPU]).
	GPU = musicsvc.GPU
	// Health is the answer of the service to its health check (see [musicsvc.Health]).
	Health = musicsvc.Health
	// Job is a song being made by the service (see [musicsvc.Job]).
	Job = musicsvc.Job
	// Client talks to a music service over HTTP (see [musicsvc.Client]).
	Client = musicsvc.Client
)

var (
	// ErrUnauthorized is the error of a service that refused the key.
	ErrUnauthorized = musicsvc.ErrUnauthorized
	// ErrUnreachable is the error of a service that cannot be reached.
	ErrUnreachable = musicsvc.ErrUnreachable
	// ErrNoRoom is the error of a service whose card has not enough free memory for the song.
	ErrNoRoom = musicsvc.ErrNoRoom
)

// NewClient returns a client for an address: a host:port or a URL.
func NewClient(address, token string) *Client {
	return musicsvc.NewClient("DiffRhythm2", address, token)
}

// serviceFiles are the files the .NET service reads. The catalog downloads the raw checkpoint and the server runs
// `diffrhythm2 install` afterwards, which fetches the rest (the MuQ and XLM-RoBERTa models) and converts everything
// into these.
var serviceFiles = []string{"config.json", "model.safetensors", "decoder.json", "decoder.safetensors", "mulan_text.safetensors", "mulan_text_tokenizer.json"}

// MissingForService returns the files the .NET service lacks in the weights folder (nothing when it is complete).
func MissingForService(dir string) []string {
	var missing []string
	for _, f := range serviceFiles {
		if fi, err := os.Stat(filepath.Join(dir, f)); err != nil || fi.Size() == 0 {
			missing = append(missing, f)
		}
	}
	return missing
}

// spec is what makes this generator different from the others that run as a service.
var spec = musicsvc.Spec{
	Name: "DiffRhythm2", Version: Version, Container: Container, WeightsDir: WeightsDir,
	MinFreeMB: MinFreeMB, Missing: MissingForService, Sends: "lyrics and style",
	Service: musicsvc.ServiceSpec{Dir: "diffrhythm2", DLL: "diffrhythm2-service.dll", Prefix: "DIFFRHYTHM2", Device: true, Espeak: true},
	Docker:  musicsvc.DockerSpec{Image: "veritmo/diffrhythm2:1", Port: 8788},
	// measured on 2026-11-19: the image is 14.0 GB, the .NET service 5.8 GB with its native libraries; the weights are about 9.5 GB
	Sizes: musicsvc.Sizes{Service: 5_800_000_000, Image: 14_000_000_000, Weights: 9_500_000_000},
}

// Spec is the description of the generator, for the code that is the same for every one.
func Spec() musicsvc.Spec { return spec }

// ExternalFromSettings reads the saved external address and the acceptance given for it.
func ExternalFromSettings() External {
	host, token := settings.DiffRhythm2()
	return External{Host: host, Token: token, Consented: host != "" && (!settings.Remote(host) || settings.DiffRhythm2Acknowledged(host))}
}

// Why the engine cannot be used, where it runs and what it reports: the same for every generator.
const (
	ReasonNoService   = musicsvc.ReasonNoService
	ReasonNoWeights   = musicsvc.ReasonNoWeights
	ReasonNoGPU       = musicsvc.ReasonNoGPU
	ReasonUnreachable = musicsvc.ReasonUnreachable
	ReasonNoConsent   = musicsvc.ReasonNoConsent
	ReasonKeyRejected = musicsvc.ReasonKeyRejected
	ReasonNotRunning  = musicsvc.ReasonNotRunning
	ModeLocal         = musicsvc.ModeLocal
	ModeExternal      = musicsvc.ModeExternal
)

type (
	// External is the address and key of a service on another machine (see [musicsvc.External]).
	External = musicsvc.External
	// Availability says whether the engine can run now and, if not, why (see [musicsvc.Availability]).
	Availability = musicsvc.Availability
	// UnavailableError is the error of an engine that cannot run; its reason is the one of [Availability].
	UnavailableError = musicsvc.UnavailableError
	// GPUInfo is a card with its memory, as the engine needs it (see [musicsvc.GPUInfo]).
	GPUInfo = musicsvc.GPUInfo
	// ContainerState is whether the service of this machine is stopped, starting or running.
	ContainerState = musicsvc.ContainerState
)

// Local is the engine as a service on this machine.
type Local struct {
	Runner runner.CommandRunner
	// Models is the absolute path of the weights folder (models/music/diffrhythm2).
	Models string
	// Services, when set, runs the .NET service.
	Services *musicsvc.ServiceHost
	// Backend is musicsvc.BackendDotnet (the default) or musicsvc.BackendDocker.
	Backend string
}

func (l Local) core() musicsvc.Local {
	return musicsvc.Local{Spec: spec, Runner: l.Runner, Models: l.Models, Services: l.Services, Backend: l.Backend}
}

// GPUs reads the cards and their free memory with nvidia-smi.
func (l Local) GPUs(ctx context.Context) ([]GPUInfo, error) { return l.core().GPUs(ctx) }

// Running returns the port the service listens on (127.0.0.1) when it is running.
func (l Local) Running(ctx context.Context) (int, bool) { return l.core().Running(ctx) }

// Start runs the service on a card and port.
func (l Local) Start(ctx context.Context, gpu GPUInfo, port int) error {
	return l.core().Start(ctx, gpu, port)
}

// Stop ends the service (it is not an error that there is none).
func (l Local) Stop(ctx context.Context) error { return l.core().Stop(ctx) }

// State says whether the service runs, on which card and port, since when and (with stats) its memory.
func (l Local) State(ctx context.Context, stats bool) ContainerState {
	return l.core().State(ctx, stats)
}

// Engine decides where the songs are made: the external service when an address is saved, the local service otherwise.
type Engine struct {
	Local    Local
	External External
	// PreferGPU is the CUDA index the run asks for; empty chooses the card with most free memory.
	PreferGPU string
	// StartTimeout and PollEvery override the defaults of the shared engine (the tests shorten them).
	StartTimeout, PollEvery time.Duration
	// NoStart: the engine never starts the service (the server sets it); WithStats asks for its memory.
	NoStart, WithStats bool

	core *musicsvc.Engine
}

func (e *Engine) engine() *musicsvc.Engine {
	if e.core == nil {
		e.core = &musicsvc.Engine{}
	}
	e.core.Spec = spec
	e.core.Local = musicsvc.Local{Spec: spec, Runner: e.Local.Runner, Models: e.Local.Models, Services: e.Local.Services, Backend: e.Local.Backend}
	e.core.External, e.core.PreferGPU = e.External, e.PreferGPU
	e.core.StartTimeout, e.core.PollEvery = e.StartTimeout, e.PollEvery
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

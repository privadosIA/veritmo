// Package clap describes the LAION-CLAP service, a .NET service that is kept stopped and started by the user from
// the Environment screen, like the music generators. It is an experiment: nothing in a run uses it, so there is no
// client here, only what the service lifecycle needs. What it shares with the generators that run as a
// service is in internal/platform/musicsvc.
package clap

import (
	"os"
	"path/filepath"

	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/platform/runner"
)

// Version is the version of the service this program is made for.
const Version = "1"

// What the local engine is made of.
const (
	// Container is the name the service is known by (its state and log files carry it).
	Container = "vt-clap"
	// WeightsDir is where the weights live, relative to the project root.
	WeightsDir = "models/music/clap"
	// MinFreeMB is the video memory the model needs: about 1 GB, and a little room.
	MinFreeMB = 1500
)

// model is the hub folder of the weights the service reads offline.
const model = "models--laion--clap-htsat-unfused"

// Missing returns what is not in the weights folder (nothing when it is complete). The model counts as there when its
// folder holds a snapshot with its configuration and its weights, so a download that stopped half way is seen as
// incomplete (the hub keeps the files in `blobs` and links them into the snapshot at the end).
func Missing(dir string) []string {
	snaps, _ := filepath.Glob(filepath.Join(dir, "hub", model, "snapshots", "*"))
	for _, s := range snaps {
		if _, err := os.Stat(filepath.Join(s, "config.json")); err != nil {
			continue
		}
		for _, pattern := range []string{"pytorch_model*", "*.safetensors"} {
			if m, _ := filepath.Glob(filepath.Join(s, pattern)); len(m) > 0 {
				if fi, err := os.Stat(m[0]); err == nil && fi.Size() > 0 {
					return nil
				}
			}
		}
	}
	return []string{model}
}

var spec = musicsvc.Spec{
	Name: "LAION-CLAP", Version: Version, Container: Container, WeightsDir: WeightsDir,
	MinFreeMB: MinFreeMB, Missing: Missing, Sends: "audio",
	Service: musicsvc.ServiceSpec{Dir: "lionclap", DLL: "lionclap-service.dll", Prefix: "CLAP", Device: true},
}

// Spec is the description of the service, for the code that is the same for every service.
func Spec() musicsvc.Spec { return spec }

// Engine is the service on this machine; it never uses another machine's.
type Engine struct {
	Runner runner.CommandRunner
	// Models is the absolute path of the weights folder (models/music/clap).
	Models string
	// Services, when set, runs the .NET service (lion-clap).
	Services *musicsvc.ServiceHost
}

// Core is the shared engine, for the server to start and stop the service. It never starts it by itself.
func (e Engine) Core() *musicsvc.Engine {
	return &musicsvc.Engine{
		Spec:    spec,
		Local:   musicsvc.Local{Spec: spec, Runner: e.Runner, Models: e.Models, Services: e.Services},
		NoStart: true,
	}
}

package heartmula

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/platform/runner"
)

// What the local engine is made of.
const (
	// Container is the name the service is known by (its state and log files carry it).
	Container = "vt-heartmula"
	// WeightsDir is where the weights live, relative to the project root.
	WeightsDir = "models/music/heartmula"
	// MinFreeMB is the free GPU memory a song needs: the language model peaks at about 8.4 GB and the decoder at
	// 6.9 GB, one stage at a time, plus what the CUDA context takes.
	MinFreeMB = 10500
)

// spec is what makes this generator different from the others that run as a service.
var spec = musicsvc.Spec{
	Name: "HeartMuLa", Version: Version, Container: Container, WeightsDir: WeightsDir,
	MinFreeMB: MinFreeMB, Missing: Missing, Sends: "lyrics and tags",
	Service: musicsvc.ServiceSpec{Dir: "heartmula", DLL: "HeartMuLa.Net.Server.dll", Prefix: "HEARTMULA", Cards: true},
	Docker:  musicsvc.DockerSpec{Image: "veritmo/heartmula:1", Port: 8787},
	// measured on 2026-11-19: the image is 14.2 GB; the weights are about 21 GB (docker/heartmula/README.md); the .NET service shares its native libraries by hard links, so its own size is not known
	Sizes: musicsvc.Sizes{Image: 14_200_000_000, Weights: 21_000_000_000},
}

// Spec is the description of the generator, for the code that is the same for every one.
func Spec() musicsvc.Spec { return spec }

type (
	// GPUInfo is a card and the memory it has free now and in all.
	GPUInfo = musicsvc.GPUInfo
)

// ErrNoRoom is returned when no card has enough free memory.
var ErrNoRoom = musicsvc.ErrNoRoom

// Local is the engine as a service on this machine.
type Local struct {
	Runner runner.CommandRunner
	// Models is the absolute path of the weights folder (models/music/heartmula).
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

// PickGPU chooses the card with the most free memory among those that have at least MinFreeMB, or the one the
// caller names (a CUDA index as text, empty for the best).
func PickGPU(gpus []GPUInfo, want string) (GPUInfo, error) { return musicsvc.PickGPU(spec, gpus, want) }

// Missing returns the weight files that are not in the weights folder (nothing when it is complete). The shards
// of each model are the ones its index lists, so a download that stopped half way is seen as incomplete.
func Missing(dir string) []string {
	var missing []string
	need := func(rel string) bool {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			missing = append(missing, rel)
			return false
		}
		return true
	}
	for _, f := range []string{"config.json", "gen_config.json", "tokenizer.json"} {
		need(f)
	}
	for _, model := range []string{"HeartMuLa-oss-3B", "HeartCodec-oss"} {
		need(model + "/config.json")
		index := model + "/model.safetensors.index.json"
		if !need(index) {
			continue
		}
		var idx struct {
			WeightMap map[string]string `json:"weight_map"`
		}
		data, err := os.ReadFile(filepath.Join(dir, index))
		if err != nil || json.Unmarshal(data, &idx) != nil || len(idx.WeightMap) == 0 {
			missing = append(missing, index+" (unreadable)")
			continue
		}
		shards := map[string]bool{}
		for _, shard := range idx.WeightMap {
			shards[shard] = true
		}
		names := make([]string, 0, len(shards))
		for s := range shards {
			names = append(names, s)
		}
		sort.Strings(names)
		for _, s := range names {
			need(model + "/" + s)
		}
	}
	return missing
}

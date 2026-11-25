// Package generate makes a 3D model from a picture or a prompt (spec model-generation): the two engines and what each needs, their optional
// container, the client of its service, and the generation of one model into the library. The engines run in a container (docker/model3d/);
// this package never touches torch.
package generate

import (
	"os"
	"path/filepath"

	"veritmo/internal/platform/musicsvc"
)

// The engines.
const (
	EngineTripoSR = "triposr"
	EngineSF3D    = "sf3d"
)

// The details: the resolution of the surface, which decides the triangles.
const (
	DetailLow    = "low"
	DetailNormal = "normal"
	DetailHigh   = "high"
)

// What identifies the service.
const (
	// Container is the name the service is known by (its state carries it).
	Container = "vt-model3d"
	// Port is the port of its container, on loopback.
	Port = 8790
	// Image is the image built from docker/model3d.
	Image = "veritmo/model3d:1"
	// WeightsDir is where the weights live, relative to the project root; the container mounts it read-only at /ckpt.
	WeightsDir = "models/model3d"
	// MinFreeMB is the free GPU memory a model needs: TripoSR peaked at 2.5 GB in the container; as a .NET service it holds up to 5.8 GB at `high` detail (measured on a 4070), so 6 GB cards are at the limit.
	MinFreeMB = 6000
	// Version is the version of the service this program is made for.
	Version = "1"
	// EnginesLabel is the label of the image that says which engines it was built with (comma-separated).
	EnginesLabel = "org.veritmo.engines"
	// SF3DRepo is the Hugging Face repository of Stable Fast 3D, whose licence the person has to accept with their own account.
	SF3DRepo = "stabilityai/stable-fast-3d"
	// MaxImageBytes is the largest picture the service takes.
	MaxImageBytes = 20 << 20
)

// More reasons an engine cannot be used, besides those of musicsvc.
const (
	// ReasonNotBuilt means the image was built without this engine.
	ReasonNotBuilt = "not_built"
	// ReasonKeyMissing means the engine needs a Hugging Face key and none is saved.
	ReasonKeyMissing = "key_missing"
	// ReasonLicenceNotAccepted means Hugging Face refuses the saved key for the engine's repository: its account has not accepted the licence.
	ReasonLicenceNotAccepted = "licence_not_accepted"
)

// EngineInfo describes an engine.
type EngineInfo struct {
	ID, Name string
	// Licence is the engine's, recorded with each model it makes; Attribution says whether its terms ask to be followed.
	Licence     string
	Attribution bool
	// Gated says its weights need a Hugging Face key whose account accepted the licence.
	Gated bool
}

var infos = map[string]EngineInfo{
	EngineTripoSR: {ID: EngineTripoSR, Name: "TripoSR", Licence: "MIT"},
	EngineSF3D:    {ID: EngineSF3D, Name: "Stable Fast 3D", Licence: "Stability AI Community License (free up to US$1M of yearly revenue; see its terms)", Attribution: true, Gated: true},
}

// Info is the description of an engine; false when it is not one.
func Info(engine string) (EngineInfo, bool) { i, ok := infos[engine]; return i, ok }

// Engines are the ids of the engines, the default first.
func Engines() []string { return []string{EngineTripoSR, EngineSF3D} }

// ValidDetail reports whether the text is one of the details.
func ValidDetail(d string) bool { return d == DetailLow || d == DetailNormal || d == DetailHigh }

// servicesOf are the .NET services (published by scripts/get-services.sh into bin/services/<dir>): one process per engine, the same HTTP contract as the
// container, on a free port of loopback. Each reads <PREFIX>_CKPT, _PORT, _DEVICE and _HOST (loopback only: there is no token here).
var servicesOf = map[string]musicsvc.ServiceSpec{
	EngineTripoSR: {Dir: "triposr", DLL: "triposr-service.dll", Prefix: "TRIPOSR", Device: true, Env: []string{"TRIPOSR_HOST=127.0.0.1"}},
	EngineSF3D:    {Dir: "stable-fast-3d", DLL: "stablefast3d-service.dll", Prefix: "SF3D", Device: true, Env: []string{"SF3D_HOST=127.0.0.1"}},
}

// serviceOnly are the files only the .NET service reads, on top of Required: the tetrahedra grid of Stable Fast 3D (a file of its repository that the
// container has inside its image).
var serviceOnly = map[string][]string{
	EngineSF3D: {"sf3d/160_tets.npz"},
}

// required are the files of the weights folder each engine reads (the same list as docker/model3d/server.py).
var required = map[string][]string{
	EngineTripoSR: {"triposr/config.yaml", "triposr/model.ckpt", "triposr/dino-vitb16-config.json", "rembg/u2net.onnx"},
	EngineSF3D:    {"sf3d/config.yaml", "sf3d/model.safetensors", "sf3d/dinov2-large-config.json", "rembg/u2net.onnx"},
}

// Required is the files an engine needs in the weights folder to run in the container.
func Required(engine string) []string { return append([]string(nil), required[engine]...) }

// RequiredFor is the files an engine needs for a backend: the container's list, plus what only the .NET service reads.
func RequiredFor(engine, backend string) []string {
	out := Required(engine)
	if backend != musicsvc.BackendDocker {
		out = append(out, serviceOnly[engine]...)
	}
	return out
}

func missingOf(files []string) func(dir string) []string {
	return func(dir string) []string {
		var out []string
		for _, rel := range files {
			if fi, err := os.Stat(filepath.Join(dir, rel)); err != nil || !fi.Mode().IsRegular() {
				out = append(out, rel)
			}
		}
		return out
	}
}

// Missing returns the function that lists the files of an engine's container that are not in a weights folder.
func Missing(engine string) func(dir string) []string { return missingOf(required[engine]) }

// MissingFor is Missing for a backend (see RequiredFor).
func MissingFor(engine, backend string) func(dir string) []string {
	return missingOf(RequiredFor(engine, backend))
}

// Spec is the description of an engine for the machinery the music generators use. Both engines are the one container, so the container name, the
// image and the folder of the weights are the same; each has its own .NET service.
func Spec(engine string) musicsvc.Spec {
	info, _ := Info(engine)
	sizes := musicsvc.Sizes{Weights: 1_900_000_000}
	if engine == EngineSF3D {
		sizes.Weights = 3_930_000_000 // measured on 2026-11-24: model.safetensors, its config and the config of its DINOv2 model
	}
	spec := musicsvc.Spec{
		Name: info.Name, Version: Version, Container: Container, WeightsDir: WeightsDir, MinFreeMB: MinFreeMB, Missing: Missing(engine), Sends: "a picture",
		Docker: musicsvc.DockerSpec{Image: Image, Port: Port}, Sizes: sizes,
	}
	// the C# ports (dotnet/triposr and dotnet/stable-fast-3d): the same weights folder as the container
	spec.Service = servicesOf[engine]
	if extra := serviceOnly[engine]; len(extra) > 0 {
		spec.ServiceMissing = missingOf(extra)
	}
	return spec
}

// HasService says whether an engine has a .NET service besides the container: both do.
func HasService(engine string) bool { return Spec(engine).Service.DLL != "" }

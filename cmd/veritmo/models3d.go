package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"veritmo/internal/assets3d"
	"veritmo/internal/assets3d/generate"
	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/platform/runner"
	"veritmo/internal/platform/sched"
	"veritmo/internal/server"
	"veritmo/internal/server/generation"
	"veritmo/internal/server/workspace"
)

const models3dUsage = `Usage: veritmo models3d generate (--prompt "text" | --image file) [--engine triposr|sf3d] [--detail low|normal|high] [--name name]

Makes a 3D model (a .glb) from a picture or from a text and puts it in the pack "generated" of the model library.
With a text, the picture is made first with the best image model installed here (the text is completed to show one
object on a plain background). The 3D service runs in a container (docker/model3d): it is started for the call and
stopped afterwards unless it was already running. Prints the model's id ("generated/<name>") on stdout.

Flags:
`

// runModels3D implements `veritmo models3d ...`.
func runModels3D(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "generate" {
		fmt.Fprint(stderr, models3dUsage)
		return 2
	}
	fs := flag.NewFlagSet("models3d generate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	prompt := fs.String("prompt", "", "what the object is, in a few words (the picture of it is made here)")
	image := fs.String("image", "", "a picture (png, jpg or webp) of the object, instead of a text")
	engine := fs.String("engine", generate.EngineTripoSR, "triposr (MIT, the default) or sf3d (Stability AI Community License; needs the licence accepted with your own Hugging Face account)")
	detail := fs.String("detail", generate.DetailNormal, "low, normal or high: the resolution of the surface, so the triangles of the model")
	name := fs.String("name", "", "the plain name of the model in the library (default: made from the text or the picture's name)")
	fs.Usage = func() { fmt.Fprint(stderr, models3dUsage); fs.PrintDefaults() }
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "models3d generate: unexpected arguments")
		return 2
	}
	in := generate.Input{Prompt: *prompt, Engine: *engine, Detail: *detail, Name: *name}
	if *image != "" {
		data, err := os.ReadFile(*image)
		if err != nil {
			fmt.Fprintf(stderr, "models3d generate: %v\n", err)
			return 1
		}
		in.Image, in.ImageName = data, filepath.Base(*image)
	}
	if _, err := in.Validate(); err != nil {
		fmt.Fprintf(stderr, "models3d generate: %v\n", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	r := runner.NewExecRunner()
	deps := generate.Deps{
		Service: server.NewModel3DService(r, false, musicServices(defaultDB)),
		Library: assets3d.New(assets3d.ResolveDir(os.Getenv), assets3d.DefaultPacks()),
		OnStep: func(step string) {
			fmt.Fprintf(stderr, "models3d generate: %s...\n", map[string]string{"picture": "making the picture", "model": "making the model"}[step])
		},
	}
	if in.Prompt != "" {
		deps.Picture = pictureMaker(ctx, r)
	}
	out, err := generate.Generate(ctx, deps, in)
	if err != nil {
		var ue *musicsvc.UnavailableError
		if errors.As(err, &ue) {
			fmt.Fprintf(stderr, "models3d generate: the engine cannot be used (%s): %s\n", ue.Reason, ue.Detail)
			return 1
		}
		fmt.Fprintf(stderr, "models3d generate: %v\n", err)
		if errors.Is(err, generate.ErrInvalid) {
			return 2
		}
		return 1
	}
	fmt.Fprintf(stderr, "models3d generate: %d triangles at %s detail, %.0f s, %s\n", out.Triangles, out.DetailUsed, out.Seconds, out.EngineVersion)
	fmt.Fprintln(stdout, out.Model)
	return 0
}

// pictureMaker is the maker of the picture of a prompt with the engines of this machine.
func pictureMaker(ctx context.Context, r runner.CommandRunner) generate.PictureMaker {
	var gpus []workspace.GPUStatus
	out, err := r.Run(ctx, "nvidia-smi", []string{"--query-gpu=index,name,memory.free,memory.total", "--format=csv,noheader,nounits"})
	if err == nil {
		gpus = parseGPUs(string(out))
	}
	var ram int64
	if total, _, err := sched.MemInfo(); err == nil {
		ram = total
	}
	return generation.PictureMaker(r, server.Catalog(), "", gpus, ram, generate.NegativePrompt)
}

func parseGPUs(csv string) []workspace.GPUStatus {
	var gpus []workspace.GPUStatus
	for _, line := range strings.Split(strings.TrimSpace(csv), "\n") {
		parts := strings.Split(line, ",")
		if len(parts) < 4 {
			continue
		}
		var g workspace.GPUStatus
		if _, err := fmt.Sscanf(strings.TrimSpace(parts[0]), "%d", &g.Index); err != nil {
			continue
		}
		g.Name = strings.TrimSpace(parts[1])
		fmt.Sscanf(strings.TrimSpace(parts[2]), "%d", &g.FreeVRAMMB)
		fmt.Sscanf(strings.TrimSpace(parts[3]), "%d", &g.TotalVRAMMB)
		gpus = append(gpus, g)
	}
	return gpus
}

// Package videovision describes the vision service of the video-edit tools (dotnet/video-vision), a .NET program
// that runs two ONNX networks (a person mask and a depth estimator) over folders of frames, and the client that talks
// to it. It is started and stopped by the user from the Environment screen like the music generators; what it shares
// with them is in internal/platform/musicsvc.
package videovision

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/platform/runner"
)

// Version is the version of the service this program is made for.
const Version = "1"

// What the engine is made of.
const (
	// Container is the name the program keeps the service's state under (there is no container: it is a process).
	Container = "vt-videovision"
	// WeightsDir is where the model files live, relative to the project root.
	WeightsDir = "models/video"
	// MinFreeMB is the memory of a card the engine asks for. The networks run on the CPU; the card is only the one the
	// program records the service against, so any card will do.
	MinFreeMB = 1
)

// The model files of the service.
const (
	DepthFile = "depth-anything-v2-small.onnx"
	MaskFile  = "u2net_human_seg.onnx"
)

// Missing returns the model files, among the ones given (all of them when none is), that are not in dir.
func Missing(dir string, needs ...string) []string {
	if len(needs) == 0 {
		needs = []string{DepthFile, MaskFile}
	}
	var out []string
	for _, f := range needs {
		if fi, err := os.Stat(filepath.Join(dir, f)); err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 {
			out = append(out, f)
		}
	}
	return out
}

var spec = musicsvc.Spec{
	Name: "Video vision", Version: Version, Container: Container, WeightsDir: WeightsDir,
	MinFreeMB: MinFreeMB, Sends: "video frames",
	Missing: func(dir string) []string { return Missing(dir) },
	Service: musicsvc.ServiceSpec{Dir: "videovision", DLL: "videovision-service.dll", Prefix: "VIDEOVISION"},
}

// Spec is the description of the service, for the code that is the same for every service.
func Spec() musicsvc.Spec { return spec }

// Engine is the service on this machine; it never uses another machine's.
type Engine struct {
	Runner runner.CommandRunner
	// Models is the absolute path of the model folder (models/video).
	Models string
	// Services runs the .NET service (bin/services/videovision).
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

// --- the client ---------------------------------------------------------------------------------

// FrameMask is what a job says of one frame: the box of the people in it (x0, y0, x1, y1) or none, and how much of
// the frame they cover.
type FrameMask struct {
	Name     string  `json:"name"`
	Box      []int   `json:"box"`
	Coverage float64 `json:"coverage"`
	// People is how many separate people the frame shows (people who touch count as one).
	People int `json:"people"`
}

// Summary is what a job says of the clip.
type Summary struct {
	Frames              int         `json:"frames"`
	Provider            string      `json:"provider"`
	Seconds             float64     `json:"seconds"`
	Fallback            string      `json:"fallback,omitempty"`
	Masks               []FrameMask `json:"masks,omitempty"`
	DepthLow            *float64    `json:"depth_low,omitempty"`
	DepthHigh           *float64    `json:"depth_high,omitempty"`
	FramesWithoutPeople *int        `json:"frames_without_people,omitempty"`
}

// jobTimeout bounds one job: a long clip on a slow CPU takes minutes.
const jobTimeout = 30 * time.Minute

type jobRequest struct {
	FramesDir string `json:"frames_dir"`
	OutDir    string `json:"out_dir"`
	Dilate    int    `json:"dilate,omitempty"`
}

func job(ctx context.Context, c *musicsvc.Client, path string, req jobRequest) (Summary, error) {
	var out Summary
	if _, err := c.Call(ctx, http.MethodPost, path, req, &out, jobTimeout); err != nil {
		return Summary{}, fmt.Errorf("the %s service: %w", c.Service, err)
	}
	return out, nil
}

// Mask asks for a grey mask per frame and the box of the people in each (POST /mask).
func Mask(ctx context.Context, c *musicsvc.Client, framesDir, outDir string) (Summary, error) {
	return job(ctx, c, "/mask", jobRequest{FramesDir: framesDir, OutDir: outDir})
}

// Depth asks for a grey depth per frame on the scale of the whole clip (POST /depth).
func Depth(ctx context.Context, c *musicsvc.Client, framesDir, outDir string) (Summary, error) {
	return job(ctx, c, "/depth", jobRequest{FramesDir: framesDir, OutDir: outDir})
}

// Control asks for the control of a motion: the depth of the people with the background black, three-channel PNG,
// the mask grown by dilate pixels (POST /control).
func Control(ctx context.Context, c *musicsvc.Client, framesDir, outDir string, dilate int) (Summary, error) {
	return job(ctx, c, "/control", jobRequest{FramesDir: framesDir, OutDir: outDir, Dilate: dilate})
}

package generation

// blenderscene.go: Blender as one more way of making a video in one call (spec video-generation-api, "Blender is one more way of
// making a video"): the option, the plan of its one-scene project and the checks of the description that comes in the request.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"veritmo/internal/project/config"
	"veritmo/internal/project/pipeline"
	"veritmo/internal/video/blender"
)

// optionBlender is the identifier of the option that renders a 3D scene.
const optionBlender = "blender"

// The duration of a 3D scene: Cycles takes long per frame, so the range is shorter than that of the other options.
const (
	minBlenderSeconds     = 1.0
	maxBlenderSeconds     = 30.0
	defaultBlenderSeconds = 5.0
	blenderFPS            = 30
)

// blenderTiers are what `quality` means for a 3D scene: the long side of the frame and the samples per pixel of Cycles.
var blenderTiers = map[string]struct{ longSide, samples int }{
	"draft":  {854, 8},
	"normal": {1280, config.DefaultBuiltSamples},
	"high":   {1280, 2 * config.DefaultBuiltSamples},
}

// blenderRequest is the `blender` object of a generation request: the description of the scene (the JSON of a file
// `.scene.json`). A generation reports the request without it, because it is kept as an input.
type blenderRequest struct {
	Description json.RawMessage `json:"description,omitempty"`
}

// blenderOption is the option of the list: usable when Blender runs here, with the reason when it does not. It is never the
// default and takes no picture or reference, only the scene.
func (h *Handler) blenderOption(ctx context.Context) videoOptionView {
	v := videoOptionView{
		ID: optionBlender, Name: "Blender (3D scene)", Engine: optionBlender,
		Description: "A scene of 3D objects, text, lights and a camera that Blender draws from a description (JSON) sent with the request.",
		ClipSeconds: defaultBlenderSeconds,
		Duration:    durationRange{Min: minBlenderSeconds, Max: maxBlenderSeconds, Default: defaultBlenderSeconds},
		Inputs:      &generationInputs{Scene: true},
	}
	p := h.probeBlender(ctx)
	if p.Err == nil {
		v.Available = true
		return v
	}
	v.Reason = p.Err.Error()
	if errors.Is(p.Err, blender.ErrNotInstalled) {
		v.Reason = blender.ErrNotInstalled.Error() + ": " + blender.InstallHint
	}
	return v
}

// videoOptions are the options of the catalog and, after them, Blender.
func (h *Handler) videoOptions(ctx context.Context, cat []catalogEntry, m planMachine, voice string) []videoOptionView {
	return append(buildVideoOptions(cat, m, voice), h.blenderOption(ctx))
}

// checkScene checks the description of a request as a hand-written one is, for a scene as long as seconds and with no design,
// no tempo and no font files. The error names each fault by its place in the JSON.
func checkScene(raw []byte, seconds float64) error {
	_, err := blender.Parse(raw, seconds, pipeline.BlenderEnv(&config.Config{}, ""))
	return err
}

// sceneBytes is a description as it is stored: indented, with a final line break.
func sceneBytes(raw []byte) []byte {
	var b bytes.Buffer
	if err := json.Indent(&b, raw, "", "  "); err != nil {
		return raw
	}
	return append(b.Bytes(), '\n')
}

// blenderFrame is the resolution of a frame of the shape aw:ah whose long side is longSide, with even sides.
func blenderFrame(aw, ah, longSide int) string {
	if aw >= ah {
		return fmt.Sprintf("%dx%d", longSide, longSide*ah/aw/2*2)
	}
	return fmt.Sprintf("%dx%d", longSide*aw/ah/2*2, longSide)
}

// planBlender is the project of a generation made with the option Blender: one scene of duration seconds that is built from
// the description stored as sources/<scene>. The aspect gives the frame; the quality the size and the samples.
func planBlender(id, scene string, req generationRequest, voice string) (*videoPlan, error) {
	aw, ah, ok := parseAspect(req.AspectRatio)
	if !ok {
		return nil, fmt.Errorf("aspect ratio %q is not one of %v", req.AspectRatio, aspectRatios)
	}
	tier, ok := blenderTiers[req.Quality]
	if !ok {
		return nil, fmt.Errorf("quality %q is not one of %v", req.Quality, qualityNames)
	}
	cfg := &config.Config{
		Project:  id,
		Output:   config.Output{Mode: config.ModeVideo, File: "out/" + id + ".mp4"},
		Defaults: config.Defaults{Voice: voice, FPS: blenderFPS, Resolution: blenderFrame(aw, ah, tier.longSide)},
		Scenes: []config.Scene{{
			ID: "scene", SilenceSeconds: fptr(req.Duration),
			Blender: &config.SceneBlender{Build: scene, Samples: tier.samples},
		}},
	}
	return &videoPlan{Config: cfg, ClipSeconds: req.Duration}, nil
}

// withoutKind is the inputs without the files of one kind.
func withoutKind(in []genInput, kind string) []genInput {
	out := in[:0:0]
	for _, f := range in {
		if f.Kind != kind {
			out = append(out, f)
		}
	}
	return out
}

// sceneOfRequest says which description a request makes its video from, checked against the option and the duration: the one
// sent (sent) or the one of the generation reused. It takes the reused scene out of the inputs, which the other checks do not
// know. raw is nil when the request is not for the option Blender; msg is why the request cannot be.
func (h *Handler) sceneOfRequest(option string, req generationRequest, sent []byte, inputs *[]genInput, startPicture bool) (name string, raw []byte, msg string) {
	var reused *genInput
	for i := range *inputs {
		if (*inputs)[i].Kind == inputScene {
			reused = &(*inputs)[i]
		}
	}
	if option != optionBlender {
		switch {
		case sent != nil:
			return "", nil, fmt.Sprintf("blender: only the option %q takes a 3D scene, not %q", optionBlender, option)
		case reused != nil:
			return "", nil, fmt.Sprintf("reuse_inputs_of: %s used a 3D scene, which option %s cannot make: use the option %q", req.ReuseInputsOf, option, optionBlender)
		}
		return "", nil, ""
	}
	if startPicture || len(*inputs) > map[bool]int{true: 1, false: 0}[reused != nil] {
		return "", nil, fmt.Sprintf("option %s takes a 3D scene and no pictures or references", optionBlender)
	}
	raw = sent
	if raw == nil && reused != nil {
		b, err := os.ReadFile(reused.Path)
		if err != nil {
			return "", nil, fmt.Sprintf("reuse_inputs_of: the 3D scene of %s cannot be read", req.ReuseInputsOf)
		}
		raw = b
	}
	if raw == nil {
		return "", nil, fmt.Sprintf("option %s needs a 3D scene: send blender.description or reuse one with reuse_inputs_of", optionBlender)
	}
	*inputs = withoutKind(*inputs, inputScene)
	if err := checkScene(raw, req.Duration); err != nil {
		return "", nil, fmt.Sprintf("the 3D scene does not pass the checks for %g seconds: %v", req.Duration, err)
	}
	return "", raw, ""
}

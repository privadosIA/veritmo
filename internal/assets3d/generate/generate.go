package generate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"veritmo/internal/assets3d"
)

// ErrInvalid is a request the generation refuses before it asks anything of an engine (the message says which part).
var ErrInvalid = errors.New("invalid")

// ErrNoPictureMaker is a prompt asked on a machine that cannot make the picture.
var ErrNoPictureMaker = errors.New("this machine has no image engine to make the picture of a prompt")

// What is added to a prompt so that the picture shows one object on its own (the engines need that: the spike's five objects were made so).
const (
	PromptSuffix   = ", single object centered, isolated on a plain pure white background, soft studio lighting, product render, clean, full object in frame"
	NegativePrompt = "text, watermark, multiple objects, cropped, shadow on the floor, background clutter"
	// MaxPromptRunes bounds a prompt.
	MaxPromptRunes = 1000
)

// CompletePrompt is the prompt the picture is made from.
func CompletePrompt(prompt string) string { return strings.TrimSpace(prompt) + PromptSuffix }

// Input is what one generation is asked.
type Input struct {
	// Prompt (the server makes the picture) or Image (a picture of the caller), never both.
	Prompt    string
	Image     []byte
	ImageName string
	// Engine is triposr by default and Detail normal.
	Engine, Detail string
	// Name is the plain name of the model ("" makes one from the prompt).
	Name string
}

// PictureMaker makes the picture of a prompt (already completed by CompletePrompt) and returns it as PNG bytes.
type PictureMaker func(ctx context.Context, prompt string) ([]byte, error)

// Opener opens the service for an engine; *Service is one.
type Opener interface {
	Open(ctx context.Context, engine string) (*Client, func(), error)
}

// Deps is what a generation uses.
type Deps struct {
	Service Opener
	Picture PictureMaker
	Library *assets3d.Library
	// OnStep, when set, hears the steps as they begin: "picture", "model".
	OnStep func(step string)
}

// Output is what a generation made.
type Output struct {
	// Model is "<pack>/<name>" in the library.
	Model         string
	Name          string
	Triangles     int
	EngineVersion string
	// DetailUsed is the detail the model was made at (lower than asked when the first had too many triangles).
	DetailUsed string
	Seconds    float64
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Validate normalises an input (the defaults) and refuses what cannot be made, naming the problem.
func (in Input) Validate() (Input, error) {
	in.Prompt = strings.TrimSpace(in.Prompt)
	switch {
	case in.Prompt != "" && len(in.Image) > 0:
		return in, invalid("send a prompt or a picture, not both")
	case in.Prompt == "" && len(in.Image) == 0:
		return in, invalid("send a prompt or a picture")
	case len([]rune(in.Prompt)) > MaxPromptRunes:
		return in, invalid("the prompt is longer than %d characters", MaxPromptRunes)
	case len(in.Image) > MaxImageBytes:
		return in, invalid("the picture is larger than %d MB", MaxImageBytes>>20)
	}
	if in.Engine == "" {
		in.Engine = EngineTripoSR
	}
	if in.Detail == "" {
		in.Detail = DetailNormal
	}
	if _, ok := Info(in.Engine); !ok {
		return in, invalid("engine must be one of %s, got %q", strings.Join(Engines(), ", "), in.Engine)
	}
	if !ValidDetail(in.Detail) {
		return in, invalid("detail must be low, normal or high, got %q", in.Detail)
	}
	if len(in.Image) > 0 {
		switch ct := http.DetectContentType(in.Image); ct {
		case "image/png", "image/jpeg", "image/webp":
		default:
			return in, invalid("the picture must be a png, jpg or webp, got %s", ct)
		}
	}
	return in, nil
}

// Generate makes one model: the picture (made from the prompt, or the caller's), the service, and the result in the pack "generated" of the library.
// An engine that cannot be used is refused with its reason (a *musicsvc.UnavailableError) before anything is made.
func Generate(ctx context.Context, d Deps, in Input) (Output, error) {
	in, err := in.Validate()
	if err != nil {
		return Output{}, err
	}
	if in.Name != "" && !plainName(in.Name) {
		return Output{}, invalid("the name must be a plain name (letters, digits, '.', '_' or '-')")
	}
	if in.Prompt != "" && d.Picture == nil {
		return Output{}, ErrNoPictureMaker
	}
	start := time.Now()
	client, closeFn, err := d.Service.Open(ctx, in.Engine)
	if err != nil {
		return Output{}, err
	}
	defer closeFn()
	step := func(s string) {
		if d.OnStep != nil {
			d.OnStep(s)
		}
	}
	image := in.Image
	if in.Prompt != "" {
		step("picture")
		if image, err = d.Picture(ctx, CompletePrompt(in.Prompt)); err != nil {
			return Output{}, fmt.Errorf("the picture of the prompt could not be made: %w", err)
		}
	}
	step("model")
	res, err := client.Generate(ctx, image, in.Engine, in.Detail, nil)
	if err != nil {
		return Output{}, err
	}
	info, _ := Info(in.Engine)
	view, err := d.Library.AddGenerated(res.GLB, assets3d.GenMeta{Name: in.Name, Prompt: in.Prompt, Picture: in.ImageName, Engine: in.Engine,
		EngineVersion: res.EngineVersion, Detail: res.DetailUsed, Licence: info.Licence, Attribution: info.Attribution})
	if err != nil {
		return Output{}, err
	}
	return Output{Model: view.ID, Name: view.Name, Triangles: view.Triangles, EngineVersion: res.EngineVersion, DetailUsed: res.DetailUsed, Seconds: time.Since(start).Seconds()}, nil
}

func plainName(s string) bool {
	if s == "" || len(s) > 96 {
		return false
	}
	for i, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || (i > 0 && (r == '.' || r == '_' || r == '-'))
		if !ok {
			return false
		}
	}
	return true
}

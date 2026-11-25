package quality

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"net/http"
	"strings"
	"time"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/assistant/llm"
)

// visionPrompt asks for a verdict on the people in one frame. A normal pose, a
// smile or a blurred background must not count: the model was found to flag
// everything when the prompt did not say so.
const visionPrompt = "You inspect a still frame taken from an AI-generated video. " +
	"Decide if the frame has a visible generation defect in the people: deformed, melted or distorted faces; " +
	"extra, missing, bent-wrong or fused arms, legs, hands or fingers; two bodies merging; impossible anatomy; " +
	"or heavy motion smear. A normal pose, a smile or a blurred background is NOT a defect. " +
	`Answer only JSON: {"defect": true or false, "severity": 0-10, "issue": "max 12 words"}`

// Ollama is a Vision backed by a vision-capable model served by Ollama.
type Ollama struct {
	Host  string // base URL, see assist.NormalizeHost
	Model string
	HTTP  *http.Client
	// Generic, when set, is a provider that is not an Ollama (an OpenAI-compatible
	// one): the judge talks to it and the unload, which is Ollama's, does nothing.
	Generic llm.Provider
}

// NewOllama returns a Vision for the given OLLAMA_HOST value and model.
func NewOllama(ollamaHost, model string) *Ollama {
	return &Ollama{Host: assist.NormalizeHost(ollamaHost), Model: model, HTTP: &http.Client{Timeout: 5 * time.Minute}}
}

// facePrompt judges one enlarged face crop.
const facePrompt = "This is a crop of one face from an AI-generated video frame. " +
	"Decide if the face is visibly deformed: melted, smeared or asymmetric features, missing or extra eyes, " +
	"a distorted mouth or nose, or skin that merges with hair. A normal expression, a smile or soft focus is NOT a defect. " +
	`Answer only JSON: {"defect": true or false, "severity": 0-10, "issue": "max 12 words"}`

// facesPrompt asks the model to locate faces; it grounds well with bbox_2d scaled 0-1000.
const facesPrompt = `Detect every human face in this image. Answer only JSON: {"faces":[{"bbox_2d":[x1,y1,x2,y2],"label":"face"}]} with coordinates scaled 0-1000.`

// Judge sends one frame to the model and reads its JSON verdict.
func (o *Ollama) Judge(ctx context.Context, framePNG []byte) (Judgement, error) {
	return o.judgeWith(ctx, visionPrompt, framePNG)
}

// JudgeFace judges an enlarged crop of one face.
func (o *Ollama) JudgeFace(ctx context.Context, facePNG []byte) (Judgement, error) {
	return o.judgeWith(ctx, facePrompt, facePNG)
}

// Faces asks the model where the faces are and returns their boxes in pixels.
func (o *Ollama) Faces(ctx context.Context, framePNG []byte) ([]Box, error) {
	cfg, err := png.DecodeConfig(bytes.NewReader(framePNG))
	if err != nil {
		return nil, err
	}
	content, err := o.chat(ctx, facesPrompt, framePNG, 300)
	if err != nil {
		return nil, err
	}
	return parseFaces(content, cfg.Width, cfg.Height), nil
}

// parseFaces reads the model's answer, which is a {"faces":[...]}, a bare list
// or a single box, and scales its 0-1000 coordinates to w x h pixels.
func parseFaces(content string, w, h int) []Box {
	type item struct {
		B []float64 `json:"bbox_2d"`
	}
	raw := []byte(strings.TrimSpace(content))
	var items []item
	var wrapped struct {
		Faces []item `json:"faces"`
	}
	var single item
	switch {
	case json.Unmarshal(raw, &wrapped) == nil && wrapped.Faces != nil:
		items = wrapped.Faces
	case json.Unmarshal(raw, &items) == nil:
	case json.Unmarshal(raw, &single) == nil && single.B != nil:
		items = []item{single}
	}
	var boxes []Box
	for _, it := range items {
		if len(it.B) != 4 {
			continue
		}
		b := Box{int(it.B[0] * float64(w) / 1000), int(it.B[1] * float64(h) / 1000), int(it.B[2] * float64(w) / 1000), int(it.B[3] * float64(h) / 1000)}
		if b.X1 > b.X0 && b.Y1 > b.Y0 {
			boxes = append(boxes, b)
		}
	}
	return boxes
}

func (o *Ollama) judgeWith(ctx context.Context, prompt string, framePNG []byte) (Judgement, error) {
	content, err := o.chat(ctx, prompt, framePNG, 120)
	if err != nil {
		return Judgement{}, err
	}
	var j Judgement
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &j); err != nil {
		return Judgement{}, fmt.Errorf("the model did not answer JSON: %q", truncate(content, 80))
	}
	if j.Severity < 0 {
		j.Severity = 0
	}
	if j.Severity > 10 {
		j.Severity = 10
	}
	return j, nil
}

// chat sends one image with a prompt and returns the model's text. It goes
// through the Ollama provider of internal/assistant/llm, asking for JSON.
func (o *Ollama) chat(ctx context.Context, prompt string, img []byte, numPredict int) (string, error) {
	res, err := o.provider().Chat(ctx, llm.Request{
		Model: o.Model, User: prompt, Images: [][]byte{img}, JSON: true, MaxTokens: numPredict, Temperature: 0,
	})
	if err != nil {
		return "", err
	}
	return res.Text, nil
}

func (o *Ollama) provider() llm.Provider {
	if o.Generic != nil {
		return o.Generic
	}
	return &llm.Ollama{Host: o.Host, HTTP: o.HTTP}
}

// Unload asks Ollama to drop the model from memory. A vision model is large
// (the ones tried take 17 GB) and the next thing to run may be the diffusion
// program, so the check does not leave it loaded for Ollama's usual minutes.
func (o *Ollama) Unload(ctx context.Context) {
	if o.Generic != nil {
		return // another provider holds nothing of ours in memory
	}
	(&llm.Ollama{Host: o.Host, HTTP: o.HTTP}).Unload(ctx, o.Model)
}

// FromResolved builds the vision judge for the provider the quality task resolved
// to. An OpenAI-compatible provider is used through its generic client and with its
// declared vision model; an Ollama keeps its lifecycle. A model named by the caller
// is used only when the provider declares none.
func FromResolved(res llm.Resolved, model string) *Ollama {
	if res.Model != "" {
		model = res.Model
	}
	o := &Ollama{Host: res.Host, Model: model, HTTP: &http.Client{Timeout: 5 * time.Minute}}
	if res.Type == "openai" {
		o.Generic = res.Provider
	}
	return o
}

// UnloadAll asks Ollama to drop every model it has loaded and returns their
// names. Nothing loaded is not an error.
func UnloadAll(ctx context.Context, ollamaHost string) ([]string, error) {
	host := assist.NormalizeHost(ollamaHost)
	p := &llm.Ollama{Host: host, HTTP: &http.Client{Timeout: 30 * time.Second}}
	loaded, err := p.Loaded(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, m := range loaded {
		p.Unload(ctx, m.Name)
		names = append(names, m.Name)
	}
	return names, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// VisionModels lists the models Ollama serves that can read images, smallest
// first, so a user who named none can be shown what is there.
func VisionModels(ctx context.Context, ollamaHost string) ([]string, error) {
	p := &llm.Ollama{Host: assist.NormalizeHost(ollamaHost), HTTP: &http.Client{Timeout: 30 * time.Second}}
	models, err := p.Models(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, m := range models {
		if m.Vision != nil && *m.Vision {
			names = append(names, m.Name)
		}
	}
	return names, nil
}

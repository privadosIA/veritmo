package clap

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"time"

	"veritmo/internal/platform/musicsvc"
)

// Template is the sentence each tag is put into before it is compared with the sound.
const Template = "A recording of {} music."

// classifyTimeout bounds one classification: the first call loads the model.
const classifyTimeout = 3 * time.Minute

// Tag is a tag and how close the sound is to it (a cosine similarity, not a probability).
type Tag struct {
	Tag   string  `json:"tag"`
	Score float64 `json:"score"`
}

// Result is what the service says of a clip: the best tags of each category, best first.
type Result struct {
	Seconds    float64          `json:"seconds"`
	Categories map[string][]Tag `json:"categories"`
}

// Classify scores a WAV clip against tags by category (POST /classify of the service) and returns the best `top`
// of each. The service reads any WAV and mixes it to mono itself.
func Classify(ctx context.Context, c *musicsvc.Client, wav []byte, labels map[string][]string, top int) (Result, error) {
	var res Result
	body := map[string]any{
		"wav_base64": base64.StdEncoding.EncodeToString(wav),
		"labels":     labels,
		"template":   Template,
		"top":        top,
	}
	if _, err := c.Call(ctx, http.MethodPost, "/classify", body, &res, classifyTimeout); err != nil {
		return Result{}, err
	}
	if len(res.Categories) == 0 {
		return Result{}, fmt.Errorf("the %s service gave no tags", c.Service)
	}
	return res, nil
}

package music

// videovision.go: where the models of the vision service are and the engine that runs it (the service itself is
// the models.Vision bridge of the video-edit tools).

import (
	"path/filepath"

	"veritmo/internal/video/videovision"
)

func (h *Handler) videoVisionWeightsDir() string {
	if h.videoVisionWeights != "" {
		return h.videoVisionWeights
	}
	abs, err := filepath.Abs(videovision.WeightsDir)
	if err != nil {
		return videovision.WeightsDir
	}
	return abs
}

func (h *Handler) videoVisionEngine() videovision.Engine {
	return videovision.Engine{Runner: h.heartMulaRunnerOrDefault(), Models: h.videoVisionWeightsDir(), Services: h.host.Services()}
}

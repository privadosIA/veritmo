package music

// clap.go: the LAION-CLAP service. It is an experiment kept stopped: it appears in Environment next
// to the music generators, with the same start and stop buttons, and nothing else uses it.

import (
	"path/filepath"

	"veritmo/internal/music/clap"
)

func (h *Handler) clapWeightsDir() string {
	if h.clapWeights != "" {
		return h.clapWeights
	}
	abs, err := filepath.Abs(clap.WeightsDir)
	if err != nil {
		return clap.WeightsDir
	}
	return abs
}

func (h *Handler) clapEngine() clap.Engine {
	return clap.Engine{Runner: h.heartMulaRunnerOrDefault(), Models: h.clapWeightsDir(), Services: h.host.Services()}
}

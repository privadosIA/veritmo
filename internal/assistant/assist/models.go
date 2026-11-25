package assist

import (
	"context"
	"sort"
	"strings"

	"veritmo/internal/platform/edition"
	"veritmo/internal/platform/sched"
)

// ModelInfo is one chat model installed in Ollama.
type ModelInfo struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	// Fits is whether the memory available now passes the assistant's rule
	// (size x 1.3 plus 4 GiB); true when the memory cannot be read.
	Fits bool `json:"fits"`
}

// nonChat are the name fragments of models that cannot answer a chat request.
var nonChat = []string{"embed", "rerank"}

// IsChatModel says whether a model of Ollama can answer a chat request: embedding and
// reranker models cannot.
func IsChatModel(name string) bool { return isChatModel(name) }

func isChatModel(name string) bool {
	n := strings.ToLower(name)
	for _, frag := range nonChat {
		if strings.Contains(n, frag) {
			return false
		}
	}
	return true
}

// ListModels returns the chat models installed in Ollama, smallest first,
// with whether each fits the memory available now.
func (a *Assistant) ListModels(ctx context.Context) ([]ModelInfo, error) {
	var tags struct {
		Models []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"models"`
	}
	if err := a.get(ctx, "/api/tags", &tags); err != nil {
		return nil, err
	}
	have, memErr := a.memAvailable()
	out := []ModelInfo{}
	for _, m := range tags.Models {
		if !isChatModel(m.Name) || edition.DenyModel(m.Name) != nil {
			continue
		}
		fits := true
		if memErr == nil {
			fits = have >= int64(float64(m.Size)*MemFactor)+MemMargin
		}
		out = append(out, ModelInfo{Name: m.Name, SizeBytes: m.Size, Fits: fits})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SizeBytes != out[j].SizeBytes {
			return out[i].SizeBytes < out[j].SizeBytes
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (a *Assistant) memAvailable() (int64, error) {
	if a.MemAvailable != nil {
		return a.MemAvailable()
	}
	return sched.MemAvailable()
}

//go:build !private

package edition

import (
	"testing"

	"veritmo/internal/platform/providers"
)

func TestDenyModel(t *testing.T) {
	for _, n := range []string{"huihui_ai/qwen3-abliterated:8b", "dolphin-mixtral:8x7b", "muse-glimmer:30b", "Llama3-Uncensored", "llama3-lexi"} {
		if DenyModel(n) == nil {
			t.Errorf("%q should be denied", n)
		}
	}
	for _, n := range []string{"gemma4:26b", "qwen3:8b", "lexicon-model", "qwen3.8:27b", "glimmer:1b", "muse:7b"} {
		if err := DenyModel(n); err != nil {
			t.Errorf("%q should be allowed: %v", n, err)
		}
	}
}

func TestProviderHosts(t *testing.T) {
	if !ProviderHostAllowed("https://api.openai.com/v1") || !ProviderHostAllowed("https://openrouter.ai/api/v1") {
		t.Fatal("listed hosts must be allowed")
	}
	for _, b := range []string{"https://my-server.example/v1", "http://127.0.0.1:1234/v1", "https://api.openai.com.evil.com/v1", "::bad"} {
		if ProviderHostAllowed(b) {
			t.Errorf("%q must not be allowed", b)
		}
	}
}

func TestProviderHostsComeFromPresets(t *testing.T) {
	for _, pr := range providers.Presets() {
		if !ProviderHostAllowed(pr.BaseURL) {
			t.Errorf("the host of preset %q must be allowed", pr.ID)
		}
	}
}

func TestPublicEdition(t *testing.T) {
	if Private() || Name != "public" || DescribeLiteral() != "" || MergeLiteral() != "" || DetailModel() != "" || RefusalAdvice() || CivitaiEnabled() {
		t.Fatal("the public edition must be the safe one")
	}
}

//go:build !private

package assist

import (
	"context"
	"errors"
	"testing"

	"veritmo/internal/platform/edition"
)

// The public edition never sends anything to a model on the deny list, whichever
// way it was chosen (flag, environment variable or the request).
func TestDeniedModelIsNeverCalled(t *testing.T) {
	f := &fakeOllama{answer: "A photo."}
	a := setup(t, f)
	a.Model = "huihui_ai/qwen3-abliterated:8b"
	_, err := a.Run(context.Background(), TaskImage, "something")
	if !errors.Is(err, edition.ErrModelNotAllowed) {
		t.Fatalf("want ErrModelNotAllowed, got %v", err)
	}
	if f.unloadCount() != 0 {
		t.Error("a denied model must not even be loaded or unloaded")
	}
	if _, err := a.Music(context.Background(), "a song", false); !errors.Is(err, edition.ErrModelNotAllowed) {
		t.Errorf("the music task too: %v", err)
	}
}

func TestDeniedModelIsNotListed(t *testing.T) {
	f := &fakeOllama{models: []string{"qwen3:8b", "dolphin-llama3:8b"}}
	a := setup(t, f)
	got, err := a.ListModels(context.Background())
	if err != nil || len(got) != 1 || got[0].Name != "qwen3:8b" {
		t.Fatalf("%v %v", got, err)
	}
}

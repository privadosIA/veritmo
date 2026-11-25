//go:build !private

package describe

import (
	"context"
	"errors"
	"testing"

	"veritmo/internal/platform/edition"
)

// What the editions change: the public one has no "more detail" descriptor and
// a refusal names only the model that declined.
const (
	privateEdition = false
	detailModel    = ""
)

func TestDeniedModelIsRefusedBeforeAnyCall(t *testing.T) {
	f := newFake()
	f.installed["huihui_ai/qwen3-abliterated:8b"] = true
	d := f.server(t)
	_, err := d.Describe(context.Background(), file(t, "a.png"), Options{Model: "huihui_ai/qwen3-abliterated:8b"})
	if !errors.Is(err, edition.ErrModelNotAllowed) {
		t.Fatalf("want ErrModelNotAllowed, got %v", err)
	}
	if len(f.chats) != 0 {
		t.Fatalf("nothing may be sent to a denied model: %d chats", len(f.chats))
	}
}

func TestDeniedModelsAreNotListedNorPicked(t *testing.T) {
	f := newFake()
	delete(f.installed, "gemma4:26b")
	d := f.server(t) // only muse-glimmer:30b is a vision model now, and it is denied
	_, _, models, err := d.ListModels(context.Background())
	if err != nil || len(models) != 0 {
		t.Fatalf("a denied model must not be listed: %v %v", models, err)
	}
	if _, err := d.Describe(context.Background(), file(t, "a.png"), Options{}); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("with only a denied model it must fail as if none were installed, got %v", err)
	}
}

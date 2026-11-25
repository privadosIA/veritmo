package llm

import (
	"context"
	"errors"
	"fmt"

	"veritmo/internal/platform/edition"
	"veritmo/internal/platform/settings"
)

// Errors of routing a task to a provider.
var (
	// ErrUnknownProvider means the provider id does not exist.
	ErrUnknownProvider = errors.New("unknown provider")
	// ErrProviderBlocked means the public edition does not use that provider's address.
	ErrProviderBlocked = errors.New("this edition does not use that provider")
	// ErrNoModel means the provider has no model for what the task needs.
	ErrNoModel = errors.New("the provider has no model for this task")
	// ErrConsentRequired means sending this task's data to a remote provider was not acknowledged.
	ErrConsentRequired = errors.New("consent required")
)

// ConsentError says which provider needs an acknowledgement and for what.
type ConsentError struct {
	ProviderID, Name, Task, Address string
}

func (e *ConsentError) Error() string {
	return fmt.Sprintf("%s: the %s of this task would leave this computer for %q (%s); accept it in Settings, under Tasks, before using it",
		ErrConsentRequired, dataOf(e.Task), e.Name, e.Address)
}

func (e *ConsentError) Is(target error) bool { return target == ErrConsentRequired }

// NoModelError says which model is missing at which provider.
type NoModelError struct {
	ProviderID, Name, Task string
}

func (e *NoModelError) Error() string {
	kind := "text"
	if settings.NeedsVision(e.Task) {
		kind = "vision"
	}
	return fmt.Sprintf("%s: set a %s model for the provider %q in Settings", ErrNoModel, kind, e.Name)
}

func (e *NoModelError) Is(target error) bool { return target == ErrNoModel }

// dataOf names what a task sends.
func dataOf(task string) string {
	switch task {
	case settings.TaskAssistant, settings.TaskMotionGraphics:
		return "texts"
	case settings.TaskQuality:
		return "frames of your clips"
	default:
		return "pictures and clips"
	}
}

// Resolved is the provider a task will use, with the model to ask for.
type Resolved struct {
	ID   string
	Name string
	// Type is "ollama" or "openai".
	Type    string
	Builtin bool
	// Remote is true when the provider is not this machine.
	Remote bool
	// Host is the base address (for an Ollama, the one to talk to).
	Host string
	// Model is the model the provider declares for the task; empty when it declares none
	// (an Ollama then picks among its installed models).
	Model string
	// Provider is a client for it.
	Provider Provider
	// Ollama is the same client as an *Ollama, set only for Ollama providers.
	Ollama *Ollama
}

// For resolves the provider that runs a task: the one named by override (a single
// call's choice), else the one routed to the task, else the built-in Ollama. It
// refuses an unknown provider, an OpenAI-compatible one without the model the task
// needs, and a remote provider the user has not acknowledged. The built-in Ollama is
// the default and needs no acknowledgement (its screen already warns when it is remote).
func For(task, override string) (Resolved, error) {
	id := override
	if id == "" {
		id = settings.RoutedProvider(task)
	}
	if id == settings.BuiltinID {
		host := settings.OllamaURL()
		o := NewOllama(host)
		return Resolved{ID: id, Name: "Ollama", Type: settings.TypeOllama, Builtin: true, Remote: settings.Remote(host), Host: host, Provider: o, Ollama: o}, nil
	}
	p, ok := settings.FindProvider(id)
	if !ok {
		return Resolved{}, fmt.Errorf("%w: %q", ErrUnknownProvider, id)
	}
	if !edition.ProviderHostAllowed(p.BaseURL) {
		// A provider saved by another edition: it is not used, as if nothing answered there.
		return Resolved{}, fmt.Errorf("%w: %q", ErrProviderBlocked, p.Name)
	}
	r := Resolved{ID: p.ID, Name: p.Name, Type: p.Type, Remote: settings.Remote(p.BaseURL), Host: p.BaseURL, Model: settings.ModelFor(p, task)}
	if p.Type == settings.TypeOpenAI && r.Model == "" {
		return Resolved{}, &NoModelError{ProviderID: p.ID, Name: p.Name, Task: task}
	}
	if r.Remote && !settings.Acknowledged(p.ID, p.BaseURL) {
		return Resolved{}, &ConsentError{ProviderID: p.ID, Name: p.Name, Task: task, Address: p.BaseURL}
	}
	if p.Type == settings.TypeOllama {
		o := NewOllama(p.BaseURL)
		r.Provider, r.Ollama = o, o
	} else {
		r.Provider = NewOpenAI(p.BaseURL, settings.Key(p.ID))
	}
	return r, nil
}

// Check is For without the result: it tells whether the task could run on that provider now.
func Check(task, override string) error {
	_, err := For(task, override)
	return err
}

type ctxKey struct{}

// WithProvider returns a context that makes the tasks run under it use the given provider
// instead of the routing, for that call only.
func WithProvider(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, id)
}

// ProviderFrom is the provider a context asks for, or "".
func ProviderFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

// ForContext is For with the override taken from the context.
func ForContext(ctx context.Context, task string) (Resolved, error) {
	return For(task, ProviderFrom(ctx))
}

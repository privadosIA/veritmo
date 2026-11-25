package describe

import (
	"context"
	"time"
	"veritmo/internal/platform/httpclient"
	"veritmo/internal/platform/legacyname"

	"veritmo/internal/assistant/llm"
	"veritmo/internal/platform/settings"
)

// TaskOf is the routed task that describes a file of this kind.
func TaskOf(kind Kind) string {
	if kind == KindVideo {
		return settings.TaskDescribeVideo
	}
	return settings.TaskDescribeImage
}

// FromResolved builds a Describer for the provider a describe task resolved to.
// An OpenAI-compatible provider gets the generic path with its declared vision
// model; an Ollama (the built-in or an extra one) keeps its lifecycle. A remote
// provider (other than the built-in Ollama) gets the call cap.
func FromResolved(res llm.Resolved) *Describer {
	d := &Describer{
		Host: res.Host, HTTP: httpclient.Long, CPU: legacyname.Getenv("DESCRIBE_CPU") == "1",
		ProviderID: res.ID, ProviderName: res.Name, ModelName: res.Model,
		Others: othersFor(res.ID),
	}
	if res.Type == settings.TypeOpenAI {
		d.Generic = res.Provider
	}
	if res.Remote && !res.Builtin {
		d.MaxCalls = settings.MaxCalls()
	}
	return d
}

// ForContext builds the Describer for a file of this kind: the provider routed to
// the task (or the one the context asks for). The built-in Ollama gives FromEnv.
func ForContext(ctx context.Context, kind Kind) (*Describer, error) {
	res, err := llm.ForContext(ctx, TaskOf(kind))
	if err != nil {
		return nil, err
	}
	if res.Builtin {
		d := FromEnv()
		d.Others = othersFor(res.ID)
		return d, nil
	}
	return FromResolved(res), nil
}

// othersFor lists, after a refusal by the provider `current`, the other providers
// that have a vision model: the saved ones that declare one, and the built-in
// Ollama (with its default installed vision model) when it is not the one that
// refused. The built-in is asked for its models with a short timeout; if it does
// not answer it is simply not offered.
func othersFor(current string) func(ctx context.Context) []Alternative {
	return func(ctx context.Context) []Alternative {
		var out []Alternative
		if current != settings.BuiltinID {
			cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			if all, err := llm.NewOllama(settings.OllamaURL()).Models(cctx); err == nil {
				var vision []Model
				for _, m := range all {
					if m.Vision != nil && *m.Vision {
						vision = append(vision, Model{Name: m.Name, SizeBytes: m.SizeBytes})
					}
				}
				if def := pickDefault(vision); def != "" {
					out = append(out, Alternative{Provider: settings.BuiltinID, Name: "Ollama", Model: def})
				}
			}
		}
		for _, p := range settings.Providers() {
			if p.ID != current && p.VisionModel != "" {
				out = append(out, Alternative{Provider: p.ID, Name: p.Name, Model: p.VisionModel})
			}
		}
		return out
	}
}

package describe

import (
	"context"
	"fmt"
	"strings"
	"time"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/assistant/llm"
)

// Session is a vision model chosen once for a job of many closed questions about pictures (the shots of a reference): it
// has the safeguards of Describe, applied to every picture it is shown, and it gives the model back when it is closed.
type Session struct {
	d      *Describer
	model  string
	models []Model
}

// OpenSession picks the vision model and checks the job against the provider's limits: planned is the most calls the job
// will make (a remote provider with a cap refuses a job over it before any call). Close it when the job is done.
func (d *Describer) OpenSession(ctx context.Context, opts Options, planned int) (*Session, error) {
	// A job of dozens of questions takes longer than one description: the timeout bounds each question (see Ask), not the whole.
	model, models, err := d.pickModel(ctx, opts)
	if err != nil {
		return nil, err
	}
	if err := d.checkCalls(planned); err != nil {
		return nil, err
	}
	return &Session{d: d, model: model, models: models}, nil
}

// Model is the model the session talks to.
func (s *Session) Model() string { return s.model }

// Close gives the model back (it is unloaded from memory) and ends the session.
func (s *Session) Close() {
	s.d.unload(s.model)
}

// Check asks the age question for one picture: nil when nobody in it is, or may be, a minor; an error that is an ErrMinor
// when somebody is or the age cannot be confirmed; a refusal is ErrRefused.
func (s *Session) Check(ctx context.Context, picture []byte) error {
	ctx, cancel := s.bound(ctx)
	defer cancel()
	return s.d.checkAdults(ctx, s.model, picture, s.models, 1, 1)
}

// Ask puts a question to the model about some pictures (JPEG or PNG) and returns its text. The pictures are not checked
// here: Check them first. An answer that is a refusal is ErrRefused, never replaced.
func (s *Session) Ask(ctx context.Context, system, user string, pictures [][]byte, asJSON bool, maxTokens int) (string, error) {
	ctx, cancel := s.bound(ctx)
	defer cancel()
	req := llm.Request{
		Model: s.model, System: system, User: user, JSON: asJSON, MaxTokens: maxTokens, Temperature: 0.2,
		Images: pictures, Local: &llm.LocalOptions{CPUOnly: s.d.CPU, KeepAlive: "5m"},
	}
	res, err := s.d.provider().Chat(ctx, req)
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(res.Text)
	if text == "" {
		return "", fmt.Errorf("%w: the model returned nothing", ErrEmpty)
	}
	if assist.IsRefusal(assist.Clean(text, false)) {
		return "", s.d.refusal(ctx, s.model, s.models)
	}
	return text, nil
}

// bound limits one question to the describer's timeout.
func (s *Session) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := s.d.Timeout
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	return context.WithTimeout(ctx, timeout)
}

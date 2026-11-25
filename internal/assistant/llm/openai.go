package llm

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"

	"veritmo/internal/platform/edition"
)

// OpenAI is a Provider for any server that speaks the OpenAI chat completions
// protocol: POST <base>/chat/completions and GET <base>/models, with the API key
// as a Bearer token. A base that ends in /v1 (with any prefix, such as OpenRouter's
// /api/v1 or Groq's /openai/v1) is used as it is; a base without a path gets /v1; one that ends in /openai (Google's v1beta/openai) is used as it is too.
type OpenAI struct {
	Base string
	// Key is the API key; empty sends no Authorization header.
	Key  string
	HTTP *http.Client
}

// NewOpenAI returns an OpenAI-compatible provider. It never follows redirects,
// so the key cannot be sent to an address the user did not choose.
func NewOpenAI(base, key string) *OpenAI {
	return &OpenAI{Base: base, Key: key, HTTP: &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// openaiMaxBody caps what is read from a provider: a chat answer or a model list.
const openaiMaxBody = 4 << 20

func (o *OpenAI) client() *http.Client {
	if o.HTTP != nil {
		return o.HTTP
	}
	return NewOpenAI("", "").HTTP
}

// URL is the address of an endpoint of this provider ("/chat/completions", "/models").
func (o *OpenAI) URL(path string) string {
	base := strings.TrimRight(o.Base, "/")
	if !strings.HasSuffix(base, "/v1") && !strings.HasSuffix(base, "/openai") {
		base += "/v1"
	}
	return base + path
}

func (o *OpenAI) headers() map[string]string {
	if o.Key == "" {
		return nil
	}
	return map[string]string{"Authorization": "Bearer " + o.Key}
}

// mime guesses the type of a picture for its data URI.
func mime(b []byte) string {
	switch ct := http.DetectContentType(b); ct {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return ct
	}
	return "image/png"
}

// fail turns a non-2xx answer into an error that says what to do, with the
// provider's body cut short and the key redacted.
func (o *OpenAI) fail(status int, data []byte, what string) error {
	return o.failFor(status, data, what, false)
}

// visionHints are words a 400 uses when a model is sent a picture it cannot read.
var visionHints = []string{"image", "vision", "multimodal", "multi-modal", "modalit"}

// failFor is fail that knows whether the request carried pictures: a 400 that
// talks about images then means the model cannot read them.
func (o *OpenAI) failFor(status int, data []byte, what string, hadImages bool) error {
	detail := redact(clip(string(data), maxErrBody), o.Key)
	if hadImages && status == http.StatusBadRequest {
		low := strings.ToLower(string(data))
		for _, h := range visionHints {
			if strings.Contains(low, h) {
				return fmt.Errorf("%w: choose a model that reads pictures (the provider said: %s)", ErrNoVision, detail)
			}
		}
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return fmt.Errorf("%w (%d)", ErrKeyRejected, status)
	case status == http.StatusTooManyRequests:
		return fmt.Errorf("%w (429): try again in a moment", ErrRateLimited)
	case status == http.StatusNotFound && what == "chat":
		return fmt.Errorf("%w (404): check the model name at this provider: %s", ErrModelNotFound, detail)
	case status >= 300 && status < 400:
		return fmt.Errorf("the provider redirected the request (%d) and redirects are not followed", status)
	}
	return fmt.Errorf("the provider answered %d: %s", status, detail)
}

// Chat sends one chat completion. When JSON is asked for it sends
// response_format json_object and, if the server answers 400 because it does not
// know that option, repeats the request once without it.
func (o *OpenAI) Chat(ctx context.Context, req Request) (Response, error) {
	if err := edition.DenyModel(req.Model); err != nil {
		return Response{}, err
	}
	req = prepared(ctx, req)
	var user any = req.User
	if len(req.Images) > 0 {
		parts := []map[string]any{{"type": "text", "text": req.User}}
		for _, img := range req.Images {
			url := "data:" + mime(img) + ";base64," + base64.StdEncoding.EncodeToString(img)
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}})
		}
		user = parts
	}
	var msgs []map[string]any
	if req.System != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": req.System})
	}
	msgs = append(msgs, map[string]any{"role": "user", "content": user})
	body := map[string]any{"model": req.Model, "messages": msgs, "temperature": req.Temperature}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if req.JSON {
		body["response_format"] = map[string]string{"type": "json_object"}
	}
	send := func() (int, []byte, error) {
		return doJSON(ctx, o.client(), http.MethodPost, o.URL("/chat/completions"), o.headers(), body, openaiMaxBody)
	}
	status, data, err := send()
	if err == nil && req.JSON && status == http.StatusBadRequest {
		delete(body, "response_format")
		status, data, err = send()
	}
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, err
		}
		return Response{}, fmt.Errorf("%w: cannot reach the provider at %s: %v", ErrUnreachable, o.Base, redact(redactNet(err).Error(), o.Key))
	}
	if status < 200 || status > 299 {
		return Response{}, o.failFor(status, data, "chat", len(req.Images) > 0)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content any `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := jsonUnmarshal(data, &out); err != nil {
		return Response{}, fmt.Errorf("unreadable answer from the provider: %w", err)
	}
	if out.Error != nil && out.Error.Message != "" {
		return Response{}, fmt.Errorf("the provider said: %s", redact(clip(out.Error.Message, maxErrBody), o.Key))
	}
	if len(out.Choices) == 0 {
		return Response{}, nil
	}
	c := out.Choices[0]
	text, _ := c.Message.Content.(string)
	return Response{Text: text, Truncated: c.FinishReason == "length"}, nil
}

// Models lists GET <base>/models. A provider does not say which models read
// pictures, so Vision stays nil.
func (o *OpenAI) Models(ctx context.Context) ([]Model, error) {
	status, data, err := doJSON(ctx, o.client(), http.MethodGet, o.URL("/models"), o.headers(), nil, openaiMaxBody)
	if err != nil {
		if ctx.Err() != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: cannot reach the provider at %s: %v", ErrUnreachable, o.Base, redact(redactNet(err).Error(), o.Key))
	}
	if status < 200 || status > 299 {
		return nil, o.fail(status, data, "models")
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := jsonUnmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("unreadable answer from the provider: %w", err)
	}
	out := make([]Model, 0, len(list.Data))
	for _, m := range list.Data {
		if edition.DenyModel(m.ID) != nil {
			continue
		}
		out = append(out, Model{Name: m.ID})
	}
	return out, nil
}

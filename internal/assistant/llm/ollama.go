package llm

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"veritmo/internal/platform/httpclient"

	"veritmo/internal/platform/edition"
)

// Ollama is a Provider backed by an Ollama server.
type Ollama struct {
	// Host is a base URL such as http://127.0.0.1:11434.
	Host string
	HTTP *http.Client
}

// NewOllama returns an Ollama provider for a base URL.
func NewOllama(host string) *Ollama { return &Ollama{Host: host, HTTP: httpclient.Long} }

func (o *Ollama) client() *http.Client {
	if o.HTTP != nil {
		return o.HTTP
	}
	return httpclient.Long
}

const ollamaMaxBody = 8 << 20

// Chat sends one /api/chat request: reasoning off, the temperature and token
// limit as options, the Ollama-only options of Local, pictures as base64.
func (o *Ollama) Chat(ctx context.Context, req Request) (Response, error) {
	if err := edition.DenyModel(req.Model); err != nil {
		return Response{}, err
	}
	req = prepared(ctx, req)
	opts := map[string]any{"temperature": req.Temperature, "num_predict": req.MaxTokens}
	if req.Local != nil && req.Local.CPUOnly {
		opts["num_gpu"] = 0
	}
	user := map[string]any{"role": "user", "content": req.User}
	if len(req.Images) > 0 {
		imgs := make([]string, len(req.Images))
		for i, b := range req.Images {
			imgs[i] = base64.StdEncoding.EncodeToString(b)
		}
		user["images"] = imgs
	}
	var msgs []map[string]any
	if req.System != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": req.System})
	}
	msgs = append(msgs, user)
	body := map[string]any{"model": req.Model, "stream": false, "think": false, "options": opts, "messages": msgs}
	if req.Local != nil && req.Local.KeepAlive != "" {
		body["keep_alive"] = req.Local.KeepAlive
	}
	if req.JSON {
		body["format"] = "json"
	}
	status, data, err := doJSON(ctx, o.client(), http.MethodPost, o.Host+"/api/chat", nil, body, ollamaMaxBody)
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, err
		}
		return Response{}, unreachable(o.Host, err)
	}
	if status < 200 || status > 299 {
		return Response{}, fmt.Errorf("ollama answered %d for /api/chat: %s", status, clip(string(data), maxErrBody))
	}
	var out struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		DoneReason string `json:"done_reason"`
		Error      string `json:"error"`
	}
	if err := jsonUnmarshal(data, &out); err != nil {
		return Response{}, fmt.Errorf("unreadable Ollama answer: %w", err)
	}
	if out.Error != "" {
		return Response{}, fmt.Errorf("ollama: %s", out.Error)
	}
	return Response{Text: out.Message.Content, Truncated: out.DoneReason == "length"}, nil
}

// Installed lists the models of /api/tags with their sizes, smallest first,
// without asking what each can do.
func (o *Ollama) Installed(ctx context.Context) ([]Model, error) {
	status, data, err := doJSON(ctx, o.client(), http.MethodGet, o.Host+"/api/tags", nil, nil, ollamaMaxBody)
	if err != nil {
		if ctx.Err() != nil {
			return nil, err
		}
		return nil, unreachable(o.Host, err)
	}
	if status < 200 || status > 299 {
		return nil, fmt.Errorf("ollama answered %d for /api/tags: %s", status, clip(string(data), maxErrBody))
	}
	var tags struct {
		Models []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"models"`
	}
	if err := jsonUnmarshal(data, &tags); err != nil {
		return nil, fmt.Errorf("unreadable Ollama answer: %w", err)
	}
	out := make([]Model, 0, len(tags.Models))
	for _, m := range tags.Models {
		if edition.DenyModel(m.Name) != nil {
			continue // the public edition does not list the models it does not use
		}
		out = append(out, Model{Name: m.Name, SizeBytes: m.Size})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SizeBytes != out[j].SizeBytes {
			return out[i].SizeBytes < out[j].SizeBytes
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Models lists the installed models and says, with /api/show, which can read pictures.
func (o *Ollama) Models(ctx context.Context) ([]Model, error) {
	models, err := o.Installed(ctx)
	if err != nil {
		return nil, err
	}
	for i, m := range models {
		status, data, err := doJSON(ctx, o.client(), http.MethodPost, o.Host+"/api/show", nil, map[string]string{"model": m.Name}, ollamaMaxBody)
		if err != nil || status != http.StatusOK {
			continue
		}
		var show struct {
			Capabilities []string `json:"capabilities"`
		}
		if jsonUnmarshal(data, &show) != nil {
			continue
		}
		vision := false
		for _, c := range show.Capabilities {
			vision = vision || c == "vision"
		}
		models[i].Vision = &vision
	}
	return models, nil
}

// Loaded is a model Ollama holds in memory and how much of it is in VRAM.
type Loaded struct {
	Name     string
	SizeVRAM int64
}

// Loaded asks /api/ps which models are in memory.
func (o *Ollama) Loaded(ctx context.Context) ([]Loaded, error) {
	status, data, err := doJSON(ctx, o.client(), http.MethodGet, o.Host+"/api/ps", nil, nil, ollamaMaxBody)
	if err != nil {
		if ctx.Err() != nil {
			return nil, err
		}
		return nil, unreachable(o.Host, err)
	}
	if status < 200 || status > 299 {
		return nil, fmt.Errorf("ollama answered %d for /api/ps: %s", status, clip(string(data), maxErrBody))
	}
	var ps struct {
		Models []struct {
			Name     string `json:"name"`
			Model    string `json:"model"`
			SizeVRAM int64  `json:"size_vram"`
		} `json:"models"`
	}
	if err := jsonUnmarshal(data, &ps); err != nil {
		return nil, fmt.Errorf("unreadable Ollama answer: %w", err)
	}
	out := make([]Loaded, 0, len(ps.Models))
	for _, m := range ps.Models {
		name := m.Name
		if name == "" {
			name = m.Model
		}
		out = append(out, Loaded{Name: name, SizeVRAM: m.SizeVRAM})
	}
	return out, nil
}

// Unload asks Ollama to drop the model from memory now. Errors are ignored: a
// model that stays loaded costs memory, not correctness.
func (o *Ollama) Unload(ctx context.Context, model string) {
	_, _, _ = doJSON(ctx, o.client(), http.MethodPost, o.Host+"/api/generate", nil, map[string]any{"model": model, "keep_alive": 0}, 1<<16)
}

// SameModel reports whether a loaded or installed name matches a wanted model
// (a name without a tag means :latest).
func SameModel(have, want string) bool {
	if !strings.Contains(want, ":") {
		want += ":latest"
	}
	return have == want
}

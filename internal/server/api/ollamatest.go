package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"time"

	"veritmo/internal/platform/settings"
)

const (
	// OllamaTestTimeout is how long the test of an Ollama address waits for it.
	OllamaTestTimeout   = 5 * time.Second
	ollamaTestMaxBody   = 4 << 20
	ollamaTestMaxModels = 200
)

// OllamaModelView is a model installed in an Ollama and whether it can read pictures.
type OllamaModelView struct {
	Name   string `json:"name"`
	Vision bool   `json:"vision"`
}

// OllamaTestResult is the answer of POST /api/settings/ollama/test (see openapi.yaml): whether the address answers, its version and its models.
type OllamaTestResult struct {
	OK      bool              `json:"ok"`
	Host    string            `json:"host"`
	Remote  bool              `json:"remote"`
	Version string            `json:"version,omitempty"`
	Models  []OllamaModelView `json:"models,omitempty"`
	Error   string            `json:"error,omitempty"`
}

// ProbeOllama asks the Ollama at host for its version and models, within 5 seconds,
// without following redirects.
func ProbeOllama(parent context.Context, host string) OllamaTestResult {
	res := OllamaTestResult{Host: host, Remote: settings.Remote(host)}
	ctx, cancel := context.WithTimeout(parent, OllamaTestTimeout)
	defer cancel()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	do := func(method, path string, body any, out any) error {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = strings.NewReader(string(b))
		}
		req, err := http.NewRequestWithContext(ctx, method, host+path, rd)
		if err != nil {
			return err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("answered %d", resp.StatusCode)
		}
		return json.NewDecoder(io.LimitReader(resp.Body, ollamaTestMaxBody)).Decode(out)
	}

	var ver struct {
		Version string `json:"version"`
	}
	if err := do(http.MethodGet, "/api/version", nil, &ver); err != nil {
		res.Error = fmt.Sprintf("no Ollama answers at %s (%s)", host, FailureKind(ctx, err))
		return res
	}
	res.Version = ver.Version
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := do(http.MethodGet, "/api/tags", nil, &tags); err != nil {
		res.Error = fmt.Sprintf("Ollama at %s answered, but its models could not be listed (%s)", host, FailureKind(ctx, err))
		return res
	}
	names := tags.Models
	if len(names) > ollamaTestMaxModels {
		names = names[:ollamaTestMaxModels]
	}
	models := make([]OllamaModelView, len(names))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, m := range names {
		models[i].Name = m.Name
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			var show struct {
				Capabilities []string `json:"capabilities"`
			}
			if do(http.MethodPost, "/api/show", map[string]string{"model": m.Name}, &show) == nil {
				for _, c := range show.Capabilities {
					if c == "vision" {
						models[i].Vision = true
					}
				}
			}
		}()
	}
	wg.Wait()
	res.OK = true
	res.Models = models
	return res
}

// isUnknownHost reports whether err says that the name of the host does not exist.
func isUnknownHost(err error) bool {
	var dns *net.DNSError
	return errors.As(err, &dns) && dns.IsNotFound
}

// FailureKind names why a request failed without echoing anything the other side sent.
func FailureKind(ctx context.Context, err error) string {
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return "timed out after 5 s"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case isUnknownHost(err):
		return "unknown host"
	case strings.HasPrefix(err.Error(), "answered "):
		return err.Error()
	default:
		return "no connection"
	}
}

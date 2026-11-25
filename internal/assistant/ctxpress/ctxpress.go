// Package ctxpress is the optional client of the ctxpress compression service (a local HTTP
// service that shrinks text before it reaches a language model). Its connection (address, token, on or off, the
// shortest text worth sending) comes from the settings, with the environment (CTXPRESS_URL, CTXPRESS_TOKEN,
// CTXPRESS_MIN_CHARS) as the default, and is read again on every call, so changing it needs no restart. It never
// makes a call fail: when the service is off, down, slow, refuses the token or answers an error, the text goes to
// the model exactly as it was.
package ctxpress

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"veritmo/internal/assistant/llm"
	"veritmo/internal/platform/settings"
)

const (
	// AnimationProfile is the ctxpress profile that keeps what drives a page's layout and motion.
	AnimationProfile = "animation-physics"
	callTimeout      = 15 * time.Second
	backoff          = 30 * time.Second
)

// Config is the connection in use for one call.
type Config struct {
	Enabled  bool
	URL      string
	Token    string
	MinChars int
}

// FromSettings reads the connection from the settings and the environment.
func FromSettings() Config {
	c := settings.Ctxpress()
	return Config{Enabled: c.Enabled, URL: c.URL, Token: c.Token, MinChars: c.MinChars}
}

// Client talks to the service the settings name. The zero value is not usable: use New.
type Client struct {
	config func() Config
	http   *http.Client
	now    func() time.Time

	mu sync.Mutex
	// downURL and downUntil: the service at downURL is not tried again until downUntil, after a failure.
	downURL   string
	downUntil time.Time
}

// New makes the client that reads the settings on every call.
func New() *Client {
	return &Client{config: FromSettings, http: &http.Client{Timeout: callTimeout}, now: time.Now}
}

// Install makes every language-model call go through the service when the settings (or the environment) turn it on. It is always installed and
// does nothing while the service is off, so it can be turned on later without a restart. It returns the client.
func Install() *Client {
	c := New()
	llm.SetPreparer(c)
	if cfg := c.config(); cfg.Enabled {
		slog.Info("ctxpress enabled", "url", cfg.URL, "min_chars", cfg.MinChars, "token_set", cfg.Token != "")
	}
	return c
}

type request struct {
	Text     string            `json:"text"`
	Kind     string            `json:"kind,omitempty"`
	Profile  string            `json:"profile,omitempty"`
	Preserve []string          `json:"preserve,omitempty"`
	Context  map[string]string `json:"context,omitempty"`
}

type response struct {
	Text       string  `json:"text"`
	EngineUsed string  `json:"engine_used"`
	Ratio      float64 `json:"ratio"`
}

// fenced matches a fenced code block, which is kept literally.
const fenced = "(?s)```.*?```"

// Prepare compresses the user message of a request when it is long prose. The system prompt (the
// instructions), the images and any fenced code are left alone.
func (c *Client) Prepare(ctx context.Context, req llm.Request) llm.Request {
	if c == nil {
		return req
	}
	cfg := c.config()
	if !cfg.Enabled || len(req.User) < cfg.MinChars {
		return req
	}
	if out, ok := c.compress(ctx, cfg, request{Text: req.User, Kind: "prose", Preserve: []string{fenced}, Context: map[string]string{"client": "veritmo", "tool": "compress_text"}}); ok {
		req.User = out
	}
	return req
}

// CompressHTML shrinks a page or style sheet that is only read as context (an example, a
// reference), keeping what drives layout and motion. Do not use it on a page the model must write
// back: the profile is lossy. It returns the text unchanged when the service cannot help.
func (c *Client) CompressHTML(ctx context.Context, text, kind string) string {
	if c == nil || (kind != "html" && kind != "css") {
		return text
	}
	cfg := c.config()
	if !cfg.Enabled {
		return text
	}
	if out, ok := c.compress(ctx, cfg, request{Text: text, Kind: kind, Profile: AnimationProfile, Context: map[string]string{"client": "veritmo", "tool": "compress_text"}}); ok {
		return out
	}
	return text
}

func (c *Client) isDown(url string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.downURL == url && c.now().Before(c.downUntil)
}

func (c *Client) markDown(url string) {
	c.mu.Lock()
	c.downURL, c.downUntil = url, c.now().Add(backoff)
	c.mu.Unlock()
}

// The service accepts only client and tool in a request context, and tool must be compress_text, compress_file or cli (ctxpress 2.3.0): anything else is
// a 400, which would turn every call into a silent fallback.

// compress asks the service; ok is false for anything but a shorter, non-empty answer from a real engine. A failure (the service does not
// answer, a refused token, 503) keeps the service apart for 30 seconds; the token is never logged.
func (c *Client) compress(ctx context.Context, cfg Config, r request) (string, bool) {
	if c.isDown(cfg.URL) {
		return "", false
	}
	body, _ := json.Marshal(r)
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL+"/v1/compress", bytes.NewReader(body))
	if err != nil {
		return "", false
	}
	hr.Header.Set("Content-Type", "application/json")
	if cfg.Token != "" {
		hr.Header.Set("Authorization", "Bearer "+cfg.Token)
	}
	resp, err := c.http.Do(hr)
	if err != nil {
		c.markDown(cfg.URL)
		slog.Warn("ctxpress unreachable; sending the text as it is", "url", cfg.URL, "err", err)
		return "", false
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		c.markDown(cfg.URL)
		slog.Warn("ctxpress refused the token; sending the text as it is (check the token and its compress scope in Settings)", "url", cfg.URL, "status", resp.StatusCode)
		return "", false
	case http.StatusServiceUnavailable:
		c.markDown(cfg.URL)
		return "", false
	default:
		return "", false
	}
	var out response
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		return "", false
	}
	if out.EngineUsed == "none" || strings.TrimSpace(out.Text) == "" || len(out.Text) >= len(r.Text) {
		return "", false
	}
	return out.Text, true
}

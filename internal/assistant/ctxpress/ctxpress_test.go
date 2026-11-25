package ctxpress

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"veritmo/internal/assistant/llm"
)

// service starts a fake ctxpress that answers status and body, counts its calls and keeps the Authorization header of the last one.
func service(t *testing.T, status int, body string) (cfg *Config, hits *atomic.Int32, auth *atomic.Value) {
	t.Helper()
	hits, auth = new(atomic.Int32), new(atomic.Value)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		auth.Store(r.Header.Get("Authorization"))
		// the real service (2.3.0) refuses a request context with any other tool, which would be a silent fallback
		var in struct {
			Context map[string]string `json:"context"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if tool := in.Context["tool"]; tool != "" && tool != "compress_text" && tool != "compress_file" && tool != "cli" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":"invalid_request","message":"context.tool must be compress_text, compress_file or cli"}`))
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return &Config{Enabled: true, URL: s.URL, MinChars: 10}, hits, auth
}

func client(cfg *Config) *Client {
	return &Client{config: func() Config { return *cfg }, http: &http.Client{Timeout: 2 * time.Second}, now: time.Now}
}

func TestPrepareUsesShorterAnswerAndSendsTheToken(t *testing.T) {
	cfg, hits, auth := service(t, 200, `{"text":"short","engine_used":"llmlingua2"}`)
	cfg.Token = "s3cret"
	got := client(cfg).Prepare(context.Background(), llm.Request{System: "sys", User: strings.Repeat("long text ", 5)})
	if got.User != "short" || got.System != "sys" || hits.Load() != 1 {
		t.Fatalf("got %+v, %d calls", got, hits.Load())
	}
	if auth.Load() != "Bearer s3cret" {
		t.Fatalf("Authorization = %v", auth.Load())
	}
	// no token: no header
	cfg.Token = ""
	client(cfg).Prepare(context.Background(), llm.Request{User: strings.Repeat("long text ", 5)})
	if auth.Load() != "" {
		t.Fatalf("Authorization without a token = %v", auth.Load())
	}
}

func TestPrepareKeepsOriginalWhenNoneOrError(t *testing.T) {
	user := strings.Repeat("long text ", 5)
	for name, tc := range map[string]struct {
		status int
		body   string
	}{"none": {200, `{"text":"s","engine_used":"none"}`}, "422": {422, `{}`}, "413": {413, `{}`}, "longer": {200, `{"text":"` + user + user + `","engine_used":"x"}`}, "bad": {200, `nope`}} {
		cfg, _, _ := service(t, tc.status, tc.body)
		if got := client(cfg).Prepare(context.Background(), llm.Request{User: user}); got.User != user {
			t.Errorf("%s: changed the text", name)
		}
	}
}

func TestOffShortTextAndNilClientSkip(t *testing.T) {
	cfg, hits, _ := service(t, 200, `{"text":"x","engine_used":"a"}`)
	c := client(cfg)
	cfg.MinChars = 1000
	c.Prepare(context.Background(), llm.Request{User: "short"})
	cfg.MinChars = 10
	cfg.Enabled = false
	c.Prepare(context.Background(), llm.Request{User: strings.Repeat("long text ", 5)})
	if got := c.CompressHTML(context.Background(), "<p>hello world</p>", "html"); got != "<p>hello world</p>" {
		t.Fatal("off: the html is not touched")
	}
	var nilc *Client
	if nilc.Prepare(context.Background(), llm.Request{User: "u"}).User != "u" || hits.Load() != 0 {
		t.Fatal("should not call")
	}
}

func TestARefusedTokenOrAnUnreachableServiceBacksOffAndNeverFailsTheCall(t *testing.T) {
	user := strings.Repeat("long text ", 5)
	for _, status := range []int{401, 403, 503} {
		cfg, hits, _ := service(t, status, `{"detail":"no"}`)
		c := client(cfg)
		if got := c.Prepare(context.Background(), llm.Request{User: user}); got.User != user {
			t.Errorf("%d: changed the text", status)
		}
		c.Prepare(context.Background(), llm.Request{User: user})
		if hits.Load() != 1 {
			t.Errorf("%d: the service was tried again at once (%d calls)", status, hits.Load())
		}
		// after the back-off it is tried again
		now := time.Now().Add(time.Minute)
		c.now = func() time.Time { return now }
		c.Prepare(context.Background(), llm.Request{User: user})
		if hits.Load() != 2 {
			t.Errorf("%d: not tried again after the back-off (%d)", status, hits.Load())
		}
	}
	down := &Config{Enabled: true, URL: "http://127.0.0.1:1", MinChars: 1}
	c := client(down)
	c.Prepare(context.Background(), llm.Request{User: "hello"})
	if !c.isDown(down.URL) {
		t.Fatal("no back-off after a failure")
	}
}

func TestTheConnectionIsReadAgainOnEveryCall(t *testing.T) {
	cfg, hits, _ := service(t, 200, `{"text":"short","engine_used":"x"}`)
	c := client(cfg)
	user := strings.Repeat("long text ", 5)
	if c.Prepare(context.Background(), llm.Request{User: user}).User != "short" {
		t.Fatal("on")
	}
	cfg.Enabled = false
	if c.Prepare(context.Background(), llm.Request{User: user}).User != user {
		t.Fatal("turned off without a restart")
	}
	// another address: the back-off of the old one does not apply
	other, hits2, _ := service(t, 200, `{"text":"short2","engine_used":"x"}`)
	cfg.Enabled, cfg.URL = true, other.URL
	if c.Prepare(context.Background(), llm.Request{User: user}).User != "short2" || hits.Load() != 1 || hits2.Load() != 1 {
		t.Fatal("another address without a restart")
	}
}

func TestCompressHTMLSendsProfile(t *testing.T) {
	cfg, _, _ := service(t, 200, `{"text":"<p>","engine_used":"html-ast"}`)
	c := client(cfg)
	if got := c.CompressHTML(context.Background(), "<p>hello world</p>", "html"); got != "<p>" {
		t.Fatalf("got %q", got)
	}
	if got := c.CompressHTML(context.Background(), "plain", "prose"); got != "plain" {
		t.Fatal("non html kind must pass through")
	}
}

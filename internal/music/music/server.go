package music

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"veritmo/internal/platform/httpclient"
	"veritmo/internal/platform/runner"
)

// BinaryServer is the HTTP server program of acestep.cpp.
const BinaryServer = "ace-server"

// Timings of the server engine.
const (
	serverReadyTimeout = 2 * time.Minute
	pollInterval       = 200 * time.Millisecond
	logTail            = 4096
)

// Client talks to one ace-server over its job API: a job is submitted, polled
// until it leaves "running" and its result fetched. It is safe for concurrent
// use; the server runs one job at a time and queues the rest.
type Client struct {
	Base string // http://127.0.0.1:port
	HTTP *http.Client
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return httpclient.Long
}

// Compose runs a /lm job and returns the first enriched request it produced.
func (c *Client) Compose(ctx context.Context, request []byte) ([]byte, error) {
	id, err := c.submit(ctx, "/lm", "application/json", request)
	if err != nil {
		return nil, err
	}
	_, body, err := c.finish(ctx, id)
	if err != nil {
		return nil, err
	}
	var items []json.RawMessage
	if err := json.Unmarshal(body, &items); err != nil || len(items) == 0 {
		return nil, fmt.Errorf("ace-server: the language model job returned no request (%q)", abbreviate(body))
	}
	var out bytes.Buffer
	if err := json.Indent(&out, items[0], "", "  "); err != nil {
		return nil, fmt.Errorf("ace-server: invalid request in the result: %w", err)
	}
	return out.Bytes(), nil
}

// Synth runs a /synth job and returns the first audio part of its result.
// src and ref, when not nil, are sent as the source and reference audio.
func (c *Client) Synth(ctx context.Context, request []byte, src, ref *AudioFile) ([]byte, error) {
	body, contentType := request, "application/json"
	if src != nil || ref != nil {
		var err error
		body, contentType, err = multipartBody(request, src, ref)
		if err != nil {
			return nil, err
		}
	}
	id, err := c.submit(ctx, "/synth", contentType, body)
	if err != nil {
		return nil, err
	}
	ct, result, err := c.finish(ctx, id)
	if err != nil {
		return nil, err
	}
	return firstAudio(ct, result)
}

// AudioFile is audio sent along with a synth job.
type AudioFile struct {
	Name string
	Data []byte
}

func multipartBody(request []byte, src, ref *AudioFile) ([]byte, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part := func(field, filename, ctype string, data []byte) error {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, field, filename))
		h.Set("Content-Type", ctype)
		p, err := w.CreatePart(h)
		if err != nil {
			return err
		}
		_, err = p.Write(data)
		return err
	}
	if err := part("request", "request.json", "application/json", request); err != nil {
		return nil, "", err
	}
	if src != nil {
		if err := part("audio", src.Name, "application/octet-stream", src.Data); err != nil {
			return nil, "", err
		}
	}
	if ref != nil {
		if err := part("ref_audio", ref.Name, "application/octet-stream", ref.Data); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

// firstAudio returns the first audio/* part of a multipart/mixed result.
func firstAudio(contentType string, body []byte) ([]byte, error) {
	mt, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.HasPrefix(mt, "multipart/") {
		return nil, fmt.Errorf("ace-server: unexpected synth result type %q", contentType)
	}
	r := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		p, err := r.NextPart()
		if err == io.EOF {
			return nil, fmt.Errorf("ace-server: the synth result has no audio")
		}
		if err != nil {
			return nil, fmt.Errorf("ace-server: cannot read the synth result: %w", err)
		}
		if strings.HasPrefix(p.Header.Get("Content-Type"), "audio/") {
			return httpclient.ReadAllLimited(p, maxAudioBytes)
		}
	}
}

func (c *Client) submit(ctx context.Context, path, contentType string, body []byte) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+path, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := c.http().Do(req)
	if err != nil {
		return "", fmt.Errorf("ace-server: %w", err)
	}
	defer resp.Body.Close()
	data, _ := httpclient.ReadAllLimited(resp.Body, maxReplyBytes)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ace-server %s: %s", path, serverMessage(resp.StatusCode, data))
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &out); err != nil || out.ID == "" {
		return "", fmt.Errorf("ace-server %s: no job id in %q", path, abbreviate(data))
	}
	return out.ID, nil
}

// finish waits for the job and fetches its result. Cancelling ctx cancels the
// job on the server.
func (c *Client) finish(ctx context.Context, id string) (contentType string, body []byte, err error) {
	for {
		status, err := c.status(ctx, id)
		if err != nil {
			if ctx.Err() != nil {
				c.cancel(id)
				return "", nil, ctx.Err()
			}
			return "", nil, err
		}
		if status != "running" {
			if status == "cancelled" && ctx.Err() != nil {
				return "", nil, ctx.Err()
			}
			ct, data, rerr := c.result(ctx, id)
			if status != "done" {
				msg := serverMessage(0, data)
				if rerr != nil {
					msg = rerr.Error()
				}
				return "", nil, fmt.Errorf("ace-server job %s %s: %s", id, status, msg)
			}
			return ct, data, rerr
		}
		select {
		case <-ctx.Done():
			c.cancel(id)
			return "", nil, ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

func (c *Client) status(ctx context.Context, id string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/job?id="+url.QueryEscape(id), nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return "", fmt.Errorf("ace-server: %w", err)
	}
	defer resp.Body.Close()
	data, _ := httpclient.ReadAllLimited(resp.Body, maxReplyBytes)
	var out struct {
		Status string `json:"status"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &out) != nil || out.Status == "" {
		return "", fmt.Errorf("ace-server: cannot read the status of job %s: %s", id, serverMessage(resp.StatusCode, data))
	}
	return out.Status, nil
}

func (c *Client) result(ctx context.Context, id string) (string, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/job?id="+url.QueryEscape(id)+"&result=1", nil)
	if err != nil {
		return "", nil, err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("ace-server: %w", err)
	}
	defer resp.Body.Close()
	data, err := httpclient.ReadAllLimited(resp.Body, maxAudioBytes)
	if err != nil {
		return "", nil, fmt.Errorf("ace-server: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", data, fmt.Errorf("ace-server: result of job %s: %s", id, serverMessage(resp.StatusCode, data))
	}
	return resp.Header.Get("Content-Type"), data, nil
}

// cancel is best effort and must work after ctx is done.
func (c *Client) cancel(id string) {
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+"/job?id="+url.QueryEscape(id)+"&cancel=1", nil)
	if err != nil {
		return
	}
	if resp, err := c.http().Do(req); err == nil {
		resp.Body.Close()
	}
}

// serverMessage reads the {"error": "..."} the server answers with, falling
// back to the raw text.
func serverMessage(status int, data []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil && e.Error != "" {
		if status != 0 {
			return fmt.Sprintf("%s (HTTP %d)", e.Error, status)
		}
		return e.Error
	}
	if len(data) == 0 {
		return fmt.Sprintf("HTTP %d", status)
	}
	return abbreviate(data)
}

func abbreviate(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// ---------------------------------------------------------------------------
// Process lifecycle
// ---------------------------------------------------------------------------

// ServerOptions configure the engine that runs ace-server.
type ServerOptions struct {
	Binary string // default BinaryServer
	// ModelsDir and AdaptersDir are project-relative, like the arguments of the
	// programs. The adapters folder is passed only if it exists. They belong to
	// the engine, not to each call, because one server serves the whole run.
	ModelsDir   string
	AdaptersDir string
	KeepLoaded  bool
	// Dir is the working directory of the process (the project directory), so
	// the relative models and adapters paths mean what they mean elsewhere.
	Dir string
	// Start launches a server; nil starts the real program. Tests replace it.
	Start func(ctx context.Context, o StartOptions) (*Process, error)
}

// StartOptions are the arguments of one server process.
type StartOptions struct {
	Binary      string
	ModelsDir   string
	AdaptersDir string // empty: no --adapters
	KeepLoaded  bool
	Dir         string
	Env         []string
}

// Process is a running ace-server.
type Process struct {
	Base string
	stop func() error
	// log returns the tail of the process output; nil when unknown.
	log func() string
}

// Log returns the last lines the process wrote, for error messages.
func (p *Process) Log() string {
	if p == nil || p.log == nil {
		return ""
	}
	return p.log()
}

// Stop ends the process.
func (p *Process) Stop() error {
	if p == nil || p.stop == nil {
		return nil
	}
	return p.stop()
}

// ServerEngine runs the phases through ace-server processes, one per GPU
// environment, started when first needed and stopped by Close.
type ServerEngine struct {
	opts ServerOptions

	mu      sync.Mutex
	servers map[string]*serverEntry
	closed  bool
}

type serverEntry struct {
	proc   *Process
	client *Client
}

// withLog adds the server's last output to an error: a failed job only says
// that it failed; the reason is in what the server printed.
func (e *ServerEngine) withLog(ctx context.Context, env []string, err error) error {
	if err == nil || ctx.Err() != nil {
		return err
	}
	e.mu.Lock()
	s := e.servers[strings.Join(env, "\x00")]
	e.mu.Unlock()
	if s == nil {
		return err
	}
	if log := lastLines(s.proc.Log(), 12); log != "" {
		hint := ""
		if e.opts.KeepLoaded && strings.Contains(strings.ToLower(log), "out of memory") {
			hint = "\nthe models stay in GPU memory between jobs: set defaults.music.keep_loaded: false, or engine: cli"
		}
		return fmt.Errorf("%w\nace-server output:\n%s%s", err, log, hint)
	}
	return err
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// NewServerEngine returns an engine that starts ace-server on demand.
func NewServerEngine(o ServerOptions) *ServerEngine {
	if o.Binary == "" {
		o.Binary = BinaryServer
	}
	if o.Start == nil {
		o.Start = startProcess
	}
	return &ServerEngine{opts: o, servers: map[string]*serverEntry{}}
}

// Resident is true when the models stay in memory for the run.
func (e *ServerEngine) Resident() bool { return e.opts.KeepLoaded }

// clientFor returns the client of the server for env, starting it if needed.
func (e *ServerEngine) clientFor(ctx context.Context, ws Workspace, env []string) (*Client, error) {
	key := strings.Join(env, "\x00")
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, fmt.Errorf("ace-server: the engine is closed")
	}
	if s, ok := e.servers[key]; ok {
		return s.client, nil
	}
	so := StartOptions{Binary: e.opts.Binary, ModelsDir: e.opts.ModelsDir, KeepLoaded: e.opts.KeepLoaded, Dir: e.opts.Dir, Env: env}
	if a := e.opts.AdaptersDir; a != "" {
		if fi, err := os.Stat(ws.Path(a)); err == nil && fi.IsDir() {
			so.AdaptersDir = a
		}
	}
	p, err := e.opts.Start(ctx, so)
	if err != nil {
		return nil, err
	}
	s := &serverEntry{proc: p, client: &Client{Base: p.Base, HTTP: httpclient.Long}}
	e.servers[key] = s
	return s.client, nil
}

// Compose sends a /lm job.
func (e *ServerEngine) Compose(ctx context.Context, ws Workspace, modelsDir, workDir string, req Request, env []string) ([]byte, error) {
	c, err := e.clientFor(ctx, ws, env)
	if err != nil {
		return nil, fmt.Errorf("compose: %w", err)
	}
	in, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("compose: %w", err)
	}
	out, err := c.Compose(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("compose: %w", e.withLog(ctx, env, err))
	}
	return out, nil
}

// Synth sends a /synth job and writes the audio to dst.
func (e *ServerEngine) Synth(ctx context.Context, ws Workspace, modelsDir, adaptersDir, workDir string, request []byte, format, dst string, env []string, src *SourceAudio) error {
	c, err := e.clientFor(ctx, ws, env)
	if err != nil {
		return fmt.Errorf("synth: %w", err)
	}
	var srcFile, refFile *AudioFile
	if src != nil {
		read := func(rel, what string) (*AudioFile, error) {
			if rel == "" {
				return nil, nil
			}
			data, err := os.ReadFile(ws.Path(rel))
			if err != nil {
				return nil, fmt.Errorf("synth: cannot read the %s audio: %w", what, err)
			}
			return &AudioFile{Name: filepath.Base(rel), Data: data}, nil
		}
		var err error
		if srcFile, err = read(src.Path, "source"); err != nil {
			return err
		}
		if src.Ref == src.Path {
			refFile = srcFile
		} else if refFile, err = read(src.Ref, "reference"); err != nil {
			return err
		}
	}
	audio, err := c.Synth(ctx, request, srcFile, refFile)
	if err != nil {
		return fmt.Errorf("synth: %w", e.withLog(ctx, env, err))
	}
	if err := os.MkdirAll(filepath.Dir(ws.Path(dst)), 0o755); err != nil {
		return fmt.Errorf("synth: %w", err)
	}
	if err := os.WriteFile(ws.Path(dst), audio, 0o644); err != nil {
		return fmt.Errorf("synth: %w", err)
	}
	return nil
}

// Close stops every server the engine started.
func (e *ServerEngine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	var first error
	for k, s := range e.servers {
		if err := s.proc.Stop(); err != nil && first == nil {
			first = err
		}
		delete(e.servers, k)
	}
	return first
}

// startProcess launches the real ace-server and waits until it is ready.
func startProcess(ctx context.Context, o StartOptions) (*Process, error) {
	port, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("ace-server: no free port: %w", err)
	}
	args := []string{"--models", o.ModelsDir, "--host", "127.0.0.1", "--port", fmt.Sprint(port)}
	if o.AdaptersDir != "" {
		args = append(args, "--adapters", o.AdaptersDir)
	}
	if o.KeepLoaded {
		args = append(args, "--keep-loaded")
	}
	cmd := exec.Command(o.Binary, args...)
	cmd.Dir = o.Dir
	cmd.Env = append(os.Environ(), o.Env...)
	tail := &runner.TailBuffer{Max: logTail}
	cmd.Stdout, cmd.Stderr = tail, tail
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ace-server: cannot start %s: %w", o.Binary, err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	stop := func() error {
		select {
		case <-exited:
			return nil
		default:
		}
		_ = cmd.Process.Kill()
		<-exited
		return nil
	}

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.After(serverReadyTimeout)
	for {
		select {
		case <-exited:
			return nil, fmt.Errorf("ace-server exited before it was ready:\n%s", tail.String())
		case <-ctx.Done():
			_ = stop()
			return nil, ctx.Err()
		case <-deadline:
			_ = stop()
			return nil, fmt.Errorf("ace-server was not ready after %s:\n%s", serverReadyTimeout, tail.String())
		case <-time.After(pollInterval):
		}
		if healthy(ctx, base) {
			return &Process{Base: base, stop: stop, log: tail.String}, nil
		}
	}
}

func healthy(ctx context.Context, base string) bool {
	c, stop := context.WithTimeout(ctx, 2*time.Second)
	defer stop()
	req, err := http.NewRequestWithContext(c, http.MethodGet, base+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := httpclient.Short.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// Limits of what is read from ace-server: a JSON reply, and an audio file (a long song as WAV).
const (
	maxReplyBytes = 64 << 20
	maxAudioBytes = 1 << 30
)

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// NewTestProcess wraps a server that is already listening (tests).
func NewTestProcess(base string, stop func() error) *Process { return &Process{Base: base, stop: stop} }

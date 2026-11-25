package generate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is a client of the 3D service.
type Client struct {
	BaseURL string // http://host:port
	Token   string
	HTTP    *http.Client
	// PollEvery is how often a job is asked about (default 500 ms).
	PollEvery time.Duration
}

// JobView is the answer of GET /jobs/{id}.
type JobView struct {
	ID            string  `json:"id"`
	State         string  `json:"state"`
	Error         string  `json:"error"`
	Seconds       float64 `json:"seconds"`
	Engine        string  `json:"engine"`
	EngineVersion string  `json:"engine_version"`
	Detail        string  `json:"detail"`
	DetailUsed    string  `json:"detail_used"`
	Resolution    int     `json:"resolution"`
	Triangles     int     `json:"triangles"`
}

// Result is a model the service made.
type Result struct {
	GLB []byte
	JobView
}

// ErrFailed is a job the service could not do; its message is the service's sentence.
var ErrFailed = errors.New("the 3D service could not make the model")

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 10 * time.Minute}
}

func (c *Client) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	return c.http().Do(req)
}

func readJSON(resp *http.Response, out any) error {
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return fmt.Errorf("the 3D service answered %d: %s", resp.StatusCode, e.Error)
		}
		return fmt.Errorf("the 3D service answered %d", resp.StatusCode)
	}
	return json.Unmarshal(data, out)
}

// EngineVersion is what a model of the engine says made it (for instance "triposr@107cefd"), asked of the service before any model is made: a run
// uses it to tell whether a model that is already there was made by the engine it would use now. "" when the service does not say (an older one).
func (c *Client) EngineVersion(ctx context.Context, engine string) (string, error) {
	resp, err := c.do(ctx, http.MethodGet, "/engines", nil)
	if err != nil {
		return "", fmt.Errorf("the 3D service does not answer (%s): %w", c.BaseURL, err)
	}
	var v struct {
		Engines map[string]struct {
			Version string `json:"version"`
		} `json:"engines"`
	}
	if err := readJSON(resp, &v); err != nil {
		return "", err
	}
	return v.Engines[engine].Version, nil
}

// Generate sends a picture, waits for the job and returns the model. onState, when given, is called with each state the job passes through.
func (c *Client) Generate(ctx context.Context, image []byte, engine, detail string, onState func(string)) (Result, error) {
	q := url.Values{"engine": {engine}, "detail": {detail}}
	resp, err := c.do(ctx, http.MethodPost, "/generate?"+q.Encode(), image)
	if err != nil {
		return Result{}, fmt.Errorf("the 3D service does not answer (%s): %w", c.BaseURL, err)
	}
	var sub struct {
		ID string `json:"id"`
	}
	if err := readJSON(resp, &sub); err != nil {
		return Result{}, err
	}
	poll := c.PollEvery
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	last := ""
	for {
		resp, err := c.do(ctx, http.MethodGet, "/jobs/"+sub.ID, nil)
		if err != nil {
			return Result{}, err
		}
		var v JobView
		if err := readJSON(resp, &v); err != nil {
			return Result{}, err
		}
		if v.State != last && onState != nil {
			onState(v.State)
		}
		last = v.State
		switch v.State {
		case "done":
			mr, err := c.do(ctx, http.MethodGet, "/jobs/"+sub.ID+"/model", nil)
			if err != nil {
				return Result{}, err
			}
			defer mr.Body.Close()
			if mr.StatusCode != http.StatusOK {
				return Result{}, fmt.Errorf("the 3D service answered %d for the model", mr.StatusCode)
			}
			glb, err := io.ReadAll(io.LimitReader(mr.Body, 64<<20))
			if err != nil {
				return Result{}, err
			}
			c.forget(sub.ID)
			return Result{GLB: glb, JobView: v}, nil
		case "failed":
			c.forget(sub.ID)
			return Result{}, fmt.Errorf("%w: %s", ErrFailed, shorten(v.Error))
		}
		select {
		case <-ctx.Done():
			c.forget(sub.ID)
			return Result{}, ctx.Err()
		case <-time.After(poll):
		}
	}
}

// forget tells the service to drop a job and its model (it is not an error that it cannot).
func (c *Client) forget(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if resp, err := c.do(ctx, http.MethodDelete, "/jobs/"+id, nil); err == nil {
		resp.Body.Close()
	}
}

// shorten keeps the start of a long message (a traceback line can be tens of kilobytes of key names).
func shorten(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 400 {
		return string(r[:400]) + "..."
	}
	return s
}

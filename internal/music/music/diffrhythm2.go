package music

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"veritmo/internal/music/diffrhythm2"
)

// DiffRhythm2Engine makes the songs with the DiffRhythm2 service. It has no compose phase (the service writes the
// song from the lyrics and the style) and only makes new songs: no tasks and no reference. It opens the service on
// first use, which starts the container when the engine is local.
type DiffRhythm2Engine struct {
	// Open returns a client of a ready service; Release is called when the engine is closed.
	Open    func(ctx context.Context) (*diffrhythm2.Client, error)
	Release func()
	// PollEvery is how often a running job is asked about; the default is two seconds.
	PollEvery time.Duration

	mu     sync.Mutex
	client *diffrhythm2.Client
}

func (e *DiffRhythm2Engine) service(ctx context.Context) (*diffrhythm2.Client, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.client == nil {
		c, err := e.Open(ctx)
		if err != nil {
			return nil, err
		}
		e.client = c
	}
	return e.client, nil
}

// Compose is not part of this generator.
func (*DiffRhythm2Engine) Compose(context.Context, Workspace, string, string, Request, []string) ([]byte, error) {
	return nil, errors.New("the diffrhythm2 generator has no compose phase")
}

// Synth makes one song and writes it, as a WAV, to dst.
func (e *DiffRhythm2Engine) Synth(ctx context.Context, ws Workspace, _, _, _ string, request []byte, format, dst string, _ []string, src *SourceAudio) error {
	if src != nil {
		return errors.New("the diffrhythm2 generator cannot transform or imitate a song")
	}
	if format != "wav" {
		return fmt.Errorf("the diffrhythm2 generator writes WAV, not %q (the pipeline encodes the mp3)", format)
	}
	var req Request
	if err := json.Unmarshal(request, &req); err != nil {
		return fmt.Errorf("diffrhythm2: unreadable request: %w", err)
	}
	if req.Style == "" {
		return errors.New("diffrhythm2: the track has no tags (style words such as pop,piano,warm)")
	}
	var seed int64
	if req.Seed != nil {
		seed = *req.Seed
	}
	c, err := e.service(ctx)
	if err != nil {
		return err
	}
	body := diffrhythm2.Request{Lyrics: req.Lyrics, Style: req.Style, MaxSecs: diffrhythm2.MaxSecsFor(req.Duration), Seed: seed}
	if req.DR2CfgStrength != nil {
		body.CfgStrength = *req.DR2CfgStrength
	}
	if req.DR2Steps != nil {
		body.Steps = *req.DR2Steps
	}
	id, err := c.Generate(ctx, body)
	if err != nil {
		return err
	}
	defer func() {
		// The job is dropped on the service whatever happened here; when the run was cancelled the context is gone.
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		c.Forget(fctx, id)
	}()
	every := e.PollEvery
	if every <= 0 {
		every = 2 * time.Second
	}
	for {
		job, err := c.Job(ctx, id)
		if err != nil {
			return err
		}
		switch job.State {
		case "done":
			return e.download(ctx, c, id, ws.Path(dst))
		case "failed":
			return fmt.Errorf("diffrhythm2: the song failed: %s", job.Error)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(every):
		}
	}
}

func (e *DiffRhythm2Engine) download(ctx context.Context, c *diffrhythm2.Client, id, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := c.Audio(ctx, id, f); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// Close releases the service (stops the container this run started).
func (e *DiffRhythm2Engine) Close() error {
	if e.Release != nil {
		e.Release()
	}
	return nil
}

// Resident is true: the service holds its own memory, outside this program's budget.
func (*DiffRhythm2Engine) Resident() bool { return true }

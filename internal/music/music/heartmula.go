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

	"veritmo/internal/music/heartmula"
)

// HeartMulaMargin is how much longer than the requested duration a HeartMuLa song may be: the model stops by
// itself at the end of the lyrics, and the duration asked for is the longest it may take.
const HeartMulaMargin = 15.0

// HeartMulaDefaultMaxMS and HeartMulaLongestMS bound the length of a HeartMuLa song (the service takes 360 s at most).
const (
	HeartMulaDefaultMaxMS = 240_000
	HeartMulaLongestMS    = 360_000
)

// HeartMulaMaxMS is the longest a song may be, in milliseconds, for the duration the track asks for (0 for none).
func HeartMulaMaxMS(durationSeconds float64) int {
	if durationSeconds <= 0 {
		return HeartMulaDefaultMaxMS
	}
	ms := int((durationSeconds + HeartMulaMargin) * 1000)
	if ms > HeartMulaLongestMS {
		return HeartMulaLongestMS
	}
	return ms
}

// HeartMulaEngine makes the songs with the HeartMuLa service. It has no compose phase (the service writes the
// song from the lyrics and the tags) and only makes new songs: no tasks and no reference. It opens the service on
// first use, which starts the container when the engine is local.
type HeartMulaEngine struct {
	// Open returns a client of a ready service; Release is called when the engine is closed.
	Open    func(ctx context.Context) (*heartmula.Client, error)
	Release func()
	// PollEvery is how often a running job is asked about; the default is two seconds.
	PollEvery time.Duration

	mu     sync.Mutex
	client *heartmula.Client
}

func (e *HeartMulaEngine) service(ctx context.Context) (*heartmula.Client, error) {
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
func (*HeartMulaEngine) Compose(context.Context, Workspace, string, string, Request, []string) ([]byte, error) {
	return nil, errors.New("the heartmula generator has no compose phase")
}

// Synth makes one song and writes it, as a WAV, to dst.
func (e *HeartMulaEngine) Synth(ctx context.Context, ws Workspace, _, _, _ string, request []byte, format, dst string, _ []string, src *SourceAudio) error {
	if src != nil {
		return errors.New("the heartmula generator cannot transform or imitate a song")
	}
	if format != "wav" {
		return fmt.Errorf("the heartmula generator writes WAV, not %q (the pipeline encodes the mp3)", format)
	}
	var req Request
	if err := json.Unmarshal(request, &req); err != nil {
		return fmt.Errorf("heartmula: unreadable request: %w", err)
	}
	if req.Tags == "" {
		return errors.New("heartmula: the track has no tags (style words such as piano,happy)")
	}
	var seed int64
	if req.Seed != nil {
		seed = *req.Seed
	}
	c, err := e.service(ctx)
	if err != nil {
		return err
	}
	id, err := c.Generate(ctx, heartmula.Request{Lyrics: req.Lyrics, Tags: req.Tags, MaxMS: HeartMulaMaxMS(req.Duration), Seed: seed})
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
			return fmt.Errorf("heartmula: the song failed: %s", job.Error)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(every):
		}
	}
}

func (e *HeartMulaEngine) download(ctx context.Context, c *heartmula.Client, id, path string) error {
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
func (e *HeartMulaEngine) Close() error {
	if e.Release != nil {
		e.Release()
	}
	return nil
}

// Resident is true: the service holds its own memory, outside this program's budget.
func (*HeartMulaEngine) Resident() bool { return true }

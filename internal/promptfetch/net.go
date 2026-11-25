package promptfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
	"veritmo/internal/platform/httpclient"
)

// Net is the part of the network the command uses, so that a test can put a fake in its place.
type Net interface {
	// Card reads the dataset card of a repo.
	Card(ctx context.Context, repo string) (Card, error)
	// Download puts a file of a repo at dest, unless it is already there.
	Download(ctx context.Context, repo, file, dest string) error
}

const hfBase = "https://huggingface.co"

// HuggingFace is the real network. Token (from HF_TOKEN) is sent as a bearer token for gated files and kept nowhere.
type HuggingFace struct {
	Token  string
	Client *http.Client
	Base   string
}

func (h HuggingFace) base() string { return orDefault(h.Base, hfBase) }

func (h HuggingFace) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if h.Token != "" {
		req.Header.Set("Authorization", "Bearer "+h.Token)
	}
	c := h.Client
	if c == nil {
		c = httpclient.Long
	}
	return c.Do(req)
}

// Card asks https://huggingface.co/api/datasets/<repo>.
func (h HuggingFace) Card(ctx context.Context, repo string) (Card, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := h.get(ctx, h.base()+"/api/datasets/"+repo)
	if err != nil {
		return Card{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return Card{}, fmt.Errorf("the card of %s answered %d", repo, res.StatusCode)
	}
	var c Card
	if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&c); err != nil {
		return Card{}, fmt.Errorf("the card of %s: %w", repo, err)
	}
	return c, nil
}

// Download gets https://huggingface.co/datasets/<repo>/resolve/main/<file>, writing it complete or not at all.
func (h HuggingFace) Download(ctx context.Context, repo, file, dest string) error {
	if _, err := os.Stat(dest); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	res, err := h.get(ctx, h.base()+"/datasets/"+repo+"/resolve/main/"+file)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s of %s answered %d", file, repo, res.StatusCode)
	}
	const maxFileBytes = 4 << 30
	part := dest + ".part"
	out, err := os.Create(part)
	if err != nil {
		return err
	}
	// A dataset file is big (hundreds of MB), but not without end: past the limit the download fails.
	n, err := io.Copy(out, io.LimitReader(res.Body, maxFileBytes+1))
	if err == nil && n > maxFileBytes {
		err = fmt.Errorf("%s of %s is larger than %d GB", file, repo, maxFileBytes>>30)
	}
	if err != nil {
		out.Close()
		_ = os.Remove(part)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(part)
		return err
	}
	return os.Rename(part, dest)
}

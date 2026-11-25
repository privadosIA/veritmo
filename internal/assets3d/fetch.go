package assets3d

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
)

// userAgent is what the downloads call themselves: the program's name and nothing about who runs it.
const userAgent = "veritmo"

var downloadID = regexp.MustCompile(`data-download-id=['"]([^'"]+)['"]`)

// countWriter counts the bytes of the zip as they come.
type countWriter struct {
	w io.Writer
	n *atomic.Int64
}

func (c countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(int64(n))
	return n, err
}

// fetchZip downloads the zip of a pack into w, from the host of its page only, and reports its progress in j.
func (l *Library) fetchZip(ctx context.Context, d PackDef, w io.Writer, j *job) error {
	client := *l.client
	jar, err := cookiejar.New(nil)
	if err != nil {
		return err
	}
	client.Jar = jar
	get := func(rawURL, referer string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		if referer != "" {
			req.Header.Set("Referer", referer)
		}
		return client.Do(req)
	}
	zipURL := d.URL
	if d.Fetch == "kenney" {
		var err error
		if zipURL, err = l.kenneyLink(d, get); err != nil {
			return err
		}
	}
	if u, err := url.Parse(zipURL); err != nil || !strings.EqualFold(u.Host, d.Host()) {
		return fmt.Errorf("the download address is not on %s: refused", d.Host())
	}
	resp, err := get(zipURL, d.Page)
	if err != nil {
		return fmt.Errorf("the download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the download answered %d", resp.StatusCode)
	}
	if resp.ContentLength > MaxZipBytes {
		return fmt.Errorf("the zip is larger than %d MB", MaxZipBytes>>20)
	}
	j.total.Store(max64(resp.ContentLength, d.SizeBytes))
	n, err := io.Copy(countWriter{w, &j.done}, io.LimitReader(resp.Body, MaxZipBytes+1))
	if err != nil {
		return fmt.Errorf("the download failed: %w", err)
	}
	if n > MaxZipBytes {
		return fmt.Errorf("the zip is larger than %d MB", MaxZipBytes>>20)
	}
	return nil
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// kenneyLink asks for the signed address of a pack the way its page does: it reads the download id of the page and asks for
// the link with the page's cookies and as coming from the page.
func (l *Library) kenneyLink(d PackDef, get func(rawURL, referer string) (*http.Response, error)) (string, error) {
	page, err := get(d.Page, "")
	if err != nil {
		return "", fmt.Errorf("the page of the pack cannot be read: %w", err)
	}
	body, _ := io.ReadAll(io.LimitReader(page.Body, 4<<20))
	page.Body.Close()
	if page.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the page of the pack answered %d", page.StatusCode)
	}
	m := downloadID.FindSubmatch(body)
	if m == nil {
		return "", fmt.Errorf("the page of the pack no longer has a download link: the site changed, download the zip in a browser and import it")
	}
	pu, err := url.Parse(d.Page)
	if err != nil {
		return "", err
	}
	linkURL := pu.Scheme + "://" + pu.Host + "/download-link/" + url.PathEscape(string(m[1]))
	resp, err := get(linkURL, d.Page)
	if err != nil {
		return "", fmt.Errorf("the download link cannot be asked for: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the download link was refused (%d): the site changed how it gives its downloads, download the zip in a browser and import it", resp.StatusCode)
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil || out.URL == "" {
		return "", fmt.Errorf("the download link has no address: the site changed, download the zip in a browser and import it")
	}
	return out.URL, nil
}

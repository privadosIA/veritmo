package server

// core_cors.go decides which browser origins may call the API from another origin (spec cors-settings): the origins given
// with --cors-origin, plus the exact origins and the networks of the settings. The settings are followed live: they are read
// again when the settings file changes (its modification time or size) and, whatever happens to the file, every 5 seconds, which
// is how a change that `veritmo cors` made in the database reaches a running server. Settings that cannot be read leave
// the last good lists in force, so a typo or a database that is down never opens access wider.

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"veritmo/internal/platform/settings"
)

// maxWarnedOrigins bounds the origins remembered for the "rejected" line.
const maxWarnedOrigins = 200

type corsPolicy struct {
	flag map[string]bool
	logf func(format string, args ...any)

	// now is the clock (a test replaces it); loadedAt is when the lists were last read and lastErr the failure last reported.
	now      func() time.Time
	loadedAt time.Time
	lastErr  string

	mu      sync.Mutex
	path    string
	mtime   time.Time
	size    int64
	loaded  bool
	origins map[string]bool
	nets    []*net.IPNet
	warned  map[string]bool
}

// corsRefresh is how long the lists are trusted before the settings are read again.
const corsRefresh = 5 * time.Second

func newCORSPolicy(flagOrigins []string) *corsPolicy {
	p := &corsPolicy{flag: map[string]bool{}, warned: map[string]bool{}, now: time.Now, logf: func(f string, a ...any) { slog.Warn(fmt.Sprintf(f, a...)) }}
	for _, o := range flagOrigins {
		if n, err := settings.NormalizeCORSOrigin(o); err == nil {
			p.flag[n] = true
		} else {
			p.flag[strings.TrimRight(o, "/")] = true
		}
	}
	return p
}

// refresh re-reads the settings when the file changed or the lists are 5 seconds old. The caller holds p.mu.
func (p *corsPolicy) refresh() {
	path := settings.Path()
	var mtime time.Time
	var size int64 = -1
	if path != "" {
		if fi, err := os.Stat(path); err == nil {
			mtime, size = fi.ModTime(), fi.Size()
		}
	}
	now := p.now()
	if p.loaded && path == p.path && mtime.Equal(p.mtime) && size == p.size && now.Sub(p.loadedAt) < corsRefresh {
		return
	}
	f, err := settings.ReadFile()
	if err != nil {
		p.path, p.mtime, p.size, p.loadedAt = path, mtime, size, now
		if p.loaded && err.Error() == p.lastErr { // the same failure is not reported again every 5 seconds
			return
		}
		p.lastErr = err.Error()
		if p.loaded {
			p.logf("warning: the settings cannot be read, keeping the CORS lists they had: %v", err)
			return
		}
		p.logf("warning: the settings cannot be read, no CORS lists from them: %v", err)
		f = settings.File{}
	}
	p.lastErr = ""
	eo, en := settings.EnvCORS() // the stack adds its dashboard's origin by environment; they are added to the saved ones, never saved
	f.CORSOrigins, f.CORSNetworks = append(f.CORSOrigins, eo...), append(f.CORSNetworks, en...)
	p.origins = map[string]bool{}
	for _, o := range f.CORSOrigins {
		n, err := settings.NormalizeCORSOrigin(o)
		if err != nil {
			p.logf("warning: cors_origins: ignoring %v", err)
			continue
		}
		p.origins[n] = true
	}
	p.nets = nil
	for _, c := range f.CORSNetworks {
		n, err := settings.NormalizeCORSNetwork(c)
		if err != nil {
			p.logf("warning: cors_networks: ignoring %v", err)
			continue
		}
		_, ipn, _ := net.ParseCIDR(n)
		p.nets = append(p.nets, ipn)
	}
	p.path, p.mtime, p.size, p.loaded, p.loadedAt = path, mtime, size, true, now
}

// allowed reports whether the Origin header value may call the API.
func (p *corsPolicy) allowed(origin string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refresh()
	n, err := settings.NormalizeCORSOrigin(origin)
	if err != nil {
		return false
	}
	if p.flag[n] || p.origins[n] {
		return true
	}
	u, err := url.Parse(n)
	if err != nil {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil {
		return false
	}
	for _, nw := range p.nets {
		if nw.Contains(ip) {
			return true
		}
	}
	return false
}

// counts says how many exact origins and networks are allowed now (for the line printed at start).
func (p *corsPolicy) counts() (origins, networks int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refresh()
	seen := map[string]bool{}
	for o := range p.flag {
		seen[o] = true
	}
	for o := range p.origins {
		seen[o] = true
	}
	return len(seen), len(p.nets)
}

// rejected logs, once per origin, that a page was refused. An origin that is the server's own address is not one.
func (p *corsPolicy) rejected(origin string, r *http.Request) {
	n, err := settings.NormalizeCORSOrigin(origin)
	if err != nil {
		n = origin
	}
	if u, err := url.Parse(n); err == nil && strings.EqualFold(u.Host, r.Host) {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.warned[n] || len(p.warned) >= maxWarnedOrigins {
		return
	}
	p.warned[n] = true
	p.logf("cors: the browser origin %s is not allowed (the API answers it without CORS headers, so the page sees \"Failed to fetch\"); allow it with: veritmo cors add %s   or its network: veritmo cors add --network <range>", n, n)
}

// CORSCounts is how many exact origins and networks the API allows from a browser right now.
func (s *Server) CORSCounts() (origins, networks int) { return s.cors.counts() }

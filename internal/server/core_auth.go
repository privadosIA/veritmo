package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"veritmo/internal/server/api"
)

// ticketTTL is the lifetime of a header-less access ticket.
const ticketTTL = 10 * time.Minute

const codeUnauthorized = "unauthorized"

// ticketRoutes are the only GET routes that accept ?ticket= in place of
// the Authorization header (EventSource and media elements cannot send
// headers). Everything else requires the Bearer token.
var ticketRoutes = []*regexp.Regexp{
	regexp.MustCompile(`^/api/runs/[^/]+/events$`),
	regexp.MustCompile(`^/api/downloads/[^/]+/events$`),
	regexp.MustCompile(`^/api/music/installs/[^/]+/events$`), // the live progress of an install of a music generator
	regexp.MustCompile(`^/api/projects/[^/]+/artifacts/.+$`),
	regexp.MustCompile(`^/api/reviews/[^/]+/findings/[^/]+/frame$`),                 // the evidence frame of a finding, in an <img>
	regexp.MustCompile(`^/api/motions/[^/]+/preview$`),                              // the preview of a motion of the library, in a <video>
	regexp.MustCompile(`^/api/music-motion/references/[^/]+/frames/[^/]+$`),         // a key frame of a reference, in an <img>
	regexp.MustCompile(`^/api/music-motion/part-drafts/[^/]+/preview/[^/]+$`),       // a preview frame of a draft of a part, in an <img>
	regexp.MustCompile(`^/api/models3d/models/[^/]+/[^/]+/thumbnail$`),              // the picture of a model of the library, in an <img>
	regexp.MustCompile(`^/api/music-motion/pieces/[^/]+/backgrounds/[^/]+/file$`),   // the video behind a piece, in a <video>
	regexp.MustCompile(`^/api/music-motion/pieces/[^/]+/backgrounds/[^/]+/frame$`),  // a picture of a clip of a piece, in an <img>
	regexp.MustCompile(`^/api/music-motion/pieces/[^/]+/backgrounds/[^/]+/thumbs$`), // the strip of pictures of a clip, in an <img>
	regexp.MustCompile(`^/api/models3d/models/[^/]+/[^/]+/file$`),                   // the binary glTF of a model of the library, for a viewer or an <a download>
}

// authenticator guards /api/* with an optional shared Bearer token and
// issues short-lived stateless tickets (see specs/api-access-control).
type authenticator struct {
	tokenSum [sha256.Size]byte // digest, so comparison length never leaks
	enabled  bool
	secret   []byte // per-process ticket HMAC key: tickets die on restart
	now      func() time.Time
	failures failureLimiter
}

// Failed authentications from one address are limited: after maxFailures in failureSpan the server answers 429
// instead of 401 until the window passes. A valid token is never limited. The address is the connection's, not a
// forwarded header: behind a reverse proxy every client shares the proxy's, so the proxy should limit as well.
const (
	maxFailures = 10
	failureSpan = time.Minute
	maxTracked  = 4096 // addresses remembered at once; older windows are swept first
)

// failureLimiter counts failed authentications per address in fixed windows.
type failureLimiter struct {
	mu sync.Mutex
	by map[string]*failureWindow
}

type failureWindow struct {
	start time.Time
	n     int
}

// failed records one failure of addr at now and reports whether addr is over the limit.
func (l *failureLimiter) failed(addr string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.by == nil {
		l.by = map[string]*failureWindow{}
	}
	if len(l.by) >= maxTracked {
		for k, w := range l.by {
			if now.Sub(w.start) >= failureSpan {
				delete(l.by, k)
			}
		}
	}
	w := l.by[addr]
	if w == nil || now.Sub(w.start) >= failureSpan {
		w = &failureWindow{start: now}
		l.by[addr] = w
	}
	w.n++
	return w.n > maxFailures
}

// remoteHost is the address of the connection, without its port.
func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func newAuthenticator(token string) *authenticator {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	a := &authenticator{secret: secret, now: time.Now}
	if token != "" {
		a.enabled = true
		a.tokenSum = sha256.Sum256([]byte(token))
	}
	return a
}

func (a *authenticator) tokenMatches(candidate string) bool {
	sum := sha256.Sum256([]byte(candidate))
	return subtle.ConstantTimeCompare(sum[:], a.tokenSum[:]) == 1
}

func (a *authenticator) sign(expiry int64) string {
	e := strconv.FormatInt(expiry, 10)
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(e))
	return base64.RawURLEncoding.EncodeToString([]byte(e)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// issueTicket returns a signed ticket and its expiry.
func (a *authenticator) issueTicket() (string, time.Time) {
	exp := a.now().Add(ticketTTL)
	return a.sign(exp.Unix()), exp
}

func (a *authenticator) ticketValid(ticket string) bool {
	head, _, ok := strings.Cut(ticket, ".")
	if !ok {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(head)
	if err != nil {
		return false
	}
	expiry, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || a.now().Unix() >= expiry {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(ticket), []byte(a.sign(expiry))) == 1
}

func ticketRoute(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	for _, re := range ticketRoutes {
		if re.MatchString(r.URL.Path) {
			return true
		}
	}
	return false
}

// middleware enforces authentication on /api/*. /api/health and CORS
// preflights are exempt; with no token configured everything passes.
func (a *authenticator) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.enabled || !strings.HasPrefix(r.URL.Path, "/api/") ||
			r.Method == http.MethodOptions || r.URL.Path == "/api/health" {
			next.ServeHTTP(w, r)
			return
		}
		if h := r.Header.Get("Authorization"); h != "" {
			if tok, ok := strings.CutPrefix(h, "Bearer "); ok && a.tokenMatches(tok) {
				next.ServeHTTP(w, r)
				return
			}
		} else if t := r.URL.Query().Get("ticket"); t != "" && ticketRoute(r) && a.ticketValid(t) {
			next.ServeHTTP(w, r)
			return
		}
		if a.failures.failed(remoteHost(r), a.now()) {
			w.Header().Set("Retry-After", strconv.Itoa(int(failureSpan/time.Second)))
			api.WriteError(w, http.StatusTooManyRequests, api.CodeTooManyAttempts, "too many failed attempts from this address: wait a minute")
			return
		}
		w.Header().Set("WWW-Authenticate", "Bearer")
		api.WriteError(w, http.StatusUnauthorized, codeUnauthorized, "missing or invalid access token")
	})
}

func (s *Server) handleAuthTicket(w http.ResponseWriter, r *http.Request) {
	ticket, exp := s.auth.issueTicket()
	api.WriteJSON(w, http.StatusOK, map[string]any{"ticket": ticket, "expiresAt": exp.UTC().Format(time.RFC3339)})
}

// corsMiddleware allows cross-origin access only from the origins the policy allows (see core_cors.go). No wildcard, no
// credentials.
func corsMiddleware(policy *corsPolicy, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		if !policy.allowed(origin) {
			policy.rejected(origin, r)
			next.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		h.Add("Vary", "Origin")
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Access-Control-Expose-Headers", "Content-Disposition, X-Export-Notes, X-Export-Count, X-Total-Count, Link")
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			h.Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

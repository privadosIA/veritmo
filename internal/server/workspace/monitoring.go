package workspace

// monitoring.go: the addresses and keys of the tools the user runs beside the server (Grafana, Prometheus, Loki,
// Redis) and a test of each. The server depends on none of them: it keeps what it was told and looks at a tool
// only when the user asks. The keys are kept like the other keys (settings file, mode 0600) and never returned.
// See specs/monitoring-settings.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"veritmo/internal/platform/settings"
	"veritmo/internal/server/api"
)

// monitoringTimeout bounds a test: a tool that is off must answer "not here" quickly.
const monitoringTimeout = 6 * time.Second

// MonitoringView is how a monitoring tool (Grafana, Prometheus, Loki, Redis) is shown: its address and user, and whether a secret is set, never the secret.
type MonitoringView struct {
	ID        string `json:"id"`
	Address   string `json:"address,omitempty"`
	User      string `json:"user,omitempty"`
	SecretSet bool   `json:"secret_set"`
	// Source says where the tool comes from: setting (saved here), env (the MONITORING_<TOOL>_* variables, used while nothing is saved) or default (none).
	Source string `json:"source"`
}

// GET /api/settings/monitoring -> {tools: [{id, address?, user?, secret_set}]}, in the order the screen lists them.
func (h *Handler) handleMonitoringGet(w http.ResponseWriter, r *http.Request) {
	out := make([]MonitoringView, 0, len(settings.MonitoringIDs))
	for _, id := range settings.MonitoringIDs {
		e := settings.GetMonitoring(id)
		out = append(out, MonitoringView{ID: id, Address: e.Address, User: e.User, SecretSet: e.Secret != "", Source: settings.MonitoringSource(id)})
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"tools": out})
}

// MonitoringRequest is the body that saves a monitoring tool; a secret that is absent keeps the saved one.
type MonitoringRequest struct {
	Address string  `json:"address"`
	User    string  `json:"user"`
	Secret  *string `json:"secret"`
}

// resolve validates the form and fills the key from what is saved when it was left out and the rest is the same.
func (q MonitoringRequest) resolve(id string) (settings.Endpoint, error) {
	if strings.TrimSpace(q.Address) == "" {
		return settings.Endpoint{}, nil
	}
	addr, err := settings.ValidateMonitoringAddress(id, q.Address)
	if err != nil {
		return settings.Endpoint{}, err
	}
	user := strings.TrimSpace(q.User)
	if len(user) > 100 {
		return settings.Endpoint{}, errors.New("the user is longer than 100 characters")
	}
	e := settings.Endpoint{Address: addr, User: user}
	if q.Secret != nil {
		e.Secret = *q.Secret
	} else if old := settings.SavedMonitoring(id); old.Address == addr && old.User == user {
		e.Secret = old.Secret
	}
	return e, nil
}

func readMonitoringRequest(w http.ResponseWriter, r *http.Request) (MonitoringRequest, bool) {
	var req MonitoringRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil && err != io.EOF {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
			return req, false
		}
	}
	return req, true
}

func monitoringID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("tool")
	if !settings.ValidMonitoringID(id) {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("no such tool %q: use %s", id, strings.Join(settings.MonitoringIDs, ", ")))
		return "", false
	}
	return id, true
}

// PUT /api/settings/monitoring/{tool} {address, user?, secret?} -> the same as GET
//
// Saves the address of a tool (an empty address removes it, key included). It connects to nothing: see /test.
func (h *Handler) handleMonitoringPut(w http.ResponseWriter, r *http.Request) {
	id, ok := monitoringID(w, r)
	if !ok {
		return
	}
	req, ok := readMonitoringRequest(w, r)
	if !ok {
		return
	}
	e, err := req.resolve(id)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	if err := settings.SetMonitoring(id, e); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, "could not save the settings: "+err.Error())
		return
	}
	h.handleMonitoringGet(w, r)
}

// monitoringTestResult is the answer of a test: a tool that is off is a normal answer (ok false).
type monitoringTestResult struct {
	OK      bool   `json:"ok"`
	Version string `json:"version,omitempty"`
	// Authenticated says the user and key (or the token) were accepted; absent when none was given or the tool
	// has nothing that needs them.
	Authenticated *bool  `json:"authenticated,omitempty"`
	Error         string `json:"error,omitempty"`
}

// POST /api/settings/monitoring/{tool}/test {address?, user?, secret?} -> {ok, version?, authenticated?, error?}
//
// Asks the tool its health (and, with a user and key, something that needs them): Grafana /api/health and
// /api/org, Prometheus /-/healthy and its build info, Loki /ready and its build info, Redis PING. It writes nothing,
// anywhere. With no body it tests what is saved.
func (h *Handler) handleMonitoringTest(w http.ResponseWriter, r *http.Request) {
	id, ok := monitoringID(w, r)
	if !ok {
		return
	}
	req, ok := readMonitoringRequest(w, r)
	if !ok {
		return
	}
	var e settings.Endpoint
	if strings.TrimSpace(req.Address) == "" && req.User == "" && req.Secret == nil {
		e = settings.GetMonitoring(id)
	} else {
		var err error
		if e, err = req.resolve(id); err != nil {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
			return
		}
	}
	if e.Address == "" {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "no address to test: give one or save it first")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), monitoringTimeout)
	defer cancel()
	api.WriteJSON(w, http.StatusOK, testMonitoring(ctx, id, e))
}

func testMonitoring(ctx context.Context, id string, e settings.Endpoint) monitoringTestResult {
	switch id {
	case settings.MonRedis:
		return testRedis(ctx, e)
	case settings.MonGrafana:
		return testGrafana(ctx, e)
	case settings.MonPrometheus:
		return testPrometheus(ctx, e)
	case settings.MonLoki:
		return testLoki(ctx, e)
	}
	return monitoringTestResult{Error: "unknown tool"}
}

// httpGet asks one path of a tool with the credentials it was given.
func httpGet(ctx context.Context, e settings.Endpoint, path string, auth bool) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.Address+path, nil)
	if err != nil {
		return 0, nil, err
	}
	if auth {
		if e.User != "" {
			req.SetBasicAuth(e.User, e.Secret)
		} else if e.Secret != "" {
			req.Header.Set("Authorization", "Bearer "+e.Secret)
		}
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, body, nil
}

func hasAuth(e settings.Endpoint) bool { return e.User != "" || e.Secret != "" }

func boolPtr(b bool) *bool { return &b }

func testGrafana(ctx context.Context, e settings.Endpoint) monitoringTestResult {
	code, body, err := httpGet(ctx, e, "/api/health", false)
	if err != nil {
		return monitoringTestResult{Error: err.Error()}
	}
	if code != http.StatusOK {
		return monitoringTestResult{Error: fmt.Sprintf("it answered %d: is this Grafana?", code)}
	}
	var h struct {
		Version  string `json:"version"`
		Database string `json:"database"`
	}
	if json.Unmarshal(body, &h) != nil || h.Version == "" {
		return monitoringTestResult{Error: "it answers, but not like Grafana (no version in /api/health)"}
	}
	res := monitoringTestResult{OK: true, Version: h.Version}
	if hasAuth(e) {
		c, _, err := httpGet(ctx, e, "/api/org", true)
		switch {
		case err != nil:
			res.OK, res.Error = false, err.Error()
		case c == http.StatusOK:
			res.Authenticated = boolPtr(true)
		default:
			res.OK, res.Authenticated = false, boolPtr(false)
			res.Error = fmt.Sprintf("Grafana is there but did not accept the user and key (it answered %d)", c)
		}
	}
	return res
}

func testPrometheus(ctx context.Context, e settings.Endpoint) monitoringTestResult {
	code, _, err := httpGet(ctx, e, "/-/healthy", hasAuth(e))
	if err != nil {
		return monitoringTestResult{Error: err.Error()}
	}
	switch code {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		if hasAuth(e) {
			return monitoringTestResult{Authenticated: boolPtr(false), Error: fmt.Sprintf("Prometheus is there but did not accept the user and key (it answered %d)", code)}
		}
		return monitoringTestResult{Error: fmt.Sprintf("it asks for a user and key (it answered %d)", code)}
	default:
		return monitoringTestResult{Error: fmt.Sprintf("it answered %d: is this Prometheus?", code)}
	}
	res := monitoringTestResult{OK: true}
	if hasAuth(e) {
		res.Authenticated = boolPtr(true)
	}
	if c, body, err := httpGet(ctx, e, "/api/v1/status/buildinfo", hasAuth(e)); err == nil && c == http.StatusOK {
		var b struct {
			Data struct {
				Version string `json:"version"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &b) == nil {
			res.Version = b.Data.Version
		}
	}
	return res
}

func testLoki(ctx context.Context, e settings.Endpoint) monitoringTestResult {
	code, body, err := httpGet(ctx, e, "/ready", hasAuth(e))
	if err != nil {
		return monitoringTestResult{Error: err.Error()}
	}
	switch {
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		if hasAuth(e) {
			return monitoringTestResult{Authenticated: boolPtr(false), Error: fmt.Sprintf("Loki is there but did not accept the user and key (it answered %d)", code)}
		}
		return monitoringTestResult{Error: fmt.Sprintf("it asks for a user and key (it answered %d)", code)}
	case code == http.StatusServiceUnavailable:
		return monitoringTestResult{Error: "Loki answers but is not ready yet: " + strings.TrimSpace(string(body))}
	case code != http.StatusOK:
		return monitoringTestResult{Error: fmt.Sprintf("it answered %d: is this Loki?", code)}
	}
	res := monitoringTestResult{OK: true}
	if hasAuth(e) {
		res.Authenticated = boolPtr(true)
	}
	if c, b, err := httpGet(ctx, e, "/loki/api/v1/status/buildinfo", hasAuth(e)); err == nil && c == http.StatusOK {
		var v struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(b, &v) == nil {
			res.Version = v.Version
		}
	}
	return res
}

// testRedis speaks the little of the protocol it needs: AUTH when there is a key, PING, and the version from INFO.
func testRedis(ctx context.Context, e settings.Endpoint) monitoringTestResult {
	d := net.Dialer{Timeout: monitoringTimeout}
	conn, err := d.DialContext(ctx, "tcp", e.Address)
	if err != nil {
		return monitoringTestResult{Error: err.Error()}
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	rd := bufio.NewReader(conn)
	res := monitoringTestResult{}
	if e.Secret != "" {
		args := []string{"AUTH"}
		if e.User != "" {
			args = append(args, e.User)
		}
		args = append(args, e.Secret)
		if reply, err := redisCall(conn, rd, args...); err != nil {
			return monitoringTestResult{Error: err.Error()}
		} else if !strings.HasPrefix(reply, "+") {
			return monitoringTestResult{Authenticated: boolPtr(false), Error: "Redis is there but did not accept the user and key: " + strings.TrimPrefix(reply, "-")}
		}
		res.Authenticated = boolPtr(true)
	}
	reply, err := redisCall(conn, rd, "PING")
	if err != nil {
		return monitoringTestResult{Error: err.Error()}
	}
	switch {
	case reply == "+PONG":
	case strings.Contains(reply, "NOAUTH") || strings.Contains(reply, "WRONGPASS"):
		return monitoringTestResult{Error: "Redis asks for a key: " + strings.TrimPrefix(reply, "-")}
	default:
		return monitoringTestResult{Error: "it answers, but not like Redis: " + reply}
	}
	res.OK = true
	if info, err := redisCall(conn, rd, "INFO", "server"); err == nil {
		for _, line := range strings.Split(info, "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "redis_version:"); ok {
				res.Version = v
			}
		}
	}
	return res
}

// redisCall sends one command and returns its reply as text: the first line for a simple string or an error (with
// its marker: "+OK", "-ERR…"), the content for a bulk string.
func redisCall(conn net.Conn, rd *bufio.Reader, args ...string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	if _, err := io.WriteString(conn, b.String()); err != nil {
		return "", err
	}
	line, err := rd.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if strings.HasPrefix(line, "$") {
		n, err := strconv.Atoi(line[1:])
		if err != nil || n < 0 || n > 1<<20 {
			return "", nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(rd, buf); err != nil {
			return "", err
		}
		return string(buf[:n]), nil
	}
	return line, nil
}

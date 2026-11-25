package server

// core_ctxpress.go: the settings of the connection to the optional ctxpress compression service (see internal/assistant/ctxpress and the
// settings package). The address, whether it is on and the shortest text worth compressing go to settings.json; the token goes to the 0600 key
// store and no answer ever carries it, only token_set. The connection takes effect on the next call, without a restart.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"veritmo/internal/platform/settings"
	"veritmo/internal/server/api"
)

// ctxpressView is what GET and PUT /api/settings/ctxpress answer. It never holds the token.
type ctxpressView struct {
	// Enabled is whether the connection is in use now: on and with an address.
	Enabled  bool   `json:"enabled"`
	URL      string `json:"url"`
	MinChars int    `json:"min_chars"`
	// TokenSet says a token is kept (in the key store, or CTXPRESS_TOKEN in the environment).
	TokenSet bool `json:"token_set"`
	// Source is where the connection comes from: setting (saved), env (CTXPRESS_URL, nothing saved) or none.
	Source string `json:"source"`
	// Remote says the service is not on this machine, so what is sent to it leaves the computer.
	Remote bool `json:"remote"`
	// Warning says what is risky about the address, for example a token that would travel in clear text.
	Warning string `json:"warning,omitempty"`
	// EnvURLSet and EnvTokenSet say the environment has CTXPRESS_URL and CTXPRESS_TOKEN (the defaults when nothing is saved).
	EnvURLSet   bool `json:"env_url_set"`
	EnvTokenSet bool `json:"env_token_set"`
}

func ctxpressViewOf() ctxpressView {
	c := settings.Ctxpress()
	v := ctxpressView{Enabled: c.Enabled, URL: c.URL, MinChars: c.MinChars, TokenSet: c.Token != "", Source: c.Source}
	if c.Saved != nil && !c.Saved.Enabled {
		v.URL = strings.TrimRight(c.Saved.URL, "/")
	}
	if v.URL != "" {
		v.Remote = settings.Remote(v.URL)
		v.Warning = settings.CtxpressWarning(v.URL)
	}
	v.EnvURLSet = strings.TrimSpace(os.Getenv("CTXPRESS_URL")) != ""
	v.EnvTokenSet = strings.TrimSpace(os.Getenv("CTXPRESS_TOKEN")) != ""
	return v
}

// ctxpressPutRequest is the body of PUT /api/settings/ctxpress; every field is optional.
type ctxpressPutRequest struct {
	Enabled  *bool   `json:"enabled"`
	URL      *string `json:"url"`
	MinChars *int    `json:"min_chars"`
	Token    *string `json:"token"`
	Reset    bool    `json:"reset"`
}

// ctxpressTestRequest is the body of POST /api/settings/ctxpress/test; every field is optional.
type ctxpressTestRequest struct {
	URL   string  `json:"url"`
	Token *string `json:"token"`
}

// GET /api/settings/ctxpress -> ctxpressView.
func (s *Server) handleCtxpressGet(w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, http.StatusOK, ctxpressViewOf())
}

// PUT /api/settings/ctxpress {enabled?, url?, min_chars?, token?, reset?}: saves the connection and answers like GET.
//
//	enabled    turns the connection on or off (on needs an address);
//	url        the address of the service; "" removes it (and turns the connection off);
//	min_chars  the shortest text worth sending (100 to 1000000); 0 is the default (2000);
//	token      the token (Authorization: Bearer); "" removes it; absent keeps it;
//	reset      removes the saved section and the saved token: the environment is the default again.
//
// 400 with the reason for an invalid value, and nothing changes.
func (s *Server) handleCtxpressPut(w http.ResponseWriter, r *http.Request) {
	var req ctxpressPutRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if !req.Reset && req.Enabled == nil && req.URL == nil && req.MinChars == nil && req.Token == nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "enabled, url, min_chars, token or reset is required")
		return
	}
	if req.Reset {
		if req.Enabled != nil || req.URL != nil || req.MinChars != nil || req.Token != nil {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "reset cannot be combined with other fields")
			return
		}
		if err := settings.SaveCtxpress(nil); err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
			return
		}
		if err := settings.SetCtxpressToken(""); err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
			return
		}
		api.WriteJSON(w, http.StatusOK, ctxpressViewOf())
		return
	}
	// validate everything before writing anything
	cur := settings.Ctxpress()
	next := settings.CtxpressSettings{Enabled: cur.Enabled, URL: cur.URL}
	if cur.Saved != nil {
		next = *cur.Saved
	} else {
		next.MinChars = cur.MinChars
	}
	if req.URL != nil {
		if u := strings.TrimSpace(*req.URL); u == "" {
			next.URL, next.Enabled = "", false
		} else {
			addr, err := settings.ValidateCtxpressAddress(u)
			if err != nil {
				api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
				return
			}
			next.URL = addr
		}
	}
	if req.MinChars != nil {
		if *req.MinChars == 0 {
			next.MinChars = 0
		} else if err := settings.ValidateCtxpressMinChars(*req.MinChars); err != nil {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
			return
		} else {
			next.MinChars = *req.MinChars
		}
	}
	if req.Enabled != nil {
		if *req.Enabled && next.URL == "" {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "the connection cannot be turned on without an address")
			return
		}
		next.Enabled = *req.Enabled
	}
	if req.Token != nil {
		if tok := strings.TrimSpace(*req.Token); tok != "" {
			if err := settings.ValidateKey(tok); err != nil {
				api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "the token: "+err.Error())
				return
			}
		}
	}
	if err := settings.SaveCtxpress(&next); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	if req.Token != nil {
		if err := settings.SetCtxpressToken(*req.Token); err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
			return
		}
	}
	api.WriteJSON(w, http.StatusOK, ctxpressViewOf())
}

// ctxpressTestResult is what POST /api/settings/ctxpress/test answers.
type ctxpressTestResult struct {
	// Status is ok, unreachable, invalid_token (401), no_scope (403: the token lacks the compress scope), degraded (the service answers but
	// does not compress) or not_configured.
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	URL    string `json:"url,omitempty"`
	Remote bool   `json:"remote"`
	// Warning is the same as in the settings: a token over plain http to another machine.
	Warning string `json:"warning,omitempty"`
	// EngineUsed and Ratio come from the minimal compression the test asks for.
	EngineUsed string  `json:"engine_used,omitempty"`
	Ratio      float64 `json:"ratio,omitempty"`
	TookMS     int64   `json:"took_ms"`
}

// ctxpressSample is a short prose text the test asks the service to compress.
var ctxpressSample = strings.Repeat("The animation of the verse enters with a soft ease, holds for two beats and leaves with the next cut of the song. ", 12)

// POST /api/settings/ctxpress/test {url?, token?}: asks GET /health and a minimal compression of the service (the saved connection, or the
// url and token given, which are not saved). The answer is always 200 with a status; 400 for an invalid address.
func (s *Server) handleCtxpressTest(w http.ResponseWriter, r *http.Request) {
	var req ctxpressTestRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil && err != io.EOF {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
			return
		}
	}
	cur := settings.Ctxpress()
	base, token := cur.URL, cur.Token
	if cur.Saved != nil && base == "" {
		base = strings.TrimRight(cur.Saved.URL, "/") // saved but off: it can still be tested
	}
	if strings.TrimSpace(req.URL) != "" {
		addr, err := settings.ValidateCtxpressAddress(req.URL)
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
			return
		}
		base = addr
	}
	if req.Token != nil {
		token = strings.TrimSpace(*req.Token)
	}
	res := ctxpressTestResult{URL: base}
	if base == "" {
		res.Status, res.Detail = "not_configured", "no address to test: give one or save it first"
		api.WriteJSON(w, http.StatusOK, res)
		return
	}
	res.Remote, res.Warning = settings.Remote(base), settings.CtxpressWarning(base)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	started := time.Now()
	testCtxpress(ctx, base, token, &res)
	res.TookMS = time.Since(started).Milliseconds()
	api.WriteJSON(w, http.StatusOK, res)
}

func testCtxpress(ctx context.Context, base, token string, res *ctxpressTestResult) {
	client := &http.Client{Timeout: 15 * time.Second}
	do := func(method, path string, body io.Reader) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, method, base+path, body)
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return client.Do(req)
	}
	refused := func(code int) bool {
		switch code {
		case http.StatusUnauthorized:
			res.Status, res.Detail = "invalid_token", "the service refused the token (401): it is missing, wrong or expired"
			return true
		case http.StatusForbidden:
			res.Status, res.Detail = "no_scope", "the service accepted the token but it lacks the compress scope (403)"
			return true
		}
		return false
	}
	hr, err := do(http.MethodGet, "/health", nil)
	if err != nil {
		res.Status, res.Detail = "unreachable", err.Error()
		return
	}
	hr.Body.Close()
	if refused(hr.StatusCode) {
		return
	}
	if hr.StatusCode < 200 || hr.StatusCode > 299 {
		res.Status, res.Detail = "degraded", fmt.Sprintf("GET /health answered %d", hr.StatusCode)
		return
	}
	payload, _ := json.Marshal(map[string]any{"text": ctxpressSample, "kind": "prose", "context": map[string]string{"client": "veritmo", "tool": "compress_text"}})
	cr, err := do(http.MethodPost, "/v1/compress", strings.NewReader(string(payload)))
	if err != nil {
		res.Status, res.Detail = "unreachable", err.Error()
		return
	}
	defer cr.Body.Close()
	if refused(cr.StatusCode) {
		return
	}
	if cr.StatusCode != http.StatusOK {
		res.Status, res.Detail = "degraded", fmt.Sprintf("the service is up but POST /v1/compress answered %d", cr.StatusCode)
		return
	}
	var out struct {
		Text       string  `json:"text"`
		EngineUsed string  `json:"engine_used"`
		Ratio      float64 `json:"ratio"`
	}
	if json.NewDecoder(cr.Body).Decode(&out) != nil || out.Text == "" {
		res.Status, res.Detail = "degraded", "the service answered something that is not a compression"
		return
	}
	res.EngineUsed, res.Ratio = out.EngineUsed, out.Ratio
	if out.EngineUsed == "none" || out.EngineUsed == "" {
		res.Status, res.Detail = "degraded", "the service is up but no engine compressed the sample"
		return
	}
	res.Status = "ok"
}

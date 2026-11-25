package workspace

import (
	"bufio"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"veritmo/internal/platform/settings"
)

func TestMonitoring_SavesWithoutShowingTheKeyAndKeepsItForTheSameAddress(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	h := s.Handler()

	var out struct {
		Tools []MonitoringView `json:"tools"`
	}
	rec := doJSON(t, h, http.MethodGet, "/api/settings/monitoring", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Tools) != 4 || out.Tools[0].ID != "grafana" || out.Tools[3].ID != "redis" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}

	rec = doJSON(t, h, http.MethodPut, "/api/settings/monitoring/grafana", map[string]any{"address": "192.168.1.20", "user": "", "secret": "glsa_token"})
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "glsa_token") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if g := out.Tools[0]; g.Address != "http://192.168.1.20:3000" || !g.SecretSet {
		t.Errorf("the port of Grafana is filled in: %+v", g)
	}

	// Saved again with no key: kept. Another address: not kept.
	doJSON(t, h, http.MethodPut, "/api/settings/monitoring/grafana", map[string]any{"address": "http://192.168.1.20:3000"})
	if got := settings.GetMonitoring("grafana"); got.Secret != "glsa_token" {
		t.Errorf("the key is kept for the same address: %+v", got)
	}
	doJSON(t, h, http.MethodPut, "/api/settings/monitoring/grafana", map[string]any{"address": "otra.lan"})
	if got := settings.GetMonitoring("grafana"); got.Secret != "" {
		t.Errorf("another address does not inherit the key: %+v", got)
	}

	// An empty address removes it.
	doJSON(t, h, http.MethodPut, "/api/settings/monitoring/grafana", map[string]any{"address": ""})
	if got := settings.GetMonitoring("grafana"); got != (settings.Endpoint{}) {
		t.Errorf("removed: %+v", got)
	}
}

func TestMonitoring_RefusesWhatIsNotAnAddress(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	h := s.Handler()
	for name, c := range map[string]struct{ tool, address string }{
		"scheme":   {"grafana", "ftp://x"},
		"user":     {"loki", "http://admin:pw@x"},
		"redis":    {"redis", "redis://x"},
		"port":     {"prometheus", "x:99999"},
		"redis ok": {"redis", "x:abc"},
	} {
		if rec := doJSON(t, h, http.MethodPut, "/api/settings/monitoring/"+c.tool, map[string]any{"address": c.address}); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := doJSON(t, h, http.MethodPut, "/api/settings/monitoring/mimir", map[string]any{"address": "x"}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown tool: %d", rec.Code)
	}
	if rec := doJSON(t, h, http.MethodPost, "/api/settings/monitoring/grafana/test", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("nothing to test: %d", rec.Code)
	}
}

func testResult(t *testing.T, s *testServer, tool string, body map[string]any) monitoringTestResult {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/settings/monitoring/"+tool+"/test", body)
	var res monitoringTestResult
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &res) != nil {
		t.Fatalf("%s: %d %s", tool, rec.Code, rec.Body)
	}
	return res
}

func TestMonitoring_TestsGrafanaWithAndWithoutAKey(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/health":
			w.Write([]byte(`{"database":"ok","version":"13.2.2"}`))
		case "/api/org":
			sawAuth = r.Header.Get("Authorization")
			if sawAuth != "Bearer good" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Write([]byte(`{"id":1}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	if res := testResult(t, s, "grafana", map[string]any{"address": srv.URL}); !res.OK || res.Version != "13.2.2" || res.Authenticated != nil {
		t.Errorf("without a key: %+v", res)
	}
	if res := testResult(t, s, "grafana", map[string]any{"address": srv.URL, "secret": "good"}); !res.OK || res.Authenticated == nil || !*res.Authenticated || sawAuth != "Bearer good" {
		t.Errorf("a token is sent as a Bearer: %+v %q", res, sawAuth)
	}
	if res := testResult(t, s, "grafana", map[string]any{"address": srv.URL, "secret": "bad"}); res.OK || res.Authenticated == nil || *res.Authenticated || !strings.Contains(res.Error, "did not accept") {
		t.Errorf("a wrong key: %+v", res)
	}
	// Testing saves nothing.
	if got := settings.GetMonitoring("grafana"); got != (settings.Endpoint{}) {
		t.Errorf("a test must not save: %+v", got)
	}
	// Not Grafana at all.
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) }))
	defer other.Close()
	if res := testResult(t, s, "grafana", map[string]any{"address": other.URL}); res.OK || res.Error == "" {
		t.Errorf("not grafana: %+v", res)
	}
}

func TestMonitoring_TestsPrometheusAndLokiWithBasicAuth(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "mo" || p != "pw" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/-/healthy", "/ready":
			w.Write([]byte("ok"))
		case "/api/v1/status/buildinfo":
			w.Write([]byte(`{"status":"success","data":{"version":"3.5.0"}}`))
		case "/loki/api/v1/status/buildinfo":
			w.Write([]byte(`{"version":"3.3.1"}`))
		}
	}))
	defer srv.Close()
	for tool, version := range map[string]string{"prometheus": "3.5.0", "loki": "3.3.1"} {
		if res := testResult(t, s, tool, map[string]any{"address": srv.URL, "user": "mo", "secret": "pw"}); !res.OK || res.Version != version || res.Authenticated == nil || !*res.Authenticated {
			t.Errorf("%s: %+v", tool, res)
		}
		if res := testResult(t, s, tool, map[string]any{"address": srv.URL}); res.OK || !strings.Contains(res.Error, "asks for a user and key") {
			t.Errorf("%s without a key: %+v", tool, res)
		}
	}
}

// fakeRedis answers AUTH, PING and INFO like the real one, requiring password when it is not empty.
func fakeRedis(t *testing.T, password string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				rd := bufio.NewReader(c)
				authed := password == ""
				for {
					line, err := rd.ReadString('\n')
					if err != nil {
						return
					}
					n := 0
					if strings.HasPrefix(line, "*") {
						n = int(line[1] - '0')
					}
					var args []string
					for i := 0; i < n; i++ {
						rd.ReadString('\n') // $len
						a, _ := rd.ReadString('\n')
						args = append(args, strings.TrimRight(a, "\r\n"))
					}
					switch strings.ToUpper(args[0]) {
					case "AUTH":
						if args[len(args)-1] == password {
							authed = true
							c.Write([]byte("+OK\r\n"))
						} else {
							c.Write([]byte("-WRONGPASS invalid username-password pair\r\n"))
						}
					case "PING":
						if authed {
							c.Write([]byte("+PONG\r\n"))
						} else {
							c.Write([]byte("-NOAUTH Authentication required.\r\n"))
						}
					case "INFO":
						body := "# Server\r\nredis_version:7.2.4\r\n"
						c.Write([]byte("$" + string(rune('0'+len(body)/10)) + string(rune('0'+len(body)%10)) + "\r\n" + body + "\r\n"))
					}
				}
			}(c)
		}
	}()
	return ln.Addr().String()
}

func TestMonitoring_TestsRedis(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	open := fakeRedis(t, "")
	if res := testResult(t, s, "redis", map[string]any{"address": open}); !res.OK || res.Version != "7.2.4" || res.Authenticated != nil {
		t.Errorf("open: %+v", res)
	}
	locked := fakeRedis(t, "s3cret")
	if res := testResult(t, s, "redis", map[string]any{"address": locked}); res.OK || !strings.Contains(res.Error, "asks for a key") {
		t.Errorf("without the key: %+v", res)
	}
	if res := testResult(t, s, "redis", map[string]any{"address": locked, "secret": "s3cret"}); !res.OK || res.Authenticated == nil || !*res.Authenticated {
		t.Errorf("with the key: %+v", res)
	}
	if res := testResult(t, s, "redis", map[string]any{"address": locked, "secret": "nope"}); res.OK || res.Authenticated == nil || *res.Authenticated {
		t.Errorf("a wrong key: %+v", res)
	}
}

func TestMonitoring_AToolThatIsOffIsANormalAnswerAndTestsWhatIsSaved(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	if res := testResult(t, s, "loki", map[string]any{"address": "127.0.0.1:1"}); res.OK || res.Error == "" {
		t.Errorf("%+v", res)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ready")) }))
	defer srv.Close()
	doJSON(t, s.Handler(), http.MethodPut, "/api/settings/monitoring/loki", map[string]any{"address": srv.URL})
	if res := testResult(t, s, "loki", nil); !res.OK {
		t.Errorf("with no body it tests what is saved: %+v", res)
	}
}

func TestMonitoring_TheEnvironmentFillsTheCardAndNeverShowsTheSecret(t *testing.T) {
	isolateSettings(t)
	t.Setenv("MONITORING_GRAFANA_ADDRESS", "http://grafana:3000")
	t.Setenv("MONITORING_GRAFANA_USER", "admin")
	t.Setenv("MONITORING_GRAFANA_SECRET", "secret-from-env")
	t.Setenv("MONITORING_PROMETHEUS_ADDRESS", "http://prometheus:9090")
	s, _ := newProjectsServer(t)
	h := s.Handler()
	var out struct {
		Tools []MonitoringView `json:"tools"`
	}
	rec := doJSON(t, h, http.MethodGet, "/api/settings/monitoring", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || strings.Contains(rec.Body.String(), "secret-from-env") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	g, p, l := out.Tools[0], out.Tools[1], out.Tools[2]
	if g.Address != "http://grafana:3000" || g.User != "admin" || !g.SecretSet || g.Source != "env" || p.Address != "http://prometheus:9090" || p.SecretSet || p.Source != "env" {
		t.Fatalf("%+v %+v", g, p)
	}
	if l.Address != "" || l.Source != "default" {
		t.Fatalf("a tool the environment does not describe: %+v", l)
	}
	// saving one tool makes it the saved one and leaves the other with the environment
	rec = doJSON(t, h, http.MethodPut, "/api/settings/monitoring/prometheus", map[string]any{"address": "http://other:9090"})
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusOK || out.Tools[1].Source != "setting" || out.Tools[0].Source != "env" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

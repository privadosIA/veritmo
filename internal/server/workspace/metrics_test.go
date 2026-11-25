package workspace

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"veritmo/internal/platform/runner"
)

// metricsServer is a server that reads two fake cards and has runs of two servers.
func metricsServer(t *testing.T) *testServer {
	t.Helper()
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	s.cfg.ServerName = "estudio"
	s.rm.hist.server = "estudio"
	s.look = func(name string) (string, error) { return "/fake/" + name, nil }
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if inv.Name == "nvidia-smi" {
			return runner.ScriptedResult{Stdout: []byte("0, NVIDIA GeForce RTX 4070, 1500, 12282, 37, 51, 28.5\n1, NVIDIA GeForce RTX 5060 Ti, 9000, 16311, [N/A], 66, [N/A]\n")}
		}
		return runner.ScriptedResult{}
	})
	s.envRunner = fr

	now := time.Now().UTC()
	mk := func(id string, state RunState, server string, secs int) {
		st := now.Add(-time.Hour)
		fin := st.Add(time.Duration(secs) * time.Second)
		r := Run{ID: id, Project: "mobile-secreto", State: state, Server: server, CreatedAt: st, StartedAt: &st, FinishedAt: &fin}
		if err := s.rm.hist.createRun(&r); err != nil {
			t.Fatal(err)
		}
	}
	mk("a", StateSucceeded, "estudio", 45)
	mk("b", StateSucceeded, "estudio", 500)
	mk("c", StateFailed, "estudio", 5)
	mk("d", StateSucceeded, "portatil", 9999) // another server's: not counted here
	return s
}

func scrape(t *testing.T, s *testServer) string {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/metrics", nil)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain; version=0.0.4") {
		t.Fatalf("%d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	return rec.Body.String()
}

var sampleLine = regexp.MustCompile(`^([a-zA-Z_:][a-zA-Z0-9_:]*)(\{[^}]*\})? (-?[0-9.]+(e[-+]?[0-9]+)?|\+Inf|NaN)$`)

func TestMetrics_IsValidExpositionText(t *testing.T) {
	out := scrape(t, metricsServer(t))
	typed := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		switch {
		case strings.HasPrefix(line, "# TYPE "):
			typed[strings.Fields(line)[2]] = true
		case strings.HasPrefix(line, "# HELP "):
		default:
			m := sampleLine.FindStringSubmatch(line)
			if m == nil {
				t.Errorf("not a sample: %q", line)
				continue
			}
			name := regexp.MustCompile(`_(bucket|sum|count)$`).ReplaceAllString(m[1], "")
			if !typed[m[1]] && !typed[name] {
				t.Errorf("%s has no TYPE before it", m[1])
			}
		}
	}
	// Prometheus refuses a scrape that declares a family twice.
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "# TYPE ") {
			name := strings.Fields(line)[2]
			if seen[name] {
				t.Errorf("%s is declared twice", name)
			}
			seen[name] = true
		}
	}
	if !strings.HasSuffix(out, "\n") {
		t.Error("the text ends with a newline")
	}
}

func TestMetrics_CountsOnlyThisServersRunsAndTheirDurations(t *testing.T) {
	out := scrape(t, metricsServer(t))
	for _, want := range []string{
		`veritmo_runs{state="succeeded",client="embedded",kind="project"} 2`,
		`veritmo_runs{state="failed",client="embedded",kind="project"} 1`,
		`veritmo_runs{state="running",client="embedded",kind="project"} 0`,
		`veritmo_run_duration_seconds_bucket{client="embedded",kind="project",le="60"} 1`,   // 45 s
		`veritmo_run_duration_seconds_bucket{client="embedded",kind="project",le="600"} 2`,  // and 500 s
		`veritmo_run_duration_seconds_bucket{client="embedded",kind="project",le="+Inf"} 2`, // the other server's 9999 s is not here
		`veritmo_run_duration_seconds_sum{client="embedded",kind="project"} 545`,
		`veritmo_run_duration_seconds_count{client="embedded",kind="project"} 2`,
		`veritmo_info{server="estudio",database="sqlite"} 1`,
		`veritmo_database_up 1`,
	} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
}

func TestMetrics_GPUsAndNeverAProjectName(t *testing.T) {
	out := scrape(t, metricsServer(t))
	for _, want := range []string{
		`veritmo_gpu_memory_used_bytes{gpu="0",name="NVIDIA GeForce RTX 4070"} 1.572864e+09`,
		`veritmo_gpu_memory_total_bytes{gpu="1",name="NVIDIA GeForce RTX 5060 Ti"} ` + formatValue(16311*1048576),
		`veritmo_gpu_utilization_ratio{gpu="0",name="NVIDIA GeForce RTX 4070"} 0.37`,
		`veritmo_gpu_utilization_ratio{gpu="1",name="NVIDIA GeForce RTX 5060 Ti"} 0`, // [N/A] reads as 0
		`veritmo_gpu_temperature_celsius{gpu="0",name="NVIDIA GeForce RTX 4070"} 51`,
		`veritmo_gpu_power_watts{gpu="0",name="NVIDIA GeForce RTX 4070"} 28.5`,
	} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(out, "mobile-secreto") || strings.Contains(out, "project=") {
		t.Error("a project must not be named in a label: the content mode hides some")
	}
	if !strings.Contains(out, "veritmo_memory_total_bytes ") {
		t.Error("the RAM of the machine")
	}
}

func TestMetrics_WithoutNvidiaSmiTheRestStillComes(t *testing.T) {
	s := metricsServer(t)
	s.look = func(string) (string, error) { return "", http.ErrMissingFile }
	out := scrape(t, s)
	if strings.Contains(out, "veritmo_gpu_") || !strings.Contains(out, "veritmo_runs{") {
		t.Errorf("no GPU lines, everything else there:\n%s", out)
	}
}

func TestDefaultGPU_PrefersTheCardWithMostFreeMemory(t *testing.T) {
	s := metricsServer(t)
	for _, c := range []struct{ smi, want string }{
		{"0, A, 11000, 12282\n1, B, 15000, 16311\n", "1"},
		{"0, A, 11000, 12282\n1, B, 8600, 16311\n", "0"}, // the big card is busy with other services
	} {
		fr := runner.NewFakeRunner()
		fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult { return runner.ScriptedResult{Stdout: []byte(c.smi)} })
		s.envRunner = fr
		if got := s.defaultGPU(""); got != c.want {
			t.Errorf("%q: got GPU %q, want %q", c.smi, got, c.want)
		}
	}
}

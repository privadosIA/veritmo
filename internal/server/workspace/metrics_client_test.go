package workspace

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// seedClientRun adds a finished run of a project, with the client it was asked for (empty: none stored).
func seedClientRun(t *testing.T, s *testServer, id, project, client string, state RunState, secs int) {
	t.Helper()
	st := time.Now().UTC().Add(-time.Hour)
	fin := st.Add(time.Duration(secs) * time.Second)
	r := Run{ID: id, Project: project, Client: client, State: state, Server: "estudio", CreatedAt: st, StartedAt: &st, FinishedAt: &fin}
	if err := s.rm.hist.createRun(&r); err != nil {
		t.Fatal(err)
	}
}

func wantLines(t *testing.T, out string, lines ...string) {
	t.Helper()
	for _, want := range lines {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
}

func TestMetrics_RunsAreCountedByClientAndKind(t *testing.T) {
	s := metricsServer(t) // two successful and one failed run of an ordinary project
	seedClientRun(t, s, "g1", GenPrefix+"1", "video-studio", StateSucceeded, 100)
	seedClientRun(t, s, "g2", GenPrefix+"2", "video-studio", StateSucceeded, 200)
	seedClientRun(t, s, "g3", GenPrefix+"3", "video-studio", StateFailed, 3)
	seedClientRun(t, s, "e1", EditPrefix+"1", "photo-lab", StateSucceeded, 20)
	out := scrape(t, s)
	wantLines(t, out,
		`veritmo_runs{state="succeeded",client="video-studio",kind="video"} 2`,
		`veritmo_runs{state="failed",client="video-studio",kind="video"} 1`,
		`veritmo_runs{state="succeeded",client="photo-lab",kind="edit"} 1`,
		`veritmo_runs{state="succeeded",client="embedded",kind="project"} 2`,
		`veritmo_run_duration_seconds_bucket{client="video-studio",kind="video",le="120"} 1`,
		`veritmo_run_duration_seconds_bucket{client="video-studio",kind="video",le="300"} 2`,
		`veritmo_run_duration_seconds_sum{client="video-studio",kind="video"} 300`,
		`veritmo_run_duration_seconds_count{client="photo-lab",kind="edit"} 1`,
	)
}

func TestMetrics_ARunWithoutClientIsUnknownIfItCameFromTheAPI(t *testing.T) {
	s := metricsServer(t)
	seedClientRun(t, s, "old", GenPrefix+"old", "", StateSucceeded, 50)
	wantLines(t, scrape(t, s), `veritmo_runs{state="succeeded",client="unknown",kind="video"} 1`)
}

func TestMetrics_WithoutRunsTheStatesStillComeAtZero(t *testing.T) {
	s := metricsServer(t)
	if _, _, _, err := s.rm.deleteRuns(nil, &runDeleteFilter{States: terminalStates}, false); err != nil {
		t.Fatal(err)
	}
	out := scrape(t, s)
	wantLines(t, out,
		`veritmo_runs{state="succeeded",client="embedded",kind="project"} 0`,
		`veritmo_run_duration_seconds_count{client="embedded",kind="project"} 0`,
	)
}

func TestMetrics_OnlyTheClientsWithMostRunsAreNamed(t *testing.T) {
	s := metricsServer(t)
	for i := 0; i < maxMetricClients+5; i++ {
		// each client has one run more than the next one, so the five last are the ones left out
		for j := 0; j <= maxMetricClients+5-i; j++ {
			seedClientRun(t, s, fmt.Sprintf("c%d-%d", i, j), GenPrefix+fmt.Sprintf("%d-%d", i, j), fmt.Sprintf("dash-%02d", i), StateSucceeded, 10)
		}
	}
	out := scrape(t, s)
	named := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "veritmo_runs{state=\"succeeded\"") {
			start := strings.Index(l, `client="`) + len(`client="`)
			named[l[start:start+strings.Index(l[start:], `"`)]] = true
		}
	}
	// the 20 clients with most runs are named ("embedded" competes like any other) and "other" holds the rest
	if !named["other"] || !named["dash-00"] || named["dash-24"] {
		t.Errorf("clients named: %v", named)
	}
	if len(named) > maxMetricClients+1 {
		t.Errorf("%d clients named, at most %d plus other", len(named), maxMetricClients)
	}
}

package workspace

import (
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"veritmo/internal/platform/settings"
)

// remoteDB reads VERITMO_TEST_MYSQL or VERITMO_TEST_POSTGRES ("user:password@host:port/database"), a server the test may fill
// and empty. Without it the tests of the remote engines are skipped, with the reason.
func remoteDB(t *testing.T, driver, env string) settings.Database {
	t.Helper()
	raw := os.Getenv(env)
	if raw == "" {
		t.Skipf("%s is not set: no %s server to test against", env, driver)
	}
	cred, rest, ok := strings.Cut(raw, "@")
	user, pass, _ := strings.Cut(cred, ":")
	hostPort, name, ok2 := strings.Cut(rest, "/")
	host, port, err := net.SplitHostPort(hostPort)
	if !ok || !ok2 || err != nil {
		t.Fatalf("%s must be user:password@host:port/database", env)
	}
	n, _ := strconv.Atoi(port)
	return settings.Database{Driver: driver, Host: host, Port: n, Name: name, User: user, Password: pass}
}

func cleanHistory(t *testing.T, h *historyStore) {
	t.Helper()
	for _, m := range historyModels() {
		if err := h.db.Migrator().DropTable(m); err != nil {
			t.Fatal(err)
		}
	}
	// The tables are created by migrations, which are recorded: the records go with the tables, or the next open would find the
	// migration applied and the tables gone.
	for _, name := range []string{"data_imports", "schema_migrations"} {
		if err := h.db.Migrator().DropTable(name); err != nil {
			t.Fatal(err)
		}
	}
}

func testRemoteHistory(t *testing.T, d settings.Database) {
	t.Helper()
	first, err := openHistoryWith("", d, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	cleanHistory(t, first)
	first, err = openHistoryWith("", d, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanHistory(t, first)
	second, err := openHistoryWith("", d, "beta")
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	long := strings.Repeat("é", 5000) // a long line of events, with accents
	a := &Run{ID: "run-1", Project: "demo", State: StateRunning, CreatedAt: now}
	b := &Run{ID: "run-2", Project: "mobile-20261005-gato", State: StateRunning, CreatedAt: now.Add(time.Minute)}
	if err := first.createRun(a); err != nil {
		t.Fatal(err)
	}
	if err := second.createRun(b); err != nil {
		t.Fatal(err)
	}
	if a.Server != "alpha" || b.Server != "beta" {
		t.Fatalf("the server that made the run is recorded: %q %q", a.Server, b.Server)
	}
	if err := first.appendEvent("run-1", 0, long); err != nil {
		t.Fatal(err)
	}
	if err := first.appendEvent("run-1", 0, long); err == nil {
		t.Error("the sequence of a run is unique")
	}

	// Both servers see both runs.
	runs, err := second.listRuns("", 0)
	if err != nil || len(runs) != 2 || runs[0].ID != "run-2" {
		t.Fatalf("listRuns: %v %+v", err, runs)
	}
	got, err := second.getRun("run-1")
	if err != nil || got.Server != "alpha" || !got.CreatedAt.Equal(now) {
		t.Fatalf("getRun: %v %+v", err, got)
	}
	evs, err := second.listEvents("run-1")
	if err != nil || len(evs) != 1 || evs[0] != long {
		t.Fatalf("events: %v", err)
	}

	// A restart of beta marks only beta's runs as interrupted.
	third, err := openHistoryWith("", d, "beta")
	if err != nil {
		t.Fatal(err)
	}
	ra, _ := third.getRun("run-1")
	rb, _ := third.getRun("run-2")
	if ra.State != StateRunning || rb.State != StateInterrupted {
		t.Errorf("recovery is per server: alpha's run is %q, beta's %q", ra.State, rb.State)
	}

	// The statistics count both, or one server's.
	res, err := third.stats("", nil, nil, nil, nil, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil || res.Totals.Runs != 2 {
		t.Fatalf("stats: %v %+v", err, res)
	}

	// The speed of a card is the server's own.
	for i := 0; i < calibrationMinSamples; i++ {
		if err := first.addCalibration([]CalibrationSample{{RunID: "run-1", GPU: "RTX", Kind: "render", Expected: 1, Actual: 2, Ratio: 2, At: now}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, n, ok := first.speedOf("RTX"); !ok || n < calibrationMinSamples {
		t.Errorf("alpha measured its card: %d %v", n, ok)
	}
	if _, n, ok := third.speedOf("RTX"); ok || n != 0 {
		t.Errorf("beta has no samples of alpha's card: %d %v", n, ok)
	}

	if n, err := first.deleteRuns([]string{"run-1", "run-2"}); err != nil || n != 2 {
		t.Fatalf("deleteRuns: %d %v", n, err)
	}
}

func TestRemoteHistory_MySQL(t *testing.T) {
	testRemoteHistory(t, remoteDB(t, settings.DBMySQL, "VERITMO_TEST_MYSQL"))
}

func TestRemoteHistory_Postgres(t *testing.T) {
	testRemoteHistory(t, remoteDB(t, settings.DBPostgres, "VERITMO_TEST_POSTGRES"))
}

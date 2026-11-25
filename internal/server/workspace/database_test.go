package workspace

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"veritmo/internal/platform/settings"
)

func TestDatabase_DefaultsToSQLiteAndNeverShowsThePassword(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	h := s.Handler()

	var a DatabaseAnswer
	rec := doJSON(t, h, http.MethodGet, "/api/settings/database", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil || a.Saved.Driver != "sqlite" || a.Active.Driver != "sqlite" || a.RestartRequired || a.Source != "default" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}

	rec = doJSON(t, h, http.MethodPut, "/api/settings/database", map[string]any{
		"driver": "mysql", "host": "db.lan", "name": "veritmo", "user": "mo", "password": "s3cret", "server_name": "estudio",
	})
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "s3cret") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &a)
	if a.Saved.Driver != "mysql" || a.Saved.Port != 3306 || !a.Saved.PasswordSet || a.Active.Driver != "sqlite" || !a.RestartRequired || a.ServerName != "estudio" {
		t.Errorf("saved: %+v", a)
	}

	// Saving again without the password keeps it; another host does not.
	doJSON(t, h, http.MethodPut, "/api/settings/database", map[string]any{"driver": "mysql", "host": "db.lan", "name": "veritmo", "user": "mo"})
	if got := settings.GetDatabase(); got.Password != "s3cret" {
		t.Errorf("the password is kept for the same server: %+v", got)
	}
	doJSON(t, h, http.MethodPut, "/api/settings/database", map[string]any{"driver": "mysql", "host": "otra.lan", "name": "veritmo", "user": "mo"})
	if got := settings.GetDatabase(); got.Password != "" {
		t.Errorf("another host does not inherit the password: %+v", got)
	}

	// Back to the file removes everything.
	doJSON(t, h, http.MethodPut, "/api/settings/database", map[string]any{"driver": "sqlite"})
	if got := settings.GetDatabase(); got != (settings.Database{}) {
		t.Errorf("sqlite: %+v", got)
	}
}

func TestDatabase_RefusesWhatIsNotADatabase(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	h := s.Handler()
	for name, body := range map[string]map[string]any{
		"engine":   {"driver": "oracle", "host": "x", "name": "n", "user": "u"},
		"host":     {"driver": "mysql", "host": "http://x", "name": "n", "user": "u"},
		"no name":  {"driver": "mysql", "host": "x", "user": "u"},
		"no user":  {"driver": "mysql", "host": "x", "name": "n"},
		"the port": {"driver": "postgres", "host": "x", "name": "n", "user": "u", "port": 70000},
		"a name":   {"driver": "mysql", "host": "x", "name": "a;drop", "user": "u"},
	} {
		if rec := doJSON(t, h, http.MethodPut, "/api/settings/database", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := doJSON(t, h, http.MethodPost, "/api/settings/database/test", map[string]any{"driver": "sqlite"}); rec.Code != http.StatusBadRequest {
		t.Errorf("testing sqlite: %d", rec.Code)
	}
	if rec := doJSON(t, h, http.MethodPost, "/api/settings/database/copy", nil); rec.Code != http.StatusConflict {
		t.Errorf("copy with no database saved: %d", rec.Code)
	}
}

func TestDatabase_ATestOfAServerNobodyAnswersOnIsANormalAnswer(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/settings/database/test", map[string]any{
		"driver": "mysql", "host": "127.0.0.1", "port": 1, "name": "x", "user": "u", "password": "p",
	})
	var res databaseTestResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || rec.Code != http.StatusOK || res.OK || res.Error == "" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestDatabase_TestsAndCopiesToMySQL(t *testing.T) {
	d := remoteDB(t, settings.DBMySQL, "VERITMO_TEST_MYSQL")
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	s.cfg.ServerName = "estudio"
	h := s.Handler()
	form := map[string]any{"driver": d.Driver, "host": d.Host, "port": d.Port, "name": d.Name, "user": d.User, "password": d.Password}

	clean, err := openHistoryWith("", d, "x")
	if err != nil {
		t.Fatal(err)
	}
	cleanHistory(t, clean)
	defer func() {
		again, _ := openHistoryWith("", d, "x")
		cleanHistory(t, again)
	}()

	rec := doJSON(t, h, http.MethodPost, "/api/settings/database/test", form)
	var res databaseTestResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || !res.OK || res.Version == "" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if !res.Empty { // the tables were just dropped
		t.Errorf("a database with no tables says so: %+v", res)
	}

	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	seedStat(t, s, statRun{id: "a", project: "demo", state: StateSucceeded, created: now, durS: 5, events: []string{ev("phase_end", "render", 100)}})
	seedStat(t, s, statRun{id: "b", project: "mobile-x", state: StateFailed, created: now.Add(time.Minute), durS: 5})
	_ = s.rm.hist.addCalibration([]CalibrationSample{{RunID: "a", GPU: "RTX", Kind: "render", Expected: 1, Actual: 2, Ratio: 2, At: now}})

	if rec := doJSON(t, h, http.MethodPost, "/api/settings/database/copy", nil); rec.Code != http.StatusConflict {
		t.Fatalf("copy before saving: %d", rec.Code)
	}
	doJSON(t, h, http.MethodPut, "/api/settings/database", form)
	rec = doJSON(t, h, http.MethodPost, "/api/settings/database/copy", nil)
	var cp databaseCopyResult
	if err := json.Unmarshal(rec.Body.Bytes(), &cp); err != nil || rec.Code != http.StatusOK || cp.Copied["runs"] != 2 || cp.Copied["run_events"] != 1 || cp.Copied["calibration_samples"] != 1 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	// Twice changes nothing.
	rec = doJSON(t, h, http.MethodPost, "/api/settings/database/copy", nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &cp)
	if cp.Copied["runs"] != 0 || cp.Copied["run_events"] != 0 {
		t.Errorf("a second copy adds nothing: %+v", cp)
	}

	// What arrived is there, and recorded as this server's.
	dst, err := openHistoryWith("", d, "otro")
	if err != nil {
		t.Fatal(err)
	}
	got, err := dst.getRun("a")
	if err != nil || got.Server == "" {
		t.Fatalf("%v %+v", err, got)
	}
	if lines, _ := dst.listEvents("a"); len(lines) != 1 {
		t.Errorf("events: %v", lines)
	}
}

func TestDatabase_TheEnvironmentIsTheDefaultAndTheAnswerSaysSo(t *testing.T) {
	isolateSettings(t)
	t.Setenv("VERITMO_DB_DRIVER", "mysql")
	t.Setenv("VERITMO_DB_HOST", "db.internal")
	t.Setenv("VERITMO_DB_NAME", "veritmo")
	t.Setenv("VERITMO_DB_USER", "mo")
	t.Setenv("VERITMO_DB_PASSWORD", "secret-from-env")
	s, _ := newProjectsServer(t)
	h := s.Handler()
	var a DatabaseAnswer
	rec := doJSON(t, h, http.MethodGet, "/api/settings/database", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil || a.Source != "env" || a.Saved.Driver != "mysql" || a.Saved.Host != "db.internal" || !a.Saved.PasswordSet {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "secret-from-env") {
		t.Fatal("the password of the environment is in the answer")
	}
	// saving a database makes the file the source, and the environment's password is not carried into it
	rec = doJSON(t, h, http.MethodPut, "/api/settings/database", map[string]any{"driver": "mysql", "host": "db.internal", "name": "veritmo", "user": "mo"})
	_ = json.Unmarshal(rec.Body.Bytes(), &a)
	if rec.Code != http.StatusOK || a.Source != "setting" || a.Saved.PasswordSet {
		t.Fatalf("%d %+v", rec.Code, a)
	}
}

package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mysqlEnv sets the VERITMO_DB_* variables of a MySQL for the length of the test.
func mysqlEnv(t *testing.T) {
	t.Helper()
	t.Setenv("VERITMO_DB_DRIVER", "mysql")
	t.Setenv("VERITMO_DB_HOST", "db.internal")
	t.Setenv("VERITMO_DB_PORT", "")
	t.Setenv("VERITMO_DB_NAME", "veritmo")
	t.Setenv("VERITMO_DB_USER", "mo")
	t.Setenv("VERITMO_DB_PASSWORD", "secret-from-env")
	t.Setenv("VERITMO_DB_PASSWORD_FILE", "")
}

func TestDatabase_TheEnvironmentIsTheDefaultWhenNothingIsSaved(t *testing.T) {
	isolate(t)
	mysqlEnv(t)
	d := GetDatabase()
	if d.Driver != DBMySQL || d.Host != "db.internal" || d.Port != 3306 || d.Name != "veritmo" || d.User != "mo" || d.Password != "secret-from-env" {
		t.Fatalf("%+v", d)
	}
	if DatabaseSource() != FromEnv || SavedDatabase() != (Database{}) || CheckEnvDatabase() != nil {
		t.Fatal("the source is the environment and nothing is saved")
	}
	// the environment is not written to the settings file by a save of anything else
	if err := SetRouting(map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if err := SetServerName("box"); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(Path()); strings.Contains(string(raw), "secret-from-env") || strings.Contains(string(raw), "db.internal") {
		t.Fatalf("the environment's database reached the settings file: %s", raw)
	}
}

func TestDatabase_WhatIsSavedWinsOverTheEnvironment(t *testing.T) {
	isolate(t)
	mysqlEnv(t)
	if err := SetDatabase(Database{Driver: DBPostgres, Host: "saved.host", Name: "n", User: "u", Password: "saved-pw"}); err != nil {
		t.Fatal(err)
	}
	d := GetDatabase()
	if d.Driver != DBPostgres || d.Host != "saved.host" || d.Password != "saved-pw" || DatabaseSource() != FromSetting {
		t.Fatalf("%+v %s", d, DatabaseSource())
	}
	// going back to SQLite in the settings falls to the environment again: nothing is saved
	if err := SetDatabase(Database{}); err != nil {
		t.Fatal(err)
	}
	if DatabaseSource() != FromEnv {
		t.Fatal("with nothing saved the environment is the default again")
	}
}

func TestDatabase_NoEnvironmentIsTheSQLiteFile(t *testing.T) {
	isolate(t)
	for _, k := range []string{"VERITMO_DB_DRIVER", "VERITMO_DB_HOST", "VERITMO_DB_PORT", "VERITMO_DB_NAME", "VERITMO_DB_USER", "VERITMO_DB_PASSWORD", "VERITMO_DB_PASSWORD_FILE"} {
		t.Setenv(k, "")
	}
	if GetDatabase() != (Database{}) || DatabaseSource() != FromDefault || CheckEnvDatabase() != nil {
		t.Fatal("no variable is the SQLite file")
	}
	t.Setenv("VERITMO_DB_DRIVER", "sqlite")
	if GetDatabase() != (Database{}) {
		t.Fatal("sqlite in the environment is the file")
	}
}

func TestDatabase_ADescriptionThatIsNotValidIsAnErrorNotASilentFile(t *testing.T) {
	isolate(t)
	mysqlEnv(t)
	t.Setenv("VERITMO_DB_HOST", "")
	err := CheckEnvDatabase()
	if err == nil || !strings.Contains(err.Error(), "VERITMO_DB_") {
		t.Fatalf("%v", err)
	}
	if strings.Contains(err.Error(), "secret-from-env") {
		t.Fatal("the error shows the password")
	}
	t.Setenv("VERITMO_DB_HOST", "db")
	t.Setenv("VERITMO_DB_PORT", "abc")
	if CheckEnvDatabase() == nil {
		t.Fatal("a port that is not a number is accepted")
	}
	// a saved database wins, so a broken environment is not the person's problem then
	if err := SetDatabase(Database{Driver: DBMySQL, Host: "h", Name: "n", User: "u"}); err != nil {
		t.Fatal(err)
	}
	if CheckEnvDatabase() != nil {
		t.Fatal("the environment is not looked at when a database is saved")
	}
}

func TestDatabase_ThePasswordCanBeAFile(t *testing.T) {
	isolate(t)
	mysqlEnv(t)
	t.Setenv("VERITMO_DB_PASSWORD", "")
	file := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(file, []byte("secret-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VERITMO_DB_PASSWORD_FILE", file)
	if GetDatabase().Password != "secret-from-file" {
		t.Fatal("the password of the file is not used")
	}
	// the variable wins over the file; a file that cannot be read names the file and never a value
	t.Setenv("VERITMO_DB_PASSWORD", "from-variable")
	if GetDatabase().Password != "from-variable" {
		t.Fatal("the variable wins")
	}
	t.Setenv("VERITMO_DB_PASSWORD", "")
	t.Setenv("VERITMO_DB_PASSWORD_FILE", filepath.Join(t.TempDir(), "missing"))
	err := CheckEnvDatabase()
	if err == nil || !strings.Contains(err.Error(), "VERITMO_DB_PASSWORD_FILE") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("%v", err)
	}
}

func TestMusicServices_TheEnvironmentIsTheDefaultAndSavedWins(t *testing.T) {
	isolate(t)
	t.Setenv("HEARTMULA_HOST", "http://heartmula:8787")
	t.Setenv("HEARTMULA_TOKEN", "hm-token-from-env")
	t.Setenv("DIFFRHYTHM2_HOST", "diffrhythm2")
	t.Setenv("DIFFRHYTHM2_TOKEN", "dr-token-from-env")
	if h, tok := HeartMula(); h != "http://heartmula:8787" || tok != "hm-token-from-env" || HeartMulaSource() != FromEnv {
		t.Fatalf("%q %q %s", h, tok, HeartMulaSource())
	}
	if h, tok := DiffRhythm2(); h != "http://diffrhythm2:8788" || tok != "dr-token-from-env" || DiffRhythm2Source() != FromEnv {
		t.Fatalf("the address is normalised like a saved one: %q %q", h, tok)
	}
	// the address of the environment is the person's own choice: it needs no acknowledgement; another address does
	if !HeartMulaAcknowledged("http://heartmula:8787") || HeartMulaAcknowledged("http://elsewhere:8787") {
		t.Fatal("the environment's address is accepted, another is not")
	}
	// what is saved wins, with its own token (never the environment's)
	if err := SetHeartMula("http://saved.example:8787", ""); err != nil {
		t.Fatal(err)
	}
	if h, tok := HeartMula(); h != "http://saved.example:8787" || tok != "" || HeartMulaSource() != FromSetting {
		t.Fatalf("%q %q", h, tok)
	}
	if HeartMulaAcknowledged("http://heartmula:8787") {
		t.Fatal("the environment's acceptance does not carry over once another address is saved")
	}
	// an address that is not valid in the environment is no address
	t.Setenv("DIFFRHYTHM2_HOST", "ftp://nope")
	if h, _ := DiffRhythm2(); h != "" || DiffRhythm2Source() != FromDefault {
		t.Fatalf("%q", h)
	}
	// nothing set is nothing
	t.Setenv("DIFFRHYTHM2_HOST", "")
	if h, tok := DiffRhythm2(); h != "" || tok != "" {
		t.Fatal("empty by default")
	}
}

func TestMonitoring_TheEnvironmentIsTheDefaultAndSavedWins(t *testing.T) {
	isolate(t)
	t.Setenv("MONITORING_GRAFANA_ADDRESS", "http://grafana:3000")
	t.Setenv("MONITORING_GRAFANA_USER", "admin")
	t.Setenv("MONITORING_GRAFANA_SECRET", "grafana-secret-from-env")
	t.Setenv("MONITORING_PROMETHEUS_ADDRESS", "prometheus")
	t.Setenv("MONITORING_LOKI_ADDRESS", "ftp://nope") // not valid: ignored
	g := GetMonitoring(MonGrafana)
	if g.Address != "http://grafana:3000" || g.User != "admin" || g.Secret != "grafana-secret-from-env" || MonitoringSource(MonGrafana) != FromEnv {
		t.Fatalf("%+v", g)
	}
	if p := GetMonitoring(MonPrometheus); p.Address != "http://prometheus:9090" {
		t.Fatalf("the usual port: %+v", p)
	}
	if GetMonitoring(MonLoki).Address != "" || MonitoringSource(MonLoki) != FromDefault || GetMonitoring(MonRedis).Address != "" {
		t.Fatal("an invalid address and a tool that is not described are nothing")
	}
	// saving a tool saves it alone and the environment is not written to the settings
	if err := SetMonitoring(MonPrometheus, Endpoint{Address: "http://saved:9090"}); err != nil {
		t.Fatal(err)
	}
	if GetMonitoring(MonPrometheus).Address != "http://saved:9090" || MonitoringSource(MonPrometheus) != FromSetting || GetMonitoring(MonGrafana).Address != "http://grafana:3000" {
		t.Fatal("saved wins, the others stay with the environment")
	}
	if raw, _ := os.ReadFile(Path()); strings.Contains(string(raw), "grafana-secret-from-env") || strings.Contains(string(raw), "grafana:3000") {
		t.Fatalf("the environment reached the settings file: %s", raw)
	}
	if SavedMonitoring(MonGrafana).Address != "" {
		t.Fatal("SavedMonitoring is what is saved")
	}
}

func TestCORS_TheEnvironmentAddsOriginsAndNetworks(t *testing.T) {
	isolate(t)
	t.Setenv("CORS_ORIGINS", "http://192.168.1.50:4200/, https://Dash.Example , not-an-origin")
	t.Setenv("CORS_NETWORKS", "100.64.0.0/10, 10.0.0.7, nope")
	if _, _, err := ChangeCORS(false, false, "http://saved.example:4200"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ChangeCORS(false, false, "http://192.168.1.50:4200"); err != nil {
		t.Fatal(err)
	}
	origins, networks := CORS()
	want := map[string]bool{"http://saved.example:4200": true, "http://192.168.1.50:4200": true, "https://dash.example": true}
	if len(origins) != 3 {
		t.Fatalf("saved and environment, without repeating one: %v", origins)
	}
	for _, o := range origins {
		if !want[o] {
			t.Fatalf("%v", origins)
		}
	}
	if len(networks) != 2 || networks[0] != "100.64.0.0/10" || networks[1] != "10.0.0.7/32" {
		t.Fatalf("%v", networks)
	}
	// the saved lists are the file's alone: the command that changes them never copies the environment into them
	f, _ := ReadFile()
	if len(f.CORSOrigins) != 2 || len(f.CORSNetworks) != 0 {
		t.Fatalf("%+v", f)
	}
}

func TestEnv_AVariableOrAFileAndNeverAValueInAnError(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "s")
	if err := os.WriteFile(file, []byte("  value-in-file \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("X_SECRET", "")
	t.Setenv("X_SECRET_FILE", file)
	if v, err := Env("X_SECRET"); err != nil || v != "value-in-file" {
		t.Fatalf("%q %v", v, err)
	}
	t.Setenv("X_SECRET", " value-in-variable ")
	if v, _ := Env("X_SECRET"); v != "value-in-variable" {
		t.Fatalf("the variable wins: %q", v)
	}
	t.Setenv("X_SECRET", "")
	t.Setenv("X_SECRET_FILE", filepath.Join(dir, "nope"))
	if _, err := Env("X_SECRET"); err == nil || !strings.Contains(err.Error(), "X_SECRET_FILE") || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("%v", err)
	}
	t.Setenv("X_SECRET_FILE", "")
	if v, err := Env("X_SECRET"); v != "" || err != nil {
		t.Fatal("neither is empty, not an error")
	}
}

func TestEnv_EverySecretOfTheStackCanBeAFile(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	put := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Setenv("HEARTMULA_HOST", "http://heartmula:8787")
	t.Setenv("HEARTMULA_TOKEN_FILE", put("hm", "hm-file-secret"))
	t.Setenv("DIFFRHYTHM2_HOST", "http://diffrhythm2:8788")
	t.Setenv("DIFFRHYTHM2_TOKEN_FILE", put("dr", "dr-file-secret"))
	t.Setenv("MONITORING_REDIS_ADDRESS", "redis:6379")
	t.Setenv("MONITORING_REDIS_SECRET_FILE", put("redis", "redis-file-secret"))
	t.Setenv("CTXPRESS_URL", "http://ctxpress:8080")
	t.Setenv("CTXPRESS_TOKEN_FILE", put("cx", "cx-file-secret"))
	if _, tok := HeartMula(); tok != "hm-file-secret" {
		t.Error("HeartMuLa token file")
	}
	if _, tok := DiffRhythm2(); tok != "dr-file-secret" {
		t.Error("DiffRhythm2 token file")
	}
	if GetMonitoring(MonRedis).Secret != "redis-file-secret" {
		t.Error("monitoring secret file")
	}
	if Ctxpress().Token != "cx-file-secret" {
		t.Error("ctxpress token file")
	}
}

// A deploy that still sets the MO_DB_* variables keeps its database (change rename-to-veritmo); when both names are set the new one wins.
func TestDatabase_TheFormerMOVariablesAreStillRead(t *testing.T) {
	isolate(t)
	for _, k := range []string{"DRIVER", "HOST", "PORT", "NAME", "USER", "PASSWORD", "PASSWORD_FILE"} {
		t.Setenv("VERITMO_DB_"+k, "")
	}
	t.Setenv("MO_DB_DRIVER", "mysql")
	t.Setenv("MO_DB_HOST", "old.internal")
	t.Setenv("MO_DB_NAME", "old-name")
	t.Setenv("MO_DB_USER", "old-user")
	t.Setenv("MO_DB_PASSWORD", "old-secret")
	d := GetDatabase()
	if d.Driver != DBMySQL || d.Host != "old.internal" || d.Name != "old-name" || d.User != "old-user" || d.Password != "old-secret" {
		t.Fatalf("old names only: %+v", d)
	}
	t.Setenv("VERITMO_DB_NAME", "new-name")
	t.Setenv("VERITMO_DB_PASSWORD", "new-secret")
	d = GetDatabase()
	if d.Name != "new-name" || d.Password != "new-secret" || d.Host != "old.internal" {
		t.Fatalf("both set, the new one wins: %+v", d)
	}
}

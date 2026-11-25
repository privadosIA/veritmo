package settingsdb

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"veritmo/internal/platform/settings"
	"veritmo/internal/platform/store"
)

const legacySettings = `{
  "ollama_host": "http://10.0.0.2:11434",
  "providers": [{"id": "gemini", "name": "Gemini", "type": "openai", "base_url": "https://example.test/v1"}, {"id": "alpha", "name": "Alpha", "type": "ollama", "base_url": "http://a:11434"}],
  "routing": {"assistant": "gemini"},
  "consent": {"gemini": "https://example.test/v1"},
  "heartmula_host": "http://hm:9", "heartmula_token": "secret-heartmula-token",
  "diffrhythm2_host": "http://dr:9", "diffrhythm2_token": "secret-diffrhythm-token",
  "database": {"driver": "mysql", "host": "db", "port": 3306, "name": "mo", "user": "mo", "password": "secret-db-password"},
  "monitoring": {"grafana": {"address": "http://g:3000", "user": "admin", "secret": "secret-grafana-password"}, "loki": {"address": "http://l:3100"}},
  "server_name": "box",
  "cors_origins": ["http://a:4200"], "cors_networks": ["100.64.0.0/10"],
  "ctxpress": {"enabled": true, "url": "http://127.0.0.1:8080"}
}`

// imported makes a database with settings.json as it was before the upgrade, runs the import and gives the source to the settings package.
func imported(t *testing.T, legacy string) (*Source, *store.Store, []store.ImportRecord) {
	t.Helper()
	src, st := open(t)
	if legacy != "" {
		if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(settings.Path(), []byte(legacy), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	recs, err := st.RunImports(context.Background(), "settings")
	if err != nil {
		t.Fatal(err)
	}
	settings.UseSource(src)
	t.Cleanup(func() { settings.UseSource(nil) })
	return src, st, recs
}

func TestImporter_ATokenSavedBeforeTheUpgradeStillWorksAndIsNotInTheDatabase(t *testing.T) {
	_, st, recs := imported(t, legacySettings)
	if len(recs) != 1 || recs[0].State != store.ImportDone || len(recs[0].Skipped) != 0 {
		t.Fatalf("%+v", recs)
	}
	// 9 settings (the database settings and the server name are not among them) and 2 providers
	if recs[0].Created != 11 {
		t.Fatalf("created %d, want 11: %+v", recs[0].Created, recs[0])
	}
	if host, token := settings.HeartMula(); host != "http://hm:9" || token != "secret-heartmula-token" {
		t.Fatalf("the HeartMuLa test would not authenticate: %q", host)
	}
	if _, token := settings.DiffRhythm2(); token != "secret-diffrhythm-token" {
		t.Fatal("the DiffRhythm2 token is gone")
	}
	if settings.GetMonitoring("grafana").Secret != "secret-grafana-password" || settings.GetMonitoring("grafana").User != "admin" {
		t.Fatal("the monitoring secret is gone")
	}
	for _, id := range []string{KeyHeartMula, KeyDiffRhythm2, MonitoringKey("grafana")} {
		if !settings.HasKey(id) {
			t.Fatalf("the key %q is not in the keys file", id)
		}
	}
	dump := allText(t, st)
	for _, secret := range []string{"secret-heartmula-token", "secret-diffrhythm-token", "secret-grafana-password", "secret-db-password"} {
		if strings.Contains(dump, secret) {
			t.Fatal("a secret is in the database") // the value is not printed
		}
	}
	// the bootstrap fields are not rows; they are still read from the file
	if db := settings.GetDatabase(); db.Host != "db" || db.Password != "secret-db-password" || settings.ServerName() != "box" {
		t.Fatalf("%+v", db)
	}
	if ps := settings.Providers(); len(ps) != 2 || ps[0].ID != "gemini" {
		t.Fatalf("%+v", ps)
	}
	if settings.RoutedProvider("assistant") != "gemini" {
		t.Fatal("the routing is gone")
	}
	// the import changed no file but the keys file
	raw, _ := os.ReadFile(settings.Path())
	if string(raw) != legacySettings {
		t.Fatal("the import changed settings.json")
	}
}

func TestImporter_ASecondImportOverwritesNoRowAndNoKey(t *testing.T) {
	src, st, _ := imported(t, legacySettings)
	// changed since the first import: a setting, a provider, a key the user set
	f, _ := src.Load()
	f.OllamaHost = "http://changed:11434"
	f.Providers = f.Providers[:1]
	f.HeartMulaToken = "token-set-later"
	if err := src.Save(f); err != nil {
		t.Fatal(err)
	}
	recs, err := st.RunImports(context.Background(), "settings")
	if err != nil || recs[0].State != store.ImportDone {
		t.Fatalf("%+v %v", recs, err)
	}
	got, _ := src.Load()
	if got.OllamaHost != "http://changed:11434" || got.HeartMulaToken != "token-set-later" {
		t.Fatalf("an import overwrote what was saved later: %+v", got)
	}
	// a provider that was deleted comes back (the file still has it), as the rule "a row that is missing becomes a row" says; the others stay
	if len(got.Providers) != 2 {
		t.Fatalf("%+v", got.Providers)
	}
	if settings.Key(KeyDiffRhythm2) != "secret-diffrhythm-token" {
		t.Fatal("a key was lost")
	}
}

func TestImporter_CleaningRewritesTheFileWithTheBootstrapFieldsAndNeverDeletesIt(t *testing.T) {
	_, st, _ := imported(t, legacySettings)
	files, err := Importer{}.LegacyFiles()
	if err != nil || len(files) != 1 || files[0].Bytes <= 0 {
		t.Fatalf("%+v %v", files, err)
	}
	res, err := st.CleanLegacy([]string{"settings"})
	if err != nil || len(res) != 1 || res[0].Bytes <= 0 {
		t.Fatalf("%+v %v", res, err)
	}
	raw, err := os.ReadFile(settings.Path())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc) != 2 || doc["database"] == nil || doc["server_name"] != "box" {
		t.Fatalf("settings.json holds more than the database settings and the server name: %s", raw)
	}
	// the server starts with it: the database settings are read, and the rest comes from the database
	if db := settings.GetDatabase(); db.Host != "db" || settings.ServerName() != "box" {
		t.Fatalf("%+v", db)
	}
	if settings.Ollama().Host != "http://10.0.0.2:11434" || settings.Load().Providers[0].ID != "gemini" {
		t.Fatal("the settings are not in the database")
	}
	if files, _ := (Importer{}).LegacyFiles(); len(files) != 0 {
		t.Fatalf("a clean file is listed again: %+v", files)
	}
	// with nothing in the bootstrap either the file stays, empty
	_ = os.WriteFile(settings.Path(), []byte(`{"ollama_host": "x"}`), 0o600)
	if _, err := st.CleanLegacy([]string{"settings"}); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(settings.Path()); err != nil || strings.TrimSpace(string(raw)) != "{}" {
		t.Fatalf("%q %v", raw, err)
	}
}

func TestImporter_NoFileAndAFileThatCannotBeReadAreNotFailures(t *testing.T) {
	_, st, recs := imported(t, "")
	if recs[0].State != store.ImportDone || recs[0].Created != 0 || len(recs[0].Skipped) != 0 {
		t.Fatalf("%+v", recs[0])
	}
	if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(settings.Path(), []byte("{not json"), 0o600)
	recs, err := st.RunImports(context.Background(), "settings")
	if err != nil || recs[0].State != store.ImportDone || len(recs[0].Skipped) != 1 {
		t.Fatalf("%+v %v", recs, err)
	}
	// the import that skipped the file is not cleaned: its content is not in the database
	if _, err := st.CleanLegacy([]string{"settings"}); err == nil {
		t.Fatal("a file that was not imported was cleaned")
	}
}

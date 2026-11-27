package settingsdb

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"veritmo/internal/platform/settings"
	"veritmo/internal/platform/store"
)

// open makes a database in memory and an isolated config directory: a test never reads or writes the real ~/.config/veritmo.
func open(t *testing.T) (*Source, *store.Store) {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("HOME", cfg)
	reg := store.NewRegistry()
	RegisterStore(reg)
	st, err := store.OpenMemory(reg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	return New(st), st
}

const (
	secretHM   = "secret-heartmula-token"
	secretDR   = "secret-diffrhythm-token"
	secretGraf = "secret-grafana-password"
	secretRed  = "secret-redis-password"
)

func fullFile() settings.File {
	return settings.File{
		OllamaHost: "http://10.0.0.2:11434", Browser: "/usr/bin/chromium", MotionModel: "gemma4:31b",
		Providers: []settings.Provider{
			{ID: "gemini", Name: "Gemini", Type: "openai", BaseURL: "https://example.test/v1", TextModel: "m1", VisionModel: "m2", Preset: "gemini", TestedAt: "2026-11-19T10:00:00Z", TestOK: true},
			{ID: "alpha", Name: "Alpha", Type: "ollama", BaseURL: "http://a:11434"},
		},
		Routing: map[string]string{"assistant": "gemini", "check": "alpha"}, Consent: map[string]string{"gemini": "https://example.test/v1"},
		Remote:        &settings.RemoteSettings{MaxCalls: 12},
		HeartMulaHost: "http://hm:9", HeartMulaToken: secretHM,
		DiffRhythm2Host: "http://dr:9", DiffRhythm2Token: secretDR,
		Monitoring: map[string]settings.Endpoint{
			"grafana": {Address: "http://g:3000", User: "admin", Secret: secretGraf},
			"redis":   {Address: "redis://r:6379", Secret: secretRed},
			"loki":    {Address: "http://l:3100"},
		},
		CORSOrigins: []string{"http://a:4200"}, CORSNetworks: []string{"100.64.0.0/10"}, ReviewDrafts: true,
		Ctxpress:           &settings.CtxpressSettings{Enabled: true, URL: "http://127.0.0.1:8080", MinChars: 2000},
		LicenceAcceptances: map[string]settings.LicenceAcceptance{"stability-community": {At: time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC), Client: "model-studio"}},
	}
}

func TestSource_AFullFileMakesARoundTrip(t *testing.T) {
	src, _ := open(t)
	f := fullFile()
	if err := src.Save(f); err != nil {
		t.Fatal(err)
	}
	got, err := src.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, f) {
		t.Fatalf("round trip:\n got  %+v\n want %+v", got, f)
	}
	// the providers keep the order the user gave them
	if got.Providers[0].ID != "gemini" || got.Providers[1].ID != "alpha" {
		t.Fatalf("%+v", got.Providers)
	}
	// the bootstrap fields are not its business
	f.ServerName, f.Database = "box", settings.Database{Driver: "mysql", Host: "db"}
	if err := src.Save(f); err != nil {
		t.Fatal(err)
	}
	if got, _ = src.Load(); got.ServerName != "" || got.Database != (settings.Database{}) {
		t.Fatalf("the bootstrap fields were stored: %+v %+v", got.ServerName, got.Database)
	}
}

func TestSource_AnEmptyFileHasNoRowsAndASettingThatIsGoneLeavesNoRow(t *testing.T) {
	src, st := open(t)
	if got, err := src.Load(); err != nil || !reflect.DeepEqual(got, settings.File{}) {
		t.Fatalf("%+v %v", got, err)
	}
	_ = src.Save(fullFile())
	f := fullFile()
	f.Browser, f.Routing, f.ReviewDrafts, f.Providers = "", nil, false, f.Providers[:1]
	if err := src.Save(f); err != nil {
		t.Fatal(err)
	}
	var n int64
	st.DB.Model(&Setting{}).Where("setting_key IN ?", []string{"browser", "routing", "music_motion_review_drafts"}).Count(&n)
	if n != 0 {
		t.Fatalf("%d rows of settings that are gone", n)
	}
	st.DB.Model(&Provider{}).Count(&n)
	if n != 1 {
		t.Fatalf("%d providers, want 1", n)
	}
	if err := src.Save(settings.File{}); err != nil {
		t.Fatal(err)
	}
	st.DB.Model(&Setting{}).Count(&n)
	if n != 0 {
		t.Fatalf("%d rows left", n)
	}
}

func TestSource_ASaveThatChangesOneFieldWritesOneRow(t *testing.T) {
	src, st := open(t)
	_ = src.Save(fullFile())
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	st.DB.Model(&Setting{}).Where("1 = 1").Update("updated_at", old)
	st.DB.Model(&Provider{}).Where("1 = 1").Update("updated_at", old)
	f := fullFile()
	f.OllamaHost = "http://10.0.0.9:11434"
	if err := src.Save(f); err != nil {
		t.Fatal(err)
	}
	var changed []Setting
	st.DB.Where("updated_at > ?", old).Find(&changed)
	if len(changed) != 1 || changed[0].Key != "ollama_host" {
		t.Fatalf("rows written: %+v", changed)
	}
	var providers int64
	st.DB.Model(&Provider{}).Where("updated_at > ?", old).Count(&providers)
	if providers != 0 {
		t.Fatalf("%d providers written for nothing", providers)
	}
	// moving a provider writes the providers that moved, not the settings
	f.Providers[0], f.Providers[1] = f.Providers[1], f.Providers[0]
	_ = src.Save(f)
	st.DB.Model(&Provider{}).Where("updated_at > ?", old).Count(&providers)
	if providers != 2 {
		t.Fatalf("%d providers written, want the 2 that moved", providers)
	}
}

// allText is every value of every table of the database as text, to look for a secret in it.
func allText(t *testing.T, st *store.Store) string {
	t.Helper()
	var tables []string
	if err := st.DB.Raw("SELECT name FROM sqlite_master WHERE type = 'table'").Scan(&tables).Error; err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for _, table := range tables {
		rows, err := st.DB.Raw("SELECT * FROM " + table).Rows()
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for _, v := range vals {
				fmt.Fprintf(&out, "%v\n", v)
			}
		}
		rows.Close()
	}
	return out.String()
}

func TestSource_NoSecretIsInTheDatabaseAndEveryOneIsInTheKeysFile(t *testing.T) {
	src, st := open(t)
	if err := src.Save(fullFile()); err != nil {
		t.Fatal(err)
	}
	dump := allText(t, st)
	for _, secret := range []string{secretHM, secretDR, secretGraf, secretRed} {
		if strings.Contains(dump, secret) {
			t.Fatal("a secret is in a row of the database") // the value is not printed
		}
	}
	for id, want := range map[string]string{KeyHeartMula: secretHM, KeyDiffRhythm2: secretDR, MonitoringKey("grafana"): secretGraf, MonitoringKey("redis"): secretRed} {
		if settings.Key(id) != want {
			t.Fatalf("the key %q is not in the keys file", id)
		}
	}
	if info, err := os.Stat(settings.KeysPath()); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("keys file: %v %v", info, err)
	}
	// a tool that is removed and a token that is cleared leave nothing behind in the keys file; the other keys stay
	_ = settings.SetKey("gemini", "provider-key")
	f := fullFile()
	delete(f.Monitoring, "redis")
	f.DiffRhythm2Token = ""
	if err := src.Save(f); err != nil {
		t.Fatal(err)
	}
	if settings.HasKey(MonitoringKey("redis")) || settings.HasKey(KeyDiffRhythm2) {
		t.Fatal("a secret that was removed is still in the keys file")
	}
	if settings.Key(MonitoringKey("grafana")) != secretGraf || settings.Key("gemini") != "provider-key" || settings.Key(KeyHeartMula) != secretHM {
		t.Fatal("a save removed a key that is still in use")
	}
}

func TestSource_BehindTheSettingsFunctionsTheCodeThatReadsATokenDoesNotChange(t *testing.T) {
	src, st := open(t)
	settings.UseSource(src)
	t.Cleanup(func() { settings.UseSource(nil) })
	if err := settings.SetHeartMula("http://hm:9", secretHM); err != nil {
		t.Fatal(err)
	}
	if host, token := settings.HeartMula(); host != "http://hm:9" || token != secretHM {
		t.Fatalf("%q %q", host, token)
	}
	if err := settings.SetMonitoring("grafana", settings.Endpoint{Address: "http://g:3000", User: "u", Secret: secretGraf}); err != nil {
		t.Fatal(err)
	}
	// a form that never shows the secret saves again and keeps it
	if err := settings.SetMonitoring("grafana", settings.Endpoint{Address: "http://g:3000", User: "u"}); err != nil {
		t.Fatal(err)
	}
	if settings.GetMonitoring("grafana").Secret != secretGraf {
		t.Fatal("the secret was lost")
	}
	if dump := allText(t, st); strings.Contains(dump, secretHM) || strings.Contains(dump, secretGraf) {
		t.Fatal("a secret is in a row of the database")
	}
	// the file of the settings was not written for any of it (no bootstrap field changed)
	if _, err := os.Stat(settings.Path()); !os.IsNotExist(err) {
		t.Fatalf("settings.json was written: %v", err)
	}
}

func TestSource_TheColoursOfTheClientLabelsMakeARoundTrip(t *testing.T) {
	src, _ := open(t)
	f := settings.File{ClientColors: map[string]string{"dashboard-full": "#3366cc", "claude-test": "#aa5500"}}
	if err := src.Save(f); err != nil {
		t.Fatal(err)
	}
	got, err := src.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.ClientColors, f.ClientColors) {
		t.Fatalf("%v", got.ClientColors)
	}
	// removing the last one leaves no row
	if err := src.Save(settings.File{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := src.Load(); len(got.ClientColors) != 0 {
		t.Errorf("a colour is left: %v", got.ClientColors)
	}
}

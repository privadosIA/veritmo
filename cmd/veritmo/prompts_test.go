package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pstore "veritmo/internal/platform/store"
	"veritmo/internal/promptfetch"
	"veritmo/internal/prompts"
	"veritmo/internal/server"
	"veritmo/internal/server/workspace"
)

type fakePromptsNet struct{ file string }

func (f fakePromptsNet) Card(_ context.Context, repo string) (promptfetch.Card, error) {
	var c promptfetch.Card
	c.CardData.License = json.RawMessage(`"apache-2.0"`)
	c.SHA = "deadbeef"
	return c, nil
}

func (f fakePromptsNet) Download(_ context.Context, repo, file, dest string) error {
	b, err := os.ReadFile(f.file)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dest, b, 0o644)
}

func promptsFixture(t *testing.T) (sources, dir string, d deps) {
	t.Helper()
	dir = t.TempDir()
	data := filepath.Join(dir, "data.json")
	_ = os.WriteFile(data, []byte(`[{"p":"A drone flight over a snowy ridge","id":"1"},{"p":"A cat asleep on a warm windowsill","id":"2"},{"p":"short","id":"3"}]`), 0o644)
	sources = filepath.Join(dir, "sources.json")
	_ = os.WriteFile(sources, []byte(`{"version":1,"sources":[
	 {"id":"clips","repo":"o/clips","files":["data.json"],"format":"json","kind":"video","columns":{"prompt":"p","source_id":"id"},"expected_licence":"apache-2.0","max_rows":10},
	 {"id":"off","enabled":false,"check":"off for a reason","repo":"o/off","files":["data.json"],"format":"json","kind":"video","columns":{"prompt":"p"},"expected_licence":"apache-2.0","max_rows":10}]}`), 0o644)
	d = realDeps()
	d.promptsNet = func() promptfetch.Net { return fakePromptsNet{file: data} }
	return sources, dir, d
}

func runP(d deps, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := d.prompts(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestPromptsFetch_WritesTheDatabaseAndTheSecondRunAddsNothing(t *testing.T) {
	isolate(t)
	sources, dir, d := promptsFixture(t)
	media := filepath.Join(dir, "media")
	db := migrated(t)
	code, out, errs := runP(d, "fetch", "--sources", sources, "--media-dir", media, "--db", db)
	if code != 0 || !strings.Contains(out, "wrote 2 presets to the database (0 were already there)") {
		t.Fatalf("%d\n%s\n%s", code, out, errs)
	}
	if files, _ := filepath.Glob(filepath.Join(media, "prompts", "video", "*.json")); len(files) != 0 || !strings.Contains(out, "off for a reason") {
		t.Fatalf("no preset file is written any more: %v\n%s", files, out)
	}
	if _, err := os.Stat(filepath.Join(media, "prompts", ".cache", "report.md")); err != nil {
		t.Errorf("the report and the downloads still go to the cache folder: %v", err)
	}
	st, err := openStore(db, "projects", settingsZero())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	all, total, err := prompts.NewDB(st).List(prompts.Filter{Kind: "video"}, pstore.ListParams{})
	if err != nil || total != 2 || all[0].Own || all[0].Source == "" || all[0].Licence == "" {
		t.Fatalf("%+v %d %v", all, total, err)
	}
	code, out, _ = runP(d, "fetch", "--sources", sources, "--media-dir", media, "--db", db)
	if code != 0 || !strings.Contains(out, "wrote 0 presets") || !strings.Contains(out, "(2 were already there)") {
		t.Fatalf("%d\n%s", code, out)
	}
	// a database that is not migrated is not written to: the command says what to do, before it downloads anything
	code, _, errs = runP(d, "fetch", "--sources", sources, "--media-dir", media, "--db", filepath.Join(t.TempDir(), "new.db"))
	if code != 1 || !strings.Contains(errs, "veritmo data migrate") {
		t.Fatalf("%d %s", code, errs)
	}
}

// Presets fetched while the server runs: the server and the command share the database, and the next list has them, with no restart.
func TestPromptsFetch_TheRunningServerListsWhatTheCommandWrote(t *testing.T) {
	isolate(t)
	sources, dir, d := promptsFixture(t)
	db := migrated(t)
	st, err := openStore(db, "projects", settingsZero())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv, err := server.New(server.Config{Addr: "127.0.0.1:0", ProjectsDir: filepath.Join(dir, "projects"), Store: st, Sampler: workspace.NoHardware()})
	if err != nil {
		t.Fatal(err)
	}
	get := func() int {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/prompts?kind=video", nil))
		var a struct{ Total int }
		_ = json.Unmarshal(rec.Body.Bytes(), &a)
		return a.Total
	}
	if get() != 0 {
		t.Fatal("presets before the command")
	}
	if code, out, errs := runP(d, "fetch", "--sources", sources, "--media-dir", filepath.Join(dir, "media"), "--db", db); code != 0 {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	if got := get(); got != 2 {
		t.Fatalf("the server lists %d presets, want the 2 the command wrote", got)
	}
}

func TestPromptsFetch_BundleDryRunAndErrors(t *testing.T) {
	sources, dir, d := promptsFixture(t)
	media := filepath.Join(dir, "media")
	bundle := filepath.Join(dir, "b.json")
	if code, out, errs := runP(d, "fetch", "--sources", sources, "--media-dir", media, "--out", bundle); code != 0 || !strings.Contains(out, "wrote 2 presets to "+bundle) {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	if _, err := os.Stat(filepath.Join(media, "prompts", "video")); err == nil {
		t.Error("--out writes no store")
	}
	if b, _ := os.ReadFile(bundle); !strings.Contains(string(b), `"format":"prompt-presets"`) {
		t.Errorf("%s", b)
	}
	os.RemoveAll(media)
	if code, out, _ := runP(d, "fetch", "--sources", sources, "--media-dir", media, "--dry-run"); code != 0 || !strings.Contains(out, "dry run: 2 presets") {
		t.Fatalf("%d %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(media, "prompts", "video")); err == nil {
		t.Error("a dry run writes no presets")
	}
	if _, err := os.Stat(filepath.Join(media, "prompts", ".cache", "report.md")); err == nil {
		t.Error("a dry run saves no report (the downloads stay in the cache)")
	}
	// the source that is off runs when it is named
	if code, out, _ := runP(d, "fetch", "--sources", sources, "--media-dir", media, "--source", "off", "--dry-run"); code != 0 || !strings.Contains(out, "### off (o/off)") {
		t.Fatalf("%d %s", code, out)
	}
	for name, args := range map[string][]string{
		"no subcommand": {}, "other subcommand": {"nope"}, "unknown source": {"fetch", "--sources", sources, "--media-dir", media, "--source", "zzz"},
		"missing sources file": {"fetch", "--sources", filepath.Join(dir, "none.json")}, "negative max": {"fetch", "--max", "-1"}, "extra argument": {"fetch", "x"},
	} {
		if code, _, _ := runP(d, args...); code == 0 {
			t.Errorf("%s succeeded", name)
		}
	}
	if code, _, errs := runP(d, "fetch", "--sources", sources, "--media-dir", media, "--source", "zzz"); code != 1 || !strings.Contains(errs, "clips") {
		t.Errorf("an unknown source names the ones there are: %s", errs)
	}
}

func TestPromptsFetch_AKindWithNoPublicSourceSaysSoAndFetchesNothing(t *testing.T) {
	sources, dir, d := promptsFixture(t)
	media := filepath.Join(dir, "media")
	code, out, errs := runP(d, "fetch", "--sources", sources, "--media-dir", media, "--kind", "model3d")
	if code != 0 || !strings.Contains(out, "no public source of prompts of kind model3d") || !strings.Contains(out, "Nothing was fetched") {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	if _, err := os.Stat(filepath.Join(media, "prompts")); err == nil {
		t.Error("nothing is written")
	}
	// a kind that has sources runs only those; one that is none is an error naming them
	if code, out, errs := runP(d, "fetch", "--sources", sources, "--media-dir", media, "--kind", "video", "--dry-run"); code != 0 || !strings.Contains(out, "dry run") {
		t.Errorf("video: %d %s %s", code, out, errs)
	}
	if code, _, errs := runP(d, "fetch", "--sources", sources, "--media-dir", media, "--kind", "audio"); code != 2 || !strings.Contains(errs, "model3d") {
		t.Errorf("an unknown kind: %d %s", code, errs)
	}
}

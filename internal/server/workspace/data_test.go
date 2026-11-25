package workspace

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"veritmo/internal/platform/settings"
	"veritmo/internal/platform/store"
)

// dataImporter is an entity whose state is the .json files of a folder; a file that starts with "bad" cannot be read.
type dataImporter struct {
	name    string
	dir     string
	hold    chan struct{}
	started chan struct{}
	once    sync.Once
}

func (d *dataImporter) Entity() string { return d.name }
func (d *dataImporter) LegacyFiles() ([]store.LegacyFile, error) {
	m, _ := filepath.Glob(filepath.Join(d.dir, "*.json"))
	var out []store.LegacyFile
	for _, p := range m {
		fi, _ := os.Stat(p)
		out = append(out, store.LegacyFile{Path: p, Bytes: fi.Size()})
	}
	return out, nil
}
func (d *dataImporter) Import(_ context.Context, tx *store.Store) (store.Report, error) {
	if d.started != nil {
		d.once.Do(func() { close(d.started) })
	}
	if d.hold != nil {
		<-d.hold
	}
	var rep store.Report
	files, _ := d.LegacyFiles()
	var rows []store.DataImport
	for _, f := range files {
		b, _ := os.ReadFile(f.Path)
		if strings.HasPrefix(string(b), "bad") {
			rep.Skipped = append(rep.Skipped, store.Skipped{Path: f.Path, Reason: "not valid"})
			continue
		}
		// the rows of this fake entity are records of the imports table, which every database has
		rows = append(rows, store.DataImport{Entity: d.name + "/" + filepath.Base(f.Path), State: "row"})
	}
	var err error
	rep.Created, rep.Existing, err = store.InsertMissing(tx, rows)
	return rep, err
}

func newDataImporter(t *testing.T, name string, files map[string]string) *dataImporter {
	t.Helper()
	dir := t.TempDir()
	for n, c := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &dataImporter{name: name, dir: dir}
}

// newDataServer is a workspace on a database that knows these importers (the registry is built here, nothing is registered globally).
func newDataServer(t *testing.T, importers ...*dataImporter) *testServer {
	t.Helper()
	reg := store.NewRegistry()
	RegisterStore(reg)
	for _, i := range importers {
		reg.AddImporter(i)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "o.db"), settings.Database{}, reg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	return newTestServerWith(t, Config{ProjectsDir: t.TempDir(), Store: st})
}

func TestData_StatusSaysTheEngineTheMigrationsTheTablesTheImportsAndTheFilesLeft(t *testing.T) {
	s := newDataServer(t, newDataImporter(t, "test/status", map[string]string{"a.json": "12345", "b.json": "123"}))
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/data/status", nil)
	var st store.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if st.Engine != "sqlite" || len(st.Migrations.Applied) < 2 || len(st.Migrations.Pending) != 0 {
		t.Fatalf("%+v", st.Migrations)
	}
	tables := map[string]int64{}
	for _, tr := range st.Tables {
		tables[tr.Name] = tr.Rows
	}
	if _, ok := tables["runs"]; !ok || len(st.Imports) != 0 {
		t.Fatalf("tables %v imports %v", tables, st.Imports)
	}
	var found bool
	for _, l := range st.LegacyFiles {
		if l.Entity == "test/status" {
			found = l.Files == 2 && l.Bytes == 8
		}
	}
	if !found {
		t.Fatalf("legacy files %+v", st.LegacyFiles)
	}
}

func TestData_ImportRunsTheOnesNotDoneAskingForOneRunsItAgainAndAnUnknownOneIs404(t *testing.T) {
	s := newDataServer(t, newDataImporter(t, "test/imp", map[string]string{"a.json": "x", "b.json": "x"}))
	h := s.Handler()
	var out struct {
		Imports []store.ImportRecord `json:"imports"`
	}
	rec := doJSON(t, h, http.MethodPost, "/api/data/import", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK || len(out.Imports) != 1 || out.Imports[0].Created != 2 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec = doJSON(t, h, http.MethodPost, "/api/data/import", nil)
	out.Imports = nil
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusOK || len(out.Imports) != 0 || !strings.Contains(rec.Body.String(), `"imports":[]`) {
		t.Fatalf("nothing left to import: %d %s", rec.Code, rec.Body)
	}
	rec = doJSON(t, h, http.MethodPost, "/api/data/import", map[string]any{"entity": "test/imp"})
	out.Imports = nil
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusOK || len(out.Imports) != 1 || out.Imports[0].Created != 0 || out.Imports[0].Existing != 2 {
		t.Fatalf("again: %d %s", rec.Code, rec.Body)
	}
	rec = doJSON(t, h, http.MethodPost, "/api/data/import", map[string]any{"entity": "nope"})
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "test/imp") {
		t.Fatalf("an unknown entity names the ones that exist: %d %s", rec.Code, rec.Body)
	}
}

func TestData_AnImportAskedForWhileAnotherRunsIs409(t *testing.T) {
	d := newDataImporter(t, "test/slow", map[string]string{"a.json": "x"})
	d.hold, d.started = make(chan struct{}), make(chan struct{})
	s := newDataServer(t, d)
	h := s.Handler()
	done := make(chan int)
	go func() { done <- doJSON(t, h, http.MethodPost, "/api/data/import", nil).Code }()
	<-d.started
	if rec := doJSON(t, h, http.MethodPost, "/api/data/import", map[string]any{"entity": "test/slow"}); rec.Code != http.StatusConflict {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	close(d.hold)
	if code := <-done; code != http.StatusOK {
		t.Fatalf("the first import ended with %d", code)
	}
}

func TestData_ExportIsAZipWithTheTablesAndItsManifest(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/data/export", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/zip" || !strings.Contains(rec.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	for _, n := range []string{"manifest.json", "runs.jsonl", "data_imports.jsonl"} {
		if !names[n] {
			t.Errorf("missing %s in %v", n, names)
		}
	}
}

func TestData_CleanDeletesTheFilesOfAnEntityImportedWithNoErrorsAndRefusesTheRest(t *testing.T) {
	good := newDataImporter(t, "test/good", map[string]string{"a.json": "ok"})
	skip := newDataImporter(t, "test/skip", map[string]string{"a.json": "ok", "broken.json": "bad"})
	notYet := newDataImporter(t, "test/notyet", map[string]string{"a.json": "ok"})
	s := newDataServer(t, good, skip, notYet)
	h := s.Handler()
	media := filepath.Join(good.dir, "clip.mp4")
	if err := os.WriteFile(media, []byte("m"), 0o644); err != nil {
		t.Fatal(err)
	}
	doJSON(t, h, http.MethodPost, "/api/data/import", map[string]any{"entity": "test/good"})
	doJSON(t, h, http.MethodPost, "/api/data/import", map[string]any{"entity": "test/skip"})
	if rec := doJSON(t, h, http.MethodPost, "/api/data/legacy/clean", map[string]any{"entities": []string{"test/skip"}}); rec.Code != http.StatusConflict {
		t.Fatalf("an entity with a skipped file: %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, h, http.MethodPost, "/api/data/legacy/clean", map[string]any{"entities": []string{"test/notyet"}}); rec.Code != http.StatusConflict {
		t.Fatalf("an entity not imported: %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, h, http.MethodPost, "/api/data/legacy/clean", map[string]any{"entities": []string{"nope"}}); rec.Code != http.StatusNotFound {
		t.Fatalf("%d", rec.Code)
	}
	if rec := doJSON(t, h, http.MethodPost, "/api/data/legacy/clean", map[string]any{"entities": []string{}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("%d", rec.Code)
	}
	rec := doJSON(t, h, http.MethodPost, "/api/data/legacy/clean", map[string]any{"entities": []string{"test/good"}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"deleted":1`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(good.dir, "a.json")); !os.IsNotExist(err) {
		t.Fatal("the state file must be gone")
	}
	if _, err := os.Stat(media); err != nil {
		t.Fatal("the media must stay")
	}
}

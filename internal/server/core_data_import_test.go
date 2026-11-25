package server

import (
	"context"
	"path/filepath"
	"testing"

	"veritmo/internal/platform/settings"
	"veritmo/internal/platform/store"
	"veritmo/internal/server/workspace"
)

type startImporter struct{ ran int }

func (*startImporter) Entity() string                           { return "test/start" }
func (*startImporter) LegacyFiles() ([]store.LegacyFile, error) { return nil, nil }
func (i *startImporter) Import(context.Context, *store.Store) (store.Report, error) {
	i.ran++
	return store.Report{Created: 3}, nil
}

func TestTheStateKeptInFilesIsImportedWhenTheServerStarts(t *testing.T) {
	imp := &startImporter{}
	reg := DataRegistry("")
	reg.AddImporter(imp)
	st, err := store.Open(filepath.Join(t.TempDir(), "o.db"), settings.Database{}, reg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{Addr: "127.0.0.1:0", ProjectsDir: t.TempDir(), Store: st, Sampler: workspace.NoHardware()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	quiet(s)
	if imp.ran != 1 {
		t.Fatalf("the importer ran %d times before the first request", imp.ran)
	}
	recs, err := s.store.ImportRecords()
	if err != nil {
		t.Fatal(err)
	}
	var got *store.ImportRecord
	for i := range recs {
		if recs[i].Entity == "test/start" {
			got = &recs[i]
		}
	}
	if got == nil || got.State != store.ImportDone || got.Created != 3 {
		t.Fatalf("%+v", recs)
	}
}

func TestTheDataRegistryHasWhatEveryAreaOwnsAndNothingRegistersItself(t *testing.T) {
	a, b := DataRegistry(""), DataRegistry("")
	base := len(b.Importers())
	a.AddImporter(&startImporter{})
	if len(b.Importers()) != base || len(a.Importers()) != base+1 {
		t.Fatal("two registries share nothing")
	}
	names := map[string]bool{}
	for _, m := range b.Migrations() {
		names[m.ID] = true
	}
	if !names["202611181200_history"] || !names["202611181210_data_imports"] {
		t.Fatalf("the registry lacks the migrations of the history or of the imports: %v", names)
	}
}

package workspace

import (
	"testing"
	"time"

	"veritmo/internal/platform/store"
)

func TestTheHistoryMigrationGivesTheTablesOfTheModels(t *testing.T) {
	st, err := openMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	missing, err := st.CheckSchema()
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) > 0 {
		t.Fatalf("the models have what the migrations do not create (a model changed without its migration): %v", missing)
	}
}

func TestADatabaseWithTheHistoryOfBeforeKeepsItsRunsWhenItIsMigrated(t *testing.T) {
	st, err := openMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// What the server of before did: AutoMigrate of the models, no record of any migration.
	if err := st.DB.AutoMigrate(historyModels()...); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.Create(&Run{ID: "run-1", Project: "p", State: StateSucceeded, CreatedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	var n int64
	st.DB.Model(&Run{}).Count(&n)
	applied, pending, _ := st.Migrations()
	if n != 1 || len(pending) != 0 || len(applied) < 2 {
		t.Fatalf("runs %d, applied %v, pending %v", n, applied, pending)
	}
	h, err := openHistoryOn(st, "x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.getRun("run-1"); err != nil {
		t.Fatal(err)
	}
}

func TestTheCopyBetweenTwoDatabasesCopiesTheHistoryAndEveryOtherTable(t *testing.T) {
	open := func() *store.Store {
		st, err := openMemory()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		if err := st.Migrate(); err != nil {
			t.Fatal(err)
		}
		return st
	}
	src, dst := open(), open()
	now := time.Now()
	for _, id := range []string{"r1", "r2"} {
		if err := src.DB.Create(&Run{ID: id, Project: "p", State: StateSucceeded, CreatedAt: now}).Error; err != nil {
			t.Fatal(err)
		}
	}
	src.DB.Create(&RunEvent{RunID: "r1", Seq: 1, Line: "{}"})
	if err := src.DB.Create(&store.DataImport{Entity: "test/x", State: "done", Created: 5, StartedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	res, err := copyStores(src, dst, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if res.Copied["runs"] != 2 || res.Copied["run_events"] != 1 || res.Copied["data_imports"] != 1 {
		t.Fatalf("%+v", res)
	}
	var run Run
	dst.DB.First(&run, "id = ?", "r1")
	if run.Server != "alpha" {
		t.Fatalf("the rows of the file are this server's: %q", run.Server)
	}
	again, err := copyStores(src, dst, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if again.Copied["runs"] != 0 || again.Existing["runs"] != 2 || again.Copied["data_imports"] != 0 || again.Existing["data_imports"] != 1 {
		t.Fatalf("a second copy adds nothing: %+v", again)
	}
}

// openMemory is a database in memory that knows what the history owns.
func openMemory() (*store.Store, error) {
	reg := store.NewRegistry()
	RegisterStore(reg)
	return store.OpenMemory(reg)
}

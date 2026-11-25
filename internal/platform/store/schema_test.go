package store

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
)

func migCreate(id, table string) *Migration {
	return &Migration{
		ID: id,
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec("CREATE TABLE " + table + " (id integer primary key)").Error
		},
		Rollback: func(tx *gorm.DB) error { return tx.Exec("DROP TABLE " + table).Error },
	}
}

func ids(a []AppliedMigration) string {
	var out []string
	for _, m := range a {
		out = append(out, m.ID)
	}
	return strings.Join(out, ",")
}

func TestANewDatabaseGetsEveryMigrationRecordedWithItsDate(t *testing.T) {
	eachEngine(t, func(t *testing.T) {
		s := newMem(t)
		list := []*Migration{migCreate("202601010000_a", "ta"), migCreate("202601020000_b", "tb")}
		before := time.Now().UTC().Add(-2 * time.Second)
		if err := s.migrateWith(list); err != nil {
			t.Fatal(err)
		}
		applied, pending, err := s.migrationsWith(list)
		if err != nil {
			t.Fatal(err)
		}
		if ids(applied) != "202601010000_a,202601020000_b" || len(pending) != 0 {
			t.Fatalf("applied %v pending %v", applied, pending)
		}
		for _, a := range applied {
			if a.AppliedAt.Before(before) || a.AppliedAt.After(time.Now().UTC().Add(2*time.Second)) {
				t.Errorf("%s applied at %v", a.ID, a.AppliedAt)
			}
		}
		if !s.DB.Migrator().HasTable("ta") || !s.DB.Migrator().HasTable("tb") {
			t.Fatal("the tables of the migrations must exist")
		}
	})
}

func TestASecondMigrateRunsNothingAndANewMigrationRunsOnlyItself(t *testing.T) {
	eachEngine(t, func(t *testing.T) {
		s := newMem(t)
		runs := 0
		counting := func(id, table string) *Migration {
			m := migCreate(id, table)
			inner := m.Migrate
			m.Migrate = func(tx *gorm.DB) error { runs++; return inner(tx) }
			return m
		}
		list := []*Migration{counting("202601010000_a", "ta")}
		if err := s.migrateWith(list); err != nil {
			t.Fatal(err)
		}
		if err := s.migrateWith(list); err != nil {
			t.Fatal(err)
		}
		if runs != 1 {
			t.Fatalf("the migration ran %d times", runs)
		}
		list = append(list, counting("202601020000_b", "tb"))
		if err := s.migrateWith(list); err != nil {
			t.Fatal(err)
		}
		if runs != 2 {
			t.Fatalf("after adding one, %d runs in all", runs)
		}
	})
}

func TestAnUpgradeKeepsTheRowsThatExisted(t *testing.T) {
	eachEngine(t, func(t *testing.T) {
		s := newMem(t)
		if err := s.migrateWith([]*Migration{migCreate("202601010000_a", "ta")}); err != nil {
			t.Fatal(err)
		}
		if err := s.DB.Exec("INSERT INTO ta (id) VALUES (7)").Error; err != nil {
			t.Fatal(err)
		}
		if err := s.migrateWith([]*Migration{migCreate("202601010000_a", "ta"), migCreate("202601020000_b", "tb")}); err != nil {
			t.Fatal(err)
		}
		var n int64
		s.DB.Table("ta").Count(&n)
		if n != 1 {
			t.Fatalf("%d rows", n)
		}
	})
}

func TestADatabaseThatHasTheTablesOfBeforeIsRecordedAndKeepsItsRows(t *testing.T) {
	eachEngine(t, func(t *testing.T) {
		s := newMem(t)
		if err := s.DB.Exec("CREATE TABLE ta (id integer primary key)").Error; err != nil {
			t.Fatal(err)
		}
		if err := s.DB.Exec("INSERT INTO ta (id) VALUES (1), (2)").Error; err != nil {
			t.Fatal(err)
		}
		m := &Migration{ID: "202601010000_a", Migrate: func(tx *gorm.DB) error {
			if tx.Migrator().HasTable("ta") {
				return nil
			}
			return tx.Exec("CREATE TABLE ta (id integer primary key)").Error
		}}
		if err := s.migrateWith([]*Migration{m}); err != nil {
			t.Fatal(err)
		}
		var n int64
		s.DB.Table("ta").Count(&n)
		applied, _, _ := s.migrationsWith([]*Migration{m})
		if n != 2 || ids(applied) != "202601010000_a" {
			t.Fatalf("rows %d applied %v", n, applied)
		}
	})
}

func TestADatabaseWithAMigrationTheProgramDoesNotKnowIsRefusedNamingIt(t *testing.T) {
	eachEngine(t, func(t *testing.T) {
		s := newMem(t)
		newer := []*Migration{migCreate("202601010000_a", "ta"), migCreate("202602010000_from_the_future", "tf")}
		if err := s.migrateWith(newer); err != nil {
			t.Fatal(err)
		}
		err := s.migrateWith(newer[:1])
		if !errors.Is(err, ErrUnknownMigrations) || !strings.Contains(err.Error(), "202602010000_from_the_future") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestOnlyTheLastMigrationCanBeUndoneAndItsRecordGoesWithIt(t *testing.T) {
	eachEngine(t, func(t *testing.T) {
		s := newMem(t)
		list := []*Migration{migCreate("202601010000_a", "ta"), migCreate("202601020000_b", "tb")}
		if err := s.migrateWith(list); err != nil {
			t.Fatal(err)
		}
		if err := s.rollbackWith(list, "202601010000_a"); err == nil || !strings.Contains(err.Error(), "not the last") {
			t.Fatalf("undoing a migration that is not the last: %v", err)
		}
		if !s.DB.Migrator().HasTable("ta") {
			t.Fatal("nothing must change when a rollback is refused")
		}
		if err := s.rollbackWith(list, "202601020000_b"); err != nil {
			t.Fatal(err)
		}
		if s.DB.Migrator().HasTable("tb") {
			t.Fatal("the table of the undone migration must be gone")
		}
		applied, pending, _ := s.migrationsWith(list)
		if ids(applied) != "202601010000_a" || len(pending) != 1 || pending[0] != "202601020000_b" {
			t.Fatalf("applied %v pending %v", applied, pending)
		}
		if err := s.migrateWith(list); err != nil || !s.DB.Migrator().HasTable("tb") {
			t.Fatalf("the next migrate must apply it again: %v", err)
		}
	})
}

func TestAMigrationThatCannotBeUndoneIsRefused(t *testing.T) {
	eachEngine(t, func(t *testing.T) {
		s := newMem(t)
		m := migCreate("202601010000_a", "ta")
		m.Rollback = CannotUndo
		list := []*Migration{m}
		if err := s.migrateWith(list); err != nil {
			t.Fatal(err)
		}
		if err := s.rollbackWith(list, m.ID); !errors.Is(err, ErrCannotUndo) {
			t.Fatalf("err = %v", err)
		}
		m2 := migCreate("202601020000_b", "tb")
		m2.Rollback = nil
		list = append(list, m2)
		if err := s.migrateWith(list); err != nil {
			t.Fatal(err)
		}
		if err := s.rollbackWith(list, m2.ID); !errors.Is(err, ErrCannotUndo) {
			t.Fatalf("no Rollback: err = %v", err)
		}
		if !s.DB.Migrator().HasTable("tb") {
			t.Fatal("nothing must change")
		}
	})
}

func TestAFailingMigrationLeavesNoRecordAndNoTable(t *testing.T) {
	eachEngine(t, func(t *testing.T) {
		s := newMem(t)
		if s.Driver == "mysql" {
			t.Skip("MySQL commits each DDL statement: a migration that fails half way cannot be undone there")
		}
		bad := &Migration{ID: "202601020000_bad", Migrate: func(tx *gorm.DB) error {
			if err := tx.Exec("CREATE TABLE tbad (id integer)").Error; err != nil {
				return err
			}
			return errors.New("boom")
		}}
		err := s.migrateWith([]*Migration{migCreate("202601010000_a", "ta"), bad})
		if err == nil {
			t.Fatal("expected an error")
		}
		applied, _, _ := s.migrationsWith([]*Migration{migCreate("202601010000_a", "ta"), bad})
		if len(applied) != 0 || s.DB.Migrator().HasTable("tbad") {
			t.Fatalf("applied %v, tbad exists: %v", applied, s.DB.Migrator().HasTable("tbad"))
		}
	})
}

func TestRegisteringABadOrRepeatedMigrationIsAPanic(t *testing.T) {
	panics := func(f func()) (p bool) {
		defer func() { p = recover() != nil }()
		f()
		return
	}
	ok := func(*gorm.DB) error { return nil }
	if !panics(func() { addMigration(nil, &Migration{ID: "nodate", Migrate: ok}) }) {
		t.Error("an id with no date must panic")
	}
	if !panics(func() { addMigration(nil, &Migration{ID: "202601010000_a"}) }) {
		t.Error("a migration with no Migrate must panic")
	}
	list := addMigration(nil, &Migration{ID: "202601010000_a", Migrate: ok})
	if !panics(func() { addMigration(list, &Migration{ID: "202601010000_a", Migrate: ok}) }) {
		t.Error("an id registered twice must panic")
	}
}

type schemaProbe struct {
	ID    uint   `gorm:"primaryKey"`
	Name  string `gorm:"size:50;index:idx_probe_name"`
	Extra string
}

func TestTheSchemaTestSeesAModelChangedWithoutItsMigration(t *testing.T) {
	eachEngine(t, func(t *testing.T) {
		s := newMem(t)
		// A migration written when the model had no Extra column and no index.
		err := s.migrateWith([]*Migration{{ID: "202601010000_probe", Migrate: func(tx *gorm.DB) error {
			return tx.Exec("CREATE TABLE schema_probes (id integer primary key, name text)").Error
		}}})
		if err != nil {
			t.Fatal(err)
		}
		missing, err := checkSchema(s.DB, []any{&schemaProbe{}})
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Join(missing, ",")
		if !strings.Contains(got, "column schema_probes.extra") || !strings.Contains(got, "index schema_probes.idx_probe_name") {
			t.Fatalf("missing = %v", missing)
		}
		if m, _ := checkSchema(newMem(t).DB, []any{&schemaProbe{}}); len(m) != 1 || m[0] != "table schema_probes" {
			t.Fatalf("an empty database lacks the table: %v", m)
		}
	})
}

func TestTheMigrationsOfThePackageGiveTheSchemaOfItsModels(t *testing.T) {
	eachEngine(t, func(t *testing.T) {
		s := newMem(t)
		if err := s.Migrate(); err != nil {
			t.Fatal(err)
		}
		if missing, err := s.CheckSchema(); err != nil || len(missing) > 0 {
			t.Fatalf("missing %v err %v", missing, err)
		}
		applied, pending, _ := s.Migrations()
		if len(applied) == 0 || len(pending) != 0 {
			t.Fatalf("applied %v pending %v", applied, pending)
		}
		if err := s.DB.Create(&DataImport{Entity: "x", State: "done", StartedAt: time.Now()}).Error; err != nil {
			t.Fatal(err)
		}
	})
}

func TestCheckMigratedSaysWhatIsPendingAndWhatToDoAndChangesNothing(t *testing.T) {
	eachEngine(t, func(t *testing.T) {
		s := newMem(t)
		list := []*Migration{migCreate("202601010000_a", "ta"), migCreate("202601020000_b", "tb")}
		err := s.checkMigratedWith(list)
		if !errors.Is(err, ErrNotMigrated) || !strings.Contains(err.Error(), "202601010000_a") || !strings.Contains(err.Error(), "veritmo data migrate") {
			t.Fatalf("err = %v", err)
		}
		if s.DB.Migrator().HasTable("ta") || s.DB.Migrator().HasTable(&schemaMigration{}) && func() bool { a, _ := s.applied(); return len(a) > 0 }() {
			t.Fatal("checking must change nothing")
		}
		if err := s.migrateWith(list); err != nil {
			t.Fatal(err)
		}
		if err := s.checkMigratedWith(list); err != nil {
			t.Fatalf("a migrated database passes: %v", err)
		}
		err = s.checkMigratedWith(list[:1])
		if !errors.Is(err, ErrUnknownMigrations) || !strings.Contains(err.Error(), "202601020000_b") {
			t.Fatalf("a database of a newer version: %v", err)
		}
	})
}

func TestTwoProgramsMigratingTheSameDatabaseTakeTurns(t *testing.T) {
	eachEngine(t, func(t *testing.T) {
		s := newMem(t)
		if s.Driver == "sqlite" {
			t.Skip("SQLite has the lock of its file and one process")
		}
		other := sameDatabase(t, s)
		var mu sync.Mutex
		runs := 0
		slow := &Migration{ID: "202601010000_slow", Migrate: func(tx *gorm.DB) error {
			mu.Lock()
			runs++
			mu.Unlock()
			time.Sleep(300 * time.Millisecond)
			return tx.Exec("CREATE TABLE tslow (id integer primary key)").Error
		}}
		list := []*Migration{slow}
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, st := range []*Store{s, other} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = st.migrateWith(list)
			}()
		}
		wg.Wait()
		if errs[0] != nil || errs[1] != nil || runs != 1 {
			t.Fatalf("errors %v %v, the migration ran %d times", errs[0], errs[1], runs)
		}
	})
}

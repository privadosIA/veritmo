package store

import (
	"path/filepath"
	"testing"

	"veritmo/internal/platform/settings"
)

type openProbe struct {
	ID   uint `gorm:"primaryKey"`
	Name string
}

func TestOpenMemoryStoreWorksAndIsPrivate(t *testing.T) {
	a, err := OpenMemory(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := OpenMemory(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := a.DB.AutoMigrate(&openProbe{}); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Create(&openProbe{Name: "x"}).Error; err != nil {
		t.Fatal(err)
	}
	if b.DB.Migrator().HasTable(&openProbe{}) {
		t.Fatal("two in-memory stores must not share their tables")
	}
	if a.Driver != settings.DBSQLite {
		t.Fatalf("driver = %q", a.Driver)
	}
}

func TestOpenFileStoreAppliesThePragmas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	s, err := Open(path, settings.Database{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Driver != settings.DBSQLite || s.Path != path {
		t.Fatalf("%+v", s)
	}
	pragma := func(name string) string {
		var v string
		if err := s.DB.Raw("PRAGMA " + name).Row().Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	for name, want := range map[string]string{"journal_mode": "wal", "foreign_keys": "1", "busy_timeout": "5000", "synchronous": "1"} {
		if got := pragma(name); got != want {
			t.Errorf("PRAGMA %s = %q, want %q", name, got, want)
		}
	}
	if err := s.DB.AutoMigrate(&openProbe{}); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRemoteEngineThatNobodyAnswersIsAnError(t *testing.T) {
	_, err := Open("", settings.Database{Driver: settings.DBPostgres, Host: "127.0.0.1", Port: 1, Name: "x", User: "x"}, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
}

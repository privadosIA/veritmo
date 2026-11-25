package store

import (
	"crypto/rand"
	"fmt"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"veritmo/internal/platform/settings"
)

// sqlitePragmas are the settings every SQLite connection is opened with: WAL lets the server and the command line read
// while one of them writes, the busy timeout makes a second writer wait 5 s instead of failing, foreign keys are
// checked and NORMAL synchronous is what WAL is designed for.
const sqlitePragmas = "_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"

// Open opens the database the data lives in, told what the program has (reg; nil is the registry of this package alone): the SQLite file at path, or the MySQL or Postgres server d names. It does not
// create or change any table: call Migrate for that.
func Open(path string, d settings.Database, reg *Registry) (*Store, error) {
	if reg == nil {
		reg = NewRegistry()
	}
	cfg := &gorm.Config{Logger: logger.Discard}
	switch d.Driver {
	case settings.DBMySQL:
		// utf8mb4 keeps every character of a prompt; parseTime reads the dates as times.
		dsn := fmt.Sprintf("%s:%s@tcp(%s)/%s?charset=utf8mb4&parseTime=True&loc=UTC&timeout=5s&readTimeout=30s&writeTimeout=30s", d.User, d.Password, d.Addr(), d.Name)
		db, err := gorm.Open(mysql.Open(dsn), cfg)
		if err != nil {
			return nil, fmt.Errorf("cannot open the MySQL database at %s: %w", d.Addr(), err)
		}
		return &Store{DB: db, Driver: settings.DBMySQL, reg: reg}, nil
	case settings.DBPostgres:
		dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=prefer connect_timeout=5 TimeZone=UTC", d.Host, d.Port, d.User, d.Password, d.Name)
		db, err := gorm.Open(postgres.Open(dsn), cfg)
		if err != nil {
			return nil, fmt.Errorf("cannot open the Postgres database at %s: %w", d.Addr(), err)
		}
		return &Store{DB: db, Driver: settings.DBPostgres, reg: reg}, nil
	}
	db, err := gorm.Open(sqlite.Open(path+"?"+sqlitePragmas), cfg)
	if err != nil {
		return nil, fmt.Errorf("cannot open the database %s: %w", path, err)
	}
	return &Store{DB: db, Driver: settings.DBSQLite, Path: path, reg: reg}, nil
}

// OpenMemory opens a SQLite database that lives in memory and is private to the caller: for tests, so that none of them touches a file.
func OpenMemory(reg *Registry) (*Store, error) {
	if reg == nil {
		reg = NewRegistry()
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	name := fmt.Sprintf("file:store-%x?mode=memory&cache=shared&_pragma=foreign_keys(1)", b)
	db, err := gorm.Open(sqlite.Open(name), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		return nil, fmt.Errorf("cannot open the in-memory database: %w", err)
	}
	return &Store{DB: db, Driver: settings.DBSQLite, reg: reg}, nil
}

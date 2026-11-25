package settings

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// The engines the run history can be kept in.
const (
	DBSQLite   = "sqlite"
	DBMySQL    = "mysql"
	DBPostgres = "postgres"
)

// Database is where the run history lives. SQLite (a file next to the project, the default) needs nothing; MySQL
// and Postgres are another machine's database that several servers can share. It is read once, when the server
// starts: changing it takes a restart. The password is kept like the other keys, in the settings file (mode 0600).
type Database struct {
	// Driver is sqlite, mysql or postgres; empty means sqlite.
	Driver   string `json:"driver,omitempty"`
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	Name     string `json:"name,omitempty"`
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"`
}

// DefaultPort is the usual port of the engine.
func DefaultPort(driver string) int {
	switch driver {
	case DBMySQL:
		return 3306
	case DBPostgres:
		return 5432
	}
	return 0
}

// IsRemote reports whether the history is in a database server (not the SQLite file).
func (d Database) IsRemote() bool { return d.Driver == DBMySQL || d.Driver == DBPostgres }

// Validate checks what a user typed and fills the port. SQLite needs nothing else.
func (d Database) Validate() (Database, error) {
	d.Driver = strings.ToLower(strings.TrimSpace(d.Driver))
	switch d.Driver {
	case "", DBSQLite:
		return Database{}, nil
	case DBMySQL, DBPostgres:
	default:
		return d, fmt.Errorf("the engine must be sqlite, mysql or postgres, not %q", d.Driver)
	}
	d.Host, d.Name, d.User = strings.TrimSpace(d.Host), strings.TrimSpace(d.Name), strings.TrimSpace(d.User)
	switch {
	case d.Host == "":
		return d, errors.New("the host is empty")
	case strings.ContainsAny(d.Host, "/@ ") || strings.Contains(d.Host, "://"):
		return d, errors.New("the host is a name or an address, with no scheme, user or path")
	case d.Name == "":
		return d, errors.New("the database name is empty")
	case strings.ContainsAny(d.Name, "/`\"' ;"):
		return d, errors.New("the database name has characters that are not allowed")
	case d.User == "":
		return d, errors.New("the user is empty")
	}
	if d.Port == 0 {
		d.Port = DefaultPort(d.Driver)
	}
	if d.Port < 1 || d.Port > 65535 {
		return d, errors.New("the port is not valid")
	}
	return d, nil
}

// Addr is host:port.
func (d Database) Addr() string { return net.JoinHostPort(d.Host, strconv.Itoa(d.Port)) }

// SavedDatabase returns the database saved in the settings file, an empty one (SQLite) when none is saved. The database settings are always in the file:
// they are read before the database is open.
func SavedDatabase() Database {
	b, _ := loadBootstrap()
	return b.Database
}

// GetDatabase returns the database the program uses: the saved one, else the one the VERITMO_DB_* variables describe (see env.go), else an empty one (SQLite).
// A description in the environment that is not valid is ignored here; CheckEnvDatabase says what is wrong.
func GetDatabase() Database {
	if d := SavedDatabase(); d != (Database{}) {
		return d
	}
	d, _ := databaseFromEnv()
	return d
}

// DatabaseSource says where GetDatabase gets the database from: FromSetting (the settings file), FromEnv or FromDefault (the SQLite file).
func DatabaseSource() string {
	switch {
	case SavedDatabase() != (Database{}):
		return FromSetting
	case GetDatabase() != (Database{}):
		return FromEnv
	}
	return FromDefault
}

// SetDatabase saves the database. An empty password keeps the saved one when the rest names the same server, so a
// form that never shows the password can be saved again; SQLite removes everything.
func SetDatabase(d Database) error {
	mu.Lock()
	defer mu.Unlock()
	f, rerr := ReadFile() // a source that cannot be read is not "no settings": saving would erase them
	if rerr != nil {
		return rerr
	}
	if d.Password == "" && d.IsRemote() {
		old := f.Database
		if old.Driver == d.Driver && old.Host == d.Host && old.Port == d.Port && old.User == d.User {
			d.Password = old.Password
		}
	}
	f.Database = d
	return Save(f)
}

// ServerName is how this server is called in a history that several share: the saved name, else the host's.
func ServerName() string {
	if b, _ := loadBootstrap(); strings.TrimSpace(b.ServerName) != "" {
		return strings.TrimSpace(b.ServerName)
	}
	h, err := os.Hostname()
	if err != nil || strings.TrimSpace(h) == "" {
		return "server"
	}
	return h
}

// SetServerName saves the name of this server (empty goes back to the host's name).
func SetServerName(name string) error {
	mu.Lock()
	defer mu.Unlock()
	f, rerr := ReadFile() // a source that cannot be read is not "no settings": saving would erase them
	if rerr != nil {
		return rerr
	}
	f.ServerName = strings.TrimSpace(name)
	return Save(f)
}

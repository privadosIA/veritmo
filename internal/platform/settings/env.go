package settings

// env.go: the environment as the default of a connection (change add-compose-stack). A container is given its connections by environment
// variables instead of a hand-written settings file, with the rule the Ollama address has always had: what is saved wins, the environment is the
// default when nothing is saved, and the default of a piece that is not there is empty. A secret may also be given as the path of a file
// (NAME_FILE, the convention of Docker secrets) so that it does not show in `docker inspect`.

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"veritmo/internal/platform/legacyname"
)

// Env is the value of an environment variable, or, when it is empty, the content of the file named by NAME_FILE (trimmed). A file that cannot be read is an
// error that names the file and never the value.
func Env(name string) (string, error) {
	get := os.Getenv
	if strings.HasPrefix(name, "VERITMO_") { // the variables of this program also answer to their former MO_ name
		get = func(n string) string { return legacyname.Getenv(strings.TrimPrefix(n, "VERITMO_")) }
	}
	if v := strings.TrimSpace(get(name)); v != "" {
		return v, nil
	}
	path := strings.TrimSpace(get(name + "_FILE"))
	if path == "" {
		return "", nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%s_FILE: cannot read %s: %w", name, path, err)
	}
	return strings.TrimSpace(string(b)), nil
}

// Where a connection or a database comes from.
const (
	FromSetting = "setting"
	FromEnv     = "env"
	FromDefault = "default"
)

// databaseFromEnv is the database the VERITMO_DB_* variables describe: nothing (the SQLite file) when VERITMO_DB_DRIVER is empty, an error when it is set and the
// rest does not describe a database (a container that asked for a MySQL must not quietly use a file).
func databaseFromEnv() (Database, error) {
	driver := strings.TrimSpace(legacyname.Getenv("DB_DRIVER"))
	if driver == "" || strings.EqualFold(driver, DBSQLite) {
		return Database{}, nil
	}
	d := Database{Driver: driver, Host: legacyname.Getenv("DB_HOST"), Name: legacyname.Getenv("DB_NAME"), User: legacyname.Getenv("DB_USER")}
	if p := strings.TrimSpace(legacyname.Getenv("DB_PORT")); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			return Database{}, fmt.Errorf("VERITMO_DB_PORT is not a number")
		}
		d.Port = n
	}
	pw, err := Env("VERITMO_DB_PASSWORD")
	if err != nil {
		return Database{}, err
	}
	d, err = d.Validate()
	if err != nil {
		return Database{}, fmt.Errorf("VERITMO_DB_*: %w", err)
	}
	d.Password = pw
	return d, nil
}

// CheckEnvDatabase says whether the VERITMO_DB_* variables are valid when they are set and nothing is saved: the commands that open the database call it first
// and stop on an error, so that a typo is not a silent SQLite file.
func CheckEnvDatabase() error {
	if b, _ := loadBootstrap(); b.Database != (Database{}) {
		return nil // what is saved wins, whatever the environment says
	}
	_, err := databaseFromEnv()
	return err
}

// envService is the address and the token of a service the environment describes: PREFIX_HOST (validated and normalised like a saved one; one that is not
// valid is ignored) and PREFIX_TOKEN. Both are empty when PREFIX_HOST is not set.
func envService(prefix string, validate func(string) (string, error)) (host, token string) {
	raw := strings.TrimSpace(os.Getenv(prefix + "_HOST"))
	if raw == "" {
		return "", ""
	}
	h, err := validate(raw)
	if err != nil {
		return "", ""
	}
	t, _ := Env(prefix + "_TOKEN")
	return h, t
}

// envMonitoring is the tool the environment describes: MONITORING_<TOOL>_ADDRESS (validated like a saved one; one that is not valid is ignored), the user
// MONITORING_<TOOL>_USER and the secret MONITORING_<TOOL>_SECRET (or _SECRET_FILE).
func envMonitoring(id string) Endpoint {
	prefix := "MONITORING_" + strings.ToUpper(id)
	raw := strings.TrimSpace(os.Getenv(prefix + "_ADDRESS"))
	if raw == "" || !ValidMonitoringID(id) {
		return Endpoint{}
	}
	addr, err := ValidateMonitoringAddress(id, raw)
	if err != nil {
		return Endpoint{}
	}
	secret, _ := Env(prefix + "_SECRET")
	return Endpoint{Address: addr, User: strings.TrimSpace(os.Getenv(prefix + "_USER")), Secret: secret}
}

// EnvCORS are the browser origins and networks the environment adds (CORS_ORIGINS and CORS_NETWORKS, comma lists), normalised like the saved ones; an
// entry that is not valid is left out. They are added to the saved ones and to --cors-origin, never written to the settings.
func EnvCORS() (origins, networks []string) {
	for _, o := range strings.Split(os.Getenv("CORS_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o == "" {
			continue
		}
		if n, err := NormalizeCORSOrigin(o); err == nil {
			origins = append(origins, n)
		}
	}
	for _, c := range strings.Split(os.Getenv("CORS_NETWORKS"), ",") {
		if c = strings.TrimSpace(c); c == "" {
			continue
		}
		if n, err := NormalizeCORSNetwork(c); err == nil {
			networks = append(networks, n)
		}
	}
	return origins, networks
}

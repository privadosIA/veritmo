// Package settings is what the server and the commands keep as settings: the Ollama address, the model providers and the routing of the tasks, the
// hosts of the music services, the monitoring tools, the CORS lists, the ctxpress connection and the database settings. The settings are read at every
// use, so saving one needs no restart and a terminal and the server cannot disagree. Where they are kept is a Source: the database for the server and
// for the commands that open it (UseSource, with package settingsdb) and the settings file (~/.config/veritmo/settings.json) when none is
// set, which is also where the database settings and the server name always are, because they are read before the database is open. Every secret is in
// the private keys file (keys.go), never in a Source.
package settings

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"veritmo/internal/platform/legacyname"
)

// DefaultOllama is the address used when neither a setting nor OLLAMA_HOST says otherwise.
const DefaultOllama = "http://127.0.0.1:11434"

// maxAddressLen is the longest address accepted.
const maxAddressLen = 200

// Where the resolved address comes from.
const (
	SourceSetting = "setting"
	SourceEnv     = "env"
	SourceDefault = "default"
)

// File is the content of settings.json. Only known keys are written.
type File struct {
	OllamaHost string `json:"ollama_host,omitempty"`
	// Browser is the path of the Chrome or Chromium that renders motion graphics; empty means search the PATH.
	Browser string `json:"browser,omitempty"`
	// MotionModel is the model that writes the pages of motion graphics and overlays, on whichever provider that task
	// is routed to; empty means the provider's own model for the task, else the assistant's.
	MotionModel string `json:"motion_model,omitempty"`
	// Providers are the model providers added by the user (the built-in Ollama is not stored here).
	Providers []Provider `json:"providers,omitempty"`
	// Routing maps a task to the id of the provider that runs it; a task not in it uses the built-in Ollama.
	Routing map[string]string `json:"routing,omitempty"`
	// Consent maps a provider id to the base address the user acknowledged sending data to.
	Consent map[string]string `json:"consent,omitempty"`
	// Remote holds the limits for jobs that go to a remote provider.
	Remote *RemoteSettings `json:"remote,omitempty"`
	// HeartMulaHost is the address of a HeartMuLa music service on another machine, and HeartMulaToken the token
	// it asks for (see internal/server/music_heartmula.go); empty means the local one, if there is one.
	HeartMulaHost  string `json:"heartmula_host,omitempty"`
	HeartMulaToken string `json:"heartmula_token,omitempty"`
	// DiffRhythm2Host and DiffRhythm2Token are the same for a DiffRhythm2 service (see internal/server/music_diffrhythm2.go).
	DiffRhythm2Host  string `json:"diffrhythm2_host,omitempty"`
	DiffRhythm2Token string `json:"diffrhythm2_token,omitempty"`
	// HeartMulaBackend and DiffRhythm2Backend say how each generator runs on this machine: "dotnet" (the service, the default) or "docker" (its
	// container); an address in the fields above wins over both (see musicbackend.go).
	HeartMulaBackend   string `json:"heartmula_backend,omitempty"`
	DiffRhythm2Backend string `json:"diffrhythm2_backend,omitempty"`
	// Model3DBackend says how TripoSR (the 3D generator) runs on this machine: "dotnet" (triposr-service, the default) or "docker" (the container of
	// docker/model3d, the fallback). Stable Fast 3D has no .NET port and always runs in the container.
	Model3DBackend string `json:"model3d_backend,omitempty"`
	// Database is where the run history is kept (see internal/server/runs_database.go); the zero value is the SQLite file.
	Database Database `json:"database,omitempty"`
	// Monitoring are the addresses and keys of the tools the user runs beside this server (Grafana, Prometheus, Loki,
	// Redis); see internal/server/runs_monitoring.go.
	Monitoring map[string]Endpoint `json:"monitoring,omitempty"`
	// ServerName is how this server is called in a history that several share; empty means the host's name.
	ServerName string `json:"server_name,omitempty"`
	// CORSOrigins and CORSNetworks say which browser pages may call the API from another origin (see internal/server/core_cors.go and the
	// cors-settings spec): exact origins, and CIDR ranges whose IP hosts are allowed on any port. They are changed on
	// the machine only (the file, or `veritmo cors`); no API endpoint reads or writes them.
	CORSOrigins  []string `json:"cors_origins,omitempty"`
	CORSNetworks []string `json:"cors_networks,omitempty"`
	// ReviewDrafts makes every part draft of music motion be graded by the vision model of the check task without asking
	// (music_motion.review_drafts); off by default.
	ReviewDrafts bool `json:"music_motion_review_drafts,omitempty"`
	// Ctxpress is the connection to the optional compression service (see ctxpress.go); its token is in the key store.
	Ctxpress *CtxpressSettings `json:"ctxpress,omitempty"`
}

// RemoteSettings are the limits for jobs sent to a remote provider.
type RemoteSettings struct {
	// MaxCalls is the most calls one job may make to a remote provider; 0 means the default.
	MaxCalls int `json:"max_calls,omitempty"`
}

func (f File) empty() bool {
	return f.OllamaHost == "" && f.Browser == "" && f.MotionModel == "" && f.HeartMulaHost == "" && f.HeartMulaToken == "" && f.DiffRhythm2Host == "" && f.DiffRhythm2Token == "" && f.HeartMulaBackend == "" && f.DiffRhythm2Backend == "" && f.Model3DBackend == "" && f.Database == (Database{}) && f.ServerName == "" && len(f.CORSOrigins) == 0 && len(f.CORSNetworks) == 0 && !f.ReviewDrafts && f.Ctxpress == nil && len(f.Monitoring) == 0 && len(f.Providers) == 0 && len(f.Routing) == 0 && len(f.Consent) == 0 && (f.Remote == nil || f.Remote.MaxCalls == 0)
}

// Dir is the server's config directory: $XDG_CONFIG_HOME or ~/.config, then
// veritmo (the one that holds the token).
func Dir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return legacyname.Dir(base)
}

// Path is the settings file.
func Path() string {
	d := Dir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "settings.json")
}

// Load reads the settings: from the source the process uses (see UseSource) or, when there is none, the settings file. A missing, unreadable or
// corrupt source means no settings: the caller falls back to the environment and the defaults.
func Load() File {
	f, err := ReadFile()
	if err != nil {
		return File{}
	}
	return f
}

// Save writes the settings: to the source the process uses (see UseSource) or, when there is none, to the settings file, atomically, in a directory of
// mode 0700 and as a file of mode 0600; a file with nothing left in it is removed. With a source, the database settings and the server name still go
// to the file (see loadBootstrap).
func Save(f File) error {
	if s := currentSource(); s != nil {
		if err := s.Save(f); err != nil {
			return err
		}
		return saveBootstrap(f.Database, f.ServerName)
	}
	return fileSource{}.Save(f)
}

// writePrivate writes data to p atomically, in a directory of mode 0700, as a file of mode 0600.
func writePrivate(p string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".settings-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// Normalize turns an OLLAMA_HOST value (as Ollama itself reads it) into a base
// URL: scheme and port are optional and a wildcard address means loopback.
// Empty gives the default.
func Normalize(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return DefaultOllama
	}
	scheme := "http"
	if i := strings.Index(host, "://"); i >= 0 {
		scheme, host = host[:i], host[i+3:]
	}
	host = strings.TrimRight(host, "/")
	h, p, err := net.SplitHostPort(host)
	if err != nil {
		h, p = strings.Trim(host, "[]"), "11434"
	}
	if h == "" || h == "0.0.0.0" || h == "::" {
		h = "127.0.0.1"
	}
	return scheme + "://" + net.JoinHostPort(h, p)
}

// ValidateAddress checks an address a user typed and returns it normalized
// (scheme://host:port). Only http and https are accepted (http when there is
// no scheme), a host is required, and no user, password, path, query or
// fragment is allowed. The error says which rule failed.
func ValidateAddress(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("the address is empty")
	}
	if len(s) > maxAddressLen {
		return "", fmt.Errorf("the address is longer than %d characters", maxAddressLen)
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", errors.New("the address is not valid")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return "", fmt.Errorf("only http and https addresses are accepted, not %q", u.Scheme)
	}
	if u.User != nil {
		return "", errors.New("credentials in the address are not allowed")
	}
	if u.Hostname() == "" {
		return "", errors.New("the address has no host")
	}
	if p := u.Port(); p != "" {
		if n, err := net.LookupPort("tcp", p); err != nil || n < 1 {
			return "", errors.New("the port is not valid")
		}
	}
	if u.Path != "" && u.Path != "/" {
		return "", errors.New("the address must have no path: use only host and port, such as http://192.168.1.50:11434")
	}
	if u.RawQuery != "" || u.ForceQuery {
		return "", errors.New("the address must have no query")
	}
	if u.Fragment != "" {
		return "", errors.New("the address must have no fragment")
	}
	return Normalize(strings.ToLower(u.Scheme) + "://" + u.Host), nil
}

// Resolved is the Ollama address in use and where it comes from.
type Resolved struct {
	// Host is a base URL such as http://127.0.0.1:11434.
	Host string `json:"host"`
	// Source is "setting", "env" or "default".
	Source string `json:"source"`
	// Remote is true when Host is not this machine.
	Remote bool `json:"remote"`
}

// Ollama resolves the address of Ollama at this moment: the saved setting,
// then OLLAMA_HOST, then the default. It is meant to be called at every use.
func Ollama() Resolved {
	if h := strings.TrimSpace(Load().OllamaHost); h != "" {
		n := Normalize(h)
		return Resolved{Host: n, Source: SourceSetting, Remote: Remote(n)}
	}
	if h := strings.TrimSpace(os.Getenv("OLLAMA_HOST")); h != "" {
		n := Normalize(h)
		return Resolved{Host: n, Source: SourceEnv, Remote: Remote(n)}
	}
	return Resolved{Host: DefaultOllama, Source: SourceDefault, Remote: false}
}

// MotionModel is the model saved for writing the pages of motion graphics, or empty.
func MotionModel() string { return strings.TrimSpace(Load().MotionModel) }

// MaxModelLen is the longest model name accepted.
const MaxModelLen = 120

// ValidateModel checks a model name a user typed: no spaces or control characters, a sensible length.
func ValidateModel(raw string) (string, error) {
	m := strings.TrimSpace(raw)
	if len(m) > MaxModelLen {
		return "", fmt.Errorf("the model name is longer than %d characters", MaxModelLen)
	}
	for _, r := range m {
		if r <= ' ' || r == 0x7f {
			return "", errors.New("the model name cannot contain spaces or control characters")
		}
	}
	return m, nil
}

// Browser is the path of the browser saved in the settings, or empty.
func Browser() string { return strings.TrimSpace(Load().Browser) }

// OllamaURL is the address in use, for the callers that need nothing else.
func OllamaURL() string { return Ollama().Host }

// Remote reports whether an address (a URL, or host[:port]) is not this
// machine: anything but localhost, 127.0.0.0/8 and ::1. The machine's own
// network address counts as remote, on purpose: it is the conservative reading
// of "leaves this computer".
func Remote(address string) bool {
	s := strings.TrimSpace(address)
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" {
		return true
	}
	h := strings.ToLower(u.Hostname())
	if h == "localhost" {
		return false
	}
	if ip := net.ParseIP(h); ip != nil {
		return !ip.IsLoopback()
	}
	return true
}

// ReviewDrafts says whether every music-motion part draft is reviewed by the vision model without being asked.
func ReviewDrafts() bool { return Load().ReviewDrafts }

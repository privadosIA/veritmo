// Package legacyname holds what is left of the project's former name (mediaorquestator, MO_*): it moves the user directories of the project's former name (mediaorquestator) to the current one (veritmo), once, by renaming.
//
// The server keeps its token, settings, keys and the file that says where the models live under ~/.config/<name>, and some caches under ~/.cache/<name>.
// A machine that ran the program under its old name still has those files in the old directory: the first process that asks for the directory moves it,
// so that nobody loses the token or the settings by updating. Nothing is copied and nothing is deleted; when both directories exist the new one is used and
// the old one is left alone, with a warning.
package legacyname

import (
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

const (
	// Name is the directory the program uses inside ~/.config and ~/.cache.
	Name = "veritmo"
	// oldName is the directory of the former name of the project.
	oldName = "mediaorquestator"
)

var (
	mu   sync.Mutex
	seen = map[string]bool{}
)

// Dir is <base>/veritmo, where base is ~/.config or ~/.cache (or what XDG_CONFIG_HOME / XDG_CACHE_HOME say). The first time a base is asked for in the
// process, <base>/mediaorquestator is renamed to it when only the old one exists; when both exist a warning says the old one is ignored.
func Dir(base string) string {
	cur := filepath.Join(base, Name)
	mu.Lock()
	defer mu.Unlock()
	if seen[base] {
		return cur
	}
	seen[base] = true
	old := filepath.Join(base, oldName)
	if _, err := os.Stat(old); err != nil {
		return cur // no old directory: a fresh machine, or one already moved
	}
	if _, err := os.Stat(cur); err == nil {
		slog.Warn("two configuration directories: the old one is ignored and may be deleted", "used", cur, "ignored", old)
		return cur
	}
	if err := os.Rename(old, cur); err != nil {
		slog.Warn("could not move the old directory; the program starts without its files", "from", old, "to", cur, "error", err.Error())
		return cur
	}
	slog.Info("moved " + old + " to " + cur)
	return cur
}

// reset forgets which bases were seen (for the tests, which use a new base every time and also ask again for one they just changed).
func reset() {
	mu.Lock()
	defer mu.Unlock()
	seen = map[string]bool{}
}

var warnedEnv sync.Map

// formerEnvPrefixes are the prefixes the variables of this program have had before VERITMO_, in the order they are tried.
var formerEnvPrefixes = []string{"MO_", "ORCHESTRATOR_"}

// Getenv is the value of VERITMO_<suffix>, or, when that is unset, of the variable it replaced: MO_<suffix> and then ORCHESTRATOR_<suffix>. The first time
// an old one is the one that answers, a line says so and names the new one; when the new one is set it wins and nothing is logged.
//
// TODO(veritmo-env): remove the fallbacks (and this warning) in the release after the deploys use the new names.
func Getenv(suffix string) string { return Lookup(os.Getenv, suffix) }

// Lookup is Getenv over any source of variables (the tests and the resolvers that are handed their environment).
func Lookup(getenv func(string) string, suffix string) string {
	if v := getenv("VERITMO_" + suffix); v != "" {
		return v
	}
	for _, prefix := range formerEnvPrefixes {
		if v := getenv(prefix + suffix); v != "" {
			if _, done := warnedEnv.LoadOrStore(prefix+suffix, true); !done {
				slog.Warn(prefix + suffix + " is obsolete, use VERITMO_" + suffix)
			}
			return v
		}
	}
	return ""
}

// resetEnv forgets which variables were warned about (for the tests).
func resetEnv() { warnedEnv = sync.Map{} }

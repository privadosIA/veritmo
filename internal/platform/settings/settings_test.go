package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolate points the config dir at a temp dir and clears OLLAMA_HOST.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("OLLAMA_HOST", "")
	return dir
}

func TestResolverPrecedence(t *testing.T) {
	isolate(t)
	if r := Ollama(); r.Host != "http://127.0.0.1:11434" || r.Source != SourceDefault || r.Remote {
		t.Fatalf("default: %+v", r)
	}
	t.Setenv("OLLAMA_HOST", "10.0.0.2")
	if r := Ollama(); r.Host != "http://10.0.0.2:11434" || r.Source != SourceEnv || !r.Remote {
		t.Fatalf("env: %+v", r)
	}
	if err := Save(File{OllamaHost: "http://192.168.1.50:11434"}); err != nil {
		t.Fatal(err)
	}
	if r := Ollama(); r.Host != "http://192.168.1.50:11434" || r.Source != SourceSetting || !r.Remote {
		t.Fatalf("setting must win over the environment: %+v", r)
	}
	// Removing the setting falls back to the environment.
	if err := Save(File{}); err != nil {
		t.Fatal(err)
	}
	if r := Ollama(); r.Source != SourceEnv || r.Host != "http://10.0.0.2:11434" {
		t.Fatalf("after removing: %+v", r)
	}
}

func TestChangeTakesEffectOnNextCall(t *testing.T) {
	isolate(t)
	a := OllamaURL()
	if err := Save(File{OllamaHost: "http://10.1.1.1:11434"}); err != nil {
		t.Fatal(err)
	}
	if b := OllamaURL(); b == a || b != "http://10.1.1.1:11434" {
		t.Fatalf("the resolver must not cache: %q then %q", a, b)
	}
}

func TestSaveIsPrivateAndAtomic(t *testing.T) {
	dir := isolate(t)
	if err := Save(File{OllamaHost: "http://192.168.1.50:11434"}); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "veritmo", "settings.json")
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v, want 0600", fi.Mode().Perm())
	}
	di, _ := os.Stat(filepath.Dir(p))
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v, want 0700", di.Mode().Perm())
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	if len(entries) != 1 {
		t.Errorf("a temp file was left behind: %v", entries)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), `"ollama_host": "http://192.168.1.50:11434"`) {
		t.Errorf("unexpected content %s", b)
	}
	// Saving nothing removes the file.
	if err := Save(File{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("an empty setting must delete the file")
	}
	if err := Save(File{}); err != nil {
		t.Errorf("removing a missing file must not fail: %v", err)
	}
}

func TestCorruptFileMeansNoSetting(t *testing.T) {
	dir := isolate(t)
	p := filepath.Join(dir, "veritmo", "settings.json")
	os.MkdirAll(filepath.Dir(p), 0o700)
	os.WriteFile(p, []byte("{not json"), 0o600)
	if r := Ollama(); r.Source != SourceDefault {
		t.Fatalf("a corrupt file must be ignored: %+v", r)
	}
}

func TestValidateAddress(t *testing.T) {
	good := map[string]string{
		"192.168.1.50:11434":        "http://192.168.1.50:11434",
		"192.168.1.50":              "http://192.168.1.50:11434",
		"http://host.lan:11434/":    "http://host.lan:11434",
		"HTTPS://Example.com:8443":  "https://Example.com:8443",
		"localhost":                 "http://localhost:11434",
		"http://[::1]:11434":        "http://[::1]:11434",
		"0.0.0.0:11434":             "http://127.0.0.1:11434",
		"  http://10.0.0.5:11434  ": "http://10.0.0.5:11434",
	}
	for in, want := range good {
		got, err := ValidateAddress(in)
		if err != nil || !strings.EqualFold(got, want) {
			t.Errorf("%q: got %q, %v; want %q", in, got, err, want)
		}
	}
	bad := map[string]string{
		"":                            "empty",
		"http://user:pass@host:11434": "credentials",
		"http://host:11434/api":       "no path",
		"http://host:11434?x=1":       "no query",
		"http://host:11434/#frag":     "path or fragment",
		"ftp://host":                  "only http and https",
		"http://":                     "no host",
		"http://host:99999":           "port",
		strings.Repeat("a", 201):      "longer than",
	}
	for in, why := range bad {
		if got, err := ValidateAddress(in); err == nil {
			t.Errorf("%q must be rejected (%s), got %q", in, why, got)
		}
	}
	_, err := ValidateAddress("http://user:pass@host")
	if err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Errorf("the message must say credentials are not allowed: %v", err)
	}
	_, err = ValidateAddress("http://host:11434/api")
	if err == nil || !strings.Contains(err.Error(), "no path") {
		t.Errorf("the message must say there is no path: %v", err)
	}
	_, err = ValidateAddress("ftp://host")
	if err == nil || !strings.Contains(err.Error(), "only http and https") {
		t.Errorf("the message must say only http and https: %v", err)
	}
}

func TestRemote(t *testing.T) {
	for addr, want := range map[string]bool{
		"http://127.0.0.1:11434":     false,
		"http://localhost:11434":     false,
		"127.0.0.5:11434":            false,
		"http://[::1]:11434":         false,
		"http://192.168.1.50:11434":  true,
		"https://ollama.example.com": true,
		"10.0.0.2":                   true,
		"":                           true,
	} {
		if got := Remote(addr); got != want {
			t.Errorf("Remote(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"":             "http://127.0.0.1:11434",
		"0.0.0.0":      "http://127.0.0.1:11434",
		"10.0.0.2:9":   "http://10.0.0.2:9",
		"https://h:1/": "https://h:1",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

// A server updated in place keeps its token and settings: the first Dir() under XDG_CONFIG_HOME moves the directory of the former name.
func TestDir_MovesTheDirectoryOfTheFormerName(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	old := filepath.Join(base, "mediaorquestator")
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "token"), []byte("t0k3n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Dir(); got != filepath.Join(base, "veritmo") {
		t.Fatalf("Dir() = %s", got)
	}
	if b, err := os.ReadFile(filepath.Join(Dir(), "token")); err != nil || string(b) != "t0k3n" {
		t.Errorf("the token is not in the new directory: %q %v", b, err)
	}
	if _, err := os.Stat(old); err == nil {
		t.Errorf("the old directory is still there")
	}
}

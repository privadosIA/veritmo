package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateBase(t *testing.T) {
	good := map[string]string{
		"https://api.openai.com/v1":      "https://api.openai.com/v1",
		"https://openrouter.ai/api/v1/":  "https://openrouter.ai/api/v1",
		"https://api.groq.com/openai/v1": "https://api.groq.com/openai/v1",
		"http://127.0.0.1:8000":          "http://127.0.0.1:8000",
		"HTTPS://Example.com":            "https://Example.com",
		"http://192.168.1.60:1234/v1":    "http://192.168.1.60:1234/v1",
	}
	for in, want := range good {
		got, err := ValidateBase(in)
		if err != nil || !strings.EqualFold(got, want) {
			t.Errorf("%q: got %q, %v; want %q", in, got, err, want)
		}
	}
	bad := map[string]string{
		"":                                    "empty",
		"api.openai.com/v1":                   "scheme",
		"http://user:pass@host/v1":            "credentials",
		"https://host/v2":                     "path",
		"https://host/chat":                   "path",
		"https://host/v1?x=1":                 "query",
		"https://host/v1#f":                   "fragment",
		"ftp://host/v1":                       "scheme",
		"https://host/a b/v1":                 "path chars",
		"https://host:99999/v1":               "port",
		"https://" + strings.Repeat("a", 200): "length",
	}
	for in, why := range bad {
		if got, err := ValidateBase(in); err == nil {
			t.Errorf("%q must be rejected (%s), got %q", in, why, got)
		}
	}
}

func TestValidateProvider(t *testing.T) {
	ok := Provider{ID: "openrouter", Name: "OpenRouter", Type: TypeOpenAI, BaseURL: "https://openrouter.ai/api/v1/", TextModel: "gpt-4o-mini", VisionModel: "gpt-4o"}
	got, err := ValidateProvider(ok)
	if err != nil || got.BaseURL != "https://openrouter.ai/api/v1" {
		t.Fatalf("%+v %v", got, err)
	}
	// an extra ollama keeps the Ollama rules (default port added)
	got, err = ValidateProvider(Provider{ID: "lab", Name: "Lab", Type: TypeOllama, BaseURL: "192.168.1.60"})
	if err != nil || got.BaseURL != "http://192.168.1.60:11434" {
		t.Fatalf("%+v %v", got, err)
	}
	for name, p := range map[string]Provider{
		"capital id":  {ID: "OpenRouter", Name: "x", Type: TypeOpenAI, BaseURL: "https://h/v1"},
		"builtin id":  {ID: "ollama", Name: "x", Type: TypeOpenAI, BaseURL: "https://h/v1"},
		"long id":     {ID: strings.Repeat("a", 41), Name: "x", Type: TypeOpenAI, BaseURL: "https://h/v1"},
		"hyphen edge": {ID: "-a", Name: "x", Type: TypeOpenAI, BaseURL: "https://h/v1"},
		"no name":     {ID: "a", Type: TypeOpenAI, BaseURL: "https://h/v1"},
		"long name":   {ID: "a", Name: strings.Repeat("n", 61), Type: TypeOpenAI, BaseURL: "https://h/v1"},
		"bad type":    {ID: "a", Name: "x", Type: "anthropic", BaseURL: "https://h/v1"},
		"long model":  {ID: "a", Name: "x", Type: TypeOpenAI, BaseURL: "https://h/v1", TextModel: strings.Repeat("m", 201)},
		"bad base":    {ID: "a", Name: "x", Type: TypeOpenAI, BaseURL: "https://u:p@h/v1"},
	} {
		if _, err := ValidateProvider(p); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
}

func TestAddUpdateDeleteProvider(t *testing.T) {
	isolate(t)
	p := Provider{ID: "openrouter", Name: "OpenRouter", Type: TypeOpenAI, BaseURL: "https://openrouter.ai/api/v1", VisionModel: "gpt-4o"}
	if _, err := AddProvider(p); err != nil {
		t.Fatal(err)
	}
	if _, err := AddProvider(p); err == nil {
		t.Error("a duplicate id must be refused")
	}
	if got, ok := FindProvider("openrouter"); !ok || got.VisionModel != "gpt-4o" {
		t.Fatalf("%+v %v", got, ok)
	}
	p.VisionModel = "gpt-4o-mini"
	if _, err := UpdateProvider("openrouter", p); err != nil {
		t.Fatal(err)
	}
	if got, _ := FindProvider("openrouter"); got.VisionModel != "gpt-4o-mini" {
		t.Errorf("update lost: %+v", got)
	}
	if _, err := UpdateProvider("nope", p); err != ErrNoSuchProvider {
		t.Errorf("want ErrNoSuchProvider, got %v", err)
	}
	if _, err := UpdateProvider("ollama", p); err == nil {
		t.Error("the built-in cannot be edited")
	}
	if err := DeleteProvider("ollama"); err == nil {
		t.Error("the built-in cannot be removed")
	}
	if err := DeleteProvider("openrouter"); err != nil {
		t.Fatal(err)
	}
	if len(Providers()) != 0 {
		t.Error("the provider must be gone")
	}
	if err := DeleteProvider("openrouter"); err != ErrNoSuchProvider {
		t.Errorf("want ErrNoSuchProvider, got %v", err)
	}
}

func TestProvidersAndOllamaHostCoexist(t *testing.T) {
	isolate(t)
	if err := Save(File{OllamaHost: "http://10.1.1.1:11434"}); err != nil {
		t.Fatal(err)
	}
	if _, err := AddProvider(Provider{ID: "a", Name: "A", Type: TypeOpenAI, BaseURL: "https://h/v1"}); err != nil {
		t.Fatal(err)
	}
	if Load().OllamaHost != "http://10.1.1.1:11434" {
		t.Error("adding a provider must keep the Ollama address")
	}
	// removing the Ollama address keeps the providers
	f := Load()
	f.OllamaHost = ""
	if err := Save(f); err != nil {
		t.Fatal(err)
	}
	if len(Providers()) != 1 {
		t.Error("removing the Ollama address must keep the providers")
	}
}

func TestKeysArePrivateAndSeparate(t *testing.T) {
	dir := isolate(t)
	if _, err := AddProvider(Provider{ID: "a", Name: "A", Type: TypeOpenAI, BaseURL: "https://h/v1"}); err != nil {
		t.Fatal(err)
	}
	const key = "sk-test-0123456789"
	if err := SetKey("a", key); err != nil {
		t.Fatal(err)
	}
	kp := filepath.Join(dir, "veritmo", "keys.json")
	fi, err := os.Stat(kp)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("keys.json must exist with mode 0600: %v %v", err, fi)
	}
	di, _ := os.Stat(filepath.Dir(kp))
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v, want 0700", di.Mode().Perm())
	}
	sp, _ := os.ReadFile(filepath.Join(dir, "veritmo", "settings.json"))
	if strings.Contains(string(sp), key) {
		t.Fatalf("the key must never be in settings.json: %s", sp)
	}
	if !HasKey("a") || Key("a") != key || HasKey("b") {
		t.Error("HasKey/Key")
	}
	entries, _ := os.ReadDir(filepath.Dir(kp))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".settings-") {
			t.Errorf("a temp file was left behind: %s", e.Name())
		}
	}
}

func TestDeletingAProviderDeletesItsKeyAndRemovingKeepsTheProvider(t *testing.T) {
	isolate(t)
	for _, id := range []string{"a", "b"} {
		if _, err := AddProvider(Provider{ID: id, Name: id, Type: TypeOpenAI, BaseURL: "https://h/v1"}); err != nil {
			t.Fatal(err)
		}
		if err := SetKey(id, "key-"+id); err != nil {
			t.Fatal(err)
		}
	}
	if err := DeleteKey("a"); err != nil {
		t.Fatal(err)
	}
	if HasKey("a") || !HasKey("b") {
		t.Error("removing a key must leave the others")
	}
	if _, ok := FindProvider("a"); !ok {
		t.Error("removing a key must leave the provider")
	}
	if err := DeleteProvider("b"); err != nil {
		t.Fatal(err)
	}
	if HasKey("b") {
		t.Error("deleting a provider must delete its key")
	}
	if _, err := os.Stat(KeysPath()); !os.IsNotExist(err) {
		t.Error("no keys left: the file goes away")
	}
	if err := DeleteKey("never"); err != nil {
		t.Errorf("no key is not an error: %v", err)
	}
}

func TestValidateKey(t *testing.T) {
	for _, bad := range []string{"", "has space", "tab\there", "new\nline", strings.Repeat("k", 501)} {
		if ValidateKey(bad) == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
	if ValidateKey(strings.Repeat("k", 500)) != nil || ValidateKey("sk-proj_ABC-123.xyz") != nil {
		t.Error("a normal key must pass")
	}
	isolate(t)
	if err := SetKey("a", "has space"); err == nil {
		t.Error("SetKey must validate")
	}
}

func TestCheckKeyTransport(t *testing.T) {
	cases := []struct {
		url    string
		hasKey bool
		ok     bool
	}{
		{"http://192.168.1.60:8000/v1", true, false},
		{"http://192.168.1.60:8000/v1", false, true},
		{"https://api.openai.com/v1", true, true},
		{"http://127.0.0.1:8000/v1", true, true},
		{"http://localhost:8000/v1", true, true},
		{"http://example.com/v1", true, false},
	}
	for _, c := range cases {
		if err := CheckKeyTransport(c.url, c.hasKey); (err == nil) != c.ok {
			t.Errorf("%s key=%v: err=%v, want ok=%v", c.url, c.hasKey, err, c.ok)
		}
	}
}

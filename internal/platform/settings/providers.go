package settings

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"veritmo/internal/platform/edition"
)

// Provider types.
const (
	TypeOllama = "ollama"
	TypeOpenAI = "openai"
)

// BuiltinID is the id of the built-in provider: the Ollama of the Ollama setting.
// It always exists, is never stored in the list and cannot be edited or removed.
const BuiltinID = "ollama"

const (
	maxNameLen  = 60
	maxModelLen = 200
)

var providerIDPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

// pathSegment is a safe segment of the path of an OpenAI-compatible base.
var pathSegment = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Provider is a model provider the user added. API keys are not part of it: they
// live in keys.json (see keys.go).
type Provider struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	BaseURL     string `json:"base_url"`
	TextModel   string `json:"text_model,omitempty"`
	VisionModel string `json:"vision_model,omitempty"`
	// Preset is the id of the preset it was created from; editing the address clears it.
	Preset string `json:"preset,omitempty"`
	// TestedAt and TestOK record the last test of the provider; changing its key or address clears them.
	TestedAt string `json:"tested_at,omitempty"`
	TestOK   bool   `json:"test_ok,omitempty"`
}

// mu serializes the read-modify-write of the settings file.
var mu sync.Mutex

// Providers returns the saved providers (not the built-in one).
func Providers() []Provider {
	return Load().Providers
}

// FindProvider returns a saved provider by id.
func FindProvider(id string) (Provider, bool) {
	for _, p := range Providers() {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// ValidateBase checks the base address of an OpenAI-compatible provider and
// returns it normalized (scheme://host[:port][/prefix/v1], no trailing slash).
// The scheme is required; the path must be empty or end in /v1.
func ValidateBase(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("the address is empty")
	}
	if len(s) > maxAddressLen {
		return "", fmt.Errorf("the address is longer than %d characters", maxAddressLen)
	}
	if !strings.Contains(s, "://") {
		return "", errors.New("the address must start with http:// or https://")
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", errors.New("the address is not valid")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
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
	if u.RawQuery != "" || u.ForceQuery {
		return "", errors.New("the address must have no query")
	}
	if u.Fragment != "" {
		return "", errors.New("the address must have no fragment")
	}
	path := strings.TrimRight(u.Path, "/")
	if path != "" {
		segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
		for _, seg := range segs {
			if !pathSegment.MatchString(seg) {
				return "", errors.New("the address path is not valid")
			}
		}
		// /v1 is the usual end; Google's OpenAI endpoint (…/v1beta/openai) ends in /openai.
		if last := segs[len(segs)-1]; last != "v1" && last != "openai" {
			return "", errors.New("the address path must be empty or end in /v1, such as https://api.example.com/v1")
		}
	}
	return scheme + "://" + u.Host + path, nil
}

// ValidateProvider checks a provider and returns it normalized. The id must be
// lowercase letters, digits and hyphens (at most 40 characters) and not the
// built-in one; the address follows the rules of its type.
func ValidateProvider(p Provider) (Provider, error) {
	p.ID = strings.TrimSpace(p.ID)
	p.Name = strings.TrimSpace(p.Name)
	p.TextModel = strings.TrimSpace(p.TextModel)
	p.VisionModel = strings.TrimSpace(p.VisionModel)
	switch {
	case p.ID == BuiltinID:
		return p, errors.New("the id \"ollama\" belongs to the built-in provider: change its address in the Ollama settings")
	case !providerIDPattern.MatchString(p.ID):
		return p, errors.New("the id must be lowercase letters, digits and hyphens, at most 40 characters")
	case p.Name == "":
		return p, errors.New("the name is empty")
	case len([]rune(p.Name)) > maxNameLen:
		return p, fmt.Errorf("the name is longer than %d characters", maxNameLen)
	case len(p.TextModel) > maxModelLen || len(p.VisionModel) > maxModelLen:
		return p, fmt.Errorf("a model name is longer than %d characters", maxModelLen)
	}
	var err error
	switch p.Type {
	case TypeOllama:
		p.BaseURL, err = ValidateAddress(p.BaseURL)
	case TypeOpenAI:
		p.BaseURL, err = ValidateBase(p.BaseURL)
	default:
		return p, fmt.Errorf("the type must be %q or %q", TypeOllama, TypeOpenAI)
	}
	if err == nil && !edition.ProviderHostAllowed(p.BaseURL) {
		return p, ErrHostNotAllowed
	}
	return p, err
}

// ErrHostNotAllowed is the refusal of a provider whose address the public edition does not accept.
var ErrHostNotAllowed = errors.New("this edition accepts only the providers on its list (the built-in Ollama is always there): that address is not allowed")

// CheckKeyTransport refuses to send a key over plain http to an address that is
// not this machine: a Bearer key in clear on a network is a leak.
func CheckKeyTransport(baseURL string, hasKey bool) error {
	if !hasKey {
		return nil
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil
	}
	if strings.EqualFold(u.Scheme, "http") && Remote(baseURL) {
		return errors.New("an API key is never sent over plain http to another machine: use an https address")
	}
	return nil
}

// AddProvider validates and saves a new provider.
func AddProvider(p Provider) (Provider, error) {
	mu.Lock()
	defer mu.Unlock()
	p, err := ValidateProvider(p)
	if err != nil {
		return p, err
	}
	f, rerr := ReadFile() // a source that cannot be read is not "no settings": saving would erase them
	if rerr != nil {
		return Provider{}, rerr
	}
	for _, q := range f.Providers {
		if q.ID == p.ID {
			return p, fmt.Errorf("there is already a provider with the id %q", p.ID)
		}
	}
	f.Providers = append(f.Providers, p)
	return p, Save(f)
}

// ErrNoSuchProvider means the id is not a saved provider.
var ErrNoSuchProvider = errors.New("there is no provider with that id")

// UpdateProvider validates and replaces the saved provider with the given id.
func UpdateProvider(id string, p Provider) (Provider, error) {
	mu.Lock()
	defer mu.Unlock()
	if id == BuiltinID {
		return p, errors.New("the built-in provider cannot be edited here: change its address in the Ollama settings")
	}
	p.ID = id
	p, err := ValidateProvider(p)
	if err != nil {
		return p, err
	}
	f, rerr := ReadFile() // a source that cannot be read is not "no settings": saving would erase them
	if rerr != nil {
		return Provider{}, rerr
	}
	for i, q := range f.Providers {
		if q.ID == id {
			if p.Type == TypeOpenAI {
				for _, t := range Tasks {
					if f.Routing[t] == id && ModelFor(p, t) == "" {
						return p, fmt.Errorf("the task %q is routed to this provider and needs its %s model", t, map[bool]string{true: "vision", false: "text"}[NeedsVision(t)])
					}
				}
			}
			p.Preset, p.TestedAt, p.TestOK = q.Preset, q.TestedAt, q.TestOK
			if p.BaseURL != q.BaseURL {
				p.Preset, p.TestedAt, p.TestOK = "", "", false
			}
			f.Providers[i] = p
			return p, Save(f)
		}
	}
	return p, ErrNoSuchProvider
}

// RecordTest remembers the result of the last test of a saved provider.
func RecordTest(id string, ok bool, at time.Time) error {
	mu.Lock()
	defer mu.Unlock()
	f, rerr := ReadFile() // a source that cannot be read is not "no settings": saving would erase them
	if rerr != nil {
		return rerr
	}
	for i, q := range f.Providers {
		if q.ID == id {
			f.Providers[i].TestedAt, f.Providers[i].TestOK = at.UTC().Format(time.RFC3339), ok
			return Save(f)
		}
	}
	return ErrNoSuchProvider
}

// ClearTest forgets the last test of a saved provider (its key or address changed).
func ClearTest(id string) error {
	mu.Lock()
	defer mu.Unlock()
	f, rerr := ReadFile() // a source that cannot be read is not "no settings": saving would erase them
	if rerr != nil {
		return rerr
	}
	for i, q := range f.Providers {
		if q.ID == id {
			f.Providers[i].TestedAt, f.Providers[i].TestOK = "", false
			return Save(f)
		}
	}
	return ErrNoSuchProvider
}

// DeleteProvider removes a saved provider and its key.
func DeleteProvider(id string) error {
	mu.Lock()
	defer mu.Unlock()
	if id == BuiltinID {
		return errors.New("the built-in provider cannot be removed: change its address in the Ollama settings")
	}
	f, rerr := ReadFile() // a source that cannot be read is not "no settings": saving would erase them
	if rerr != nil {
		return rerr
	}
	for _, t := range Tasks {
		if f.Routing[t] == id {
			return ErrProviderInUse{Task: t}
		}
	}
	for i, q := range f.Providers {
		if q.ID == id {
			f.Providers = append(f.Providers[:i], f.Providers[i+1:]...)
			delete(f.Consent, id)
			if len(f.Consent) == 0 {
				f.Consent = nil
			}
			if err := Save(f); err != nil {
				return err
			}
			return deleteKeyLocked(id)
		}
	}
	return ErrNoSuchProvider
}

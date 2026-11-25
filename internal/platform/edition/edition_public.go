//go:build !private

package edition

import (
	"net/url"
	"strings"

	"veritmo/internal/platform/providers"
)

// Name is "public" or "private".
const Name = "public"

// Private reports whether this is the private edition.
func Private() bool { return false }

// denyWords are the words that mark a model without safety training. A model is
// denied when its name, split on "-", "_", ":", "." and "/", holds one of them,
// or the run of words "muse glimmer".
var denyWords = []string{"abliterated", "uncensored", "unfiltered", "dolphin", "lexi"}

// denyRuns are denied word runs.
var denyRuns = [][]string{{"muse", "glimmer"}}

func nameWords(name string) []string {
	return strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return r == '-' || r == '_' || r == ':' || r == '.' || r == '/' || r == ' '
	})
}

// DenyModel returns a *NotAllowedError for a model on the deny list.
func DenyModel(name string) error {
	words := nameWords(name)
	for _, w := range words {
		for _, d := range denyWords {
			if w == d {
				return &NotAllowedError{Model: name}
			}
		}
	}
	for _, run := range denyRuns {
		for i := 0; i+len(run) <= len(words); i++ {
			match := true
			for j, r := range run {
				if words[i+j] != r {
					match = false
					break
				}
			}
			if match {
				return &NotAllowedError{Model: name}
			}
		}
	}
	return nil
}

// providerHosts are the hosts a saved provider may have: the hosts of the presets and nothing else.
var providerHosts = providers.Hosts()

// ProviderHostAllowed says whether a saved provider may use that base address.
func ProviderHostAllowed(base string) bool {
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	for _, a := range providerHosts {
		if h == a {
			return true
		}
	}
	return false
}

// AllowProviderHostsForTest adds hosts to the list for the tests of the packages
// that talk to fake servers. Only _test.go files may call it: a tree test fails
// when any other file does.
func AllowProviderHostsForTest(hosts ...string) { providerHosts = append(providerHosts, hosts...) }

// DescribeLiteral is the sentence that asks the describing model for a literal
// description; the public edition does not ask for it.
func DescribeLiteral() string { return "" }

// MergeLiteral is the clause that asks the merging model for a literal prompt.
func MergeLiteral() string { return "" }

// DetailModel is the "more detail" descriptor; the public edition has none.
func DetailModel() string { return "" }

// RefusalAdvice is whether a refusal may suggest another model or provider.
func RefusalAdvice() bool { return false }

// CivitaiEnabled is whether Civitai entries, keys and downloads exist.
func CivitaiEnabled() bool { return false }

// ContentAll is whether the server may be started showing every project.
func ContentAll() bool { return false }

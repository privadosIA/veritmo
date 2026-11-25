// Package assets3d is the server's library of 3D model packs (spec model-library): a list of known packs that are installed on
// demand and checked, the files of the installed ones, an index of their models, and the copy of a model into the sources of a
// project or a piece. It follows the shape of the HyperFrames blocks: a folder next to the server that is not committed, data
// fetched only when a person asks, pinned by SHA-256, from a host the list names.
package assets3d

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

//go:embed packs.json
var embeddedPacks []byte

// PackDef is a pack of the list that ships with the server.
type PackDef struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Author    string `json:"author"`
	Licence   string `json:"licence"`
	Page      string `json:"page"`
	Version   string `json:"version"`
	SizeBytes int64  `json:"size_bytes"`
	// SHA256 is the hash of the zip of the version that was checked.
	SHA256 string `json:"sha256"`
	// Fetch is how the zip is got: "kenney" (the signed link of the page) or "url" (the address in URL).
	Fetch string `json:"fetch"`
	URL   string `json:"url,omitempty"`
	// LicenceWords must all be in the licence text of the zip (case-insensitive) or the install is refused.
	LicenceWords []string `json:"licence_words"`
	Description  string   `json:"description"`
	Tags         []string `json:"tags"`
}

// Host is the only host the zip of the pack may be fetched from: the one of its page.
func (d PackDef) Host() string {
	u, err := url.Parse(d.Page)
	if err != nil {
		return ""
	}
	return u.Host
}

var (
	idPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)
	shaPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// LoadPacks reads a list of packs and refuses one that breaks the rules of the library: a plain id, only CC0 packs, a pinned
// hash, a known way to fetch it, an address on its own host and the words its licence must have. strict also asks for https.
func LoadPacks(data []byte, strict bool) ([]PackDef, error) {
	var defs []PackDef
	if err := json.Unmarshal(data, &defs); err != nil {
		return nil, fmt.Errorf("the list of packs is not valid: %w", err)
	}
	seen := map[string]bool{}
	for i, d := range defs {
		at := fmt.Sprintf("packs[%d] (%s)", i, d.ID)
		u, perr := url.Parse(d.Page)
		switch {
		case !idPattern.MatchString(d.ID):
			return nil, fmt.Errorf("%s: the id must be lower case letters, digits and dashes", at)
		case seen[d.ID]:
			return nil, fmt.Errorf("%s: the id is repeated", at)
		case d.Name == "" || d.Author == "":
			return nil, fmt.Errorf("%s: a pack has a name and an author", at)
		case d.Licence != "CC0":
			return nil, fmt.Errorf("%s: only CC0 packs are in the list, got %q", at, d.Licence)
		case !shaPattern.MatchString(d.SHA256):
			return nil, fmt.Errorf("%s: the zip's SHA-256 must be pinned", at)
		case d.Fetch != "kenney" && d.Fetch != "url":
			return nil, fmt.Errorf("%s: fetch must be kenney or url, got %q", at, d.Fetch)
		case perr != nil || u.Host == "" || (strict && u.Scheme != "https"):
			return nil, fmt.Errorf("%s: the page must be an https address", at)
		case len(d.LicenceWords) == 0:
			return nil, fmt.Errorf("%s: the words the licence text must have are needed", at)
		case d.Fetch == "url" && !sameHost(d.URL, d.Page):
			return nil, fmt.Errorf("%s: the address to fetch must be on the host of the page", at)
		}
		seen[d.ID] = true
	}
	return defs, nil
}

func sameHost(a, b string) bool {
	ua, err1 := url.Parse(a)
	ub, err2 := url.Parse(b)
	return err1 == nil && err2 == nil && ua.Host != "" && strings.EqualFold(ua.Host, ub.Host)
}

// DefaultPacks is the list that ships with the server.
func DefaultPacks() []PackDef {
	defs, err := LoadPacks(embeddedPacks, true)
	if err != nil {
		panic(err) // the list is embedded and tested: a broken one is a bug of the build
	}
	return defs
}

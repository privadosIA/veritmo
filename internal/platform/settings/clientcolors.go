package settings

// clientcolors.go: the colour of a client label (spec client-labels, "A label has a colour the server keeps"). The colour is only a visual aid:
// the server keeps the ones the owner chose and works the others out from the name by a fixed rule, so every dashboard shows the same one.

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"regexp"
	"strings"
)

var reColour = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// ValidClientColour says whether s is a colour written #rrggbb.
func ValidClientColour(s string) bool { return reColour.MatchString(s) }

// DefaultClientColour is the colour a label has when none was set: the hue is the first four bytes of the SHA-256 of its name, as a big-endian
// number, modulo 360, with a saturation of 55 % and a lightness of 50 %, written #rrggbb. The same rule is in docs/http-api.md.
func DefaultClientColour(name string) string {
	sum := sha256.Sum256([]byte(name))
	hue := float64(binary.BigEndian.Uint32(sum[:4]) % 360)
	return hslToHex(hue, 0.55, 0.50)
}

// hslToHex converts a hue in degrees, a saturation and a lightness (0 to 1) to #rrggbb.
func hslToHex(h, s, l float64) string {
	c := (1 - math.Abs(2*l-1)) * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := l - c/2
	var r, g, b float64
	switch {
	case h < 60:
		r, g, b = c, x, 0
	case h < 120:
		r, g, b = x, c, 0
	case h < 180:
		r, g, b = 0, c, x
	case h < 240:
		r, g, b = 0, x, c
	case h < 300:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	to := func(v float64) int { return int(math.Round((v + m) * 255)) }
	return fmt.Sprintf("#%02x%02x%02x", to(r), to(g), to(b))
}

// ClientColour is the colour of a label: the one the owner set, or the default one. A stored value that is not a colour is ignored.
func ClientColour(name string) string {
	if c, ok := Load().ClientColors[name]; ok && ValidClientColour(c) {
		return strings.ToLower(c)
	}
	return DefaultClientColour(name)
}

// SetClientColour keeps the colour of a label (#rrggbb), or removes it (an empty text) so that the default one applies again.
func SetClientColour(name, colour string) error {
	colour = strings.TrimSpace(colour)
	if colour != "" && !ValidClientColour(colour) {
		return fmt.Errorf("the colour %q is not written #rrggbb", colour)
	}
	mu.Lock()
	defer mu.Unlock()
	f, err := ReadFile() // a source that cannot be read is not "no settings": saving would erase them
	if err != nil {
		return err
	}
	if colour == "" {
		delete(f.ClientColors, name)
	} else {
		if f.ClientColors == nil {
			f.ClientColors = map[string]string{}
		}
		f.ClientColors[name] = strings.ToLower(colour)
	}
	return Save(f)
}

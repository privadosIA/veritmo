package musicmotion

// fonts.go: the pack of free fonts that comes with the program (spec motion-house-style). A design that names one of them
// (or one of the six old stack names that now map to the pack) gets the @font-face of it embedded in every page as a data: URL, so
// the render and the browser preview draw the same letters and the page still loads nothing from outside.

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strings"

	"veritmo/internal/music/musicmotion/fonts"
)

const fontLicence = "OFL-1.1"

// The pack itself (the files, the families and the names the six old stacks map to) lives in the fonts package, which
// has no dependencies, so the 3D scenes of the pipeline can read it too.
var (
	packed    = fonts.Packed
	fontAlias = fonts.Alias
)

// legacyStacks are the system stacks the first six names had.
var legacyStacks = map[string]string{
	"grotesk":   `"Helvetica Neue", Arial, "Liberation Sans", sans-serif`,
	"heavy":     `"Arial Black", "Helvetica Neue", Impact, sans-serif`,
	"mono":      `"DejaVu Sans Mono", "Liberation Mono", Menlo, Consolas, monospace`,
	"serif":     `Georgia, "Times New Roman", "Liberation Serif", serif`,
	"rounded":   `"Trebuchet MS", "Segoe UI", Verdana, sans-serif`,
	"condensed": `"Arial Narrow", "Liberation Sans Narrow", Impact, sans-serif`,
}

// Fonts are the typefaces a design may use: the pack and the six old names. Each is a stack whose first choice is embedded
// when it is a font of the pack (the page loads nothing from outside) and the value is its CSS.
var Fonts = buildFonts()

func buildFonts() map[string]string {
	out := map[string]string{}
	for name, l := range legacyStacks {
		if pack, ok := fontAlias[name]; ok {
			out[name] = `"` + packed[pack].Family + `", ` + l
		} else {
			out[name] = l
		}
	}
	for name, p := range packed {
		out[name] = p.Stack
	}
	return out
}

// FontFile says what is embedded for a font name: the weights and the licence of the font.
type FontFile struct {
	Family  string `json:"family"`
	Weights []int  `json:"weights"`
	Licence string `json:"licence"`
}

// FontFiles are the fonts that are embedded, by the name a design uses (the six old names that map to the pack included).
func FontFiles() map[string]FontFile {
	out := map[string]FontFile{}
	for name, p := range packed {
		out[name] = FontFile{p.Family, p.Weights, fontLicence}
	}
	for old, pack := range fontAlias {
		p := packed[pack]
		out[old] = FontFile{p.Family, p.Weights, fontLicence}
	}
	return out
}

// packOf is the font of the pack a name stands for, if it stands for one.
func packOf(name string) (string, bool) { return fonts.Of(name) }

// FontFaceCSS is the @font-face of the fonts of the pack the names stand for, each once, with the font as a data: URL (the
// weights it has). Names that stand for no font of the pack (the system stacks) add nothing.
func FontFaceCSS(names ...string) string {
	var order []string
	seen := map[string]bool{}
	for _, n := range names {
		if p, ok := packOf(n); ok && !seen[p] {
			seen[p] = true
			order = append(order, p)
		}
	}
	sort.Strings(order)
	var b strings.Builder
	for _, name := range order {
		p := packed[name]
		for _, w := range p.Weights {
			raw, err := fonts.ReadFile(name, w)
			if err != nil {
				continue // the pack is embedded at build time; a test checks every file is there
			}
			fmt.Fprintf(&b, "@font-face{font-family:%q;font-style:normal;font-weight:%d;font-display:block;src:url(data:font/woff2;base64,%s) format(\"woff2\")}",
				p.Family, w, base64.StdEncoding.EncodeToString(raw))
		}
	}
	return b.String()
}

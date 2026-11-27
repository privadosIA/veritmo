// Package musicmotion makes motion-graphics pieces that follow a song: the design of a piece (its colours, fonts,
// modes and concept), the catalog of animated parts, the script and the render. See specs/music-motion.
package musicmotion

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Mode is how a section is painted: the dark tone as the background and the light one as the text (dark), or the
// other way round (light).
const (
	ModeDark  = "dark"
	ModeLight = "light"
)

// The ways the verse is marked as it is sung (Design.VerseStyle). The empty value is plain.
const (
	VerseStylePlain     = "plain"     // the words appear and the keyword is in the accent colour
	VerseStyleHighlight = "highlight" // the word being sung has a background in the accent colour and keeps it once sung
	VerseStyleKaraoke   = "karaoke"   // the words already sung change to the accent colour
	VerseStyleGlow      = "glow"      // each word in the text colour with a soft glow and a halo, no background
)

// The places the verse can be drawn (VerseLayout.Position) and how it is aligned (VerseLayout.Align).
const (
	VersePositionTop    = "top"
	VersePositionCenter = "center"
	VersePositionBottom = "bottom"
	VerseAlignLeft      = "left"
	VerseAlignCenter    = "center"
	VerseAlignRight     = "right"
)

// The limits of the scale of the verse.
const (
	MinVerseScale = 0.6
	MaxVerseScale = 1.6
)

// VerseLayout says where the verse is drawn, how big and how it is aligned. An empty field is the default (bottom, 1, center), and a design
// with no layout draws the verse exactly as it was drawn before there was one.
type VerseLayout struct {
	Position string  `json:"position,omitempty"`
	Scale    float64 `json:"scale,omitempty"`
	Align    string  `json:"align,omitempty"`
}

// Validate checks the layout as a whole: a place and an alignment that exist and a scale inside its limits (0 is the default, 1).
func (l VerseLayout) Validate() error {
	switch l.Position {
	case "", VersePositionTop, VersePositionCenter, VersePositionBottom:
	default:
		return fmt.Errorf("verse_layout.position must be top, center or bottom, got %q", l.Position)
	}
	switch l.Align {
	case "", VerseAlignLeft, VerseAlignCenter, VerseAlignRight:
	default:
		return fmt.Errorf("verse_layout.align must be left, center or right, got %q", l.Align)
	}
	if l.Scale != 0 && (l.Scale < MinVerseScale || l.Scale > MaxVerseScale) {
		return fmt.Errorf("verse_layout.scale must be between %v and %v, got %v", MinVerseScale, MaxVerseScale, l.Scale)
	}
	return nil
}

// IsDefault says the layout draws the verse where and as it has always been drawn.
func (l *VerseLayout) IsDefault() bool {
	return l == nil || ((l.Position == "" || l.Position == VersePositionBottom) && (l.Scale == 0 || l.Scale == 1) && (l.Align == "" || l.Align == VerseAlignCenter))
}

// VerseStyles lists the verse styles.
var VerseStyles = []string{VerseStylePlain, VerseStyleHighlight, VerseStyleKaraoke, VerseStyleGlow}

// The looks of the word hit (Design.HitStyle). The empty value is plain.
const (
	HitStylePlain = "plain" // the word in the text colour, as one piece
	HitStyleMetal = "metal" // metallic letters with a shine that crosses them, entering one after another
	HitStyleGlow  = "glow"  // letters in the accent colour with a light around them, entering one after another
)

// HitStyles lists the looks of the word hit.
var HitStyles = []string{HitStylePlain, HitStyleMetal, HitStyleGlow}

// MaxConcept is the longest concept, in characters.
const MaxConcept = 200

// MinAccentContrast is the contrast the accent needs against the background of a mode that is used: the text is large
// and bold, for which 3:1 is the accepted floor.
const MinAccentContrast = 3.0

// FontNames lists the fonts a design may name, in order.
func FontNames() []string {
	out := make([]string, 0, len(Fonts))
	for n := range Fonts {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Design is the look of a piece.
type Design struct {
	// Dark and Light are the two tones. In a dark section Dark is the background and Light the text; in a light
	// section it is the other way round.
	Dark   string `json:"dark"`
	Light  string `json:"light"`
	Accent string `json:"accent"`
	// Display is the font of the big words, Body the font of the verses and the small text.
	Display string `json:"display"`
	Body    string `json:"body"`
	// DefaultMode is the mode of a section tag that Modes does not name.
	DefaultMode string `json:"default_mode"`
	// Modes gives the mode of each section tag (lower case: "chorus").
	Modes map[string]string `json:"modes,omitempty"`
	// Concept is the visual idea every scene returns to.
	Concept string `json:"concept"`
	// VerseStyle is how the verse is marked as it is sung: plain (also when empty), highlight or karaoke.
	VerseStyle string `json:"verse_style,omitempty"`
	// VerseLayout is where the verse is drawn, how big and how it is aligned; nil is the default (at the bottom, centred, the size the frame gives).
	VerseLayout *VerseLayout `json:"verse_layout,omitempty"`
	// HitStyle is how the word hit looks: plain (also when empty), metal or glow.
	HitStyle string `json:"hit_style,omitempty"`
	// VerseGroups, when false, shows every line whole instead of in groups of words (a line of more than six words is grouped by the
	// energy of its section by default).
	VerseGroups *bool `json:"verse_groups,omitempty"`
	// Theme is the stylesheet, the drawings and the HUD texts every page of the piece shares (see theme.go); nil for none.
	Theme *Theme `json:"theme,omitempty"`
	// TransitionSeconds is the crossfade between the pieces of the script, 0 to MaxTransitionSeconds (0, the default, cuts);
	// a script piece may set its own.
	TransitionSeconds float64 `json:"transition_seconds,omitempty"`
}

// MaxTransitionSeconds is the longest crossfade between two pieces.
const MaxTransitionSeconds = 2.0

// ResolveMode is the mode a script piece is drawn in: its own when someone set one, else the one the design gives
// its section. Every reader of a piece's mode goes through it, so a design that changes is followed by every piece
// that does not carry a mode of its own.
func ResolveMode(d Design, p Piece) string {
	if p.Mode != "" {
		return p.Mode
	}
	return d.ModeFor(p.Section)
}

// ModeFor is the mode of a section.
func (d Design) ModeFor(tag string) string {
	if m, ok := d.Modes[strings.ToLower(strings.TrimSpace(tag))]; ok {
		return m
	}
	if d.DefaultMode == ModeLight {
		return ModeLight
	}
	return ModeDark
}

// Colours are the background, the text and the accent of a mode.
func (d Design) Colours(mode string) (background, text, accent string) {
	if mode == ModeLight {
		return d.Light, d.Dark, d.Accent
	}
	return d.Dark, d.Light, d.Accent
}

var hexColour = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

// luminance is the relative luminance of a hex colour; ok is false when it is not one.
func luminance(hex string) (float64, bool) {
	if !hexColour.MatchString(hex) {
		return 0, false
	}
	h := hex[1:]
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	var c [3]float64
	for i := 0; i < 3; i++ {
		v, _ := strconv.ParseUint(h[i*2:i*2+2], 16, 8)
		f := float64(v) / 255
		if f <= 0.03928 {
			c[i] = f / 12.92
		} else {
			c[i] = math.Pow((f+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*c[0] + 0.7152*c[1] + 0.0722*c[2], true
}

// Contrast is the WCAG contrast ratio (1 to 21) of two hex colours; 0 when one is not a colour.
func Contrast(a, b string) float64 {
	la, ok1 := luminance(a)
	lb, ok2 := luminance(b)
	if !ok1 || !ok2 {
		return 0
	}
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// Validate checks a design and names the first field that is wrong.
func (d Design) Validate() error {
	for _, c := range []struct{ field, v string }{{"dark", d.Dark}, {"light", d.Light}, {"accent", d.Accent}} {
		if !hexColour.MatchString(c.v) {
			return fmt.Errorf("design.%s must be a hex colour like #1a2b3c, got %q", c.field, c.v)
		}
	}
	if Contrast(d.Dark, d.Light) < 4.5 {
		return fmt.Errorf("design.dark and design.light need a contrast of at least 4.5:1 to be read, they have %.1f:1", Contrast(d.Dark, d.Light))
	}
	for _, f := range []struct{ field, v string }{{"display", d.Display}, {"body", d.Body}} {
		if _, ok := Fonts[f.v]; !ok {
			return fmt.Errorf("design.%s must be one of %s, got %q", f.field, strings.Join(FontNames(), ", "), f.v)
		}
	}
	used := map[string]bool{}
	switch d.DefaultMode {
	case ModeDark, ModeLight:
		used[d.DefaultMode] = true
	default:
		return fmt.Errorf("design.default_mode must be %q or %q, got %q", ModeDark, ModeLight, d.DefaultMode)
	}
	tags := make([]string, 0, len(d.Modes))
	for tag := range d.Modes {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	for _, tag := range tags {
		m := d.Modes[tag]
		if m != ModeDark && m != ModeLight {
			return fmt.Errorf("design.modes[%q] must be %q or %q, got %q", tag, ModeDark, ModeLight, m)
		}
		used[m] = true
	}
	for _, m := range []string{ModeDark, ModeLight} {
		if !used[m] {
			continue
		}
		bg, _, _ := d.Colours(m)
		if c := Contrast(d.Accent, bg); c < MinAccentContrast {
			return &AccentContrastError{Accent: d.Accent, Contrast: c, Mode: m, Background: bg}
		}
	}
	if d.VerseLayout != nil {
		if err := d.VerseLayout.Validate(); err != nil {
			return fmt.Errorf("design.%w", err)
		}
	}
	if d.VerseStyle != "" && !slices.Contains(VerseStyles, d.VerseStyle) {
		return fmt.Errorf("design.verse_style must be one of %s, got %q", strings.Join(VerseStyles, ", "), d.VerseStyle)
	}
	if d.HitStyle != "" && !slices.Contains(HitStyles, d.HitStyle) {
		return fmt.Errorf("design.hit_style must be one of %s, got %q", strings.Join(HitStyles, ", "), d.HitStyle)
	}
	if n := len([]rune(d.Concept)); n > MaxConcept {
		return fmt.Errorf("design.concept has %d characters, the most is %d", n, MaxConcept)
	}
	if d.TransitionSeconds < 0 || d.TransitionSeconds > MaxTransitionSeconds {
		return fmt.Errorf("design.transition_seconds must be between 0 and %v, got %v", MaxTransitionSeconds, d.TransitionSeconds)
	}
	if d.Theme != nil {
		if err := d.Theme.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// palette is a fixed design the fallback picks by the words of a style.
type palette struct {
	name     string
	words    []string
	dark     string
	light    string
	accent   string
	display  string
	body     string
	base     string
	concept  string
	chorusBy string // the mode of the chorus-like sections
}

var palettes = []palette{
	{name: "neon", words: []string{"electronic", "synth", "techno", "cyber", "code", "tech", "robot", "ai", "glitch", "dance"}, dark: "#0b0f14", light: "#e8f1ec", accent: "#2bff88", display: "heavy", body: "mono", base: ModeDark, chorusBy: ModeDark, concept: "a terminal that keeps writing the song"},
	{name: "crimson", words: []string{"rock", "metal", "punk", "angry", "alarm", "dark", "heavy", "drama"}, dark: "#12090c", light: "#f4eeee", accent: "#ff2e54", display: "heavy", body: "grotesk", base: ModeDark, chorusBy: ModeLight, concept: "a warning that gets louder with every chorus"},
	{name: "magenta", words: []string{"pop", "night", "city", "club", "synthwave", "disco", "love"}, dark: "#0d0a12", light: "#f6eef6", accent: "#ff3da8", display: "condensed", body: "grotesk", base: ModeDark, chorusBy: ModeDark, concept: "a city at night seen through a glowing screen"},
	{name: "paper", words: []string{"folk", "acoustic", "indie", "calm", "soft", "gentle", "ballad", "lofi", "lo-fi", "jazz"}, dark: "#1d1b19", light: "#f5f0e6", accent: "#b8411f", display: "serif", body: "serif", base: ModeLight, chorusBy: ModeLight, concept: "pages of a notebook turning to the rhythm"},
	{name: "sunset", words: []string{"summer", "happy", "sun", "beach", "reggae", "latin", "tropical", "fun", "party"}, dark: "#1a1030", light: "#fff4e6", accent: "#e8451c", display: "rounded", body: "rounded", base: ModeLight, chorusBy: ModeDark, concept: "a sunset that changes colour on every beat"},
	{name: "ink", words: []string{"classical", "orchestral", "epic", "cinematic", "sad", "melancholic", "piano"}, dark: "#0e1116", light: "#eef0f4", accent: "#4da3ff", display: "serif", body: "grotesk", base: ModeDark, chorusBy: ModeDark, concept: "light that opens slowly over a dark stage"},
}

// Fallback makes a design with no model: a fixed palette chosen by the words of the style (the first one that
// matches, or one picked from the style's hash so the same style always gets the same), the modes by the section
// tags and a concept from the palette. sectionTags are the tags of the song's sections.
func Fallback(style string, sectionTags []string) Design {
	words := strings.FieldsFunc(strings.ToLower(style), func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') })
	var pick *palette
	for i := range palettes {
		for _, w := range words {
			for _, k := range palettes[i].words {
				if w == k {
					pick = &palettes[i]
					break
				}
			}
			if pick != nil {
				break
			}
		}
		if pick != nil {
			break
		}
	}
	if pick == nil {
		h := fnv.New32a()
		h.Write([]byte(strings.ToLower(strings.TrimSpace(style))))
		pick = &palettes[int(h.Sum32())%len(palettes)]
	}
	d := Design{Dark: pick.dark, Light: pick.light, Accent: pick.accent, Display: pick.display, Body: pick.body,
		DefaultMode: pick.base, Modes: map[string]string{}, Concept: pick.concept}
	for _, tag := range sectionTags {
		if IsChorus(tag) {
			d.Modes[strings.ToLower(strings.TrimSpace(tag))] = pick.chorusBy
		}
	}
	return d
}

// IsChorus says whether a section tag is the chorus.
func IsChorus(tag string) bool {
	t := strings.ToLower(tag)
	return strings.Contains(t, "chorus") || strings.Contains(t, "hook") || strings.Contains(t, "estribillo") || strings.Contains(t, "coro")
}

// Stored is a design as kept in the piece: who made it and whether the user edited it.
type Stored struct {
	Design Design `json:"design"`
	// Source is "assistant", "fallback" or "user".
	Source string `json:"source"`
	// FallbackReason says why the design is a fallback; empty when the assistant or the user wrote it.
	FallbackReason string `json:"fallback_reason,omitempty"`
	// Edited is true once the user has set the design: a new proposal does not replace it unless asked.
	Edited bool `json:"edited"`
	// Note says what the program changed in the assistant's design: the accent moved to reach the contrast. Empty for none.
	Note string `json:"note,omitempty"`
	// Notice is only in the answer of a save, never in the file: what the person may want to know about the script
	// after the change (see OwnModeNotice).
	Notice string `json:"notice,omitempty"`
}

// ModesChanged says whether two designs give some section a different mode.
func ModesChanged(a, b Design) bool {
	if a.DefaultMode != b.DefaultMode {
		return true
	}
	tags := map[string]bool{}
	for t := range a.Modes {
		tags[t] = true
	}
	for t := range b.Modes {
		tags[t] = true
	}
	for t := range tags {
		if a.ModeFor(t) != b.ModeFor(t) {
			return true
		}
	}
	return false
}

// OwnModeNotice counts the pieces of a script that carry a mode of their own that differs from the one the design
// gives them, and says so; empty when there is none. It changes nothing: a person may want a light chorus on a dark
// design, so the pieces are theirs to clear.
func OwnModeNotice(s Script, d Design) string {
	n := 0
	for _, p := range s.Pieces {
		if p.Mode != "" && p.Mode != d.With(p.Design).ModeFor(p.Section) {
			n++
		}
	}
	switch n {
	case 0:
		return ""
	case 1:
		return "1 piece of the script carries a mode of its own that differs from the design's: clear it to follow the design"
	}
	return fmt.Sprintf("%d pieces of the script carry a mode of their own that differs from the design's: clear them to follow it", n)
}

// DesignPath is where the design of a piece is kept.
func DesignPath(dir string) string { return filepath.Join(dir, "design.json") }

// LoadDesign reads the stored design of a piece kept in a folder.
func LoadDesign(dir string) (Stored, bool) { return LoadDesignAt(DirPlace(dir)) }

// LoadDesignAt reads the stored design of a piece.
func LoadDesignAt(p Place) (Stored, bool) {
	raw, ok := p.ReadDoc(DocDesign)
	if !ok {
		return Stored{}, false
	}
	var s Stored
	if json.Unmarshal(raw, &s) != nil {
		return Stored{}, false
	}
	return s, true
}

// SaveDesign stores a design in a folder after validating it.
func SaveDesign(dir string, s Stored) error { return SaveDesignAt(DirPlace(dir), s) }

// SaveDesignAt stores a design after validating it.
func SaveDesignAt(p Place, s Stored) error {
	if err := s.Design.Validate(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return p.WriteDoc(DocDesign, raw)
}

// DesignOverride has the fields of a design that a piece may replace for itself alone. An empty field leaves the song's
// design as it is.
type DesignOverride struct {
	Dark        string       `json:"dark,omitempty"`
	Light       string       `json:"light,omitempty"`
	Accent      string       `json:"accent,omitempty"`
	Display     string       `json:"display,omitempty"`
	Body        string       `json:"body,omitempty"`
	VerseStyle  string       `json:"verse_style,omitempty"`
	VerseLayout *VerseLayout `json:"verse_layout,omitempty"`
	HitStyle    string       `json:"hit_style,omitempty"`
	// HUD replaces the theme's HUD texts for this piece alone (another act, another label).
	HUD *HUD `json:"hud,omitempty"`
}

// With is the design with the fields of o that are set replaced; the design itself is not changed. A nil override gives
// the design as it is.
func (d Design) With(o *DesignOverride) Design {
	out := d
	if d.Modes != nil {
		out.Modes = make(map[string]string, len(d.Modes))
		for k, v := range d.Modes {
			out.Modes[k] = v
		}
	}
	if o == nil {
		return out
	}
	set := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	set(&out.Dark, o.Dark)
	set(&out.Light, o.Light)
	set(&out.Accent, o.Accent)
	set(&out.Display, o.Display)
	set(&out.Body, o.Body)
	set(&out.VerseStyle, o.VerseStyle)
	set(&out.HitStyle, o.HitStyle)
	if o.VerseLayout != nil {
		l := *o.VerseLayout
		out.VerseLayout = &l
	}
	if o.HUD != nil {
		out.Theme = out.Theme.withHUD(o.HUD)
	}
	return out
}

// AccentContrastError is the refusal of an accent that does not read against the background of a mode; the assistant
// gets the numbers back (see designHint).
type AccentContrastError struct {
	Accent     string
	Contrast   float64
	Mode       string
	Background string
}

func (e *AccentContrastError) Error() string {
	return fmt.Sprintf("design.accent %s has a contrast of %.1f:1 with the background of the %s mode (%s); it needs %.0f:1", e.Accent, e.Contrast, e.Mode, e.Background, MinAccentContrast)
}

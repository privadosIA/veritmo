package musicmotion

// catalog.go: the catalog of animated parts. A part is a folder with manifest.json (name, description, the levels
// it supports, its parameters with their limits) and part.html (a fragment of HTML, CSS and script that draws and
// animates it from the beat clock). The catalog is read from the parts that come with the program and from a folder
// of the user, on every request, so a part added by putting a folder there shows up on the next list. An item
// that is not valid is left out with one warning in the log. See specs/music-motion ("The parts come from a catalog").

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// The levels of complexity, from the simplest.
const (
	LevelSimple   = "simple"
	LevelMedium   = "medium"
	LevelAdvanced = "advanced"
)

// Levels lists the levels in order.
var Levels = []string{LevelSimple, LevelMedium, LevelAdvanced}

// LevelRank is 0 for simple, 1 for medium and 2 for advanced; -1 for anything else.
func LevelRank(level string) int {
	for i, l := range Levels {
		if l == level {
			return i
		}
	}
	return -1
}

// Param is one parameter of a part.
type Param struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"` // string, int, number, bool or enum
	Description string   `json:"description,omitempty"`
	Default     any      `json:"default"`
	Min         *float64 `json:"min,omitempty"`
	Max         *float64 `json:"max,omitempty"`
	MaxLength   int      `json:"max_length,omitempty"`
	Options     []string `json:"options,omitempty"`
	// Animatable says a script may change the parameter over a piece: the part reads it inside __partSeek, not only
	// while it loads, and the fragment does not fill it in as text.
	Animatable bool `json:"animatable,omitempty"`
}

// Manifest describes a part.
type Manifest struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Version     int      `json:"version"`
	Levels      []string `json:"levels"`
	// CostMS is what a frame of this part takes to render at 1280x720, in milliseconds (measured); the render
	// estimate starts from it. Zero means DefaultCostMS.
	CostMS int `json:"cost_ms,omitempty"`
	// Manual, when true, keeps the part out of what the fixed rule and the level substitution choose on their own: it
	// is used only when a script names it (the blank part, which draws nothing, is one).
	Manual bool    `json:"manual,omitempty"`
	Params []Param `json:"params"`
	// Verse and Hit say who draws the lyric and the word hit of a piece whose main part this is: "page" (the default)
	// is the page's own layer, "own" is the part itself (from window.__scene.lines and window.__scene.hit).
	Verse string `json:"verse,omitempty"`
	Hit   string `json:"hit,omitempty"`
	// OverVideo says the part leaves what is behind it visible: it draws no opaque background over the whole frame, so a piece
	// with a video behind it (a background) may use it. A part is marked after it is drawn on a transparent page and measured.
	OverVideo bool `json:"over_video"`
	// Bands is how much of each third of the frame the part paints opaque (see Bands), measured with the same drawing as OverVideo. Nil
	// is unknown, not empty: a part nobody has measured says nothing about where the verse can go.
	Bands *Bands `json:"bands,omitempty"`
	// Scene says the part was written for one script piece of one piece (see scene_draft.go); nil for any other part.
	Scene *SceneRef `json:"scene,omitempty"`
	// World is the world of the video a scene was written in, one line: the writer of the first scene of a piece that has none
	// states it here, and the batch keeps it with the piece so that the scenes after it belong to the same film.
	World string `json:"world,omitempty"`
	// FixedCopy, when set, overrides what the program works out about the part's text (see Part.HasFixedCopy): true says its
	// page draws example text of its own, false says it does not although a parameter or its origin suggests it.
	FixedCopy *bool `json:"fixed_copy,omitempty"`
	// Pictures is how many pictures of a HyperFrames block the part carries inside (data: URLs); such a part keeps their
	// colours and does not follow the design's. Zero for any other part.
	Pictures int `json:"pictures,omitempty"`
}

// SceneRef names the script piece of a piece a scene part was written for.
type SceneRef struct {
	Piece       string `json:"piece"`
	ScriptPiece string `json:"script_piece"`
}

// Draws values of Manifest.Verse and Manifest.Hit.
const (
	DrawnByPage = "page"
	DrawnByPart = "own"
)

// OwnsVerse says the part draws the lyric itself, so the page leaves its verse layer out.
func (m Manifest) OwnsVerse() bool { return m.Verse == DrawnByPart }

// OwnsHit says the part draws the word hit itself, so the page leaves its hit layers out.
func (m Manifest) OwnsHit() bool { return m.Hit == DrawnByPart }

// IsScene says the part is a scene written for a piece: it stays out of the catalog's default list.
func (m Manifest) IsScene() bool { return m.Scene != nil }

// DefaultCostMS is the cost of a frame of a part that does not say.
const DefaultCostMS = 60

// Part is a part of the catalog.
type Part struct {
	Manifest
	// Fragment is the content of part.html.
	Fragment string `json:"-"`
	// Source is "builtin" for the parts that come with the program and "user" for the ones the user added.
	Source string `json:"source"`
	// Origin is the reference video the part came from, for a user's part that was drafted from one; nil otherwise.
	Origin *PartOrigin `json:"-"`
	// HasFixedCopy says the part's page draws example text that is not the song's (a name, a button label, a title):
	// the fixed rule of the script and the level substitution leave it out. Worked out when the catalog is read.
	HasFixedCopy bool `json:"fixed_copy"`
	// UpdatedAt is when the part was last written (zero for the parts that come with the program); lists sort by it.
	UpdatedAt time.Time `json:"-"`
}

// fixedCopy works out whether a part draws example text of its own: the manifest says so (or not) when it sets
// fixed_copy; otherwise a text parameter with a default that is not a colour does, and so does a block converted
// from HyperFrames, whose pages carry the example copy of the original.
func fixedCopy(m Manifest, o *PartOrigin) bool {
	if m.FixedCopy != nil {
		return *m.FixedCopy
	}
	for _, p := range m.Params {
		if p.Type != "string" {
			continue
		}
		d, _ := p.Default.(string)
		d = strings.TrimSpace(d)
		if d != "" && !strings.HasPrefix(d, "#") && !strings.HasPrefix(d, "rgb") {
			return true
		}
	}
	return o != nil && o.Source == "hyperframes"
}

// Supports says whether the part can be used at the level.
func (p Part) Supports(level string) bool {
	for _, l := range p.Levels {
		if l == level {
			return true
		}
	}
	return false
}

var (
	reName  = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	rePName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	// reSeekAssign finds a part that takes over window.__seek: the page's own seek belongs to the builder.
	reSeekAssign = regexp.MustCompile(`window\.__seek\s*=`)
)

// parseManifest reads and checks a manifest; the folder name must be the part's name.
func parseManifest(raw []byte, folder string) (Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("manifest.json is not valid: %w", err)
	}
	if !reName.MatchString(m.Name) {
		return Manifest{}, fmt.Errorf("the name %q must be lower case letters, digits and dashes, 2 to 32 characters", m.Name)
	}
	if m.Name != folder {
		return Manifest{}, fmt.Errorf("the name %q must be the name of its folder %q", m.Name, folder)
	}
	if strings.TrimSpace(m.Description) == "" {
		return Manifest{}, errors.New("the description is empty")
	}
	if m.Version < 1 {
		return Manifest{}, errors.New("the version must be 1 or more")
	}
	if m.CostMS < 0 || m.CostMS > 5000 {
		return Manifest{}, fmt.Errorf("cost_ms must be between 0 and 5000, got %d", m.CostMS)
	}
	if len(m.Levels) == 0 {
		return Manifest{}, errors.New("the part supports no level")
	}
	for _, l := range m.Levels {
		if LevelRank(l) < 0 {
			return Manifest{}, fmt.Errorf("the level %q does not exist (%s)", l, strings.Join(Levels, ", "))
		}
	}
	for what, v := range map[string]string{"verse": m.Verse, "hit": m.Hit} {
		if v != "" && v != DrawnByPage && v != DrawnByPart {
			return Manifest{}, fmt.Errorf("%s must be %q or %q, got %q", what, DrawnByPage, DrawnByPart, v)
		}
	}
	if m.Bands != nil {
		if err := m.Bands.Validate(); err != nil {
			return Manifest{}, err
		}
	}
	if m.Scene != nil {
		if m.Scene.Piece == "" || !reID.MatchString(m.Scene.ScriptPiece) {
			return Manifest{}, errors.New("scene needs the piece and the script_piece it was written for")
		}
	}
	seen := map[string]bool{}
	for i := range m.Params {
		p := &m.Params[i]
		if !rePName.MatchString(p.Name) {
			return Manifest{}, fmt.Errorf("the parameter %q must be lower case letters, digits and underscores", p.Name)
		}
		if seen[p.Name] {
			return Manifest{}, fmt.Errorf("the parameter %q is repeated", p.Name)
		}
		seen[p.Name] = true
		switch p.Type {
		case "string", "int", "number", "bool", "enum":
		default:
			return Manifest{}, fmt.Errorf("the parameter %q has the type %q; use string, int, number, bool or enum", p.Name, p.Type)
		}
		if p.Type == "enum" && len(p.Options) == 0 {
			return Manifest{}, fmt.Errorf("the enum parameter %q has no options", p.Name)
		}
		if p.Default == nil {
			return Manifest{}, fmt.Errorf("the parameter %q has no default", p.Name)
		}
		if _, err := checkParam(*p, p.Default); err != nil {
			return Manifest{}, fmt.Errorf("the default of %q is not valid: %w", p.Name, err)
		}
	}
	return m, nil
}

// checkParam checks one value against its parameter and returns it in its canonical type (string, int64,
// float64 or bool).
func checkParam(p Param, v any) (any, error) {
	switch p.Type {
	case "string":
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("must be text")
		}
		if p.MaxLength > 0 && len([]rune(s)) > p.MaxLength {
			return nil, fmt.Errorf("has %d characters, the most is %d", len([]rune(s)), p.MaxLength)
		}
		return s, nil
	case "bool":
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("must be true or false")
		}
		return b, nil
	case "enum":
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("must be one of %s", strings.Join(p.Options, ", "))
		}
		for _, o := range p.Options {
			if o == s {
				return s, nil
			}
		}
		return nil, fmt.Errorf("%q is not one of %s", s, strings.Join(p.Options, ", "))
	case "int", "number":
		f, ok := v.(float64) // JSON numbers
		if !ok {
			switch n := v.(type) {
			case int:
				f, ok = float64(n), true
			case int64:
				f, ok = float64(n), true
			}
		}
		if !ok {
			return nil, fmt.Errorf("must be a number")
		}
		if p.Type == "int" && f != float64(int64(f)) {
			return nil, fmt.Errorf("must be a whole number")
		}
		if p.Min != nil && f < *p.Min {
			return nil, fmt.Errorf("is %v, the least is %v", f, *p.Min)
		}
		if p.Max != nil && f > *p.Max {
			return nil, fmt.Errorf("is %v, the most is %v", f, *p.Max)
		}
		if p.Type == "int" {
			return int64(f), nil
		}
		return f, nil
	}
	return nil, fmt.Errorf("unknown type %q", p.Type)
}

// ResolveParams fills the defaults of a part and checks what is given: an unknown parameter or a value outside its
// limits is refused and named.
func (p Part) ResolveParams(given map[string]any) (map[string]any, error) {
	out := map[string]any{}
	known := map[string]Param{}
	for _, pr := range p.Params {
		known[pr.Name] = pr
	}
	names := make([]string, 0, len(given))
	for n := range given {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		pr, ok := known[n]
		if !ok {
			return nil, fmt.Errorf("the part %q has no parameter %q", p.Name, n)
		}
		v, err := checkParam(pr, given[n])
		if err != nil {
			return nil, fmt.Errorf("the parameter %q of the part %q %w", n, p.Name, err)
		}
		out[n] = v
	}
	for _, pr := range p.Params {
		if _, ok := out[pr.Name]; !ok {
			v, _ := checkParam(pr, pr.Default)
			out[pr.Name] = v
		}
	}
	return out, nil
}

// Catalog reads parts from a built-in tree and a folder of the user.
type Catalog struct {
	Builtin fs.FS  // parts that come with the program (a folder per part); may be nil
	UserDir string // the user's folder; may be empty or missing; used when Parts is nil
	// Parts is where the user's parts are kept (the database, in the program). Nil with UserDir empty means the catalog has only the built-in parts.
	Parts PartStore
	// cache holds the built-in parts, parsed once (NewCatalog sets it); a Catalog made as a literal parses them on each call.
	cache *builtinCache
}

type builtinCache struct {
	once  sync.Once
	parts map[string]Part
}

// NewCatalog is the catalog the program uses: the parts that come with it (parsed once) and the user's, kept in parts (nil: none).
func NewCatalog(parts PartStore) Catalog {
	return Catalog{Builtin: BuiltinParts(), Parts: parts, cache: &builtinCache{}}
}

// WithParts is the same catalog with another store of the user's parts: a transaction gives the store that works inside it.
func (c Catalog) WithParts(p PartStore) Catalog {
	c.Parts = p
	return c
}

func (c Catalog) userStore() PartStore {
	switch {
	case c.Parts != nil:
		return c.Parts
	case c.UserDir != "":
		return dirParts{dir: c.UserDir}
	}
	return nil
}

func (c Catalog) builtinParts() map[string]Part {
	if c.Builtin == nil {
		return nil
	}
	if c.cache != nil {
		c.cache.once.Do(func() { c.cache.parts = readParts(c.Builtin, "builtin") })
		return c.cache.parts
	}
	return readParts(c.Builtin, "builtin")
}

var warned sync.Map

// warnOnce logs a problem with an item once, however many requests list the catalog.
func warnOnce(key, msg string) {
	if _, dup := warned.LoadOrStore(key, true); !dup {
		slog.Warn("music-motion catalog: " + msg)
	}
}

func readParts(fsys fs.FS, source string) map[string]Part {
	out := map[string]Part{}
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if part, ok := readPartFolder(fsys, e.Name(), source); ok {
			out[e.Name()] = part
		}
	}
	return out
}

// readPartFolder reads the part in the folder name of fsys, or says why it is left out (one warning in the log, once).
func readPartFolder(fsys fs.FS, name, source string) (Part, bool) {
	key := source + "/" + name
	raw, err := fs.ReadFile(fsys, name+"/manifest.json")
	if err != nil {
		warnOnce(key+"#"+err.Error(), fmt.Sprintf("the part folder %q has no manifest.json, left out", key))
		return Part{}, false
	}
	frag, _ := fs.ReadFile(fsys, name+"/part.html")
	var origin *PartOrigin
	if source == "user" {
		if b, err := fs.ReadFile(fsys, name+"/"+originFile); err == nil {
			var o PartOrigin
			if json.Unmarshal(b, &o) == nil {
				origin = &o
			}
		}
	}
	part, err := parsePart(name, raw, string(frag), origin, source)
	if err != nil {
		warnOnce(key+"#"+err.Error(), fmt.Sprintf("the part %q is left out: %v", key, err))
		return Part{}, false
	}
	if st, err := fs.Stat(fsys, name); err == nil && source == "user" {
		part.UpdatedAt = st.ModTime().UTC()
	}
	return part, true
}

// parsePart makes a part of what is kept of it: its manifest, its fragment and, for a part of the user's, its origin. It is what a folder
// and a row of the database are read with, so that both give the same part: the manifest and the fragment are checked as a part is
// checked to enter the catalog, and the error says why a part is left out.
func parsePart(name string, manifest []byte, fragment string, origin *PartOrigin, source string) (Part, error) {
	m, err := parseManifest(manifest, name)
	if err != nil {
		return Part{}, err
	}
	if strings.TrimSpace(fragment) == "" {
		return Part{}, errors.New("part.html is missing or empty")
	}
	if probs := fragmentProblems(fragment); len(probs) > 0 {
		return Part{}, errors.New(probs[0])
	}
	if probs := animatableSlotProblems(m, fragment); len(probs) > 0 {
		return Part{}, errors.New(probs[0])
	}
	part := Part{Manifest: m, Fragment: fragment, Source: source}
	if source == "user" {
		part.Origin = origin
	}
	part.HasFixedCopy = fixedCopy(m, part.Origin)
	return part, nil
}

var reExternalURL = regexp.MustCompile(`(?i)(?:\b(?:src|href|poster)\s*=\s*["']?\s*(?:https?:)?//|url\(\s*["']?\s*(?:https?:)?//|@import\s+(?:url\()?\s*["']?\s*(?:https?:)?//)`)

// fragmentProblems are what can be seen in the text of a fragment: it must load nothing from outside and must not
// take over window.__seek.
func fragmentProblems(frag string) []string {
	var out []string
	if reExternalURL.MatchString(frag) {
		out = append(out, "it loads an external resource; everything must be inline")
	}
	if reSeekAssign.MatchString(frag) {
		out = append(out, "it sets window.__seek, which belongs to the page; define window.__partSeek(t, beat) instead")
	}
	out = append(out, timelineProblems(frag)...)
	return out
}

// animatableSlotProblems refuses a manifest that marks animatable a parameter its fragment fills in as text ({{name}}):
// that text is written once, while the page loads, so the parameter cannot move.
func animatableSlotProblems(m Manifest, frag string) []string {
	var out []string
	for _, match := range reSlot.FindAllStringSubmatch(frag, -1) {
		for _, pr := range m.Params {
			if pr.Name == match[2] && pr.Animatable {
				out = append(out, fmt.Sprintf("the parameter %q is animatable but the fragment fills it in as text ({{%s}}), which is written once while the page loads", pr.Name, pr.Name))
			}
		}
	}
	return out
}

// List returns the valid parts people choose from, by name. A part of the user's replaces a built-in one of the
// same name. The scenes written for a piece are not here (see ListAll, ListScenes).
func (c Catalog) List() []Part { return c.listParts(SceneFilter{Scenes: NoScenes}) }

// ListScenes returns the scene parts, all of them or those written for one piece (piece non-empty).
func (c Catalog) ListScenes(piece string) []Part {
	return c.listParts(SceneFilter{Scenes: OnlyScenes, Piece: piece})
}

// ListAll returns every valid part, the scenes written for a piece included.
func (c Catalog) ListAll() []Part { return c.listParts(SceneFilter{Scenes: AllParts}) }

func (c Catalog) listParts(f SceneFilter) []Part {
	all := map[string]Part{}
	if f.Scenes != OnlyScenes { // the parts that come with the program are never scenes
		for n, p := range c.builtinParts() {
			all[n] = p
		}
	}
	if st := c.userStore(); st != nil {
		for _, p := range st.List(f) {
			all[p.Name] = p
		}
	}
	out := make([]Part, 0, len(all))
	for _, p := range all {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Get returns the part called name: a part of the user's before one that comes with the program. It reads that part only.
func (c Catalog) Get(name string) (Part, bool) {
	if st := c.userStore(); st != nil {
		if p, ok := st.Get(name); ok {
			return p, true
		}
	}
	p, ok := c.builtinParts()[name]
	return p, ok
}

// GetMany returns the parts called names that are in the catalog, by name, reading each once.
func (c Catalog) GetMany(names []string) map[string]Part {
	out := map[string]Part{}
	if st := c.userStore(); st != nil {
		for n, p := range st.GetMany(names) {
			out[n] = p
		}
	}
	builtin := c.builtinParts()
	for _, n := range names {
		if _, ok := out[n]; ok {
			continue
		}
		if p, ok := builtin[n]; ok {
			out[n] = p
		}
	}
	return out
}

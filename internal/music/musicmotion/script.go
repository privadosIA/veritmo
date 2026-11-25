package musicmotion

// script.go: the script of a piece, a list of pieces that cover the song with no gap and no overlap, each with the
// part that draws it, its parameters, its mode, the lines it covers (with the keyword of each) and, optionally,
// a word hit. It is a plain JSON file the user can read and edit; whatever is edited is validated again before it
// is used. See specs/music-motion ("A script is proposed from the song and can be edited").

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"veritmo/internal/music/songmap"
	"veritmo/internal/project/config"
	"veritmo/internal/project/pipeline"
	"veritmo/internal/video/blender"
)

// ScriptVersion is the format of the script file.
const ScriptVersion = 1

// coverTolerance is how far the pieces may be from the ends of the song, and from each other, in seconds.
const coverTolerance = 0.05

// ScriptLine is a verse a piece covers. Line is the number of the line in the song map's words (the Whisper
// segment); when it is set, Text, Start and End are taken from the map. A line with no timed words gives Text, Start
// and End itself (in song time) and is shown whole. Keyword is a word of the line, painted in the accent colour.
type ScriptLine struct {
	Line    *int    `json:"line,omitempty"`
	Text    string  `json:"text,omitempty"`
	Start   float64 `json:"start,omitempty"`
	End     float64 `json:"end,omitempty"`
	Keyword string  `json:"keyword,omitempty"`
}

// ScriptHit is a word shown at full screen, with its time in the song.
type ScriptHit struct {
	Word   string  `json:"word"`
	At     float64 `json:"at"`
	Length float64 `json:"length"`
}

// PartRef names a part of the catalog with its parameters.
type PartRef struct {
	Part   string         `json:"part"`
	Params map[string]any `json:"params,omitempty"`
	// Animate changes parameters over the piece (see animate.go).
	Animate Animate `json:"animate,omitempty"`
}

// PieceBlender makes a piece a 3D scene that Blender draws instead of a page made from a part: Description is the JSON of
// a scene of the contract of the Blender engine (its keys in seconds or in beats of the song's tempo).
type PieceBlender struct {
	Description json.RawMessage `json:"description"`
}

// Piece is one scene of the video.
type Piece struct {
	ID      string  `json:"id"`
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Section string  `json:"section,omitempty"`
	// Part is the part of the catalog the piece is made from; empty for a 3D piece (Blender).
	Part string `json:"part,omitempty"`
	// Blender makes the piece a 3D scene: it has no part and none of the fields of a page (params, animate, with, design,
	// mode, lines, hit, stage).
	Blender *PieceBlender  `json:"blender,omitempty"`
	Params  map[string]any `json:"params,omitempty"`
	// Animate changes parameters of the main part over the piece (see animate.go); the extras have their own.
	Animate Animate `json:"animate,omitempty"`
	// With are extra parts drawn together with the main one, over it, in order (up to MaxExtras): a part that stays
	// (a strip of years) on pieces whose main part changes.
	With []PartRef `json:"with,omitempty"`
	// Design replaces some fields of the song's design for this piece only (another palette for an era); the result is
	// validated as a whole design. Nil: the song's design.
	Design *DesignOverride `json:"design,omitempty"`
	// Mode is "light" or "dark"; empty follows the design (see ResolveMode). Only a person, or an assistant with a reason, sets it.
	Mode  string       `json:"mode,omitempty"`
	Lines []ScriptLine `json:"lines,omitempty"`
	Hit   *ScriptHit   `json:"hit,omitempty"`
	// Stage is the background and the characters of an illustrated piece (see stage.go).
	Stage *Stage `json:"stage,omitempty"`
	// TransitionSeconds is the crossfade into this piece from the one before (0 to MaxTransitionSeconds; 0 is a cut); nil takes
	// the design's. It is ignored on the first piece.
	TransitionSeconds *float64 `json:"transition_seconds,omitempty"`
}

// Script is the file.
type Script struct {
	Version int     `json:"version"`
	Pieces  []Piece `json:"pieces"`
	// Home is the part the chorus comes back to (the one every chorus piece uses); empty when the script names none.
	Home string `json:"home,omitempty"`
	// Layers are lanes of parts drawn over the pieces (see layers.go).
	Layers []Layer `json:"layers,omitempty"`
	// sources is the sources folder of the piece this script belongs to, where its 3D pieces find the models they place
	// (see WithSources); empty when the script has no folder, and then a model is refused.
	sources string
}

// WithSources is the script with the sources folder of its piece set: the descriptions of its 3D pieces are then checked
// against the models and pictures that are there. The folder is not part of what is stored.
func (s Script) WithSources(dir string) Script {
	s.sources = dir
	return s
}

var reID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,23}$`)

// words splits a line into its words as they are compared: without punctuation, lower case.
func lineWords(text string) []string {
	var out []string
	for _, f := range strings.Fields(text) {
		w := strings.ToLower(strings.TrimFunc(f, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }))
		if w != "" {
			out = append(out, w)
		}
	}
	return out
}

func hasWord(text, word string) bool {
	w := strings.ToLower(strings.TrimFunc(strings.TrimSpace(word), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }))
	if w == "" {
		return false
	}
	for _, x := range lineWords(text) {
		if x == w {
			return true
		}
	}
	return false
}

// LineText gives the text of a script line: its own, or the one the map holds for its Line number.
func LineText(l ScriptLine, m songmap.Map) (string, bool) {
	if l.Line == nil {
		return l.Text, strings.TrimSpace(l.Text) != ""
	}
	var parts []string
	for _, w := range m.Words {
		if w.Line == *l.Line {
			parts = append(parts, w.Text)
		}
	}
	return strings.Join(parts, " "), len(parts) > 0
}

func name(p Piece, i int) string {
	if p.ID != "" {
		return fmt.Sprintf("piece %q (#%d)", p.ID, i+1)
	}
	return fmt.Sprintf("piece #%d", i+1)
}

// Validate checks a script against the catalog, the song map and the design, and names the first piece that is
// wrong. It does not look at the level: a part above the level is replaced when the script is built (ApplyLevel).
func (s Script) Validate(cat Catalog, m songmap.Map, d Design) error {
	if s.Version != ScriptVersion {
		return fmt.Errorf("the script version must be %d, got %d", ScriptVersion, s.Version)
	}
	if len(s.Pieces) == 0 {
		return fmt.Errorf("the script has no pieces")
	}
	ids := map[string]bool{}
	for i, p := range s.Pieces {
		n := name(p, i)
		if !reID.MatchString(p.ID) {
			return fmt.Errorf("%s: the id must be lower case letters, digits and dashes, up to 24 characters", n)
		}
		if ids[p.ID] {
			return fmt.Errorf("%s: the id is repeated", n)
		}
		ids[p.ID] = true
		if p.End <= p.Start {
			return fmt.Errorf("%s ends before it starts (%.2f s to %.2f s)", n, p.Start, p.End)
		}
		if p.TransitionSeconds != nil && (*p.TransitionSeconds < 0 || *p.TransitionSeconds > MaxTransitionSeconds) {
			return fmt.Errorf("%s: transition_seconds must be between 0 and %v, got %v", n, MaxTransitionSeconds, *p.TransitionSeconds)
		}
		if i == 0 && math.Abs(p.Start) > coverTolerance {
			return fmt.Errorf("%s: the script must start at 0 s, it starts at %.2f s", n, p.Start)
		}
		if i > 0 {
			prev := s.Pieces[i-1]
			if gap := p.Start - prev.End; gap > coverTolerance {
				return fmt.Errorf("%s: there is a gap of %.2f s after %s", n, gap, name(prev, i-1))
			} else if gap < -coverTolerance {
				return fmt.Errorf("%s overlaps %s by %.2f s", n, name(prev, i-1), -gap)
			}
		}
		if p.Blender != nil {
			if err := p.validateBlender(n, s.blenderEnv(m, d)); err != nil {
				return err
			}
			continue
		}
		part, ok := cat.Get(p.Part)
		if !ok {
			return fmt.Errorf("%s: the part %q is not in the catalog", n, p.Part)
		}
		if _, err := part.ResolveParams(p.Params); err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
		if err := part.checkAnimate(n, p.Animate, p.End-p.Start); err != nil {
			return err
		}
		if p.Design != nil {
			if err := d.With(p.Design).Validate(); err != nil {
				return fmt.Errorf("%s: %w", n, err)
			}
		}
		if len(p.With) > MaxExtras {
			return fmt.Errorf("%s: a piece may carry up to %d extra parts (with), it has %d", n, MaxExtras, len(p.With))
		}
		used := map[string]bool{p.Part: true}
		for k, w := range p.With {
			wp, ok := cat.Get(w.Part)
			if !ok {
				return fmt.Errorf("%s: the extra part %q (with #%d) is not in the catalog", n, w.Part, k+1)
			}
			if used[w.Part] {
				return fmt.Errorf("%s: the part %q is in the piece twice", n, w.Part)
			}
			used[w.Part] = true
			if wp.OwnsVerse() || wp.OwnsHit() {
				return fmt.Errorf("%s: the extra part %q draws the lyric or the word hit itself (verse or hit: own): only the main part of a piece may", n, w.Part)
			}
			if _, err := wp.ResolveParams(w.Params); err != nil {
				return fmt.Errorf("%s: %w", n, err)
			}
			if err := wp.checkAnimate(n, w.Animate, p.End-p.Start); err != nil {
				return err
			}
		}
		if p.Mode != "" && p.Mode != ModeDark && p.Mode != ModeLight {
			return fmt.Errorf("%s: the mode %q is neither %q nor %q", n, p.Mode, ModeDark, ModeLight)
		}
		var texts []string
		for j, l := range p.Lines {
			text, ok := LineText(l, m)
			if !ok {
				return fmt.Errorf("%s: line %d has no text (give a line number that is in the song's words, or its text)", n, j+1)
			}
			if l.Line == nil && l.End <= l.Start {
				return fmt.Errorf("%s: line %d needs a start and an end in seconds", n, j+1)
			}
			if l.Keyword != "" && !hasWord(text, l.Keyword) {
				return fmt.Errorf("%s: the keyword %q is not a word of line %d (%q)", n, l.Keyword, j+1, text)
			}
			texts = append(texts, text)
		}
		if p.Hit != nil {
			h := p.Hit
			if strings.ContainsAny(strings.TrimSpace(h.Word), " \t\n") || strings.TrimSpace(h.Word) == "" {
				return fmt.Errorf("%s: the word hit must be one word", n)
			}
			found := false
			for _, t := range texts {
				if hasWord(t, h.Word) {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("%s: the word hit %q is not a word of the lines of the piece", n, h.Word)
			}
			if h.Length <= 0 || h.At < p.Start-coverTolerance || h.At >= p.End {
				return fmt.Errorf("%s: the word hit must start inside the piece (%.2f s to %.2f s) and last more than 0 s", n, p.Start, p.End)
			}
		}
	}
	last := s.Pieces[len(s.Pieces)-1]
	if m.Duration > 0 && math.Abs(last.End-m.Duration) > coverTolerance {
		return fmt.Errorf("%s ends at %.2f s but the song lasts %.2f s: the script must cover the whole song", name(last, len(s.Pieces)-1), last.End, m.Duration)
	}
	return s.validateLayers(cat)
}

// blenderEnv is what the description of a 3D piece is checked against: the song's tempo and the song's design (its colours and
// the fonts it has in the server's pack), with no font files, the same environment the render writes into the project. With
// the piece's sources folder (WithSources) it also sees the models and pictures there; without it a model is refused.
func (s Script) blenderEnv(m songmap.Map, d Design) blender.Env {
	env := pipeline.BlenderEnv(BlenderConfig(m, d, 0, 0, 0), "")
	if s.sources != "" {
		env.NoFiles, env.NoFontFiles, env.Sources = false, true, s.sources
	}
	return env
}

// BlenderConfig is the settings a 3D piece is written and checked for: the frame and the frame rate when given, the song's tempo
// as defaults.motion.bpm and the song's design as defaults.motion.design.
func BlenderConfig(m songmap.Map, d Design, width, height, fps int) *config.Config {
	cfg := &config.Config{Defaults: config.Defaults{FPS: fps}}
	if width > 0 && height > 0 {
		cfg.Defaults.Resolution = fmt.Sprintf("%dx%d", width, height)
	}
	motion := &config.MotionDefaults{Design: d.ToConfig()}
	if m.BPM > 0 {
		bpm := m.BPM
		motion.BPM = &bpm
	}
	cfg.Defaults.Motion = motion
	return cfg
}

// IsBlender reports whether the piece is a 3D scene.
func (p Piece) IsBlender() bool { return p.Blender != nil }

// validateBlender checks a 3D piece: no part and none of the fields of a page, and a description that passes the checks of a
// hand-written one for a scene as long as the piece.
func (p Piece) validateBlender(n string, env blender.Env) error {
	if p.Part != "" {
		return fmt.Errorf("%s: a piece is made from a part or is a 3D scene (blender), not both", n)
	}
	for field, set := range map[string]bool{
		"params": len(p.Params) > 0, "animate": len(p.Animate) > 0, "with": len(p.With) > 0, "design": p.Design != nil,
		"mode": p.Mode != "", "lines": len(p.Lines) > 0, "hit": p.Hit != nil, "stage": p.Stage != nil,
	} {
		if set {
			return fmt.Errorf("%s: a 3D piece (blender) cannot have %s: it is drawn by Blender and not from a part", n, field)
		}
	}
	if len(bytes.TrimSpace(p.Blender.Description)) == 0 {
		return fmt.Errorf("%s: the 3D scene has no description", n)
	}
	if _, err := blender.Parse(p.Blender.Description, p.End-p.Start, env); err != nil {
		return fmt.Errorf("%s: the 3D scene does not pass the checks: %w", n, err)
	}
	return nil
}

// ValidateStages checks the stage of every piece against the library and the frame, and names the piece.
func (s Script) ValidateStages(lib Library, width, height int) error {
	for i, p := range s.Pieces {
		if p.Stage == nil {
			continue
		}
		if err := p.Stage.Validate(lib, width, height); err != nil {
			return fmt.Errorf("%s: %w", name(p, i), err)
		}
	}
	return nil
}

// ValidateAll is Validate and ValidateStages: every check a script goes through before it is used.
func (s Script) ValidateAll(cat Catalog, lib Library, m songmap.Map, d Design, width, height int) error {
	if err := s.Validate(cat, m, d); err != nil {
		return err
	}
	return s.ValidateStages(lib, width, height)
}

// ScriptPath is where the script of a piece is kept.
func ScriptPath(dir string) string { return filepath.Join(dir, "script.json") }

// LoadScript reads the script of a piece kept in a folder.
func LoadScript(dir string) (Script, bool) { return LoadScriptAt(DirPlace(dir)) }

// LoadScriptAt reads the script of a piece.
func LoadScriptAt(p Place) (Script, bool) {
	raw, ok := p.ReadDoc(DocScript)
	if !ok {
		return Script{}, false
	}
	var s Script
	if json.Unmarshal(raw, &s) != nil {
		return Script{}, false
	}
	return s, true
}

// SaveScript stores the script in a folder as a file the user can read and edit.
func SaveScript(dir string, s Script) error { return SaveScriptAt(DirPlace(dir), s) }

// SaveScriptAt stores the script.
func SaveScriptAt(p Place, s Script) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return p.WriteDoc(DocScript, raw)
}

// CheckSceneParts refuses a script that uses, in a piece, a scene part that was written for another piece: a scene belongs
// to the piece it was written for (it may be used by any of that piece's script pieces).
func (s Script) CheckSceneParts(cat Catalog, pieceID string) error {
	check := func(where, part string) error {
		p, ok := cat.Get(part)
		if ok && p.IsScene() && p.Scene.Piece != pieceID {
			return fmt.Errorf("%s: the scene part %q belongs to piece %s, not to piece %s", where, part, p.Scene.Piece, pieceID)
		}
		return nil
	}
	for i, p := range s.Pieces {
		if p.Blender != nil {
			continue
		}
		if err := check(name(p, i), p.Part); err != nil {
			return err
		}
		for _, w := range p.With {
			if err := check(name(p, i), w.Part); err != nil {
				return err
			}
		}
	}
	for _, l := range s.Layers {
		for _, it := range l.Items {
			if err := check("layer "+l.ID, it.Part); err != nil {
				return err
			}
		}
	}
	return nil
}

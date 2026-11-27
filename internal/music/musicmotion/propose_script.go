package musicmotion

// propose_script.go: the script is proposed by the assistant from the song map, the design, the level and the
// catalog, and checked; one that fails is asked again with the reason, up to Attempts times, and with no assistant
// (or when it fails every time) the script is made by a fixed rule: one piece per section, long sections cut in
// shorter pieces, parts taken in turn from those the level supports.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"

	"veritmo/internal/music/songmap"
)

// MaxPieceSeconds is the longest piece the fixed rule leaves: a longer section is cut in equal pieces.
const MaxPieceSeconds = 6.0

// ScriptInput is what a script is made from.
type ScriptInput struct {
	Map    songmap.Map
	Design Design
	Level  string
	Cat    Catalog
	// Parts and Rhythm come from a style: the fixed rule takes these parts in turn (those the level supports; all the
	// usual ones when the list is empty or none of them is usable) and makes pieces of at most Rhythm seconds
	// (MaxPieceSeconds when it is zero).
	Parts  []string
	Rhythm float64
	// Prompt is the person's direction for the video; NoAssistant says why the writer is nil (see Song).
	Prompt string
	// Device and Hook are the storyline of the plan the piece was made from (empty for none): the pieces serve the device and the
	// first one opens with the hook.
	Device, Hook string
	// Reference is the rhythm of a reference video the piece names; nil leaves the proposal as it was. The fixed rule aims the
	// pieces of each third of the song at its median shot of that third.
	Reference   *ReferenceSheet
	NoAssistant string
	// NoBlocks keeps the writer from being offered blocks and components as layers (the plan's `blocks: off`).
	NoBlocks bool
	// Menu is the blocks the writer may name; nil means "read them from the catalog of blocks" (BlockMenu), unless NoBlocks. A test seam.
	Menu []MenuEntry
	// Video is the video track of the script this proposal replaces: the proposal keeps it as it is, the pieces that sit over it use only parts
	// that leave a video visible, and the assistant is told where it plays. Empty for none, which leaves the proposal as it was.
	Video []VideoItem
	// Sources is the sources folder of the piece, where its videos are kept: the script is checked against it (see Script.WithSources).
	// Only read when there is a Video to keep.
	Sources string
}

// menu is the blocks the writer is offered.
func (in ScriptInput) menu() []MenuEntry {
	if in.NoBlocks {
		return nil
	}
	if in.Menu != nil {
		return in.Menu
	}
	return BlockMenu()
}

// Replacement says that a part was replaced by a simpler one because the level does not support it.
type Replacement struct {
	Piece string `json:"piece"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// Proposal is a script with where it came from and what was replaced.
type Proposal struct {
	Script Script `json:"script"`
	// Source is "assistant" or "fallback".
	Source       string        `json:"source"`
	Replacements []Replacement `json:"replacements,omitempty"`
	// Snapped counts the cuts and hits moved to the beat.
	Snapped int `json:"snapped"`
	// FallbackReason says why the script is a fallback; empty when the assistant wrote it.
	FallbackReason string `json:"fallback_reason,omitempty"`
	// Warnings are what was left out of the writer's answer (block layers that did not pass), without refusing the script.
	Warnings []string `json:"warnings,omitempty"`
	// FallbackPieces are the pieces whose part the fixed rule chose; empty when the assistant wrote the script.
	FallbackPieces []string `json:"fallback_pieces,omitempty"`
}

// FallbackPieces lists the pieces of a script that use a part chosen by the fixed rule, not a scene written for them: the
// pieces whose part is not a scene (a part that is no longer in the catalog counts as chosen by the rule).
func FallbackPieces(s Script, cat Catalog) []string {
	return FallbackPiecesWith(s, cat.GetMany(s.PartNames()))
}

// FallbackPiecesWith is FallbackPieces for a caller that has already read the parts of the script (see EstimateRenderWith).
func FallbackPiecesWith(s Script, parts map[string]Part) []string {
	var out []string
	for _, p := range s.Pieces {
		if p.Blender != nil {
			continue // a 3D scene is not a part the fixed rule chose
		}
		if part, ok := parts[p.Part]; ok && part.Scene != nil {
			continue
		}
		out = append(out, p.ID)
	}
	return out
}

// partsFor lists the parts of the catalog that support a level, in the order the fixed rule takes them: the parts
// made for the level come first (an advanced song starts with the advanced parts), then the simpler ones, each
// group by name. Manual parts are left out, and so are the ones that draw example text of their own (fixed_copy): the
// rule has no way to make them say the song's words.
func partsFor(cat Catalog, level string) []Part {
	var out []Part
	for _, p := range levelParts(cat, level) {
		// a block imported from HyperFrames is a background, an overlay or a transition made to go over or under another part:
		// alone it is an empty or unreadable piece, so the fixed rule leaves it to the assistant and to the layers
		if !p.HasFixedCopy && (p.Origin == nil || p.Origin.Source != "hyperframes") {
			out = append(out, p)
		}
	}
	return out
}

// levelParts lists the parts a level supports in the same order, fixed copy or not: the assistant is told all of them, because it
// can write the part's text; only the fixed rule leaves out the ones that cannot say the song's words.
func levelParts(cat Catalog, level string) []Part {
	var out []Part
	for _, p := range cat.List() {
		if p.Supports(level) && !p.Manual {
			out = append(out, p)
		}
	}
	minRank := func(p Part) int {
		m := len(Levels)
		for _, l := range p.Levels {
			if r := LevelRank(l); r < m {
				m = r
			}
		}
		return m
	}
	sort.SliceStable(out, func(i, j int) bool { return minRank(out[i]) > minRank(out[j]) })
	return out
}

// ApplyLevel replaces the part of every piece the level does not support with one it supports, taken in turn (so
// the replacements do not repeat one part), with its default parameters, and says what it replaced. A level that
// has no part at all is an error.
func ApplyLevel(s *Script, cat Catalog, level string) ([]Replacement, error) {
	if LevelRank(level) < 0 {
		return nil, fmt.Errorf("the level %q does not exist (%s)", level, strings.Join(Levels, ", "))
	}
	cands := partsFor(cat, level)
	var out []Replacement
	for i := range s.Pieces {
		p := &s.Pieces[i]
		part, ok := cat.Get(p.Part)
		if !ok || part.Supports(level) {
			continue
		}
		if len(cands) == 0 {
			return nil, fmt.Errorf("no part of the catalog supports the level %q", level)
		}
		to := cands[i%len(cands)]
		out = append(out, Replacement{Piece: p.ID, From: p.Part, To: to.Name})
		p.Part, p.Params = to.Name, nil
	}
	return out, nil
}

// keywordOf is the word a line is painted with: the longest one of at least four letters (the first of those),
// or none.
func keywordOf(text string) string {
	best := ""
	for _, w := range lineWords(text) {
		if len([]rune(w)) >= 4 && len([]rune(w)) > len([]rune(best)) {
			best = w
		}
	}
	return best
}

// linesOf lists the lines of the map (Whisper segments) whose first word starts inside [start, end), as script
// lines: a line belongs to the piece where it begins.
func linesOf(m songmap.Map, start, end float64) []ScriptLine {
	seen := map[int]bool{}
	var out []ScriptLine
	for _, w := range m.Words {
		if seen[w.Line] {
			continue
		}
		seen[w.Line] = true // the first word of a line is the first one met
		if w.Start < start-1e-9 || w.Start >= end {
			continue
		}
		n := w.Line
		l := ScriptLine{Line: &n}
		if text, ok := LineText(l, m); ok {
			l.Keyword = keywordOf(text)
		}
		out = append(out, l)
	}
	return out
}

// hitFor is the word hit of a chorus-like piece: the first word of the piece's first line that is a keyword, at the
// time it is sung. nil when the piece has no timed words.
func hitFor(m songmap.Map, p Piece) *ScriptHit {
	for _, l := range p.Lines {
		if l.Keyword == "" || l.Line == nil {
			continue
		}
		for _, w := range m.Words {
			if w.Line == *l.Line && hasWord(w.Text, l.Keyword) && w.Start >= p.Start && w.Start < p.End {
				length := math.Min(0.9, math.Max(0.4, w.End-w.Start))
				return &ScriptHit{Word: strings.ToUpper(strings.TrimFunc(w.Text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })), At: w.Start, Length: length}
			}
		}
	}
	return nil
}

// pieceBounds are the times a section is cut at, the start and the end of the section included. Without a reference the
// section is cut in equal pieces of at most longest seconds; with one that says something of the third a piece starts in, a
// piece lasts about that third's median shot (between two beats and eight beats), and a cut goes to the start of the nearest
// sung line so that no line is split (the cuts are snapped to the beat afterwards, as always).
func pieceBounds(in ScriptInput, sec songmap.Section, longest float64) []float64 {
	span := sec.End - sec.Start
	beat := 0.5
	if in.Map.BPM >= 20 {
		beat = 60 / in.Map.BPM
	}
	if in.Reference == nil || in.Reference.target(thirdOf(sec.Start, in.Map.Duration), beat)+in.Reference.target(thirdOf(sec.End, in.Map.Duration), beat) == 0 {
		count := int(math.Max(1, math.Ceil(span/longest-1e-9)))
		bounds := make([]float64, count+1)
		for k := 0; k <= count; k++ {
			bounds[k] = sec.Start + span*float64(k)/float64(count)
		}
		bounds[count] = sec.End
		return bounds
	}
	bounds := []float64{sec.Start}
	cursor := sec.Start
	for {
		target := in.Reference.target(thirdOf(cursor, in.Map.Duration), beat)
		if target == 0 {
			target = math.Min(longest, maxReferenceBeats*beat)
		}
		if sec.End-cursor <= target*1.5 {
			break
		}
		// No piece is shorter than minReferenceBeats beats, the last one included: a cut stays inside [lo, hi].
		lo, hi := cursor+minReferenceBeats*beat, sec.End-minReferenceBeats*beat
		if hi < lo {
			break
		}
		next := math.Min(math.Max(nearestLineStart(in.Map, cursor+target, target/2, lo, hi), lo), hi)
		bounds = append(bounds, next)
		cursor = next
	}
	return append(bounds, sec.End)
}

// nearestLineStart is the start of the sung line nearest to at, within radius of it and strictly between lo and hi; at itself
// when there is none.
func nearestLineStart(m songmap.Map, at, radius, lo, hi float64) float64 {
	best, bestD := at, math.Inf(1)
	seen := map[int]bool{}
	for _, w := range m.Words {
		if seen[w.Line] {
			continue
		}
		seen[w.Line] = true
		if w.Start <= lo || w.Start >= hi {
			continue
		}
		if d := math.Abs(w.Start - at); d <= radius && d < bestD {
			best, bestD = w.Start, d
		}
	}
	return best
}

// FixedScript makes a script with no assistant: one piece per section of the song (cut in equal pieces when it
// is longer than MaxPieceSeconds), the part chosen in turn among those the level supports, no mode (the pieces follow the design's),
// its lines with their keywords, and a word hit for the chorus. The cuts are snapped to the beat.
func FixedScript(in ScriptInput) (Script, error) {
	cands := partsFor(in.Cat, in.Level)
	if len(in.Parts) > 0 {
		var chosen []Part
		for _, n := range in.Parts {
			if p, ok := in.Cat.Get(n); ok && p.Supports(in.Level) {
				chosen = append(chosen, p)
			}
		}
		if len(chosen) > 0 {
			cands = chosen
		}
	}
	longest := MaxPieceSeconds
	if in.Rhythm > 0 {
		longest = in.Rhythm
	}
	// Without a style's own rhythm the length of a piece follows the energy of its section (quiet sections longer, loud ones shorter,
	// a breakdown held), and every chorus piece uses one home part.
	energies := SectionEnergies(in.Map)
	if len(cands) == 0 {
		return Script{}, fmt.Errorf("no part of the catalog supports the level %q", in.Level)
	}
	if len(in.Map.Sections) == 0 {
		return Script{}, errors.New("the song map has no sections")
	}
	s := Script{Version: ScriptVersion}
	n := 0
	for si, sec := range in.Map.Sections {
		sectionLongest := longest
		if in.Rhythm == 0 {
			sectionLongest = energyLongest(in.Map, energies, si, longest)
		}
		bounds := pieceBounds(in, sec, sectionLongest)
		for k := 0; k+1 < len(bounds); k++ {
			n++
			prev := ""
			if len(s.Pieces) > 0 {
				prev = s.Pieces[len(s.Pieces)-1].Part
			}
			pieceCands := cands
			if overVideoAt(in.Video, bounds[k], bounds[k+1]) {
				pieceCands = onlyOverVideo(cands)
			}
			p := Piece{ID: fmt.Sprintf("p%d", n), Start: bounds[k], End: bounds[k+1], Section: sec.Tag,
				Part: nextPart(pieceCands, n-1, prev)}
			if IsChorus(sec.Tag) {
				if s.Home == "" {
					s.Home = p.Part
				}
				// the chorus returns to its home part on the first piece and on every third one; the pieces between take
				// the next part in turn, so a long chorus is not one part held for a minute
				if k%3 == 0 && prev != s.Home && homeFits(in.Cat, s.Home, pieceCands, len(pieceCands) != len(cands)) {
					p.Part = s.Home
				} else {
					p.Part = otherPart(pieceCands, n-1, s.Home, prev)
				}
			}
			p.Lines = linesOf(in.Map, p.Start, p.End)
			if IsChorus(sec.Tag) {
				p.Hit = hitFor(in.Map, p)
			}
			s.Pieces = append(s.Pieces, p)
		}
	}
	// A section can start where the song ends (or a hair before): its piece would last nothing, so the piece before it
	// is the one that reaches the end of the song.
	for len(s.Pieces) > 1 && in.Map.Duration-s.Pieces[len(s.Pieces)-1].Start < minPieceSeconds {
		s.Pieces = s.Pieces[:len(s.Pieces)-1]
	}
	s.Pieces[0].Start = 0
	s.Pieces[len(s.Pieces)-1].End = in.Map.Duration
	return s, nil
}

// homeFits says whether the home part of the chorus can be used on a piece whose candidates were narrowed to the parts that leave a video
// visible (narrowed is true): it can when it is one of them. With no narrowing it always can.
func homeFits(cat Catalog, home string, cands []Part, narrowed bool) bool {
	if !narrowed {
		return true
	}
	for _, p := range cands {
		if p.Name == home {
			return true
		}
	}
	return false
}

// nextPart takes the part at position i of the candidates, in turn, and the next one when it is the part of the piece
// before, so that two consecutive pieces differ whenever there is more than one candidate.
func nextPart(cands []Part, i int, prev string) string {
	for j := 0; j < len(cands); j++ {
		if name := cands[(i+j)%len(cands)].Name; name != prev {
			return name
		}
	}
	return cands[i%len(cands)].Name
}

const scriptSystem = `You plan a music video made of animated parts, from a song's sections, its sung lines and a catalog of parts.
Answer with ONE JSON object and nothing else: {"pieces":[ ... ]}. Each piece has:
  "section": the tag of the song section it covers,
  "start", "end": seconds; the pieces follow each other with no gap and no overlap and cover the whole song (0 to %s),
  "part": a name from the catalog below,
  "params": an object with parameters of that part (optional, only the ones you want to change),
  "lines": the sung lines of the piece, each {"line": <the line number given below>, "keyword": "<one word of that line>"},
  "hit": optional, for a chorus: {"word": "<one word of those lines>", "at": <seconds>, "length": <seconds>}.
Use a different part in consecutive pieces. Pieces last between 3 and 15 seconds. Use only the parts of the catalog.
Do not write a "mode": the light or dark mode of each piece comes from the design.
Craft rules (follow them):
  - Cut density follows the energy given for each section: pieces of 8 to 15 seconds in the quiet sections, one piece per two to four bars (a bar lasts 240 / bpm seconds) in the loud ones, one single held piece on a breakdown (the quietest section after a louder one).
  - The chorus comes back to ONE home part: name it at the top of the answer as "home": "<part>", use it for every chorus piece and make it stronger on each return (other parameter values, or one more part in "with").
  - The last piece uses the part of the first one, or a mirror of it, so the ending rhymes with the opening.
  - A piece longer than 8 seconds carries a change inside it: an "animate" on a parameter, a "with" part that enters later ("start"), or an item of a layer.
  - At most a third of the seams between pieces are hard cuts: give "transition_seconds" between 0.3 and 0.6 to the others.
The visual idea of the video is: %s`

func scriptUser(in ScriptInput, problem string) string {
	var b strings.Builder
	b.WriteString(storylineBlock("", in.Device, in.Hook))
	b.WriteString(in.Reference.scriptBlock())
	b.WriteString("Catalog (parts usable at the level " + in.Level + "):\n")
	for _, p := range levelParts(in.Cat, in.Level) {
		ps, _ := json.Marshal(p.Params)
		mark := ""
		if len(in.Video) > 0 && p.OverVideo {
			mark = " [over_video]"
		}
		fmt.Fprintf(&b, "- %s%s: %s. Parameters: %s\n", p.Name, mark, p.Description, ps)
	}
	b.WriteString(videoBlock(in.Video))
	b.WriteString(menuBlock(in.menu()))
	b.WriteString("\nSections:\n")
	energies := SectionEnergies(in.Map)
	for i, s := range in.Map.Sections {
		fmt.Fprintf(&b, "- %s: %.2f s to %.2f s", s.Tag, s.Start, s.End)
		if i < len(energies) {
			fmt.Fprintf(&b, ", loudness %.2f, energy %d of %d (1 is the loudest)", energies[i].Loudness, energies[i].Rank, len(energies))
			if IsBreakdown(energies, i) {
				b.WriteString(", a breakdown")
			}
		}
		b.WriteString("\n")
	}
	if in.Map.BPM >= 20 {
		fmt.Fprintf(&b, "Tempo: %.0f bpm, a bar lasts %.2f s\n", in.Map.BPM, BarSeconds(in.Map))
	}
	b.WriteString("\nSung lines (number, start, text):\n")
	lastLine, text, start := -1, "", 0.0
	flush := func() {
		if lastLine >= 0 {
			fmt.Fprintf(&b, "- %d, %.2f s: %s\n", lastLine, start, text)
		}
	}
	for _, w := range in.Map.Words {
		if w.Line != lastLine {
			flush()
			lastLine, text, start = w.Line, w.Text, w.Start
			continue
		}
		text += " " + w.Text
	}
	flush()
	if problem != "" {
		fmt.Fprintf(&b, "\nYour previous answer was refused: %s\nAnswer again with the JSON object fixed.\n", problem)
	}
	return b.String()
}

// parseScript reads the assistant's answer into a script: it numbers the pieces and leaves validation to the caller.
// A mode the assistant wrote is kept; one it left out stays empty, so the piece follows the design.
func parseScript(answer string, in ScriptInput) (Script, error) {
	s, _, err := parseScriptWarn(answer, in)
	return s, err
}

// parseScriptWarn is parseScript that also says what it left out of the block layers.
func parseScriptWarn(answer string, in ScriptInput) (Script, []string, error) {
	raw := extractJSON(answer)
	if raw == "" {
		return Script{}, nil, errors.New("the answer holds no JSON object")
	}
	var got struct {
		Home   string  `json:"home"`
		Pieces []Piece `json:"pieces"`
		Layers []Layer `json:"layers"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		return Script{}, nil, fmt.Errorf("the answer is not a valid JSON object: %w", err)
	}
	s := Script{Version: ScriptVersion, Pieces: got.Pieces}
	var warns []string
	s.Layers, warns = blockLayersFrom(got.Layers, in.menu(), in.Map.Duration)
	if home := strings.TrimSpace(got.Home); home != "" {
		if _, ok := in.Cat.Get(home); ok {
			s.Home = home
		}
	}
	for i := range s.Pieces {
		p := &s.Pieces[i]
		p.ID = fmt.Sprintf("p%d", i+1)
	}
	if n := len(s.Pieces); n > 0 {
		s.Pieces[0].Start = math.Min(s.Pieces[0].Start, 0.0)
		if math.Abs(s.Pieces[n-1].End-in.Map.Duration) <= 1.5 {
			s.Pieces[n-1].End = in.Map.Duration
		}
	}
	return s, warns, nil
}

// ProposeScript asks the assistant for a script and checks it (validation, the level, the beat); a refused answer
// is asked again with the reason, up to Attempts times, and with no assistant, or when none is valid, the fixed
// rule makes it. Parts the level does not support are replaced and reported.
func ProposeScript(ctx context.Context, w Writer, in ScriptInput) (Proposal, error) {
	finish := func(s Script, source, why string, warns []string) (Proposal, error) {
		rep, err := ApplyLevel(&s, in.Cat, in.Level)
		if err != nil {
			return Proposal{}, err
		}
		b, h := SnapScript(&s, in.Map)
		addFrameLayers(&s, in)
		warns = append(warns, KeepTrack(&s, in.Video, in.Cat, in.Level)...)
		warns = append(warns, PlaceVerse(&s, in.Cat, in.Design)...)
		s = s.WithSources(in.Sources)
		if err := s.Validate(in.Cat, in.Map, in.Design); err != nil {
			// the block layers are an extra: a script that only fails with them is kept without them
			if n := dropBlockLayers(&s); n > 0 {
				warns = append(warns, fmt.Sprintf("the block layers were dropped: %v", err))
				err = s.Validate(in.Cat, in.Map, in.Design)
			}
			if err != nil {
				return Proposal{}, err
			}
		}
		return Proposal{Script: s, Source: source, FallbackReason: why, Replacements: rep, Snapped: b + h, Warnings: warns}, nil
	}
	why := in.NoAssistant
	if w == nil && why == "" {
		why = "there is no assistant available"
	}
	if w != nil {
		problem := ""
		for i := 0; i < Attempts && ctx.Err() == nil; i++ {
			answer, err := w.Write(ctx, fmt.Sprintf(scriptSystem, fmt.Sprintf("%.2f s", in.Map.Duration), in.Design.Concept+scriptPromptTail(in.Prompt)), scriptUser(in, problem), 4096)
			if err != nil {
				why = "the assistant failed: " + err.Error()
				break
			}
			s, warns, err := parseScriptWarn(answer, in)
			if err == nil {
				if p, perr := finish(s, "assistant", "", warns); perr == nil {
					return p, nil
				} else {
					err = perr
				}
			}
			why = "the last answer of the assistant was refused: " + err.Error()
			problem = err.Error() + scriptHint(in.Cat, err)
		}
		if ctx.Err() != nil {
			why = "the proposal was cancelled"
		}
	}
	s, err := FixedScript(in)
	if err != nil {
		return Proposal{}, err
	}
	prop, err := finish(s, "fallback", why+"."+fixedRuleNote(in.Prompt), nil)
	if err == nil {
		prop.FallbackPieces = FallbackPieces(prop.Script, in.Cat)
	}
	return prop, err
}

// scriptPromptTail is what follows the visual idea in the system prompt: nothing without a prompt.
func scriptPromptTail(prompt string) string {
	if b := strings.TrimSpace(promptBlock(prompt)); b != "" {
		return "\n" + b
	}
	return ""
}

// addFrameLayers puts the persistent frame (the HUD and the grain) over the whole song when the design's theme has HUD texts: a
// layer each, from 0 to the end of the script. Nothing is added without HUD texts, when the catalog lacks the parts or when
// the script has no room for two more layers.
func addFrameLayers(s *Script, in ScriptInput) {
	t := in.Design.Theme
	if t == nil || t.HUD == nil || *t.HUD == (HUD{}) || len(s.Pieces) == 0 || len(s.Layers)+2 > MaxLayers {
		return
	}
	for _, name := range []string{"hud-chrome", "grain"} {
		if _, ok := in.Cat.Get(name); !ok {
			return
		}
	}
	end := s.Pieces[len(s.Pieces)-1].End
	for _, l := range s.Layers {
		if l.ID == "frame-hud" || l.ID == "frame-grain" {
			return
		}
	}
	s.Layers = append(s.Layers,
		Layer{ID: "frame-grain", Items: []LayerItem{{Part: "grain", Start: 0, End: end}}},
		Layer{ID: "frame-hud", Items: []LayerItem{{Part: "hud-chrome", Start: 0, End: end}}})
}

// otherPart takes, in turn, a part that is neither the home part nor the one of the piece before; when the candidates leave no
// such part it takes one that is not the piece before, and then the next in turn.
func otherPart(cands []Part, i int, home, prev string) string {
	for j := 0; j < len(cands); j++ {
		if name := cands[(i+j)%len(cands)].Name; name != home && name != prev {
			return name
		}
	}
	return nextPart(cands, i, prev)
}

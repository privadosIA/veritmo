package musicmotion

// scene_draft.go: the scenes the assistant writes for the verses of a piece (spec motion-scene-parts). The server builds the
// writer's whole input from the saved piece (the lines with their words and times, the section, the design, the parts around)
// so a client sends ids and never the lyric.

import (
	"embed"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

//go:embed sceneexample
var sceneExampleFS embed.FS

// MaxSceneBrief is the longest idea a scene can be asked for with.
const MaxSceneBrief = 500

// ErrScenePieceNotFound is a script piece that is not in the piece's script.
var ErrScenePieceNotFound = errors.New("script piece not found")

// SceneDraftInput is what the scene writer is given.
type SceneDraftInput struct {
	Ref   SceneRef
	Brief string
	// Scene is the scene of the script piece as the page is made of it: its lines with words and times in the piece's own
	// seconds, the hit, the design, the mode and the duration. Its Part and Params are empty (the part is what is written).
	Scene Scene
	// Section is the section tag of the script piece and SectionIndex how many pieces of that section came before it.
	Section      string
	SectionIndex int
	// Index and Total place the script piece in the script.
	Index, Total int
	// Start and End are its place in the song, in seconds.
	Start, End float64
	// Neighbours describe the parts of the pieces before and after it (up to two each side), so that a metaphor is not repeated.
	Neighbours []NeighbourScene
	// Used are the one-sentence descriptions of every scene of the piece already approved (the one being written for this script
	// piece left out, which it replaces), so that no metaphor is repeated anywhere in the video, not only next door.
	Used []string
	// Title is the title of the song: a scene may say it.
	Title string
	// World and Device are the storyline of the video, every scene of which belongs to them (World empty: the writer chooses one and
	// states it in its manifest). Hook is what the first two seconds show, given only for the first script piece.
	World, Device, Hook string
	// Archetype is the kind of shot the reference video the piece names had at the same relative place, as one line (see
	// ReferenceShots); empty for none. A cue for the kind of shot, never something to copy.
	Archetype string
}

// NeighbourScene is the part of a script piece next to the one being written, with what it draws.
type NeighbourScene struct {
	// Index is the number of its script piece (from 1).
	Index int
	Part  string
	// Description is the one sentence of its manifest; empty when the part is not in the catalog.
	Description string
}

// sentence is a description as one line of at most n bytes cut at a rune boundary.
func sentence(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// BuildSceneInput reads what the writer needs from a project spec (the map, the design, the script and the catalog of the piece).
func BuildSceneInput(spec ProjectSpec, ref SceneRef, brief string) (SceneDraftInput, error) {
	idx := -1
	for i, p := range spec.Script.Pieces {
		if p.ID == ref.ScriptPiece {
			idx = i
		}
	}
	if idx < 0 {
		return SceneDraftInput{}, fmt.Errorf("%w: %q", ErrScenePieceNotFound, ref.ScriptPiece)
	}
	p := spec.Script.Pieces[idx]
	sc, _, err := sceneFor(p, spec)
	if err != nil {
		return SceneDraftInput{}, err
	}
	sc.Part, sc.Params, sc.Extras, sc.Layers, sc.Backdrop, sc.Actors = Part{}, nil, nil, nil, "", nil
	in := SceneDraftInput{Ref: ref, Brief: strings.TrimSpace(brief), Scene: sc, Section: p.Section, Index: idx + 1, Total: len(spec.Script.Pieces), Start: p.Start, End: p.End, Title: spec.Title,
		World: spec.World, Device: spec.Device}
	if idx == 0 {
		in.Hook = spec.Hook
	}
	for i := 0; i < idx; i++ {
		if spec.Script.Pieces[i].Section == p.Section {
			in.SectionIndex++
		}
	}
	in.Archetype = spec.Reference.archetypeAt(p.Start, p.End, spec.Map.Duration)
	for i := max(0, idx-2); i <= min(len(spec.Script.Pieces)-1, idx+2); i++ {
		if i == idx {
			continue
		}
		q := spec.Script.Pieces[i]
		n := NeighbourScene{Index: i + 1, Part: q.Part}
		if q.Blender != nil {
			n.Part, n.Description = "3D scene", "a 3D scene drawn by Blender"
		} else if part, ok := spec.Cat.Get(q.Part); ok {
			n.Description = sentence(part.Description, 110)
		}
		in.Neighbours = append(in.Neighbours, n)
	}
	// Every scene of the piece already approved, in the order of the script.
	order := map[string]int{}
	for i, q := range spec.Script.Pieces {
		order[q.ID] = i
	}
	scenes := spec.Cat.ListScenes(ref.Piece)
	sort.SliceStable(scenes, func(a, b int) bool { return order[scenes[a].Scene.ScriptPiece] < order[scenes[b].Scene.ScriptPiece] })
	for _, s := range scenes {
		if s.Scene.ScriptPiece == ref.ScriptPiece {
			continue
		}
		if d := sentence(s.Description, 140); d != "" {
			in.Used = append(in.Used, d)
		}
	}
	return in, nil
}

// SceneWith is the scene the draft is checked and previewed in: the real piece, with the part in place.
func (in SceneDraftInput) SceneWith(p Part) (Scene, error) {
	params, err := p.ResolveParams(nil)
	if err != nil {
		return Scene{}, err
	}
	sc := in.Scene
	sc.Part, sc.Params = p, params
	return sc, nil
}

var sceneSystem = `You write ONE scene for a music video: an animated page for ONE verse of a song. A scene is a part with two files, which you answer as two fenced blocks and nothing else: first a ` + "```json" + ` block with manifest.json, then a ` + "```html" + ` block with part.html.

manifest.json: {"name": "scene-1" (the program renames it), "description": "the picture and how it moves, one sentence", "version": 1, "cost_ms": 80, "levels": ["simple","medium","advanced"], "verse": "own", "params": []}. Parameters are allowed ({"name": "label", "type": "string", "default": "x", "max_length": 20}; types string, int, number, bool, enum; every parameter has a default) but a scene usually has none: the lyric is its input. Keep "verse": "own". When the request says no world was chosen for the video, add "world": "<where the video happens and what it looks like, one line>" to the manifest: the scenes after yours keep it.

part.html is a FRAGMENT (no <html>, no <body>): HTML, CSS in a <style> and one <script>. Rules:
- It must MOVE: define window.__partSeek = function (t, beat) {...}, called before each frame with the time in seconds from the start of this scene and the beat clock beat = {number, position, section, sectionIndex, bpm, time}: number is the index of the last beat (-1 before the first), position how far inside it the frame is (0 up to 1). The beat also carries beat.word and beat.line: the written word being sung ({text, index, line, start, end, position}, position from 0 at its start to 1 at its end, in the seconds of this scene) and its line ({index, text, start, end, position}), each null when nothing is sung (a break between lines); use them to move on the word itself. Draw ONLY from t and beat, so the frames land on the music. No Date, no performance.now, no requestAnimationFrame, no setTimeout, no Math.random (use a small seeded generator if you need chance). No CSS transitions or CSS animations: set every style directly from t and beat inside __partSeek.
- YOU draw the lyric. The page does NOT draw it for you: read window.__scene.lines (each {text, start, end, words: [{t, s, e}], keyword}, all in the seconds of this scene) and show each word at its time s (never before), on the line whose start has come, with the keyword (the word at index "keyword") in var(--accent). Build the elements while the script loads and show, hide and move them inside __partSeek. window.__scene.hit, when there is one, is {word, at, length}: a word to show at full screen from at for length seconds.
- You may use the whole frame (1280x720; use vh and vw). Keep every text fully inside it with a margin.
- Colours and fonts come from the design's CSS variables only. Every font-family names var(--display) or var(--body); every colour is var(--bg), var(--fg), var(--accent) (var(--dark) and var(--light) are its two tones), a color-mix() or a relative colour of those, transparent or currentColor. A colour of your own (#fff, rgb(), a name such as red) or a font of your own is refused, in CSS and in SVG (fill, stroke).
- The only text of the scene is the verse, its keyword and the concept of the video: no captions, no section names, no timestamps, no counters, no route numbers; the HUD part draws those, not you. Sizes in vh (of the frame's height): the words of the verse at least 4 vh, the word that matters at least 12 vh, nothing under 2 vh. The picture, what is not text, covers at least a quarter of the frame (a few small dots do not): a scene with one tiny shape and a caption is refused.
- The request lists what the pieces around it and the scenes already written draw: do not repeat any of those metaphors. The metaphor comes from the world and the device the request gives (or the world you choose), so that the scenes of the video belong to one film.
- Everything is inline. NEVER load anything from outside (no http(s) URLs, no external images, fonts, scripts or @import) and never write window.__seek.
- It must change over every beat (a pulse, a step, a flip on beat.number).
- When the request carries a cue from a reference video, it names the kind of shot that video had at this moment (what fills the screen, how the text enters, how long it holds): take it as a cue, never as a picture to copy, and use nothing else of that video.
` + timelineInstruction("scene") + `
- Craft (measured on shipped films; follow the numbers):
` + SceneCraft + `
- Follow this rubric:
` + RubricRules()

// SceneCraft is the motion craft the scene writer is given, with its numbers (the Motion and Captions rules of docs/MUSIC_MODE_MOTION.md
// § Craft, distilled from the HyperFrames skill references, Apache-2.0: see THIRD_PARTY.md). The reviewer marks what the last line lists.
const SceneCraft = `  - A title that has landed is dead still: the motion moves to the next element, the camera or the world behind it.
  - Vary the entrance of the text within the video and never use the same one twice in a row: per word, blur 6 px to 0 and opacity 0 to 1 in 0.13 s, the words 0.067 s apart; per word from light grey to ink, 0.07 to 0.1 s apart; the whole line blurring in from 8 px in 0.13 to 0.2 s; per letter for a name, 0.1 s apart, opacity 0.3 to 1; one word rolling through options, about 0.1 s a roll.
  - A line to be read holds 0.5 s plus 0.3 s for each word (a line of one or two words 0.4 s); the last line of the video at least 2 s. Then it leaves.
  - An exit is 40 to 50 % shorter than the entrance and goes with a blur-out of 0.13 to 0.25 s, never slide, stop and then leave.
  - The next element starts when the previous one is 30 to 50 % done; letters a frame apart, words 60 to 120 ms apart.
  - Ease by role: an arrival lands on a long tail (expo.out, power4.out), a camera move or a colour flood eases both ways (power2.inOut), an exit accelerates (power3.in). The same power2.out on everything is the flat default; nothing is linear except a steady drift.
  - A move that lands on a hit starts 40 to 190 ms early; the hit itself is set with no easing; a reaction starts 0 to 45 ms after it.
  - The word that matters fills 60 to 90 % of the width; the quiet lines are 2.5 to 4 % of the frame's height; one word in the accent.
  - A long verse line is shown in groups of words, one group at a time, each opening 0.2 s before its first word and gone 0.12 s after its last: 2 to 3 words in a loud section, 3 to 5 in a normal one, 4 to 6 in a calm one, the keyword always inside a group.
  - The reviewer marks these as looks that read as generated: a breathing, floating or pulsing loop as the motion of a title; a title that keeps sliding or scaling after it lands; a slow creeping scale; bounce on text; every element fading and sliding the same way; a waveform or spectrum bar as the main picture; the same treatment for more than 6 seconds.`

// SceneSystem is the instruction of the assistant that writes a scene.
func SceneSystem() string { return sceneSystem }

// sceneExample is the scene of the program shown to the model as the technique expected (never to copy).
func sceneExample() string {
	manifest, err1 := sceneExampleFS.ReadFile("sceneexample/manifest.json")
	fragment, err2 := sceneExampleFS.ReadFile("sceneexample/part.html")
	if err1 != nil || err2 != nil {
		return ""
	}
	return "EXAMPLE of a scene that follows all the rules (take the technique, not its picture):\n```json\n" + strings.TrimSpace(string(manifest)) +
		"\n```\n```html\n" + strings.TrimSpace(string(fragment)) + "\n```\n"
}

// SceneExample is the scene part kept for the writer and for tests; it is not in any catalog.
func SceneExample() (manifest, fragment string) {
	m, _ := sceneExampleFS.ReadFile("sceneexample/manifest.json")
	f, _ := sceneExampleFS.ReadFile("sceneexample/part.html")
	return string(m), string(f)
}

// SceneUser is the request: the verse with its words and times, its place in the song, the design, what is around it, the
// idea asked for, an example and, in a later attempt, what was wrong with the last answer.
func SceneUser(in SceneDraftInput, cat Catalog, problem string) string {
	var b strings.Builder
	sc := in.Scene
	fmt.Fprintf(&b, "Scene for script piece %d of %d (%s): from %.2f s to %.2f s of the song, so %.2f seconds long. Section: %q (number %d of that section). Mode: %s.\n",
		in.Index, in.Total, in.Ref.ScriptPiece, in.Start, in.End, sc.Duration, in.Section, in.SectionIndex+1, sc.Mode)
	d := sc.Design
	bg, fg, accent := d.Colours(sc.Mode)
	fmt.Fprintf(&b, "Design: background %s, text %s, accent %s; display font %q, body font %q; concept of the video: %s.\n", bg, fg, accent, d.Display, d.Body, d.Concept)
	if len(sc.Lines) == 0 {
		b.WriteString("\nThe verse: nothing is sung in this piece (an instrumental part): draw the picture alone.\n")
	} else {
		b.WriteString("\nThe verse (times in seconds from the start of this scene; draw each word at its time):\n")
		for i, l := range sc.Lines {
			fmt.Fprintf(&b, "line %d: %q from %.2f to %.2f", i+1, l.Text, l.Start, l.End)
			if len(l.Words) > 0 {
				var ws []string
				for k, w := range l.Words {
					mark := ""
					if k == l.Keyword {
						mark = " (keyword)"
					}
					ws = append(ws, fmt.Sprintf("%s@%.2f%s", w.Text, w.Start, mark))
				}
				b.WriteString("; words: " + strings.Join(ws, " "))
			} else if l.Keyword == 0 {
				b.WriteString("; the whole line is the keyword")
			}
			b.WriteString("\n")
		}
	}
	if sc.Hit != nil {
		fmt.Fprintf(&b, "Word hit: %q at %.2f s for %.2f s (window.__scene.hit): show it at full screen.\n", sc.Hit.Word, sc.Hit.At, sc.Hit.Length)
	}
	b.WriteString("\n")
	if in.World != "" {
		b.WriteString("The world of the video (where it happens and what it looks like; every metaphor belongs to it): " + in.World + "\n")
	} else {
		b.WriteString("No world was chosen for this video: choose one (where it happens and what it looks like), draw this scene in it and state it as \"world\" in the manifest, so that the next scenes keep it.\n")
	}
	if in.Device != "" {
		b.WriteString("The device that carries the video (the metaphors serve it): " + in.Device + "\n")
	}
	if in.Hook != "" {
		b.WriteString("This is the first piece: the first two seconds of the video show: " + in.Hook + "\n")
	}
	if in.Brief != "" {
		b.WriteString("\nThe idea asked for (the metaphor to draw):\n" + in.Brief + "\n")
	} else {
		b.WriteString("\nNo idea was given: choose the one visual metaphor that fits what the verse means.\n")
	}
	if in.Archetype != "" {
		b.WriteString("\nA cue from the reference video the person likes (the kind of shot, never something to copy):\n" + in.Archetype + "\n")
	}
	if len(in.Neighbours) > 0 {
		b.WriteString("\nThe pieces around it (do not repeat their picture):\n")
		for _, n := range in.Neighbours {
			fmt.Fprintf(&b, "- piece %d uses %q", n.Index, n.Part)
			if n.Description != "" {
				b.WriteString(": " + n.Description)
			}
			b.WriteString("\n")
		}
	}
	if len(in.Used) > 0 {
		b.WriteString("\nAlready used in this video (do not repeat any of these metaphors):\n- " + strings.Join(in.Used, "\n- ") + "\n")
	}
	var names []string
	for _, p := range cat.ListAll() {
		names = append(names, p.Name)
	}
	b.WriteString("\nNames already taken: " + strings.Join(names, ", ") + " (the program gives your scene its own name).\n\n")
	b.WriteString(sceneExample())
	if problem != "" {
		fmt.Fprintf(&b, "\nYour previous answer was refused: %s\nAnswer again with the two blocks fixed.\n", problem)
	}
	return b.String()
}

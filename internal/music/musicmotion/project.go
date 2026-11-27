package musicmotion

// project.go: a piece is rendered as a project of its own: a video project with one motion_graphic scene for each
// piece of the script, each with its stored page (made here from a part, the design and the lines) and the song map
// sliced to the scene's own time, and the song as the soundtrack. The pipeline, its cache and its events then do the
// rest. Nothing here loads a model.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	yamlv3 "gopkg.in/yaml.v3"

	"veritmo/internal/music/songmap"
	"veritmo/internal/project/config"
	"veritmo/internal/video/motion"
)

// ProjectSpec is what the project of a piece is made from.
type ProjectSpec struct {
	// Dir is the folder of the project; Name its id.
	Dir  string
	Name string
	// SongFile is the audio to use as the soundtrack (.mp3 or .wav); it is copied into the project's sources.
	SongFile string
	Map      songmap.Map
	Design   Design
	Script   Script
	Cat      Catalog
	Lib      Library
	FPS      int
	Width    int
	Height   int
	// Client is the dashboard that asked for the piece, written in the project (see config.ValidClient); empty for none.
	Client string
	// Adult marks the project of the piece as for adults (`adult: true` in its scenes.yaml), so its video and its runs are hidden like any
	// project for adults. False writes nothing: the project is then as it always was.
	Adult bool
	// Title is the song's title, when it has one: a scene may say it (see SceneDraftInput.Title).
	Title string
	// World, Device and Hook are the storyline of the plan the piece was made from (empty for none): the scene writer draws its
	// metaphor from the world and the device, and the first piece opens with the hook.
	World, Device, Hook string
	// Reference is the shots of the reference video the piece names, when it names one with shots: the scene writer is told the kind
	// of shot at its place (see SceneDraftInput.Archetype). Nil for none.
	Reference *ReferenceShots
}

// SceneFiles says what was written for a piece.
type SceneFiles struct {
	ID       string  `json:"id"`
	Part     string  `json:"part"`
	Seconds  float64 `json:"seconds"`
	PagePath string  `json:"page"`
	// Blender is the description file of a 3D piece (in sources/), which has no part and no page.
	Blender string `json:"blender,omitempty"`
	// Layer says the scene is a transparent page laid over the video track (the piece has an item of the track over it).
	Layer bool `json:"layer,omitempty"`
}

// sceneFor makes the scene of a piece, in the piece's own time.
func sceneFor(p Piece, spec ProjectSpec) (Scene, Part, error) {
	if p.Blender != nil {
		return Scene{}, Part{}, fmt.Errorf("piece %q is a 3D scene (blender): it has no page", p.ID)
	}
	part, ok := spec.Cat.Get(p.Part)
	if !ok {
		return Scene{}, Part{}, fmt.Errorf("piece %q: the part %q is not in the catalog", p.ID, p.Part)
	}
	params, err := part.ResolveParams(p.Params)
	if err != nil {
		return Scene{}, Part{}, fmt.Errorf("piece %q: %w", p.ID, err)
	}
	design := spec.Design.With(p.Design)
	sc := Scene{Part: part, Params: params, Animate: p.Animate, Design: design, Mode: ResolveMode(design, p), Duration: round3(p.End - p.Start), Width: spec.Width, Height: spec.Height,
		SongStart: p.Start, SongLength: spec.Map.Duration}
	// A piece that has an item of the video track over it is drawn as a layer: its page paints nothing over the frame (the scrim belongs to the item).
	if track, _ := spec.Script.WithTrack(); len(track.ItemsOver(p)) > 0 {
		sc.Background = &VideoBackground{}
	}
	for _, w := range p.With {
		wp, ok := spec.Cat.Get(w.Part)
		if !ok {
			return Scene{}, Part{}, fmt.Errorf("piece %q: the extra part %q is not in the catalog", p.ID, w.Part)
		}
		wparams, err := wp.ResolveParams(w.Params)
		if err != nil {
			return Scene{}, Part{}, fmt.Errorf("piece %q: %w", p.ID, err)
		}
		sc.Extras = append(sc.Extras, ScenePart{Part: wp, Params: wparams, Animate: w.Animate})
	}
	for _, it := range spec.Script.layerItemsFor(p) {
		if it.Block != "" {
			blk, err := loadSceneBlock(it.Block, it.Params)
			if err != nil {
				return Scene{}, Part{}, fmt.Errorf("piece %q: %w", p.ID, err)
			}
			sc.Layers = append(sc.Layers, SceneLayer{Block: blk, From: it.Start, To: it.End})
			continue
		}
		ip, ok := spec.Cat.Get(it.Part)
		if !ok {
			return Scene{}, Part{}, fmt.Errorf("piece %q: the layer part %q is not in the catalog", p.ID, it.Part)
		}
		iparams, err := ip.ResolveParams(it.Params)
		if err != nil {
			return Scene{}, Part{}, fmt.Errorf("piece %q: %w", p.ID, err)
		}
		sc.Layers = append(sc.Layers, SceneLayer{Part: ip, Params: iparams, From: it.Start, To: it.End, Animate: it.Animate})
	}
	// The energy of the section the piece sits in sizes the groups of words of its long lines.
	energy := EnergyNormal
	if es := SectionEnergies(spec.Map); es != nil {
		mid := (p.Start + p.End) / 2
		for i, s := range spec.Map.Sections {
			if mid >= s.Start && mid < s.End {
				energy = EnergyClass(es[i].Rank, len(es))
			}
		}
	}
	for _, l := range p.Lines {
		text, _ := LineText(l, spec.Map)
		line := Line{Text: text, Keyword: -1}
		if l.Line != nil {
			for _, w := range spec.Map.Words {
				if w.Line == *l.Line {
					line.Words = append(line.Words, Word{Text: w.Text, Start: round3(w.Start - p.Start), End: round3(w.End - p.Start), Heard: w.Heard})
				}
			}
			if len(line.Words) > 0 {
				line.Start, line.End = line.Words[0].Start, line.Words[len(line.Words)-1].End
			}
		} else {
			line.Start, line.End = round3(l.Start-p.Start), round3(l.End-p.Start)
		}
		if line.Start < 0 {
			line.Start = 0 // a line that began in the piece before: shown from the start
		}
		if l.Keyword != "" {
			if len(line.Words) > 0 {
				for i, w := range line.Words {
					if hasWord(w.Text, l.Keyword) {
						line.Keyword = i
						break
					}
				}
			} else if hasWord(text, l.Keyword) {
				line.Keyword = 0
			}
		}
		if design.VerseGroups == nil || *design.VerseGroups {
			line.Groups = LineGroups(line, energy)
		}
		sc.Lines = append(sc.Lines, line)
	}
	if p.Stage != nil {
		if err := p.Stage.Validate(spec.Lib, spec.Width, spec.Height); err != nil {
			return Scene{}, Part{}, fmt.Errorf("piece %q: %w", p.ID, err)
		}
		sc.Backdrop, sc.Actors, err = ResolveStage(p.Stage, spec.Lib)
		if err != nil {
			return Scene{}, Part{}, fmt.Errorf("piece %q: %w", p.ID, err)
		}
	}
	if p.Hit != nil {
		sc.Hit = &Hit{Word: p.Hit.Word, At: round3(p.Hit.At - p.Start), Length: p.Hit.Length}
		if sc.Hit.At < 0 {
			sc.Hit.At = 0
		}
	}
	return sc, part, nil
}

func round3(v float64) float64 {
	return float64(int64(v*1000+0.5)) / 1000
}

// PieceMotion is the page of one piece of a script and the song map cut to it, as the renderer is given them.
type PieceMotion struct {
	ID         string
	Part       string
	Section    string
	Start, End float64
	Seconds    float64
	HTML       string
	Song       json.RawMessage
	// Background is the video behind the piece, when it has one: its page is then drawn on transparent and is the overlay of a scene
	// that plays the video.
	Background *PieceBackground
}

// BuildPages makes the page and the sliced song map of each piece of the script, in script order, and writes nothing.
// only names the pieces to make; empty makes all, and a name that is not a piece of the script is an error. The render
// and the preview both come from here, so they cannot differ.
func BuildPages(spec ProjectSpec, only []string) ([]PieceMotion, error) {
	known := map[string]Piece{}
	for _, p := range spec.Script.Pieces {
		known[p.ID] = p
	}
	want := map[string]bool{}
	for _, id := range only {
		p, ok := known[id]
		if !ok {
			return nil, fmt.Errorf("piece %s: not in the script", id)
		}
		if p.Blender != nil {
			return nil, fmt.Errorf("piece %s is a 3D scene (blender): it has no page", id)
		}
		want[id] = true
	}
	var out []PieceMotion
	for _, p := range spec.Script.Pieces {
		if p.Blender != nil || (len(want) > 0 && !want[p.ID]) {
			continue // a 3D piece is drawn by Blender: it has no page
		}
		sc, part, err := sceneFor(p, spec)
		if err != nil {
			return nil, err
		}
		page, err := BuildPage(sc)
		if err != nil {
			return nil, fmt.Errorf("piece %q: %w", p.ID, err)
		}
		// The piece's page reads where the beats, the words and the loudness are, not where the words' times came from or how many attacks the whole
		// song has: those are left out so that moving one word does not change the song of every piece (and so draw every page again).
		cut := songmap.Slice(spec.Map, p.Start, p.End)
		cut.WordsSource, cut.Attacks = "", 0
		song, _ := json.Marshal(cut)
		out = append(out, PieceMotion{ID: p.ID, Part: part.Name, Section: strings.TrimSpace(p.Section), Start: p.Start, End: p.End, Seconds: sc.Duration, HTML: page, Song: song, Background: p.Background})
	}
	return out, nil
}

// WriteProject writes the project of a piece into spec.Dir: scenes.yaml, the song in sources/, and for each piece
// of the script its page and its song map in motion/. Files whose content does not change are left alone, so the
// scenes of the pieces that did not change keep their cache. It returns what it wrote.
func WriteProject(spec ProjectSpec) ([]SceneFiles, error) {
	if spec.Dir == "" || spec.Name == "" {
		return nil, fmt.Errorf("the project needs a folder and a name")
	}
	if spec.FPS <= 0 || spec.Width <= 0 || spec.Height <= 0 {
		return nil, fmt.Errorf("the project needs a frame rate and a size")
	}
	ext := strings.ToLower(filepath.Ext(spec.SongFile))
	if ext != ".mp3" && ext != ".wav" {
		return nil, fmt.Errorf("the song must be an .mp3 or a .wav, got %q", filepath.Ext(spec.SongFile))
	}
	songName := "song" + ext
	if err := os.MkdirAll(spec.Dir, 0o755); err != nil {
		return nil, err
	}
	if err := writeIfChanged(filepath.Join(spec.Dir, config.SourcesSubdir, songName), nil, spec.SongFile); err != nil {
		return nil, fmt.Errorf("cannot copy the song: %w", err)
	}
	var files []SceneFiles
	var yaml strings.Builder
	if spec.Client != "" {
		fmt.Fprintf(&yaml, "client: %s\n", spec.Client)
	}
	if spec.Adult {
		yaml.WriteString("adult: true\n")
	}
	// the `background` of a piece is the item of the video track it becomes: from here on there is one form
	var terr error
	if spec.Script, terr = spec.Script.WithTrack(); terr != nil {
		return nil, terr
	}
	blenderPieces := 0
	for _, p := range spec.Script.Pieces {
		if p.Blender != nil {
			blenderPieces++
		}
	}
	fmt.Fprintf(&yaml, "project: %s\noutput: {mode: video, file: out/%s.mp4}\ndefaults:\n  resolution: %dx%d\n  fps: %d\n", spec.Name, spec.Name, spec.Width, spec.Height, spec.FPS)
	if blenderPieces > 0 || len(spec.Script.Video) > 0 {
		// A 3D piece takes its beats from the song's tempo and its colours and fonts from the song's design; the video track takes the colour of its
		// gaps and of its scrim from the design too.
		motionYAML, err := yamlv3.Marshal(map[string]any{"motion": BlenderConfig(spec.Map, spec.Design, 0, 0, 0).Defaults.Motion})
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(strings.TrimRight(string(motionYAML), "\n"), "\n") {
			fmt.Fprintf(&yaml, "  %s\n", line)
		}
	}
	fmt.Fprintf(&yaml, "bgm: {file: %s, volume: 1, ducking: false, fade_seconds: 0}\n", songName)
	if len(spec.Script.Video) > 0 {
		yaml.WriteString("video_track:\n")
		for _, it := range spec.Script.Video {
			fmt.Fprintf(&yaml, "  - {id: %s, clip: %s, start: %v, end: %v", it.ID, it.Clip, it.Start, it.End)
			for _, f := range []struct {
				name string
				v    *float64
			}{{"clip_start", it.ClipStart}, {"clip_end", it.ClipEnd}, {"clip_speed", it.ClipSpeed}, {"scrim", it.Scrim}, {"transition_seconds", it.TransitionSeconds}} {
				if f.v != nil {
					fmt.Fprintf(&yaml, ", %s: %v", f.name, *f.v)
				}
			}
			if len(it.Local) > 0 {
				yaml.WriteString(", local_scrims: [")
				for k, l := range it.Local {
					if k > 0 {
						yaml.WriteString(", ")
					}
					fmt.Fprintf(&yaml, "{band: %s, scrim: %v}", l.Band, l.Scrim)
				}
				yaml.WriteString("]")
			}
			yaml.WriteString("}\n")
		}
	}
	yaml.WriteString("scenes:\n")
	pages, err := BuildPages(spec, nil)
	if err != nil {
		return nil, err
	}
	pageOf := map[string]PieceMotion{}
	for _, pg := range pages {
		pageOf[pg.ID] = pg
	}
	fades := Crossfades(spec.Script, spec.Design)
	crossfading := false
	for _, f := range fades {
		crossfading = crossfading || f > 0
	}
	described := map[string]bool{}
	for i, p := range spec.Script.Pieces {
		// With crossfades, the end of a piece and the start of the next overlap, so each piece is drawn as long as the fade into the
		// next one more: the pieces then land on the song as they do with cuts.
		seconds := SceneSeconds(spec.Script, spec.Design, i)
		if p.Blender != nil {
			name := p.ID + config.SceneSpecSuffix
			if err := writeIfChanged(filepath.Join(spec.Dir, config.SourcesSubdir, name), sceneJSON(p.Blender.Description), ""); err != nil {
				return nil, err
			}
			described[name] = true
			fmt.Fprintf(&yaml, "  - id: %s\n    blender: {build: %s}\n    silence_seconds: %v\n", p.ID, name, seconds)
			if crossfading && i > 0 {
				fmt.Fprintf(&yaml, "    transition_seconds: %v\n", fades[i])
			}
			files = append(files, SceneFiles{ID: p.ID, Seconds: round3(p.End - p.Start), Blender: name})
			continue
		}
		pg := pageOf[p.ID]
		// A piece with an item of the video track over it is a layer: a transparent page laid over the track. A stale overlay page (the form a
		// piece's own video had before the track) is removed so that it is never read.
		layer := len(spec.Script.ItemsOver(p)) > 0
		if err := writeIfChanged(motion.PagePath(spec.Dir, pg.ID, false), []byte(pg.HTML), ""); err != nil {
			return nil, err
		}
		if err := writeIfChanged(motion.SongPath(spec.Dir, pg.ID), pg.Song, ""); err != nil {
			return nil, err
		}
		_ = os.Remove(motion.PagePath(spec.Dir, pg.ID, true))
		_ = os.Remove(motion.MetaPath(spec.Dir, pg.ID, true))
		label := "music piece: " + pg.Part + " (" + pg.Section + ")"
		fmt.Fprintf(&yaml, "  - id: %s\n    motion_graphic: %q\n    silence_seconds: %v\n", pg.ID, label, seconds)
		if layer {
			yaml.WriteString("    layer: true\n")
		}
		if crossfading && i > 0 {
			fmt.Fprintf(&yaml, "    transition_seconds: %v\n", fades[i])
		}
		files = append(files, SceneFiles{ID: pg.ID, Part: pg.Part, Seconds: pg.Seconds, PagePath: filepath.Join(motion.Dir, pg.ID+".html"), Layer: layer})
	}
	removeStaleDescriptions(filepath.Join(spec.Dir, config.SourcesSubdir), described)
	if err := writeIfChanged(filepath.Join(spec.Dir, "scenes.yaml"), []byte(yaml.String()), ""); err != nil {
		return nil, err
	}
	return files, nil
}

// writeIfChanged writes data (or copies the file from) to path unless it already has that content.
func writeIfChanged(path string, data []byte, from string) error {
	if from != "" {
		src, err := os.ReadFile(from)
		if err != nil {
			return err
		}
		return writeIfChanged(path, src, "")
	}
	if old, err := os.ReadFile(path); err == nil && string(old) == string(data) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// Crossfades are the seconds of crossfade into each piece of a script from the one before: the piece's own, else the design's,
// shortened to half of the shorter of the two pieces it joins. The first is 0.
func Crossfades(s Script, d Design) []float64 {
	out := make([]float64, len(s.Pieces))
	for i := 1; i < len(s.Pieces); i++ {
		f := d.TransitionSeconds
		if t := s.Pieces[i].TransitionSeconds; t != nil {
			f = *t
		}
		limit := math.Min(s.Pieces[i-1].End-s.Pieces[i-1].Start, s.Pieces[i].End-s.Pieces[i].Start) / 2
		out[i] = round3(math.Max(0, math.Min(f, limit)))
	}
	return out
}

// SceneOfPiece is the scene of one piece of a script as its page is made of it (its lines with their words and times, its design).
func SceneOfPiece(p Piece, spec ProjectSpec) (Scene, error) {
	sc, _, err := sceneFor(p, spec)
	return sc, err
}

// sceneJSON is a description as it is stored in the sources: indented, with a final line break.
func sceneJSON(raw []byte) []byte {
	var b bytes.Buffer
	if err := json.Indent(&b, raw, "", "  "); err != nil {
		return raw
	}
	return append(b.Bytes(), '\n')
}

// removeStaleDescriptions deletes the descriptions of the sources that no 3D piece of the script names any more.
func removeStaleDescriptions(sources string, keep map[string]bool) {
	entries, _ := os.ReadDir(sources)
	for _, e := range entries {
		if e.Type().IsRegular() && config.ValidSceneSpecName(e.Name()) && !keep[e.Name()] {
			_ = os.Remove(filepath.Join(sources, e.Name()))
		}
	}
}

package musicmotion

// scenepage.go: the page of an ordinary motion scene (not a piece of a song) made from a part of the catalog: the part, the
// project's design (or a palette chosen from the words of its subject prompt), the line of text of the scene and a clock
// of a fixed tempo. See spec motion-graphics ("A motion scene can be made from a part of the catalog").

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"veritmo/internal/music/songmap"
	"veritmo/internal/project/config"
	"veritmo/internal/project/pipeline"
)

// fixedTempoSeconds is how much of a fixed-tempo clock is made: a narrated scene's length is only known after its
// narration, so the beats go well past the estimate (a beat past the last one repeats the interval).
const fixedTempoSeconds = 900.0

// FixedTempoMap is the song map of a scene with no song: the beats of a tempo, and nothing else.
func FixedTempoMap(bpm float64) songmap.Map {
	return songmap.Build(songmap.Input{Duration: fixedTempoSeconds, BPM: bpm})
}

// BuildScenePage makes the page and the beat clock of a scene made from a part.
func BuildScenePage(cat Catalog, r pipeline.PartRequest) (pipeline.PartPage, error) {
	part, ok := cat.Get(r.Part)
	if !ok {
		return pipeline.PartPage{}, fmt.Errorf("the part %q is not in the catalog", r.Part)
	}
	params, err := part.ResolveParams(r.Params)
	if err != nil {
		return pipeline.PartPage{}, err
	}
	var design Design
	if r.Design != nil {
		design = DesignFromConfig(*r.Design)
	} else {
		design = Fallback(r.Style, nil)
	}
	if err := design.Validate(); err != nil {
		return pipeline.PartPage{}, err
	}
	if len(r.Song) == 0 && (r.BPM < config.MinMotionBPM || r.BPM > config.MaxMotionBPM) {
		return pipeline.PartPage{}, fmt.Errorf("the tempo %v is outside %v to %v", r.BPM, config.MinMotionBPM, config.MaxMotionBPM)
	}
	dur := math.Max(r.Duration, 1)
	sc := Scene{Part: part, Params: params, Design: design, Mode: design.ModeFor(""), Duration: dur, Width: r.Width, Height: r.Height, SongStart: r.SongStart, SongLength: r.SongLength}
	if text := strings.TrimSpace(r.Text); text != "" {
		// One line for the whole scene, with no time for each word: the verse styles that need it show it plain.
		sc.Lines = []Line{{Text: text, Start: 0, End: dur, Keyword: -1}}
	}
	html, err := BuildPage(sc)
	if err != nil {
		return pipeline.PartPage{}, err
	}
	song := []byte(r.Song) // a project with a song: the map of the song, cut to the scene
	if len(song) == 0 {
		song, _ = json.Marshal(FixedTempoMap(r.BPM))
	}
	return pipeline.PartPage{HTML: html, Song: song}, nil
}

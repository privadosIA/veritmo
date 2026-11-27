package musicmotion

// blenderpiece.go: what a 3D piece (a piece with `blender`, drawn by Blender) needs besides its validation: where the song's
// beats fall inside it, and how a piece made from a part becomes one. See specs/music-motion ("A piece of the script can be a
// 3D scene").

import (
	"encoding/json"
	"fmt"

	"veritmo/internal/music/songmap"
)

// BeatPhase is how many seconds after the start of a piece the song's first beat inside it falls: a key in beats of a 3D scene
// counts from the start of the scene, which is where the piece starts, so the keys land on the song's beats only when this is 0.
// It is 0 when the song has no beats.
func BeatPhase(m songmap.Map, start float64) float64 {
	best := -1.0
	for _, b := range m.Beats {
		if b >= start-1e-6 && (best < 0 || b < best) {
			best = b
		}
	}
	if best < 0 {
		return 0
	}
	if d := best - start; d > 0 {
		return round3(d)
	}
	return 0
}

// MakeBlender turns the piece into a 3D scene with this description: the part and everything that belongs to a page is cleared.
func (p *Piece) MakeBlender(description json.RawMessage) {
	p.Part, p.Params, p.Animate, p.With, p.Design, p.Mode, p.Lines, p.Hit, p.Stage, p.Background = "", nil, nil, nil, nil, "", nil, nil, nil, nil
	p.Blender = &PieceBlender{Description: description}
}

// PieceByID is the index of the piece of the script with this id, or an error naming it.
func (s Script) PieceByID(id string) (int, error) {
	for i, p := range s.Pieces {
		if p.ID == id {
			return i, nil
		}
	}
	return -1, fmt.Errorf("script piece %q not found", id)
}

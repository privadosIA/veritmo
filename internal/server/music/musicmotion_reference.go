package music

// musicmotion_reference.go: a piece may name a reference video of the library (spec music-motion, "A reference steers the
// proposal and the script"): the rhythm sheet of the reference reaches the design and the script proposals, and the shots of
// the reference reach the scene writer. The frames, the words and the colours of the reference are not given here.

import (
	"net/http"
	"strings"

	"veritmo/internal/music/motionref"
	"veritmo/internal/music/musicmotion"
	"veritmo/internal/server/api"
)

// checkReference refuses (422) a reference that is not in the library or not ready; "" is no reference. It changes nothing.
func (h *Handler) checkReference(w http.ResponseWriter, id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return true
	}
	ref, err := h.assistant.RefStore().Get(id)
	if err != nil {
		api.Refuse(w, "the reference %q does not exist", id)
		return false
	}
	if ref.State != motionref.StateReady {
		api.Refuse(w, "the reference %q is %s: only a ready reference can steer a piece", id, ref.State)
		return false
	}
	return true
}

// referenceOf reads the reference a piece names (nil for none, or when it has been deleted since).
func (h *Handler) referenceOf(info *pieceInfo) *motionref.Reference {
	if info.Reference == "" {
		return nil
	}
	ref, err := h.assistant.RefStore().Get(info.Reference)
	if err != nil {
		return nil
	}
	return &ref
}

// referenceSheet is what the proposals are told of the reference of the piece, and the notes to give back: nil with a note
// when the reference has no rhythm sheet yet (it was analysed before shots existed) or is gone; nil and no note when the
// piece names none.
func (h *Handler) referenceSheet(info *pieceInfo) (*musicmotion.ReferenceSheet, []string) {
	if info.Reference == "" {
		return nil, nil
	}
	ref := h.referenceOf(info)
	switch {
	case ref == nil:
		return nil, []string{"the reference " + info.Reference + " is no longer in the library: the proposal does not use it"}
	case ref.Analysis == nil || ref.Analysis.Rhythm == nil || ref.Analysis.RhythmText == "":
		return nil, []string{"the reference " + info.Reference + " has no rhythm sheet (it was analysed before shots existed: run its breakdown): the proposal does not use it"}
	}
	return &musicmotion.ReferenceSheet{Text: ref.Analysis.RhythmText, MedianShotByThird: ref.Analysis.Rhythm.MedianShotByThird}, nil
}

// referenceShots are the shots of the reference of the piece for the scene writer (nil for none, or for a reference with no shots).
func (h *Handler) referenceShots(info *pieceInfo) *musicmotion.ReferenceShots {
	ref := h.referenceOf(info)
	if ref == nil || ref.Analysis == nil || len(ref.Analysis.Shots) == 0 {
		return nil
	}
	out := &musicmotion.ReferenceShots{Duration: ref.Analysis.Duration}
	for _, s := range ref.Analysis.Shots {
		shot := musicmotion.ReferenceShot{Start: s.Start, End: s.End, BeatCut: s.BeatCut}
		if !s.Read.Unknown() {
			shot.Subject, shot.Text, shot.TextSize, shot.Entrance, shot.Camera = s.Read.Subject, s.Read.Text, s.Read.TextSize, s.Read.Entrance, s.Read.Camera
		}
		out.Shots = append(out.Shots, shot)
	}
	return out
}

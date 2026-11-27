package musicmotion

// carry.go: what a new script proposal does with the video track of the script it replaces. The track does not hang on pieces (an item has
// its own seconds of the song), so a proposal keeps it as it is, and what has to change is the pieces that now sit over a video: they use only
// parts that leave a video visible, and what in them would hide it is changed and named in the notes of the proposal.

import (
	"fmt"
	"strings"
)

// overVideoAt says whether a proposed piece from start to end has an item of the track over it (they share more than a rounding).
func overVideoAt(items []VideoItem, start, end float64) bool {
	for _, it := range items {
		if it.Start < end-coverTolerance && it.End > start+coverTolerance {
			return true
		}
	}
	return false
}

// onlyOverVideo keeps the parts that leave a video visible; the candidates as they were when none does.
func onlyOverVideo(cands []Part) []Part {
	var out []Part
	for _, p := range cands {
		if p.OverVideo {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return cands
	}
	return out
}

// KeepTrack puts the video track of the replaced script on s, as it was, and makes each piece of s that is over an item fit a video behind it: a
// main part that does not leave the video visible is replaced by one that does (taken in turn, as ApplyLevel does), the extras that do not are
// taken out, a stage background is cleared, and an item of a layer that would hide a video is taken out. It returns what it changed, one sentence
// each, for the notes of the proposal; a script and a track with nothing over a video are left as they are. Items outside the song of s (a song
// that got shorter) are kept too: the script then says so when it is read and refuses to save until they are dealt with.
func KeepTrack(s *Script, track []VideoItem, cat Catalog, level string) []string {
	if len(track) == 0 {
		return nil
	}
	s.Video = append([]VideoItem(nil), track...)
	var notes []string
	cands := onlyOverVideo(partsFor(cat, level))
	for i := range s.Pieces {
		p := &s.Pieces[i]
		over := s.ItemsOver(*p)
		if len(over) == 0 {
			continue
		}
		if main, ok := cat.Get(p.Part); ok && !main.OverVideo && len(cands) > 0 {
			prev := ""
			if i > 0 {
				prev = s.Pieces[i-1].Part
			}
			to := nextPart(cands, i, prev)
			notes = append(notes, fmt.Sprintf("piece %s is over the video %q: its part %q, which would hide it, was replaced by %q", p.ID, over[0].ID, p.Part, to))
			p.Part, p.Params, p.Animate = to, nil, nil
		}
		var keep []PartRef
		for _, w := range p.With {
			if wp, ok := cat.Get(w.Part); ok && !wp.OverVideo {
				notes = append(notes, fmt.Sprintf("piece %s is over the video %q: its extra part %q, which would hide it, was taken out", p.ID, over[0].ID, w.Part))
				continue
			}
			keep = append(keep, w)
		}
		p.With = keep
		if p.Stage != nil && p.Stage.Background != "" {
			notes = append(notes, fmt.Sprintf("piece %s is over the video %q: its stage background %q, which would hide it, was taken out", p.ID, over[0].ID, p.Stage.Background))
			p.Stage.Background = ""
		}
	}
	// the items of the layers that touch such a piece have to leave the video visible too: an item that does not is taken out
	for li := range s.Layers {
		var keep []LayerItem
		for _, it := range s.Layers[li].Items {
			hides := false
			if it.Block == "" {
				if ip, ok := cat.Get(it.Part); ok && !ip.OverVideo {
					for _, p := range s.Pieces {
						hides = hides || (it.touches(p) && len(s.ItemsOver(p)) > 0)
					}
				}
			}
			if hides {
				notes = append(notes, fmt.Sprintf("layer %q: its item %q (%.1f s to %.1f s) would hide a video behind a piece: it was taken out", s.Layers[li].ID, it.Part, it.Start, it.End))
				continue
			}
			keep = append(keep, it)
		}
		s.Layers[li].Items = keep
	}
	return notes
}

// videoBlock is what the assistant is told when the script it replaces has a video track.
func videoBlock(track []VideoItem) string {
	if len(track) == 0 {
		return ""
	}
	var stretches []string
	for _, it := range track {
		stretches = append(stretches, fmt.Sprintf("%.1f s to %.1f s", it.Start, it.End))
	}
	return "A video plays behind these stretches of the song: " + strings.Join(stretches, "; ") +
		". Use there only the parts marked [over_video] in the catalog: the others paint the whole frame and would hide it.\n"
}

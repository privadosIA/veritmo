package musicmotion

// video.go: the video track of a script (spec music-motion, "A script has a video track"). One lane of items in seconds of the song, in
// order, never overlapping, with gaps allowed; an item plays a clip the piece keeps. A clip can span several pieces and a piece can have
// several items, because the cuts of the picture are not the cuts of the text.

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// MaxVideoItems is the most items a video track has.
const MaxVideoItems = 64

// BackgroundItemPrefix starts the id of the item a piece's `background` becomes.
const BackgroundItemPrefix = "bg-"

// VideoItem is a clip played from Start to End (seconds of the song). Clip, ClipStart, ClipEnd, ClipSpeed and Scrim are those of a
// PieceBackground; TransitionSeconds is the crossfade from the item before it when the two touch (nil or 0 is a cut).
type VideoItem struct {
	ID                string   `json:"id"`
	Clip              string   `json:"clip"`
	Start             float64  `json:"start"`
	End               float64  `json:"end"`
	ClipStart         *float64 `json:"clip_start,omitempty"`
	ClipEnd           *float64 `json:"clip_end,omitempty"`
	ClipSpeed         *float64 `json:"clip_speed,omitempty"`
	Scrim             *float64 `json:"scrim,omitempty"`
	TransitionSeconds *float64 `json:"transition_seconds,omitempty"`
	// Local are the strips of the scrim colour that EffectiveScrims lays over the band where the verse falls on the item. It is derived from the
	// clip and the design, set only on the copy of the script that is rendered, drawn or previewed, and never saved or answered with the script.
	Local []LocalScrim `json:"-"`
}

// Play is the clip the item plays, as a PieceBackground (the type the clip fields are checked and mapped with).
func (it VideoItem) Play() PieceBackground {
	return PieceBackground{Clip: it.Clip, ClipStart: it.ClipStart, ClipEnd: it.ClipEnd, ClipSpeed: it.ClipSpeed, Scrim: it.Scrim}
}

// Seconds is how long the item lasts.
func (it VideoItem) Seconds() float64 { return it.End - it.Start }

// touchesPiece says whether the item shows in the piece: they share more than a rounding.
func (it VideoItem) touchesPiece(p Piece) bool {
	return it.Start < p.End-coverTolerance && it.End > p.Start+coverTolerance
}

// ItemsOver lists, in order, the items of the video track that show in the piece.
func (s Script) ItemsOver(p Piece) []VideoItem {
	var out []VideoItem
	for _, it := range s.Video {
		if it.touchesPiece(p) {
			out = append(out, it)
		}
	}
	return out
}

func itemName(it VideoItem, i int) string {
	if it.ID != "" {
		return fmt.Sprintf("video item %q (#%d)", it.ID, i+1)
	}
	return fmt.Sprintf("video item #%d", i+1)
}

// WithTrack is the script with the `background` of each piece turned into an item of the video track that spans the piece (id
// BackgroundItemPrefix + the piece's id), and the field taken off the piece: the one form every other part of the program reads. A piece
// whose background would overlap an item of the track, or whose item id is already taken by one that does not match it, is an error naming
// both. A script with no background comes back as it was (the same slices).
func (s Script) WithTrack() (Script, error) {
	any := false
	for _, p := range s.Pieces {
		any = any || p.Background != nil
	}
	if !any {
		return s, nil
	}
	out := s
	out.Pieces = append([]Piece(nil), s.Pieces...)
	out.Video = append([]VideoItem(nil), s.Video...)
	for i, p := range out.Pieces {
		if p.Background == nil {
			continue
		}
		b := p.Background
		item := VideoItem{ID: BackgroundItemPrefix + p.ID, Clip: b.Clip, Start: p.Start, End: p.End, ClipStart: b.ClipStart, ClipEnd: b.ClipEnd, ClipSpeed: b.ClipSpeed, Scrim: b.Scrim}
		replaced := false
		for k, it := range out.Video {
			if it.ID == item.ID {
				out.Video[k], replaced = item, true // the shorthand wins over an item of its own id: the same piece, said again
				continue
			}
		}
		if !replaced {
			for _, it := range out.Video {
				if it.Start < item.End-coverTolerance && it.End > item.Start+coverTolerance {
					return Script{}, fmt.Errorf("%s: its background (%.2f s to %.2f s) overlaps the video item %q (%.2f s to %.2f s): give the video one way or the other", name(p, i), item.Start, item.End, it.ID, it.Start, it.End)
				}
			}
			out.Video = append(out.Video, item)
		}
		out.Pieces[i].Background = nil
	}
	sort.SliceStable(out.Video, func(a, b int) bool { return out.Video[a].Start < out.Video[b].Start })
	return out, nil
}

// validateVideo checks the video track of a script whose pieces are already valid: each item (id, the lane rules, the clip, the part of it,
// the speed, the scrim, the transition) and what the pieces that show an item may draw. sources is the piece's sources folder.
func (s Script) validateVideo(cat Catalog) error {
	if len(s.Video) == 0 {
		return nil
	}
	if len(s.Video) > MaxVideoItems {
		return fmt.Errorf("the video track may have up to %d items, it has %d", MaxVideoItems, len(s.Video))
	}
	end := s.Pieces[len(s.Pieces)-1].End
	ids := map[string]bool{}
	for i, it := range s.Video {
		n := itemName(it, i)
		if !reID.MatchString(it.ID) {
			return fmt.Errorf("%s: the id must be lower case letters, digits and dashes, up to 24 characters", n)
		}
		if ids[it.ID] {
			return fmt.Errorf("%s: the id is repeated", n)
		}
		ids[it.ID] = true
		prev := -1.0
		if i > 0 {
			prev = s.Video[i-1].End
		}
		if err := checkLaneItem(n, i, it.Start, it.End, prev, end, "the video track"); err != nil {
			return err
		}
		if err := validateClipFields(n, "", it.Play(), s.sources); err != nil {
			return err
		}
		if t := it.TransitionSeconds; t != nil {
			if *t < 0 {
				return fmt.Errorf("%s: transition_seconds must be 0 or more, got %v", n, *t)
			}
			if i > 0 && *t > 0 {
				p := s.Video[i-1]
				if p.End >= it.Start-coverTolerance { // they touch: the fade is inside both
					if limit := minFloat(p.Seconds(), it.Seconds()); *t > limit+coverTolerance {
						return fmt.Errorf("%s: transition_seconds %v is longer than the shorter of the two items it joins (%.2f s)", n, *t, limit)
					}
				}
			}
		}
	}
	for i, p := range s.Pieces {
		over := s.ItemsOver(p)
		if len(over) == 0 {
			continue
		}
		n := name(p, i)
		if p.Blender != nil {
			return fmt.Errorf("%s: a 3D piece (blender) cannot be under the video item %q: it is drawn by Blender and not from a part", n, over[0].ID)
		}
		if p.Stage != nil && p.Stage.Background != "" {
			return fmt.Errorf("%s: a piece over the video item %q cannot have a stage background (%q): it would hide the video", n, over[0].ID, p.Stage.Background)
		}
		if err := s.overVideoParts(cat, p, n); err != nil {
			return err
		}
	}
	return nil
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// ClipTime is the second of the file that is on screen at second songT of the song, for an item that is built buildSeconds long (its own length
// plus the crossfade into the next item) from a file of fileSeconds: the one mapping the build, the frame of the preview and the player of a
// dashboard agree on (see PieceBackground.ClipTime).
func (it VideoItem) ClipTime(songT, buildSeconds, fileSeconds float64) (float64, error) {
	return it.Play().ClipTime(songT-it.Start, buildSeconds, fileSeconds)
}

// TrackFade is the crossfade into item i of a track from the one before it: what it says, when the two touch, never longer than the shorter of
// them (0 for a cut, a gap or the first item).
func TrackFade(items []VideoItem, i int) float64 {
	if i <= 0 || items[i].TransitionSeconds == nil || *items[i].TransitionSeconds <= 0 || items[i].Start > items[i-1].End+coverTolerance {
		return 0
	}
	return minFloat(*items[i].TransitionSeconds, minFloat(items[i-1].Seconds(), items[i].Seconds()))
}

// TrackBuildSeconds is how long item i of a track is built: its own length plus the crossfade into the next one when the two touch, so that
// the items land where they start once the track is joined.
func TrackBuildSeconds(items []VideoItem, i int) float64 {
	secs := items[i].Seconds()
	if i+1 < len(items) {
		secs += TrackFade(items, i+1)
	}
	return secs
}

// ItemMapping says how an item plays its clip: the part of the file (From to To, seconds of the file), how fast, and whether the part is
// repeated to fill the item. It is what a dashboard needs to play the clip under the transparent pages with a <video> of its own.
type ItemMapping struct {
	From    float64 `json:"from"`
	To      float64 `json:"to"`
	Speed   float64 `json:"speed"`
	Repeats bool    `json:"repeats"`
}

// Mapping is how the item plays a file of fileSeconds.
func (it VideoItem) Mapping(fileSeconds float64) ItemMapping {
	from, to := 0.0, fileSeconds
	if it.ClipStart != nil {
		from = *it.ClipStart
	}
	if it.ClipEnd != nil && *it.ClipEnd < to {
		to = *it.ClipEnd
	}
	speed := it.effectiveSpeed(fileSeconds)
	return ItemMapping{From: from, To: to, Speed: speed, Repeats: it.Seconds()*speed > (to-from)+coverTolerance}
}

// effectiveSpeed is how fast the item plays its part: the speed it says, or the one the automatic fit gives (slowed by at most 2x to fill its
// length, never sped up).
func (it VideoItem) effectiveSpeed(fileSeconds float64) float64 {
	if it.ClipSpeed != nil && *it.ClipSpeed > 0 {
		return *it.ClipSpeed
	}
	start, end := 0.0, fileSeconds
	if it.ClipStart != nil {
		start = *it.ClipStart
	}
	if it.ClipEnd != nil && *it.ClipEnd < end {
		end = *it.ClipEnd
	}
	if l := end - start; l > 0 && it.Seconds() > l {
		slow := it.Seconds() / l
		if slow > 2 {
			slow = 2
		}
		return 1 / slow
	}
	return 1
}

// SplitItem cuts an item at second at of the song (strictly inside it) into two that touch there, with no jump in the picture: the second part
// takes up the file where the first stops, and both keep the speed the whole had (written out when the whole's was automatic, since a shorter
// part would otherwise be fitted differently). ids are the ids of the two parts; empty ones are item-a and item-b. A part that repeats its
// clip repeats from where it starts, so a wrap of the clip inside the second part is not the one the whole would have had.
func SplitItem(it VideoItem, at, fileSeconds float64, ids [2]string) (first, second VideoItem, err error) {
	if at <= it.Start+coverTolerance || at >= it.End-coverTolerance {
		return VideoItem{}, VideoItem{}, fmt.Errorf("the cut (%.2f s) must be inside the item %q (%.2f s to %.2f s)", at, it.ID, it.Start, it.End)
	}
	pos, err := it.ClipTime(at, it.Seconds(), fileSeconds)
	if err != nil {
		return VideoItem{}, VideoItem{}, err
	}
	speed := it.effectiveSpeed(fileSeconds)
	first, second = it, it
	first.ID, second.ID = ids[0], ids[1]
	if first.ID == "" {
		first.ID = it.ID + "-a"
	}
	if second.ID == "" {
		second.ID = it.ID + "-b"
	}
	first.End, second.Start = at, at
	first.ClipSpeed, second.ClipSpeed = &speed, &speed
	p := pos
	second.ClipStart = &p
	second.TransitionSeconds = nil // the cut is a cut: the fade into the whole stays on the first part
	return first, second, nil
}

// JoinItems makes one item of two that touch and play the same clip as one continuous stretch (what SplitItem made, or the same by hand). It
// refuses what is not: other clips, a gap, another end of the part, another speed, or a second part that does not take up the file where the first
// stops. The speed is written as automatic again when that is what the whole would have.
func JoinItems(a, b VideoItem, fileSeconds float64) (VideoItem, error) {
	switch {
	case a.Clip != b.Clip:
		return VideoItem{}, fmt.Errorf("the items %q and %q play different clips (%q and %q)", a.ID, b.ID, a.Clip, b.Clip)
	case b.Start-a.End > coverTolerance || a.End-b.Start > coverTolerance:
		return VideoItem{}, fmt.Errorf("the items %q and %q do not touch (%.2f s and %.2f s)", a.ID, b.ID, a.End, b.Start)
	case !sameFloat(a.ClipEnd, b.ClipEnd):
		return VideoItem{}, fmt.Errorf("the items %q and %q do not end the part of the clip at the same second", a.ID, b.ID)
	case a.effectiveSpeed(fileSeconds) != b.effectiveSpeed(fileSeconds) && math.Abs(a.effectiveSpeed(fileSeconds)-b.effectiveSpeed(fileSeconds)) > 1e-6:
		return VideoItem{}, fmt.Errorf("the items %q and %q play at different speeds", a.ID, b.ID)
	}
	end, err := a.ClipTime(a.End, a.Seconds(), fileSeconds)
	if err != nil {
		return VideoItem{}, err
	}
	start := 0.0
	if b.ClipStart != nil {
		start = *b.ClipStart
	}
	if math.Abs(end-start) > 0.05 {
		return VideoItem{}, fmt.Errorf("the item %q does not take up the clip where %q stops (%.2f s, not %.2f s): they are not one continuous stretch", b.ID, a.ID, start, end)
	}
	whole := a
	whole.End = b.End
	whole.TransitionSeconds = a.TransitionSeconds
	if strings.HasSuffix(a.ID, "-a") && strings.HasSuffix(b.ID, "-b") && strings.TrimSuffix(a.ID, "-a") == strings.TrimSuffix(b.ID, "-b") {
		whole.ID = strings.TrimSuffix(a.ID, "-a")
	}
	// an automatic speed that the whole fits the same way is written as automatic again
	if auto := (VideoItem{ClipStart: whole.ClipStart, ClipEnd: whole.ClipEnd, Start: whole.Start, End: whole.End}).effectiveSpeed(fileSeconds); whole.ClipSpeed != nil && math.Abs(auto-*whole.ClipSpeed) < 1e-6 {
		whole.ClipSpeed = nil
	}
	return whole, nil
}

func sameFloat(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return math.Abs(*a-*b) < 1e-6
}

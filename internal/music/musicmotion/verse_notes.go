package musicmotion

import "fmt"

// verse_notes.go: advice about where the verse falls (spec music-motion, "A piece says when its verse falls on its part"). A part's bands are
// measured once (overvideo.go) and kept in its manifest, so saying that a verse lands on a part costs nothing and needs no browser.

// VerseNote is one piece of advice about a script: either a piece whose verse is drawn on the band its main part occupies (Piece, Part,
// Band, Share and, when there is one, the clear band Free), or a track item whose clip leaves the accent under 3:1 (Item, Band, Contrast).
type VerseNote struct {
	Piece string `json:"piece,omitempty"`
	Item  string `json:"item,omitempty"`
	Part  string `json:"part,omitempty"`
	// Band is where the verse is drawn: top, center or bottom.
	Band string `json:"band"`
	// Share is how much of that band the part paints opaque.
	Share float64 `json:"share,omitempty"`
	// Free is the band with the least of the part in it, which is clear (below BandClear): where the verse could go.
	Free string `json:"free,omitempty"`
	// Contrast is the contrast of the accent against what the verse is read on, for an item that stays under 3:1.
	Contrast float64 `json:"contrast,omitempty"`
}

// verseBand is the band the verse of a piece is drawn in with the design it has (the design's layout, or the piece's own over it).
func verseBand(d Design, p Piece) string {
	if l := d.With(p.Design).VerseLayout; l != nil {
		switch l.Position {
		case VersePositionTop, VersePositionCenter:
			return l.Position
		}
	}
	return VersePositionBottom
}

// freeBand is the band the part leaves clearest, or "" when none of them is clear. A tie goes to the bottom, then the top: the centre is
// the least usual place for a verse.
func freeBand(b Bands) string {
	best, share := "", 1.0
	for _, c := range []struct {
		name  string
		share float64
	}{{VersePositionBottom, b.Bottom}, {VersePositionTop, b.Top}, {VersePositionCenter, b.Center}} {
		if c.share < share {
			best, share = c.name, c.share
		}
	}
	if share < BandClear {
		return best
	}
	return ""
}

// VerseNotes lists the pieces of the script whose verse is drawn on a band their main part occupies by BandOccupied or more and that have a
// clear band to move it to (a part made to fill the frame gives none: nothing is advised). A piece with
// no verse, one whose part draws the verse itself, a 3D piece and a part with no measured bands give none: nothing is said of what is not
// known. It is advice: nothing is moved or refused.
func VerseNotes(cat Catalog, s Script, d Design) []VerseNote {
	var out []VerseNote
	for _, p := range s.Pieces {
		if p.Part == "" || len(p.Lines) == 0 {
			continue
		}
		part, ok := cat.Get(p.Part)
		if !ok || part.Bands == nil || part.OwnsVerse() {
			continue
		}
		band := verseBand(d, p)
		if share := part.Bands.Share(band); share >= BandOccupied {
			if free := freeBand(*part.Bands); free != "" {
				out = append(out, VerseNote{Piece: p.ID, Part: p.Part, Band: band, Share: roundBand(share), Free: free})
			}
		}
	}
	return out
}

// PlaceVerse moves the verse of the pieces that sit over the video track away from their part (spec music-motion, "A proposal places the
// verse away from the part"): a piece with a verse, a main part whose bottom band is occupied by BandOccupied or more, a clear band
// elsewhere and no layout of its own gets, in its own design, the verse in that band; the design's scale and alignment stay. A piece that
// touches no item, a 3D piece, one whose part draws the verse itself or has no measured bands, one with a layout of its own and a design that
// already puts the verse elsewhere are left as they are. It says what it did, one line for each piece.
func PlaceVerse(s *Script, cat Catalog, d Design) []string {
	if len(s.Video) == 0 {
		return nil
	}
	var notes []string
	for i := range s.Pieces {
		p := &s.Pieces[i]
		if p.Part == "" || len(p.Lines) == 0 || len(s.ItemsOver(*p)) == 0 || (p.Design != nil && p.Design.VerseLayout != nil) || verseBand(d, *p) != VersePositionBottom {
			continue
		}
		part, ok := cat.Get(p.Part)
		if !ok || part.Bands == nil || part.OwnsVerse() || part.Bands.Bottom < BandOccupied {
			continue
		}
		free := freeBand(*part.Bands)
		if free == "" || free == VersePositionBottom {
			continue
		}
		l := VerseLayout{}
		if d.VerseLayout != nil {
			l = *d.VerseLayout
		}
		l.Position = free
		if p.Design == nil {
			p.Design = &DesignOverride{}
		}
		p.Design.VerseLayout = &l
		notes = append(notes, fmt.Sprintf("piece %s is over the video: its part %q paints %.0f %% of the bottom third, so its verse was put at the %s", p.ID, p.Part, part.Bands.Bottom*100, free))
	}
	return notes
}

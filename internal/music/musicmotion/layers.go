package musicmotion

// layers.go: lanes of parts drawn over the sequence of pieces (spec music-motion, "A script can have layers over its
// pieces"). An item is drawn in the page of every piece its range touches, and is visible only inside its range.

import (
	"fmt"
	"strings"
)

// Limits of the layers of a script.
const (
	MaxLayers     = 4
	MaxLayerItems = 32
	// MaxPageParts is how many parts one page draws: the main part, its extras and the items that touch the piece.
	MaxPageParts = 8
)

// LayerItem is a part drawn from Start to End, in seconds of the song.
type LayerItem struct {
	// Part is the part the item draws; a block item has none (see Block).
	Part string `json:"part,omitempty"`
	// Block is a HyperFrames block drawn instead of a part (see blocklayer.go); Params are then the values of its variables.
	Block  string         `json:"block,omitempty"`
	Params map[string]any `json:"params,omitempty"`
	Start  float64        `json:"start"`
	End    float64        `json:"end"`
	// Animate changes parameters over the item; t is in seconds from the item's own start.
	Animate Animate `json:"animate,omitempty"`
}

// Layer is a lane: items that follow one another and never overlap. Lanes are drawn in order, the last on top.
type Layer struct {
	ID    string      `json:"id"`
	Items []LayerItem `json:"items"`
}

func layerName(l Layer) string { return fmt.Sprintf("layer %q", l.ID) }

// touches says whether the item shows in the piece: they share more than a rounding.
func (it LayerItem) touches(p Piece) bool {
	return it.Start < p.End-coverTolerance && it.End > p.Start+coverTolerance
}

// validateLayers checks the layers of a script, whose pieces are already valid, and the parts every page would draw.
func (s Script) validateLayers(cat Catalog) error {
	if len(s.Layers) == 0 {
		return nil
	}
	if len(s.Layers) > MaxLayers {
		return fmt.Errorf("a script may have up to %d layers, it has %d", MaxLayers, len(s.Layers))
	}
	end := s.Pieces[len(s.Pieces)-1].End
	ids := map[string]bool{}
	for _, l := range s.Layers {
		if !reID.MatchString(l.ID) {
			return fmt.Errorf("%s: the id must be lower case letters, digits and dashes, up to 24 characters", layerName(l))
		}
		if ids[l.ID] {
			return fmt.Errorf("%s: the id is repeated", layerName(l))
		}
		ids[l.ID] = true
		if len(l.Items) > MaxLayerItems {
			return fmt.Errorf("%s: a layer may have up to %d items, it has %d", layerName(l), MaxLayerItems, len(l.Items))
		}
		for i, it := range l.Items {
			n := fmt.Sprintf("%s item #%d", layerName(l), i+1)
			var part Part
			if it.Block != "" {
				if it.Part != "" {
					return fmt.Errorf("%s names a part and a block: it is one or the other", n)
				}
				if len(it.Animate) > 0 {
					return fmt.Errorf("%s: a block item cannot animate parameters (the block has its own timeline)", n)
				}
				if err := checkBlockItem(it.Block); err != nil {
					return fmt.Errorf("%s: %w", n, err)
				}
			} else {
				var ok bool
				if part, ok = cat.Get(it.Part); !ok {
					return fmt.Errorf("%s: the part %q is not in the catalog", n, it.Part)
				}
				if _, err := part.ResolveParams(it.Params); err != nil {
					return fmt.Errorf("%s: %w", n, err)
				}
			}
			prev := -1.0
			if i > 0 {
				prev = l.Items[i-1].End
			}
			if err := checkLaneItem(n, i, it.Start, it.End, prev, end, "a layer"); err != nil {
				return err
			}
			if it.Block == "" {
				if err := part.checkAnimate(n, it.Animate, it.End-it.Start); err != nil {
					return err
				}
			}
		}
	}
	for i, p := range s.Pieces {
		used := map[string]string{p.Part: "the main part"}
		count := 1
		for _, w := range p.With {
			used[w.Part] = "an extra part (with)"
			count++
		}
		for _, l := range s.Layers {
			for k, it := range l.Items {
				if !it.touches(p) {
					continue
				}
				count++
				key, what := it.Part, "part"
				if it.Block != "" {
					key, what = blockItemKey(it.Block), "block"
				}
				if by, dup := used[key]; dup {
					return fmt.Errorf("%s: the %s %q is drawn twice (%s and %s item #%d)", name(p, i), what, strings.TrimPrefix(key, "block "), by, layerName(l), k+1)
				}
				used[key] = fmt.Sprintf("%s item #%d", layerName(l), k+1)
			}
		}
		if count > MaxPageParts {
			return fmt.Errorf("%s: its page would draw %d parts, the most is %d", name(p, i), count, MaxPageParts)
		}
	}
	return nil
}

// layerItemsFor lists, in drawing order, the items that show in the piece.
func (s Script) layerItemsFor(p Piece) []LayerItem {
	var out []LayerItem
	for _, l := range s.Layers {
		for _, it := range l.Items {
			if it.touches(p) {
				out = append(out, it)
			}
		}
	}
	return out
}

// SceneLayer is an item of a layer as a scene draws it, with its bounds in song seconds.
type SceneLayer struct {
	// Block is set for an item that draws a block; Part is then empty.
	Block   *SceneBlock
	Part    Part
	Params  map[string]any
	From    float64
	To      float64
	Animate Animate
}

// layerSlot says where a layer item sits in the page: its wrapper's number and its bounds in song seconds.
type layerSlot struct {
	N        int
	From, To float64
}

// layerSeek replaces, in the page's seek, the end of the loop over the parts: it shows each layer item inside its range
// and calls its seek with the seconds from the item's start. The song time is where the piece starts plus t.
const layerSeek = `
    var LY = window.__layers || [], abs = ((window.__scene && window.__scene.song_start) || 0) + t;
    for (var li = 0; li < LY.length; li++) {
      var on = abs >= LY[li].from - 1e-9 && abs < LY[li].to;
      LY[li].el.style.display = on ? 'contents' : 'none';
      if (on && LY[li].seek) LY[li].seek(abs - LY[li].from, beat);
    }`

// checkLaneItem is what every lane of items over the song asks of an item (a layer's, the video track's): it ends after it starts, it is
// inside the song (0 to songEnd), and it does not begin before the item that came before it ended (prevEnd, negative for the first).
// n names the item in the message; a is the lane as a noun phrase ("a layer", "the video track").
func checkLaneItem(n string, i int, start, end, prevEnd, songEnd float64, a string) error {
	if end <= start {
		return fmt.Errorf("%s ends before it starts (%.2f s to %.2f s)", n, start, end)
	}
	if start < -coverTolerance || end > songEnd+coverTolerance {
		return fmt.Errorf("%s (%.2f s to %.2f s) is outside the song (0 s to %.2f s)", n, start, end, songEnd)
	}
	if i > 0 && start < prevEnd-coverTolerance {
		return fmt.Errorf("%s overlaps item #%d (the items of %s follow one another, in order)", n, i, a)
	}
	return nil
}

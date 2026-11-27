package musicmotion

// scrim.go: the scrim of a track item that follows the clip (spec music-motion, "The scrim of an item follows the clip behind the verse").
// The contrast of a design is measured against its own background colour; over a video the verse is read on the clip, so the clip's
// brightness behind the verse is measured and the scrim, which is the design's colour laid over the clip, is raised until the accent reads.

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"

	"veritmo/internal/video/assemble"
)

// LocalScrim is a strip of the design's colour over the band where the verse falls (see assemble.LocalScrimStrips).
type LocalScrim = assemble.LocalScrim

// MinVerseContrast is the contrast of the accent against what the verse is read on from which an item needs no more scrim: the 3:1 the design
// asks of its accent against its own background.
const MinVerseContrast = 3.0

// scrimStep is how much a local scrim grows each time it is raised, and MaxLocalScrim the most it may reach (the most any scrim has).
const (
	scrimStep     = 0.05
	MaxLocalScrim = 0.8
)

// LuminanceFunc is the mean brightness (0 to 255, the grey of the picture) of the band of the clip where the verse is drawn, over the part
// of the clip an item plays (from clipStart to clipEnd, or the end of the file when clipEnd is nil).
type LuminanceFunc func(ctx context.Context, path string, clipStart float64, clipEnd *float64, band string) (float64, error)

// verseBox is where the verse is in the frame, as fractions: the width the page gives it (left 7 %, right 7 %) and the rows its lines take
// at the top, the centre or the bottom of the frame.
func verseBox(band string) (y0, h float64) {
	from, to := assemble.VerseRows(band)
	return from, to - from
}

var lumCache sync.Map

// ClipBandLuminance reads the clip with ffmpeg: one picture a second of the part the item plays (at most 24), the band where the verse is
// cropped, scaled to a few pixels and made grey; the answer is their mean. It is the LuminanceFunc of a server with ffmpeg.
func ClipBandLuminance(ctx context.Context, path string, clipStart float64, clipEnd *float64, band string) (float64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	end := -1.0
	if clipEnd != nil {
		end = *clipEnd
	}
	key := fmt.Sprintf("%s|%d|%d|%v|%v|%s", path, fi.Size(), fi.ModTime().UnixNano(), clipStart, end, band)
	if v, ok := lumCache.Load(key); ok {
		return v.(float64), nil
	}
	y0, h := verseBox(band)
	args := []string{"-v", "error", "-ss", strconv.FormatFloat(clipStart, 'f', 3, 64)}
	if clipEnd != nil && *clipEnd > clipStart {
		args = append(args, "-t", strconv.FormatFloat(*clipEnd-clipStart, 'f', 3, 64))
	}
	args = append(args, "-i", filepath.Clean(path),
		"-vf", fmt.Sprintf(`select='isnan(prev_selected_t)+gte(t-prev_selected_t\,1)',crop=iw*0.86:ih*%g:iw*0.07:ih*%g,scale=16:4:flags=area,format=gray`, h, y0),
		"-fps_mode", "vfr", "-frames:v", "24", "-f", "rawvideo", "-")
	raw, err := exec.CommandContext(ctx, "ffmpeg", args...).Output()
	if err != nil {
		return 0, fmt.Errorf("cannot measure the clip %s: %w", filepath.Base(path), err)
	}
	if len(raw) == 0 {
		return 0, fmt.Errorf("cannot measure the clip %s: no picture", filepath.Base(path))
	}
	sum := 0
	for _, b := range raw {
		sum += int(b)
	}
	mean := float64(sum) / float64(len(raw))
	lumCache.Store(key, mean)
	return mean, nil
}

// greyOf is the grey (0 to 255) ffmpeg makes of a hex colour, the way it makes the clip's pictures grey (BT.601 over the coded values).
func greyOf(hex string) float64 {
	if !hexColour.MatchString(hex) {
		return 0
	}
	r, g, b := hexToRGB(hex)
	return 255 * (0.299*r + 0.587*g + 0.114*b)
}

// groundHex is the colour the verse is read on: the clip's grey with the scrim colour laid over it by the scrim, as the renderer lays it
// (on the coded values, not in light).
func groundHex(clipGrey, scrimGrey, scrim float64) string {
	y := int(math.Round((1-scrim)*clipGrey + scrim*scrimGrey))
	if y < 0 {
		y = 0
	} else if y > 255 {
		y = 255
	}
	return fmt.Sprintf("#%02x%02x%02x", y, y, y)
}

// EffectiveScrims says what the render lays over each item of the track so the verse reads (spec music-motion, "The scrim of an item follows the
// clip behind the verse"). The global scrim of an item stays as it is (its own, or DefaultScrim). An item with a scrim of its own is never given
// more: it gets a note when the accent reads under MinVerseContrast on it. An item with none gets, for each band where the verse of a piece
// that touches it falls and the accent reads under MinVerseContrast against the clip behind it, a local scrim (VideoItem.Local, derived, never
// saved) raised by scrimStep from scrimStep up to MaxLocalScrim until the accent of every piece with its verse in that band reads; one that still
// does not keeps MaxLocalScrim and gets the note. A clip that cannot be measured (no ffmpeg) is left as it is, with no note. The script it is
// given is not changed.
func EffectiveScrims(ctx context.Context, s Script, d Design, sources string, lum LuminanceFunc) (Script, []VerseNote) {
	if len(s.Video) == 0 || lum == nil {
		return s, nil
	}
	out := s
	out.Video = append([]VideoItem(nil), s.Video...)
	scrimBG, _, _ := d.Colours(d.DefaultMode)
	scrimGrey := greyOf(scrimBG)
	var notes []VerseNote
	for i, it := range out.Video {
		out.Video[i].Local = nil
		spots := map[string][]string{} // band -> the accents of the verses that fall in it
		for _, p := range s.Pieces {
			if p.Part == "" || len(p.Lines) == 0 || !it.touchesPiece(p) {
				continue
			}
			b := verseBand(d, p)
			spots[b] = append(spots[b], d.With(p.Design).Accent)
		}
		if len(spots) == 0 {
			continue
		}
		grey := map[string]float64{}
		for band := range spots {
			g, err := lum(ctx, filepath.Join(sources, it.Clip), derefOr(it.ClipStart, 0), it.ClipEnd, band)
			if err != nil {
				grey = nil
				break
			}
			grey[band] = g
		}
		if grey == nil {
			continue
		}
		global := DefaultScrim
		if it.Scrim != nil {
			global = *it.Scrim
		}
		// the worst contrast of the accents that fall in a band, with the global scrim and a local one over it
		worst := func(band string, local float64) float64 {
			ground := (1-global)*grey[band] + global*scrimGrey
			ground = (1-local)*ground + local*scrimGrey
			low := math.Inf(1)
			for _, accent := range spots[band] {
				low = math.Min(low, Contrast(accent, groundHex(ground, ground, 0)))
			}
			return low
		}
		for _, band := range []string{VersePositionTop, VersePositionCenter, VersePositionBottom} {
			if _, ok := spots[band]; !ok {
				continue
			}
			c := worst(band, 0)
			if c >= MinVerseContrast {
				continue
			}
			if it.Scrim != nil { // the author's scrim is never changed, and no strip is laid on it
				notes = append(notes, VerseNote{Item: it.ID, Band: band, Contrast: math.Round(c*10) / 10})
				continue
			}
			local := 0.0
			for c < MinVerseContrast && local+scrimStep <= MaxLocalScrim+1e-9 {
				local = math.Round((local+scrimStep)*100) / 100
				c = worst(band, local)
			}
			if c < MinVerseContrast {
				notes = append(notes, VerseNote{Item: it.ID, Band: band, Contrast: math.Round(c*10) / 10})
			}
			out.Video[i].Local = append(out.Video[i].Local, LocalScrim{Band: band, Scrim: local})
		}
	}
	return out, notes
}

func derefOr(v *float64, def float64) float64 {
	if v == nil {
		return def
	}
	return *v
}

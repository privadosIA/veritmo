package musicmotion

// overvideo.go: whether a part leaves a video behind it visible. A part over a video is drawn on a transparent page; if it paints
// most of the frame opaque it hides the video, which defeats a background. The share of opaque pixels is measured, not declared:
// the manifest says over_video and this decides whether it is true (spec music-motion-parts, "A part says whether it can be over a video").

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"math"

	"veritmo/internal/music/songmap"
	"veritmo/internal/video/motion"

	"encoding/json"
)

// MaxOverVideoCoverage is the most of the frame a part may paint opaque and still be over a video.
const MaxOverVideoCoverage = 0.5

// opaqueAlpha is the alpha from which a pixel counts as opaque: a text edge or a soft glow does not hide a video, a solid shape does.
const opaqueAlpha = 0.5

// OverVideoMeasure is what drawing a part on a transparent page showed.
type OverVideoMeasure struct {
	// Coverage is the share of the frame (0 to 1) painted opaque, at the worst of the frames looked at.
	Coverage float64 `json:"coverage"`
	// At is the second of the frame where it was worst.
	At float64 `json:"at"`
	// Times are the seconds that were looked at: the first, the middle and the last frame of the scene.
	Times []float64 `json:"times"`
	// Bands is the share of each third of the frame painted opaque, the largest of the frames looked at for each third.
	Bands Bands `json:"bands"`
}

// Bands is how much of each horizontal third of the frame a part paints opaque, from 0 to 1 (spec music-motion-parts, "A part says how much of
// each band of the frame it occupies"). The verse is drawn in one of them (VersePosition).
type Bands struct {
	Top    float64 `json:"top"`
	Center float64 `json:"center"`
	Bottom float64 `json:"bottom"`
}

// The shares from which the verse's band counts as occupied by a part and as clear of it. They are apart on purpose: a band half full is
// neither, so it is not offered as the place to move the verse to.
const (
	BandOccupied = 0.2
	BandClear    = 0.1
	// bandTolerance is how far a band a manifest claims may be from the measure.
	bandTolerance = 0.1
)

// Share is the share of the band that holds the verse at position pos (VersePositionTop, VersePositionCenter or the bottom otherwise).
func (b Bands) Share(pos string) float64 {
	switch pos {
	case VersePositionTop:
		return b.Top
	case VersePositionCenter:
		return b.Center
	}
	return b.Bottom
}

// max is the bands with each third the larger of the two.
func (b Bands) max(o Bands) Bands {
	return Bands{Top: math.Max(b.Top, o.Top), Center: math.Max(b.Center, o.Center), Bottom: math.Max(b.Bottom, o.Bottom)}
}

// Validate says whether every share is a number from 0 to 1.
func (b Bands) Validate() error {
	for name, v := range map[string]float64{"top": b.Top, "center": b.Center, "bottom": b.Bottom} {
		if math.IsNaN(v) || v < 0 || v > 1 {
			return fmt.Errorf("bands.%s must be between 0 and 1, got %v", name, v)
		}
	}
	return nil
}

// differsFrom names the first band that is more than bandTolerance from the measured one, or "" when none is.
func (b Bands) differsFrom(measured Bands) string {
	for _, c := range []struct {
		name          string
		claimed, real float64
	}{{VersePositionTop, b.Top, measured.Top}, {VersePositionCenter, b.Center, measured.Center}, {VersePositionBottom, b.Bottom, measured.Bottom}} {
		if math.Abs(c.claimed-c.real) > bandTolerance {
			return c.name
		}
	}
	return ""
}

// OK says the part leaves the video visible.
func (m OverVideoMeasure) OK() bool { return m.Coverage <= MaxOverVideoCoverage }

// overVideoTimes are the frames looked at: the first, the middle and the last of a scene.
func overVideoTimes(duration float64) []float64 {
	last := duration - 1.0/30
	if last < 0 {
		last = 0
	}
	return []float64{0, duration / 2, last}
}

// MeasureOverVideo draws the part on a transparent page (with no verse and no word hit: they are the page's, not the part's) at the
// first, the middle and the last frame of the scene and measures the share of the frame it paints opaque. The scene is the part's
// standard check scene, or the one given.
func MeasureOverVideo(ctx context.Context, browser string, p Part, scene *Scene) (OverVideoMeasure, error) {
	sc, err := sceneOf(p, CheckOptions{Scene: scene})
	if err != nil {
		return OverVideoMeasure{}, err
	}
	if !p.OwnsVerse() {
		sc.Lines = nil
	}
	if !p.OwnsHit() {
		sc.Hit = nil
	}
	sc.Background = &VideoBackground{}
	sc.Width, sc.Height = 1280, 720
	html, err := BuildPage(sc)
	if err != nil {
		return OverVideoMeasure{}, err
	}
	song, _ := json.Marshal(songmap.Build(songmap.Input{Duration: sc.Duration, BPM: 120}))
	s, err := motion.Open(ctx, browser, motion.Page{HTML: html, Width: sc.Width, Height: sc.Height, Duration: sc.Duration, Transparent: true, Song: song}, motion.Limits{})
	if err != nil {
		return OverVideoMeasure{}, err
	}
	defer s.Close()
	m := OverVideoMeasure{Times: overVideoTimes(sc.Duration)}
	for _, at := range m.Times {
		raw, err := s.Frame(ctx, at)
		if err != nil {
			return OverVideoMeasure{}, err
		}
		bands, share, err := opaqueBands(raw)
		if err != nil {
			return OverVideoMeasure{}, err
		}
		m.Bands = m.Bands.max(bands)
		if share > m.Coverage {
			m.Coverage, m.At = share, at
		}
	}
	return m, nil
}

// opaqueShare is the share of the pixels of a PNG whose alpha is at least opaqueAlpha.
func opaqueShare(raw []byte) (float64, error) {
	_, share, err := opaqueBands(raw)
	return share, err
}

// opaqueBands is the share of the pixels of each third of a PNG (from the top) whose alpha is at least opaqueAlpha, and the share of
// the whole picture.
func opaqueBands(raw []byte) (Bands, float64, error) {
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return Bands{}, 0, fmt.Errorf("cannot read the frame: %w", err)
	}
	b := img.Bounds()
	if b.Dx()*b.Dy() == 0 {
		return Bands{}, 0, fmt.Errorf("the frame is empty")
	}
	var opaque, rows [3]int
	for y := b.Min.Y; y < b.Max.Y; y++ {
		third := (y - b.Min.Y) * 3 / b.Dy()
		rows[third] += b.Dx()
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); float64(a)/65535 >= opaqueAlpha {
				opaque[third]++
			}
		}
	}
	share := func(i int) float64 {
		if rows[i] == 0 {
			return 0
		}
		return float64(opaque[i]) / float64(rows[i])
	}
	return Bands{Top: share(0), Center: share(1), Bottom: share(2)}, float64(opaque[0]+opaque[1]+opaque[2]) / float64(b.Dx()*b.Dy()), nil
}

// OverVideoProblem is what is said about a part whose measure is over the limit: forVideo says the part is being used over a video
// (a piece with a background), otherwise its manifest claims it can be.
func OverVideoProblem(m OverVideoMeasure, forVideo bool) string {
	if forVideo {
		return fmt.Sprintf("the part paints %.0f %% of the frame opaque at %.1f s, so it hides the video behind the piece (the limit is half): draw only words, shapes and objects, with no background, panel or gradient over the whole frame", m.Coverage*100, m.At)
	}
	return fmt.Sprintf("the manifest says over_video but the part paints %.0f %% of a transparent frame opaque at %.1f s (the limit is half): remove over_video or stop the part painting a background over the whole frame", m.Coverage*100, m.At)
}

// WithOverVideo is the manifest with over_video set to on, the other fields as they were (in the order of their names): the program
// decides it, from what MeasureOverVideo showed, not the one who wrote the part.
func WithOverVideo(manifest []byte, on bool) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(manifest, &m); err != nil {
		return nil, fmt.Errorf("manifest.json is not valid JSON: %w", err)
	}
	if on {
		m["over_video"] = json.RawMessage("true")
	} else {
		delete(m, "over_video")
	}
	return json.Marshal(m)
}

// BandsProblem is what is said about a manifest whose bands are not what the drawing measured.
func BandsProblem(claimed, measured Bands, band string) string {
	return fmt.Sprintf("the manifest says bands %s but the part paints %s on a transparent frame (the %s band differs by more than %.1f): remove bands or write what was measured", bandsText(claimed), bandsText(measured), band, bandTolerance)
}

func bandsText(b Bands) string {
	return fmt.Sprintf("{top: %.2f, center: %.2f, bottom: %.2f}", b.Top, b.Center, b.Bottom)
}

// WithBands is the manifest with bands set to the measured ones, the other fields as they were: the program decides them, from what
// MeasureOverVideo showed (the shares are rounded to 0.01 so the manifest does not carry noise).
func WithBands(manifest []byte, b Bands) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(manifest, &m); err != nil {
		return nil, fmt.Errorf("manifest.json is not valid JSON: %w", err)
	}
	r := roundBand
	raw, err := json.Marshal(Bands{Top: r(b.Top), Center: r(b.Center), Bottom: r(b.Bottom)})
	if err != nil {
		return nil, err
	}
	m["bands"] = raw
	return json.Marshal(m)
}

func roundBand(v float64) float64 { return math.Round(v*100) / 100 }

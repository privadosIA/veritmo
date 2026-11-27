package music

// musicmotion_backgroundframe.go: the frame of a piece over the video track (spec music-motion, "A piece over a track can be seen without
// rendering"). It is composed the way the render composes it: the track's picture at that second (the clip mapped through the item's trim,
// speed and repetition, the scrim of the item, the colour of the design in a gap, the crossfade of two items), then the pages of the pieces
// over it, dissolving into one another where the pieces crossfade.

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"veritmo/internal/music/hyperframes"
	"veritmo/internal/music/musicmotion"
	"veritmo/internal/platform/runner"
	"veritmo/internal/video/assemble"
	"veritmo/internal/video/motion"
)

// clipLuminance measures the clip behind the verse (see musicmotion.EffectiveScrims). A test seam.
var clipLuminance musicmotion.LuminanceFunc = musicmotion.ClipBandLuminance

// withEffectiveScrims is the script with the scrim each item of its track really has (the render, the frame and the preview all take it from
// here, so they agree) and the notes about the items whose clip leaves the accent hard to read. Without a track or without ffmpeg it is the
// script as it was.
func (h *Handler) withEffectiveScrims(ctx context.Context, info *pieceInfo, script musicmotion.Script, d musicmotion.Design) (musicmotion.Script, []musicmotion.VerseNote) {
	if len(script.Video) == 0 || !ffmpegAvailable() {
		return script, nil
	}
	return musicmotion.EffectiveScrims(ctx, script, d, h.pieceSources(info.ID), clipLuminance)
}

// verseNotesOf is all the advice about a script: where its verses fall on their parts and which clips leave the accent hard to read.
func (h *Handler) verseNotesOf(ctx context.Context, info *pieceInfo, script musicmotion.Script, d musicmotion.Design) []musicmotion.VerseNote {
	notes := musicmotion.VerseNotes(h.mmCatalog(), script, d)
	_, items := h.withEffectiveScrims(ctx, info, script, d)
	return append(notes, items...)
}

// ffmpegAvailable says whether ffmpeg can be run here. A test seam.
var ffmpegAvailable = func() bool { _, err := exec.LookPath("ffmpeg"); return err == nil }

// clipFrame is the picture of the video at `at` seconds of the file, covering w x h the way the clip phase covers the frame. A test seam:
// nil uses ffmpeg.
func (h *Handler) clipFrame(ctx context.Context, path string, at float64, w, ht int) ([]byte, error) {
	if h.clipFrameSeam != nil {
		return h.clipFrameSeam(ctx, path, at, w, ht)
	}
	return runner.NewExecRunner().Run(ctx, "ffmpeg", []string{"-v", "error", "-ss", strconv.FormatFloat(at, 'f', 3, 64), "-i", path, "-frames:v", "1",
		"-vf", fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d", w, ht, w, ht), "-f", "image2pipe", "-c:v", "png", "-"})
}

// hexColour reads "#rrggbb" or "#rgb" (black for anything else).
func hexColour(s string) color.NRGBA {
	if len(s) > 0 && s[0] == '#' {
		s = s[1:]
	}
	if len(s) == 3 {
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil || len(s) != 6 {
		return color.NRGBA{A: 255}
	}
	return color.NRGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 255}
}

// gapColour is the colour of the gaps of the track and of its scrim: the design's background of its default mode.
func gapColour(d musicmotion.Design) color.NRGBA {
	bg, _, _ := d.Colours(d.DefaultMode)
	return hexColour(bg)
}

// itemPicture is the picture of item i of the track at second t of the song, with its scrim laid over it.
func (h *Handler) itemPicture(ctx context.Context, info *pieceInfo, spec musicmotion.ProjectSpec, i int, t float64, bg color.NRGBA) (*image.NRGBA, error) {
	it := spec.Script.Video[i]
	path := filepath.Join(h.pieceSources(info.ID), it.Clip)
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("the piece keeps no clip %q", it.Clip)
	}
	clip, err := h.clips.get(ctx, path, fi)
	if err != nil {
		return nil, fmt.Errorf("cannot read the clip %q: %w", it.Clip, err)
	}
	at, err := it.ClipTime(t, musicmotion.TrackBuildSeconds(spec.Script.Video, i), clip.Seconds)
	if err != nil {
		return nil, err
	}
	raw, err := h.clipFrame(ctx, path, at, info.Width, info.Height)
	if err != nil {
		return nil, err
	}
	src, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("cannot read the picture of the clip: %w", err)
	}
	out := image.NewNRGBA(image.Rect(0, 0, info.Width, info.Height))
	draw.Draw(out, out.Bounds(), src, src.Bounds().Min, draw.Src)
	scrim := 0.3
	if it.Scrim != nil {
		scrim = *it.Scrim
	}
	if scrim > 0 {
		c := bg
		c.A = uint8(scrim*255 + 0.5)
		draw.Draw(out, out.Bounds(), &image.Uniform{c}, image.Point{}, draw.Over)
	}
	// the local scrims, with the same strips the build lays with drawbox (assemble.LocalScrimStrips)
	for _, l := range it.Local {
		for _, st := range assemble.LocalScrimStrips(info.Height, l.Band, l.Scrim) {
			c := bg
			c.A = uint8(st.Alpha*255 + 0.5)
			draw.Draw(out, image.Rect(0, st.Y, info.Width, st.Y+st.H), &image.Uniform{c}, image.Point{}, draw.Over)
		}
	}
	return out, nil
}

// blend returns a over b with the weight w of b (0 is a, 1 is b): the dissolve of two opaque pictures.
func blend(a, b *image.NRGBA, w float64) *image.NRGBA {
	out := image.NewNRGBA(a.Bounds())
	draw.Draw(out, out.Bounds(), a, a.Bounds().Min, draw.Src)
	draw.DrawMask(out, out.Bounds(), b, b.Bounds().Min, &image.Uniform{color.Alpha{A: uint8(w*255 + 0.5)}}, image.Point{}, draw.Over)
	return out
}

// trackPicture is the picture of the video track at second t of the song: the item that holds it (dissolving from the one before while its
// crossfade runs), or the colour of the design in a gap.
func (h *Handler) trackPicture(ctx context.Context, info *pieceInfo, spec musicmotion.ProjectSpec, t float64) (*image.NRGBA, error) {
	bg := gapColour(spec.Design)
	items := spec.Script.Video
	cur := -1
	for i, it := range items {
		if t >= it.Start && t < it.End {
			cur = i
		}
	}
	if cur < 0 {
		out := image.NewNRGBA(image.Rect(0, 0, info.Width, info.Height))
		draw.Draw(out, out.Bounds(), &image.Uniform{bg}, image.Point{}, draw.Src)
		return out, nil
	}
	pic, err := h.itemPicture(ctx, info, spec, cur, t, bg)
	if err != nil {
		return nil, err
	}
	if f := musicmotion.TrackFade(items, cur); f > 0 && t < items[cur].Start+f {
		prev, err := h.itemPicture(ctx, info, spec, cur-1, t, bg)
		if err != nil {
			return nil, err
		}
		return blend(prev, pic, (t-items[cur].Start)/f), nil
	}
	return pic, nil
}

// trackPictureAt is trackPicture as a PNG (what the review lays a piece's page over).
func (h *Handler) trackPictureAt(ctx context.Context, info *pieceInfo, spec musicmotion.ProjectSpec, t float64) ([]byte, error) {
	img, err := h.trackPicture(ctx, info, spec, t)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// pieceLayer is the frame of the page of the piece at index i at second t of the song, drawn by Chrome (on transparent when the piece is a layer).
func (h *Handler) pieceLayer(ctx context.Context, browser string, info *pieceInfo, spec musicmotion.ProjectSpec, i int, t float64) (image.Image, error) {
	p := spec.Script.Pieces[i]
	pages, err := musicmotion.BuildPages(spec, []string{p.ID})
	if err != nil {
		return nil, err
	}
	pg := pages[0]
	page := motion.Page{HTML: pg.HTML, Width: info.Width, Height: info.Height, Duration: pg.Seconds, Song: pg.Song, Transparent: len(spec.Script.ItemsOver(p)) > 0}
	hyperframes.Extend(&page)
	s, err := motion.Open(ctx, browser, page, motion.Limits{})
	if err != nil {
		return nil, err
	}
	defer s.Close()
	raw, err := s.Frame(ctx, t-p.Start)
	if err != nil {
		return nil, err
	}
	return png.Decode(bytes.NewReader(raw))
}

// composedFrame is the frame of the video at second t of the song for a script with a video track: the track's picture with the pages of the
// pieces over it, as the render stacks them. The piece that holds t (index k) is drawn at full weight, or dissolving in over the one before
// while its crossfade into it runs.
func (h *Handler) composedFrame(ctx context.Context, browser string, info *pieceInfo, spec musicmotion.ProjectSpec, k int, t float64) ([]byte, error) {
	base, err := h.trackPicture(ctx, info, spec, t)
	if err != nil {
		return nil, err
	}
	type layer struct {
		piece  int
		weight float64
	}
	layers := []layer{{k, 1}}
	if fades := musicmotion.Crossfades(spec.Script, spec.Design); k > 0 && fades[k] > 0 && t < spec.Script.Pieces[k].Start+fades[k] {
		w := (t - spec.Script.Pieces[k].Start) / fades[k]
		layers = []layer{{k - 1, 1 - w}, {k, w}}
	}
	for _, l := range layers {
		if spec.Script.Pieces[l.piece].Blender != nil {
			continue // drawn by Blender: not part of the composed preview
		}
		img, err := h.pieceLayer(ctx, browser, info, spec, l.piece, t)
		if err != nil {
			return nil, err
		}
		draw.DrawMask(base, base.Bounds(), img, img.Bounds().Min, &image.Uniform{color.Alpha{A: uint8(l.weight*255 + 0.5)}}, image.Point{}, draw.Over)
	}
	var b bytes.Buffer
	if err := png.Encode(&b, base); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

package musicmotion

// background.go: a video behind a piece (spec music-motion, "A piece of the script can have a video behind it"). The video is a file the
// piece keeps in its sources folder; the piece says which one, which part of it, how fast and how much of the design's colour is laid
// over it so the verse reads.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
	"veritmo/internal/video/assemble"
)

// PieceBackground is the video behind a piece. ClipStart, ClipEnd and ClipSpeed mean what they mean in a scene with `clip`; Scrim is how
// much of the design's --bg colour is laid over the video (nil: DefaultScrim).
type PieceBackground struct {
	Clip      string   `json:"clip"`
	ClipStart *float64 `json:"clip_start,omitempty"`
	ClipEnd   *float64 `json:"clip_end,omitempty"`
	ClipSpeed *float64 `json:"clip_speed,omitempty"`
	Scrim     *float64 `json:"scrim,omitempty"`
}

// ScrimValue is the scrim with the default filled in.
func (b PieceBackground) ScrimValue() float64 {
	if b.Scrim == nil {
		return DefaultScrim
	}
	return *b.Scrim
}

// ClipExts are the kinds of video a piece keeps.
var ClipExts = []string{".mp4", ".mov", ".webm", ".mkv"}

var reClipName = regexp.MustCompile(`(?i)^[a-z0-9][a-z0-9._-]{0,62}\.(mp4|mov|webm|mkv)$`)

// ValidClipName says whether name can be the name of a video a piece keeps: a file name, not a path, with one of ClipExts.
func ValidClipName(name string) bool { return reClipName.MatchString(name) }

// ClipInfo is what is known about a video file.
type ClipInfo struct {
	Seconds float64 `json:"seconds"`
	Width   int     `json:"width"`
	Height  int     `json:"height"`
	FPS     float64 `json:"fps"`
}

// ErrNoVideoStream is the answer for a file that ffprobe reads but that has no picture.
var ErrNoVideoStream = fmt.Errorf("the file has no video stream")

// ProbeClip reads a video file. It is a variable so that tests (and servers without ffprobe) can put their own; the default asks ffprobe.
var ProbeClip = probeClipWithFFprobe

func probeClipWithFFprobe(ctx context.Context, path string) (ClipInfo, error) {
	out, err := runner.NewExecRunner().Run(ctx, "ffprobe", []string{"-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height,avg_frame_rate,r_frame_rate:format=duration", "-of", "json", path})
	if err != nil {
		return ClipInfo{}, err
	}
	return parseClipInfo(out)
}

func parseClipInfo(raw []byte) (ClipInfo, error) {
	var p struct {
		Streams []struct {
			Width  int    `json:"width"`
			Height int    `json:"height"`
			Avg    string `json:"avg_frame_rate"`
			R      string `json:"r_frame_rate"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return ClipInfo{}, fmt.Errorf("cannot read what ffprobe said: %w", err)
	}
	if len(p.Streams) == 0 || p.Streams[0].Width <= 0 || p.Streams[0].Height <= 0 {
		return ClipInfo{}, ErrNoVideoStream
	}
	s := p.Streams[0]
	secs, err := strconv.ParseFloat(p.Format.Duration, 64)
	if err != nil || secs <= 0 {
		return ClipInfo{}, fmt.Errorf("the file has no length")
	}
	fps := parseRate(s.Avg)
	if fps <= 0 {
		fps = parseRate(s.R)
	}
	return ClipInfo{Seconds: secs, Width: s.Width, Height: s.Height, FPS: fps}, nil
}

// parseRate reads "16/1" or "30000/1001" (0 when it is not a rate).
func parseRate(s string) float64 {
	num, den, ok := strings.Cut(s, "/")
	if !ok {
		f, _ := strconv.ParseFloat(s, 64)
		return f
	}
	n, err1 := strconv.ParseFloat(num, 64)
	d, err2 := strconv.ParseFloat(den, 64)
	if err1 != nil || err2 != nil || d == 0 {
		return 0
	}
	return n / d
}

// clipTrimTolerance is how far past the end of a file a clip_end may be (a rounded length).
const clipTrimTolerance = 0.05

// validateBackgrounds checks the background of each piece: the clip the piece keeps, the part of it, the speed, the scrim, and that
// everything the piece draws leaves the video visible. It needs the piece's sources folder (WithSources): without it no clip can be
// named.
func (s Script) validateBackgrounds(cat Catalog) error {
	for i, p := range s.Pieces {
		if p.Background == nil {
			continue
		}
		n := name(p, i)
		b := *p.Background
		if p.Blender != nil {
			return fmt.Errorf("%s: a 3D piece (blender) cannot have a background: it is drawn by Blender and not from a part", n)
		}
		if err := validateClipFields(n, "background.", b, s.sources); err != nil {
			return err
		}
		if p.Stage != nil && p.Stage.Background != "" {
			return fmt.Errorf("%s: a piece over a video cannot have a stage background (%q): it would hide the video", n, p.Stage.Background)
		}
		if err := s.overVideoParts(cat, p, n); err != nil {
			return err
		}
	}
	return nil
}

// validateClipFields checks the fields a clip played behind pieces has, in a piece's background or in an item of the video track (prefix is
// the field's path in the message: "background." or ""): a name the piece keeps, a speed, a scrim, and a part of the file that is inside it.
// n names what is being checked. It needs the piece's sources folder (WithSources): without it no clip can be named.
func validateClipFields(n, prefix string, b PieceBackground, sources string) error {
	if !ValidClipName(b.Clip) {
		return fmt.Errorf("%s: %sclip %q must be the name of a video the piece keeps (a file name ending in %s)", n, prefix, b.Clip, strings.Join(ClipExts, ", "))
	}
	if sp := b.ClipSpeed; sp != nil && (*sp < config.MinClipSpeed || *sp > config.MaxClipSpeed) {
		return fmt.Errorf("%s: %sclip_speed must be between %v and %v, got %v", n, prefix, config.MinClipSpeed, config.MaxClipSpeed, *sp)
	}
	if st := b.ClipStart; st != nil && *st < 0 {
		return fmt.Errorf("%s: %sclip_start must be 0 or more, got %v", n, prefix, *st)
	}
	if b.ClipEnd != nil {
		start := 0.0
		if b.ClipStart != nil {
			start = *b.ClipStart
		}
		if *b.ClipEnd <= start {
			return fmt.Errorf("%s: %sclip_end must be greater than clip_start (%v), got %v", n, prefix, start, *b.ClipEnd)
		}
	}
	if sc := b.ScrimValue(); sc < 0 || sc > MaxScrim {
		return fmt.Errorf("%s: %sscrim must be between 0 and %v, got %v", n, prefix, MaxScrim, sc)
	}
	if sources == "" {
		return fmt.Errorf("%s: this piece keeps no videos, so %sclip %q cannot be named", n, prefix, b.Clip)
	}
	info, err := ProbeClip(context.Background(), filepath.Join(sources, b.Clip))
	switch {
	case os.IsNotExist(err):
		return fmt.Errorf("%s: the piece keeps no clip %q (put it with PUT .../backgrounds/%s)", n, b.Clip, b.Clip)
	case err != nil:
		return fmt.Errorf("%s: cannot read the clip %q: %w", n, b.Clip, err)
	}
	if b.ClipStart != nil && *b.ClipStart >= info.Seconds {
		return fmt.Errorf("%s: %sclip_start (%.2f s) is beyond the end of %q, which lasts %.2f s", n, prefix, *b.ClipStart, b.Clip, info.Seconds)
	}
	if b.ClipEnd != nil && *b.ClipEnd > info.Seconds+clipTrimTolerance {
		return fmt.Errorf("%s: %sclip_end (%.2f s) is beyond the end of %q, which lasts %.2f s", n, prefix, *b.ClipEnd, b.Clip, info.Seconds)
	}
	return nil
}

// overVideoParts refuses a part of the piece that does not leave the video visible: the main part, its extras and the parts of the
// layer items that show in the piece. The blocks of a layer are not parts of the catalog and are not measured.
func (s Script) overVideoParts(cat Catalog, p Piece, n string) error {
	refuse := func(part, what string) error {
		return fmt.Errorf("%s: the part %q (%s) is not marked over_video: it can hide the video behind the piece; choose a part that leaves it visible (GET /api/music-motion/catalog lists them)", n, part, what)
	}
	if main, ok := cat.Get(p.Part); ok && !main.OverVideo {
		return refuse(p.Part, "the main part")
	}
	for _, w := range p.With {
		if wp, ok := cat.Get(w.Part); ok && !wp.OverVideo {
			return refuse(w.Part, "an extra part (with)")
		}
	}
	for _, it := range s.layerItemsFor(p) {
		if it.Block != "" {
			continue
		}
		if ip, ok := cat.Get(it.Part); ok && !ip.OverVideo {
			return refuse(it.Part, "a layer item")
		}
	}
	return nil
}

// ClipTime is the second of the video file that is on screen at second t of the scene that plays it (t counts from the start of the
// piece), as the render maps it: the part of the file (clip_start to clip_end) plays at clip_speed, or, when none is set, as fast as makes
// it fill the scene without being slowed by more than 2x; and it repeats from its start when it runs out. sceneSeconds is how long the
// scene lasts (the piece plus the fade into the next one: SceneSeconds) and fileSeconds how long the file lasts.
//
// It is the one place that knows this mapping: the preview takes the frame of the video from it, and the dashboard plays the same part.
func (b PieceBackground) ClipTime(t, sceneSeconds, fileSeconds float64) (float64, error) {
	start := 0.0
	if b.ClipStart != nil {
		start = *b.ClipStart
	}
	end := fileSeconds
	if b.ClipEnd != nil && *b.ClipEnd < end {
		end = *b.ClipEnd
	}
	length := end - start
	if length <= 0 || fileSeconds <= 0 {
		return 0, fmt.Errorf("the part of %q (%.2f s to %.2f s) is empty", b.Clip, start, end)
	}
	speed := 1.0
	if b.ClipSpeed != nil && *b.ClipSpeed > 0 {
		speed = *b.ClipSpeed
	} else {
		speed = 1 / assemble.AnimatedSlowdown(sceneSeconds, length)
	}
	if t < 0 {
		t = 0
	}
	return start + math.Mod(t*speed, length), nil
}

// SceneSeconds is how long the scene of the piece at index i lasts in the project of the script: the piece and the crossfade into the next
// one, so that the pieces land on the song when they are joined (see WriteProject).
func SceneSeconds(s Script, d Design, i int) float64 {
	p := s.Pieces[i]
	seconds := round3(p.End - p.Start)
	if fades := Crossfades(s, d); i+1 < len(fades) {
		seconds = round3(seconds + fades[i+1])
	}
	return seconds
}

// LayOver draws the PNG over (with its transparency) on the PNG under and answers the result as a PNG: the page of a piece drawn on
// transparent over the picture of the video behind it.
func LayOver(under, over []byte) ([]byte, error) {
	base, err := png.Decode(bytes.NewReader(under))
	if err != nil {
		return nil, fmt.Errorf("cannot read the picture of the video: %w", err)
	}
	top, err := png.Decode(bytes.NewReader(over))
	if err != nil {
		return nil, fmt.Errorf("cannot read the page: %w", err)
	}
	out := image.NewNRGBA(base.Bounds())
	draw.Draw(out, out.Bounds(), base, base.Bounds().Min, draw.Src)
	draw.Draw(out, out.Bounds(), top, top.Bounds().Min, draw.Over)
	var b bytes.Buffer
	if err := png.Encode(&b, out); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

package assemble

// track.go: the video track and the stack (spec assemble, "The video track is built from its items" and "The layers are stacked over the
// track with the song"). Every function here is a pure builder of ffmpeg arguments: the pipeline runs them.

import (
	"fmt"
	"math"
	"strings"

	"veritmo/internal/project/config"
)

// TrackItemSpec is one item of the video track to build: a clip (or the part of it that was cut), as long as Seconds, at the project's size
// and frame rate, with no sound.
type TrackItemSpec struct {
	// Part is the clip, or the part of it that the item plays (already cut), and PartSeconds how long it lasts.
	Part        string
	PartSeconds float64
	// Seconds is the length to build: the item's own plus the crossfade into the next one, so that the items land where they start.
	Seconds float64
	// Speed is how fast the part plays; 0 is the automatic fit of a scene's clip (slowed by at most 2x to fill Seconds, then repeated).
	Speed         float64
	Width, Height int
	// Scrim is the share (0 to 1) of ScrimColour ("#rrggbb") laid over the picture.
	Scrim       float64
	ScrimColour string
	// LocalScrims are strips of ScrimColour laid over the rows where the verse is drawn, over Scrim (see LocalScrimStrips). Empty for none, which
	// builds the item exactly as before.
	LocalScrims []LocalScrim
}

// LocalScrim is a strip of the scrim colour over the band of the frame where the verse is drawn (see LocalScrimStrips); it is the one of the
// project's configuration.
type LocalScrim = config.LocalScrim

// The geometry of a local scrim, as fractions of the frame's height: the rows the verse takes in each band and how far the strip fades out
// above and below them.
const (
	localScrimFeather = 0.06
	localScrimBoxes   = 64
)

// VerseRows are the rows (fractions of the height, from the top) the verse takes when it is drawn at band: what the page gives it at the
// bottom, and the top and centre of its layout.
func VerseRows(band string) (from, to float64) {
	switch band {
	case "top":
		return 0.06, 0.22
	case "center":
		return 0.42, 0.58
	}
	return 0.78, 0.94
}

// Strip is a band of rows (Y, H in pixels) laid with the opacity Alpha.
type Strip struct {
	Y, H  int
	Alpha float64
}

// LocalScrimStrips are the boxes that make a local scrim on a frame of the given height: a trapezoid, flat (opacity scrim) over the rows of the
// verse and fading to nothing over localScrimFeather above and below, clamped to the frame, in localScrimBoxes thin rows. It is the one
// definition of the strip: the build turns it into drawbox filters, the frame of the server into drawn rectangles, and a dashboard can
// repeat it from the same numbers.
func LocalScrimStrips(height int, band string, scrim float64) []Strip {
	if height <= 0 || scrim <= 0 {
		return nil
	}
	from, to := VerseRows(band)
	lo, hi := math.Max(0, from-localScrimFeather), math.Min(1, to+localScrimFeather)
	alphaAt := func(y float64) float64 {
		a := math.Min(1, math.Min((y-(from-localScrimFeather))/localScrimFeather, ((to+localScrimFeather)-y)/localScrimFeather))
		if a < 0 {
			return 0
		}
		return scrim * a
	}
	var out []Strip
	for i := 0; i < localScrimBoxes; i++ {
		y0 := int(math.Round((lo + (hi-lo)*float64(i)/localScrimBoxes) * float64(height)))
		y1 := int(math.Round((lo + (hi-lo)*float64(i+1)/localScrimBoxes) * float64(height)))
		mid := (lo + (hi-lo)*(float64(i)+0.5)/localScrimBoxes)
		if a := math.Round(alphaAt(mid)*1000) / 1000; y1 > y0 && a > 0 {
			out = append(out, Strip{Y: y0, H: y1 - y0, Alpha: a})
		}
	}
	return out
}

// BuildTrackItemArgs builds one item: the part retimed, repeated for as long as the item lasts, scaled and cropped to cover the frame, at the
// project's frame rate, with the scrim laid over it and no audio.
func BuildTrackItemArgs(it TrackItemSpec, framerate int, out string) []string {
	if framerate <= 0 {
		framerate = 30
	}
	factor := animatedFactor(Scene{AnimatedSpeed: it.Speed, Duration: it.Seconds, AnimatedDuration: it.PartSeconds})
	vf := []string{
		fmt.Sprintf("setpts=%.6f*PTS", factor),
		fmt.Sprintf("fps=%d", framerate),
		fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase", it.Width, it.Height),
		fmt.Sprintf("crop=%d:%d", it.Width, it.Height),
		"setsar=1", // clips come with their own pixel aspect ratio; xfade and concat refuse to join pictures that differ
		"scale=out_range=tv",
	}
	if it.Scrim > 0 && it.ScrimColour != "" {
		vf = append(vf, fmt.Sprintf("drawbox=x=0:y=0:w=iw:h=ih:color=0x%s@%.3f:t=fill", strings.TrimPrefix(it.ScrimColour, "#"), it.Scrim))
	}
	if it.ScrimColour != "" {
		for _, l := range it.LocalScrims {
			for _, st := range LocalScrimStrips(it.Height, l.Band, l.Scrim) {
				vf = append(vf, fmt.Sprintf("drawbox=x=0:y=%d:w=iw:h=%d:color=0x%s@%.3f:t=fill", st.Y, st.H, strings.TrimPrefix(it.ScrimColour, "#"), st.Alpha))
			}
		}
	}
	vf = append(vf, "format=yuv420p")
	return []string{"-y", "-stream_loop", "-1", "-i", it.Part, "-t", fmt.Sprintf("%.6f", it.Seconds), "-an",
		"-vf", strings.Join(vf, ","), "-c:v", "libx264", "-preset", "veryfast", "-crf", "18", "-pix_fmt", "yuv420p", out}
}

// TrackSegment is a piece of the joined track: a built item (Path) or a stretch of one colour (Lavfi, the source of ffmpeg that makes it).
type TrackSegment struct {
	Path     string
	Lavfi    string
	Duration float64
	// Fade is the crossfade from the segment before it (0 is a cut); the first segment's is ignored.
	Fade float64
}

// GapSource is the lavfi source of a stretch of one colour ("#rrggbb") of the given size, frame rate and length.
func GapSource(colour string, width, height, framerate int, seconds float64) string {
	return fmt.Sprintf("color=c=0x%s:s=%dx%d:r=%d:d=%.6f", strings.TrimPrefix(colour, "#"), width, height, framerate, seconds)
}

// BuildTrackJoinArgs joins the segments of the track into one video without sound: crossfades (xfade) where a segment has a Fade and cuts
// elsewhere, in one call that encodes once. As in the join of the scenes the segments are split into runs separated by cuts, each run is chained
// with xfade (the offset is the length of the run so far minus the fade) and the runs are concatenated; every segment has the size and the frame
// rate xfade needs.
func BuildTrackJoinArgs(segs []TrackSegment, framerate int, out string) []string {
	if framerate <= 0 {
		framerate = 30
	}
	args := []string{"-y"}
	for _, s := range segs {
		if s.Lavfi != "" {
			args = append(args, "-f", "lavfi", "-i", s.Lavfi)
		} else {
			args = append(args, "-i", s.Path)
		}
	}
	var filters, tails []string
	var cur string
	var length float64
	groups := 0
	flush := func() {
		if cur != "" {
			tails = append(tails, cur)
			groups++
		}
	}
	for i, s := range segs {
		if i == 0 || s.Fade <= 0 {
			flush()
			cur, length = fmt.Sprintf("[%d:v]", i), s.Duration
			continue
		}
		next := fmt.Sprintf("[v%d]", i)
		filters = append(filters, fmt.Sprintf("%s[%d:v]xfade=transition=fade:duration=%.3f:offset=%.3f%s", cur, i, s.Fade, length-s.Fade, next))
		cur = next
		length += s.Duration - s.Fade
	}
	flush()
	filters = append(filters, fmt.Sprintf("%sconcat=n=%d:v=1:a=0[vc]", strings.Join(tails, ""), groups))
	filters = append(filters, fmt.Sprintf("[vc]fps=%d,format=yuv420p[vout]", framerate))
	return append(args, "-filter_complex", strings.Join(filters, ";"), "-map", "[vout]", "-c:v", "libx264", "-preset", "veryfast", "-crf", "18", "-pix_fmt", "yuv420p", out)
}

// StackLayer is a scene's video laid over the track: a transparent video (Alpha: VP9 with alpha, or ProRes) or an opaque one, from Start
// seconds of the output, for Seconds; FadeIn and FadeOut are the dissolves of its alpha where it overlaps the layer before and after it.
type StackLayer struct {
	Path            string
	Start, Seconds  float64
	FadeIn, FadeOut float64
	Alpha           bool
}

// StackSpec is what the stack is made from: the joined track, the layers in order (the last on top) and the length and frame rate of the video.
type StackSpec struct {
	Track   string
	Layers  []StackLayer
	Seconds float64
	FPS     int
}

// silentInput is the audio the stack carries (the soundtrack of the project is mixed under it afterwards): silence of the length of the video.
func silentInput(seconds float64) []string {
	return []string{"-f", "lavfi", "-t", fmt.Sprintf("%.6f", seconds), "-i", SilentAudioSource}
}

// BuildStackArgs lays the layers over the track in one graph and one encode, with a silent stereo track as its sound (the project's song is mixed
// in afterwards, so it is the only sound). A VP9 file with alpha is read with the libvpx decoder, which keeps the alpha channel; every layer is made
// RGBA with its alpha faded where it overlaps its neighbours, shifted to its second of the output, and overlaid with the track below it.
func BuildStackArgs(sp StackSpec, out string) []string {
	fps := sp.FPS
	if fps <= 0 {
		fps = 30
	}
	args := []string{"-y", "-i", sp.Track}
	for _, l := range sp.Layers {
		if l.Alpha && strings.HasSuffix(l.Path, ".webm") {
			args = append(args, "-c:v", "libvpx-vp9")
		}
		args = append(args, "-i", l.Path)
	}
	args = append(args, silentInput(sp.Seconds)...)
	audio := len(sp.Layers) + 1
	var filters []string
	for i, l := range sp.Layers {
		chain := []string{fmt.Sprintf("[%d:v]format=yuva420p", i+1)}
		if l.FadeIn > 0 {
			chain = append(chain, fmt.Sprintf("fade=t=in:st=0:d=%.3f:alpha=1", l.FadeIn))
		}
		if l.FadeOut > 0 {
			chain = append(chain, fmt.Sprintf("fade=t=out:st=%.3f:d=%.3f:alpha=1", l.Seconds-l.FadeOut, l.FadeOut))
		}
		chain = append(chain, fmt.Sprintf("setpts=PTS-STARTPTS+%.6f/TB", l.Start))
		filters = append(filters, strings.Join(chain, ",")+fmt.Sprintf("[l%d]", i))
	}
	cur := "[0:v]"
	for i := range sp.Layers {
		next := fmt.Sprintf("[o%d]", i)
		filters = append(filters, fmt.Sprintf("%s[l%d]overlay=0:0:eof_action=pass:format=auto%s", cur, i, next))
		cur = next
	}
	filters = append(filters, fmt.Sprintf("%sfps=%d,format=yuv420p[v]", cur, fps))
	return append(args, "-filter_complex", strings.Join(filters, ";"), "-map", "[v]", "-map", fmt.Sprintf("%d:a", audio),
		"-t", fmt.Sprintf("%.6f", sp.Seconds), "-c:v", "libx264", "-preset", "medium", "-crf", "18", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "192k", "-ar", "44100", "-ac", "2", "-movflags", "+faststart", out)
}

package assemble

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func probeNumber(t *testing.T, file, entry string) float64 {
	t.Helper()
	out := strings.TrimSpace(run(t, "ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", entry, "-of", "csv=p=0", file))
	num, den, isRate := strings.Cut(strings.Split(out, ",")[0], "/") // a frame rate is "30/1"
	v, err := strconv.ParseFloat(num, 64)
	if err != nil {
		t.Fatalf("%s of %s: %q", entry, file, out)
	}
	if isRate {
		d, _ := strconv.ParseFloat(den, 64)
		v /= d
	}
	return v
}

func formatDuration(t *testing.T, file string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(strings.TrimSpace(run(t, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", file)), 64)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// pixelAt is the RGB of the pixel (x, y) of the frame at second t of a video (decoded as the alpha-keeping decoder when libvpx is asked for).
func pixelAt(t *testing.T, file string, at float64, x, y int, libvpx bool) [3]int {
	t.Helper()
	args := []string{"-nostdin", "-v", "error"}
	if libvpx {
		args = append(args, "-c:v", "libvpx-vp9")
	}
	args = append(args, "-ss", strconv.FormatFloat(at, 'f', 3, 64), "-i", file, "-frames:v", "1", "-vf", "format=rgb24,crop=1:1:"+strconv.Itoa(x)+":"+strconv.Itoa(y), "-f", "rawvideo", "-")
	out := run(t, "ffmpeg", args...)
	if len(out) < 3 {
		t.Fatalf("no pixel at %v in %s", at, file)
	}
	return [3]int{int(out[0]), int(out[1]), int(out[2])}
}

func near(a, b [3]int, tol int) bool {
	for i := range a {
		if d := a[i] - b[i]; d > tol || d < -tol {
			return false
		}
	}
	return true
}

func TestATrackItemIsBuiltAtTheLengthAndSizeWithNoSoundAndTheScrim(t *testing.T) {
	ffmpegOrSkip(t)
	dir := t.TempDir()
	clip := filepath.Join(dir, "clip.mp4")
	run(t, "ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "color=c=0x2060e0:s=640x360:r=16:d=2", "-f", "lavfi", "-i", "sine=frequency=880:duration=2", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", clip)
	spec := TrackItemSpec{Part: clip, PartSeconds: 2, Seconds: 5, Width: 320, Height: 180}
	plain := filepath.Join(dir, "plain.mp4")
	run(t, "ffmpeg", append([]string{"-v", "error"}, BuildTrackItemArgs(spec, 30, plain)...)...)
	if d := formatDuration(t, plain); d < 4.95 || d > 5.1 {
		t.Errorf("the item lasts %v s, want 5 (a 2 s clip is slowed by 2x and repeated)", d)
	}
	if w, h := probeNumber(t, plain, "stream=width"), probeNumber(t, plain, "stream=height"); w != 320 || h != 180 {
		t.Errorf("size %vx%v", w, h)
	}
	if audioStreams(t, plain) != 0 {
		t.Error("an item has no sound")
	}
	if fps := probeNumber(t, plain, "stream=r_frame_rate"); fps != 30 {
		t.Errorf("fps %v", fps)
	}
	// the scrim lays its colour over the picture: half red over blue gives a purple that is redder than the blue it was
	spec.Scrim, spec.ScrimColour = 0.5, "#ff0000"
	scrimmed := filepath.Join(dir, "scrim.mp4")
	run(t, "ffmpeg", append([]string{"-v", "error"}, BuildTrackItemArgs(spec, 30, scrimmed)...)...)
	a, b := pixelAt(t, plain, 1, 10, 10, false), pixelAt(t, scrimmed, 1, 10, 10, false)
	if b[0] < a[0]+60 || b[2] > a[2]-40 {
		t.Errorf("the scrim did not colour the picture: %v then %v", a, b)
	}
}

func TestTheTrackIsJoinedWithCutsFadesAndGaps(t *testing.T) {
	ffmpegOrSkip(t)
	dir := t.TempDir()
	mk := func(name, colour string, secs int) string {
		p := filepath.Join(dir, name)
		run(t, "ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "color=c="+colour+":s=320x180:r=30:d="+strconv.Itoa(secs), "-c:v", "libx264", "-pix_fmt", "yuv420p", p)
		return p
	}
	red, green := mk("red.mp4", "0xe02020", 3), mk("green.mp4", "0x20c040", 3)
	segs := []TrackSegment{
		{Lavfi: GapSource("#101020", 320, 180, 30, 1), Duration: 1},
		{Path: red, Duration: 3},
		{Path: green, Duration: 3, Fade: 1},
		{Lavfi: GapSource("#101020", 320, 180, 30, 1), Duration: 1},
	}
	out := filepath.Join(dir, "track.mp4")
	run(t, "ffmpeg", append([]string{"-v", "error"}, BuildTrackJoinArgs(segs, 30, out)...)...)
	// 1 + 3 + (3 - 1 of fade) + 1
	if d := formatDuration(t, out); d < 6.9 || d > 7.1 {
		t.Fatalf("the track lasts %v s, want 7", d)
	}
	if audioStreams(t, out) != 0 {
		t.Error("the track has no sound")
	}
	if p := pixelAt(t, out, 0.5, 5, 5, false); !near(p, [3]int{16, 16, 32}, 12) {
		t.Errorf("the gap at the start is its colour: %v", p)
	}
	if p := pixelAt(t, out, 2, 5, 5, false); !near(p, [3]int{224, 32, 32}, 20) {
		t.Errorf("the first item: %v", p)
	}
	if p := pixelAt(t, out, 4.5, 5, 5, false); !near(p, [3]int{32, 192, 64}, 20) {
		t.Errorf("the second item: %v", p)
	}
	if p := pixelAt(t, out, 3.5, 5, 5, false); near(p, [3]int{224, 32, 32}, 20) || near(p, [3]int{32, 192, 64}, 20) {
		t.Errorf("at the middle of the fade the picture is a mix of both: %v", p)
	}
	if p := pixelAt(t, out, 6.7, 5, 5, false); !near(p, [3]int{16, 16, 32}, 12) {
		t.Errorf("the gap at the end: %v", p)
	}
}

func TestTheLayersAreStackedOverTheTrackWithTheirAlphaAndTheSongsPlace(t *testing.T) {
	ffmpegOrSkip(t)
	dir := t.TempDir()
	track := filepath.Join(dir, "track.mp4")
	run(t, "ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "color=c=0x204060:s=320x180:r=30:d=8", "-c:v", "libx264", "-pix_fmt", "yuv420p", track)
	// a transparent layer with one opaque white box, and an opaque layer of another colour
	alpha := filepath.Join(dir, "alpha.webm")
	run(t, "ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "color=c=white:s=80x40:r=30:d=3,format=yuva420p,pad=320:180:100:60:color=black@0.0", "-c:v", "libvpx-vp9", "-pix_fmt", "yuva420p", "-auto-alt-ref", "0", alpha)
	opaque := filepath.Join(dir, "opaque.mp4")
	run(t, "ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "color=c=0xc02020:s=320x180:r=30:d=3", "-c:v", "libx264", "-pix_fmt", "yuv420p", opaque)
	sp := StackSpec{Track: track, Seconds: 8, FPS: 30, Layers: []StackLayer{
		{Path: alpha, Start: 1, Seconds: 3, Alpha: true, FadeOut: 0.5},
		{Path: opaque, Start: 5, Seconds: 3, FadeIn: 0.5},
	}}
	out := filepath.Join(dir, "stack.mp4")
	run(t, "ffmpeg", append([]string{"-v", "error"}, BuildStackArgs(sp, out)...)...)
	if d := formatDuration(t, out); d < 7.9 || d > 8.1 {
		t.Fatalf("lasts %v s, want 8", d)
	}
	if n := audioStreams(t, out); n != 1 {
		t.Errorf("the stack carries one sound track (silence, for the song to be mixed under): %d", n)
	}
	if p := pixelAt(t, out, 0.5, 120, 80, false); !near(p, [3]int{32, 64, 96}, 16) {
		t.Errorf("before the layer the track shows: %v", p)
	}
	if p := pixelAt(t, out, 2, 120, 80, false); !near(p, [3]int{255, 255, 255}, 24) {
		t.Errorf("the box of the layer is over the track at its second: %v", p)
	}
	if p := pixelAt(t, out, 2, 20, 20, false); !near(p, [3]int{32, 64, 96}, 16) {
		t.Errorf("where the layer is transparent the track shows: %v", p)
	}
	if p := pixelAt(t, out, 6.5, 20, 20, false); !near(p, [3]int{192, 32, 32}, 24) {
		t.Errorf("an opaque layer covers the track: %v", p)
	}
	if p := pixelAt(t, out, 5.2, 20, 20, false); near(p, [3]int{192, 32, 32}, 20) || near(p, [3]int{32, 64, 96}, 8) {
		t.Errorf("it dissolves in: %v", p)
	}
}

func TestTheStackAndTheJoinArgumentsAreTheOnesTheSpecAsksFor(t *testing.T) {
	args := strings.Join(BuildStackArgs(StackSpec{Track: "t.mp4", Seconds: 10, FPS: 30, Layers: []StackLayer{
		{Path: "a.webm", Start: 0, Seconds: 4, Alpha: true, FadeOut: 0.4},
		{Path: "b.mp4", Start: 3.6, Seconds: 4.4, FadeIn: 0.4},
	}}, "o.mp4"), " ")
	for _, want := range []string{"-c:v libvpx-vp9 -i a.webm", "-i b.mp4", "fade=t=out:st=3.600:d=0.400:alpha=1", "fade=t=in:st=0:d=0.400:alpha=1", "setpts=PTS-STARTPTS+3.600000/TB", "overlay=0:0:eof_action=pass", "-map 3:a", "-t 10.000000"} {
		if !strings.Contains(args, want) {
			t.Errorf("the stack arguments lack %q:\n%s", want, args)
		}
	}
	if strings.Contains(args, "-c:v libvpx-vp9 -i b.mp4") {
		t.Error("only a layer with alpha is read with the libvpx decoder")
	}
	join := strings.Join(BuildTrackJoinArgs([]TrackSegment{{Path: "a.mp4", Duration: 3}, {Path: "b.mp4", Duration: 3, Fade: 0.5}, {Path: "c.mp4", Duration: 2}}, 30, "o.mp4"), " ")
	for _, want := range []string{"xfade=transition=fade:duration=0.500:offset=2.500", "concat=n=2:v=1:a=0", "-i c.mp4"} {
		if !strings.Contains(join, want) {
			t.Errorf("the join arguments lack %q:\n%s", want, join)
		}
	}
	if strings.Contains(join, ":a=1") || strings.Contains(join, "acrossfade") {
		t.Error("the track has no sound")
	}
}

// A clip with its own pixel aspect ratio (a generated video often has one that is not 1:1) is built with the square one, so that the items can be
// crossfaded and joined with the colour of a gap: the real run of 2026-11-26 failed in xfade with SAR 3345:3344 against 1977:1976.
func TestTrackItemIsBuiltWithSquarePixels(t *testing.T) {
	args := strings.Join(BuildTrackItemArgs(TrackItemSpec{Part: "a.mp4", PartSeconds: 2, Seconds: 3, Width: 640, Height: 360}, 30, "o.mp4"), " ")
	if !strings.Contains(args, "crop=640:360,setsar=1,") {
		t.Fatalf("%s", args)
	}
}

func TestLocalScrimStripsAreATrapezoidOverTheRowsOfTheVerse(t *testing.T) {
	const h = 720
	strips := LocalScrimStrips(h, "bottom", 0.8)
	if len(strips) == 0 || len(strips) > localScrimBoxes {
		t.Fatalf("%d strips", len(strips))
	}
	// the rows of the verse (78 % to 94 %) have the whole opacity; above and below it grows and shrinks without a step
	from, to := VerseRows("bottom")
	full, last := 0, 0.0
	for i, st := range strips {
		if st.Alpha < 0 || st.Alpha > 0.8+1e-9 || st.H <= 0 || st.Y < 0 || st.Y+st.H > h {
			t.Fatalf("strip %d: %+v", i, st)
		}
		mid := (float64(st.Y) + float64(st.H)/2) / h
		if mid > from+0.01 && mid < to-0.01 && st.Alpha != 0.8 {
			t.Errorf("the verse rows have %.3f at %.2f", st.Alpha, mid)
		}
		if st.Alpha == 0.8 {
			full++
		}
		if i > 0 && st.Y != strips[i-1].Y+strips[i-1].H {
			t.Errorf("the strips are not adjacent at %d", i)
		}
		if i > 0 && i < len(strips)-1 && st.Alpha-last > 0.8/3 {
			t.Errorf("a step of %.2f in the opacity at strip %d", st.Alpha-last, i)
		}
		last = st.Alpha
	}
	if full == 0 {
		t.Error("no strip has the whole opacity")
	}
	if strips[0].Alpha >= strips[len(strips)/2].Alpha || strips[len(strips)-1].Alpha >= strips[len(strips)/2].Alpha {
		t.Error("the edges are not softer than the middle")
	}
	// the top band is clamped to the frame, the others sit inside it
	top := LocalScrimStrips(h, "top", 0.5)
	if top[0].Y != 0 || LocalScrimStrips(h, "center", 0.5)[0].Y == 0 {
		t.Errorf("the clamping: %+v", top[0])
	}
	if LocalScrimStrips(h, "bottom", 0) != nil || LocalScrimStrips(0, "bottom", 0.5) != nil {
		t.Error("nothing to draw gave strips")
	}
}

func TestAnItemWithNoLocalScrimIsBuiltWithTheCommandItAlwaysHad(t *testing.T) {
	spec := TrackItemSpec{Part: "a.mp4", PartSeconds: 2, Seconds: 5, Width: 320, Height: 180, Scrim: 0.3, ScrimColour: "#112233"}
	before := strings.Join(BuildTrackItemArgs(spec, 30, "o.mp4"), " ")
	spec.LocalScrims = []LocalScrim{}
	if strings.Join(BuildTrackItemArgs(spec, 30, "o.mp4"), " ") != before {
		t.Fatal("an empty list changed the command")
	}
	spec.LocalScrims = []LocalScrim{{Band: "bottom", Scrim: 0.5}}
	with := strings.Join(BuildTrackItemArgs(spec, 30, "o.mp4"), " ")
	if !strings.HasPrefix(with, before[:strings.Index(before, "format=yuv420p")]) || strings.Count(with, "drawbox") <= 1 || !strings.Contains(with, "color=0x112233@") {
		t.Fatalf("%s", with)
	}
}

// The strip, built by ffmpeg: a black clip with a white scrim colour is white behind the verse, black above it, and the opacity grows across the ramp.
func TestALocalScrimIsBuiltOverTheRowsOfTheVerseOnly(t *testing.T) {
	ffmpegOrSkip(t)
	dir := t.TempDir()
	clip := filepath.Join(dir, "black.mp4")
	run(t, "ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "color=c=black:s=320x180:r=10:d=2", "-c:v", "libx264", "-pix_fmt", "yuv420p", clip)
	out := filepath.Join(dir, "item.mp4")
	spec := TrackItemSpec{Part: clip, PartSeconds: 2, Seconds: 2, Width: 320, Height: 180, ScrimColour: "#ffffff", LocalScrims: []LocalScrim{{Band: "bottom", Scrim: 0.8}}}
	run(t, "ffmpeg", append([]string{"-v", "error"}, BuildTrackItemArgs(spec, 10, out)...)...)
	grey := func(frac float64) int { return pixelAt(t, out, 1, 10, int(frac*180), false)[0] }
	if g := grey(0.05); g > 30 {
		t.Errorf("above the strip the clip stays black: %d", g)
	}
	if g := grey(0.7); g > 30 {
		t.Errorf("at 70 %% of the height there is no scrim: %d", g)
	}
	if g := grey(0.86); g < 150 {
		t.Errorf("behind the verse the colour is laid at 0.8: %d", g)
	}
	// across the ramp (72 % to 78 %) the colour grows
	a, b, c := grey(0.73), grey(0.75), grey(0.77)
	if !(a <= b && b <= c) || c-a < 5 {
		t.Errorf("the ramp: %d %d %d", a, b, c)
	}
}

package music

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/music/musicmotion"
	"veritmo/internal/video/motion"
)

func f64p(v float64) *float64 { return &v }

// trackScript is the piece's script with its first two pieces drawn by `blank` and a video track of two items over them.
func trackScript(v PieceView, items ...musicmotion.VideoItem) musicmotion.Script {
	script := *v.Script
	script.Pieces = append([]musicmotion.Piece(nil), script.Pieces...)
	for i := 0; i < 2 && i < len(script.Pieces); i++ {
		p := script.Pieces[i]
		p.Part, p.Params, p.Animate, p.With, p.Mode, p.Hit = "blank", nil, nil, nil, "", nil
		script.Pieces[i] = p
	}
	script.Video = items
	return script
}

func saveScript(t *testing.T, s *testServer, base string, script musicmotion.Script) musicmotion.Script {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodPut, base+"/script", script)
	var out musicmotion.Script
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	return out
}

func TestClipFrameAndThumbs(t *testing.T) {
	fakeProbe(t)
	s := mmServer(t)
	v := newPiece(t, s, nil)
	base := "/api/music-motion/pieces/" + v.ID
	putRaw(t, s, base+"/backgrounds/intro.mp4", fakeVideo(6))
	var asked []float64
	s.mHandler.clipFrameSeam = func(ctx context.Context, path string, at float64, w, h int) ([]byte, error) {
		asked = append(asked, at)
		return solidPNG(w, h, color.NRGBA{10, 200, 30, 255}), nil
	}
	rec := doJSON(t, s.Handler(), http.MethodGet, base+"/backgrounds/intro.mp4/frame?t=2.5&width=320", nil)
	img, err := png.Decode(bytes.NewReader(rec.Body.Bytes()))
	if rec.Code != http.StatusOK || err != nil || img.Bounds().Dx() != 320 || img.Bounds().Dy() != 184 || len(asked) != 1 || asked[0] != 2.5 {
		t.Fatalf("a frame: %d %v %v", rec.Code, err, asked)
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, base+"/backgrounds/intro.mp4/frame?t=7", nil); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("t outside the clip: %d", rec.Code)
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, base+"/backgrounds/nope.mp4/frame?t=1", nil); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown clip: %d", rec.Code)
	}
	asked = nil
	rec = doJSON(t, s.Handler(), http.MethodGet, base+"/backgrounds/intro.mp4/thumbs?count=12&height=60", nil)
	strip, err := png.Decode(bytes.NewReader(rec.Body.Bytes()))
	if rec.Code != http.StatusOK || err != nil || rec.Header().Get("X-Frame-Count") != "12" || rec.Header().Get("X-Frame-Width") != "104" ||
		strip.Bounds().Dx() != 12*104 || strip.Bounds().Dy() != 60 || len(asked) != 12 {
		t.Fatalf("a strip: %d %v %v %v", rec.Code, err, rec.Header(), len(asked))
	}
	if asked[0] != 0.25 || asked[11] != 5.75 {
		t.Errorf("the pictures are in the middle of each stretch: %v", asked)
	}
	for _, bad := range []string{"count=1", "count=61", "height=10", "height=300", "count=x"} {
		if rec := doJSON(t, s.Handler(), http.MethodGet, base+"/backgrounds/intro.mp4/thumbs?"+bad, nil); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
	ffmpegAvailable = func() bool { return false }
	defer func() { ffmpegAvailable = func() bool { return true } }()
	if rec := doJSON(t, s.Handler(), http.MethodGet, base+"/backgrounds/intro.mp4/thumbs", nil); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no ffmpeg: %d", rec.Code)
	}
}

func TestSplitAndJoinTheTrack(t *testing.T) {
	fakeProbe(t)
	s := mmServer(t)
	v := newPiece(t, s, nil)
	base := "/api/music-motion/pieces/" + v.ID
	putRaw(t, s, base+"/backgrounds/intro.mp4", fakeVideo(30))
	putRaw(t, s, base+"/backgrounds/other.mp4", fakeVideo(30))
	p0 := v.Script.Pieces[0]
	whole := musicmotion.VideoItem{ID: "v1", Clip: "intro.mp4", Start: p0.Start, End: p0.End}
	saved := saveScript(t, s, base, trackScript(v, whole))
	at := p0.Start + 3
	rec := doJSON(t, s.Handler(), http.MethodPost, base+"/video/split", map[string]any{"item": "v1", "at": at})
	var split musicmotion.Script
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &split) != nil || len(split.Video) != 2 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	a, b := split.Video[0], split.Video[1]
	if a.ID != "v1-a" || b.ID != "v1-b" || a.End != at || b.Start != at || b.ClipStart == nil || *b.ClipStart < 2.99 || *b.ClipStart > 3.01 {
		t.Fatalf("split: %+v", split.Video)
	}
	if got := pieceOf(t, s, v.ID); len(got.Script.Video) != 2 {
		t.Errorf("it is saved: %+v", got.Script.Video)
	}
	// split then join gives the original track
	rec = doJSON(t, s.Handler(), http.MethodPost, base+"/video/join", map[string]any{"first": "v1-a", "second": "v1-b"})
	var joined musicmotion.Script
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &joined) != nil {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	want, _ := json.Marshal(saved.Video)
	got, _ := json.Marshal(joined.Video)
	if string(want) != string(got) {
		t.Errorf("the original track:\n%s\n%s", want, got)
	}
	// refusals leave the track as it was
	for name, tc := range map[string]struct {
		path string
		body map[string]any
		code int
	}{
		"a cut outside the item":      {"/video/split", map[string]any{"item": "v1", "at": p0.End + 1}, http.StatusUnprocessableEntity},
		"an unknown item":             {"/video/split", map[string]any{"item": "zzz", "at": at}, http.StatusNotFound},
		"an unknown item to join":     {"/video/join", map[string]any{"first": "v1", "second": "zzz"}, http.StatusNotFound},
		"an item to join with itself": {"/video/join", map[string]any{"first": "v1", "second": "v1"}, http.StatusUnprocessableEntity},
	} {
		if rec := doJSON(t, s.Handler(), http.MethodPost, base+tc.path, tc.body); rec.Code != tc.code {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	// two items of different clips cannot be joined
	p1 := v.Script.Pieces[1]
	saveScript(t, s, base, trackScript(v, musicmotion.VideoItem{ID: "x", Clip: "intro.mp4", Start: p0.Start, End: p0.End}, musicmotion.VideoItem{ID: "y", Clip: "other.mp4", Start: p0.End, End: p1.End}))
	if rec := doJSON(t, s.Handler(), http.MethodPost, base+"/video/join", map[string]any{"first": "x", "second": "y"}); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "different clips") {
		t.Errorf("different clips: %d %s", rec.Code, rec.Body)
	}
	if got := pieceOf(t, s, v.ID); len(got.Script.Video) != 2 || got.Script.Video[0].ID != "x" {
		t.Errorf("the track is as it was: %+v", got.Script.Video)
	}
}

func TestTheBackgroundShorthandIsSavedAsAnItem(t *testing.T) {
	fakeProbe(t)
	s := mmServer(t)
	v := newPiece(t, s, nil)
	base := "/api/music-motion/pieces/" + v.ID
	putRaw(t, s, base+"/backgrounds/intro.mp4", fakeVideo(30))
	script := scriptWithBackground(v, "intro.mp4")
	out := saveScript(t, s, base, script)
	if len(out.Video) != 1 || out.Video[0].ID != "bg-"+script.Pieces[0].ID || out.Video[0].Clip != "intro.mp4" || out.Pieces[0].Background != nil {
		t.Fatalf("the shorthand becomes the item and is not returned: %+v", out)
	}
	raw := doJSON(t, s.Handler(), http.MethodGet, base, nil).Body.String()
	if strings.Contains(raw, `"background"`) {
		t.Errorf("the saved script has no background: %s", raw)
	}
	// it overlaps an item of the track: refused
	both := script
	both.Video = []musicmotion.VideoItem{{ID: "v1", Clip: "intro.mp4", Start: script.Pieces[0].Start, End: script.Pieces[0].End}}
	if rec := doJSON(t, s.Handler(), http.MethodPut, base+"/script", both); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("overlap: %d %s", rec.Code, rec.Body)
	}
}

func TestTheRenderWithATrackNeedsFFmpegAndFFprobe(t *testing.T) {
	fakeProbe(t)
	s := mmServer(t)
	v := newPiece(t, s, nil)
	base := "/api/music-motion/pieces/" + v.ID
	putRaw(t, s, base+"/backgrounds/intro.mp4", fakeVideo(30))
	saveScript(t, s, base, scriptWithBackground(v, "intro.mp4"))
	ffmpegAvailable = func() bool { return false }
	if rec := doJSON(t, s.Handler(), http.MethodPost, base+"/render", nil); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "ffmpeg") {
		t.Errorf("no ffmpeg: %d %s", rec.Code, rec.Body)
	}
	ffmpegAvailable = func() bool { return true }
	ffprobeAvailable = func() bool { return false }
	if rec := doJSON(t, s.Handler(), http.MethodPost, base+"/render", nil); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "ffprobe") {
		t.Errorf("no ffprobe: %d %s", rec.Code, rec.Body)
	}
}

// The frame of a script with two clips: the colour of the design in a gap, the item at its own second, and the crossfade of two items.
func TestFrame_TheTrackWithAGapAndACrossfade(t *testing.T) {
	b, err := motion.Find("")
	if err != nil {
		t.Skip("no Chrome or Chromium on this machine")
	}
	fakeProbe(t)
	s := mmServer(t)
	s.motionBrowser = func() (motion.Browser, error) { return b, nil }
	v := newPiece(t, s, nil)
	base := "/api/music-motion/pieces/" + v.ID
	putRaw(t, s, base+"/backgrounds/red.mp4", fakeVideo(30))
	putRaw(t, s, base+"/backgrounds/green.mp4", fakeVideo(30))
	p0, p1 := v.Script.Pieces[0], v.Script.Pieces[1]
	mid := p0.End
	first := musicmotion.VideoItem{ID: "r", Clip: "red.mp4", Start: p0.Start + 1, End: mid, Scrim: f64p(0)}
	second := musicmotion.VideoItem{ID: "g", Clip: "green.mp4", Start: mid, End: p1.End, Scrim: f64p(0), TransitionSeconds: f64p(1)}
	saveScript(t, s, base, trackScript(v, first, second))
	s.mHandler.clipFrameSeam = func(ctx context.Context, path string, at float64, w, h int) ([]byte, error) {
		if filepath.Base(path) == "red.mp4" {
			return solidPNG(w, h, color.NRGBA{255, 0, 0, 255}), nil
		}
		return solidPNG(w, h, color.NRGBA{0, 255, 0, 255}), nil
	}
	corner := func(t0 float64) (r, g, bl uint32) {
		rec := doJSON(t, s.Handler(), http.MethodGet, fmt.Sprintf("%s/frame?t=%g", base, t0), nil)
		img, err := png.Decode(bytes.NewReader(rec.Body.Bytes()))
		if rec.Code != http.StatusOK || err != nil {
			t.Fatalf("t=%g: %d %v", t0, rec.Code, rec.Body.String()[:min(200, rec.Body.Len())])
		}
		r, g, bl, _ = img.At(3, 3).RGBA()
		return r >> 8, g >> 8, bl >> 8
	}
	if r, g, _ := corner(p0.Start + 0.3); r > 60 || g > 60 {
		t.Errorf("in the gap the picture is the design's background, not a clip: %d %d", r, g)
	}
	if r, g, _ := corner(p0.Start + 2); r < 200 || g > 40 {
		t.Errorf("inside the first item the clip shows: %d %d", r, g)
	}
	if r, g, _ := corner(mid + 0.5); r < 100 || r > 160 || g < 100 || g > 160 {
		t.Errorf("half way through the fade the pictures are mixed: %d %d", r, g)
	}
	if r, g, _ := corner(mid + 1.5); r > 40 || g < 200 {
		t.Errorf("after the fade the second clip shows: %d %d", r, g)
	}
}

// A song that ends before an item of the track does (its map is replaced by a shorter one): the script is kept, the warning names the item, and
// neither the save nor the render goes on with it until the item is moved or removed.
func TestAnItemOutsideAShorterSongIsNamedAndRefused(t *testing.T) {
	fakeProbe(t)
	s := mmServer(t)
	v := newPiece(t, s, nil)
	base := "/api/music-motion/pieces/" + v.ID
	putRaw(t, s, base+"/backgrounds/intro.mp4", fakeVideo(30))
	last := v.Script.Pieces[len(v.Script.Pieces)-1]
	script := trackScript(v, musicmotion.VideoItem{ID: "tail", Clip: "intro.mp4", Start: last.Start, End: last.End})
	n := len(script.Pieces) - 1
	script.Pieces[n].Part, script.Pieces[n].Params, script.Pieces[n].Animate, script.Pieces[n].With, script.Pieces[n].Mode, script.Pieces[n].Hit = "blank", nil, nil, nil, "", nil
	saved := saveScript(t, s, base, script)
	info, ok := s.mHandler.readPiece(v.ID)
	if !ok {
		t.Fatal("no piece")
	}
	info.ScriptEdited = true
	m, _ := s.mHandler.pieceMap(v.ID)
	m.Duration = last.Start + 1
	w := httptest.NewRecorder()
	warning, ok := s.mHandler.followTheMap(w, info, m, musicmotion.Design{})
	if !ok || !strings.Contains(warning, `"tail"`) || !strings.Contains(warning, "outside the new song") {
		t.Fatalf("the warning names the item: %q (%v)", warning, ok)
	}
	// the shorter song is what the piece has now: saving the same script is refused and so is rendering it
	if err := s.mHandler.savePieceMap(v.ID, m); err != nil {
		t.Fatal(err)
	}
	if rec := doJSON(t, s.Handler(), http.MethodPut, base+"/script", saved); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("saving: %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodPost, base+"/render", nil); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("rendering: %d %s", rec.Code, rec.Body)
	}
}

// With real ffmpeg and Chrome and no seam: a two-second clip under a longer item repeats, and the frames at four seconds of the song (the gap,
// inside the item, at the fade and over the repeat) are the colours the track has there, scrim included.
func TestFrame_TheRealTrackAtFourSeconds(t *testing.T) {
	b, err := motion.Find("")
	if err != nil {
		t.Skip("no Chrome or Chromium on this machine")
	}
	if !ffmpegAvailable() || !ffprobeAvailable() {
		t.Skip("no ffmpeg")
	}
	s := mmServer(t)
	s.motionBrowser = func() (motion.Browser, error) { return b, nil }
	v := newPiece(t, s, nil)
	base := "/api/music-motion/pieces/" + v.ID
	for name, colour := range map[string]string{"red.mp4": "0xff0000", "blue.mp4": "0x0000ff"} {
		tmp := filepath.Join(t.TempDir(), name)
		if out, err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "color=c="+colour+":s=320x180:r=16:d=2", "-pix_fmt", "yuv420p", tmp).CombinedOutput(); err != nil {
			t.Skipf("ffmpeg: %v %s", err, out)
		}
		raw, _ := os.ReadFile(tmp)
		putRaw(t, s, base+"/backgrounds/"+name, raw)
	}
	p0, p1 := v.Script.Pieces[0], v.Script.Pieces[1]
	first := musicmotion.VideoItem{ID: "r", Clip: "red.mp4", Start: p0.Start + 1, End: p0.End, Scrim: f64p(0)}
	second := musicmotion.VideoItem{ID: "b", Clip: "blue.mp4", Start: p0.End, End: p1.End, Scrim: f64p(0), TransitionSeconds: f64p(1)}
	saveScript(t, s, base, trackScript(v, first, second))
	px := func(at float64) [3]int {
		rec := doJSON(t, s.Handler(), http.MethodGet, fmt.Sprintf("%s/frame?t=%g", base, at), nil)
		img, err := png.Decode(bytes.NewReader(rec.Body.Bytes()))
		if rec.Code != http.StatusOK || err != nil {
			t.Fatalf("t=%g: %d %s", at, rec.Code, rec.Body.String()[:min(200, rec.Body.Len())])
		}
		r, g, bl, _ := img.At(3, 3).RGBA()
		return [3]int{int(r >> 8), int(g >> 8), int(bl >> 8)}
	}
	if c := px(p0.Start + 0.4); c[0] > 60 || c[2] > 60 {
		t.Errorf("a gap: %v", c)
	}
	if c := px(p0.Start + 1.5); c[0] < 220 || c[2] > 40 {
		t.Errorf("inside the first item: %v", c)
	}
	if c := px(p0.Start + 1 + 2.5); c[0] < 220 { // the clip lasts 2 s: at 2.5 s into the item it is repeating
		t.Errorf("over the repeat: %v", c)
	}
	if c := px(p0.End + 0.5); c[0] < 90 || c[0] > 170 || c[2] < 90 || c[2] > 170 {
		t.Errorf("at the fade the two clips are mixed: %v", c)
	}
}

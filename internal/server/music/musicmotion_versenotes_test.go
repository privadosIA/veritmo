package music

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/color"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/music/musicmotion"
	"veritmo/internal/video/motion"
)

// A verse drawn on the band its part occupies is told by the answer of the script and by the piece, with the band that is clear; nothing
// is refused and the script is saved as it was sent.
func TestVerseNotes_AreAnsweredByTheScriptAndThePiece(t *testing.T) {
	s := mmServer(t)
	v := newPiece(t, s, nil)
	script := *v.Script
	at := -1
	for i, p := range script.Pieces {
		if len(p.Lines) > 0 && p.Part != "" {
			at = i
			break
		}
	}
	if at < 0 {
		t.Skip("the test song has no piece with a verse")
	}
	id := script.Pieces[at].ID
	script.Pieces[at].Part, script.Pieces[at].Params, script.Pieces[at].Animate, script.Pieces[at].With = "flat-slabs", nil, nil, nil
	script.Pieces[at].Design = &musicmotion.DesignOverride{VerseLayout: &musicmotion.VerseLayout{Position: musicmotion.VersePositionTop}}

	rec := doJSON(t, s.Handler(), http.MethodPut, "/api/music-motion/pieces/"+v.ID+"/script", script)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var saved ScriptAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	find := func(notes []musicmotion.VerseNote) *musicmotion.VerseNote {
		for i := range notes {
			if notes[i].Piece == id {
				return &notes[i]
			}
		}
		return nil
	}
	n := find(saved.VerseNotes)
	if n == nil || n.Part != "flat-slabs" || n.Band != "top" || n.Free != "bottom" || n.Share < musicmotion.BandOccupied {
		t.Fatalf("the answer of the script: %+v", saved.VerseNotes)
	}
	if saved.Pieces[at].Design == nil || saved.Pieces[at].Design.VerseLayout.Position != "top" {
		t.Error("the script was not saved as it was sent")
	}

	got := doJSON(t, s.Handler(), http.MethodGet, "/api/music-motion/pieces/"+v.ID, nil)
	var pv PieceView
	if err := json.Unmarshal(got.Body.Bytes(), &pv); err != nil {
		t.Fatal(err)
	}
	if n := find(pv.VerseNotes); n == nil || n.Free != "bottom" {
		t.Fatalf("the piece: %+v", pv.VerseNotes)
	}

	// the author moves the verse to the clear band: the note is gone, the script is saved as always
	script.Pieces[at].Design = &musicmotion.DesignOverride{VerseLayout: &musicmotion.VerseLayout{Position: musicmotion.VersePositionBottom}}
	rec = doJSON(t, s.Handler(), http.MethodPut, "/api/music-motion/pieces/"+v.ID+"/script", script)
	var again ScriptAnswer
	_ = json.Unmarshal(rec.Body.Bytes(), &again)
	if rec.Code != http.StatusOK || find(again.VerseNotes) != nil {
		t.Fatalf("%d %+v", rec.Code, again.VerseNotes)
	}
}

// The local scrim an item really has is one definition for the render (the video_track of scenes.yaml), the preview (video[].local_scrims) and
// the frame (the picture of the track): a clip too dark for the accent keeps its global scrim and gets a strip over the verse's band, and the
// three say the same.
func TestEffectiveScrim_TheRenderThePreviewAndTheFrameAgree(t *testing.T) {
	b, err := motion.Find("")
	if err != nil {
		t.Skip("no Chrome or Chromium on this machine")
	}
	fakeProbe(t)
	s := mmServer(t)
	s.motionBrowser = func() (motion.Browser, error) { return b, nil }
	old := clipLuminance
	clipLuminance = func(context.Context, string, float64, *float64, string) (float64, error) { return 110, nil }
	defer func() { clipLuminance = old }()

	v := newPiece(t, s, nil)
	base := "/api/music-motion/pieces/" + v.ID
	putRaw(t, s, base+"/backgrounds/grey.mp4", fakeVideo(30))
	p0 := v.Script.Pieces[0]
	script := trackScript(v, musicmotion.VideoItem{ID: "g", Clip: "grey.mp4", Start: p0.Start, End: p0.End})
	if len(script.Pieces[0].Lines) == 0 {
		t.Skip("the first piece of the test song has no verse")
	}
	saveScript(t, s, base, script)
	s.mHandler.clipFrameSeam = func(ctx context.Context, path string, at float64, w, h int) ([]byte, error) {
		return solidPNG(w, h, color.NRGBA{110, 110, 110, 255}), nil
	}

	// the preview: the global scrim is the default and the strip is over the bottom band
	rec := doJSON(t, s.Handler(), http.MethodPost, base+"/preview", nil)
	var pv struct {
		Video []struct {
			Scrim       float64                  `json:"scrim"`
			LocalScrims []musicmotion.LocalScrim `json:"local_scrims"`
		} `json:"video"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &pv) != nil || len(pv.Video) != 1 {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String()[:min(300, rec.Body.Len())])
	}
	if pv.Video[0].Scrim != musicmotion.DefaultScrim || len(pv.Video[0].LocalScrims) != 1 || pv.Video[0].LocalScrims[0].Band != "bottom" || pv.Video[0].LocalScrims[0].Scrim <= 0 {
		t.Fatalf("the preview: %+v", pv.Video[0])
	}
	local := pv.Video[0].LocalScrims[0].Scrim

	// the saved script carries none of it: it is derived
	if got := doJSON(t, s.Handler(), http.MethodGet, base, nil); strings.Contains(got.Body.String(), "local_scrims") {
		t.Error("the script on disk or in the answer carries the derived local scrims")
	}

	// the render writes the same strip into the track
	rec = doJSON(t, s.Handler(), http.MethodPost, base+"/render", nil)
	var render struct {
		RunID string `json:"run_id"`
	}
	if rec.Code != http.StatusAccepted || json.Unmarshal(rec.Body.Bytes(), &render) != nil {
		t.Fatalf("render: %d %s", rec.Code, rec.Body)
	}
	waitUntil(t, func() bool {
		rr := doJSON(t, s.Handler(), http.MethodGet, "/api/runs/"+render.RunID, nil)
		return rr.Code == http.StatusOK && strings.Contains(rr.Body.String(), `"state":"succeeded"`)
	})
	raw, err := os.ReadFile(filepath.Join(s.pieceDir(v.ID), "scenes.yaml"))
	if err != nil || !strings.Contains(string(raw), fmt.Sprintf("local_scrims: [{band: bottom, scrim: %v}]", local)) {
		t.Fatalf("scenes.yaml has not the local scrim %v: %v\n%s", local, err, raw)
	}

	// the frame: outside the band the clip has the global scrim, in the rows of the verse the strip is laid over it
	st, _ := musicmotion.LoadDesignAt(s.mHandler.place(v.ID))
	bg, _, _ := st.Design.Colours(st.Design.DefaultMode)
	want := float64(hexColour(bg).R)
	rec = doJSON(t, s.Handler(), http.MethodGet, fmt.Sprintf("%s/frame?t=%g", base, p0.Start+0.5), nil)
	img, err := png.Decode(bytes.NewReader(rec.Body.Bytes()))
	if rec.Code != http.StatusOK || err != nil {
		t.Fatalf("frame: %d %v", rec.Code, err)
	}
	h := img.Bounds().Dy()
	at := func(frac float64) float64 {
		r, _, _, _ := img.At(3, int(frac*float64(h))).RGBA()
		return float64(r >> 8)
	}
	global := 110*(1-musicmotion.DefaultScrim) + want*musicmotion.DefaultScrim
	within := func(got, exp float64, what string) {
		if got < exp-4 || got > exp+4 {
			t.Errorf("%s: %v, want %.0f", what, got, exp)
		}
	}
	within(at(0.5), global, "outside the band the clip has the global scrim")
	within(at(0.86), global*(1-local)+want*local, "in the rows of the verse the strip is laid over it")
}

// An item whose clip cannot be measured keeps the default scrim: nothing is raised and nothing is said.
func TestEffectiveScrim_AClipThatCannotBeMeasuredKeepsTheDefault(t *testing.T) {
	fakeProbe(t)
	s := mmServer(t)
	old := clipLuminance
	clipLuminance = func(context.Context, string, float64, *float64, string) (float64, error) { return 0, context.Canceled }
	defer func() { clipLuminance = old }()
	v := newPiece(t, s, nil)
	base := "/api/music-motion/pieces/" + v.ID
	putRaw(t, s, base+"/backgrounds/grey.mp4", fakeVideo(30))
	p0 := v.Script.Pieces[0]
	saveScript(t, s, base, trackScript(v, musicmotion.VideoItem{ID: "g", Clip: "grey.mp4", Start: p0.Start, End: p0.End}))
	rec := doJSON(t, s.Handler(), http.MethodPost, base+"/preview", nil)
	var pv struct {
		Video []struct {
			Scrim float64 `json:"scrim"`
		} `json:"video"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &pv) != nil || len(pv.Video) != 1 || pv.Video[0].Scrim != musicmotion.DefaultScrim || strings.Contains(rec.Body.String(), "local_scrims") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String()[:min(300, rec.Body.Len())])
	}
}

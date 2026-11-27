package music

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"veritmo/internal/music/musicmotion"
	"veritmo/internal/server/generation"
	"veritmo/internal/video/motion"
)

// fakeVideo is the body of a "video" the fake ffprobe reads: the seconds it lasts.
func fakeVideo(seconds float64) []byte {
	return []byte("VIDEO:" + strconv.FormatFloat(seconds, 'f', -1, 64))
}

// fakeProbe replaces ffprobe: a file that holds "VIDEO:<seconds>" is an 832x480 clip at 16 fps, "NOVIDEO" has no picture, anything else cannot be read.
func fakeProbe(t *testing.T) {
	t.Helper()
	old, oldAvail := musicmotion.ProbeClip, ffprobeAvailable
	musicmotion.ProbeClip = func(ctx context.Context, path string) (musicmotion.ClipInfo, error) {
		raw, err := os.ReadFile(path)
		if err != nil {
			return musicmotion.ClipInfo{}, err
		}
		body := string(raw)
		switch {
		case strings.HasPrefix(body, "VIDEO:"):
			secs, _ := strconv.ParseFloat(strings.TrimPrefix(body, "VIDEO:"), 64)
			return musicmotion.ClipInfo{Seconds: secs, Width: 832, Height: 480, FPS: 16}, nil
		case body == "NOVIDEO":
			return musicmotion.ClipInfo{}, musicmotion.ErrNoVideoStream
		}
		return musicmotion.ClipInfo{}, errors.New("not a video")
	}
	ffprobeAvailable = func() bool { return true }
	t.Cleanup(func() { musicmotion.ProbeClip, ffprobeAvailable = old, oldAvail })
}

func backgroundsURL(id string) string { return "/api/music-motion/pieces/" + id + "/backgrounds" }

type bgList struct {
	Backgrounds []struct {
		Name    string   `json:"name"`
		Seconds float64  `json:"seconds"`
		Width   int      `json:"width"`
		Height  int      `json:"height"`
		FPS     float64  `json:"fps"`
		Source  string   `json:"source"`
		UsedBy  []string `json:"used_by"`
	} `json:"backgrounds"`
}

func listBackgrounds(t *testing.T, s *testServer, id string) bgList {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodGet, backgroundsURL(id), nil)
	var out bgList
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	return out
}

// scriptWithBackground is the piece's script with the first piece drawn by a part that leaves a video visible, over the clip.
func scriptWithBackground(v PieceView, clip string) musicmotion.Script {
	script := *v.Script
	script.Pieces = append([]musicmotion.Piece(nil), script.Pieces...)
	p := script.Pieces[0]
	p.Part, p.Params, p.Animate, p.With, p.Mode = "blank", nil, nil, nil, ""
	p.Background = &musicmotion.PieceBackground{Clip: clip}
	script.Pieces[0] = p
	return script
}

func TestPieceBackgrounds_UploadAndList(t *testing.T) {
	fakeProbe(t)
	s := mmServer(t)
	v := newPiece(t, s, nil)
	base := backgroundsURL(v.ID)
	rec := putRaw(t, s, base+"/intro.mp4", fakeVideo(6))
	var made map[string]any
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &made) != nil || made["name"] != "intro.mp4" || made["seconds"] != 6.0 || made["width"] != 832.0 || made["height"] != 480.0 || made["fps"] != 16.0 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	// the file is in the sources of the piece's project, where the render looks for a clip
	if raw, err := os.ReadFile(filepath.Join(s.pieceDir(v.ID), "sources", "intro.mp4")); err != nil || string(raw) != string(fakeVideo(6)) {
		t.Fatalf("not stored: %v", err)
	}
	list := listBackgrounds(t, s, v.ID)
	if len(list.Backgrounds) != 1 || list.Backgrounds[0].Name != "intro.mp4" || list.Backgrounds[0].Seconds != 6 || list.Backgrounds[0].Source != "upload" ||
		list.Backgrounds[0].UsedBy == nil || len(list.Backgrounds[0].UsedBy) != 0 {
		t.Fatalf("%+v", list)
	}
	// never replaced
	if rec := putRaw(t, s, base+"/intro.mp4", fakeVideo(3)); rec.Code != http.StatusConflict {
		t.Errorf("an existing name: %d", rec.Code)
	}
	// what is not a video is refused and not kept
	for name, c := range map[string]struct {
		body []byte
		code int
	}{"flat.mp4": {[]byte("NOVIDEO"), 422}, "text.mp4": {[]byte("hello"), 422}, "empty.mp4": {nil, 400}} {
		if rec := putRaw(t, s, base+"/"+name, c.body); rec.Code != c.code {
			t.Errorf("%s: %d %s, want %d", name, rec.Code, rec.Body, c.code)
		}
		if _, err := os.Stat(filepath.Join(s.pieceDir(v.ID), "sources", name)); err == nil {
			t.Errorf("%s was stored", name)
		}
	}
	for _, name := range []string{"x.avi", "x.mp3", "song.wav", ".hidden.mp4"} {
		if rec := putRaw(t, s, base+"/"+name, fakeVideo(2)); rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want a refusal", name, rec.Code)
		}
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, backgroundsURL("nope"), nil); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown piece: %d", rec.Code)
	}
	// no temporary file is left behind
	entries, _ := os.ReadDir(filepath.Join(s.pieceDir(v.ID), "sources"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".upload-") || strings.HasPrefix(e.Name(), ".copy-") {
			t.Errorf("a temporary file is left: %s", e.Name())
		}
	}
}

func TestPieceBackgrounds_NoFFprobeIs503(t *testing.T) {
	fakeProbe(t)
	ffprobeAvailable = func() bool { return false }
	s := mmServer(t)
	v := newPiece(t, s, nil)
	rec := putRaw(t, s, backgroundsURL(v.ID)+"/intro.mp4", fakeVideo(6))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "ffprobe") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestPieceBackgrounds_OneTheScriptUsesIsNotDeleted(t *testing.T) {
	fakeProbe(t)
	s := mmServer(t)
	v := newPiece(t, s, nil)
	base := backgroundsURL(v.ID)
	putRaw(t, s, base+"/intro.mp4", fakeVideo(30))
	putRaw(t, s, base+"/spare.mp4", fakeVideo(5))
	script := scriptWithBackground(v, "intro.mp4")
	if rec := doJSON(t, s.Handler(), http.MethodPut, "/api/music-motion/pieces/"+v.ID+"/script", script); rec.Code != http.StatusOK {
		t.Fatalf("a script with a background the piece keeps: %d %s", rec.Code, rec.Body)
	}
	pieceID := script.Pieces[0].ID
	rec := doJSON(t, s.Handler(), http.MethodDelete, base+"/intro.mp4", nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), pieceID) {
		t.Fatalf("a background in use: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(s.pieceDir(v.ID), "sources", "intro.mp4")); err != nil {
		t.Error("the file must stay")
	}
	used := map[string][]string{}
	for _, b := range listBackgrounds(t, s, v.ID).Backgrounds {
		used[b.Name] = b.UsedBy
	}
	if len(used["intro.mp4"]) != 1 || used["intro.mp4"][0] != pieceID || used["spare.mp4"] == nil || len(used["spare.mp4"]) != 0 {
		t.Fatalf("used_by: %v", used)
	}
	// one no piece uses is kept until it is deleted, and can be
	if rec := doJSON(t, s.Handler(), http.MethodDelete, base+"/spare.mp4", nil); rec.Code != http.StatusNoContent {
		t.Errorf("a free one: %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodDelete, base+"/spare.mp4", nil); rec.Code != http.StatusNotFound {
		t.Errorf("again: %d", rec.Code)
	}
	// the script gives it up: now it can go
	if rec := doJSON(t, s.Handler(), http.MethodPut, "/api/music-motion/pieces/"+v.ID+"/script", v.Script); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodDelete, base+"/intro.mp4", nil); rec.Code != http.StatusNoContent {
		t.Errorf("no longer in use: %d %s", rec.Code, rec.Body)
	}
}

func TestPieceBackgrounds_FromAGeneration(t *testing.T) {
	fakeProbe(t)
	s := mmServer(t)
	v := newPiece(t, s, nil)
	gdir := t.TempDir()
	clip := filepath.Join(gdir, "g.mp4")
	os.WriteFile(clip, fakeVideo(4), 0o644)
	s.mHandler.generationVideoSeam = func(id string) (string, bool, error) {
		switch id {
		case "api-gen-done":
			return clip, false, nil
		case "api-gen-adult":
			return clip, true, nil
		case "api-gen-running":
			return "", false, errors.Join(generation.ErrGenerationNotDone, errors.New("it is running"))
		case "api-gen-novideo":
			return "", false, generation.ErrGenerationNoVideo
		}
		// a generation that does not exist, or that is adult on a server that does not show them
		return "", false, generation.ErrGenerationNotFound
	}
	post := func(body any) *httptest.ResponseRecorder {
		return doJSON(t, s.Handler(), http.MethodPost, backgroundsURL(v.ID), body)
	}
	rec := post(map[string]any{"from": "generation:api-gen-done"})
	var made map[string]any
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &made) != nil || made["name"] != "api-gen-done.mp4" || made["seconds"] != 4.0 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	list := listBackgrounds(t, s, v.ID)
	if len(list.Backgrounds) != 1 || list.Backgrounds[0].Source != "generation:api-gen-done" {
		t.Fatalf("%+v", list)
	}
	// it is a copy: the generation's file can go and the piece keeps its own
	if err := os.Remove(clip); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.pieceDir(v.ID), "sources", "api-gen-done.mp4")); err != nil {
		t.Fatalf("the piece lost its copy: %v", err)
	}
	if len(listBackgrounds(t, s, v.ID).Backgrounds) != 1 {
		t.Fatal("the list lost it")
	}
	// the name can be chosen; an existing one is not replaced
	os.WriteFile(clip, fakeVideo(4), 0o644)
	if rec := post(map[string]any{"from": "generation:api-gen-done", "name": "verse.mp4"}); rec.Code != http.StatusCreated {
		t.Errorf("a chosen name: %d %s", rec.Code, rec.Body)
	}
	if rec := post(map[string]any{"from": "generation:api-gen-done"}); rec.Code != http.StatusConflict {
		t.Errorf("an existing name: %d", rec.Code)
	}
	for _, c := range []struct {
		body map[string]any
		code int
	}{
		{map[string]any{"from": "generation:api-gen-running", "name": "r.mp4"}, http.StatusConflict},
		{map[string]any{"from": "generation:api-gen-missing", "name": "m.mp4"}, http.StatusNotFound},
		{map[string]any{"from": "generation:api-gen-hidden", "name": "h.mp4"}, http.StatusNotFound}, // an adult generation on a server that does not show them
		{map[string]any{"from": "generation:api-gen-novideo", "name": "n.mp4"}, http.StatusUnprocessableEntity},
		{map[string]any{"from": "project:x"}, http.StatusUnprocessableEntity},
		{map[string]any{"from": "generation:"}, http.StatusUnprocessableEntity},
		{map[string]any{"from": "generation:api-gen-done", "name": "bad name.mp4"}, http.StatusBadRequest},
	} {
		if rec := post(c.body); rec.Code != c.code {
			t.Errorf("%v: %d %s, want %d", c.body, rec.Code, rec.Body, c.code)
		}
	}
	if len(listBackgrounds(t, s, v.ID).Backgrounds) != 2 {
		t.Fatalf("what was refused was kept: %+v", listBackgrounds(t, s, v.ID))
	}
}

func TestPieceBackgrounds_TheFileIsStreamedWithRangeAndATicket(t *testing.T) {
	fakeProbe(t)
	s := mmServer(t)
	v := newPiece(t, s, nil)
	putRaw(t, s, backgroundsURL(v.ID)+"/intro.mp4", []byte("VIDEO:6 and some more bytes to read a range of"))
	url := backgroundsURL(v.ID) + "/intro.mp4/file"
	rec := doJSON(t, s.Handler(), http.MethodGet, url, nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "video/mp4" || rec.Header().Get("ETag") == "" || rec.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Range", "bytes=0-5")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusPartialContent || rr.Body.String() != "VIDEO:" {
		t.Fatalf("a range: %d %q", rr.Code, rr.Body)
	}
	// the ETag answers 304
	req = httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("If-None-Match", rec.Header().Get("ETag"))
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: %d", rr.Code)
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, backgroundsURL(v.ID)+"/other.mp4/file", nil); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown clip: %d", rec.Code)
	}
}

// a piece's project is made when it is first rendered; its backgrounds are in its sources, which are what the package of a project
// carries, so they travel with it and go with it.
func TestPieceBackgrounds_GoWithThePieceInAnExportADuplicateAndADelete(t *testing.T) {
	fakeProbe(t)
	s := mmServer(t)
	v := newPiece(t, s, nil)
	base := "/api/music-motion/pieces/" + v.ID
	putRaw(t, s, base+"/backgrounds/intro.mp4", fakeVideo(30))
	if rec := doJSON(t, s.Handler(), http.MethodPut, base+"/script", scriptWithBackground(v, "intro.mp4")); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec := doJSON(t, s.Handler(), http.MethodPost, base+"/render", nil)
	var render struct {
		RunID string `json:"run_id"`
	}
	if rec.Code != http.StatusAccepted || json.Unmarshal(rec.Body.Bytes(), &render) != nil || render.RunID == "" {
		t.Fatalf("render: %d %s", rec.Code, rec.Body)
	}
	waitUntil(t, func() bool {
		rr := doJSON(t, s.Handler(), http.MethodGet, "/api/runs/"+render.RunID, nil)
		return rr.Code == http.StatusOK && strings.Contains(rr.Body.String(), `"state":"succeeded"`)
	})
	dir := s.pieceDir(v.ID)
	// the clip is what the scene of the piece plays
	if raw, err := os.ReadFile(filepath.Join(dir, "scenes.yaml")); err != nil || !strings.Contains(string(raw), "clip: intro.mp4") {
		t.Fatalf("scenes.yaml: %v\n%s", err, raw)
	}
	// export: the package carries the clip
	rec = doJSON(t, s.Handler(), http.MethodGet, "/api/projects/"+v.ID+"/export?format=zip&lang=en", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "intro.mp4") {
		t.Fatalf("export: %d (the clip is not in the package)", rec.Code)
	}
	// import: a project made from the package has the clip and the scene that plays it
	pkg := append([]byte(nil), rec.Body.Bytes()...)
	conf := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/import/package/confirm?id=imported-piece", bytes.NewReader(pkg))
	s.Handler().ServeHTTP(conf, req)
	if conf.Code != http.StatusCreated {
		t.Fatalf("import: %d %s", conf.Code, conf.Body)
	}
	if raw, err := os.ReadFile(filepath.Join(s.Workspace.ProjectDir("imported-piece"), "sources", "intro.mp4")); err != nil || string(raw) != string(fakeVideo(30)) {
		t.Fatalf("the imported project lost the clip: %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(s.Workspace.ProjectDir("imported-piece"), "scenes.yaml")); err != nil || !strings.Contains(string(raw), "clip: intro.mp4") {
		t.Fatalf("the imported project does not play the clip: %v", err)
	}
	// duplicate: the copy keeps the clip
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/"+v.ID+"/duplicate", map[string]string{"new_id": "copy-of-piece"}); rec.Code != http.StatusCreated {
		t.Fatalf("duplicate: %d %s", rec.Code, rec.Body)
	}
	if raw, err := os.ReadFile(filepath.Join(s.Workspace.ProjectDir("copy-of-piece"), "sources", "intro.mp4")); err != nil || string(raw) != string(fakeVideo(30)) {
		t.Fatalf("the copy lost the clip: %v", err)
	}
	// delete: the folder, and so the clip, goes
	if rec := doJSON(t, s.Handler(), http.MethodDelete, base, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("the folder of the piece is still there")
	}
}

// a plain colour picture of the size of the frame, as ffmpeg would give the picture of the video
func solidPNG(w, h int, c color.NRGBA) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{c}, image.Point{}, draw.Src)
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func TestMusicmotionLayOverPutsTheTransparentPageOnThePictureOfTheVideo(t *testing.T) {
	under := solidPNG(8, 8, color.NRGBA{200, 0, 0, 255})
	over := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	over.Set(1, 1, color.NRGBA{0, 0, 255, 255}) // one opaque dot of the page
	var b bytes.Buffer
	png.Encode(&b, over)
	out, err := musicmotion.LayOver(under, b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	img, _ := png.Decode(bytes.NewReader(out))
	if r, g, bl, a := img.At(5, 5).RGBA(); r>>8 != 200 || g != 0 || bl != 0 || a>>8 != 255 {
		t.Errorf("where the page is transparent the video shows: %v %v %v %v", r>>8, g, bl, a>>8)
	}
	if r, _, bl, _ := img.At(1, 1).RGBA(); r != 0 || bl>>8 != 255 {
		t.Errorf("where the page draws, the page shows: %v %v", r, bl)
	}
	if _, err := musicmotion.LayOver([]byte("no"), b.Bytes()); err == nil {
		t.Error("a broken picture is an error")
	}
}

func TestFrame_OverAVideoTheClipShowsWhereThePageIsTransparent(t *testing.T) {
	b, err := motion.Find("")
	if err != nil {
		t.Skip("no Chrome or Chromium on this machine")
	}
	fakeProbe(t)
	s := mmServer(t)
	s.motionBrowser = func() (motion.Browser, error) { return b, nil }
	v := newPiece(t, s, nil)
	base := "/api/music-motion/pieces/" + v.ID
	putRaw(t, s, base+"/backgrounds/intro.mp4", fakeVideo(30))
	script := scriptWithBackground(v, "intro.mp4")
	if rec := doJSON(t, s.Handler(), http.MethodPut, base+"/script", script); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var asked []float64
	s.mHandler.clipFrameSeam = func(ctx context.Context, path string, at float64, w, h int) ([]byte, error) {
		asked = append(asked, at)
		if filepath.Base(path) != "intro.mp4" || w != v.Width || h != v.Height {
			t.Errorf("the picture is asked for %s at %dx%d", path, w, h)
		}
		return solidPNG(w, h, color.NRGBA{200, 0, 0, 255}), nil
	}
	piece := script.Pieces[0]
	at := piece.Start + 1.5
	rec := doJSON(t, s.Handler(), http.MethodGet, fmt.Sprintf("%s/frame?t=%g", base, at), nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String()[:min(200, rec.Body.Len())])
	}
	img, err := png.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil || img.Bounds().Dx() != v.Width || img.Bounds().Dy() != v.Height {
		t.Fatalf("%v", err)
	}
	// the corner of the frame is the video's: the piece is "blank" and the scrim is 0.3 of the design's background laid over it
	r, _, _, a := img.At(3, 3).RGBA()
	if a>>8 != 255 || r>>8 < 100 {
		t.Errorf("the corner shows the video: red %d alpha %d", r>>8, a>>8)
	}
	// the second of the file is the one the render maps: a 30 s file in a piece of 10 s plays at its own speed, from its start
	if len(asked) != 1 || asked[0] < 1.49 || asked[0] > 1.51 {
		t.Errorf("the picture of the video is taken at %v, want 1.5 s", asked)
	}
	// no ffmpeg
	ffmpegAvailable = func() bool { return false }
	defer func() { ffmpegAvailable = func() bool { _, err := exec.LookPath("ffmpeg"); return err == nil } }()
	if rec := doJSON(t, s.Handler(), http.MethodGet, fmt.Sprintf("%s/frame?t=%g", base, at), nil); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "ffmpeg") {
		t.Errorf("no ffmpeg: %d %s", rec.Code, rec.Body)
	}
	// outside the song is still a 422, and no picture is asked for
	asked = nil
	if rec := doJSON(t, s.Handler(), http.MethodGet, base+"/frame?t=999", nil); rec.Code != http.StatusUnprocessableEntity || len(asked) != 0 {
		t.Errorf("outside: %d", rec.Code)
	}
}

func TestPreview_ASceneOverAVideoNamesItAndGetsTheTransparentPage(t *testing.T) {
	fakeProbe(t)
	s := mmServer(t)
	v := newPiece(t, s, nil)
	base := "/api/music-motion/pieces/" + v.ID
	putRaw(t, s, base+"/backgrounds/intro.mp4", fakeVideo(30))
	script := scriptWithBackground(v, "intro.mp4")
	rec := doJSON(t, s.Handler(), http.MethodPost, base+"/preview", PreviewBody{Script: &script})
	var pv PiecePreview
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &pv) != nil || len(pv.Scenes) != len(script.Pieces) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	first := pv.Scenes[0]
	if !first.Layer || len(pv.Video) != 1 || pv.Video[0].Clip != "intro.mp4" || pv.Video[0].URL != base+"/backgrounds/intro.mp4/file" || pv.Video[0].FileSeconds != 30 {
		t.Fatalf("the scene over the video is a layer and the track names the clip: %+v %+v", first.Layer, pv.Video)
	}
	if !strings.Contains(first.HTML, "html,body{background:transparent}") {
		t.Error("its page is not the transparent one")
	}
	for _, sc := range pv.Scenes[1:] {
		if sc.Layer || strings.Contains(sc.HTML, "html,body{background:transparent}") {
			t.Errorf("a piece with no background is as it was: %+v", sc.Layer)
		}
	}
	// nothing was saved by previewing
	if got := doJSON(t, s.Handler(), http.MethodGet, base, nil); strings.Contains(got.Body.String(), `"background"`) {
		t.Error("the preview saved the script")
	}
	// a clip the piece does not keep is the same refusal as saving
	bad := scriptWithBackground(v, "nope.mp4")
	if rec := doJSON(t, s.Handler(), http.MethodPost, base+"/preview", PreviewBody{Script: &bad}); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "nope.mp4") {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}

// pictureSeen is a vision model that keeps the pictures it is shown.
type pictureSeen struct {
	mu     sync.Mutex
	images [][]byte
	n      int
	script int
}

func (p *pictureSeen) Look(ctx context.Context, system, user string, images [][]byte) (string, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.images = append(p.images, images...)
	p.n++
	if p.n > p.script {
		return `{"scores": {"consistency": 2, "variety": 2}, "notes": [], "repeated": []}`, "seam-vision", nil
	}
	return scoresEach("2", "fine"), "seam-vision", nil
}

func TestReview_APieceOverAVideoIsGradedOverTheVideo(t *testing.T) {
	b, err := motion.Find("")
	if err != nil {
		t.Skip("no Chrome or Chromium on this machine")
	}
	fakeProbe(t)
	s := mmServer(t)
	s.motionBrowser = func() (motion.Browser, error) { return b, nil }
	v := newPiece(t, s, nil)
	base := "/api/music-motion/pieces/" + v.ID
	putRaw(t, s, base+"/backgrounds/intro.mp4", fakeVideo(30))
	script := scriptWithBackground(v, "intro.mp4")
	if rec := doJSON(t, s.Handler(), http.MethodPut, base+"/script", script); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	s.mHandler.clipFrameSeam = func(ctx context.Context, path string, at float64, w, h int) ([]byte, error) {
		return solidPNG(w, h, color.NRGBA{200, 0, 0, 255}), nil // a red video
	}
	seen := &pictureSeen{script: len(script.Pieces)}
	s.reviewVisionSeam = func(context.Context) (musicmotion.Vision, error) { return seen, nil }
	if rec := doJSON(t, s.Handler(), http.MethodPost, base+"/review", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for i := 0; i < 2400; i++ {
		if got := pieceOf(t, s, v.ID).Review; got != nil && got.State != PieceReviewRunning {
			if got.State != PieceReviewDone {
				t.Fatalf("%+v", got)
			}
			break
		}
		sleep25()
	}
	seen.mu.Lock()
	defer seen.mu.Unlock()
	if len(seen.images) == 0 {
		t.Fatal("the reviewer saw nothing")
	}
	// the first picture is the three frames of the piece over the video, side by side: the video shows in it, red under the scrim
	img, err := png.Decode(bytes.NewReader(seen.images[0]))
	if err != nil {
		t.Fatal(err)
	}
	red := 0
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y += 4 {
		for x := bounds.Min.X; x < bounds.Max.X; x += 4 {
			if r, g, bl, _ := img.At(x, y).RGBA(); r>>8 > 100 && g>>8 < 60 && bl>>8 < 60 {
				red++
			}
		}
	}
	if red < 50 {
		t.Errorf("the pictures the reviewer saw do not have the video in them (%d red samples)", red)
	}
}

func TestPropose_ABackgroundGoesToThePieceThatTakesItsPlaceAndWhatCannotIsNamed(t *testing.T) {
	fakeProbe(t)
	s := mmServer(t)
	v := newPiece(t, s, nil)
	base := "/api/music-motion/pieces/" + v.ID
	putRaw(t, s, base+"/backgrounds/intro.mp4", fakeVideo(30))
	putRaw(t, s, base+"/backgrounds/spare.mp4", fakeVideo(30))
	script := scriptWithBackground(v, "intro.mp4")
	// a second video behind the last piece too
	last := len(script.Pieces) - 1
	if last > 0 {
		p := script.Pieces[last]
		p.Part, p.Params, p.Animate, p.With, p.Mode, p.Hit = "blank", nil, nil, nil, "", nil
		p.Background = &musicmotion.PieceBackground{Clip: "spare.mp4"}
		script.Pieces[last] = p
	}
	if rec := doJSON(t, s.Handler(), http.MethodPut, base+"/script", script); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	old := script.Pieces[0]
	code, a, body := propose(t, s, v.ID, map[string]any{"what": "script"})
	if code != http.StatusOK || a.Script == nil {
		t.Fatalf("%d %s", code, body)
	}
	// the track is in the time of the song, so it stays where it was; the pieces over an item use only parts that leave the video visible
	cat := s.mmCatalog()
	if len(a.Script.Video) != 2 || a.Script.Video[0].Clip != "intro.mp4" || a.Script.Video[0].Start != old.Start || a.Script.Video[0].End != old.End {
		t.Fatalf("the video was not carried: %s", body)
	}
	for _, p := range a.Script.Pieces {
		if len(a.Script.ItemsOver(p)) == 0 {
			continue
		}
		if part, ok := cat.Get(p.Part); !ok || !part.OverVideo {
			t.Errorf("the piece %s uses %q, which hides the video", p.ID, p.Part)
		}
	}
	// what is on the answer is what was saved
	saved := pieceOf(t, s, v.ID)
	if len(saved.Script.Video) != 2 {
		t.Fatalf("the saved script lost its videos: %+v", saved.Script.Video)
	}
	// both files are still in the piece, used or not
	if got := listBackgrounds(t, s, v.ID); len(got.Backgrounds) != 2 {
		t.Fatalf("%+v", got)
	}
}

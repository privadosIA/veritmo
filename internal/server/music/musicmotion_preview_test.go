package music

import (
	"bytes"
	"encoding/json"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/music/musicmotion"
	"veritmo/internal/platform/settings"
	"veritmo/internal/video/motion"
)

func previewOf(t *testing.T, s *testServer, id, query string, body any) (int, PiecePreview, string) {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces/"+id+"/preview"+query, body)
	var out PiecePreview
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, out, rec.Body.String()
}

func TestPreview_ASavedPieceGivesThePagesTheRenderDraws(t *testing.T) {
	s := mmServer(t)
	v := newPiece(t, s, nil)
	code, pv, body := previewOf(t, s, v.ID, "", nil)
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	if len(pv.Scenes) != len(v.Script.Pieces) || pv.Width != v.Width || pv.FPS != v.FPS || pv.Replacements == nil {
		t.Fatalf("%d scenes of %d, %+v", len(pv.Scenes), len(v.Script.Pieces), pv)
	}
	for i, sc := range pv.Scenes {
		if sc.ID != v.Script.Pieces[i].ID || !strings.Contains(sc.HTML, "seek") || len(sc.Song) == 0 {
			t.Errorf("scene %d: %s", i, sc.ID)
		}
	}
	// The pages are the ones the render writes.
	spec := musicmotion.ProjectSpec{Name: v.ID, Map: *v.SongMap, Design: v.Design.Design, Script: *v.Script, Cat: s.mmCatalog(), Lib: s.mmLibrary(), FPS: v.FPS, Width: v.Width, Height: v.Height}
	pages, err := musicmotion.BuildPages(spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, pg := range pages {
		if pv.Scenes[i].HTML != motion.PreviewHTMLSong(pg.HTML, pg.Seconds, pg.Song) {
			t.Errorf("scene %s differs from the page of the render", pg.ID)
		}
	}
}

func TestPreview_AnEditThatIsNotSavedIsDrawnAndNotKept(t *testing.T) {
	s := mmServer(t)
	v := newPiece(t, s, nil)
	script := *v.Script
	script.Pieces = append([]musicmotion.Piece(nil), script.Pieces...)
	script.Pieces[0].Part = "toggle"
	_, before, _ := previewOf(t, s, v.ID, "", nil)
	code, pv, body := previewOf(t, s, v.ID, "", map[string]any{"script": script})
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	if pv.Scenes[0].HTML == before.Scenes[0].HTML && v.Script.Pieces[0].Part != "toggle" {
		t.Fatal("the edited piece must be drawn with the new part")
	}
	var after PieceView
	json.Unmarshal(doJSON(t, s.Handler(), http.MethodGet, "/api/music-motion/pieces/"+v.ID, nil).Body.Bytes(), &after)
	if after.Script.Pieces[0].Part != v.Script.Pieces[0].Part || after.ScriptSource == "user" {
		t.Fatalf("the preview saved something: %+v", after.Script.Pieces[0])
	}
	d := v.Design.Design
	d.Accent = "#ff00aa"
	if code, pv2, _ := previewOf(t, s, v.ID, "", map[string]any{"design": d}); code != http.StatusOK || pv2.Scenes[0].HTML == before.Scenes[0].HTML {
		t.Fatalf("a design of the body must be used: %d", code)
	}
}

func TestPreview_AnInvalidEditIs422WithTheReasonOfPutAndSavesNothing(t *testing.T) {
	s := mmServer(t)
	v := newPiece(t, s, nil)
	script := *v.Script
	script.Pieces = append([]musicmotion.Piece(nil), script.Pieces...)
	script.Pieces[0].Part = "hologram"
	put := doJSON(t, s.Handler(), http.MethodPut, "/api/music-motion/pieces/"+v.ID+"/script", script)
	code, _, body := previewOf(t, s, v.ID, "", map[string]any{"script": script})
	if code != http.StatusUnprocessableEntity || put.Code != http.StatusUnprocessableEntity || !strings.Contains(body, "hologram") {
		t.Fatalf("preview %d %s, put %d", code, body, put.Code)
	}
	bad := v.Design.Design
	bad.Accent = "nope"
	if code, _, body := previewOf(t, s, v.ID, "", map[string]any{"design": bad}); code != http.StatusUnprocessableEntity {
		t.Fatalf("%d %s", code, body)
	}
}

func TestPreview_OnlyTheNamedScenesAndAnUnknownPieceOrPiecePathIsRefused(t *testing.T) {
	s := mmServer(t)
	v := newPiece(t, s, nil)
	second := v.Script.Pieces[1].ID
	code, pv, body := previewOf(t, s, v.ID, "?scenes="+second, nil)
	if code != http.StatusOK || len(pv.Scenes) != 1 || pv.Scenes[0].ID != second {
		t.Fatalf("%d %s", code, body)
	}
	if code, _, _ := previewOf(t, s, v.ID, "?scenes=nope", nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("an unknown piece: %d", code)
	}
	if code, _, _ := previewOf(t, s, "pc-nope", "", nil); code != http.StatusNotFound {
		t.Fatalf("an unknown piece of the server: %d", code)
	}
}

func TestPreview_NeedsNoBrowserAndIsAllowedWhileTheRunIsInProgress(t *testing.T) {
	s := mmServer(t)
	s.motionBrowser = func() (motion.Browser, error) { return motion.Browser{}, os.ErrNotExist }
	v := newPiece(t, s, nil)
	if code, _, body := previewOf(t, s, v.ID, "", nil); code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
}

func TestFrame_Errors(t *testing.T) {
	s := mmServer(t)
	v := newPiece(t, s, nil)
	base := "/api/music-motion/pieces/" + v.ID + "/frame"
	for _, q := range []string{"", "?t=abc", "?t=-1", "?t=999"} {
		if rec := doJSON(t, s.Handler(), http.MethodGet, base+q, nil); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%q: %d %s", q, rec.Code, rec.Body)
		}
	}
	s.motionBrowser = func() (motion.Browser, error) { return motion.Browser{}, os.ErrNotExist }
	if rec := doJSON(t, s.Handler(), http.MethodGet, base+"?t=1", nil); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no browser: %d %s", rec.Code, rec.Body)
	}
}

func TestMusicMotion_ABuiltInPartIsReadBackWithItsFragment(t *testing.T) {
	s := mmServer(t)
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/music-motion/parts/toggle", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var got UserPart
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Name != "toggle" || got.Source != "builtin" || got.Manifest.Name != "toggle" || !strings.Contains(got.Fragment, "<") {
		t.Fatalf("%+v", got)
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/music-motion/parts/..%2F..%2Fsettings", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("%d", rec.Code)
	}
}

func TestMusicMotion_TheCatalogGivesAFontStackForEachFont(t *testing.T) {
	s := mmServer(t)
	var c PieceCatalog
	json.Unmarshal(doJSON(t, s.Handler(), http.MethodGet, "/api/music-motion/catalog", nil).Body.Bytes(), &c)
	if len(c.Fonts) == 0 || len(c.FontStacks) != len(c.Fonts) {
		t.Fatalf("%v %v", c.Fonts, c.FontStacks)
	}
	for _, f := range c.Fonts {
		if c.FontStacks[f] == "" || c.FontStacks[f] != musicmotion.Fonts[f] {
			t.Errorf("%s: %q", f, c.FontStacks[f])
		}
	}
}

func TestLibrary_ACharacterAndABackgroundAreReadWithTheirDrawings(t *testing.T) {
	s := mmServer(t)
	var c PieceCatalog
	json.Unmarshal(doJSON(t, s.Handler(), http.MethodGet, "/api/music-motion/catalog", nil).Body.Bytes(), &c)
	for _, ch := range c.Characters {
		rec := doJSON(t, s.Handler(), http.MethodGet, "/api/music-motion/library/characters/"+ch.Name, nil)
		var got LibraryCharacter
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
			t.Fatalf("%s: %d %s", ch.Name, rec.Code, rec.Body)
		}
		for _, pose := range ch.Poses {
			if !strings.Contains(got.Poses[pose], "<svg") {
				t.Errorf("%s/%s has no drawing", ch.Name, pose)
			}
		}
		if got.Width == 0 || got.DefaultPose == "" || strings.Contains(rec.Body.String(), filepath.Dir(s.mmLibrary().UserDir)) && s.mmLibrary().UserDir != "" {
			t.Errorf("%s: %+v", ch.Name, got)
		}
	}
	for _, b := range c.Backgrounds {
		rec := doJSON(t, s.Handler(), http.MethodGet, "/api/music-motion/library/backgrounds/"+b.Name, nil)
		var got LibraryBackground
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || !strings.Contains(got.SVG, "<svg") {
			t.Fatalf("%s: %d", b.Name, rec.Code)
		}
	}
	for _, p := range []string{"characters/nope", "characters/..%2F..%2Fsettings", "backgrounds/nope", "backgrounds/..%2Fx"} {
		if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/music-motion/library/"+p, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
}

func TestFrame_ARealFrameOfTheSavedPieceIsAPNGOfItsSizeAndTheSameTwice(t *testing.T) {
	b, err := motion.Find("")
	if err != nil {
		t.Skip("no Chrome or Chromium on this machine")
	}
	s := mmServer(t)
	s.motionBrowser = func() (motion.Browser, error) { return b, nil }
	v := newPiece(t, s, nil)
	url := "/api/music-motion/pieces/" + v.ID + "/frame?t=" + "3.2"
	one := doJSON(t, s.Handler(), http.MethodGet, url, nil)
	if one.Code != http.StatusOK || one.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("%d %s %s", one.Code, one.Header().Get("Content-Type"), one.Body.String()[:min(200, one.Body.Len())])
	}
	img, err := png.Decode(bytes.NewReader(one.Body.Bytes()))
	if err != nil || img.Bounds().Dx() != v.Width || img.Bounds().Dy() != v.Height {
		t.Fatalf("%v %v", err, img.Bounds())
	}
	two := doJSON(t, s.Handler(), http.MethodGet, url, nil)
	if !bytes.Equal(one.Body.Bytes(), two.Body.Bytes()) {
		t.Fatal("the same second must draw the same frame")
	}
}

func TestAnimate_TheCatalogSaysSoAndPutAndPreviewAgreeOnWhatMayMove(t *testing.T) {
	s := mmServer(t)
	var c PieceCatalog
	json.Unmarshal(doJSON(t, s.Handler(), http.MethodGet, "/api/music-motion/catalog", nil).Body.Bytes(), &c)
	has := false
	for _, f := range c.Features {
		has = has || f == "animate"
	}
	moving := false
	for _, p := range c.Parts {
		if p.Name == "counter" {
			for _, pr := range p.Params {
				moving = moving || (pr.Name == "from" && pr.Animatable)
			}
		}
	}
	if !has || !moving {
		t.Fatalf("features %v, counter.from animatable %v", c.Features, moving)
	}
	v := newPiece(t, s, nil)
	script := *v.Script
	script.Pieces = append([]musicmotion.Piece(nil), script.Pieces...)
	script.Pieces[0].Part = "counter"
	script.Pieces[0].With = nil
	script.Pieces[0].Animate = musicmotion.Animate{"unit": {{T: 0, Value: "a"}}}
	put := doJSON(t, s.Handler(), http.MethodPut, "/api/music-motion/pieces/"+v.ID+"/script", script)
	code, _, body := previewOf(t, s, v.ID, "", map[string]any{"script": script})
	if put.Code != http.StatusUnprocessableEntity || code != http.StatusUnprocessableEntity || !strings.Contains(body, `cannot be animated`) || !strings.Contains(body, `unit`) || body != put.Body.String() {
		t.Fatalf("put %d %s, preview %d %s", put.Code, put.Body, code, body)
	}
	script.Pieces[0].Animate = musicmotion.Animate{"from": {{T: 0, Value: 0.0}, {T: 2, Value: 50.0, Ease: "out"}}}
	code, pv, body := previewOf(t, s, v.ID, "", map[string]any{"script": script})
	if code != http.StatusOK || !strings.Contains(pv.Scenes[0].HTML, "__animate") {
		t.Fatalf("%d %s", code, body)
	}
	if put := doJSON(t, s.Handler(), http.MethodPut, "/api/music-motion/pieces/"+v.ID+"/script", script); put.Code != http.StatusOK {
		t.Fatalf("%d %s", put.Code, put.Body)
	}
}

func TestLayers_TheCatalogSaysSoAndPutAndPreviewDrawAndRefuseThem(t *testing.T) {
	s := mmServer(t)
	var c PieceCatalog
	json.Unmarshal(doJSON(t, s.Handler(), http.MethodGet, "/api/music-motion/catalog", nil).Body.Bytes(), &c)
	has := false
	for _, f := range c.Features {
		has = has || f == "layers"
	}
	if !has {
		t.Fatalf("features %v", c.Features)
	}
	v := newPiece(t, s, nil)
	script := *v.Script
	end := script.Pieces[len(script.Pieces)-1].End
	script.Layers = []musicmotion.Layer{{ID: "years", Items: []musicmotion.LayerItem{{Part: "timeline-strip", Start: 0, End: end}}}}
	for _, p := range script.Pieces {
		if p.Part == "timeline-strip" {
			t.Skip("the piece already uses the strip")
		}
	}
	code, pv, body := previewOf(t, s, v.ID, "", map[string]any{"script": script})
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	for _, sc := range pv.Scenes {
		if !strings.Contains(sc.HTML, "layer-0") {
			t.Errorf("the page of %s does not draw the strip", sc.ID)
		}
	}
	if put := doJSON(t, s.Handler(), http.MethodPut, "/api/music-motion/pieces/"+v.ID+"/script", script); put.Code != http.StatusOK {
		t.Fatalf("%d %s", put.Code, put.Body)
	}
	script.Layers[0].Items[0].End = end + 10
	code, _, body = previewOf(t, s, v.ID, "", map[string]any{"script": script})
	put := doJSON(t, s.Handler(), http.MethodPut, "/api/music-motion/pieces/"+v.ID+"/script", script)
	if code != http.StatusUnprocessableEntity || put.Code != http.StatusUnprocessableEntity || !strings.Contains(body, "outside the song") || !strings.Contains(body, "years") {
		t.Fatalf("preview %d %s, put %d", code, body, put.Code)
	}
}

func TestMusicMotion_AnImportedPartWithPicturesIsListedAndReadBackWithoutAnyPath(t *testing.T) {
	s := mmServer(t)
	man := musicmotion.Manifest{Name: "hf-inked", Description: "inked", Version: 1, Levels: []string{"simple"}, Params: []musicmotion.Param{}, Pictures: 1}
	mb, _ := json.Marshal(man)
	frag := `<div id="x"><img src="data:image/svg+xml;base64,PHN2Zy8+"></div>`
	origin := &musicmotion.PartOrigin{Source: "hyperframes", Block: "inked", Licence: "Apache-2.0", Files: []musicmotion.OriginFile{{Name: "stroke.svg", Kind: "svg", Bytes: 99}}}
	if err := s.mmCatalog().CreatePart(mb, frag, origin); err != nil {
		t.Fatal(err)
	}
	var c PieceCatalog
	json.Unmarshal(doJSON(t, s.Handler(), http.MethodGet, "/api/music-motion/catalog", nil).Body.Bytes(), &c)
	found := false
	for _, p := range c.Parts {
		if p.Name == "hf-inked" {
			found = p.Pictures == 1 && p.Origin != nil && len(p.Origin.Files) == 1
		}
	}
	if !found {
		t.Fatalf("the catalog lists pictures: 1 and the origin's files for hf-inked")
	}
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/music-motion/parts/hf-inked", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"pictures":1`) || !strings.Contains(body, "data:image/svg+xml") || strings.Contains(body, settings.Dir()) {
		t.Fatalf("manifest and fragment only, no path of this machine: %s", body)
	}
}

package workspace

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"veritmo/internal/platform/events"
	"veritmo/internal/video/motion"
)

const motionYAML = `project: demo
output:
  mode: video
  file: out/demo.mp4
defaults:
  voice: v.onnx
  resolution: 1280x720
  sampler: euler
  steps: 20
  fps: 24
scenes:
  - id: gfx
    motion_graphic: three bullets over dark blue
    tts_text: Tres ideas clave para recordar
  - id: pic
    prompt: a lighthouse
    tts_text: Un faro
    overlay:
      text: Chapter 1
      position: top
  - id: plain
    prompt: a city
    tts_text: Una ciudad
`

const validPage = `<!doctype html><html><head><style>body{background:#012}</style></head><body><h1>hi</h1></body></html>`

func motionServer(t *testing.T) (*testServer, string) {
	t.Helper()
	isolateSettings(t)
	s, projects := newProjectsServer(t)
	createProject(t, s, "demo")
	if err := s.SaveProjectDefinition("demo", []byte(motionYAML), "save"); err != nil {
		t.Fatal(err)
	}
	s.Overrides.MotionBrowser = func() (motion.Browser, error) { return motion.Browser{Path: "/fake/chrome", Version: "126.0.1"}, nil }
	return s, filepath.Join(projects, "demo")
}

// storedPage reads the page of a scene as the database keeps it (the pages of motion/ are only a working copy of a run).
func storedPage(t *testing.T, s *testServer, scene string, overlay bool) (html string, meta motion.Meta, found bool) {
	t.Helper()
	html, meta, _, found = s.Projects().Page("demo", scene, overlay)
	return html, meta, found
}

func putPage(t *testing.T, s *testServer, scene string, html string, meta *motion.Meta) {
	t.Helper()
	if err := s.Projects().PutPage("demo", scene, false, html, meta); err != nil {
		t.Fatal(err)
	}
}

func getMotion(t *testing.T, s *testServer, scene, query string) (int, motionView) {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/projects/demo/scenes/"+scene+"/motion"+query, nil)
	var v motionView
	json.Unmarshal(rec.Body.Bytes(), &v)
	return rec.Code, v
}

func TestMotionAPI_NothingStoredThenTheStoredPageWithItsPreviewAndOrigin(t *testing.T) {
	s, _ := motionServer(t)
	code, v := getMotion(t, s, "gfx", "")
	if code != http.StatusOK || v.Exists || v.HTML != "" || v.Duration <= 0 {
		t.Fatalf("no page yet: %d %+v", code, v)
	}
	putPage(t, s, "gfx", validPage, nil)
	_, v = getMotion(t, s, "gfx", "?duration=6")
	if !v.Exists || v.HTML != validPage || v.Duration != 6 || v.Origin != motion.OriginSupplied {
		t.Fatalf("a page nobody generated is supplied: %+v", v)
	}
	for _, want := range []string{"Content-Security-Policy", "__motionSeek", validPage[:20]} {
		if !strings.Contains(v.Preview, want) {
			t.Errorf("the preview must contain %q", want)
		}
	}
	putPage(t, s, "gfx", validPage, &motion.Meta{Kind: "motion_graphic", Model: "writer:7b", Browser: "126.0.1", GeneratedSHA256: motion.SHA256(validPage)})
	_, v = getMotion(t, s, "gfx", "")
	if v.Origin != motion.OriginGenerated || !strings.Contains(v.Summary, "writer:7b") || v.Model != "writer:7b" {
		t.Fatalf("generated page: %+v", v)
	}
	// the overlay is a separate page
	if _, v = getMotion(t, s, "gfx", "?overlay=1"); v.Exists {
		t.Fatal("the overlay has its own page")
	}
}

func TestMotionAPI_UnknownSceneAndBadIDs(t *testing.T) {
	s, _ := motionServer(t)
	if code, _ := getMotion(t, s, "nope", ""); code != http.StatusNotFound {
		t.Fatalf("unknown scene: %d", code)
	}
	if code, _ := getMotion(t, s, "bad.id", ""); code != http.StatusBadRequest {
		t.Fatalf("a scene id with a dot is refused: %d", code)
	}
}

func TestMotionAPI_SavingByHandIsCheckedAndMarksTheEdit(t *testing.T) {
	s, _ := motionServer(t)
	put := func(scene string, body map[string]any) (int, motionView, string) {
		rec := doJSON(t, s.Handler(), http.MethodPut, "/api/projects/demo/scenes/"+scene+"/motion", body)
		var v motionView
		json.Unmarshal(rec.Body.Bytes(), &v)
		return rec.Code, v, rec.Body.String()
	}
	if code, v, body := put("gfx", map[string]any{"html": validPage}); code != http.StatusOK || !v.Exists {
		t.Fatalf("%d %s", code, body)
	}
	if html, _, _ := storedPage(t, s, "gfx", false); html != validPage {
		t.Fatal("the page is stored in the database")
	}
	if code, _, body := put("gfx", map[string]any{"html": `<html><body><script src="https://cdn.x/a.js"></script></body></html>`}); code != http.StatusBadRequest || !strings.Contains(body, "external resource") {
		t.Fatalf("an external resource is refused: %d %s", code, body)
	}
	if code, _, _ := put("gfx", map[string]any{"html": "just text"}); code != http.StatusBadRequest {
		t.Fatal("a fragment is refused")
	}
	if code, _, body := put("plain", map[string]any{"html": validPage}); code != http.StatusBadRequest || !strings.Contains(body, "no motion graphic") {
		t.Fatalf("a scene without a motion graphic cannot hold a page: %d %s", code, body)
	}
	if code, _, body := put("pic", map[string]any{"html": validPage, "overlay": true}); code != http.StatusOK {
		t.Fatalf("a scene with an overlay holds its overlay page: %d %s", code, body)
	}
	// generated, then edited
	putPage(t, s, "gfx", validPage, &motion.Meta{Kind: "motion_graphic", Model: "m", GeneratedSHA256: motion.SHA256(validPage)})
	_, v, _ := put("gfx", map[string]any{"html": strings.ReplaceAll(validPage, "hi", "bye")})
	if v.Origin != motion.OriginEdited {
		t.Fatalf("a page changed by hand after the model wrote it is edited: %+v", v)
	}
}

func TestMotionAPI_GenerateWritesTheModelsPageAndItsDetails(t *testing.T) {
	s, _ := motionServer(t)
	var got motion.Request
	s.Overrides.MotionWriter = func(context.Context) (motion.Writer, motion.ModelInfo, error) {
		return nil, motion.ModelInfo{Provider: "Ollama", Model: "writer:7b"}, nil
	}
	s.Overrides.MotionGenerate = func(_ context.Context, _ motion.Writer, browser string, r motion.Request, _ motion.Limits) (string, error) {
		got = r
		if browser != "/fake/chrome" {
			t.Errorf("the page is checked in the browser found: %q", browser)
		}
		return validPage, nil
	}
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/demo/scenes/pic/motion/generate", map[string]any{"overlay": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if !got.Overlay || got.Description != "Chapter 1" || got.Position != "top" || got.Width != 1280 || got.Height != 720 {
		t.Fatalf("the request is built from the saved scene: %+v", got)
	}
	var v motionView
	json.Unmarshal(rec.Body.Bytes(), &v)
	if !v.Exists || v.Origin != motion.OriginGenerated || v.Model != "writer:7b" || v.Browser != "126.0.1" {
		t.Fatalf("%+v", v)
	}
	if html, _, _ := storedPage(t, s, "pic", true); html != validPage {
		t.Fatal("the overlay page is stored")
	}
	// a new page replaces the stored one only because it was asked for
	s.Overrides.MotionGenerate = func(context.Context, motion.Writer, string, motion.Request, motion.Limits) (string, error) {
		return validPage + "<!-- v2 -->", nil
	}
	doJSON(t, s.Handler(), http.MethodPost, "/api/projects/demo/scenes/pic/motion/generate", map[string]any{"overlay": true})
	if html, _, _ := storedPage(t, s, "pic", true); !strings.Contains(html, "v2") {
		t.Fatal("a regenerated page replaces the stored one")
	}
}

func TestMotionAPI_GenerateFailuresSayWhy(t *testing.T) {
	s, _ := motionServer(t)
	s.Overrides.MotionWriter = func(context.Context) (motion.Writer, motion.ModelInfo, error) { return nil, motion.ModelInfo{}, nil }
	// no browser
	s.Overrides.MotionBrowser = func() (motion.Browser, error) { return motion.Browser{}, motion.ErrNoBrowser }
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/demo/scenes/gfx/motion/generate", nil)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "Chrome") {
		t.Fatalf("no browser: %d %s", rec.Code, rec.Body)
	}
	// the model could not write a working page
	s.Overrides.MotionBrowser = func() (motion.Browser, error) { return motion.Browser{Path: "/fake/chrome"}, nil }
	s.Overrides.MotionGenerate = func(context.Context, motion.Writer, string, motion.Request, motion.Limits) (string, error) {
		return "", motion.ErrPageRejected
	}
	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/projects/demo/scenes/gfx/motion/generate", nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a rejected page: %d %s", rec.Code, rec.Body)
	}
	if _, _, found := storedPage(t, s, "gfx", false); found {
		t.Fatal("nothing is stored when generation fails")
	}
	// a scene with nothing to generate
	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/projects/demo/scenes/plain/motion/generate", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a plain scene has no page to generate: %d", rec.Code)
	}
}

// A page replaced through the API is what the next run draws: the run gets it in motion/ and the model is not asked again; the page a run
// writes for a scene that had none is the one the API answers afterwards, with its provenance.
func TestMotionAPI_APageReplacedThroughTheAPIIsTheOneTheNextRunDraws(t *testing.T) {
	s, dir := motionServer(t)
	for _, f := range []string{"v.onnx", "v.onnx.json"} { // what the validation of a run looks for
		_ = os.WriteFile(filepath.Join(dir, f), []byte("stub"), 0o644)
	}
	s.rm.lookPath = fakeLook
	t.Cleanup(s.rm.shutdown)
	var drawn string
	var calls int
	s.rm.execute = func(ctx context.Context, rn *run, em *events.Emitter) (string, error) {
		b, err := os.ReadFile(filepath.Join(dir, "motion", "gfx.html"))
		if err == nil {
			drawn = string(b)
		} else { // no page for the scene: the run asks the model and stores what it writes
			calls++
			_ = os.MkdirAll(filepath.Join(dir, "motion"), 0o755)
			_ = os.WriteFile(filepath.Join(dir, "motion", "gfx.html"), []byte(validPage), 0o644)
			_ = motion.StoreMeta(dir, "gfx", false, motion.Meta{Kind: "motion_graphic", Provider: "ollama", Model: "gemma"})
		}
		em.PipelineStart()
		em.Done("out/x.mp4")
		return "out/x.mp4", nil
	}
	// the first run writes a page
	rec, _, lerr := s.Launch("demo", RunRequest{})
	if lerr != nil {
		t.Fatal(lerr)
	}
	waitState(t, s, rec.ID, StateSucceeded)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, found := storedPage(t, s, "gfx", false); found {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	code, v := getMotion(t, s, "gfx", "")
	if code != 200 || !v.Exists || v.HTML != validPage || v.Model != "gemma" || calls != 1 {
		t.Fatalf("after the first run: %d %+v (model calls %d)", code, v, calls)
	}
	// the second run gets the stored page: the model is not called
	rec, _, lerr = s.Launch("demo", RunRequest{})
	if lerr != nil {
		t.Fatal(lerr)
	}
	waitState(t, s, rec.ID, StateSucceeded)
	if calls != 1 || drawn != validPage {
		t.Fatalf("the second run must use the stored page: calls %d, drew %q", calls, drawn)
	}
	// a page replaced with PUT is the one the run after that draws
	newer := strings.Replace(validPage, "hi", "replaced", 1)
	if rec := doJSON(t, s.Handler(), http.MethodPut, "/api/projects/demo/scenes/gfx/motion", map[string]any{"html": newer}); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec, _, lerr = s.Launch("demo", RunRequest{})
	if lerr != nil {
		t.Fatal(lerr)
	}
	waitState(t, s, rec.ID, StateSucceeded)
	if drawn != newer || calls != 1 {
		t.Fatalf("the run draws the replaced page: %q (model calls %d)", drawn, calls)
	}
}

const timelinePage = `<!doctype html><html><head><style>body{background:#012}</style></head><body><h1 id="t">hi</h1>` +
	`<script type="application/json" id="motion-timeline">{"version":1,"tracks":[{"id":"title","target":"#t","start":0,"duration":1,"from":{"opacity":0},"to":{"opacity":1}}]}</script></body></html>`

func reviseRequest(t *testing.T, s *testServer, scene string, body any) (int, string) {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/demo/scenes/"+scene+"/motion/revise", body)
	return rec.Code, rec.Body.String()
}

func TestMotionAPI_ReviseChangesTheTimelineOfTheStoredPageOnly(t *testing.T) {
	s, _ := motionServer(t)
	generated := motion.Meta{Kind: "motion_graphic", Provider: "Ollama", Model: "writer:7b", GeneratedSHA256: motion.SHA256(timelinePage)}
	putPage(t, s, "gfx", timelinePage, &generated)
	s.Overrides.MotionWriter = func(context.Context) (motion.Writer, motion.ModelInfo, error) {
		return nil, motion.ModelInfo{Provider: "Gemini", Model: "flash"}, nil
	}
	var gotPage, gotInstruction string
	var gotReq motion.Request
	var viewed bool
	s.Overrides.MotionRevise = func(ctx context.Context, _ motion.Writer, browser, page, instruction string, r motion.Request, _ motion.Limits, view motion.Viewer) (string, error) {
		gotPage, gotInstruction, gotReq = page, instruction, r
		if browser != "/fake/chrome" {
			t.Errorf("the revised page is checked in the browser found: %q", browser)
		}
		if view != nil {
			viewed = true
			if got := view(ctx, "<p>x</p>"); got != "<p>x</p>" { // no compression service configured: the page as it is
				t.Errorf("without a service the model sees the page: %q", got)
			}
		}
		next, _ := motion.SpliceTimeline(page, `{"version":1,"tracks":[]}`)
		return next, nil
	}
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/demo/scenes/gfx/motion/revise", map[string]any{"instruction": "  slower entrances  "})
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if gotPage != timelinePage || gotInstruction != "slower entrances" || gotReq.Width != 1280 || gotReq.Height != 720 || gotReq.Overlay || !viewed {
		t.Fatalf("what Revise got: page ok=%v instruction=%q request=%+v viewed=%v", gotPage == timelinePage, gotInstruction, gotReq, viewed)
	}
	var out struct {
		motionView
		Revised struct{ Provider, Model string } `json:"revised"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Revised.Model != "flash" || out.Revised.Provider != "Gemini" {
		t.Errorf("the answer says who retimed it: %+v", out.Revised)
	}
	if !out.Exists || !strings.Contains(out.HTML, `"tracks":[]`) || out.Preview == "" || out.Browser != "126.0.1" {
		t.Errorf("the answer is the stored page as in the GET: %+v", out.motionView)
	}
	html, meta, _ := storedPage(t, s, "gfx", false)
	if !strings.Contains(html, `"tracks":[]`) || !strings.Contains(html, `<h1 id="t">hi</h1>`) {
		t.Fatal("the new timeline is stored and the rest of the page is as it was")
	}
	// the model that wrote the page is still the one that wrote it, and the page now reads as edited since
	if meta.Model != "writer:7b" || meta.Origin(html) != motion.OriginEdited || out.Origin != motion.OriginEdited {
		t.Errorf("meta = %+v, origin %q", meta, out.Origin)
	}
}

func TestMotionAPI_ReviseAPageSuppliedByHandStaysSuppliedAndAnOverlayHasItsOwn(t *testing.T) {
	s, _ := motionServer(t)
	if err := s.Projects().PutPage("demo", "pic", true, timelinePage, nil); err != nil {
		t.Fatal(err)
	}
	s.Overrides.MotionWriter = func(context.Context) (motion.Writer, motion.ModelInfo, error) { return nil, motion.ModelInfo{}, nil }
	var gotReq motion.Request
	s.Overrides.MotionRevise = func(_ context.Context, _ motion.Writer, _, page, _ string, r motion.Request, _ motion.Limits, _ motion.Viewer) (string, error) {
		gotReq = r
		return page + "<!-- retimed -->", nil
	}
	code, body := reviseRequest(t, s, "pic", map[string]any{"instruction": "a bounce on the title", "overlay": true})
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	if !gotReq.Overlay {
		t.Errorf("an overlay is revised as one: %+v", gotReq)
	}
	html, meta, _ := storedPage(t, s, "pic", true)
	if !strings.Contains(html, "retimed") || meta.Kind != "overlay" || meta.Origin(html) != motion.OriginSupplied {
		t.Errorf("a supplied page stays supplied: %+v", meta)
	}
	if _, _, found := storedPage(t, s, "pic", false); found {
		t.Error("the other page of the scene is not touched")
	}
}

func TestMotionAPI_ReviseFailuresSayWhyAndKeepTheStoredPage(t *testing.T) {
	s, _ := motionServer(t)
	s.Overrides.MotionWriter = func(context.Context) (motion.Writer, motion.ModelInfo, error) { return nil, motion.ModelInfo{}, nil }
	called := false
	s.Overrides.MotionRevise = func(context.Context, motion.Writer, string, string, string, motion.Request, motion.Limits, motion.Viewer) (string, error) {
		called = true
		return "", motion.ErrTimelineRejected
	}
	for name, tc := range map[string]struct {
		scene string
		body  any
		want  int
		text  string
	}{
		"no instruction":   {"gfx", map[string]any{}, http.StatusBadRequest, "instruction is required"},
		"a blank one":      {"gfx", map[string]any{"instruction": "   "}, http.StatusBadRequest, "instruction is required"},
		"too long":         {"gfx", map[string]any{"instruction": strings.Repeat("a", 1001)}, http.StatusBadRequest, "longer than 1000"},
		"nothing stored":   {"gfx", map[string]any{"instruction": "slower"}, http.StatusNotFound, "no stored page"},
		"a plain scene":    {"plain", map[string]any{"instruction": "slower"}, http.StatusBadRequest, ""},
		"an unknown scene": {"nope", map[string]any{"instruction": "slower"}, http.StatusNotFound, "not found"},
	} {
		if code, body := reviseRequest(t, s, tc.scene, tc.body); code != tc.want || !strings.Contains(body, tc.text) {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	if called {
		t.Fatal("the model is not called for a request that is refused before")
	}
	// a page with no timeline block cannot be revised and the model is not called
	putPage(t, s, "gfx", validPage, nil)
	if code, body := reviseRequest(t, s, "gfx", map[string]any{"instruction": "slower"}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "no motion-timeline block") || called {
		t.Fatalf("a page without a timeline: %d %s (called %v)", code, body, called)
	}
	// the model could not write a working timeline: 422 and the stored page is as it was
	putPage(t, s, "gfx", timelinePage, nil)
	if code, body := reviseRequest(t, s, "gfx", map[string]any{"instruction": "slower"}); code != http.StatusUnprocessableEntity || !called {
		t.Fatalf("a rejected timeline: %d %s", code, body)
	}
	if html, _, _ := storedPage(t, s, "gfx", false); html != timelinePage {
		t.Fatal("a failed revision leaves the stored page as it was")
	}
	// no browser to check the page in
	s.Overrides.MotionBrowser = func() (motion.Browser, error) { return motion.Browser{}, motion.ErrNoBrowser }
	if code, body := reviseRequest(t, s, "gfx", map[string]any{"instruction": "slower"}); code != http.StatusServiceUnavailable || !strings.Contains(body, "Chrome") {
		t.Fatalf("no browser: %d %s", code, body)
	}
}

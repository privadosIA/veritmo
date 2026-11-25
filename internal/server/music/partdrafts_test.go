package music

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/assistant/llm"
	"veritmo/internal/music/motionref"
	"veritmo/internal/music/musicmotion"
	"veritmo/internal/platform/settings"
	"veritmo/internal/video/motion"
)

const draftManifest = `{"name":"probe","description":"a test part","version":1,"levels":["simple"],"params":[]}`

const draftMoving = `<style>#d{position:absolute;left:560px;top:200px;width:120px;height:120px;border-radius:50%;background:var(--accent)}</style><div id="d"></div>
<script>window.__partSeek=function(t,b){var k=Math.pow(1-b.position,2);document.getElementById('d').style.transform='translateX('+(b.number*37%400)+'px) scale('+(1+k)+')';};</script>`

const draftStill = `<div style="position:absolute;left:10px;top:10px;width:50px;height:50px"></div><script>window.__partSeek=function(t,b){};</script>`

type draftWriter struct {
	mu      sync.Mutex
	answers []string
	n       int
}

func (w *draftWriter) Write(ctx context.Context, system, user string, max int) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	i := w.n
	if i >= len(w.answers) {
		i = len(w.answers) - 1
	}
	w.n++
	return w.answers[i], nil
}

func draftAnswer(manifest, fragment string) string {
	return "```json\n" + manifest + "\n```\n```html\n" + fragment + "\n```"
}

// draftServer is a server with a real browser (the test is skipped without one), the writer that answers answers in
// turn, a describer that answers "gold on black" and a settings folder of its own.
func draftServer(t *testing.T, answers ...string) (*testServer, *draftWriter) {
	t.Helper()
	b, err := motion.Find("")
	if err != nil {
		t.Skip("no Chrome or Chromium on this machine")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s, _ := newProjectsServer(t)
	w := &draftWriter{answers: answers}
	s.motionBrowser = func() (motion.Browser, error) { return b, nil }
	s.motionWriter = func(context.Context) (motion.Writer, motion.ModelInfo, error) { return w, motion.ModelInfo{}, nil }
	s.draftDescriber = func(context.Context, []byte) (string, error) { return "gold on black", nil }
	return s, w
}

func postDraft(t *testing.T, s *testServer, body string) (int, musicmotion.Draft, string) {
	t.Helper()
	rec := call(s.Handler(), http.MethodPost, "/api/music-motion/part-drafts", body)
	var d musicmotion.Draft
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	return rec.Code, d, rec.Body.String()
}

func getDraft(t *testing.T, s *testServer, id string) (int, musicmotion.Draft) {
	t.Helper()
	rec := call(s.Handler(), http.MethodGet, "/api/music-motion/part-drafts/"+id, "")
	var d musicmotion.Draft
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	return rec.Code, d
}

func postUpload(t *testing.T, s *testServer, request string, file []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("request", request)
	if file != nil {
		fw, _ := mw.CreateFormFile("file", "pic.png")
		fw.Write(file)
	}
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/music-motion/part-drafts", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

var draftPNG = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 40)...)

func TestPartDrafts_ASentenceAloneMakesAReadyDraftWithAPreview(t *testing.T) {
	s, _ := draftServer(t, draftAnswer(draftManifest, draftMoving))
	code, d, body := postDraft(t, s, `{"request":"a coral dot that swells"}`)
	if code != http.StatusAccepted || d.State != musicmotion.DraftWriting || d.WithPicture {
		t.Fatalf("%d %s", code, body)
	}
	s.waitDrafts()
	code, got := getDraft(t, s, d.ID)
	if code != 200 || got.State != musicmotion.DraftReady || got.Name != "probe" || len(got.Preview) != musicmotion.DraftPreviewFrames || got.Manifest == "" || got.Fragment == "" {
		t.Fatalf("%d %+v", code, got)
	}
	rec := call(s.Handler(), http.MethodGet, "/api/music-motion/part-drafts/"+d.ID+"/preview/"+got.Preview[0], "")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || !bytes.HasPrefix(rec.Body.Bytes(), []byte("\x89PNG")) {
		t.Fatalf("%d", rec.Code)
	}
	for _, name := range []string{"..%2Fdraft.json", "99.png", "x.png"} {
		if rec := call(s.Handler(), http.MethodGet, "/api/music-motion/part-drafts/"+d.ID+"/preview/"+name, ""); rec.Code != 404 {
			t.Fatalf("%s: %d", name, rec.Code)
		}
	}
	rec = call(s.Handler(), http.MethodGet, "/api/music-motion/part-drafts", "")
	var list struct{ Drafts []musicmotion.Draft }
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Drafts) != 1 || list.Drafts[0].Manifest != "" {
		t.Fatalf("the list has no files: %s", rec.Body)
	}
	cat := call(s.Handler(), http.MethodGet, "/api/music-motion/catalog", "")
	if strings.Contains(cat.Body.String(), `"probe"`) {
		t.Fatal("a draft is not in the catalog")
	}
}

func TestPartDrafts_AFrameOfAReferenceIsDescribedAndKept(t *testing.T) {
	s, _ := draftServer(t, draftAnswer(draftManifest, draftMoving))
	ref, err := s.Assistant.RefStore().Create("Gold", "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(s.Assistant.RefStore().FramesDir(ref.ID), "03.jpg"), []byte("\xff\xd8\xffjpg"), 0o644)
	ref.State, ref.Analysis = motionref.StateReady, &motionref.Analysis{Frames: []string{"03.jpg"}}
	_ = s.Assistant.RefStore().Save(ref)
	code, d, body := postDraft(t, s, `{"request":"like this","reference_id":"`+ref.ID+`","frame":"03.jpg"}`)
	if code != http.StatusAccepted || !d.WithPicture || d.Source != "reference:"+ref.ID+"/03.jpg" {
		t.Fatalf("%d %s", code, body)
	}
	s.waitDrafts()
	if _, got := getDraft(t, s, d.ID); got.State != musicmotion.DraftReady || got.Description != "gold on black" {
		t.Fatalf("%+v", got)
	}
	// The draft has its own copy: the reference can go.
	call(s.Handler(), http.MethodDelete, "/api/music-motion/references/"+ref.ID, "")
	if _, err := os.Stat(filepath.Join(s.draftStore().Dir, d.ID, "frame.jpg")); err != nil {
		t.Fatal("the copy stays with the draft")
	}
}

func TestPartDrafts_AnUploadedPictureIsDescribedAndAMinorStopsTheDraft(t *testing.T) {
	s, w := draftServer(t, draftAnswer(draftManifest, draftMoving))
	rec := postUpload(t, s, "like this picture", draftPNG)
	var d musicmotion.Draft
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	if rec.Code != http.StatusAccepted || d.Source != "upload" || !d.WithPicture {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	s.waitDrafts()
	if _, got := getDraft(t, s, d.ID); got.State != musicmotion.DraftReady {
		t.Fatalf("%+v", got)
	}
	s.draftDescriber = func(context.Context, []byte) (string, error) {
		return "", errors.New("wrapped: " + musicmotion.ErrMinorPicture.Error())
	}
	s.draftDescriber = func(context.Context, []byte) (string, error) { return "", musicmotion.ErrMinorPicture }
	before := w.n
	rec = postUpload(t, s, "again", draftPNG)
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	s.waitDrafts()
	_, got := getDraft(t, s, d.ID)
	if got.State != musicmotion.DraftFailed || !strings.Contains(got.Error, "under 18") || w.n != before {
		t.Fatalf("a minor: nothing is written: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(s.draftStore().Dir, d.ID, "frame.jpg")); err == nil {
		t.Fatal("the picture of a minor is not kept")
	}
}

func TestPartDrafts_WhatCannotBeDoneIsRefusedAndNothingIsCreated(t *testing.T) {
	s, _ := draftServer(t, draftAnswer(draftManifest, draftMoving))
	bad := map[string]string{
		"no sentence":   `{"request":"  "}`,
		"too long":      `{"request":"` + strings.Repeat("x", 501) + `"}`,
		"no reference":  `{"request":"a","reference_id":"ref-nope","frame":"00.jpg"}`,
		"only a frame":  `{"request":"a","frame":"00.jpg"}`,
		"not a picture": "",
	}
	for name, body := range bad {
		var code int
		if name == "not a picture" {
			code = postUpload(t, s, "a part", []byte("plain text")).Code
		} else {
			code, _, _ = postDraft(t, s, body)
		}
		if code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d", name, code)
		}
	}
	// A frame that is not one of the reference's.
	ref, _ := s.Assistant.RefStore().Create("R", "", nil, "")
	ref.State, ref.Analysis = motionref.StateReady, &motionref.Analysis{Frames: []string{"00.jpg"}}
	_ = s.Assistant.RefStore().Save(ref)
	if code, _, _ := postDraft(t, s, `{"request":"a","reference_id":"`+ref.ID+`","frame":"07.jpg"}`); code != 422 {
		t.Errorf("unknown frame: %d", code)
	}
	if code, _, _ := postDraft(t, s, `{"request":"a","reference_id":"`+ref.ID+`","frame":"../x"}`); code != 422 {
		t.Errorf("a path is not a frame: %d", code)
	}
	if list := s.draftStore().List(); len(list) != 0 {
		t.Fatalf("nothing is created: %+v", list)
	}
}

func TestPartDrafts_WithoutBrowserAssistantOrVisionTheAnswerIs503(t *testing.T) {
	s, _ := draftServer(t, draftAnswer(draftManifest, draftMoving))
	s.motionBrowser = func() (motion.Browser, error) { return motion.Browser{}, errors.New("none") }
	if code, _, body := postDraft(t, s, `{"request":"a"}`); code != 503 || !strings.Contains(body, "Chrome") {
		t.Fatalf("%d %s", code, body)
	}
	s, _ = draftServer(t, draftAnswer(draftManifest, draftMoving))
	s.motionWriter = func(context.Context) (motion.Writer, motion.ModelInfo, error) {
		return nil, motion.ModelInfo{}, errors.New("no model")
	}
	if code, _, body := postDraft(t, s, `{"request":"a"}`); code != 503 || !strings.Contains(body, "assistant") {
		t.Fatalf("%d %s", code, body)
	}
	s, _ = draftServer(t, draftAnswer(draftManifest, draftMoving))
	s.draftVisionCheck = func(context.Context) string { return "no vision model is installed" }
	if rec := postUpload(t, s, "a", draftPNG); rec.Code != 503 || !strings.Contains(rec.Body.String(), "vision") {
		t.Fatalf("a picture that was given is not dropped: %d %s", rec.Code, rec.Body)
	}
	// ...but a sentence alone needs no vision model.
	if code, _, _ := postDraft(t, s, `{"request":"a"}`); code != http.StatusAccepted {
		t.Fatalf("%d", code)
	}
	s.waitDrafts()
	if list := s.draftStore().List(); len(list) != 1 {
		t.Fatalf("only the sentence draft exists: %d", len(list))
	}
}

func TestPartDrafts_WhileARunIsBusyTheLocalModelsWait(t *testing.T) {
	s, _ := draftServer(t, draftAnswer(draftManifest, draftMoving))
	releaseRun := s.Workspace.SimulateRun("busy", nil)
	if code, _, _ := postDraft(t, s, `{"request":"a"}`); code != http.StatusConflict {
		t.Fatalf("%d", code)
	}
	releaseRun()
}

func TestPartDrafts_DeleteAndUnknown(t *testing.T) {
	s, _ := draftServer(t, draftAnswer(draftManifest, draftMoving))
	_, d, _ := postDraft(t, s, `{"request":"a"}`)
	s.waitDrafts()
	if rec := call(s.Handler(), http.MethodDelete, "/api/music-motion/part-drafts/"+d.ID, ""); rec.Code != 204 {
		t.Fatalf("%d", rec.Code)
	}
	for _, m := range []string{http.MethodGet, http.MethodDelete, http.MethodPut, http.MethodPost} {
		path := "/api/music-motion/part-drafts/" + d.ID
		if m == http.MethodPost {
			path += "/approve"
		}
		if rec := call(s.Handler(), m, path, `{}`); rec.Code != 404 {
			t.Errorf("%s: %d", m, rec.Code)
		}
	}
	if _, err := os.Stat(filepath.Join(s.draftStore().Dir, d.ID)); err == nil {
		t.Fatal("the files go with it")
	}
}

func putDraft(t *testing.T, s *testServer, id, manifest, fragment string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"manifest": manifest, "fragment": fragment})
	return call(s.Handler(), http.MethodPut, "/api/music-motion/part-drafts/"+id, string(b))
}

func TestPartDrafts_EditIsCheckedAndANewPreviewIsDrawn(t *testing.T) {
	s, _ := draftServer(t, draftAnswer(draftManifest, draftStill))
	_, d, _ := postDraft(t, s, `{"request":"a"}`)
	s.waitDrafts()
	if _, got := getDraft(t, s, d.ID); got.State != musicmotion.DraftFailed || len(got.Problems) == 0 {
		t.Fatalf("a part that never moves fails: %+v", got)
	}
	// A bad edit: 422 with the problems and the draft as it was.
	rec := putDraft(t, s, d.ID, draftManifest, `<img src="https://example.com/a.png">`+draftMoving)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "external") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if _, got := getDraft(t, s, d.ID); got.State != musicmotion.DraftFailed || strings.Contains(got.Fragment, "example.com") {
		t.Fatalf("%+v", got)
	}
	// A good edit.
	rec = putDraft(t, s, d.ID, draftManifest, draftMoving)
	var got musicmotion.Draft
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != 200 || got.State != musicmotion.DraftReady || len(got.Preview) != musicmotion.DraftPreviewFrames || !strings.Contains(got.Fragment, "translateX") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := call(s.Handler(), http.MethodPut, "/api/music-motion/part-drafts/"+d.ID, `{`); rec.Code != 400 {
		t.Fatalf("%d", rec.Code)
	}
	if rec := putDraft(t, s, d.ID, `{"name":"toggle","description":"x","version":1,"levels":["simple"],"params":[]}`, draftMoving); rec.Code != 422 {
		t.Fatalf("a name of the catalog: %d", rec.Code)
	}
}

func TestPartDrafts_AnotherAttemptOnAFailedDraft(t *testing.T) {
	s, w := draftServer(t, draftAnswer(draftManifest, draftStill), draftAnswer(draftManifest, draftStill), draftAnswer(draftManifest, draftStill), draftAnswer(draftManifest, draftMoving))
	_, d, _ := postDraft(t, s, `{"request":"a"}`)
	s.waitDrafts()
	if _, got := getDraft(t, s, d.ID); got.State != musicmotion.DraftFailed || got.Attempts != musicmotion.Attempts {
		t.Fatalf("%+v", got)
	}
	rec := call(s.Handler(), http.MethodPost, "/api/music-motion/part-drafts/"+d.ID+"/retry", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	s.waitDrafts()
	if _, got := getDraft(t, s, d.ID); got.State != musicmotion.DraftReady || got.Attempts != 1 || w.n != musicmotion.Attempts+1 {
		t.Fatalf("%+v %d", got, w.n)
	}
	if rec := call(s.Handler(), http.MethodPost, "/api/music-motion/part-drafts/"+d.ID+"/retry", ""); rec.Code != 422 {
		t.Fatalf("a ready draft is not tried again: %d", rec.Code)
	}
}

func TestPartDrafts_OnlyApprovingAddsThePartToTheCatalog(t *testing.T) {
	s, _ := draftServer(t, draftAnswer(draftManifest, draftMoving))
	_, d, _ := postDraft(t, s, `{"request":"a"}`)
	s.waitDrafts()
	partNames := func() string { return call(s.Handler(), http.MethodGet, "/api/music-motion/catalog", "").Body.String() }
	if strings.Contains(partNames(), `"probe"`) {
		t.Fatal("not before approving")
	}
	rec := call(s.Handler(), http.MethodPost, "/api/music-motion/part-drafts/"+d.ID+"/approve", "")
	var part CatalogPart
	_ = json.Unmarshal(rec.Body.Bytes(), &part)
	if rec.Code != 200 || part.Name != "probe" || part.Source != "user" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(partNames(), `"probe"`) {
		t.Fatal("approved: the catalog lists it")
	}
	if code, _ := getDraft(t, s, d.ID); code != 404 {
		t.Fatalf("the draft is gone: %d", code)
	}
	// A second draft with the same name is refused with 409 and stays.
	_, d2, _ := postDraft(t, s, `{"request":"again"}`)
	s.waitDrafts()
	_, got := getDraft(t, s, d2.ID)
	if got.State != musicmotion.DraftFailed {
		t.Fatalf("the name is taken now, so the assistant's answer is refused: %+v", got)
	}
}

func TestPartDrafts_ANameTakenInTheUsersFolderIsA409AndAFailedDraftIsNotApproved(t *testing.T) {
	s, _ := draftServer(t, draftAnswer(draftManifest, draftStill))
	_, d, _ := postDraft(t, s, `{"request":"a"}`)
	s.waitDrafts()
	if rec := call(s.Handler(), http.MethodPost, "/api/music-motion/part-drafts/"+d.ID+"/approve", ""); rec.Code != 422 {
		t.Fatalf("a failed draft: %d", rec.Code)
	}
	if rec := putDraft(t, s, d.ID, draftManifest, draftMoving); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	// Somebody added a part of that name to the catalog after the draft was made.
	if err := s.mmCatalog().CreatePart([]byte(draftManifest), draftMoving, nil); err != nil {
		t.Fatal(err)
	}
	if rec := call(s.Handler(), http.MethodPost, "/api/music-motion/part-drafts/"+d.ID+"/approve", ""); rec.Code != http.StatusConflict {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if code, got := getDraft(t, s, d.ID); code != 200 || got.State != musicmotion.DraftReady {
		t.Fatalf("the draft stays: %d %+v", code, got)
	}
}

func TestPartDrafts_AnApprovedPartRemembersTheVideoItCameFromEvenAfterTheReferenceIsGone(t *testing.T) {
	s, _ := draftServer(t, draftAnswer(draftManifest, draftMoving))
	const video = "https://www.youtube.com/watch?v=abcdefghijk"
	ref, err := s.Assistant.RefStore().Create("Gold", "", nil, video)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(s.Assistant.RefStore().FramesDir(ref.ID), "03.jpg"), []byte("\xff\xd8\xffjpg"), 0o644)
	ref.State, ref.Analysis = motionref.StateReady, &motionref.Analysis{Frames: []string{"03.jpg"}}
	_ = s.Assistant.RefStore().Save(ref)
	code, d, body := postDraft(t, s, `{"request":"like this","reference_id":"`+ref.ID+`","frame":"03.jpg"}`)
	if code != http.StatusAccepted || d.ReferenceID != ref.ID || d.Video != video || d.VideoTitle != "Gold" {
		t.Fatalf("the draft says which video inspired it: %d %s", code, body)
	}
	s.waitDrafts()
	// The reference is deleted before the draft is approved: the origin is in the draft.
	call(s.Handler(), http.MethodDelete, "/api/music-motion/references/"+ref.ID, "")
	rec := call(s.Handler(), http.MethodPost, "/api/music-motion/part-drafts/"+d.ID+"/approve", "")
	var part CatalogPart
	_ = json.Unmarshal(rec.Body.Bytes(), &part)
	o := part.Origin
	if rec.Code != 200 || o == nil || o.ReferenceID != ref.ID || o.Title != "Gold" || o.Video != video || o.Frame != "03.jpg" || o.Learned || o.ApprovedAt.IsZero() {
		t.Fatalf("the part says where it came from: %d %s", rec.Code, rec.Body)
	}
	// The catalog lists it with that origin, on every read (it is in origin.json beside the part).
	var cat PieceCatalog
	_ = json.Unmarshal(call(s.Handler(), http.MethodGet, "/api/music-motion/catalog", "").Body.Bytes(), &cat)
	found := false
	for _, p := range cat.Parts {
		if p.Name == "probe" {
			found = p.Origin != nil && p.Origin.Video == video
		}
		if p.Source == "builtin" && p.Origin != nil {
			t.Fatalf("a part that comes with the program has no origin: %s", p.Name)
		}
	}
	if !found {
		t.Fatalf("the catalog shows the origin of the user's part: %+v", cat.Parts)
	}
}

func TestPartDrafts_APartAskedForWithASentenceAloneHasNoOrigin(t *testing.T) {
	s, _ := draftServer(t, draftAnswer(draftManifest, draftMoving))
	_, d, _ := postDraft(t, s, `{"request":"a coral dot"}`)
	s.waitDrafts()
	rec := call(s.Handler(), http.MethodPost, "/api/music-motion/part-drafts/"+d.ID+"/approve", "")
	var part CatalogPart
	_ = json.Unmarshal(rec.Body.Bytes(), &part)
	if rec.Code != 200 || part.Origin != nil || strings.Contains(rec.Body.String(), `"origin"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if p, ok := s.mmCatalog().Get("probe"); !ok || p.Origin != nil {
		t.Fatalf("the part is in the catalog and has no origin when it was written from a sentence: %+v", p)
	}
}

func TestPartDrafts_APartOfTheUserCanBeReadBack(t *testing.T) {
	s, _ := draftServer(t, draftAnswer(draftManifest, draftMoving))
	_, d, _ := postDraft(t, s, `{"request":"a"}`)
	s.waitDrafts()
	if rec := call(s.Handler(), http.MethodPost, "/api/music-motion/part-drafts/"+d.ID+"/approve", ""); rec.Code != 200 && rec.Code != 201 {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}
	rec := call(s.Handler(), http.MethodGet, "/api/music-motion/parts/probe", "")
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var got UserPart
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "probe" || got.Source != "user" || got.Fragment != draftMoving || got.Manifest.Name != "probe" {
		t.Fatalf("%+v", got)
	}
	if strings.Contains(rec.Body.String(), settings.Dir()) {
		t.Fatalf("the answer names a folder of this machine: %s", rec.Body)
	}
	// The same part can be sent as a draft to another server: its two files are what a part is.
	if _, probs := musicmotion.ParsePart(mustJSON(t, got.Manifest), got.Fragment); len(probs) != 0 {
		t.Fatalf("%v", probs)
	}
	for _, name := range []string{"nope", "..%2F..%2Fsettings", "probe%2F..%2Fterminal"} {
		if rec := call(s.Handler(), http.MethodGet, "/api/music-motion/parts/"+name, ""); rec.Code != http.StatusNotFound {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestUserParts_ReplaceAndDeleteAPartOfTheUser(t *testing.T) {
	s, _ := draftServer(t, draftAnswer(draftManifest, draftMoving))
	_, d, _ := postDraft(t, s, `{"request":"a"}`)
	s.waitDrafts()
	call(s.Handler(), http.MethodPost, "/api/music-motion/part-drafts/"+d.ID+"/approve", "")
	put := func(name, manifest, fragment string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(DraftPut{Manifest: manifest, Fragment: fragment})
		return call(s.Handler(), http.MethodPut, "/api/music-motion/parts/"+name, string(b))
	}
	// Replaced with the checks of an approval; the catalog shows it at once.
	better := strings.Replace(draftManifest, "a test part", "a better part", 1)
	if rec := put("probe", better, draftMoving); rec.Code != 200 || !strings.Contains(rec.Body.String(), "a better part") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(call(s.Handler(), http.MethodGet, "/api/music-motion/catalog", "").Body.String(), "a better part") {
		t.Fatal("the catalog reads the change")
	}
	// What does not pass changes nothing; another name is refused; a name that is not a part is a 404.
	if rec := put("probe", better, draftStill); rec.Code != 422 {
		t.Fatalf("a still part: %d", rec.Code)
	}
	if rec := put("probe", strings.Replace(better, `"probe"`, `"other"`, 1), draftMoving); rec.Code != 422 {
		t.Fatalf("a renamed part: %d", rec.Code)
	}
	if rec := put("nothing", better, draftMoving); rec.Code != 404 {
		t.Fatalf("%d", rec.Code)
	}
	if rec := call(s.Handler(), http.MethodGet, "/api/music-motion/parts/probe", ""); !strings.Contains(rec.Body.String(), "a better part") {
		t.Fatalf("the old part stays whole: %s", rec.Body)
	}
	// A piece that uses it stops the delete, naming the piece; force does it.
	used := pieceInfo{ID: piecePrefix + "x", CreatedAt: time.Now().UTC()}
	if err := s.pieces().put(&used); err != nil {
		t.Fatal(err)
	}
	if err := s.place(used.ID).WriteDoc(musicmotion.DocScript, []byte(`{"pieces":[{"id":"a","start":0,"end":4,"part":"blank","with":[{"part":"probe"}]}]}`)); err != nil {
		t.Fatal(err)
	}
	rec := call(s.Handler(), http.MethodDelete, "/api/music-motion/parts/probe", "")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), used.ID) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := call(s.Handler(), http.MethodDelete, "/api/music-motion/parts/probe?force=true", ""); rec.Code != 204 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := call(s.Handler(), http.MethodGet, "/api/music-motion/parts/probe", ""); rec.Code != 404 {
		t.Fatalf("gone: %d", rec.Code)
	}
	// A part that comes with the program is not touched.
	if rec := call(s.Handler(), http.MethodDelete, "/api/music-motion/parts/blank", ""); rec.Code != http.StatusConflict {
		t.Fatalf("%d", rec.Code)
	}
	if rec := put("blank", better, draftMoving); rec.Code != http.StatusConflict {
		t.Fatalf("%d", rec.Code)
	}
}

func TestPartDrafts_FromScratchWithNoAssistant(t *testing.T) {
	s, w := draftServer(t) // the assistant has no answers: it must not be asked
	post := func(body string) *httptest.ResponseRecorder {
		return call(s.Handler(), http.MethodPost, "/api/music-motion/part-drafts", body)
	}
	b, _ := json.Marshal(DraftPut{Manifest: draftManifest, Fragment: draftMoving})
	rec := post(string(b))
	var d musicmotion.Draft
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	if rec.Code != 200 || d.State != musicmotion.DraftReady || d.Name != "probe" || len(d.Preview) == 0 || d.Manifest == "" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if w.n != 0 {
		t.Fatal("the assistant was asked")
	}
	if rec := call(s.Handler(), http.MethodPost, "/api/music-motion/part-drafts/"+d.ID+"/approve", ""); rec.Code != 200 {
		t.Fatalf("it goes on like any draft: %d %s", rec.Code, rec.Body)
	}
	// A part that does not pass is a failed draft with its problems and its files, which can be edited.
	b, _ = json.Marshal(DraftPut{Manifest: strings.Replace(draftManifest, "probe", "other", 1), Fragment: draftStill})
	rec = post(string(b))
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	if rec.Code != 200 || d.State != musicmotion.DraftFailed || len(d.Problems) == 0 || d.Fragment == "" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := putDraft(t, s, d.ID, strings.Replace(draftManifest, "probe", "other", 1), draftMoving); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	// A request with a picture or a frame and files is a 400.
	b, _ = json.Marshal(map[string]string{"request": "a", "manifest": draftManifest, "fragment": draftMoving, "frame": "01.jpg", "reference_id": "ref-x"})
	if rec := post(string(b)); rec.Code != 400 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestPartDrafts_AJSONBodyOfAPartMayBeLargeButNotWithoutLimit(t *testing.T) {
	s, _ := draftServer(t)
	post := func(frag string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(DraftPut{Manifest: draftManifest, Fragment: frag})
		return call(s.Handler(), http.MethodPost, "/api/music-motion/part-drafts", string(b))
	}
	// 200 KB went over the old 64 KiB limit and now passes the size check (the part itself is judged as ever).
	if rec := post(draftMoving + "<!-- " + strings.Repeat("x", 200<<10) + " -->"); rec.Code == 400 {
		t.Fatalf("a body of 200 KB was refused as too large: %s", rec.Body)
	}
	rec := post(draftMoving + "<!-- " + strings.Repeat("x", 9<<20) + " -->")
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "8 MiB") {
		t.Fatalf("%d %.200s", rec.Code, rec.Body)
	}
}

func TestPartDrafts_TheAssistantChangesTheTextOfAPart(t *testing.T) {
	changed := strings.Replace(draftManifest, "a test part", "the same, but bigger", 1)
	s, w := draftServer(t, draftAnswer(draftManifest, draftMoving), draftAnswer(changed, draftMoving))
	_, d, _ := postDraft(t, s, `{"request":"a"}`)
	s.waitDrafts()
	call(s.Handler(), http.MethodPost, "/api/music-motion/part-drafts/"+d.ID+"/approve", "")
	// "probe" is in the catalog now: the answer may keep its name.
	b, _ := json.Marshal(map[string]string{"request": "make it bigger", "manifest": draftManifest, "fragment": draftMoving})
	rec := call(s.Handler(), http.MethodPost, "/api/music-motion/part-drafts", string(b))
	var r musicmotion.Draft
	_ = json.Unmarshal(rec.Body.Bytes(), &r)
	if rec.Code != http.StatusAccepted || r.State != musicmotion.DraftWriting || r.Replaces != "probe" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	s.waitDrafts()
	_, got := getDraft(t, s, r.ID)
	if got.State != musicmotion.DraftReady || got.Name != "probe" || !strings.Contains(got.Manifest, "bigger") {
		t.Fatalf("%+v", got)
	}
	w.mu.Lock()
	n := w.n
	w.mu.Unlock()
	if n != 2 {
		t.Fatalf("the assistant was asked %d times", n)
	}
	// Without the manifest and the fragment, or with a manifest that has no name: a 422.
	b, _ = json.Marshal(map[string]string{"request": "x", "manifest": draftManifest})
	if rec := call(s.Handler(), http.MethodPost, "/api/music-motion/part-drafts", string(b)); rec.Code != 422 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// modelSeam makes the server list two installed models and records the model each writer is built for.
func modelSeam(s *testServer, w motion.Writer) *[]string {
	var mu sync.Mutex
	seen := &[]string{}
	s.Assistant.AssistModels = func(context.Context) (string, []assist.ModelInfo, error) {
		return "qwen3:8b", []assist.ModelInfo{{Name: "qwen3:8b"}, {Name: "gemma4:26b"}}, nil
	}
	s.motionWriter = func(ctx context.Context) (motion.Writer, motion.ModelInfo, error) {
		mu.Lock()
		defer mu.Unlock()
		*seen = append(*seen, motion.ResolveModel(ctx, llm.Resolved{}))
		return w, motion.ModelInfo{}, nil
	}
	return seen
}

func TestPartDrafts_ChoosingTheModel(t *testing.T) {
	s, w := draftServer(t, draftAnswer(draftManifest, draftStill), draftAnswer(draftManifest, draftStill), draftAnswer(draftManifest, draftStill), draftAnswer(draftManifest, draftMoving))
	seen := modelSeam(s, w)

	// A model that is not installed is refused with the installed ones, and nothing is created or written.
	code, _, body := postDraft(t, s, `{"request":"a coral dot","model":"nope:1b"}`)
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "nope:1b") || !strings.Contains(body, "gemma4:26b") || len(*seen) != 0 {
		t.Fatalf("%d %s (writers %v)", code, body, *seen)
	}
	if rec := call(s.Handler(), http.MethodGet, "/api/music-motion/part-drafts", ""); strings.Contains(rec.Body.String(), `"id"`) {
		t.Fatalf("a refused request created a draft: %s", rec.Body)
	}

	// An installed one reaches the writer, is kept in the draft and is used again by a retry.
	code, d, body := postDraft(t, s, `{"request":"a coral dot","model":" gemma4:26b "}`)
	if code != http.StatusAccepted || d.Model != "gemma4:26b" || len(*seen) != 1 || (*seen)[0] != "gemma4:26b" {
		t.Fatalf("%d %s (writers %v)", code, body, *seen)
	}
	s.waitDrafts()
	if _, got := getDraft(t, s, d.ID); got.State != musicmotion.DraftFailed || got.Model != "gemma4:26b" {
		t.Fatalf("%+v", got)
	}
	if rec := call(s.Handler(), http.MethodPost, "/api/music-motion/part-drafts/"+d.ID+"/retry", ""); rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	s.waitDrafts()
	if len(*seen) != 2 || (*seen)[1] != "gemma4:26b" {
		t.Fatalf("the retry used %v", *seen)
	}

	// Without a model the routing's own (here the default) is used, and the draft says none.
	code, d2, _ := postDraft(t, s, `{"request":"another dot"}`)
	if code != http.StatusAccepted || d2.Model != "" || (*seen)[2] == "gemma4:26b" {
		t.Fatalf("%d %+v %v", code, d2, *seen)
	}
	s.waitDrafts()
}

func TestPartDrafts_TheModelCanComeInAMultipartForm(t *testing.T) {
	s, w := draftServer(t, draftAnswer(draftManifest, draftMoving))
	seen := modelSeam(s, w)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("request", "a coral dot")
	mw.WriteField("model", "gemma4:26b")
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/music-motion/part-drafts", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted || len(*seen) != 1 || (*seen)[0] != "gemma4:26b" || !strings.Contains(rec.Body.String(), `"model":"gemma4:26b"`) {
		t.Fatalf("%d %s (writers %v)", rec.Code, rec.Body, *seen)
	}
	s.waitDrafts()
}

func TestPartDrafts_ChoosingTheProvider(t *testing.T) {
	s, w := draftServer(t, draftAnswer(draftManifest, draftStill), draftAnswer(draftManifest, draftStill), draftAnswer(draftManifest, draftStill))
	var mu sync.Mutex
	var seen []string
	s.motionWriter = func(ctx context.Context) (motion.Writer, motion.ModelInfo, error) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, llm.ProviderFrom(ctx))
		return w, motion.ModelInfo{}, nil
	}
	for _, p := range []settings.Provider{
		{ID: "lan", Name: "LAN", Type: settings.TypeOpenAI, BaseURL: "http://127.0.0.1:8000/v1", TextModel: "m", VisionModel: "v"},
		{ID: "cloud", Name: "Cloud", Type: settings.TypeOpenAI, BaseURL: "https://example.com/v1", TextModel: "m", VisionModel: "v"},
	} {
		if _, err := settings.AddProvider(p); err != nil {
			t.Fatal(err)
		}
	}
	// Unknown and not-consented providers are refused before anything is created.
	if code, _, body := postDraft(t, s, `{"request":"a dot","provider":"nope"}`); code != http.StatusBadRequest || !strings.Contains(body, "nope") {
		t.Fatalf("%d %s", code, body)
	}
	if code, _, body := postDraft(t, s, `{"request":"a dot","provider":"cloud"}`); code != http.StatusBadRequest || !strings.Contains(body, "consent_required") {
		t.Fatalf("%d %s", code, body)
	}
	if len(seen) != 0 {
		t.Fatalf("a refused request reached the writer: %v", seen)
	}
	// A usable one reaches the writer, is kept in the draft and is used again by a retry.
	code, d, body := postDraft(t, s, `{"request":"a dot","provider":" lan "}`)
	if code != http.StatusAccepted || d.Provider != "lan" || len(seen) != 1 || seen[0] != "lan" {
		t.Fatalf("%d %s (writers %v)", code, body, seen)
	}
	s.waitDrafts()
	if rec := call(s.Handler(), http.MethodPost, "/api/music-motion/part-drafts/"+d.ID+"/retry", ""); rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	s.waitDrafts()
	if len(seen) != 2 || seen[1] != "lan" {
		t.Fatalf("the retry used %v", seen)
	}
}

package music

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/music/motionref"
	"veritmo/internal/music/musicmotion"
	"veritmo/internal/server/servertest"
	"veritmo/internal/video/motion"
)

// readyRef makes a reference of the library that is analysed, with three key frames on disk.
func readyRef(t *testing.T, s *testServer, title string) motionref.Reference {
	t.Helper()
	ref, err := s.Assistant.RefStore().Create(title, "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	ref.State = motionref.StateReady
	ref.Analysis = &motionref.Analysis{MeanShot: 3, Cuts: 40, Frames: []string{"00.jpg", "01.jpg", "02.jpg"},
		Colours:     []motionref.Colour{{Hex: "#0b1020", Share: 0.6}, {Hex: "#22e07a", Share: 0.25}, {Hex: "#f4f4f8", Share: 0.15}},
		Description: "Dark neon city at night with green glowing signs and fast cuts"}
	for _, f := range ref.Analysis.Frames {
		if err := os.WriteFile(filepath.Join(s.Assistant.RefStore().FramesDir(ref.ID), f), draftPNG, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Assistant.RefStore().Save(ref); err != nil {
		t.Fatal(err)
	}
	return ref
}

func learnServer(t *testing.T) *testServer {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s, _ := newProjectsServer(t)
	s.draftDescriber = func(context.Context, []byte) (string, error) { return "gold on black", nil }
	return s
}

func postLearn(t *testing.T, s *testServer, id, body string) (int, LearningView) {
	t.Helper()
	rec := call(s.Handler(), http.MethodPost, "/api/music-motion/references/"+id+"/learn", body)
	var v LearningView
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	return rec.Code, v
}

func getLearning(t *testing.T, s *testServer, id string) LearningView {
	t.Helper()
	rec := call(s.Handler(), http.MethodGet, "/api/music-motion/learnings/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get %s: %d %s", id, rec.Code, rec.Body)
	}
	var v LearningView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func noBrowser(s *testServer) {
	s.motionBrowser = func() (motion.Browser, error) { return motion.Browser{}, os.ErrNotExist }
}

func TestLearn_TheCallIsRefusedWhenThereIsNothingToLearnFrom(t *testing.T) {
	s := learnServer(t)
	noBrowser(s)
	if code, _ := postLearn(t, s, "ref-20260101-000000-abcdef", ""); code != http.StatusNotFound {
		t.Errorf("unknown reference: %d", code)
	}
	analysing, _ := s.Assistant.RefStore().Create("still", "", nil, "")
	if code, _ := postLearn(t, s, analysing.ID, ""); code != http.StatusConflict {
		t.Errorf("a reference that is not ready: %d", code)
	}
	ref := readyRef(t, s, "Neon")
	rec := call(s.Handler(), http.MethodPost, "/api/music-motion/references/"+ref.ID+"/learn", `{"strictness":"extreme"}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "low, normal or high") {
		t.Errorf("a strictness that does not exist: %d %s", rec.Code, rec.Body)
	}
	if rec := call(s.Handler(), http.MethodPost, "/api/music-motion/references/"+ref.ID+"/learn", `{`); rec.Code != http.StatusBadRequest {
		t.Errorf("a body that is not JSON: %d", rec.Code)
	}
	if n := len(s.mHandler.learningStore().List("")); n != 0 {
		t.Errorf("nothing is created by a refused call: %d", n)
	}
	if rec := call(s.Handler(), http.MethodGet, "/api/music-motion/learnings/learning-20260101-000000-abcdef", ""); rec.Code != http.StatusNotFound {
		t.Errorf("a learning that does not exist: %d", rec.Code)
	}
}

func TestLearn_WithoutABrowserTheStyleComesAndTheReasonIsSaid(t *testing.T) {
	s := learnServer(t)
	noBrowser(s)
	ref := readyRef(t, s, "Neon")
	code, v := postLearn(t, s, ref.ID, "")
	if code != http.StatusAccepted || v.State != "running" || v.Strictness != "normal" || v.ReferenceID != ref.ID {
		t.Fatalf("%d %+v", code, v)
	}
	s.mHandler.waitLearnings()
	// What the description says an answer looks like is what the handlers send.
	c := servertest.NewContractChecker(t)
	servertest.ExpectFits(t, c, "/api/music-motion/learnings/{id}", call(s.Handler(), http.MethodGet, "/api/music-motion/learnings/"+v.ID, "").Body.String())
	servertest.ExpectFits(t, c, "/api/music-motion/learnings", call(s.Handler(), http.MethodGet, "/api/music-motion/learnings", "").Body.String())
	got := getLearning(t, s, v.ID)
	if got.State != "ready" || len(got.Styles) != 1 || got.Styles[0].State != "ready" || !got.Styles[0].Novel || len(got.PartDrafts) != 0 {
		t.Fatalf("%+v", got)
	}
	if len(got.Skipped) != 1 || got.Skipped[0].What != "parts" || !strings.Contains(got.Skipped[0].Reason, "Chrome") {
		t.Fatalf("the reason of what was left out: %+v", got.Skipped)
	}
	// The style is an ordinary one, marked as learned.
	rec := call(s.Handler(), http.MethodGet, "/api/music-motion/presets", "")
	var list struct{ Presets []musicmotion.Preset }
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Presets) != 1 || list.Presets[0].Origin != musicmotion.OriginLearned || list.Presets[0].ID != got.Styles[0].ID {
		t.Fatalf("%s", rec.Body)
	}
	// It can be edited like any style and keeps its origin.
	p := list.Presets[0]
	b, _ := json.Marshal(map[string]any{"name": "My neon", "design": p.Design, "rhythm": p.Rhythm, "parts": []string{}})
	if rec := call(s.Handler(), http.MethodPut, "/api/music-motion/presets/"+p.ID, string(b)); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"origin":"learned"`) {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body)
	}
	// A second call finds that style and keeps nothing new.
	_, second := postLearn(t, s, ref.ID, `{"strictness":"normal"}`)
	s.mHandler.waitLearnings()
	again := getLearning(t, s, second.ID)
	if len(again.Styles) != 0 || len(again.Discarded) != 1 || again.Discarded[0].Kind != "style" || again.Discarded[0].SimilarTo.Kind != "style" {
		t.Fatalf("%+v", again)
	}
	// Deleting the style is shown by the learning that kept it.
	if rec := call(s.Handler(), http.MethodDelete, "/api/music-motion/presets/"+p.ID, ""); rec.Code != 204 {
		t.Fatal(rec.Code)
	}
	if v := getLearning(t, s, got.ID); v.Styles[0].State != "gone" {
		t.Fatalf("%+v", v.Styles)
	}
	// Without a body at all the strictness is normal; the learnings list newest first and can be cut by reference.
	rec = call(s.Handler(), http.MethodGet, "/api/music-motion/learnings?reference_id="+ref.ID, "")
	var all struct{ Learnings []LearningView }
	_ = json.Unmarshal(rec.Body.Bytes(), &all)
	if len(all.Learnings) != 2 || all.Learnings[0].ID != second.ID {
		t.Fatalf("%s", rec.Body)
	}
	if rec := call(s.Handler(), http.MethodGet, "/api/music-motion/learnings?reference_id=other", ""); !strings.Contains(rec.Body.String(), `"learnings":[]`) {
		t.Fatalf("%s", rec.Body)
	}
}

func TestLearn_AReferenceThatIsBeingLearnedFromIsNotLearnedFromTwice(t *testing.T) {
	s := learnServer(t)
	noBrowser(s)
	ref := readyRef(t, s, "Neon")
	running, err := s.mHandler.learningStore().Create(ref.ID, musicmotion.StrictnessHigh)
	if err != nil {
		t.Fatal(err)
	}
	code, v := postLearn(t, s, ref.ID, "")
	if code != http.StatusOK || v.ID != running.ID || v.Strictness != "high" {
		t.Fatalf("the running one is returned: %d %+v", code, v)
	}
	if n := len(s.mHandler.learningStore().List(ref.ID)); n != 1 {
		t.Fatalf("no second learning starts: %d", n)
	}
	// Another reference is not held back.
	other := readyRef(t, s, "Other")
	if code, _ := postLearn(t, s, other.ID, ""); code != http.StatusAccepted {
		t.Fatalf("%d", code)
	}
	s.mHandler.waitLearnings()
}

func TestLearn_ALearningThatWasRunningWhenTheServerStoppedReadsFailed(t *testing.T) {
	s := learnServer(t)
	ref := readyRef(t, s, "Neon")
	running, _ := s.mHandler.learningStore().Create(ref.ID, musicmotion.StrictnessNormal)
	s.mHandler.learnings = learningState{} // a new process opens the store again
	got := getLearning(t, s, running.ID)
	if got.State != "failed" || !strings.Contains(got.Reason, "interrupted") {
		t.Fatalf("%+v", got)
	}
}

// learnWriter answers the ideas of parts, the choice of parts of a style and the parts themselves.
type learnWriter struct {
	ideas []string
}

func (w *learnWriter) Write(ctx context.Context, system, user string, max int) (string, error) {
	switch {
	case strings.Contains(system, `"ideas"`):
		return `{"ideas": [` + strings.Join(w.ideas, ",") + `]}`, nil
	case strings.Contains(system, `"parts"`):
		return `{"parts": []}`, nil
	}
	return draftAnswer(`{"name":"probe","description":"a test part","version":1,"levels":["simple"],"params":[]}`, draftMoving), nil
}

func TestLearn_PartsComeAsLearnedDraftsThatWaitForApproval(t *testing.T) {
	b, err := motion.Find("")
	if err != nil {
		t.Skip("no Chrome or Chromium on this machine")
	}
	s := learnServer(t)
	s.motionBrowser = func() (motion.Browser, error) { return b, nil }
	counter, _ := musicmotion.NewCatalog(nil).Get("counter")
	w := &learnWriter{ideas: []string{
		fmt.Sprintf(`{"name": "ripple-rings", "idea": %q}`, "Concentric neon rings spread from the centre on each beat and fade into the dark"),
		fmt.Sprintf(`{"name": "counter-up", "idea": %q}`, counter.Description),
	}}
	s.motionWriter = func(context.Context) (motion.Writer, motion.ModelInfo, error) { return w, motion.ModelInfo{}, nil }
	ref := readyRef(t, s, "Neon")
	_, v := postLearn(t, s, ref.ID, "")
	s.mHandler.waitLearnings()
	s.mHandler.waitDrafts()
	got := getLearning(t, s, v.ID)
	servertest.ExpectFits(t, servertest.NewContractChecker(t), "/api/music-motion/learnings/{id}", call(s.Handler(), http.MethodGet, "/api/music-motion/learnings/"+v.ID, "").Body.String())
	if got.State != "ready" || len(got.Skipped) != 0 {
		t.Fatalf("%+v", got)
	}
	if len(got.PartDrafts) != 1 || got.PartDrafts[0].Name != "ripple-rings" || got.PartDrafts[0].State != musicmotion.DraftReady {
		t.Fatalf("one idea is new: %+v", got.PartDrafts)
	}
	if len(got.Discarded) != 1 || got.Discarded[0].Kind != "part" || got.Discarded[0].SimilarTo.Name != "counter" {
		t.Fatalf("the copy of the counter is dropped and says of what: %+v", got.Discarded)
	}
	code, d := getDraft(t, s, got.PartDrafts[0].ID)
	if code != 200 || d.Origin != musicmotion.OriginLearned || d.LearningID != v.ID || !d.WithPicture {
		t.Fatalf("%d %+v", code, d)
	}
	if cat := call(s.Handler(), http.MethodGet, "/api/music-motion/catalog", ""); strings.Contains(cat.Body.String(), `"probe"`) {
		t.Fatal("a learned part is not in the catalog until it is approved")
	}
	rec := call(s.Handler(), http.MethodPost, "/api/music-motion/part-drafts/"+d.ID+"/approve", "")
	if rec.Code != 200 {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}
	var approved CatalogPart
	_ = json.Unmarshal(rec.Body.Bytes(), &approved)
	if o := approved.Origin; o == nil || !o.Learned || o.LearningID != v.ID || o.ReferenceID != ref.ID || o.Title != "Neon" {
		t.Fatalf("a learned part keeps the learning and the reference it came from: %+v", approved.Origin)
	}
	if cat := call(s.Handler(), http.MethodGet, "/api/music-motion/catalog", ""); !strings.Contains(cat.Body.String(), `"probe"`) {
		t.Fatal("approved, it is in the catalog")
	}
	// An approved draft is no longer a draft: the learning says it is gone.
	if v := getLearning(t, s, v.ID); v.PartDrafts[0].State != "gone" {
		t.Fatalf("%+v", v.PartDrafts)
	}
}

func TestLearn_ALearnedStyleMakesAPieceLikeAnyStyle(t *testing.T) {
	s := mmServer(t)
	ref := readyRef(t, s, "Neon")
	ref.Analysis.Colours[1].Hex = "#22e07a"
	_ = s.Assistant.RefStore().Save(ref)
	_, v := postLearn(t, s, ref.ID, "")
	s.mHandler.waitLearnings()
	got := getLearning(t, s, v.ID)
	if len(got.Styles) != 1 {
		t.Fatalf("%+v", got)
	}
	piece := newPiece(t, s, map[string]any{"preset": got.Styles[0].ID})
	if piece.Preset != got.Styles[0].ID || piece.Design == nil || piece.Design.Design.Accent == "" {
		t.Fatalf("the piece follows the learned style: %+v", piece.Design)
	}
}

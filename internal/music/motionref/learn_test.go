package motionref

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"veritmo/internal/music/musicmotion"
)

// fakeAssistant answers the choice of parts of a style and the ideas of parts.
type fakeAssistant struct {
	ideas []partIdea
	err   error
	calls int
}

func (f *fakeAssistant) Write(ctx context.Context, system, user string, maxTokens int) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	if strings.Contains(system, `"ideas"`) {
		var parts []string
		for _, i := range f.ideas {
			parts = append(parts, fmt.Sprintf(`{"name": %q, "idea": %q}`, i.Name, i.Idea))
		}
		return `{"ideas": [` + strings.Join(parts, ",") + `]}`, nil
	}
	return `{"parts": ["counter"]}`, nil
}

func neonRef(id string) Reference {
	return Reference{ID: id, Title: "Neon city", State: StateReady, Analysis: &Analysis{
		MeanShot: 3, Cuts: 40, Frames: []string{"00.jpg", "01.jpg", "02.jpg"},
		Colours:     []Colour{{Hex: "#0b1020", Share: 0.6}, {Hex: "#22e07a", Share: 0.25}, {Hex: "#f4f4f8", Share: 0.15}},
		Description: "Dark neon city at night with green glowing signs and fast cuts",
	}}
}

type drafted struct{ learning, request, frame string }

func newEngine(t *testing.T, w musicmotion.Writer) (*LearnEngine, *[]drafted) {
	t.Helper()
	dir := t.TempDir()
	var made []drafted
	e := &LearnEngine{Store: NewLearningStore(dir + "/learnings"), Presets: NewPresetStore(dir + "/presets"), Drafts: musicmotion.NewDraftStore(dir + "/drafts"),
		Cat: musicmotion.NewCatalog(nil), Writer: w, WithPictures: true}
	e.StartDraft = func(ctx context.Context, learningID, request, frame string) (string, error) {
		made = append(made, drafted{learningID, request, frame})
		d, err := e.Drafts.Create(request, "reference:x/"+frame, nil)
		if err != nil {
			return "", err
		}
		d.Origin, d.LearningID = musicmotion.OriginLearned, learningID
		return d.ID, e.Drafts.Save(d)
	}
	return e, &made
}

func learn(t *testing.T, e *LearnEngine, ref Reference, st musicmotion.Strictness) Learning {
	t.Helper()
	l, err := e.Store.Create(ref.ID, st)
	if err != nil {
		t.Fatal(err)
	}
	e.Run(context.Background(), l.ID, ref)
	got, err := e.Store.Get(l.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestLearn_AStyleThatIsNewIsKeptAsLearned(t *testing.T) {
	e, _ := newEngine(t, nil)
	l := learn(t, e, neonRef("ref-a"), musicmotion.StrictnessNormal)
	if l.State != LearningReady || len(l.Styles) != 1 || !l.Styles[0].Novel || l.Styles[0].SimilarTo != nil {
		t.Fatalf("%+v", l)
	}
	p, err := e.Presets.Get(l.Styles[0].ID)
	if err != nil || p.Origin != musicmotion.OriginLearned || p.ReferenceID != "ref-a" {
		t.Fatalf("the style is an ordinary one marked as learned: %+v %v", p, err)
	}
	if l.FinishedAt == nil {
		t.Error("a finished learning says when")
	}
}

func TestLearn_LearningTwiceKeepsNothingNew(t *testing.T) {
	e, _ := newEngine(t, nil)
	learn(t, e, neonRef("ref-a"), musicmotion.StrictnessNormal)
	second := learn(t, e, neonRef("ref-a"), musicmotion.StrictnessNormal)
	if len(second.Styles) != 0 || len(second.Discarded) != 1 || second.Discarded[0].Kind != "style" || second.Discarded[0].Novel {
		t.Fatalf("the second call drops its own earlier output as similar: %+v", second)
	}
	if d := second.Discarded[0]; d.SimilarTo.Kind != "style" || d.SimilarTo.Distance >= 0.25 || !strings.Contains(d.Reason, "too similar") {
		t.Fatalf("it says what it is similar to and why: %+v", d)
	}
	if n := len(e.Presets.List()); n != 1 {
		t.Fatalf("no second style was stored: %d", n)
	}
}

func TestLearn_AnotherLookIsKeptNextToTheFirst(t *testing.T) {
	e, _ := newEngine(t, nil)
	learn(t, e, neonRef("ref-a"), musicmotion.StrictnessNormal)
	warm := Reference{ID: "ref-b", Title: "Warm film", State: StateReady, Analysis: &Analysis{MeanShot: 9, Frames: []string{"00.jpg"},
		Colours: []Colour{{Hex: "#2a1408", Share: 0.7}, {Hex: "#ff3da8", Share: 0.2}, {Hex: "#fff3e0", Share: 0.1}}, Description: "Warm soft film grain with slow dissolves"}}
	l := learn(t, e, warm, musicmotion.StrictnessNormal)
	if len(l.Styles) != 1 || l.Styles[0].SimilarTo == nil || len(e.Presets.List()) != 2 {
		t.Fatalf("a different look is kept and names its closest style: %+v", l)
	}
}

func TestLearn_StrictnessMovesTheLine(t *testing.T) {
	// A style a little off the first: low keeps it, high drops it.
	base := neonRef("ref-a")
	near := neonRef("ref-n")
	near.Analysis.MeanShot = 4
	near.Analysis.Colours[1].Hex = "#2ee07f"
	counts := map[musicmotion.Strictness]int{}
	for _, st := range []musicmotion.Strictness{musicmotion.StrictnessLow, musicmotion.StrictnessHigh} {
		e, _ := newEngine(t, nil)
		learn(t, e, base, musicmotion.StrictnessNormal)
		counts[st] = len(learn(t, e, near, st).Discarded)
	}
	if counts[musicmotion.StrictnessHigh] < counts[musicmotion.StrictnessLow] {
		t.Fatalf("high discards at least what low discards: %v", counts)
	}
	if counts[musicmotion.StrictnessHigh] != 1 {
		t.Fatalf("a near copy is dropped under high: %v", counts)
	}
}

func TestLearn_PartIdeasAreComparedBeforeADraftIsMade(t *testing.T) {
	counter, _ := musicmotion.NewCatalog(nil).Get("counter")
	w := &fakeAssistant{ideas: []partIdea{
		{Name: "counter", Idea: "anything"}, // the name of a part of the catalog
		{Name: "beat-count", Idea: counter.Description},
		{Name: "ripple-rings", Idea: "Concentric neon rings spread from the centre on each beat and fade into the dark"},
		{Name: "scan-bars", Idea: "Horizontal bars sweep across the frame in steps that follow the beat"},
	}}
	e, made := newEngine(t, w)
	l := learn(t, e, neonRef("ref-a"), musicmotion.StrictnessNormal)
	if len(l.Discarded) != 2 {
		t.Fatalf("the two near copies of the catalog are dropped: %+v", l.Discarded)
	}
	for _, d := range l.Discarded {
		if d.Kind != "part" || d.Idea == "" || d.SimilarTo.Name != "counter" {
			t.Errorf("%+v", d)
		}
	}
	if len(*made) != 1 || len(l.PartDrafts) != 1 {
		t.Fatalf("a draft only for the idea that is new (the fourth is past the cap of %d): %+v %+v", MaxPartIdeas, *made, l.PartDrafts)
	}
	dr, err := e.Drafts.Get(l.PartDrafts[0].ID)
	if err != nil || dr.Origin != musicmotion.OriginLearned || dr.LearningID != l.ID {
		t.Fatalf("the draft says it was learned: %+v %v", dr, err)
	}
	if (*made)[0].frame == "" {
		t.Error("with a vision model a key frame inspires the draft")
	}
}

func TestLearn_ADraftThatFailsToStartDoesNotStopTheOthers(t *testing.T) {
	w := &fakeAssistant{ideas: []partIdea{
		{Name: "ripple-rings", Idea: "Concentric neon rings spread from the centre on each beat and fade"},
		{Name: "scan-bars", Idea: "Horizontal bars sweep across the frame in steps that follow the beat"},
	}}
	e, _ := newEngine(t, w)
	inner := e.StartDraft
	e.StartDraft = func(ctx context.Context, lid, req, frame string) (string, error) {
		if strings.Contains(req, "Concentric") {
			return "", errors.New("the browser went away")
		}
		return inner(ctx, lid, req, frame)
	}
	l := learn(t, e, neonRef("ref-a"), musicmotion.StrictnessNormal)
	if l.State != LearningReady || len(l.PartDrafts) != 1 || l.PartDrafts[0].Name != "scan-bars" || len(l.Skipped) != 1 || !strings.Contains(l.Skipped[0].Reason, "browser") {
		t.Fatalf("%+v", l)
	}
}

func TestLearn_AnIdeaIsNotProposedAgainAfterItsDraftExists(t *testing.T) {
	w := &fakeAssistant{ideas: []partIdea{{Name: "ripple-rings", Idea: "Concentric neon rings spread from the centre on each beat and fade"}}}
	e, made := newEngine(t, w)
	learn(t, e, neonRef("ref-a"), musicmotion.StrictnessNormal)
	second := learn(t, e, neonRef("ref-a"), musicmotion.StrictnessNormal)
	if len(*made) != 1 || len(second.PartDrafts) != 0 || len(second.Discarded) < 1 {
		t.Fatalf("the second call finds the first one's draft: %d drafts, %+v", len(*made), second)
	}
}

func TestLearn_WithoutAnAssistantTheStyleStillComes(t *testing.T) {
	e, made := newEngine(t, nil)
	l := learn(t, e, neonRef("ref-a"), musicmotion.StrictnessNormal)
	if l.State != LearningReady || len(l.Styles) != 1 || len(l.PartDrafts) != 0 || len(*made) != 0 {
		t.Fatalf("%+v", l)
	}
	if len(l.Skipped) != 1 || l.Skipped[0].What != "parts" || !strings.Contains(l.Skipped[0].Reason, "assistant") {
		t.Fatalf("it says what was left out and why: %+v", l.Skipped)
	}
}

func TestLearn_WithNoBrowserOrNoVisionTheReasonIsNamed(t *testing.T) {
	w := &fakeAssistant{ideas: []partIdea{{Name: "ripple-rings", Idea: "Concentric neon rings spread from the centre on each beat and fade"}}}
	e, made := newEngine(t, w)
	e.PartsUnavailable = "there is no Chrome or Chromium to check and draw the part"
	l := learn(t, e, neonRef("ref-a"), musicmotion.StrictnessNormal)
	if l.State != LearningReady || len(*made) != 0 || len(l.Skipped) != 1 || !strings.Contains(l.Skipped[0].Reason, "Chrome") || w.calls != 1 {
		t.Fatalf("no browser: no ideas asked, the style is made: %+v (%d calls)", l, w.calls)
	}
	// Without a vision model the drafts come without a picture.
	e2, made2 := newEngine(t, w)
	e2.WithPictures = false
	learn(t, e2, neonRef("ref-a"), musicmotion.StrictnessNormal)
	if len(*made2) != 1 || (*made2)[0].frame != "" {
		t.Fatalf("no vision, no picture: %+v", *made2)
	}
}

func TestLearn_AnAssistantThatFailsLeavesTheStyleAndSaysWhy(t *testing.T) {
	e, _ := newEngine(t, &fakeAssistant{err: errors.New("ollama is down")})
	l := learn(t, e, neonRef("ref-a"), musicmotion.StrictnessNormal)
	if l.State != LearningReady || len(l.Styles) != 1 || len(l.Skipped) != 1 || !strings.Contains(l.Skipped[0].Reason, "ollama is down") {
		t.Fatalf("%+v", l)
	}
}

func TestLearningStore_ListsNewestFirstAndMarksInterrupted(t *testing.T) {
	t.Run("folder", func(t *testing.T) { learningStoreSuite(t, NewLearningStore(t.TempDir())) })
	t.Run("database", func(t *testing.T) { learningStoreSuite(t, NewLearningStoreDB(memStore(t))) })
}

func learningStoreSuite(t *testing.T, s *LearningStore) {
	a, _ := s.Create("ref-a", musicmotion.StrictnessNormal)
	b, _ := s.Create("ref-b", musicmotion.StrictnessHigh)
	if got, ok := s.Running("ref-a"); !ok || got.ID != a.ID {
		t.Fatalf("running: %+v %v", got, ok)
	}
	if got := s.List("ref-b"); len(got) != 1 || got[0].ID != b.ID || got[0].Strictness != "high" {
		t.Fatalf("a reference's learnings: %+v", got)
	}
	if n := s.MarkInterrupted(); n != 2 {
		t.Fatalf("interrupted: %d", n)
	}
	got, err := s.Get(a.ID)
	if err != nil || got.State != LearningFailed || !strings.Contains(got.Reason, "interrupted") || len(got.Styles) != 0 || got.Styles == nil {
		t.Fatalf("%+v %v", got, err)
	}
	if _, ok := s.Running("ref-a"); ok {
		t.Error("nothing runs any more")
	}
	if _, err := s.Get("../x"); err != ErrLearningNotFound {
		t.Fatalf("%v", err)
	}
}

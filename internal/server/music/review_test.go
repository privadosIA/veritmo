package music

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"veritmo/internal/music/musicmotion"
	"veritmo/internal/platform/settings"
)

// visionSeam is a vision model that answers every request with the next of its answers.
type visionSeam struct {
	mu      sync.Mutex
	answers []string
	users   []string
	images  []int
	err     error
}

func (v *visionSeam) Look(ctx context.Context, system, user string, images [][]byte) (string, string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.users = append(v.users, user)
	v.images = append(v.images, len(images))
	if v.err != nil {
		return "", "", v.err
	}
	i := min(len(v.users)-1, len(v.answers)-1)
	return v.answers[i], "seam-vision", nil
}

func (v *visionSeam) count() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.users)
}

// scoresEach is the reviewer's answer: the same score (0 to 3) for every criterion, the notes one to each criterion in turn.
func scoresEach(n string, notes ...string) string {
	var parts []string
	for i, c := range musicmotion.RubricCriteria {
		note := ""
		if i < len(notes) {
			note = notes[i]
		}
		parts = append(parts, `{"id": "`+c.Key+`", "score": `+n+`, "note": "`+note+`"}`)
	}
	return `{"criteria": [` + strings.Join(parts, ", ") + `]}`
}

func withVision(s *testServer, v *visionSeam) {
	s.reviewVisionSeam = func(context.Context) (musicmotion.Vision, error) { return v, nil }
}

func TestReview_ADraftAskedForAReviewIsGradedAndRewrittenFromItsNotes(t *testing.T) {
	s, w, v := sceneServer(t)
	vision := &visionSeam{answers: []string{scoresEach("1", "two accents: red and green"), scoresEach("3")}}
	withVision(s, vision)
	sp := v.Script.Pieces[1].ID
	body, _ := json.Marshal(map[string]any{"scene": map[string]any{"piece": v.ID, "script_piece": sp}, "review": map[string]any{"min": 7, "attempts": 2}})
	code, d, out := postDraft(t, s, string(body))
	if code != http.StatusAccepted || d.ReviewAsk == nil || d.ReviewAsk.Min != 7 {
		t.Fatalf("%d %s", code, out)
	}
	got := waitDraft(t, s, d.ID)
	if got.State != musicmotion.DraftReady || got.Review == nil || got.Review.Score != 10 || got.Review.Verdict != "pass" || len(got.Review.Attempts) != 2 ||
		got.Review.Attempts[0].Score != 3 || got.Review.Model != "seam-vision" {
		t.Fatalf("%+v %+v", got, got.Review)
	}
	if w.count() != 2 || !strings.Contains(w.asked[1], "two accents: red and green") || vision.images[0] != 1 {
		t.Fatalf("the notes reach the writer and the four frames, as one picture, the reviewer: %d %v", w.count(), vision.images)
	}
	if !strings.Contains(vision.users[0], "script piece") && !strings.Contains(vision.users[0], "frames") {
		t.Fatalf("%s", vision.users[0])
	}
	// read back with its review
	if _, again := getDraft(t, s, d.ID); again.Review == nil || again.Review.Score != 10 {
		t.Fatalf("%+v", again.Review)
	}
	// nothing was approved
	if names := catalogNames(t, s, "?include=scene"); has(names, "scene-") {
		t.Fatalf("%v", names)
	}
}

func TestReview_WhatCanBeAskedAndWhatIsLeftOutWhenItCannot(t *testing.T) {
	s, _, v := sceneServer(t)
	sp := v.Script.Pieces[0].ID
	scene := map[string]any{"piece": v.ID, "script_piece": sp}
	post := func(review any) int {
		m := map[string]any{"scene": scene}
		if review != nil {
			m["review"] = review
		}
		b, _ := json.Marshal(m)
		code, _, _ := postDraft(t, s, string(b))
		return code
	}
	if code := post(map[string]any{"min": 11}); code != http.StatusUnprocessableEntity {
		t.Errorf("min out of range: %d", code)
	}
	if code := post(map[string]any{"min": 7, "attempts": 4}); code != http.StatusUnprocessableEntity {
		t.Errorf("attempts out of range: %d", code)
	}
	if code := post("maybe"); code != http.StatusBadRequest {
		t.Errorf("not a bool or an object: %d", code)
	}
	// no vision model: refused before anything is created, with the reason
	s.reviewVisionSeam = func(context.Context) (musicmotion.Vision, error) {
		return nil, errors.New("no vision model is installed")
	}
	b, _ := json.Marshal(map[string]any{"scene": scene, "review": true})
	if code, _, out := postDraft(t, s, string(b)); code != http.StatusServiceUnavailable || !strings.Contains(out, "no vision model") {
		t.Errorf("%d %s", code, out)
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/music-motion/part-drafts", nil); strings.Contains(rec.Body.String(), `"scene"`) {
		t.Errorf("nothing was created: %s", rec.Body)
	}
	// review: false asks for none even when the setting is on; the draft carries no review
	vision := &visionSeam{answers: []string{scoresEach("3")}}
	withVision(s, vision)
	b, _ = json.Marshal(map[string]any{"scene": scene, "review": false})
	if code, d, _ := postDraft(t, s, string(b)); code != http.StatusAccepted || d.ReviewAsk != nil {
		t.Fatalf("%d %+v", code, d)
	} else if got := waitDraft(t, s, d.ID); got.Review != nil || vision.count() != 0 {
		t.Fatalf("%+v", got.Review)
	}
}

func TestReview_TheSettingReviewsEveryDraftAndIsInTheSettings(t *testing.T) {
	s, _, v := sceneServer(t)
	vision := &visionSeam{answers: []string{scoresEach("1", "weak")}}
	withVision(s, vision)
	// the setting is read from the settings file of the machine
	before := settingsReviewDrafts(t)
	setReviewDrafts(t, true)
	defer setReviewDrafts(t, before)
	code, d, out := postScene(t, s, map[string]any{"piece": v.ID, "script_piece": v.Script.Pieces[0].ID})
	if code != http.StatusAccepted || d.ReviewAsk == nil || d.ReviewAsk.Min != 0 {
		t.Fatalf("%d %s", code, out)
	}
	got := waitDraft(t, s, d.ID)
	if got.Review == nil || got.Review.Score != 3 || got.Review.Verdict != "fail" || len(got.Review.Attempts) != 1 {
		t.Fatalf("a review with no minimum grades once and never rewrites: %+v", got.Review)
	}
}

func TestReview_ABatchPassesItsReviewToEveryDraftAndAFailingVisionLeavesTheDraftsReady(t *testing.T) {
	s, _, v := sceneServer(t)
	vision := &visionSeam{err: errors.New("the model crashed")}
	withVision(s, vision)
	ids := []string{v.Script.Pieces[0].ID, v.Script.Pieces[1].ID}
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces/"+v.ID+"/scenes", map[string]any{"script_pieces": ids, "review": map[string]any{"min": 6}})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	b := waitBatch(t, s, v.ID, SceneBatchDone, SceneBatchFailed)
	if b.State != SceneBatchDone || b.Review == nil || b.Review.Min != 6 {
		t.Fatalf("%+v", b)
	}
	for _, d := range b.Drafts {
		_, got := getDraft(t, s, d.DraftID)
		if got.State != musicmotion.DraftReady || got.Review == nil || !strings.Contains(got.Review.Error, "the model crashed") || got.Review.Verdict != "" {
			t.Fatalf("%+v %+v", got.State, got.Review)
		}
	}
}

func TestReview_AWholePieceIsGradedSceneBySceneAndAsAWhole(t *testing.T) {
	s, _, v := sceneServer(t)
	n := len(v.Script.Pieces)
	answers := make([]string, 0, n+1)
	for i := 0; i < n; i++ {
		answers = append(answers, scoresEach("2", "scene note"))
	}
	answers = append(answers, `{"scores": {"consistency": 2, "variety": 1}, "notes": ["scenes 1 and 2 are the same stamp"], "repeated": [[1, 2]]}`)
	vision := &visionSeam{answers: answers}
	withVision(s, vision)
	base := "/api/music-motion/pieces/" + v.ID
	rec := doJSON(t, s.Handler(), http.MethodPost, base+"/review", nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var rv PieceReview
	for i := 0; i < 2400; i++ {
		if got := pieceOf(t, s, v.ID).Review; got != nil && got.State != PieceReviewRunning {
			rv = *got
			break
		}
		sleep25()
	}
	if rv.State != PieceReviewDone || len(rv.Scenes) != n || rv.Overall == nil || rv.Overall.Score != 8 || len(rv.Overall.Repeated) != 1 || rv.Model != "seam-vision" {
		t.Fatalf("%+v %+v", rv, rv.Overall)
	}
	for i, sc := range rv.Scenes {
		if sc.ScriptPiece != v.Script.Pieces[i].ID || sc.Score != 7 || sc.Verdict != "pass" || len(sc.Notes) != 1 {
			t.Fatalf("%+v", sc)
		}
	}
	for i := 0; i < n; i++ {
		if vision.images[i] != 1 {
			t.Fatalf("the three frames of every scene are one picture: %v", vision.images)
		}
	}
	if vision.images[n] != 1 || !strings.Contains(vision.users[n], v.Script.Pieces[0].ID) {
		t.Fatalf("one contact sheet: %v", vision.images)
	}
	// 409 while it runs, 422 without a script is covered by the piece having one
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces/api-mm-none/review", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("%d", rec.Code)
	}
}

func sleep25() { time.Sleep(25 * time.Millisecond) }

func settingsReviewDrafts(t *testing.T) bool {
	t.Helper()
	return settings.ReviewDrafts()
}

func setReviewDrafts(t *testing.T, on bool) {
	t.Helper()
	f := settings.Load()
	f.ReviewDrafts = on
	if err := settings.Save(f); err != nil {
		t.Fatal(err)
	}
}

// The piece review names first the scenes that are older than the last batch (the ones it could not write again).
func TestReview_TheFirstNoteNamesTheScenesOlderThanTheLastBatch(t *testing.T) {
	s, w, v := sceneServer(t)
	ids := []string{v.Script.Pieces[0].ID, v.Script.Pieces[1].ID}
	base := "/api/music-motion/pieces/" + v.ID
	w.bad = func(user string) bool { return strings.Contains(user, "("+ids[1]+")") }
	_ = doJSON(t, s.Handler(), http.MethodPost, base+"/scenes", map[string]any{"script_pieces": ids})
	waitBatch(t, s, v.ID, SceneBatchDone, SceneBatchFailed)
	n := len(v.Script.Pieces)
	answers := make([]string, 0, n+1)
	for i := 0; i < n; i++ {
		answers = append(answers, scoresEach("2", "scene note"))
	}
	answers = append(answers, `{"scores": {"consistency": 2, "variety": 1}, "notes": ["a note"], "repeated": []}`)
	withVision(s, &visionSeam{answers: answers})
	if rec := doJSON(t, s.Handler(), http.MethodPost, base+"/review", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var rv PieceReview
	for i := 0; i < 2400; i++ {
		if got := pieceOf(t, s, v.ID).Review; got != nil && got.State != PieceReviewRunning {
			rv = *got
			break
		}
		sleep25()
	}
	if rv.Overall == nil || len(rv.Overall.Notes) < 2 || !strings.Contains(rv.Overall.Notes[0], "older than the last batch") || !strings.Contains(rv.Overall.Notes[0], ids[1]) || strings.Contains(rv.Overall.Notes[0], ids[0]) {
		t.Fatalf("%+v", rv.Overall)
	}
}

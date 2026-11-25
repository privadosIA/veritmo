package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/music/motionref"
)

// shotsAsker is the vision model of the tests: it notes the state the reference was in at each question.
type shotsAsker struct {
	s      *Server
	id     func() string
	states []string
	closed bool
}

func (a *shotsAsker) Check(context.Context, []byte) error { return nil }
func (a *shotsAsker) Ask(context.Context, string, string, [][]byte, bool, int) (string, error) {
	if ref, err := a.s.assistant.RefStore().Get(a.id()); err == nil && ref.Analysis != nil {
		a.states = append(a.states, ref.Analysis.BreakdownState)
	}
	return `{"subject":"typography","text":"word","text_size":"huge","entrance":"word","camera":"static"}`, nil
}
func (a *shotsAsker) Close() { a.closed = true }

// withShots makes the analysis of the test give three shots with their reading still to do.
func withShots(s *Server) {
	analyse := s.assistant.RefAnalyse
	s.assistant.RefAnalyse = func(ctx context.Context, video, framesDir string) (*motionref.Analysis, error) {
		an, err := analyse(ctx, video, framesDir)
		if err != nil {
			return nil, err
		}
		an.Shots = []motionref.Shot{{Index: 0, Start: 0, End: 3}, {Index: 1, Start: 3, End: 6.5}, {Index: 2, Start: 6.5, End: 10}}
		an.Cuts, an.MeanShot = 2, 3.33
		an.BreakdownState = motionref.BreakdownPending
		return an, nil
	}
}

func TestReferences_TheShotsAreReadInTheBackgroundAfterTheReferenceIsReady(t *testing.T) {
	s, _ := refServer(t, "95.5", nil)
	withShots(s)
	var id string
	asker := &shotsAsker{s: s, id: func() string { return id }}
	var atOpen string
	s.assistant.RefReader = func(context.Context, int) (motionref.ShotAsker, error) {
		if ref, err := s.assistant.RefStore().Get(id); err == nil && ref.Analysis != nil {
			atOpen = ref.State + "/" + ref.Analysis.BreakdownState
		}
		return asker, nil
	}
	_, ref := addReference(t, s, `{"url":"`+goodURL+`"}`)
	id = ref.ID
	s.assistant.WaitReferences()
	_, got := getReference(t, s, id)
	if got.State != motionref.StateReady || got.Analysis.BreakdownState != motionref.BreakdownDone {
		t.Fatalf("%+v", got.Analysis)
	}
	if atOpen != "ready/pending" {
		t.Fatalf("the reference is ready before the reading, which is still to do: %q", atOpen)
	}
	if len(asker.states) != 3 || asker.states[0] != "reading" || !asker.closed {
		t.Fatalf("asked in the state %v, closed %v", asker.states, asker.closed)
	}
	for i, shot := range got.Analysis.Shots {
		if shot.Read == nil || shot.Read.Entrance != "word" {
			t.Fatalf("shot %d: %+v", i, shot.Read)
		}
		if _, err := os.Stat(filepath.Join(s.assistant.RefStore().ShotsDir(id), strings.Join([]string{string(rune('0' + i)), "jpg"}, "."))); err != nil {
			t.Fatalf("the picture of shot %d stays with the reference: %v", i, err)
		}
	}
	if !strings.Contains(got.Analysis.RhythmText, "100 % of shots are text filling the frame") {
		t.Fatalf("%q", got.Analysis.RhythmText)
	}
	// the frames route serves the key frames only, not the folder of the shots
	if rec := call(s.Handler(), http.MethodGet, "/api/music-motion/references/"+id+"/frames/shots%2F0.jpg", ""); rec.Code != 404 {
		t.Fatalf("a frame is a jpg of the frames folder, not of its subfolders: %d", rec.Code)
	}
}

func TestReferences_WithoutAModelTheReadingIsSkippedWithTheReason(t *testing.T) {
	s, _ := refServer(t, "95.5", nil)
	withShots(s)
	s.assistant.RefReader = func(context.Context, int) (motionref.ShotAsker, error) {
		return nil, os.ErrNotExist
	}
	_, ref := addReference(t, s, `{"url":"`+goodURL+`"}`)
	s.assistant.WaitReferences()
	_, got := getReference(t, s, ref.ID)
	if got.State != motionref.StateReady || got.Analysis.BreakdownState != motionref.BreakdownSkipped || !strings.Contains(got.Analysis.BreakdownNote, "no shots were read") {
		t.Fatalf("%+v", got.Analysis)
	}
	for _, shot := range got.Analysis.Shots {
		if shot.Read != nil {
			t.Fatal("nothing was read")
		}
	}
}

// breakdownServer is a server whose breakdown finds three shots at once (or waits for the context when block is given).
func breakdownServer(t *testing.T, block chan struct{}) *Server {
	t.Helper()
	s, _ := refServer(t, "95.5", nil) // the analysis of an old reference: no shots
	s.assistant.RefBreakdown = func(ctx context.Context, video string, an *motionref.Analysis) error {
		if block != nil {
			select {
			case <-block:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		an.Shots = []motionref.Shot{{Index: 0, Start: 0, End: 3}, {Index: 1, Start: 3, End: 6.5}, {Index: 2, Start: 6.5, End: 10}}
		an.Cuts, an.MeanShot = 2, 3.33
		an.RhythmText = "Cuts: made by the test."
		an.BreakdownState = motionref.BreakdownPending
		return nil
	}
	return s
}

func postBreakdown(s *Server, id, body string) *httptest.ResponseRecorder {
	return call(s.Handler(), http.MethodPost, "/api/music-motion/references/"+id+"/breakdown", body)
}

func TestBreakdown_AnOldReferenceGetsItsShotsAndKeepsTheRest(t *testing.T) {
	s := breakdownServer(t, nil)
	var id string
	s.assistant.RefReader = func(context.Context, int) (motionref.ShotAsker, error) {
		return &shotsAsker{s: s, id: func() string { return id }}, nil
	}
	_, ref := addReference(t, s, `{"url":"`+goodURL+`"}`)
	id = ref.ID
	s.assistant.WaitReferences()
	_, old := getReference(t, s, id)
	if old.Analysis.Shots != nil || old.Analysis.BreakdownState != "" {
		t.Fatalf("an old reference has no shots: %+v", old.Analysis)
	}
	rec := postBreakdown(s, id, `{}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	s.assistant.WaitReferences()
	_, got := getReference(t, s, id)
	if len(got.Analysis.Shots) != 3 || got.Analysis.BreakdownState != motionref.BreakdownDone || got.Analysis.Shots[2].Read == nil {
		t.Fatalf("%+v", got.Analysis)
	}
	if got.Analysis.Duration != old.Analysis.Duration || len(got.Analysis.Frames) != len(old.Analysis.Frames) || got.Analysis.Colours[0] != old.Analysis.Colours[0] {
		t.Fatalf("the key frames and the colours do not change: %+v", got.Analysis)
	}
	if got.Title != old.Title {
		t.Fatalf("%+v", got)
	}
}

func TestBreakdown_WithReadFalseTheShotsAreNotRead(t *testing.T) {
	s := breakdownServer(t, nil)
	s.assistant.RefReader = func(context.Context, int) (motionref.ShotAsker, error) {
		t.Error("the model must not be asked")
		return nil, os.ErrNotExist
	}
	_, ref := addReference(t, s, `{"url":"`+goodURL+`"}`)
	s.assistant.WaitReferences()
	if rec := postBreakdown(s, ref.ID, `{"read":false}`); rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	s.assistant.WaitReferences()
	_, got := getReference(t, s, ref.ID)
	if len(got.Analysis.Shots) != 3 || got.Analysis.BreakdownState != motionref.BreakdownSkipped || got.Analysis.Shots[0].Read != nil {
		t.Fatalf("%+v", got.Analysis)
	}
}

func TestBreakdown_TheFourAnswers(t *testing.T) {
	block := make(chan struct{})
	s := breakdownServer(t, block)
	s.assistant.RefReader = func(context.Context, int) (motionref.ShotAsker, error) { return nil, os.ErrNotExist }
	_, ref := addReference(t, s, `{"url":"`+goodURL+`"}`)
	s.assistant.WaitReferences()
	if rec := postBreakdown(s, "ref-20260101-000000-abcdef", ``); rec.Code != http.StatusNotFound {
		t.Fatalf("an unknown reference: %d", rec.Code)
	}
	if rec := postBreakdown(s, ref.ID, `{"read":`); rec.Code != http.StatusBadRequest {
		t.Fatalf("a body that is not JSON: %d", rec.Code)
	}
	if rec := postBreakdown(s, ref.ID, ``); rec.Code != http.StatusAccepted { // the breakdown waits for the block
		t.Fatalf("an empty body is the default: %d %s", rec.Code, rec.Body)
	}
	if rec := postBreakdown(s, ref.ID, `{}`); rec.Code != http.StatusConflict {
		t.Fatalf("while one runs: %d %s", rec.Code, rec.Body)
	}
	close(block)
	s.assistant.WaitReferences()
	// a reference that is not ready
	st := s.assistant.RefStore()
	failing, err := st.Create("x", "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	failing.State = motionref.StateFailed
	if err := st.Save(failing); err != nil {
		t.Fatal(err)
	}
	if rec := postBreakdown(s, failing.ID, ``); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "failed") {
		t.Fatalf("a reference that is not ready: %d %s", rec.Code, rec.Body)
	}
	// one that has no address (it was uploaded) cannot be fetched again
	up, err := st.Create("up", "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	up.State, up.Analysis = motionref.StateReady, &motionref.Analysis{Duration: 5}
	if err := st.Save(up); err != nil {
		t.Fatal(err)
	}
	if rec := postBreakdown(s, up.ID, ``); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "not kept") {
		t.Fatalf("an uploaded reference: %d %s", rec.Code, rec.Body)
	}
	if _, got := getReference(t, s, ref.ID); got.Analysis.BreakdownState == motionref.BreakdownPending {
		t.Fatalf("the breakdown ended: %+v", got.Analysis)
	}
}

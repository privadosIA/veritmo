package music

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"veritmo/internal/music/musicmotion"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/platform/events"
	"veritmo/internal/server/workspace"
	"veritmo/internal/video/motion"
)

const designAnswer = `{"dark":"#0b0f14","light":"#e8f1ec","accent":"#ff2e54","display":"heavy","body":"grotesk","default_mode":"dark","concept":"a stage light that follows the voice"}`

// recordingWriter answers the design request with a design and anything else with `other`, and keeps what it is told.
type recordingWriter struct {
	mu      sync.Mutex
	other   string
	systems []string
	users   []string
}

func (w *recordingWriter) Write(_ context.Context, system, user string, _ int) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.systems, w.users = append(w.systems, system), append(w.users, user)
	if strings.Contains(system, "You design the look") {
		return designAnswer, nil
	}
	return w.other, nil
}

func (w *recordingWriter) told(sub string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for i := range w.systems {
		if strings.Contains(w.systems[i], sub) || strings.Contains(w.users[i], sub) {
			return true
		}
	}
	return false
}

func withWriter(s *testServer, w motion.Writer, model string) {
	s.motionWriter = func(context.Context) (motion.Writer, motion.ModelInfo, error) {
		return w, motion.ModelInfo{Model: model}, nil
	}
}

func propose(t *testing.T, s *testServer, id string, body map[string]any) (int, ProposeAnswer, string) {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces/"+id+"/propose", body)
	var a ProposeAnswer
	_ = json.Unmarshal(rec.Body.Bytes(), &a)
	return rec.Code, a, rec.Body.String()
}

func pieceOf(t *testing.T, s *testServer, id string) PieceView {
	t.Helper()
	var v PieceView
	if err := json.Unmarshal([]byte(get(t, s, "/api/music-motion/pieces/"+id)), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func waitProposal(t *testing.T, s *testServer, id string, states ...string) Proposal {
	t.Helper()
	for i := 0; i < 400; i++ {
		if p := pieceOf(t, s, id).Proposal; p != nil {
			for _, st := range states {
				if p.State == st {
					return *p
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("the proposal never reached %v: %+v", states, pieceOf(t, s, id).Proposal)
	return Proposal{}
}

func TestPropose_ThePromptIsKeptWithThePieceAndReachesTheAssistant(t *testing.T) {
	s := mmServer(t)
	w := &recordingWriter{other: "nothing useful"}
	withWriter(s, w, "")
	v := newPiece(t, s, map[string]any{"prompt": "  calm nature documentary  "})
	if v.Prompt != "calm nature documentary" {
		t.Fatalf("%q", v.Prompt)
	}
	if code, a, body := propose(t, s, v.ID, map[string]any{"what": "design"}); code != 200 || a.Source != "assistant" || !w.told("calm nature documentary") {
		t.Fatalf("%d %s", code, body)
	}
	// A prompt of the proposal replaces the kept one.
	if code, _, body := propose(t, s, v.ID, map[string]any{"what": "design", "prompt": "dark horror"}); code != 200 || !w.told("dark horror") {
		t.Fatalf("%d %s", code, body)
	}
	if got := pieceOf(t, s, v.ID).Prompt; got != "dark horror" {
		t.Fatalf("%q", got)
	}
	// Too long, in both places.
	long := strings.Repeat("a", 2001)
	if code, _, _ := propose(t, s, v.ID, map[string]any{"what": "design", "prompt": long}); code != http.StatusUnprocessableEntity {
		t.Fatalf("propose: %d", code)
	}
	body := map[string]any{"project": "songs", "track_id": "night_drive", "client": "video-studio", "prompt": long}
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces", body); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("create: %d", rec.Code)
	}
}

func TestPropose_AFallbackSaysWhyAndAnAssistantAnswerSaysItsModel(t *testing.T) {
	s := mmServer(t) // no model in the tests
	v := newPiece(t, s, nil)
	code, a, body := propose(t, s, v.ID, map[string]any{"what": "script", "prompt": "calm"})
	if code != 200 || a.Source != "fallback" || !strings.Contains(a.FallbackReason, "not available") || !strings.Contains(a.FallbackReason, "does not read the prompt") {
		t.Fatalf("%d %s", code, body)
	}
	w := &recordingWriter{other: "nothing useful"}
	withWriter(s, w, "gemma4:26b")
	code, a, body = propose(t, s, v.ID, map[string]any{"what": "design"})
	if code != 200 || a.Source != "assistant" || a.FallbackReason != "" || a.Model != "gemma4:26b" {
		t.Fatalf("%d %s", code, body)
	}
	code, a, body = propose(t, s, v.ID, map[string]any{"what": "script"})
	if code != 200 || a.Source != "fallback" || !strings.Contains(a.FallbackReason, "refused") || a.Model != "gemma4:26b" {
		t.Fatalf("%d %s", code, body)
	}
}

func TestPropose_AModelThatIsNotInstalledIsRefusedBeforeAnythingRuns(t *testing.T) {
	s := mmServer(t)
	w := &recordingWriter{}
	withWriter(s, w, "")
	s.Assistant.AssistModels = func(context.Context) (string, []assist.ModelInfo, error) {
		return "qwen3:8b", []assist.ModelInfo{{Name: "qwen3:8b"}, {Name: "gemma4:26b"}}, nil
	}
	v := newPiece(t, s, nil)
	code, _, body := propose(t, s, v.ID, map[string]any{"what": "design", "model": "nope:1b"})
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "nope:1b") || !strings.Contains(body, "gemma4:26b") || len(w.systems) != 0 {
		t.Fatalf("%d %s (%d calls)", code, body, len(w.systems))
	}
	if code, a, body := propose(t, s, v.ID, map[string]any{"what": "design", "model": "gemma4:26b"}); code != 200 || a.Model != "gemma4:26b" {
		t.Fatalf("%d %s", code, body)
	}
}

func TestPropose_AllWritesTheDesignAndTheScriptInTheBackground(t *testing.T) {
	s := mmServer(t)
	w := &recordingWriter{other: "nothing useful"}
	withWriter(s, w, "gemma4:26b")
	v := newPiece(t, s, nil)
	code, _, body := propose(t, s, v.ID, map[string]any{"what": "all", "prompt": "dark horror"})
	if code != http.StatusAccepted {
		t.Fatalf("%d %s", code, body)
	}
	p := waitProposal(t, s, v.ID, ProposalDone, ProposalFailed)
	if p.State != ProposalDone || p.What != "all" || p.Model != "gemma4:26b" || p.DesignSource != "assistant" || p.DesignFallbackReason != "" ||
		p.ScriptSource != "fallback" || !strings.Contains(p.ScriptFallbackReason, "refused") || p.FinishedAt == nil {
		t.Fatalf("%+v", p)
	}
	got := pieceOf(t, s, v.ID)
	if got.Design == nil || got.Design.Design.Concept != "a stage light that follows the voice" || got.Script == nil || got.ScriptSource != "fallback" {
		t.Fatalf("%+v", got)
	}
	// The script was asked with the design just written and with the prompt.
	if !w.told("dark horror") || !w.told("a stage light that follows the voice") {
		t.Fatal("the script request lacks the prompt or the design")
	}
	// An async design alone is accepted too; a synchronous one stays as it was.
	if code, _, body := propose(t, s, v.ID, map[string]any{"what": "design", "async": true}); code != http.StatusAccepted {
		t.Fatalf("%d %s", code, body)
	}
	waitProposal(t, s, v.ID, ProposalDone)
}

func TestPropose_WaitsForARunToEndAndRefusesEditsMeanwhile(t *testing.T) {
	s := mmServer(t)
	w := &recordingWriter{other: "nothing useful"}
	withWriter(s, w, "")
	s.proposalPoll = 10 * time.Millisecond
	release := make(chan struct{})
	s.Workspace.Execute = func(ctx context.Context, rn workspace.ExecRun, em *events.Emitter) (string, error) {
		em.PipelineStart()
		select {
		case <-release:
		case <-ctx.Done():
		}
		out := "out/" + rn.Rec.Project + ".mp4"
		_ = os.MkdirAll(rn.Dir+"/out", 0o755)
		_ = os.WriteFile(rn.Dir+"/"+out, []byte("video"), 0o644)
		em.Done(out)
		return out, nil
	}
	busy := newPiece(t, s, nil)
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces/"+busy.ID+"/render", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	v := newPiece(t, s, nil)
	if code, _, body := propose(t, s, v.ID, map[string]any{"what": "all"}); code != http.StatusAccepted {
		t.Fatalf("%d %s", code, body)
	}
	waitProposal(t, s, v.ID, ProposalWaiting)
	if len(w.systems) != 0 {
		t.Fatal("the assistant was asked while a run held the memory")
	}
	if code, _, _ := propose(t, s, v.ID, map[string]any{"what": "design"}); code != http.StatusConflict {
		t.Fatalf("a second proposal: %d", code)
	}
	if rec := doJSON(t, s.Handler(), http.MethodPut, "/api/music-motion/pieces/"+v.ID+"/design", map[string]any{}); rec.Code != http.StatusConflict {
		t.Fatalf("an edit: %d", rec.Code)
	}
	close(release)
	if p := waitProposal(t, s, v.ID, ProposalDone, ProposalFailed); p.State != ProposalDone || p.DesignSource != "assistant" {
		t.Fatalf("%+v", p)
	}
}

func TestPropose_AWaitThatTimesOutFallsBackWithItsReason(t *testing.T) {
	s := mmServer(t)
	withWriter(s, &recordingWriter{}, "")
	s.proposalPoll, s.proposalMaxWait = 5*time.Millisecond, 30*time.Millisecond
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	s.Workspace.Execute = func(ctx context.Context, rn workspace.ExecRun, em *events.Emitter) (string, error) {
		em.PipelineStart()
		select {
		case <-release:
		case <-ctx.Done():
		}
		return "", errors.New("stopped")
	}
	busy := newPiece(t, s, nil)
	doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces/"+busy.ID+"/render", nil)
	v := newPiece(t, s, nil)
	propose(t, s, v.ID, map[string]any{"what": "design", "async": true})
	p := waitProposal(t, s, v.ID, ProposalDone, ProposalFailed)
	if p.State != ProposalDone || p.DesignSource != "fallback" || !strings.Contains(p.DesignFallbackReason, "waited") {
		t.Fatalf("%+v", p)
	}
}

func TestPropose_AProposalThatWasRunningWhenTheServerStoppedReadsAsFailed(t *testing.T) {
	s := mmServer(t)
	v := newPiece(t, s, nil)
	raw, _ := json.Marshal(Proposal{State: ProposalWriting, What: "all", StartedAt: time.Now().UTC()})
	if err := s.place(v.ID).WriteDoc(musicmotion.DocProposal, raw); err != nil {
		t.Fatal(err)
	}
	p := pieceOf(t, s, v.ID).Proposal
	if p == nil || p.State != ProposalFailed || !strings.Contains(p.Error, "restart") {
		t.Fatalf("%+v", p)
	}
}

func TestPropose_TheStorylineOfThePlanIsKeptWithThePieceAndReachesTheProposals(t *testing.T) {
	s := mmServer(t)
	w := &recordingWriter{other: "nothing useful"}
	withWriter(s, w, "")
	v := newPiece(t, s, map[string]any{"world": "  a night road,  headlights and dust ", "device": "one object across the song", "hook": "a pair of headlights cut the dark"})
	if v.World != "a night road, headlights and dust" || v.Device != "one object across the song" || v.Hook != "a pair of headlights cut the dark" {
		t.Fatalf("the piece keeps the three: %+v", v)
	}
	if got := pieceOf(t, s, v.ID); got.World != v.World || got.Device != v.Device || got.Hook != v.Hook {
		t.Fatalf("and gives them back: %+v", got)
	}
	if code, _, body := propose(t, s, v.ID, map[string]any{"what": "design"}); code != 200 || !w.told("a night road, headlights and dust") || !w.told("one object across the song") || w.told("a pair of headlights") {
		t.Fatalf("the design proposal reads the world and the device, not the hook: %d %s", code, body)
	}
	if code, _, body := propose(t, s, v.ID, map[string]any{"what": "script"}); code != 200 || !w.told("The first two seconds show: a pair of headlights cut the dark") || !w.told("The device that carries the video: one object across the song") {
		t.Fatalf("the script proposal reads the device and the hook: %d %s", code, body)
	}
	body := map[string]any{"project": "songs", "track_id": "night_drive", "client": "video-studio", "device": "a heist movie"}
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces", body); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("an unknown device is refused: %d %s", rec.Code, rec.Body)
	}
}

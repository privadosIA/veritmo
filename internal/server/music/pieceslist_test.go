package music

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func pieceList(t *testing.T, s *testServer, query string) (int, []PieceView, http.Header) {
	t.Helper()
	rec := call(s.Handler(), http.MethodGet, "/api/music-motion/pieces"+query, "")
	var out []PieceView
	if rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%v %s", err, rec.Body)
		}
	}
	return rec.Code, out, rec.Header()
}

func titles(vs []PieceView) string {
	var out []string
	for _, v := range vs {
		out = append(out, v.Title)
	}
	return strings.Join(out, ",")
}

func TestPieces_TheListIsPagedSortedSearchedAndFilteredByClient(t *testing.T) {
	s := mmServer(t)
	mk := func(title, client string) {
		v := newPiece(t, s, map[string]any{"client": client})
		if rec := doJSON(t, s.Handler(), http.MethodPatch, "/api/music-motion/pieces/"+v.ID, map[string]any{"title": title}); rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		time.Sleep(1100 * time.Millisecond) // created_at goes by the second
	}
	mk("Alpha night", "lab")
	mk("Beta day", "video-studio")
	mk("Gamma night", "lab")
	code, all, hdr := pieceList(t, s, "")
	if code != 200 || titles(all) != "Gamma night,Beta day,Alpha night" || hdr.Get("X-Total-Count") != "" {
		t.Fatalf("without parameters: %d %s %v", code, titles(all), hdr)
	}
	_, page, hdr := pieceList(t, s, "?limit=2&offset=1")
	if titles(page) != "Beta day,Alpha night" || hdr.Get("X-Total-Count") != "3" || hdr.Get("Link") == "" {
		t.Fatalf("a page: %s %v", titles(page), hdr)
	}
	if _, byTitle, _ := pieceList(t, s, "?sort=title"); titles(byTitle) != "Alpha night,Beta day,Gamma night" {
		t.Fatalf("sort by title: %s", titles(byTitle))
	}
	if _, oldest, _ := pieceList(t, s, "?sort=created_at&limit=1"); titles(oldest) != "Alpha night" {
		t.Fatalf("oldest first: %s", titles(oldest))
	}
	if _, found, hdr := pieceList(t, s, "?q=NIGHT"); titles(found) != "Gamma night,Alpha night" || hdr.Get("X-Total-Count") != "2" {
		t.Fatalf("q: %s %v", titles(found), hdr)
	}
	if _, mine, _ := pieceList(t, s, "?client=video-studio"); titles(mine) != "Beta day" {
		t.Fatalf("client: %s", titles(mine))
	}
	if _, both, hdr := pieceList(t, s, "?client=lab&q=night&sort=title&limit=1"); titles(both) != "Alpha night" || hdr.Get("X-Total-Count") != "2" {
		t.Fatalf("everything together: %s %v", titles(both), hdr)
	}
	for _, bad := range []string{"?sort=password", "?limit=0", "?limit=501", "?offset=-1"} {
		if code, _, _ := pieceList(t, s, bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, code)
		}
	}
}

func TestPieces_PatchChangesTheDetailsSentAndNothingElse(t *testing.T) {
	s := mmServer(t)
	v := newPiece(t, s, nil)
	patch := func(id string, body map[string]any) (int, PieceView, string) {
		rec := doJSON(t, s.Handler(), http.MethodPatch, "/api/music-motion/pieces/"+id, body)
		var got PieceView
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		return rec.Code, got, rec.Body.String()
	}
	code, got, body := patch(v.ID, map[string]any{"title": "Neon Pulse v2"})
	if code != 200 || got.Title != "Neon Pulse v2" || got.Client != v.Client || got.Level != v.Level || got.FPS != v.FPS {
		t.Fatalf("%d %s", code, body)
	}
	code, got, body = patch(v.ID, map[string]any{"client": "lab", "prompt": "make it blue", "world": "a city at night", "hook": "we are alive"})
	if code != 200 || got.Client != "lab" || got.Prompt != "make it blue" || got.World != "a city at night" || got.Hook != "we are alive" || got.Title != "Neon Pulse v2" {
		t.Fatalf("%d %s", code, body)
	}
	if code, _, body := patch(v.ID, map[string]any{"fps": 60}); code != http.StatusBadRequest || !strings.Contains(body, "fps") || !strings.Contains(body, "title") {
		t.Fatalf("a field that cannot be changed this way is named: %d %s", code, body)
	}
	if code, got, _ := patch(v.ID, map[string]any{"title": ""}); code != http.StatusUnprocessableEntity || got.Title != "" {
		t.Fatalf("an empty title: %d", code)
	}
	if code, _, _ := patch(v.ID, map[string]any{"device": "teleport"}); code != http.StatusUnprocessableEntity {
		t.Fatalf("a device that is not one: %d", code)
	}
	if code, _, _ := patch(v.ID, map[string]any{"prompt": strings.Repeat("x", 2001)}); code != http.StatusUnprocessableEntity {
		t.Fatalf("a prompt that is too long: %d", code)
	}
	if code, _, _ := patch(v.ID, map[string]any{"title": 5}); code != http.StatusUnprocessableEntity {
		t.Fatalf("a title that is not text: %d", code)
	}
	if code, _, _ := patch("api-mm-nope", map[string]any{"title": "x"}); code != http.StatusNotFound {
		t.Fatalf("%d", code)
	}
	if again := pieceOf(t, s, v.ID); again.Title != "Neon Pulse v2" || again.Client != "lab" {
		t.Fatalf("a refused patch changes nothing: %+v", again)
	}
	// while a proposal of the piece is being written an edit is refused
	if !s.proposals.start(v.ID, context.CancelFunc(func() {})) {
		t.Fatal("start")
	}
	defer s.proposals.end(v.ID)
	if code, _, _ := patch(v.ID, map[string]any{"title": "during"}); code != http.StatusConflict {
		t.Fatalf("during a proposal: %d", code)
	}
}

func TestPieces_AProposalAndABatchThatWereRunningWhenTheServerStoppedAreMarkedWhenItStarts(t *testing.T) {
	s := mmServer(t)
	v := newPiece(t, s, nil)
	now := time.Now().UTC()
	prop, _ := json.Marshal(Proposal{State: ProposalWriting, What: "all", StartedAt: now})
	batch, _ := json.Marshal(SceneBatch{State: SceneBatchWriting, StartedAt: now, Total: 3})
	done, _ := json.Marshal(Proposal{State: ProposalDone, What: "design", StartedAt: now})
	if err := s.place(v.ID).WriteDoc("proposal", prop); err != nil {
		t.Fatal(err)
	}
	if err := s.place(v.ID).WriteDoc("scenes", batch); err != nil {
		t.Fatal(err)
	}
	other := newPiece(t, s, nil)
	if err := s.place(other.ID).WriteDoc("proposal", done); err != nil {
		t.Fatal(err)
	}
	// a new process: its handler has no job running, so what the documents say is running is not
	_ = New(testHost{s})
	read := func(id, kind string) string {
		body, _ := s.pieces().doc(id, kind)
		return string(body)
	}
	if got := read(v.ID, "proposal"); !strings.Contains(got, `"state": "failed"`) || !strings.Contains(got, "restart") {
		t.Fatalf("the proposal is marked in the document, with no request in between: %s", got)
	}
	if got := read(v.ID, "scenes"); !strings.Contains(got, `"state": "failed"`) || !strings.Contains(got, "restart") {
		t.Fatalf("the batch: %s", got)
	}
	if got := read(other.ID, "proposal"); !strings.Contains(got, `"state":"done"`) {
		t.Fatalf("a proposal that ended is left as it is: %s", got)
	}
}

// The label of an animation lives in its own row and in its project (which the shared listings read, from its first render): changing it changes both,
// so `managed=all&client=` finds it under the new label, and `GET /api/clients` counts it there.
func TestPieces_ItsNewLabelFollowsToItsProjectInTheSharedListings(t *testing.T) {
	s := mmServer(t)
	v := newPiece(t, s, map[string]any{"client": "first-dash"})
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces/"+v.ID+"/render", nil)
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
	listed := func(client string) bool {
		rr := doJSON(t, s.Handler(), http.MethodGet, "/api/projects?managed=all&client="+client, nil)
		return strings.Contains(rr.Body.String(), v.ID) && strings.Contains(rr.Body.String(), `"kind":"music_motion"`)
	}
	if !listed("first-dash") || listed("second-dash") {
		t.Fatalf("before: %s", doJSON(t, s.Handler(), http.MethodGet, "/api/projects?managed=all", nil).Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodPatch, "/api/music-motion/pieces/"+v.ID, map[string]any{"client": "second-dash"}); rec.Code != http.StatusOK {
		t.Fatalf("relabel: %d %s", rec.Code, rec.Body)
	}
	if listed("first-dash") || !listed("second-dash") {
		t.Errorf("the project still has the old label")
	}
	if rr := doJSON(t, s.Handler(), http.MethodGet, "/api/clients", nil); !strings.Contains(rr.Body.String(), `"name":"second-dash"`) || strings.Contains(rr.Body.String(), `"name":"first-dash"`) {
		t.Errorf("the labels in use: %s", rr.Body)
	}
	// an include does not list it, and an ordinary change of the title leaves the label alone
	if rr := doJSON(t, s.Handler(), http.MethodGet, "/api/projects?managed=include&client=second-dash", nil); strings.Contains(rr.Body.String(), v.ID) {
		t.Errorf("managed=include lists an animation")
	}
	if rec := doJSON(t, s.Handler(), http.MethodPatch, "/api/music-motion/pieces/"+v.ID, map[string]any{"title": "Other title"}); rec.Code != http.StatusOK || !listed("second-dash") {
		t.Errorf("a title change: %d", rec.Code)
	}
}

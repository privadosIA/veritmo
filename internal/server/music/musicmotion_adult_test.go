package music

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/music/musicmotion"
)

func piecesBody(extra map[string]any) map[string]any {
	body := map[string]any{"project": "songs", "track_id": "night_drive", "client": "video-studio"}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

func TestPieceAdult_ASeverThatDoesNotKeepAdultContentRefusesToMakeOne(t *testing.T) {
	s := mmServer(t)
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces", piecesBody(map[string]any{"adult": true}))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "content_not_allowed") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if list := doJSON(t, s.Handler(), http.MethodGet, "/api/music-motion/pieces", nil); strings.Contains(list.Body.String(), `"id"`) {
		t.Fatalf("nothing was made: %s", list.Body)
	}
	// not for adults, said or not: made, and says so
	for _, extra := range []map[string]any{nil, {"adult": false}} {
		v := newPiece(t, s, extra)
		if v.Adult {
			t.Errorf("%v: the piece is not for adults", extra)
		}
	}
}

func TestPieceAdult_APieceForAdultsDoesNotExistForAServerThatDoesNotShowThem(t *testing.T) {
	s := mmServer(t)
	s.showsAdult = true
	adult := newPiece(t, s, map[string]any{"adult": true})
	plain := newPiece(t, s, nil)
	if !adult.Adult {
		t.Fatal("the view says so")
	}
	base := "/api/music-motion/pieces/" + adult.ID
	// a server that shows them: everything as usual
	for _, path := range []string{"", "/backgrounds", "/models"} {
		if rec := doJSON(t, s.Handler(), http.MethodGet, base+path, nil); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
	count := func(url string) int {
		rec := doJSON(t, s.Handler(), http.MethodGet, url, nil)
		var v []PieceView
		_ = json.Unmarshal(rec.Body.Bytes(), &v)
		return len(v)
	}
	if count("/api/music-motion/pieces") != 2 || count("/api/music-motion/pieces?limit=10") != 2 {
		t.Fatal("both are listed")
	}
	// the same data on a server that does not show them: the 404 of an unknown piece, and out of the lists and their totals
	s.showsAdult = false
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, base}, {http.MethodGet, base + "/backgrounds"}, {http.MethodGet, base + "/frame?t=1"}, {http.MethodPost, base + "/preview"},
		{http.MethodPut, base + "/script"}, {http.MethodPost, base + "/render"}, {http.MethodDelete, base},
	} {
		if rec := doJSON(t, s.Handler(), c.method, c.path, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: %d, want 404", c.method, c.path, rec.Code)
		}
	}
	if count("/api/music-motion/pieces") != 1 {
		t.Fatalf("the list leaves it out")
	}
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/music-motion/pieces?limit=10", nil)
	if rec.Header().Get("X-Total-Count") != "1" {
		t.Errorf("the total counts without it: %q", rec.Header().Get("X-Total-Count"))
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/music-motion/pieces/"+plain.ID, nil); rec.Code != http.StatusOK {
		t.Errorf("the other piece is still there: %d", rec.Code)
	}
	// back to a server that shows them: it was never touched
	s.showsAdult = true
	if rec := doJSON(t, s.Handler(), http.MethodGet, base, nil); rec.Code != http.StatusOK {
		t.Errorf("%d", rec.Code)
	}
}

func TestPieceAdult_AnAdultMusicProjectMakesAnAdultPiece(t *testing.T) {
	s := mmServer(t)
	s.showsAdult = true
	yaml := strings.Replace(songsYAML, "output: {mode: music}", "adult: true\noutput: {mode: music}", 1)
	if err := s.Workspace.SaveProjectDefinition("songs", []byte(yaml), "save"); err != nil {
		t.Fatal(err)
	}
	v := newPiece(t, s, nil)
	if !v.Adult {
		t.Fatal("the song is for adults, so the piece is")
	}
	// and a server that does not keep them refuses it, before anything is made
	s.showsAdult = false
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces", piecesBody(nil)); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestPieceAdult_APieceThatKeepsAnAdultVideoIsOne(t *testing.T) {
	fakeProbe(t)
	s := mmServer(t)
	s.showsAdult = true
	clip := filepath.Join(t.TempDir(), "g.mp4")
	os.WriteFile(clip, fakeVideo(4), 0o644)
	s.mHandler.generationVideoSeam = func(id string) (string, bool, error) { return clip, id == "api-gen-adult", nil }
	post := func(pieceID, gen, name string) int {
		return doJSON(t, s.Handler(), http.MethodPost, backgroundsURL(pieceID), map[string]any{"from": "generation:" + gen, "name": name}).Code
	}
	// a video that is not for adults changes nothing
	a := newPiece(t, s, nil)
	if post(a.ID, "api-gen-fine", "fine.mp4") != http.StatusCreated || pieceOf(t, s, a.ID).Adult {
		t.Fatal("a video that is not for adults does not make the piece one")
	}
	// one that is does, and stays
	if post(a.ID, "api-gen-adult", "adult.mp4") != http.StatusCreated || !pieceOf(t, s, a.ID).Adult {
		t.Fatal("the piece keeps a video for adults: it is one")
	}
	if rec := doJSON(t, s.Handler(), http.MethodDelete, backgroundsURL(a.ID)+"/adult.mp4", nil); rec.Code != http.StatusNoContent || !pieceOf(t, s, a.ID).Adult {
		t.Fatal("taking the video away does not take the label away: a person decides that")
	}
	// an upload says it with ?adult=true
	b := newPiece(t, s, nil)
	if rec := putRaw(t, s, backgroundsURL(b.ID)+"/x.mp4?adult=true", fakeVideo(3)); rec.Code != http.StatusCreated || !pieceOf(t, s, b.ID).Adult {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	// a server that does not keep them refuses it and keeps nothing
	s.showsAdult = false
	c := newPiece(t, s, nil)
	if rec := putRaw(t, s, backgroundsURL(c.ID)+"/y.mp4?adult=true", fakeVideo(3)); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "content_not_allowed") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(listBackgrounds(t, s, c.ID).Backgrounds) != 0 || pieceOf(t, s, c.ID).Adult {
		t.Fatal("nothing was kept")
	}
}

func TestPieceAdult_TheProjectOfAPieceForAdultsCarriesTheLabel(t *testing.T) {
	s := mmServer(t)
	s.showsAdult = true
	adult := newPiece(t, s, map[string]any{"adult": true})
	plain := newPiece(t, s, nil)
	for id, want := range map[string]bool{adult.ID: true, plain.ID: false} {
		if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces/"+id+"/render", nil); rec.Code != http.StatusAccepted {
			t.Fatalf("render: %d %s", rec.Code, rec.Body)
		}
		raw, err := os.ReadFile(filepath.Join(s.Workspace.ProjectDir(id), "scenes.yaml"))
		if err != nil || strings.Contains(string(raw), "adult: true") != want {
			t.Fatalf("%s: adult: true expected %v in\n%s", id, want, raw)
		}
		if label, exists := s.Workspace.ProjectAdult(id); want && (!exists || label == nil || !*label) {
			t.Errorf("%s: the project row says %v %v", id, exists, label)
		}
	}
}

func TestPieceAdult_APieceMadeBeforeTheLabelIsNotForAdults(t *testing.T) {
	// the field is absent in a stored piece: it reads as false and is not hidden
	var info pieceInfo
	if err := json.Unmarshal([]byte(`{"id":"api-mm-x","style":"typographic"}`), &info); err != nil || info.Adult {
		t.Fatalf("%v %+v", err, info)
	}
	raw, _ := json.Marshal(musicmotion.ProjectSpec{}.Adult)
	if string(raw) != "false" {
		t.Fatal(string(raw))
	}
}

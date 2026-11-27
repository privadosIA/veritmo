package music

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/music/musicmotion"
	"veritmo/internal/music/songmap"
	"veritmo/internal/platform/events"
	"veritmo/internal/server/workspace"
)

// withSeparator installs the fake demucs and its weights on the server of the test; its vocals are amp loud.
func withSeparator(t *testing.T, s *testServer, hr *hearRunner, amp float64) {
	t.Helper()
	dir := t.TempDir()
	s.demucsDir = dir
	if err := os.WriteFile(filepath.Join(dir, "ggml-model-htdemucs-4s-f16.bin"), []byte("weights"), 0o644); err != nil {
		t.Fatal(err)
	}
	hr.voiceAmp = amp
	look := s.Look
	s.Look = func(name string) (string, error) {
		if name == "demucs" {
			return "/fake/demucs", nil
		}
		return look(name)
	}
}

// voicedJSON is heardJSON with every word where the fake voice sings (0-2, 4-6, 8-10 and 12-14 s), so the stem recognises all
// the written words and the mix is never read in addition (see voiceLossRatio).
var voicedJSON = heardAt(map[string]float64{"one": 1, "two": 5, "three": 9, "four": 12}, "one", "two", "three", "four")

func transcribeWith(t *testing.T, s *testServer, id string, body any) (int, PieceView, string) {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces/"+id+"/transcribe", body)
	var v PieceView
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	return rec.Code, v, rec.Body.String()
}

func TestSeparate_InstalledAndAskedTheWordsAreHeardOnTheVoice(t *testing.T) {
	s := mmServer(t)
	hr := withWhisper(t, s, voicedJSON)
	withSeparator(t, s, hr, 0.5)
	v := newPiece(t, s, map[string]any{"transcribe": true, "separate": true})
	if hr.separations != 1 || v.SongMap.WordsSource != songmap.SourceHeardVoice || v.SongMap.WordsEstimated {
		t.Fatalf("separations=%d source=%q", hr.separations, v.SongMap.WordsSource)
	}
	if filepath.Base(hr.whisperWav) != "vocals-16k.wav" {
		t.Fatalf("whisper read %q, want the kept stem", hr.whisperWav)
	}
	stem := filepath.Join(s.pieceDir(v.ID), "vocals-16k.wav")
	if fi, err := os.Stat(stem); err != nil || fi.Size() == 0 {
		t.Fatalf("the stem is kept with the piece: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(s.pieceDir(v.ID), "*.wav")); len(left) != 1 {
		t.Fatalf("only the 16 kHz voice stays: %v", left)
	}
	if !v.SongMap.Words[0].Heard || v.SongMap.Words[0].Confidence != 0.9 {
		t.Fatalf("%+v", v.SongMap.Words[0])
	}
	// the attacks of the voice are found and a heard word that lies on one is moved onto it (the voice enters at 0, 4, 8, 12 s)
	snapped := 0
	for _, w := range v.SongMap.Words {
		if w.Snapped == songmap.SnappedAttack {
			snapped++
		}
	}
	if v.SongMap.Attacks < 3 || snapped < 1 {
		t.Fatalf("attacks=%d snapped on an attack=%d: %+v", v.SongMap.Attacks, snapped, v.SongMap.Words)
	}
}

func TestSeparate_AutoSeparatesWhenTheSeparatorIsInstalled(t *testing.T) {
	for _, body := range []map[string]any{{"transcribe": true}, {"transcribe": true, "separate": "auto"}} {
		s := mmServer(t)
		hr := withWhisper(t, s, heardJSON)
		withSeparator(t, s, hr, 0.5)
		v := newPiece(t, s, body)
		if hr.separations != 1 || v.SongMap.WordsSource != songmap.SourceHeardVoice || v.SongMap.Attacks < 3 {
			t.Fatalf("%v: separations=%d source=%q attacks=%d", body, hr.separations, v.SongMap.WordsSource, v.SongMap.Attacks)
		}
	}
}

func TestSeparate_AutoWithoutTheSeparatorHearsTheMixAndSaysSo(t *testing.T) {
	for _, body := range []map[string]any{{"transcribe": true}, {"transcribe": true, "separate": "auto"}} {
		s := mmServer(t)
		hr := withWhisper(t, s, heardJSON)
		v := newPiece(t, s, body)
		if hr.separations != 0 || v.SongMap.WordsSource != songmap.SourceHeard || v.SongMap.Attacks != 0 || !strings.Contains(v.WordsNote, "get-demucs.sh") || !strings.Contains(v.WordsNote, "heard in the mix") {
			t.Fatalf("%v: source=%q attacks=%d note=%q", body, v.SongMap.WordsSource, v.SongMap.Attacks, v.WordsNote)
		}
		if filepath.Base(hr.whisperWav) != "audio.wav" {
			t.Fatalf("whisper read %q", hr.whisperWav)
		}
	}
}

func TestSeparate_NotInstalledAndAskedSaysHowToInstallIt(t *testing.T) {
	s := mmServer(t)
	withWhisper(t, s, heardJSON)
	v := newPiece(t, s, nil)
	code, _, body := transcribeWith(t, s, v.ID, map[string]any{"separate": true})
	if code != http.StatusServiceUnavailable || !strings.Contains(body, "get-demucs.sh") {
		t.Fatalf("%d %s", code, body)
	}
}

func TestSeparate_NotInstalledRequiredIs503AndNothingIsMade(t *testing.T) {
	s := mmServer(t)
	withWhisper(t, s, heardJSON)
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces", map[string]any{"project": "songs", "track_id": "night_drive", "client": "video-studio", "transcribe": true, "separate": true})
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "get-demucs.sh") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	list := doJSON(t, s.Handler(), http.MethodGet, "/api/music-motion/pieces", nil)
	if strings.TrimSpace(list.Body.String()) != "[]" {
		t.Fatalf("no piece must be left: %s", list.Body)
	}
	// the same on the route of a piece, which is left as it was
	v := newPiece(t, s, nil)
	code, _, body := transcribeWith(t, s, v.ID, map[string]any{"separate": true})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("%d %s", code, body)
	}
}

func TestSeparate_HeardAgainUsesTheKeptStem(t *testing.T) {
	s := mmServer(t)
	hr := withWhisper(t, s, voicedJSON)
	withSeparator(t, s, hr, 0.5)
	v := newPiece(t, s, map[string]any{"transcribe": true, "separate": true})
	code, again, body := transcribeWith(t, s, v.ID, nil) // auto: the kept voice is used, nothing is separated again
	if code != http.StatusOK || hr.separations != 1 || hr.calls != 2 || again.SongMap.WordsSource != songmap.SourceHeardVoice {
		t.Fatalf("%d separations=%d whisper=%d source=%q %s", code, hr.separations, hr.calls, again.SongMap.WordsSource, body)
	}
}

func TestSeparate_AStemWithNoVoiceIsTreatedAsNoStem(t *testing.T) {
	s := mmServer(t)
	hr := withWhisper(t, s, heardJSON)
	withSeparator(t, s, hr, 0) // silence
	v := newPiece(t, s, map[string]any{"transcribe": true, "separate": true})
	if hr.separations != 1 || v.SongMap.WordsSource != songmap.SourceHeard || !strings.Contains(v.WordsNote, "heard in the mix") {
		t.Fatalf("separations=%d source=%q note=%q", hr.separations, v.SongMap.WordsSource, v.WordsNote)
	}
	if filepath.Base(hr.whisperWav) != "audio.wav" {
		t.Fatalf("the mix is read: %q", hr.whisperWav)
	}
	if _, err := os.Stat(filepath.Join(s.pieceDir(v.ID), "vocals-16k.wav")); err == nil {
		t.Fatal("a stem with no voice is not kept")
	}
}

func TestSeparate_FalseNeverSeparatesAndAnUnknownValueIs422(t *testing.T) {
	s := mmServer(t)
	hr := withWhisper(t, s, heardJSON)
	withSeparator(t, s, hr, 0.5)
	v := newPiece(t, s, map[string]any{"transcribe": true, "separate": false})
	if hr.separations != 0 || v.SongMap.WordsSource != songmap.SourceHeard || strings.Contains(v.WordsNote, "separated") {
		t.Fatalf("separations=%d source=%q note=%q", hr.separations, v.SongMap.WordsSource, v.WordsNote)
	}
	if code, _, body := transcribeWith(t, s, v.ID, map[string]any{"separate": "maybe"}); code != http.StatusUnprocessableEntity {
		t.Fatalf("%d %s", code, body)
	}
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces", map[string]any{"project": "songs", "track_id": "night_drive", "client": "video-studio", "separate": "maybe"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestSeparate_NoRoomForTheSeparatorFallsBackToTheMixOrRefusesWhenRequired(t *testing.T) {
	s := mmServer(t)
	hr := withWhisper(t, s, heardJSON)
	withSeparator(t, s, hr, 0.5)
	s.memAvailable = func() (int64, error) { return 3 << 30, nil } // room for Whisper (1.3 x 5 bytes + 1 GiB), not for demucs
	v := newPiece(t, s, map[string]any{"transcribe": true})
	if hr.separations != 0 || v.SongMap.WordsSource != songmap.SourceHeard || !strings.Contains(v.WordsNote, "not enough memory") {
		t.Fatalf("auto hears the mix and says why: separations=%d note=%q", hr.separations, v.WordsNote)
	}
	if code, _, body := transcribeWith(t, s, v.ID, map[string]any{"separate": true}); code != http.StatusServiceUnavailable || !strings.Contains(body, "voice separator") {
		t.Fatalf("%d %s", code, body)
	}
}

// --- moving a word by hand -------------------------------------------------------------------------

func patchWords(t *testing.T, s *testServer, id string, moves []map[string]any) (int, PieceView, string) {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodPatch, "/api/music-motion/pieces/"+id+"/words", map[string]any{"words": moves})
	var v PieceView
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	return rec.Code, v, rec.Body.String()
}

func TestWords_OneWordMovedFollowsTheNewTimeAndTheScriptIsProposedAgain(t *testing.T) {
	s := mmServer(t)
	withWhisper(t, s, heardJSON)
	v := newPiece(t, s, map[string]any{"transcribe": true})
	w := v.SongMap.Words[2] // "three" at 10 s
	code, got, body := patchWords(t, s, v.ID, []map[string]any{{"index": 2, "start": 9.6}})
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	moved := got.SongMap.Words[2]
	if moved.Start != 9.6 || moved.Heard || moved.Confidence != 0 || (moved.End-moved.Start) < (w.End-w.Start)-0.002 || (moved.End-moved.Start) > (w.End-w.Start)+0.002 {
		t.Fatalf("the word moves keeping its length: %+v from %+v", moved, w)
	}
	if got.SongMap.WordsSource != songmap.SourceEdited || got.SongMap.WordsEstimated || got.ScriptSource != "fallback" || got.ScriptWarning != "" {
		t.Fatalf("source=%q script=%q warning=%q", got.SongMap.WordsSource, got.ScriptSource, got.ScriptWarning)
	}
	if !got.SongMap.Words[0].Heard || got.SongMap.Words[0].Start != 1 {
		t.Fatalf("the other words stay: %+v", got.SongMap.Words[0])
	}
	// the same body twice gives the same map
	_, again, _ := patchWords(t, s, v.ID, []map[string]any{{"index": 2, "start": 9.6}})
	a, _ := json.Marshal(got.SongMap)
	b, _ := json.Marshal(again.SongMap)
	if string(a) != string(b) {
		t.Fatalf("not idempotent:\n%s\n%s", a, b)
	}
	// and it is stored
	stored := get(t, s, "/api/music-motion/pieces/"+v.ID)
	var sv PieceView
	_ = json.Unmarshal([]byte(stored), &sv)
	if sv.SongMap.Words[2].Start != 9.6 || sv.SongMap.WordsSource != songmap.SourceEdited {
		t.Fatalf("stored: %+v", sv.SongMap.Words[2])
	}
}

func TestWords_AMoveThatCrossesANeighbourIs422AndNothingChanges(t *testing.T) {
	s := mmServer(t)
	withWhisper(t, s, heardJSON)
	v := newPiece(t, s, map[string]any{"transcribe": true})
	for name, moves := range map[string][]map[string]any{
		"before the word before it": {{"index": 2, "start": 2.0}},
		"after the word after it":   {{"index": 1, "start": 11.0}},
		"unknown index":             {{"index": 9, "start": 5.0}},
		"outside the song":          {{"index": 3, "start": 25.0}},
		"too short":                 {{"index": 3, "start": 12.0, "end": 12.01}},
		"twice":                     {{"index": 3, "start": 12.0}, {"index": 3, "start": 12.1}},
		"none":                      {},
	} {
		code, _, body := patchWords(t, s, v.ID, moves)
		if code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: %d %s", name, code, body)
		}
	}
	var after PieceView
	_ = json.Unmarshal([]byte(get(t, s, "/api/music-motion/pieces/"+v.ID)), &after)
	a, _ := json.Marshal(v.SongMap)
	b, _ := json.Marshal(after.SongMap)
	if string(a) != string(b) {
		t.Fatal("a refused move must leave the map as it was")
	}
	if code, _, _ := patchWords(t, s, "api-mm-nothing", []map[string]any{{"index": 0, "start": 1.0}}); code != http.StatusNotFound {
		t.Fatalf("%d", code)
	}
}

func TestWords_WhileDrawingIs409(t *testing.T) {
	s := mmServer(t)
	withWhisper(t, s, heardJSON)
	v := newPiece(t, s, map[string]any{"transcribe": true})
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	s.Workspace.Execute = func(ctx context.Context, rn workspace.ExecRun, em *events.Emitter) (string, error) {
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return "", errors.New("stopped")
	}
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces/"+v.ID+"/render", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	<-started
	code, _, body := patchWords(t, s, v.ID, []map[string]any{{"index": 0, "start": 1.1}})
	close(release)
	if code != http.StatusConflict {
		t.Fatalf("%d %s", code, body)
	}
}

func TestWords_AnEditedScriptIsKeptWithAWarning(t *testing.T) {
	s := mmServer(t)
	withWhisper(t, s, heardJSON)
	v := newPiece(t, s, map[string]any{"transcribe": true})
	script := *v.Script
	script.Pieces = []musicmotion.Piece{
		{ID: "a", Start: 0, End: 4.5, Section: "Verse", Part: "toggle"},
		{ID: "b", Start: 4.5, End: v.SongMap.Duration, Section: "Chorus", Part: "stamp"},
	}
	if rec := doJSON(t, s.Handler(), http.MethodPut, "/api/music-motion/pieces/"+v.ID+"/script", script); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	code, got, body := patchWords(t, s, v.ID, []map[string]any{{"index": 2, "start": 9.0}})
	if code != http.StatusOK || got.ScriptSource != "user" || len(got.Script.Pieces) != 2 || !strings.Contains(got.ScriptWarning, "adjusting") {
		t.Fatalf("%d %s", code, body)
	}
}

func TestCatalog_SaysTheWordsCanBeTimedAndMoved(t *testing.T) {
	s := mmServer(t)
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/music-motion/catalog", nil)
	var c PieceCatalog
	_ = json.Unmarshal(rec.Body.Bytes(), &c)
	for _, f := range c.Features {
		if f == "word-timing" {
			return
		}
	}
	t.Fatalf("features = %v", c.Features)
}

func TestCatalog_SaysPiecesCanHaveAVideoBehindThem(t *testing.T) {
	s := mmServer(t)
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/music-motion/catalog", nil)
	var c PieceCatalog
	_ = json.Unmarshal(rec.Body.Bytes(), &c)
	found := map[string]bool{}
	for _, f := range c.Features {
		found[f] = true
	}
	if !found["video-background"] || !found["video-track"] || !found["verse-legibility"] {
		t.Fatalf("features = %v", c.Features)
	}
	if !strings.Contains(rec.Body.String(), `"bands":{"top":`) {
		t.Fatal("the parts do not say how much of each band of the frame they occupy")
	}
	if !strings.Contains(rec.Body.String(), `"over_video":`) {
		t.Fatal("the parts do not say whether they can be over a video")
	}
}

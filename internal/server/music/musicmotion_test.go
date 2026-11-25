package music

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"veritmo/internal/platform/settings"
	"veritmo/internal/server/workspace"

	"veritmo/internal/music/musicmotion"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/runner"
	"veritmo/internal/video/motion"
)

// mmServer is a server with a music project whose song is real audio (a 20 s tone), runs that finish at once and a
// browser that is not looked for. The settings folder is the test's own, so no part or character of the user's is read.
func mmServer(t *testing.T) *testServer {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s, projects := newProjectsServer(t)
	songsProject(t, s, projects)
	song := filepath.Join(projects, "songs", "out", "songs", "night_drive.mp3")
	if b, err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=330:duration=20", song).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, b)
	}
	s.motionBrowser = func() (motion.Browser, error) { return motion.Browser{Path: "/fake/chrome", Version: "1"}, nil }
	// No model: the proposals must never reach the Ollama of the machine that runs the tests.
	s.motionWriter = func(context.Context) (motion.Writer, motion.ModelInfo, error) {
		return nil, motion.ModelInfo{}, errors.New("no model in the tests")
	}
	s.Workspace.SetPipelineLookPath(fakeLook)
	s.Workspace.Execute = func(ctx context.Context, rn workspace.ExecRun, em *events.Emitter) (string, error) {
		em.PipelineStart()
		out := "out/" + rn.Rec.Project + ".mp4"
		if err := os.MkdirAll(filepath.Join(rn.Dir, "out"), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(rn.Dir, filepath.FromSlash(out)), []byte("video"), 0o644); err != nil {
			return "", err
		}
		em.Done(out)
		return out, nil
	}
	t.Cleanup(s.Workspace.Shutdown)
	return s
}

func newPiece(t *testing.T, s *testServer, extra map[string]any) PieceView {
	t.Helper()
	body := map[string]any{"project": "songs", "track_id": "night_drive", "client": "video-studio"}
	for k, v := range extra {
		body[k] = v
	}
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var v PieceView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestMusicMotion_TheCatalogListsPartsCharactersAndTheChoices(t *testing.T) {
	s := mmServer(t)
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/music-motion/catalog", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var c PieceCatalog
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Parts) < 12 || len(c.Characters) != 3 || len(c.Backgrounds) != 4 || len(c.Levels) != 3 || len(c.Styles) != 2 || len(c.FrameRates) != 3 {
		t.Fatalf("%d parts %d characters %d backgrounds %v %v %v", len(c.Parts), len(c.Characters), len(c.Backgrounds), c.Levels, c.Styles, c.FrameRates)
	}
	var toggle CatalogPart
	for _, p := range c.Parts {
		if p.Name == "toggle" {
			toggle = p
		}
	}
	if toggle.Name == "" || len(toggle.Params) != 2 || toggle.CostMS == 0 || len(toggle.Levels) != 3 {
		t.Fatalf("%+v", toggle)
	}
}

func TestMusicMotion_CreateMakesAPieceWithItsMapDesignAndScript(t *testing.T) {
	s := mmServer(t)
	v := newPiece(t, s, nil)
	if !strings.HasPrefix(v.ID, "api-mm-") || v.State != "draft" || v.Style != "typographic" || v.Level != "simple" || v.FPS != 30 || v.Client != "video-studio" {
		t.Fatalf("%+v", v)
	}
	if v.Design == nil || v.Script == nil || v.SongMap == nil || len(v.Script.Pieces) < 2 || v.SongMap.Duration < 19 || v.Estimate == nil || !v.Estimate.Estimate {
		t.Fatalf("design/script/map/estimate: %+v", v)
	}
	if !v.SongMap.WordsEstimated || len(v.SongMap.Words) != 4 {
		t.Fatalf("the lyrics of the song are spread over its sections: %+v", v.SongMap.Words)
	}
	got := doJSON(t, s.Handler(), http.MethodGet, "/api/music-motion/pieces/"+v.ID, nil)
	if got.Code != http.StatusOK {
		t.Fatalf("%d", got.Code)
	}
	list := doJSON(t, s.Handler(), http.MethodGet, "/api/music-motion/pieces", nil)
	var views []PieceView
	json.Unmarshal(list.Body.Bytes(), &views)
	if len(views) != 1 || views[0].Script != nil {
		t.Fatalf("the list holds summaries: %+v", views)
	}
}

func TestMusicMotion_TheIllustratedStyleDefaultsTo24AndBringsAStage(t *testing.T) {
	s := mmServer(t)
	v := newPiece(t, s, map[string]any{"style": "illustrated", "level": "medium"})
	if v.FPS != 24 || v.Level != "medium" {
		t.Fatalf("%+v", v)
	}
	for _, p := range v.Script.Pieces {
		if p.Part != "blank" || p.Stage == nil || len(p.Stage.Characters) == 0 {
			t.Fatalf("%+v", p)
		}
	}
}

func TestMusicMotion_ARefusedRequestIsA422AndCreatesNothing(t *testing.T) {
	s := mmServer(t)
	cases := map[string]map[string]any{
		"bad level":       {"level": "extreme"},
		"bad style":       {"style": "cubist"},
		"bad fps":         {"fps": 25},
		"bad client":      {"client": "Bad Client!"},
		"unknown track":   {"track_id": "nope"},
		"unknown project": {"project": "nowhere"},
		"bad resolution":  {"resolution": "wide"},
		"bad design":      {"design": map[string]any{"dark": "x"}},
	}
	for name, extra := range cases {
		body := map[string]any{"project": "songs", "track_id": "night_drive"}
		for k, v := range extra {
			body[k] = v
		}
		rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces", body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	entries, _ := os.ReadDir(s.ProjectsDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "api-mm-") {
			t.Errorf("a refused request must create nothing, found %s", e.Name())
		}
	}
}

func TestMusicMotion_APieceIsNotAUsersProject(t *testing.T) {
	s := mmServer(t)
	v := newPiece(t, s, nil)
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces/"+v.ID+"/render", nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	list := doJSON(t, s.Handler(), http.MethodGet, "/api/projects", nil)
	if strings.Contains(list.Body.String(), "api-mm-") {
		t.Fatalf("a piece must not be listed as a project: %s", list.Body)
	}
}

func TestMusicMotion_EditingTheDesignAndTheScriptIsValidated(t *testing.T) {
	s := mmServer(t)
	v := newPiece(t, s, nil)
	base := "/api/music-motion/pieces/" + v.ID
	bad := *v.Design
	bad.Design.Accent = "nope"
	if rec := doJSON(t, s.Handler(), http.MethodPut, base+"/design", bad.Design); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	good := v.Design.Design
	good.Concept = "my own idea"
	if rec := doJSON(t, s.Handler(), http.MethodPut, base+"/design", good); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	script := *v.Script
	script.Pieces = append([]musicmotion.Piece(nil), script.Pieces...)
	script.Pieces[0].Part = "hologram"
	if rec := doJSON(t, s.Handler(), http.MethodPut, base+"/script", script); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "hologram") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	script.Pieces[0].Part = "toggle"
	if rec := doJSON(t, s.Handler(), http.MethodPut, base+"/script", script); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	got := doJSON(t, s.Handler(), http.MethodGet, base, nil)
	var after PieceView
	json.Unmarshal(got.Body.Bytes(), &after)
	if after.Design.Design.Concept != "my own idea" || !after.Design.Edited || after.Script.Pieces[0].Part != "toggle" || after.ScriptSource != "user" {
		t.Fatalf("%+v", after)
	}
}

func TestMusicMotion_ProposeWithoutAnAssistantFallsBackWithNoError(t *testing.T) {
	s := mmServer(t)
	v := newPiece(t, s, nil)
	base := "/api/music-motion/pieces/" + v.ID
	rec := doJSON(t, s.Handler(), http.MethodPost, base+"/propose", map[string]any{"what": "design"})
	var a ProposeAnswer
	json.Unmarshal(rec.Body.Bytes(), &a)
	if rec.Code != http.StatusOK || a.Source != "fallback" || a.Design == nil {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec = doJSON(t, s.Handler(), http.MethodPost, base+"/propose", map[string]any{"what": "script"})
	a = ProposeAnswer{}
	json.Unmarshal(rec.Body.Bytes(), &a)
	if rec.Code != http.StatusOK || a.Source != "fallback" || a.Script == nil || len(a.Script.Pieces) == 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodPost, base+"/propose", map[string]any{"what": "music"}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("%d", rec.Code)
	}
}

func TestMusicMotion_RenderLaunchesARunAndEndsWithTheVideo(t *testing.T) {
	s := mmServer(t)
	v := newPiece(t, s, nil)
	base := "/api/music-motion/pieces/" + v.ID
	rec := doJSON(t, s.Handler(), http.MethodPost, base+"/render", nil)
	var ans RenderAnswer
	json.Unmarshal(rec.Body.Bytes(), &ans)
	if rec.Code != http.StatusAccepted || ans.RunID == "" || !ans.Estimate.Estimate || ans.Estimate.Seconds <= 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	// The project of the piece is written with a scene for each piece of the script.
	if _, err := os.Stat(filepath.Join(s.pieceDir(v.ID), "scenes.yaml")); err != nil {
		t.Fatal(err)
	}
	var got PieceView
	for i := 0; i < 100; i++ {
		r := doJSON(t, s.Handler(), http.MethodGet, base, nil)
		json.Unmarshal(r.Body.Bytes(), &got)
		if got.State == "done" || got.State == "failed" {
			break
		}
		waitABit()
	}
	if got.State != "done" || got.Video == "" || got.RunID != ans.RunID {
		t.Fatalf("%+v", got)
	}
	// The project of the piece is in the database, of the kind animation, and is not among the user's projects.
	if !s.Workspace.ProjectExists(v.ID) {
		t.Fatal("the project of a rendered piece has a row")
	}
	var kind string
	s.Workspace.Store().DB.Table("projects").Select("kind").Where("id = ?", v.ID).Scan(&kind)
	if kind != "animation" {
		t.Fatalf("kind %q, want animation", kind)
	}
	if list := doJSON(t, s.Handler(), http.MethodGet, "/api/projects?managed=include", nil); strings.Contains(list.Body.String(), v.ID) {
		t.Fatal("an animation is never in the list of projects")
	}
	// The run is the project's, written with the client of the piece.
	raw, _ := os.ReadFile(filepath.Join(s.pieceDir(v.ID), "scenes.yaml"))
	if !strings.Contains(string(raw), "client: video-studio") {
		t.Fatalf("scenes.yaml: %s", raw)
	}
}

func TestMusicMotion_RenderWithoutABrowserSaysSo(t *testing.T) {
	s := mmServer(t)
	s.motionBrowser = func() (motion.Browser, error) { return motion.Browser{}, os.ErrNotExist }
	v := newPiece(t, s, nil)
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces/"+v.ID+"/render", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestMusicMotion_DeleteRemovesThePieceAndItsFiles(t *testing.T) {
	s := mmServer(t)
	v := newPiece(t, s, nil)
	if rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/music-motion/pieces/"+v.ID, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(s.pieceDir(v.ID)); !os.IsNotExist(err) {
		t.Fatal("the files must be gone")
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/music-motion/pieces/"+v.ID, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("%d", rec.Code)
	}
	if rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/music-motion/pieces/api-mm-nothing", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("%d", rec.Code)
	}
}

func waitABit() { time.Sleep(30 * time.Millisecond) }

type answerWriter struct{ answer string }

func (a answerWriter) Write(ctx context.Context, system, user string, maxTokens int) (string, error) {
	return a.answer, nil
}

func TestMusicMotion_AnAssistantDesignIsUsedAndSaid(t *testing.T) {
	s := mmServer(t)
	s.motionWriter = func(context.Context) (motion.Writer, motion.ModelInfo, error) {
		return answerWriter{`{"dark":"#0b0f14","light":"#e8f1ec","accent":"#ff2e54","display":"heavy","body":"grotesk","default_mode":"dark","concept":"a stage light that follows the voice"}`}, motion.ModelInfo{}, nil
	}
	v := newPiece(t, s, nil)
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces/"+v.ID+"/propose", map[string]any{"what": "design"})
	var a ProposeAnswer
	json.Unmarshal(rec.Body.Bytes(), &a)
	if rec.Code != http.StatusOK || a.Source != "assistant" || a.Design.Design.Concept != "a stage light that follows the voice" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestContract_APieceAndItsCatalogFitTheirDescription(t *testing.T) {
	c := newContractChecker(t)
	s := mmServer(t)
	v := newPiece(t, s, nil)
	expectFits(t, c, "/api/music-motion/catalog", get(t, s, "/api/music-motion/catalog"))
	expectFits(t, c, "/api/music-motion/pieces", get(t, s, "/api/music-motion/pieces"))
	expectFits(t, c, "/api/music-motion/pieces/{id}", get(t, s, "/api/music-motion/pieces/"+v.ID))
	// An illustrated one carries stages, and a done one carries its video: both shapes must fit too.
	ill := newPiece(t, s, map[string]any{"style": "illustrated"})
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces/"+ill.ID+"/render", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for i := 0; i < 100; i++ {
		var got PieceView
		json.Unmarshal([]byte(get(t, s, "/api/music-motion/pieces/"+ill.ID)), &got)
		if got.State == "done" {
			break
		}
		waitABit()
	}
	expectFits(t, c, "/api/music-motion/pieces/{id}", get(t, s, "/api/music-motion/pieces/"+ill.ID))
}

// --- hearing the words ---------------------------------------------------------------------------

// heardJSON is the full output of whisper-cli (-ojf -dtw): the segments, and in each its token with the time DTW gave it. The
// words are timed by token; read by segment (a model with no preset) they get the same times.
const heardJSON = `{"result":{"language":"en"},"transcription":[
 {"offsets":{"from":1000,"to":1500},"text":" one","tokens":[{"text":" one","offsets":{"from":1000,"to":1500},"p":0.9,"t_dtw":100}]},
 {"offsets":{"from":3000,"to":3500},"text":" two","tokens":[{"text":" two","offsets":{"from":3000,"to":3500},"p":0.9,"t_dtw":300}]},
 {"offsets":{"from":10000,"to":10500},"text":" three","tokens":[{"text":" three","offsets":{"from":10000,"to":10500},"p":0.9,"t_dtw":1000}]},
 {"offsets":{"from":12000,"to":12500},"text":" four","tokens":[{"text":" four","offsets":{"from":12000,"to":12500},"p":0.9,"t_dtw":1200}]}]}`

// hearRunner is the real runner except for whisper-cli, which writes the JSON given and is counted.
type hearRunner struct {
	inner  runner.CommandRunner
	result string
	// byWav, when it has the base name of the file whisper-cli is asked to read, answers that JSON instead of result.
	byWav map[string]string
	calls int
	// whisperWav is the file whisper-cli was last asked to read.
	whisperWav string
	// separations counts the runs of the fake demucs, which writes the four stems; its vocals are voiceAmp loud (0 is silence).
	separations int
	voiceAmp    float64
}

func (h *hearRunner) Run(ctx context.Context, name string, args []string, opts ...runner.Option) ([]byte, error) {
	switch name {
	case "/fake/whisper-cli":
		h.calls++
		var of string
		for i, a := range args {
			switch a {
			case "-f":
				h.whisperWav = args[i+1]
			case "-of":
				of = args[i+1]
			}
		}
		if of == "" {
			return nil, nil
		}
		result := h.result
		if r, ok := h.byWav[filepath.Base(h.whisperWav)]; ok {
			result = r
		}
		return nil, os.WriteFile(of+".json", []byte(result), 0o644)
	case "/fake/demucs":
		h.separations++
		for _, stem := range []string{"target_0_drums.wav", "target_1_bass.wav", "target_2_other.wav"} {
			if err := os.WriteFile(filepath.Join(args[2], stem), []byte("stem"), 0o644); err != nil {
				return nil, err
			}
		}
		// a voice that sings two seconds in every four (a steady tone would be taken for the noise floor)
		expr := fmt.Sprintf("aevalsrc=%v*sin(2*PI*220*t)*lt(mod(t\\,4)\\,2):d=20:s=44100", h.voiceAmp)
		out, err := h.inner.Run(ctx, "ffmpeg", []string{"-v", "error", "-y", "-f", "lavfi", "-i", expr,
			"-ac", "1", "-ar", "44100", filepath.Join(args[2], "target_3_vocals.wav")})
		return out, err
	}
	return h.inner.Run(ctx, name, args, opts...)
}

// withWhisper gives the server a Whisper that answers `result` (the model is a file in a folder of the test's own).
func withWhisper(t *testing.T, s *testServer, result string) *hearRunner {
	t.Helper()
	dir := t.TempDir()
	s.whisperDir = dir
	if err := os.WriteFile(filepath.Join(dir, "ggml-large-v3-turbo-q5_0.bin"), []byte("model"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.memAvailable = func() (int64, error) { return 8 << 30, nil }
	hr := &hearRunner{inner: s.MediaRunner, result: result}
	s.MediaRunner = hr
	s.Look = func(name string) (string, error) {
		if name == "whisper-cli" {
			return "/fake/whisper-cli", nil
		}
		return exec.LookPath(name)
	}
	return hr
}

func TestMusicMotion_NothingIsHeardUnlessAsked(t *testing.T) {
	s := mmServer(t)
	hr := withWhisper(t, s, heardJSON)
	v := newPiece(t, s, nil)
	if hr.calls != 0 || !v.SongMap.WordsEstimated || v.WordsNote != "" {
		t.Fatalf("calls=%d estimated=%v note=%q", hr.calls, v.SongMap.WordsEstimated, v.WordsNote)
	}
}

func TestMusicMotion_AskedAtCreationTheWordsAreHeard(t *testing.T) {
	s := mmServer(t)
	hr := withWhisper(t, s, heardJSON)
	v := newPiece(t, s, map[string]any{"transcribe": true})
	if hr.calls != 1 || v.SongMap.WordsEstimated || len(v.SongMap.Words) != 4 {
		t.Fatalf("calls=%d %+v", hr.calls, v.SongMap)
	}
	if v.SongMap.Words[0].Text != "one" || v.SongMap.Words[0].Start != 1 || v.SongMap.Words[2].Start != 10 {
		t.Fatalf("the words are the written ones with the heard times: %+v", v.SongMap.Words)
	}
	chorus := v.SongMap.Sections[len(v.SongMap.Sections)-1]
	if chorus.Tag != "Chorus" || chorus.Start > 10 || chorus.Start < 9 {
		t.Fatalf("the chorus starts where it is first sung: %+v", v.SongMap.Sections)
	}
}

func TestMusicMotion_AskedAtCreationWithoutWhisperTheWordsAreEstimatedAndSaid(t *testing.T) {
	s := mmServer(t) // no Whisper in this machine's test settings
	s.Look = func(name string) (string, error) {
		if name == "whisper-cli" {
			return "", errors.New("not found")
		}
		return exec.LookPath(name)
	}
	v := newPiece(t, s, map[string]any{"transcribe": true})
	if !v.SongMap.WordsEstimated || !strings.Contains(v.WordsNote, "not heard") {
		t.Fatalf("estimated=%v note=%q", v.SongMap.WordsEstimated, v.WordsNote)
	}
}

func TestMusicMotion_TheRouteHearsTheWordsOfAPieceAndMakesTheScriptAgain(t *testing.T) {
	s := mmServer(t)
	withWhisper(t, s, heardJSON)
	v := newPiece(t, s, nil)
	before := v.Script.Pieces
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces/"+v.ID+"/transcribe", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var got PieceView
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got.SongMap.WordsEstimated || got.SongMap.Words[0].Start != 1 || got.ScriptWarning != "" || got.ScriptSource != "fallback" {
		t.Fatalf("%+v", got)
	}
	last := got.Script.Pieces[len(got.Script.Pieces)-1]
	if got.Script.Pieces[0].Start != 0 || last.End != got.SongMap.Duration {
		t.Fatalf("the new script covers the song: %+v", got.Script.Pieces)
	}
	if len(before) == 0 || !reflect.DeepEqual(got.Design, v.Design) || got.Style != v.Style || got.Level != v.Level || got.FPS != v.FPS {
		t.Fatal("the design, the level, the style and the rate do not change")
	}
	again := doJSON(t, s.Handler(), http.MethodGet, "/api/music-motion/pieces/"+v.ID, nil)
	var stored PieceView
	json.Unmarshal(again.Body.Bytes(), &stored)
	if stored.SongMap.WordsEstimated || stored.ScriptWarning != "" {
		t.Fatalf("the rebuilt map is stored: %+v", stored.SongMap.WordsEstimated)
	}
}

func TestMusicMotion_AScriptTheUserEditedIsKeptWithAWarning(t *testing.T) {
	s := mmServer(t)
	withWhisper(t, s, heardJSON)
	v := newPiece(t, s, nil)
	base := "/api/music-motion/pieces/" + v.ID
	script := *v.Script
	script.Pieces = []musicmotion.Piece{
		{ID: "a", Start: 0, End: 4.5, Section: "Verse", Part: "toggle"},
		{ID: "b", Start: 4.5, End: v.SongMap.Duration, Section: "Chorus", Part: "stamp"},
	}
	if rec := doJSON(t, s.Handler(), http.MethodPut, base+"/script", script); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec := doJSON(t, s.Handler(), http.MethodPost, base+"/transcribe", nil)
	var got PieceView
	json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != http.StatusOK || got.ScriptSource != "user" || len(got.Script.Pieces) != 2 || got.Script.Pieces[0].Part != "toggle" || !strings.Contains(got.ScriptWarning, "adjusting") {
		t.Fatalf("%d %+v", rec.Code, got.ScriptWarning)
	}
}

func TestMusicMotion_TheRouteSaysWhyItCannotHear(t *testing.T) {
	s := mmServer(t)
	v := newPiece(t, s, nil)
	url := "/api/music-motion/pieces/" + v.ID + "/transcribe"
	// No Whisper installed.
	s.Look = func(name string) (string, error) {
		if name == "whisper-cli" {
			return "", errors.New("not found")
		}
		return exec.LookPath(name)
	}
	if rec := doJSON(t, s.Handler(), http.MethodPost, url, nil); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "not installed") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	// No room for the model.
	withWhisper(t, s, heardJSON)
	s.memAvailable = func() (int64, error) { return 1 << 20, nil }
	if rec := doJSON(t, s.Handler(), http.MethodPost, url, nil); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "not enough memory") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces/api-mm-nothing/transcribe", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("%d", rec.Code)
	}
	var unchanged PieceView
	json.Unmarshal([]byte(get(t, s, "/api/music-motion/pieces/"+v.ID)), &unchanged)
	if !unchanged.SongMap.WordsEstimated {
		t.Fatal("a refused request changes nothing")
	}
}

func TestMusicMotion_TheRouteWaitsWhileARunIsInProgress(t *testing.T) {
	s := mmServer(t)
	withWhisper(t, s, heardJSON)
	v := newPiece(t, s, nil)
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
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces/"+v.ID+"/transcribe", nil)
	close(release)
	if rec.Code != http.StatusConflict {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestContract_APieceWithHeardWordsAndAWarningFitsItsDescription(t *testing.T) {
	c := newContractChecker(t)
	s := mmServer(t)
	withWhisper(t, s, heardJSON)
	v := newPiece(t, s, nil)
	base := "/api/music-motion/pieces/" + v.ID
	script := *v.Script
	script.Pieces = []musicmotion.Piece{{ID: "a", Start: 0, End: 4.5, Section: "Verse", Part: "toggle"}, {ID: "b", Start: 4.5, End: v.SongMap.Duration, Section: "Chorus", Part: "stamp"}}
	doJSON(t, s.Handler(), http.MethodPut, base+"/script", script)
	rec := doJSON(t, s.Handler(), http.MethodPost, base+"/transcribe", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "script_warning") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	expectFits(t, c, "/api/music-motion/pieces/{id}", rec.Body.String())
	// And the note of a piece whose words could not be heard.
	s.Look = func(name string) (string, error) {
		if name == "whisper-cli" {
			return "", errors.New("not found")
		}
		return exec.LookPath(name)
	}
	made := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces", map[string]any{"project": "songs", "track_id": "night_drive", "transcribe": true})
	if made.Code != http.StatusCreated || !strings.Contains(made.Body.String(), "words_note") {
		t.Fatalf("%d %s", made.Code, made.Body)
	}
	expectFits(t, c, "/api/music-motion/pieces/{id}", made.Body.String())
}

func TestPresets_APieceMadeWithAStyleUsesItsDesignItsPartsAndItsRhythm(t *testing.T) {
	s := mmServer(t)
	d := musicmotion.Fallback("neon synth", []string{"verse", "chorus"})
	d.Accent = "#22e07a"
	p, err := s.gen.PresetStore().Add(musicmotion.Preset{Name: "Two parts", Design: d, Rhythm: 4, Parts: []string{"toggle", "counter"}})
	if err != nil {
		t.Fatal(err)
	}
	v := newPiece(t, s, map[string]any{"preset": p.ID})
	if v.Preset != p.ID || v.Design == nil || v.Design.Design.Accent != "#22e07a" || v.Design.Design.Dark != d.Dark {
		t.Fatalf("%+v", v.Design)
	}
	if !v.Design.Edited {
		t.Fatal("the style's design is the user's to edit: a new song does not overwrite it")
	}
	for _, pc := range v.Script.Pieces {
		if pc.Part != "toggle" && pc.Part != "counter" {
			t.Fatalf("only the style's parts: %s", pc.Part)
		}
		if pc.End-pc.Start > 4.05 {
			t.Fatalf("pieces of at most the rhythm: %v", pc.End-pc.Start)
		}
	}
	// The design the request names wins over the style's.
	other := d
	other.Accent = "#ff3da8"
	v = newPiece(t, s, map[string]any{"preset": p.ID, "design": other})
	if v.Design.Design.Accent != "#ff3da8" {
		t.Fatalf("%+v", v.Design.Design)
	}
}

func TestPresets_AnUnknownStyleIsRefusedAndNoPieceIsMade(t *testing.T) {
	s := mmServer(t)
	before := len(s.Workspace.ManagedList(piecePrefix))
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces", map[string]any{"project": "songs", "track_id": "night_drive", "client": "video-studio", "preset": "preset-nope"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if after := len(s.Workspace.ManagedList(piecePrefix)); after != before {
		t.Fatalf("nothing is created: %d pieces before, %d after", before, after)
	}
}

func TestPresets_APieceWithNoStyleIsMadeAsBefore(t *testing.T) {
	s := mmServer(t)
	v := newPiece(t, s, nil)
	if v.Preset != "" {
		t.Fatalf("%q", v.Preset)
	}
	used := map[string]bool{}
	home := map[string]bool{}
	for _, pc := range v.Script.Pieces {
		used[pc.Part] = true
		if musicmotion.IsChorus(pc.Section) {
			home[pc.Part] = true
		}
	}
	// the usual parts are taken in turn, and the chorus comes back to its home part (its first piece) without holding one part for
	// all its pieces, so it uses at most two parts per three pieces and never the same one twice in a row
	if len(used) < 2 {
		t.Fatalf("the usual parts are taken in turn: %v, chorus %v", used, home)
	}
	if v.Script.Home == "" {
		t.Fatal("the script names its home part")
	}
	for i := 1; i < len(v.Script.Pieces); i++ {
		if v.Script.Pieces[i].Part == v.Script.Pieces[i-1].Part {
			t.Fatalf("%s repeats %s of the piece before", v.Script.Pieces[i].ID, v.Script.Pieces[i].Part)
		}
	}
}

func TestMusicMotion_ProposeWithAProvider(t *testing.T) {
	s := mmServer(t)
	for _, p := range []settings.Provider{
		{ID: "lan", Name: "LAN", Type: settings.TypeOpenAI, BaseURL: "http://127.0.0.1:8000/v1", TextModel: "m", VisionModel: "v"},
		{ID: "cloud", Name: "Cloud", Type: settings.TypeOpenAI, BaseURL: "https://example.com/v1", TextModel: "m", VisionModel: "v"},
	} {
		if _, err := settings.AddProvider(p); err != nil {
			t.Fatal(err)
		}
	}
	v := newPiece(t, s, nil)
	base := "/api/music-motion/pieces/" + v.ID + "/propose"
	// Asking for a provider is not silently replaced by the fixed rule when it cannot be used.
	for prov, want := range map[string]int{"nope": http.StatusBadRequest, "cloud": http.StatusBadRequest} {
		if rec := doJSON(t, s.Handler(), http.MethodPost, base, map[string]any{"what": "design", "provider": prov}); rec.Code != want {
			t.Fatalf("%s: %d %s", prov, rec.Code, rec.Body)
		}
	}
	rec := doJSON(t, s.Handler(), http.MethodPost, base, map[string]any{"what": "design", "provider": "lan"})
	var a ProposeAnswer
	json.Unmarshal(rec.Body.Bytes(), &a)
	if rec.Code != http.StatusOK || a.Provider != "lan" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// A run whose output has no token times (an older whisper-cli, or flash attention left on) is timed by segments and the
// answer says so; one with token times says nothing.
const segmentsOnlyJSON = `{"result":{"language":"en"},"transcription":[
 {"offsets":{"from":1000,"to":1500},"text":" one"},
 {"offsets":{"from":3000,"to":3500},"text":" two"},
 {"offsets":{"from":10000,"to":10500},"text":" three"},
 {"offsets":{"from":12000,"to":12500},"text":" four"}]}`

func TestMusicMotion_TheNoteSaysWhenTheWordsWereTimedBySegments(t *testing.T) {
	s := mmServer(t)
	hr := withWhisper(t, s, segmentsOnlyJSON)
	v := newPiece(t, s, map[string]any{"transcribe": true})
	if hr.calls != 2 || !strings.Contains(v.WordsNote, "timed by segments") || v.SongMap.WordsEstimated || len(v.SongMap.Words) != 4 {
		t.Fatalf("the token call and then the segments one: calls=%d note=%q words=%d", hr.calls, v.WordsNote, len(v.SongMap.Words))
	}
	s2 := mmServer(t)
	hr2 := withWhisper(t, s2, heardJSON)
	if v := newPiece(t, s2, map[string]any{"transcribe": true}); hr2.calls != 1 || strings.Contains(v.WordsNote, "timed by segments") {
		t.Fatalf("token times: calls=%d note=%q", hr2.calls, v.WordsNote)
	}
}

// A design saved with other modes says how many pieces still carry a mode of their own that differs; it changes none.
func TestMusicMotion_ADesignSavedUnderExplicitModesCountsThem(t *testing.T) {
	s := mmServer(t)
	v := newPiece(t, s, nil)
	base := "/api/music-motion/pieces/" + v.ID
	design := v.Design.Design
	design.DefaultMode, design.Modes = "light", nil
	design.Accent = "#d12f58" // readable on both tones
	if rec := doJSON(t, s.Handler(), http.MethodPut, base+"/design", design); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	script := *v.Script
	script.Pieces = append([]musicmotion.Piece(nil), script.Pieces...)
	for i := range script.Pieces {
		script.Pieces[i].Mode = "light"
	}
	if rec := doJSON(t, s.Handler(), http.MethodPut, base+"/script", script); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	n := len(script.Pieces)

	design.DefaultMode = "dark"
	rec := doJSON(t, s.Handler(), http.MethodPut, base+"/design", design)
	var st musicmotion.Stored
	json.Unmarshal(rec.Body.Bytes(), &st)
	want := fmt.Sprintf("%d pieces of the script carry a mode of their own that differs from the design's", n)
	if rec.Code != http.StatusOK || !strings.HasPrefix(st.Notice, want) {
		t.Fatalf("notice %q, want it to start with %q (%d %s)", st.Notice, want, rec.Code, rec.Body)
	}
	var after PieceView
	json.Unmarshal(doJSON(t, s.Handler(), http.MethodGet, base, nil).Body.Bytes(), &after)
	for _, p := range after.Script.Pieces {
		if p.Mode != "light" {
			t.Fatalf("the script was changed: %+v", p)
		}
	}

	// Nothing about the modes changed: no notice. The notice is never kept in the file.
	design.Concept = "another idea"
	rec = doJSON(t, s.Handler(), http.MethodPut, base+"/design", design)
	if strings.Contains(rec.Body.String(), "notice") {
		t.Fatalf("a notice without a change of modes: %s", rec.Body)
	}

	// The modes cleared: the next change of the design has nothing to count.
	for i := range script.Pieces {
		script.Pieces[i].Mode = ""
	}
	if rec := doJSON(t, s.Handler(), http.MethodPut, base+"/script", script); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	design.DefaultMode = "light"
	rec = doJSON(t, s.Handler(), http.MethodPut, base+"/design", design)
	if strings.Contains(rec.Body.String(), "notice") {
		t.Fatalf("a notice with no explicit mode: %s", rec.Body)
	}
}

func TestTheCatalogMarksThePartsThatRegisterATimeline(t *testing.T) {
	tl := musicmotion.Part{Manifest: musicmotion.Manifest{Name: "tl", Description: "d", Version: 1, Levels: []string{"simple"}},
		Fragment: `<script>window.__timelines = {m: gsap.timeline({paused: true})};</script>`, Source: "user"}
	plain := musicmotion.Part{Manifest: musicmotion.Manifest{Name: "plain", Description: "d", Version: 1, Levels: []string{"simple"}},
		Fragment: `<script>window.__partSeek = function () {};</script>`, Source: "user"}
	if !catalogPartOf(tl).Timeline || catalogPartOf(plain).Timeline {
		t.Fatal("timeline must be true for a part that registers a timeline and absent for any other")
	}
	if b, _ := json.Marshal(catalogPartOf(plain)); strings.Contains(string(b), "timeline") {
		t.Fatalf("a part with no timeline does not carry the field: %s", b)
	}
}

func TestTheCatalogListsHowManyPicturesAPartCarries(t *testing.T) {
	inked := musicmotion.Part{Manifest: musicmotion.Manifest{Name: "hf-inked", Description: "d", Version: 1, Levels: []string{"simple"}, Pictures: 1},
		Fragment: `<img src="data:image/svg+xml;base64,PHN2Zy8+">`, Source: "user",
		Origin: &musicmotion.PartOrigin{Source: "hyperframes", Block: "inked", Files: []musicmotion.OriginFile{{Name: "stroke.svg", Kind: "svg", Bytes: 99}}}}
	plain := musicmotion.Part{Manifest: musicmotion.Manifest{Name: "plain", Description: "d", Version: 1, Levels: []string{"simple"}}, Source: "user"}
	if catalogPartOf(inked).Pictures != 1 {
		t.Fatal("pictures: 1 for a part that carries one")
	}
	b, _ := json.Marshal(catalogPartOf(inked))
	if !strings.Contains(string(b), `"pictures":1`) || !strings.Contains(string(b), `"stroke.svg"`) {
		t.Fatalf("the catalog entry carries the count and the origin's files: %s", b)
	}
	if b, _ := json.Marshal(catalogPartOf(plain)); strings.Contains(string(b), "pictures") {
		t.Fatalf("a part with no pictures does not carry the field: %s", b)
	}
}

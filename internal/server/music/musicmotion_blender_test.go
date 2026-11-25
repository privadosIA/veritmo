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

	"veritmo/internal/music/musicmotion"
	"veritmo/internal/platform/runner"
	"veritmo/internal/video/blender"
	"veritmo/internal/video/motion"
)

// a description in seconds with explicit colours whose keys fit any piece of a second or more
const pieceScene3D = `{"version":1,"camera":{"keys":[{"t":0,"location":[0,-9,1.2],"target":[0,0,0.6]},{"t":0.8,"location":[0,-7,1],"target":[0,0,0.6]}]},` +
	`"materials":{"m":{"color":"#ff3b30"}},"objects":[{"id":"ball","type":"sphere","size":1,"material":"m","keys":[{"t":0,"location":[0,0,2]},{"t":0.5,"location":[0,0,0.5]}]}]}`

func blenderUsable(t *testing.T) {
	t.Helper()
	if _, err := blender.Detect(context.Background(), runner.NewExecRunner()); err != nil {
		t.Skipf("Blender is not usable here: %v", err)
	}
}

// putScript3D makes the piece idx of the piece's script a 3D one and saves it through the API.
func putScript3D(t *testing.T, s *testServer, v PieceView, idx int) (musicmotion.Script, int, string) {
	t.Helper()
	script := *v.Script
	script.Pieces = append([]musicmotion.Piece(nil), script.Pieces...)
	script.Pieces[idx].MakeBlender(json.RawMessage(pieceScene3D))
	rec := doJSON(t, s.Handler(), http.MethodPut, "/api/music-motion/pieces/"+v.ID+"/script", script)
	return script, rec.Code, rec.Body.String()
}

func TestBlenderPiece_TheScriptSavesReturnsAndRefusesA3DPiece(t *testing.T) {
	s := mmServer(t)
	v := newPiece(t, s, nil)
	script, code, body := putScript3D(t, s, v, 0)
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	got := doJSON(t, s.Handler(), http.MethodGet, "/api/music-motion/pieces/"+v.ID, nil)
	var after PieceView
	_ = json.Unmarshal(got.Body.Bytes(), &after)
	if p := after.Script.Pieces[0]; p.Blender == nil || !strings.Contains(string(p.Blender.Description), `"sphere"`) || p.Part != "" {
		t.Errorf("the script returns the description of a 3D piece: %+v", p)
	}
	// each fault names the piece and its place in the JSON
	bad := script
	bad.Pieces = append([]musicmotion.Piece(nil), script.Pieces...)
	bad.Pieces[0].Blender = &musicmotion.PieceBlender{Description: json.RawMessage(strings.Replace(pieceScene3D, `"t":0.8`, `"t":999`, 1))}
	rec := doJSON(t, s.Handler(), http.MethodPut, "/api/music-motion/pieces/"+v.ID+"/script", bad)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `\"`+bad.Pieces[0].ID+`\"`) || !strings.Contains(rec.Body.String(), "camera") {
		t.Errorf("a fault: %d %s", rec.Code, rec.Body)
	}
	bad.Pieces[0].Part = "toggle"
	if rec := doJSON(t, s.Handler(), http.MethodPut, "/api/music-motion/pieces/"+v.ID+"/script", bad); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "not both") {
		t.Errorf("a part and a scene: %d %s", rec.Code, rec.Body)
	}
}

func TestBlenderPiece_RenderNeedsBlenderAndOnlyNeedsTheBrowserForPages(t *testing.T) {
	blenderUsable(t)
	s := mmServer(t)
	v := newPiece(t, s, nil)
	if _, code, body := putScript3D(t, s, v, 0); code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	base := "/api/music-motion/pieces/" + v.ID
	// no Blender: 503 with how to install it, no run
	s.Stack.Workspace.Overrides.BlenderInfo = func(context.Context) (blender.Info, error) {
		return blender.Info{}, fmt.Errorf("%w: %s", blender.ErrNotInstalled, blender.InstallHint)
	}
	if rec := doJSON(t, s.Handler(), http.MethodPost, base+"/render", nil); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "pacman -S blender") {
		t.Fatalf("no Blender: %d %s", rec.Code, rec.Body)
	}
	s.Stack.Workspace.Overrides.BlenderInfo = func(context.Context) (blender.Info, error) {
		return blender.Info{Path: "/usr/bin/blender", Version: "5.2.3"}, nil
	}
	rec := doJSON(t, s.Handler(), http.MethodPost, base+"/render", nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("a mixed piece renders: %d %s", rec.Code, rec.Body)
	}
	yml, _ := os.ReadFile(filepath.Join(s.pieceDir(v.ID), "scenes.yaml"))
	first := v.Script.Pieces[0].ID
	if !strings.Contains(string(yml), "blender: {build: "+first+".scene.json}") {
		t.Errorf("scenes.yaml:\n%s", yml)
	}
	if _, err := os.Stat(filepath.Join(s.pieceDir(v.ID), "sources", first+".scene.json")); err != nil {
		t.Errorf("the description is in the sources: %v", err)
	}
}

func TestBlenderPiece_AScriptOf3DPiecesOnlyDoesNotNeedTheBrowser(t *testing.T) {
	blenderUsable(t)
	s := mmServer(t)
	v := newPiece(t, s, nil)
	script := *v.Script
	script.Pieces = append([]musicmotion.Piece(nil), script.Pieces...)
	for i := range script.Pieces {
		script.Pieces[i].MakeBlender(json.RawMessage(pieceScene3D))
	}
	if rec := doJSON(t, s.Handler(), http.MethodPut, "/api/music-motion/pieces/"+v.ID+"/script", script); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	// the review has no page to grade
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces/"+v.ID+"/review", nil); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "3D scene") {
		t.Errorf("review of 3D pieces only: %d %s", rec.Code, rec.Body)
	}
	s.motionBrowser = func() (motion.Browser, error) { return motion.Browser{}, os.ErrNotExist }
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/music-motion/pieces/"+v.ID+"/render", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("only 3D pieces need no browser: %d %s", rec.Code, rec.Body)
	}
}

func writerSeams(s *testServer) *[]blender.WriteRequest {
	var asked []blender.WriteRequest
	s.Stack.Workspace.Overrides.MotionWriter = func(context.Context) (motion.Writer, motion.ModelInfo, error) {
		return nil, motion.ModelInfo{Provider: "Ollama", Model: "writer:7b"}, nil
	}
	s.Stack.Workspace.Overrides.BlenderWrite = func(_ context.Context, _ blender.Writer, r blender.WriteRequest) (blender.WriteResult, error) {
		asked = append(asked, r)
		if strings.Contains(r.Idea, "FAIL") {
			return blender.WriteResult{}, fmt.Errorf("%w: nothing moves", blender.ErrRejected)
		}
		spec, _ := blender.Parse([]byte(pieceScene3D), 1, r.Env)
		return blender.WriteResult{Raw: []byte(pieceScene3D), Spec: spec, Attempts: 2}, nil
	}
	return &asked
}

func TestBlenderPiece_TheWriterIsToldThePieceAndSavesTheScript(t *testing.T) {
	s := mmServer(t)
	asked := writerSeams(s)
	v := newPiece(t, s, nil)
	piece := v.Script.Pieces[0]
	url := "/api/music-motion/pieces/" + v.ID + "/script/" + piece.ID + "/blender"
	rec := doJSON(t, s.Handler(), http.MethodPost, url, map[string]any{"idea": "a red ball that hits the floor"})
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var ans BlenderPieceAnswer
	_ = json.Unmarshal(rec.Body.Bytes(), &ans)
	if !ans.Saved || ans.Model != "writer:7b" || ans.Attempts != 2 || ans.Piece.Blender == nil || ans.Piece.Part != "" || ans.Resolved == nil {
		t.Fatalf("answer = %s", rec.Body)
	}
	m, _ := s.pieceMap(v.ID)
	r := (*asked)[0]
	if r.Seconds != piece.End-piece.Start || r.Width != v.Width || r.Height != v.Height || r.FPS != v.FPS || r.Env.BPM != m.BPM || r.Env.NoFiles || !r.Env.NoFontFiles || r.Env.Sources != s.pieceSources(v.ID) || !strings.HasPrefix(r.Idea, "a red ball that hits the floor") {
		t.Errorf("the writer is told the piece, the song and the design: %+v (bpm of the song %v)", r, m.BPM)
	}
	got := doJSON(t, s.Handler(), http.MethodGet, "/api/music-motion/pieces/"+v.ID, nil)
	var after PieceView
	_ = json.Unmarshal(got.Body.Bytes(), &after)
	if after.Script.Pieces[0].Blender == nil || after.Script.Pieces[0].Part != "" || after.ScriptSource != "user" || len(after.Script.Pieces) != len(v.Script.Pieces) {
		t.Errorf("the script has the 3D piece: %+v", after.Script.Pieces[0])
	}
	// revise the stored one, change a given one, and ask without saving
	doJSON(t, s.Handler(), http.MethodPost, url, map[string]any{"idea": "make it blue", "revise": true})
	if !strings.Contains(string((*asked)[1].Current), `"sphere"`) {
		t.Errorf("revise shows the stored description: %s", (*asked)[1].Current)
	}
	given := strings.Replace(pieceScene3D, "sphere", "cube", 1)
	rec = doJSON(t, s.Handler(), http.MethodPost, url, map[string]any{"idea": "bigger", "description": json.RawMessage(given), "save": false})
	_ = json.Unmarshal(rec.Body.Bytes(), &ans)
	if rec.Code != http.StatusOK || ans.Saved || !strings.Contains(string((*asked)[2].Current), `"cube"`) {
		t.Errorf("change a given one: %d %s current=%s", rec.Code, rec.Body, (*asked)[2].Current)
	}
	raw := doJSON(t, s.Handler(), http.MethodGet, "/api/music-motion/pieces/"+v.ID, nil)
	if !strings.Contains(raw.Body.String(), `"sphere"`) || strings.Contains(raw.Body.String(), `"cube"`) {
		t.Errorf("save false leaves the script as it was")
	}
}

func TestBlenderPiece_TheWriterFailsAndRefusesAsTheOtherWritersDo(t *testing.T) {
	s := mmServer(t)
	writerSeams(s)
	v := newPiece(t, s, nil)
	base := "/api/music-motion/pieces/" + v.ID + "/script/"
	first := v.Script.Pieces[0].ID
	if rec := doJSON(t, s.Handler(), http.MethodPost, base+first+"/blender", map[string]any{"idea": "FAIL x"}); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "nothing moves") {
		t.Errorf("a rejected scene: %d %s", rec.Code, rec.Body)
	}
	if raw := doJSON(t, s.Handler(), http.MethodGet, "/api/music-motion/pieces/"+v.ID, nil); strings.Contains(raw.Body.String(), `"blender"`) {
		t.Error("a failure leaves the script as it was")
	}
	for name, c := range map[string]struct {
		url  string
		body map[string]any
		code int
	}{
		"unknown piece":      {base + "nope/blender", map[string]any{"idea": "x"}, http.StatusNotFound},
		"revise a part one":  {base + first + "/blender", map[string]any{"idea": "x", "revise": true}, http.StatusNotFound},
		"no idea":            {base + first + "/blender", map[string]any{"idea": " "}, http.StatusBadRequest},
		"unknown piece (id)": {"/api/music-motion/pieces/api-mm-nope/script/" + first + "/blender", map[string]any{"idea": "x"}, http.StatusNotFound},
	} {
		if rec := doJSON(t, s.Handler(), http.MethodPost, c.url, c.body); rec.Code != c.code {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

func TestBlenderPiece_ThePreviewMarksItAndTheFrameIsDrawnByBlender(t *testing.T) {
	s := mmServer(t)
	v := newPiece(t, s, nil)
	if _, code, body := putScript3D(t, s, v, 0); code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	piece := v.Script.Pieces[0]
	base := "/api/music-motion/pieces/" + v.ID
	rec := doJSON(t, s.Handler(), http.MethodPost, base+"/preview", nil)
	var prev PiecePreview
	_ = json.Unmarshal(rec.Body.Bytes(), &prev)
	if rec.Code != http.StatusOK || len(prev.Scenes) != len(v.Script.Pieces) || !prev.Scenes[0].Blender || prev.Scenes[0].HTML != "" || prev.Scenes[1].Blender || prev.Scenes[1].HTML == "" {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodPost, base+"/preview?scenes="+piece.ID, nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"blender":true`) {
		t.Errorf("asking for the 3D piece alone: %d %s", rec.Code, rec.Body)
	}
	s.Stack.Workspace.Overrides.BlenderInfo = func(context.Context) (blender.Info, error) {
		return blender.Info{Path: "/fake/blender", Version: "5.3.0"}, nil
	}
	var got blender.PreviewOptions
	s.Stack.Workspace.Overrides.BlenderPreview = func(_ context.Context, _ string, _ *blender.Spec, o blender.PreviewOptions) ([]byte, error) {
		got = o
		return []byte("\x89PNG fake"), nil
	}
	at := piece.Start + 0.3
	frame := doJSON(t, s.Handler(), http.MethodGet, fmt.Sprintf("%s/frame?t=%g", base, at), nil)
	if frame.Code != http.StatusOK || frame.Header().Get("Content-Type") != "image/png" || !strings.HasPrefix(frame.Body.String(), "\x89PNG") {
		t.Fatalf("frame: %d %s", frame.Code, frame.Body)
	}
	if got.T < 0.29 || got.T > 0.31 || got.Seconds != piece.End-piece.Start || got.FPS != v.FPS || got.AspectW != v.Width || got.AspectH != v.Height {
		t.Errorf("the frame is drawn at the piece's own second: %+v", got)
	}
	// a description in the body, which is not stored
	url := base + "/script/" + piece.ID + "/blender/frame"
	given := strings.Replace(pieceScene3D, "sphere", "cube", 1)
	rec = doJSON(t, s.Handler(), http.MethodPost, url, map[string]any{"description": json.RawMessage(given), "t": 0.4, "width": 320})
	if rec.Code != http.StatusOK || got.Width != 320 {
		t.Errorf("a given description: %d %s %+v", rec.Code, rec.Body, got)
	}
	if raw := doJSON(t, s.Handler(), http.MethodGet, base, nil); strings.Contains(raw.Body.String(), `"cube"`) {
		t.Error("nothing is stored")
	}
	late := strings.Replace(pieceScene3D, `"t":0.8`, `"t":999`, 1)
	if rec := doJSON(t, s.Handler(), http.MethodPost, url, map[string]any{"description": json.RawMessage(late), "t": 0.4}); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "camera") {
		t.Errorf("a key beyond the piece: %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodPost, url, map[string]any{"description": json.RawMessage(given), "t": 9999}); rec.Code != http.StatusBadRequest {
		t.Errorf("t outside the piece: %d %s", rec.Code, rec.Body)
	}
	s.Stack.Workspace.Overrides.BlenderInfo = func(context.Context) (blender.Info, error) {
		return blender.Info{}, fmt.Errorf("%w: %s", blender.ErrNotInstalled, blender.InstallHint)
	}
	if rec := doJSON(t, s.Handler(), http.MethodPost, url, map[string]any{"description": json.RawMessage(given), "t": 0.4}); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no Blender: %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, fmt.Sprintf("%s/frame?t=%g", base, at), nil); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no Blender for the stored frame: %d %s", rec.Code, rec.Body)
	}
}

func TestBlenderPiece_TheCatalogAnnouncesItApartFromTheDatasets(t *testing.T) {
	has := map[string]bool{}
	for _, f := range musicmotion.Features {
		has[f] = true
	}
	if !has["blender"] || !has["blender-motion"] || !has["blender-models"] {
		t.Fatal(musicmotion.Features)
	}
}

func TestBlenderPiece_TheWriterAndTheFrameKnowTheModelsOfThePiece(t *testing.T) {
	s := mmServer(t)
	asked := writerSeams(s)
	v := newPiece(t, s, nil)
	putModels(t, s, v.ID)
	piece := v.Script.Pieces[0]
	url := "/api/music-motion/pieces/" + v.ID + "/script/" + piece.ID + "/blender"
	if rec := doJSON(t, s.Handler(), http.MethodPost, url, map[string]any{"idea": "a girl running across the stage"}); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	env := (*asked)[0].Env
	models := blender.ProjectModels(env)
	if len(models) != 1 || models[0].Name != "ana.glb" || strings.Join(models[0].Clips, ",") != "idle,sprint,walk" {
		t.Errorf("the writer is given the models of the piece: %+v", models)
	}
	// the frame of a description that is not saved draws the model, from the piece's sources
	s.Stack.Workspace.Overrides.BlenderInfo = func(context.Context) (blender.Info, error) {
		return blender.Info{Path: "/fake/blender", Version: "5.3.0"}, nil
	}
	var spec *blender.Spec
	var opts blender.PreviewOptions
	s.Stack.Workspace.Overrides.BlenderPreview = func(_ context.Context, _ string, sp *blender.Spec, o blender.PreviewOptions) ([]byte, error) {
		spec, opts = sp, o
		return []byte("\x89PNG fake"), nil
	}
	frame := url + "/frame"
	if rec := doJSON(t, s.Handler(), http.MethodPost, frame, map[string]any{"description": json.RawMessage(sceneWithCharacter), "t": 0.4}); rec.Code != http.StatusOK {
		t.Fatalf("a frame with a model: %d %s", rec.Code, rec.Body)
	}
	if spec == nil || spec.Objects[0].File != "ana.glb" || opts.Sources != s.pieceSources(v.ID) {
		t.Errorf("the preview gets the model and the piece's sources: %+v %q", spec, opts.Sources)
	}
	// a clip the file does not have is a 400 naming the clips
	bad := strings.Replace(sceneWithCharacter, `"sprint"`, `"skate"`, 1)
	if rec := doJSON(t, s.Handler(), http.MethodPost, frame, map[string]any{"description": json.RawMessage(bad), "t": 0.4}); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "(clips: idle, sprint, walk)") {
		t.Errorf("a clip the file lacks: %d %s", rec.Code, rec.Body)
	}
}

func TestBlenderPiece_APieceWithoutModelsGivesTheWriterNothingToListAndRefusesAModel(t *testing.T) {
	s := mmServer(t)
	asked := writerSeams(s)
	v := newPiece(t, s, nil)
	url := "/api/music-motion/pieces/" + v.ID + "/script/" + v.Script.Pieces[0].ID + "/blender"
	doJSON(t, s.Handler(), http.MethodPost, url, map[string]any{"idea": "a cube"})
	if got := blender.ProjectModels((*asked)[0].Env); len(got) != 0 {
		t.Errorf("no models, nothing listed: %+v", got)
	}
}

func TestBlenderPiece_TheStoredFrameAndTheRenderUseTheModelsOfThePiece(t *testing.T) {
	s := mmServer(t)
	v := newPiece(t, s, nil)
	putModels(t, s, v.ID)
	script := scriptWithCharacter(v, 0, sceneWithCharacter)
	if rec := doJSON(t, s.Handler(), http.MethodPut, "/api/music-motion/pieces/"+v.ID+"/script", script); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	s.Stack.Workspace.Overrides.BlenderInfo = func(context.Context) (blender.Info, error) {
		return blender.Info{Path: "/fake/blender", Version: "5.3.0"}, nil
	}
	var spec *blender.Spec
	var sources string
	s.Stack.Workspace.Overrides.BlenderPreview = func(_ context.Context, _ string, sp *blender.Spec, o blender.PreviewOptions) ([]byte, error) {
		spec, sources = sp, o.Sources
		return []byte("\x89PNG fake"), nil
	}
	piece := v.Script.Pieces[0]
	base := "/api/music-motion/pieces/" + v.ID
	if rec := doJSON(t, s.Handler(), http.MethodGet, fmt.Sprintf("%s/frame?t=%g", base, piece.Start+0.3), nil); rec.Code != http.StatusOK {
		t.Fatalf("the stored frame: %d %s", rec.Code, rec.Body)
	}
	if spec == nil || spec.Objects[0].File != "ana.glb" || sources != s.pieceSources(v.ID) {
		t.Errorf("the frame of a stored piece draws its model: %+v %q", spec, sources)
	}
	if rec := doJSON(t, s.Handler(), http.MethodPost, base+"/preview", nil); rec.Code != http.StatusOK {
		t.Errorf("the preview of a script with a model: %d %s", rec.Code, rec.Body)
	}
	// the render: the piece is written as a project whose sources keep the models, and it is accepted
	if rec := doJSON(t, s.Handler(), http.MethodPost, base+"/render", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("a script with a model renders: %d %s", rec.Code, rec.Body)
	}
	for _, name := range []string{"ana.glb", "colormap.png", piece.ID + ".scene.json"} {
		if _, err := os.Stat(filepath.Join(s.pieceDir(v.ID), "sources", name)); err != nil {
			t.Errorf("the project of the piece keeps %s: %v", name, err)
		}
	}
	yml, _ := os.ReadFile(filepath.Join(s.pieceDir(v.ID), "scenes.yaml"))
	if !strings.Contains(string(yml), "blender: {build: "+piece.ID+".scene.json}") {
		t.Errorf("scenes.yaml:\n%s", yml)
	}
}

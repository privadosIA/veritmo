package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/video/blender"
	"veritmo/internal/video/motion"
)

const describedYAML = `project: demo
output:
  mode: video
  file: out/demo.mp4
defaults:
  voice: v.onnx
  resolution: 1280x720
  sampler: euler
  steps: 20
  fps: 30
  motion:
    bpm: 120
    design:
      dark: "#0b0f14"
      light: "#e8f1ec"
      accent: "#ff3b30"
      display: playfair-display
      body: mono
      default_mode: dark
scenes:
  - id: gfx
    motion_graphic: three bullets over dark blue
    tts_text: Tres ideas
`

// scriptedWriter is the model of the tests: it gives its answers in order (the last one for ever) and keeps what it was asked.
type scriptedWriter struct {
	answers []string
	users   []string
}

func (s *scriptedWriter) Write(_ context.Context, _, user string, _ int) (string, error) {
	s.users = append(s.users, user)
	i := len(s.users) - 1
	if i >= len(s.answers) {
		i = len(s.answers) - 1
	}
	return s.answers[i], nil
}

func writerServer(t *testing.T, answers ...string) (*testServer, string, *scriptedWriter) {
	t.Helper()
	isolateSettings(t)
	s, projects := newProjectsServer(t)
	createProject(t, s, "demo")
	if err := s.SaveProjectDefinition("demo", []byte(describedYAML), "save"); err != nil {
		t.Fatal(err)
	}
	w := &scriptedWriter{answers: answers}
	s.Overrides.MotionWriter = func(context.Context) (motion.Writer, motion.ModelInfo, error) {
		return w, motion.ModelInfo{Provider: "Ollama", Model: "writer:7b"}, nil
	}
	return s, filepath.Join(projects, "demo"), w
}

func writeDescription(t *testing.T, s *testServer, body map[string]any) (int, string) {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/demo/blender/descriptions", body)
	return rec.Code, rec.Body.String()
}

// valid is a description in beats, for the project of the tests (120 bpm, fonts of the design).
const validBeats = `{"version":1,"camera":{"keys":[{"beat":0,"location":[0,-9,1.2],"target":[0,0,0.6]},{"beat":10,"location":[0,-7,1],"target":[0,0,0.6]}]},` +
	`"materials":{"m":{"color":"accent"}},"objects":[{"id":"title","type":"text","text":"Hola","size":1,"font":"display","material":"m","keys":[{"beat":0,"scale":1},{"beat":1,"scale":1.2,"ease":"back"}]}]}`

func TestDescriptions_AModelWritesOneFromAnIdeaAndItIsStoredAsWritten(t *testing.T) {
	s, dir, w := writerServer(t, validBeats)
	code, body := writeDescription(t, s, map[string]any{"name": "hola.scene.json", "idea": "  the word Hola rising  ", "seconds": 5})
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	var out struct {
		Name        string
		Description map[string]any
		Resolved    struct {
			Camera struct{ Keys []struct{ T float64 } }
		}
		Provider, Model string
		Attempts        int
	}
	json.Unmarshal([]byte(body), &out)
	if out.Name != "hola.scene.json" || out.Provider != "Ollama" || out.Model != "writer:7b" || out.Attempts != 1 {
		t.Errorf("answer = %+v", out)
	}
	if out.Resolved.Camera.Keys[1].T != 5 {
		t.Errorf("the resolved description has the beat in seconds: %+v", out.Resolved)
	}
	if _, ok := out.Description["camera"]; !ok {
		t.Errorf("the description is in the answer: %s", body)
	}
	// the file is what the model wrote (beats and names, indented), not the resolved one
	stored, err := os.ReadFile(filepath.Join(dir, "sources", "hola.scene.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stored), `"beat": 10`) || !strings.Contains(string(stored), `"accent"`) || strings.Contains(string(stored), "#ff3b30") || !strings.HasSuffix(string(stored), "}\n") {
		t.Errorf("stored:\n%s", stored)
	}
	// the model was told about this project and got the idea trimmed
	if !strings.Contains(w.users[0], "120 bpm, so the scene lasts 10 beats") || !strings.Contains(w.users[0], "display = Playfair Display") ||
		!strings.Contains(w.users[0], "accent #ff3b30") || !strings.HasSuffix(strings.TrimSpace(w.users[0]), "the word Hola rising") {
		t.Errorf("what the model was told:\n%s", w.users[0])
	}
	// the file is listed as a source of kind scene and a scene may now name it
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/projects/demo/sources", nil)
	if !strings.Contains(rec.Body.String(), `"kind":"scene"`) {
		t.Errorf("sources: %s", rec.Body)
	}
}

func TestDescriptions_AFileIsReplacedOnlyByADescriptionThatPasses(t *testing.T) {
	bad := strings.Replace(validBeats, `{"beat":10,`, `{"beat":99,`, 1)
	s, dir, w := writerServer(t, bad)
	path := filepath.Join(dir, "sources", "x.scene.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte("OLD"), 0o644)
	code, body := writeDescription(t, s, map[string]any{"name": "x.scene.json", "idea": "a title", "seconds": 5})
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "camera.keys[1].beat") || len(w.users) != blender.MaxAttempts {
		t.Fatalf("three failures: %d %s (asked %d times)", code, body, len(w.users))
	}
	if got, _ := os.ReadFile(path); string(got) != "OLD" {
		t.Fatalf("a failed call leaves the file as it was: %q", got)
	}
	// repaired at the second attempt: the stored file is the repaired one
	w.answers, w.users = []string{bad, validBeats}, nil
	code, body = writeDescription(t, s, map[string]any{"name": "x.scene.json", "idea": "a title", "seconds": 5})
	if code != http.StatusOK || !strings.Contains(body, `"attempts":2`) {
		t.Fatalf("repaired: %d %s", code, body)
	}
	if got, _ := os.ReadFile(path); !strings.Contains(string(got), `"beat": 10`) {
		t.Fatalf("the repaired description replaced the file: %q", got)
	}
}

func TestDescriptions_TheWriterUsesOnlyWhatTheProjectHas(t *testing.T) {
	s, _, w := writerServer(t, validBeats)
	// a project with no tempo: a description in beats is asked for again, and the model is told to use seconds
	noTempo := strings.Replace(describedYAML, "    bpm: 120\n", "", 1)
	if err := s.SaveProjectDefinition("demo", []byte(noTempo), "save"); err != nil {
		t.Fatal(err)
	}
	code, body := writeDescription(t, s, map[string]any{"name": "x.scene.json", "idea": "a title", "seconds": 5})
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "the project has no tempo") {
		t.Fatalf("beats without a tempo: %d %s", code, body)
	}
	if !strings.Contains(w.users[0], "TEMPO: none: key the scene in seconds") {
		t.Errorf("the model is told:\n%s", w.users[0])
	}
}

func TestDescriptions_RefusedBeforeTheModelIsCalled(t *testing.T) {
	s, _, w := writerServer(t, validBeats)
	for name, body := range map[string]map[string]any{
		"parent folder":  {"name": "../x.scene.json", "idea": "a", "seconds": 5},
		"absolute":       {"name": "/tmp/x.scene.json", "idea": "a", "seconds": 5},
		"no suffix":      {"name": "x.json", "idea": "a", "seconds": 5},
		"no idea":        {"name": "x.scene.json", "idea": "  ", "seconds": 5},
		"too long":       {"name": "x.scene.json", "idea": strings.Repeat("a", 2001), "seconds": 5},
		"no seconds":     {"name": "x.scene.json", "idea": "a"},
		"too many":       {"name": "x.scene.json", "idea": "a", "seconds": 601},
		"too few":        {"name": "x.scene.json", "idea": "a", "seconds": 0.1},
		"revise nothing": {"name": "x.scene.json", "idea": "slower", "seconds": 5, "revise": true},
	} {
		code, text := writeDescription(t, s, body)
		want := http.StatusBadRequest
		if name == "revise nothing" {
			want = http.StatusNotFound
		}
		if code != want {
			t.Errorf("%s: %d %s", name, code, text)
		}
	}
	if len(w.users) != 0 {
		t.Fatalf("the model was called %d times for requests that are refused", len(w.users))
	}
	if code, _ := writeDescription(t, s, map[string]any{"name": "x.scene.json", "idea": "a", "seconds": 5, "provider": "nope"}); code == http.StatusOK {
		t.Errorf("an unknown provider is refused by the routing, not answered")
	}
}

func TestDescriptions_ARunInProgressKeepsALocalModelWaiting(t *testing.T) {
	s, _, w := writerServer(t, validBeats)
	s.rm.mu.Lock()
	s.rm.runs["run-busy"] = &run{}
	s.rm.mu.Unlock()
	code, body := writeDescription(t, s, map[string]any{"name": "x.scene.json", "idea": "a title", "seconds": 5})
	if code != http.StatusConflict || !strings.Contains(body, "run-busy") || len(w.users) != 0 {
		t.Fatalf("a run is active: %d %s", code, body)
	}
}

func TestDescriptions_RevisingShowsTheStoredDescriptionToTheModel(t *testing.T) {
	slower := strings.Replace(validBeats, `{"beat":10,`, `{"beat":8,`, 1)
	s, dir, w := writerServer(t, slower)
	os.MkdirAll(filepath.Join(dir, "sources"), 0o755)
	os.WriteFile(filepath.Join(dir, "sources", "x.scene.json"), []byte(validBeats), 0o644)
	code, body := writeDescription(t, s, map[string]any{"name": "x.scene.json", "idea": "shorter camera move", "seconds": 5, "revise": true})
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	u := w.users[0]
	if !strings.Contains(u, "CURRENT DESCRIPTION") || strings.Index(u, `"beat":10`) < 0 || strings.Index(u, `"beat":10`) > strings.Index(u, "shorter camera move") {
		t.Errorf("the model is shown the stored description before the request:\n%s", u)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "sources", "x.scene.json")); !strings.Contains(string(got), `"beat": 8`) {
		t.Errorf("the revision is stored: %s", got)
	}
}

func TestDescriptions_ReadAndSaveByHand(t *testing.T) {
	s, dir, _ := writerServer(t, validBeats)
	get := func(name string) (int, string) {
		rec := doJSON(t, s.Handler(), http.MethodGet, "/api/projects/demo/blender/descriptions/"+name, nil)
		return rec.Code, rec.Body.String()
	}
	put := func(name string, desc string, seconds float64) (int, string) {
		rec := doJSON(t, s.Handler(), http.MethodPut, "/api/projects/demo/blender/descriptions/"+name, map[string]any{"description": json.RawMessage(desc), "seconds": seconds})
		return rec.Code, rec.Body.String()
	}
	if code, _ := get("x.scene.json"); code != http.StatusNotFound {
		t.Fatalf("nothing stored: %d", code)
	}
	if code, body := put("x.scene.json", validBeats, 5); code != http.StatusOK || !strings.Contains(body, `"resolved"`) {
		t.Fatalf("a good description: %d %s", code, body)
	}
	if code, body := get("x.scene.json"); code != http.StatusOK || !strings.Contains(body, `"beat": 10`) && !strings.Contains(body, `"beat":10`) || !strings.Contains(body, `"resolved"`) {
		t.Fatalf("get: %d %s", code, body)
	}
	// a faulty one is refused naming the place in the JSON, and the stored file is as it was
	before, _ := os.ReadFile(filepath.Join(dir, "sources", "x.scene.json"))
	faulty := strings.Replace(validBeats, `{"beat":10,`, `{"t":9,`, 1)
	if code, body := put("x.scene.json", faulty, 5); code != http.StatusBadRequest || !strings.Contains(body, "camera.keys[1].t") {
		t.Fatalf("a faulty description: %d %s", code, body)
	}
	if after, _ := os.ReadFile(filepath.Join(dir, "sources", "x.scene.json")); string(after) != string(before) {
		t.Error("a refused save leaves the file as it was")
	}
	// a still scene is accepted when written by hand (the quality floor is only asked of a model)
	still := `{"version":1,"camera":{"keys":[{"t":0,"location":[0,-6,1]}]}}`
	if code, body := put("still.scene.json", still, 5); code != http.StatusOK {
		t.Errorf("a static scene by hand: %d %s", code, body)
	}
	for _, c := range []struct {
		name string
		body string
	}{{"../x.scene.json", validBeats}, {"x.json", validBeats}} {
		if code, _ := put(c.name, c.body, 5); code == http.StatusOK {
			t.Errorf("name %q accepted", c.name)
		}
	}
	if code, _ := put("x.scene.json", validBeats, 0); code != http.StatusBadRequest {
		t.Errorf("seconds are required: %d", code)
	}
	// a scene of the project that names the stored description validates
	yaml := strings.Replace(describedYAML, "scenes:\n", "scenes:\n  - id: three\n    silence_seconds: 5\n    blender:\n      build: x.scene.json\n", 1)
	if err := s.SaveProjectDefinition("demo", []byte(yaml), "save"); err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/projects/demo/config", nil)
	// (the project's voice does not exist in this test: that is the only thing its validation may still complain about)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "blender.build") {
		t.Errorf("the project accepts the scene that names the stored description: %d %s", rec.Code, rec.Body)
	}
}

// The description of a scene is a file of sources/, so it travels in the package of a project, export and import.
func TestDescriptions_TravelInTheProjectPackage(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "demo")
	yaml := "project: demo\noutput: {mode: video, file: out/m.mp4}\ndefaults: {voice: voices/v.onnx, resolution: 1280x720, sampler: euler, steps: 6, fps: 30}\n" +
		"scenes:\n  - {id: three, silence_seconds: 5, blender: {build: x.scene.json}}\n"
	os.MkdirAll(filepath.Join(projects, "demo", "sources"), 0o755)
	os.WriteFile(filepath.Join(projects, "demo", "sources", "x.scene.json"), []byte(validBeats), 0o644)
	if err := s.SaveProjectDefinition("demo", []byte(yaml), "save"); err != nil {
		t.Fatal(err)
	}
	rec := call(s.Handler(), http.MethodGet, "/api/projects/demo/export?format=zip&lang=en", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d", rec.Code)
	}
	if conf := post(s.Handler(), "/api/import/package/confirm?id=copy", rec.Body.Bytes()); conf.Code != http.StatusCreated {
		t.Fatalf("confirm: %d %s", conf.Code, conf.Body)
	}
	if b, err := os.ReadFile(filepath.Join(projects, "copy", "sources", "x.scene.json")); err != nil || string(b) != validBeats {
		t.Errorf("the description is in the copy: %v %q", err, b)
	}
}

func TestDescriptions_APreviewIsOneSmallFrameOnTheCPU(t *testing.T) {
	s, dir, _ := writerServer(t, validBeats)
	os.MkdirAll(filepath.Join(dir, "sources"), 0o755)
	os.WriteFile(filepath.Join(dir, "sources", "x.scene.json"), []byte(validBeats), 0o644)
	s.Overrides.BlenderInfo = func(context.Context) (blender.Info, error) {
		return blender.Info{Path: "/fake/blender", Version: "5.3.0"}, nil
	}
	var got blender.PreviewOptions
	var bin string
	s.Overrides.BlenderPreview = func(_ context.Context, b string, spec *blender.Spec, o blender.PreviewOptions) ([]byte, error) {
		bin, got = b, o
		if spec.Camera.Keys[1].T == nil || *spec.Camera.Keys[1].T != 5 {
			t.Errorf("the preview gets the description resolved: %+v", spec.Camera.Keys[1])
		}
		return []byte("\x89PNG fake"), nil
	}
	preview := func(body map[string]any) *httptest.ResponseRecorder {
		return doJSON(t, s.Handler(), http.MethodPost, "/api/projects/demo/blender/descriptions/x.scene.json/preview", body)
	}
	rec := preview(map[string]any{"t": 1.5, "seconds": 5})
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || !strings.HasPrefix(rec.Body.String(), "\x89PNG") {
		t.Fatalf("%d %q %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	if bin != "/fake/blender" || got.T != 1.5 || got.Seconds != 5 || got.FPS != 30 || got.AspectW != 1280 || got.AspectH != 720 || got.Width != 0 ||
		got.Sources != filepath.Join(dir, "sources") || got.Fonts["display"] == "" {
		t.Errorf("what the preview was asked for: %q %+v", bin, got)
	}
	// the design's display font is a TrueType file Blender can read, decoded from the pack
	if head, err := os.ReadFile(got.Fonts["display"]); err != nil || len(head) < 4 || string(head[:4]) != "\x00\x01\x00\x00" {
		t.Errorf("the display font: %v", err)
	}
	for name, body := range map[string]map[string]any{
		"t after the end":  {"t": 5.5, "seconds": 5},
		"a negative t":     {"t": -1, "seconds": 5},
		"no seconds":       {"t": 1},
		"a width too big":  {"t": 1, "seconds": 5, "width": 2000},
		"a negative width": {"t": 1, "seconds": 5, "width": -5},
	} {
		if rec := preview(body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	// a description that breaks the contract for this length is refused before Blender is asked
	os.WriteFile(filepath.Join(dir, "sources", "x.scene.json"), []byte(strings.Replace(validBeats, `{"beat":10,`, `{"beat":99,`, 1)), 0o644)
	if rec := preview(map[string]any{"t": 1, "seconds": 5}); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "camera.keys[1].beat") {
		t.Errorf("an invalid description: %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/demo/blender/descriptions/gone.scene.json/preview", map[string]any{"t": 1, "seconds": 5}); rec.Code != http.StatusNotFound {
		t.Errorf("no such description: %d", rec.Code)
	}
}

func TestDescriptions_ThePreviewNeedsBlenderAndSaysHowToGetIt(t *testing.T) {
	s, dir, w := writerServer(t, validBeats)
	os.MkdirAll(filepath.Join(dir, "sources"), 0o755)
	os.WriteFile(filepath.Join(dir, "sources", "x.scene.json"), []byte(validBeats), 0o644)
	s.Overrides.BlenderInfo = func(context.Context) (blender.Info, error) {
		return blender.Info{}, fmt.Errorf("%w: %s", blender.ErrNotInstalled, blender.InstallHint)
	}
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/demo/blender/descriptions/x.scene.json/preview", map[string]any{"t": 1, "seconds": 5})
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "pacman -S blender") {
		t.Fatalf("no Blender: %d %s", rec.Code, rec.Body)
	}
	// the writer does not need it
	if code, body := writeDescription(t, s, map[string]any{"name": "y.scene.json", "idea": "a title", "seconds": 5}); code != http.StatusOK {
		t.Fatalf("writing without Blender: %d %s (asked %d)", code, body, len(w.users))
	}
}

func TestDescribeSceneFor_TheEnvironmentComesFromTheProjectOrFromTheDefaults(t *testing.T) {
	s, _, _ := writerServer(t, validBeats)
	var got blender.WriteRequest
	s.Overrides.BlenderWrite = func(_ context.Context, _ blender.Writer, req blender.WriteRequest) (blender.WriteResult, error) {
		got = req
		return blender.WriteResult{Raw: []byte("{}"), Attempts: 1}, nil
	}
	// with no project: no tempo, no design, a 16:9 frame at 30 fps
	if _, err := s.DescribeSceneFor(context.Background(), nil, "", "a ball", 4); err != nil {
		t.Fatal(err)
	}
	if got.Env.BPM != 0 || got.Width != 1280 || got.Height != 720 || got.FPS != 30 || got.Idea != "a ball" || got.Seconds != 4 || len(got.Families) != 0 {
		t.Errorf("defaults: %+v", got)
	}
	// with the project of the tests: its tempo and its frame
	if _, err := s.DescribeSceneFor(context.Background(), nil, "demo", "a ball", 4); err != nil {
		t.Fatal(err)
	}
	if got.Env.BPM != 120 || got.Width != 1280 || got.FPS != 30 || got.Families["display"] == "" {
		t.Errorf("project: %+v", got)
	}
	if _, err := s.DescribeSceneFor(context.Background(), nil, "nope", "a ball", 4); !IsNoProject(err) {
		t.Errorf("an unknown project must be ErrNoProject: %v", err)
	}
}

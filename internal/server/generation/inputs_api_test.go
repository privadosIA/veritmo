package generation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"veritmo/internal/project/config"
	"veritmo/internal/video/videoedit"
)

// inputsCatalog has the three bundles: first/last frame, references and LTX (and the rest).
func inputsCatalog() []catalogEntry {
	return append(baseCatalog()[:5:5], tH3Ref(), tH3FF(), tLTX(), tAD(), tKontext(), tQwen())
}

// A file's content stands for what ffprobe would say: "bad" is not media, "silent" has no sound, "dur=N" lasts N s.
func stubProbes(t *testing.T, s *testServer) {
	t.Helper()
	read := func(path string) string { b, _ := os.ReadFile(path); return string(b) }
	secs := func(c string) float64 {
		if i := strings.Index(c, "dur="); i >= 0 {
			v, _ := strconv.ParseFloat(strings.TrimSpace(c[i+4:]), 64)
			return v
		}
		return 2
	}
	s.Stack.VideoEdit.Probe = func(_ context.Context, path string) (videoedit.Info, error) {
		c := read(path)
		if strings.HasPrefix(c, "bad") {
			return videoedit.Info{}, fmt.Errorf("no video stream")
		}
		return videoedit.Info{Width: 832, Height: 480, Duration: secs(c)}, nil
	}
	s.gHandler.probeAudio = func(_ context.Context, path string) (float64, bool, error) {
		c := read(path)
		if strings.HasPrefix(c, "bad") {
			return 0, false, fmt.Errorf("unreadable")
		}
		return secs(c), !strings.HasPrefix(c, "silent"), nil
	}
}

type testPart struct{ field, file, body string }

func postParts(t *testing.T, s *testServer, req any, parts ...testPart) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	raw, _ := json.Marshal(req)
	_ = mw.WriteField("request", string(raw))
	for _, p := range parts {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, p.field, p.file))
		w, _ := mw.CreatePart(h)
		_, _ = w.Write([]byte(p.body))
	}
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/api/generations", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	return rec
}

func countGenerations(t *testing.T, s *testServer) int {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/generations", nil)
	return strings.Count(rec.Body.String(), `"id":"api-gen-`)
}

func TestGenerationInputs_ALastPictureOnH3AndLTX(t *testing.T) {
	s := genServer(t, inputsCatalog())
	stubProbes(t, s)
	for _, option := range []string{"h3-full", "ltx"} {
		rec := postParts(t, s, map[string]any{"option": option},
			testPart{"image", "a.png", "first"}, testPart{"end_image", "z.JPG", "last"})
		if rec.Code != http.StatusAccepted {
			t.Fatalf("%s: status %d: %s", option, rec.Code, rec.Body)
		}
		g := decodeGeneration(t, rec)
		for name, want := range map[string]string{"inicio.png": "first", "final.jpg": "last"} {
			if data, err := os.ReadFile(filepath.Join(s.Workspace.ProjectDir(g.ID), "sources", name)); err != nil || string(data) != want {
				t.Errorf("%s: %s = %q (%v)", option, name, data, err)
			}
		}
		cfg, err := config.Load(s.Workspace.ProjectConfigPath(g.ID))
		if err != nil || cfg.Scenes[0].Image != "inicio.png" || cfg.Scenes[0].EndImage != "final.jpg" || cfg.Scenes[0].References != nil {
			t.Errorf("%s: scene = %+v (%v)", option, cfg.Scenes[0], err)
		}
		if err := cfg.ValidateInDir(s.Workspace.ProjectDir(g.ID)); err != nil {
			t.Errorf("%s: the scene does not validate: %v", option, err)
		}
		info, _ := s.Workspace.ReadManaged(g.ID)
		if len(info.Inputs) != 1 || info.Inputs[0] != (managedInput{Kind: inputEndImage, Name: "final.jpg"}) {
			t.Errorf("%s: recorded inputs = %+v", option, info.Inputs)
		}
		waitGeneration(t, s, g.ID, GenDone)
	}
}

func TestGenerationInputs_ReferencesOnH3Ref(t *testing.T) {
	s := genServer(t, inputsCatalog())
	stubProbes(t, s)
	rec := postParts(t, s, map[string]any{"option": "h3-ref", "prompt": "they dance"},
		testPart{"reference_images", "one.png", "p1"}, testPart{"reference_images", "two.webp", "p2"},
		testPart{"reference_videos", "move.mp4", "dur=2"}, testPart{"reference_audios", "voice.m4a", "dur=3"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	g := decodeGeneration(t, rec)
	for name, want := range map[string]string{"ref-imagen-1.png": "p1", "ref-imagen-2.webp": "p2", "ref-video-1.mp4": "dur=2", "ref-audio-1.m4a": "dur=3"} {
		if data, err := os.ReadFile(filepath.Join(s.Workspace.ProjectDir(g.ID), "sources", name)); err != nil || string(data) != want {
			t.Errorf("%s = %q (%v)", name, data, err)
		}
	}
	cfg, err := config.Load(s.Workspace.ProjectConfigPath(g.ID))
	sc := cfg.Scenes[0]
	if err != nil || sc.Image != "ref-imagen-1.png" || sc.EndImage != "" || sc.References == nil ||
		strings.Join(sc.References.Images, ",") != "ref-imagen-2.webp" || sc.References.Videos[0] != "ref-video-1.mp4" || sc.References.Audios[0] != "ref-audio-1.m4a" {
		t.Errorf("scene = %+v refs=%+v (%v)", sc, sc.References, err)
	}
	if err := cfg.ValidateInDir(s.Workspace.ProjectDir(g.ID)); err != nil {
		t.Errorf("the scene does not validate: %v", err)
	}
	if cfg.Defaults.Animate.Resolution != referenceResolution || cfg.Defaults.Animate.FirstFrame {
		t.Errorf("animate = %+v", cfg.Defaults.Animate)
	}
	info, _ := s.Workspace.ReadManaged(g.ID)
	if len(info.Inputs) != 4 || info.Inputs[0].Kind != inputRefImage || info.Inputs[3].Kind != inputRefAudio {
		t.Errorf("recorded inputs = %+v", info.Inputs)
	}
	waitGeneration(t, s, g.ID, GenDone)
}

func TestGenerationInputs_EveryRefusalIsA422AndCreatesNothing(t *testing.T) {
	s := genServer(t, inputsCatalog())
	stubProbes(t, s)
	img := func(f, n string) testPart { return testPart{f, n, "px"} }
	cases := map[string]struct {
		req   map[string]any
		parts []testPart
		want  string
	}{
		"last picture alone":     {map[string]any{"option": "h3-full"}, []testPart{img("end_image", "e.png")}, "needs the start picture"},
		"last picture on wan":    {map[string]any{"option": "wan-medium"}, []testPart{img("image", "a.png"), img("end_image", "e.png")}, "cannot end on a picture"},
		"last picture on refs":   {map[string]any{"option": "h3-ref"}, []testPart{img("image", "a.png"), img("end_image", "e.png")}, "cannot end on a picture"},
		"two last pictures":      {map[string]any{"option": "h3-full"}, []testPart{img("image", "a.png"), img("end_image", "e.png"), img("end_image", "f.png")}, "takes one picture"},
		"references on ltx":      {map[string]any{"option": "ltx"}, []testPart{img("reference_images", "r.png")}, "takes no references"},
		"references on h3-full":  {map[string]any{"option": "h3-full"}, []testPart{img("reference_images", "r.png")}, "takes no references"},
		"references with image":  {map[string]any{"option": "h3-ref"}, []testPart{img("image", "a.png"), img("reference_images", "r.png")}, "cannot be combined"},
		"too many pictures":      {map[string]any{"option": "h3-ref"}, manyImages(7), "at most 6"},
		"too many videos":        {map[string]any{"option": "h3-ref"}, []testPart{img("reference_images", "r.png"), {"reference_videos", "a.mp4", "dur=1"}, {"reference_videos", "b.mp4", "dur=1"}, {"reference_videos", "c.mp4", "dur=1"}}, "reference_videos: 3 sent"},
		"too many audios":        {map[string]any{"option": "h3-ref"}, []testPart{img("reference_images", "r.png"), {"reference_audios", "a.wav", "dur=1"}, {"reference_audios", "b.wav", "dur=1"}, {"reference_audios", "c.wav", "dur=1"}, {"reference_audios", "d.wav", "dur=1"}}, "reference_audios: 4 sent"},
		"video too long":         {map[string]any{"option": "h3-ref"}, []testPart{img("reference_images", "r.png"), {"reference_videos", "a.mp4", "dur=3.5"}}, "lasts 3.5 s"},
		"audio too long":         {map[string]any{"option": "h3-ref"}, []testPart{img("reference_images", "r.png"), {"reference_audios", "a.wav", "dur=4.5"}}, "lasts 4.5 s"},
		"not a video":            {map[string]any{"option": "h3-ref"}, []testPart{img("reference_images", "r.png"), {"reference_videos", "a.mp4", "bad"}}, "is not a video"},
		"audio without sound":    {map[string]any{"option": "h3-ref"}, []testPart{img("reference_images", "r.png"), {"reference_audios", "a.wav", "silent"}}, "has no sound"},
		"unreadable picture":     {map[string]any{"option": "h3-ref"}, []testPart{{"reference_images", "r.png", "bad"}}, "cannot be read as a picture"},
		"wrong extension":        {map[string]any{"option": "h3-ref"}, []testPart{img("reference_images", "r.gif")}, "must be a .png"},
		"video named as audio":   {map[string]any{"option": "h3-ref"}, []testPart{img("reference_images", "r.png"), {"reference_audios", "a.mp4", "x"}}, "reference_audios must be an .mp3"},
		"references, no picture": {map[string]any{"option": "h3-ref"}, []testPart{{"reference_videos", "a.mp4", "dur=1"}}, "at least one reference picture"},
	}
	for name, c := range cases {
		rec := postParts(t, s, c.req, c.parts...)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%s: status %d, want 422 with %q: %s", name, rec.Code, c.want, rec.Body)
		}
	}
	if n := countGenerations(t, s); n != 0 {
		t.Errorf("%d generations were created by refused requests", n)
	}
}

func manyImages(n int) []testPart {
	var out []testPart
	for i := 0; i < n; i++ {
		out = append(out, testPart{"reference_images", fmt.Sprintf("r%d.png", i), "px"})
	}
	return out
}

func TestGenerationInputs_TheRefusalSaysWhichOptionsDo(t *testing.T) {
	s := genServer(t, inputsCatalog())
	stubProbes(t, s)
	rec := postParts(t, s, map[string]any{"option": "wan-medium"}, testPart{"image", "a.png", "x"}, testPart{"end_image", "e.png", "x"})
	if !strings.Contains(rec.Body.String(), "h3-full") || !strings.Contains(rec.Body.String(), "ltx") || strings.Contains(rec.Body.String(), "wan") && strings.Contains(rec.Body.String(), "that can: wan") {
		t.Errorf("%s", rec.Body)
	}
	rec = postParts(t, s, map[string]any{"option": "ltx"}, testPart{"reference_images", "r.png", "x"})
	if !strings.Contains(rec.Body.String(), "h3-ref") {
		t.Errorf("%s", rec.Body)
	}
}

func TestGenerationInputs_AReferencesOptionOnASmallCardIsUnavailable(t *testing.T) {
	s := genServer(t, inputsCatalog())
	stubProbes(t, s)
	s.GenMachine = func() planMachine { return machine(8192, 32) }
	rec := postParts(t, s, map[string]any{"option": "h3-ref"}, testPart{"reference_images", "r.png", "x"})
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "11 GB") {
		t.Errorf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestGenerationInputs_ARequestWithoutThemIsAsBefore(t *testing.T) {
	s := genServer(t, inputsCatalog())
	rec := postGeneration(t, s, map[string]any{"prompt": "a dog", "option": "h3-full"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	g := decodeGeneration(t, rec)
	cfg, _ := config.Load(s.Workspace.ProjectConfigPath(g.ID))
	if cfg.Scenes[0].EndImage != "" || cfg.Scenes[0].References != nil {
		t.Errorf("scene = %+v", cfg.Scenes[0])
	}
	if info, _ := s.Workspace.ReadManaged(g.ID); len(info.Inputs) != 0 {
		t.Errorf("inputs = %+v", info.Inputs)
	}
}

func getBody(t *testing.T, s *testServer, path string) (int, string) {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodGet, path, nil)
	return rec.Code, rec.Body.String()
}

func TestGenerationInputs_AGenerationReportsItsInputsAndTheyAreServed(t *testing.T) {
	s := genServer(t, inputsCatalog())
	stubProbes(t, s)
	g := decodeGeneration(t, postParts(t, s, map[string]any{"option": "h3-full"}, testPart{"image", "a.png", "first"}, testPart{"end_image", "z.jpg", "last"}))
	want := []generationInput{
		{inputStartImage, "inicio.png", "/api/projects/" + g.ID + "/artifacts/sources/inicio.png"},
		{inputEndImage, "final.jpg", "/api/projects/" + g.ID + "/artifacts/sources/final.jpg"},
	}
	if fmt.Sprint(g.Inputs) != fmt.Sprint(want) {
		t.Errorf("inputs = %+v, want %+v", g.Inputs, want)
	}
	if code, body := getBody(t, s, want[1].Address); code != http.StatusOK || body != "last" {
		t.Errorf("the address serves %d %q", code, body)
	}
	read := decodeGeneration(t, doJSON(t, s.Handler(), http.MethodGet, "/api/generations/"+g.ID, nil))
	if fmt.Sprint(read.Inputs) != fmt.Sprint(want) {
		t.Errorf("read: %+v", read.Inputs)
	}
	if _, list := getBody(t, s, "/api/generations"); !strings.Contains(list, `"kind":"end_image"`) {
		t.Errorf("the list does not carry the inputs: %s", list)
	}

	refs := decodeGeneration(t, postParts(t, s, map[string]any{"option": "h3-ref"},
		testPart{"reference_images", "a.png", "1"}, testPart{"reference_audios", "v.wav", "dur=1"}))
	if len(refs.Inputs) != 2 || refs.Inputs[0].Kind != inputRefImage || refs.Inputs[1].Kind != inputRefAudio {
		t.Errorf("references: %+v", refs.Inputs)
	}
	// Nothing sent: none, and an old generation (no inputs recorded) shows its start picture alone.
	plain := decodeGeneration(t, postGeneration(t, s, map[string]any{"prompt": "a dog"}))
	if plain.Inputs == nil || len(plain.Inputs) != 0 {
		t.Errorf("a generation with none reports an empty list: %#v", plain.Inputs)
	}
	old := decodeGeneration(t, postParts(t, s, map[string]any{"option": "ltx"}, testPart{"image", "a.png", "x"}))
	info, _ := s.Workspace.ReadManaged(old.ID)
	info.Inputs = nil
	if got := s.inputsReported(info); len(got) != 1 || got[0].Kind != inputStartImage {
		t.Errorf("old generation: %+v", got)
	}
}

func TestGenerationInputs_ReuseTheInputsOfAnother(t *testing.T) {
	s := genServer(t, inputsCatalog())
	stubProbes(t, s)
	first := decodeGeneration(t, postParts(t, s, map[string]any{"option": "h3-full"}, testPart{"image", "a.png", "first"}, testPart{"end_image", "z.jpg", "last"}))
	rec := postGeneration(t, s, map[string]any{"option": "ltx", "reuse_inputs_of": first.ID, "prompt": "again"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	g := decodeGeneration(t, rec)
	if len(g.Inputs) != 2 || g.Inputs[0].Kind != inputStartImage || g.Inputs[1].Kind != inputEndImage {
		t.Errorf("inputs = %+v", g.Inputs)
	}
	for name, want := range map[string]string{"inicio.png": "first", "final.jpg": "last"} {
		if data, err := os.ReadFile(filepath.Join(s.Workspace.ProjectDir(g.ID), "sources", name)); err != nil || string(data) != want {
			t.Errorf("%s = %q (%v)", name, data, err)
		}
	}
	// the references of a generation can be reused too, and removing the first one leaves the copy
	refs := decodeGeneration(t, postParts(t, s, map[string]any{"option": "h3-ref"}, testPart{"reference_images", "a.png", "1"}, testPart{"reference_videos", "m.mp4", "dur=1"}))
	again := decodeGeneration(t, postGeneration(t, s, map[string]any{"option": "h3-ref-draft", "reuse_inputs_of": refs.ID}))
	if len(again.Inputs) != 2 {
		t.Errorf("reused references: %+v", again.Inputs)
	}
	waitGeneration(t, s, first.ID, GenDone)
	waitGeneration(t, s, g.ID, GenDone)
	if rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/generations/"+first.ID, nil); rec.Code != http.StatusNoContent && rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(s.Workspace.ProjectDir(first.ID)); !os.IsNotExist(err) {
		t.Errorf("removing a generation removes its inputs: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Workspace.ProjectDir(g.ID), "sources", "final.jpg")); err != nil {
		t.Errorf("the copy must stay: %v", err)
	}
}

func TestGenerationInputs_ReuseRefusals(t *testing.T) {
	s := genServer(t, inputsCatalog())
	stubProbes(t, s)
	first := decodeGeneration(t, postParts(t, s, map[string]any{"option": "h3-full"}, testPart{"image", "a.png", "first"}, testPart{"end_image", "z.jpg", "last"}))
	bare := decodeGeneration(t, postGeneration(t, s, map[string]any{"prompt": "a dog"}))
	before := countGenerations(t, s)
	for name, c := range map[string]struct {
		req   map[string]any
		parts []testPart
		want  string
	}{
		"with parts":          {map[string]any{"option": "ltx", "reuse_inputs_of": first.ID}, []testPart{{"image", "b.png", "x"}}, "send the inputs or reuse them"},
		"with a last picture": {map[string]any{"option": "ltx", "reuse_inputs_of": first.ID}, []testPart{{"end_image", "b.png", "x"}}, "send the inputs or reuse them"},
		"unknown":             {map[string]any{"option": "ltx", "reuse_inputs_of": "api-gen-nope"}, nil, "does not exist"},
		"not a generation":    {map[string]any{"option": "ltx", "reuse_inputs_of": "demo"}, nil, "not the identifier of a generation"},
		"kept none":           {map[string]any{"option": "ltx", "reuse_inputs_of": bare.ID}, nil, "kept no inputs"},
		"option that cannot":  {map[string]any{"option": "wan-medium", "reuse_inputs_of": first.ID}, nil, "cannot end on a picture"},
	} {
		rec := postParts(t, s, c.req, c.parts...)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%s: status %d, want 422 with %q: %s", name, rec.Code, c.want, rec.Body)
		}
	}
	if n := countGenerations(t, s); n != before {
		t.Errorf("%d generations after the refusals, %d before", n, before)
	}
}

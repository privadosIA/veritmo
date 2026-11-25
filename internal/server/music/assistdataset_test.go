package music

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/music/musicmotion"
	"veritmo/internal/video/blender"
	"veritmo/internal/video/motion"
)

func datasetSeam(s *testServer, entries []assist.DatasetEntry, err error) *assist.DatasetInput {
	var seen assist.DatasetInput
	s.Assistant.AssistDatasetRun = func(ctx context.Context, in assist.DatasetInput, model string) ([]assist.DatasetEntry, error) {
		seen = in
		return entries, err
	}
	return &seen
}

func postDataset(t *testing.T, s *testServer, body map[string]any) (int, DatasetAnswer, string) {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist/dataset", body)
	var a DatasetAnswer
	_ = json.Unmarshal(rec.Body.Bytes(), &a)
	return rec.Code, a, rec.Body.String()
}

func TestAssistDataset_VideoScenesGetTheSharedLines(t *testing.T) {
	s := mmServer(t)
	datasetSeam(s, []assist.DatasetEntry{
		{ID: "S01", Title: "The hive", Body: "Dialogue: Al amanecer\nMotion: slow push-in\nNegative: whatever\na wooden beehive in a meadow."},
		{ID: "s01", Title: "Honey", Body: "**Picture:** a jar of honey"},
		{ID: "s03", Title: "No picture", Body: "Motion: pan"},
	}, nil)
	code, a, body := postDataset(t, s, map[string]any{"mode": "video", "text": "bees", "negative": "text, watermark", "look": "documentary", "images": map[string]int{"variants": 2}})
	if code != http.StatusOK || a.Mode != "video" || len(a.Entries) != 2 {
		t.Fatalf("%d %s", code, body)
	}
	e := a.Entries[0]
	for _, want := range []string{"Dialogue: \"Al amanecer\"", "Motion: slow push-in", "a wooden beehive in a meadow., documentary", "Negative: text, watermark", "Variants: 2"} {
		if !strings.Contains(e[3], want) && !strings.Contains(strings.ReplaceAll(e[3], ".,", ","), strings.ReplaceAll(want, ".,", ",")) {
			t.Errorf("entry lacks %q:\n%s", want, e[3])
		}
	}
	if strings.Contains(a.Entries[1][3], "Picture") || !strings.HasPrefix(a.Entries[1][3], "a jar of honey") {
		t.Errorf("the label before the picture is not part of the prompt: %q", a.Entries[1][3])
	}
	if strings.Contains(e[3], "whatever") || e[0] != "s01" || a.Entries[1][0] == "s01" {
		t.Errorf("the request's negative replaces the model's and ids are unique: %v", a.Entries)
	}
	if len(a.Warnings) != 2 {
		t.Errorf("a repeated id and a scene with no picture each warn: %v", a.Warnings)
	}
}

func TestAssistDataset_MotionPartsAreCheckedAgainstTheCatalog(t *testing.T) {
	s := mmServer(t)
	existing := musicmotion.NewCatalog(nil).List()[0].Name
	datasetSeam(s, []assist.DatasetEntry{
		{ID: "g1", Title: "A part", Body: "Part: " + existing + "\nDuration: 4"},
		{ID: "g2", Title: "Unknown", Body: "Part: no-such-part\nDuration: 4"},
		{ID: "g3", Title: "Free", Body: "Graphic: a red bar chart growing\nDuration: 5"},
	}, nil)
	code, a, body := postDataset(t, s, map[string]any{"mode": "motion", "text": "three graphics"})
	if code != http.StatusOK || len(a.Entries) != 2 || a.Entries[0][0] != "g1" || a.Entries[1][0] != "g3" {
		t.Fatalf("%d %s", code, body)
	}
	if len(a.Warnings) != 1 || !strings.Contains(a.Warnings[0], "g2") {
		t.Fatalf("%v", a.Warnings)
	}
}

func TestAssistDataset_MusicEntriesAreTracks(t *testing.T) {
	s := mmServer(t)
	datasetSeam(s, []assist.DatasetEntry{{ID: "t1", Title: "Night", Body: "synthwave, driving\nInstrumental: yes"}, {ID: "t2", Title: "Sun", Body: "folk ballad"}}, nil)
	planSeams(s, assist.PlanGuess{}, nil, nil)
	code, a, body := postDataset(t, s, map[string]any{"mode": "music", "text": "two songs", "generator": "heartmula"})
	if code != http.StatusOK || len(a.Entries) != 2 {
		t.Fatalf("%d %s", code, body)
	}
	if !strings.Contains(a.Entries[0][3], "Lyrics: [Instrumental]") || !strings.Contains(a.Entries[1][3], "Lyrics:\n[verse]") || !strings.Contains(a.Entries[1][3], "Tags: synthwave,neon") {
		t.Fatalf("%v", a.Entries)
	}
}

func TestAssistDataset_RefusalsAndBadRequests(t *testing.T) {
	s := mmServer(t)
	datasetSeam(s, nil, assist.ErrRefused)
	if code, _, body := postDataset(t, s, map[string]any{"mode": "video", "text": "x"}); code != http.StatusUnprocessableEntity {
		t.Fatalf("%d %s", code, body)
	}
	datasetSeam(s, []assist.DatasetEntry{{ID: "a", Body: "Motion: pan"}}, nil)
	if code, _, body := postDataset(t, s, map[string]any{"mode": "video", "text": "x"}); code != http.StatusUnprocessableEntity {
		t.Fatalf("nothing usable is 422: %d %s", code, body)
	}
	six := []map[string]string{{"prompt": "1"}, {"prompt": "2"}, {"prompt": "3"}, {"prompt": "4"}, {"prompt": "5"}, {"prompt": "6"}}
	for _, body := range []map[string]any{
		{"mode": "audio", "text": "x"}, {"text": "x"}, {"mode": "video", "text": " "}, {"mode": "video", "text": "x", "language": "español"},
		{"mode": "video", "text": "x", "provider": "nope"}, {"mode": "video", "text": "x", "images": map[string]int{"variants": 0}},
		{"mode": "video", "text": "x", "examples": six},
	} {
		if code, _, b := postDataset(t, s, body); code != http.StatusBadRequest {
			t.Fatalf("%v: %d %s", body, code, b)
		}
	}
}

func TestAssistDataset_TheCatalogSaysItExists(t *testing.T) {
	has := map[string]bool{}
	for _, f := range musicmotion.Features {
		has[f] = true
	}
	if !has["dataset"] || !has["blender"] || !has["examples"] {
		t.Fatal(musicmotion.Features)
	}
}

func TestAssistPlan_SecondsGivenAreUsedAndNotWarnedAbout(t *testing.T) {
	s := mmServer(t)
	var seen assist.PlanInput
	planSeams(s, assist.PlanGuess{Title: "x", Style: "illustrated", Level: "medium", FPS: 30, Mode: "dark", Concept: "c", World: "a city", Device: "a number lands", Hook: "a door opens"}, nil, nil)
	inner := s.Assistant.AssistPlanRun
	s.Assistant.AssistPlanRun = func(ctx context.Context, in assist.PlanInput, model string) (assist.PlanGuess, error) {
		seen = in
		return inner(ctx, in, model)
	}
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist/plan", map[string]any{"text": "algo", "seconds": 90})
	var a PlanAnswer
	_ = json.Unmarshal(rec.Body.Bytes(), &a)
	if rec.Code != http.StatusOK || a.Song.Seconds != 90 || seen.Seconds != 90 || len(a.Warnings) != 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for _, v := range []int{-1, 601} {
		if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist/plan", map[string]any{"text": "algo", "seconds": v}); rec.Code != http.StatusBadRequest {
			t.Fatalf("%d: %d", v, rec.Code)
		}
	}
	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/assist/plan", map[string]any{"text": "algo", "examples": []map[string]string{{"prompt": ""}}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// a description in seconds and explicit colours, which passes the checks with no design and no tempo
const datasetScene3D = `{"version":1,"camera":{"keys":[{"t":0,"location":[0,-9,1.2],"target":[0,0,0.6]},{"t":4,"location":[0,-7,1],"target":[0,0,0.6]}]},` +
	`"materials":{"m":{"color":"#ff3b30"}},"objects":[{"id":"title","type":"text","text":"Hola","size":1,"material":"m","keys":[{"t":0,"scale":1},{"t":2,"scale":1.2,"ease":"back"}]}]}`

// sceneWriterSeam makes the model of the 3D descriptions answer datasetScene3D, except for the ideas that say FAIL; it keeps what
// it was asked.
func sceneWriterSeam(s *testServer) *[]blender.WriteRequest {
	var asked []blender.WriteRequest
	s.Stack.Workspace.Overrides.MotionWriter = func(context.Context) (motion.Writer, motion.ModelInfo, error) {
		return nil, motion.ModelInfo{Provider: "Ollama", Model: "writer:7b"}, nil
	}
	s.Stack.Workspace.Overrides.BlenderWrite = func(_ context.Context, _ blender.Writer, r blender.WriteRequest) (blender.WriteResult, error) {
		asked = append(asked, r)
		if strings.Contains(r.Idea, "FAIL") {
			return blender.WriteResult{}, fmt.Errorf("%w: nothing moves", blender.ErrRejected)
		}
		return blender.WriteResult{Raw: []byte(datasetScene3D), Attempts: 1}, nil
	}
	return &asked
}

func TestAssistDataset_Blender3DScenesGetTheirDescriptions(t *testing.T) {
	s := mmServer(t)
	asked := sceneWriterSeam(s)
	seen := datasetSeam(s, []assist.DatasetEntry{
		{ID: "s01", Title: "Hola", Body: "Dialogue: Hola a todos\nMotion: slow push-in\nNegative: whatever\nBlender: mine.scene.json\na red text HOLA rising from the floor"},
		{ID: "s02", Title: "Ball", Body: "Silence: 8\na blue ball bouncing"},
		{ID: "s03", Title: "Bad", Body: "FAIL a thing that never moves"},
		{ID: "s04", Title: "No picture", Body: "Dialogue: solo"},
	}, nil)
	code, a, body := postDataset(t, s, map[string]any{"mode": "video", "text": "3d intro", "picture": "blender", "negative": "x", "images": map[string]int{"variants": 3}})
	if code != http.StatusOK || len(a.Entries) != 2 {
		t.Fatalf("%d %s", code, body)
	}
	if seen.Picture != "blender" {
		t.Errorf("the dataset task must be told the pictures are 3D: %+v", seen)
	}
	e := a.Entries[0]
	if !strings.Contains(e[3], "Blender: s01.scene.json") || strings.Contains(e[3], "mine.scene.json") || strings.Contains(e[3], "Motion:") || strings.Contains(e[3], "Negative:") || strings.Contains(e[3], "Variants:") {
		t.Errorf("a 3D scene has the server's Blender line and no Motion, Negative or Variants:\n%s", e[3])
	}
	if len(a.Descriptions) != 2 || a.Descriptions["s01.scene.json"] == nil || a.Descriptions["s02.scene.json"] == nil || a.Descriptions["s03.scene.json"] != nil {
		t.Errorf("descriptions: %v", a.Descriptions)
	}
	if len(*asked) != 3 || (*asked)[0].Idea != "a red text HOLA rising from the floor" || (*asked)[1].Seconds != 8 || (*asked)[0].Seconds != 3 {
		t.Errorf("the ideas are the picture lines and the seconds come from the entry: %+v", *asked)
	}
	joined := strings.Join(a.Warnings, "|")
	if !strings.Contains(joined, "s03") || !strings.Contains(joined, "nothing moves") || !strings.Contains(joined, "scene 4") {
		t.Errorf("the scenes left out warn: %v", a.Warnings)
	}
	// what comes back previews with the import's own mapping, with the descriptions beside
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/import/preview", map[string]any{"content": string(mustJSON(t, a.Entries)), "descriptions": a.Descriptions})
	var prev struct {
		Imported int
		Warnings []string
		YAML     string
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &prev)
	if rec.Code != http.StatusOK || prev.Imported != 2 || len(prev.Warnings) != 0 || !strings.Contains(prev.YAML, "build: s01.scene.json") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestAssistDataset_Blender3DScenesAreLimited(t *testing.T) {
	s := mmServer(t)
	sceneWriterSeam(s)
	var entries []assist.DatasetEntry
	for i := 1; i <= 8; i++ {
		entries = append(entries, assist.DatasetEntry{ID: fmt.Sprintf("s%02d", i), Body: "a cube"})
	}
	datasetSeam(s, entries, nil)
	code, a, body := postDataset(t, s, map[string]any{"mode": "video", "text": "many", "picture": "blender"})
	if code != http.StatusOK || len(a.Entries) != 6 || len(a.Descriptions) != 6 || len(a.Warnings) != 1 || !strings.Contains(a.Warnings[0], "first 6") {
		t.Fatalf("%d %s", code, body)
	}
}

func TestAssistDataset_Blender3DNothingUsableIs422AndBadRequestsAre400(t *testing.T) {
	s := mmServer(t)
	sceneWriterSeam(s)
	datasetSeam(s, []assist.DatasetEntry{{ID: "a", Body: "FAIL x"}}, nil)
	if code, _, body := postDataset(t, s, map[string]any{"mode": "video", "text": "x", "picture": "blender"}); code != http.StatusUnprocessableEntity {
		t.Fatalf("nothing usable is 422: %d %s", code, body)
	}
	for _, body := range []map[string]any{
		{"mode": "video", "text": "x", "picture": "photo"},
		{"mode": "music", "text": "x", "picture": "blender"},
		{"mode": "video", "text": "x", "project": "demo"},
		{"mode": "video", "text": "x", "picture": "blender", "project": "../x"},
	} {
		if code, _, b := postDataset(t, s, body); code != http.StatusBadRequest {
			t.Fatalf("%v: %d %s", body, code, b)
		}
	}
	if code, _, b := postDataset(t, s, map[string]any{"mode": "video", "text": "x", "picture": "blender", "project": "nope"}); code != http.StatusNotFound {
		t.Fatalf("an unknown project is 404: %d %s", code, b)
	}
}

func TestAssistDataset_Blender3DNeedsTheModelOfTheDescriptions(t *testing.T) {
	s := mmServer(t)
	sceneWriterSeam(s)
	s.Stack.Workspace.Overrides.MotionWriter = func(context.Context) (motion.Writer, motion.ModelInfo, error) {
		return nil, motion.ModelInfo{}, errors.New("no model")
	}
	called := false
	s.Assistant.AssistDatasetRun = func(context.Context, assist.DatasetInput, string) ([]assist.DatasetEntry, error) {
		called = true
		return nil, nil
	}
	if code, _, body := postDataset(t, s, map[string]any{"mode": "video", "text": "x", "picture": "blender"}); code == http.StatusOK || called {
		t.Fatalf("with no model the storyboard is not even asked for: %d called=%v %s", code, called, body)
	}
}

func TestAssistDataset_Blender3DGraphicsInTheMotionMode(t *testing.T) {
	s := mmServer(t)
	asked := sceneWriterSeam(s)
	seen := datasetSeam(s, []assist.DatasetEntry{
		{ID: "t1", Title: "Logo", Body: "a red cube spinning above a dark floor\nDuration: 6"},
		{ID: "t2", Title: "Part", Body: "Part: stamp\nGraphic: something\nBlender: mine.scene.json\n**Picture:** a blue ball bouncing\nDuration: 4"},
		{ID: "t3", Title: "Bad", Body: "FAIL a thing\nDuration: 3"},
		{ID: "t4", Title: "Only a part", Body: "Part: stamp\nDuration: 3"},
	}, nil)
	code, a, body := postDataset(t, s, map[string]any{"mode": "motion", "text": "four 3d titles", "picture": "blender"})
	if code != http.StatusOK || len(a.Entries) != 2 {
		t.Fatalf("%d %s", code, body)
	}
	if seen.Picture != "blender" || len(seen.Parts) != 0 {
		t.Errorf("the dataset task is told 3D graphics and no parts: %+v", seen)
	}
	e := a.Entries[1]
	if !strings.Contains(e[3], "a blue ball bouncing") || !strings.Contains(e[3], "Duration: 4") || !strings.Contains(e[3], "Blender: t2.scene.json") ||
		strings.Contains(e[3], "Part:") || strings.Contains(e[3], "Graphic:") || strings.Contains(e[3], "mine.scene.json") {
		t.Errorf("a 3D graphic has the idea, the Duration and the server's Blender line:\n%s", e[3])
	}
	if len(a.Descriptions) != 2 || a.Descriptions["t1.scene.json"] == nil || a.Descriptions["t2.scene.json"] == nil {
		t.Errorf("descriptions: %v", a.Descriptions)
	}
	if len(*asked) != 3 || (*asked)[0].Seconds != 6 || (*asked)[1].Seconds != 4 {
		t.Errorf("each is written for the Duration it says: %+v", *asked)
	}
	joined := strings.Join(a.Warnings, "|")
	if !strings.Contains(joined, "t3") || !strings.Contains(joined, "graphic 4") {
		t.Errorf("warnings: %v", a.Warnings)
	}
	// previews with the motion mode of the import, with the descriptions beside
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/import/preview", map[string]any{"mode": "motion", "content": string(mustJSON(t, a.Entries)), "descriptions": a.Descriptions})
	var prev struct {
		Imported int
		Warnings []string
		YAML     string
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &prev)
	if rec.Code != http.StatusOK || prev.Imported != 2 || len(prev.Warnings) != 0 || !strings.Contains(prev.YAML, "build: t1.scene.json") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	// without the files the preview warns, as in the video mode
	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/import/preview", map[string]any{"mode": "motion", "content": string(mustJSON(t, a.Entries))})
	_ = json.Unmarshal(rec.Body.Bytes(), &prev)
	if len(prev.Warnings) != 2 {
		t.Errorf("a line without its file warns in the motion mode too: %v", prev.Warnings)
	}
	if code, _, b := postDataset(t, s, map[string]any{"mode": "music", "text": "x", "picture": "blender"}); code != http.StatusBadRequest {
		t.Errorf("music is 400: %d %s", code, b)
	}
}

func TestAssistDataset_Models3DEntriesAreOneObjectEachAndImport(t *testing.T) {
	s := mmServer(t)
	var seenCtx context.Context
	var seenIn assist.DatasetInput
	s.Assistant.AssistDatasetRun = func(ctx context.Context, in assist.DatasetInput, model string) ([]assist.DatasetEntry, error) {
		seenCtx, seenIn = ctx, in
		return []assist.DatasetEntry{
			{ID: "S01", Title: "Patín", Body: "**Prompt:** a retro roller skate, glossy pink leather with white wheels"},
			{ID: "s02", Title: "Casco", Body: "a matte black derby helmet with a chrome star\nDetail: high\nVariants: 2"},
			{ID: "s03", Title: "Equipo", Body: "a team of five women skating in a crowded rink\nEngine: sf3d"},
			{ID: "s04", Title: "Trofeo", Body: "a golden trophy cup\nSeed: 5\nAdult: true\nImage: x.png\nMotion: pan\nDialogue: hola"},
			{ID: "s05", Title: "Nada", Body: "Detail: low"},
		}, nil
	}
	rewrites := 0
	s.Assistant.AssistRun = func(ctx context.Context, task assist.Task, text, model string) (string, error) {
		if task != assist.TaskModel3D {
			t.Errorf("task %s", task)
		}
		rewrites++
		return "a pair of white quad roller skates with red wheels", nil
	}
	code, a, body := postDataset(t, s, map[string]any{"mode": "models3d", "text": "cinco cosas para una sala de roller derby", "examples": []map[string]string{{"prompt": "a glossy pink roller skate, studio render"}}})
	if code != http.StatusOK || a.Mode != "models3d" || len(a.Entries) != 4 {
		t.Fatalf("%d %s", code, body)
	}
	if seenIn.Mode != assist.DatasetModels3D || len(assist.ExamplesOf(seenCtx)) != 1 {
		t.Errorf("the writer is told the mode and gets the examples: %+v %v", seenIn, assist.ExamplesOf(seenCtx))
	}
	byID := map[string]string{}
	for _, e := range a.Entries {
		byID[e[0]] = e[3]
	}
	if byID["s01"] != "a retro roller skate, glossy pink leather with white wheels" {
		t.Errorf("the label before the prompt is not part of it: %q", byID["s01"])
	}
	if !strings.HasPrefix(byID["s02"], "a matte black derby helmet with a chrome star") || !strings.Contains(byID["s02"], "Detail: high") || !strings.Contains(byID["s02"], "Variants: 2") {
		t.Errorf("the settings the request asked for are kept: %q", byID["s02"])
	}
	if byID["s03"] != "a pair of white quad roller skates with red wheels\nEngine: sf3d" || rewrites != 1 {
		t.Errorf("an entry that is a scene with people is written again as one object (%d calls): %q", rewrites, byID["s03"])
	}
	if got := byID["s04"]; got != "a golden trophy cup" {
		t.Errorf("Seed, Adult, Image and the lines of other modes are not the writer's to give: %q", got)
	}
	warnings := strings.Join(a.Warnings, "|")
	if !strings.Contains(warnings, "model 3 described \"women\"") || !strings.Contains(warnings, "model 5 has no prompt") {
		t.Errorf("warnings: %v", a.Warnings)
	}
	// what comes back previews as a project of the mode, with nothing left out
	raw, _ := json.Marshal(a.Entries)
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/import/preview", map[string]any{"content": string(raw), "project": "objetos", "mode": "models3d"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"imported":4`) || !strings.Contains(rec.Body.String(), `"rejected":0`) {
		t.Errorf("the answer imports: %d %s", rec.Code, rec.Body)
	}
}

func TestAssistDataset_Models3DAnEntryThatStaysAScenePeopleIsLeftOut(t *testing.T) {
	s := mmServer(t)
	datasetSeam(s, []assist.DatasetEntry{
		{ID: "a", Title: "Escena", Body: "a crowd of fans cheering in the stands"},
		{ID: "b", Title: "Copa", Body: "a golden trophy cup"},
		{ID: "c", Title: "Otra", Body: "two women skating around a rink"},
	}, nil)
	calls := 0
	s.Assistant.AssistRun = func(ctx context.Context, task assist.Task, text, model string) (string, error) {
		calls++
		if calls == 1 {
			return "", assist.ErrNoMemory // the rewrite cannot be done
		}
		return "a woman in a rink", nil // and the rewrite is still people
	}
	code, a, body := postDataset(t, s, map[string]any{"mode": "models3d", "text": "x"})
	if code != http.StatusOK || len(a.Entries) != 1 || a.Entries[0][0] != "b" {
		t.Fatalf("%d %s", code, body)
	}
	w := strings.Join(a.Warnings, "|")
	if !strings.Contains(w, "could not be written again as one object: it was left out") || !strings.Contains(w, "is not one object alone") {
		t.Errorf("warnings: %v", a.Warnings)
	}
}

func TestAssistDataset_Models3DRefusalsAndBadRequests(t *testing.T) {
	s := mmServer(t)
	datasetSeam(s, []assist.DatasetEntry{{ID: "a", Body: "a lamp"}}, nil)
	for _, body := range []map[string]any{
		{"mode": "models3d", "text": "x", "picture": "blender"},
		{"mode": "models3d", "text": "x", "project": "p1"},
		{"mode": "audio", "text": "x"},
	} {
		code, _, b := postDataset(t, s, body)
		if code != http.StatusBadRequest {
			t.Errorf("%v: %d %s", body, code, b)
		}
		if body["mode"] == "audio" && !strings.Contains(b, "models3d") {
			t.Errorf("a wrong mode lists the accepted ones: %s", b)
		}
	}
	datasetSeam(s, nil, assist.ErrRefused)
	if code, _, b := postDataset(t, s, map[string]any{"mode": "models3d", "text": "x"}); code != http.StatusUnprocessableEntity || !strings.Contains(b, "assist_refused") {
		t.Errorf("the model's own refusal: %d %s", code, b)
	}
	datasetSeam(s, []assist.DatasetEntry{{ID: "a", Body: "Detail: low"}}, nil)
	if code, _, b := postDataset(t, s, map[string]any{"mode": "models3d", "text": "x"}); code != http.StatusUnprocessableEntity {
		t.Errorf("nothing usable is 422: %d %s", code, b)
	}
}

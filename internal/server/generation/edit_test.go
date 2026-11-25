package generation

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"veritmo/internal/server/workspace"

	"veritmo/internal/platform/events"
	"veritmo/internal/project/config"
	"veritmo/internal/project/export"
)

func pngBytes(w, h int) []byte {
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)))
	return buf.Bytes()
}

// editServer is a generation server whose runs write the pictures and the manifest like the images pipeline.
func editServer(t *testing.T) *testServer {
	t.Helper()
	s := genServer(t, fullCatalog())
	s.Workspace.Execute = func(ctx context.Context, rn workspace.ExecRun, em *events.Emitter) (string, error) {
		em.PipelineStart()
		scene := rn.Cfg.Scenes[0]
		n := 1
		if scene.Variants != nil {
			n = *scene.Variants
		}
		dir := filepath.Join(rn.Dir, "out", rn.Rec.Project)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		var man export.Manifest
		for i := 0; i < n; i++ {
			file := "out/" + rn.Rec.Project + "/" + scene.ID + ".png"
			if n > 1 {
				file = "out/" + rn.Rec.Project + "/" + scene.ID + "_v" + string(rune('1'+i)) + ".png"
			}
			if err := os.WriteFile(filepath.Join(rn.Dir, filepath.FromSlash(file)), []byte("png"), 0o644); err != nil {
				return "", err
			}
			man.Images = append(man.Images, export.Entry{SceneID: scene.ID, Variant: i, EffectiveSeed: int64(1000 + i), File: file})
		}
		raw, _ := json.Marshal(man)
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o644); err != nil {
			return "", err
		}
		em.Done("out/" + rn.Rec.Project + "/manifest.json")
		return "out/" + rn.Rec.Project + "/manifest.json", nil
	}
	return s
}

func postEdit(t *testing.T, s *testServer, req any, pic []byte) *httptest.ResponseRecorder {
	t.Helper()
	body, ct := multipartGeneration(t, req, "photo.png", pic)
	r := httptest.NewRequest(http.MethodPost, "/api/image-edits", body)
	r.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	return rec
}

func decodeEdit(t *testing.T, rec *httptest.ResponseRecorder) imageEditView {
	t.Helper()
	var v imageEditView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("not an edit: %v: %s", err, rec.Body)
	}
	return v
}

func waitEdit(t *testing.T, s *testServer, id, want string) imageEditView {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/image-edits/"+id, nil); rec.Code == http.StatusOK {
			if v := decodeEdit(t, rec); v.State == want {
				return v
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("edit %s never reached %s", id, want)
	return imageEditView{}
}

func TestPlanImageEdit_BothKindsWriteAScenesYAMLThePipelineLoads(t *testing.T) {
	cat := fullCatalog()
	m := machine(16384, 32)
	root := t.TempDir()
	touchFiles(t, root, cat)
	_ = os.MkdirAll(filepath.Join(root, "sources"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "sources", "inicio.png"), pngBytes(4, 3), 0o644)
	strength := 0.7
	for _, tc := range []struct {
		model string
		req   imageEditRequest
	}{
		{"into", imageEditRequest{Prompt: "make it night", NegativePrompt: "blur", Strength: &strength, Variants: 3}},
		{"sd-1-5", imageEditRequest{Prompt: "make it night", Variants: 1}},
		{"kontext", imageEditRequest{Prompt: "put a red cap on the man", Variants: 1}},
		{"qwen", imageEditRequest{Prompt: "put a red cap on the man", Variants: 1}},
	} {
		t.Run(tc.model, func(t *testing.T) {
			var mv imageModelView
			for _, v := range buildImageModels(cat, m) {
				if v.ID == tc.model {
					mv = v
				}
			}
			var entry catalogEntry
			for _, e := range cat {
				if e.ID == tc.model {
					entry = e
				}
			}
			cfg := planImageEdit("api-edit-x", tc.req, mv, entry, m, "inicio.png", 1600, 900)
			yml, err := scenesYAML(cfg)
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := config.LoadBytes(yml)
			if err != nil {
				t.Fatalf("does not load: %v\n%s", err, yml)
			}
			if err := loaded.ValidateInDir(root); err != nil {
				t.Fatalf("does not validate: %v\n%s", err, yml)
			}
			sc := loaded.Scenes[0]
			if mv.Kind == editKindInstruction {
				if sc.Strength != nil || loaded.Defaults.ModelSet == nil || !loaded.Defaults.ModelSet.Edit {
					t.Errorf("an instruction edit has no strength and an editing model set: %+v", loaded.Defaults.ModelSet)
				}
			} else if sc.Strength == nil || sc.Variants == nil || *sc.Variants != tc.req.Variants {
				t.Errorf("an img2img edit carries strength and variants: %+v", sc)
			}
			w, h, _ := parseRes(loaded.Defaults.Resolution)
			if float64(w)/float64(h) < 1.6 || float64(w)/float64(h) > 1.9 {
				t.Errorf("the edit keeps the shape of the picture (16:9): %s", loaded.Defaults.Resolution)
			}
		})
	}
}

func TestPictureSizeReadsPNGAndJPEGOnly(t *testing.T) {
	if w, h := pictureSize(pngBytes(40, 30)); w != 40 || h != 30 {
		t.Errorf("png = %dx%d", w, h)
	}
	if w, h := pictureSize([]byte("RIFFxxxxWEBP")); w != 0 || h != 0 {
		t.Errorf("webp = %dx%d, want unknown", w, h)
	}
}

func TestImageEdits_AnImg2ImgEditGivesOneResultPerVariant(t *testing.T) {
	s := editServer(t)
	rec := postEdit(t, s, map[string]any{"model": "into", "prompt": "make it night", "strength": 0.6, "variants": 3}, pngBytes(160, 90))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	e := decodeEdit(t, rec)
	if !strings.HasPrefix(e.ID, editPrefix) || e.RunID == "" {
		t.Errorf("answer = %+v", e)
	}
	done := waitEdit(t, s, e.ID, GenDone)
	if len(done.Results) != 3 || done.Results[0].Variant != 1 || done.Results[0].Seed != 1000 || done.Results[2].Seed != 1002 || done.Progress != 100 {
		t.Fatalf("results = %+v", done)
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, done.Results[1].Image, nil); rec.Code != http.StatusOK {
		t.Errorf("a result is not served: %d", rec.Code)
	}
}

func TestImageEdits_AnEditByInstruction(t *testing.T) {
	s := editServer(t)
	rec := postEdit(t, s, map[string]any{"model": "kontext", "prompt": "put a red cap on the man"}, pngBytes(160, 90))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	e := decodeEdit(t, rec)
	cfg, err := config.Load(s.Workspace.ProjectConfigPath(e.ID))
	if err != nil || cfg.Defaults.ModelSet == nil || !cfg.Defaults.ModelSet.Edit || cfg.Scenes[0].Prompt != "put a red cap on the man" {
		t.Fatalf("config = %+v (%v)", cfg, err)
	}
	if done := waitEdit(t, s, e.ID, GenDone); len(done.Results) != 1 {
		t.Errorf("results = %+v", done.Results)
	}
}

func TestImageEdits_TheDefaultModelIsTheFirstAvailable(t *testing.T) {
	s := editServer(t)
	e := decodeEdit(t, postEdit(t, s, map[string]any{"prompt": "p"}, pngBytes(16, 9)))
	if e.Request.Model != "kontext" || e.Request.Variants != 1 {
		t.Errorf("request = %+v", e.Request)
	}
}

func TestImageEdits_ARefusedRequestCreatesNothing(t *testing.T) {
	s := editServer(t)
	img := pngBytes(16, 9)
	cases := []struct {
		name string
		req  map[string]any
		want int
		text string
	}{
		{"blank prompt", map[string]any{"model": "into", "prompt": "  "}, 422, "blank"},
		{"unknown model", map[string]any{"model": "nope", "prompt": "p"}, 422, "unknown"},
		{"strength on an instruction model", map[string]any{"model": "kontext", "prompt": "p", "strength": 0.5}, 422, "strength"},
		{"negative prompt on an instruction model", map[string]any{"model": "qwen", "prompt": "p", "negative_prompt": "x"}, 422, "negative prompt"},
		{"strength of zero", map[string]any{"model": "into", "prompt": "p", "strength": 0}, 422, "strength"},
		{"strength above one", map[string]any{"model": "into", "prompt": "p", "strength": 1.5}, 422, "strength"},
		{"too many variants", map[string]any{"model": "into", "prompt": "p", "variants": 9}, 422, "variants"},
		{"negative variants", map[string]any{"model": "into", "prompt": "p", "variants": -1}, 422, "variants"},
		{"variants of an instruction model", map[string]any{"model": "kontext", "prompt": "p", "variants": 2}, 422, "variants"},
		{"adult on a censored server", map[string]any{"model": "into", "prompt": "p", "adult": true}, 422, "adults"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postEdit(t, s, tc.req, img)
			if rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.text) {
				t.Fatalf("status %d, want %d with %q: %s", rec.Code, tc.want, tc.text, rec.Body)
			}
		})
	}
	// No picture at all.
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/image-edits", map[string]any{"prompt": "p"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("no picture: %d", rec.Code)
	}
	entries, _ := os.ReadDir(s.ProjectsDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), editPrefix) {
			t.Errorf("a refused request left %s behind", e.Name())
		}
	}
}

func TestImageEdits_AModelThatCannotRunIsRefusedWithTheReason(t *testing.T) {
	cat := fullCatalog()
	for i := range cat {
		if cat[i].ID == "qwen" {
			cat[i].Installed = false
		}
	}
	s := genServer(t, cat)
	rec := postEdit(t, s, map[string]any{"model": "qwen", "prompt": "p"}, pngBytes(16, 9))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "not installed") {
		t.Errorf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestImageEdits_DeletingRemovesFilesAndHistoryAndTheyAreNotProjects(t *testing.T) {
	s := editServer(t)
	e := decodeEdit(t, postEdit(t, s, map[string]any{"model": "into", "prompt": "p", "variants": 2}, pngBytes(16, 9)))
	waitEdit(t, s, e.ID, GenDone)
	var projects []projectSummary
	_ = json.Unmarshal(doJSON(t, s.Handler(), http.MethodGet, "/api/projects", nil).Body.Bytes(), &projects)
	for _, p := range projects {
		if isManagedProject(p.ID) {
			t.Errorf("%s is among the projects", p.ID)
		}
	}
	if rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/image-edits/"+e.ID, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/image-edits/"+e.ID, nil); rec.Code != http.StatusNotFound {
		t.Errorf("get after delete: %d", rec.Code)
	}
	if _, err := os.Stat(s.Workspace.ProjectDir(e.ID)); !os.IsNotExist(err) {
		t.Errorf("files remain: %v", err)
	}
	if runs, _ := s.Workspace.RunsOf(e.ID); len(runs) != 0 {
		t.Errorf("history remains: %+v", runs)
	}
	// A generation is not an edit and the other way round.
	g := decodeGeneration(t, postGeneration(t, s, map[string]any{"prompt": "p", "option": "ltx"}))
	if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/image-edits/"+g.ID, nil); rec.Code != http.StatusNotFound {
		t.Errorf("a generation read as an edit: %d", rec.Code)
	}
}

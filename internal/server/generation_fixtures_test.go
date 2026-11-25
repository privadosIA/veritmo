package server

// generation_fixtures_test.go: the servers and helpers the HTTP-level tests of the generation API share. The tests of
// the family itself (internal/server/generation) have their own over the family alone.

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"veritmo/internal/platform/events"
	"veritmo/internal/project/export"
	"veritmo/internal/server/generation"
	"veritmo/internal/server/library3d"
	"veritmo/internal/server/servertest"
	"veritmo/internal/server/workspace"
)

func fullCatalog() []catalogEntry { return servertest.FullCatalog() }
func baseCatalog() []catalogEntry { return servertest.BaseCatalog() }

func pngBytes(w, h int) []byte {
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)))
	return buf.Bytes()
}

// touchFiles creates, under root, the files of the installed entries (models/ or models/video/).
func touchFiles(t *testing.T, root string, cat []catalogEntry) {
	t.Helper()
	for _, e := range cat {
		dir := filepath.Join(root, "models")
		if e.Kind == "video" {
			dir = filepath.Join(dir, "video")
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, f := range e.Files {
			if err := os.WriteFile(filepath.Join(dir, f.Name), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// genServer is a launch server whose catalog and machine are the fixtures (a 16 GB card, 32 GB of RAM) and whose
// runs finish at once, writing the video or the pictures like the pipeline.
func genServer(t *testing.T, cat []catalogEntry) *Server {
	t.Helper()
	s, _ := newProjectsServer(t) // moves into a temp repo with models/ and voices/
	s.generation.GenCatalog = func() []catalogEntry { return cat }
	s.generation.GenMachine = func() generation.Machine { return generation.Machine{VRAMMB: 16384, RAMGB: 32, Speed: 1} }
	touchFiles(t, ".", cat)
	s.workspace.SetPipelineLookPath(fakeLook)
	s.workspace.Execute = func(ctx context.Context, rn workspace.ExecRun, em *events.Emitter) (string, error) {
		em.PipelineStart()
		for _, ph := range []string{"render", "animate", "clip"} {
			em.PhaseStart("escena_1", ph)
			em.PhaseEnd("escena_1", ph, "")
		}
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
	t.Cleanup(s.workspace.Shutdown)
	return s
}

func postGeneration(t *testing.T, s *Server, body any) *httptest.ResponseRecorder {
	t.Helper()
	return doJSON(t, s.Handler(), http.MethodPost, "/api/generations", body)
}

func decodeGeneration(t *testing.T, rec *httptest.ResponseRecorder) generation.GenerationView {
	t.Helper()
	var g generation.GenerationView
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
		t.Fatalf("not a generation: %v: %s", err, rec.Body)
	}
	return g
}

func waitGeneration(t *testing.T, s *Server, id, want string) generation.GenerationView {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rec := doJSON(t, s.Handler(), http.MethodGet, "/api/generations/"+id, nil)
		if rec.Code == http.StatusOK {
			if g := decodeGeneration(t, rec); g.State == want {
				return g
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("generation %s never reached %s", id, want)
	return generation.GenerationView{}
}

func multipartGeneration(t *testing.T, req any, name string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	raw, _ := json.Marshal(req)
	if err := mw.WriteField("request", string(raw)); err != nil {
		t.Fatal(err)
	}
	fw, err := mw.CreateFormFile("image", name)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write(data)
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

// editServer is a generation server whose runs write the pictures and the manifest like the images pipeline.
func editServer(t *testing.T) *Server {
	t.Helper()
	s := genServer(t, fullCatalog())
	s.workspace.Execute = func(ctx context.Context, rn workspace.ExecRun, em *events.Emitter) (string, error) {
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

func postEdit(t *testing.T, s *Server, req any, pic []byte) *httptest.ResponseRecorder {
	t.Helper()
	body, ct := multipartGeneration(t, req, "photo.png", pic)
	r := httptest.NewRequest(http.MethodPost, "/api/image-edits", body)
	r.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	return rec
}

func decodeEdit(t *testing.T, rec *httptest.ResponseRecorder) generation.ImageEditView {
	t.Helper()
	var v generation.ImageEditView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("not an edit: %v: %s", err, rec.Body)
	}
	return v
}

func waitEdit(t *testing.T, s *Server, id, want string) generation.ImageEditView {
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
	return generation.ImageEditView{}
}

// The types of the answers of the generation API, under the names the tests use.
type (
	generationRequest      = generation.GenerationRequest
	generationView         = generation.GenerationView
	generationInput        = generation.GenerationInput
	generationInputs       = generation.GenerationInputs
	generationOptions      = generation.GenerationOptions
	durationRange          = generation.DurationRange
	inputLimit             = generation.InputLimit
	videoOptionView        = generation.VideoOptionView
	imageModelView         = generation.ImageModelView
	imageEditRequest       = generation.ImageEditRequest
	imageEditResult        = generation.ImageEditResult
	imageEditView          = generation.ImageEditView
	videoEditRequest       = generation.VideoEditRequest
	videoEditView          = generation.VideoEditView
	modelGenerationView    = library3d.GenerationView
	modelGenerationRequest = library3d.GenerationRequest
	videoEditResult        = generation.VideoEditResult
	videoEditToolView      = generation.VideoEditToolView
	libraryMotionView      = generation.LibraryMotionView
	libraryMotionRequest   = generation.LibraryMotionRequest
	presetRequest          = generation.PresetRequest
	presetPut              = generation.PresetPut
)

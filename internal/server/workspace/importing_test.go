package workspace

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/platform/settings"
	"veritmo/internal/project/config"
	"veritmo/internal/server/api"
	"veritmo/internal/video/motion"
)

// fixture resolves a repo testdata file (tests chdir into a temp root, so
// resolve from the original module path via the source file location).
func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(moduleRoot(t), "testdata", name))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return string(data)
}

var repoRoot string

func moduleRoot(t *testing.T) string {
	t.Helper()
	if repoRoot == "" {
		t.Fatal("repoRoot not captured")
	}
	return repoRoot
}

func init() {
	// Capture the real module root before any test chdirs away.
	if wd, err := os.Getwd(); err == nil {
		repoRoot = filepath.Dir(filepath.Dir(filepath.Dir(wd))) // internal/server/workspace -> repo root
	}
}

type previewResponse struct {
	YAML     string              `json:"yaml"`
	Report   string              `json:"report"`
	Entries  []importEntryReport `json:"entries"`
	Imported int                 `json:"imported"`
	Silent   int                 `json:"silent"`
	Rejected int                 `json:"rejected"`
}

func TestImport_PreviewReturnsReportWithoutCreating(t *testing.T) {
	s, projects := newProjectsServer(t)
	dataset := fixture(t, "demo_prompts.json")

	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/import/preview",
		map[string]any{"content": dataset, "project": "importado"})
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	var resp previewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Imported == 0 || len(resp.Entries) == 0 || resp.Report == "" {
		t.Errorf("empty preview: %+v", resp)
	}
	if !strings.Contains(resp.YAML, "project: importado") {
		t.Errorf("yaml should name the project: %s", resp.YAML)
	}
	// The server fills the placeholders it knows (shared assets exist in
	// this fixture repo), so the preview validates as-is.
	if strings.Contains(resp.YAML, "# model:") || strings.Contains(resp.YAML, "# file:") {
		t.Errorf("placeholders left commented despite available assets:\n%s", resp.YAML)
	}

	// Nothing was created.
	entries, _ := os.ReadDir(projects)
	if len(entries) != 0 {
		t.Errorf("preview created something: %v", entries)
	}
}

func TestImport_InvalidDatasetRejectedWithImportError(t *testing.T) {
	s, _ := newProjectsServer(t)
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/import/preview",
		map[string]any{"content": "<html><body>no data here</body></html>"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if code := decodeAPIError(t, rec.Body.String()); code != api.CodeValidation {
		t.Errorf("code = %s", code)
	}
	if !strings.Contains(rec.Body.String(), "import:") {
		t.Errorf("error should come from the import logic: %s", rec.Body.String())
	}
}

func TestImport_ConfirmCreatesIdenticalValidProject(t *testing.T) {
	s, projects := newProjectsServer(t)
	dataset := fixture(t, "demo_prompts.html")

	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/import/preview",
		map[string]any{"content": dataset, "project": "web-import"})
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	var resp previewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/import/confirm",
		map[string]any{"id": "web-import", "yaml": resp.YAML})
	if rec.Code != http.StatusCreated {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body.String())
	}

	// Identical content, and it passes the pipeline's validation in place.
	dir := filepath.Join(projects, "web-import")
	onDisk, err := os.ReadFile(filepath.Join(dir, "scenes.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != resp.YAML {
		t.Error("created scenes.yaml differs from the previewed content")
	}
	cfg, err := config.LoadBytes(onDisk)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateInDir(dir); err != nil {
		t.Errorf("imported project does not validate: %v", err)
	}

	// Existing name is never overwritten.
	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/import/confirm",
		map[string]any{"id": "web-import", "yaml": resp.YAML})
	if rec.Code != http.StatusConflict {
		t.Errorf("confirm over existing: %d, want 409", rec.Code)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "scenes.yaml"))
	if string(after) != string(onDisk) {
		t.Error("existing project was modified")
	}
}

const songsDataset = `[["balada","t","","Spanish pop ballad\nBPM: 90\nLyrics:\n[Verse]\nhola"],["lofi","t","","Lo-fi\nInstrumental"]]`

func TestImport_MusicModePreviewAndConfirm(t *testing.T) {
	s, projects := newProjectsServer(t)
	installMusicModels(t) // confirm validates the project like any save does
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/import/preview",
		map[string]any{"content": songsDataset, "project": "canciones", "mode": "music"})
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		YAML         string `json:"yaml"`
		Imported     int    `json:"imported"`
		Instrumental int    `json:"instrumental"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Imported != 2 || resp.Instrumental != 1 {
		t.Fatalf("%+v", resp)
	}
	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/import/confirm",
		map[string]any{"id": "canciones", "yaml": resp.YAML})
	if rec.Code != http.StatusCreated {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body.String())
	}
	b, err := os.ReadFile(filepath.Join(projects, "canciones", "scenes.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadBytes(b)
	if err != nil || cfg.Output.Mode != config.ModeMusic || len(cfg.Tracks) != 2 {
		t.Fatalf("%v %+v", err, cfg)
	}
}

func TestImport_UnknownModeIsRejected(t *testing.T) {
	s, _ := newProjectsServer(t)
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/import/preview",
		map[string]any{"content": songsDataset, "mode": "audio"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

const graphicsDataset = `[["grafico","t","","A bar chart growing from 2 to 9\nDuration: 4\nOverlay: Sales 2026\nOverlay position: top"],["titulo","t","","Opening title over blue waves"]]`

func TestImport_MotionModePreviewAndConfirm(t *testing.T) {
	if _, err := motion.Find(""); err != nil {
		t.Skipf("no Chrome/Chromium available: %v", err)
	}
	s, projects := newProjectsServer(t)
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/import/preview",
		map[string]any{"content": graphicsDataset, "project": "graficos", "mode": "motion"})
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		YAML     string `json:"yaml"`
		Imported int    `json:"imported"`
		Silent   int    `json:"silent"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Imported != 2 || resp.Silent != 2 || !strings.Contains(resp.YAML, "motion_graphic: A bar chart growing from 2 to 9") {
		t.Fatalf("%+v\n%s", resp, resp.YAML)
	}
	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/import/confirm",
		map[string]any{"id": "graficos", "yaml": resp.YAML})
	if rec.Code != http.StatusCreated {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body.String())
	}
	b, err := os.ReadFile(filepath.Join(projects, "graficos", "scenes.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadBytes(b)
	if err != nil || cfg.Output.Mode != config.ModeVideo || len(cfg.Scenes) != 2 || cfg.Scenes[0].MotionGraphic == "" || cfg.Scenes[0].Overlay == nil {
		t.Fatalf("%v %+v", err, cfg)
	}
}

// Importing needs no browser: the graphics are drawn where the project runs.
func TestImport_MotionModePreviewNeedsNoBrowser(t *testing.T) {
	// a browser that is not there: the settings name a file that does not exist
	old := settings.Load()
	gone := old
	gone.Browser = filepath.Join(t.TempDir(), "no-such-browser")
	if err := settings.Save(gone); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = settings.Save(old) })
	s, _ := newProjectsServer(t)
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/import/preview",
		map[string]any{"content": graphicsDataset, "mode": "motion"})
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
}

func TestImport_OutputModeImages(t *testing.T) {
	s, projects := newProjectsServer(t)
	dataset := fixture(t, "demo_prompts.json")

	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/import/preview",
		map[string]any{"content": dataset, "project": "pics", "output_mode": "images"})
	var resp previewResponse
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &resp) != nil {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	cfg, err := config.LoadBytes([]byte(resp.YAML))
	if err != nil || cfg.Output.Mode != config.ModeImages {
		t.Fatalf("the previewed project is an images project: %v %q", err, cfg.Output.Mode)
	}
	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/import/confirm", map[string]any{"id": "pics", "yaml": resp.YAML})
	if rec.Code != http.StatusCreated {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body.String())
	}

	// The confirm can also override the mode of a YAML previewed as video; comments stay.
	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/import/preview", map[string]any{"content": dataset, "project": "pics2"})
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/import/confirm", map[string]any{"id": "pics2", "yaml": resp.YAML, "output_mode": "images"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("confirm with override: %d %s", rec.Code, rec.Body.String())
	}
	onDisk, _ := os.ReadFile(filepath.Join(projects, "pics2", "scenes.yaml"))
	if got, err := config.LoadBytes(onDisk); err != nil || got.Output.Mode != config.ModeImages || len(got.Scenes) != len(cfg.Scenes) {
		t.Fatalf("%v %+v", err, got)
	}

	// Default stays video; bad values and images for music or motion are refused.
	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/import/preview", map[string]any{"content": dataset, "output_mode": "video"})
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if got, _ := config.LoadBytes([]byte(resp.YAML)); got == nil || got.Output.Mode != config.ModeVideo {
		t.Fatalf("video: %s", resp.YAML)
	}
	for _, body := range []map[string]any{
		{"content": dataset, "output_mode": "audio"},
		{"content": songsDataset, "mode": "music", "output_mode": "images"},
		{"content": dataset, "mode": "motion", "output_mode": "images"},
	} {
		if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/import/preview", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%v: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/import/confirm", map[string]any{"id": "x1", "yaml": resp.YAML, "output_mode": "audio"}); rec.Code != http.StatusBadRequest {
		t.Errorf("confirm audio: %d", rec.Code)
	}
}

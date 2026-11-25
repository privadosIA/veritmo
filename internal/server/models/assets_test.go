package models

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"veritmo/internal/server/workspace"

	"veritmo/internal/server/api"
)

// newAssetServer returns a server whose assets root is a fresh temp dir.
func newAssetServer(t *testing.T) *testServer {
	t.Helper()
	s := newTestServer(t)
	s.assetsRoot = t.TempDir()
	return s
}

func writeAsset(t *testing.T, s *testServer, kind, name string, size int) string {
	t.Helper()
	k, _ := FindKind(kind)
	dir := s.KindDir(k)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil { // a kind of units may take "<folder>/<file>"
		t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func call(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestValidateAssetName(t *testing.T) {
	model, _ := FindKind("model")
	voice, _ := FindKind("voice")
	cases := []struct {
		kind AssetKind
		name string
		ok   bool
	}{
		{model, "v1-5.safetensors", true},
		{model, "UPPER.SAFETENSORS", true},
		{model, "a.ckpt", true},
		{model, "a.gguf", true},
		{voice, "x.onnx", true},
		{voice, "x.onnx.json", true},
		{model, "", false},
		{model, ".", false},
		{model, "..", false},
		{model, "../x.safetensors", false},
		{model, "a/b.safetensors", false},
		{model, `a\b.safetensors`, false},
		{model, "/etc/passwd.safetensors", false},
		{model, ".hidden.safetensors", false},
		{model, "run.exe", false},
		{model, "x.onnx", false},
		{voice, "x.safetensors", false},
	}
	for _, c := range cases {
		err := ValidateAssetName(c.kind, c.name)
		if (err == nil) != c.ok {
			t.Errorf("ValidateAssetName(%s, %q) = %v, want ok=%v", c.kind.Kind, c.name, err, c.ok)
		}
	}
}

func TestAssetsList(t *testing.T) {
	s := newAssetServer(t)
	writeAsset(t, s, "model", "m.safetensors", 10)
	writeAsset(t, s, "model", "m2.safetensors.partial", 5)
	writeAsset(t, s, "model", ".hidden", 5)
	writeAsset(t, s, "voice", "v.onnx", 7)
	writeAsset(t, s, "voice", "v.onnx.json", 3)

	rec := call(s.Handler(), "GET", "/api/assets", "")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		Kinds []struct {
			Kind   string                 `json:"kind"`
			Note   string                 `json:"note"`
			Assets []workspace.AssetEntry `json:"assets"`
		} `json:"kinds"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	got := map[string][]workspace.AssetEntry{}
	notes := map[string]string{}
	for _, k := range out.Kinds {
		got[k.Kind] = k.Assets
		notes[k.Kind] = k.Note
		if k.Assets == nil {
			t.Errorf("kind %s: assets must be [] not null", k.Kind)
		}
	}
	if len(got["model"]) != 1 || got["model"][0].Name != "m.safetensors" || got["model"][0].Size != 10 {
		t.Errorf("models = %+v (partial and hidden files must not be listed)", got["model"])
	}
	if len(got["voice"]) != 2 {
		t.Errorf("voices = %+v", got["voice"])
	}
	if len(got["lora"]) != 0 {
		t.Errorf("missing loras dir must list nothing, got %+v", got["lora"])
	}
	if notes["lora"] == "" || notes["model"] != "" {
		t.Errorf("only lora carries the not-applied note: %v", notes)
	}
}

func TestAssetsCatalog_InstalledState(t *testing.T) {
	s := newAssetServer(t)
	writeAsset(t, s, "model", "v1-5-pruned-emaonly.safetensors", 1)
	writeAsset(t, s, "voice", "en_US-lessac-medium.onnx", 1) // companion json missing

	rec := call(s.Handler(), "GET", "/api/assets/catalog", "")
	var out struct{ Entries []catalogEntry }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	state := map[string]bool{}
	for _, e := range out.Entries {
		state[e.ID] = e.Installed
		if len(e.Files) == 0 || e.Name == "" || e.SizeBytes == 0 {
			t.Errorf("catalog entry %s incomplete: %+v", e.ID, e)
		}
	}
	if !state["sd-1-5"] {
		t.Error("sd-1-5 must be installed")
	}
	if state["voice-en-us-lessac-medium"] {
		t.Error("a voice with only its .onnx must not count as installed")
	}
	if state["sdxl-base-1-0"] {
		t.Error("sdxl must not be installed")
	}
	// every voice entry carries both files
	for _, e := range catalog {
		if e.Kind == "voice" && len(e.Files) != 2 {
			t.Errorf("voice %s must list .onnx and .onnx.json", e.ID)
		}
	}
}

func TestAssetDelete(t *testing.T) {
	s := newAssetServer(t)
	h := s.Handler()
	m := writeAsset(t, s, "model", "m.safetensors", 1)
	v := writeAsset(t, s, "voice", "v.onnx", 1)
	vj := writeAsset(t, s, "voice", "v.onnx.json", 1)

	if rec := call(h, "DELETE", "/api/assets/model/nope.safetensors", ""); rec.Code != 404 {
		t.Errorf("unknown file: %d", rec.Code)
	}
	if rec := call(h, "DELETE", "/api/assets/widget/x.safetensors", ""); rec.Code != 404 {
		t.Errorf("unknown kind: %d", rec.Code)
	}
	if rec := call(h, "DELETE", "/api/assets/model/a%2Fb.safetensors", ""); rec.Code != 400 {
		t.Errorf("encoded separator: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "DELETE", "/api/assets/model/bad.exe", ""); rec.Code != 400 {
		t.Errorf("bad extension: %d", rec.Code)
	}

	// refused while a run is queued/active
	s.busyRun = "r1"
	releaseRun := func() { s.busyRun = "" }
	rec := call(h, "DELETE", "/api/assets/model/m.safetensors", "")
	if rec.Code != 409 || decodeAPIError(t, rec.Body.String()) != api.CodeConflict {
		t.Fatalf("busy: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(m); err != nil {
		t.Fatal("file must remain while a run is active")
	}
	releaseRun()

	if rec := call(h, "DELETE", "/api/assets/model/m.safetensors", ""); rec.Code != 204 {
		t.Fatalf("delete model: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(m); !os.IsNotExist(err) {
		t.Error("model not removed")
	}
	if rec := call(h, "DELETE", "/api/assets/voice/v.onnx", ""); rec.Code != 204 {
		t.Fatalf("delete voice: %d", rec.Code)
	}
	for _, p := range []string{v, vj} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s must be removed with the voice", p)
		}
	}
}

func TestCatalog_PurposeAndVRAM(t *testing.T) {
	for _, e := range catalog {
		if len(strings.TrimSpace(e.Description)) < 30 {
			t.Errorf("%s: description must say what the asset is for, got %q", e.ID, e.Description)
		}
		switch e.Kind {
		case "model":
			if e.VRAMMB <= 0 {
				t.Errorf("%s: models need an approximate vram_mb", e.ID)
			}
		case "video":
			// optional: only when it was measured
		case "music":
			if e.VRAMMB <= 0 {
				t.Errorf("%s: music bundles need an approximate vram_mb", e.ID)
			}
		default:
			if e.VRAMMB != 0 {
				t.Errorf("%s: only models (and measured video bundles) carry vram_mb", e.ID)
			}
		}
	}
	s := newAssetServer(t)
	rec := call(s.Handler(), "GET", "/api/assets/catalog", "")
	if !strings.Contains(rec.Body.String(), `"vram_mb":4096`) {
		t.Errorf("catalog JSON lacks vram_mb: %s", rec.Body)
	}
	if strings.Contains(rec.Body.String(), `"vram_mb":0`) || strings.Contains(rec.Body.String(), `"vram_mb":null`) {
		t.Error("voices must omit vram_mb, not send 0")
	}
}

func TestCatalog_EveryKindHasRecommendationsAndBundlesNameTheirRoles(t *testing.T) {
	seen := map[string]int{}
	starters := 0
	for _, e := range catalog {
		seen[e.Kind]++
		if e.Starter {
			starters++
		}
		k, ok := FindKind(e.Kind)
		if !ok {
			t.Fatalf("%s: unknown kind %q", e.ID, e.Kind)
		}
		var sum int64
		for _, f := range e.Files {
			if err := ValidateAssetName(k, f.Name); err != nil {
				t.Errorf("%s: file %s: %v", e.ID, f.Name, err)
			}
			if !strings.HasPrefix(f.URL, "https://") {
				t.Errorf("%s: %s must be an https URL", e.ID, f.Name)
			}
			_ = sum
		}
		if e.Kind == "video" {
			roles := map[string]bool{}
			for _, f := range e.Files {
				roles[f.Role] = true
			}
			// A Wan bundle names its three files; an AnimateDiff entry is the
			// motion module alone (the SD 1.5 checkpoint is the user's image model).
			wants := []string{"model", "text_encoder", "vae"}
			if roles["motion_module"] {
				wants = []string{"motion_module"}
			}
			for _, want := range wants {
				if !roles[want] {
					t.Errorf("%s: video bundle lacks a %s file", e.ID, want)
				}
			}
		}
	}
	for _, kind := range []string{"model", "voice", "lora", "video"} {
		if seen[kind] == 0 {
			t.Errorf("catalog has no recommended %s entry", kind)
		}
	}
	if starters == 0 {
		t.Error("catalog must flag some starter entries")
	}
	if e, _ := findCatalog("sd-1-5"); !e.Starter {
		t.Error("SD 1.5 must be a starter entry")
	}
	if e, _ := findCatalog("sdxl-base-1-0"); e.Starter {
		t.Error("SDXL must not be a starter entry")
	}
}

func TestVideoKind_ListsValidatesAndStaysOutOfModels(t *testing.T) {
	s := newAssetServer(t)
	writeAsset(t, s, "video", "wan-Q4.gguf", 5)
	writeAsset(t, s, "model", "m.safetensors", 3)

	var out struct {
		Kinds []struct {
			Kind   string                 `json:"kind"`
			Note   string                 `json:"note"`
			Assets []workspace.AssetEntry `json:"assets"`
		} `json:"kinds"`
	}
	rec := call(s.Handler(), "GET", "/api/assets", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	got := map[string][]workspace.AssetEntry{}
	notes := map[string]string{}
	for _, k := range out.Kinds {
		got[k.Kind], notes[k.Kind] = k.Assets, k.Note
	}
	if len(got["video"]) != 1 || got["video"][0].Name != "wan-Q4.gguf" {
		t.Errorf("video listing = %+v", got["video"])
	}
	if len(got["model"]) != 1 || got["model"][0].Name != "m.safetensors" {
		t.Errorf("a video file leaked into models: %+v", got["model"])
	}
	if !strings.Contains(notes["video"], "Animation models") || strings.Contains(notes["video"], "todavía no genera") {
		t.Errorf("video note = %q", notes["video"])
	}

	k, _ := FindKind("video")
	if ValidateAssetName(k, "x.gguf") != nil || ValidateAssetName(k, "x.safetensors") != nil {
		t.Error("video must accept .gguf and .safetensors")
	}
	if ValidateAssetName(k, "x.ckpt") == nil {
		t.Error("video must reject .ckpt")
	}

	// the editor's environment inventory never offers video files
	env := call(s.Handler(), "GET", "/api/environment", "")
	if strings.Contains(env.Body.String(), "wan-Q4.gguf") {
		t.Error("video files must not appear in the environment inventory")
	}
}

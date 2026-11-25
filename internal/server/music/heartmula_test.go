package music

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"veritmo/internal/music/heartmula"
	"veritmo/internal/platform/runner"
)

// heartMulaWeights writes a complete weights folder.
func writeHeartMulaWeights(t *testing.T, dir string) {
	t.Helper()
	files := map[string]string{
		"config.json": "{}", "gen_config.json": "{}", "tokenizer.json": "{}",
		"HeartMuLa-oss-3B/config.json":                  "{}",
		"HeartMuLa-oss-3B/model.safetensors.index.json": `{"weight_map": {"a": "model-1.safetensors"}}`,
		"HeartMuLa-oss-3B/model-1.safetensors":          "x",
		"HeartCodec-oss/config.json":                    "{}",
		"HeartCodec-oss/model.safetensors.index.json":   `{"weight_map": {"a": "model-1.safetensors"}}`,
		"HeartCodec-oss/model-1.safetensors":            "x",
	}
	for rel, c := range files {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(c), 0o644)
	}
}

// smiWith answers nvidia-smi as a machine with a 16 GB card; calls counts the commands asked.
func smiWith(calls *atomic.Int32) runner.CommandRunner {
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		calls.Add(1)
		if inv.Name == "nvidia-smi" {
			return runner.ScriptedResult{Stdout: []byte("1, NVIDIA GeForce RTX 5060 Ti, 15900, 16311\n")}
		}
		return runner.ScriptedResult{}
	})
	return fr
}

type enginesBody struct {
	Engines map[string]heartmula.Availability `json:"engines"`
}

func getEngines(t *testing.T, s *testServer) heartmula.Availability {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/music/engines", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var b enginesBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	return b.Engines["heartmula"]
}

func TestEngines_NothingInstalledMeansNotAvailable(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	var calls atomic.Int32
	s.heartMulaRunner = smiWith(&calls)
	s.heartMulaWeights = t.TempDir()
	a := getEngines(t, s)
	if a.Available || a.Reason != heartmula.ReasonNoService || a.Mode != heartmula.ModeLocal {
		t.Errorf("no service: %+v", a)
	}
}

func TestEngines_TheLocalEngineIsReportedPiece_byPiece(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	var calls atomic.Int32
	weights := t.TempDir()
	s.heartMulaWeights = weights
	s.engines.ttl = 1 // do not cache: each step asks again
	s.heartMulaRunner = smiWith(&calls)
	if a := getEngines(t, s); a.Reason != heartmula.ReasonNoService {
		t.Errorf("no service: %+v", a)
	}
	installServices(t, s)
	if a := getEngines(t, s); a.Reason != heartmula.ReasonNoWeights || len(a.Missing) == 0 {
		t.Errorf("no weights: %+v", a)
	}
	writeHeartMulaWeights(t, weights)
	if a := getEngines(t, s); a.Reason != heartmula.ReasonNotRunning || a.Container == nil || a.Container.State != "stopped" || a.Mode != heartmula.ModeLocal || a.Version != heartmula.Version {
		t.Errorf("everything there (ready, but the service is stopped): %+v", a)
	}
}

func TestEngines_AnswerIsCachedAndDroppedByASettingsChange(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	var calls atomic.Int32
	s.heartMulaRunner = smiWith(&calls)
	s.heartMulaWeights = t.TempDir()
	writeHeartMulaWeights(t, s.heartMulaWeights)
	installServices(t, s)
	getEngines(t, s)
	first := calls.Load()
	if first == 0 {
		t.Fatal("the first answer asks the cards")
	}
	getEngines(t, s)
	if calls.Load() != first {
		t.Errorf("a second answer within 30 s asks nothing again: %d -> %d", first, calls.Load())
	}
	rec := doJSON(t, s.Handler(), http.MethodPut, "/api/settings", map[string]any{"heartmula_host": "127.0.0.1:1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	if a := getEngines(t, s); a.Mode != heartmula.ModeExternal {
		t.Errorf("a settings change drops the cache: %+v", a)
	}
}

// fakeHeartMulaService answers /health like the container does.
func fakeHeartMulaService(t *testing.T, token string, ready bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		h := heartmula.Health{Ready: ready, Version: "1"}
		if ready {
			h.GPU = &heartmula.GPU{Name: "RTX 5060 Ti", FreeMB: 15000, TotalMB: 16311}
		} else {
			h.Missing = []string{"HeartCodec-oss"}
		}
		json.NewEncoder(w).Encode(h)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSettings_ExternalHeartMulaAddressTokenAndConsent(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	var calls atomic.Int32
	s.heartMulaRunner = smiWith(&calls)
	s.heartMulaWeights = t.TempDir()
	srv := fakeHeartMulaService(t, "tok", true)
	h := s.Handler()

	// An address that is invalid is refused with the reason.
	if rec := doJSON(t, h, http.MethodPut, "/api/settings", map[string]any{"heartmula_host": "ftp://x"}); rec.Code != http.StatusBadRequest {
		t.Errorf("a bad address: %d", rec.Code)
	}
	// Saved: the token is stored but never shown.
	rec := doJSON(t, h, http.MethodPut, "/api/settings", map[string]any{"heartmula_host": srv.URL, "heartmula_token": "tok"})
	if rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		HeartMula heartMulaSettingsView `json:"heartmula"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out.HeartMula.Saved != srv.URL || !out.HeartMula.TokenSet || strings.Contains(rec.Body.String(), `"tok"`) {
		t.Errorf("the address is saved and the token is not shown: %s", rec.Body)
	}
	// 127.0.0.1 is this machine, so no acceptance is needed and the service is available.
	if a := getEngines(t, s); !a.Available || a.Mode != heartmula.ModeExternal {
		t.Errorf("a service on this machine: %+v", a)
	}
	// Another machine needs the acceptance, tied to its address.
	doJSON(t, h, http.MethodPut, "/api/settings", map[string]any{"heartmula_host": "192.168.77.5:8787"})
	if a := getEngines(t, s); a.Available || a.Reason != heartmula.ReasonNoConsent {
		t.Errorf("not accepted: %+v", a)
	}
	rec = doJSON(t, h, http.MethodPut, "/api/settings", map[string]any{"heartmula_accept": true})
	json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusOK || !out.HeartMula.Consented || !out.HeartMula.Remote {
		t.Errorf("accepted: %d %s", rec.Code, rec.Body)
	}
	if a := getEngines(t, s); a.Reason == heartmula.ReasonNoConsent {
		t.Errorf("accepted for that address: %+v", a)
	}
	// Removing it goes back to the local engine.
	rec = doJSON(t, h, http.MethodPut, "/api/settings", map[string]any{"heartmula_host": ""})
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out.HeartMula.Saved != "" || out.HeartMula.TokenSet {
		t.Errorf("removed: %s", rec.Body)
	}
	if a := getEngines(t, s); a.Mode != heartmula.ModeLocal {
		t.Errorf("local again: %+v", a)
	}
	// Accepting with nothing saved is refused.
	if rec := doJSON(t, h, http.MethodPut, "/api/settings", map[string]any{"heartmula_accept": true}); rec.Code != http.StatusBadRequest {
		t.Errorf("nothing to accept: %d", rec.Code)
	}
}

func TestSettings_HeartMulaTest(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	h := s.Handler()
	ok := fakeHeartMulaService(t, "tok", true)
	notReady := fakeHeartMulaService(t, "", false)
	call := func(body map[string]any) heartMulaTestResult {
		rec := doJSON(t, h, http.MethodPost, "/api/settings/heartmula/test", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		var r heartMulaTestResult
		json.Unmarshal(rec.Body.Bytes(), &r)
		return r
	}
	if r := call(map[string]any{"host": ok.URL, "token": "tok"}); !r.OK || r.GPU != "RTX 5060 Ti" || r.FreeMB != 15000 || r.Version != "1" {
		t.Errorf("a ready service: %+v", r)
	}
	if r := call(map[string]any{"host": ok.URL, "token": "wrong"}); r.OK || r.Reason != heartmula.ReasonKeyRejected {
		t.Errorf("a wrong token: %+v", r)
	}
	if r := call(map[string]any{"host": notReady.URL}); r.OK || !strings.Contains(r.Error, "HeartCodec-oss") {
		t.Errorf("not ready: %+v", r)
	}
	if r := call(map[string]any{"host": "127.0.0.1:1"}); r.OK || r.Reason != heartmula.ReasonUnreachable {
		t.Errorf("nobody there: %+v", r)
	}
	if rec := doJSON(t, h, http.MethodPost, "/api/settings/heartmula/test", map[string]any{}); rec.Code != http.StatusBadRequest {
		t.Errorf("no address anywhere: %d", rec.Code)
	}
	// Without an address in the body the saved one is tested.
	doJSON(t, h, http.MethodPut, "/api/settings", map[string]any{"heartmula_host": ok.URL, "heartmula_token": "tok"})
	if r := call(map[string]any{}); !r.OK {
		t.Errorf("the saved address: %+v", r)
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the condition never came true")
}

func TestEnvironment_ReportsTheEngineAsOptionalAndNoDocker(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	var calls atomic.Int32
	s.heartMulaRunner = smiWith(&calls)
	s.heartMulaWeights = t.TempDir()
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/environment", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var rep struct {
		Binaries []struct {
			Name string `json:"name"`
		} `json:"binaries"`
		HeartMula *heartmula.Availability `json:"heartmula"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	for _, b := range rep.Binaries {
		if b.Name == "docker" {
			t.Error("nothing runs in docker any more: it is not looked for")
		}
	}
	if rep.HeartMula == nil || rep.HeartMula.Available || rep.HeartMula.Reason != heartmula.ReasonNoService {
		t.Errorf("the engine: %+v", rep.HeartMula)
	}
}

func TestAssets_HeartMulaWeightsAreAKindWithFoldersOfShards(t *testing.T) {
	k, ok := findKind("heartmula")
	if !ok || !k.Units {
		t.Fatalf("kind: %+v %v", k, ok)
	}
	for _, name := range []string{"config.json", "HeartMuLa-oss-3B/model-00001-of-00004.safetensors", "HeartCodec-oss/model.safetensors.index.json"} {
		if err := validateAssetName(k, name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, name := range []string{"../x.json", "a/b/c.safetensors", "HeartCodec-oss/run.sh"} {
		if err := validateAssetName(k, name); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	e, found := findCatalog("music-heartmula-3b")
	if !found || e.Kind != "heartmula" || len(e.Files) != 13 || e.SizeBytes < 20<<30 {
		t.Errorf("catalog entry: %+v", e)
	}
	// What the catalog installs is exactly what the engine looks for.
	dir := t.TempDir()
	for _, f := range e.Files {
		p := filepath.Join(dir, f.Name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		content := "x"
		if strings.HasSuffix(f.Name, "model.safetensors.index.json") {
			// the index names the shards the entry installs
			var shards []string
			for _, g := range e.Files {
				if strings.HasPrefix(g.Name, filepath.Dir(f.Name)+"/") && strings.HasSuffix(g.Name, ".safetensors") {
					shards = append(shards, `"w`+itoaInt(len(shards))+`":"`+filepath.Base(g.Name)+`"`)
				}
			}
			content = `{"weight_map":{` + strings.Join(shards, ",") + `}}`
		}
		os.WriteFile(p, []byte(content), 0o644)
	}
	if missing := heartmula.Missing(dir); len(missing) != 0 {
		t.Errorf("the entry installs everything the engine needs: missing %v", missing)
	}
}

func itoaInt(n int) string { b, _ := json.Marshal(n); return string(b) }

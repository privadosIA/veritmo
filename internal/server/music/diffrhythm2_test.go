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

	"veritmo/internal/music/diffrhythm2"
)

// writeDR2Weights writes a complete weights folder of the DiffRhythm2 engine: the downloaded checkpoint and the
// files the service reads.
func writeDR2Weights(t *testing.T, dir string) {
	t.Helper()
	for _, f := range []string{"config.json", "model.safetensors", "decoder.bin", "decoder.json", "decoder.safetensors", "mulan_text.safetensors", "mulan_text_tokenizer.json"} {
		os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644)
	}
	for _, m := range []string{"models--OpenMuQ--MuQ-MuLan-large", "models--OpenMuQ--MuQ-large-msd-iter", "models--xlm-roberta-base"} {
		snap := filepath.Join(dir, m, "snapshots", "abc")
		os.MkdirAll(snap, 0o755)
		os.WriteFile(filepath.Join(snap, "model.bin"), []byte("x"), 0o644)
	}
}

func getDR2Engine(t *testing.T, s *testServer) diffrhythm2.Availability {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/music/engines", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var b enginesBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	a, ok := b.Engines["diffrhythm2"]
	if !ok {
		t.Fatalf("the engines answer has no diffrhythm2: %s", rec.Body)
	}
	return a
}

func TestDR2Engines_TheLocalEngineIsReportedPieceByPiece(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	var calls atomic.Int32
	weights := t.TempDir()
	s.diffRhythm2Weights = weights
	s.heartMulaWeights = t.TempDir()
	s.engines.ttl = 1
	s.heartMulaRunner = smiWith(&calls)
	if a := getDR2Engine(t, s); a.Reason != diffrhythm2.ReasonNoService || a.Mode != diffrhythm2.ModeLocal {
		t.Errorf("no service: %+v", a)
	}
	installServices(t, s)
	if a := getDR2Engine(t, s); a.Reason != diffrhythm2.ReasonNoWeights || len(a.Missing) == 0 {
		t.Errorf("no weights: %+v", a)
	}
	writeDR2Weights(t, weights)
	if a := getDR2Engine(t, s); a.Reason != diffrhythm2.ReasonNotRunning || a.Container == nil || a.Container.State != "stopped" || a.Mode != diffrhythm2.ModeLocal || a.Version != diffrhythm2.Version {
		t.Errorf("everything there (ready, but the service is stopped): %+v", a)
	}
	// HeartMuLa is reported on its own: its weights are not there
	if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/music/engines", nil); !strings.Contains(rec.Body.String(), `"heartmula"`) {
		t.Errorf("both engines are reported: %s", rec.Body)
	}
}

func fakeDR2Service(t *testing.T, token string, ready bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		h := diffrhythm2.Health{Ready: ready, Version: "1"}
		if ready {
			h.GPU = &diffrhythm2.GPU{Name: "RTX 5060 Ti", FreeMB: 15000, TotalMB: 16311}
		} else {
			h.Missing = []string{"decoder.bin"}
		}
		json.NewEncoder(w).Encode(h)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDR2Settings_ExternalAddressTokenAndConsent(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	var calls atomic.Int32
	s.heartMulaRunner = smiWith(&calls)
	s.diffRhythm2Weights, s.heartMulaWeights = t.TempDir(), t.TempDir()
	srv := fakeDR2Service(t, "tok", true)
	h := s.Handler()

	if rec := doJSON(t, h, http.MethodPut, "/api/settings", map[string]any{"diffrhythm2_host": "ftp://x"}); rec.Code != http.StatusBadRequest {
		t.Errorf("a bad address: %d", rec.Code)
	}
	rec := doJSON(t, h, http.MethodPut, "/api/settings", map[string]any{"diffrhythm2_host": srv.URL, "diffrhythm2_token": "tok"})
	if rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		DiffRhythm2 diffRhythm2SettingsView `json:"diffrhythm2"`
		HeartMula   heartMulaSettingsView   `json:"heartmula"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out.DiffRhythm2.Saved != srv.URL || !out.DiffRhythm2.TokenSet || strings.Contains(rec.Body.String(), `"tok"`) {
		t.Errorf("the address is saved and the token is not shown: %s", rec.Body)
	}
	if out.HeartMula.Saved != "" {
		t.Errorf("HeartMuLa's address is its own: %+v", out.HeartMula)
	}
	if a := getDR2Engine(t, s); !a.Available || a.Mode != diffrhythm2.ModeExternal {
		t.Errorf("a service on this machine: %+v", a)
	}
	doJSON(t, h, http.MethodPut, "/api/settings", map[string]any{"diffrhythm2_host": "192.168.77.5:8788"})
	if a := getDR2Engine(t, s); a.Available || a.Reason != diffrhythm2.ReasonNoConsent {
		t.Errorf("not accepted: %+v", a)
	}
	rec = doJSON(t, h, http.MethodPut, "/api/settings", map[string]any{"diffrhythm2_accept": true})
	json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusOK || !out.DiffRhythm2.Consented || !out.DiffRhythm2.Remote {
		t.Errorf("accepted: %d %s", rec.Code, rec.Body)
	}
	rec = doJSON(t, h, http.MethodPut, "/api/settings", map[string]any{"diffrhythm2_host": ""})
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out.DiffRhythm2.Saved != "" || out.DiffRhythm2.TokenSet {
		t.Errorf("removed: %s", rec.Body)
	}
	if a := getDR2Engine(t, s); a.Mode != diffrhythm2.ModeLocal {
		t.Errorf("local again: %+v", a)
	}
	if rec := doJSON(t, h, http.MethodPut, "/api/settings", map[string]any{"diffrhythm2_accept": true}); rec.Code != http.StatusBadRequest {
		t.Errorf("nothing to accept: %d", rec.Code)
	}
}

func TestDR2Settings_Test(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	h := s.Handler()
	ok := fakeDR2Service(t, "tok", true)
	notReady := fakeDR2Service(t, "", false)
	call := func(body map[string]any) heartMulaTestResult {
		rec := doJSON(t, h, http.MethodPost, "/api/settings/diffrhythm2/test", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		var r heartMulaTestResult
		json.Unmarshal(rec.Body.Bytes(), &r)
		return r
	}
	if r := call(map[string]any{"host": ok.URL, "token": "tok"}); !r.OK || r.GPU != "RTX 5060 Ti" || r.Version != "1" {
		t.Errorf("a ready service: %+v", r)
	}
	if r := call(map[string]any{"host": ok.URL, "token": "wrong"}); r.OK || r.Reason != diffrhythm2.ReasonKeyRejected {
		t.Errorf("a wrong token: %+v", r)
	}
	if r := call(map[string]any{"host": notReady.URL}); r.OK || !strings.Contains(r.Error, "decoder.bin") {
		t.Errorf("not ready: %+v", r)
	}
	if r := call(map[string]any{"host": "127.0.0.1:1"}); r.OK || r.Reason != diffrhythm2.ReasonUnreachable {
		t.Errorf("nobody there: %+v", r)
	}
	if rec := doJSON(t, h, http.MethodPost, "/api/settings/diffrhythm2/test", map[string]any{}); rec.Code != http.StatusBadRequest {
		t.Errorf("no address anywhere: %d", rec.Code)
	}
	doJSON(t, h, http.MethodPut, "/api/settings", map[string]any{"diffrhythm2_host": ok.URL, "diffrhythm2_token": "tok"})
	if r := call(map[string]any{}); !r.OK {
		t.Errorf("the saved address: %+v", r)
	}
}

func TestDR2Environment_ReportsTheEngineAndTheCatalogEntryFollowsTheWeights(t *testing.T) {
	isolateSettings(t)
	s, _ := newProjectsServer(t)
	var calls atomic.Int32
	s.heartMulaRunner = smiWith(&calls)
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/environment", nil)
	if !strings.Contains(rec.Body.String(), `"diffrhythm2"`) {
		t.Errorf("the environment carries the engine: %s", rec.Body)
	}
	entry := func() (installed, found bool) {
		rec := doJSON(t, s.Handler(), http.MethodGet, "/api/assets/catalog", nil)
		var out struct {
			Entries []struct {
				ID        string `json:"id"`
				Kind      string `json:"kind"`
				Installed bool   `json:"installed"`
			} `json:"entries"`
		}
		json.Unmarshal(rec.Body.Bytes(), &out)
		for _, e := range out.Entries {
			if e.ID == "music-diffrhythm2" {
				return e.Installed, e.Kind == "diffrhythm2"
			}
		}
		return false, false
	}
	if inst, found := entry(); !found || inst {
		t.Errorf("listed and not installed: %v %v", inst, found)
	}
	wd, _ := os.Getwd() // the server's kind folder is relative to its working directory (the test chdirs into a repo)
	w := filepath.Join(wd, "models", "music", "diffrhythm2")
	os.MkdirAll(w, 0o755)
	writeDR2Weights(t, w)
	if inst, _ := entry(); !inst {
		t.Error("with the folder complete the entry is installed")
	}
}

// The stack gives the address of a generator by the environment: with nothing saved the engine is external and available, the token goes with it,
// and what is saved wins.
func TestDR2Settings_TheEnvironmentIsTheDefaultOfTheExternalService(t *testing.T) {
	isolateSettings(t)
	srv := fakeDR2Service(t, "tok-from-env", true)
	t.Setenv("DIFFRHYTHM2_HOST", srv.URL)
	t.Setenv("DIFFRHYTHM2_TOKEN", "tok-from-env")
	s, _ := newProjectsServer(t)
	var calls atomic.Int32
	s.heartMulaRunner = smiWith(&calls)
	s.diffRhythm2Weights, s.heartMulaWeights = t.TempDir(), t.TempDir()
	h := s.Handler()
	if a := getDR2Engine(t, s); !a.Available || a.Mode != diffrhythm2.ModeExternal {
		t.Fatalf("an address of the environment, nothing saved: %+v", a)
	}
	view := CurrentDiffRhythm2Settings()
	if view.Source != "env" || view.Saved != srv.URL || !view.TokenSet || !view.Consented {
		t.Fatalf("the settings say where it comes from, that a token is set, and need no click for it: %+v", view)
	}
	if raw, _ := json.Marshal(view); strings.Contains(string(raw), "tok-from-env") {
		t.Fatal("the token is in the view")
	}
	// what is saved wins
	doJSON(t, h, http.MethodPut, "/api/settings", map[string]any{"diffrhythm2_host": ""})
	if a := getDR2Engine(t, s); a.Mode != diffrhythm2.ModeExternal {
		t.Fatalf("clearing what is saved goes back to the environment: %+v", a)
	}
	doJSON(t, h, http.MethodPut, "/api/settings", map[string]any{"diffrhythm2_host": "192.168.77.5:8788"})
	if view = CurrentDiffRhythm2Settings(); view.Source != "setting" || view.Saved == srv.URL {
		t.Fatalf("saved wins: %+v", view)
	}
}

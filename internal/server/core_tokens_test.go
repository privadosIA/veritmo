package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"veritmo/internal/server/models"
)

// tokenRig is a download server whose key files live in a throwaway HOME, with
// every key variable cleared so only what a test sets is in play.
type tokenRig struct {
	s    *Server
	home string
	log  authLog
	all  strings.Builder // every response body, to look for leaks
}

func newTokenRig(t *testing.T) *tokenRig {
	t.Helper()
	r := &tokenRig{home: t.TempDir()}
	t.Setenv("HOME", r.home)
	for _, v := range []string{"HF_HOME", "XDG_CACHE_HOME", "HF_TOKEN", "CIVITAI_TOKEN"} {
		t.Setenv(v, "")
	}
	r.s = newDLServer(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.log.add(req.Host, req.Header.Get("Authorization"))
		_, _ = w.Write([]byte("payload"))
	}))
	t.Cleanup(srv.Close)
	routeAll(t, r.s, srv)
	return r
}

func (r *tokenRig) civitaiFile() string { return filepath.Join(r.home, ".config", "civitai-token") }
func (r *tokenRig) hfFile() string      { return filepath.Join(r.home, ".cache", "huggingface", "token") }

func (r *tokenRig) put(service, key string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(map[string]string{"token": key})
	rec := call(r.s.Handler(), http.MethodPut, "/api/tokens/"+service, string(b))
	r.all.WriteString(rec.Body.String())
	return rec
}

func (r *tokenRig) del(service string) *httptest.ResponseRecorder {
	rec := call(r.s.Handler(), http.MethodDelete, "/api/tokens/"+service, "")
	r.all.WriteString(rec.Body.String())
	return rec
}

func (r *tokenRig) report(t *testing.T) (tokens tokenStatus, sources tokenSources, raw string) {
	t.Helper()
	rec := call(r.s.Handler(), http.MethodGet, "/api/environment", "")
	r.all.WriteString(rec.Body.String())
	var out struct {
		Tokens       tokenStatus  `json:"tokens"`
		TokenSources tokenSources `json:"token_sources"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("environment: %v: %s", err, rec.Body.String())
	}
	return out.Tokens, out.TokenSources, rec.Body.String()
}

func readFileT(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTokens_SaveHuggingFaceKeyUsesTheStandardFile(t *testing.T) {
	r := newTokenRig(t)
	if rec := r.put("huggingface", "hf_abcdefgh"); rec.Code != http.StatusNoContent {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	if got := readFileT(t, r.hfFile()); got != "hf_abcdefgh" {
		t.Errorf("file holds %q", got)
	}
	startDL(t, r.s, `{"kind":"model","url":"http://huggingface.co/x/y/resolve/main/m.safetensors"}`)
	if got := r.log.get("huggingface.co"); len(got) != 1 || got[0] != "Bearer hf_abcdefgh" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestTokens_HuggingFaceFileFollowsHFHomeAndXDG(t *testing.T) {
	r := newTokenRig(t)
	hfHome := t.TempDir()
	t.Setenv("HF_HOME", hfHome)
	r.put("huggingface", "hf_in_hf_home")
	if got := readFileT(t, filepath.Join(hfHome, "token")); got != "hf_in_hf_home" {
		t.Errorf("HF_HOME file: %q", got)
	}
	t.Setenv("HF_HOME", "")
	xdg := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", xdg)
	r.put("huggingface", "hf_in_xdg_cache")
	if got := readFileT(t, filepath.Join(xdg, "huggingface", "token")); got != "hf_in_xdg_cache" {
		t.Errorf("XDG file: %q", got)
	}
}

func TestTokens_TheEnvironmentStillWinsOverTheFile(t *testing.T) {
	r := newTokenRig(t)
	t.Setenv("HF_TOKEN", "hf_from_environment")
	r.put("huggingface", "hf_from_the_file")
	startDL(t, r.s, `{"kind":"model","url":"http://huggingface.co/x/y/resolve/main/m.safetensors"}`)
	if got := r.log.get("huggingface.co"); len(got) != 1 || got[0] != "Bearer hf_from_environment" {
		t.Errorf("Authorization = %q, want the environment variable's value", got)
	}
	if _, src, _ := r.report(t); src.HuggingFace != "env" {
		t.Errorf("source = %q, want env", src.HuggingFace)
	}
}

func TestTokens_ASymlinkIsNotOverwritten(t *testing.T) {
	r := newTokenRig(t)
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(r.civitaiFile()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, r.civitaiFile()); err != nil {
		t.Fatal(err)
	}
	rec := r.put("civitai", "civ_through_link")
	if rec.Code == http.StatusNoContent {
		t.Fatalf("a symlinked key file must be refused")
	}
	if got := readFileT(t, target); got != "untouched" {
		t.Errorf("the link target was modified: %q", got)
	}
	if fi, _ := os.Lstat(r.civitaiFile()); fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link itself must be left alone")
	}
}

func TestTokens_RemovingWhenOnlyTheEnvironmentProvidesTheKeyIsRefused(t *testing.T) {
	r := newTokenRig(t)
	t.Setenv("HF_TOKEN", "hf_only_in_env")
	rec := r.del("huggingface")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "HF_TOKEN") {
		t.Errorf("DELETE: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "hf_only_in_env") {
		t.Errorf("the key leaked: %s", rec.Body)
	}
}

func TestTokens_NoAnswerEverContainsAKeyOrAFilePath(t *testing.T) {
	r := newTokenRig(t)
	t.Setenv("HF_TOKEN", "hf_secret_from_env")
	r.put("civitai", "civ_secret_saved")
	r.put("civitai", "bad key")
	r.put("huggingface", "hf_secret_saved")
	r.del("huggingface")
	r.del("civitai")
	r.report(t)
	out := r.all.String()
	for _, secret := range []string{"hf_secret_from_env", "civ_secret_saved", "hf_secret_saved", r.home, "civitai-token"} {
		if strings.Contains(out, secret) {
			t.Errorf("an answer contains %q", secret)
		}
	}
}

func TestTokens_AccessGuidancePointsToTheModelsTab(t *testing.T) {
	if m := models.HFAccessMessage(401, false); !strings.Contains(m, "Models tab") || strings.Contains(m, "start the server") || strings.Contains(m, "restart") {
		t.Errorf("hf without key: %s", m)
	}
	if m := models.HFAccessMessage(401, true); !strings.Contains(m, "Models tab") || strings.Contains(m, "restart") {
		t.Errorf("hf with key: %s", m)
	}
	if m := models.CivitaiAccessMessage(401, false); !strings.Contains(m, "Models tab") {
		t.Errorf("civitai without key: %s", m)
	}
}

package server

// core_tokens.go saves and removes the download keys (Hugging Face, Civitai) from
// the API (see specs/model-management, "Download keys can be saved and
// removed through the API"). There is deliberately no read operation: a key
// goes in and never comes back. Keys live in files with permissions 0600; an
// environment variable still wins over the file.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"veritmo/internal/platform/edition"
	"veritmo/internal/server/api"
	"veritmo/internal/server/models"
)

const (
	minKeyLen = 8
	maxKeyLen = 512
)

type keyService struct {
	name string
	env  string
	file func() string
}

var keyServices = map[string]keyService{
	"huggingface": {"huggingface", "HF_TOKEN", hfTokenFile},
	"civitai":     {"civitai", "CIVITAI_TOKEN", models.CivitaiTokenFile},
}

// hfTokenFile is the standard Hugging Face token file, shared with its own
// tools: $HF_HOME/token, else $XDG_CACHE_HOME/huggingface/token, else
// ~/.cache/huggingface/token.
func hfTokenFile() string {
	if v := os.Getenv("HF_HOME"); v != "" {
		return filepath.Join(v, "token")
	}
	if v := os.Getenv("XDG_CACHE_HOME"); v != "" {
		return filepath.Join(v, "huggingface", "token")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".cache", "huggingface", "token")
}

func (k keyService) envValue() string { return strings.TrimSpace(os.Getenv(k.env)) }

func (k keyService) fileValue() string {
	path := k.file()
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// effective is the key a download uses: the environment variable, else the file.
func (k keyService) effective() string {
	if v := k.envValue(); v != "" {
		return v
	}
	return k.fileValue()
}

// source says where the key in use comes from: "env", "file" or "".
func (k keyService) source() string {
	switch {
	case k.envValue() != "":
		return "env"
	case k.fileValue() != "":
		return "file"
	}
	return ""
}

// errSaveKey is the answer for a key that could not be written: it never says why, as the reason can hold a path.
var errSaveKey = errors.New("cannot save the key")

// validKey checks only the shape: printable ASCII without spaces, so a key
// can neither break its file nor inject a header.
func validKey(s string) error {
	if len(s) < minKeyLen || len(s) > maxKeyLen {
		return fmt.Errorf("the key must be between %d and %d characters long", minKeyLen, maxKeyLen)
	}
	for i := 0; i < len(s); i++ {
		if s[i] <= 0x20 || s[i] >= 0x7f {
			return errors.New("the key cannot contain spaces, line breaks or control characters")
		}
	}
	return nil
}

// writeKeyFile replaces the key file atomically with permissions 0600. A
// symbolic link at the destination is refused: renaming over it would replace
// the link, not the file it points to.
func writeKeyFile(path, key string) error {
	if path == "" {
		return errors.New("there is no user directory to keep the key in")
	}
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return errors.New("the key file is a symbolic link: it is not overwritten")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cannot create the folder of the key: %w", redactPathErr(err))
	}
	tmp, err := os.CreateTemp(dir, ".key-*")
	if err != nil {
		return fmt.Errorf("%w: %w", errSaveKey, redactPathErr(err))
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		cleanup()
		return errSaveKey
	}
	if _, err := tmp.WriteString(key); err != nil {
		tmp.Close()
		cleanup()
		return errSaveKey
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return errSaveKey
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return errSaveKey
	}
	if err := os.Rename(name, path); err != nil {
		cleanup()
		return errSaveKey
	}
	return nil
}

// redactPathErr keeps the reason (permission denied, read-only...) without the
// path, which the spec keeps out of every answer.
func redactPathErr(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

var tokenMu sync.Mutex

func keyServiceOf(w http.ResponseWriter, r *http.Request) (keyService, bool) {
	name := r.PathValue("service")
	if name == "civitai" && !edition.CivitaiEnabled() {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, "this edition has no Civitai key")
		return keyService{}, false
	}
	svc, ok := keyServices[name]
	if !ok {
		use := "huggingface"
		if edition.CivitaiEnabled() {
			use = "huggingface o civitai"
		}
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "servicio desconocido: usa "+use)
	}
	return svc, ok
}

// PUT /api/tokens/{service}  {"token": "..."}
func (s *Server) handleTokenPut(w http.ResponseWriter, r *http.Request) {
	svc, ok := keyServiceOf(w, r)
	if !ok {
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 4096))
	if err := dec.Decode(&body); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "invalid body: expected {\"token\": \"...\"}")
		return
	}
	key := strings.TrimSpace(body.Token)
	if err := validKey(key); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	tokenMu.Lock()
	defer tokenMu.Unlock()
	if err := writeKeyFile(svc.file(), key); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/tokens/{service}
func (s *Server) handleTokenDelete(w http.ResponseWriter, r *http.Request) {
	svc, ok := keyServiceOf(w, r)
	if !ok {
		return
	}
	tokenMu.Lock()
	defer tokenMu.Unlock()
	path := svc.file()
	if path != "" {
		err := os.Remove(path)
		switch {
		case err == nil:
			w.WriteHeader(http.StatusNoContent)
			return
		case !errors.Is(err, os.ErrNotExist):
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, "cannot remove the key")
			return
		}
	}
	if svc.envValue() != "" {
		api.WriteError(w, http.StatusConflict, api.CodeConflict,
			fmt.Sprintf("the key comes from the environment variable %s: remove it when starting the server, it cannot be removed from here", svc.env))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

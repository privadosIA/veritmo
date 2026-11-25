package models

// assets.go implements the inventory half of the model-management
// capability: the asset kinds and their directories, name validation, the
// recommended catalog as installed on disk and safe deletion (see
// specs/model-management). Downloads live in downloads.go.

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"veritmo/internal/music/diffrhythm2"
	"veritmo/internal/server/api"
	"veritmo/internal/server/modelcat"
	"veritmo/internal/server/workspace"
)

// AssetKind describes one managed asset kind: where it lives and which
// file extensions may be installed.
type AssetKind struct {
	Kind       string   `json:"kind"`
	Dir        string   `json:"dir"`
	Extensions []string `json:"extensions"`
	Note       string   `json:"note,omitempty"`
	// Units marks a kind whose assets are a file or a folder, listed and
	// deleted as one (a music adapter). Its files may be named
	// "<folder>/<file>": one folder level, each part a bare name.
	Units bool `json:"-"`
}

var assetKinds = []AssetKind{
	{Kind: "model", Dir: "models", Extensions: []string{".safetensors", ".ckpt", ".gguf"}},
	{Kind: "voice", Dir: "voices", Extensions: []string{".onnx", ".onnx.json"}},
	{Kind: "lora", Dir: filepath.Join("models", "loras"), Extensions: []string{".safetensors", ".ckpt", ".gguf"},
		Note: "The pipeline does not apply LoRA yet: the files are stored and listed, but not used when rendering."},
	// Video-generation models are bundles of files (diffusion model, text
	// encoder, VAE) kept flat in one directory; subdirectories of models/
	// are not listed as models, so the editor never offers them.
	{Kind: "video", Dir: filepath.Join("models", "video"), Extensions: []string{".safetensors", ".gguf"},
		Note: "Animation models: Wan or the AnimateDiff motion module. A project uses them from its animation block (defaults.animate)."},
	// Music models (acestep.cpp) are bundles of GGUF files kept flat in one
	// directory, which the engine scans by file name: language model, text
	// encoder, synthesis model and VAE.
	{Kind: "music", Dir: filepath.Join("models", "music"), Extensions: []string{".gguf"}},
	// Music adapters (LoRAs) are either one .safetensors file or a folder with
	// adapter_model.safetensors and adapter_config.json; the engine merges them
	// into the synthesis model.
	{Kind: "adapter", Dir: filepath.Join("models", "music", "adapters"), Extensions: []string{".safetensors", ".json"}, Units: true},
	// The weights of the optional HeartMuLa music engine (a container reads them from here): three small files and
	// two folders, one per model, with their shards.
	{Kind: "heartmula", Dir: filepath.Join("models", "music", "heartmula"), Extensions: []string{".safetensors", ".json"}, Units: true,
		Note: "Used by the HeartMuLa music engine, which runs in a container."},
	// The weights of the optional DiffRhythm2 music engine: its own files and the hub folders of the three models it
	// reads offline. The engine's build downloads them (see music_diffrhythm2.go).
	// The Whisper model that reads the lyrics of a song (whisper-cli, on the CPU).
	{Kind: "transcription", Dir: filepath.Join("models", "whisper"), Extensions: []string{".bin"},
		Note: "Used to read the lyrics of a song with whisper-cli."},
	// The weights of the voice separator (demucs.cpp, on the CPU) that gives the words of a song their times.
	{Kind: "separation", Dir: filepath.Join("models", "demucs"), Extensions: []string{".bin"},
		Note: "Used to separate the voice of a song with demucs.cpp, so its words are timed from the voice alone."},
	// The two ONNX models of the vision service of the video-edit tools (dotnet/video-vision). They sit in the same
	// folder as the video models; the extension tells them apart, as the video kind lists none of them.
	{Kind: "vision", Dir: filepath.Join("models", "video"), Extensions: []string{".onnx"},
		Note: "Used by the vision service of the video-edit tools (person mask and depth), which runs on the CPU."},
	{Kind: "diffrhythm2", Dir: filepath.Join("models", "music", "diffrhythm2"), Extensions: []string{".safetensors", ".json", ".bin"}, Units: true,
		Note: "Used by the DiffRhythm2 music engine, which runs in a container."},
}

// FindKind is the asset kind with that name.
func FindKind(name string) (AssetKind, bool) {
	for _, k := range assetKinds {
		if k.Kind == name {
			return k, true
		}
	}
	return AssetKind{}, false
}

// ValidateAssetName accepts only a bare file name with an extension from
// the kind's allow-list. For a kind of units it also accepts
// "<folder>/<file>", each part a bare name.
func ValidateAssetName(k AssetKind, name string) error {
	if k.Units && strings.Count(name, "/") == 1 {
		folder, file, _ := strings.Cut(name, "/")
		if err := validateBareName(folder); err != nil {
			return err
		}
		if err := validateBareName(file); err != nil {
			return err
		}
		return validateExtension(k, file)
	}
	if err := validateBareName(name); err != nil {
		return err
	}
	return validateExtension(k, name)
}

// validateBareName rejects anything that is not a plain file or folder name.
func validateBareName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("asset name is empty")
	case name == "." || name == "..":
		return fmt.Errorf("invalid asset name %q", name)
	case strings.ContainsAny(name, `/\`) || strings.ContainsRune(name, 0):
		return fmt.Errorf("asset name %q must be a bare file name, not a path", name)
	case strings.HasPrefix(name, "."):
		return fmt.Errorf("asset name %q must not start with a dot", name)
	}
	return nil
}

// validateExtension checks the extension against the kind's allow-list.
func validateExtension(k AssetKind, name string) error {
	lower := strings.ToLower(name)
	for _, ext := range k.Extensions {
		if strings.HasSuffix(lower, ext) {
			return nil
		}
	}
	return fmt.Errorf("extension of %q is not allowed for kind %s (allowed: %s)", name, k.Kind, strings.Join(k.Extensions, ", "))
}

// KindDir resolves a kind's directory under the server's assets root.
func (h *Handler) KindDir(k AssetKind) string { return filepath.Join(h.host.AssetsRoot(), k.Dir) }

// assetPath is the validated location of a named asset inside its kind
// directory.
func (h *Handler) assetPath(k AssetKind, name string) (string, error) {
	if err := ValidateAssetName(k, name); err != nil {
		return "", err
	}
	dir := h.KindDir(k)
	p := filepath.Join(dir, name)
	parent := filepath.Dir(p)
	if parent != filepath.Clean(dir) && (!k.Units || filepath.Dir(parent) != filepath.Clean(dir)) {
		return "", fmt.Errorf("asset name %q escapes %s", name, k.Dir)
	}
	return p, nil
}

type assetKindListing struct {
	AssetKind
	Assets []workspace.AssetEntry `json:"assets"`
}

// GET /api/assets
func (h *Handler) handleAssetsList(w http.ResponseWriter, r *http.Request) {
	out := make([]assetKindListing, 0, len(assetKinds))
	for _, k := range assetKinds {
		assets := workspace.ListAssets(h.KindDir(k))
		if k.Units {
			assets = workspace.ListAdapters(h.KindDir(k))
		}
		kept := assets[:0]
		for _, a := range assets {
			if !strings.HasSuffix(a.Name, ".partial") {
				kept = append(kept, a)
			}
		}
		out = append(out, assetKindListing{AssetKind: k, Assets: kept})
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"kinds": out})
}

// ---------------------------------------------------------------------------
// catalog
// ---------------------------------------------------------------------------

// InstalledCatalog is the built-in catalog with `installed` set from the files on disk.
func (h *Handler) InstalledCatalog() []modelcat.Entry {
	catalog := h.host.Catalog()
	out := make([]modelcat.Entry, 0, len(catalog))
	for _, e := range catalog {
		k, ok := FindKind(e.Kind)
		e.Installed = ok && len(e.Files) > 0
		if ok && e.Kind == "diffrhythm2" {
			// The catalog lists only the downloaded files; the service reads the converted ones that `diffrhythm2 install`
			// makes after the download, so it is installed when the engine's own check passes.
			e.Installed = len(diffrhythm2.MissingForService(h.KindDir(k))) == 0
			out = append(out, e)
			continue
		}
		for _, f := range e.Files {
			if !ok {
				break
			}
			if fi, err := os.Stat(filepath.Join(h.KindDir(k), f.Name)); err != nil || !fi.Mode().IsRegular() {
				e.Installed = false
				break
			}
		}
		out = append(out, e)
	}
	return out
}

// WithoutHiddenAdult leaves out the models made for adults when the server hides adult content (they do not exist
// for the API then: not in the catalog, not among the models of a generation or an edit).
func (h *Handler) WithoutHiddenAdult(entries []modelcat.Entry) []modelcat.Entry {
	if h.host.ShowsAdult() {
		return entries
	}
	shown := make([]modelcat.Entry, 0, len(entries))
	for _, e := range entries {
		if !e.Adult {
			shown = append(shown, e)
		}
	}
	return shown
}

// GET /api/assets/catalog
func (h *Handler) handleAssetsCatalog(w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, http.StatusOK, map[string]any{"entries": h.WithoutHiddenAdult(h.InstalledCatalog())})
}

// ---------------------------------------------------------------------------
// delete
// ---------------------------------------------------------------------------

// DELETE /api/assets/{kind}/{name}
func (h *Handler) handleAssetDelete(w http.ResponseWriter, r *http.Request) {
	k, ok := FindKind(r.PathValue("kind"))
	if !ok {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("unknown asset kind %q", r.PathValue("kind")))
		return
	}
	name := r.PathValue("name")
	if k.Units {
		h.deleteUnit(w, k, name)
		return
	}
	p, err := h.assetPath(k, name)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("%s %q is not installed", k.Kind, name))
		return
	}
	if id, busy := h.host.AnyBusy(); busy {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("run %s is queued or active: assets cannot be deleted while a run may be using them", id))
		return
	}
	if err := os.Remove(p); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, fmt.Sprintf("cannot delete %s: %v", name, err))
		return
	}
	if k.Kind == "voice" && strings.HasSuffix(name, ".onnx") {
		_ = os.Remove(p + ".json")
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteUnit removes an asset of a kind of units: a file, or a folder with
// everything in it. The name is a bare name, so it cannot leave the kind's
// directory.
func (h *Handler) deleteUnit(w http.ResponseWriter, k AssetKind, name string) {
	if err := validateBareName(name); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	p := filepath.Join(h.KindDir(k), name)
	if _, err := os.Stat(p); err != nil {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("%s %q is not installed", k.Kind, name))
		return
	}
	if id, busy := h.host.AnyBusy(); busy {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("run %s is queued or active: assets cannot be deleted while a run may be using them", id))
		return
	}
	if err := os.RemoveAll(p); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, fmt.Sprintf("cannot delete %s: %v", name, err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// findCatalog is the entry of the catalog with that id.
func (h *Handler) findCatalog(id string) (modelcat.Entry, bool) {
	for _, e := range h.host.Catalog() {
		if e.ID == id {
			return e, true
		}
	}
	return modelcat.Entry{}, false
}

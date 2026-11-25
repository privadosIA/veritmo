package generate

// fetch.go: the weights of the engines downloaded by this program, so the .NET services need no Docker to be installed. It is the same list of files, from
// the same places and with the same hashes, as docker/model3d/fetch.py (which stays for the container), plus the tetrahedra grid that only the service
// of Stable Fast 3D reads. The files of Stable Fast 3D are gated: they need the Hugging Face key of an account that accepted its licence, which this never
// accepts for anyone. Nothing is fetched unless the person asks for the install.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// CutoutURL is where the cut-out model comes from (the rembg release of u2net).
const CutoutURL = "https://github.com/danielgatis/rembg/releases/download/v0.0.0/u2net.onnx"

// TetsURL is where the tetrahedra grid of Stable Fast 3D comes from: a file of its repository (the container carries it inside its image).
const TetsURL = "https://raw.githubusercontent.com/Stability-AI/stable-fast-3d/main/load/tets/160_tets.npz"

// weightFile is one file of the weights folder: where it is downloaded from and the sha256 it must have (the files that were fetched and tried on 2026-11-24).
type weightFile struct {
	rel, repo, file, sha string
	url                  string // when set, the whole address (the cut-out model is not on Hugging Face)
	gated                bool   // needs the key of an account that accepted the licence
}

var tripoSRFiles = []weightFile{
	{rel: "rembg/u2net.onnx", url: CutoutURL, sha: "8d10d2f3bb75ae3b6d527c77944fc5e7dcd94b29809d47a739a7a728a912b491"},
	{rel: "triposr/config.yaml", repo: "stabilityai/TripoSR", file: "config.yaml", sha: "74ca708ce086bf68e97709ea6b3d91f14717921c04691e84043f0eb8fcc68e62"},
	{rel: "triposr/model.ckpt", repo: "stabilityai/TripoSR", file: "model.ckpt", sha: "429e2c6b22a0923967459de24d67f05962b235f79cde6b032aa7ed2ffcd970ee"},
	{rel: "triposr/dino-vitb16-config.json", repo: "facebook/dino-vitb16", file: "config.json", sha: "b87c0270b97db085fd82cf114a761fd0f62ae7914fbd407c752a2260646b689c"},
}

var sf3dFiles = []weightFile{
	{rel: "rembg/u2net.onnx", url: CutoutURL, sha: "8d10d2f3bb75ae3b6d527c77944fc5e7dcd94b29809d47a739a7a728a912b491"},
	{rel: "sf3d/config.yaml", repo: SF3DRepo, file: "config.yaml", gated: true, sha: "5e880cfec3dec28d4e20afe07bfa1799c8a561f56f0532ebf1b4022ae4ddc8e4"},
	{rel: "sf3d/model.safetensors", repo: SF3DRepo, file: "model.safetensors", gated: true, sha: "a3416e1cf654e7d4f5e75f116cec2c3f0a14501a77d30c2f6068bbda178de388"},
	{rel: "sf3d/dinov2-large-config.json", repo: "facebook/dinov2-large", file: "config.json", sha: "12df51c069a2dc1305e34ba71ef58bc2407ea553b75f4722a1715c1bce3bbed0"},
	{rel: "sf3d/160_tets.npz", url: TetsURL, sha: "1f4be37efc604d28d55a1a78c2aabefeeab7e63149f541aa45f9dd858ee35bb9"},
}

// Fetcher downloads the weights of the engines.
type Fetcher struct {
	// HTTP is the client (default: one that follows redirects, with no overall timeout: model.ckpt is 1.7 GB).
	HTTP *http.Client
	// HFBase is Hugging Face's address (default https://huggingface.co); CutoutURL and TetsURL replace the addresses of the cut-out model and of the
	// tetrahedra grid (the tests give theirs).
	HFBase, CutoutURL, TetsURL string
	files                      []weightFile // the tests give their own (with the hashes of their files)
}

func (f Fetcher) client() *http.Client {
	if f.HTTP != nil {
		return f.HTTP
	}
	return &http.Client{}
}

func (f Fetcher) address(w weightFile) string {
	switch {
	case w.url == CutoutURL && f.CutoutURL != "":
		return f.CutoutURL
	case w.url == TetsURL && f.TetsURL != "":
		return f.TetsURL
	case w.url != "":
		return w.url
	}
	base := strings.TrimRight(f.HFBase, "/")
	if base == "" {
		base = "https://huggingface.co"
	}
	return base + "/" + w.repo + "/resolve/main/" + w.file
}

func fileHash(path string) (string, error) {
	fh, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer fh.Close()
	h := sha256.New()
	if _, err := io.Copy(h, fh); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Fetch downloads what is missing of the weights of an engine for its .NET service into dir (a file that is there with the right hash is not
// downloaded again), writes each file to a .part and renames it when its hash is right, and says each step through log. A file whose hash differs is
// not kept. key is the Hugging Face key, used only for the gated files of Stable Fast 3D (without it, or when Hugging Face refuses it, this says so and stops).
func (f Fetcher) Fetch(ctx context.Context, engine, dir, key string, log func(string)) error {
	files := f.files
	if files == nil {
		switch engine {
		case EngineTripoSR:
			files = tripoSRFiles
		case EngineSF3D:
			files = sf3dFiles
		default:
			return fmt.Errorf("unknown engine %q", engine)
		}
	}
	key = strings.TrimSpace(key)
	for _, w := range files {
		dst := filepath.Join(dir, w.rel)
		if sum, err := fileHash(dst); err == nil && sum == w.sha {
			continue
		}
		if w.gated && key == "" {
			return fmt.Errorf("%s needs a Hugging Face key of an account that has accepted the licence of https://huggingface.co/%s: save it with PUT /api/tokens/huggingface", w.rel, SF3DRepo)
		}
		addr := f.address(w)
		log("step: " + addr)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		bearer := ""
		if w.gated {
			bearer = key
		}
		if err := f.download(ctx, addr, dst, w.sha, bearer); err != nil {
			return fmt.Errorf("%s: %w", w.rel, err)
		}
	}
	log("done")
	return nil
}

func (f Fetcher) download(ctx context.Context, addr, dst, want, bearer string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "veritmo")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := f.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized && bearer != "":
		return fmt.Errorf("Hugging Face does not know the saved key: save another one with PUT /api/tokens/huggingface")
	case resp.StatusCode == http.StatusForbidden && bearer != "":
		return fmt.Errorf("Hugging Face refuses the saved key for %s: its account has to accept the licence at https://huggingface.co/%s", SF3DRepo, SF3DRepo)
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("the server answered %d", resp.StatusCode)
	}
	tmp := dst + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(out, h), resp.Body)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		_ = os.Remove(tmp)
		return fmt.Errorf("it is not the file that was checked (its sha256 differs): the source changed, nothing was kept")
	}
	return os.Rename(tmp, dst)
}

// NeedsKey says whether any of the files given (relative to the weights folder) is one that needs the Hugging Face key of Stable Fast 3D.
func NeedsKey(rels []string) bool {
	for _, w := range sf3dFiles {
		if !w.gated {
			continue
		}
		for _, rel := range rels {
			if rel == w.rel {
				return true
			}
		}
	}
	return false
}

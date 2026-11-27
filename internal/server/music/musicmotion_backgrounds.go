package music

// musicmotion_backgrounds.go: the videos a piece keeps to put behind its pieces (spec music-motion, "A piece keeps video backgrounds"). They
// are files of the sources of the piece's project, where the render already looks for the clip of a scene. They come from an upload or from
// a finished generation of the generation API, are copied (never linked, so removing the generation changes nothing) and are listed with
// the pieces of the saved script that use them.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"veritmo/internal/music/musicmotion"
	"veritmo/internal/project/config"
	"veritmo/internal/server/api"
	"veritmo/internal/server/generation"
)

// backgroundFile is one entry of the list of a piece's backgrounds.
type backgroundFile struct {
	Name    string  `json:"name"`
	Seconds float64 `json:"seconds"`
	Width   int     `json:"width"`
	Height  int     `json:"height"`
	FPS     float64 `json:"fps"`
	Size    int64   `json:"size"`
	// Source is "upload" or "generation:<id>".
	Source string `json:"source"`
	// UsedBy are the ids of the pieces of the saved script that play it behind them; empty (not absent) for a background no piece uses.
	UsedBy []string `json:"used_by"`
}

// backgroundSources remembers where each background of a piece came from. It is kept in the piece's folder, apart from its sources.
type backgroundSources map[string]string

const backgroundSourcesFile = "backgrounds.json"

func (h *Handler) backgroundSourcesPath(id string) string {
	return filepath.Join(h.pieceDir(id), backgroundSourcesFile)
}

func (h *Handler) readBackgroundSources(id string) backgroundSources {
	out := backgroundSources{}
	if raw, err := os.ReadFile(h.backgroundSourcesPath(id)); err == nil {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

func (h *Handler) writeBackgroundSources(id string, s backgroundSources) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := h.backgroundSourcesPath(id) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, h.backgroundSourcesPath(id))
}

// ffprobeAvailable says whether a video can be read here. A test seam.
var ffprobeAvailable = func() bool { _, err := exec.LookPath("ffprobe"); return err == nil }

// clipInfoCache remembers what ffprobe said about a file while the file does not change.
type clipInfoCache struct {
	mu sync.Mutex
	m  map[string]cachedClip
}

type cachedClip struct {
	size int64
	mod  time.Time
	info musicmotion.ClipInfo
}

func (c *clipInfoCache) get(ctx context.Context, path string, fi os.FileInfo) (musicmotion.ClipInfo, error) {
	c.mu.Lock()
	hit, ok := c.m[path]
	c.mu.Unlock()
	if ok && hit.size == fi.Size() && hit.mod.Equal(fi.ModTime()) {
		return hit.info, nil
	}
	info, err := musicmotion.ProbeClip(ctx, path)
	if err != nil {
		return musicmotion.ClipInfo{}, err
	}
	c.mu.Lock()
	if c.m == nil {
		c.m = map[string]cachedClip{}
	}
	c.m[path] = cachedClip{size: fi.Size(), mod: fi.ModTime(), info: info}
	c.mu.Unlock()
	return info, nil
}

func badClipName(w http.ResponseWriter, name string) {
	api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid name %q: a background is a plain file name ending in %s (letters, digits, '.', '_' or '-', up to 63 characters)", name, strings.Join(musicmotion.ClipExts, ", ")))
}

// refuseClip answers for a file ffprobe cannot use: 503 when ffprobe is missing, 422 for what it cannot read or has no picture.
func refuseClip(w http.ResponseWriter, name string, err error) {
	switch {
	case errors.Is(err, musicmotion.ErrNoVideoStream):
		api.Refuse(w, "%q has no video stream: a background is a video", name)
	default:
		api.Refuse(w, "cannot read %q as a video: %v", name, err)
	}
}

// PUT /api/music-motion/pieces/{id}/backgrounds/{name}  (raw body: a video)
func (h *Handler) handlePieceBackgroundPut(w http.ResponseWriter, r *http.Request) {
	info, ok := h.pieceFromPath(w, r)
	if !ok || h.refuseIfRendering(w, info) {
		return
	}
	name := r.PathValue("name")
	if !musicmotion.ValidClipName(name) {
		badClipName(w, name)
		return
	}
	if !ffprobeAvailable() {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, "ffprobe is not installed: it is needed to read a video before it is kept")
		return
	}
	dir := h.pieceSources(info.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	dst := filepath.Join(dir, name)
	if _, err := os.Lstat(dst); err == nil {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("%q already exists in this piece: choose another name", name))
		return
	}
	api.RaiseBodyLimit(w, r, config.MaxSourceBytes+1)
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	defer os.Remove(tmp.Name())
	n, cerr := io.Copy(tmp, io.LimitReader(r.Body, config.MaxSourceBytes+1))
	if clerr := tmp.Close(); cerr == nil {
		cerr = clerr
	}
	switch {
	case cerr != nil:
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("cannot read the upload: %v", cerr))
		return
	case n == 0:
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "the file is empty")
		return
	case n > config.MaxSourceBytes:
		api.WriteError(w, http.StatusRequestEntityTooLarge, api.CodeInvalid, fmt.Sprintf("the file is larger than %d MB", config.MaxSourceBytes>>20))
		return
	}
	// a video the person says is for adults makes the piece one
	h.keepBackground(w, info, name, tmp.Name(), dst, "upload", r.URL.Query().Get("adult") == "true")
}

// keepBackground reads the video at tmp, moves it to dst and records where it came from: the common end of an upload and of a copy.
func (h *Handler) keepBackground(w http.ResponseWriter, info *pieceInfo, name, tmp, dst, source string, adult bool) {
	pieceID := info.ID
	if adult && !h.host.ShowsAdult() {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeContentNotAllowed, "this server does not keep content for adults")
		return
	}
	clip, err := musicmotion.ProbeClip(context.Background(), tmp)
	if err != nil {
		refuseClip(w, name, err)
		return
	}
	if err := os.Rename(tmp, dst); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	srcs := h.readBackgroundSources(pieceID)
	srcs[name] = source
	if err := h.writeBackgroundSources(pieceID, srcs); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	if adult {
		if err := h.markAdult(info); err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
			return
		}
	}
	api.WriteJSON(w, http.StatusCreated, map[string]any{"name": name, "seconds": clip.Seconds, "width": clip.Width, "height": clip.Height, "fps": clip.FPS})
}

// generationVideo is the path of the video of a finished generation and whether it is marked for adults (nil seam: the generation API's).
func (h *Handler) generationVideo(id string) (string, bool, error) {
	if h.generationVideoSeam != nil {
		return h.generationVideoSeam(id)
	}
	return h.gen.FinishedVideo(id)
}

// POST /api/music-motion/pieces/{id}/backgrounds  {"from": "generation:<id>", "name"?}
//
// Copies the video of a finished generation of the generation API into the piece. The copy is the piece's own: removing the generation
// does not change it.
func (h *Handler) handlePieceBackgroundAdd(w http.ResponseWriter, r *http.Request) {
	info, ok := h.pieceFromPath(w, r)
	if !ok || h.refuseIfRendering(w, info) {
		return
	}
	var req struct {
		From string `json:"from"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	genID, found := strings.CutPrefix(req.From, "generation:")
	if !found || genID == "" {
		api.Refuse(w, `from must be "generation:<id>", the id of a generation of the generation API (got %q)`, req.From)
		return
	}
	name := req.Name
	if name == "" {
		name = genID + ".mp4"
	}
	if !musicmotion.ValidClipName(name) {
		badClipName(w, name)
		return
	}
	if !ffprobeAvailable() {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, "ffprobe is not installed: it is needed to read a video before it is kept")
		return
	}
	src, adultClip, err := h.generationVideo(genID)
	switch {
	case errors.Is(err, generation.ErrGenerationNotFound):
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("generation %q not found", genID))
		return
	case errors.Is(err, generation.ErrGenerationNotDone):
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("generation %q: %v", genID, err))
		return
	case errors.Is(err, generation.ErrGenerationNoVideo):
		api.Refuse(w, "generation %q finished with no video", genID)
		return
	case err != nil:
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	dir := h.pieceSources(info.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	dst := filepath.Join(dir, name)
	if _, err := os.Lstat(dst); err == nil {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("%q already exists in this piece: choose another name", name))
		return
	}
	tmp, err := os.CreateTemp(dir, ".copy-*")
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	defer os.Remove(tmp.Name())
	in, err := os.Open(src)
	if err != nil {
		tmp.Close()
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	_, cerr := io.Copy(tmp, in)
	in.Close()
	if clerr := tmp.Close(); cerr == nil {
		cerr = clerr
	}
	if cerr != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, cerr.Error())
		return
	}
	h.keepBackground(w, info, name, tmp.Name(), dst, "generation:"+genID, adultClip)
}

// backgroundUse is, for each name, the ids of the pieces of the saved script that have an item of the video track that plays it over them.
func (h *Handler) backgroundUse(id string) map[string][]string {
	use := map[string][]string{}
	if script, ok := musicmotion.LoadScriptAt(h.place(id)); ok {
		for _, p := range script.Pieces {
			for _, it := range script.ItemsOver(p) {
				if !slices.Contains(use[it.Clip], p.ID) {
					use[it.Clip] = append(use[it.Clip], p.ID)
				}
			}
		}
	}
	return use
}

// GET /api/music-motion/pieces/{id}/backgrounds
func (h *Handler) handlePieceBackgroundsList(w http.ResponseWriter, r *http.Request) {
	info, ok := h.pieceFromPath(w, r)
	if !ok {
		return
	}
	out := []backgroundFile{}
	dir := h.pieceSources(info.ID)
	entries, _ := os.ReadDir(dir)
	srcs, use := h.readBackgroundSources(info.ID), h.backgroundUse(info.ID)
	for _, e := range entries {
		if !musicmotion.ValidClipName(e.Name()) || !e.Type().IsRegular() {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		clip, err := h.clips.get(r.Context(), filepath.Join(dir, e.Name()), fi)
		if err != nil {
			continue // not a video any more: it is left out of the list
		}
		source := srcs[e.Name()]
		if source == "" {
			source = "upload"
		}
		usedBy := use[e.Name()]
		if usedBy == nil {
			usedBy = []string{}
		}
		out = append(out, backgroundFile{Name: e.Name(), Seconds: clip.Seconds, Width: clip.Width, Height: clip.Height, FPS: clip.FPS, Size: fi.Size(), Source: source, UsedBy: usedBy})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	api.WriteJSON(w, http.StatusOK, map[string]any{"backgrounds": out})
}

// DELETE /api/music-motion/pieces/{id}/backgrounds/{name}
func (h *Handler) handlePieceBackgroundDelete(w http.ResponseWriter, r *http.Request) {
	info, ok := h.pieceFromPath(w, r)
	if !ok || h.refuseIfRendering(w, info) {
		return
	}
	name := r.PathValue("name")
	if !musicmotion.ValidClipName(name) {
		badClipName(w, name)
		return
	}
	path := filepath.Join(h.pieceSources(info.ID), name)
	if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("%q is not in this piece", name))
		return
	}
	if pieces := h.backgroundUse(info.ID)[name]; len(pieces) > 0 {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("the piece(s) %s of the script play %q behind them: change or remove their background first", strings.Join(pieces, ", "), name))
		return
	}
	if err := os.Remove(path); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	srcs := h.readBackgroundSources(info.ID)
	if _, had := srcs[name]; had {
		delete(srcs, name)
		_ = h.writeBackgroundSources(info.ID, srcs)
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/music-motion/pieces/{id}/backgrounds/{name}/file
//
// The video itself, with Range, an ETag and ?ticket= (a <video> cannot send a header), so a dashboard can play it under the transparent
// page of the piece.
func (h *Handler) handlePieceBackgroundFile(w http.ResponseWriter, r *http.Request) {
	info, ok := h.pieceFromPath(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if !musicmotion.ValidClipName(name) {
		badClipName(w, name)
		return
	}
	f, err := os.Open(filepath.Join(h.pieceSources(info.ID), name))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("%q is not in this piece", name))
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("%q is not in this piece", name))
		return
	}
	etag := fmt.Sprintf(`"%x-%x"`, fi.Size(), fi.ModTime().UnixNano())
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

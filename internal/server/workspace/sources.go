package workspace

// sources.go keeps the audio files a project's tracks transform (covers,
// repaints, extracts): its sources/ folder. Listing, uploading, copying an
// exported song in, and deleting; playing goes through the artifact endpoint
// (see specs/project-management).

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"veritmo/internal/assets3d"
	"veritmo/internal/assets3d/generate"
	"veritmo/internal/project/config"
	"veritmo/internal/project/genmeta"
	"veritmo/internal/server/api"
	"veritmo/internal/video/blender"
)

type sourceInfo struct {
	Name string `json:"name"`
	// Kind is "audio" (mp3, wav), "image", "video" (a clip a scene can use), "blend", "scene" (a description of a 3D scene),
	// "font" or "model" (a .glb).
	Kind string `json:"kind"`
	// UsedBy lists the scenes that use a video as their clip (empty: nobody does); only videos have it.
	UsedBy   *[]string `json:"used_by,omitempty"`
	Size     int64     `json:"size"`
	Modified string    `json:"modified"`
	// A model (kind "model") says what its file holds: the clips it can play, its triangles and the pictures of the sources it
	// refers to (the texture atlas of a pack, for one). They are only given when the file is a valid binary glTF.
	Clips     *[]string `json:"clips,omitempty"`
	Triangles *int      `json:"triangles,omitempty"`
	Needs     *[]string `json:"needs,omitempty"`
}

// SourcesDir is the sources folder of a project (its pictures, clips and songs).
func (h *Handler) SourcesDir(id string) string {
	return filepath.Join(h.ProjectDir(id), config.SourcesSubdir)
}

// SourceKind says what a source file is from its name: image, video, blend, scene (a .scene.json description), font or audio.
func SourceKind(name string) string {
	if config.ValidImageName(name) {
		return "image"
	}
	if config.ValidClipName(name) {
		return "video"
	}
	if config.ValidBlendName(name) {
		return "blend"
	}
	if config.ValidSceneSpecName(name) {
		return "scene"
	}
	if config.ValidFontName(name) {
		return "font"
	}
	if config.ValidModelName(name) {
		return "model"
	}
	return "audio"
}

func (h *Handler) listSources(id string) []sourceInfo {
	out := []sourceInfo{}
	used := h.clipUsers(id)
	entries, _ := os.ReadDir(h.SourcesDir(id))
	for _, e := range entries {
		if !e.Type().IsRegular() || !config.ValidSourceFile(e.Name()) {
			continue
		}
		if fi, err := e.Info(); err == nil {
			info := sourceInfo{Name: e.Name(), Kind: SourceKind(e.Name()), Size: fi.Size(), Modified: fi.ModTime().UTC().Format(time.RFC3339)}
			if info.Kind == "model" {
				if m, err := blender.ReadModel(filepath.Join(h.SourcesDir(id), e.Name())); err == nil {
					clips, needs := append([]string{}, m.Clips...), append([]string{}, m.Needs...)
					info.Clips, info.Triangles, info.Needs = &clips, &m.Triangles, &needs
				}
			}
			if info.Kind == "video" {
				scenes := used[e.Name()]
				if scenes == nil {
					scenes = []string{}
				}
				info.UsedBy = &scenes
			}
			out = append(out, info)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// GET /api/projects/{id}/sources
func (h *Handler) handleSourcesList(w http.ResponseWriter, r *http.Request) {
	id, ok := h.RequireProject(w, r)
	if !ok {
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"sources": h.listSources(id)})
}

// sourceName validates the name in the path; it answers 400 itself.
func sourceName(w http.ResponseWriter, name string) bool {
	if !config.ValidSourceFile(name) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid source name %q: use a plain file name ending in .mp3, .wav, .png, .jpg, .jpeg, .webp, .mp4, .mov, .webm, .mkv, .blend or .glb (letters, digits, '.', '_' or '-')", name))
		return false
	}
	return true
}

// writeSource stores r under sources/<name> without ever replacing a file.
func (h *Handler) writeSource(w http.ResponseWriter, id, name string, r io.Reader) bool {
	dir := h.SourcesDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return false
	}
	dst := filepath.Join(dir, name)
	if _, err := os.Lstat(dst); err == nil {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("source %q already exists: choose another name", name))
		return false
	}
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return false
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(r, config.MaxSourceBytes+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("cannot read the upload: %v", err))
		return false
	case n == 0:
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "the file is empty")
		return false
	case n > config.MaxSourceBytes:
		api.WriteError(w, http.StatusRequestEntityTooLarge, api.CodeInvalid, fmt.Sprintf("the file is larger than %d MB", config.MaxSourceBytes>>20))
		return false
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return false
	}
	return true
}

// videoExts are the uploads whose audio track becomes a source.
var videoExts = map[string]bool{".mp4": true, ".mov": true, ".mkv": true, ".webm": true, ".avi": true}

// PUT /api/projects/{id}/sources/{name}  (raw audio body, or a video whose
// audio is extracted)
func (h *Handler) handleSourcePut(w http.ResponseWriter, r *http.Request) {
	id, ok := h.RequireProject(w, r)
	if !ok {
		return
	}
	api.RaiseBodyLimit(w, r, config.MaxSourceBytes+1)
	name := r.PathValue("name")
	if r.URL.Query().Get("as") == "picture" {
		h.putPicture(w, r, id, name)
		return
	}
	if r.URL.Query().Get("as") == "clip" {
		if !config.ValidClipName(name) {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("%q is not a video: use a plain file name ending in .mp4, .mov, .webm or .mkv", name))
			return
		}
		if !h.writeSource(w, id, name, r.Body) {
			return
		}
		h.keepSizeOf(id) // the sources changed: the size kept with the project follows
		api.WriteJSON(w, http.StatusCreated, map[string]string{"name": name})
		return
	}
	if ext := strings.ToLower(filepath.Ext(name)); videoExts[ext] {
		h.putVideoSource(w, r, id, strings.TrimSuffix(name, filepath.Ext(name))+".mp3")
		return
	}
	if !sourceName(w, name) {
		return
	}
	body := io.Reader(r.Body)
	if config.ValidModelName(name) {
		// A model is checked before it is stored: a file that is not a binary glTF is refused now, not at the first run.
		data, err := io.ReadAll(io.LimitReader(r.Body, blender.MaxModelBytes+1))
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("cannot read the upload: %v", err))
			return
		}
		if _, err := blender.ParseModel(data); err != nil {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("%q is not a usable model: %v", name, err))
			return
		}
		body = bytes.NewReader(data)
	}
	if !h.writeSource(w, id, name, body) {
		return
	}
	h.keepSizeOf(id) // the sources changed: the size kept with the project follows
	api.WriteJSON(w, http.StatusCreated, map[string]string{"name": name})
}

// putVideoSource stores the audio of an uploaded video as sources/<target>
// (an mp3). The video itself is only a temporary file.
func (h *Handler) putVideoSource(w http.ResponseWriter, r *http.Request, id, target string) {
	if !sourceName(w, target) {
		return
	}
	if _, err := h.host.LookPath("ffmpeg"); err != nil {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, "ffmpeg is not installed: it is needed to take the audio out of a video")
		return
	}
	dir := h.SourcesDir(id)
	dst := filepath.Join(dir, target)
	if _, err := os.Lstat(dst); err == nil {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("source %q already exists: choose another name", target))
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	video, err := os.CreateTemp(dir, ".upload-*.video")
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	defer os.Remove(video.Name())
	n, err := io.Copy(video, io.LimitReader(r.Body, config.MaxSourceBytes+1))
	if cerr := video.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("cannot read the upload: %v", err))
		return
	case n == 0:
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "the file is empty")
		return
	case n > config.MaxSourceBytes:
		api.WriteError(w, http.StatusRequestEntityTooLarge, api.CodeInvalid, fmt.Sprintf("the file is larger than %d MB", config.MaxSourceBytes>>20))
		return
	}
	out := dst + ".part.mp3"
	defer os.Remove(out)
	args := []string{"-y", "-i", video.Name(), "-vn", "-ac", "2", "-ar", "48000", "-c:a", "libmp3lame", "-q:a", "2", out}
	if _, err := h.host.Runner().Run(r.Context(), "ffmpeg", args); err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, fmt.Sprintf("cannot take the audio out of this video (does it have sound?): %s", api.LastLines(err.Error(), 4)))
		return
	}
	if fi, err := os.Stat(out); err != nil || fi.Size() == 0 {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, "the video has no audio track")
		return
	}
	if err := os.Rename(out, dst); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	h.keepSizeOf(id) // the sources changed: the size kept with the project follows
	api.WriteJSON(w, http.StatusCreated, map[string]string{"name": target})
}

// POST /api/projects/{id}/sources  {"from": "out/<project>/<file>", "name": "..."}
func (h *Handler) handleSourceCopy(w http.ResponseWriter, r *http.Request) {
	id, ok := h.RequireProject(w, r)
	if !ok {
		return
	}
	var req struct {
		From string `json:"from"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	from := filepath.ToSlash(filepath.Clean(filepath.FromSlash(req.From)))
	if !strings.HasPrefix(from, "out/") || !config.ValidSourceName(filepath.Base(from)) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "from must be an .mp3 or .wav file under out/")
		return
	}
	full, ok := h.ConfinedArtifactPath(id, from)
	if !ok {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "invalid path")
		return
	}
	f, err := os.Open(full)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("%q does not exist", from))
		return
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("%q is not a file", from))
		return
	}
	name := req.Name
	if name == "" {
		name = filepath.Base(from)
	}
	if !sourceName(w, name) {
		return
	}
	if !h.writeSource(w, id, name, f) {
		return
	}
	h.keepSizeOf(id) // the sources changed: the size kept with the project follows
	api.WriteJSON(w, http.StatusCreated, map[string]string{"name": name})
}

// DELETE /api/projects/{id}/sources/{name}
func (h *Handler) handleSourceDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := h.RequireProject(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if !sourceName(w, name) {
		return
	}
	path := filepath.Join(h.SourcesDir(id), name)
	if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("source %q does not exist", name))
		return
	}
	if runID, busy := h.ProjectBusy(id); busy {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("project %q has an active or queued run (%s)", id, runID))
		return
	}
	if raw, err := h.ProjectDefinition(id); err == nil {
		if cfg, err := config.LoadBytes(raw); err == nil {
			if cfg.BGM != nil && cfg.BGM.File == name {
				api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("the background music uses source %q: change or remove it first", name))
				return
			}
			for _, sc := range cfg.Scenes {
				if sc.InitImage == name {
					api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("scene %q starts from the image %q: change or remove it first", sc.ID, name))
					return
				}
				if sc.Clip == name {
					api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("scene %q uses the clip %q: change or remove the scene's clip first", sc.ID, name))
					return
				}
				if sc.Image == name {
					api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("scene %q uses the image %q: change or remove the scene's image first", sc.ID, name))
					return
				}
			}
			for _, t := range cfg.Tracks {
				if t.Source == name || t.Reference == name {
					api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("track %q uses source %q: change or remove the track first", t.ID, name))
					return
				}
			}
			if config.ValidModelName(name) || config.ValidImageName(name) {
				// a 3D scene that places this model, or a model that needs this picture, is using it
				for _, sc := range cfg.Scenes {
					if sc.Blender == nil || !sc.Blender.IsBuild() {
						continue
					}
					raw, err := os.ReadFile(filepath.Join(h.SourcesDir(id), sc.Blender.Build))
					if err != nil {
						continue
					}
					if spec, err := blender.Decode(raw); err == nil && spec.UsesSource(h.SourcesDir(id), name) {
						api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("scene %q places %q (through %s): change or remove it first", sc.ID, name, sc.Blender.Build))
						return
					}
				}
			}
		}
	}
	if err := os.Remove(path); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	h.keepSizeOf(id) // the sources changed: the size kept with the project follows
	w.WriteHeader(http.StatusNoContent)
}

// copySources copies a project's sources folder into another project (used
// when duplicating), best-effort per file like the rest of the duplicate.
func (h *Handler) copySources(from, to string) error {
	entries, err := os.ReadDir(h.SourcesDir(from))
	if err != nil {
		return nil // no sources: nothing to copy
	}
	if err := os.MkdirAll(h.SourcesDir(to), 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		src, err := os.Open(filepath.Join(h.SourcesDir(from), e.Name()))
		if err != nil {
			return err
		}
		dst, err := os.Create(filepath.Join(h.SourcesDir(to), e.Name()))
		if err == nil {
			_, err = io.Copy(dst, src)
			if cerr := dst.Close(); err == nil {
				err = cerr
			}
		}
		src.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// pictureClipExts are the uploads that become their first frame when the
// upload asks for a picture.
var pictureClipExts = map[string]bool{".gif": true, ".mp4": true, ".mov": true, ".mkv": true, ".webm": true, ".avi": true}

// maxMetadataRead is how much of a stored picture is read for its metadata.
const maxMetadataRead = genmeta.MaxInspect

// putPicture handles PUT .../sources/{name}?as=picture: a picture is stored as
// it is, a GIF or a video becomes its first frame (sources/<stem>.png, the
// original is not kept). The answer carries the generation metadata found in
// the original file, when there is any.
func (h *Handler) putPicture(w http.ResponseWriter, r *http.Request, id, name string) {
	ext := strings.ToLower(filepath.Ext(name))
	if !pictureClipExts[ext] {
		if !sourceName(w, name) {
			return
		}
		if !config.ValidImageName(name) {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("%q is not a picture, a GIF or a video", name))
			return
		}
		if !h.writeSource(w, id, name, r.Body) {
			return
		}
		h.writePictureAnswer(w, name, readHead(filepath.Join(h.SourcesDir(id), name)))
		return
	}

	target := strings.TrimSuffix(name, filepath.Ext(name)) + ".png"
	if !sourceName(w, target) {
		return
	}
	if _, err := h.host.LookPath("ffmpeg"); err != nil {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, "ffmpeg is not installed: it is needed to take the first frame out of a GIF or a video")
		return
	}
	dir := h.SourcesDir(id)
	dst := filepath.Join(dir, target)
	if _, err := os.Lstat(dst); err == nil {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("source %q already exists: choose another name", target))
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	up, err := os.CreateTemp(dir, ".upload-*"+ext)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	defer os.Remove(up.Name())
	n, err := io.Copy(up, io.LimitReader(r.Body, config.MaxSourceBytes+1))
	if cerr := up.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("cannot read the upload: %v", err))
		return
	case n == 0:
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "the file is empty")
		return
	case n > config.MaxSourceBytes:
		api.WriteError(w, http.StatusRequestEntityTooLarge, api.CodeInvalid, fmt.Sprintf("the file is larger than %d MB", config.MaxSourceBytes>>20))
		return
	}

	// The metadata is read from the original: the clip is not kept.
	var meta genmeta.Metadata
	if ext == ".gif" {
		meta = readHead(up.Name())
	} else {
		meta = genmeta.FromVideo(r.Context(), h.host.Runner(), up.Name())
	}

	out := dst + ".part.png"
	defer os.Remove(out)
	args := []string{"-y", "-i", up.Name(), "-frames:v", "1", "-f", "image2", out}
	if _, err := h.host.Runner().Run(r.Context(), "ffmpeg", args); err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, fmt.Sprintf("cannot take the first frame out of this file: %s", api.LastLines(err.Error(), 4)))
		return
	}
	if fi, err := os.Stat(out); err != nil || fi.Size() == 0 {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, "cannot take the first frame out of this file")
		return
	}
	if err := os.Rename(out, dst); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	h.writePictureAnswer(w, target, meta)
}

// readHead reads the metadata of a stored picture, looking only at its first
// bytes; an unreadable file yields none.
func readHead(path string) genmeta.Metadata {
	f, err := os.Open(path)
	if err != nil {
		return genmeta.Metadata{}
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, maxMetadataRead))
	return genmeta.FromBytes(b)
}

func (h *Handler) writePictureAnswer(w http.ResponseWriter, name string, meta genmeta.Metadata) {
	out := map[string]any{"name": name}
	if !meta.Empty() {
		out["metadata"] = meta
	}
	api.WriteJSON(w, http.StatusCreated, out)
}

// SetModelLibrary gives the handler the library of 3D model packs: a model of it can be added to a project's sources.
func (h *Handler) SetModelLibrary(lib *assets3d.Library) {
	h.models3d = lib
	h.rm.models3d.Library = lib
}

// SetModelService gives the runs of the models3d mode the 3D service they make their models with (see add-models3d-mode): without it such a run fails
// saying there is none.
func (h *Handler) SetModelService(svc generate.Opener) { h.rm.models3d.Service = svc }

// POST /api/projects/{id}/sources/model  {"from": "<pack>/<model>", "name"?}
//
// Copies a model of the library into the project's sources with the pictures it needs (see assets3d.CopyModel): from then on it
// is an ordinary source, independent of the library.
func (h *Handler) handleSourceModel(w http.ResponseWriter, r *http.Request) {
	id, ok := h.RequireProject(w, r)
	if !ok {
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
	if h.models3d == nil {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeEngineNotRunning, "the library of 3D models is not available")
		return
	}
	if runID, busy := h.ProjectBusy(id); busy {
		api.WriteError(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("project %q has an active or queued run (%s)", id, runID))
		return
	}
	res, err := h.models3d.CopyModel(req.From, h.SourcesDir(id), req.Name)
	if err != nil {
		WriteModelCopyError(w, err)
		return
	}
	h.keepSizeOf(id) // the sources changed: the size kept with the project follows
	status := http.StatusCreated
	if res.Already {
		status = http.StatusOK
	}
	api.WriteJSON(w, status, res)
}

// WriteModelCopyError answers an error of assets3d.CopyModel with the status of its kind (used by the routes of a project and of a
// piece, which copy the same way).
func WriteModelCopyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, assets3d.ErrNotFound):
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, err.Error())
	case errors.Is(err, assets3d.ErrConflict):
		api.WriteError(w, http.StatusConflict, api.CodeConflict, err.Error())
	case errors.Is(err, assets3d.ErrInvalid):
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
	default:
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
	}
}

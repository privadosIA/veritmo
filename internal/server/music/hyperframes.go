package music

// hyperframes.go: the catalog of whole HyperFrames blocks (spec hyperframes-catalog).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"veritmo/internal/music/hyperframes"
	pstore "veritmo/internal/platform/store"
	"veritmo/internal/server/api"
	"veritmo/internal/video/motion"
)

// hfStore is the catalog of the blocks: their records are rows of the database and their files are in the folder of the blocks (HYPERFRAMES_DIR or
// hyperframes/); a test seam sets hfDir.
func (h *Handler) hfStore() hyperframes.Store {
	dir := h.hfDir
	if dir == "" {
		dir = hyperframes.ResolveDir(os.Getenv)
	}
	return hyperframes.NewStoreDB(dir, h.ws.Store())
}

// hfDrawer makes the drawer of blocks, or answers 503 without Chrome.
func (h *Handler) hfDrawer(w http.ResponseWriter) (hyperframes.Drawer, bool) {
	b, err := h.ws.MotionBrowserFor()
	if err != nil {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, "there is no Chrome or Chromium to draw the block: "+err.Error())
		return hyperframes.Drawer{}, false
	}
	return hyperframes.Drawer{Store: h.hfStore(), Browser: b.Path, LibsDir: h.hfLibs}, true
}

// HFBlock is a block as the catalog lists it.
type HFBlock struct {
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	License     string   `json:"license,omitempty"`
	Width       int      `json:"width"`
	Height      int      `json:"height"`
	Duration    float64  `json:"duration"`
	// State is ok, needs-files, needs-gpu, failed or unchecked (the last run of the minimum checks).
	State    string   `json:"state"`
	Problems []string `json:"problems,omitempty"`
	Edited   bool     `json:"edited,omitempty"`
	// Kind is "component" for a component of the registry; empty for a block. Family and Profile classify a component.
	Kind    string `json:"kind,omitempty"`
	Family  string `json:"family,omitempty"`
	Profile string `json:"profile,omitempty"`
	// Runtime is what drew the block in its last checks: upstream or builtin.
	Runtime string `json:"runtime,omitempty"`
	// Origin says where the block comes from, for the licence notice a screen shows.
	Origin    hyperframes.Origin `json:"origin"`
	Thumbnail string             `json:"thumbnail"`
}

func hfBlockOf(b hyperframes.Block) HFBlock {
	out := HFBlock{
		Name: b.Name, Title: b.Title, Description: b.Description, Tags: b.Tags, License: b.License, Width: b.Width, Height: b.Height,
		Duration: b.Duration, State: hyperframes.StateOf(b), Edited: b.Edited, Origin: b.Origin, Kind: b.Kind, Family: b.Family, Profile: b.Profile,
		Thumbnail: "/api/hyperframes/blocks/" + b.Name + "/thumbnail",
	}
	if b.Checks != nil {
		out.Problems = b.Checks.Problems
		out.Runtime = b.Checks.Runtime
	}
	return out
}

// HFBlockList is the answer of GET /api/hyperframes/blocks.
type HFBlockList struct {
	Blocks []HFBlock      `json:"blocks"`
	Total  int            `json:"total"`
	States map[string]int `json:"states"`
	// LibsInstalled says the libraries the blocks load are kept locally; if not, Fix is what to run.
	LibsInstalled bool   `json:"libs_installed"`
	Fix           string `json:"fix,omitempty"`
}

// GET /api/hyperframes/blocks[?q=&tag=&state=&kind=&family=&limit=&offset=&sort=]: the blocks and components, filtered by text (name, title, description,
// tags), tag, state, kind (block or component) and family; sort is name (the default) or title. It can be paged (see api-listing): total and states
// count everything that matches, not only the page.
func (h *Handler) handleHFBlocks(w http.ResponseWriter, r *http.Request) {
	params, err := pstore.ParseList(r, hyperframes.ListSpec)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	q := r.URL.Query()
	f := hyperframes.Filter{
		Q: params.Q, Tag: strings.TrimSpace(q.Get("tag")), State: q.Get("state"), Family: strings.TrimSpace(q.Get("family")),
	}
	if k := q.Get("kind"); k == "component" || k == "block" {
		f.Kind = k
	}
	page, err := h.hfStore().ListPage(f, params)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	out := HFBlockList{Blocks: []HFBlock{}, States: page.States, Total: int(page.Total)}
	for _, b := range page.Blocks {
		out.Blocks = append(out.Blocks, hfBlockOf(b))
	}
	libs := h.hfLibs
	if libs == "" {
		libs = hyperframes.LibsDir
	}
	if out.LibsInstalled = len(hyperframes.Missing(libs)) == 0; !out.LibsInstalled {
		out.Fix = "bash scripts/get-hyperframes-libs.sh"
	}
	pstore.WriteListHeaders(w, r, page.Total, params)
	api.WriteJSON(w, http.StatusOK, out)
}

// DELETE /api/hyperframes/blocks/{name}: deletes the record of a block and its folder (204). A part that was made from the block is a part of its own
// and stays in the catalog of parts.
func (h *Handler) handleHFBlockDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	err := h.hfStore().Delete(name)
	switch {
	case errors.Is(err, hyperframes.ErrNotFound), err != nil && !hyperframes.ValidName(name):
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("block %q not found", name))
	case err != nil:
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// HFBlockDetail is a block with its main file.
type HFBlockDetail struct {
	HFBlock
	HTML  string   `json:"html"`
	Files []string `json:"files"`
	// Variables are the variables the block declares, as the registry gives them (id, type, label, default, min, max...).
	Variables json.RawMessage `json:"variables,omitempty"`
	// Licenses are the licence files that came with the block (a name with LICENSE, LICENCE or OFL in it), with their text.
	Licenses []HFLicenseFile `json:"licenses,omitempty"`
}

// HFLicenseFile is a licence file of a block.
type HFLicenseFile struct {
	File string `json:"file"`
	Text string `json:"text"`
}

const hfMaxLicenseBytes = 64 << 10

// hfLicenses reads the licence files of a block, each cut at 64 KB.
func (h *Handler) hfLicenses(b hyperframes.Block) []HFLicenseFile {
	var out []HFLicenseFile
	for _, f := range b.Files {
		low := strings.ToLower(filepath.Base(f))
		if !(strings.Contains(low, "licen") || strings.Contains(low, "ofl")) || !(strings.HasSuffix(low, ".txt") || strings.HasSuffix(low, ".md") || !strings.Contains(low, ".")) {
			continue
		}
		data, err := h.hfStore().Read(b.Name, f)
		if err != nil {
			continue
		}
		if len(data) > hfMaxLicenseBytes {
			data = data[:hfMaxLicenseBytes]
		}
		out = append(out, HFLicenseFile{File: f, Text: string(data)})
	}
	return out
}

func (h *Handler) hfBlockFromPath(w http.ResponseWriter, r *http.Request) (hyperframes.Block, bool) {
	name := r.PathValue("name")
	b, err := h.hfStore().Get(name)
	switch {
	case errors.Is(err, hyperframes.ErrNotFound):
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("block %q not found", name))
		return b, false
	case err != nil && !hyperframes.ValidName(name):
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("block %q not found", name))
		return b, false
	case err != nil:
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return b, false
	}
	return b, true
}

// GET /api/hyperframes/blocks/{name}: the block with the text of its main file.
func (h *Handler) handleHFBlockGet(w http.ResponseWriter, r *http.Request) {
	b, ok := h.hfBlockFromPath(w, r)
	if !ok {
		return
	}
	html, err := h.hfStore().Read(b.Name, b.HTML)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, HFBlockDetail{HFBlock: hfBlockOf(b), HTML: string(html), Files: b.Files, Variables: b.Variables, Licenses: h.hfLicenses(b)})
}

// GET /api/hyperframes/blocks/{name}/thumbnail: the picture of the block, drawn the first time and kept.
func (h *Handler) handleHFBlockThumbnail(w http.ResponseWriter, r *http.Request) {
	b, ok := h.hfBlockFromPath(w, r)
	if !ok {
		return
	}
	d, ok := h.hfDrawer(w)
	if !ok {
		return
	}
	jpg, err := d.Thumbnail(r.Context(), b.Name)
	if err != nil {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, h.hfDrawError(err))
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(jpg)
}

// hfDrawError words a failure to draw a block; with the libraries missing it says so first.
func (h *Handler) hfDrawError(err error) string {
	libs := h.hfLibs
	if libs == "" {
		libs = hyperframes.LibsDir
	}
	if n := len(hyperframes.Missing(libs)); n > 0 {
		return fmt.Sprintf("%v (%d libraries are not installed: bash scripts/get-hyperframes-libs.sh)", err, n)
	}
	return err.Error()
}

func (h *Handler) hfFrame(w http.ResponseWriter, r *http.Request, name, html string, t float64) {
	d, ok := h.hfDrawer(w)
	if !ok {
		return
	}
	sess, _, err := d.Open(r.Context(), name, html)
	if err != nil {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, h.hfDrawError(err))
		return
	}
	defer sess.Close()
	png, err := sess.Frame(r.Context(), t)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func hfTime(w http.ResponseWriter, raw string, b hyperframes.Block) (float64, bool) {
	if raw == "" {
		return b.Duration * 0.4, true
	}
	t, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(t) || t < 0 || t > b.Duration+0.001 {
		api.Refuse(w, "t must be a number of seconds from 0 to %g", b.Duration)
		return 0, false
	}
	return t, true
}

// GET /api/hyperframes/blocks/{name}/frame?t=: the block drawn at t seconds (a frame at 40% of its length without t).
func (h *Handler) handleHFBlockFrame(w http.ResponseWriter, r *http.Request) {
	b, ok := h.hfBlockFromPath(w, r)
	if !ok {
		return
	}
	t, ok := hfTime(w, r.URL.Query().Get("t"), b)
	if !ok {
		return
	}
	h.hfFrame(w, r, b.Name, "", t)
}

// HFPreviewBody is the body of POST /api/hyperframes/blocks/{name}/preview.
type HFPreviewBody struct {
	HTML string   `json:"html"`
	T    *float64 `json:"t,omitempty"`
}

// POST /api/hyperframes/blocks/{name}/preview {html, t?}: the block drawn with another main file, to see an edit before saving it. Nothing is stored.
func (h *Handler) handleHFBlockPreview(w http.ResponseWriter, r *http.Request) {
	b, ok := h.hfBlockFromPath(w, r)
	if !ok {
		return
	}
	var body HFPreviewBody
	if !api.DecodeJSONLimited(w, r, &body, 8<<20) {
		return
	}
	if strings.TrimSpace(body.HTML) == "" {
		api.Refuse(w, "html is required")
		return
	}
	raw := ""
	if body.T != nil {
		raw = strconv.FormatFloat(*body.T, 'f', -1, 64)
	}
	t, ok := hfTime(w, raw, b)
	if !ok {
		return
	}
	h.hfFrame(w, r, b.Name, body.HTML, t)
}

// HFCheckAnswer is the result of the checks.
type HFCheckAnswer struct {
	Block  HFBlock                 `json:"block"`
	Checks hyperframes.CheckResult `json:"checks"`
}

// POST /api/hyperframes/blocks/{name}/check: runs the minimum checks again and keeps the result.
func (h *Handler) handleHFBlockCheck(w http.ResponseWriter, r *http.Request) {
	b, ok := h.hfBlockFromPath(w, r)
	if !ok {
		return
	}
	d, ok := h.hfDrawer(w)
	if !ok {
		return
	}
	res, err := d.Check(r.Context(), b.Name)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	nb, _ := d.Store.Get(b.Name)
	api.WriteJSON(w, http.StatusOK, HFCheckAnswer{Block: hfBlockOf(nb), Checks: res})
}

// HFPutBody is the body of PUT /api/hyperframes/blocks/{name}.
type HFPutBody struct {
	HTML string `json:"html"`
}

// PUT /api/hyperframes/blocks/{name} {html}: replaces the main file of a block after the minimum checks; a block that fails them is refused
// naming the problems and the stored one does not change. The block is marked as edited, so a later import does not overwrite it.
func (h *Handler) handleHFBlockPut(w http.ResponseWriter, r *http.Request) {
	b, ok := h.hfBlockFromPath(w, r)
	if !ok {
		return
	}
	var body HFPutBody
	if !api.DecodeJSONLimited(w, r, &body, 8<<20) {
		return
	}
	if strings.TrimSpace(body.HTML) == "" {
		api.Refuse(w, "html is required")
		return
	}
	d, ok := h.hfDrawer(w)
	if !ok {
		return
	}
	res, err := d.Run(r.Context(), b.Name, body.HTML)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	// what the registry does not ship is not the edit's doing: only the checks of the page decide
	var probs []string
	for _, p := range res.Problems {
		if !strings.HasPrefix(p, "the registry lists") {
			probs = append(probs, p)
		}
	}
	if len(probs) > 0 {
		api.Refuse(w, "the block does not pass the checks: %s", strings.Join(probs, "; "))
		return
	}
	if err := d.Store.WriteHTML(b.Name, body.HTML, res); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	nb, _ := d.Store.Get(b.Name)
	api.WriteJSON(w, http.StatusOK, hfBlockOf(nb))
}

// HFImportBody is the body of POST /api/hyperframes/blocks/import.
type HFImportBody struct {
	// Dir is the registry/blocks folder of a clone of HyperFrames on the server.
	Dir string `json:"dir"`
	// ComponentsDir is the registry/components folder of the same clone; when given, the components are imported too.
	ComponentsDir string   `json:"components_dir,omitempty"`
	Only          []string `json:"only,omitempty"`
	Force         bool     `json:"force,omitempty"`
	// Check runs the minimum checks on what was imported (default true).
	Check *bool `json:"check,omitempty"`
}

// HFImportAnswer is the result of an import.
type HFImportAnswer struct {
	Imported []hyperframes.Imported `json:"imported"`
	Report   *hyperframes.Report    `json:"report,omitempty"`
}

// POST /api/hyperframes/blocks/import {dir, only?, force?, check?}: copies blocks from a clone of the registry on this server and, unless check is false, runs the
// checks on them. It can take minutes for all of them.
func (h *Handler) handleHFImport(w http.ResponseWriter, r *http.Request) {
	var body HFImportBody
	if !api.DecodeJSONLimited(w, r, &body, 1<<20) {
		return
	}
	dir := filepath.Clean(strings.TrimSpace(body.Dir))
	if strings.TrimSpace(body.Dir) == "" || !filepath.IsAbs(dir) {
		api.Refuse(w, "dir must be the absolute path of the registry/blocks folder of a HyperFrames clone on this server")
		return
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		api.Refuse(w, "%s is not a folder", dir)
		return
	}
	doCheck := body.Check == nil || *body.Check
	var d hyperframes.Drawer
	if doCheck {
		var ok bool
		if d, ok = h.hfDrawer(w); !ok {
			return
		}
	}
	st := h.hfStore()
	res, err := st.Import(hyperframes.Source{Dir: dir, URL: hyperframes.RepoURL}, body.Only, body.Force)
	if err != nil {
		api.Refuse(w, "%v", err)
		return
	}
	if cd := strings.TrimSpace(body.ComponentsDir); cd != "" {
		if !filepath.IsAbs(cd) {
			api.Refuse(w, "components_dir must be an absolute path")
			return
		}
		cres, err := st.ImportComponents(hyperframes.Source{Dir: filepath.Clean(cd), URL: hyperframes.RepoURL}, body.Only, body.Force)
		if err != nil {
			api.Refuse(w, "components: %v", err)
			return
		}
		res = append(res, cres...)
	}
	if doCheck {
		for _, r2 := range res {
			if r2.Written {
				if _, err := d.Check(r.Context(), r2.Name); err != nil {
					break
				}
			}
		}
	}
	out := HFImportAnswer{Imported: res}
	if all, err := st.List(); err == nil {
		rep := hyperframes.BuildReport(all)
		out.Report = &rep
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// HFRenderBody is the body of POST /api/hyperframes/blocks/{name}/render.
type HFRenderBody struct {
	// Duration is the length of the clip in seconds (default: the block's own, at most 60).
	Duration float64 `json:"duration,omitempty"`
	// FPS is the frame rate (default 30, at most 60).
	FPS int `json:"fps,omitempty"`
	// Variables override the defaults of the variables the block declares.
	Variables map[string]any `json:"variables,omitempty"`
}

// HFClip is a rendered clip.
type HFClip struct {
	Block    string   `json:"block"`
	File     string   `json:"file"`
	Path     string   `json:"path"`
	URL      string   `json:"url"`
	Seconds  float64  `json:"seconds"`
	FPS      int      `json:"fps"`
	Frames   int      `json:"frames"`
	Width    int      `json:"width"`
	Height   int      `json:"height"`
	Problems []string `json:"problems,omitempty"`
}

const (
	hfMaxClipSeconds = 60
	hfMaxClipFPS     = 60
)

// POST /api/hyperframes/blocks/{name}/render {duration?, fps?, variables?}: plays the block to an MP4 with no song (the frames come from the
// block's own timeline) in the clips folder of the catalog, where the answer says it is, and GET .../clips/{file} gives it. A project uses it
// as the `clip` of a scene. The same request gives the same file name, and a clip that is there is not drawn again.
func (h *Handler) handleHFBlockRender(w http.ResponseWriter, r *http.Request) {
	b, ok := h.hfBlockFromPath(w, r)
	if !ok {
		return
	}
	var body HFRenderBody
	if r.ContentLength != 0 && !api.DecodeJSONLimited(w, r, &body, 1<<20) {
		return
	}
	if body.Duration == 0 {
		body.Duration = b.Duration
	}
	if body.FPS == 0 {
		body.FPS = 30
	}
	if math.IsNaN(body.Duration) || body.Duration <= 0 || body.Duration > hfMaxClipSeconds {
		api.Refuse(w, "duration must be more than 0 and at most %d seconds", hfMaxClipSeconds)
		return
	}
	if body.FPS < 1 || body.FPS > hfMaxClipFPS {
		api.Refuse(w, "fps must be from 1 to %d", hfMaxClipFPS)
		return
	}
	d, ok := h.hfDrawer(w)
	if !ok {
		return
	}
	vars, _ := json.Marshal(body.Variables)
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%g|%d|%s|%v", b.Name, body.Duration, body.FPS, vars, b.Edited)))
	file := fmt.Sprintf("%s-%s.mp4", b.Name, hex.EncodeToString(sum[:])[:10])
	dir := filepath.Join(d.Store.Dir, "clips")
	out := filepath.Join(dir, file)
	clip := HFClip{Block: b.Name, File: file, Path: out, URL: "/api/hyperframes/clips/" + file, Seconds: body.Duration, FPS: body.FPS,
		Frames: motion.Frames(body.Duration, body.FPS), Width: b.Width, Height: b.Height}
	if st, err := os.Stat(out); err == nil && st.Size() > 0 && !hfEditedSince(b, st.ModTime()) {
		api.WriteJSON(w, http.StatusOK, clip)
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	tmp := filepath.Join(dir, "."+file)
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, "ffmpeg is needed to make the clip")
		return
	}
	frames, probs, err := d.RenderClip(r.Context(), b.Name, body.Duration, body.FPS, body.Variables, tmp)
	if err != nil {
		_ = os.Remove(tmp)
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, h.hfDrawError(err))
		return
	}
	if err := os.Rename(tmp, out); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	clip.Frames, clip.Problems = frames, probs
	api.WriteJSON(w, http.StatusOK, clip)
}

// hfEditedSince says an edit of the block is newer than a clip, so the clip has to be drawn again.
func hfEditedSince(b hyperframes.Block, t time.Time) bool {
	return b.Edited && b.Checks != nil && b.Checks.At.After(t)
}

// GET /api/hyperframes/clips/{file}: a clip the render made.
func (h *Handler) handleHFClip(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	if file == "" || file != filepath.Base(file) || !strings.HasSuffix(file, ".mp4") || strings.HasPrefix(file, ".") {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, "clip not found")
		return
	}
	p := filepath.Join(h.hfStore().Dir, "clips", file)
	if st, err := os.Stat(p); err != nil || !st.Mode().IsRegular() {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, "clip not found")
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	http.ServeFile(w, r, p)
}

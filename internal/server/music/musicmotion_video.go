package music

// musicmotion_video.go: what a dashboard needs to edit the video track of a piece (spec music-motion, "A clip can be seen and cut into
// thumbnails" and "Cutting and joining items of the track"): the picture of a clip at a second, a strip of pictures to draw the lane with, and
// split and join as the cut tool.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"veritmo/internal/music/musicmotion"
	"veritmo/internal/server/api"
)

// clipOf finds the clip the piece keeps under the name of the path and its length, answering the error itself when it cannot.
func (h *Handler) clipOf(w http.ResponseWriter, r *http.Request) (info *pieceInfo, path string, clip musicmotion.ClipInfo, ok bool) {
	info, ok = h.pieceFromPath(w, r)
	if !ok {
		return nil, "", clip, false
	}
	name := r.PathValue("name")
	if !musicmotion.ValidClipName(name) {
		badClipName(w, name)
		return nil, "", clip, false
	}
	path = filepath.Join(h.pieceSources(info.ID), name)
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("%q is not in this piece", name))
		return nil, "", clip, false
	}
	if !ffmpegAvailable() {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, "ffmpeg is not installed: it is needed to take the pictures of a clip")
		return nil, "", clip, false
	}
	clip, err = h.clips.get(r.Context(), path, fi)
	if err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeInvalid, fmt.Sprintf("cannot read the clip %q: %v", name, err))
		return nil, "", clip, false
	}
	return info, path, clip, true
}

// intParam reads an integer query parameter in [lo, hi] (def when absent); a bad one is answered 422 naming it.
func intParam(w http.ResponseWriter, r *http.Request, name string, def, lo, hi int) (int, bool) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		api.Refuse(w, "%s must be a whole number from %d to %d, got %q", name, lo, hi, v)
		return 0, false
	}
	return n, true
}

// GET /api/music-motion/pieces/{id}/backgrounds/{name}/frame?t=&width=
func (h *Handler) handlePieceBackgroundFrame(w http.ResponseWriter, r *http.Request) {
	_, path, clip, ok := h.clipOf(w, r)
	if !ok {
		return
	}
	t, err := strconv.ParseFloat(r.URL.Query().Get("t"), 64)
	if err != nil || t < 0 || t > clip.Seconds {
		api.Refuse(w, "t must be a second of the clip, from 0 to %.2f, got %q", clip.Seconds, r.URL.Query().Get("t"))
		return
	}
	width, ok := intParam(w, r, "width", 640, 32, 1920)
	if !ok {
		return
	}
	height := even(clip.Height * width / max(clip.Width, 1))
	raw, err := h.clipFrame(r.Context(), path, minF(t, maxF(clip.Seconds-0.05, 0)), width, height)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, fmt.Sprintf("cannot take the picture: %v", err))
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(raw)
}

// GET /api/music-motion/pieces/{id}/backgrounds/{name}/thumbs?count=&height=
//
// One PNG with `count` pictures side by side, evenly spaced over the file (the middle of each stretch), each `height` pixels high: the strip a
// dashboard draws a clip with in the lane of the track. X-Frame-Count and X-Frame-Width say how to cut it.
func (h *Handler) handlePieceBackgroundThumbs(w http.ResponseWriter, r *http.Request) {
	_, path, clip, ok := h.clipOf(w, r)
	if !ok {
		return
	}
	count, ok := intParam(w, r, "count", 12, 2, 60)
	if !ok {
		return
	}
	height, ok := intParam(w, r, "height", 60, 16, 240)
	if !ok {
		return
	}
	width := even(clip.Width * height / max(clip.Height, 1))
	strip := image.NewNRGBA(image.Rect(0, 0, width*count, height))
	for i := 0; i < count; i++ {
		at := clip.Seconds * (float64(i) + 0.5) / float64(count)
		raw, err := h.clipFrame(r.Context(), path, at, width, height)
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, fmt.Sprintf("cannot take the picture at %.2f s: %v", at, err))
			return
		}
		img, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, fmt.Sprintf("cannot read the picture at %.2f s: %v", at, err))
			return
		}
		draw.Draw(strip, image.Rect(i*width, 0, (i+1)*width, height), img, img.Bounds().Min, draw.Src)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, strip); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("X-Frame-Count", strconv.Itoa(count))
	w.Header().Set("X-Frame-Width", strconv.Itoa(width))
	_, _ = w.Write(buf.Bytes())
}

func even(n int) int {
	if n < 2 {
		return 2
	}
	return n &^ 1
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// itemIndex finds the item of the track with the id.
func itemIndex(video []musicmotion.VideoItem, id string) int {
	for i, it := range video {
		if it.ID == id {
			return i
		}
	}
	return -1
}

// trackOf loads the saved script of the piece with its track (the shorthand converted) and the clip lengths needed, answering the error when it
// cannot. The item named `id` is looked for.
func (h *Handler) trackOf(w http.ResponseWriter, info *pieceInfo, id string) (musicmotion.Script, int, bool) {
	script, ok := musicmotion.LoadScriptAt(h.place(info.ID))
	if !ok {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, "the piece has lost its script")
		return script, 0, false
	}
	script, err := script.WithTrack()
	if err != nil {
		api.Refuse(w, "%v", err)
		return script, 0, false
	}
	i := itemIndex(script.Video, id)
	if i < 0 {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("the video track has no item %q", id))
		return script, 0, false
	}
	return script, i, true
}

func (h *Handler) fileSeconds(w http.ResponseWriter, r *http.Request, info *pieceInfo, clip string) (float64, bool) {
	path := filepath.Join(h.pieceSources(info.ID), clip)
	fi, err := os.Stat(path)
	if err != nil {
		api.Refuse(w, "the piece keeps no clip %q", clip)
		return 0, false
	}
	c, err := h.clips.get(r.Context(), path, fi)
	if err != nil {
		api.Refuse(w, "cannot read the clip %q: %v", clip, err)
		return 0, false
	}
	return c.Seconds, true
}

// POST /api/music-motion/pieces/{id}/video/split  {"item", "at", "ids"?: [a, b]}
func (h *Handler) handleVideoSplit(w http.ResponseWriter, r *http.Request) {
	info, ok := h.pieceFromPath(w, r)
	if !ok || h.refuseIfRendering(w, info) {
		return
	}
	var body struct {
		Item string    `json:"item"`
		At   float64   `json:"at"`
		IDs  [2]string `json:"ids"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	script, i, ok := h.trackOf(w, info, body.Item)
	if !ok {
		return
	}
	secs, ok := h.fileSeconds(w, r, info, script.Video[i].Clip)
	if !ok {
		return
	}
	first, second, err := musicmotion.SplitItem(script.Video[i], body.At, secs, body.IDs)
	if err != nil {
		api.Refuse(w, "%v", err)
		return
	}
	video := append(append(append([]musicmotion.VideoItem{}, script.Video[:i]...), first, second), script.Video[i+1:]...)
	script.Video = video
	h.saveScript(w, info, script)
}

// POST /api/music-motion/pieces/{id}/video/join  {"first", "second"}
func (h *Handler) handleVideoJoin(w http.ResponseWriter, r *http.Request) {
	info, ok := h.pieceFromPath(w, r)
	if !ok || h.refuseIfRendering(w, info) {
		return
	}
	var body struct {
		First  string `json:"first"`
		Second string `json:"second"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	script, i, ok := h.trackOf(w, info, body.First)
	if !ok {
		return
	}
	j := itemIndex(script.Video, body.Second)
	if j < 0 {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("the video track has no item %q", body.Second))
		return
	}
	if j != i+1 {
		api.Refuse(w, "the items %q and %q are not next to each other in the track", body.First, body.Second)
		return
	}
	secs, ok := h.fileSeconds(w, r, info, script.Video[i].Clip)
	if !ok {
		return
	}
	whole, err := musicmotion.JoinItems(script.Video[i], script.Video[j], secs)
	if err != nil {
		api.Refuse(w, "%v", err)
		return
	}
	script.Video = append(append(append([]musicmotion.VideoItem{}, script.Video[:i]...), whole), script.Video[j+1:]...)
	h.saveScript(w, info, script)
}

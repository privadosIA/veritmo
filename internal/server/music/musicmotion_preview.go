package music

// musicmotion_preview.go: what a dashboard that edits a piece needs to see it exactly (spec music-motion): the pages of
// a piece with no saving, a frame of the saved piece, and the drawings and fonts it is made of.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"veritmo/internal/music/hyperframes"
	"veritmo/internal/music/musicmotion"
	"veritmo/internal/server/api"
	"veritmo/internal/video/motion"
)

// PreviewBody is what a preview may carry: a design and a script that replace the saved ones for this call only.
type PreviewBody struct {
	Design *musicmotion.Design `json:"design,omitempty"`
	Script *musicmotion.Script `json:"script,omitempty"`
}

// PreviewScene is the page of one piece as a sandboxed frame is given it.
type PreviewScene struct {
	ID    string          `json:"id"`
	Start float64         `json:"start"`
	End   float64         `json:"end"`
	HTML  string          `json:"html"`
	Song  json.RawMessage `json:"song"`
	// Blender says the piece is a 3D scene drawn by Blender: it has no page (html and song are empty); ask for its frames with
	// GET .../frame?t= or POST .../script/{piece}/blender/frame.
	Blender bool `json:"blender,omitempty"`
	// Layer says the page is drawn on transparent to be laid over the video track: an item of the track is over the piece. The items are in
	// `video` of the preview.
	Layer bool `json:"layer,omitempty"`
}

// PreviewVideoItem is an item of the video track as a dashboard plays it: the clip and where to play it from, and how the song's time maps to
// the file's, so a <video> under the transparent pages can follow the same mapping the render uses (the server's own frame is the reference).
type PreviewVideoItem struct {
	ID           string  `json:"id"`
	Clip         string  `json:"clip"`
	URL          string  `json:"url"`
	MediaType    string  `json:"media_type"`
	FileSeconds  float64 `json:"file_seconds"`
	Start        float64 `json:"start"`
	End          float64 `json:"end"`
	BuildSeconds float64 `json:"build_seconds"`
	Scrim        float64 `json:"scrim"`
	// LocalScrims are the strips of the design's colour the render lays over the band where the verse falls on the item (absent for none): see
	// assemble.LocalScrimStrips for their profile.
	LocalScrims       []musicmotion.LocalScrim `json:"local_scrims,omitempty"`
	TransitionSeconds float64                  `json:"transition_seconds"`
	musicmotion.ItemMapping
}

// PreviewReplacement says that the part of a piece was swapped for another.
type PreviewReplacement struct {
	Piece string `json:"piece"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// PiecePreview is the answer of the preview.
type PiecePreview struct {
	Width        int                  `json:"width"`
	Height       int                  `json:"height"`
	FPS          int                  `json:"fps"`
	Scenes       []PreviewScene       `json:"scenes"`
	Replacements []PreviewReplacement `json:"replacements"`
	// Video is the video track of the script, in order (absent for a script with none).
	Video []PreviewVideoItem `json:"video,omitempty"`
}

// specOf gathers what the pages of a piece are made from: the saved design and script unless the body gives its own,
// each checked as the PUT that saves it would. It answers 422 itself and returns false when something is not valid.
func (h *Handler) specOf(w http.ResponseWriter, info *pieceInfo, body PreviewBody) (musicmotion.ProjectSpec, bool) {
	m, okMap := h.pieceMap(info.ID)
	st, okDesign := musicmotion.LoadDesignAt(h.place(info.ID))
	script, okScript := musicmotion.LoadScriptAt(h.place(info.ID))
	if !okMap || !okDesign || !okScript {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, "the piece has lost its song map, its design or its script")
		return musicmotion.ProjectSpec{}, false
	}
	design := st.Design
	if body.Design != nil {
		design = *body.Design
		if err := design.Validate(); err != nil {
			api.Refuse(w, "%v", err)
			return musicmotion.ProjectSpec{}, false
		}
	}
	if body.Script != nil {
		script = *body.Script
		t, err := script.WithTrack() // the shorthand counts as the item it becomes
		if err != nil {
			api.Refuse(w, "%v", err)
			return musicmotion.ProjectSpec{}, false
		}
		script = t
	}
	cat, lib := h.mmCatalog(), h.mmLibrary()
	if err := script.WithSources(h.pieceSources(info.ID)).ValidateAll(cat, lib, m, design, info.Width, info.Height); err != nil {
		api.Refuse(w, "%v", err)
		return musicmotion.ProjectSpec{}, false
	}
	script, _ = h.withEffectiveScrims(context.Background(), info, script, design)
	return musicmotion.ProjectSpec{Name: info.ID, Map: m, Design: design, Script: script, Cat: cat, Lib: lib, FPS: info.FPS, Width: info.Width, Height: info.Height}, true
}

// POST /api/music-motion/pieces/{id}/preview
func (h *Handler) handleMusicMotionPreview(w http.ResponseWriter, r *http.Request) {
	info, ok := h.pieceFromPath(w, r)
	if !ok {
		return
	}
	var body PreviewBody
	if r.ContentLength != 0 {
		api.RaiseBodyLimit(w, r, 4<<20)
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&body); err != nil && err.Error() != "EOF" {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
			return
		}
	}
	spec, ok := h.specOf(w, info, body)
	if !ok {
		return
	}
	var only []string
	if q := strings.TrimSpace(r.URL.Query().Get("scenes")); q != "" {
		for _, id := range strings.Split(q, ",") {
			only = append(only, strings.TrimSpace(id))
		}
	}
	// The 3D pieces have no page: they are listed as they are and the others are drawn.
	threeD := map[string]musicmotion.Piece{}
	for _, p := range spec.Script.Pieces {
		if p.Blender != nil {
			threeD[p.ID] = p
		}
	}
	var onlyPages []string
	for _, id := range only {
		if _, is3D := threeD[id]; !is3D {
			onlyPages = append(onlyPages, id)
		}
	}
	if len(only) > 0 && len(onlyPages) == 0 {
		onlyPages = []string{} // every piece asked for is a 3D one: no page to build
	}
	var pages []musicmotion.PieceMotion
	if len(only) == 0 || len(onlyPages) > 0 {
		var err error
		if pages, err = musicmotion.BuildPages(spec, onlyPages); err != nil {
			api.Refuse(w, "%v", err)
			return
		}
	}
	pageOf := map[string]musicmotion.PieceMotion{}
	for _, pg := range pages {
		pageOf[pg.ID] = pg
	}
	asked := map[string]bool{}
	for _, id := range only {
		asked[id] = true
	}
	out := PiecePreview{Width: info.Width, Height: info.Height, FPS: info.FPS, Scenes: []PreviewScene{}, Replacements: []PreviewReplacement{}}
	for _, p := range spec.Script.Pieces {
		if len(asked) > 0 && !asked[p.ID] {
			continue
		}
		if p.Blender != nil {
			out.Scenes = append(out.Scenes, PreviewScene{ID: p.ID, Start: p.Start, End: p.End, Song: json.RawMessage("{}"), Blender: true})
			continue
		}
		pg := pageOf[p.ID]
		ps := PreviewScene{ID: pg.ID, Start: pg.Start, End: pg.End, Song: pg.Song, HTML: motion.PreviewHTMLSong(pg.HTML, pg.Seconds, pg.Song)}
		ps.Layer = len(spec.Script.ItemsOver(p)) > 0
		out.Scenes = append(out.Scenes, ps)
	}
	out.Video = h.previewVideo(r.Context(), info, spec.Script)
	api.WriteJSON(w, http.StatusOK, out)
}

// previewVideo lists the items of the video track with where each clip is played from and how it is mapped; an item whose clip cannot be read
// is listed without its length (the script would not have validated).
func (h *Handler) previewVideo(ctx context.Context, info *pieceInfo, script musicmotion.Script) []PreviewVideoItem {
	var out []PreviewVideoItem
	for i, it := range script.Video {
		pv := PreviewVideoItem{ID: it.ID, Clip: it.Clip, URL: "/api/music-motion/pieces/" + info.ID + "/backgrounds/" + it.Clip + "/file", MediaType: mime.TypeByExtension(filepath.Ext(it.Clip)),
			Start: it.Start, End: it.End, BuildSeconds: musicmotion.TrackBuildSeconds(script.Video, i), Scrim: it.Play().ScrimValue(), LocalScrims: it.Local}
		if it.TransitionSeconds != nil {
			pv.TransitionSeconds = musicmotion.TrackFade(script.Video, i)
		}
		path := filepath.Join(h.pieceSources(info.ID), it.Clip)
		if fi, err := os.Stat(path); err == nil {
			if clip, err := h.clips.get(ctx, path, fi); err == nil {
				pv.FileSeconds = clip.Seconds
				pv.ItemMapping = it.Mapping(clip.Seconds)
			}
		}
		out = append(out, pv)
	}
	return out
}

// GET /api/music-motion/pieces/{id}/frame?t=12.4
func (h *Handler) handleMusicMotionFrame(w http.ResponseWriter, r *http.Request) {
	info, ok := h.pieceFromPath(w, r)
	if !ok {
		return
	}
	t, err := strconv.ParseFloat(r.URL.Query().Get("t"), 64)
	if err != nil || math.IsNaN(t) || t < 0 {
		api.Refuse(w, "t must be a number of seconds, 0 or more")
		return
	}
	spec, ok := h.specOf(w, info, PreviewBody{})
	if !ok {
		return
	}
	var piece *musicmotion.Piece
	pieceIndex := -1
	for i := range spec.Script.Pieces {
		if p := &spec.Script.Pieces[i]; t >= p.Start && t < p.End {
			piece, pieceIndex = p, i
		}
	}
	if piece == nil {
		api.Refuse(w, "t=%g is outside the piece (0 to %g)", t, spec.Script.Pieces[len(spec.Script.Pieces)-1].End)
		return
	}
	if piece.Blender != nil { // a 3D piece: Blender draws it, on the CPU, at the piece's own second
		cfg := musicmotion.BlenderConfig(spec.Map, spec.Design, info.Width, info.Height, info.FPS)
		h.writeBlenderFrame(w, r, cfg, h.pieceSources(info.ID), piece.Blender.Description, t-piece.Start, piece.End-piece.Start, 0)
		return
	}
	b, err := h.ws.MotionBrowserFor()
	if err != nil {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, "there is no Chrome or Chromium to draw the frame: "+err.Error())
		return
	}
	if len(spec.Script.Video) > 0 {
		// a script with a video track: the frame is composed as the render stacks it (the track's picture and the pages over it)
		if !ffmpegAvailable() {
			api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, "ffmpeg is not installed: it is needed to take the picture of the video track")
			return
		}
		out, err := h.composedFrame(r.Context(), b.Path, info, spec, pieceIndex, t)
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(out)
		return
	}
	pages, err := musicmotion.BuildPages(spec, []string{piece.ID})
	if err != nil {
		api.Refuse(w, "%v", err)
		return
	}
	pg := pages[0]
	page := motion.Page{HTML: pg.HTML, Width: info.Width, Height: info.Height, Duration: pg.Seconds, Song: pg.Song}
	hyperframes.Extend(&page)
	s, err := motion.Open(r.Context(), b.Path, page, motion.Limits{})
	if err != nil {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, err.Error())
		return
	}
	defer s.Close()
	png, err := s.Frame(r.Context(), t-piece.Start)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

// LibraryCharacter is a character with the drawing of each pose.
type LibraryCharacter struct {
	Name              string             `json:"name"`
	Width             int                `json:"width"`
	Height            int                `json:"height"`
	Anchor            musicmotion.Anchor `json:"anchor"`
	DefaultPose       string             `json:"default_pose"`
	DefaultExpression string             `json:"default_expression"`
	Expressions       []string           `json:"expressions"`
	Poses             map[string]string  `json:"poses"`
	Source            string             `json:"source"`
}

// LibraryBackground is a background with its drawing.
type LibraryBackground struct {
	Name   string `json:"name"`
	SVG    string `json:"svg"`
	Source string `json:"source"`
}

// GET /api/music-motion/library/characters/{name}: found through the library, never through a path built from the
// request.
func (h *Handler) handleLibraryCharacter(w http.ResponseWriter, r *http.Request) {
	writeLibraryCharacter(h, w, http.StatusOK, r.PathValue("name"))
}

// GET /api/music-motion/library/backgrounds/{name}
func (h *Handler) handleLibraryBackground(w http.ResponseWriter, r *http.Request) {
	writeLibraryBackground(h, w, http.StatusOK, r.PathValue("name"))
}

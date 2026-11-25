package music

// musicmotion_preview.go: what a dashboard that edits a piece needs to see it exactly (spec music-motion): the pages of
// a piece with no saving, a frame of the saved piece, and the drawings and fonts it is made of.

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
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
	}
	cat, lib := h.mmCatalog(), h.mmLibrary()
	if err := script.WithSources(h.pieceSources(info.ID)).ValidateAll(cat, lib, m, design, info.Width, info.Height); err != nil {
		api.Refuse(w, "%v", err)
		return musicmotion.ProjectSpec{}, false
	}
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
		out.Scenes = append(out.Scenes, PreviewScene{ID: pg.ID, Start: pg.Start, End: pg.End, Song: pg.Song,
			HTML: motion.PreviewHTMLSong(pg.HTML, pg.Seconds, pg.Song)})
	}
	api.WriteJSON(w, http.StatusOK, out)
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
	for i := range spec.Script.Pieces {
		if p := &spec.Script.Pieces[i]; t >= p.Start && t < p.End {
			piece = p
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

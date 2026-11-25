package music

// musicmotion_blender.go: the 3D pieces of the script (spec music-motion and blender-scene-writer): a model writes or changes the
// scene of a piece with the settings of the song, and a description can be looked at before it is saved.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"veritmo/internal/music/musicmotion"
	"veritmo/internal/music/songmap"
	"veritmo/internal/project/config"
	"veritmo/internal/server/api"
	"veritmo/internal/server/workspace"
	"veritmo/internal/video/blender"
)

// blenderPieceContext is what the routes of a 3D piece start from: the piece, the script, the song and the settings the scene is
// written and checked for. It answers the error itself.
type blenderPieceContext struct {
	info   *pieceInfo
	script musicmotion.Script
	m      songmap.Map
	design musicmotion.Design
	idx    int
	cfg    *config.Config
	// sources is the piece's sources folder: the models its 3D pieces place are there.
	sources string
}

func (h *Handler) blenderPiece(w http.ResponseWriter, r *http.Request, forWriting bool) (blenderPieceContext, bool) {
	var c blenderPieceContext
	info, ok := h.pieceFromPath(w, r)
	if !ok || (forWriting && h.refuseIfRendering(w, info)) {
		return c, false
	}
	script, has := musicmotion.LoadScriptAt(h.place(info.ID))
	script = script.WithSources(h.pieceSources(info.ID))
	m, mok := h.pieceMap(info.ID)
	st, dok := musicmotion.LoadDesignAt(h.place(info.ID))
	if !has || !mok || !dok {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, "the piece has no script yet: propose or set one first")
		return c, false
	}
	idx, err := script.PieceByID(r.PathValue("piece"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, err.Error())
		return c, false
	}
	return blenderPieceContext{info: info, script: script, m: m, design: st.Design, idx: idx, sources: h.pieceSources(info.ID),
		cfg: musicmotion.BlenderConfig(m, st.Design, info.Width, info.Height, info.FPS)}, true
}

func (c blenderPieceContext) piece() musicmotion.Piece { return c.script.Pieces[c.idx] }

func (c blenderPieceContext) seconds() float64 {
	p := c.piece()
	return p.End - p.Start
}

// BlenderPieceAnswer is the answer of the writer of a 3D piece.
type BlenderPieceAnswer struct {
	// Piece is the piece of the script as it is now (or would be, when Saved is false).
	Piece    musicmotion.Piece `json:"piece"`
	Resolved *blender.Spec     `json:"resolved,omitempty"`
	Provider string            `json:"provider"`
	Model    string            `json:"model"`
	Attempts int               `json:"attempts"`
	// Saved says whether the script was written.
	Saved bool `json:"saved"`
}

// POST /api/music-motion/pieces/{id}/script/{piece}/blender {"idea", "revise"?, "description"?, "provider"?, "save"?}
//
// The model of the motion_graphics task writes the 3D scene of the piece {piece} with the writer and the checks of the descriptions
// of a project, told the piece's length and frame, the song's tempo and design; unless save is false it is put in the script.
func (h *Handler) handlePieceBlenderWrite(w http.ResponseWriter, r *http.Request) {
	c, ok := h.blenderPiece(w, r, true)
	if !ok {
		return
	}
	var req struct {
		Idea        string          `json:"idea"`
		Revise      bool            `json:"revise"`
		Description json.RawMessage `json:"description"`
		Provider    string          `json:"provider"`
		Save        *bool           `json:"save"`
	}
	limit := int64(blender.MaxSpecBytes + 16<<10)
	api.RaiseBodyLimit(w, r, limit)
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	idea := strings.TrimSpace(req.Idea)
	switch {
	case idea == "":
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "idea is required: say in words what the scene shows")
		return
	case utf8.RuneCountInString(idea) > blender.MaxIdeaRunes:
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("the idea is longer than %d characters", blender.MaxIdeaRunes))
		return
	}
	var current []byte
	switch {
	case len(bytes.TrimSpace(req.Description)) > 0 && !bytes.Equal(bytes.TrimSpace(req.Description), []byte("null")):
		current = req.Description
	case req.Revise:
		p := c.piece()
		if p.Blender == nil {
			api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("script piece %q is not a 3D scene: there is nothing to revise (write it first)", p.ID))
			return
		}
		current = p.Blender.Description
	}
	ctx, wr, mi, ok := h.ws.SceneWriter(w, r, req.Provider)
	if !ok {
		return
	}
	if phase := musicmotion.BeatPhase(c.m, c.piece().Start); phase > 0 {
		idea += fmt.Sprintf(" (The first beat of the song falls %.2f seconds after the start of the scene: a key in beats counts from the start of the scene, so use seconds when a movement must land on the song's beat.)", phase)
	}
	res, err := h.ws.DescribeSceneIn(ctx, wr, c.cfg, c.sources, idea, c.seconds(), current)
	if err != nil {
		h.ws.WriteSceneError(w, err)
		return
	}
	piece := c.piece()
	piece.MakeBlender(json.RawMessage(bytes.TrimSpace(res.Raw)))
	saved := req.Save == nil || *req.Save
	if saved {
		script := c.script
		script.Pieces = append([]musicmotion.Piece(nil), c.script.Pieces...)
		script.Pieces[c.idx] = piece
		if err := script.ValidateAll(h.mmCatalog(), h.mmLibrary(), c.m, c.design, c.info.Width, c.info.Height); err != nil {
			api.Refuse(w, "%v", err)
			return
		}
		if err := musicmotion.SaveScriptAt(h.place(c.info.ID), script); err != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
			return
		}
		c.info.ScriptEdited, c.info.ScriptSource = true, "user"
		_ = h.writePiece(c.info)
	}
	api.WriteJSON(w, http.StatusOK, BlenderPieceAnswer{Piece: piece, Resolved: res.Spec, Provider: mi.Provider, Model: mi.Model, Attempts: res.Attempts, Saved: saved})
}

// POST /api/music-motion/pieces/{id}/script/{piece}/blender/frame {"description", "t", "width"?} -> image/png
//
// A frame of a description given in the body, as a scene as long as the piece, with the song's tempo and design; t is the second
// of the piece. Nothing is stored.
func (h *Handler) handlePieceBlenderFrame(w http.ResponseWriter, r *http.Request) {
	c, ok := h.blenderPiece(w, r, false)
	if !ok {
		return
	}
	var req struct {
		Description json.RawMessage `json:"description"`
		T           float64         `json:"t"`
		Width       int             `json:"width"`
	}
	limit := int64(blender.MaxSpecBytes + 8<<10)
	api.RaiseBodyLimit(w, r, limit)
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	secs := c.seconds()
	switch {
	case len(bytes.TrimSpace(req.Description)) == 0:
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "description is required")
		return
	case !(req.T >= 0 && req.T <= secs):
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("t must be inside the piece (0 to %g s)", secs))
		return
	case req.Width < 0 || req.Width > blender.MaxPreviewWidth:
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("width must be at most %d", blender.MaxPreviewWidth))
		return
	}
	h.writeBlenderFrame(w, r, c.cfg, c.sources, req.Description, req.T, secs, req.Width)
}

// writeBlenderFrame draws the frame and answers it, or the error with the status of its kind.
func (h *Handler) writeBlenderFrame(w http.ResponseWriter, r *http.Request, cfg *config.Config, sources string, desc []byte, t, seconds float64, width int) {
	png, err := h.ws.PreviewDescriptionIn(r.Context(), cfg, sources, desc, t, seconds, width)
	var bad *workspace.DescriptionError
	var missing *workspace.BlenderUnavailableError
	switch {
	case errors.As(err, &bad):
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
	case errors.As(err, &missing):
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, err.Error())
	case err != nil:
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, fmt.Sprintf("cannot draw the frame: %v", err))
	default:
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(png)
	}
}

package music

// pieceslist.go: the list of the pieces in pages and the edit of their details (spec music-motion, "The pieces can be paged and their details
// edited").

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"gorm.io/gorm"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/music/musicmotion"
	"veritmo/internal/platform/store"
	"veritmo/internal/server/api"
)

// piecesListSpec says what the list of pieces can be sorted and searched by.
var piecesListSpec = store.ListSpec{
	Sortable:   map[string]string{"created_at": "created_at", "title": "title"},
	Searchable: []string{"title"},
	Default:    "-created_at",
	Tie:        "id",
}

// list reads a page of pieces, newest first unless the parameters say another order, and how many match in all.
func (p pieceStore) list(params store.ListParams, client string, showAdult bool) ([]pieceInfo, int64, error) {
	var scopes []func(*gorm.DB) *gorm.DB
	if !showAdult {
		// a server that does not show content for adults leaves the pieces marked for them out, and counts without them
		scopes = append(scopes, func(db *gorm.DB) *gorm.DB { return db.Where("doc NOT LIKE ?", `%"adult":true%`) })
	}
	if client != "" {
		scopes = append(scopes, func(db *gorm.DB) *gorm.DB { return db.Where("client = ?", client) })
	}
	rows, total, err := store.NewRepo[pieceRow](p.st, "id").List(params, piecesListSpec, scopes...)
	if err != nil {
		return nil, 0, err
	}
	out := make([]pieceInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Doc.V)
	}
	return out, total, nil
}

// GET /api/music-motion/pieces?client=&q=&sort=&limit=&offset=
//
// The pieces, newest first. With limit, offset, sort or q it is a page (X-Total-Count and Link say where the others are); without them it
// answers every piece as it always did.
func (h *Handler) handleMusicMotionList(w http.ResponseWriter, r *http.Request) {
	params, err := store.ParseList(r, piecesListSpec)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	client := r.URL.Query().Get("client")
	out := []PieceView{}
	if !params.Paged {
		infos, _, lerr := h.pieces().list(store.ListParams{}, client, h.host.ShowsAdult())
		if lerr != nil {
			api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, lerr.Error())
			return
		}
		for i := range infos {
			out = append(out, h.pieceViewOf(&infos[i], false))
		}
		sortPieces(out) // the order of always: newest first, a later id first on the same second
		api.WriteJSON(w, http.StatusOK, out)
		return
	}
	infos, total, lerr := h.pieces().list(params, client, h.host.ShowsAdult())
	if lerr != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, lerr.Error())
		return
	}
	for i := range infos {
		out = append(out, h.pieceViewOf(&infos[i], false))
	}
	store.WriteListHeaders(w, r, total, params)
	api.WriteJSON(w, http.StatusOK, out)
}

// PiecePatch is the body of PATCH /api/music-motion/pieces/{id}: every field is optional. The handler reads the body as a map, to name a
// field that cannot be changed this way, and this type says what the fields are.
type PiecePatch struct {
	Title  *string `json:"title,omitempty"`
	Client *string `json:"client,omitempty"`
	Prompt *string `json:"prompt,omitempty"`
	World  *string `json:"world,omitempty"`
	Device *string `json:"device,omitempty"`
	Hook   *string `json:"hook,omitempty"`
}

// patchableFields are the details of a piece that PATCH changes.
var patchableFields = []string{"title", "client", "prompt", "world", "device", "hook"}

const maxTitle = 200

// PATCH /api/music-motion/pieces/{id} {title?, client?, prompt?, world?, device?, hook?}
func (h *Handler) handleMusicMotionPatch(w http.ResponseWriter, r *http.Request) {
	info, ok := h.pieceFromPath(w, r)
	if !ok {
		return
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	for k := range body {
		if !slices.Contains(patchableFields, k) {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("%q cannot be changed this way: use one of %s", k, strings.Join(patchableFields, ", ")))
			return
		}
	}
	if h.refuseIfProposing(w, info) {
		return
	}
	text := func(key string) (string, bool, bool) { // value, sent, valid
		raw, sent := body[key]
		if !sent {
			return "", false, true
		}
		var v *string
		if err := json.Unmarshal(raw, &v); err != nil {
			api.Refuse(w, "%s must be text", key)
			return "", true, false
		}
		if v == nil {
			return "", true, true
		}
		return *v, true, true
	}
	var updated pieceInfo = *info
	if v, sent, valid := text("title"); !valid {
		return
	} else if sent {
		v = strings.TrimSpace(v)
		if n := len([]rune(v)); n == 0 || n > maxTitle {
			api.Refuse(w, "the title must have from 1 to %d characters", maxTitle)
			return
		}
		updated.Title = v
	}
	if raw, sent := body["client"]; sent {
		var c *string
		if err := json.Unmarshal(raw, &c); err != nil {
			api.Refuse(w, "client must be text")
			return
		}
		client, okc := requestClient(w, c)
		if !okc {
			return
		}
		updated.Client = client
	}
	if v, sent, valid := text("prompt"); !valid {
		return
	} else if sent {
		if n := len([]rune(v)); n > musicmotion.MaxPrompt {
			api.Refuse(w, "the prompt has %d characters: at most %d", n, musicmotion.MaxPrompt)
			return
		}
		updated.Prompt = strings.TrimSpace(v)
	}
	if v, sent, valid := text("device"); !valid {
		return
	} else if sent {
		if v != "" && !slices.Contains(assist.PlanDevices, v) {
			api.Refuse(w, "device %q is not one of %s", v, strings.Join(assist.PlanDevices, ", "))
			return
		}
		updated.Device = v
	}
	for _, key := range []string{"world", "hook"} {
		v, sent, valid := text(key)
		if !valid {
			return
		}
		if sent {
			if len([]rune(v)) > assist.MaxPlanLine {
				api.Refuse(w, "world and hook are one line each: at most %d characters", assist.MaxPlanLine)
				return
			}
			if key == "world" {
				updated.World = clipLine(v)
			} else {
				updated.Hook = clipLine(v)
			}
		}
	}
	if err := h.writePiece(&updated); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, h.pieceViewOf(&updated, true))
}

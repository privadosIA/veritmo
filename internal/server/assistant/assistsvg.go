package assistant

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/server/api"
)

// svgBody is the body of POST /api/assist/svg.
type svgBody struct {
	Text     string `json:"text"`
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
}

// POST /api/assist/svg {"text", "model"?, "provider"?} -> {"svg", "model"?}
//
// Writes one SVG document from a description, for an editor that starts from a drawing. It shares the
// guards of every assistant call (the routing and its consent, the wait for a run in progress, the memory
// rule, the model's own refusal) and checks what came back: it parses as XML, the root is <svg>, it has a
// viewBox and it holds no script, event attribute or outside reference; anything else is 422.
func (h *Handler) handleAssistSVG(w http.ResponseWriter, r *http.Request) {
	var req svgBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if err := assist.ValidateText(req.Text); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	ctx, ok := h.Gate(w, r, req.Provider)
	if !ok {
		return
	}
	run := h.AssistSVGRun
	if run == nil {
		run = DefaultAssistSVG
	}
	svg, err := run(ctx, strings.TrimSpace(req.Text), req.Model)
	if err != nil {
		if errors.Is(err, assist.ErrBadSVG) {
			api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, err.Error())
			return
		}
		api.WriteAssistError(w, err)
		return
	}
	ans := map[string]string{"svg": svg}
	if req.Model != "" {
		ans["model"] = req.Model
	}
	api.WriteJSON(w, http.StatusOK, ans)
}

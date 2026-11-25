package assistant

// assistant/describe.go: the "describe a reference" step. A picture or clip already in
// the project's sources/ is read by a local vision model and comes back as a
// prompt; the file never reaches a generator. See specs/describe-reference.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/assistant/describe"
	"veritmo/internal/assistant/llm"
	"veritmo/internal/project/config"
	"veritmo/internal/server/api"
)

const (
	codeDescribeRefused = "describe_refused"
	codeDescribeMinor   = "describe_minor"
)

func defaultDescribeRun(ctx context.Context, path string, opts describe.Options) (describe.Result, error) {
	kind, err := describe.KindOf(path)
	if err != nil {
		return describe.Result{}, err
	}
	d, err := describe.ForContext(ctx, kind)
	if err != nil {
		return describe.Result{}, err
	}
	return d.Describe(ctx, path, opts)
}

func defaultDescribeModels(ctx context.Context) (string, string, []describe.Model, error) {
	return describe.FromEnv().ListModels(ctx)
}

// GET /api/describe/models -> {"default": "...", "detail": "...", "models": [{"name","size_bytes","fits"}]}
func (h *Handler) handleDescribeModels(w http.ResponseWriter, r *http.Request) {
	list := h.DescribeModels
	if list == nil {
		list = defaultDescribeModels
	}
	def, detail, models, err := list(r.Context())
	if err != nil {
		writeDescribeError(w, err)
		return
	}
	if models == nil {
		models = []describe.Model{}
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"default": def, "detail": detail, "models": models})
}

// POST /api/describe {"project": "...", "source": "photo.png", "model"?: "...", "detail"?: bool, "split"?: bool}
//
//	-> {"prompt", "motion"?, "model", "kind", "frames", "shots"?: [{"start", "end", "prompt", "motion"?}]}
//
// With split a clip is answered shot by shot (shots; prompt is the first one's).
//
// The vision model needs several GB, so it is refused while a pipeline run is
// queued or active. A picture of a minor (or whose ages cannot be confirmed)
// is answered with describe_minor and the file is deleted from sources/.
func (h *Handler) handleDescribe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Project string `json:"project"`
		Source  string `json:"source"`
		Model   string `json:"model"`
		Detail  bool   `json:"detail"`
		Split   bool   `json:"split"`
		// Provider replaces the routing of this task for this call only (the retry after a refusal).
		Provider string `json:"provider"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if !api.ProjectIDPattern.MatchString(req.Project) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "project must name a project")
		return
	}
	if _, err := os.Stat(h.host.ProjectDir(req.Project)); err != nil || !h.host.ProjectVisible(req.Project) {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("project %q does not exist", req.Project))
		return
	}
	if !config.ValidSourceFile(req.Source) || filepath.Base(req.Source) != req.Source {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "source must be the name of a picture or clip in the project's sources")
		return
	}
	if _, err := describe.KindOf(req.Source); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	path := filepath.Join(h.host.SourcesDir(req.Project), req.Source)
	if _, err := os.Stat(path); err != nil {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("the project has no source %q", req.Source))
		return
	}
	kind, _ := describe.KindOf(req.Source)
	task := describe.TaskOf(kind)
	ctx := llm.WithProvider(r.Context(), req.Provider)
	if err := llm.Check(task, req.Provider); err != nil && api.WriteRoutingError(w, err) {
		return
	}
	// Only a model that loads into this machine's memory waits for a run in progress.
	if id, busy := h.host.AnyBusy(); busy && api.NeedsLocalMemory(ctx, task) {
		api.WriteError(w, http.StatusConflict, api.CodeConflict,
			fmt.Sprintf("run %s is queued or active: the vision model needs several GB of memory, so describing waits until no run is in progress", id))
		return
	}
	run := h.DescribeRun
	if run == nil {
		run = defaultDescribeRun
	}
	res, err := run(ctx, path, describe.Options{Model: req.Model, Detail: req.Detail, Split: req.Split})
	if err != nil {
		if errors.Is(err, describe.ErrMinor) {
			_ = os.Remove(path) // nothing of a refused file is kept
		}
		writeDescribeError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, res)
}

// writeDescribeError answers each failure of the describe step with its own status.
func writeDescribeError(w http.ResponseWriter, err error) {
	if api.WriteRoutingError(w, err) {
		return
	}
	switch {
	case errors.Is(err, describe.ErrInvalidInput):
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
	case errors.Is(err, describe.ErrMinor):
		api.WriteError(w, http.StatusUnprocessableEntity, codeDescribeMinor, err.Error()+"; the file was deleted")
	case errors.Is(err, describe.ErrRefused):
		body := api.APIErrorBody{Code: codeDescribeRefused, Message: err.Error()}
		var re *describe.RefusedError
		if errors.As(err, &re) {
			body.Provider = re.Provider
			if len(re.Choices) > 0 {
				body.Alternatives = re.Choices
			}
		}
		api.WriteJSON(w, http.StatusUnprocessableEntity, api.APIError{Error: body})
	case errors.Is(err, assist.ErrUnreachable), errors.Is(err, assist.ErrNotInstalled), errors.Is(err, assist.ErrNoMemory):
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, err.Error())
	default:
		api.WriteError(w, http.StatusBadGateway, api.CodeInternal, err.Error())
	}
}

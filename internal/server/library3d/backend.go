package library3d

// backend.go: the change of how the 3D engines run on this machine (the setting model3d_backend, one for both), the same two ways as the music
// generators: a .NET service per engine (triposr-service, stablefast3d-service; the default) or the container of docker/model3d, which stays as the fallback.

import (
	"context"
	"net/http"

	"veritmo/internal/assets3d/generate"
	"veritmo/internal/platform/musicsvc"
)

// BackendError is why the backend cannot be changed now: Status is the HTTP status the settings answer with.
type BackendError struct {
	Status int
	Reason string
	Msg    string
}

func (e *BackendError) Error() string { return e.Msg }

// PrepareBackend is called by the settings before the backend changes to `to` ("" is the default, the .NET service). It checks that Docker
// answers when the container is asked for, and stops what runs on the old backend so that nothing is left on the card; it saves nothing.
// A server that has no 3D service, or that uses one somewhere else (MODEL3D_HOST), has nothing to change here.
func (h *Handler) PrepareBackend(ctx context.Context, to string) *BackendError {
	if h.svc == nil {
		return &BackendError{Status: http.StatusConflict, Msg: "the 3D generator is not set up on this server"}
	}
	if h.svc.IsExternal() {
		return &BackendError{Status: http.StatusConflict, Msg: "this server uses the 3D service at " + h.svc.External.Host + " (MODEL3D_HOST): how it runs is decided there"}
	}
	if to == "" {
		to = musicsvc.BackendDotnet
	}
	if to == musicsvc.BackendDocker {
		if st := (musicsvc.Local{Runner: h.svc.Runner}).DockerCheck(ctx); !st.Daemon {
			return &BackendError{Status: http.StatusUnprocessableEntity, Reason: musicsvc.ReasonNoDocker, Msg: "docker cannot be used here: " + st.Error}
		}
	}
	current := h.svc.BackendOf(generate.EngineTripoSR)
	if current == to {
		return nil
	}
	if h.generating() {
		return &BackendError{Status: http.StatusConflict, Msg: "a run is making 3D models: wait for it to end or cancel it before changing the backend"}
	}
	for _, engine := range generate.Engines() {
		if err := h.svc.Stop(ctx, engine); err != nil {
			return &BackendError{Status: http.StatusInternalServerError, Msg: "could not stop the " + current + " backend of " + engine + ": " + err.Error()}
		}
	}
	return nil
}

// generating says whether a run is making 3D models now (a generation of POST /api/models3d/generations or a project of the models3d mode): stopping
// the service under it would end it half way.
func (h *Handler) generating() bool { return h.ws != nil && h.ws.UsesModels3D() }

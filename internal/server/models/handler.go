// Package models is the family of the models and the files they live in: the inventory of the assets on disk (models,
// voices, adapters...), the download manager (queue, progress, conversion of a checkpoint), the Civitai lookup of the
// private edition and the vision service of the video-edit tools. The catalog itself is embedded by the server and
// reaches the family through its Host.
package models

import (
	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/platform/runner"
	"veritmo/internal/server/api"
	"veritmo/internal/server/modelcat"
)

// Host is what the family needs from the server. The values tests replace after the server is built are asked
// every time.
type Host interface {
	// AssetsRoot is the directory holding models/ and voices/; LookPath finds a program on the PATH; Runner runs
	// the external commands.
	AssetsRoot() string
	LookPath(name string) (string, error)
	Runner() runner.CommandRunner
	// AnyBusy names a pipeline run that is queued or active, if there is one; ModelsIssue says why the models
	// directory cannot be written, if it cannot.
	AnyBusy() (string, bool)
	ModelsIssue() error
	// ShowsAdult says the server shows adult content too; Catalog is the embedded catalog of recommended models.
	ShowsAdult() bool
	Catalog() []modelcat.Entry
	// HuggingFaceKey is the Hugging Face key of the account, if any.
	HuggingFaceKey() string
	// DiffRhythm2CLI is the program that installs the DiffRhythm2 files, or why there is none.
	DiffRhythm2CLI() (exe, why string)
	// VisionEngine is the engine that runs the vision service, and OverlayVision adds what the server knows of it
	// (whether its container is up) to its availability.
	VisionEngine() *musicsvc.Engine
	OverlayVision(a *musicsvc.Availability)
}

// Handler serves the endpoints of the family.
type Handler struct {
	host Host
	dm   *downloadManager
}

// New makes the handler of the family on top of the host.
func New(host Host) *Handler {
	h := &Handler{host: host}
	h.dm = newDownloadManager(host.HuggingFaceKey)
	return h
}

// Register registers the endpoints of the family.
func (h *Handler) Register(handle api.HandleFunc) {
	// model-management
	handle("GET /api/assets", h.handleAssetsList)
	handle("GET /api/assets/catalog", h.handleAssetsCatalog)
	handle("GET /api/civitai/resolve", h.handleCivitaiResolve)
	handle("DELETE /api/assets/{kind}/{name}", h.handleAssetDelete)
	handle("POST /api/downloads", h.handleDownloadCreate)
	handle("GET /api/downloads", h.handleDownloadsList)
	handle("GET /api/downloads/{id}", h.handleDownloadGet)
	handle("POST /api/downloads/{id}/cancel", h.handleDownloadCancel)
	handle("GET /api/downloads/{id}/events", h.handleDownloadEvents)
}

// Shutdown cancels the downloads in progress and waits for them.
func (h *Handler) Shutdown() { h.dm.shutdown() }

// SweepPartials removes the leftovers of interrupted downloads.
func (h *Handler) SweepPartials() { h.sweepPartials() }

// HFToken is the Hugging Face key the downloads use ("" when there is none).
func (h *Handler) HFToken() string { return h.dm.hfToken() }

// CivitaiToken is the Civitai key the downloads use ("" when there is none).
func (h *Handler) CivitaiToken() string { return h.dm.civitaiToken() }

// Vision is the vision service as the video-edit tools use it.
func (h *Handler) Vision() Vision { return Vision{host: h.host} }

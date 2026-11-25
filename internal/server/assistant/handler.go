// Package assistant is the family of the prompt assistant of the server: POST /api/assist and its request
// planner, the description of a picture or clip, the speech preview, the library of motion references and the
// model providers the assistant is routed through.
package assistant

import (
	"context"
	"net/http"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/assistant/describe"
	"veritmo/internal/music/motionref"
	"veritmo/internal/platform/runner"
	"veritmo/internal/platform/store"
	"veritmo/internal/server/api"
)

// Host is what the assistant needs from the server.
type Host interface {
	// ProjectDir is the folder of a project (it may not exist); ProjectVisible says whether the content mode
	// lets the caller see it; SourcesDir is its sources/ folder.
	ProjectDir(id string) string
	ProjectVisible(id string) bool
	SourcesDir(id string) string
	// AnyBusy names a pipeline run that is queued or active, if there is one.
	AnyBusy() (string, bool)
	// Runner runs the external commands (ffmpeg, ffprobe, yt-dlp, piper); AssetsRoot is the folder holding
	// models/ and voices/; LookPath finds a program on the PATH.
	Runner() runner.CommandRunner
	AssetsRoot() string
	LookPath(name string) (string, error)
	// Store is the database of the process: the library of references is kept in it.
	Store() *store.Store
	// DropsAvailability wraps a handler that changes the providers so that the cached availability is dropped.
	DropsAvailability(h http.HandlerFunc) http.HandlerFunc
}

// Handler serves the endpoints of the family. The exported function fields are test seams: nil uses the real
// thing (Ollama, the vision model, the real analysis).
type Handler struct {
	host Host

	// AssistRun generates a prompt for POST /api/assist; nil uses Ollama on the CPU.
	AssistRun func(ctx context.Context, task assist.Task, text, model string) (string, error)
	// AssistMusicRun is the music task of the same endpoint (a style and lyrics); nil uses Ollama.
	AssistMusicRun func(ctx context.Context, text string, opts assist.MusicOptions, model string) (assist.MusicSuggestion, error)
	// AssistSongRun is the lyrics-from-transcript task of the same endpoint; nil uses Ollama.
	AssistSongRun func(ctx context.Context, in assist.TranscriptInput, model string) (assist.SongDraft, error)
	// AssistSVGRun is the svg task of POST /api/assist/svg (a drawing from a description); nil uses Ollama.
	AssistSVGRun func(ctx context.Context, text, model string) (string, error)
	// AssistPlanRun is the plan task of POST /api/assist/plan (a whole video from one sentence); nil uses Ollama.
	AssistPlanRun func(ctx context.Context, in assist.PlanInput, model string) (assist.PlanGuess, error)
	// AssistDatasetRun is the dataset task of POST /api/assist/dataset (several entries from one sentence); nil uses Ollama.
	AssistDatasetRun func(ctx context.Context, in assist.DatasetInput, model string) ([]assist.DatasetEntry, error)
	// AssistModels lists the installed chat models for GET /api/assist/models; nil asks Ollama.
	AssistModels func(ctx context.Context) (string, []assist.ModelInfo, error)
	// DescribeRun reads a picture or clip for POST /api/describe; nil uses the vision model in Ollama.
	DescribeRun func(ctx context.Context, path string, opts describe.Options) (describe.Result, error)
	// DescribeModels lists the vision models for GET /api/describe/models; nil asks Ollama.
	DescribeModels func(ctx context.Context) (string, string, []describe.Model, error)
	// RefAnalyse measures a reference video and writes its key frames (nil: the real analysis).
	RefAnalyse func(ctx context.Context, video, framesDir string) (*motionref.Analysis, error)
	// RefReader opens the vision model that reads the shots of a reference (nil: the one routed to describing).
	RefReader motionref.Reader
	// RefBreakdown finds the shots of a reference video again and fills the analysis (nil: the real one).
	RefBreakdown func(ctx context.Context, video string, an *motionref.Analysis) error

	refs refState
}

// New makes the handler of the family on top of the host.
func New(h Host) *Handler { return &Handler{host: h} }

// RunFunc is the text task of the assistant: the seam, else the real one.
func (h *Handler) RunFunc() func(ctx context.Context, task assist.Task, text, model string) (string, error) {
	if h.AssistRun != nil {
		return h.AssistRun
	}
	return DefaultAssistRun
}

// Register registers the endpoints of the family.
func (h *Handler) Register(handle api.HandleFunc) {
	// prompt-assistant
	handle("POST /api/assist", h.handleAssist)
	handle("POST /api/assist/request", h.handleAssistRequest)
	handle("POST /api/assist/svg", h.handleAssistSVG)
	handle("POST /api/tts/preview", h.HandleSpeechPreview)
	handle("GET /api/assist/models", h.handleAssistModels)
	handle("GET /api/assist/music-tags", h.handleAssistMusicTags)

	// model-providers
	handle("GET /api/providers", h.handleProvidersList)
	handle("GET /api/providers/{id}", h.handleProviderGet)
	handle("GET /api/providers/presets", h.handleProviderPresets)
	handle("POST /api/providers", h.host.DropsAvailability(h.handleProviderCreate))
	handle("PUT /api/providers/{id}", h.host.DropsAvailability(h.handleProviderUpdate))
	handle("DELETE /api/providers/{id}", h.host.DropsAvailability(h.handleProviderDelete))
	handle("POST /api/providers/{id}/test", h.handleProviderTest)

	// describe-reference
	handle("POST /api/describe", h.handleDescribe)
	handle("GET /api/describe/models", h.handleDescribeModels)

	// music-motion references
	handle("POST /api/music-motion/references", h.handleReferenceCreate)
	handle("GET /api/music-motion/references", h.handleReferenceList)
	handle("GET /api/music-motion/references/{id}", h.handleReferenceGet)
	handle("PATCH /api/music-motion/references/{id}", h.handleReferencePatch)
	handle("DELETE /api/music-motion/references/{id}", h.handleReferenceDelete)
	handle("POST /api/music-motion/references/{id}/breakdown", h.handleReferenceBreakdown)
	handle("GET /api/music-motion/references/{id}/frames/{name}", h.handleReferenceFrame)
}

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/assistant/llm"
	"veritmo/internal/platform/settings"
	"veritmo/internal/project/config"
	"veritmo/internal/server/api"
)

// assistRequest is the body of POST /api/assist.
type assistRequest struct {
	Task string `json:"task"`
	Text string `json:"text"`
	// Instrumental asks the music task for the style only.
	Instrumental bool `json:"instrumental,omitempty"`
	// Generator is the track's music generator (ace-step, heartmula, diffrhythm2): the music task then writes
	// the section tags it reads and, for the last two, the tags.
	Generator string `json:"generator,omitempty"`
	// Seconds is the length of the song for the music task: it sets the structure of the lyrics (0: a normal one).
	Seconds int `json:"seconds,omitempty"`
	// Language, Hint, BPM, KeyScale and Picture are the extra input of the lyrics-from-transcript task.
	Language string  `json:"language,omitempty"`
	Hint     string  `json:"hint,omitempty"`
	BPM      float64 `json:"bpm,omitempty"`
	KeyScale string  `json:"keyscale,omitempty"`
	Picture  string  `json:"picture,omitempty"`
	// Model names an installed Ollama model for this request; empty means the
	// server's default (VERITMO_ASSIST_MODEL, else qwen3:8b).
	Model string `json:"model,omitempty"`
	// Provider replaces the routing of the assistant for this call only (see provider-routing).
	Provider string `json:"provider,omitempty"`
	// Examples are prompts of the house style shown to the model as examples (at most 5, never copied).
	Examples []assist.Example `json:"examples,omitempty"`
}

// DefaultAssistRun is how the handler reaches the assistant: Ollama on the CPU, with
// the model from VERITMO_ASSIST_MODEL and the address from the saved setting, else OLLAMA_HOST.
// A test seam on Handler.AssistRun.
func DefaultAssistRun(ctx context.Context, task assist.Task, text, model string) (string, error) {
	a, err := assist.ForContext(ctx, model)
	if err != nil {
		return "", err
	}
	return a.Run(ctx, task, text)
}

// DefaultAssistMusic is the music task of the assistant: a style and lyrics.
// A test seam on Handler.AssistMusicRun.
func DefaultAssistMusic(ctx context.Context, text string, opts assist.MusicOptions, model string) (assist.MusicSuggestion, error) {
	a, err := assist.ForContext(ctx, model)
	if err != nil {
		return assist.MusicSuggestion{}, err
	}
	return a.MusicFor(ctx, text, opts)
}

// DefaultAssistPlan is the plan task of the assistant: the whole video from one sentence.
// A test seam on Handler.AssistPlanRun.
func DefaultAssistPlan(ctx context.Context, in assist.PlanInput, model string) (assist.PlanGuess, error) {
	a, err := assist.ForContext(ctx, model)
	if err != nil {
		return assist.PlanGuess{}, err
	}
	return a.Plan(ctx, in)
}

// DefaultAssistSong is the lyrics-from-transcript task. A test seam on Handler.AssistSongRun.
func DefaultAssistSong(ctx context.Context, in assist.TranscriptInput, model string) (assist.SongDraft, error) {
	a, err := assist.ForContext(ctx, model)
	if err != nil {
		return assist.SongDraft{}, err
	}
	return a.LyricsFromTranscript(ctx, in)
}

// DefaultAssistModels lists the chat models installed in Ollama and names the
// one a request without a model would use. A test seam on Handler.AssistModels.
func DefaultAssistModels(ctx context.Context) (string, []assist.ModelInfo, error) {
	a := assist.FromEnv("")
	models, err := a.ListModels(ctx)
	return a.Model, models, err
}

// GET /api/assist/models -> {"default": "...", "models": [{"name", "size_bytes", "fits"}]}
func (h *Handler) handleAssistModels(w http.ResponseWriter, r *http.Request) {
	list := h.AssistModels
	if list == nil {
		list = DefaultAssistModels
	}
	def, models, err := list(r.Context())
	if err != nil {
		api.WriteAssistError(w, err)
		return
	}
	if models == nil {
		models = []assist.ModelInfo{}
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"default": def, "models": models})
}

// GET /api/assist/music-tags -> {"categories": [{"id", "tags": [...]}], "combos": [{"id", "name", "tags": [...]}]}
//
// The vocabulary of the tags HeartMuLa is asked for, by category, and the popular combinations of them: the one the assistant chooses from and
// the server checks against, so that a client offers the same tags.
func (h *Handler) handleAssistMusicTags(w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, http.StatusOK, map[string]any{"categories": assist.MusicTagCategories, "combos": assist.MusicTagCombos})
}

// POST /api/assist {"task": "image"|"motion"|"music", "text": "...", "instrumental"?: bool}
//
//	-> {"text": "..."}                        (image, motion)
//	-> {"text", "caption", "lyrics"}           (music)
//
// The assistant needs several GB of memory while the model is loaded, so it
// is refused while a pipeline run is queued or active.
func (h *Handler) handleAssist(w http.ResponseWriter, r *http.Request) {
	var req assistRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	task, err := assist.ParseTask(req.Task)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	if task == assist.TaskLyrics {
		if err := validateSongInput(req); err != nil {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
			return
		}
	} else if err := assist.ValidateText(req.Text); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	if err := assist.ValidateExamples(req.Examples); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	ctx, ok := h.Gate(w, r, req.Provider)
	if !ok {
		return
	}
	ctx = assist.WithExamples(ctx, req.Examples)
	if task == assist.TaskMusic {
		run := h.AssistMusicRun
		if run == nil {
			run = DefaultAssistMusic
		}
		out, err := run(ctx, req.Text, assist.MusicOptions{Instrumental: req.Instrumental, Engine: req.Generator, Seconds: req.Seconds}, req.Model)
		if err != nil {
			api.WriteAssistError(w, err)
			return
		}
		ans := map[string]any{"text": out.Raw, "caption": out.Caption, "lyrics": out.Lyrics}
		if tags := assistTags(out.Tags); tags != "" {
			ans["tags"] = tags
		}
		api.WriteJSON(w, http.StatusOK, ans)
		return
	}
	if task == assist.TaskLyrics {
		run := h.AssistSongRun
		if run == nil {
			run = DefaultAssistSong
		}
		out, err := run(ctx, assist.TranscriptInput{Text: req.Text, Language: req.Language, Hint: req.Hint, BPM: req.BPM, Key: req.KeyScale, Picture: req.Picture, Engine: req.Generator}, req.Model)
		if err != nil {
			api.WriteAssistError(w, err)
			return
		}
		ans := map[string]any{"text": out.Raw, "caption": out.Caption, "lyrics": out.Lyrics, "unchanged_words": out.UnchangedWords}
		if tags := assistTags(out.Tags); tags != "" {
			ans["tags"] = tags
		}
		api.WriteJSON(w, http.StatusOK, ans)
		return
	}
	run := h.AssistRun
	if run == nil {
		run = DefaultAssistRun
	}
	out, err := run(ctx, task, req.Text, req.Model)
	if err != nil {
		api.WriteAssistError(w, err)
		return
	}
	if task == assist.TaskTags {
		// The tags are given in the form the generator takes (lower case, separated by commas).
		out = assistTags(out)
	}
	api.WriteJSON(w, http.StatusOK, map[string]string{"text": out})
}

// Gate applies what every call of the assistant shares: the routing of the task (and the
// consent for a remote provider) and the wait for a run in progress, for a model that loads here.
// It answers the error itself and returns the context carrying the provider.
func (h *Handler) Gate(w http.ResponseWriter, r *http.Request, provider string) (context.Context, bool) {
	ctx := llm.WithProvider(r.Context(), provider)
	if err := llm.Check(settings.TaskAssistant, provider); err != nil && api.WriteRoutingError(w, err) {
		return ctx, false
	}
	// Only a model that loads into this machine's memory waits for a run in progress.
	if id, busy := h.host.AnyBusy(); busy && api.NeedsLocalMemory(ctx, settings.TaskAssistant) {
		api.WriteError(w, http.StatusConflict, api.CodeConflict,
			fmt.Sprintf("run %s is queued or active: the prompt assistant needs several GB of memory, so it waits until no run is in progress", id))
		return ctx, false
	}
	return ctx, true
}

// assistTags gives the tags a model wrote in the form the generators take: lower case, separated by
// commas, with no full stop at the end.
func assistTags(s string) string {
	return string(config.NormalizeTags(strings.Trim(strings.TrimSpace(s), ".,")))
}

// validateSongInput checks the extra input of the lyrics-from-transcript task. The text may be empty (a
// song with no words) and is as long as a transcription.
func validateSongInput(req assistRequest) error {
	switch {
	case len([]rune(req.Text)) > assist.MaxTranscriptLen:
		return fmt.Errorf("the text is longer than %d characters", assist.MaxTranscriptLen)
	case len([]rune(req.Hint)) > 500:
		return errors.New("the hint is longer than 500 characters")
	case len([]rune(req.Picture)) > 2000:
		return errors.New("the picture description is longer than 2000 characters")
	case req.Language != "" && !api.LanguagePattern.MatchString(req.Language):
		return errors.New("language must be an ISO 639-1 code such as es or en")
	case req.BPM < 0 || req.BPM > 400:
		return errors.New("bpm must be a number from 0 to 400")
	case len(req.KeyScale) > 20:
		return errors.New("keyscale is too long")
	}
	if strings.TrimSpace(req.Text) == "" && strings.TrimSpace(req.Hint) == "" && req.BPM == 0 && req.KeyScale == "" && strings.TrimSpace(req.Picture) == "" {
		return errors.New("give the transcription or at least something about the song")
	}
	return nil
}

// DefaultAssistSVG is the svg task of the assistant: a drawing from a description. A test seam on Handler.AssistSVGRun.
func DefaultAssistSVG(ctx context.Context, text, model string) (string, error) {
	a, err := assist.ForContext(ctx, model)
	if err != nil {
		return "", err
	}
	return a.SVG(ctx, text)
}

// DefaultAssistDataset is the dataset task of the assistant: entries from one sentence. A test seam on Handler.AssistDatasetRun.
func DefaultAssistDataset(ctx context.Context, in assist.DatasetInput, model string) ([]assist.DatasetEntry, error) {
	a, err := assist.ForContext(ctx, model)
	if err != nil {
		return nil, err
	}
	return a.Dataset(ctx, in)
}

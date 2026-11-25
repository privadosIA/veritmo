package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/server/api"
)

// requestAttachments says which kinds of file the user attached to a request. The files themselves are
// uploaded apart; here they only change what the model is asked to write.
type requestAttachments struct {
	Image bool `json:"image,omitempty"`
	Video bool `json:"video,omitempty"`
	Audio bool `json:"audio,omitempty"`
}

// requestPlanBody is the body of POST /api/assist/request.
type requestPlanBody struct {
	Text string `json:"text"`
	// Language is the language of the request when the caller knows it (an ISO 639-1 code).
	Language string `json:"language,omitempty"`
	// Kind fixes video, image, music or model3d; the model is then not asked to decide it (and never decides model3d).
	Kind      string `json:"kind,omitempty"`
	Generator string `json:"generator,omitempty"`
	// Instrumental fixes whether a song has a voice when the caller already knows (a new suggestion for the
	// same card); absent, the model reads it from the request.
	Instrumental *bool `json:"instrumental,omitempty"`
	// Seconds is the length of a song when the caller knows it (the card's choice); it sets the structure of the lyrics.
	Seconds     int                `json:"seconds,omitempty"`
	Attachments requestAttachments `json:"attachments,omitempty"`
	Model       string             `json:"model,omitempty"`
	Provider    string             `json:"provider,omitempty"`
}

// requestPlan is the answer: what was asked for and the texts that kind needs.
type requestPlan struct {
	Kind       string `json:"kind"`
	KindSource string `json:"kind_source"` // user, model, attachment or default
	Title      string `json:"title"`
	Language   string `json:"language,omitempty"`
	// ImagePrompt is for an image or a video that starts from no picture; MotionPrompt for a video.
	ImagePrompt  string `json:"image_prompt,omitempty"`
	MotionPrompt string `json:"motion_prompt,omitempty"`
	// ModelPrompt is for a 3D model: an English description of one object alone.
	ModelPrompt string `json:"model_prompt,omitempty"`
	// Speech is what a video has to say, taken from the request without a model call.
	Speech string `json:"speech,omitempty"`
	// Caption, Lyrics and Tags are the music task's: a style, lyrics (empty when instrumental) and, for
	// the engines that read them, tags.
	Caption      string   `json:"caption,omitempty"`
	Lyrics       string   `json:"lyrics,omitempty"`
	Tags         string   `json:"tags,omitempty"`
	Instrumental bool     `json:"instrumental"`
	Seconds      int      `json:"seconds,omitempty"`
	Warnings     []string `json:"warnings"`
}

// POST /api/assist/request {"text", "language"?, "kind"?, "generator"?, "attachments"?} -> requestPlan (kind model3d: model_prompt)
//
// Turns a free-form request into what the generator needs: the kind (decided by the caller, the model,
// the attachments or by default, in that order), a title and the texts of that kind, written by the
// tasks the assistant already has. Everything those tasks refuse still applies: the model's refusal, an
// unreachable model, short memory and the wait for a run in progress. When the kind is known but a text
// cannot be written, the answer carries the kind and the title with a warning and that text empty.
func (h *Handler) handleAssistRequest(w http.ResponseWriter, r *http.Request) {
	var req requestPlanBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if err := assist.ValidateText(req.Text); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	if req.Seconds < 0 || req.Seconds > 1800 {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "seconds must be a number from 0 to 1800")
		return
	}
	if req.Language != "" && !api.LanguagePattern.MatchString(req.Language) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "language must be an ISO 639-1 code such as es or en")
		return
	}
	var fixed assist.Kind
	if req.Kind != "" {
		k, err := assist.ParseKind(req.Kind)
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
			return
		}
		fixed = k
	}
	ctx, ok := h.Gate(w, r, req.Provider)
	if !ok {
		return
	}
	text := strings.TrimSpace(req.Text)
	plan := requestPlan{Language: req.Language, Warnings: []string{}}

	// 1. The kind, the title and the details the request names.
	var guess assist.KindGuess
	if fixed != "" {
		plan.Kind, plan.KindSource = string(fixed), "user"
	} else {
		reply, err := h.runTask(ctx, assist.TaskKind, text, req.Model)
		if err != nil {
			api.WriteAssistError(w, err)
			return
		}
		guess = assist.ParseKindReply(reply)
		switch {
		case guess.Understood:
			plan.Kind, plan.KindSource = string(guess.Kind), "model"
		case req.Attachments.Audio:
			plan.Kind, plan.KindSource = string(assist.KindMusic), "attachment"
		case req.Attachments.Image || req.Attachments.Video:
			plan.Kind, plan.KindSource = string(assist.KindVideo), "attachment"
		default:
			plan.Kind, plan.KindSource = string(assist.KindVideo), "default"
		}
	}
	plan.Title = guess.Title
	if plan.Title == "" {
		plan.Title = assist.TitleFrom(text)
	}
	if plan.Language == "" && api.LanguagePattern.MatchString(guess.Language) {
		plan.Language = guess.Language
	}
	plan.Instrumental, plan.Seconds = guess.Instrumental, guess.Seconds
	if req.Instrumental != nil {
		plan.Instrumental = *req.Instrumental
	}

	// 2. The texts of that kind. A text that cannot be written is a warning, not the end of the request.
	kind := assist.Kind(plan.Kind)
	picture := req.Attachments.Image || req.Attachments.Video
	if kind == assist.KindVideo {
		// The words are not described to the picture or movement tasks: a quote would be drawn as text.
		plan.Speech, text = assist.SplitSpeech(text)
		if text == "" {
			text = strings.TrimSpace(req.Text)
		}
	}
	var refusal error
	warn := func(what string, err error) {
		if errors.Is(err, assist.ErrRefused) {
			refusal = err
		}
		plan.Warnings = append(plan.Warnings, fmt.Sprintf("the %s could not be written: %s", what, err))
	}
	// A picture to edit or to start from has no scene to describe: the user's words are turned into the short
	// English instruction an editing model follows (an instruction such as "put a red cap on him" must not be
	// rewritten as a description). If that cannot be done they go to the generator as they are.
	if kind == assist.KindImage && picture {
		if out, err := h.runTask(ctx, assist.TaskEdit, text, req.Model); err == nil {
			plan.ImagePrompt = assist.EditInstruction(out)
		} else if ctx.Err() != nil {
			return
		} else {
			warn("picture instruction", err)
		}
	}
	if (kind == assist.KindImage || kind == assist.KindVideo) && !picture {
		if out, err := h.runTask(ctx, assist.TaskImage, text, req.Model); err == nil {
			plan.ImagePrompt = out
		} else if ctx.Err() != nil {
			return
		} else {
			warn("image prompt", err)
		}
	}
	if kind == assist.KindModel3D {
		// the picture of a prompt is drawn when the model is made, with the background wording the engines need: the prompt is the object alone
		if out, err := h.runTask(ctx, assist.TaskModel3D, text, req.Model); err == nil {
			plan.ModelPrompt = out
		} else if ctx.Err() != nil {
			return
		} else {
			warn("model prompt", err)
		}
	}
	if kind == assist.KindVideo {
		scene := text
		if picture {
			scene += " (The scene starts from a picture the user supplied: describe only how it moves.)"
		}
		if out, err := h.runTask(ctx, assist.TaskMotion, scene, req.Model); err == nil {
			plan.MotionPrompt = out
		} else if ctx.Err() != nil {
			return
		} else {
			warn("movement prompt", err)
		}
	}
	if kind == assist.KindMusic {
		brief := text
		if req.Attachments.Audio {
			brief += " (The user supplied a song as a reference: write a style that complements it.)"
		}
		run := h.AssistMusicRun
		if run == nil {
			run = DefaultAssistMusic
		}
		out, err := run(ctx, brief, assist.MusicOptions{Instrumental: plan.Instrumental, Engine: req.Generator, Seconds: SongSeconds(plan.Seconds, req.Seconds)}, req.Model)
		switch {
		case err == nil:
			plan.Caption, plan.Lyrics = out.Caption, out.Lyrics
			plan.Tags = assistTags(out.Tags)
			if out.NoGenre {
				plan.Warnings = append(plan.Warnings, "the tags have no genre of the vocabulary: choose one")
			}
		case ctx.Err() != nil:
			return
		default:
			warn("song", err)
		}
	}
	// A refusal of the model is not hidden behind a warning when nothing at all could be written: the
	// kind alone is of no use to someone whose request was refused.
	if refusal != nil && plan.ImagePrompt == "" && plan.MotionPrompt == "" && plan.Caption == "" && plan.ModelPrompt == "" {
		api.WriteAssistError(w, refusal)
		return
	}
	api.WriteJSON(w, http.StatusOK, plan)
}

// SongSeconds is the length that shapes the lyrics: the one the caller gave, else the one the request names.
func SongSeconds(named, given int) int {
	if given > 0 {
		return given
	}
	return named
}

// runTask runs a one-line task of the assistant through the test seam.
func (h *Handler) runTask(ctx context.Context, task assist.Task, text, model string) (string, error) {
	run := h.AssistRun
	if run == nil {
		run = DefaultAssistRun
	}
	out, err := run(ctx, task, text, model)
	if err != nil && errors.Is(err, context.Canceled) {
		return "", err
	}
	return out, err
}

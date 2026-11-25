package music

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/music/musicmotion"
	"veritmo/internal/project/config"
	"veritmo/internal/server/api"
	"veritmo/internal/server/assistant"
)

// PlanRequest is the body of POST /api/assist/plan.
type PlanRequest struct {
	Text string `json:"text"`
	// Generator is the track's generator (ace-step, heartmula, diffrhythm2): it shapes the song's lyrics and tags.
	Generator string `json:"generator,omitempty"`
	Language  string `json:"language,omitempty"`
	// Seconds is the length of the song when the person already chose it (1 to 600); without it the model reads it from the text.
	Seconds  int              `json:"seconds,omitempty"`
	Examples []assist.Example `json:"examples,omitempty"`
	Model    string           `json:"model,omitempty"`
	Provider string           `json:"provider,omitempty"`
}

// PlanSong is the song's half of a plan.
type PlanSong struct {
	Title        string `json:"title"`
	Caption      string `json:"caption"`
	Lyrics       string `json:"lyrics"`
	Tags         string `json:"tags,omitempty"`
	Instrumental bool   `json:"instrumental"`
	Seconds      int    `json:"seconds,omitempty"`
}

// PlanAnimation is the animation's half: only values the catalog offers (an empty one is a warning).
type PlanAnimation struct {
	Style   string `json:"style"`
	Level   string `json:"level"`
	FPS     int    `json:"fps,omitempty"`
	Mode    string `json:"mode"`
	Concept string `json:"concept"`
	// Prompt is the sentence itself: what the piece and its proposals take as the person's direction.
	Prompt string `json:"prompt"`
	// World, Device and Hook are the storyline the scenes of the video share: where it happens, which of the devices
	// carries it and what the first two seconds show. Note says what was changed when the model's device was not one offered.
	World  string `json:"world,omitempty"`
	Device string `json:"device"`
	Hook   string `json:"hook,omitempty"`
	Note   string `json:"note,omitempty"`
}

// PlanPart is a part worth having: Exists says it is in the catalog; Request is the sentence for a draft when it is not.
type PlanPart struct {
	Name    string `json:"name"`
	Exists  bool   `json:"exists"`
	Request string `json:"request,omitempty"`
}

// PlanAnswer is the answer of POST /api/assist/plan.
type PlanAnswer struct {
	Song      PlanSong      `json:"song"`
	Animation PlanAnimation `json:"animation"`
	Parts     []PlanPart    `json:"parts"`
	Warnings  []string      `json:"warnings"`
}

// clipLine is a one-line text of a plan cut to what a piece keeps.
func clipLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > assist.MaxPlanLine {
		s = string(r[:assist.MaxPlanLine])
	}
	return s
}

func oneOf(v string, list []string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// POST /api/assist/plan {"text", "generator"?, "language"?, "model"?, "provider"?} -> PlanAnswer
//
// One sentence to the plan of a whole video: the song's brief (caption, lyrics, tags, length), the animation's choices
// (style, level, frame rate, mode, concept) and the parts worth having, checked against the catalog. It has the guards
// of the other assistant calls (routing and consent, the wait for a run in progress, the memory rule, the model's own
// refusal). What the model cannot fill comes empty with a warning; a refusal with nothing at all written is 422.
func (h *Handler) handleAssistPlan(w http.ResponseWriter, r *http.Request) {
	var req PlanRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if err := assist.ValidateText(req.Text); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	if req.Language != "" && !api.LanguagePattern.MatchString(req.Language) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "language must be an ISO 639-1 code such as es or en")
		return
	}
	if req.Seconds < 0 || req.Seconds > 600 {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "seconds must be between 1 and 600")
		return
	}
	if err := assist.ValidateExamples(req.Examples); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	ctx, ok := h.assistant.Gate(w, r, strings.TrimSpace(req.Provider))
	if !ok {
		return
	}
	text := strings.TrimSpace(req.Text)
	cat := h.mmCatalog()
	modes := []string{musicmotion.ModeDark, musicmotion.ModeLight}
	in := assist.PlanInput{Text: text, Language: req.Language, Seconds: req.Seconds, Styles: musicmotion.Styles, Levels: musicmotion.Levels, FrameRates: musicmotion.FrameRates, Modes: modes}
	for _, p := range cat.List() {
		in.Parts = append(in.Parts, assist.PlanPart{Name: p.Name, Description: p.Description})
	}
	planRun := h.assistant.AssistPlanRun
	if planRun == nil {
		planRun = assistant.DefaultAssistPlan
	}
	g, err := planRun(assist.WithExamples(ctx, req.Examples), in, req.Model)
	if err != nil {
		api.WriteAssistError(w, err)
		return
	}

	ans := PlanAnswer{Parts: []PlanPart{}, Warnings: []string{}}
	warn := func(format string, a ...any) { ans.Warnings = append(ans.Warnings, fmt.Sprintf(format, a...)) }

	// The animation: only values the catalog offers.
	an := &ans.Animation
	an.Prompt, an.Concept = text, g.Concept
	if an.Concept == "" {
		warn("the concept could not be written")
	}
	an.World, an.Hook = clipLine(g.World), clipLine(g.Hook)
	switch {
	case oneOf(g.Device, assist.PlanDevices):
		an.Device = g.Device
	default:
		an.Device = assist.PlanDeviceDefault
		if g.Device == "" {
			an.Note = fmt.Sprintf("the model gave no device: %q was used", an.Device)
		} else {
			an.Note = fmt.Sprintf("the model's device %q is not one of the devices offered: %q was used", g.Device, an.Device)
		}
		warn("%s", an.Note)
	}
	if an.World == "" {
		warn("the world could not be written")
	}
	if an.Hook == "" {
		warn("the hook could not be written")
	}
	if oneOf(g.Style, musicmotion.Styles) {
		an.Style = g.Style
	} else {
		warn("the style %q is not one of %s: choose one", g.Style, strings.Join(musicmotion.Styles, ", "))
	}
	if oneOf(g.Level, musicmotion.Levels) {
		an.Level = g.Level
	} else {
		warn("the level %q is not one of %s: choose one", g.Level, strings.Join(musicmotion.Levels, ", "))
	}
	for _, f := range musicmotion.FrameRates {
		if f == g.FPS {
			an.FPS = f
		}
	}
	if an.FPS == 0 {
		warn("the frame rate %d is not one the server offers: choose one", g.FPS)
	}
	if oneOf(g.Mode, modes) {
		an.Mode = g.Mode
	} else {
		warn("the mode %q is not one of %s: choose one", g.Mode, strings.Join(modes, ", "))
	}

	// The parts: those in the catalog as they are, a new one with the sentence that asks for it.
	seen := map[string]bool{}
	for _, p := range g.Parts {
		if len(ans.Parts) == assist.MaxPlanParts || seen[p.Name] {
			continue
		}
		seen[p.Name] = true
		if _, ok := cat.Get(p.Name); ok {
			ans.Parts = append(ans.Parts, PlanPart{Name: p.Name, Exists: true})
			continue
		}
		if n := len([]rune(p.Request)); n == 0 || n > 500 {
			warn("the new part %q was left out: its description has %d characters (1 to 500 are needed)", p.Name, n)
			continue
		}
		ans.Parts = append(ans.Parts, PlanPart{Name: p.Name, Request: p.Request})
	}

	// The song: the title and the length from the plan, the texts from the music task.
	ans.Song = PlanSong{Title: g.Title, Instrumental: g.Instrumental, Seconds: g.Seconds}
	if ans.Song.Title == "" {
		ans.Song.Title = assist.TitleFrom(text)
	}
	if req.Seconds > 0 {
		g.Seconds = req.Seconds
		ans.Song.Seconds = req.Seconds
	}
	if g.Seconds == 0 {
		warn("the length of the song is not in the request: choose one")
	}
	musicRun := h.assistant.AssistMusicRun
	if musicRun == nil {
		musicRun = assistant.DefaultAssistMusic
	}
	out, err := musicRun(assist.WithExamples(ctx, req.Examples), text, assist.MusicOptions{Instrumental: g.Instrumental, Engine: req.Generator, Seconds: g.Seconds}, req.Model)
	switch {
	case err == nil:
		ans.Song.Caption, ans.Song.Lyrics = out.Caption, out.Lyrics
		ans.Song.Tags = string(config.NormalizeTags(strings.Trim(strings.TrimSpace(out.Tags), ".,")))
		if out.NoGenre {
			warn("the tags have no genre of the vocabulary: choose one")
		}
	case ctx.Err() != nil:
		return
	case errors.Is(err, assist.ErrRefused) && an.Concept == "" && len(ans.Parts) == 0:
		api.WriteAssistError(w, err)
		return
	default:
		warn("the song could not be written: %s", err)
	}
	api.WriteJSON(w, http.StatusOK, ans)
}

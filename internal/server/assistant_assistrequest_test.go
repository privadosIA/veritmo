package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/server/assistant"
)

type plannedRequest struct {
	Kind         string   `json:"kind"`
	KindSource   string   `json:"kind_source"`
	Title        string   `json:"title"`
	Language     string   `json:"language"`
	ImagePrompt  string   `json:"image_prompt"`
	MotionPrompt string   `json:"motion_prompt"`
	ModelPrompt  string   `json:"model_prompt"`
	Speech       string   `json:"speech"`
	Caption      string   `json:"caption"`
	Lyrics       string   `json:"lyrics"`
	Instrumental bool     `json:"instrumental"`
	Seconds      int      `json:"seconds"`
	Warnings     []string `json:"warnings"`
}

// plannerServer answers each task with a canned text and remembers what it was asked.
func plannerServer(t *testing.T, kindReply string) (*Server, *[]string) {
	t.Helper()
	s, _ := newProjectsServer(t)
	asked := &[]string{}
	s.assistant.AssistRun = func(ctx context.Context, task assist.Task, text, model string) (string, error) {
		*asked = append(*asked, string(task)+"|"+text)
		switch task {
		case assist.TaskKind:
			return kindReply, nil
		case assist.TaskImage:
			return "A tabby cat on a rooftop at sunset.", nil
		case assist.TaskMotion:
			return "Slow push-in, the cat's tail sways.", nil
		case assist.TaskEdit:
			return "Make the hair of the man on the left blonde", nil
		}
		return "", errors.New("unexpected task")
	}
	s.assistant.AssistMusicRun = func(ctx context.Context, text string, o assist.MusicOptions, model string) (assist.MusicSuggestion, error) {
		*asked = append(*asked, "music|"+text)
		lyrics := "[Verse]\nlluvia en la estación"
		if o.Instrumental {
			lyrics = ""
		}
		return assist.MusicSuggestion{Caption: "sad piano ballad", Lyrics: lyrics}, nil
	}
	return s, asked
}

func planRequest(t *testing.T, s *Server, body map[string]any) (int, plannedRequest, string) {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist/request", body)
	var p plannedRequest
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	return rec.Code, p, rec.Body.String()
}

func TestAssistRequest_AVideoGetsAnImageAndAMovementPrompt(t *testing.T) {
	s, asked := plannerServer(t, "KIND=video; LANG=es; TITLE=Gato en la azotea; INSTRUMENTAL=no; SECONDS=0")
	code, p, body := planRequest(t, s, map[string]any{"text": "un gato en una azotea al atardecer"})
	if code != http.StatusOK || p.Kind != "video" || p.KindSource != "model" || p.Title != "Gato en la azotea" || p.Language != "es" ||
		p.ImagePrompt == "" || p.MotionPrompt == "" || len(p.Warnings) != 0 {
		t.Fatalf("%d %s", code, body)
	}
	if len(*asked) != 3 {
		t.Errorf("asked %v", *asked)
	}
}

func TestAssistRequest_ASongHonoursInstrumentalAndLength(t *testing.T) {
	s, _ := plannerServer(t, "KIND=music; LANG=es; TITLE=Despedida; INSTRUMENTAL=yes; SECONDS=90")
	code, p, body := planRequest(t, s, map[string]any{"text": "una canción triste de piano, instrumental, de minuto y medio"})
	if code != http.StatusOK || p.Kind != "music" || !p.Instrumental || p.Seconds != 90 || p.Caption == "" || p.Lyrics != "" || p.ImagePrompt != "" {
		t.Fatalf("%d %s", code, body)
	}
	s, _ = plannerServer(t, "KIND=music; LANG=es; TITLE=Despedida; INSTRUMENTAL=no; SECONDS=0")
	_, p, body = planRequest(t, s, map[string]any{"text": "una canción triste"})
	if p.Lyrics == "" || p.Instrumental {
		t.Fatalf("%s", body)
	}
}

func TestAssistRequest_AnImageNeedsOnlyAnImagePrompt(t *testing.T) {
	s, _ := plannerServer(t, "KIND=image; LANG=en; TITLE=A cat; INSTRUMENTAL=no; SECONDS=0")
	_, p, body := planRequest(t, s, map[string]any{"text": "a photo of a cat"})
	if p.Kind != "image" || p.ImagePrompt == "" || p.MotionPrompt != "" {
		t.Fatalf("%s", body)
	}
}

func TestAssistRequest_TheCallerFixesTheKindAndTheModelIsNotAskedForIt(t *testing.T) {
	s, asked := plannerServer(t, "KIND=music")
	_, p, body := planRequest(t, s, map[string]any{"text": "un gato en una azotea", "kind": "image", "language": "es"})
	if p.Kind != "image" || p.KindSource != "user" || p.Language != "es" || p.Title == "" {
		t.Fatalf("%s", body)
	}
	for _, a := range *asked {
		if strings.HasPrefix(a, "kind|") {
			t.Errorf("the kind was fixed but the model was asked: %v", *asked)
		}
	}
	if p.Title != "un gato en una azotea" {
		t.Errorf("the title falls back on the first words: %q", p.Title)
	}
}

func TestAssistRequest_TheCallerCanFixWhetherASongIsInstrumental(t *testing.T) {
	s, _ := plannerServer(t, "KIND=music; INSTRUMENTAL=no")
	_, p, body := planRequest(t, s, map[string]any{"text": "una canción", "kind": "music", "instrumental": true})
	if p.Kind != "music" || !p.Instrumental {
		t.Fatalf("%s", body)
	}
	_, p, body = planRequest(t, s, map[string]any{"text": "una canción", "kind": "music"})
	if p.Instrumental {
		t.Fatalf("without the flag the song keeps a voice: %s", body)
	}
}

func TestAssistRequest_ARepliesWithoutAKindFallsBack(t *testing.T) {
	cases := []struct {
		name  string
		att   map[string]any
		kind  string
		count string
	}{
		{"audio", map[string]any{"audio": true}, "music", "attachment"},
		{"image", map[string]any{"image": true}, "video", "attachment"},
		{"video", map[string]any{"video": true}, "video", "attachment"},
		{"none", nil, "video", "default"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _ := plannerServer(t, "I am not sure what this is")
			body := map[string]any{"text": "algo relajante"}
			if c.att != nil {
				body["attachments"] = c.att
			}
			code, p, raw := planRequest(t, s, body)
			if code != http.StatusOK || p.Kind != c.kind || p.KindSource != c.count {
				t.Fatalf("%d %s", code, raw)
			}
		})
	}
}

func TestAssistRequest_APictureMeansNoImagePromptAndTheMovementIsAboutIt(t *testing.T) {
	s, asked := plannerServer(t, "KIND=video; LANG=es; TITLE=Pelo al viento")
	_, p, body := planRequest(t, s, map[string]any{"text": "haz que se mueva el pelo", "attachments": map[string]any{"image": true}})
	if p.ImagePrompt != "" || p.MotionPrompt == "" {
		t.Fatalf("%s", body)
	}
	for _, a := range *asked {
		if strings.HasPrefix(a, "image|") {
			t.Errorf("an image prompt was written for a picture the user supplied: %v", *asked)
		}
		if strings.HasPrefix(a, "motion|") && !strings.Contains(a, "picture the user supplied") {
			t.Errorf("the movement prompt does not know about the picture: %s", a)
		}
	}
}

func TestAssistRequest_ATextThatFailsIsAWarningNotTheEndOfTheRequest(t *testing.T) {
	s, _ := plannerServer(t, "KIND=music; LANG=es; TITLE=Despedida")
	s.assistant.AssistMusicRun = func(ctx context.Context, text string, o assist.MusicOptions, model string) (assist.MusicSuggestion, error) {
		return assist.MusicSuggestion{}, assist.ErrNoMemory
	}
	code, p, body := planRequest(t, s, map[string]any{"text": "una canción triste"})
	if code != http.StatusOK || p.Kind != "music" || p.Title != "Despedida" || p.Caption != "" || len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "song") {
		t.Fatalf("%d %s", code, body)
	}
}

func TestAssistRequest_Refusals(t *testing.T) {
	// The model refuses the first step: an error, there is no valid request.
	s, _ := plannerServer(t, "")
	s.assistant.AssistRun = func(ctx context.Context, task assist.Task, text, model string) (string, error) {
		return "", assist.ErrRefused
	}
	if code, _, body := planRequest(t, s, map[string]any{"text": "x"}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "assist_refused") {
		t.Errorf("refused: %d %s", code, body)
	}
	// It refuses every text of a request whose kind was fixed: nothing to show, so an error too.
	if code, _, body := planRequest(t, s, map[string]any{"text": "x", "kind": "image"}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "assist_refused") {
		t.Errorf("refused texts: %d %s", code, body)
	}
	// An unreachable model while deciding the kind is a 503.
	s.assistant.AssistRun = func(ctx context.Context, task assist.Task, text, model string) (string, error) {
		return "", assist.ErrUnreachable
	}
	if code, _, _ := planRequest(t, s, map[string]any{"text": "x"}); code != http.StatusServiceUnavailable {
		t.Errorf("unreachable: %d", code)
	}
}

func TestAssistRequest_InvalidInputAndARunInProgress(t *testing.T) {
	s, asked := plannerServer(t, "KIND=video")
	for name, body := range map[string]map[string]any{
		"empty":    {"text": "  "},
		"kind":     {"text": "x", "kind": "poem"},
		"language": {"text": "x", "language": "Spanish"},
		"long":     {"text": strings.Repeat("a", assist.MaxTextLen+1)},
	} {
		if code, _, b := planRequest(t, s, body); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, code, b)
		}
	}
	if len(*asked) != 0 {
		t.Errorf("the assistant ran for invalid input: %v", *asked)
	}
	s.workspace.SimulateRun("run-x", nil)
	if code, _, body := planRequest(t, s, map[string]any{"text": "x"}); code != http.StatusConflict || !strings.Contains(body, "run-x") {
		t.Errorf("a run in progress: %d %s", code, body)
	}
}

func TestAssistRequest_AVideoCarriesTheSpeechItNamesAndTheTasksDoNotSeeIt(t *testing.T) {
	s, asked := plannerServer(t, "KIND=video; LANG=es; TITLE=Mujer en la playa; INSTRUMENTAL=no; SECONDS=0")
	code, p, body := planRequest(t, s, map[string]any{"text": "una mujer en la playa que dice «hola, bienvenidos»"})
	if code != http.StatusOK || p.Speech != "hola, bienvenidos" {
		t.Fatalf("%d %s", code, body)
	}
	for _, a := range *asked {
		if (strings.HasPrefix(a, "image|") || strings.HasPrefix(a, "motion|")) && (strings.Contains(a, "bienvenidos") || strings.Contains(a, "«")) {
			t.Errorf("the speech reached a picture task: %q", a)
		}
	}
	_, silent, _ := planRequest(t, s, map[string]any{"text": "un gato en una azotea"})
	if silent.Speech != "" {
		t.Errorf("speech = %q, want none", silent.Speech)
	}
}

func TestAssistRequest_ASongHasNoSpeech(t *testing.T) {
	s, _ := plannerServer(t, "KIND=music; LANG=es; TITLE=Balada; INSTRUMENTAL=no; SECONDS=0")
	_, p, body := planRequest(t, s, map[string]any{"text": `una canción que dice "te quiero"`})
	if p.Kind != "music" || p.Speech != "" {
		t.Fatalf("%s", body)
	}
}

func TestAssistRequest_AnImageWithAPictureIsNotDescribedAsAScene(t *testing.T) {
	s, asked := plannerServer(t, "KIND=image; LANG=es; TITLE=Gorra roja; INSTRUMENTAL=no; SECONDS=0")
	_, p, body := planRequest(t, s, map[string]any{"text": "ponle una gorra roja", "attachments": map[string]bool{"image": true}})
	if p.Kind != "image" || !strings.HasSuffix(p.ImagePrompt, "Keep everything else exactly the same.") {
		t.Fatalf("a picture to edit gets an instruction that keeps the rest: %s", body)
	}
	for _, a := range *asked {
		if strings.HasPrefix(a, "image|") {
			t.Errorf("the image prompt was asked for: %q", a)
		}
	}
}

func TestAssistRequest_TheLengthOfASongIsValidatedAndSizesTheLyrics(t *testing.T) {
	s, _ := plannerServer(t, "KIND=music")
	if code, _, _ := planRequest(t, s, map[string]any{"text": "una canción", "kind": "music", "seconds": -5}); code != http.StatusBadRequest {
		t.Fatalf("a negative length is refused: %d", code)
	}
	if got := assistant.SongSeconds(0, 180); got != 180 {
		t.Errorf("the caller's length wins: %d", got)
	}
	if got := assistant.SongSeconds(90, 0); got != 90 {
		t.Errorf("the request's own length is the fallback: %d", got)
	}
}

func TestAssistRequest_APictureToChangeBecomesAnEnglishInstruction(t *testing.T) {
	s, asked := plannerServer(t, "KIND=image")
	_, p, body := planRequest(t, s, map[string]any{"text": "ponle el pelo rubio al chico de la camiseta roja", "kind": "image", "attachments": map[string]any{"image": true}})
	if p.Kind != "image" || p.ImagePrompt != "Make the hair of the man on the left blonde. Keep everything else exactly the same." {
		t.Fatalf("the instruction is in English and keeps the rest: %s", body)
	}
	var edits int
	for _, a := range *asked {
		if strings.HasPrefix(a, "edit|") {
			edits++
		}
		if strings.HasPrefix(a, "image|") {
			t.Errorf("a picture to change is not described as a scene: %v", *asked)
		}
	}
	if edits != 1 {
		t.Errorf("the instruction is asked once: %v", *asked)
	}
}

func TestAssistRequest_IfTheInstructionCannotBeWrittenTheWordsGoAsTheyAre(t *testing.T) {
	s, _ := plannerServer(t, "KIND=image")
	inner := s.assistant.AssistRun
	s.assistant.AssistRun = func(ctx context.Context, task assist.Task, text, model string) (string, error) {
		if task == assist.TaskEdit {
			return "", errors.New("memory is short")
		}
		return inner(ctx, task, text, model)
	}
	_, p, body := planRequest(t, s, map[string]any{"text": "ponle una gorra", "kind": "image", "attachments": map[string]any{"image": true}})
	if p.ImagePrompt != "" || len(p.Warnings) == 0 || !strings.Contains(p.Warnings[0], "picture instruction") {
		t.Fatalf("the plan carries the kind and a warning, and the page uses the user's words: %s", body)
	}
}

func TestAssistRequest_AModelPromptIsOneObjectAndTheCallerNamesTheKind(t *testing.T) {
	s, asked := plannerServer(t, "KIND=music")
	var told string
	prev := s.assistant.AssistRun
	s.assistant.AssistRun = func(ctx context.Context, task assist.Task, text, model string) (string, error) {
		if task == assist.TaskModel3D {
			told = text
			return "A glossy pink retro roller skate with white wheels, leather boot and chrome trucks.", nil
		}
		return prev(ctx, task, text, model)
	}
	code, p, body := planRequest(t, s, map[string]any{"text": "un patín de ruedas retro, rosa y brillante", "kind": "model3d", "language": "es"})
	if code != http.StatusOK || p.Kind != "model3d" || p.KindSource != "user" || !strings.Contains(p.ModelPrompt, "roller skate") || len(p.Warnings) != 0 {
		t.Fatalf("%d %s", code, body)
	}
	if told != "un patín de ruedas retro, rosa y brillante" || p.ImagePrompt != "" || p.MotionPrompt != "" || p.Caption != "" || p.Title == "" {
		t.Errorf("only the model prompt is written: told %q, %+v", told, p)
	}
	for _, a := range *asked {
		if strings.HasPrefix(a, "kind|") {
			t.Errorf("the caller named the kind and the model was asked for it: %v", *asked)
		}
	}
	// a picture attached does not change the kind the caller named
	if _, p, body := planRequest(t, s, map[string]any{"text": "este patín", "kind": "model3d", "attachments": map[string]bool{"image": true}}); p.Kind != "model3d" || p.ModelPrompt == "" {
		t.Errorf("with a picture: %s", body)
	}
}

func TestAssistRequest_TheModelNeverDecidesThatARequestIsA3DModel(t *testing.T) {
	s, _ := plannerServer(t, "KIND=model3d; LANG=es; TITLE=Un patín")
	_, p, body := planRequest(t, s, map[string]any{"text": "un patín de ruedas"})
	if p.Kind == "model3d" || p.KindSource == "model" && p.Kind == "model3d" {
		t.Fatalf("the kind of a 3D model is the caller's to name: %s", body)
	}
	if p.Kind != "video" || p.KindSource != "default" {
		t.Errorf("an answer the planner does not know falls back as before: %s", body)
	}
}

func TestAssistRequest_AModelPromptThatCannotBeWrittenIsAWarning(t *testing.T) {
	s, _ := plannerServer(t, "")
	s.assistant.AssistRun = func(ctx context.Context, task assist.Task, text, model string) (string, error) {
		return "", assist.ErrNoMemory
	}
	code, p, body := planRequest(t, s, map[string]any{"text": "un patín", "kind": "model3d"})
	if code != http.StatusOK || p.Kind != "model3d" || p.Title == "" || p.ModelPrompt != "" || len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "model prompt") {
		t.Fatalf("%d %s", code, body)
	}
	// the model's refusal, with nothing else written, is an error as for the other kinds
	s.assistant.AssistRun = func(ctx context.Context, task assist.Task, text, model string) (string, error) {
		return "", assist.ErrRefused
	}
	if code, _, body := planRequest(t, s, map[string]any{"text": "x", "kind": "model3d"}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "assist_refused") {
		t.Errorf("refused: %d %s", code, body)
	}
}

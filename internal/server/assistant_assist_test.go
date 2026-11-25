package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/server/api"
)

// assistServer is a server whose assistant is a counting fake.
func assistServer(t *testing.T) (*Server, *atomic.Int32, *atomic.Value) {
	t.Helper()
	s, _ := newProjectsServer(t)
	var calls atomic.Int32
	var last atomic.Value
	last.Store("")
	s.assistant.AssistRun = func(ctx context.Context, task assist.Task, text, model string) (string, error) {
		calls.Add(1)
		last.Store(string(task) + "|" + text)
		return "A woman reads in a sunlit cafe.", nil
	}
	return s, &calls, &last
}

func TestAssist_ReturnsTheGeneratedPrompt(t *testing.T) {
	s, calls, last := assistServer(t)
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist", map[string]string{"task": "image", "text": "Una mujer lee"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"text":"A woman reads in a sunlit cafe."`) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if calls.Load() != 1 || last.Load() != "image|Una mujer lee" {
		t.Errorf("calls=%d last=%v", calls.Load(), last.Load())
	}
}

func TestAssist_InvalidInputIs400AndNeverRuns(t *testing.T) {
	s, calls, _ := assistServer(t)
	for name, body := range map[string]any{
		"unknown task": map[string]string{"task": "foo", "text": "x"},
		"empty text":   map[string]string{"task": "image", "text": "  "},
		"missing task": map[string]string{"text": "x"},
		"long text":    map[string]string{"task": "image", "text": strings.Repeat("a", assist.MaxTextLen+1)},
	} {
		rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400: %s", name, rec.Code, rec.Body)
		}
	}
	// Not even JSON.
	rec := call(s.Handler(), "POST", "/api/assist", "not json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad JSON: status %d", rec.Code)
	}
	if calls.Load() != 0 {
		t.Errorf("the assistant ran %d times for invalid input", calls.Load())
	}
}

func TestAssist_RefusedWhileARunIsActive(t *testing.T) {
	s, fe := launchServer(t)
	var calls atomic.Int32
	s.assistant.AssistRun = func(ctx context.Context, task assist.Task, text, model string) (string, error) {
		calls.Add(1)
		return "x", nil
	}
	id, _ := launchRun(t, s, "demo", map[string]any{"gpu": "1"})
	waitStart(t, fe)

	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist", map[string]string{"task": "image", "text": "una mujer lee"})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), id) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if calls.Load() != 0 {
		t.Errorf("Ollama was contacted while a run was active")
	}

	// Once the run ends the assistant works again.
	fe.chanFor(id) <- true
	waitState(t, s, id, stateSucceeded)
	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/assist", map[string]string{"task": "image", "text": "una mujer lee"})
	if rec.Code != http.StatusOK || calls.Load() != 1 {
		t.Fatalf("after the run: status %d calls %d", rec.Code, calls.Load())
	}
}

func TestAssist_FailuresUseTheStandardErrorBody(t *testing.T) {
	s, _ := newProjectsServer(t)
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{fmt.Errorf("%w: cannot reach Ollama at http://127.0.0.1:11434", assist.ErrUnreachable), http.StatusServiceUnavailable, api.CodeUnavailable},
		{fmt.Errorf("%w: ollama pull qwen3:8b", assist.ErrNotInstalled), http.StatusServiceUnavailable, api.CodeUnavailable},
		{fmt.Errorf("%w: only 3 GiB", assist.ErrNoMemory), http.StatusServiceUnavailable, api.CodeUnavailable},
		{fmt.Errorf("%w: loaded on a GPU", assist.ErrOnGPU), http.StatusBadGateway, api.CodeInternal},
		{fmt.Errorf("%w: nothing", assist.ErrEmpty), http.StatusBadGateway, api.CodeInternal},
	} {
		err := tc.err
		s.assistant.AssistRun = func(context.Context, assist.Task, string, string) (string, error) { return "", err }
		rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist", map[string]string{"task": "image", "text": "texto"})
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), `"code":"`+tc.code+`"`) || !strings.Contains(rec.Body.String(), err.Error()) {
			t.Errorf("%v: status %d body %s", err, rec.Code, rec.Body)
		}
	}
}

func musicAssistServer(t *testing.T) (*Server, *atomic.Value) {
	t.Helper()
	s, _ := newProjectsServer(t)
	var last atomic.Value
	last.Store("")
	s.assistant.AssistMusicRun = func(ctx context.Context, text string, opts assist.MusicOptions, model string) (assist.MusicSuggestion, error) {
		last.Store(text + "|" + map[bool]string{true: "instrumental", false: "vocal"}[opts.Instrumental])
		out := assist.MusicSuggestion{Caption: "Slow pop ballad with piano", Lyrics: "[Verse]\nhola", Raw: "STYLE: ...\nLYRICS:\n..."}
		if opts.Instrumental {
			out.Lyrics = ""
		}
		return out, nil
	}
	return s, &last
}

func TestAssistMusic_ReturnsAStyleAndLyrics(t *testing.T) {
	s, last := musicAssistServer(t)
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist", map[string]any{"task": "music", "text": "Una balada"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["caption"] != "Slow pop ballad with piano" || got["lyrics"] != "[Verse]\nhola" || got["text"] == "" {
		t.Fatalf("body = %v", got)
	}
	if last.Load() != "Una balada|vocal" {
		t.Errorf("last = %v", last.Load())
	}
}

func TestAssistMusic_InstrumentalAsksForTheStyleOnly(t *testing.T) {
	s, last := musicAssistServer(t)
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist", map[string]any{"task": "music", "text": "lo-fi", "instrumental": true})
	var got map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != 200 || got["lyrics"] != "" || last.Load() != "lo-fi|instrumental" {
		t.Fatalf("%d %v %v", rec.Code, got, last.Load())
	}
}

func TestAssistMusic_SharesTheErrorMappingOfTheOtherTasks(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{assist.ErrNoMemory, http.StatusServiceUnavailable},
		{assist.ErrUnreachable, http.StatusServiceUnavailable},
		{assist.ErrInvalidInput, http.StatusBadRequest},
		{assist.ErrEmpty, http.StatusBadGateway},
	} {
		s, _ := newProjectsServer(t)
		err := tc.err
		s.assistant.AssistMusicRun = func(context.Context, string, assist.MusicOptions, string) (assist.MusicSuggestion, error) {
			return assist.MusicSuggestion{}, err
		}
		rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist", map[string]any{"task": "music", "text": "x"})
		if rec.Code != tc.want {
			t.Errorf("%v: status %d, want %d", tc.err, rec.Code, tc.want)
		}
	}
}

func TestAssistMusic_RefusedWhileARunIsActiveAndNeverRuns(t *testing.T) {
	s, fe := launchServer(t)
	var calls atomic.Int32
	s.assistant.AssistMusicRun = func(context.Context, string, assist.MusicOptions, string) (assist.MusicSuggestion, error) {
		calls.Add(1)
		return assist.MusicSuggestion{Caption: "x"}, nil
	}
	id, _ := launchRun(t, s, "demo", map[string]any{"gpu": "1"})
	waitStart(t, fe)
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist", map[string]any{"task": "music", "text": "x"})
	if rec.Code != http.StatusConflict || calls.Load() != 0 {
		t.Fatalf("status %d calls %d: %s", rec.Code, calls.Load(), rec.Body)
	}
	fe.chanFor(id) <- true
	waitState(t, s, id, stateSucceeded)
}

func TestAssist_AModelNamedInTheRequestReachesTheAssistant(t *testing.T) {
	s, _ := newProjectsServer(t)
	var got atomic.Value
	got.Store("unset")
	s.assistant.AssistRun = func(ctx context.Context, task assist.Task, text, model string) (string, error) {
		got.Store(model)
		return "ok", nil
	}
	s.assistant.AssistMusicRun = func(ctx context.Context, text string, opts assist.MusicOptions, model string) (assist.MusicSuggestion, error) {
		got.Store("music:" + model)
		return assist.MusicSuggestion{Caption: "pop", Lyrics: "la", Raw: "x"}, nil
	}
	doJSON(t, s.Handler(), http.MethodPost, "/api/assist", map[string]string{"task": "image", "text": "x", "model": "example/qwen3-tuned:8b"})
	if got.Load() != "example/qwen3-tuned:8b" {
		t.Errorf("model = %v", got.Load())
	}
	doJSON(t, s.Handler(), http.MethodPost, "/api/assist", map[string]string{"task": "image", "text": "x"})
	if got.Load() != "" {
		t.Errorf("no model named means the default (empty): %v", got.Load())
	}
	doJSON(t, s.Handler(), http.MethodPost, "/api/assist", map[string]string{"task": "music", "text": "x", "model": "m:1"})
	if got.Load() != "music:m:1" {
		t.Errorf("the music task takes the model too: %v", got.Load())
	}
}

func TestAssist_ARefusalIs422WithItsOwnCodeForEveryTask(t *testing.T) {
	s, _ := newProjectsServer(t)
	refusal := fmt.Errorf("%w: qwen3:8b refused to write this text. choose another installed model", assist.ErrRefused)
	s.assistant.AssistRun = func(context.Context, assist.Task, string, string) (string, error) { return "", refusal }
	s.assistant.AssistMusicRun = func(context.Context, string, assist.MusicOptions, string) (assist.MusicSuggestion, error) {
		return assist.MusicSuggestion{}, refusal
	}
	for _, task := range []string{"image", "motion", "music"} {
		rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist", map[string]string{"task": task, "text": "x"})
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"assist_refused"`) ||
			!strings.Contains(rec.Body.String(), "qwen3:8b refused") {
			t.Errorf("%s: %d %s", task, rec.Code, rec.Body)
		}
	}
}

func TestAssist_ANotInstalledModelIsTheUsualUnavailableError(t *testing.T) {
	s, _ := newProjectsServer(t)
	s.assistant.AssistRun = func(context.Context, assist.Task, string, string) (string, error) {
		return "", fmt.Errorf("%w: the model %q is not installed in Ollama; install it with: ollama pull %s", assist.ErrNotInstalled, "nope:1", "nope:1")
	}
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist", map[string]string{"task": "image", "text": "x", "model": "nope:1"})
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "ollama pull nope:1") {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}

func TestAssistModels_ListsTheInstalledOnesAndTheDefault(t *testing.T) {
	s, _ := newProjectsServer(t)
	s.assistant.AssistModels = func(context.Context) (string, []assist.ModelInfo, error) {
		return "qwen3:8b", []assist.ModelInfo{
			{Name: "example/qwen3-tuned:8b", SizeBytes: 5 << 30, Fits: true},
			{Name: "gemma4:31b", SizeBytes: 19 << 30, Fits: false},
		}, nil
	}
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/assist/models", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var out struct {
		Default string             `json:"default"`
		Models  []assist.ModelInfo `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Default != "qwen3:8b" || len(out.Models) != 2 || out.Models[1].Fits {
		t.Errorf("%+v", out)
	}
	s.assistant.AssistModels = func(context.Context) (string, []assist.ModelInfo, error) {
		return "qwen3:8b", nil, fmt.Errorf("%w: cannot reach http://127.0.0.1:11434", assist.ErrUnreachable)
	}
	rec = doJSON(t, s.Handler(), http.MethodGet, "/api/assist/models", nil)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "127.0.0.1:11434") {
		t.Errorf("Ollama down: %d %s", rec.Code, rec.Body)
	}
}

func TestAssistModels_NeedsTheTokenLikeEveryOtherRoute(t *testing.T) {
	s := newAuthServer(t)
	if rec := do(s.Handler(), http.MethodGet, "/api/assist/models", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("without a token: %d %s", rec.Code, rec.Body)
	}
	hdr := map[string]string{"Authorization": "Bearer " + testToken}
	s.assistant.AssistModels = func(context.Context) (string, []assist.ModelInfo, error) { return "qwen3:8b", nil, nil }
	if rec := do(s.Handler(), http.MethodGet, "/api/assist/models", hdr); rec.Code != http.StatusOK {
		t.Errorf("with the token: %d %s", rec.Code, rec.Body)
	}
}

// The tags task answers the tags in the form HeartMuLa takes, whatever the model wrote around them.
func TestAssist_TagsAreNormalised(t *testing.T) {
	s, _ := newProjectsServer(t)
	s.assistant.AssistRun = func(ctx context.Context, task assist.Task, text, model string) (string, error) {
		return " Romantic, Wedding , Piano, Soft Vocals.", nil
	}
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist", map[string]string{"task": "tags", "text": "boda romántica"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"text":"romantic,wedding,piano,soft vocals"`) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestAssist_TheMusicTaskPassesTheGeneratorAndAnswersTheTags(t *testing.T) {
	s, _ := newProjectsServer(t)
	var seen assist.MusicOptions
	s.assistant.AssistMusicRun = func(ctx context.Context, text string, opts assist.MusicOptions, model string) (assist.MusicSuggestion, error) {
		seen = opts
		return assist.MusicSuggestion{Caption: "Warm wedding song", Lyrics: "[verse]\nhola", Tags: "Piano, Acoustic, Wedding.", Raw: "x"}, nil
	}
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist", map[string]string{"task": "music", "text": "una boda", "generator": "heartmula"})
	if rec.Code != http.StatusOK || seen.Engine != "heartmula" {
		t.Fatalf("%d %s (engine %q)", rec.Code, rec.Body.String(), seen.Engine)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["tags"] != "piano,acoustic,wedding" {
		t.Errorf("tags must be normalised like the tags task: %v", got["tags"])
	}
	// ACE-Step answers no tags at all
	s.assistant.AssistMusicRun = func(context.Context, string, assist.MusicOptions, string) (assist.MusicSuggestion, error) {
		return assist.MusicSuggestion{Caption: "pop", Lyrics: "la", Raw: "x"}, nil
	}
	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/assist", map[string]string{"task": "music", "text": "x"})
	if strings.Contains(rec.Body.String(), "tags") {
		t.Errorf("no tags key expected: %s", rec.Body.String())
	}
}

func TestAssist_LyricsFromTranscript(t *testing.T) {
	s, _ := newProjectsServer(t)
	var seen assist.TranscriptInput
	s.assistant.AssistSongRun = func(ctx context.Context, in assist.TranscriptInput, model string) (assist.SongDraft, error) {
		seen = in
		return assist.SongDraft{MusicSuggestion: assist.MusicSuggestion{Caption: "Slow ballad", Lyrics: "[verse]\nhola", Tags: "ballad,slow", Raw: "x"}, UnchangedWords: true}, nil
	}
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist", map[string]any{
		"task": "lyrics-from-transcript", "text": "hola\nadios", "language": "es", "hint": "una balada", "bpm": 71.0,
		"keyscale": "F# major", "picture": "a rainy street", "generator": "diffrhythm2",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if seen.Text != "hola\nadios" || seen.Language != "es" || seen.BPM != 71 || seen.Key != "F# major" || seen.Picture != "a rainy street" || seen.Engine != "diffrhythm2" || seen.Hint != "una balada" {
		t.Fatalf("input = %+v", seen)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["unchanged_words"] != true || got["tags"] != "ballad,slow" || got["lyrics"] != "[verse]\nhola" {
		t.Fatalf("%v", got)
	}
}

func TestAssist_LyricsFromTranscriptValidation(t *testing.T) {
	s, _ := newProjectsServer(t)
	s.assistant.AssistSongRun = func(context.Context, assist.TranscriptInput, string) (assist.SongDraft, error) {
		return assist.SongDraft{MusicSuggestion: assist.MusicSuggestion{Caption: "x"}}, nil
	}
	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"nothing at all", map[string]any{"task": "lyrics-from-transcript"}, http.StatusBadRequest},
		{"only a hint (a song with no words)", map[string]any{"task": "lyrics-from-transcript", "hint": "ambient"}, http.StatusOK},
		{"a bad language", map[string]any{"task": "lyrics-from-transcript", "text": "hola", "language": "Spanish"}, http.StatusBadRequest},
		{"a long transcription is fine", map[string]any{"task": "lyrics-from-transcript", "text": strings.Repeat("la ", 2500)}, http.StatusOK},
		{"too long", map[string]any{"task": "lyrics-from-transcript", "text": strings.Repeat("la ", 5000)}, http.StatusBadRequest},
		{"the music task keeps its 2000 limit", map[string]any{"task": "music", "text": strings.Repeat("la ", 1000)}, http.StatusBadRequest},
	}
	for _, c := range cases {
		if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist", c.body); rec.Code != c.want {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body.String())
		}
	}
}

func TestAssistMusicTags_ListsTheVocabularyByCategory(t *testing.T) {
	s, _ := newProjectsServer(t)
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/assist/music-tags", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		Categories []struct {
			ID   string   `json:"id"`
			Tags []string `json:"tags"`
		} `json:"categories"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Categories) < 5 || out.Categories[0].ID != "genre" || len(out.Categories[0].Tags) == 0 {
		t.Fatalf("categories = %+v", out.Categories)
	}
}

func TestAssistMusicTags_AlsoListsTheTenCombinations(t *testing.T) {
	s, _ := newProjectsServer(t)
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/assist/music-tags", nil)
	var out struct {
		Combos []struct {
			ID   string   `json:"id"`
			Name string   `json:"name"`
			Tags []string `json:"tags"`
		} `json:"combos"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Combos) != 10 || out.Combos[0].Name != "Chill Lo-fi" || len(out.Combos[0].Tags) != 4 {
		t.Fatalf("combos = %+v", out.Combos)
	}
}

func TestAssist_ExamplesReachTheTaskAndTooManyAre400(t *testing.T) {
	s, calls, _ := assistServer(t)
	var got []assist.Example
	s.assistant.AssistRun = func(ctx context.Context, task assist.Task, text, model string) (string, error) {
		calls.Add(1)
		got = assist.ExamplesOf(ctx)
		return "ok", nil
	}
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist", map[string]any{"task": "image", "text": "x",
		"examples": []map[string]string{{"prompt": "neon", "negative": "blur"}, {"prompt": "second"}}})
	if rec.Code != http.StatusOK || len(got) != 2 || got[0].Prompt != "neon" || got[0].Negative != "blur" {
		t.Fatalf("%d %s %+v", rec.Code, rec.Body, got)
	}
	before := calls.Load()
	six := []map[string]string{{"prompt": "1"}, {"prompt": "2"}, {"prompt": "3"}, {"prompt": "4"}, {"prompt": "5"}, {"prompt": "6"}}
	for name, ex := range map[string]any{"six": six, "no prompt": []map[string]string{{"negative": "x"}}} {
		rec := doJSON(t, s.Handler(), http.MethodPost, "/api/assist", map[string]any{"task": "image", "text": "x", "examples": ex})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if calls.Load() != before {
		t.Error("the model was called for a bad request")
	}
}

func TestSettings_TheReviewOfMusicMotionDraftsIsASetting(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s, _ := newProjectsServer(t)
	read := func() bool {
		rec := doJSON(t, s.Handler(), http.MethodGet, "/api/settings", nil)
		var body struct {
			MusicMotion struct {
				ReviewDrafts bool `json:"review_drafts"`
			} `json:"music_motion"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil || !strings.Contains(rec.Body.String(), `"music_motion":{"review_drafts"`) {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		return body.MusicMotion.ReviewDrafts
	}
	if read() {
		t.Fatal("it is off by default")
	}
	if rec := doJSON(t, s.Handler(), http.MethodPut, "/api/settings", map[string]any{"music_motion": map[string]any{"review_drafts": true}}); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"review_drafts":true`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if !read() {
		t.Fatal("it is saved")
	}
	if rec := doJSON(t, s.Handler(), http.MethodPut, "/api/settings", map[string]any{"music_motion": map[string]any{}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("an empty music_motion changes nothing and is not a request: %d", rec.Code)
	}
	_ = doJSON(t, s.Handler(), http.MethodPut, "/api/settings", map[string]any{"music_motion": map[string]any{"review_drafts": false}})
	if read() {
		t.Fatal("it can be turned off")
	}
}

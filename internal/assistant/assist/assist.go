// Package assist turns a short scene description into a prompt for the image
// generator or for the animation, with a local Ollama model that always runs
// on the CPU so the GPUs stay free for rendering. It never downloads models,
// refuses to run when memory is short, verifies that the model did not land
// in VRAM and unloads it when the task ends.
package assist

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
	"veritmo/internal/platform/httpclient"

	"veritmo/internal/assistant/llm"
	"veritmo/internal/platform/edition"
	"veritmo/internal/platform/sched"
	"veritmo/internal/platform/settings"
)

// Task selects what to write.
type Task string

const (
	// TaskImage writes a photographic image prompt in English.
	TaskImage Task = "image"
	// TaskMotion writes a short English prompt for animating a still.
	TaskMotion Task = "motion"
	// TaskMusic writes the style and the lyrics of a song. Its answer has two
	// parts, so it goes through Music, not Run.
	TaskMusic Task = "music"
	// TaskTags writes the style tags of a song for the HeartMuLa generator (a few short words separated by commas).
	TaskTags Task = "tags"
	// TaskModel3D writes the prompt of one 3D model: an English description of a single object alone, from a request in any language. It is not offered
	// through ParseTask: the request planner (kind model3d) is its only caller.
	TaskModel3D Task = "model3d"
	// TaskLyrics puts the transcription of a song into sections and proposes its style and tags, without
	// inventing text. Its input has several parts, so it goes through LyricsFromTranscript, not Run.
	TaskLyrics Task = "lyrics-from-transcript"
)

// DefaultModel is used when neither the environment nor a flag names one.
const DefaultModel = "qwen3:8b"

// MaxTextLen is the longest input accepted, in characters.
const MaxTextLen = 2000

// Memory needed beyond the model's own size: MemFactor times the size plus
// MemMargin, in bytes available on the machine.
const (
	MemFactor = 1.3
	MemMargin = 4 * sched.GiB
)

// Error kinds, so callers (CLI, server) can answer each one properly.
var (
	ErrInvalidInput = errors.New("invalid input")
	ErrUnreachable  = llm.ErrUnreachable
	ErrNotInstalled = errors.New("model not installed")
	ErrNoMemory     = errors.New("not enough memory")
	ErrOnGPU        = errors.New("model loaded on a GPU")
	ErrEmpty        = errors.New("empty answer")
)

// Generator produces text from a system and a user message. truncated is true
// when generation stopped at the token limit. Another engine (for example the
// NPU) can be plugged in by implementing it.
type Generator interface {
	Generate(ctx context.Context, system, user string, maxTokens int) (text string, truncated bool, err error)
}

// taskSpec holds the prompts and the generation limit of a task.
type taskSpec struct {
	system    string
	user      func(text string) string
	maxTokens int
}

var tasks = map[Task]taskSpec{
	TaskImage: {
		system: "You write prompts for an image generator. Reply with ONE line in English, " +
			"70 to 100 words, describing a photorealistic photograph: subject, setting, light, " +
			"camera and lens, mood. No preamble, no quotes.",
		user:      func(text string) string { return text + "\n(Answer in English.)" },
		maxTokens: 160,
	},
	TaskTags: {
		system: "You write style tags for a music generator. Reply with ONE line in English: 4 to 8 short tags " +
			"(a word or two each) separated by commas, with no space after the commas, in lower case, covering " +
			"the genre, the main instruments, the mood, the tempo and the kind of voice. No preamble, no quotes, no full stops.",
		user:      func(text string) string { return "Song description: " + text + "\n(Answer in English.)" },
		maxTokens: 60,
	},
	TaskMotion: {
		system: "You write motion prompts to animate a still image. Reply with ONE line in English, " +
			"at most 25 words, describing only subtle movement of the scene and of the camera. " +
			"No preamble, no quotes.",
		user:      func(text string) string { return "Scene: " + text + "\n(Answer in English.)" },
		maxTokens: 80,
	},
	TaskListen:  listenTask(),
	TaskImprove: improveTask(),
	TaskKind:    kindTask(),
	TaskEdit:    editTask(),
	TaskModel3D: model3DTask(),
}

// ParseTask validates a task name.
func ParseTask(s string) (Task, error) {
	t := Task(s)
	if _, ok := tasks[t]; !ok && t != TaskMusic && t != TaskLyrics || t == TaskKind || t == TaskListen || t == TaskEdit || t == TaskModel3D {
		return "", fmt.Errorf("%w: unknown task %q (use %q, %q, %q, %q, %q or %q)", ErrInvalidInput, s, TaskImage, TaskMotion, TaskMusic, TaskTags, TaskLyrics, TaskImprove)
	}
	return t, nil
}

// ValidateText checks the input text.
func ValidateText(text string) error {
	switch {
	case strings.TrimSpace(text) == "":
		return fmt.Errorf("%w: the text is empty", ErrInvalidInput)
	case len([]rune(text)) > MaxTextLen:
		return fmt.Errorf("%w: the text is longer than %d characters", ErrInvalidInput, MaxTextLen)
	}
	return nil
}

// ModelFromEnv resolves the model: the flag value when given, else
// ORCHESTRATOR_ASSIST_MODEL, else DefaultModel.
func ModelFromEnv(flagValue string, getenv func(string) string) string {
	if flagValue != "" {
		return flagValue
	}
	if m := strings.TrimSpace(getenv("ORCHESTRATOR_ASSIST_MODEL")); m != "" {
		return m
	}
	return DefaultModel
}

// NormalizeHost turns an OLLAMA_HOST value (as Ollama itself reads it) into a
// base URL: scheme and port are optional and a wildcard address means
// loopback. Empty gives the default 127.0.0.1:11434.
func NormalizeHost(host string) string { return settings.Normalize(host) }

// Assistant runs tasks against one Ollama server and model.
type Assistant struct {
	Host  string // base URL, see NormalizeHost
	Model string
	// Generic, when set, is a provider that is not an Ollama (an OpenAI-compatible
	// one): the CPU-only request, the memory and installed-model checks, the VRAM
	// verification and the unload are Ollama's and are all skipped for it.
	Generic      llm.Provider
	HTTP         *http.Client
	MemAvailable func() (int64, error) // nil reads /proc/meminfo
	// Timeout bounds one whole task; zero means 3 minutes.
	Timeout time.Duration
}

// New returns an Assistant for the given OLLAMA_HOST value and model.
func New(ollamaHost, model string) *Assistant {
	return &Assistant{Host: NormalizeHost(ollamaHost), Model: model, HTTP: httpclient.Long}
}

// FromEnv builds an Assistant from the Ollama address in use (the saved
// setting, then OLLAMA_HOST, see internal/platform/settings) and the model resolution.
func FromEnv(modelFlag string) *Assistant {
	return New(settings.OllamaURL(), ModelFromEnv(modelFlag, os.Getenv))
}

// Run executes a one-line task (image or motion) and returns the generated
// prompt. The music task has two parts and goes through Music.
func (a *Assistant) Run(ctx context.Context, task Task, text string) (string, error) {
	spec, ok := tasks[task]
	if !ok {
		return "", fmt.Errorf("%w: unknown task %q", ErrInvalidInput, task)
	}
	if err := ValidateText(text); err != nil {
		return "", err
	}
	reply, truncated, err := a.generateOnCPU(ctx, spec.system, spec.user(strings.TrimSpace(text)), spec.maxTokens)
	if err != nil {
		return "", err
	}
	out := Clean(reply, truncated)
	if IsRefusal(out) {
		return "", refused(a.Model)
	}
	if out == "" {
		return "", fmt.Errorf("%w: the model returned nothing", ErrEmpty)
	}
	return out, nil
}

// generateOnCPU is the lifecycle every task shares: check the model and the
// memory, generate on the CPU, verify the model did not land in VRAM and
// unload it when done. truncated is true when generation stopped at the token
// limit.
func (a *Assistant) generateOnCPU(ctx context.Context, system, user string, maxTokens int) (text string, truncated bool, err error) {
	if err := edition.DenyModel(a.Model); err != nil {
		return "", false, err
	}
	user += examplesBlock(ctx)
	if a.Generic != nil {
		return a.generateGeneric(ctx, system, user, maxTokens)
	}
	timeout := a.Timeout
	if timeout == 0 {
		timeout = 3 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	size, err := a.modelSize(ctx)
	if err != nil {
		return "", false, err
	}
	if err := a.checkMemory(size); err != nil {
		return "", false, err
	}
	// From here on the model may be loaded: always unload it, even after an
	// error or a cancelled context.
	defer a.unload()

	text, truncated, err = a.Generate(ctx, system, user, maxTokens)
	if err != nil {
		return "", false, err
	}
	if err := a.verifyCPUOnly(ctx); err != nil {
		return "", false, err
	}
	return text, truncated, nil
}

// generateGeneric runs a task on a provider that is not an Ollama: one chat call,
// nothing of Ollama's lifecycle.
func (a *Assistant) generateGeneric(ctx context.Context, system, user string, maxTokens int) (string, bool, error) {
	timeout := a.Timeout
	if timeout == 0 {
		timeout = 3 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, err := a.Generic.Chat(ctx, llm.Request{Model: a.Model, System: system, User: user, MaxTokens: maxTokens, Temperature: 0})
	if err != nil {
		return "", false, err
	}
	return res.Text, res.Truncated, nil
}

// FromResolved builds an Assistant for the provider a task resolved to. An
// OpenAI-compatible provider gets the generic path with its declared text model; an
// Ollama (the built-in or an extra one) keeps the CPU-only lifecycle. A model named
// by the caller wins over the provider's declared one.
func FromResolved(res llm.Resolved, modelFlag string) *Assistant {
	model := modelFlag
	if model == "" {
		model = res.Model
	}
	if res.Type == "openai" {
		return &Assistant{Host: res.Host, Model: model, HTTP: httpclient.Long, Generic: res.Provider}
	}
	if model == "" {
		model = ModelFromEnv("", os.Getenv)
	}
	return New(res.Host, model)
}

// ForContext builds the Assistant for the provider the assistant task is routed to,
// or the one the context asks for. The built-in Ollama gives exactly FromEnv.
func ForContext(ctx context.Context, modelFlag string) (*Assistant, error) {
	res, err := llm.ForContext(ctx, settings.TaskAssistant)
	if err != nil {
		return nil, err
	}
	if res.Builtin {
		return FromEnv(modelFlag), nil
	}
	return FromResolved(res, modelFlag), nil
}

// Generate is the Ollama implementation of Generator: one chat request on the
// CPU (num_gpu 0), reasoning off, deterministic, kept loaded for a few seconds
// so the caller can verify where it landed. It goes through the Ollama provider
// of internal/assistant/llm.
func (a *Assistant) Generate(ctx context.Context, system, user string, maxTokens int) (string, bool, error) {
	res, err := a.provider().Chat(ctx, llm.Request{
		Model: a.Model, System: system, User: user, MaxTokens: maxTokens, Temperature: 0,
		Local: &llm.LocalOptions{CPUOnly: true, KeepAlive: "30s"},
	})
	if err != nil {
		return "", false, err
	}
	return res.Text, res.Truncated, nil
}

// provider is the Ollama provider this assistant talks to.
func (a *Assistant) provider() *llm.Ollama { return &llm.Ollama{Host: a.Host, HTTP: a.client()} }

// modelSize checks the model is installed and returns its size in bytes.
func (a *Assistant) modelSize(ctx context.Context) (int64, error) {
	var tags struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
			Size  int64  `json:"size"`
		} `json:"models"`
	}
	if err := a.get(ctx, "/api/tags", &tags); err != nil {
		return 0, err
	}
	want := a.Model
	if !strings.Contains(want, ":") {
		want += ":latest"
	}
	for _, m := range tags.Models {
		if m.Name == want || m.Model == want {
			return m.Size, nil
		}
	}
	return 0, fmt.Errorf("%w: the model %q is not installed in Ollama; install it with: ollama pull %s", ErrNotInstalled, a.Model, a.Model)
}

func (a *Assistant) checkMemory(modelBytes int64) error {
	avail := a.MemAvailable
	if avail == nil {
		avail = sched.MemAvailable
	}
	have, err := avail()
	if err != nil {
		return nil // cannot tell: do not block on an unreadable meminfo
	}
	need := int64(float64(modelBytes)*MemFactor) + MemMargin
	if have < need {
		return fmt.Errorf("%w: %s needs about %.1f GiB free (model %.1f GiB x %.1f + %d GiB margin) and only %.1f GiB are available; free memory (for example stop a running render) and try again",
			ErrNoMemory, a.Model, float64(need)/float64(sched.GiB), float64(modelBytes)/float64(sched.GiB), MemFactor, MemMargin/sched.GiB, float64(have)/float64(sched.GiB))
	}
	return nil
}

// verifyCPUOnly asks Ollama which models are loaded and fails (after
// unloading) when this one occupies any VRAM.
func (a *Assistant) verifyCPUOnly(ctx context.Context) error {
	var ps struct {
		Models []struct {
			Name     string `json:"name"`
			Model    string `json:"model"`
			SizeVRAM int64  `json:"size_vram"`
		} `json:"models"`
	}
	if err := a.get(ctx, "/api/ps", &ps); err != nil {
		return err
	}
	want := a.Model
	if !strings.Contains(want, ":") {
		want += ":latest"
	}
	for _, m := range ps.Models {
		if (m.Name == want || m.Model == want) && m.SizeVRAM != 0 {
			a.unload()
			return fmt.Errorf("%w: Ollama loaded %s using %d MB of VRAM although the CPU was requested; it was unloaded", ErrOnGPU, a.Model, m.SizeVRAM>>20)
		}
	}
	return nil
}

// unload asks Ollama to drop the model now. It uses its own short context so
// it still runs when the task's context was cancelled.
func (a *Assistant) unload() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = a.post(ctx, "/api/generate", map[string]any{"model": a.Model, "keep_alive": 0}, nil)
}

// maxAnswerBytes is the most of an Ollama answer that is read: a model never writes that much.
const maxAnswerBytes = 32 << 20

func (a *Assistant) client() *http.Client {
	if a.HTTP != nil {
		return a.HTTP
	}
	return httpclient.Long
}

func (a *Assistant) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.Host+path, nil)
	if err != nil {
		return err
	}
	return a.do(req, out)
}

func (a *Assistant) post(ctx context.Context, path string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.Host+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return a.do(req, out)
}

func (a *Assistant) do(req *http.Request, out any) error {
	resp, err := a.client().Do(req)
	if err != nil {
		if ctxErr := req.Context().Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("%w: cannot reach Ollama at %s: %w", ErrUnreachable, a.Host, redactNet(err))
	}
	defer resp.Body.Close()
	data, _ := httpclient.ReadAllLimited(resp.Body, maxAnswerBytes)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("ollama answered %s for %s: %s", resp.Status, req.URL.Path, strings.TrimSpace(string(data)))
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

func redactNet(err error) error {
	var ne *net.OpError
	if errors.As(err, &ne) && ne.Err != nil {
		return ne.Err
	}
	return err
}

var thinkRe = regexp.MustCompile(`(?s)<think>.*?</think>`)

// Clean reduces a model answer to one line: reasoning blocks, line breaks,
// wrapping quotes and extra spaces are removed, and a generation cut at the
// token limit ends at its last complete sentence.
func Clean(s string, truncated bool) string {
	s = thinkRe.ReplaceAllString(s, " ")
	if i := strings.Index(s, "<think>"); i >= 0 { // reasoning that never closed
		s = s[:i]
	}
	s = strings.Join(strings.Fields(s), " ")
	s = trimWrappingQuotes(s)
	if truncated {
		if i := strings.LastIndexAny(s, ".!?"); i >= 0 {
			s = s[:i+1]
		}
	}
	return strings.TrimSpace(s)
}

func trimWrappingQuotes(s string) string {
	pairs := [][2]string{{`"`, `"`}, {"“", "”"}, {"'", "'"}, {"«", "»"}}
	for _, p := range pairs {
		if len(s) >= len(p[0])+len(p[1]) && strings.HasPrefix(s, p[0]) && strings.HasSuffix(s, p[1]) {
			return strings.TrimSpace(s[len(p[0]) : len(s)-len(p[1])])
		}
	}
	return s
}

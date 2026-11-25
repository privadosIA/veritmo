// Package describe reads an uploaded picture or clip with a local vision model
// served by Ollama and writes the prompt that would draw it. The file is only
// read: nothing here hands it to a generator. Before any description is
// returned every person in the picture (every extracted frame, for a clip)
// goes through an age check, and a picture of a minor, or of someone whose age
// cannot be told, is refused. The model is unloaded when the work ends.
package describe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"veritmo/internal/platform/httpclient"
	"veritmo/internal/platform/legacyname"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/assistant/llm"
	"veritmo/internal/platform/edition"
	"veritmo/internal/platform/probe"
	"veritmo/internal/platform/runner"
	"veritmo/internal/platform/sched"
	"veritmo/internal/platform/settings"
)

// DefaultModel is the descriptor used when none is asked for (about 12 s per
// picture on a 12 GB card). The "more detail" descriptor exists only in the
// private edition: see edition.DetailModel.
const DefaultModel = "gemma4:26b"

// Limits on what is accepted.
const (
	MaxImageBytes   = 20 << 20
	MaxVideoBytes   = 500 << 20
	MaxVideoSeconds = 300
	minFrames       = 4
	maxFrames       = 8
	imageSide       = 1280 // longest side sent to the model, in pixels
)

// Errors. ErrRefused and ErrMinor carry the two answers the caller must tell
// apart; the rest are the assistant's, so servers and the CLI can map both
// the same way.
var (
	ErrInvalidInput = assist.ErrInvalidInput
	ErrUnreachable  = assist.ErrUnreachable
	ErrNotInstalled = assist.ErrNotInstalled
	ErrNoMemory     = assist.ErrNoMemory
	ErrEmpty        = assist.ErrEmpty
	// ErrRefused marks a model that declined to describe, or answered with
	// something that is not a description.
	ErrRefused = errors.New("the model refused to describe the file")
	// ErrMinor marks a file in which a person is, or looks, under 18, or whose
	// age could not be confirmed. No description is returned.
	ErrMinor = errors.New("a person in the file is, or looks, under 18")
)

// Alternative is something the user can retry with after a refusal: a model at a
// provider. Retrying is a new call the user asks for; nothing retries by itself.
type Alternative struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
	Model    string `json:"model"`
}

// RefusedError is ErrRefused with the model that refused and what the user can
// retry with: the other vision models of the same provider and the other
// providers that have a vision model.
type RefusedError struct {
	Provider     string
	ProviderName string
	Model        string
	// Alternatives are the other vision models of the provider that refused (names only).
	Alternatives []string
	// Choices are the alternatives to retry with, each naming its provider: the other
	// models of the same provider first, then the other providers.
	Choices []Alternative
}

func (e *RefusedError) Error() string {
	msg := fmt.Sprintf("%v: %s", ErrRefused, e.Model)
	if len(e.Alternatives) > 0 {
		msg += "; try another vision model: " + strings.Join(e.Alternatives, ", ")
	}
	var others []string
	for _, c := range e.Choices {
		if c.Provider != e.Provider {
			others = append(others, fmt.Sprintf("%s (%s)", c.Name, c.Model))
		}
	}
	if len(others) > 0 {
		msg += "; or another provider: " + strings.Join(others, ", ")
	}
	return msg
}

func (e *RefusedError) Is(target error) bool { return target == ErrRefused }

// Kind is what an uploaded file is.
type Kind string

// The kinds of file that can be described: a picture or a video.
const (
	KindImage Kind = "image"
	KindVideo Kind = "video"
)

var (
	imageExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".webp": true}
	videoExts = map[string]bool{".mp4": true, ".mov": true, ".webm": true, ".mkv": true, ".avi": true}
)

// KindOf says whether the file name is a picture or a clip.
func KindOf(path string) (Kind, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch {
	case imageExts[ext]:
		return KindImage, nil
	case videoExts[ext]:
		return KindVideo, nil
	}
	return "", fmt.Errorf("%w: %q is not a picture (png, jpg, jpeg, webp) or a clip (mp4, mov, webm, mkv, avi)", ErrInvalidInput, filepath.Base(path))
}

// Options choose the descriptor. An empty Model means the default; Detail
// asks for the more detailed one when Model is empty.
type Options struct {
	Model  string
	Detail bool
	// Split describes a clip as one scene per shot (Result.Shots) instead of
	// one merged prompt. A picture ignores it.
	Split bool
}

// Result is what describe returns. Motion is set for a clip only.
type Result struct {
	Prompt string `json:"prompt"`
	Motion string `json:"motion,omitempty"`
	Model  string `json:"model"`
	Kind   Kind   `json:"kind"`
	Frames int    `json:"frames"`
	// Provider is the id of the provider that described it.
	Provider string `json:"provider"`
	// Shots is set only when a clip was split: one entry per shot, in order.
	// Prompt then holds the first shot's.
	Shots []Shot `json:"shots,omitempty"`
}

// Model is a model installed in Ollama that can read pictures.
type Model struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	Fits      bool   `json:"fits"`
}

// Describer talks to one Ollama server.
type Describer struct {
	Host         string // base URL, see assist.NormalizeHost
	HTTP         *http.Client
	Runner       runner.CommandRunner  // ffmpeg and ffprobe; nil runs them for real
	MemAvailable func() (int64, error) // nil reads /proc/meminfo
	// CPU keeps the model off the GPUs (num_gpu 0). Slow for a 26 B model.
	CPU bool
	// Timeout bounds the whole work; zero means 10 minutes.
	Timeout time.Duration
	// Generic, when set, is a provider that is not an Ollama (an OpenAI-compatible
	// one): the installed-model list, the memory check and the unload are Ollama's
	// and are skipped, and the model is the one declared in ModelName.
	Generic llm.Provider
	// ModelName is the vision model the provider declares for describing; empty for an
	// Ollama without one (the default or the first installed vision model is used).
	ModelName string
	// ProviderID and ProviderName say which provider this is, for the answer and the
	// alternatives; empty means the built-in Ollama.
	ProviderID, ProviderName string
	// MaxCalls, when above zero, is the most calls one job may make: a job that plans
	// more is refused before any call. Set for remote providers.
	MaxCalls int
	// Others lists the other providers that have a vision model, to offer after a refusal.
	Others func(ctx context.Context) []Alternative
	// Frames is a seam for tests: it returns the PNGs to describe. Nil
	// extracts them with ffmpeg.
	Frames func(ctx context.Context, path string, kind Kind) ([][]byte, error)
	// Shots is a seam for tests: it returns the shots of a clip and the PNG
	// of the frame in the middle of each. Nil finds the cuts with ffmpeg.
	Shots func(ctx context.Context, path string) ([]ShotRange, [][]byte, error)
}

// FromEnv builds a Describer from the Ollama address in use (see
// internal/platform/settings); VERITMO_DESCRIBE_CPU=1
// keeps the model on the CPU.
func FromEnv() *Describer {
	return &Describer{
		Host: settings.OllamaURL(),
		HTTP: httpclient.Long,
		CPU:  legacyname.Getenv("DESCRIBE_CPU") == "1",
	}
}

// describeSystem and mergeSystem carry the literal-description sentence only in
// the private edition (see internal/platform/edition).
var describeSystem = "You write prompts for an image generator from a reference picture. " +
	edition.DescribeLiteral() +
	"Reply with ONE paragraph in English, 70 to 120 words, covering every person (state an adult age range, " +
	"18 or older, then body, hair, and clothing or its absence), pose, expression, setting, light, camera framing " +
	"and photographic or artistic style. No preamble, no quotes, no lists."

const ageSystem = "You check a picture before it is described. Look at every person or character in it, " +
	"whether it is a photograph, a drawing, anime or a render. " +
	`Answer only JSON: {"people": number, "minor": true or false, "unclear": true or false, "medium": "photo|illustration|anime|render|other"}. ` +
	`"minor" is true if any person or character is, or looks, under 18 (a child, a teenager, a school-age or youthful face or body). ` +
	`"unclear" is true if the age of any person cannot be judged. With no people, people is 0 and both are false.`

var mergeSystem = "You merge the descriptions of key frames of ONE video into a single image prompt for the " +
	"scene (70 to 120 words, same people and setting, adult ages only" + edition.MergeLiteral() + ") and " +
	"a motion prompt of at most 25 words about the subtle movement of the people and the camera. " +
	`Answer only JSON: {"prompt": "...", "motion": "..."}`

// ListModels returns the vision models installed in Ollama, smallest first,
// the one a request without a model would use and the one for more detail
// (empty when it is not installed).
func (d *Describer) ListModels(ctx context.Context) (def, detail string, models []Model, err error) {
	models, err = d.visionModels(ctx)
	if err != nil {
		return "", "", nil, err
	}
	def = pickDefault(models)
	for _, m := range models {
		if dm := edition.DetailModel(); dm != "" && m.Name == dm {
			detail = m.Name
		}
	}
	return def, detail, models, nil
}

func pickDefault(models []Model) string {
	for _, m := range models {
		if m.Name == DefaultModel {
			return m.Name
		}
	}
	if len(models) > 0 {
		return models[0].Name
	}
	return ""
}

// Describe reads the file at path and returns its prompt. See the package
// comment for the order of the checks.
func (d *Describer) Describe(ctx context.Context, path string, opts Options) (Result, error) {
	kind, err := KindOf(path)
	if err != nil {
		return Result{}, err
	}
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return Result{}, fmt.Errorf("%w: %q is not a readable file", ErrInvalidInput, filepath.Base(path))
	}
	if limit := int64(MaxImageBytes); kind == KindVideo {
		limit = MaxVideoBytes
		if fi.Size() > limit {
			return Result{}, fmt.Errorf("%w: the clip is larger than %d MB", ErrInvalidInput, limit>>20)
		}
	} else if fi.Size() > limit {
		return Result{}, fmt.Errorf("%w: the picture is larger than %d MB", ErrInvalidInput, limit>>20)
	}

	timeout := d.Timeout
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	model, models, err := d.pickModel(ctx, opts)
	if err != nil {
		return Result{}, err
	}

	if kind == KindVideo && opts.Split {
		return d.describeSplit(ctx, path, model, models)
	}

	frames, err := d.frames(ctx, path, kind)
	if err != nil {
		return Result{}, err
	}
	if len(frames) == 0 {
		return Result{}, fmt.Errorf("%w: no picture could be read from the file", ErrInvalidInput)
	}

	// One picture is two calls (age check and description); a clip as one scene is two per
	// frame plus the merge. A remote provider is refused, before any call, a job over its cap.
	planned := 2 * len(frames)
	if kind == KindVideo {
		planned++
	}
	if err := d.checkCalls(planned); err != nil {
		return Result{}, err
	}

	// The model may be loaded from here on: always unload it.
	defer d.unload(model)

	for i, f := range frames {
		if err := d.checkAdults(ctx, model, f, models, i+1, len(frames)); err != nil {
			return Result{}, err
		}
	}
	var texts []string
	for _, f := range frames {
		out, err := d.chat(ctx, model, describeSystem, "Describe this picture as a prompt.", f, false, 400)
		if err != nil {
			return Result{}, err
		}
		text := assist.Clean(out, false)
		if isNotADescription(text) {
			return Result{}, d.refusal(ctx, model, models)
		}
		texts = append(texts, text)
	}
	res := Result{Prompt: texts[0], Model: model, Provider: d.providerID(), Kind: kind, Frames: len(frames)}
	if kind == KindVideo {
		res.Prompt, res.Motion = d.merge(ctx, model, texts)
	}
	return res, nil
}

// pickModel chooses the vision model a call uses (the one asked for, the one the provider declares, or the default
// installed one) and checks that it fits in memory. models is the installed list of an Ollama (nil for another provider).
func (d *Describer) pickModel(ctx context.Context, opts Options) (string, []Model, error) {
	var models []Model
	var model string
	if d.Generic != nil {
		// Not an Ollama: the model is the one the provider declares (or the caller names).
		if model = opts.Model; model == "" {
			model = d.ModelName
		}
		if model == "" {
			return "", nil, fmt.Errorf("%w: the provider %q has no vision model: set one in Settings", llm.ErrNoModel, d.providerName())
		}
		if err := edition.DenyModel(model); err != nil {
			return "", nil, err
		}
	} else {
		var err error
		if models, err = d.visionModels(ctx); err != nil {
			return "", nil, err
		}
		if opts.Model == "" && !opts.Detail && d.ModelName != "" {
			opts.Model = d.ModelName
		}
		var size int64
		if model, size, err = choose(models, opts); err != nil {
			return "", nil, err
		}
		if err := d.checkMemory(model, size); err != nil {
			return "", nil, err
		}
	}
	return model, models, nil
}

// merge turns the per-frame descriptions of a clip into one prompt and one
// motion prompt. When the model does not answer the JSON, the first frame's
// own description stands in for the prompt and there is no motion prompt: a
// real description, never an invented one.
func (d *Describer) merge(ctx context.Context, model string, texts []string) (prompt, motion string) {
	var b strings.Builder
	for i, t := range texts {
		fmt.Fprintf(&b, "Frame %d of %d: %s\n", i+1, len(texts), t)
	}
	out, err := d.chat(ctx, model, mergeSystem, b.String(), nil, true, 500)
	if err == nil {
		var m struct {
			Prompt string `json:"prompt"`
			Motion string `json:"motion"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(out)), &m) == nil && len(strings.Fields(m.Prompt)) >= 20 {
			return assist.Clean(m.Prompt, false), assist.Clean(m.Motion, false)
		}
	}
	return texts[0], ""
}

// checkAdults asks the age question for one picture. A refusal to answer is a
// refusal; an answer that is not the JSON, or that says minor or unclear, is
// ErrMinor: when the age cannot be confirmed nothing is described.
func (d *Describer) checkAdults(ctx context.Context, model string, img []byte, models []Model, n, total int) error {
	out, err := d.chat(ctx, model, ageSystem, "Check this picture.", img, true, 120)
	if err != nil {
		return err
	}
	var a struct {
		People  int  `json:"people"`
		Minor   bool `json:"minor"`
		Unclear bool `json:"unclear"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &a); err != nil {
		if assist.IsRefusal(assist.Clean(out, false)) {
			return d.refusal(ctx, model, models)
		}
		return fmt.Errorf("%w: the age check could not be read (%s), so nobody was described", ErrMinor, where(n, total))
	}
	if a.Minor {
		return fmt.Errorf("%w (%s)", ErrMinor, where(n, total))
	}
	if a.Unclear {
		return fmt.Errorf("%w: the age of someone could not be confirmed (%s)", ErrMinor, where(n, total))
	}
	return nil
}

func where(n, total int) string {
	if total == 1 {
		return "the picture"
	}
	return fmt.Sprintf("frame %d of %d", n, total)
}

// isNotADescription reports a refusal or an answer too short to be a prompt.
func isNotADescription(text string) bool {
	return assist.IsRefusal(text) || len(strings.Fields(text)) < 15
}

// refusal builds the error for a model that declined, with what the user can retry
// with: the other vision models of the same provider, then the other providers.
func (d *Describer) refusal(ctx context.Context, model string, models []Model) error {
	e := &RefusedError{Provider: d.providerID(), ProviderName: d.providerName(), Model: model}
	if !edition.RefusalAdvice() {
		return e // the public edition only says which model declined
	}
	for _, m := range models {
		if m.Name != model {
			e.Alternatives = append(e.Alternatives, m.Name)
			e.Choices = append(e.Choices, Alternative{Provider: e.Provider, Name: e.ProviderName, Model: m.Name})
		}
	}
	if d.Others != nil {
		e.Choices = append(e.Choices, d.Others(ctx)...)
	}
	return e
}

func (d *Describer) providerID() string {
	if d.ProviderID == "" {
		return settings.BuiltinID
	}
	return d.ProviderID
}

func (d *Describer) providerName() string {
	if d.ProviderName == "" {
		return "Ollama"
	}
	return d.ProviderName
}

// TooManyCallsError is a job that would make more calls to a remote provider than its cap.
type TooManyCallsError struct {
	Planned, Cap int
	Provider     string
}

func (e *TooManyCallsError) Error() string {
	return fmt.Sprintf("this job would make %d calls to %s and the cap for remote providers is %d: describe it as one scene, use a shorter clip, or raise the cap in Settings", e.Planned, e.Provider, e.Cap)
}

// ErrTooManyCalls is what a TooManyCallsError matches with errors.Is.
var ErrTooManyCalls = errors.New("too many calls for a remote provider")

func (e *TooManyCallsError) Is(target error) bool { return target == ErrTooManyCalls }

// checkCalls refuses a job that plans more calls than MaxCalls (when it is set).
func (d *Describer) checkCalls(planned int) error {
	if d.MaxCalls > 0 && planned > d.MaxCalls {
		return &TooManyCallsError{Planned: planned, Cap: d.MaxCalls, Provider: d.providerName()}
	}
	return nil
}

// choose picks the model: the one named, else the detailed one on request,
// else the default (or the first vision model installed).
func choose(models []Model, opts Options) (string, int64, error) {
	want := opts.Model
	switch {
	case want == "" && opts.Detail && edition.DetailModel() != "":
		want = edition.DetailModel()
	case want == "":
		want = pickDefault(models)
	}
	if want != "" {
		if err := edition.DenyModel(want); err != nil {
			return "", 0, err
		}
	}
	if want == "" {
		return "", 0, fmt.Errorf("%w: no vision model is installed in Ollama; install one with: ollama pull %s", ErrNotInstalled, DefaultModel)
	}
	for _, m := range models {
		if m.Name == want || (!strings.Contains(want, ":") && m.Name == want+":latest") {
			return m.Name, m.SizeBytes, nil
		}
	}
	return "", 0, fmt.Errorf("%w: %q is not installed in Ollama (or cannot read pictures); install it with: ollama pull %s", ErrNotInstalled, want, want)
}

// memNeeded is the free memory a model needs. On the CPU the whole model lives
// in RAM (size x 1.3 plus a margin, as for the text assistant); on a GPU the
// weights go to VRAM and only the margin must be free.
func (d *Describer) memNeeded(size int64) int64 {
	if d.CPU {
		return int64(float64(size)*assist.MemFactor) + assist.MemMargin
	}
	return assist.MemMargin
}

func (d *Describer) checkMemory(model string, size int64) error {
	avail := d.MemAvailable
	if avail == nil {
		avail = sched.MemAvailable
	}
	have, err := avail()
	if err != nil {
		return nil // cannot tell: do not block on an unreadable meminfo
	}
	need := d.memNeeded(size)
	if have < need {
		return fmt.Errorf("%w: %s needs about %.1f GiB free and only %.1f GiB are available; free memory (for example stop a running render) and try again",
			ErrNoMemory, model, float64(need)/float64(sched.GiB), float64(have)/float64(sched.GiB))
	}
	return nil
}

// --- Ollama ---------------------------------------------------------------

func (d *Describer) client() *http.Client {
	if d.HTTP != nil {
		return d.HTTP
	}
	return httpclient.Long
}

// visionModels lists the installed models that can read pictures, smallest first.
func (d *Describer) visionModels(ctx context.Context) ([]Model, error) {
	all, err := (&llm.Ollama{Host: d.Host, HTTP: d.client()}).Models(ctx)
	if err != nil {
		return nil, err
	}
	have, memErr := d.mem()
	var out []Model
	for _, m := range all {
		if m.Vision == nil || !*m.Vision {
			continue
		}
		fits := memErr != nil || have >= d.memNeeded(m.SizeBytes)
		out = append(out, Model{Name: m.Name, SizeBytes: m.SizeBytes, Fits: fits})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SizeBytes != out[j].SizeBytes {
			return out[i].SizeBytes < out[j].SizeBytes
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (d *Describer) mem() (int64, error) {
	if d.MemAvailable != nil {
		return d.MemAvailable()
	}
	return sched.MemAvailable()
}

// chat sends one request, with a picture when img is not nil, and returns the
// model's text. Reasoning is off and the answer deterministic enough to
// repeat; asJSON asks Ollama to force JSON. It goes through the Ollama provider
// of internal/assistant/llm.
func (d *Describer) chat(ctx context.Context, model, system, user string, img []byte, asJSON bool, maxTokens int) (string, error) {
	req := llm.Request{
		Model: model, System: system, User: user, JSON: asJSON, MaxTokens: maxTokens, Temperature: 0.2,
		Local: &llm.LocalOptions{CPUOnly: d.CPU, KeepAlive: "5m"},
	}
	if img != nil {
		req.Images = [][]byte{img}
	}
	res, err := d.provider().Chat(ctx, req)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(res.Text) == "" {
		return "", fmt.Errorf("%w: the model returned nothing", ErrEmpty)
	}
	return res.Text, nil
}

// provider is the provider this describer talks to.
func (d *Describer) provider() llm.Provider {
	if d.Generic != nil {
		return d.Generic
	}
	return &llm.Ollama{Host: d.Host, HTTP: d.client()}
}

// unload drops the model from memory now. It has its own context so it still
// runs after a cancelled task.
func (d *Describer) unload(model string) {
	if d.Generic != nil {
		return // nothing of Ollama's applies to another provider
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	(&llm.Ollama{Host: d.Host, HTTP: d.client()}).Unload(ctx, model)
}

// --- frames ---------------------------------------------------------------

func (d *Describer) frames(ctx context.Context, path string, kind Kind) ([][]byte, error) {
	if d.Frames != nil {
		return d.Frames(ctx, path, kind)
	}
	r := d.Runner
	if r == nil {
		r = runner.NewExecRunner()
	}
	return extractFrames(ctx, r, path, kind)
}

// frameCount is how many frames a clip of this length gets: about one every
// four seconds, between 4 and 8.
func frameCount(seconds float64) int {
	n := int(seconds / 4)
	if n < minFrames {
		return minFrames
	}
	if n > maxFrames {
		return maxFrames
	}
	return n
}

// extractFrames reads one picture (a scaled PNG) or the key frames of a clip
// with ffmpeg. The frames live in a temp dir that is removed before return.
func extractFrames(ctx context.Context, r runner.CommandRunner, path string, kind Kind) ([][]byte, error) {
	dir, err := os.MkdirTemp("", "describe-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	grab := func(seek float64, name string) ([]byte, error) {
		return grabFrame(ctx, r, path, seek, filepath.Join(dir, name))
	}

	if kind == KindImage {
		b, err := grab(-1, "frame.png")
		if err != nil {
			return nil, err
		}
		return [][]byte{b}, nil
	}
	dur, err := probe.Duration(ctx, r, path)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot read the length of %q: %w", ErrInvalidInput, filepath.Base(path), err)
	}
	secs := dur.Seconds()
	if secs > MaxVideoSeconds {
		return nil, fmt.Errorf("%w: the clip lasts %d s and the limit is %d s", ErrInvalidInput, int(secs), MaxVideoSeconds)
	}
	n := frameCount(secs)
	var frames [][]byte
	for i := 0; i < n; i++ {
		b, err := grab(secs*(float64(i)+0.5)/float64(n), fmt.Sprintf("frame-%02d.png", i))
		if err != nil {
			return nil, err
		}
		frames = append(frames, b)
	}
	return frames, nil
}

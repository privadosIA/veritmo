// Package music invokes the acestep.cpp programs (bin/ace-lm and
// bin/ace-synth, built by scripts/build-acestep.sh) to turn a track into a
// song. Two phases, each its own short-lived process so the models leave the
// GPU when it ends: compose (ace-lm: caption, lyrics and metadata to an
// enriched request with final lyrics and audio codes) and synth (ace-synth:
// DiT + VAE to stereo 48 kHz audio).
//
// The engine names its outputs after the input file and writes them next to
// it (req.json -> req0.json -> req0.mp3), so each variant gets a scratch
// directory with a fixed input name and the result is then moved to its
// cache path.
package music

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"veritmo/internal/platform/probe"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
)

// Engine programs installed in bin/.
const (
	BinaryLM    = "ace-lm"
	BinarySynth = "ace-synth"
)

// Flags centralizes every engine CLI flag in one place so they are easy to
// correct later.
var Flags = struct {
	Models   string
	Request  string
	Adapters string
	SrcAudio string
	RefAudio string
}{
	Models:   "--models",
	Request:  "--request",
	Adapters: "--adapters",
	SrcAudio: "--src-audio",
	RefAudio: "--ref-audio",
}

// Names of the files inside a variant's scratch directory. The engine
// appends the batch index (0) to the input's stem for its outputs.
const (
	inputName      = "req.json"
	composedName   = "req0.json"
	synthStem      = "req0"
	instrumentalTx = "[Instrumental]"
)

// Engine output formats. The engine rejects a plain "wav".
const (
	engineMP3 = "mp3"
	engineWAV = "wav16"
)

// Request is the JSON the engine reads (upstream docs/ARCHITECTURE.md). Only
// fields the engine defines are written, and only when set: an unset field is
// left to the engine's own default.
type Request struct {
	Caption          string   `json:"caption"`
	Lyrics           string   `json:"lyrics,omitempty"`
	BPM              int      `json:"bpm,omitempty"`
	Duration         float64  `json:"duration,omitempty"`
	KeyScale         string   `json:"keyscale,omitempty"`
	TimeSignature    string   `json:"timesignature,omitempty"`
	VocalLanguage    string   `json:"vocal_language,omitempty"`
	Seed             *int64   `json:"seed,omitempty"`
	LMSeed           *int64   `json:"lm_seed,omitempty"`
	LMTemperature    *float64 `json:"lm_temperature,omitempty"`
	LMCfgScale       *float64 `json:"lm_cfg_scale,omitempty"`
	LMTopP           *float64 `json:"lm_top_p,omitempty"`
	LMTopK           *int     `json:"lm_top_k,omitempty"`
	LMNegativePrompt string   `json:"lm_negative_prompt,omitempty"`
	InferenceSteps   int      `json:"inference_steps,omitempty"`
	GuidanceScale    *float64 `json:"guidance_scale,omitempty"`
	Shift            *float64 `json:"shift,omitempty"`
	Solver           string   `json:"solver,omitempty"`
	OutputFormat     string   `json:"output_format,omitempty"`
	MP3Bitrate       int      `json:"mp3_bitrate,omitempty"`
	SynthModel       string   `json:"synth_model,omitempty"`
	LMModel          string   `json:"lm_model,omitempty"`
	// Adapter (a LoRA from the adapters directory) and its strength are
	// merged into the synthesis model; the language model ignores them.
	Adapter      string   `json:"adapter,omitempty"`
	AdapterScale *float64 `json:"adapter_scale,omitempty"`
	// TaskType and the fields after it make the request a transformation of a
	// source song (the source itself is a program argument, not a field).
	TaskType      string   `json:"task_type,omitempty"`
	CoverStrength *float64 `json:"audio_cover_strength,omitempty"`
	RepaintStart  *float64 `json:"repainting_start,omitempty"`
	RepaintEnd    *float64 `json:"repainting_end,omitempty"`
	Track         string   `json:"track,omitempty"`
	// Tags are the style words of the HeartMuLa generator; ACE-Step never sees them (the field is omitted).
	Tags string `json:"heartmula_tags,omitempty"`
	// Style is what the DiffRhythm2 generator reads as the style (the words of the track's tags, as a sentence), and
	// DR2CfgStrength and DR2Steps its guidance and sampling steps; no other generator sees them.
	Style          string   `json:"diffrhythm2_style,omitempty"`
	DR2CfgStrength *float64 `json:"diffrhythm2_cfg_strength,omitempty"`
	DR2Steps       *int     `json:"diffrhythm2_steps,omitempty"`
}

// HeartMulaSynthModel is what a HeartMuLa song records as its synthesis model: it keeps the cache of those songs
// apart from the ACE-Step ones and says in the manifest how the song was made.
const HeartMulaSynthModel = "heartmula"

// DiffRhythm2SynthModel is the same for the DiffRhythm2 generator.
const DiffRhythm2SynthModel = "diffrhythm2"

// StyleOf writes the tags of a track as the style text DiffRhythm2 reads ("pop, piano, warm").
func StyleOf(tags config.Tags) string { return strings.ReplaceAll(string(tags), ",", ", ") }

// SourceAudio is the audio handed to the synthesis besides the request: Path,
// the song a task transforms (--src-audio), and Ref, a timbre reference
// (--ref-audio). Either may be empty. A close cover sends its source as both;
// a new song with a reference sends only Ref. Paths are project-relative.
type SourceAudio struct {
	Path string
	Ref  string
}

// SourceOf returns the audio a track's synthesis needs, or nil for a plain
// new song.
func SourceOf(t config.Track) *SourceAudio {
	switch {
	case t.Task != "":
		src := filepath.Join(config.SourcesSubdir, t.Source)
		out := &SourceAudio{Path: src}
		if t.Task == config.TaskCoverNoFSQ {
			out.Ref = src
		}
		return out
	case t.Reference != "":
		return &SourceAudio{Ref: filepath.Join(config.SourcesSubdir, t.Reference)}
	}
	return nil
}

// EngineFormat maps the configured output format (mp3, wav) to the engine's.
func EngineFormat(format string) string {
	if format == config.MusicFormatWAV {
		return engineWAV
	}
	return engineMP3
}

// BuildRequest builds the request for one variant of a track. seed is the
// effective seed (it seeds both the language model and the synthesis, so the
// same request reproduces the same song). The language model is named only
// when the track uses it.
func BuildRequest(cfg *config.Config, t config.Track, seed int64) Request {
	m := cfg.Defaults.Music
	req := Request{
		Caption:       m.CaptionFor(t),
		KeyScale:      t.KeyScale,
		TimeSignature: t.TimeSignature,
		Seed:          &seed,
		LMSeed:        &seed,
		SynthModel:    m.ResolvedSynth(),
		OutputFormat:  EngineFormat(m.ResolvedOutputFormat()),
	}
	switch {
	case m.UsesHeartMuLa():
		req.SynthModel = HeartMulaSynthModel
		req.Tags = string(t.Tags)
	case m.UsesDiffRhythm2():
		req.SynthModel = DiffRhythm2SynthModel
		req.Style = StyleOf(t.Tags)
		req.DR2CfgStrength, req.DR2Steps = m.DR2CfgStrength, m.DR2Steps
	}
	switch {
	case t.Instrumental:
		req.Lyrics = instrumentalTx
	default:
		req.Lyrics = t.Lyrics
		req.VocalLanguage = t.VocalLanguage
		if req.VocalLanguage == "" && m != nil {
			req.VocalLanguage = m.VocalLanguage
		}
		// A new song never leaves the language to the model (it picked
		// Vietnamese and Chinese for Spanish prompts): deduce it from the
		// lyrics, English when they say nothing. Task tracks keep the source's.
		if req.VocalLanguage == "" && t.Task == "" {
			req.VocalLanguage = DetectLanguage(t.Lyrics)
		}
	}
	if t.BPM != nil {
		req.BPM = *t.BPM
	}
	if t.DurationSeconds != nil {
		req.Duration = *t.DurationSeconds
	}
	if t.Task != "" {
		req.TaskType = t.Task
		req.CoverStrength = t.CoverStrength
		req.RepaintStart = t.RepaintStart
		req.RepaintEnd = t.RepaintEnd
		req.Track = t.Stem
	}
	if t.EffectiveUseLM() && !m.UsesService() {
		req.LMModel = m.ResolvedLM()
	}
	if name, scale := cfg.ResolveAdapter(t); name != "" {
		req.Adapter, req.AdapterScale = name, &scale
	}
	if m != nil {
		req.LMTemperature = m.LMTemperature
		req.LMCfgScale = m.LMCfgScale
		req.LMTopP = m.LMTopP
		req.LMTopK = m.LMTopK
		req.LMNegativePrompt = m.LMNegativePrompt
		req.InferenceSteps = m.InferenceSteps
		req.GuidanceScale = m.GuidanceScale
		req.Shift = m.Shift
		req.Solver = m.Solver
		req.MP3Bitrate = m.MP3Bitrate
	}
	return req
}

// VariantSeeds resolves the effective seed of each of n variants: an explicit
// track seed gives seed+i, otherwise one random value per variant from rng.
// The orchestrator owns random seeds so they are recorded and hashed.
func VariantSeeds(t config.Track, n int, rng func() int64) []int64 {
	seeds := make([]int64, n)
	base := t.ResolvedSeed()
	for i := range seeds {
		if base == -1 {
			seeds[i] = rng()
		} else {
			seeds[i] = base + int64(i)
		}
	}
	return seeds
}

// Workspace resolves the project-relative paths the engine is given against
// the project directory, for the orchestrator's own file access. An empty
// Base means the process cwd.
type Workspace struct{ Base string }

// Path resolves a project-relative path against the project directory.
func (w Workspace) Path(p string) string { return w.abs(p) }

func (w Workspace) abs(p string) string {
	if w.Base == "" {
		return p
	}
	return filepath.Join(w.Base, p)
}

// prepare creates an empty scratch directory.
func (w Workspace) prepare(dir string) error {
	if err := os.RemoveAll(w.abs(dir)); err != nil {
		return err
	}
	return os.MkdirAll(w.abs(dir), 0o755)
}

func runOpts(env []string) []runner.Option {
	if len(env) == 0 {
		return nil
	}
	return []runner.Option{runner.WithEnv(env...)}
}

// Compose runs ace-lm on req and returns the enriched request it wrote
// (final lyrics, audio codes and resolved metadata). workDir is a
// project-relative scratch directory, modelsDir the models directory as the
// engine should see it.
func Compose(ctx context.Context, r runner.CommandRunner, ws Workspace, modelsDir, workDir string, req Request, env []string) ([]byte, error) {
	if err := ws.prepare(workDir); err != nil {
		return nil, fmt.Errorf("compose: prepare %s: %w", workDir, err)
	}
	in, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("compose: %w", err)
	}
	if err := os.WriteFile(ws.abs(filepath.Join(workDir, inputName)), in, 0o644); err != nil {
		return nil, fmt.Errorf("compose: %w", err)
	}
	args := []string{Flags.Models, modelsDir, Flags.Request, filepath.Join(workDir, inputName)}
	var stderr bytes.Buffer
	if _, err := r.Run(ctx, BinaryLM, args, append(runOpts(env), runner.WithStderr(&stderr))...); err != nil {
		return nil, fmt.Errorf("compose: %w", err)
	}
	out, err := os.ReadFile(ws.abs(filepath.Join(workDir, composedName)))
	if err != nil {
		return nil, fmt.Errorf("compose: ace-lm did not write %s: %w%s", composedName, err, whyNoOutput(stderr.String()))
	}
	_ = os.RemoveAll(ws.abs(workDir))
	return out, nil
}

// SynthRequest returns the bytes handed to ace-synth for a variant. With the
// language model it overlays the current synthesis settings of base on the
// enriched request, so changing only those re-synthesizes without
// composing again; without it, base itself is the request. Keys are sorted,
// so equal inputs give equal bytes (they feed the cache hash).
func SynthRequest(enriched []byte, base Request) ([]byte, error) {
	if enriched == nil {
		return json.MarshalIndent(base, "", "  ")
	}
	var doc map[string]any
	dec := json.NewDecoder(bytes.NewReader(enriched))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("synth: enriched request is not valid JSON: %w", err)
	}
	var overlay map[string]any
	raw, _ := json.Marshal(base)
	if err := json.Unmarshal(raw, &overlay); err != nil {
		return nil, err
	}
	for _, k := range []string{"inference_steps", "guidance_scale", "shift", "solver", "output_format", "mp3_bitrate", "synth_model", "adapter", "adapter_scale"} {
		if v, ok := overlay[k]; ok {
			doc[k] = v
		} else {
			delete(doc, k) // unset now: fall back to the engine default
		}
	}
	return json.MarshalIndent(doc, "", "  ")
}

// whyNoOutput returns the lines of a program's output that say what went wrong (it exits 0 even then), ready to
// append to an error; empty when none stands out. A CUDA out-of-memory gets a hint, the usual cause on a busy card.
func whyNoOutput(out string) string {
	var bad []string
	for _, l := range strings.Split(out, "\n") {
		if u := strings.ToUpper(l); strings.Contains(u, "FATAL") || strings.Contains(u, "ERROR") || strings.Contains(u, "OUT OF MEMORY") {
			bad = append(bad, strings.TrimSpace(l))
		}
	}
	if len(bad) == 0 {
		return ""
	}
	if len(bad) > 6 {
		bad = bad[len(bad)-6:]
	}
	msg := "\n" + strings.Join(bad, "\n")
	if strings.Contains(strings.ToLower(out), "out of memory") {
		msg += "\nthe graphics card ran out of memory: stop the other services that use it (HeartMuLa, DiffRhythm2, Ollama), or pick the other GPU"
	}
	return msg
}

// Synth runs ace-synth on request and moves the audio it wrote to dst. format
// is the configured output format (mp3 or wav). adaptersDir, when not empty, is
// handed to the program so the adapter the request names can be found (the
// program stops without it); the language model never needs it. src, when not
// nil, is the song a cover, repaint or extract transforms.
func Synth(ctx context.Context, r runner.CommandRunner, ws Workspace, modelsDir, adaptersDir, workDir string, request []byte, format, dst string, env []string, src *SourceAudio) error {
	if err := ws.prepare(workDir); err != nil {
		return fmt.Errorf("synth: prepare %s: %w", workDir, err)
	}
	if err := os.WriteFile(ws.abs(filepath.Join(workDir, inputName)), request, 0o644); err != nil {
		return fmt.Errorf("synth: %w", err)
	}
	args := []string{Flags.Models, modelsDir}
	if adaptersDir != "" {
		args = append(args, Flags.Adapters, adaptersDir)
	}
	if src != nil {
		if src.Path != "" {
			args = append(args, Flags.SrcAudio, src.Path)
		}
		if src.Ref != "" {
			args = append(args, Flags.RefAudio, src.Ref)
		}
	}
	args = append(args, Flags.Request, filepath.Join(workDir, inputName))
	var stderr bytes.Buffer
	if _, err := r.Run(ctx, BinarySynth, args, append(runOpts(env), runner.WithStderr(&stderr))...); err != nil {
		return fmt.Errorf("synth: %w", err)
	}
	produced := ws.abs(filepath.Join(workDir, synthStem+"."+format))
	if err := os.MkdirAll(filepath.Dir(ws.abs(dst)), 0o755); err != nil {
		return fmt.Errorf("synth: %w", err)
	}
	if err := os.Rename(produced, ws.abs(dst)); err != nil {
		return fmt.Errorf("synth: ace-synth did not write %s: %w%s", filepath.Base(produced), err, whyNoOutput(stderr.String()))
	}
	_ = os.RemoveAll(ws.abs(workDir))
	return nil
}

// Resolved is what the manifest needs from a request after composition.
type Resolved struct {
	Caption       string  `json:"caption"`
	Lyrics        string  `json:"lyrics"`
	BPM           int     `json:"bpm"`
	Duration      float64 `json:"duration"`
	KeyScale      string  `json:"keyscale"`
	TimeSignature string  `json:"timesignature"`
	VocalLanguage string  `json:"vocal_language"`
	Seed          int64   `json:"seed"`
	LMSeed        int64   `json:"lm_seed"`
}

// ParseResolved reads the resolved fields of an enriched or direct request.
func ParseResolved(request []byte) (Resolved, error) {
	var res Resolved
	if err := json.Unmarshal(request, &res); err != nil {
		return res, fmt.Errorf("music: request is not valid JSON: %w", err)
	}
	return res, nil
}

// Params returns every generation parameter of the request as the manifest
// records it, leaving out the bulky text and audio codes (recorded apart or
// reproducible from the seed).
func Params(request []byte) (map[string]any, error) {
	doc := map[string]any{}
	if err := json.Unmarshal(request, &doc); err != nil {
		return nil, fmt.Errorf("music: request is not valid JSON: %w", err)
	}
	for _, k := range []string{"audio_codes", "caption", "lyrics"} {
		delete(doc, k)
	}
	return doc, nil
}

// AudioSeconds estimates the length of an exported file without ffprobe: a
// 16-bit stereo 48 kHz WAV from its size, a constant-bitrate MP3 from its
// size and bitrate (the engine's default is 128 kbps).
func AudioSeconds(path, format string, bitrate int) (float64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if strings.EqualFold(format, config.MusicFormatWAV) {
		const header, bytesPerSecond = 44, 48000 * 2 * 2
		return float64(fi.Size()-header) / bytesPerSecond, nil
	}
	if bitrate <= 0 {
		bitrate = 128
	}
	return float64(fi.Size()) * 8 / float64(bitrate*1000), nil
}

// FindSupport returns the base name of the first file in modelsDir matching
// glob (the text encoder and the VAE, which the engine finds by name), or ""
// when there is none. Matches are sorted so the choice is stable.
func FindSupport(ws Workspace, modelsDir, glob string) string {
	matches, _ := filepath.Glob(ws.abs(filepath.Join(modelsDir, glob)))
	if len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	return filepath.Base(matches[0])
}

// AdapterBytes is the total size of an adapter's files: the file itself, or
// everything in its folder. An adapter that is not there counts as 0 (the
// config validation reports it elsewhere).
func AdapterBytes(ws Workspace, adaptersDir, name string) int64 {
	if name == "" {
		return 0
	}
	root := ws.abs(filepath.Join(adaptersDir, name))
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
}

// limiterCeiling keeps the encoded song under 0 dBFS: the engine's own MP3
// encoder lets peaks through that decode above full scale (+0.7 dB measured),
// which clips on playback and is heard as crackle. 0.79 is about -2 dBFS,
// leaving room for the overshoot the encoding itself adds.
const limiterCeiling = "0.79"

// EncodeMP3 turns the engine's WAV at src into an MP3 at dst, passing it
// through a peak limiter first. Paths are relative to the project directory,
// where the runner works (as the mixes do). The WAV is removed on success.
func EncodeMP3(ctx context.Context, r runner.CommandRunner, ws Workspace, src, dst string, bitrate int) error {
	if bitrate <= 0 {
		bitrate = 128
	}
	args := []string{"-y", "-i", src, "-af", "alimiter=limit=" + limiterCeiling + ":level=disabled",
		"-c:a", "libmp3lame", "-b:a", fmt.Sprintf("%dk", bitrate), dst}
	if _, err := r.Run(ctx, "ffmpeg", args); err != nil {
		return fmt.Errorf("synth: encoding the mp3: %w", err)
	}
	_ = os.Remove(ws.abs(src))
	return nil
}

// MaxLoudnessGainDB is the largest correction Normalize applies, either way: a song that far from the target
// is broken, and a bigger boost would only raise the noise.
const MaxLoudnessGainDB = 12.0

// Normalize evens the loudness of the song at path (relative to the project directory, like EncodeMP3's
// paths) to target LUFS: it measures the song, applies the missing gain and passes it through the same peak
// limiter as the export, writing the result over the file. It returns the original loudness and the gain
// applied; a song already within 0.3 dB of the target, or silent, is left untouched (gain 0). An mp3 is
// encoded once more at the given bitrate.
func Normalize(ctx context.Context, r runner.CommandRunner, ws Workspace, path, format string, bitrate int, target float64) (from, gain float64, err error) {
	from, err = probe.Loudness(ctx, r, path)
	if err != nil {
		if errors.Is(err, probe.ErrSilent) {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("music: measuring the loudness: %w", err)
	}
	gain = math.Max(-MaxLoudnessGainDB, math.Min(MaxLoudnessGainDB, target-from))
	if math.Abs(gain) < 0.3 {
		return from, 0, nil
	}
	tmp := path + ".norm." + format
	args := []string{"-y", "-i", path, "-vn", "-af", fmt.Sprintf("volume=%.2fdB,alimiter=limit=%s:level=disabled", gain, limiterCeiling)}
	if format == config.MusicFormatMP3 {
		if bitrate <= 0 {
			bitrate = 128
		}
		args = append(args, "-c:a", "libmp3lame", "-b:a", fmt.Sprintf("%dk", bitrate))
	}
	args = append(args, tmp)
	if _, err := r.Run(ctx, "ffmpeg", args); err != nil {
		_ = os.Remove(ws.abs(tmp))
		return from, 0, fmt.Errorf("music: normalizing the loudness: %w", err)
	}
	if err := os.Rename(ws.abs(tmp), ws.abs(path)); err != nil {
		_ = os.Remove(ws.abs(tmp))
		return from, 0, err
	}
	return from, gain, nil
}

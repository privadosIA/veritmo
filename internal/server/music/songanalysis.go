package music

// songanalysis.go reads a song or a video that is already in a project's sources/:
// its lyrics (Whisper, on this machine) and its tempo and key (an estimator of our
// own). Nothing here writes into the project and nothing leaves the machine.
// See specs/song-analysis.

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"veritmo/internal/music/audiomeasure"
	"veritmo/internal/music/separate"
	"veritmo/internal/music/transcribe"
	"veritmo/internal/platform/sched"
	"veritmo/internal/project/config"
	"veritmo/internal/server/api"
)

// whisperDir is where the Whisper model lives (a variable so tests can point it elsewhere).
// defaultWhisperDir is where the Whisper models are, relative to where the server runs.
const defaultWhisperDir = "models/whisper"

// defaultDemucsDir is where the weights of the voice separator are, relative to where the server runs.
const defaultDemucsDir = "models/demucs"

const (
	// transcribeTimeout bounds a transcription; closing the request cancels it earlier.
	transcribeTimeout = 10 * time.Minute
	// separateTimeout bounds the separation of the voice of a song (about 0.8 times its length with four threads).
	separateTimeout = 15 * time.Minute
	// measureMaxBytes bounds the decoded audio held to measure it (about 20 minutes at 22.05 kHz).
	measureMaxBytes = 20 * 60 * audiomeasure.Rate * 4
	// whisperMemFactor and whisperMemMargin are what the model needs besides its file.
	whisperMemFactor = 1.3
	whisperMemMargin = 1 << 30
)

// songSource checks the request's source (an mp3, a wav or a kept video of the project) and
// answers the error itself when it is not one. It returns the file's path.
func (h *Handler) songSource(w http.ResponseWriter, id, source string) (string, bool) {
	if source == "" || filepath.Base(source) != source || !config.ValidSourceFile(source) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "source must be the name of an audio or a video in the project's sources")
		return "", false
	}
	if k := sourceKind(source); k != "audio" && k != "video" {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "source must be an audio (.mp3, .wav) or a video, not a picture")
		return "", false
	}
	path := filepath.Join(h.ws.SourcesDir(id), source)
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("the project has no source %q", source))
		return "", false
	}
	return path, true
}

// toolProblem is why a local tool (Whisper, the voice separator) cannot be used now, with the answer the API gives for it.
type toolProblem struct {
	status int
	code   string
	msg    string
}

func (p *toolProblem) write(w http.ResponseWriter) { api.WriteError(w, p.status, p.code, p.msg) }

// whisperReady says whether Whisper and ffmpeg are installed: the binary, its model and its size, or the problem.
func (h *Handler) whisperReady() (bin, model string, modelSize int64, p *toolProblem) {
	bin, err := h.host.LookPath("whisper-cli")
	model, modelSize = h.whisperModel()
	if err != nil || model == "" {
		return "", "", 0, &toolProblem{http.StatusServiceUnavailable, api.CodeUnavailable, "the lyrics reader is not installed: run `bash scripts/get-whisper.sh` (whisper-cli and the model in models/whisper)"}
	}
	if _, err := h.host.LookPath("ffmpeg"); err != nil {
		return "", "", 0, &toolProblem{http.StatusServiceUnavailable, api.CodeUnavailable, "ffmpeg is not installed: it is needed to read the audio"}
	}
	return bin, model, modelSize, nil
}

// whisperRoom says whether Whisper can run now: no run is queued or active (the model needs memory) and the machine
// holds the model (its file and what it needs besides it); otherwise the problem.
func (h *Handler) whisperRoom(modelSize int64) *toolProblem {
	if rid, busy := h.ws.AnyBusy(); busy {
		return &toolProblem{http.StatusConflict, api.CodeConflict, fmt.Sprintf("run %s is queued or active: Whisper needs memory, so reading the lyrics waits until no run is in progress", rid)}
	}
	return h.whisperFitsProblem(modelSize)
}

// whisperFitsProblem is the problem when the machine cannot hold the Whisper model, with how much memory is missing.
func (h *Handler) whisperFitsProblem(modelSize int64) *toolProblem {
	return h.fitsProblem("Whisper", int64(float64(modelSize)*whisperMemFactor)+whisperMemMargin)
}

// fitsProblem is the problem when fewer than need bytes of memory are available, naming the tool and how much is missing.
func (h *Handler) fitsProblem(tool string, need int64) *toolProblem {
	avail := h.host.MemAvailable()
	if avail == nil {
		avail = sched.MemAvailable
	}
	if have, err := avail(); err == nil && have < need {
		return &toolProblem{http.StatusServiceUnavailable, api.CodeUnavailable,
			fmt.Sprintf("not enough memory for %s: %.1f GB are needed and %.1f GB are available (%.1f GB missing)", tool, gb(need), gb(have), gb(need-have))}
	}
	return nil
}

// separateReady says whether the voice separator (demucs and its weights) and ffmpeg are installed: the program and the
// weights, or the problem. The separator is optional: a caller that gets a problem times the words from the whole mix.
func (h *Handler) separateReady() (bin, model string, p *toolProblem) {
	bin, err := h.host.LookPath("demucs")
	model, _, merr := separate.Model(h.demucsDir)
	if err != nil || merr != nil {
		return "", "", &toolProblem{http.StatusServiceUnavailable, api.CodeUnavailable, "the voice separator is not installed: run `bash scripts/get-demucs.sh --build` (demucs and its weights in models/demucs)"}
	}
	if _, err := h.host.LookPath("ffmpeg"); err != nil {
		return "", "", &toolProblem{http.StatusServiceUnavailable, api.CodeUnavailable, "ffmpeg is not installed: it is needed to read the audio"}
	}
	return bin, model, nil
}

// separateFits is the problem when the machine cannot hold a separation (separate.MemBytes), or when a run is queued or
// active: the separator needs memory too, so it waits until no run is in progress.
func (h *Handler) separateFits() *toolProblem {
	if rid, busy := h.ws.AnyBusy(); busy {
		return &toolProblem{http.StatusConflict, api.CodeConflict, fmt.Sprintf("run %s is queued or active: the voice separator needs memory, so it waits until no run is in progress", rid)}
	}
	return h.fitsProblem("the voice separator", separate.MemBytes)
}

// separateVoice takes the voice out of a song: a 44.1 kHz stereo wav of the source for demucs and, from its vocal stem, the
// 16 kHz mono wav Whisper reads (dir/vocals16k.wav). The caller has checked separateReady and separateFits and owns dir.
func (h *Handler) separateVoice(ctx context.Context, bin, model, in, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, separateTimeout)
	defer cancel()
	wav := filepath.Join(dir, "song44k.wav")
	if _, err := h.host.Runner().Run(ctx, "ffmpeg", []string{"-y", "-v", "error", "-i", in, "-vn", "-ac", "2", "-ar", "44100", wav}); err != nil {
		return "", fmt.Errorf("cannot read this audio: %s", api.LastLines(err.Error(), 3))
	}
	vocals, err := (separate.Tool{Runner: h.host.Runner(), Binary: bin, Model: model}).Run(ctx, wav, filepath.Join(dir, "stems"))
	_ = os.Remove(wav) // as big as the song and not needed any more
	if err != nil {
		return "", err
	}
	out := filepath.Join(dir, "vocals16k.wav")
	if _, err := h.host.Runner().Run(ctx, "ffmpeg", []string{"-y", "-v", "error", "-i", vocals, "-ac", "1", "-ar", "16000", out}); err != nil {
		return "", fmt.Errorf("cannot read the separated voice: %s", api.LastLines(err.Error(), 3))
	}
	_ = os.RemoveAll(filepath.Dir(vocals))
	return out, nil
}

// whisperFits answers the 503 itself, saying how much memory is missing, when the machine cannot hold the Whisper
// model (its file and what it needs besides it).
func (h *Handler) whisperFits(w http.ResponseWriter, modelSize int64) bool {
	if p := h.whisperFitsProblem(modelSize); p != nil {
		p.write(w)
		return false
	}
	return true
}

// toWhisperWAV converts any audio or video to the 16 kHz mono wav Whisper reads, keeping at most
// maxSeconds of it when that is not zero. It answers the 422 itself when the file cannot be read; a
// client that left gets no answer.
func (h *Handler) toWhisperWAV(w http.ResponseWriter, r *http.Request, ctx context.Context, in, out string, maxSeconds int) bool {
	args := []string{"-y", "-v", "error"}
	if maxSeconds > 0 {
		args = append(args, "-t", fmt.Sprint(maxSeconds))
	}
	args = append(args, "-i", in, "-vn", "-ac", "1", "-ar", "16000", out)
	if _, err := h.host.Runner().Run(ctx, "ffmpeg", args); err != nil {
		if r.Context().Err() == nil {
			api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, fmt.Sprintf("cannot read this audio (does it have sound?): %s", api.LastLines(err.Error(), 3)))
		}
		return false
	}
	return true
}

func (h *Handler) needFFmpeg(w http.ResponseWriter) bool {
	if _, err := h.host.LookPath("ffmpeg"); err != nil {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, "ffmpeg is not installed: it is needed to read the audio")
		return false
	}
	return true
}

// POST /api/projects/{id}/transcribe {"source": "song.mp3", "language"?: "es"}
//
//	-> {"language", "language_detected", "text", "segments": [{"start","end","text"}], "dropped", "model"}
//
// A source with no words is a 200 with an empty text. It waits for no GPU, but a
// run in progress is a 409 because the model needs memory.
func (h *Handler) handleTranscribe(w http.ResponseWriter, r *http.Request) {
	id, ok := h.ws.RequireProject(w, r)
	if !ok {
		return
	}
	var req struct {
		Source   string `json:"source"`
		Language string `json:"language"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if req.Language != "" && !api.LanguagePattern.MatchString(req.Language) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "language must be an ISO 639-1 code such as es or en")
		return
	}
	path, ok := h.songSource(w, id, req.Source)
	if !ok {
		return
	}
	bin, model, modelSize, problem := h.whisperReady()
	if problem != nil {
		problem.write(w)
		return
	}
	if problem := h.whisperRoom(modelSize); problem != nil {
		problem.write(w)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), transcribeTimeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "transcribe-*")
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	defer os.RemoveAll(dir)
	wav := filepath.Join(dir, "audio.wav")
	if !h.toWhisperWAV(w, r, ctx, path, wav, 0) {
		return
	}
	res, err := (transcribe.Tool{Runner: h.host.Runner(), Binary: bin, Model: model}).Run(ctx, wav, req.Language)
	if err != nil {
		if r.Context().Err() != nil {
			return // the client left: nothing to answer, and the process was stopped with the context
		}
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			api.WriteError(w, http.StatusGatewayTimeout, api.CodeInternal, "reading the lyrics took more than 10 minutes and was stopped")
			return
		}
		api.WriteError(w, http.StatusBadGateway, api.CodeInternal, err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, res)
}

// whisperModel is the Whisper model in models/whisper: the large-v3-turbo one when it is there,
// otherwise the first .bin by name. Empty when there is none.
func (h *Handler) whisperModel() (string, int64) {
	entries, err := os.ReadDir(h.whisperDir)
	if err != nil {
		return "", 0
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".bin") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", 0
	}
	sort.Strings(names)
	pick := names[0]
	for _, n := range names {
		if strings.Contains(n, "large-v3-turbo") {
			pick = n
			break
		}
	}
	fi, err := os.Stat(filepath.Join(h.whisperDir, pick))
	if err != nil {
		return "", 0
	}
	return filepath.Join(h.whisperDir, pick), fi.Size()
}

func gb(b int64) float64 { return float64(b) / float64(sched.GiB) }

type measureAnswer struct {
	Duration       float64   `json:"duration"`
	BPM            *float64  `json:"bpm"`
	BPMAlternative []float64 `json:"bpm_alternatives"`
	BPMConfidence  float64   `json:"bpm_confidence"`
	KeyScale       *string   `json:"keyscale"`
	KeyConfidence  float64   `json:"key_confidence"`
}

// POST /api/projects/{id}/measure {"source": "song.mp3"}
//
//	-> {"duration", "bpm", "bpm_alternatives", "bpm_confidence", "keyscale", "key_confidence"}
//
// The tempo and the key are estimates (null when there is no clear pulse or key). It uses no
// model and no GPU, so it does not wait for runs.
func (h *Handler) handleMeasure(w http.ResponseWriter, r *http.Request) {
	id, ok := h.ws.RequireProject(w, r)
	if !ok {
		return
	}
	var req struct {
		Source string `json:"source"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	path, ok := h.songSource(w, id, req.Source)
	if !ok {
		return
	}
	if !h.needFFmpeg(w) {
		return
	}
	raw, err := h.host.Runner().Run(r.Context(), "ffmpeg", []string{"-v", "error", "-i", path, "-vn", "-ac", "1", "-ar", fmt.Sprint(audiomeasure.Rate), "-f", "f32le", "-"})
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, fmt.Sprintf("cannot read this audio (does it have sound?): %s", api.LastLines(err.Error(), 3)))
		return
	}
	if len(raw) == 0 {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, "the source has no audio")
		return
	}
	if len(raw) > measureMaxBytes {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, "the audio is too long to measure (more than 20 minutes)")
		return
	}
	samples := make([]float32, len(raw)/4)
	for i := range samples {
		samples[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	m := audiomeasure.Measure(samples)
	ans := measureAnswer{Duration: m.Duration, BPM: m.BPM, BPMAlternative: m.BPMAlternative, BPMConfidence: round2(m.BPMConfidence), KeyScale: m.Key, KeyConfidence: round2(m.KeyConfidence)}
	if ans.BPMAlternative == nil {
		ans.BPMAlternative = []float64{}
	}
	api.WriteJSON(w, http.StatusOK, ans)
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

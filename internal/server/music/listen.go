package music

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/music/audiomeasure"
	"veritmo/internal/music/transcribe"
	"veritmo/internal/server/api"
)

const (
	// listenMaxBytes and listenMaxSeconds bound a recording of a song playing: half a minute is plenty.
	listenMaxBytes   = 10 << 20
	listenMaxSeconds = 60
	// listenMinSeconds is the least that can be told anything from.
	listenMinSeconds = 4
	// listenWords is how many words of the lyrics the model reads: enough for the genre and the theme.
	listenWords = 120
	// listenMinConfidence is the confidence under which a tempo or a key is not offered.
	listenMinConfidence = 0.25
)

// listenAnswer is the body of POST /api/assist/listen. The lyrics are never part of it: only what kind of
// song it is and what it talks about.
type listenAnswer struct {
	Seconds  float64 `json:"seconds"`
	Language string  `json:"language,omitempty"`
	// NoWords says that fewer than four words were recognised: the song may be instrumental or its voice could not be read.
	NoWords bool `json:"no_words"`
	// Sound is what the song sounds like, in plain words, measured on the recording.
	Sound []string `json:"sound,omitempty"`
	// GenreUnknown says the assistant could not put a genre to the song; the style then only describes the sound.
	GenreUnknown  bool     `json:"genre_unknown,omitempty"`
	BPM           *float64 `json:"bpm,omitempty"`
	BPMConfidence float64  `json:"bpm_confidence,omitempty"`
	KeyScale      string   `json:"keyscale,omitempty"`
	KeyConfidence float64  `json:"key_confidence,omitempty"`
	// Heard are the tags CLAP gave to the sound (genre, mood, instruments), when that method was asked for.
	Heard    []string `json:"heard,omitempty"`
	Style    string   `json:"style,omitempty"`
	Theme    string   `json:"theme,omitempty"`
	Request  string   `json:"request,omitempty"`
	Warnings []string `json:"warnings"`
}

// POST /api/assist/listen[?language=es] with the recording as the body
//
//	-> {"seconds", "language", "no_words", "bpm"?, "keyscale"?, "sound"?, "style", "theme", "genre_unknown"?, "request", "warnings"}
//
// Listens to a recording of a song that is playing (the browser's microphone) and says what kind of song it
// is: its tempo and key (an estimator of our own), what it is about and a description of its style (the
// assistant, from a few words of its lyrics read with Whisper and the figures). `request` is the sentence to
// give the Create page's assistant to write a new song of that kind: the lyrics heard are NOT returned
// nor kept, and nothing of the recording is logged. Every figure is an estimate.
func (h *Handler) handleListenSong(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Query().Get("method")
	switch method {
	case "", recognizeWhisper, recognizeCLAP, recognizeMuQ:
	default:
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "method must be whisper, clap or muq")
		return
	}
	lang := r.URL.Query().Get("language")
	if lang != "" && !api.LanguagePattern.MatchString(lang) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "language must be an ISO 639-1 code such as es or en")
		return
	}
	api.RaiseBodyLimit(w, r, listenMaxBytes)
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, listenMaxBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			api.WriteError(w, http.StatusRequestEntityTooLarge, api.CodeInvalid, fmt.Sprintf("the recording is larger than %d MB", listenMaxBytes>>20))
		} else if r.Context().Err() == nil {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "cannot read the recording")
		}
		return
	}
	if len(body) == 0 {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "the body must be the recording")
		return
	}
	// The assistant is needed to describe the song: its gate (routing, consent, a run in progress) comes first,
	// before the work of reading the recording.
	ctx, ok := h.assistant.Gate(w, r, "")
	if !ok {
		return
	}
	bin, err := h.host.LookPath("whisper-cli")
	model, modelSize := h.whisperModel()
	if err != nil || model == "" {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, "the voice reader is not installed: run `bash scripts/get-whisper.sh` (whisper-cli and the model in models/whisper)")
		return
	}
	if !h.needFFmpeg(w) || !h.whisperFits(w, modelSize) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, transcribeTimeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "listen-*")
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	defer os.RemoveAll(dir)
	in, wav := filepath.Join(dir, "recording"), filepath.Join(dir, "audio.wav")
	if err := os.WriteFile(in, body, 0o600); err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	if !h.toWhisperWAV(w, r, ctx, in, wav, listenMaxSeconds+1) {
		return
	}
	fi, err := os.Stat(wav)
	if err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, "cannot read this audio (does it have sound?)")
		return
	}
	seconds := math.Max(0, float64(fi.Size()-wavHeaderBytes)/wavBytesPerSecond)
	if seconds > listenMaxSeconds {
		api.WriteError(w, http.StatusRequestEntityTooLarge, api.CodeInvalid, fmt.Sprintf("the recording is longer than %d seconds", listenMaxSeconds))
		return
	}
	if seconds < listenMinSeconds {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, fmt.Sprintf("the recording is too short: at least %d seconds of the song are needed", listenMinSeconds))
		return
	}
	ans := listenAnswer{Seconds: seconds, Warnings: []string{}}

	// Tempo and key: estimators of our own, with no model.
	if raw, err := h.host.Runner().Run(ctx, "ffmpeg", []string{"-v", "error", "-t", fmt.Sprint(listenMaxSeconds), "-i", in, "-vn", "-ac", "1", "-ar", fmt.Sprint(audiomeasure.Rate), "-f", "f32le", "-"}); err == nil && len(raw) >= 4 {
		samples := make([]float32, len(raw)/4)
		for i := range samples {
			samples[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
		}
		if snd, ok := audiomeasure.Describe(samples); ok {
			ans.Sound = snd.Words()
		}
		m := audiomeasure.Measure(samples)
		if m.BPM != nil && m.BPMConfidence >= listenMinConfidence {
			ans.BPM, ans.BPMConfidence = m.BPM, round2(m.BPMConfidence)
		}
		if m.Key != nil && *m.Key != "" && m.KeyConfidence >= listenMinConfidence {
			ans.KeyScale, ans.KeyConfidence = *m.Key, round2(m.KeyConfidence)
		}
	} else if r.Context().Err() != nil {
		return
	}

	// What the sound is like, from CLAP (its service must be running) or MuQ-MuLan, when it was asked for.
	var heard clapReading
	if method == recognizeCLAP || method == recognizeMuQ {
		var err error
		if heard, err = h.hear(ctx, method, in); err != nil {
			if r.Context().Err() != nil {
				return
			}
			var down *errClapDown
			if errors.As(err, &down) {
				api.WriteJSON(w, http.StatusServiceUnavailable, api.APIError{Error: api.APIErrorBody{Code: api.CodeUnavailable, Message: down.detail, Reason: down.reason}})
				return
			}
			api.WriteError(w, http.StatusBadGateway, api.CodeInternal, err.Error())
			return
		}
		ans.Heard = heard.Tags()
	}

	// The words, if any: a few of them are enough to tell the genre and the theme.
	res, err := (transcribe.Tool{Runner: h.host.Runner(), Binary: bin, Model: model}).Run(ctx, wav, lang)
	if err != nil {
		if r.Context().Err() != nil {
			return // the client left
		}
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			api.WriteError(w, http.StatusGatewayTimeout, api.CodeInternal, "reading the recording took too long and was stopped")
			return
		}
		api.WriteError(w, http.StatusBadGateway, api.CodeInternal, err.Error())
		return
	}
	text, voiced := spokenText(res.Segments)
	if voiced < dictationMinVoice || hallucinations[squash(text)] {
		text = ""
	}
	ans.Language = res.Language
	words := strings.Fields(text)
	ans.NoWords = len(words) < 4
	if len(words) > listenWords {
		words = words[:listenWords]
	}

	facts := listenFacts(strings.Join(words, " "), ans) + soundFacts(heard)
	run := h.assistant.RunFunc()
	reply, err := run(ctx, assist.TaskListen, facts, "")
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		api.WriteAssistError(w, err)
		return
	}
	g := assist.ParseListenReply(reply)
	ans.Style, ans.Theme, ans.GenreUnknown = g.Style, g.Theme, g.GenreUnknown()
	if ans.NoWords {
		ans.Theme = ""
	}
	if ans.Style == "" {
		ans.Warnings = append(ans.Warnings, "the assistant could not tell the style of the song")
	} else if ans.GenreUnknown {
		ans.Warnings = append(ans.Warnings, "the genre could not be told: the style only describes the sound, check it")
	}
	ans.Request = listenRequest(ans)
	api.WriteJSON(w, http.StatusOK, ans)
}

// listenFacts is what the model is given: the words, the tempo and the key, as they were estimated.
func listenFacts(words string, a listenAnswer) string {
	var b strings.Builder
	if words == "" {
		b.WriteString("Lyrics: none recognised (the song may be instrumental or its voice could not be read)\n")
	} else {
		b.WriteString("Lyrics (a few words): " + words + "\n")
	}
	if a.BPM != nil {
		fmt.Fprintf(&b, "Tempo: about %.0f BPM\n", *a.BPM)
	}
	if a.KeyScale != "" {
		b.WriteString("Key: " + a.KeyScale + "\n")
	}
	if a.Language != "" && words != "" {
		b.WriteString("Language of the lyrics: " + a.Language + "\n")
	}
	if len(a.Sound) > 0 {
		b.WriteString("Sound (measured): " + strings.Join(a.Sound, "; ") + "\n")
	}
	return b.String()
}

// listenRequest is the sentence for the Create page's assistant: a new song of the kind that was heard.
func listenRequest(a listenAnswer) string {
	if a.Style == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("A new original song in this style: " + a.Style)
	if a.BPM != nil {
		fmt.Fprintf(&b, ", about %.0f BPM", *a.BPM)
	}
	if a.KeyScale != "" {
		b.WriteString(", in " + a.KeyScale)
	}
	b.WriteString(".")
	if a.Theme != "" {
		b.WriteString(" It is about: " + a.Theme + ".")
	}
	return b.String()
}

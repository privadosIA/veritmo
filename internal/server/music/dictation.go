package music

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"veritmo/internal/music/transcribe"
	"veritmo/internal/server/api"
)

const (
	// dictationMaxBytes and dictationMaxSeconds bound a spoken request: a sentence or two.
	dictationMaxBytes   = 5 << 20
	dictationMaxSeconds = 60
	// dictationMinVoice is the least speech, in seconds, that counts as something said.
	dictationMinVoice = 0.4
	// wavHeaderBytes and wavBytesPerSecond describe the 16 kHz mono 16-bit wav ffmpeg writes.
	wavHeaderBytes    = 44
	wavBytesPerSecond = 32000
)

// hallucinations are the lines Whisper makes up for silence and noise (a silent recording of four seconds
// came back as "Thank you."). A text that is exactly one
// of them (compared without case, accents, punctuation or spaces) is taken for no speech.
var hallucinations = map[string]bool{
	"graciasporverelvideo":                         true,
	"subtitulosrealizadosporlacomunidaddeamaraorg": true,
	"subtitulosporlacomunidaddeamaraorg":           true,
	"thankyou":                                     true,
	"thanks":                                       true,
	"gracias":                                      true,
	"thanksforwatching":                            true,
	"thankyouforwatching":                          true,
}

// dictationAnswer is the body of POST /api/transcribe.
type dictationAnswer struct {
	Text             string  `json:"text"`
	Language         string  `json:"language"`
	LanguageDetected bool    `json:"language_detected"`
	Duration         float64 `json:"duration"`
	Model            string  `json:"model"`
}

// POST /api/transcribe[?language=es] with the recording as the body -> {"text", "language",
// "language_detected", "duration", "model"}
//
// Reads a short recording, as the browser makes it, with Whisper on this machine and the CPU. The
// recording lives in a temporary folder for the length of the call and nothing of it (nor its text)
// is logged. A recording without words is a 200 with an empty text. Unlike the transcription of a
// song it does not wait for runs (only for memory) and does not clean repeated lines.
func (h *Handler) handleDictation(w http.ResponseWriter, r *http.Request) {
	lang := r.URL.Query().Get("language")
	if lang != "" && !api.LanguagePattern.MatchString(lang) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "language must be an ISO 639-1 code such as es or en")
		return
	}
	api.RaiseBodyLimit(w, r, dictationMaxBytes)
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, dictationMaxBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			api.WriteError(w, http.StatusRequestEntityTooLarge, api.CodeInvalid, fmt.Sprintf("the recording is larger than %d MB", dictationMaxBytes>>20))
		} else if r.Context().Err() == nil {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "cannot read the recording")
		}
		return
	}
	if len(body) == 0 {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "the body must be the recording")
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
	ctx, cancel := context.WithTimeout(r.Context(), transcribeTimeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "dictation-*")
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
	// One second more than the limit is read, so a longer recording is told from one that just fits.
	if !h.toWhisperWAV(w, r, ctx, in, wav, dictationMaxSeconds+1) {
		return
	}
	fi, err := os.Stat(wav)
	if err != nil {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, "cannot read this audio (does it have sound?)")
		return
	}
	seconds := float64(fi.Size()-wavHeaderBytes) / wavBytesPerSecond
	if seconds < 0 {
		seconds = 0
	}
	if seconds > dictationMaxSeconds {
		api.WriteError(w, http.StatusRequestEntityTooLarge, api.CodeInvalid, fmt.Sprintf("the recording is longer than %d seconds", dictationMaxSeconds))
		return
	}
	res, err := (transcribe.Tool{Runner: h.host.Runner(), Binary: bin, Model: model, KeepRepeats: true}).Run(ctx, wav, lang)
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
	api.WriteJSON(w, http.StatusOK, dictationAnswer{
		Text: text, Language: res.Language, LanguageDetected: res.LanguageDetected, Duration: seconds, Model: res.Model,
	})
}

// spokenText joins the segments with spaces and adds up the seconds they cover.
func spokenText(segs []transcribe.Segment) (string, float64) {
	parts := make([]string, 0, len(segs))
	var voiced float64
	for _, s := range segs {
		parts = append(parts, s.Text)
		voiced += s.End - s.Start
	}
	return strings.Join(parts, " "), voiced
}

// squash keeps the letters and digits of a text in lower case, without spaces or punctuation, so
// that a line can be told from a known hallucination whatever its spelling.
func squash(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch r {
		case 'á':
			r = 'a'
		case 'é':
			r = 'e'
		case 'í':
			r = 'i'
		case 'ó':
			r = 'o'
		case 'ú':
			r = 'u'
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

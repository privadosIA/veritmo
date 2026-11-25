package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"veritmo/internal/platform/runner"
	"veritmo/internal/server/api"
	"veritmo/internal/speech/tts"
)

// MaxSpeechChars bounds a voice preview: it is a listen-before-you-run check, not the narration.
const MaxSpeechChars = 600

// speechRequest is the body of POST /api/tts/preview.
type speechRequest struct {
	Text string `json:"text"`
	// Voice is a voice as scenes.yaml names it: voices/<name>.onnx.
	Voice string  `json:"voice"`
	Speed float64 `json:"speed,omitempty"`
	// Speaker is the speaker of a voice that holds several (see GET /api/environment); default 0.
	Speaker int `json:"speaker,omitempty"`
}

// HandleSpeechPreview serves POST /api/tts/preview {"text", "voice", "speed"?} -> audio/wav.
//
// It speaks the text with Piper (on the CPU) so a narration can be heard before a run. The voice must
// be an installed one; nothing is written to the project.
func (h *Handler) HandleSpeechPreview(w http.ResponseWriter, r *http.Request) {
	var req speechRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "text is empty")
		return
	}
	if utf8.RuneCountInString(text) > MaxSpeechChars {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("text is longer than %d characters: listen to a part of it", MaxSpeechChars))
		return
	}
	if req.Speed == 0 {
		req.Speed = 1
	}
	if req.Speed < 0.1 || req.Speed > 4 {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "speed must be between 0.1 and 4")
		return
	}
	voice := path.Clean(strings.ReplaceAll(req.Voice, "\\", "/"))
	if path.Dir(voice) != "voices" || !strings.HasSuffix(voice, ".onnx") {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "voice must be one of the installed voices (voices/<name>.onnx)")
		return
	}
	file := filepath.Join(h.host.AssetsRoot(), filepath.FromSlash(voice))
	for _, f := range []string{file, file + ".json"} {
		if _, err := os.Stat(f); err != nil {
			api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("voice %s is not installed", voice))
			return
		}
	}
	if n := tts.SpeakerCount(file); req.Speaker < 0 || req.Speaker >= n {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("speaker must be between 0 and %d for %s", n-1, voice))
		return
	}
	out, err := os.CreateTemp("", "vt-speech-*.wav")
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	defer os.Remove(out.Name())
	out.Close()
	if err := tts.SynthesizeSpeaker(r.Context(), speechAdapter{h.host.Runner()}, text, req.Speed, file, req.Speaker, out.Name()); err != nil {
		api.WriteError(w, http.StatusBadGateway, api.CodeUnavailable, fmt.Sprintf("the voice could not speak: %v", err))
		return
	}
	data, err := os.ReadFile(out.Name())
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

// speechAdapter lets the command runner (the one that runs ffmpeg, a test seam) run Piper.
type speechAdapter struct{ r runner.CommandRunner }

func (a speechAdapter) Run(ctx context.Context, name string, args []string, stdin io.Reader, stdout io.Writer) (string, error) {
	var opts []runner.Option
	if stdin != nil {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return "", err
		}
		opts = append(opts, runner.WithStdin(data))
	}
	out, err := a.r.Run(ctx, name, args, opts...)
	if err != nil {
		return "", err
	}
	_, err = stdout.Write(out)
	return "", err
}

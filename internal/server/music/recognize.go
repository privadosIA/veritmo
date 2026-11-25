package music

// recognize.go: the ways a song or a video is recognised besides its lyrics (Whisper): LAION-CLAP, which scores the
// sound against genre, mood and instrument tags, and MuQ-MuLan, which ranks the same tags with the `diffrhythm2` executable (music_recognize_muq.go). The user picks one in the
// "Take it from a video or audio" window and on the phone. CLAP runs as the service started from the Environment screen;
// nothing here starts it. See specs/song-analysis.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/music/clap"
	"veritmo/internal/platform/musicsvc"
	"veritmo/internal/server/api"
)

// The recognition methods.
const (
	recognizeWhisper = "whisper"
	recognizeCLAP    = "clap"
	recognizeMuQ     = "muq"
)

const (
	// clapSeconds is the length of the clip sent to CLAP: it reads three 10-second windows from the start, so more is wasted.
	clapSeconds = 30
	// clapTop is how many tags per category go to the assistant and to the user.
	clapTopGenre, clapTopMood, clapTopInstrument = 3, 2, 4
)

// recognizerView is one method in GET /api/music/recognizers.
type recognizerView struct {
	ID        string `json:"id"`
	Available bool   `json:"available"`
	// Reason is why it cannot be used: not_running, no_service, no_weights... (the engine's reasons) or not_ported.
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// GET /api/music/recognizers -> {"recognizers": [{id, available, reason?, detail?}]}
//
// Whisper reads the lyrics and is always the first; the other two add what the sound is like. It starts nothing.
func (h *Handler) handleRecognizers(w http.ResponseWriter, r *http.Request) {
	out := []recognizerView{{ID: recognizeWhisper, Available: true}}
	ce, _ := h.engineOf(engineCLAP, false)
	a := ce.Availability(r.Context())
	h.overlay(engineCLAP, &a)
	out = append(out, recognizerView{ID: recognizeCLAP, Available: a.Available, Reason: a.Reason, Detail: a.Detail})
	out = append(out, h.muqAvailability())
	api.WriteJSON(w, http.StatusOK, map[string]any{"recognizers": out})
}

// hear reads the sound of a file with the method asked for ("clap" or "muq").
func (h *Handler) hear(ctx context.Context, method, file string) (clapReading, error) {
	if method == recognizeMuQ {
		return h.readMuQ(ctx, file)
	}
	return h.readSound(ctx, file)
}

// clapReading is what CLAP heard in a clip.
type clapReading struct {
	Genre      []clap.Tag `json:"genre"`
	Mood       []clap.Tag `json:"mood"`
	Instrument []clap.Tag `json:"instrument"`
}

// Tags is the reading as the words the assistant is given.
func (c clapReading) Tags() []string {
	var out []string
	for _, group := range [][]clap.Tag{c.Genre, c.Mood, c.Instrument} {
		for _, t := range group {
			out = append(out, t.Tag)
		}
	}
	return out
}

func vocabulary(id string) []string {
	for _, c := range assist.MusicTagCategories {
		if c.ID == id {
			return c.Tags
		}
	}
	return nil
}

// errClapDown is a CLAP that cannot answer, with the engine's reason.
type errClapDown struct{ reason, detail string }

func (e *errClapDown) Error() string { return e.detail }

// readSound sends the first seconds of an audio or video file to the CLAP service and returns the best tags of
// genre, mood and instrument. It never starts the service: when it is not running the error says why.
func (h *Handler) readSound(ctx context.Context, file string) (clapReading, error) {
	eng, _ := h.engineOf(engineCLAP, false)
	a := eng.Availability(ctx)
	h.overlay(engineCLAP, &a)
	if !a.Available {
		return clapReading{}, &errClapDown{reason: a.Reason, detail: a.Detail}
	}
	c, err := eng.Open(ctx)
	if err != nil {
		var ue *musicsvc.UnavailableError
		if errors.As(err, &ue) {
			return clapReading{}, &errClapDown{reason: ue.Reason, detail: ue.Detail}
		}
		return clapReading{}, err
	}
	dir, err := os.MkdirTemp("", "clap-*")
	if err != nil {
		return clapReading{}, err
	}
	defer os.RemoveAll(dir)
	wav := filepath.Join(dir, "clip.wav")
	if _, err := h.host.Runner().Run(ctx, "ffmpeg", []string{"-y", "-v", "error", "-t", fmt.Sprint(clapSeconds), "-i", file, "-vn", "-ac", "1", "-ar", "48000", wav}); err != nil {
		return clapReading{}, fmt.Errorf("cannot read this audio (does it have sound?): %s", api.LastLines(err.Error(), 3))
	}
	data, err := os.ReadFile(wav)
	if err != nil {
		return clapReading{}, err
	}
	res, err := clap.Classify(ctx, c, data, map[string][]string{
		"genre": vocabulary("genre"), "mood": vocabulary("mood"), "instrument": vocabulary("instrument"),
	}, clapTopInstrument)
	if err != nil {
		return clapReading{}, err
	}
	cut := func(tags []clap.Tag, n int) []clap.Tag {
		if len(tags) > n {
			return tags[:n]
		}
		return tags
	}
	return clapReading{Genre: cut(res.Categories["genre"], clapTopGenre), Mood: cut(res.Categories["mood"], clapTopMood), Instrument: cut(res.Categories["instrument"], clapTopInstrument)}, nil
}

// soundFacts is the line the assistant is given about what CLAP heard (empty without a reading).
func soundFacts(c clapReading) string {
	tags := c.Tags()
	if len(tags) == 0 {
		return ""
	}
	return "Sound tags (an audio-text model scored the sound against a list; closest first, only a hint): " + strings.Join(tags, ", ") + "\n"
}

// POST /api/projects/{id}/recognize {"source": "song.mp3", "method": "clap"}
//
//	-> {"method", "genre": [{tag, score}], "mood": [...], "instrument": [...], "tags": ["pop", ...]}
//
// Hears a source of the project with the method asked for. "whisper" is the lyrics' own call (/transcribe) and is a
// 400 here. "clap" needs its service running (Environment screen): 503 with the reason when it is not. "muq" needs the diffrhythm2 executable and the converted audio weights: 503 with the reason when not.
func (h *Handler) handleRecognize(w http.ResponseWriter, r *http.Request) {
	id, ok := h.ws.RequireProject(w, r)
	if !ok {
		return
	}
	var req struct {
		Source string `json:"source"`
		Method string `json:"method"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	switch req.Method {
	case recognizeCLAP, recognizeMuQ:
	default:
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, `method must be "clap" or "muq" (the lyrics have their own call, /transcribe)`)
		return
	}
	path, ok := h.songSource(w, id, req.Source)
	if !ok || !h.needFFmpeg(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	reading, err := h.hear(ctx, req.Method, path)
	if err != nil {
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
	api.WriteJSON(w, http.StatusOK, map[string]any{"method": req.Method, "genre": reading.Genre, "mood": reading.Mood, "instrument": reading.Instrument, "tags": reading.Tags()})
}

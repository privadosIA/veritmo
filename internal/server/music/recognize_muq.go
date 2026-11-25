package music

// recognize_muq.go: MuQ-MuLan as a way to hear a song. It runs the `diffrhythm2` executable published next to the
// DiffRhythm2 service (bin/services/diffrhythm2, `classify`), on the CPU, one process per song: a few seconds and no
// memory kept afterwards. The weights are the music ones (mulan_audio.safetensors, made with `diffrhythm2 convert
// --audio`). MuQ-MuLan's weights are CC-BY-NC. See specs/song-analysis.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/music/clap"
	"veritmo/internal/server/api"
)

const (
	// muqSeconds is the length of the clip MuQ-MuLan hears: six 10-second windows are enough and cost seconds.
	muqSeconds = 60
	// muqSampleRate is the only rate the audio tower reads.
	muqSampleRate = 24000
	muqAudioFile  = "mulan_audio.safetensors"
)

// muqTemplates are the sentences each tag is put in before it is embedded (the ones of the reference's muq_genres.py).
var muqTemplates = map[string]string{
	"genre": "This is a {} song.", "mood": "{} music", "instrument": "A song featuring {}.",
	"tempo": "{} tempo music", "vocal": "music with {}", "era": "A song in {} style.", "production": "A {} recording.",
}

// muqCLI is the executable, or the reason it cannot be used.
func (h *Handler) muqCLI() (string, *errClapDown) { // errClapDown: a recogniser that cannot answer, with its reason
	exe, why := h.diffRhythm2CLI()
	if why != "" {
		return "", &errClapDown{"no_service", why}
	}
	if _, err := os.Stat(filepath.Join(h.diffRhythm2WeightsDir(), muqAudioFile)); err != nil {
		return "", &errClapDown{"no_weights", "MuQ-MuLan's audio weights are not converted (diffrhythm2 convert --model models/music/diffrhythm2 --audio)"}
	}
	return exe, nil
}

// muqAvailability says whether MuQ-MuLan can be used now; it starts nothing.
func (h *Handler) muqAvailability() recognizerView {
	if _, down := h.muqCLI(); down != nil {
		return recognizerView{ID: recognizeMuQ, Reason: down.reason, Detail: down.detail}
	}
	return recognizerView{ID: recognizeMuQ, Available: true}
}

// readMuQ ranks the tags of genre, mood and instrument for the first minute of an audio or video file.
func (h *Handler) readMuQ(ctx context.Context, file string) (clapReading, error) {
	exe, down := h.muqCLI()
	if down != nil {
		return clapReading{}, down
	}
	dir, err := os.MkdirTemp("", "muq-*")
	if err != nil {
		return clapReading{}, err
	}
	defer os.RemoveAll(dir)
	wav := filepath.Join(dir, "clip.wav")
	if _, err := h.host.Runner().Run(ctx, "ffmpeg", []string{"-y", "-v", "error", "-t", fmt.Sprint(muqSeconds), "-i", file, "-vn", "-ac", "1", "-ar", fmt.Sprint(muqSampleRate), wav}); err != nil {
		return clapReading{}, fmt.Errorf("cannot read this audio (does it have sound?): %s", api.LastLines(err.Error(), 3))
	}
	vocab := map[string]any{}
	for _, c := range assist.MusicTagCategories {
		if t, ok := muqTemplates[c.ID]; ok {
			vocab[c.ID] = map[string]any{"template": t, "tags": c.Tags}
		}
	}
	vocabJSON, err := json.Marshal(vocab)
	if err != nil {
		return clapReading{}, err
	}
	vocabFile := filepath.Join(dir, "vocab.json")
	if err := os.WriteFile(vocabFile, vocabJSON, 0o600); err != nil {
		return clapReading{}, err
	}
	out, err := h.host.Runner().Run(ctx, exe, []string{"classify", "--model", h.diffRhythm2WeightsDir(), "--audio", wav,
		"--vocab", vocabFile, "--top", fmt.Sprint(clapTopInstrument), "--json"})
	if err != nil {
		return clapReading{}, fmt.Errorf("MuQ-MuLan failed: %s", api.LastLines(err.Error(), 3))
	}
	return parseMuQ(out)
}

// parseMuQ reads the JSON of `diffrhythm2 classify --json` into the same reading CLAP gives.
func parseMuQ(out []byte) (clapReading, error) {
	var res struct {
		Categories map[string][]clap.Tag `json:"categories"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return clapReading{}, fmt.Errorf("MuQ-MuLan answered something unreadable: %w", err)
	}
	cut := func(tags []clap.Tag, n int) []clap.Tag {
		if len(tags) > n {
			return tags[:n]
		}
		return tags
	}
	return clapReading{Genre: cut(res.Categories["genre"], clapTopGenre), Mood: cut(res.Categories["mood"], clapTopMood),
		Instrument: cut(res.Categories["instrument"], clapTopInstrument)}, nil
}

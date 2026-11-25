package export

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"veritmo/internal/platform/fsutil"
)

// MusicEntry is one exported song variant in the music manifest.json. With
// the models and the parameters it carries everything needed to reproduce
// the track: the effective seeds and the final caption and lyrics.
type MusicEntry struct {
	TrackID string `json:"track_id"`
	Variant int    `json:"variant"`
	// Seed feeds the synthesis; LMSeed the language model (0 and omitted
	// when the language model did not run).
	EffectiveSeed int64 `json:"effective_seed"`
	LMSeed        int64 `json:"lm_seed,omitempty"`
	UseLM         bool  `json:"use_lm"`
	// Caption is the final caption handed to the synthesis (enriched by the
	// language model when it ran). LyricsRequested is what the track asked
	// for; LyricsSung what was actually sung.
	Caption         string `json:"caption"`
	LyricsRequested string `json:"lyrics_requested,omitempty"`
	LyricsSung      string `json:"lyrics_sung,omitempty"`
	Instrumental    bool   `json:"instrumental,omitempty"`
	BPM             int    `json:"bpm,omitempty"`
	// DurationSeconds is the target length in the request; AudioSeconds the
	// length of the produced file.
	DurationSeconds float64 `json:"duration_seconds,omitempty"`
	AudioSeconds    float64 `json:"audio_seconds,omitempty"`
	KeyScale        string  `json:"keyscale,omitempty"`
	TimeSignature   string  `json:"timesignature,omitempty"`
	VocalLanguage   string  `json:"vocal_language,omitempty"`
	LMModel         string  `json:"lm_model,omitempty"`
	SynthModel      string  `json:"synth_model"`
	// Title is the title written in the tags; Cover the picture exported next to the song (empty when none).
	Title string `json:"title,omitempty"`
	Cover string `json:"cover,omitempty"`
	// LoudnessFrom and LoudnessGain record the loudness evening of defaults.music.loudness_lufs: the song's
	// integrated LUFS before it and the gain in dB that was applied (absent when none).
	LoudnessFrom *float64 `json:"loudness_from,omitempty"`
	LoudnessGain *float64 `json:"loudness_gain,omitempty"`
	// Check is what the automatic checks measured in the exported song; absent when ffmpeg could not.
	Check *MusicCheck `json:"check,omitempty"`
	// Task and Source are set when the song transforms another one.
	Task   string `json:"task,omitempty"`
	Source string `json:"source,omitempty"`
	// Reference is the timbre reference of a new song.
	Reference string `json:"reference,omitempty"`
	// Params holds every generation parameter sent to the engine.
	Params map[string]any `json:"params"`
	File   string         `json:"file"`
	// SHA256 is the content hash of File, so a mix can tell whether the song
	// it used has been regenerated since.
	SHA256 string `json:"sha256,omitempty"`
}

// MusicCheck holds the measurements of a song and the problems found in them
// (empty Issues: all good). Codes: clipping, silence, quiet, loud, duration.
type MusicCheck struct {
	PeakDB *float64     `json:"peak_db,omitempty"`
	LUFS   *float64     `json:"lufs,omitempty"`
	Issues []MusicIssue `json:"issues,omitempty"`
}

// MusicIssue is one problem found in a song.
type MusicIssue struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// MixClip is one clip of a rendered mix, as it was rendered.
type MixClip struct {
	Track     string   `json:"track,omitempty"`
	Variant   int      `json:"variant,omitempty"`
	File      string   `json:"file,omitempty"`
	Start     float64  `json:"start"`
	Gain      float64  `json:"gain"`
	Mute      bool     `json:"mute,omitempty"`
	FadeIn    float64  `json:"fade_in,omitempty"`
	FadeOut   float64  `json:"fade_out,omitempty"`
	TrimStart *float64 `json:"trim_start,omitempty"`
	TrimEnd   *float64 `json:"trim_end,omitempty"`
}

// MixEntry is one rendered mix in the music manifest.json. Hash identifies
// its description and the content of its inputs: an unchanged mix is not
// rendered again.
type MixEntry struct {
	ID           string    `json:"id"`
	File         string    `json:"file"`
	AudioSeconds float64   `json:"audio_seconds"`
	Hash         string    `json:"hash"`
	Clips        []MixClip `json:"clips"`
	// Gain and Normalize are the master settings the mix was rendered with
	// (Gain absent means 1).
	Gain      *float64 `json:"gain,omitempty"`
	Normalize bool     `json:"normalize,omitempty"`
	// LUFS is the integrated loudness of the rendered file, when it could be measured.
	LUFS *float64 `json:"lufs,omitempty"`
	// Inputs are the files the mix read and their content hashes; a song whose
	// current hash differs was regenerated after the mix was rendered.
	Inputs []MixInput `json:"inputs,omitempty"`
}

// MixInput is one file a mix read, as it was when the mix was rendered.
type MixInput struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
}

// MusicManifest is the top-level manifest.json of a music project. Mixes is
// absent for a project without mixes.
type MusicManifest struct {
	Project     string       `json:"project"`
	Mode        string       `json:"mode"`
	GeneratedAt string       `json:"generated_at"`
	Tracks      []MusicEntry `json:"tracks"`
	Mixes       []MixEntry   `json:"mixes,omitempty"`
}

// WriteMusicManifest writes m atomically.
func WriteMusicManifest(path string, m MusicManifest) error {
	return writeJSONAtomic(path, m)
}

// ReadMusicManifest reads a music manifest.json. It fails for a file that is
// missing, unreadable or not a music manifest, so callers that only want to
// reuse an earlier one can treat any error as "nothing to reuse".
func ReadMusicManifest(path string) (MusicManifest, error) {
	var m MusicManifest
	data, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("export: parse %s: %w", path, err)
	}
	if m.Mode != "music" {
		return m, fmt.Errorf("export: %s is not a music manifest (mode %q)", path, m.Mode)
	}
	return m, nil
}

// CopyFile copies src to dst, creating dst's directory.
func CopyFile(src, dst string) error {
	if err := fsutil.CopyFile(src, dst); err != nil {
		return fmt.Errorf("export: %w", err)
	}
	return nil
}

// RejectCollisions fails when two entries resolve to the same file, before
// any file is touched.
func RejectCollisions(entries []MusicEntry) error {
	seen := make(map[string]string, len(entries))
	for _, e := range entries {
		key := filepath.Clean(e.File)
		if other, dup := seen[key]; dup && other != e.TrackID {
			return fmt.Errorf("export: tracks %q and %q both resolve to output path %q", other, e.TrackID, e.File)
		}
		seen[key] = e.TrackID
	}
	return nil
}

package pipeline

// motionsong.go: a song put on a motion video (defaults.motion.song). The scenes made from parts then follow its beats,
// followed in its audio, instead of a fixed tempo: the map of the song is made once, kept in the project keyed by the
// audio, and each scene gets it cut to the span of the song where the scene sits. The song is also the soundtrack.
// See spec motion-graphics ("A song can be added to a motion video").

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"

	"veritmo/internal/music/audiomeasure"
	"veritmo/internal/music/songmap"
	"veritmo/internal/platform/events"
	"veritmo/internal/project/config"
)

// songMapMargin is how much of the song past the end of a scene its map keeps: a narrated scene lasts what its narration
// does, which is only an estimate when the page is built.
const songMapMargin = 20.0

// applyMotionSong makes the song of defaults.motion.song the soundtrack of the project at full volume when the project has
// no bgm of its own. It changes the configuration in memory only (the file is not rewritten).
func applyMotionSong(cfg *config.Config) {
	m := cfg.Defaults.Motion
	if m == nil || m.Song == "" || cfg.BGM != nil {
		return
	}
	volume, fade, ducking := 1.0, 0.0, false
	cfg.BGM = &config.BGM{File: m.Song, Volume: &volume, FadeSeconds: &fade, Ducking: &ducking}
}

// songPath is where the song of the project is.
func songPath(base, name string) string { return filepath.Join(base, config.SourcesSubdir, name) }

type songMapFile struct {
	Key string      `json:"key"`
	Map songmap.Map `json:"map"`
}

// readSongMap makes the map of the project's song, or reads the one kept for this very audio (motion/song-map.json).
func readSongMap(ctx context.Context, base, name string) (songmap.Map, string, error) {
	path := songPath(base, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return songmap.Map{}, "", fmt.Errorf("cannot read the song %s: %w", name, err)
	}
	sum := sha256.Sum256(data)
	key := hex.EncodeToString(sum[:])
	keep := filepath.Join(base, "motion", "song-map.json")
	if raw, err := os.ReadFile(keep); err == nil {
		var f songMapFile
		if json.Unmarshal(raw, &f) == nil && f.Key == key && f.Map.Version == songmap.Version {
			return f.Map, key, nil
		}
	}
	out, err := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-i", path, "-vn", "-ac", "1", "-ar", fmt.Sprint(audiomeasure.Rate), "-f", "f32le", "-").Output()
	if err != nil {
		return songmap.Map{}, "", fmt.Errorf("cannot read the song %s (does it have sound?): %w", name, err)
	}
	samples := make([]float32, len(out)/4)
	for i := range samples {
		samples[i] = math.Float32frombits(binary.LittleEndian.Uint32(out[i*4:]))
	}
	if len(samples) == 0 {
		return songmap.Map{}, "", fmt.Errorf("the song %s has no audio", name)
	}
	bpm := 0.0
	if t := audiomeasure.Measure(samples).BPM; t != nil {
		bpm = *t
	}
	m := songmap.Analyse(samples, audiomeasure.Rate, songmap.Input{BPM: bpm, AudioKey: key})
	if err := os.MkdirAll(filepath.Dir(keep), 0o755); err == nil {
		if raw, err := json.Marshal(songMapFile{Key: key, Map: m}); err == nil {
			_ = os.WriteFile(keep, raw, 0o644)
		}
	}
	return m, key, nil
}

// songSpan is where a scene sits in the song: the scenes follow one another, so it starts where the ones before it end. A
// silent scene's length is exact; a narrated one's is estimated from its words.
func songSpan(cfg *config.Config, sceneID string) (start, end float64) {
	for _, s := range cfg.Scenes {
		if s.ID == sceneID {
			return start, start + estimateSeconds(s)
		}
		start += estimateSeconds(s)
	}
	return 0, 0
}

// sliceFor is the map of the song for a scene made from a part, cut to the scene's span and shifted to the scene's own time
// (nil when the project has no song). A scene that starts after the end of the song gets nil and a warning.
func (m *motionRun) sliceFor(ctx context.Context, cfg *config.Config, scene config.Scene, base string, emitter *events.Emitter) ([]byte, float64, float64) {
	d := cfg.Defaults.Motion
	if d == nil || d.Song == "" {
		return nil, 0, 0
	}
	m.mu.Lock()
	if !m.songSet {
		m.song, m.songKey, m.songErr = readSongMap(ctx, base, d.Song)
		m.songSet = true
	}
	song, err := m.song, m.songErr
	m.mu.Unlock()
	if err != nil {
		emitter.Warn(scene.ID, fmt.Sprintf("the song cannot be read, the scene keeps a fixed tempo: %v", err))
		return nil, 0, 0
	}
	start, end := songSpan(cfg, scene.ID)
	if start >= song.Duration {
		emitter.Warn(scene.ID, fmt.Sprintf("the scene starts at %.1f s and the song lasts %.1f s: the scene keeps a fixed tempo", start, song.Duration))
		return nil, 0, 0
	}
	if !scene.IsSilent() {
		end += songMapMargin
	}
	raw, _ := json.Marshal(songmap.Slice(song, start, math.Min(end+1, song.Duration)))
	return raw, start, song.Duration
}

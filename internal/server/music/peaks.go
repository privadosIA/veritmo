package music

// peaks.go gives the editor what it needs to draw a waveform and to offer
// "trim the silences": the peak level of equal slices of an audio file and
// the range where there is sound (see specs/project-management).

import (
	"encoding/binary"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"veritmo/internal/music/beatgrid"
	"veritmo/internal/server/api"
	"veritmo/internal/server/workspace"

	"veritmo/internal/project/config"
)

const (
	peaksRate           = workspace.PeaksRate // Hz the file is decoded to (mono): plenty for a drawing
	peaksDefaultBuckets = 800
	peaksMaxBuckets     = 4000
	// peaksMaxBytes bounds the decoded audio held in memory (about 33 minutes).
	peaksMaxBytes = workspace.PeaksMaxBytes
	// silenceDB is the level (RMS over a window, dBFS) below which a window
	// counts as silent; activeWindow is that window in seconds.
	silenceDB       = -45.0
	activeWindow    = 0.1
	peaksMaxEntries = 64
)

type peaksRange struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

type peaksAnswer struct {
	Duration float64     `json:"duration"`
	Buckets  int         `json:"buckets"`
	Peaks    []float64   `json:"peaks"`
	Active   *peaksRange `json:"active"`
	// BeatOffset is where the pulse falls in the file (the second of its first
	// beat, from 0 up to one beat), when the request gave the tempo and the
	// audio has a clear pulse; null otherwise.
	BeatOffset *float64 `json:"beat_offset"`
}

// computePeaks turns decoded mono samples into the answer: the maximum
// absolute value of each of n equal slices (0..1) and the first and last
// windows louder than silenceDB.
func computePeaks(samples []float32, rate, n int) peaksAnswer {
	ans := peaksAnswer{Duration: float64(len(samples)) / float64(rate), Buckets: n, Peaks: make([]float64, n)}
	if len(samples) == 0 {
		return ans
	}
	for i := 0; i < n; i++ {
		lo, hi := i*len(samples)/n, (i+1)*len(samples)/n
		if hi <= lo {
			hi = lo + 1
			if hi > len(samples) {
				lo, hi = len(samples)-1, len(samples)
			}
		}
		var peak float32
		for _, v := range samples[lo:hi] {
			if v < 0 {
				v = -v
			}
			if v > peak {
				peak = v
			}
		}
		if peak > 1 {
			peak = 1
		}
		ans.Peaks[i] = math.Round(float64(peak)*1000) / 1000
	}
	w := int(activeWindow * float64(rate))
	if w < 1 {
		w = 1
	}
	first, last := -1, -1
	for i := 0; i+w <= len(samples); i += w {
		var sum float64
		for _, v := range samples[i : i+w] {
			sum += float64(v) * float64(v)
		}
		if 10*math.Log10(sum/float64(w)+1e-12) > silenceDB {
			if first < 0 {
				first = i
			}
			last = i + w
		}
	}
	if first >= 0 {
		ans.Active = &peaksRange{Start: math.Round(float64(first)/float64(rate)*100) / 100, End: math.Round(float64(last)/float64(rate)*100) / 100}
	}
	return ans
}

type peaksKey struct {
	path    string
	size    int64
	mtimeNS int64
	buckets int
	bpmX100 int // the tempo asked for, times 100; 0 without one
}

// peaksCache is the answers of the waveform endpoint kept per file version; it holds at most peaksCache entries.
type peaksCache struct {
	mu    sync.Mutex
	store map[peaksKey]peaksAnswer
}

func (c *peaksCache) cached(k peaksKey) (peaksAnswer, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a, ok := c.store[k]
	return a, ok
}

func (c *peaksCache) remember(k peaksKey, a peaksAnswer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.store == nil {
		c.store = map[peaksKey]peaksAnswer{}
	}
	if len(c.store) >= peaksMaxEntries {
		for old := range c.store { // drop an arbitrary entry: the cache only saves repeated work
			delete(c.store, old)
			break
		}
	}
	c.store[k] = a
}

// peaksFileOK reports whether rel names an audio file the endpoint serves:
// an mp3 or wav directly inside sources/, or one inside a folder of out/.
func peaksFileOK(rel string) bool {
	rel = filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
	parts := strings.Split(rel, "/")
	ext := strings.ToLower(filepath.Ext(rel))
	if ext != ".mp3" && ext != ".wav" {
		return false
	}
	switch parts[0] {
	case config.SourcesSubdir:
		return len(parts) == 2 && config.ValidSourceName(parts[1])
	case "out":
		return len(parts) == 3
	}
	return false
}

// GET /api/projects/{id}/peaks?file=<path>&buckets=<n>&bpm=<tempo>
func (h *Handler) handlePeaks(w http.ResponseWriter, r *http.Request) {
	id, ok := h.ws.RequireProject(w, r)
	if !ok {
		return
	}
	rel := r.URL.Query().Get("file")
	if !peaksFileOK(rel) {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "file must be an .mp3 or .wav in sources/ (sources/<name>) or in out/ (out/<project>/<name>)")
		return
	}
	buckets := peaksDefaultBuckets
	if v := r.URL.Query().Get("buckets"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > peaksMaxBuckets {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, fmt.Sprintf("buckets must be a whole number from 1 to %d", peaksMaxBuckets))
			return
		}
		buckets = n
	}
	bpm := 0.0
	if v := r.URL.Query().Get("bpm"); v != "" {
		n, err := strconv.ParseFloat(v, 64)
		if err != nil || n < 20 || n > 400 {
			api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "bpm must be a number from 20 to 400")
			return
		}
		bpm = n
	}
	full, ok := h.ws.ConfinedArtifactPath(id, rel)
	if !ok {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "invalid file path")
		return
	}
	fi, err := os.Stat(full)
	if err != nil || !fi.Mode().IsRegular() {
		api.WriteError(w, http.StatusNotFound, api.CodeNotFound, fmt.Sprintf("%q does not exist in the project", rel))
		return
	}
	key := peaksKey{path: full, size: fi.Size(), mtimeNS: fi.ModTime().UnixNano(), buckets: buckets, bpmX100: int(math.Round(bpm * 100))}
	if a, ok := h.peaks.cached(key); ok {
		api.WriteJSON(w, http.StatusOK, a)
		return
	}
	if _, err := h.host.LookPath("ffmpeg"); err != nil {
		api.WriteError(w, http.StatusServiceUnavailable, api.CodeUnavailable, "ffmpeg is not installed: it is needed to read the audio")
		return
	}
	raw, err := h.host.Runner().Run(r.Context(), "ffmpeg", []string{"-v", "error", "-i", full, "-vn", "-ac", "1", "-ar", strconv.Itoa(peaksRate), "-f", "f32le", "-"})
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, fmt.Sprintf("cannot read this audio: %s", api.LastLines(err.Error(), 3)))
		return
	}
	if len(raw) > peaksMaxBytes {
		api.WriteError(w, http.StatusUnprocessableEntity, api.CodeValidation, "the audio is too long to draw")
		return
	}
	samples := make([]float32, len(raw)/4)
	for i := range samples {
		samples[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	a := computePeaks(samples, peaksRate, buckets)
	if bpm > 0 {
		if phase, ok := beatgrid.Phase(samples, peaksRate, bpm); ok {
			a.BeatOffset = &phase
		}
	}
	h.peaks.remember(key, a)
	api.WriteJSON(w, http.StatusOK, a)
}

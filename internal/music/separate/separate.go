// Package separate takes the voice out of a song with demucs.cpp (a C++ port of Demucs v4 that runs on the CPU, where
// whisper-cli runs) and says where the voice is. The vocal stem gives Whisper a track without the drums and the synths, and
// its envelope (voice or no voice, every 20 ms) lets the alignment of the words leave out a word heard where nobody sings
// and keep a written word from being spread over an instrumental bar. See spec music-word-timing.
package separate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"veritmo/internal/platform/runner"
)

// Threads is how many threads demucs is given. Its memory grows with them, not with the length of the song, and its time
// hardly shrinks past four. Measured on 2026-11-14 (a machine of 20 threads, a song looped to 60 s and to 180 s of 44.1 kHz
// stereo, the htdemucs 4-source weights, f16):
//
//	threads   60 s of audio        peak memory
//	      2         84.6 s            3.6 GB
//	      4         49.6 s            6.2 GB
//	      8         47.4 s           12.3 GB
//
// and 180 s with 8 threads took 114 s with 11.1 GB, so the memory does not depend on the length.
const Threads = 4

// MemBytes is the memory a separation needs: the 6.2 GB measured with Threads threads, and some room.
const MemBytes = 7 << 30

// ErrNotInstalled is demucs, or its weights, missing.
var ErrNotInstalled = errors.New("the voice separator is not installed")

// Tool is the demucs CLI and the weights it reads.
type Tool struct {
	Runner runner.CommandRunner
	Binary string // demucs <model> <wav> <out dir> <threads>
	Model  string // a ggml htdemucs file
}

// Lookup finds the program (binDir/demucs, then demucs in the PATH) and the weights (the first *.bin of modelDir, an
// htdemucs 4-source model before any other) and gives the size of the weights. ErrNotInstalled, saying which is missing.
func Lookup(binDir, modelDir string) (bin, model string, size int64, err error) {
	for _, c := range []string{filepath.Join(binDir, "demucs")} {
		if fi, e := os.Stat(c); e == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			bin = c
		}
	}
	if bin == "" {
		if p, e := exec.LookPath("demucs"); e == nil {
			bin = p
		}
	}
	if bin == "" {
		return "", "", 0, fmt.Errorf("%w: run `bash scripts/get-demucs.sh --build` (the program demucs is missing)", ErrNotInstalled)
	}
	model, size, err = Model(modelDir)
	if err != nil {
		return "", "", 0, err
	}
	return bin, model, size, nil
}

// Model finds the weights in modelDir (the first *.bin, an htdemucs 4-source model before any other) and gives their size.
// ErrNotInstalled when there are none.
func Model(modelDir string) (path string, size int64, err error) {
	missing := fmt.Errorf("%w: run `bash scripts/get-demucs.sh` (the weights of demucs are missing from %s)", ErrNotInstalled, modelDir)
	entries, e := os.ReadDir(modelDir)
	if e != nil {
		return "", 0, missing
	}
	var names []string
	for _, en := range entries {
		if en.Type().IsRegular() && strings.HasSuffix(en.Name(), ".bin") {
			names = append(names, en.Name())
		}
	}
	if len(names) == 0 {
		return "", 0, missing
	}
	sort.SliceStable(names, func(i, j int) bool { return modelRank(names[i]) < modelRank(names[j]) })
	pick := filepath.Join(modelDir, names[0])
	fi, e := os.Stat(pick)
	if e != nil {
		return "", 0, missing
	}
	return pick, fi.Size(), nil
}

// modelRank orders the weights by how well they suit the job: the htdemucs of 4 sources first, then the other htdemucs, then
// the rest.
func modelRank(name string) int {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "htdemucs-4s") && !strings.Contains(n, "_ft"):
		return 0
	case strings.Contains(n, "htdemucs") && !strings.Contains(n, "6s"):
		return 1
	case strings.Contains(n, "htdemucs"):
		return 2
	}
	return 3
}

// vocalsStem is the file demucs writes for the voice: the fourth source, after the drums, the bass and the rest.
const vocalsStem = "target_3_vocals.wav"

// Run separates a 44.1 kHz stereo wav into outDir and returns the path of the vocal stem; the other stems are deleted (they
// are as big as the song and nothing uses them). The program is stopped with the context.
func (t Tool) Run(ctx context.Context, wav44k, outDir string) (string, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	if _, err := t.Runner.Run(ctx, t.Binary, []string{t.Model, wav44k, outDir, strconv.Itoa(Threads)}); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("demucs failed: %w", err)
	}
	vocals := filepath.Join(outDir, vocalsStem)
	if fi, err := os.Stat(vocals); err != nil || fi.Size() == 0 {
		return "", errors.New("demucs wrote no vocal stem")
	}
	stems, _ := filepath.Glob(filepath.Join(outDir, "target_*.wav"))
	for _, s := range stems {
		if s != vocals {
			_ = os.Remove(s)
		}
	}
	return vocals, nil
}

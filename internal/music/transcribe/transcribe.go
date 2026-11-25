// Package transcribe turns a sung or spoken audio into text with whisper.cpp's
// whisper-cli, on this machine. It keeps whisper from carrying the previous text
// into the next window (a song repeats lines, and the carried text makes the
// model repeat itself or invent) and does not use its voice-activity filter
// (measured on a sung line: it cut the first seven seconds), fixes the language for the whole file, and
// cleans what comes back: empty or note-only segments and the loops a model
// falls into over music with no singing.
package transcribe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"unicode"

	"veritmo/internal/platform/runner"
)

// Segment is one line whisper heard, in seconds.
type Segment struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

// Result is a cleaned transcription.
type Result struct {
	Language         string    `json:"language"`
	LanguageDetected bool      `json:"language_detected"`
	Text             string    `json:"text"`
	Segments         []Segment `json:"segments"`
	Dropped          int       `json:"dropped"`
	Model            string    `json:"model"`
}

// Tool is whisper-cli and the files it reads.
type Tool struct {
	Runner runner.CommandRunner
	Binary string // whisper-cli
	Model  string // ggml model
	// KeepRepeats keeps every repetition of a line: a sung line repeated beyond maxRepeat
	// is a hallucination, but in something said ("más, más, más rápido") it is what was said.
	KeepRepeats bool
}

// maxRepeat is how many identical consecutive segments are kept.
const maxRepeat = 3

// ErrNoWords is not returned by Run (an instrumental source is a valid result);
// it is here for callers that want to name that case.
var ErrNoWords = errors.New("no words were found")

// Run transcribes a 16 kHz mono wav. language is an ISO 639-1 code; empty lets
// whisper detect it once from the start of the file and keep it.
func (t Tool) Run(ctx context.Context, wav, language string) (Result, error) {
	dir, err := os.MkdirTemp("", "whisper-*")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(dir)
	prefix := filepath.Join(dir, "out")
	lang := language
	if lang == "" {
		lang = "auto"
	}
	args := []string{
		"-m", t.Model, "-f", wav,
		"-l", lang,
		"-mc", "0", // no previous text as context
		"-t", strconv.Itoa(threads()),
		"-oj", "-of", prefix, "-np",
	}
	if _, err := t.Runner.Run(ctx, t.Binary, args); err != nil {
		return Result{}, fmt.Errorf("whisper-cli failed: %w", err)
	}
	raw, err := os.ReadFile(prefix + ".json")
	if err != nil {
		return Result{}, fmt.Errorf("whisper-cli wrote no result: %w", err)
	}
	res, err := parse(raw, t.KeepRepeats)
	if err != nil {
		return Result{}, err
	}
	res.Model = filepath.Base(t.Model)
	if language != "" {
		res.Language = language
	} else {
		res.LanguageDetected = true
	}
	return res, nil
}

func threads() int {
	n := runtime.NumCPU()
	if n > 8 {
		n = 8
	}
	if n < 1 {
		n = 1
	}
	return n
}

type whisperJSON struct {
	Result struct {
		Language string `json:"language"`
	} `json:"result"`
	Transcription []struct {
		Offsets struct {
			From int64 `json:"from"`
			To   int64 `json:"to"`
		} `json:"offsets"`
		Text string `json:"text"`
	} `json:"transcription"`
}

func parse(raw []byte, keepRepeats bool) (Result, error) {
	var w whisperJSON
	if err := json.Unmarshal(raw, &w); err != nil {
		return Result{}, fmt.Errorf("cannot read whisper's result: %w", err)
	}
	segs := make([]Segment, 0, len(w.Transcription))
	for _, s := range w.Transcription {
		segs = append(segs, Segment{Start: float64(s.Offsets.From) / 1000, End: float64(s.Offsets.To) / 1000, Text: s.Text})
	}
	limit := maxRepeat
	if keepRepeats {
		limit = len(segs)
	}
	kept, dropped := clean(segs, limit)
	lines := make([]string, len(kept))
	for i, s := range kept {
		lines[i] = s.Text
	}
	return Result{Language: w.Result.Language, Text: strings.Join(lines, "\n"), Segments: kept, Dropped: dropped}, nil
}

// Clean trims the segments and drops the ones with no letters (empty, "♪", "[Music]"
// reduced to nothing) and every repetition of the same line beyond maxRepeat in a row.
// It returns what was kept and how many were dropped.
func Clean(in []Segment) ([]Segment, int) { return clean(in, maxRepeat) }

func clean(in []Segment, limit int) ([]Segment, int) {
	out := make([]Segment, 0, len(in))
	dropped, run := 0, 0
	last := ""
	for _, s := range in {
		s.Text = strings.TrimSpace(s.Text)
		if !hasLyrics(s.Text) {
			dropped++
			continue
		}
		key := normalise(s.Text)
		if key == last {
			run++
		} else {
			last, run = key, 1
		}
		if run > limit {
			dropped++
			continue
		}
		out = append(out, s)
	}
	return out, dropped
}

// hasLyrics reports whether a segment says words: it has a letter outside of
// brackets (whisper writes [Music], (music) or ♪ for what it hears but cannot spell).
func hasLyrics(s string) bool {
	depth := 0
	for _, r := range s {
		switch {
		case r == '[' || r == '(':
			depth++
		case r == ']' || r == ')':
			if depth > 0 {
				depth--
			}
		case depth == 0 && unicode.IsLetter(r):
			return true
		}
	}
	return false
}

func normalise(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

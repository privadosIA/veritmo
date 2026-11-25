package transcribe

// words.go: Whisper by word. The transcription by line (Run) says when a line is sung; this says when each word is,
// which is what a piece of music motion needs to show a word as it is sung (spec music-word-timing).
//
// A model that has a DTW preset is asked for the time of every token (whisper.cpp's dynamic time warping over the
// attention of the decoder, the timing the OpenAI reference implementation uses for word timestamps) and the words are
// built from the tokens, with the confidence of the model for each. A model with no preset, or a run that gives no token
// time, falls back to the call that cuts the output into one segment per word, which only gives the span of a slice.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Word is one word heard, in seconds.
type Word struct {
	Text  string  `json:"text"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	// Confidence is what the model was sure of the word (the geometric mean of the probabilities of its tokens, 0 to 1); 0
	// when the timing came from segments, which say nothing of it.
	Confidence float64 `json:"confidence,omitempty"`
}

// How the times of the words were found.
const (
	// TimingTokens is the time of each token by DTW: the start of a word is where its first token is.
	TimingTokens = "tokens"
	// TimingSegments is the span of the slice whisper cut the word in (a model with no DTW preset).
	TimingSegments = "segments"
)

// Heard is the words of a song and how their times were found.
type Heard struct {
	Words  []Word
	Timing string
}

// maxWordRepeat is how many times in a row the same word is kept: more is Whisper looping on a silence or a held
// note (a song may say "oh oh oh oh", but not forty times).
const maxWordRepeat = 6

// minConfidence is the confidence under which a heard word is dropped: what whisper writes over a drum fill, with a
// probability that says it does not believe it itself.
const minConfidence = 0.15

// minWordLength is the shortest a word is given when the next one starts at its own start, in seconds.
const minWordLength = 0.05

// RunWords reads a 16 kHz mono wav by word and returns the words in order, with what Whisper invents on silence
// dropped (a word with no length, a bracketed sound, a long run of the same word, a word the model does not believe).
// No previous text is carried into the next window, as in Run. language is an ISO 639-1 code; empty lets whisper detect it
// once. A song with no words gives an empty list and no error. With a model that has a DTW preset the times are those of
// the tokens (Timing is TimingTokens); otherwise, or when no token has a time, the call that cuts the output into one
// segment per word is made (TimingSegments).
func (t Tool) RunWords(ctx context.Context, wav, language string) (Heard, error) {
	if preset := dtwPreset(t.Model); preset != "" {
		raw, err := t.runWhisper(ctx, wav, language, "-nfa", "-ojf", "-dtw", preset) // DTW needs flash attention off
		if err != nil {
			return Heard{}, err
		}
		if words, ok, err := parseTokens(raw); err != nil {
			return Heard{}, err
		} else if ok {
			return Heard{Words: words, Timing: TimingTokens}, nil
		}
	}
	return t.RunWordsBySegments(ctx, wav, language)
}

// RunWordsBySegments reads the words with the call that cuts the output into one segment per word (the way the words were
// timed before the times of the tokens were used): each word has the span of its slice, which can be off by a good part of
// a second. RunWords falls back to it; the bench of the timing of the words (veritmo words bench) calls it to measure
// what the tokens improve.
func (t Tool) RunWordsBySegments(ctx context.Context, wav, language string) (Heard, error) {
	raw, err := t.runWhisper(ctx, wav, language, "-ml", "1", "-sow", "-oj") // one word per segment
	if err != nil {
		return Heard{}, err
	}
	words, err := parseWords(raw)
	if err != nil {
		return Heard{}, err
	}
	return Heard{Words: words, Timing: TimingSegments}, nil
}

// runWhisper runs whisper-cli with the common arguments and the ones given and returns the JSON it wrote.
func (t Tool) runWhisper(ctx context.Context, wav, language string, extra ...string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "whisper-words-*")
	if err != nil {
		return nil, err
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
		"-of", prefix, "-np",
	}
	args = append(args, extra...)
	if _, err := t.Runner.Run(ctx, t.Binary, args); err != nil {
		return nil, fmt.Errorf("whisper-cli failed: %w", err)
	}
	raw, err := os.ReadFile(prefix + ".json")
	if err != nil {
		return nil, fmt.Errorf("whisper-cli wrote no result: %w", err)
	}
	return raw, nil
}

// quantSuffix is what a quantised or converted model adds to its name.
var quantSuffix = regexp.MustCompile(`-(q\d+_\d+|f16|f32|tdrz)$`)

// dtwPresets are the models whisper.cpp has the alignment heads of (-dtw), by the name the file is given.
var dtwPresets = map[string]string{
	"large-v3-turbo": "large.v3.turbo", "large-v3": "large.v3", "large-v2": "large.v2", "large-v1": "large.v1",
	"medium": "medium", "medium.en": "medium.en", "small": "small", "small.en": "small.en",
	"base": "base", "base.en": "base.en", "tiny": "tiny", "tiny.en": "tiny.en",
}

// dtwPreset is the -dtw preset of a model, from its file name (ggml-large-v3-turbo-q5_0.bin is large.v3.turbo), or "" for
// a model whisper.cpp has no alignment heads for.
func dtwPreset(modelPath string) string {
	name := strings.ToLower(filepath.Base(modelPath))
	name = strings.TrimSuffix(strings.TrimPrefix(name, "ggml-"), ".bin")
	for quantSuffix.MatchString(name) {
		name = quantSuffix.ReplaceAllString(name, "")
	}
	return dtwPresets[name]
}

// tokenJSON is the full output of whisper-cli (-ojf): per segment its tokens, with their offsets, the probability of the
// model and the DTW time in centiseconds (-1 when there is none).
type tokenJSON struct {
	Transcription []struct {
		Offsets struct {
			From int64 `json:"from"`
			To   int64 `json:"to"`
		} `json:"offsets"`
		Tokens []struct {
			Text    string `json:"text"`
			Offsets struct {
				From int64 `json:"from"`
			} `json:"offsets"`
			P    float64 `json:"p"`
			TDTW int64   `json:"t_dtw"`
		} `json:"tokens"`
	} `json:"transcription"`
}

// partialWord is a word being built from tokens.
type partialWord struct {
	text    string
	start   float64
	hasTime bool
	logP    float64
	n       int
}

// parseTokens builds the words from the tokens of a full output. ok is false when no token has a DTW time (the caller falls
// back to the segments). A token starts a word when its text starts with a space or when it is the first of its segment;
// punctuation sticks to the word before it and is not timed; a special token ([_BEG_], [_TT_1234]) is not text. The start of
// a word is the DTW time of its first token, its end the start of the next word of the segment (the end of the segment for
// the last one), never before its start and never before the word before it started.
func parseTokens(raw []byte) (words []Word, ok bool, err error) {
	var j tokenJSON
	if err := json.Unmarshal(raw, &j); err != nil {
		return nil, false, fmt.Errorf("cannot read whisper's result: %w", err)
	}
	for _, s := range j.Transcription {
		for _, t := range s.Tokens {
			if t.TDTW >= 0 && !isSpecialToken(t.Text) {
				ok = true
			}
		}
	}
	if !ok {
		return nil, false, nil
	}
	words = []Word{}
	last, run, prevStart := "", 0, 0.0
	for _, s := range j.Transcription {
		segEnd := float64(s.Offsets.To) / 1000
		var built []partialWord
		for _, t := range s.Tokens {
			if isSpecialToken(t.Text) {
				continue
			}
			body := strings.TrimSpace(t.Text)
			if body == "" {
				continue
			}
			startsWord := strings.HasPrefix(t.Text, " ") || len(built) == 0
			spoken := hasWordChars(body) || strings.ContainsAny(body, "[](){}")
			if !spoken && !startsWord {
				// punctuation after a word stays with it and takes no time
				built[len(built)-1].text += body
				continue
			}
			when, timed := float64(t.Offsets.From)/1000, false
			if t.TDTW >= 0 {
				when, timed = float64(t.TDTW)/100, true
			}
			if startsWord || len(built) == 0 {
				built = append(built, partialWord{text: body, start: when, hasTime: timed})
			} else {
				cur := &built[len(built)-1]
				cur.text += body
				if !cur.hasTime && timed {
					cur.start, cur.hasTime = when, true
				}
			}
			if spoken && t.P > 0 {
				cur := &built[len(built)-1]
				cur.logP += math.Log(t.P)
				cur.n++
			}
		}
		for i, b := range built {
			if !hasWordChars(b.text) {
				continue
			}
			start := math.Max(b.start, prevStart)
			end := segEnd
			if i+1 < len(built) {
				end = math.Max(built[i+1].start, start)
			}
			if end < start+minWordLength {
				end = start + minWordLength
			}
			conf := 0.0
			if b.n > 0 {
				conf = math.Exp(b.logP / float64(b.n))
			}
			if conf < minConfidence {
				continue
			}
			key := normalise(b.text)
			if key == last {
				run++
			} else {
				last, run = key, 1
			}
			if run > maxWordRepeat {
				continue
			}
			prevStart = start
			words = append(words, Word{Text: b.text, Start: math.Round(start*1000) / 1000, End: math.Round(end*1000) / 1000, Confidence: math.Round(conf*1000) / 1000})
		}
	}
	return words, true, nil
}

// isSpecialToken says whether a token is one of whisper's markers ([_BEG_], [_TT_280], [_SOT_]...), which are not text.
func isSpecialToken(text string) bool {
	return strings.HasPrefix(text, "[_") && strings.HasSuffix(text, "]")
}

func parseWords(raw []byte) ([]Word, error) {
	var w whisperJSON
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("cannot read whisper's result: %w", err)
	}
	out := make([]Word, 0, len(w.Transcription))
	last, run := "", 0
	for _, s := range w.Transcription {
		text := strings.TrimSpace(s.Text)
		start, end := float64(s.Offsets.From)/1000, float64(s.Offsets.To)/1000
		if !hasWordChars(text) || end <= start {
			continue
		}
		key := normalise(text)
		if key == last {
			run++
		} else {
			last, run = key, 1
		}
		if run > maxWordRepeat {
			continue
		}
		out = append(out, Word{Text: text, Start: start, End: end})
	}
	return out, nil
}

// hasWordChars says whether a token is a word: a letter or a digit outside brackets (whisper writes [Music], (music) or
// ♪ for what it hears and cannot spell, and a number as digits).
func hasWordChars(s string) bool {
	depth := 0
	for _, r := range s {
		switch {
		case r == '[' || r == '(':
			depth++
		case r == ']' || r == ')':
			if depth > 0 {
				depth--
			}
		case depth == 0 && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			return true
		}
	}
	return false
}

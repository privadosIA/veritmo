package songmap

// edit.go: moving words of a map by hand (spec music-word-timing, "A word can be moved by hand"). Heard times are the best a
// machine can do, and a person who watches the verse against the audio knows where a word really falls.

import (
	"fmt"
	"math"

	"veritmo/internal/music/videoclip"
)

// WordMove moves the word at Index of Map.Words to start at Start (song seconds). End, when not nil, is where it ends; without
// it the word keeps its length.
type WordMove struct {
	Index int      `json:"index"`
	Start float64  `json:"start"`
	End   *float64 `json:"end,omitempty"`
}

// MoveError is a move that cannot be made; the map is left as it was.
type MoveError struct{ Msg string }

func (e *MoveError) Error() string { return e.Msg }

func moveErr(format string, a ...any) error { return &MoveError{Msg: fmt.Sprintf(format, a...)} }

// MoveWords is the map with the words moved as asked: the moved words say they were not heard (Heard false, Confidence 0), the
// map says its words are edited (WordsSource SourceEdited, WordsEstimated false) and, when the lyrics have tags and the
// words are the written ones, the sections start again where their first word is sung. The beats, the tempo and the loudness
// do not change, and nor does any word that was not named. A word is moved, never reordered: it must not start before the
// word that precedes it or after the one that follows it (counting the moves of the same call). The map given is not changed.
func MoveWords(m Map, lyrics string, moves []WordMove) (Map, error) {
	if len(moves) == 0 {
		return Map{}, moveErr("words must name at least one word to move")
	}
	words := append([]Word(nil), m.Words...)
	seen := map[int]bool{}
	for _, mv := range moves {
		if mv.Index < 0 || mv.Index >= len(words) {
			return Map{}, moveErr("word %d does not exist: the map has %d words (indexes 0 to %d)", mv.Index, len(words), len(words)-1)
		}
		if seen[mv.Index] {
			return Map{}, moveErr("word %d is named twice", mv.Index)
		}
		seen[mv.Index] = true
		w := &words[mv.Index]
		length := w.End - w.Start
		end := mv.Start + length
		if mv.End != nil {
			end = *mv.End
		}
		if math.IsNaN(mv.Start) || math.IsInf(mv.Start, 0) || math.IsNaN(end) || math.IsInf(end, 0) {
			return Map{}, moveErr("word %d: the times must be numbers", mv.Index)
		}
		if mv.Start < 0 || end > m.Duration+1e-9 {
			return Map{}, moveErr("word %d: %.3f to %.3f is outside the song (0 to %.3f)", mv.Index, mv.Start, end, m.Duration)
		}
		if end < mv.Start+minWord-1e-9 {
			return Map{}, moveErr("word %d: it must last at least %.2f s (it would last %.3f s)", mv.Index, minWord, end-mv.Start)
		}
		w.Start, w.End = round3(mv.Start), round3(end)
		w.Heard, w.Confidence = false, 0
	}
	for i := range words {
		if !seen[i] {
			continue
		}
		if i > 0 && words[i].Start < words[i-1].Start {
			return Map{}, moveErr("word %d would start at %.3f s, before the word that precedes it (%.3f s): words are moved, never reordered", i, words[i].Start, words[i-1].Start)
		}
		if i+1 < len(words) && words[i].Start > words[i+1].Start {
			return Map{}, moveErr("word %d would start at %.3f s, after the word that follows it (%.3f s): words are moved, never reordered", i, words[i].Start, words[i+1].Start)
		}
	}
	m.Words = words
	m.WordsSource, m.WordsEstimated = SourceEdited, false
	if secs := videoclip.ParseLyrics(lyrics); len(secs) > 0 && len(words) == len(WrittenWords(lyrics)) {
		m.Sections = sectionsFromWords(secs, words, m.Duration)
	}
	return m, nil
}

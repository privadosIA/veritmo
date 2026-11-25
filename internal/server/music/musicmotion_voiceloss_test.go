package music

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/music/songmap"
	"veritmo/internal/music/transcribe"
)

// heardAt is a whisper-cli output with the given words, each 0.5 s long from its start (seconds), timed by token. The fake
// voice sings two seconds in every four (0-2, 4-6, 8-10, 12-14 s), so a word that starts at 0.5, 4.5, 8.5 or 12.5 is on it.
func heardAt(words map[string]float64, order ...string) string {
	var segs []string
	for _, w := range order {
		from := int(words[w] * 1000)
		segs = append(segs, fmt.Sprintf(`{"offsets":{"from":%d,"to":%d},"text":" %s","tokens":[{"text":" %s","offsets":{"from":%d,"to":%d},"p":0.9,"t_dtw":%d}]}`,
			from, from+500, w, w, from, from+500, from/10))
	}
	return `{"result":{"language":"en"},"transcription":[` + strings.Join(segs, ",") + `]}`
}

var (
	allFour = heardAt(map[string]float64{"one": 0.5, "two": 4.5, "three": 8.5, "four": 12.5}, "one", "two", "three", "four")
	onlyOne = heardAt(map[string]float64{"one": 0.5}, "one")
)

func TestVoiceLoss_TheStemLosesWordsAndTheMixIsKeptWithTheStemsAttacks(t *testing.T) {
	s := mmServer(t)
	hr := withWhisper(t, s, onlyOne)
	hr.byWav = map[string]string{"audio.wav": allFour}
	withSeparator(t, s, hr, 0.5)
	v := newPiece(t, s, map[string]any{"transcribe": true, "separate": true})
	if hr.calls != 2 || filepath.Base(hr.whisperWav) != "audio.wav" {
		t.Fatalf("whisper ran %d times, last on %q: the stem and then the mix", hr.calls, hr.whisperWav)
	}
	if v.SongMap.WordsSource != songmap.SourceHeardVoice || v.SongMap.Attacks < 3 {
		t.Fatalf("source %q attacks %d: the voice was separated and gives the attacks", v.SongMap.WordsSource, v.SongMap.Attacks)
	}
	heard := 0
	for _, w := range v.SongMap.Words {
		if w.Heard {
			heard++
		}
	}
	if heard != 4 || v.SongMap.Words[1].Start < 4 || v.SongMap.Words[1].Start > 5 {
		t.Fatalf("heard %d: the words come from the mix: %+v", heard, v.SongMap.Words)
	}
	if !strings.Contains(v.WordsNote, "voice heard 1 of 4") || !strings.Contains(v.WordsNote, "the mix 4") {
		t.Fatalf("the note says both counts: %q", v.WordsNote)
	}
}

func TestVoiceLoss_TheStemHearsEnoughAndTheMixIsNotRead(t *testing.T) {
	s := mmServer(t)
	hr := withWhisper(t, s, allFour)
	hr.byWav = map[string]string{"audio.wav": onlyOne}
	withSeparator(t, s, hr, 0.5)
	v := newPiece(t, s, map[string]any{"transcribe": true, "separate": true})
	if hr.calls != 1 || filepath.Base(hr.whisperWav) != "vocals-16k.wav" || strings.Contains(v.WordsNote, "the mix") {
		t.Fatalf("whisper ran %d times on %q, note %q", hr.calls, hr.whisperWav, v.WordsNote)
	}
}

func TestVoiceLoss_TheMixHearingNoMoreKeepsTheVoice(t *testing.T) {
	s := mmServer(t)
	hr := withWhisper(t, s, onlyOne)
	hr.byWav = map[string]string{"audio.wav": onlyOne}
	withSeparator(t, s, hr, 0.5)
	v := newPiece(t, s, map[string]any{"transcribe": true, "separate": true})
	if hr.calls != 2 || strings.Contains(v.WordsNote, "the mix") {
		t.Fatalf("whisper ran %d times, note %q: both hear one word, the voice stays and nothing is said", hr.calls, v.WordsNote)
	}
}

func TestVoiceLoss_AlsoOnTheRouteThatHearsAPieceAgain(t *testing.T) {
	s := mmServer(t)
	hr := withWhisper(t, s, onlyOne)
	hr.byWav = map[string]string{"audio.wav": allFour}
	withSeparator(t, s, hr, 0.5)
	v := newPiece(t, s, nil)
	code, again, body := transcribeWith(t, s, v.ID, map[string]any{"separate": true})
	if code != 200 || !strings.Contains(again.WordsNote, "the mix 4") || again.SongMap.WordsSource != songmap.SourceHeardVoice {
		t.Fatalf("%d %q %s", code, again.WordsNote, body)
	}
}

func TestVoiceLoss_WithoutLyricsTheMixIsNotRead(t *testing.T) {
	h := &Handler{}
	out := &hearing{Heard: transcribe.Heard{Words: []transcribe.Word{{Text: "one", Start: 1, End: 1.5, Confidence: 0.9}}}}
	mix := func() (string, error) { return "", errors.New("the mix must not be read without lyrics") }
	hear := func(string) (transcribe.Heard, error) { return transcribe.Heard{}, errors.New("nor heard") }
	if err := h.hearMixIfVoiceLost(out, "", mix, hear); err != nil || out.Note != "" || len(out.Heard.Words) != 1 {
		t.Fatalf("%v %+v", err, out)
	}
}

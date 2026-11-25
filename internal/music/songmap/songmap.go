// Package songmap reads a song into the map a music motion piece is made from: its tempo and the time of each
// beat, its sections (the tags of the lyrics, with their times), each sung word with its time and its loudness
// every 0.25 s. It builds the map from what the server already measures (the tempo, the Whisper segments, the
// lyrics tags and the loudness) and does not use the network. The same inputs always give the same map. See
// specs/music-motion ("A song is read into a song map").
package songmap

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"veritmo/internal/music/beatgrid"
	"veritmo/internal/music/separate"
	"veritmo/internal/music/transcribe"
	"veritmo/internal/music/videoclip"
)

// Version is the format of a stored map; a map of another version is built again, except the one before it, which Load
// upgrades (see Load).
const Version = 2

// previousVersion is the format Load still reads: it had no words_source, and no heard or confidence on its words.
const previousVersion = 1

// Where the times of the words of a map come from (Map.WordsSource).
const (
	// SourceEstimated says the lines were spread over their sections, nobody heard them.
	SourceEstimated = "estimated"
	// SourceHeard says Whisper heard the words in the mix.
	SourceHeard = "heard"
	// SourceHeardVoice says Whisper heard the words in the separated voice.
	SourceHeardVoice = "heard-voice"
	// SourceEdited says at least one word was moved by hand.
	SourceEdited = "edited"
)

// Section is a part of the song: the tag of the lyrics ("Verse", "Chorus"; "Instrumental" when there are
// none) with its start and end in seconds.
type Section struct {
	Tag   string  `json:"tag"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// Word is a sung word with its time. Line is the number of the line (the Whisper segment) it belongs to. Heard is true when
// the word took its time from a word Whisper heard, and Confidence is that word's (0 to 1; 0 when it was not heard, or when
// the way it was timed gives none).
type Word struct {
	Text       string  `json:"text"`
	Start      float64 `json:"start"`
	End        float64 `json:"end"`
	Line       int     `json:"line"`
	Heard      bool    `json:"heard"`
	Confidence float64 `json:"confidence"`
	// Snapped says that the start of the word was moved onto something: SnappedAttack (an attack of the separated voice) or
	// SnappedBeat (a beat). Empty for a word that was left where it was heard, or that was not heard.
	Snapped string `json:"snapped,omitempty"`
}

// What a word was moved onto (Word.Snapped).
const (
	// SnappedAttack says the word starts on an attack of the separated voice.
	SnappedAttack = "attack"
	// SnappedBeat says the word starts on a beat (or a half beat in a slow song).
	SnappedBeat = "beat"
)

// Map is a song read for a piece.
type Map struct {
	Version  int     `json:"version"`
	AudioKey string  `json:"audio_key,omitempty"` // hash of the audio the map was made from
	Duration float64 `json:"duration"`
	BPM      float64 `json:"bpm"`
	// Beats are the times of the beats. When the pulse of the audio could not be found (GridAnchored false) the
	// grid starts at 0 and only its spacing is believed.
	Beats []float64 `json:"beats"`
	// BeatBase is the number of the first beat of Beats in the whole song: 0 for a whole song, more for a slice.
	BeatBase     int       `json:"beat_base"`
	GridAnchored bool      `json:"grid_anchored"`
	Sections     []Section `json:"sections"`
	Words        []Word    `json:"words"`
	// WordsEstimated is true when the words' times were spread over the sections instead of heard.
	WordsEstimated bool `json:"words_estimated,omitempty"`
	// WordsSource says where the times of the words come from: one of the Source constants; empty for a map with no words.
	WordsSource string `json:"words_source,omitempty"`
	// Attacks is how many attacks of the separated voice were found when the words were placed (0 when the voice was not separated
	// or the map was made before the attacks were looked for).
	Attacks     int       `json:"attacks"`
	LoudnessHop float64   `json:"loudness_hop"`
	Loudness    []float64 `json:"loudness"`
}

// Input is what a map is made from.
type Input struct {
	Duration float64
	BPM      float64
	// Phase is where the pulse falls (seconds from 0 up to one beat), when it is known.
	Phase *float64
	// Beats, when given, are the times of the beats as followed in the audio (beatgrid.Track); they replace the grid
	// made from BPM and Phase.
	Beats  []float64
	Lyrics string
	// Segments are the lines Whisper heard, with their times; none for an instrumental song or when the song was
	// not transcribed.
	Segments []transcribe.Segment
	// Loudness is the loudness of the song every videoclip.EnvelopeHop seconds.
	Loudness []float64
	AudioKey string
	// Heard are the words heard in the song, by word (transcribe.Tool.RunWords). With them the words of the map are the
	// written ones with the times heard (AlignWords) and the sections start where their first word is sung; without
	// written lyrics the lines are made from what was heard.
	Heard []transcribe.Word
	// HeardSource is SourceHeard (the default) or SourceHeardVoice: what the heard words were read from.
	HeardSource string
	// Voice is the envelope of the separated voice, when there is one: a heard word over no voice is left out and a word
	// with no time is placed where the voice is heard (see AlignWords).
	Voice *separate.Env
	// Attacks are the attacks of the separated voice, in seconds and in order (separate.Attacks); a heard word near one is moved
	// onto it (see snapToAttacks). None when the voice was not separated.
	Attacks []float64
	// EstimateWords says that, when no segments are given (the song was not transcribed), each lyric line is
	// given a time inside its section, in proportion to its letters, so the lines can be shown when they are
	// sung. The words of such a map are estimates (Map.WordsEstimated).
	EstimateWords bool
}

// Build makes the map. A song with no length gives an empty map with no sections.
func Build(in Input) Map {
	m := Map{Version: Version, AudioKey: in.AudioKey, Duration: in.Duration, BPM: in.BPM, LoudnessHop: videoclip.EnvelopeHop, Loudness: in.Loudness}
	if m.Loudness == nil {
		m.Loudness = []float64{}
	}
	if len(in.Beats) > 0 {
		m.Beats, m.GridAnchored = in.Beats, true
	} else {
		m.Beats, m.GridAnchored = beats(in.BPM, in.Phase, in.Duration)
	}
	if len(in.Heard) > 0 {
		m.Sections, m.Words = fromHeard(in, m)
		m.WordsEstimated = false
		m.Attacks = len(in.Attacks)
		m.WordsSource = SourceHeard
		if in.HeardSource != "" {
			m.WordsSource = in.HeardSource
		}
		return m
	}
	m.Sections = sections(in)
	segs := in.Segments
	estimated := false
	if len(segs) == 0 && in.EstimateWords {
		segs = estimateSegments(in.Lyrics, m.Sections)
		estimated = len(segs) > 0
		m.WordsEstimated = estimated
	}
	m.Words = words(segs, !estimated)
	switch {
	case estimated:
		m.WordsSource = SourceEstimated
	case len(m.Words) > 0:
		m.WordsSource = SourceHeard
	}
	return m
}

// Analyse is Build for mono samples at the given rate: it takes the length from the samples, measures the loudness
// and, when a tempo is given, finds where the pulse falls at it. The rest of the input (lyrics, segments, key) is as
// given; Duration, Loudness and Phase are filled in.
func Analyse(samples []float32, rate int, in Input) Map {
	in.Duration = float64(len(samples)) / float64(rate)
	in.Loudness = videoclip.Envelope(samples, rate)
	if in.BPM > 0 && in.Phase == nil && len(in.Beats) == 0 {
		// The tempo a song was asked for is not the one it has, and a model's songs drift: follow the pulse through the
		// whole song. When there is none to follow, a plain grid anchored where the first beats fall.
		if tr, ok := beatgrid.Track(samples, rate, in.BPM); ok {
			in.Beats, in.BPM = tr.Beats, tr.BPM
		} else if p, ok := beatgrid.Phase(samples, rate, in.BPM); ok {
			in.Phase = &p
		}
	}
	return Build(in)
}

// FromSamples is Analyse with the common inputs.
func FromSamples(samples []float32, rate int, bpm float64, lyrics string, segs []transcribe.Segment, audioKey string) Map {
	return Analyse(samples, rate, Input{BPM: bpm, Lyrics: lyrics, Segments: segs, AudioKey: audioKey})
}

// Key is the hash that tells whether a stored map belongs to an audio: the same bytes give the same key.
func Key(audio []byte) string {
	sum := sha256.Sum256(audio)
	return hex.EncodeToString(sum[:])
}

func beats(bpm float64, phase *float64, duration float64) ([]float64, bool) {
	out := []float64{}
	if bpm < 20 || bpm > 400 || duration <= 0 {
		return out, false
	}
	period := 60 / bpm
	start, anchored := 0.0, false
	if phase != nil {
		start, anchored = *phase, true
	}
	for k := 0; ; k++ {
		t := start + float64(k)*period
		if t >= duration {
			break
		}
		out = append(out, round3(t))
	}
	return out, anchored
}

func sections(in Input) []Section {
	if in.Duration <= 0 {
		return []Section{}
	}
	secs := videoclip.ParseLyrics(in.Lyrics)
	if len(secs) == 0 {
		return []Section{{Tag: "Instrumental", Start: 0, End: round3(in.Duration)}}
	}
	starts, _ := videoclip.SectionStarts(secs, in.Duration, in.Loudness)
	if w := words(in.Segments, true); len(w) >= len(secs) {
		starts = startsFromWords(secs, w, in.Duration, starts)
	}
	out := make([]Section, len(secs))
	for i, s := range secs {
		end := in.Duration
		if i+1 < len(secs) {
			end = starts[i+1]
		}
		tag := s.Tag
		if tag == "" {
			tag = "Verse"
		}
		out[i] = Section{Tag: tag, Start: round3(starts[i]), End: round3(end)}
	}
	return out
}

// words spreads each heard line over its time, in proportion to the letters of each word: Whisper gives the time
// of the line, not of its words, and a word of more letters takes longer to sing.
func words(segs []transcribe.Segment, heard bool) []Word {
	out := []Word{}
	for line, sg := range segs {
		fields := strings.Fields(sg.Text)
		var total float64
		weights := make([]float64, len(fields))
		for i, f := range fields {
			n := 0
			for _, r := range f {
				if unicode.IsLetter(r) || unicode.IsDigit(r) {
					n++
				}
			}
			weights[i] = math.Max(1, float64(n))
			total += weights[i]
		}
		if total == 0 || sg.End <= sg.Start {
			continue
		}
		at := sg.Start
		for i, f := range fields {
			end := at + (sg.End-sg.Start)*weights[i]/total
			out = append(out, Word{Text: f, Start: round3(at), End: round3(end), Line: line, Heard: heard})
			at = end
		}
	}
	return out
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// Path is where the map of a piece is stored inside its folder.
func Path(dir string) string { return filepath.Join(dir, "songmap.json") }

// Load reads the stored map of dir when it exists, is of this version and belongs to the audio key.
func Load(dir, audioKey string) (Map, bool) {
	raw, err := os.ReadFile(Path(dir))
	if err != nil {
		return Map{}, false
	}
	return Decode(raw, audioKey)
}

// Decode reads a stored map: it must be of this version (or the one before, which is upgraded) and belong to the audio key.
func Decode(raw []byte, audioKey string) (Map, bool) {
	var m Map
	if json.Unmarshal(raw, &m) != nil || (m.Version != Version && m.Version != previousVersion) || m.AudioKey != audioKey {
		return Map{}, false
	}
	if m.Version == previousVersion {
		m = upgrade(m)
	}
	return m, true
}

// Encode is the text a map is stored as.
func Encode(m Map) ([]byte, error) { return json.MarshalIndent(m, "", "  ") }

// upgrade brings a map of the previous version to this one without touching a time: its words say whether they were heard
// (they were unless the map says its words were estimated), with no confidence, and the map says where they come from.
func upgrade(m Map) Map {
	m.Version = Version
	for i := range m.Words {
		m.Words[i].Heard = !m.WordsEstimated
	}
	switch {
	case m.WordsEstimated:
		m.WordsSource = SourceEstimated
	case len(m.Words) > 0:
		m.WordsSource = SourceHeard
	}
	return m
}

// Save stores the map in dir.
func Save(dir string, m Map) error {
	raw, err := Encode(m)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(Path(dir), raw, 0o644)
}

// Cached returns the stored map of dir when it belongs to the audio key; otherwise it builds one with build,
// stores it and returns it. build is not called when the stored map is good, so the analysis does not run again.
// rebuild forces a new one.
func Cached(dir, audioKey string, rebuild bool, build func() (Map, error)) (Map, error) {
	if !rebuild {
		if m, ok := Load(dir, audioKey); ok {
			return m, nil
		}
	}
	m, err := build()
	if err != nil {
		return Map{}, err
	}
	m.AudioKey = audioKey
	if err := Save(dir, m); err != nil {
		return Map{}, err
	}
	return m, nil
}

// Slice is the part of the map from start to end seconds, shifted so that start is 0: the map a scene that
// begins at start is given, so that its page works in its own time. It keeps the beat before start (at a negative
// time) so the position inside the first beat is right, and BeatBase tells the number of the first beat kept.
// Sections are cut to the range, words and loudness are those inside it.
func Slice(m Map, start, end float64) Map {
	if end <= start {
		return Map{Version: Version, AudioKey: m.AudioKey, BPM: m.BPM, LoudnessHop: m.LoudnessHop, Beats: []float64{}, Sections: []Section{}, Words: []Word{}, Loudness: []float64{}}
	}
	out := Map{Version: Version, AudioKey: m.AudioKey, Duration: round3(end - start), BPM: m.BPM, GridAnchored: m.GridAnchored, WordsSource: m.WordsSource, Attacks: m.Attacks,
		LoudnessHop: m.LoudnessHop, Beats: []float64{}, Sections: []Section{}, Words: []Word{}, Loudness: []float64{}}
	first := -1
	for i, b := range m.Beats {
		if b > end {
			break
		}
		next := end + 1
		if i+1 < len(m.Beats) {
			next = m.Beats[i+1]
		}
		if next <= start { // wholly before the range, and not the beat just before it
			continue
		}
		if first < 0 {
			first = i
		}
		out.Beats = append(out.Beats, round3(b-start))
	}
	if first > 0 {
		out.BeatBase = first
	}
	for _, sec := range m.Sections {
		s0, e0 := math.Max(sec.Start, start), math.Min(sec.End, end)
		if e0 > s0 {
			out.Sections = append(out.Sections, Section{Tag: sec.Tag, Start: round3(s0 - start), End: round3(e0 - start)})
		}
	}
	for _, w := range m.Words {
		if w.Start >= start-1e-9 && w.Start < end {
			out.Words = append(out.Words, Word{Text: w.Text, Start: round3(w.Start - start), End: round3(math.Min(w.End, end) - start), Line: w.Line, Heard: w.Heard, Confidence: w.Confidence, Snapped: w.Snapped})
		}
	}
	if m.LoudnessHop > 0 {
		a, b := int(math.Floor(start/m.LoudnessHop)), int(math.Ceil(end/m.LoudnessHop))
		if a < 0 {
			a = 0
		}
		if b > len(m.Loudness) {
			b = len(m.Loudness)
		}
		if a < b {
			out.Loudness = append(out.Loudness, m.Loudness[a:b]...)
		}
	}
	return out
}

// startsFromWords places the start of each section where its first sung word is, instead of where the text's share
// of the song says: the lyrics' sections are mapped onto the words Whisper heard by the share of the text before
// each one, counted in words (the heard words are fewer or more than the written ones, but in the same order and proportion). A
// section with no lines (an intro, a break) starts a moment after the last word of the one before, or at 0 when
// it is first. est are the estimated starts, kept for whatever cannot be placed.
func startsFromWords(secs []videoclip.Section, w []Word, total float64, est []float64) []float64 {
	chars := make([]int, len(secs))
	sum := 0
	for i, sc := range secs {
		for _, l := range sc.Lines {
			chars[i] += len(strings.Fields(l)) // the written words: a better yardstick than letters against the heard words
		}
		sum += chars[i]
	}
	if sum == 0 {
		return est
	}
	out := make([]float64, len(secs))
	before, lastEnd := 0, 0.0
	for i := range secs {
		switch {
		case i == 0:
			out[i] = 0
		case chars[i] == 0:
			out[i] = math.Min(total, lastEnd+0.2)
		default:
			idx := int(math.Round(float64(before) / float64(sum) * float64(len(w))))
			if idx >= len(w) {
				idx = len(w) - 1
			}
			t := w[idx].Start
			if idx > 0 {
				t -= math.Min(0.4, math.Max(0, (w[idx].Start-w[idx-1].End)/2))
			}
			out[i] = math.Max(0, t)
		}
		if chars[i] > 0 {
			before += chars[i]
			idx := int(math.Round(float64(before)/float64(sum)*float64(len(w)))) - 1
			if idx < 0 {
				idx = 0
			}
			if idx >= len(w) {
				idx = len(w) - 1
			}
			lastEnd = w[idx].End
		}
		if i > 0 && out[i] <= out[i-1]+0.5 {
			if est[i] > out[i-1]+0.5 {
				out[i] = est[i]
			} else {
				out[i] = math.Min(total, out[i-1]+0.5)
			}
		}
	}
	return out
}

// estimateSegments gives each lyric line a time inside its section: the section's span (leaving a breath at its end)
// is shared among its lines in proportion to their letters. Sections with no lines have no segments.
func estimateSegments(lyrics string, secs []Section) []transcribe.Segment {
	parsed := videoclip.ParseLyrics(lyrics)
	if len(parsed) != len(secs) {
		return nil
	}
	var out []transcribe.Segment
	for i, ps := range parsed {
		total := 0
		for _, l := range ps.Lines {
			total += len([]rune(l))
		}
		if total == 0 {
			continue
		}
		span := (secs[i].End - secs[i].Start) * 0.94
		at := secs[i].Start
		for _, l := range ps.Lines {
			slot := span * float64(len([]rune(l))) / float64(total)
			out = append(out, transcribe.Segment{Start: round3(at), End: round3(at + slot*0.92), Text: l})
			at += slot
		}
	}
	return out
}

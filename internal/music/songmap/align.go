package songmap

// align.go: the words heard in a song (internal/music/transcribe, by word) aligned to the written lyrics. The user's lyrics
// stay as they are, with their words, lines and section tags; what was heard only gives the times. See spec
// music-word-timing.

import (
	"math"
	"sort"
	"strings"
	"unicode"

	"veritmo/internal/music/separate"
	"veritmo/internal/music/transcribe"
	"veritmo/internal/music/videoclip"
)

// Written is a word of the lyrics and the number of its line (over the whole lyrics, in order).
type Written struct {
	Text string
	Line int
}

// WrittenWords lists the words of the lyrics in order, each with the global number of its line: the lines of every
// section counted one after another, the same numbering a script uses to name a line. The tags ([Verse]...) are not
// words.
func WrittenWords(lyrics string) []Written {
	var out []Written
	line := 0
	for _, sec := range videoclip.ParseLyrics(lyrics) {
		for _, l := range sec.Lines {
			for _, f := range strings.Fields(l) {
				out = append(out, Written{Text: f, Line: line})
			}
			line++
		}
	}
	return out
}

// key is a word as it is compared: lower case, letters and digits only.
func key(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// same says whether a written word and a heard one are the same word: equal, or one the start of the other when it has
// at least three letters (a word Whisper cuts or joins: "quick sync" heard as "quicksync").
func same(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	short, long := a, b
	if len(short) > len(long) {
		short, long = long, short
	}
	return len([]rune(short)) >= 3 && strings.HasPrefix(long, short)
}

// Alignment costs: a match is free, a mismatch (a written word and a heard one that are not the same, as `nine` and `9`)
// costs a little less than leaving both out, so words heard another way still take their time from where they are heard.
const (
	costMatch = 0.0
	costSub   = 1.0
	costGap   = 1.0
	// subDoubt is what a mismatch costs more for every unit of confidence the heard word lacks: a word Whisper is sure of
	// (confidence 1) may stand in for a written one spelt another way at costSub, but one it is not sure of costs more than
	// leaving both out (2 gaps) from a confidence of about 0.3 down, so it does not displace the written order.
	subDoubt = 1.5
	// minVoiceShare is the share of a heard word's span where the voice must be heard for the word to count.
	minVoiceShare = 0.3
)

// sureness is the confidence a heard word counts with: 0 means unknown (a word timed by segment) and counts as sure.
func sureness(c float64) float64 {
	if c <= 0 || c > 1 {
		return 1
	}
	return c
}

// Unvoiced is how many of the heard words AlignWords leaves out because they lie where the envelope hears no voice.
func Unvoiced(heard []transcribe.Word, env *separate.Env) int {
	return len(heard) - len(onVoice(heard, env))
}

// onVoice is the heard words that lie, for at least minVoiceShare of their span, where the envelope hears a voice: a word
// Whisper writes over the drums goes. Without an envelope (nil, or one with no frames) every word stays.
func onVoice(heard []transcribe.Word, env *separate.Env) []transcribe.Word {
	if env == nil || len(env.Voiced) == 0 {
		return heard
	}
	out := make([]transcribe.Word, 0, len(heard))
	for _, h := range heard {
		if h.End <= h.Start || env.Share(h.Start, h.End) >= minVoiceShare {
			out = append(out, h)
		}
	}
	return out
}

// minWord is the shortest a word is given, in seconds, when its time is made up between its neighbours.
const minWord = 0.05

// AlignWords gives every written word a start and an end. Written words that match a heard one (or that stand in the
// place of one the voice sang another way) take its time; the others get a time between those of their nearest timed
// neighbours, shared by their letters; heard words that match nothing are left out. The words, their order and their
// line numbers are the written ones; the result is deterministic, in order and has no word that ends before it starts.
//
// With the envelope of the voice (env, nil for none) a heard word that lies mostly where no voice is heard is left out, and
// a word with no heard time is placed only inside the time where the voice is heard between its neighbours. The confidence
// of a heard word weighs its claim: see subDoubt.
func AlignWords(written []Written, heard []transcribe.Word, env *separate.Env) []Word {
	n := len(written)
	if n == 0 {
		return []Word{}
	}
	heard = onVoice(heard, env)
	m := len(heard)
	wk := make([]string, n)
	for i, w := range written {
		wk[i] = key(w.Text)
	}
	hk := make([]string, m)
	for j, h := range heard {
		hk[j] = key(h.Text)
	}
	// cost[i][j]: the cheapest way to align the first i written words with the first j heard ones.
	cost := make([][]float64, n+1)
	for i := range cost {
		cost[i] = make([]float64, m+1)
	}
	for i := 1; i <= n; i++ {
		cost[i][0] = float64(i) * costGap
	}
	for j := 1; j <= m; j++ {
		cost[0][j] = float64(j) * costGap
	}
	// step is the cost of putting the heard word j-1 in the place of the written word i-1.
	step := func(i, j int) float64 {
		if same(wk[i-1], hk[j-1]) {
			return costMatch
		}
		return costSub + subDoubt*(1-sureness(heard[j-1].Confidence))
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			best := cost[i-1][j-1] + step(i, j)
			if c := cost[i-1][j] + costGap; c < best {
				best = c
			}
			if c := cost[i][j-1] + costGap; c < best {
				best = c
			}
			cost[i][j] = best
		}
	}
	// Trace back: which heard word, if any, each written word was aligned with.
	at := make([]int, n)
	for i := range at {
		at[i] = -1
	}
	for i, j := n, m; i > 0 && j > 0; {
		switch {
		case cost[i][j] == cost[i-1][j-1]+step(i, j):
			at[i-1] = j - 1
			i, j = i-1, j-1
		case cost[i][j] == cost[i-1][j]+costGap:
			i--
		default:
			j--
		}
	}
	out := make([]Word, n)
	timed := make([]bool, n)
	for i, w := range written {
		out[i] = Word{Text: w.Text, Line: w.Line}
		if j := at[i]; j >= 0 {
			out[i].Start, out[i].End = round3(heard[j].Start), round3(heard[j].End)
			out[i].Heard, out[i].Confidence = true, round3(heard[j].Confidence)
			timed[i] = true
		}
	}
	fillGaps(out, timed, m > 0, env)
	return out
}

// fillGaps gives the words with no heard time one between the nearest timed words, in proportion to their letters. When
// nothing was timed at all the words keep no time (all zero). With an envelope of the voice, the words between two timed
// words are placed only inside the time where the voice is heard between them, when there is enough of it (a bar of
// instrumental in the gap is left empty); otherwise, and before the first and after the last timed word, as without it.
func fillGaps(out []Word, timed []bool, any bool, env *separate.Env) {
	n := len(out)
	for i := 0; i < n; {
		if timed[i] {
			i++
			continue
		}
		j := i
		for j < n && !timed[j] {
			j++
		}
		// out[i:j] have no time: they sit between the end of i-1 and the start of j.
		from, to := 0.0, 0.0
		between := i > 0 && j < n
		switch {
		case between:
			from, to = out[i-1].End, out[j].Start
		case i > 0: // after the last timed word: a little each
			from = out[i-1].End
			to = from + float64(j-i)*0.35
		case j < n: // before the first timed word
			to = out[j].Start
			from = math.Max(0, to-float64(j-i)*0.35)
		default: // nothing was heard
			from, to = 0, 0
		}
		if to-from < float64(j-i)*minWord {
			to = from + float64(j-i)*minWord
		}
		var total float64
		weights := make([]float64, j-i)
		for k := range weights {
			weights[k] = math.Max(1, float64(len([]rune(key(out[i+k].Text)))))
			total += weights[k]
		}
		var spans []span
		if between && env != nil {
			spans = voicedSpans(env, from, to)
			if sum(spans) < float64(j-i)*minWord {
				spans = nil // too little voice between them to hold the words: as without an envelope
			}
		}
		if spans == nil {
			spans = []span{{from, to}}
		}
		voiced := sum(spans)
		done := 0.0
		for k := range weights {
			share := weights[k] / total * voiced
			s0, e0 := posIn(spans, done, false), posIn(spans, done+share, true)
			out[i+k].Start, out[i+k].End = round3(s0), round3(e0)
			done += share
		}
		i = j
	}
	// In order, and none ends before it starts.
	for i := range out {
		if i > 0 && out[i].Start < out[i-1].Start {
			out[i].Start = out[i-1].Start
		}
		if out[i].End < out[i].Start+minWord && any {
			out[i].End = round3(out[i].Start + minWord)
		}
	}
}

// span is a stretch of time, in seconds.
type span struct{ from, to float64 }

func sum(spans []span) float64 {
	var t float64
	for _, s := range spans {
		t += s.to - s.from
	}
	return t
}

// voicedSpans is where the envelope hears a voice inside [from, to), as stretches in order.
func voicedSpans(env *separate.Env, from, to float64) []span {
	var out []span
	if env == nil || env.Hop <= 0 || to <= from {
		return out
	}
	first, last := int(math.Floor(from/env.Hop)), int(math.Ceil(to/env.Hop))
	if first < 0 {
		first = 0
	}
	if last > len(env.Voiced) {
		last = len(env.Voiced)
	}
	for f := first; f < last; {
		if !env.Voiced[f] {
			f++
			continue
		}
		g := f
		for g < last && env.Voiced[g] {
			g++
		}
		s, e := math.Max(from, float64(f)*env.Hop), math.Min(to, float64(g)*env.Hop)
		if e > s {
			out = append(out, span{s, e})
		}
		f = g
	}
	return out
}

// posIn is the time that lies d seconds into the stretches laid one after the other. A position that falls exactly on the
// join of two stretches is the end of the first when it ends a word (end) and the start of the second when it starts one.
func posIn(spans []span, d float64, end bool) float64 {
	for k, s := range spans {
		length := s.to - s.from
		if d < length || (end && d <= length+1e-9) || k == len(spans)-1 {
			return s.from + math.Min(math.Max(d, 0), length)
		}
		d -= length
	}
	return 0
}

// snapWindow is how far, in seconds, from a beat the start of a heard word is moved onto it.
const snapWindow = 0.06

// snapToBeats moves the start of each heard word that lies within snapWindow of a beat (of a half beat too in a song under
// 100 bpm) onto it, keeping its length, unless that would put it before the start of the word that precedes it or after the
// start of the word that follows it. A word that was not heard (its time was made up between its neighbours) is never moved:
// it has no attack to honour.
func snapToBeats(words []Word, beats []float64, bpm float64) {
	if len(beats) == 0 {
		return
	}
	grid := beats
	if bpm > 0 && bpm < 100 {
		grid = make([]float64, 0, 2*len(beats))
		for i, b := range beats {
			grid = append(grid, b)
			if i+1 < len(beats) {
				grid = append(grid, (b+beats[i+1])/2)
			}
		}
	}
	for i := range words {
		w := &words[i]
		if !w.Heard || w.Snapped != "" { // a word moved onto an attack is not moved again
			continue
		}
		best, bestD := 0.0, snapWindow+1
		for _, b := range grid {
			if d := math.Abs(b - w.Start); d < bestD {
				best, bestD = b, d
			}
			if b > w.Start+snapWindow {
				break
			}
		}
		if bestD > snapWindow || bestD == 0 {
			continue
		}
		if i > 0 && best < words[i-1].Start {
			continue
		}
		if i+1 < len(words) && best > words[i+1].Start {
			continue
		}
		length := w.End - w.Start
		w.Start, w.End = round3(best), round3(best+length)
		w.Snapped = SnappedBeat
	}
}

// attackWindow is how far, in seconds, from an attack of the voice the start of a heard word is moved onto it. 120 ms is under
// half the usual distance between two attacks (about one per 280 ms of voice), so the nearest attack is the word's own and not a
// neighbouring syllable's; the bench reports the shares within 50, 100 and 150 ms to argue for another value.
const attackWindow = 0.12

// attackGuard is how close, in seconds, a word may be moved to its neighbour's start: closer than this and the word stays.
const attackGuard = 0.04

// snapToAttacks moves the start of each heard word that lies within attackWindow of an attack of the voice onto it, keeping its
// length, unless that would put it within attackGuard of the start of the word before (as already moved) or of the word after.
// A word that was not heard (its time was made up between its neighbours) is never moved. The words moved say SnappedAttack. The
// attacks are in seconds, in order; none means nothing is done.
func snapToAttacks(words []Word, attacks []float64) {
	if len(attacks) == 0 {
		return
	}
	for i := range words {
		w := &words[i]
		if !w.Heard {
			continue
		}
		j := sort.SearchFloat64s(attacks, w.Start)
		best, bestD := 0.0, attackWindow+1
		for _, k := range []int{j - 1, j} {
			if k >= 0 && k < len(attacks) {
				if d := math.Abs(attacks[k] - w.Start); d < bestD {
					best, bestD = attacks[k], d
				}
			}
		}
		if bestD > attackWindow {
			continue
		}
		if i > 0 && best-words[i-1].Start < attackGuard {
			continue
		}
		if i+1 < len(words) && words[i+1].Start-best < attackGuard {
			continue
		}
		length := w.End - w.Start
		w.Start, w.End = round3(best), round3(best+length)
		w.Snapped = SnappedAttack
	}
}

// pauseLine is the silence, in seconds, after which a word heard with no written lyrics starts a new line.
const pauseLine = 0.8

// fromHeard makes the sections and the words of a map from the words heard. With written lyrics the words are the
// written ones with the heard times and each section starts where its first word is sung; without them the lines are
// cut at the pauses of the voice and the song is one section. The words heard are snapped to the beats of m when its grid
// was found in the audio (a grid that only has its spacing believed says nothing about where a word falls).
func fromHeard(in Input, m Map) ([]Section, []Word) {
	if in.Duration <= 0 {
		return []Section{}, []Word{}
	}
	snap := func(words []Word) {
		snapToAttacks(words, in.Attacks)
		if m.GridAnchored {
			snapToBeats(words, m.Beats, m.BPM)
		}
	}
	secs := videoclip.ParseLyrics(in.Lyrics)
	if len(secs) == 0 {
		words := linesFromHeard(onVoice(in.Heard, in.Voice))
		snap(words)
		return []Section{{Tag: "Song", Start: 0, End: round3(in.Duration)}}, words
	}
	words := AlignWords(WrittenWords(in.Lyrics), in.Heard, in.Voice)
	snap(words)
	return sectionsFromWords(secs, words, in.Duration), words
}

// linesFromHeard is the heard words as words of lines: a line ends where the voice pauses for pauseLine or more.
func linesFromHeard(heard []transcribe.Word) []Word {
	out := make([]Word, 0, len(heard))
	line := 0
	for i, h := range heard {
		if i > 0 && h.Start-heard[i-1].End >= pauseLine {
			line++
		}
		out = append(out, Word{Text: strings.TrimSpace(h.Text), Start: round3(h.Start), End: round3(h.End), Line: line, Heard: true, Confidence: round3(h.Confidence)})
	}
	return out
}

// sectionsFromWords starts each section where its first word is sung (a moment before it, never earlier than the end
// of the last word of the section before) and ends it where the next one starts. A section with no words (an intro, a
// break) starts a little after the last word of the one before, or at 0 when it is the first. The first section
// always starts at 0 and the last ends with the song.
func sectionsFromWords(secs []videoclip.Section, words []Word, duration float64) []Section {
	starts := make([]float64, len(secs))
	idx, lastEnd := 0, 0.0
	for i, sc := range secs {
		n := 0
		for _, l := range sc.Lines {
			n += len(strings.Fields(l))
		}
		switch {
		case i == 0:
			starts[i] = 0
		case n == 0:
			starts[i] = math.Min(duration, lastEnd+0.2)
		default:
			first := words[idx].Start
			starts[i] = math.Max(lastEnd, first-math.Min(0.4, math.Max(0, (first-lastEnd)/2)))
		}
		if n > 0 {
			idx += n
			lastEnd = words[idx-1].End
		}
		if i > 0 && starts[i] < starts[i-1] {
			starts[i] = starts[i-1]
		}
	}
	out := make([]Section, len(secs))
	for i, sc := range secs {
		end := duration
		if i+1 < len(secs) {
			end = starts[i+1]
		}
		tag := sc.Tag
		if tag == "" {
			tag = "Verse"
		}
		out[i] = Section{Tag: tag, Start: round3(math.Min(starts[i], duration)), End: round3(math.Min(end, duration))}
	}
	return out
}

// WithHeard is the map with its sections and words made from the words heard in the song (see fromHeard): the beats, the
// tempo and the loudness stay as they were. source says what the words were read from (SourceHeard for the mix,
// SourceHeardVoice for the separated voice) and env is the envelope of that voice, nil when there is none. attacks are the
// attacks of that voice (nil for none): a heard word near one is moved onto it before the beat is looked at (see snapToAttacks).
// An empty heard list gives the map back unchanged.
func WithHeard(m Map, lyrics string, heard []transcribe.Word, env *separate.Env, attacks []float64, source string) Map {
	if len(heard) == 0 {
		return m
	}
	m.Sections, m.Words = fromHeard(Input{Duration: m.Duration, Lyrics: lyrics, Heard: heard, Voice: env, Attacks: attacks}, m)
	m.WordsEstimated = false
	m.Attacks = len(attacks)
	m.WordsSource = SourceHeard
	if source != "" {
		m.WordsSource = source
	}
	return m
}

// MatchedCount is how many of the written words take a time from the words heard (the ones AlignWords marks Heard), with
// the envelope of the voice when there is one: the measure of how much of the lyrics a hearing recognised.
func MatchedCount(written []Written, heard []transcribe.Word, env *separate.Env) int {
	n := 0
	for _, w := range AlignWords(written, heard, env) {
		if w.Heard {
			n++
		}
	}
	return n
}

// VoiceLossRatio is the share of the written words a hearing of the separated voice has to recognise for the mix not to be heard
// as well. Measured on the ten-song bench (hear-mix-when-voice-loses-words, with this count): the song that lost words had 0.57
// of them matched on the stem (52 of 91, the mix matched 91), another 0.59 (62 of 105, the mix the same 62: unsung words, not a
// loss), the next lowest 0.78 and the rest 0.89 or more, so 0.75 sits in the gap.
const VoiceLossRatio = 0.75

// VoiceLost says whether a hearing of the voice that matched `matched` of `written` words lost enough of them to hear the mix too.
// Without written words there is nothing to count against and it is never lost.
func VoiceLost(matched, written int) bool {
	return written > 0 && float64(matched) < VoiceLossRatio*float64(written)
}

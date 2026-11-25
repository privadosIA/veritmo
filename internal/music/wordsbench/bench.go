// Package wordsbench measures how well the words of a song are timed (spec music-word-timing, "The timing is measured against
// the voice"). For each song of a music project that has written lyrics it hears the words three ways (one word per segment on the
// mix, which is how they were timed before; the time of each token on the mix; the time of each token on the separated voice, with
// and without moving the words onto the attacks of the voice) and compares each result with the attacks of the separated voice
// (where a syllable starts: about one every 280 ms of voice) and with the beats. The report keeps every word and the attacks, so
// it can be measured again without the audio (Recompute). It is a command (veritmo words bench) and not a test because it
// needs Whisper, the separator and real songs.
package wordsbench

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"veritmo/internal/music/audiomeasure"
	"veritmo/internal/music/separate"
	"veritmo/internal/music/songmap"
	"veritmo/internal/music/transcribe"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/export"
)

// The ways a song is heard, in the order they are reported.
const (
	// WaySegments is one word per segment on the mix, with no voice and no snapping: how the words were timed before.
	WaySegments = "segments-mix"
	// WayTokensMix is the time of each token on the mix.
	WayTokensMix = "tokens-mix"
	// WayTokensVoice is the time of each token on the separated voice, with its envelope, before the words are moved onto the attacks.
	WayTokensVoice = "tokens-voice"
	// WayTokensVoiceSnapped is the same words with the ones near an attack moved onto it: the shares of the voice way after snapping.
	WayTokensVoiceSnapped = "tokens-voice-snapped"
)

// Ways lists the ways in the order they are measured.
var Ways = []string{WaySegments, WayTokensMix, WayTokensVoice, WayTokensVoiceSnapped}

// Thresholds of the measures and of the decision (spec music-word-timing, "The timing is measured against the voice").
const (
	// ReachSeconds is how far from a word start an attack may be to count as the word's own, in seconds.
	ReachSeconds = 0.3
	// SpanMargin is how far before the first and after the last written word the span that is measured goes, in seconds.
	SpanMargin = 1.0
	// BeatWindow is how close to a beat a word start counts as on it, in seconds.
	BeatWindow = 0.06
	// MinSongs is how many distinct songs the decision needs.
	MinSongs = 8
	// DecisionPoints is how many points of heard words within 50 ms of an attack moving the words onto the attacks must add, on the
	// median song, for `separate: auto` to separate by default.
	DecisionPoints = 20.0
	// DecisionTimes is how many times the length of the song the whole of the voice way (separation, attacks, Whisper) may take.
	DecisionTimes = 3.0
)

// Song is a song of a music project that has written lyrics.
type Song struct {
	Project  string  `json:"project"`
	TrackID  string  `json:"track_id"`
	Variant  int     `json:"variant"`
	Title    string  `json:"title,omitempty"`
	File     string  `json:"file"`
	Lyrics   string  `json:"-"`
	Language string  `json:"language,omitempty"`
	BPM      float64 `json:"-"`
}

// Songs lists the songs with written lyrics of a music project (a folder with out/<id>/manifest.json) or of a folder of
// music projects, up to n (n <= 0 for all), in a fixed order. A song that is instrumental, has no lyrics or whose file is
// missing is left out.
func Songs(dir string, n int) ([]Song, error) {
	var manifests []string
	for _, pattern := range []string{filepath.Join(dir, "out", "*", "manifest.json"), filepath.Join(dir, "*", "out", "*", "manifest.json")} {
		found, _ := filepath.Glob(pattern)
		manifests = append(manifests, found...)
	}
	sort.Strings(manifests)
	var out []Song
	for _, mf := range manifests {
		raw, err := os.ReadFile(mf)
		if err != nil {
			continue
		}
		var m export.MusicManifest
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		root := filepath.Dir(filepath.Dir(filepath.Dir(mf))) // <project>/out/<id>/manifest.json
		for _, e := range m.Tracks {
			lyrics := e.LyricsSung
			if strings.TrimSpace(lyrics) == "" {
				lyrics = e.LyricsRequested
			}
			file := filepath.Join(root, e.File)
			if e.Instrumental || len(strings.Fields(lyrics)) < 4 {
				continue
			}
			if fi, err := os.Stat(file); err != nil || !fi.Mode().IsRegular() {
				continue
			}
			out = append(out, Song{Project: filepath.Base(root), TrackID: e.TrackID, Variant: e.Variant, Title: e.Title, File: file,
				Lyrics: lyrics, Language: e.VocalLanguage, BPM: float64(e.BPM)})
			if n > 0 && len(out) >= n {
				return out, nil
			}
		}
	}
	return out, nil
}

// Tools are the programs the bench uses. Separate is nil when the separator is not installed: then the third way and the
// onsets cannot be measured.
type Tools struct {
	Runner    runner.CommandRunner
	FFmpeg    string
	Whisper   transcribe.Tool
	Separator *separate.Tool
}

// WordRow is a word of a map as the report keeps it: enough to measure it again without the audio.
type WordRow struct {
	Index      int     `json:"index"`
	Text       string  `json:"text"`
	Start      float64 `json:"start"`
	End        float64 `json:"end"`
	Heard      bool    `json:"heard"`
	Confidence float64 `json:"confidence"`
	// OffsetMs is the signed offset from the start of the word to the nearest attack within ReachSeconds (positive: the word starts
	// before the attack); absent for a word that was not heard, lies outside the span or has no attack within reach.
	OffsetMs *float64 `json:"offset_ms,omitempty"`
	Snapped  string   `json:"snapped,omitempty"`
}

// Metrics is how well one way timed the words of one song, measured against the attacks of the voice and the beats.
type Metrics struct {
	// Words is how many written words there are, Heard how many of them took the time of a heard word, InSpan how many of those lie
	// inside the written span and WithAttack how many of those have an attack within ReachSeconds.
	Words      int `json:"words"`
	Heard      int `json:"heard"`
	InSpan     int `json:"in_span"`
	WithAttack int `json:"with_attack"`
	// MedianAbsMs is the median absolute offset, SignedMedianMs the median signed offset (the lag of Whisper: positive when the words
	// start before the attacks), in milliseconds, over the words that have an attack within reach. Absent without any.
	MedianAbsMs    *float64 `json:"median_abs_ms,omitempty"`
	SignedMedianMs *float64 `json:"signed_median_ms,omitempty"`
	// Within50, Within100 and Within150 are the shares (0 to 100) of the heard words inside the span that start within that many
	// milliseconds of an attack.
	Within50  float64 `json:"within_50_pct"`
	Within100 float64 `json:"within_100_pct"`
	Within150 float64 `json:"within_150_pct"`
	// OrphanAttacks is how many attacks inside the span have no word start (of any word) within ReachSeconds: voice that is not in
	// the lyrics (a chorus, an ad-lib) or syllables the words did not reach.
	OrphanAttacks int `json:"orphan_attacks"`
	// BeatHit is the share of word starts within BeatWindow of a beat and BeatChance the share a start placed at random would get at
	// this tempo (2 x 60 ms x bpm / 60 000): a hit rate near the chance level says nothing.
	BeatHit    float64 `json:"beat_hit_pct"`
	BeatChance float64 `json:"beat_chance_pct"`
}

// WayResult is one way of hearing a song: how long it took, what it measured and every word it gave.
type WayResult struct {
	Way string `json:"way"`
	// Timing is how the times were found: tokens or segments.
	Timing  string    `json:"timing"`
	Seconds float64   `json:"seconds"`
	Metrics Metrics   `json:"metrics"`
	Words   []WordRow `json:"words"`
}

// SongResult is the measures of one song.
type SongResult struct {
	Song
	Seconds float64 `json:"song_seconds"`
	// LyricsKey is the lyrics in lower case without punctuation or spaces, shortened: songs with the same key are variants of one
	// another and count once. SameAs names the first song with the same key when this one is a repeat.
	LyricsKey string `json:"lyrics_key"`
	SameAs    string `json:"same_as,omitempty"`
	// SeparationSeconds is what separating the voice took and AttackSeconds what finding its attacks took.
	SeparationSeconds float64 `json:"separation_seconds"`
	AttackSeconds     float64 `json:"attack_seconds"`
	VoicedShare       float64 `json:"voiced_share"`
	BPM               float64 `json:"bpm"`
	// Attacks and Beats are what the words were measured against, kept so that the report can be measured again.
	Attacks []float64   `json:"attacks"`
	Beats   []float64   `json:"beats"`
	Ways    []WayResult `json:"ways"`
	// Fallback is what the server does when the voice loses words: it hears the mix as well and keeps the better hearing. Absent
	// when the song failed.
	Fallback *Fallback `json:"fallback,omitempty"`
	Error    string    `json:"error,omitempty"`
}

// Kept values of Fallback.
const (
	// KeptNotNeeded says the voice recognised enough of the written words and the mix is not heard.
	KeptNotNeeded = "not needed"
	// KeptMix says the fallback triggers and the mix matched more written words than the voice.
	KeptMix = "mix"
	// KeptVoice says the fallback triggers and the voice matched as many as the mix or more.
	KeptVoice = "voice"
)

// Fallback is the decision of the server for one song, from the written words each hearing matched (with the envelope of the voice).
type Fallback struct {
	// Voice and Mix are the written words matched on the separated voice and on the mix, out of Written.
	Voice   int `json:"voice"`
	Mix     int `json:"mix"`
	Written int `json:"written"`
	// Kept is `not needed`, `mix` or `voice` (see the constants).
	Kept string `json:"kept"`
}

// Triggered says whether the voice lost enough words for the mix to be heard as well.
func (f *Fallback) Triggered() bool { return f != nil && f.Kept != KeptNotNeeded }

// fallbackOf decides what the server would do with these two hearings of a song with written lyrics: the same test and the same tie
// as hearMixIfVoiceLost.
func fallbackOf(lyrics string, voice, mix []transcribe.Word, env *separate.Env) *Fallback {
	written := songmap.WrittenWords(lyrics)
	f := &Fallback{Written: len(written), Kept: KeptNotNeeded}
	f.Voice = songmap.MatchedCount(written, voice, env)
	f.Mix = songmap.MatchedCount(written, mix, env)
	if songmap.VoiceLost(f.Voice, f.Written) {
		f.Kept = KeptVoice
		if f.Mix > f.Voice {
			f.Kept = KeptMix
		}
	}
	return f
}

// WayMedian is the median of every measure of one way over the songs that count.
type WayMedian struct {
	Way     string  `json:"way"`
	Timing  string  `json:"timing"`
	Seconds float64 `json:"seconds"`
	Metrics Metrics `json:"metrics"`
}

// Summary is the medians per way over the distinct songs and the decision they lead to.
type Summary struct {
	// Songs is how many songs were measured, Distinct how many of them have lyrics of their own (the ones that count).
	Songs    int         `json:"songs"`
	Distinct int         `json:"distinct"`
	Medians  []WayMedian `json:"medians"`
	// GainPoints is the median, over the distinct songs, of the share of heard words within 50 ms of an attack after the words are
	// moved onto the attacks minus the share before, in points; VoiceTimes the median of the seconds the voice way took over the length
	// of the song.
	GainPoints *float64 `json:"gain_points,omitempty"`
	VoiceTimes *float64 `json:"voice_times,omitempty"`
	// Fallbacks is how many of the distinct songs would have the mix heard as well because the voice lost words.
	Fallbacks int `json:"fallbacks"`
	// Decision is `auto` (separate by default), `false` (do not) or `not decided`.
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// Report is what the bench writes.
type Report struct {
	Machine string       `json:"machine,omitempty"`
	Date    string       `json:"date"`
	Model   string       `json:"model"`
	Notes   []string     `json:"notes,omitempty"`
	Songs   []SongResult `json:"songs"`
	Summary Summary      `json:"summary"`
}

// LyricsKey is the key under which songs with the same words count once: the lyrics in lower case, letters and digits only.
func LyricsKey(lyrics string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(lyrics) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8])
}

// Run measures the songs. A song that fails is reported with its error and the others go on. Without the separator nothing can be
// measured (the stick is the attacks of the voice): the report says so.
func Run(ctx context.Context, tools Tools, songs []Song, progress func(string)) Report {
	if progress == nil {
		progress = func(string) {}
	}
	rep := Report{Date: time.Now().Format("2006-01-02"), Model: filepath.Base(tools.Whisper.Model)}
	if tools.Separator == nil {
		rep.Notes = append(rep.Notes, "the voice separator is not installed (bash scripts/get-demucs.sh --build): the words cannot be measured against the voice, which is what they are measured against; nothing was heard")
		rep.Summary = Summary{Decision: "not decided", Reason: "the separator is not installed"}
		return rep
	}
	first := map[string]string{}
	for i, s := range songs {
		progress(fmt.Sprintf("[%d/%d] %s %s", i+1, len(songs), s.Project, s.TrackID))
		res, err := runSong(ctx, tools, s)
		if err != nil {
			res.Song = s
			res.Error = err.Error()
			progress("    failed: " + err.Error())
		}
		res.LyricsKey = LyricsKey(s.Lyrics)
		name := s.Project + "/" + s.TrackID
		if prev, ok := first[res.LyricsKey]; ok {
			res.SameAs = prev
		} else {
			first[res.LyricsKey] = name
		}
		rep.Songs = append(rep.Songs, res)
	}
	rep.Summary = Summarise(rep.Songs)
	return rep
}

// samples decodes a song to mono samples at the rate of the measures.
func samples(ctx context.Context, tools Tools, path string) ([]float32, error) {
	raw, err := tools.Runner.Run(ctx, tools.FFmpeg, []string{"-v", "error", "-i", path, "-vn", "-ac", "1", "-ar", fmt.Sprint(audiomeasure.Rate), "-f", "f32le", "-"})
	if err != nil {
		return nil, fmt.Errorf("cannot read the song: %w", err)
	}
	if len(raw) == 0 {
		return nil, errors.New("the song has no audio")
	}
	out := make([]float32, len(raw)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	return out, nil
}

func runSong(ctx context.Context, tools Tools, s Song) (SongResult, error) {
	res := SongResult{Song: s}
	dir, err := os.MkdirTemp("", "words-bench-*")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(dir)
	pcm, err := samples(ctx, tools, s.File)
	if err != nil {
		return res, err
	}
	res.Seconds = float64(len(pcm)) / float64(audiomeasure.Rate)
	bpm := s.BPM
	if bpm <= 0 { // as the server does when the song has no tempo of its own
		if m := audiomeasure.Measure(pcm); m.BPM != nil {
			bpm = *m.BPM
		}
	}
	base := songmap.Analyse(pcm, audiomeasure.Rate, songmap.Input{BPM: bpm, Lyrics: s.Lyrics})
	res.BPM, res.Beats = base.BPM, base.Beats

	mix := filepath.Join(dir, "mix16k.wav")
	if _, err := tools.Runner.Run(ctx, tools.FFmpeg, []string{"-y", "-v", "error", "-i", s.File, "-vn", "-ac", "1", "-ar", "16000", mix}); err != nil {
		return res, fmt.Errorf("cannot convert the song: %w", err)
	}

	// the stick: the voice, separated once, and its attacks
	start := time.Now()
	wav44 := filepath.Join(dir, "song44k.wav")
	if _, err := tools.Runner.Run(ctx, tools.FFmpeg, []string{"-y", "-v", "error", "-i", s.File, "-vn", "-ac", "2", "-ar", "44100", wav44}); err != nil {
		return res, fmt.Errorf("cannot convert the song: %w", err)
	}
	stem, err := tools.Separator.Run(ctx, wav44, filepath.Join(dir, "stems"))
	if err != nil {
		return res, fmt.Errorf("separating the voice: %w", err)
	}
	voice := filepath.Join(dir, "voice16k.wav")
	if _, err := tools.Runner.Run(ctx, tools.FFmpeg, []string{"-y", "-v", "error", "-i", stem, "-ac", "1", "-ar", "16000", voice}); err != nil {
		return res, fmt.Errorf("cannot convert the voice: %w", err)
	}
	res.SeparationSeconds = round(time.Since(start).Seconds(), 1)
	start = time.Now()
	env, attacks, err := separate.AnalyseFile(voice)
	if err != nil {
		return res, err
	}
	res.AttackSeconds = round(time.Since(start).Seconds(), 1)
	res.VoicedShare, res.Attacks = round(env.VoicedShare(), 3), attacks

	hear := func(name string, fn func() (transcribe.Heard, error)) (transcribe.Heard, float64, error) {
		t0 := time.Now()
		h, err := fn()
		if err != nil {
			return h, 0, fmt.Errorf("%s: %w", name, err)
		}
		return h, time.Since(t0).Seconds(), nil
	}
	heardSeg, secSeg, err := hear(WaySegments, func() (transcribe.Heard, error) { return tools.Whisper.RunWordsBySegments(ctx, mix, s.Language) })
	if err != nil {
		return res, err
	}
	heardMix, secMix, err := hear(WayTokensMix, func() (transcribe.Heard, error) { return tools.Whisper.RunWords(ctx, mix, s.Language) })
	if err != nil {
		return res, err
	}
	heardVoice, secVoice, err := hear(WayTokensVoice, func() (transcribe.Heard, error) { return tools.Whisper.RunWords(ctx, voice, s.Language) })
	if err != nil {
		return res, err
	}
	voiceSeconds := secVoice + res.SeparationSeconds + res.AttackSeconds
	add := func(way, timing string, seconds float64, m songmap.Map) {
		met, rows := Measure(m.Words, attacks, res.Beats, res.BPM)
		res.Ways = append(res.Ways, WayResult{Way: way, Timing: timing, Seconds: round(seconds, 1), Metrics: met, Words: rows})
	}
	seg := base // as before: aligned with nothing else, not snapped
	seg.Words = songmap.AlignWords(songmap.WrittenWords(s.Lyrics), heardSeg.Words, nil)
	add(WaySegments, heardSeg.Timing, secSeg, seg)
	add(WayTokensMix, heardMix.Timing, secMix, songmap.WithHeard(base, s.Lyrics, heardMix.Words, nil, nil, songmap.SourceHeard))
	add(WayTokensVoice, heardVoice.Timing, voiceSeconds, songmap.WithHeard(base, s.Lyrics, heardVoice.Words, &env, nil, songmap.SourceHeardVoice))
	add(WayTokensVoiceSnapped, heardVoice.Timing, voiceSeconds, songmap.WithHeard(base, s.Lyrics, heardVoice.Words, &env, attacks, songmap.SourceHeardVoice))
	res.Fallback = fallbackOf(s.Lyrics, heardVoice.Words, heardMix.Words, &env)
	return res, nil
}

// Measure compares the words of a map with the attacks of the voice and with the beats, and gives the rows the report keeps.
func Measure(words []songmap.Word, attacks, beats []float64, bpm float64) (Metrics, []WordRow) {
	m := Metrics{Words: len(words)}
	rows := make([]WordRow, len(words))
	for i, w := range words {
		rows[i] = WordRow{Index: i, Text: w.Text, Start: w.Start, End: w.End, Heard: w.Heard, Confidence: w.Confidence, Snapped: w.Snapped}
		if w.Heard {
			m.Heard++
		}
	}
	if len(words) == 0 {
		return m, rows
	}
	lo, hi := words[0].Start-SpanMargin, words[len(words)-1].End+SpanMargin
	var abs, signed []float64
	within := [3]int{}
	for i, w := range words {
		if !w.Heard || w.Start < lo || w.Start > hi {
			continue
		}
		m.InSpan++
		d, ok := nearestSigned(attacks, w.Start)
		if !ok || math.Abs(d) > ReachSeconds {
			continue
		}
		m.WithAttack++
		ms := round(d*1000, 1)
		rows[i].OffsetMs = &ms
		abs, signed = append(abs, math.Abs(d)), append(signed, d)
		for k, limit := range [3]float64{0.05, 0.1, 0.15} {
			if math.Abs(d) <= limit {
				within[k]++
			}
		}
	}
	if len(abs) > 0 {
		a, sg := round(1000*median(abs), 0), round(1000*median(signed), 0)
		m.MedianAbsMs, m.SignedMedianMs = &a, &sg
	}
	if m.InSpan > 0 {
		m.Within50 = round(100*float64(within[0])/float64(m.InSpan), 1)
		m.Within100 = round(100*float64(within[1])/float64(m.InSpan), 1)
		m.Within150 = round(100*float64(within[2])/float64(m.InSpan), 1)
	}
	starts := make([]float64, len(words))
	for i, w := range words {
		starts[i] = w.Start
	}
	sort.Float64s(starts)
	for _, a := range attacks {
		if a < lo || a > hi {
			continue
		}
		if d, ok := nearestSigned(starts, a); !ok || math.Abs(d) > ReachSeconds {
			m.OrphanAttacks++
		}
	}
	near := 0
	for _, w := range words {
		if d, ok := nearestSigned(beats, w.Start); ok && math.Abs(d) <= BeatWindow {
			near++
		}
	}
	m.BeatHit = round(100*float64(near)/float64(len(words)), 1)
	m.BeatChance = round(math.Min(100, 2*BeatWindow*bpm/60*100), 1)
	return m, rows
}

// nearestSigned is the signed distance from t to the closest of the sorted times (positive: that time is after t).
func nearestSigned(times []float64, t float64) (float64, bool) {
	if len(times) == 0 {
		return 0, false
	}
	i := sort.SearchFloat64s(times, t)
	best, found := math.Inf(1), false
	for _, k := range []int{i - 1, i} {
		if k >= 0 && k < len(times) {
			if d := times[k] - t; math.Abs(d) < math.Abs(best) {
				best, found = d, true
			}
		}
	}
	return best, found
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

// Recompute measures a report again from what it keeps (every word, the attacks, the beats), without the audio: the metrics of every
// way and the summary. It gives the report back unchanged when nothing in it has changed since it was written.
func Recompute(rep Report) Report {
	out := rep
	out.Songs = make([]SongResult, len(rep.Songs))
	for i, s := range rep.Songs {
		out.Songs[i] = s
		out.Songs[i].Ways = make([]WayResult, len(s.Ways))
		for j, w := range s.Ways {
			words := make([]songmap.Word, len(w.Words))
			for k, r := range w.Words {
				words[k] = songmap.Word{Text: r.Text, Start: r.Start, End: r.End, Heard: r.Heard, Confidence: r.Confidence, Snapped: r.Snapped}
			}
			met, rows := Measure(words, s.Attacks, s.Beats, s.BPM)
			out.Songs[i].Ways[j] = WayResult{Way: w.Way, Timing: w.Timing, Seconds: w.Seconds, Metrics: met, Words: rows}
		}
	}
	out.Summary = Summarise(out.Songs)
	return out
}

// Summarise takes the median of every measure per way over the songs that count (measured without error, and the first of each
// lyrics: variants of one track share their lyrics and count once) and applies the decision rule: the voice is separated by default
// if, on the median song, moving the words onto the attacks lifts the share of heard words within 50 ms of an attack by at least
// DecisionPoints points and the voice way takes under DecisionTimes the length of the song. With fewer than MinSongs distinct songs
// nothing is decided and the default does not change.
func Summarise(songs []SongResult) Summary {
	sum := Summary{Medians: []WayMedian{}}
	type col struct {
		timing                                     string
		seconds                                    []float64
		heard, inSpan, withAttack, orphans         []float64
		abs, signed, w50, w100, w150, beat, chance []float64
		words                                      []float64
	}
	cols := map[string]*col{}
	var gains, ratios []float64
	for _, s := range songs {
		if s.Error != "" {
			continue
		}
		sum.Songs++
		if s.SameAs != "" {
			continue
		}
		sum.Distinct++
		if s.Fallback.Triggered() {
			sum.Fallbacks++
		}
		var voice, snapped *WayResult
		for i := range s.Ways {
			w := &s.Ways[i]
			c := cols[w.Way]
			if c == nil {
				c = &col{timing: w.Timing}
				cols[w.Way] = c
			}
			m := w.Metrics
			c.seconds = append(c.seconds, w.Seconds)
			c.words = append(c.words, float64(m.Words))
			c.heard = append(c.heard, float64(m.Heard))
			c.inSpan = append(c.inSpan, float64(m.InSpan))
			c.withAttack = append(c.withAttack, float64(m.WithAttack))
			c.orphans = append(c.orphans, float64(m.OrphanAttacks))
			c.w50, c.w100, c.w150 = append(c.w50, m.Within50), append(c.w100, m.Within100), append(c.w150, m.Within150)
			c.beat, c.chance = append(c.beat, m.BeatHit), append(c.chance, m.BeatChance)
			if m.MedianAbsMs != nil {
				c.abs = append(c.abs, *m.MedianAbsMs)
				c.signed = append(c.signed, *m.SignedMedianMs)
			}
			switch w.Way {
			case WayTokensVoice:
				voice = w
			case WayTokensVoiceSnapped:
				snapped = w
			}
		}
		if voice != nil && snapped != nil {
			gains = append(gains, snapped.Metrics.Within50-voice.Metrics.Within50)
			if s.Seconds > 0 {
				ratios = append(ratios, snapped.Seconds/s.Seconds)
			}
		}
	}
	for _, way := range Ways {
		c := cols[way]
		if c == nil {
			continue
		}
		m := Metrics{Words: int(median(c.words)), Heard: int(median(c.heard)), InSpan: int(median(c.inSpan)), WithAttack: int(median(c.withAttack)),
			OrphanAttacks: int(median(c.orphans)), Within50: round(median(c.w50), 1), Within100: round(median(c.w100), 1), Within150: round(median(c.w150), 1),
			BeatHit: round(median(c.beat), 1), BeatChance: round(median(c.chance), 1)}
		if len(c.abs) > 0 {
			a, sg := round(median(c.abs), 0), round(median(c.signed), 0)
			m.MedianAbsMs, m.SignedMedianMs = &a, &sg
		}
		sum.Medians = append(sum.Medians, WayMedian{Way: way, Timing: c.timing, Seconds: round(median(c.seconds), 1), Metrics: m})
	}
	if len(gains) > 0 {
		g, r := round(median(gains), 1), round(median(ratios), 2)
		sum.GainPoints, sum.VoiceTimes = &g, &r
	}
	switch {
	case sum.Distinct < MinSongs:
		sum.Decision = "not decided"
		sum.Reason = fmt.Sprintf("%d distinct songs were measured (%d songs, the others share their lyrics with one of them); the rule needs %d", sum.Distinct, sum.Songs, MinSongs)
	default:
		gain, ratio := *sum.GainPoints, *sum.VoiceTimes
		if gain >= DecisionPoints && ratio < DecisionTimes {
			sum.Decision = "auto"
			sum.Reason = fmt.Sprintf("moving the words onto the attacks lifts the heard words within 50 ms of an attack by %.1f points on the median song (at least %.0f) and the voice way takes %.2f times the length of the song (under %.0f): `separate: auto` separates by default", gain, DecisionPoints, ratio, DecisionTimes)
		} else {
			sum.Decision = "false"
			sum.Reason = fmt.Sprintf("moving the words onto the attacks lifts the heard words within 50 ms of an attack by %.1f points on the median song (it needs %.0f) and the voice way takes %.2f times the length of the song (it must be under %.0f): `separate: auto` means `false`", gain, DecisionPoints, ratio, DecisionTimes)
		}
	}
	return sum
}

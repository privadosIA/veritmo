package musicmotion

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"veritmo/internal/music/audiomeasure"
	"veritmo/internal/music/beatgrid"
	"veritmo/internal/music/songmap"
	"veritmo/internal/music/transcribe"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/pipeline"
)

type realSong struct {
	File         string `json:"file"`
	Caption      string `json:"caption"`
	LyricsSung   string `json:"lyrics_sung"`
	LyricsReq    string `json:"lyrics_requested"`
	BPM          int    `json:"bpm"`
	TrackID      string `json:"track_id"`
	Variant      int    `json:"variant"`
	Instrumental bool   `json:"instrumental"`
}

func loadRealSong(t *testing.T, root, project, track string, variant int) (realSong, string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, project, "out", project, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Tracks []realSong `json:"tracks"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, s := range m.Tracks {
		if s.TrackID == track && s.Variant == variant {
			return s, filepath.Join(root, project, filepath.FromSlash(s.File))
		}
	}
	t.Fatalf("no track %s v%d in %s", track, variant, project)
	return realSong{}, ""
}

func decodeSamples(t *testing.T, path string) []float32 {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-v", "error", "-i", path, "-vn", "-ac", "1", "-ar", fmt.Sprint(audiomeasure.Rate), "-f", "f32le", "-").Output()
	if err != nil {
		t.Fatal(err)
	}
	s := make([]float32, len(out)/4)
	for i := range s {
		s[i] = math.Float32frombits(binary.LittleEndian.Uint32(out[i*4:]))
	}
	return s
}

// With REAL_PROJECTS_DIR set (the projects folder), reads the beat grid of three real songs and says how it came out.
func TestReportTheBeatGridOfRealSongs(t *testing.T) {
	root := os.Getenv("REAL_PROJECTS_DIR")
	if root == "" {
		t.Skip("set REAL_PROJECTS_DIR to the projects folder")
	}
	for _, c := range []struct {
		project, track string
		variant        int
	}{{"cyberpunk", "track_1", 1}, {"demo-indie", "reunion", 0}, {"blues", "track_1", 0}} {
		s, path := loadRealSong(t, root, c.project, c.track, c.variant)
		samples := decodeSamples(t, path)
		bpm := float64(s.BPM)
		measured := audiomeasure.Measure(samples)
		src := "manifest"
		if bpm <= 0 && measured.BPM != nil {
			bpm, src = *measured.BPM, "measured"
		}
		m := songmap.Analyse(samples, audiomeasure.Rate, songmap.Input{BPM: bpm})
		src += fmt.Sprintf("; followed in the song: %.1f", m.BPM)
		first := math.NaN()
		if len(m.Beats) > 0 {
			first = m.Beats[0]
		}
		mb := "none"
		if measured.BPM != nil {
			mb = fmt.Sprintf("%.1f", *measured.BPM)
		}
		t.Logf("%-12s %6.1f s  bpm %.0f (%s; the measure says %s)  grid anchored: %v  first beat %.3f s  %d beats", c.project, m.Duration, bpm, src, mb, m.GridAnchored, first, len(m.Beats))
	}
}

// With REAL_PROJECTS_DIR and REAL_OUT_DIR set, draws a real song in the three styles (simple, advanced, illustrated)
// and copies the videos to REAL_OUT_DIR with a note of the time each took.
func TestDrawARealSongInEveryStyle(t *testing.T) {
	root, out := os.Getenv("REAL_PROJECTS_DIR"), os.Getenv("REAL_OUT_DIR")
	if root == "" || out == "" {
		t.Skip("set REAL_PROJECTS_DIR and REAL_OUT_DIR")
	}
	needTools(t)
	s, path := loadRealSong(t, root, "demo-indie", "reunion", 0)
	samples := decodeSamples(t, path)
	lyrics := s.LyricsSung
	if strings.TrimSpace(lyrics) == "" {
		lyrics = s.LyricsReq
	}
	m := songmap.Analyse(samples, audiomeasure.Rate, songmap.Input{BPM: float64(s.BPM), Lyrics: lyrics, EstimateWords: true})
	tags := []string{}
	for _, sec := range m.Sections {
		tags = append(tags, sec.Tag)
	}
	d := Fallback(s.Caption, tags)
	cat, library := Catalog{Builtin: BuiltinParts()}, lib()
	var notes strings.Builder
	fmt.Fprintf(&notes, "song: demo-indie/reunion v1, %.0f s, %d beats at %d bpm, %d sections, %d words (times estimated: %v)\n", m.Duration, len(m.Beats), s.BPM, len(m.Sections), len(m.Words), m.WordsEstimated)
	for _, c := range []struct{ name, level, style string }{{"simple", LevelSimple, StyleTypographic}, {"avanzado", LevelAdvanced, StyleTypographic}, {"ilustrado", LevelSimple, StyleIllustrated}} {
		fps, _ := ResolveFPS(c.style, 0)
		p, err := ProposeScript(context.Background(), nil, ScriptInput{Map: m, Design: d, Level: c.level, Cat: cat})
		if err != nil {
			t.Fatal(err)
		}
		if c.style == StyleIllustrated {
			AddStages(&p.Script, library, d)
		}
		dir := filepath.Join(t.TempDir(), c.name)
		spec := ProjectSpec{Dir: dir, Name: "real-" + c.name, SongFile: path, Map: m, Design: d, Script: p.Script, Cat: cat, Lib: library, FPS: fps, Width: 1280, Height: 720}
		if err := p.Script.ValidateAll(cat, library, m, d, 1280, 720); err != nil {
			t.Fatal(err)
		}
		forecast := EstimateRender(p.Script, cat, fps, 1280, 720, nil)
		if _, err := WriteProject(spec); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadParts(filepath.Join(dir, "scenes.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		var log strings.Builder
		if _, err := pipeline.Run(context.Background(), cfg, pipeline.Options{MotionParts: Parts{}, Dir: dir}, events.New(&log, true)); err != nil {
			t.Fatalf("%v\n%s", err, log.String())
		}
		took := time.Since(start)
		data, err := os.ReadFile(filepath.Join(dir, "out", "real-"+c.name+".mp4"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, "cancion-real-"+c.name+".mp4"), data, 0o644); err != nil {
			t.Fatal(err)
		}
		line := fmt.Sprintf("%-10s %d piezas, %d fps: tardó %.0f s (previsión %.0f s)\n", c.name, len(p.Script.Pieces), fps, took.Seconds(), forecast.Seconds)
		notes.WriteString(line)
		t.Log(strings.TrimSpace(line))
	}
	if err := os.WriteFile(filepath.Join(out, "notas.txt"), []byte(notes.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// onsetOf is the rise in loudness every 10 ms.
func onsetOf(samples []float32, rate int) []float64 {
	hop, win := rate/100, rate/50
	n := (len(samples) - win) / hop
	e := make([]float64, n)
	for i := range e {
		var sum float64
		for _, v := range samples[i*hop : i*hop+win] {
			sum += float64(v) * float64(v)
		}
		e[i] = math.Sqrt(sum / float64(win))
	}
	o := make([]float64, n)
	for i := 1; i < n; i++ {
		if d := e[i] - e[i-1]; d > 0 {
			o[i] = d
		}
	}
	return o
}

// contrast is how much stronger the pulse is on the beats than half a beat after them, over the beats in [from, to):
// above 1 the beats sit on the pulse, near 1 they sit nowhere in particular.
func contrast(o []float64, beats []float64, from, to float64) float64 {
	at := func(t float64) float64 {
		i := int(math.Round(t * 100))
		var m float64
		for k := i - 2; k <= i+2; k++ {
			if k >= 0 && k < len(o) && o[k] > m {
				m = o[k]
			}
		}
		return m
	}
	var on, off float64
	var n int
	for i := 0; i+1 < len(beats); i++ {
		if beats[i] < from || beats[i] >= to {
			continue
		}
		on += at(beats[i] + 0.01)
		off += at((beats[i]+beats[i+1])/2 + 0.01)
		n++
	}
	if n == 0 || off == 0 {
		return 0
	}
	return on / off
}

// With REAL_PROJECTS_DIR set, compares how well a plain grid and the tracked beats sit on the pulse in each third of
// three real songs (the on-beat contrast: the pulse on the beats against the pulse between them).
func TestReportTheDriftOfTheGridOfRealSongs(t *testing.T) {
	root := os.Getenv("REAL_PROJECTS_DIR")
	if root == "" {
		t.Skip("set REAL_PROJECTS_DIR to the projects folder")
	}
	for _, c := range []struct {
		project, track string
		variant        int
	}{{"cyberpunk", "track_1", 1}, {"demo-indie", "reunion", 0}, {"blues", "track_1", 0}} {
		s, path := loadRealSong(t, root, c.project, c.track, c.variant)
		samples := decodeSamples(t, path)
		bpm := float64(s.BPM)
		if bpm <= 0 {
			bpm = *audiomeasure.Measure(samples).BPM
		}
		o := onsetOf(samples, audiomeasure.Rate)
		tracked := songmap.Analyse(samples, audiomeasure.Rate, songmap.Input{BPM: bpm})
		ph, _ := beatgridPhase(samples[:min(len(samples), 30*audiomeasure.Rate)], audiomeasure.Rate, bpm)
		var plain []float64
		for k := 0; ph+float64(k)*60/bpm < tracked.Duration; k++ {
			plain = append(plain, ph+float64(k)*60/bpm)
		}
		third := tracked.Duration / 3
		var line string
		for i := 0; i < 3; i++ {
			line += fmt.Sprintf("  third %d: plain %.2f, tracked %.2f", i+1, contrast(o, plain, float64(i)*third, float64(i+1)*third), contrast(o, tracked.Beats, float64(i)*third, float64(i+1)*third))
		}
		t.Logf("%-10s%s", c.project, line)
	}
}

var beatgridPhase = beatgrid.Phase

// With REAL_PROJECTS_DIR, REAL_OUT_DIR and REAL_WHISPER (the whisper-cli) set, hears the words of a real song, draws a
// piece with the heard times next to one with the estimated times (same beats, same design) and writes the videos
// and a note of how far apart the two timings are.
func TestDrawARealSongWithHeardWordsAndWithEstimatedOnes(t *testing.T) {
	root, out, whisper := os.Getenv("REAL_PROJECTS_DIR"), os.Getenv("REAL_OUT_DIR"), os.Getenv("REAL_WHISPER")
	if root == "" || out == "" || whisper == "" {
		t.Skip("set REAL_PROJECTS_DIR, REAL_OUT_DIR and REAL_WHISPER")
	}
	needTools(t)
	s, path := loadRealSong(t, root, "demo-indie", "reunion", 0)
	samples := decodeSamples(t, path)
	lyrics := s.LyricsSung
	if strings.TrimSpace(lyrics) == "" {
		lyrics = s.LyricsReq
	}
	tmp := t.TempDir()
	wav := filepath.Join(tmp, "a.wav")
	if b, err := exec.Command("ffmpeg", "-v", "error", "-y", "-i", path, "-vn", "-ac", "1", "-ar", "16000", wav).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, b)
	}
	started := time.Now()
	heard, err := (transcribe.Tool{Runner: runner.NewExecRunner(), Binary: whisper, Model: filepath.Join(root, "..", "models", "whisper", "ggml-large-v3-turbo-q5_0.bin")}).RunWords(context.Background(), wav, "en")
	if err != nil {
		t.Fatal(err)
	}
	heardTook := time.Since(started)
	bpm := float64(s.BPM)
	estimated := songmap.Analyse(samples, audiomeasure.Rate, songmap.Input{BPM: bpm, Lyrics: lyrics, EstimateWords: true})
	real := songmap.Analyse(samples, audiomeasure.Rate, songmap.Input{BPM: bpm, Lyrics: lyrics, Heard: heard.Words})
	var diff, worst float64
	for i := range real.Words {
		d := math.Abs(real.Words[i].Start - estimated.Words[i].Start)
		diff += d
		worst = math.Max(worst, d)
	}
	var notes strings.Builder
	fmt.Fprintf(&notes, "song: demo-indie/reunion v1, %.0f s\nhearing the words: %d words heard in %.0f s of CPU, %d written words\n", real.Duration, len(heard.Words), heardTook.Seconds(), len(real.Words))
	fmt.Fprintf(&notes, "estimated against heard: the start of a word is %.1f s off on average and %.1f s at worst\n", diff/float64(len(real.Words)), worst)
	cat, library := Catalog{Builtin: BuiltinParts()}, lib()
	for _, c := range []struct {
		name string
		m    songmap.Map
	}{{"palabras-estimadas", estimated}, {"palabras-oidas", real}} {
		tags := []string{}
		for _, sec := range c.m.Sections {
			tags = append(tags, sec.Tag)
		}
		d := Fallback(s.Caption, tags)
		p, err := ProposeScript(context.Background(), nil, ScriptInput{Map: c.m, Design: d, Level: LevelSimple, Cat: cat})
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(tmp, c.name)
		spec := ProjectSpec{Dir: dir, Name: c.name, SongFile: path, Map: c.m, Design: d, Script: p.Script, Cat: cat, Lib: library, FPS: 30, Width: 1280, Height: 720}
		if _, err := WriteProject(spec); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadParts(filepath.Join(dir, "scenes.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		var log strings.Builder
		if _, err := pipeline.Run(context.Background(), cfg, pipeline.Options{MotionParts: Parts{}, Dir: dir}, events.New(&log, true)); err != nil {
			t.Fatalf("%v\n%s", err, log.String())
		}
		data, err := os.ReadFile(filepath.Join(dir, "out", c.name+".mp4"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, "cancion-real-"+c.name+".mp4"), data, 0o644); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&notes, "%s: %d piezas, dibujada en %.0f s\n", c.name, len(p.Script.Pieces), time.Since(start).Seconds())
	}
	if err := os.WriteFile(filepath.Join(out, "notas-palabras.txt"), []byte(notes.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Log(notes.String())
}

// With REAL_PROJECTS_DIR, REAL_OUT_DIR and REAL_WHISPER set, hears the words of a real song and draws a piece whose design
// marks the word being sung (highlight) and shows the word hits in metal, with the whole catalog of simple parts, so
// the interface parts show up.
func TestDrawARealSongWithTheNewPartsAndTextFinishes(t *testing.T) {
	root, out, whisper := os.Getenv("REAL_PROJECTS_DIR"), os.Getenv("REAL_OUT_DIR"), os.Getenv("REAL_WHISPER")
	if root == "" || out == "" || whisper == "" {
		t.Skip("set REAL_PROJECTS_DIR, REAL_OUT_DIR and REAL_WHISPER")
	}
	needTools(t)
	s, path := loadRealSong(t, root, "demo-indie", "reunion", 0)
	samples := decodeSamples(t, path)
	lyrics := s.LyricsSung
	if strings.TrimSpace(lyrics) == "" {
		lyrics = s.LyricsReq
	}
	tmp := t.TempDir()
	wav := filepath.Join(tmp, "a.wav")
	if b, err := exec.Command("ffmpeg", "-v", "error", "-y", "-i", path, "-vn", "-ac", "1", "-ar", "16000", wav).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, b)
	}
	heard, err := (transcribe.Tool{Runner: runner.NewExecRunner(), Binary: whisper, Model: filepath.Join(root, "..", "models", "whisper", "ggml-large-v3-turbo-q5_0.bin")}).RunWords(context.Background(), wav, "en")
	if err != nil {
		t.Fatal(err)
	}
	m := songmap.Analyse(samples, audiomeasure.Rate, songmap.Input{BPM: float64(s.BPM), Lyrics: lyrics, Heard: heard.Words})
	tags := []string{}
	for _, sec := range m.Sections {
		tags = append(tags, sec.Tag)
	}
	d := Fallback("indie pop, theatrical, upbeat", tags)
	d.VerseStyle, d.HitStyle = VerseStyleHighlight, HitStyleMetal
	cat, library := Catalog{Builtin: BuiltinParts()}, lib()
	p, err := ProposeScript(context.Background(), nil, ScriptInput{Map: m, Design: d, Level: LevelSimple, Cat: cat})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(tmp, "piece")
	spec := ProjectSpec{Dir: dir, Name: "partes-nuevas", SongFile: path, Map: m, Design: d, Script: p.Script, Cat: cat, Lib: library, FPS: 30, Width: 1280, Height: 720}
	if _, err := WriteProject(spec); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadParts(filepath.Join(dir, "scenes.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	if _, err := pipeline.Run(context.Background(), cfg, pipeline.Options{MotionParts: Parts{}, Dir: dir}, events.New(&log, true)); err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, "out", "partes-nuevas.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "cancion-real-partes-nuevas.mp4"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	used := map[string]int{}
	for _, pc := range p.Script.Pieces {
		used[pc.Part]++
	}
	t.Logf("%d pieces, parts used: %v", len(p.Script.Pieces), used)
}

// TestDrawARealSongThroughSeveralEras draws a real song as a piece that goes through five eras (green terminal, amber
// terminal, synthwave, warped mesh, nebula) with a timeline strip over all of them and a palette per piece.
func TestDrawARealSongThroughSeveralEras(t *testing.T) {
	root, out := os.Getenv("REAL_PROJECTS_DIR"), os.Getenv("REAL_OUT_DIR")
	if root == "" || out == "" {
		t.Skip("set REAL_PROJECTS_DIR and REAL_OUT_DIR")
	}
	needTools(t)
	s, path := loadRealSong(t, root, "demo-indie", "reunion", 0)
	samples := decodeSamples(t, path)
	lyrics := s.LyricsSung
	if strings.TrimSpace(lyrics) == "" {
		lyrics = s.LyricsReq
	}
	m := songmap.Analyse(samples, audiomeasure.Rate, songmap.Input{BPM: float64(s.BPM), Lyrics: lyrics})
	tags := []string{}
	for _, sec := range m.Sections {
		tags = append(tags, sec.Tag)
	}
	d := Fallback("indie pop, theatrical, upbeat", tags)
	cat, library := Catalog{Builtin: BuiltinParts()}, lib()
	p, err := ProposeScript(context.Background(), nil, ScriptInput{Map: m, Design: d, Level: LevelSimple, Cat: cat})
	if err != nil {
		t.Fatal(err)
	}
	type era struct {
		part   string
		params map[string]any
		design DesignOverride
	}
	eras := []era{
		{"phosphor-terminal", map[string]any{"tone": "green", "lines": "$ boot 1983|loading memory|HELLO, WORLD|ready_"}, DesignOverride{Dark: "#050a05", Accent: "#3dff72", VerseStyle: VerseStyleGlow}},
		{"phosphor-terminal", map[string]any{"tone": "amber", "lines": "$ login: guest|mounting disk|WELCOME BACK|ready_"}, DesignOverride{Dark: "#0c0700", Accent: "#ffb000", VerseStyle: VerseStyleGlow}},
		{"synth-sun", map[string]any{"stripes": 8}, DesignOverride{Dark: "#12062b", Accent: "#ff3ea5", VerseStyle: VerseStyleGlow}},
		{"warp-grid", map[string]any{"density": 20}, DesignOverride{Dark: "#06121f", Accent: "#42d9ff", VerseStyle: VerseStyleGlow}},
		{"nebula", map[string]any{"amount": 260, "seed": 4}, DesignOverride{Dark: "#0a0618", Accent: "#b78cff", VerseStyle: VerseStyleGlow}},
	}
	n := len(p.Script.Pieces)
	for i := range p.Script.Pieces {
		pc := &p.Script.Pieces[i]
		e := eras[i*len(eras)/n]
		o := e.design
		pc.Part, pc.Params, pc.Mode, pc.Design = e.part, e.params, "dark", &o
		pc.With = []PartRef{{Part: "timeline-strip", Params: map[string]any{"labels": "1983|1991|2004|2012|2024"}}}
	}
	dir := filepath.Join(t.TempDir(), "piece")
	spec := ProjectSpec{Dir: dir, Name: "epocas", SongFile: path, Map: m, Design: d, Script: p.Script, Cat: cat, Lib: library, FPS: 30, Width: 1280, Height: 720}
	if _, err := WriteProject(spec); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadParts(filepath.Join(dir, "scenes.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	if _, err := pipeline.Run(context.Background(), cfg, pipeline.Options{MotionParts: Parts{}, Dir: dir}, events.New(&log, true)); err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, "out", "epocas.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "cancion-real-epocas.mp4"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d pieces through %d eras", n, len(eras))
}

// TestDrawARealSongWithAStyle makes a piece of a real song with a style drawn from a reference (REAL_PRESET is the
// style's JSON file): its design, its parts in turn and its rhythm.
func TestDrawARealSongWithAStyle(t *testing.T) {
	root, out, presetFile := os.Getenv("REAL_PROJECTS_DIR"), os.Getenv("REAL_OUT_DIR"), os.Getenv("REAL_PRESET")
	if root == "" || out == "" || presetFile == "" {
		t.Skip("set REAL_PROJECTS_DIR, REAL_OUT_DIR and REAL_PRESET")
	}
	needTools(t)
	raw, err := os.ReadFile(presetFile)
	if err != nil {
		t.Fatal(err)
	}
	var preset Preset
	if err := json.Unmarshal(raw, &preset); err != nil {
		t.Fatal(err)
	}
	s, path := loadRealSong(t, root, "demo-indie", "reunion", 0)
	samples := decodeSamples(t, path)
	lyrics := s.LyricsSung
	if strings.TrimSpace(lyrics) == "" {
		lyrics = s.LyricsReq
	}
	m := songmap.Analyse(samples, audiomeasure.Rate, songmap.Input{BPM: float64(s.BPM), Lyrics: lyrics, EstimateWords: true})
	cat, library := Catalog{Builtin: BuiltinParts()}, lib()
	p, err := ProposeScript(context.Background(), nil, ScriptInput{Map: m, Design: preset.Design, Level: LevelAdvanced, Cat: cat, Parts: preset.Parts, Rhythm: preset.Rhythm})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "piece")
	spec := ProjectSpec{Dir: dir, Name: "estilo", SongFile: path, Map: m, Design: preset.Design, Script: p.Script, Cat: cat, Lib: library, FPS: 30, Width: 1280, Height: 720}
	if _, err := WriteProject(spec); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadParts(filepath.Join(dir, "scenes.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	if _, err := pipeline.Run(context.Background(), cfg, pipeline.Options{MotionParts: Parts{}, Dir: dir}, events.New(&log, true)); err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, "out", "estilo.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	name := os.Getenv("REAL_NAME")
	if name == "" {
		name = "estilo"
	}
	if err := os.WriteFile(filepath.Join(out, name+".mp4"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	used := map[string]int{}
	for _, pc := range p.Script.Pieces {
		used[pc.Part]++
	}
	t.Logf("%d pieces of %.1f s at most, parts used: %v", len(p.Script.Pieces), preset.Rhythm, used)
}

package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/platform/events"
	"veritmo/internal/project/config"
)

func fp(v float64) *float64 { return &v }

// mixConfig is a music project with two tracks and two mixes: "final" layers
// both tracks and a source; "solo" only uses the source.
func mixConfig() *config.Config {
	c := musicConfig(config.Track{ID: "base", Prompt: "calm", Lyrics: "[Verse]\nhola"}, config.Track{ID: "voz", Prompt: "choir", Lyrics: "[Verse]\nhola"})
	c.Mixes = []config.Mix{
		{ID: "final", Clips: []config.Clip{{Track: "base"}, {Track: "voz", Start: fp(10), Gain: fp(0.8)}, {File: "efecto.mp3", Start: fp(5)}}},
		{ID: "solo", Clips: []config.Clip{{File: "efecto.mp3"}}},
	}
	return c
}

func putSource(t *testing.T, e *musicEnv, name, content string) {
	t.Helper()
	dir := filepath.Join(e.dir, config.SourcesSubdir)
	os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func callsOf(e *musicEnv, name string) int {
	n := 0
	for _, c := range e.fr.Calls() {
		if c.Name == name {
			n++
		}
	}
	return n
}

func TestMix_RendersAfterTheTracksAndRecordsThemInTheManifest(t *testing.T) {
	e := newMusicEnv(t)
	putSource(t, e, "efecto.mp3", "FX")
	_, evs, err := e.run(t, mixConfig(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(phases(evs, "final"), ","); got != "phase_start:mix,phase_end:mix" {
		t.Fatalf("final phases = %s", got)
	}
	if got := strings.Join(phases(evs, "solo"), ","); got != "phase_start:mix,phase_end:mix" {
		t.Fatalf("solo phases = %s", got)
	}
	// every track phase ended before the first mix started
	lastTrack, firstMix := -1, -1
	for i, ev := range evs {
		if ev.Phase == "export" && (ev.SceneID == "base" || ev.SceneID == "voz") {
			lastTrack = i
		}
		if ev.Phase == "mix" && firstMix < 0 {
			firstMix = i
		}
	}
	if firstMix < lastTrack {
		t.Fatalf("a mix started (event %d) before the tracks ended (event %d)", firstMix, lastTrack)
	}
	if b, err := os.ReadFile(filepath.Join(e.dir, "out", "songs", "final.mp3")); err != nil || !strings.HasPrefix(string(b), "MIX:") {
		t.Fatalf("mix file: %v %q", err, b)
	}
	m := readMusicManifest(t, e.dir)
	if len(m.Mixes) != 2 {
		t.Fatalf("mixes = %+v", m.Mixes)
	}
	f := m.Mixes[0]
	if f.ID != "final" || f.File != "out/songs/final.mp3" || f.AudioSeconds != 20 || f.Hash == "" || len(f.Clips) != 3 {
		t.Fatalf("entry = %+v", f)
	}
	if c := f.Clips[1]; c.Track != "voz" || c.Variant != 1 || c.Start != 10 || c.Gain != 0.8 {
		t.Fatalf("clip as rendered = %+v", c)
	}
	if c := f.Clips[2]; c.File != "efecto.mp3" || c.Track != "" || c.Variant != 0 {
		t.Fatalf("source clip = %+v", c)
	}
	// the mix records the files it read, and each song its own hash, so a regenerated song shows
	if len(f.Inputs) != 3 {
		t.Fatalf("inputs = %+v", f.Inputs)
	}
	songs := map[string]string{}
	for _, tr := range m.Tracks {
		if tr.SHA256 == "" {
			t.Fatalf("track entry without sha256: %+v", tr)
		}
		songs[tr.File] = tr.SHA256
	}
	for _, in := range f.Inputs[:2] {
		if songs[in.File] != in.SHA256 || in.SHA256 == "" {
			t.Fatalf("input %+v does not match the song's hash %v", in, songs)
		}
	}
	if f.LUFS == nil || *f.LUFS != -14.2 {
		t.Fatalf("the rendered mix's loudness must be recorded, rounded to a tenth: %+v", f.LUFS)
	}
	// a filter graph with the three clips went to ffmpeg
	var graph string
	for _, c := range e.fr.Calls() {
		if c.Name == "ffmpeg" && strings.HasSuffix(c.Args[len(c.Args)-1], "final.part.mp3") {
			graph = strings.Join(c.Args, " ")
		}
	}
	if !strings.Contains(graph, "amix=inputs=3") || !strings.Contains(graph, "adelay=delays=10000") || !strings.Contains(graph, "volume=0.8") {
		t.Fatalf("graph = %s", graph)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "out", "songs", "final.part.mp3")); err == nil {
		t.Fatal("the temporary file must not stay")
	}
}

func TestMix_UnchangedIsSkippedAndAChangedInputRendersAgain(t *testing.T) {
	e := newMusicEnv(t)
	putSource(t, e, "efecto.mp3", "FX")
	cfg := mixConfig()
	e.run(t, cfg, Options{})
	renders := callsOf(e, "ffmpeg")

	_, evs, err := e.run(t, cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(phases(evs, "final"), ","); got != "skipped:mix" {
		t.Fatalf("unchanged mix phases = %s", got)
	}
	if callsOf(e, "ffmpeg") != renders {
		t.Fatal("an unchanged mix must not run ffmpeg")
	}
	if m := readMusicManifest(t, e.dir); len(m.Mixes) != 2 || m.Mixes[0].Hash == "" {
		t.Fatalf("the skipped mix must stay in the manifest: %+v", m.Mixes)
	}

	// the content of an input changes: both mixes that read it render again
	putSource(t, e, "efecto.mp3", "FX-2")
	_, evs, _ = e.run(t, cfg, Options{})
	if got := strings.Join(phases(evs, "final"), ","); got != "phase_start:mix,phase_end:mix" {
		t.Fatalf("changed input, final = %s", got)
	}
	if got := strings.Join(phases(evs, "solo"), ","); got != "phase_start:mix,phase_end:mix" {
		t.Fatalf("changed input, solo = %s", got)
	}

	// a clip parameter changes: only that mix renders again
	cfg.Mixes[0].Clips[1].Gain = fp(0.5)
	_, evs, _ = e.run(t, cfg, Options{})
	if got := strings.Join(phases(evs, "final"), ","); got != "phase_start:mix,phase_end:mix" {
		t.Fatalf("changed gain, final = %s", got)
	}
	if got := strings.Join(phases(evs, "solo"), ","); got != "skipped:mix" {
		t.Fatalf("changed gain, solo = %s", got)
	}

	// the master volume and the normalisation also count, and are recorded
	cfg.Mixes[0].Gain = fp(0.8)
	cfg.Mixes[0].Normalize = true
	_, evs, _ = e.run(t, cfg, Options{})
	if got := strings.Join(phases(evs, "final"), ","); got != "phase_start:mix,phase_end:mix" {
		t.Fatalf("changed master, final = %s", got)
	}
	if got := strings.Join(phases(evs, "solo"), ","); got != "skipped:mix" {
		t.Fatalf("changed master, solo = %s", got)
	}
	if f := readMusicManifest(t, e.dir).Mixes[0]; f.Gain == nil || *f.Gain != 0.8 || !f.Normalize {
		t.Fatalf("master not recorded: %+v", f)
	}
	var graph string
	for _, c := range e.fr.Calls() {
		if c.Name == "ffmpeg" && strings.HasSuffix(c.Args[len(c.Args)-1], "final.part.mp3") {
			graph = strings.Join(c.Args, " ")
		}
	}
	if !strings.Contains(graph, "volume=0.8,loudnorm=") {
		t.Fatalf("graph without the master: %s", graph)
	}

	// --force renders again
	_, evs, _ = e.run(t, cfg, Options{Force: true})
	if got := strings.Join(phases(evs, "solo"), ","); got != "phase_start:mix,phase_end:mix" {
		t.Fatalf("force, solo = %s", got)
	}
}

func TestMix_AMutedClipWhoseFileIsMissingDoesNotFailTheMix(t *testing.T) {
	e := newMusicEnv(t)
	cfg := musicConfig(config.Track{ID: "base", Prompt: "calm", Lyrics: "[Verse]\nhola"})
	cfg.Mixes = []config.Mix{{ID: "m", Clips: []config.Clip{{Track: "base"}, {File: "ausente.mp3", Mute: true}}}}
	if _, evs, err := e.run(t, cfg, Options{}); err != nil || strings.Join(phases(evs, "m"), ",") != "phase_start:mix,phase_end:mix" {
		t.Fatalf("a muted clip whose file is missing must not fail: %v", err)
	}
}

func TestMix_MissingAudioFailsThatMixNamingTheClipAndTheOthersStillRun(t *testing.T) {
	e := newMusicEnv(t)
	putSource(t, e, "efecto.mp3", "FX")
	cfg := mixConfig()
	// no track was generated: only "solo" can be mixed
	_, evs, err := e.run(t, cfg, Options{Only: "final"})
	if err == nil || !strings.Contains(err.Error(), `clip 1: track "base" has no exported song yet`) || !strings.Contains(err.Error(), "generate the track first") {
		t.Fatalf("error = %v", err)
	}
	var sawError bool
	for _, ev := range evs {
		if ev.Type == events.TypeError && ev.SceneID == "final" {
			sawError = true
		}
	}
	if !sawError {
		t.Fatal("the failure must be reported on the mix")
	}

	e2 := newMusicEnv(t)
	putSource(t, e2, "efecto.mp3", "FX")
	cfg.Tracks = nil
	cfg.Tracks = []config.Track{{ID: "base", Prompt: "calm", Lyrics: "[Verse]\nhola"}}
	cfg.Mixes = []config.Mix{
		{ID: "roto", Clips: []config.Clip{{File: "falta.mp3"}}},
		{ID: "solo", Clips: []config.Clip{{File: "efecto.mp3"}}},
	}
	_, evs, err = e2.run(t, cfg, Options{})
	if err == nil || !strings.Contains(err.Error(), `the source "falta.mp3" is not in the project's sources folder`) {
		t.Fatalf("error = %v", err)
	}
	if got := strings.Join(phases(evs, "solo"), ","); got != "phase_start:mix,phase_end:mix" {
		t.Fatalf("the other mix must still run, phases = %s", got)
	}
	if m := readMusicManifest(t, e2.dir); len(m.Mixes) != 1 || m.Mixes[0].ID != "solo" {
		t.Fatalf("the manifest keeps what was rendered: %+v", m.Mixes)
	}
}

func TestMix_OnlyAMixRunsNoTrackAndOnlyATrackRunsNoMix(t *testing.T) {
	e := newMusicEnv(t)
	putSource(t, e, "efecto.mp3", "FX")
	cfg := mixConfig()
	if _, _, err := e.run(t, cfg, Options{}); err != nil {
		t.Fatal(err)
	}
	e.fr = newMusicEnv(t).fr // fresh call log with the same behaviour

	putSource(t, e, "efecto.mp3", "FX-2")
	_, evs, err := e.run(t, cfg, Options{Only: "solo"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range engineCalls(e.fr) {
		if name == "ace-synth" || name == "ace-lm" {
			t.Fatalf("a run restricted to a mix must not generate tracks: %v", engineCalls(e.fr))
		}
	}
	if got := strings.Join(phases(evs, "solo"), ","); got != "phase_start:mix,phase_end:mix" {
		t.Fatalf("solo = %s", got)
	}
	if got := phases(evs, "final"); len(got) != 0 {
		t.Fatalf("the other mix must be left alone: %v", got)
	}
	m := readMusicManifest(t, e.dir)
	if len(m.Tracks) != 2 || len(m.Mixes) != 2 {
		t.Fatalf("the manifest keeps the other entries: %d tracks, %d mixes", len(m.Tracks), len(m.Mixes))
	}

	e.fr = newMusicEnv(t).fr
	_, _, err = e.run(t, cfg, Options{Only: "base"})
	if err != nil {
		t.Fatal(err)
	}
	if callsOf(e, "ffmpeg") != 0 {
		t.Fatal("a run restricted to a track must not render mixes")
	}
	if m := readMusicManifest(t, e.dir); len(m.Mixes) != 2 {
		t.Fatalf("the manifest keeps the previous mixes: %+v", m.Mixes)
	}
}

func TestMix_NoMixesLeavesTheManifestWithoutTheField(t *testing.T) {
	e := newMusicEnv(t)
	if _, _, err := e.run(t, musicConfig(config.Track{ID: "a", Prompt: "p", Lyrics: "[Verse]\nx"}), Options{}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(e.dir, "out", "songs", "manifest.json"))
	if strings.Contains(string(b), `"mixes"`) {
		t.Fatalf("a project without mixes must not get the key: %s", b)
	}
	// ffmpeg still encodes each song to mp3, but nothing is mixed or probed
	if callsOf(e, "ffprobe") != 0 {
		t.Fatal("no mix, no ffprobe")
	}
}

func TestMix_RequiredBinariesAndOnlyValidation(t *testing.T) {
	cfg := mixConfig()
	has := func(bins []string, name string) bool {
		for _, b := range bins {
			if b == name {
				return true
			}
		}
		return false
	}
	all := RequiredBinariesFor(cfg, "")
	if !has(all, "ace-synth") || !has(all, "ffmpeg") || !has(all, "ffprobe") {
		t.Fatalf("a full run needs the engine and ffmpeg/ffprobe: %v", all)
	}
	one := RequiredBinariesFor(cfg, "solo")
	if has(one, "ace-synth") || has(one, "ace-lm") || !has(one, "ffmpeg") || !has(one, "ffprobe") {
		t.Fatalf("a mix-only run needs only ffmpeg/ffprobe: %v", one)
	}
	track := RequiredBinariesFor(cfg, "base")
	if has(track, "ffmpeg") || !has(track, "ace-synth") {
		t.Fatalf("a track-only run needs no ffmpeg: %v", track)
	}
	if err := CheckOnlyScene(cfg, "solo"); err != nil {
		t.Fatalf("a mix id is a valid --only: %v", err)
	}
	if err := CheckOnlyScene(cfg, "nope"); err == nil || !strings.Contains(err.Error(), "track or mix") {
		t.Fatalf("unknown id: %v", err)
	}
}

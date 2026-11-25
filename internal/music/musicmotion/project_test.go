package musicmotion

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"veritmo/internal/music/songmap"
	"veritmo/internal/music/transcribe"
	"veritmo/internal/platform/events"
	"veritmo/internal/project/config"
	"veritmo/internal/project/pipeline"
)

func needTools(t *testing.T) {
	t.Helper()
	browserOrSkip(t)
	for _, n := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(n); err != nil {
			t.Skipf("%s is not installed", n)
		}
	}
}

// makeSong writes a short wav with a click on every beat of 120 bpm.
func makeSong(t *testing.T, dir string, seconds int) string {
	t.Helper()
	out := filepath.Join(dir, "in.wav")
	cmd := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration="+strconv.Itoa(seconds), "-ac", "2", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return out
}

func shortMap() songmap.Map {
	return songmap.Build(songmap.Input{Duration: 8, BPM: 120, Lyrics: "[Verse]\nfoo bar baz\n[Chorus]\nallow it now\n",
		Segments: []transcribe.Segment{{Start: 0.5, End: 3, Text: "foo bar baz"}, {Start: 4.5, End: 7, Text: "allow it now"}}})
}

func shortSpec(t *testing.T, dir, song string) ProjectSpec {
	t.Helper()
	cat := Catalog{Builtin: BuiltinParts()}
	m := shortMap()
	p, err := ProposeScript(context.Background(), nil, ScriptInput{Map: m, Design: good(), Level: LevelSimple, Cat: cat})
	if err != nil {
		t.Fatal(err)
	}
	return ProjectSpec{Dir: dir, Name: "piece-test", SongFile: song, Map: m, Design: good(), Script: p.Script, Cat: cat, FPS: 30, Width: 640, Height: 360}
}

func loadProject(t *testing.T, dir string) *config.Config {
	t.Helper()
	cfg, err := loadParts(filepath.Join(dir, "scenes.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestTheProjectOfAPieceIsAValidVideoProjectWithAPagePerPiece(t *testing.T) {
	// Validating the project checks that the machine can draw the scenes' motion graphics, which asks for Chrome.
	browserOrSkip(t)
	dir := t.TempDir()
	song := makeSongOrFake(t, dir)
	spec := shortSpec(t, filepath.Join(dir, "proj"), song)
	files, err := WriteProject(spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(spec.Script.Pieces) || len(files) < 2 {
		t.Fatalf("one scene per piece: %+v", files)
	}
	cfg := loadProject(t, spec.Dir)
	if err := cfg.ValidateInDir(spec.Dir); err != nil {
		t.Fatalf("the project must be valid: %v", err)
	}
	if cfg.BGM == nil || cfg.BGM.File != "song.wav" || cfg.BGM.ResolvedVolume() != 1 || cfg.BGM.ResolvedDucking() {
		t.Fatalf("the song is the soundtrack at full volume: %+v", cfg.BGM)
	}
	if len(cfg.Scenes) != len(files) {
		t.Fatalf("scenes: %d", len(cfg.Scenes))
	}
	for i, sc := range cfg.Scenes {
		if sc.MotionGraphic == "" || !sc.IsSilent() || sc.SilenceDuration() != files[i].Seconds {
			t.Errorf("scene %s: %+v", sc.ID, sc)
		}
		for _, f := range []string{"motion/" + sc.ID + ".html", "motion/" + sc.ID + ".song.json"} {
			if _, err := os.Stat(filepath.Join(spec.Dir, f)); err != nil {
				t.Errorf("%s is missing", f)
			}
		}
	}
	// The song map of a scene is in the scene's own time: it starts at 0.
	var m songmap.Map
	raw, _ := os.ReadFile(filepath.Join(spec.Dir, "motion", "p2.song.json"))
	if err := json.Unmarshal(raw, &m); err != nil || m.Duration <= 0 || m.BeatBase == 0 {
		t.Fatalf("the second scene's map is sliced to its own time: %+v %v", m, err)
	}
}

// makeSongOrFake is a real wav when ffmpeg exists and a placeholder otherwise (the files are only copied then).
func makeSongOrFake(t *testing.T, dir string) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err == nil {
		return makeSong(t, dir, 8)
	}
	p := filepath.Join(dir, "in.wav")
	os.WriteFile(p, []byte("RIFF....WAVE"), 0o644)
	return p
}

func TestWritingTheProjectAgainLeavesUnchangedFilesAlone(t *testing.T) {
	dir := t.TempDir()
	spec := shortSpec(t, filepath.Join(dir, "proj"), makeSongOrFake(t, dir))
	if _, err := WriteProject(spec); err != nil {
		t.Fatal(err)
	}
	page := filepath.Join(spec.Dir, "motion", "p1.html")
	old := time.Now().Add(-time.Hour)
	os.Chtimes(page, old, old)
	if _, err := WriteProject(spec); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(page); !st.ModTime().Equal(old) {
		t.Fatal("an unchanged page must not be rewritten")
	}
	spec.Script.Pieces[0].Params = map[string]any{"label": "changed"}
	spec.Script.Pieces[0].Part = "toggle"
	WriteProject(spec)
	if st, _ := os.Stat(page); st.ModTime().Equal(old) {
		t.Fatal("a changed piece must be written again")
	}
}

func TestAnUnknownPartOrAWrongSongIsRefused(t *testing.T) {
	dir := t.TempDir()
	spec := shortSpec(t, filepath.Join(dir, "proj"), makeSongOrFake(t, dir))
	spec.Script.Pieces[0].Part = "hologram"
	if _, err := WriteProject(spec); err == nil || !strings.Contains(err.Error(), "hologram") {
		t.Fatalf("%v", err)
	}
	spec = shortSpec(t, filepath.Join(dir, "proj2"), filepath.Join(dir, "song.ogg"))
	if _, err := WriteProject(spec); err == nil || !strings.Contains(err.Error(), ".mp3") {
		t.Fatalf("%v", err)
	}
}

func probeSeconds(t *testing.T, file string) (float64, string) {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration:stream=codec_type", "-of", "default=nw=1", file).Output()
	if err != nil {
		t.Fatal(err)
	}
	var d float64
	for _, l := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(l, "duration="); ok {
			d, _ = strconv.ParseFloat(v, 64)
		}
	}
	return d, string(out)
}

// End to end with a short song, a real browser and ffmpeg: a video as long as the song with the song as its sound,
// and editing one piece draws only its scene again.
func TestEndToEndAPieceRendersAVideoAsLongAsTheSongAndOnlyTheEditedSceneAgain(t *testing.T) {
	needTools(t)
	dir := t.TempDir()
	spec := shortSpec(t, filepath.Join(dir, "proj"), makeSong(t, dir, 8))
	spec.Script.Pieces[0].Part, spec.Script.Pieces[0].Params = "toggle", nil
	if _, err := WriteProject(spec); err != nil {
		t.Fatal(err)
	}
	run := func() {
		t.Helper()
		var log bytes.Buffer
		cfg := loadProject(t, spec.Dir)
		if _, err := pipeline.Run(context.Background(), cfg, pipeline.Options{MotionParts: Parts{}, Dir: spec.Dir, KeepTmp: true}, events.New(&log, true)); err != nil {
			t.Fatalf("%v\n%s", err, log.String())
		}
	}
	run()
	out := filepath.Join(spec.Dir, "out", "piece-test.mp4")
	d, probe := probeSeconds(t, out)
	if d < 7.8 || d > 8.4 || !strings.Contains(probe, "codec_type=audio") || !strings.Contains(probe, "codec_type=video") {
		t.Fatalf("a video as long as the song with its sound: %.2f s\n%s", d, probe)
	}
	mp := func(id string) time.Time {
		st, err := os.Stat(filepath.Join(spec.Dir, "tmp", "scene_"+id+".motion.mp4"))
		if err != nil {
			t.Fatal(err)
		}
		return st.ModTime()
	}
	t1, t2 := mp("p1"), mp("p2")
	time.Sleep(1100 * time.Millisecond)
	spec.Script.Pieces[1].Part, spec.Script.Pieces[1].Params = "counter", map[string]any{"unit": "beats"}
	if _, err := WriteProject(spec); err != nil {
		t.Fatal(err)
	}
	run()
	if !mp("p1").Equal(t1) {
		t.Error("the scene of the piece that did not change must come from the cache")
	}
	if !mp("p2").After(t2) {
		t.Error("the scene of the edited piece must be drawn again")
	}
}

func TestTheEstimateGrowsWithLevelAndFrameRateAndIsCorrectedByHistory(t *testing.T) {
	cat := Catalog{Builtin: BuiltinParts()}
	mk := func(part string) Script {
		return Script{Version: 1, Pieces: []Piece{{ID: "a", Start: 0, End: 60, Part: part}, {ID: "b", Start: 60, End: 240, Part: part}}}
	}
	simple30 := EstimateRender(mk("toggle"), cat, 30, 1280, 720, nil)
	adv30 := EstimateRender(mk("particles"), cat, 30, 1280, 720, nil)
	adv60 := EstimateRender(mk("particles"), cat, 60, 1280, 720, nil)
	if !simple30.Estimate || !(adv30.Seconds > simple30.Seconds) || !(adv60.Seconds > adv30.Seconds*1.8) {
		t.Fatalf("simple30 %+v adv30 %+v adv60 %+v", simple30, adv30, adv60)
	}
	if simple30.Seconds < 300 || simple30.Seconds > 900 {
		t.Fatalf("4 minutes of simple at 30 fps is some minutes, not %.0f s", simple30.Seconds)
	}
	hist := t.TempDir()
	// The machine turned out twice as slow as forecast, three times.
	s := mk("toggle")
	for i := 0; i < 3; i++ {
		if err := RecordRender(s, cat, 30, 1280, 720, simple30.Frames, simple30.Seconds*2, DirHistory(hist)); err != nil {
			t.Fatal(err)
		}
	}
	got := EstimateRender(s, cat, 30, 1280, 720, DirHistory(hist))
	if !got.Corrected || got.Factor < 1.9 || got.Factor > 2.1 || got.Seconds < simple30.Seconds*1.9 {
		t.Fatalf("history must correct the forecast: %+v", got)
	}
	if err := RecordRender(s, cat, 30, 1280, 720, 0, 5, DirHistory(hist)); err != nil {
		t.Fatal(err)
	}
	if again := EstimateRender(s, cat, 30, 1280, 720, DirHistory(hist)); again.Factor != got.Factor {
		t.Fatal("a render that drew nothing says nothing about speed")
	}
}

func TestATimeFarFromTheForecastIsNotRecorded(t *testing.T) {
	cat := Catalog{Builtin: BuiltinParts()}
	s := Script{Version: 1, Pieces: []Piece{{ID: "a", Start: 0, End: 60, Part: "toggle"}}}
	e := EstimateRender(s, cat, 30, 1280, 720, nil)
	hist := t.TempDir()
	for _, took := range []float64{0.01, e.Seconds * 50} {
		if err := RecordRender(s, cat, 30, 1280, 720, e.Frames, took, DirHistory(hist)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(HistoryPath(hist)); !os.IsNotExist(err) {
		t.Fatal("neither an instant render nor a stalled one says how fast this machine is")
	}
}

func TestBuildPagesGivesTheSamePagesTheRenderWritesAndWritesNothing(t *testing.T) {
	dir := t.TempDir()
	spec := shortSpec(t, filepath.Join(dir, "proj"), makeSongOrFake(t, dir))
	pages, err := BuildPages(spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(spec.Dir); err == nil {
		t.Fatal("BuildPages must write nothing")
	}
	if len(pages) != len(spec.Script.Pieces) || len(pages) < 2 {
		t.Fatalf("one page per piece: %d", len(pages))
	}
	if _, err := WriteProject(spec); err != nil {
		t.Fatal(err)
	}
	for i, pg := range pages {
		written, err := os.ReadFile(filepath.Join(spec.Dir, "motion", pg.ID+".html"))
		if err != nil || string(written) != pg.HTML {
			t.Errorf("piece %s: the page differs from the one the render writes (%v)", pg.ID, err)
		}
		song, _ := os.ReadFile(filepath.Join(spec.Dir, "motion", pg.ID+".song.json"))
		if string(song) != string(pg.Song) {
			t.Errorf("piece %s: the song map differs", pg.ID)
		}
		if pg.Start != spec.Script.Pieces[i].Start || pg.End != spec.Script.Pieces[i].End || pg.Part == "" {
			t.Errorf("piece %s: %+v", pg.ID, pg)
		}
	}
}

func TestBuildPagesOnlyTheNamedPiecesAndRefusesAnUnknownOne(t *testing.T) {
	dir := t.TempDir()
	spec := shortSpec(t, dir, makeSongOrFake(t, dir))
	second := spec.Script.Pieces[1].ID
	one, err := BuildPages(spec, []string{second})
	if err != nil || len(one) != 1 || one[0].ID != second {
		t.Fatalf("only %s: %v %v", second, one, err)
	}
	if _, err := BuildPages(spec, []string{"nope"}); err == nil || !strings.Contains(err.Error(), "piece nope") {
		t.Fatalf("an unknown piece is refused with its name: %v", err)
	}
}

// A piece with no mode is drawn in the mode of the design: dark on a dark design, whatever the script says elsewhere.
func TestAPieceWithNoModeIsDrawnInTheDesignsMode(t *testing.T) {
	dir := t.TempDir()
	spec := shortSpec(t, dir, filepath.Join(dir, "in.wav"))
	spec.Design.DefaultMode, spec.Design.Modes = ModeDark, nil
	pc := spec.Script.Pieces[0]
	pc.Mode = ""
	sc, _, err := sceneFor(pc, spec)
	if err != nil {
		t.Fatal(err)
	}
	if sc.Mode != ModeDark {
		t.Fatalf("scene mode %q on a dark design", sc.Mode)
	}
	html, err := BuildPage(sc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, `class="mode-dark"`) {
		t.Fatalf("the page is not drawn dark")
	}
	pc.Mode = ModeLight
	if sc, _, _ = sceneFor(pc, spec); sc.Mode != ModeLight {
		t.Fatalf("an explicit light mode was overridden: %q", sc.Mode)
	}
}

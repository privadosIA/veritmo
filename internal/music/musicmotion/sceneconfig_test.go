package musicmotion

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/platform/events"
	"veritmo/internal/project/config"
	"veritmo/internal/project/pipeline"
	"veritmo/internal/video/motion"
)

// sceneProject is a video project with the scenes given (a block of YAML, indented as the scenes are) and the extra lines
// of defaults.
func sceneProject(defaults, scenes string) string {
	return "project: t\noutput: {mode: video, file: out/t.mp4}\ndefaults:\n  resolution: 1280x720\n  fps: 30\n" + defaults + "scenes:\n" + scenes
}

func loadScenes(t *testing.T, defaults, scenes string) (*config.Config, error) {
	t.Helper()
	return validated(t, sceneProject(defaults, scenes))
}

// validated loads a project and runs the validation of a project in a folder of its own.
func validated(t *testing.T, yaml string) (*config.Config, error) {
	t.Helper()
	cfg, err := loadPartsBytes([]byte(yaml))
	if err != nil {
		return cfg, err
	}
	cfg.Checks.Browser = func() error { return nil } // these tests do not draw anything
	return cfg, cfg.ValidateInDir(t.TempDir())
}

func TestAValidSceneMadeFromAPart(t *testing.T) {
	cfg, err := loadScenes(t, "", `  - id: a
    motion_part: terminal
    motion_params: {lines: "$ run|ready", title: shell}
    motion_text: Hello there
    motion_bpm: 100
    silence_seconds: 4
  - id: b
    motion_part: auto
    motion_graphic: a counter that goes from ten to zero
    silence_seconds: 4
`)
	if err != nil {
		t.Fatal(err)
	}
	a := cfg.Scenes[0]
	if !a.IsMotionGraphic() || a.MotionPart != "terminal" || a.MotionParams["title"] != "shell" || a.MotionText != "Hello there" || a.MotionBPM == nil || *a.MotionBPM != 100 {
		t.Fatalf("%+v", a)
	}
	if !cfg.Scenes[1].IsMotionGraphic() {
		t.Fatal("an auto scene is a motion scene")
	}
}

func TestEachBadSceneIsRefusedAndNamed(t *testing.T) {
	cases := []struct {
		name, scene, want string
	}{
		{"unknown part", "motion_part: hologram", `motion_part "hologram"`},
		{"parameter out of its limits", "motion_part: toggle\n    motion_params: {count: 99}", `"count"`},
		{"unknown parameter", "motion_part: toggle\n    motion_params: {colour: red}", `"colour"`},
		{"auto without a description", "motion_part: auto", "needs motion_graphic"},
		{"auto with parameters", "motion_part: auto\n    motion_graphic: x\n    motion_params: {count: 2}", "model fills them in"},
		{"params without a part", "motion_graphic: x\n    motion_params: {count: 2}", "motion_params needs motion_part"},
		{"text without a part", "motion_graphic: x\n    motion_text: hello", "motion_text needs motion_part"},
		{"tempo without a part", "motion_graphic: x\n    motion_bpm: 100", "motion_bpm needs motion_part"},
		{"tempo out of range", "motion_part: counter\n    motion_bpm: 400", "motion_bpm must be between"},
		{"text too long", "motion_part: counter\n    motion_text: " + strings.Repeat("x", 121), "motion_text has 121 characters"},
		{"with a picture", "motion_part: counter\n    image: a.png", "cannot be combined with image"},
		{"with a clip", "motion_part: counter\n    clip: a.mp4", "cannot be combined with clip"},
		{"with an animation", "motion_part: counter\n    animate: true", "cannot be combined with animate"},
	}
	for _, c := range cases {
		_, err := loadScenes(t, "", "  - id: s1\n    "+c.scene+"\n    silence_seconds: 3\n")
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), `"s1"`) {
			t.Errorf("%s: %v, want an error naming the scene and %q", c.name, err, c.want)
		}
	}
}

func TestAParameterOutsideItsLimitsNamesTheScenePartAndParameter(t *testing.T) {
	_, err := loadScenes(t, "", "  - id: intro\n    motion_part: toggle\n    motion_params: {count: 99}\n    silence_seconds: 3\n")
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{`"intro"`, `"toggle"`, `"count"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must name %s: %v", want, err)
		}
	}
}

func TestAMotionPartIsOnlyForVideoProjects(t *testing.T) {
	yaml := "project: t\noutput: {mode: images}\ndefaults:\n  resolution: 1280x720\nscenes:\n  - id: a\n    prompt: x\n    motion_part: counter\n"
	if _, err := validated(t, yaml); err == nil || !strings.Contains(err.Error(), "motion_part only works in video mode") {
		t.Fatalf("%v", err)
	}
}

func TestTheMotionDefaultsAreValidatedLikeAPiecesDesign(t *testing.T) {
	scene := "  - id: a\n    motion_part: counter\n    silence_seconds: 3\n"
	if _, err := loadScenes(t, "  motion:\n    bpm: 90\n    design: {dark: '#0b0f14', light: '#e8f1ec', accent: '#2bff88', display: heavy, body: mono, default_mode: dark, verse_style: highlight, hit_style: metal}\n", scene); err != nil {
		t.Fatal(err)
	}
	for name, d := range map[string]struct{ defaults, want string }{
		"accent not a colour":       {"  motion:\n    design: {dark: '#0b0f14', light: '#e8f1ec', accent: green, display: heavy, body: mono, default_mode: dark}\n", "defaults.motion.design.accent"},
		"accent with no contrast":   {"  motion:\n    design: {dark: '#0b0f14', light: '#e8f1ec', accent: '#101418', display: heavy, body: mono, default_mode: dark}\n", "defaults.motion.design.accent"},
		"a verse style that is not": {"  motion:\n    design: {dark: '#0b0f14', light: '#e8f1ec', accent: '#2bff88', display: heavy, body: mono, default_mode: dark, verse_style: wiggle}\n", "defaults.motion.design.verse_style"},
		"a tempo out of the range":  {"  motion:\n    bpm: 20\n", "defaults.motion.bpm"},
	} {
		if _, err := loadScenes(t, d.defaults, scene); err == nil || !strings.Contains(err.Error(), d.want) {
			t.Errorf("%s: %v, want %q", name, err, d.want)
		}
	}
}

func TestAProjectWithoutPartsIsAsItWas(t *testing.T) {
	if _, err := loadScenes(t, "", "  - id: a\n    motion_graphic: three bullet points\n    silence_seconds: 3\n"); err != nil {
		t.Fatal(err)
	}
}

// End to end with a real browser: a scene made from a part is drawn for its length with a beat every half second at
// 120 bpm, gives the same frames twice, passes the checks of a motion page, and a project with a design uses it.
func TestARealSceneMadeFromAPartMovesOnTheTempoAndIsExact(t *testing.T) {
	needTools(t)
	dir := t.TempDir()
	yaml := sceneProject("  motion:\n    bpm: 120\n    design: {dark: '#0b0f14', light: '#e8f1ec', accent: '#ff3da8', display: heavy, body: mono, default_mode: dark}\n", `  - id: a
    motion_part: counter
    motion_params: {from: 0, step: 100, unit: beats}
    motion_text: counting on the beat
    silence_seconds: 4
  - id: b
    motion_part: search-box
    silence_seconds: 4
`)
	if err := os.WriteFile(filepath.Join(dir, "scenes.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadParts(filepath.Join(dir, "scenes.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	if _, err := pipeline.Run(context.Background(), cfg, pipeline.Options{MotionParts: Parts{}, Dir: dir, KeepTmp: true}, events.New(&log, true)); err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	d, probe := probeSeconds(t, filepath.Join(dir, "out", "t.mp4"))
	if d < 7.8 || d > 8.3 || !strings.Contains(probe, "codec_type=video") {
		t.Fatalf("two scenes of 4 s: %.2f s\n%s", d, probe)
	}
	html, err := os.ReadFile(filepath.Join(dir, "motion", "a.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), "#ff3da8") || !strings.Contains(string(html), "counting on the beat") {
		t.Fatal("the page is made with the design of the project and the text of the scene")
	}
	song, _ := os.ReadFile(filepath.Join(dir, "motion", "a.song.json"))
	page := motion.Page{HTML: string(html), Width: 1280, Height: 720, Duration: 4, Song: song}
	probs, err := motion.Check(context.Background(), browserOrSkip(t), page, motion.Limits{})
	if err != nil || len(probs) != 0 {
		t.Fatalf("the page passes the checks: %v %v", probs, err)
	}
	// A beat every half second: the counter goes up by one step at 1.0, 1.5 and 2.0 s and is steady between them.
	frame := func(at float64) string {
		s, err := motion.Open(context.Background(), browserOrSkip(t), page, motion.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		var v string
		if _, err := s.Frame(context.Background(), at); err != nil {
			t.Fatal(err)
		}
		if err := s.EvalJSON(context.Background(), "JSON.stringify(document.querySelector('#ct .n').textContent)", &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if a, b := frame(1.0), frame(1.5); a == b {
		t.Fatalf("the counter moves from one beat to the next: %q %q", a, b)
	}
	first, again := frame(1.0), frame(1.0)
	if first != again {
		t.Fatal("the same time gives the same frame")
	}
}

func TestTheSongOfTheMotionDefaultsIsValidatedWithItsSources(t *testing.T) {
	scene := "  - id: a\n    motion_part: counter\n    silence_seconds: 3\n"
	cases := map[string]string{
		"a song that is not there": "  motion:\n    song: missing.mp3\n",
		"a path":                   "  motion:\n    song: ../other/song.mp3\n",
		"another kind of file":     "  motion:\n    song: song.ogg\n",
	}
	for name, defaults := range cases {
		if _, err := loadScenes(t, defaults, scene); err == nil || !strings.Contains(err.Error(), "defaults.motion.song") {
			t.Errorf("%s: %v, want an error naming defaults.motion.song", name, err)
		}
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sources"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sources", "song.mp3"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadPartsBytes([]byte(sceneProject("  motion:\n    song: song.mp3\n", "  - id: a\n    motion_part: counter\n    motion_bpm: 90\n    silence_seconds: 3\n  - id: b\n    motion_part: counter\n    tts_text: A narrated one.\n")))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Checks.Browser = func() error { return nil }
	if err := cfg.ValidateInDir(dir); err != nil {
		t.Fatalf("a song that is in the sources is valid: %v", err)
	}
	warnings := strings.Join(cfg.Warnings, "\n")
	if !strings.Contains(warnings, "motion_bpm is ignored") || !strings.Contains(warnings, "narrated") {
		t.Fatalf("the tempo is ignored and a narrated scene's place is an estimate, and it is said:\n%s", warnings)
	}
}

// With a song of 90 bpm the clock of a scene's page ticks on the beats of the song, not on a beat every half second.
func TestARealSceneFollowsTheBeatsOfASongThatIsNotAt120(t *testing.T) {
	needTools(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sources"), 0o755); err != nil {
		t.Fatal(err)
	}
	song := filepath.Join(dir, "sources", "song.wav")
	period := 60.0 / 90
	filter := fmt.Sprintf("aevalsrc='0.55*sin(2*PI*55*mod(t,%[1]f)*(1+8*exp(-mod(t,%[1]f)*30)))*exp(-mod(t,%[1]f)*7)+0.05*sin(2*PI*220*t)':d=16:s=22050", period)
	if b, err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", filter, "-ac", "1", song).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, b)
	}
	yaml := sceneProject("  motion:\n    song: song.wav\n", "  - id: a\n    motion_part: counter\n    silence_seconds: 8\n  - id: b\n    motion_part: toggle\n    silence_seconds: 8\n")
	if err := os.WriteFile(filepath.Join(dir, "scenes.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadParts(filepath.Join(dir, "scenes.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	if _, err := pipeline.Run(context.Background(), cfg, pipeline.Options{MotionParts: Parts{}, Dir: dir, KeepTmp: true}, events.New(&log, true)); err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	d, probe := probeSeconds(t, filepath.Join(dir, "out", "t.mp4"))
	if d < 15.8 || d > 16.4 || !strings.Contains(probe, "codec_type=audio") {
		t.Fatalf("the song is the soundtrack of a video as long as the scenes: %.2f s\n%s", d, probe)
	}
	for _, id := range []string{"a", "b"} {
		html, _ := os.ReadFile(filepath.Join(dir, "motion", id+".html"))
		raw, _ := os.ReadFile(filepath.Join(dir, "motion", id+".song.json"))
		var m struct {
			BPM      float64   `json:"bpm"`
			BeatBase int       `json:"beat_base"`
			Beats    []float64 `json:"beats"`
		}
		if err := json.Unmarshal(raw, &m); err != nil || m.BPM < 87 || m.BPM > 93 || len(m.Beats) < 8 {
			t.Fatalf("scene %s: the map of its span: %v %+v", id, err, m)
		}
		s, err := motion.Open(context.Background(), browserOrSkip(t), motion.Page{HTML: string(html), Width: 1280, Height: 720, Duration: 8, Song: raw}, motion.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		for k := 1; k < 6; k++ {
			if m.Beats[k] < 0 {
				continue
			}
			var n float64
			if _, err := s.Frame(context.Background(), m.Beats[k]+0.03); err != nil {
				t.Fatal(err)
			}
			if err := s.EvalJSON(context.Background(), "JSON.stringify(window.__beat.number)", &n); err != nil {
				t.Fatal(err)
			}
			if int(n) != m.BeatBase+k {
				t.Errorf("scene %s: at %.3f s, just after the beat %d of its map, the clock says beat %v", id, m.Beats[k]+0.03, k, n)
			}
		}
		s.Close()
	}
}

// loadParts and loadPartsBytes read a project the way the programs do: given the catalog of parts the engine keeps.
func loadParts(path string) (*config.Config, error) {
	cfg, err := config.Load(path)
	if cfg != nil {
		cfg.Parts = Parts{}
	}
	return cfg, err
}

func loadPartsBytes(data []byte) (*config.Config, error) {
	cfg, err := config.LoadBytes(data)
	if cfg != nil {
		cfg.Parts = Parts{}
	}
	return cfg, err
}

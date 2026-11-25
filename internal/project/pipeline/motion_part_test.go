package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"veritmo/internal/platform/cache"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
	"veritmo/internal/video/motion"
)

// stubParts is the catalog of parts of a test: the builder of pages and the choice of a part are functions the test sets.
type stubParts struct {
	page   func(PartRequest) (PartPage, error)
	choose func(ctx context.Context, w motion.Writer, r PartChoiceRequest) (PartChoice, error)
}

func (s *stubParts) Page(r PartRequest) (PartPage, error) { return s.page(r) }
func (s *stubParts) Choose(ctx context.Context, w motion.Writer, r PartChoiceRequest) (PartChoice, error) {
	return s.choose(ctx, w, r)
}

// partStubs holds the stub of each test that has one (the key is the test), so the tests share nothing.
var partStubs sync.Map

// stubOf is the stub of the test, made on first use.
func stubOf(t *testing.T) *stubParts {
	v, loaded := partStubs.LoadOrStore(t, &stubParts{})
	if !loaded {
		t.Cleanup(func() { partStubs.Delete(t) })
	}
	return v.(*stubParts)
}

// partsOf is what the run of the test is given: its stub, or nothing when it set none.
func partsOf(t *testing.T) MotionParts {
	if v, ok := partStubs.Load(t); ok {
		return v.(*stubParts)
	}
	return nil
}

// fakeParts replaces the builder of the pages of scenes made from a part and counts what it was asked for.
func fakeParts(t *testing.T) *[]PartRequest {
	t.Helper()
	var asked []PartRequest
	stubOf(t).page = func(r PartRequest) (PartPage, error) {
		asked = append(asked, r)
		why, _ := json.Marshal(r)
		html := strings.Replace(testPage, "</body>", "<!-- "+strings.ReplaceAll(string(why), "--", "- -")+" --></body>", 1) // a page that depends on the whole request
		song, _ := json.Marshal(map[string]any{"bpm": r.BPM, "beats": []float64{0, 60 / r.BPM}})
		if r.Part == "broken" {
			return PartPage{}, errors.New("the part is broken")
		}
		return PartPage{HTML: html, Song: song}, nil
	}
	return &asked
}

func partScene(id, part string) config.Scene {
	return config.Scene{ID: id, MotionPart: part, SilenceSeconds: silent(2), Speed: 1.0}
}

// runPartScenes is runImageScenes with the project changed by mutate (defaults.motion, for one).
func runPartScenes(t *testing.T, scenes []config.Scene, mutate func(*config.Config), manifest *cache.Manifest) (*cache.Manifest, string, error) {
	t.Helper()
	cfg := &config.Config{
		Project:  "demo",
		Output:   config.Output{File: "out/demo.mp4"},
		Defaults: config.Defaults{Model: "m.safetensors", Voice: "v.onnx", Resolution: "1280x720", Sampler: "euler", Steps: 30, FPS: 30},
		Scenes:   scenes,
	}
	if mutate != nil {
		mutate(cfg)
	}
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if inv.Name == "ffprobe" {
			return runner.ScriptedResult{Stdout: []byte(`{"format":{"duration":"3.0"}}`)}
		}
		return runner.ScriptedResult{}
	})
	var buf bytes.Buffer
	if manifest == nil {
		manifest = &cache.Manifest{Scenes: map[string]cache.SceneEntry{}}
	}
	_, err := runVideoOverlap(context.Background(), cfg, events.New(&buf, true), fr, ttsRunnerAdapter{r: fr}, assembleRunnerAdapter{r: fr},
		manifest, cache.NewOnlyFilter(""), false, nil, "tmp", "", nil, nil, 0, partsOf(t))
	return manifest, buf.String(), err
}

func TestPartScene_ThePageIsBuiltFromThePartWithNoModelAndRendered(t *testing.T) {
	imageSetup(t)
	jobs := motionStubs(t) // fails the test if the model is asked
	asked := fakeParts(t)
	scene := partScene("a", "counter")
	scene.MotionParams = map[string]any{"from": 5}
	scene.MotionText = "hello"
	if _, out, err := runPartScenes(t, []config.Scene{scene}, nil, nil); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(*asked) != 1 || (*asked)[0].Part != "counter" || (*asked)[0].Text != "hello" || (*asked)[0].BPM != 120 || (*asked)[0].Width != 1280 || (*asked)[0].Duration != 2 {
		t.Fatalf("the builder is asked once with the scene's part, text, the default tempo and the size: %+v", *asked)
	}
	if _, err := os.Stat(motion.PagePath("", "a", false)); err != nil {
		t.Fatal("the page is stored under motion/")
	}
	if len(*jobs) != 1 || string((*jobs)[0].Page.Song) == "" || !strings.Contains(string((*jobs)[0].Page.Song), `"bpm":120`) {
		t.Fatalf("the render is given the clock of the scene: %+v", *jobs)
	}
	var rec partRecord
	data, _ := os.ReadFile(partRecordPath("", "a"))
	if json.Unmarshal(data, &rec) != nil || rec.Part != "counter" || rec.Key == "" || rec.PageSHA256 == "" {
		t.Fatalf("what the page was built from is recorded: %s", data)
	}
	if meta, _ := motion.LoadMeta("", "a", false); meta.Provider != "catalog" || meta.Model != "counter" {
		t.Fatalf("the page says it came from the catalog: %+v", meta)
	}
}

func TestPartScene_ReusedWhileNothingChangesAndBuiltAgainWhenSomethingDoes(t *testing.T) {
	imageSetup(t)
	jobs := motionStubs(t)
	asked := fakeParts(t)
	scenes := []config.Scene{partScene("a", "counter")}
	m, _, err := runPartScenes(t, scenes, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m, out, _ := runPartScenes(t, scenes, nil, m)
	if len(*asked) != 1 || len(*jobs) != 1 || !strings.Contains(out, `"type":"skipped","scene_id":"a","phase":"render"`) {
		t.Fatalf("unchanged: the page is reused and the video cached (%d builds, %d renders)", len(*asked), len(*jobs))
	}
	changes := map[string]func(){
		"a parameter": func() { scenes[0].MotionParams = map[string]any{"from": 7} },
		"the text":    func() { scenes[0].MotionText = "new" },
		"the tempo":   func() { b := 90.0; scenes[0].MotionBPM = &b },
		"the length":  func() { scenes[0].SilenceSeconds = silent(3) },
		"the part":    func() { scenes[0].MotionPart = "toggle" },
	}
	for _, name := range []string{"a parameter", "the text", "the tempo", "the length", "the part"} {
		before, renders := len(*asked), len(*jobs)
		changes[name]()
		var err error
		m, _, err = runPartScenes(t, scenes, nil, m)
		if err != nil {
			t.Fatal(err)
		}
		if len(*asked) != before+1 || len(*jobs) != renders+1 {
			t.Errorf("%s: the page is built again and the scene drawn again (builds %d to %d, renders %d to %d)", name, before, len(*asked), renders, len(*jobs))
		}
	}
	// The design of the project.
	before := len(*asked)
	_, _, _ = runPartScenes(t, scenes, func(c *config.Config) {
		c.Defaults.Motion = &config.MotionDefaults{Design: &config.MotionDesign{Dark: "#000000", Light: "#ffffff", Accent: "#ff0000", Display: "heavy", Body: "mono", DefaultMode: "dark"}}
	}, m)
	if len(*asked) != before+1 {
		t.Error("a new project design builds the page again")
	}
}

func TestPartScene_TheDefaultTempoOfTheProjectAndTheScenesOwn(t *testing.T) {
	imageSetup(t)
	motionStubs(t)
	asked := fakeParts(t)
	own := 80.0
	a, b := partScene("a", "counter"), partScene("b", "toggle")
	b.MotionBPM = &own
	proj := 100.0
	if _, _, err := runPartScenes(t, []config.Scene{a, b}, func(c *config.Config) { c.Defaults.Motion = &config.MotionDefaults{BPM: &proj} }, nil); err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, r := range *asked {
		got[r.Part] = r.BPM
	}
	if got["counter"] != 100 || got["toggle"] != 80 {
		t.Fatalf("the project's tempo, and the scene's own over it: %v", got)
	}
}

func TestPartScene_AnEditOfThePageIsKeptUntilWhatItWasBuiltFromChanges(t *testing.T) {
	imageSetup(t)
	jobs := motionStubs(t)
	asked := fakeParts(t)
	scenes := []config.Scene{partScene("a", "counter")}
	m, _, _ := runPartScenes(t, scenes, nil, nil)
	edited := strings.ReplaceAll(testPage, "#003", "#0a0")
	if err := motion.Store("", "a", false, edited); err != nil {
		t.Fatal(err)
	}
	m, _, _ = runPartScenes(t, scenes, nil, m)
	if len(*asked) != 1 {
		t.Fatal("the builder is not asked again for a page the user edited")
	}
	if len(*jobs) != 2 || !strings.Contains((*jobs)[1].Page.HTML, "#0a0") {
		t.Fatalf("the edited page is the one drawn: %d renders", len(*jobs))
	}
	scenes[0].MotionText = "changed"
	runPartScenes(t, scenes, nil, m)
	if len(*asked) != 2 {
		t.Fatal("a change of what it was built from builds the page again, replacing the edit")
	}
}

func TestPartScene_WithNoBuilderOrABrokenPartTheSceneFailsSaying(t *testing.T) {
	imageSetup(t)
	motionStubs(t)
	_, _, err := runPartScenes(t, []config.Scene{partScene("a", "counter")}, nil, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	fakeParts(t)
	_, out, err := runPartScenes(t, []config.Scene{partScene("a", "broken")}, nil, nil)
	if err == nil || !strings.Contains(out, "the part is broken") || !strings.Contains(out, "broken") || !strings.Contains(out, `scene \"a\"`) {
		t.Fatalf("the error names the scene and the part: %v\n%s", err, out)
	}
}

// --- a song put on the scenes --------------------------------------------------------------------

// makeClickSong writes a song of the given seconds with a kick on every beat of the tempo into sources/.
func makeClickSong(t *testing.T, name string, seconds int, bpm float64) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	period := 60 / bpm
	filter := fmt.Sprintf("aevalsrc='0.55*sin(2*PI*55*mod(t,%[1]f)*(1+8*exp(-mod(t,%[1]f)*30)))*exp(-mod(t,%[1]f)*7)+0.05*sin(2*PI*220*t)':d=%d:s=22050", period, seconds)
	if b, err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", filter, "-ac", "1", filepath.Join("sources", name)).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, b)
	}
}

func withSong(name string) func(*config.Config) {
	return func(c *config.Config) { c.Defaults.Motion = &config.MotionDefaults{Song: name} }
}

type sliceView struct {
	Duration float64   `json:"duration"`
	BPM      float64   `json:"bpm"`
	BeatBase int       `json:"beat_base"`
	Beats    []float64 `json:"beats"`
}

func TestPartScene_WithASongEachSceneGetsTheBeatsOfItsOwnSpan(t *testing.T) {
	imageSetup(t)
	motionStubs(t)
	asked := fakeParts(t)
	makeClickSong(t, "song.wav", 20, 90)
	a, b := partScene("a", "counter"), partScene("b", "toggle")
	a.SilenceSeconds, b.SilenceSeconds = silent(5), silent(5)
	if _, out, err := runPartScenes(t, []config.Scene{a, b}, withSong("song.wav"), nil); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(*asked) != 2 || len((*asked)[0].Song) == 0 || (*asked)[0].BPM != 0 {
		t.Fatalf("each scene is built from the song, not from a tempo: %+v", *asked)
	}
	full, _, err := readSongMap(context.Background(), "", "song.wav")
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Beats) < 20 || !full.GridAnchored || full.BPM < 87 || full.BPM > 93 {
		t.Fatalf("the beats are followed in the song: bpm %v, %d beats, anchored %v", full.BPM, len(full.Beats), full.GridAnchored)
	}
	var first, second sliceView
	json.Unmarshal((*asked)[0].Song, &first)
	json.Unmarshal((*asked)[1].Song, &second)
	if first.BeatBase > 1 || second.BeatBase < 5 {
		t.Fatalf("the second scene's beats start where the first ends (about the 8th beat of the song): %d and %d", first.BeatBase, second.BeatBase)
	}
	// Each beat of a scene is a beat of the song, in the scene's own time: the song's beat minus where the scene starts.
	for _, c := range []struct {
		v     sliceView
		start float64
	}{{first, 0}, {second, 5}} {
		for k, beat := range c.v.Beats {
			want := math.Round((full.Beats[c.v.BeatBase+k]-c.start)*1000) / 1000
			if math.Abs(beat-want) > 0.0011 {
				t.Fatalf("beat %d of the scene starting at %.0f s: %v, the song's is %v", k, c.start, beat, want)
			}
		}
	}
	if second.Beats[len(second.Beats)-1] > 7.1 {
		t.Fatalf("a scene keeps only a little past its end: %v", second.Beats[len(second.Beats)-1])
	}
}

func TestPartScene_TheScenesAreDrawnAgainWhenTheSongChangesAndNotWhenItDoesNot(t *testing.T) {
	imageSetup(t)
	jobs := motionStubs(t)
	asked := fakeParts(t)
	makeClickSong(t, "song.wav", 12, 90)
	scenes := []config.Scene{partScene("a", "counter")}
	m, _, err := runPartScenes(t, scenes, withSong("song.wav"), nil)
	if err != nil {
		t.Fatal(err)
	}
	m, _, _ = runPartScenes(t, scenes, withSong("song.wav"), m)
	if len(*asked) != 1 || len(*jobs) != 1 {
		t.Fatalf("the same song: nothing is built or drawn again (%d builds, %d renders)", len(*asked), len(*jobs))
	}
	makeClickSong(t, "song.wav", 12, 120) // another tempo
	runPartScenes(t, scenes, withSong("song.wav"), m)
	if len(*asked) != 2 || len(*jobs) != 2 {
		t.Fatalf("another song: the page is built again and the scene drawn again (%d builds, %d renders)", len(*asked), len(*jobs))
	}
}

func TestPartScene_ASceneAfterTheEndOfTheSongKeepsAFixedTempoAndSaysSo(t *testing.T) {
	imageSetup(t)
	motionStubs(t)
	asked := fakeParts(t)
	makeClickSong(t, "song.wav", 6, 90)
	a, b := partScene("a", "counter"), partScene("b", "toggle")
	a.SilenceSeconds, b.SilenceSeconds = silent(8), silent(3)
	_, out, err := runPartScenes(t, []config.Scene{a, b}, withSong("song.wav"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len((*asked)[1].Song) != 0 || (*asked)[1].BPM != 120 || !strings.Contains(out, "the song lasts") {
		t.Fatalf("the second scene starts at 8 s, after the 6 s song: %+v\n%s", (*asked)[1], out)
	}
}

func TestMotionSong_ItIsTheSoundtrackAtFullVolumeUnlessBGMIsSet(t *testing.T) {
	cfg := &config.Config{Defaults: config.Defaults{Motion: &config.MotionDefaults{Song: "song.wav"}}}
	applyMotionSong(cfg)
	if cfg.BGM == nil || cfg.BGM.File != "song.wav" || cfg.BGM.ResolvedVolume() != 1 || cfg.BGM.ResolvedDucking() {
		t.Fatalf("%+v", cfg.BGM)
	}
	own := &config.BGM{File: "other.mp3"}
	cfg = &config.Config{BGM: own, Defaults: config.Defaults{Motion: &config.MotionDefaults{Song: "song.wav"}}}
	applyMotionSong(cfg)
	if cfg.BGM != own {
		t.Fatal("a bgm of the project's own is kept")
	}
	plain := &config.Config{}
	applyMotionSong(plain)
	if plain.BGM != nil {
		t.Fatal("no song, no soundtrack")
	}
}

// --- the model chooses the part -----------------------------------------------------------------

func autoScene(id string) config.Scene {
	return config.Scene{ID: id, MotionPart: "auto", MotionGraphic: "a counter that goes from ten to zero", SilenceSeconds: silent(2), Speed: 1.0}
}

// fakeChoice replaces the model's choice of a part and counts how often it is asked; fails says how many times it refuses first.
func fakeChoice(t *testing.T, answer PartChoice, err error) *int {
	t.Helper()
	asked := 0
	stubOf(t).choose = func(ctx context.Context, w motion.Writer, r PartChoiceRequest) (PartChoice, error) {
		asked++
		if r.Description == "" {
			t.Error("the model is told the description")
		}
		return answer, err
	}
	return &asked
}

func TestAutoScene_AValidChoiceBuildsThePageStoresTheChoiceAndIsNotAskedAgain(t *testing.T) {
	imageSetup(t)
	motionStubs(t)
	built := fakeParts(t)
	asked := fakeChoice(t, PartChoice{Part: "counter", Params: map[string]any{"from": 10, "to": 0}, Text: "ten to zero"}, nil)
	scenes := []config.Scene{autoScene("a")}
	m, out, err := runPartScenes(t, scenes, nil, nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if *asked != 1 || len(*built) != 1 || (*built)[0].Part != "counter" || (*built)[0].Text != "ten to zero" || (*built)[0].Params["to"] != 0 {
		t.Fatalf("the page is built from the chosen part: asked %d, %+v", *asked, *built)
	}
	data, _ := os.ReadFile(choicePath("", "a"))
	var kept storedChoice
	if json.Unmarshal(data, &kept) != nil || kept.Choice.Part != "counter" {
		t.Fatalf("the choice is stored with the page: %s", data)
	}
	runPartScenes(t, scenes, nil, m)
	if *asked != 1 || len(*built) != 1 {
		t.Fatalf("the next run does not ask the model again (asked %d, built %d)", *asked, len(*built))
	}
	// A new description is a new choice.
	scenes[0].MotionGraphic = "a table of risks"
	runPartScenes(t, scenes, nil, m)
	if *asked != 2 {
		t.Fatalf("another description asks again: %d", *asked)
	}
}

func TestAutoScene_TheTextOfTheSceneWinsOverTheTextTheModelGave(t *testing.T) {
	imageSetup(t)
	motionStubs(t)
	built := fakeParts(t)
	fakeChoice(t, PartChoice{Part: "counter", Text: "the model's text"}, nil)
	scene := autoScene("a")
	scene.MotionText = "mine"
	if _, _, err := runPartScenes(t, []config.Scene{scene}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if (*built)[0].Text != "mine" {
		t.Fatalf("%+v", (*built)[0])
	}
}

// When the model cannot choose, the scene goes the way a scene with only motion_graphic goes (the model writes the whole
// page) and the run says why the choice failed.
func TestAutoScene_WhenTheChoiceFailsTheModelWritesTheWholePageAndTheRunSaysWhy(t *testing.T) {
	b, err := motion.Find("")
	if err != nil {
		t.Skip("no Chrome or Chromium on this machine")
	}
	imageSetup(t)
	motionStubs(t)
	findBrowser = func() (motion.Browser, error) { return b, nil }
	wrote := 0
	newMotionWriter = func(context.Context) (motion.Writer, motion.ModelInfo, error) {
		return writerFunc(func(system, user string) (string, error) { wrote++; return testPage, nil }), motion.ModelInfo{Provider: "Ollama", Model: "m:1b"}, nil
	}
	built := fakeParts(t)
	asked := fakeChoice(t, PartChoice{}, errors.New("no valid choice in 3 answers; the last was refused: the part \"hologram\" is not in the catalog"))
	scenes := []config.Scene{autoScene("a")}
	m, out, err := runPartScenes(t, scenes, nil, nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if *asked != 1 || wrote != 1 || len(*built) != 0 {
		t.Fatalf("the choice failed, so the model wrote the whole page (asked %d, wrote %d, built %d)", *asked, wrote, len(*built))
	}
	if !strings.Contains(out, "could not choose a part") || !strings.Contains(out, "hologram") {
		t.Fatalf("the run says why the choice failed:\n%s", out)
	}
	_, _, _ = runPartScenes(t, scenes, nil, m)
	if *asked != 1 || wrote != 1 {
		t.Fatalf("the page the model wrote is reused: no new choice (%d) and no new page (%d)", *asked, wrote)
	}
}

func TestPartScene_WithASongTheBuilderIsToldWhereTheSceneSitsInIt(t *testing.T) {
	imageSetup(t)
	motionStubs(t)
	asked := fakeParts(t)
	makeClickSong(t, "song.wav", 20, 90)
	a, b := partScene("a", "counter"), partScene("b", "toggle")
	a.SilenceSeconds, b.SilenceSeconds = silent(5), silent(5)
	if _, _, err := runPartScenes(t, []config.Scene{a, b}, withSong("song.wav"), nil); err != nil {
		t.Fatal(err)
	}
	if (*asked)[0].SongStart != 0 || (*asked)[1].SongStart != 5 || (*asked)[0].SongLength < 19 || (*asked)[1].SongLength != (*asked)[0].SongLength {
		t.Fatalf("the second scene starts at 5 s of a 20 s song: %+v", *asked)
	}
}

func TestPartScene_WithNoSongThePositionIsZero(t *testing.T) {
	imageSetup(t)
	motionStubs(t)
	asked := fakeParts(t)
	if _, _, err := runPartScenes(t, []config.Scene{partScene("a", "counter")}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if (*asked)[0].SongStart != 0 || (*asked)[0].SongLength != 0 {
		t.Fatalf("%+v", (*asked)[0])
	}
}

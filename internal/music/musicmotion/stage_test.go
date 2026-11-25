package musicmotion

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"veritmo/internal/music/transcribe"

	"veritmo/internal/music/songmap"
	"veritmo/internal/platform/events"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/pipeline"
	"veritmo/internal/video/motion"
)

func stageOK() Stage {
	return Stage{Background: "stage", Characters: []StageCharacter{
		{Character: "bot", Pose: "wave", Expression: "happy", Action: ActionBounce, X: 30, Y: 80, Size: 40},
		{Character: "scientist", Pose: "idle", Expression: "worried", Action: ActionLook, X: 70, Y: 80, Size: 40},
	}}
}

func TestAValidStagePasses(t *testing.T) {
	if err := stageOK().Validate(lib(), 1280, 720); err != nil {
		t.Fatal(err)
	}
}

func TestAStageWithSomethingUnknownIsRefusedAndNamed(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Stage)
		want string
	}{
		{"unknown background", func(s *Stage) { s.Background = "moon" }, `"moon"`},
		{"unknown character", func(s *Stage) { s.Characters[0].Character = "dragon" }, `"dragon"`},
		{"unknown pose", func(s *Stage) { s.Characters[0].Pose = "dance" }, `"dance"`},
		{"unknown expression", func(s *Stage) { s.Characters[0].Expression = "angry" }, `"angry"`},
		{"unknown action", func(s *Stage) { s.Characters[0].Action = "fly" }, `"fly"`},
		{"size out of range", func(s *Stage) { s.Characters[0].Size = 0 }, "percentages"},
	}
	for _, c := range cases {
		st := stageOK()
		c.edit(&st)
		if err := st.Validate(lib(), 1280, 720); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error naming %s", c.name, err, c.want)
		}
	}
	st := stageOK()
	st.Characters[0].Pose = "dance"
	err := st.Validate(lib(), 1280, 720)
	if !strings.Contains(err.Error(), "bot") || !strings.Contains(err.Error(), "dance") {
		t.Errorf("the error must name the character and the pose: %v", err)
	}
}

func TestACharacterPartlyOutsideTheFrameIsRefusedUnlessItEntersOrExits(t *testing.T) {
	for _, action := range []string{"", ActionBounce, ActionLook, ActionSlide} {
		st := Stage{Characters: []StageCharacter{{Character: "bot", Action: action, X: 2, Y: 80, Size: 40}}}
		if err := st.Validate(lib(), 1280, 720); err == nil || !strings.Contains(err.Error(), "outside the frame") {
			t.Errorf("action %q at the edge: %v", action, err)
		}
	}
	for _, action := range []string{ActionEnter, ActionExit} {
		st := Stage{Characters: []StageCharacter{{Character: "bot", Action: action, X: 2, Y: 80, Size: 40}}}
		if err := st.Validate(lib(), 1280, 720); err != nil {
			t.Errorf("%s may start or end outside: %v", action, err)
		}
	}
	// A slide needs room: a tenth of the frame either side.
	st := Stage{Characters: []StageCharacter{{Character: "bot", Action: ActionSlide, X: 20, Y: 80, Size: 40}}}
	if err := st.Validate(lib(), 1280, 720); err != nil {
		t.Errorf("a slide with room: %v", err)
	}
	st.Characters[0].X = 11
	if err := st.Validate(lib(), 1280, 720); err == nil {
		t.Error("a slide without room must be refused")
	}
}

func TestTheStagesOfAScriptAreCheckedWithThePieceNamed(t *testing.T) {
	s := testScript()
	st := stageOK()
	st.Characters[0].Character = "dragon"
	s.Pieces[1].Stage = &st
	err := s.ValidateStages(lib(), 1280, 720)
	if err == nil || !strings.Contains(err.Error(), `"p2"`) || !strings.Contains(err.Error(), "dragon") {
		t.Fatalf("%v", err)
	}
	if err := s.ValidateAll(Catalog{Builtin: BuiltinParts()}, lib(), testMap(), good(), 1280, 720); err == nil {
		t.Fatal("ValidateAll must include the stages")
	}
}

func TestTheIllustratedStyleDefaultsTo24FPS(t *testing.T) {
	if f, err := ResolveFPS(StyleIllustrated, 0); err != nil || f != 24 {
		t.Fatalf("%d %v", f, err)
	}
	if f, _ := ResolveFPS(StyleTypographic, 0); f != 30 {
		t.Fatalf("%d", f)
	}
	if f, _ := ResolveFPS(StyleIllustrated, 60); f != 60 {
		t.Fatalf("%d", f)
	}
	if _, err := ResolveFPS(StyleIllustrated, 25); err == nil {
		t.Fatal("25 fps is not one of 24, 30, 60")
	}
}

func TestTwoCharactersDoNotChangeEachOthersDrawings(t *testing.T) {
	_, actors, err := ResolveStage(&Stage{Characters: []StageCharacter{{Character: "bot", X: 30, Y: 80, Size: 40}, {Character: "scientist", X: 70, Y: 80, Size: 40}}}, lib())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(actors[0].SVG, ".char-bot-a1 .leg") || !strings.Contains(actors[1].SVG, ".char-scientist-a2 .leg") || strings.Contains(actors[0].SVG, "<style>.body") {
		t.Fatalf("the CSS of each drawing must be scoped to it:\n%s", actors[0].SVG[:400])
	}
}

func stagePiece() Scene {
	p, _ := (Catalog{Builtin: BuiltinParts()}).Get("blank")
	params, _ := p.ResolveParams(nil)
	back, actors, _ := ResolveStage(&Stage{Background: "stage", Characters: []StageCharacter{
		{Character: "bot", Pose: "wave", Expression: "happy", Action: ActionBounce, X: 30, Y: 82, Size: 42},
		{Character: "scientist", Pose: "idle", Expression: "worried", Action: ActionLook, X: 70, Y: 82, Size: 42},
	}}, lib())
	d := good()
	d.Accent = "#e8451c"
	return Scene{Part: p, Params: params, Design: d, Mode: ModeLight, Duration: 4, Width: 1280, Height: 720, Backdrop: back, Actors: actors,
		Lines: []Line{{Text: "hello there", Start: 0.5, End: 4, Keyword: 0, Words: []Word{{"hello", 0.5, 1, true}, {"there", 1, 1.5, true}}}}}
}

func TestABouncingCharacterMovesOnceInEveryBeat(t *testing.T) {
	sc := stagePiece()
	s, _ := openPage(t, sc)
	y := func(at float64) float64 {
		var v float64
		evalAt(t, s, at, `document.getElementById('actor-a1').getBoundingClientRect().top`, &v)
		return v
	}
	// 120 bpm: the beat that starts at 1.0 s hops up in its middle (1.25 s) and is back down at 1.5 s.
	on, mid, next := y(1.0), y(1.25), y(1.5)
	if !(mid < on-5) || abs(on-next) > 0.5 {
		t.Fatalf("it should hop up inside the beat and land on the next: %.1f %.1f %.1f", on, mid, next)
	}
	// The one that looks sways: its rotation changes across two beats.
	var a, b string
	evalAt(t, s, 1.0, `document.getElementById('actor-a2').style.transform`, &a)
	evalAt(t, s, 1.5, `document.getElementById('actor-a2').style.transform`, &b)
	if a == b {
		t.Fatalf("the character that looks must sway: %q", a)
	}
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func TestACharacterEntersFromOutsideAndExitsAtTheEnd(t *testing.T) {
	sc := stagePiece()
	for i := range sc.Actors {
		sc.Actors[i].Action = ActionEnter
	}
	sc.Actors[1].Action = ActionExit
	s, _ := openPage(t, sc)
	left := func(id string, at float64) float64 {
		var v float64
		evalAt(t, s, at, `document.getElementById('`+id+`').getBoundingClientRect().left`, &v)
		return v
	}
	if left("actor-a1", 0) > -50 || left("actor-a1", 1.0) < 100 {
		t.Fatalf("it comes in from the left in the first beat: %.0f then %.0f", left("actor-a1", 0), left("actor-a1", 1.0))
	}
	if r := left("actor-a2", 3.0); r < 700 || r > 1000 {
		t.Fatalf("it is in its place before the exit: %.0f", r)
	}
	if left("actor-a2", 4.0) < 1280 {
		t.Fatalf("it is outside at the end: %.0f", left("actor-a2", 4.0))
	}
}

func TestAStagePageIsAGoodPageAndLoadsNothingFromOutside(t *testing.T) {
	sc := stagePiece()
	sc.Actors[0].Action, sc.Actors[1].Action = ActionBounce, ActionLook
	html, err := BuildPage(sc)
	if err != nil {
		t.Fatal(err)
	}
	song := songmap.Build(songmap.Input{Duration: 4, BPM: 120})
	raw, _ := json.Marshal(song)
	probs, err := motion.Check(context.Background(), browserOrSkip(t), motion.Page{HTML: html, Width: 1280, Height: 720, Duration: 4, Song: raw}, motion.Limits{})
	if err != nil || len(probs) != 0 {
		t.Fatalf("%v %v", probs, err)
	}
}

// spy records the programs a run starts.
type spy struct {
	inner runner.CommandRunner
	names []string
}

func (s *spy) Run(ctx context.Context, name string, args []string, opts ...runner.Option) ([]byte, error) {
	s.names = append(s.names, name)
	return s.inner.Run(ctx, name, args, opts...)
}

// An illustrated piece is made of characters of the library only: no diffusion model is loaded to draw it.
func TestAnIllustratedPieceLoadsNoImageModel(t *testing.T) {
	needTools(t)
	dir := t.TempDir()
	spec := shortSpec(t, filepath.Join(dir, "proj"), makeSong(t, dir, 8))
	spec.Lib = lib()
	spec.FPS, _ = ResolveFPS(StyleIllustrated, 0)
	for i := range spec.Script.Pieces {
		spec.Script.Pieces[i].Part, spec.Script.Pieces[i].Params = "blank", nil
		spec.Script.Pieces[i].Stage = &Stage{Background: "paper", Characters: []StageCharacter{{Character: "bot", Action: ActionBounce, X: 50, Y: 85, Size: 45}}}
	}
	if _, err := WriteProject(spec); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadParts(filepath.Join(spec.Dir, "scenes.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Defaults.FPS != 24 {
		t.Fatalf("the illustrated style defaults to 24 fps: %d", cfg.Defaults.FPS)
	}
	sp := &spy{inner: runner.NewExecRunner()}
	var log strings.Builder
	if _, err := pipeline.Run(context.Background(), cfg, pipeline.Options{MotionParts: Parts{}, Dir: spec.Dir, KeepTmp: true, Runner: sp}, events.New(&log, true)); err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	for _, n := range sp.names {
		if n == "sd" || strings.Contains(n, "sd-") || n == "ollama" {
			t.Fatalf("no model may be loaded for an illustrated piece, but %q ran (%v)", n, sp.names)
		}
	}
	if _, err := os.Stat(filepath.Join(spec.Dir, "out", "piece-test.mp4")); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip()
	}
}

// With DUMP_PARTS_DIR set, writes a frame of each background with the characters on it (a look at the art).
func TestDumpStagesForALook(t *testing.T) {
	dir := os.Getenv("DUMP_PARTS_DIR")
	if dir == "" {
		t.Skip("set DUMP_PARTS_DIR to write the frames")
	}
	for _, bg := range []string{"stage", "night-sky", "paper"} {
		for _, mode := range []string{ModeDark, ModeLight} {
			sc := stagePiece()
			sc.Mode = mode
			st := &Stage{Background: bg, Characters: []StageCharacter{
				{Character: "bot", Pose: "wave", Expression: "happy", Action: ActionBounce, X: 30, Y: 82, Size: 45},
				{Character: "scientist", Pose: "point", Expression: "worried", Action: ActionLook, X: 68, Y: 82, Size: 45}}}
			sc.Backdrop, sc.Actors, _ = ResolveStage(st, lib())
			f := framesOf(t, sc, []float64{2.3})
			if err := os.WriteFile(filepath.Join(dir, "stage-"+bg+"-"+mode+".png"), f[0], 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// With DUMP_VIDEOS_DIR set, renders a demo piece of each style (simple, advanced, illustrated) over a 16 s synthetic
// song with a kick on every beat of 120 bpm, and copies the videos there (to watch the sync).
func TestDumpDemoVideos(t *testing.T) {
	out := os.Getenv("DUMP_VIDEOS_DIR")
	if out == "" {
		t.Skip("set DUMP_VIDEOS_DIR to write the demo videos")
	}
	needTools(t)
	dir := t.TempDir()
	song := filepath.Join(dir, "demo.wav")
	// A kick (a short low sine) on every beat of 120 bpm over a soft chord.
	filter := "aevalsrc='0.55*sin(2*PI*55*mod(t,0.5)*(1+8*exp(-mod(t,0.5)*30)))*exp(-mod(t,0.5)*7)+0.08*sin(2*PI*220*t)+0.06*sin(2*PI*277.2*t)+0.05*sin(2*PI*329.6*t)':d=16:s=44100"
	if b, err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", filter, "-ac", "2", song).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, b)
	}
	m := songmap.Build(songmap.Input{Duration: 16, BPM: 120, Lyrics: "[Verse]\nthe app asks me twice\nI say yes again\n[Chorus]\nallow allow allow\nforget me not\n",
		Segments: []transcribe.Segment{{Start: 0.5, End: 3.5, Text: "the app asks me twice"}, {Start: 4, End: 7.5, Text: "I say yes again"}, {Start: 8.5, End: 11.5, Text: "allow allow allow"}, {Start: 12, End: 15.5, Text: "forget me not"}}})
	d := good()
	d.Accent = "#e8451c"
	d.Modes = map[string]string{"chorus": ModeDark}
	cat := Catalog{Builtin: BuiltinParts()}
	for _, c := range []struct{ name, level, style string }{{"simple", LevelSimple, StyleTypographic}, {"advanced", LevelAdvanced, StyleTypographic}, {"ilustrado", LevelSimple, StyleIllustrated}} {
		fps, _ := ResolveFPS(c.style, 0)
		p, err := ProposeScript(context.Background(), nil, ScriptInput{Map: m, Design: d, Level: c.level, Cat: cat})
		if err != nil {
			t.Fatal(err)
		}
		if c.style == StyleIllustrated {
			for i := range p.Script.Pieces {
				p.Script.Pieces[i].Part, p.Script.Pieces[i].Params = "blank", nil
				p.Script.Pieces[i].Mode = ModeLight
				acts := []string{ActionBounce, ActionLook, ActionWave, ActionSlide}
				p.Script.Pieces[i].Stage = &Stage{Background: []string{"stage", "paper", "night-sky"}[i%3], Characters: []StageCharacter{
					{Character: "bot", Pose: []string{"idle", "wave", "point"}[i%3], Expression: []string{"happy", "surprised", "happy"}[i%3], Action: acts[i%4], X: 32, Y: 78, Size: 42},
					{Character: "scientist", Pose: "idle", Expression: []string{"neutral", "worried", "happy"}[i%3], Action: ActionLook, X: 68, Y: 78, Size: 42}}}
			}
		}
		spec := ProjectSpec{Dir: filepath.Join(dir, c.name), Name: "demo-" + c.name, SongFile: song, Map: m, Design: d, Script: p.Script, Cat: cat, Lib: lib(), FPS: fps, Width: 1280, Height: 720}
		if _, err := WriteProject(spec); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadParts(filepath.Join(spec.Dir, "scenes.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var log strings.Builder
		if _, err := pipeline.Run(context.Background(), cfg, pipeline.Options{MotionParts: Parts{}, Dir: spec.Dir}, events.New(&log, true)); err != nil {
			t.Fatalf("%v\n%s", err, log.String())
		}
		data, err := os.ReadFile(filepath.Join(spec.Dir, "out", "demo-"+c.name+".mp4"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, "demo-"+c.name+".mp4"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTheMascotAndThePastelBackgroundComeWithTheLibraryAndFollowTheDesign(t *testing.T) {
	m, ok := lib().Character("mascot")
	if !ok || strings.Join(m.PoseNames(), ",") != "cheer,idle,point,wave" || strings.Join(m.Expressions, ",") != "happy,sad,surprised" {
		t.Fatalf("%+v", m.CharacterManifest)
	}
	if _, ok := lib().Background("pastel-dots"); !ok {
		t.Fatal("pastel-dots is missing")
	}
	st := Stage{Background: "pastel-dots", Characters: []StageCharacter{
		{Character: "mascot", Pose: "cheer", Expression: "surprised", Action: ActionBounce, X: 32, Y: 80, Size: 44},
		{Character: "mascot", Pose: "point", Expression: "sad", Action: ActionLook, X: 68, Y: 80, Size: 44}}}
	if err := st.Validate(lib(), 1280, 720); err != nil {
		t.Fatalf("a stage with the mascot is valid: %v", err)
	}
	for _, c := range []StageCharacter{{Character: "mascot", Pose: "dance", X: 50, Y: 80, Size: 40}, {Character: "mascot", Expression: "angry", X: 50, Y: 80, Size: 40}} {
		if err := (Stage{Characters: []StageCharacter{c}}).Validate(lib(), 1280, 720); err == nil {
			t.Fatalf("an unknown pose or expression must be refused: %+v", c)
		}
	}
	// Drawn with the colours of the design: the hair is the accent, and the picture changes with the expression.
	sc := stagePiece()
	sc.Backdrop, sc.Actors, _ = ResolveStage(&st, lib())
	s, _ := openPage(t, sc)
	var hair string
	evalAt(t, s, 1.0, `getComputedStyle(document.querySelector('#actor-a1 .hair')).fill`, &hair)
	if hair != "rgb(232, 69, 28)" { // the accent of stagePiece's design (#e8451c)
		t.Fatalf("the hair is the accent of the design: %s", hair)
	}
	shown := func(id string) string {
		var v string
		evalAt(t, s, 1.0, `Array.from(document.querySelectorAll('#`+id+` .expr')).filter(function(g){return getComputedStyle(g).display!=='none'}).map(function(g){return g.getAttribute('class')}).join(',')`, &v)
		return v
	}
	if shown("actor-a1") != "expr expr-surprised" || shown("actor-a2") != "expr expr-sad" {
		t.Fatalf("each mascot shows its own expression: %q %q", shown("actor-a1"), shown("actor-a2"))
	}
}

// With DUMP_PARTS_DIR set, writes frames of the verse styles and of the hit styles (a look at the finishes of the text).
func TestDumpTextStylesForALook(t *testing.T) {
	dir := os.Getenv("DUMP_PARTS_DIR")
	if dir == "" {
		t.Skip("set DUMP_PARTS_DIR to write the frames")
	}
	for _, v := range []string{VerseStylePlain, VerseStyleHighlight, VerseStyleKaraoke} {
		sc := partScene(t, mustPart(t, "terminal"))
		sc.Design.Accent = "#e8451c"
		sc.Design.VerseStyle = v
		sc.Lines = []Line{{Text: "x", Start: 0.5, End: 4, Keyword: 1, Words: []Word{{"the", 0.5, 1, true}, {"calendar", 1, 1.5, true}, {"says", 1.5, 2, true}, {"quick", 2, 2.5, true}, {"sync", 2.5, 3, true}}}}
		f := framesOf(t, sc, []float64{2.3})
		if err := os.WriteFile(filepath.Join(dir, "verso-"+v+".png"), f[0], 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, h := range []string{HitStylePlain, HitStyleMetal, HitStyleGlow} {
		sc := partScene(t, mustPart(t, "blank"))
		sc.Design.Accent = "#e8451c"
		sc.Design.HitStyle = h
		sc.Hit = &Hit{Word: "ALLOW", At: 1.0, Length: 1.6}
		f := framesOf(t, sc, []float64{1.0 + 0.9})
		if err := os.WriteFile(filepath.Join(dir, "golpe-"+h+".png"), f[0], 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func mustPart(t *testing.T, name string) Part {
	t.Helper()
	p, ok := (Catalog{Builtin: BuiltinParts()}).Get(name)
	if !ok {
		t.Fatalf("no part %s", name)
	}
	return p
}

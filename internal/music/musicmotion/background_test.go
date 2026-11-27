package musicmotion

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func f64(v float64) *float64 { return &v }

// withClips puts fake clip files in a sources folder and answers ffprobe for them: every clip lasts the seconds the table says.
func withClips(t *testing.T, lengths map[string]float64) string {
	t.Helper()
	dir := t.TempDir()
	for name := range lengths {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := ProbeClip
	ProbeClip = func(ctx context.Context, path string) (ClipInfo, error) {
		secs, ok := lengths[filepath.Base(path)]
		if !ok {
			return ClipInfo{}, os.ErrNotExist
		}
		return ClipInfo{Seconds: secs, Width: 832, Height: 480, FPS: 16}, nil
	}
	t.Cleanup(func() { ProbeClip = old })
	return dir
}

func TestAScriptWithABackgroundIsValid(t *testing.T) {
	dir := withClips(t, map[string]float64{"chorus.mp4": 6})
	s := testScript().WithSources(dir)
	s.Pieces[1].Background = &PieceBackground{Clip: "chorus.mp4", ClipStart: f64(1), ClipEnd: f64(5), ClipSpeed: f64(0.5), Scrim: f64(0.4)}
	if err := s.Validate(Catalog{Builtin: BuiltinParts()}, testMap(), good()); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(s)
	var back Script
	if err := json.Unmarshal(raw, &back); err != nil || back.Pieces[1].Background == nil || back.Pieces[1].Background.Clip != "chorus.mp4" || back.Pieces[1].Background.ScrimValue() != 0.4 {
		t.Fatalf("round trip: %v %s", err, raw)
	}
	if testScript().Pieces[0].Background != nil || strings.Contains(string(mustJSON(t, testScript())), "background") {
		t.Fatal("a script with no background does not say so")
	}
	if (PieceBackground{Clip: "a.mp4"}).ScrimValue() != DefaultScrim {
		t.Fatal("the default scrim")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestEachBadBackgroundIsRefusedAndNamed(t *testing.T) {
	cat := Catalog{Builtin: BuiltinParts()}
	cases := []struct {
		name string
		edit func(*Script)
		want string
	}{
		{"a clip the piece does not keep", func(s *Script) { s.Pieces[1].Background.Clip = "missing.mp4" }, `no clip "missing.mp4"`},
		{"a path", func(s *Script) { s.Pieces[1].Background.Clip = "../chorus.mp4" }, "name of a video"},
		{"not a video", func(s *Script) { s.Pieces[1].Background.Clip = "chorus.txt" }, "name of a video"},
		{"a trim past the file", func(s *Script) { s.Pieces[1].Background.ClipEnd = f64(9) }, "lasts 6.00 s"},
		{"a start past the file", func(s *Script) { s.Pieces[1].Background.ClipStart = f64(7) }, "beyond the end"},
		{"an end before the start", func(s *Script) { s.Pieces[1].Background.ClipStart, s.Pieces[1].Background.ClipEnd = f64(4), f64(3) }, "clip_end"},
		{"a negative start", func(s *Script) { s.Pieces[1].Background.ClipStart = f64(-1) }, "clip_start"},
		{"a speed too slow", func(s *Script) { s.Pieces[1].Background.ClipSpeed = f64(0.1) }, "clip_speed"},
		{"a speed too fast", func(s *Script) { s.Pieces[1].Background.ClipSpeed = f64(5) }, "clip_speed"},
		{"a scrim too strong", func(s *Script) { s.Pieces[1].Background.Scrim = f64(0.9) }, "scrim"},
		{"a negative scrim", func(s *Script) { s.Pieces[1].Background.Scrim = f64(-0.1) }, "scrim"},
		{"a part that fills the frame", func(s *Script) { s.Pieces[1].Part, s.Pieces[1].Mode = "sunburst", "" }, `"sunburst"`},
		{"an extra that fills the frame", func(s *Script) { s.Pieces[1].With = []PartRef{{Part: "synth-sun"}} }, `"synth-sun"`},
		{"a stage background", func(s *Script) { s.Pieces[1].Stage = &Stage{Background: "stage"} }, "stage background"},
		{"a layer item that fills the frame", func(s *Script) {
			s.Layers = []Layer{{ID: "l1", Items: []LayerItem{{Part: "sunburst", Start: 10, End: 20}}}}
		}, `"sunburst"`},
	}
	dir := withClips(t, map[string]float64{"chorus.mp4": 6})
	for _, c := range cases {
		s := testScript().WithSources(dir)
		s.Pieces[1].Background = &PieceBackground{Clip: "chorus.mp4"}
		c.edit(&s)
		err := s.Validate(cat, testMap(), good())
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), `p2`) && c.name != "a layer item that fills the frame" {
			t.Errorf("%s: got %v, want an error naming the piece (or the item it becomes) with %q", c.name, err, c.want)
		}
	}
}

func TestABackgroundNeedsTheSourcesOfThePiece(t *testing.T) {
	s := testScript() // no WithSources
	s.Pieces[1].Background = &PieceBackground{Clip: "chorus.mp4"}
	if err := s.Validate(Catalog{Builtin: BuiltinParts()}, testMap(), good()); err == nil || !strings.Contains(err.Error(), "keeps no videos") {
		t.Fatalf("%v", err)
	}
}

func TestABlenderPieceCannotHaveABackground(t *testing.T) {
	dir := withClips(t, map[string]float64{"chorus.mp4": 6})
	s := blenderScript().WithSources(dir)
	s.Pieces[1].Background = &PieceBackground{Clip: "chorus.mp4"}
	err := s.Validate(Catalog{Builtin: BuiltinParts()}, testMap(), good())
	if err == nil || !strings.Contains(err.Error(), `p2`) || !strings.Contains(err.Error(), "3D piece") {
		t.Fatalf("%v", err)
	}
}

func TestABackgroundOnlyCostsWhatItIsUsedFor(t *testing.T) {
	// a script with no background does not look at sources at all: the probe is never asked
	old := ProbeClip
	ProbeClip = func(ctx context.Context, path string) (ClipInfo, error) {
		t.Fatalf("the probe was asked about %s", path)
		return ClipInfo{}, nil
	}
	defer func() { ProbeClip = old }()
	if err := testScript().Validate(Catalog{Builtin: BuiltinParts()}, testMap(), good()); err != nil {
		t.Fatal(err)
	}
}

func TestTheClipInfoIsReadFromWhatFFprobeSays(t *testing.T) {
	got, err := parseClipInfo([]byte(`{"streams":[{"width":832,"height":480,"avg_frame_rate":"16/1","r_frame_rate":"16/1"}],"format":{"duration":"6.062000"}}`))
	if err != nil || got.Seconds != 6.062 || got.Width != 832 || got.Height != 480 || got.FPS != 16 {
		t.Fatalf("%+v %v", got, err)
	}
	got, err = parseClipInfo([]byte(`{"streams":[{"width":1280,"height":720,"avg_frame_rate":"0/0","r_frame_rate":"30000/1001"}],"format":{"duration":"2"}}`))
	if err != nil || got.FPS < 29.9 || got.FPS > 30 {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := parseClipInfo([]byte(`{"streams":[],"format":{"duration":"2"}}`)); err != ErrNoVideoStream {
		t.Fatalf("a file with no picture: %v", err)
	}
	if _, err := parseClipInfo([]byte(`{"streams":[{"width":1,"height":1}],"format":{}}`)); err == nil {
		t.Fatal("a file with no length")
	}
}

func TestClipNames(t *testing.T) {
	for _, ok := range []string{"a.mp4", "Intro-2.MOV", "x_y.z.webm", "clip.mkv"} {
		if !ValidClipName(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "a", "a.mp3", "../a.mp4", "a/b.mp4", ".hidden.mp4", "a b.mp4", strings.Repeat("a", 70) + ".mp4"} {
		if ValidClipName(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestAPieceOverAVideoCostsMoreToRenderThanTheSamePieceAlone(t *testing.T) {
	parts := map[string]Part{"toggle": {Manifest: Manifest{Name: "toggle", CostMS: 60}}, "stamp": {Manifest: Manifest{Name: "stamp", CostMS: 60}}}
	alone := testScript()
	over := testScript()
	over.Video = []VideoItem{vitem("v", 10, 20, "a.mp4")} // over the second piece only
	a, b := EstimateRenderWith(alone, parts, 30, 1280, 720, nil), EstimateRenderWith(over, parts, 30, 1280, 720, nil)
	// the second piece is 10 s at 30 fps: 300 frames, each DefaultBackgroundCostMS more; the item is built once and the stack lays 20 s of video
	extra := float64(300)*DefaultBackgroundCostMS/1000 + 10*TrackItemRate + perSceneOverheadSeconds + 20*StackRate
	if got := b.Seconds - a.Seconds; got < extra-0.2 || got > extra+0.2 {
		t.Fatalf("a track adds %.1f s, want about %.1f s (%v vs %v)", got, extra, a, b)
	}
	if a.Frames != b.Frames {
		t.Fatal("the frames are the same")
	}
	// the background of a piece counts as the item it becomes
	short := testScript()
	short.Pieces[1].Background = &PieceBackground{Clip: "a.mp4"}
	if c := EstimateRenderWith(short, parts, 30, 1280, 720, nil); c.Seconds != b.Seconds {
		t.Fatalf("the shorthand is estimated as its item: %v vs %v", c.Seconds, b.Seconds)
	}
	// a script with no track is estimated as it was
	if EstimateRenderWith(testScript(), parts, 30, 1280, 720, nil) != a {
		t.Fatal("unchanged")
	}
}

func TestTheSecondOfTheFileThatIsOnScreen(t *testing.T) {
	cases := []struct {
		name        string
		b           PieceBackground
		scene, file float64
		at, want    float64
		wantErr     bool
	}{
		{"the whole file, no change", PieceBackground{}, 4, 4, 1, 1, false},
		{"the end of the scene", PieceBackground{}, 4, 4, 3.9, 3.9, false},
		{"a file longer than the scene is cut, not sped up", PieceBackground{}, 4, 10, 3, 3, false},
		{"a short file is slowed down to 2x and repeated: 2 s in a 6 s scene", PieceBackground{}, 6, 2, 2, 1, false},
		{"...its second time round", PieceBackground{}, 6, 2, 4, 0, false},
		{"...and a bit further", PieceBackground{}, 6, 2, 5, 0.5, false},
		{"a file a little short is slowed by what fills the scene", PieceBackground{}, 5, 4, 2.5, 2, false},
		{"a part of the file", PieceBackground{ClipStart: f64(1), ClipEnd: f64(3)}, 2, 10, 1, 2, false},
		{"a part that is repeated at its own speed", PieceBackground{ClipStart: f64(1), ClipEnd: f64(3), ClipSpeed: f64(1)}, 10, 10, 3, 2, false},
		{"a chosen speed: half", PieceBackground{ClipStart: f64(1), ClipEnd: f64(3), ClipSpeed: f64(0.5)}, 10, 10, 2, 2, false},
		{"a chosen speed: half, then it starts again", PieceBackground{ClipStart: f64(1), ClipEnd: f64(3), ClipSpeed: f64(0.5)}, 10, 10, 4, 1, false},
		{"a chosen speed: double", PieceBackground{ClipSpeed: f64(2)}, 10, 6, 2, 4, false},
		{"a chosen speed: double, wrapping the file", PieceBackground{ClipSpeed: f64(2)}, 10, 6, 3, 0, false},
		{"an end past the file is the end of the file", PieceBackground{ClipStart: f64(1), ClipEnd: f64(99)}, 2, 3, 1.5, 2.5, false},
		{"a start only (the part is 4 s, the scene 4 s: no change of speed)", PieceBackground{ClipStart: f64(2)}, 4, 6, 1, 3, false},
		{"a start only, in a longer scene: slowed by 1.5", PieceBackground{ClipStart: f64(2)}, 6, 6, 1.5, 3, false},
		{"before the start", PieceBackground{}, 4, 4, -1, 0, false},
		{"an empty part", PieceBackground{ClipStart: f64(3), ClipEnd: f64(3)}, 4, 6, 1, 0, true},
		{"a start past the file", PieceBackground{ClipStart: f64(9)}, 4, 6, 1, 0, true},
	}
	for _, c := range cases {
		c.b.Clip = "a.mp4"
		got, err := c.b.ClipTime(c.at, c.scene, c.file)
		if (err != nil) != c.wantErr || (err == nil && (got < c.want-1e-9 || got > c.want+1e-9)) {
			t.Errorf("%s: ClipTime(t=%v, scene=%v, file=%v) = %v, %v; want %v (error %v)", c.name, c.at, c.scene, c.file, got, err, c.want, c.wantErr)
		}
	}
}

func TestTheSceneOfAPieceLastsAsLongAsItsFadeIntoTheNextOne(t *testing.T) {
	s := testScript()
	d := good()
	d.TransitionSeconds = 0
	if got := SceneSeconds(s, d, 0); got != 10 {
		t.Errorf("with cuts a scene is its piece: %v", got)
	}
	d.TransitionSeconds = 0.5
	if got := SceneSeconds(s, d, 0); got != 10.5 {
		t.Errorf("a scene is longer by the fade into the next piece: %v", got)
	}
	if got := SceneSeconds(s, d, 1); got != 10 {
		t.Errorf("the last has none: %v", got)
	}
}

func carryCatalog() Catalog { return Catalog{Builtin: BuiltinParts()} }

func TestANewScriptKeepsTheTrackAsItWas(t *testing.T) {
	s := testScript() // pieces 0-10 and 10-20
	track := []VideoItem{vitem("a", 4, 12, "a.mp4"), vitem("b", 14, 20, "b.mp4")}
	notes := KeepTrack(&s, track, carryCatalog(), LevelSimple)
	if len(s.Video) != 2 || s.Video[0].ID != "a" || s.Video[1].ID != "b" || s.Video[0].Start != 4 || s.Video[1].End != 20 {
		t.Fatalf("%+v", s.Video)
	}
	if len(notes) != 0 {
		t.Fatalf("toggle and stamp leave a video visible: nothing was changed: %v", notes)
	}
	// the track given is not shared with the script
	track[0].Start = 99
	if s.Video[0].Start == 99 {
		t.Fatal("the script has its own copy")
	}
}

func TestThePiecesOverTheTrackAreMadeToLeaveItVisible(t *testing.T) {
	cat := carryCatalog()
	s := testScript()
	s.Pieces[0].Part, s.Pieces[0].Params = "sunburst", nil // fills the frame
	s.Pieces[0].With = []PartRef{{Part: "synth-sun"}, {Part: "stamp"}}
	s.Pieces[0].Stage = &Stage{Background: "stage"}
	s.Layers = []Layer{{ID: "l1", Items: []LayerItem{{Part: "sunburst", Start: 0, End: 5}, {Part: "grain", Start: 5, End: 10}, {Part: "sunburst", Start: 12, End: 18}}}}
	notes := KeepTrack(&s, []VideoItem{vitem("v", 2, 8, "a.mp4")}, cat, LevelSimple)
	if main, _ := cat.Get(s.Pieces[0].Part); !main.OverVideo {
		t.Errorf("the main part %q does not leave the video visible", s.Pieces[0].Part)
	}
	if len(s.Pieces[0].With) != 1 || s.Pieces[0].With[0].Part != "stamp" || s.Pieces[0].Stage.Background != "" {
		t.Errorf("extras and stage: %+v %+v", s.Pieces[0].With, s.Pieces[0].Stage)
	}
	// the item over the piece that fills the frame goes; the one over the other piece, which has no video, stays; the grain stays
	if len(s.Layers[0].Items) != 2 || s.Layers[0].Items[0].Part != "grain" || s.Layers[0].Items[1].Start != 12 {
		t.Errorf("layers: %+v", s.Layers[0].Items)
	}
	if len(notes) != 4 {
		t.Errorf("each change is named: %v", notes)
	}
	for _, n := range notes {
		if !strings.Contains(n, `"v"`) && !strings.Contains(n, "layer") {
			t.Errorf("a note names the item or the layer: %q", n)
		}
	}
	dir := withClips(t, map[string]float64{"a.mp4": 10})
	if err := s.WithSources(dir).Validate(cat, testMap(), good()); err != nil {
		t.Fatalf("what comes out is a valid script: %v", err)
	}
}

func TestNothingIsKeptWhenThereIsNoTrack(t *testing.T) {
	s := testScript()
	before, _ := json.Marshal(s)
	if notes := KeepTrack(&s, nil, carryCatalog(), LevelSimple); len(notes) != 0 {
		t.Fatal(notes)
	}
	if after, _ := json.Marshal(s); string(after) != string(before) {
		t.Fatal("the script changed")
	}
}

func TestTheFixedRuleUsesOnlyPartsThatLeaveTheVideoVisibleWhereOneWillPlay(t *testing.T) {
	cat := carryCatalog()
	m := testMap()
	in := ScriptInput{Map: m, Design: good(), Level: LevelAdvanced, Cat: cat}
	plain, err := FixedScript(in)
	if err != nil {
		t.Fatal(err)
	}
	// a video over the whole song
	in.Video = []VideoItem{vitem("all", 0, m.Duration, "a.mp4")}
	with, err := FixedScript(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range with.Pieces {
		if part, _ := cat.Get(p.Part); !part.OverVideo {
			t.Errorf("piece %s uses %q, which fills the frame", p.ID, p.Part)
		}
	}
	// without a track the rule is what it was
	again, _ := FixedScript(ScriptInput{Map: m, Design: good(), Level: LevelAdvanced, Cat: cat})
	a, _ := json.Marshal(again)
	b, _ := json.Marshal(plain)
	if string(a) != string(b) {
		t.Fatal("the fixed rule changed for a script with no track")
	}
}

func TestTheAssistantIsToldWhereAVideoPlaysAndWhichPartsLeaveItVisible(t *testing.T) {
	in := ScriptInput{Map: testMap(), Design: good(), Level: LevelAdvanced, Cat: carryCatalog()}
	plain := scriptUser(in, "")
	if strings.Contains(plain, "over_video") || strings.Contains(plain, "A video plays") {
		t.Fatal("the prompt of a script with no track changed")
	}
	in.Video = []VideoItem{vitem("a", 4, 12, "a.mp4")}
	told := scriptUser(in, "")
	if !strings.Contains(told, "A video plays behind these stretches of the song: 4.0 s to 12.0 s") || !strings.Contains(told, "- stamp [over_video]:") || strings.Contains(told, "- sunburst [over_video]") {
		t.Fatalf("%s", told)
	}
}

// The assistant's script is shaped to fit a video: its pieces over a video are given parts that leave it visible, whatever it chose, the track
// is kept as it was, and each change is named.
func TestAnAssistantScriptOverAVideoIsMadeToLeaveItVisible(t *testing.T) {
	dir := withClips(t, map[string]float64{"a.mp4": 40})
	x := in(LevelAdvanced)
	x.Sources = dir
	x.Video = []VideoItem{{ID: "v1", Clip: "a.mp4", Start: 30, End: 45}}
	// the assistant chooses a part that fills the frame for the chorus
	answer := `{"pieces":[
 {"section":"Intro","start":0,"end":12,"part":"glass-card","params":{"title":"Intro"}},
 {"section":"Verse","start":12,"end":30,"part":"terminal","lines":[{"line":0,"keyword":"line"},{"line":1,"keyword":"line"}]},
 {"section":"Chorus","start":30,"end":45,"part":"sunburst","lines":[{"line":2,"keyword":"allow"}],"hit":{"word":"ALLOW","at":30.2,"length":0.8}},
 {"section":"Verse","start":45,"end":60,"part":"flat-slabs","lines":[{"line":3,"keyword":"verse"}]}]}`
	w := &fakeWriter{answers: []string{answer}}
	p, err := ProposeScript(context.Background(), w, x)
	if err != nil {
		t.Fatal(err)
	}
	if p.Source != "assistant" {
		t.Fatalf("%s: %s", p.Source, p.FallbackReason)
	}
	if len(p.Script.Video) != 1 || p.Script.Video[0].ID != "v1" || p.Script.Video[0].Start != 30 {
		t.Fatalf("the track is kept: %+v", p.Script.Video)
	}
	chorus := p.Script.Pieces[2]
	if part, _ := x.Cat.Get(chorus.Part); !part.OverVideo {
		t.Fatalf("the chorus uses %q, which hides the video", chorus.Part)
	}
	if !strings.Contains(strings.Join(p.Warnings, " | "), `"sunburst", which would hide it`) {
		t.Fatalf("the change is named: %v", p.Warnings)
	}
	if !strings.Contains(w.users[0], "A video plays behind these stretches of the song: 30.0 s to 45.0 s") {
		t.Fatalf("the assistant is told where the video plays:\n%s", w.users[0])
	}
}

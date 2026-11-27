package musicmotion

import (
	"encoding/json"
	"strings"
	"testing"
)

func vitem(id string, start, end float64, clip string) VideoItem {
	return VideoItem{ID: id, Clip: clip, Start: start, End: end}
}

func trackScript(dir string, items ...VideoItem) Script {
	s := testScript().WithSources(dir)
	s.Video = items
	return s
}

func TestAVideoTrackIsValid(t *testing.T) {
	dir := withClips(t, map[string]float64{"city.mp4": 30, "sea.mp4": 8})
	cat := Catalog{Builtin: BuiltinParts()}
	cases := map[string][]VideoItem{
		"a clip over two pieces":       {vitem("v1", 4, 16, "city.mp4")},
		"two clips in one piece":       {vitem("a", 0, 5, "city.mp4"), vitem("b", 5, 10, "sea.mp4")},
		"a gap before, between, after": {vitem("a", 2, 6, "city.mp4"), vitem("b", 12, 15, "sea.mp4")},
		"the whole song":               {vitem("all", 0, 20, "city.mp4")},
		"an item with all its fields":  {{ID: "x", Clip: "city.mp4", Start: 0, End: 10, ClipStart: f64(1), ClipEnd: f64(9), ClipSpeed: f64(0.5), Scrim: f64(0.4), TransitionSeconds: f64(0.5)}},
		"touching items with a fade":   {vitem("a", 0, 10, "city.mp4"), {ID: "b", Clip: "sea.mp4", Start: 10, End: 20, TransitionSeconds: f64(1)}},
		"nothing":                      nil,
	}
	for name, items := range cases {
		if err := trackScript(dir, items...).Validate(cat, testMap(), good()); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestEachBadVideoItemIsRefusedAndNamed(t *testing.T) {
	dir := withClips(t, map[string]float64{"city.mp4": 30, "sea.mp4": 8})
	cat := Catalog{Builtin: BuiltinParts()}
	cases := []struct {
		name  string
		items []VideoItem
		edit  func(*Script)
		want  string
	}{
		{"overlap", []VideoItem{vitem("a", 0, 12, "city.mp4"), vitem("b", 10, 20, "sea.mp4")}, nil, "overlaps item #1"},
		{"out of order", []VideoItem{vitem("a", 10, 20, "city.mp4"), vitem("b", 0, 5, "sea.mp4")}, nil, "overlaps item #1"},
		{"outside the song", []VideoItem{vitem("a", 15, 25, "city.mp4")}, nil, "outside the song"},
		{"before the song", []VideoItem{vitem("a", -2, 5, "city.mp4")}, nil, "outside the song"},
		{"ends before it starts", []VideoItem{vitem("a", 5, 5, "city.mp4")}, nil, "ends before it starts"},
		{"a bad id", []VideoItem{vitem("A b", 0, 5, "city.mp4")}, nil, "the id must be"},
		{"a repeated id", []VideoItem{vitem("a", 0, 5, "city.mp4"), vitem("a", 6, 8, "city.mp4")}, nil, "repeated"},
		{"a clip the piece does not keep", []VideoItem{vitem("a", 0, 5, "missing.mp4")}, nil, `no clip "missing.mp4"`},
		{"a trim past the file", []VideoItem{{ID: "a", Clip: "sea.mp4", Start: 0, End: 5, ClipEnd: f64(9)}}, nil, "lasts 8.00 s"},
		{"a bad speed", []VideoItem{{ID: "a", Clip: "sea.mp4", Start: 0, End: 5, ClipSpeed: f64(9)}}, nil, "clip_speed"},
		{"a bad scrim", []VideoItem{{ID: "a", Clip: "sea.mp4", Start: 0, End: 5, Scrim: f64(0.95)}}, nil, "scrim"},
		{"a negative transition", []VideoItem{{ID: "a", Clip: "sea.mp4", Start: 0, End: 5, TransitionSeconds: f64(-1)}}, nil, "transition_seconds"},
		{"a transition longer than the shorter item", []VideoItem{vitem("a", 0, 10, "city.mp4"), {ID: "b", Clip: "sea.mp4", Start: 10, End: 12, TransitionSeconds: f64(3)}}, nil, "shorter of the two"},
		{"a part that fills the frame under an item", []VideoItem{vitem("a", 4, 12, "city.mp4")}, func(s *Script) { s.Pieces[1].Part, s.Pieces[1].Mode = "sunburst", "" }, `"sunburst"`},
		{"an extra that fills the frame", []VideoItem{vitem("a", 0, 5, "city.mp4")}, func(s *Script) { s.Pieces[0].With = []PartRef{{Part: "synth-sun"}} }, `"synth-sun"`},
		{"a stage background", []VideoItem{vitem("a", 0, 5, "city.mp4")}, func(s *Script) { s.Pieces[0].Stage = &Stage{Background: "stage"} }, "stage background"},
		{"a 3D piece under an item", []VideoItem{vitem("a", 12, 18, "city.mp4")}, func(s *Script) {
			s.Pieces[1] = blenderScript().Pieces[1]
		}, "3D piece"},
	}
	for _, c := range cases {
		s := trackScript(dir, c.items...)
		if c.edit != nil {
			c.edit(&s)
		}
		err := s.Validate(cat, testMap(), good())
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error with %q", c.name, err, c.want)
		}
	}
	// the most items there are
	var many []VideoItem
	for i := 0; i < MaxVideoItems+1; i++ {
		many = append(many, vitem("i"+strings.Repeat("a", i%5)+string(rune('a'+i/5)), float64(i)*0.2, float64(i)*0.2+0.1, "sea.mp4"))
	}
	if err := trackScript(dir, many...).Validate(cat, testMap(), good()); err == nil || !strings.Contains(err.Error(), "up to 64") {
		t.Errorf("65 items: %v", err)
	}
}

func TestAPieceHoldsTheItemsThatTouchItAndOnlyThose(t *testing.T) {
	s := testScript()
	s.Video = []VideoItem{vitem("a", 4, 12, "x.mp4"), vitem("b", 12, 20, "y.mp4")}
	first, second := s.ItemsOver(s.Pieces[0]), s.ItemsOver(s.Pieces[1])
	if len(first) != 1 || first[0].ID != "a" || len(second) != 2 {
		t.Fatalf("%+v %+v", first, second)
	}
	// an item that only ends where the piece starts does not touch it (a rounding is not a touch)
	s.Video = []VideoItem{vitem("c", 0, 10, "x.mp4")}
	if len(s.ItemsOver(s.Pieces[1])) != 0 {
		t.Fatal("it ends where the second piece begins")
	}
}

func TestTheBackgroundOfAPieceBecomesAnItemOfTheTrack(t *testing.T) {
	s := testScript()
	s.Pieces[1].Background = &PieceBackground{Clip: "a.mp4", ClipStart: f64(1), Scrim: f64(0.4)}
	got, err := s.WithTrack()
	if err != nil {
		t.Fatal(err)
	}
	if got.Pieces[1].Background != nil || len(got.Video) != 1 {
		t.Fatalf("%+v %+v", got.Pieces[1].Background, got.Video)
	}
	it := got.Video[0]
	if it.ID != "bg-p2" || it.Start != 10 || it.End != 20 || it.Clip != "a.mp4" || *it.ClipStart != 1 || *it.Scrim != 0.4 {
		t.Fatalf("%+v", it)
	}
	if s.Pieces[1].Background == nil {
		t.Fatal("the script that was given is not changed")
	}
	// saying it again replaces the item of its own id
	again := got
	again.Pieces = append([]Piece(nil), got.Pieces...)
	again.Pieces[1].Background = &PieceBackground{Clip: "b.mp4"}
	back, err := again.WithTrack()
	if err != nil || len(back.Video) != 1 || back.Video[0].Clip != "b.mp4" {
		t.Fatalf("%v %+v", err, back.Video)
	}
	// an item of the track in its place is refused, naming both
	clash := testScript()
	clash.Pieces[1].Background = &PieceBackground{Clip: "a.mp4"}
	clash.Video = []VideoItem{vitem("mine", 15, 18, "z.mp4")}
	if _, err := clash.WithTrack(); err == nil || !strings.Contains(err.Error(), `"mine"`) || !strings.Contains(err.Error(), `"p2"`) {
		t.Fatalf("%v", err)
	}
	// the items stay in order
	two := testScript()
	two.Pieces[0].Background = &PieceBackground{Clip: "a.mp4"}
	two.Video = []VideoItem{vitem("late", 12, 18, "z.mp4")}
	got2, err := two.WithTrack()
	if err != nil || len(got2.Video) != 2 || got2.Video[0].ID != "bg-p1" || got2.Video[1].ID != "late" {
		t.Fatalf("%v %+v", err, got2.Video)
	}
}

func TestAScriptWithNoBackgroundOrTrackIsUnchangedByWithTrack(t *testing.T) {
	s := testScript()
	got, err := s.WithTrack()
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(s)
	b, _ := json.Marshal(got)
	if string(a) != string(b) || strings.Contains(string(a), `"video"`) || strings.Contains(string(a), "background") {
		t.Fatalf("%s\n%s", a, b)
	}
}

func TestAVideoTrackSurvivesTheFileAndIsValidatedFromTheShorthand(t *testing.T) {
	dir := withClips(t, map[string]float64{"city.mp4": 30})
	s := trackScript(dir, vitem("v1", 4, 16, "city.mp4"))
	raw, _ := json.Marshal(s)
	var back Script
	if err := json.Unmarshal(raw, &back); err != nil || len(back.Video) != 1 || back.Video[0].ID != "v1" || back.Video[0].End != 16 {
		t.Fatalf("%v %s", err, raw)
	}
	// the shorthand is checked as the item it becomes: the same refusals, named by the item
	short := testScript().WithSources(dir)
	short.Pieces[1].Part = "blank"
	short.Pieces[1].Background = &PieceBackground{Clip: "city.mp4"}
	if err := short.Validate(Catalog{Builtin: BuiltinParts()}, testMap(), good()); err != nil {
		t.Fatal(err)
	}
	short.Pieces[1].Background.ClipEnd = f64(99)
	if err := short.Validate(Catalog{Builtin: BuiltinParts()}, testMap(), good()); err == nil || !strings.Contains(err.Error(), "bg-p2") {
		t.Fatalf("%v", err)
	}
}

func TestTheSecondOfTheFileForAnItemOfTheTrack(t *testing.T) {
	it := VideoItem{ID: "a", Clip: "c.mp4", Start: 10, End: 20}
	// a 40 s file in a 10 s item plays at its own speed from its start
	if got, err := it.ClipTime(14, 10, 40); err != nil || got != 4 {
		t.Fatalf("%v %v", got, err)
	}
	// a 4 s file in a 10 s item is slowed 2x and repeated: 6 s into the item it is at 3 s of the file, and at 8 s it starts again
	if got, _ := it.ClipTime(16, 10, 4); got != 3 {
		t.Fatalf("6 s into the item: %v", got)
	}
	if got, _ := it.ClipTime(18, 10, 4); got != 0 {
		t.Fatalf("at the wrap the file starts again: %v", got)
	}
	// the build length includes the fade into the next item when the two touch
	items := []VideoItem{{ID: "a", Start: 0, End: 6}, {ID: "b", Start: 6, End: 12, TransitionSeconds: f64(1)}, {ID: "c", Start: 14, End: 18, TransitionSeconds: f64(1)}}
	for i, want := range []float64{7, 6, 4} { // c does not touch b: a gap between them
		if got := TrackBuildSeconds(items, i); got != want {
			t.Errorf("item %d is built %v s, want %v", i, got, want)
		}
	}
}

func TestSplittingAnItemKeepsThePictureWhereItWasAndJoiningGivesTheOriginalBack(t *testing.T) {
	// a 30 s file under 20 s of the song, at its own speed, with a part chosen
	whole := VideoItem{ID: "v", Clip: "c.mp4", Start: 10, End: 30, ClipStart: f64(2), ClipEnd: f64(28), Scrim: f64(0.4), TransitionSeconds: f64(0.5)}
	a, b, err := SplitItem(whole, 18, 30, [2]string{})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "v-a" || b.ID != "v-b" || a.Start != 10 || a.End != 18 || b.Start != 18 || b.End != 30 {
		t.Fatalf("%+v %+v", a, b)
	}
	if a.TransitionSeconds == nil || b.TransitionSeconds != nil {
		t.Fatal("the fade into the whole stays on the first part, the cut has none")
	}
	// the picture at the cut is the one the whole showed there: no jump
	wantAt18, _ := whole.ClipTime(18, whole.Seconds(), 30)
	gotA, _ := a.ClipTime(17.99, a.Seconds(), 30)
	gotB, _ := b.ClipTime(18, b.Seconds(), 30)
	if wantAt18 != 10 || gotB < wantAt18-0.001 || gotB > wantAt18+0.001 || gotA > gotB || gotB-gotA > 0.05 {
		t.Fatalf("the whole shows %v at 18 s, the parts %v then %v", wantAt18, gotA, gotB)
	}
	// and both play at the speed of the whole
	for _, p := range []VideoItem{a, b} {
		if p.ClipSpeed == nil || *p.ClipSpeed != 1 || *p.Scrim != 0.4 || *p.ClipEnd != 28 {
			t.Fatalf("%+v", p)
		}
	}
	// joined again: the original, with its automatic speed
	back, err := JoinItems(a, b, 30)
	if err != nil {
		t.Fatal(err)
	}
	if back.ID != "v" || back.Start != 10 || back.End != 30 || back.ClipSpeed != nil || *back.ClipStart != 2 || *back.TransitionSeconds != 0.5 {
		t.Fatalf("%+v", back)
	}
	// a short file that is slowed down by the whole keeps the same slowing in the parts
	short := VideoItem{ID: "s", Clip: "c.mp4", Start: 0, End: 8}
	sa, sb, err := SplitItem(short, 4, 4, [2]string{"s1", "s2"})
	if err != nil || sa.ID != "s1" || sb.ID != "s2" || *sa.ClipSpeed != 0.5 || *sb.ClipSpeed != 0.5 {
		t.Fatalf("%v %+v %+v", err, sa, sb)
	}
	if *sb.ClipStart != 2 { // 4 s of the song at half speed: 2 s of the file
		t.Fatalf("the second part starts at %v", *sb.ClipStart)
	}
	if again, err := JoinItems(sa, sb, 4); err != nil || again.ClipSpeed != nil || again.ID != "s1" {
		t.Fatalf("%v %+v", err, again)
	}
}

func TestACutHasToBeInsideTheItemAndAJoinHasToBeContinuous(t *testing.T) {
	it := VideoItem{ID: "v", Clip: "c.mp4", Start: 10, End: 20}
	for _, at := range []float64{10, 20, 9, 25} {
		if _, _, err := SplitItem(it, at, 30, [2]string{}); err == nil {
			t.Errorf("a cut at %v is not inside the item", at)
		}
	}
	a := VideoItem{ID: "a", Clip: "x.mp4", Start: 0, End: 5}
	for name, b := range map[string]VideoItem{
		"another clip":   {ID: "b", Clip: "y.mp4", Start: 5, End: 10},
		"a gap":          {ID: "b", Clip: "x.mp4", Start: 6, End: 10},
		"not continuous": {ID: "b", Clip: "x.mp4", Start: 5, End: 10, ClipStart: f64(0)},
		"another end":    {ID: "b", Clip: "x.mp4", Start: 5, End: 10, ClipStart: f64(5), ClipEnd: f64(9)},
		"another speed":  {ID: "b", Clip: "x.mp4", Start: 5, End: 10, ClipStart: f64(5), ClipSpeed: f64(2)},
	} {
		if _, err := JoinItems(a, b, 30); err == nil {
			t.Errorf("%s: joined", name)
		}
	}
	// continuous: the second starts where the first stops
	if _, err := JoinItems(a, VideoItem{ID: "b", Clip: "x.mp4", Start: 5, End: 10, ClipStart: f64(5)}, 30); err != nil {
		t.Fatal(err)
	}
}

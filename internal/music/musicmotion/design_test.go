package musicmotion

import (
	"context"
	"errors"
	"strings"
	"testing"

	"veritmo/internal/project/pipeline"
)

func good() Design {
	return Design{Dark: "#0b0f14", Light: "#e8f1ec", Accent: "#2bff88", Display: "heavy", Body: "mono",
		DefaultMode: ModeDark, Modes: map[string]string{"chorus": ModeDark}, Concept: "a terminal that writes the song"}
}

func TestAGoodDesignIsValid(t *testing.T) {
	if err := good().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestEachBadFieldIsRefusedAndNamed(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Design)
		want string
	}{
		{"colour not hex", func(d *Design) { d.Accent = "green" }, "design.accent"},
		{"short hex is fine but a bad one is not", func(d *Design) { d.Dark = "#12" }, "design.dark"},
		{"tones too close", func(d *Design) { d.Light = "#222222" }, "contrast"},
		{"unknown font", func(d *Design) { d.Display = "comic" }, "design.display"},
		{"unknown body font", func(d *Design) { d.Body = "" }, "design.body"},
		{"bad default mode", func(d *Design) { d.DefaultMode = "blue" }, "design.default_mode"},
		{"bad section mode", func(d *Design) { d.Modes["verse"] = "grey" }, `design.modes["verse"]`},
		{"concept too long", func(d *Design) { d.Concept = strings.Repeat("x", MaxConcept+1) }, "design.concept"},
		// A mid-grey accent against the dark background of a dark section is too weak.
		{"accent too faint on the dark background", func(d *Design) { d.Accent = "#1c2430" }, "design.accent"},
		// ... and so is a pale accent on the light background of a light section.
		{"accent too faint on the light background", func(d *Design) { d.Accent = "#dfeee6"; d.Modes["chorus"] = ModeLight }, "design.accent"},
	}
	for _, c := range cases {
		d := good()
		c.edit(&d)
		err := d.Validate()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error naming %q", c.name, err, c.want)
		}
	}
}

func TestAnAccentOnlyNeedsContrastWithTheModesThatAreUsed(t *testing.T) {
	d := good()
	d.Accent = "#2bff88" // strong on dark, weak on the light tone
	d.DefaultMode = ModeDark
	d.Modes = nil
	if err := d.Validate(); err != nil {
		t.Fatalf("no section is light, so the light background does not matter: %v", err)
	}
	d.Modes = map[string]string{"chorus": ModeLight}
	if err := d.Validate(); err == nil || !strings.Contains(err.Error(), "light mode") {
		t.Fatalf("a light section makes the light background count: %v", err)
	}
}

func TestEveryFallbackPaletteIsValid(t *testing.T) {
	for _, p := range palettes {
		d := Fallback(p.words[0], []string{"Verse", "Chorus", "Bridge"})
		if err := d.Validate(); err != nil {
			t.Errorf("palette %s: %v", p.name, err)
		}
	}
}

func TestFallbackChoosesByStyleWordsAndIsStable(t *testing.T) {
	a := Fallback("Upbeat electronic dance", []string{"Verse", "Chorus"})
	if a.Accent != "#2bff88" {
		t.Fatalf("electronic style should give the neon palette: %+v", a)
	}
	if a.ModeFor("Chorus") != ModeDark || a.ModeFor("verse") != ModeDark {
		t.Fatalf("modes: %+v", a)
	}
	b := Fallback("a style with no known word", nil)
	c := Fallback("a style with no known word", nil)
	if b.Accent != c.Accent || b.Validate() != nil {
		t.Fatalf("the same style must give the same valid design: %+v %+v", b, c)
	}
	if got := Fallback("rock", []string{"Chorus"}); got.ModeFor("Chorus") != ModeLight || got.ModeFor("Verse") != ModeDark {
		t.Fatalf("the chorus of a rock song flips: %+v", got)
	}
}

type fakeWriter struct {
	answers []string
	err     error
	calls   int
	users   []string
}

func (f *fakeWriter) Write(ctx context.Context, system, user string, maxTokens int) (string, error) {
	f.users = append(f.users, user)
	if f.err != nil {
		return "", f.err
	}
	a := f.answers[min(f.calls, len(f.answers)-1)]
	f.calls++
	return a, nil
}

const goodJSON = `Here you go:
` + "```json" + `
{"dark":"#0b0f14","light":"#e8f1ec","accent":"#ff2e54","display":"heavy","body":"grotesk","default_mode":"dark","modes":{"Chorus":"dark"},"concept":"permission dialogs that cannot be refused"}
` + "```"

func TestAValidAnswerIsStoredAsTheDesign(t *testing.T) {
	w := &fakeWriter{answers: []string{goodJSON}}
	d, src := ProposeDesign(context.Background(), w, Song{Style: "pop", SectionTags: []string{"Verse", "Chorus"}})
	if src != "assistant" || d.Accent != "#ff2e54" || d.ModeFor("chorus") != ModeDark || !strings.Contains(d.Concept, "permission") {
		t.Fatalf("%s %+v", src, d)
	}
}

func TestAnInvalidAnswerIsAskedAgainWithTheReason(t *testing.T) {
	bad := `{"dark":"#0b0f14","light":"#e8f1ec","accent":"red","display":"heavy","body":"mono","default_mode":"dark","concept":"x"}`
	w := &fakeWriter{answers: []string{bad, goodJSON}}
	d, src := ProposeDesign(context.Background(), w, Song{Style: "pop"})
	if src != "assistant" || w.calls != 2 || d.Accent != "#ff2e54" {
		t.Fatalf("src=%s calls=%d %+v", src, w.calls, d)
	}
	if !strings.Contains(w.users[1], "design.accent") {
		t.Fatalf("the second request must carry the reason: %q", w.users[1])
	}
}

func TestThreeFailuresFallBackToAPalette(t *testing.T) {
	w := &fakeWriter{answers: []string{"no json at all"}}
	d, src := ProposeDesign(context.Background(), w, Song{Style: "metal rock"})
	if src != "fallback" || w.calls != Attempts || d.Validate() != nil {
		t.Fatalf("src=%s calls=%d %+v", src, w.calls, d)
	}
}

func TestNoAssistantOrABrokenOneFallsBackWithNoError(t *testing.T) {
	if _, src := ProposeDesign(context.Background(), nil, Song{Style: "pop"}); src != "fallback" {
		t.Fatal(src)
	}
	w := &fakeWriter{err: errors.New("connection refused")}
	if _, src := ProposeDesign(context.Background(), w, Song{Style: "pop"}); src != "fallback" || len(w.users) != 1 {
		t.Fatalf("an assistant that is not there is asked once: %s %d", src, len(w.users))
	}
}

func TestANewProposalDoesNotOverwriteAnEditedDesign(t *testing.T) {
	dir := t.TempDir()
	mine := good()
	mine.Accent = "#ff2e54"
	if _, err := SetDesign(dir, mine); err != nil {
		t.Fatal(err)
	}
	w := &fakeWriter{answers: []string{goodJSON}}
	st, err := Resolve(context.Background(), w, dir, Song{Style: "pop"}, false)
	if err != nil || !st.Edited || st.Design.Accent != "#ff2e54" || w.calls != 0 {
		t.Fatalf("a stored design must be kept without asking: %+v %v calls=%d", st, err, w.calls)
	}
	st, err = Resolve(context.Background(), w, dir, Song{Style: "pop"}, true)
	if err != nil || st.Edited || st.Source != "assistant" || w.calls != 1 {
		t.Fatalf("asking for a new proposal replaces it: %+v %v", st, err)
	}
	again, _ := LoadDesign(dir)
	if again.Source != "assistant" {
		t.Fatalf("the proposal must be stored: %+v", again)
	}
}

func TestAnInvalidEditedDesignIsRefusedAndNothingIsStored(t *testing.T) {
	dir := t.TempDir()
	bad := good()
	bad.Accent = "nope"
	if _, err := SetDesign(dir, bad); err == nil {
		t.Fatal("expected a refusal")
	}
	if _, ok := LoadDesign(dir); ok {
		t.Fatal("nothing must be stored")
	}
}

// --- the choice of a part by the model ---------------------------------------------------------------

func choiceRequest() pipeline.PartChoiceRequest {
	return pipeline.PartChoiceRequest{Description: "a search box that types a question and lists suggestions", Narration: "Ask anything.", Duration: 8, Concept: "a terminal that keeps writing"}
}

func TestAValidChoiceIsUsedAndTheModelIsToldTheCatalogAndTheScene(t *testing.T) {
	w := &fakeWriter{answers: []string{"Sure:\n" + "```json\n" + `{"part":"search-box","params":{"query":"what is a beat","type_beats":3},"text":"ask anything"}` + "\n```"}}
	c, err := ChoosePart(context.Background(), w, Catalog{Builtin: BuiltinParts()}, choiceRequest())
	if err != nil || c.Part != "search-box" || c.Params["query"] != "what is a beat" || c.Text != "ask anything" || w.calls != 1 {
		t.Fatalf("%+v %v calls=%d", c, err, w.calls)
	}
	for _, want := range []string{"search-box", "a search box that types a question", "Ask anything.", "about 8 seconds", "a terminal that keeps writing", "type_beats (int, default 4, from 1 to 12"} {
		if !strings.Contains(w.users[0], want) {
			t.Errorf("the model is told %q:\n%s", want, w.users[0])
		}
	}
	if strings.Contains(w.users[0], "- blank:") {
		t.Error("a part that is only for a stage is not offered")
	}
}

func TestAnInvalidChoiceIsAskedAgainWithTheReason(t *testing.T) {
	bad := `{"part":"hologram"}`
	good := `{"part":"counter","params":{"from":3,"step":2}}`
	w := &fakeWriter{answers: []string{bad, good}}
	c, err := ChoosePart(context.Background(), w, Catalog{Builtin: BuiltinParts()}, choiceRequest())
	if err != nil || c.Part != "counter" || w.calls != 2 || !strings.Contains(w.users[1], "hologram") {
		t.Fatalf("%+v %v calls=%d\n%v", c, err, w.calls, w.users)
	}
}

func TestEachBadChoiceIsRefusedWithAReason(t *testing.T) {
	cases := map[string]string{
		"no json":               "I would use a counter",
		"a part that is not":    `{"part":"hologram"}`,
		"a parameter not its":   `{"part":"toggle","params":{"colour":"red"}}`,
		"a parameter too large": `{"part":"toggle","params":{"count":99}}`,
		"a part for the stage":  `{"part":"blank"}`,
		"a text too long":       `{"part":"counter","text":"` + strings.Repeat("x", 130) + `"}`,
	}
	for name, answer := range cases {
		w := &fakeWriter{answers: []string{answer}}
		_, err := ChoosePart(context.Background(), w, Catalog{Builtin: BuiltinParts()}, choiceRequest())
		if err == nil || w.calls != Attempts {
			t.Errorf("%s: %v after %d answers", name, err, w.calls)
		}
	}
}

func TestThreeFailuresGiveAnErrorThatSaysWhyAndNoModelIsAnError(t *testing.T) {
	w := &fakeWriter{answers: []string{`{"part":"hologram"}`}}
	_, err := ChoosePart(context.Background(), w, Catalog{Builtin: BuiltinParts()}, choiceRequest())
	if err == nil || !strings.Contains(err.Error(), "hologram") || !strings.Contains(err.Error(), "3 answers") {
		t.Fatalf("%v", err)
	}
	if _, err := ChoosePart(context.Background(), nil, Catalog{Builtin: BuiltinParts()}, choiceRequest()); err == nil {
		t.Fatal("no model, no choice")
	}
	broken := &fakeWriter{err: errors.New("connection refused")}
	if _, err := ChoosePart(context.Background(), broken, Catalog{Builtin: BuiltinParts()}, choiceRequest()); err == nil || broken.calls != 0 || len(broken.users) != 1 {
		t.Fatalf("a model that is not there is asked once: %v", err)
	}
}

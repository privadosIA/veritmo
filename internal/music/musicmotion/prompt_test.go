package musicmotion

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"veritmo/internal/music/songmap"
)

// systemWriter keeps what the assistant is told, system and user, and answers in turn.
type systemWriter struct {
	answers []string
	err     error
	systems []string
	users   []string
}

func (w *systemWriter) Write(_ context.Context, system, user string, _ int) (string, error) {
	w.systems, w.users = append(w.systems, system), append(w.users, user)
	if w.err != nil {
		return "", w.err
	}
	return w.answers[min(len(w.users)-1, len(w.answers)-1)], nil
}

const promptText = "dark horror: glitchy red and black text, no synthwave"

func TestPrompt_ReachesTheDesignAndTheScriptOfTheAssistant(t *testing.T) {
	w := &systemWriter{answers: []string{goodJSON}}
	if _, src, why := ProposeDesignWhy(context.Background(), w, Song{Style: "pop", Prompt: promptText}); src != "assistant" || why != "" {
		t.Fatalf("%s %q", src, why)
	}
	if !strings.Contains(w.users[0], promptText) {
		t.Fatalf("the design request lacks the prompt:\n%s", w.users[0])
	}
	x := in(LevelSimple)
	x.Prompt = promptText
	s := &systemWriter{answers: []string{assistantScript}}
	p, err := ProposeScript(context.Background(), s, x)
	if err != nil || p.Source != "assistant" || p.FallbackReason != "" {
		t.Fatalf("%v %s %q", err, p.Source, p.FallbackReason)
	}
	if !strings.Contains(s.systems[0], promptText) {
		t.Fatalf("the script request lacks the prompt:\n%s", s.systems[0])
	}
}

func TestPrompt_WithoutOneTheTextIsWhatItWas(t *testing.T) {
	w := &systemWriter{answers: []string{goodJSON}}
	_, _, _ = ProposeDesignWhy(context.Background(), w, Song{Style: "pop"})
	if strings.Contains(w.users[0], "direction for this video") {
		t.Fatalf("no prompt, no block:\n%s", w.users[0])
	}
	s := &systemWriter{answers: []string{assistantScript}}
	_, _ = ProposeScript(context.Background(), s, in(LevelSimple))
	if strings.Contains(s.systems[0], "direction for this video") || strings.HasSuffix(s.systems[0], "\n") {
		t.Fatalf("no prompt, no block:\n%s", s.systems[0])
	}
}

func TestPrompt_AFallbackSaysWhy(t *testing.T) {
	// Refused three times: the last refusal.
	w := &systemWriter{answers: []string{"no json at all"}}
	_, src, why := ProposeDesignWhy(context.Background(), w, Song{Style: "pop"})
	if src != "fallback" || !strings.Contains(why, "refused") || strings.Contains(why, "prompt") {
		t.Fatalf("%s %q", src, why)
	}
	// The assistant failed: its error, and the note on the prompt.
	w = &systemWriter{err: errors.New("connection refused")}
	_, src, why = ProposeDesignWhy(context.Background(), w, Song{Style: "pop", Prompt: promptText})
	if src != "fallback" || !strings.Contains(why, "connection refused") || !strings.Contains(why, "does not read the prompt") {
		t.Fatalf("%s %q", src, why)
	}
	// No assistant: the reason the caller found.
	_, src, why = ProposeDesignWhy(context.Background(), nil, Song{Style: "pop", NoAssistant: "run run-1 is in progress"})
	if src != "fallback" || !strings.HasPrefix(why, "run run-1 is in progress") {
		t.Fatalf("%s %q", src, why)
	}
	// The same for the script.
	x := in(LevelSimple)
	x.Prompt = promptText
	p, err := ProposeScript(context.Background(), &systemWriter{answers: []string{"nothing useful"}}, x)
	if err != nil || p.Source != "fallback" || !strings.Contains(p.FallbackReason, "refused") || !strings.Contains(p.FallbackReason, "does not read the prompt") {
		t.Fatalf("%v %s %q", err, p.Source, p.FallbackReason)
	}
	x.Map = songmap.Slice(x.Map, 0, 60)
	p, _ = ProposeScript(context.Background(), nil, x)
	if p.Source != "fallback" || !strings.Contains(p.FallbackReason, "no assistant") && !strings.Contains(p.FallbackReason, "there is no assistant") {
		t.Fatalf("%s %q", p.Source, p.FallbackReason)
	}
}

func TestLimits_TheDesignTextStatesWhatTheAnswerIsCheckedAgainst(t *testing.T) {
	w := &systemWriter{answers: []string{goodJSON}}
	_, _, _ = ProposeDesignWhy(context.Background(), w, Song{Style: "pop"})
	for _, want := range []string{"4.5:1", "3:1", "EVERY mode", "both tones", "ONE mode"} {
		if !strings.Contains(w.systems[0], want) {
			t.Errorf("the design text lacks %q:\n%s", want, w.systems[0])
		}
	}
	if strings.Index(w.systems[0], "4.5:1") > strings.Index(w.systems[0], `"dark": a hex colour`) {
		t.Error("the contrast rules must come before the keys")
	}
}

func TestLimits_ARefusedContrastIsAskedAgainWithBothBackgrounds(t *testing.T) {
	// The accent of the real refusal: 1.8:1 against the light background.
	bad := `{"dark":"#0b0f14","light":"#d1d5db","accent":"#c084fc","display":"heavy","body":"grotesk","default_mode":"dark","modes":{"chorus":"light"},"concept":"x"}`
	w := &systemWriter{answers: []string{bad, goodJSON}}
	d, src, why := ProposeDesignWhy(context.Background(), w, Song{Style: "pop"})
	if src != "assistant" || why != "" || d.Accent != "#ff2e54" || len(w.users) != 2 {
		t.Fatalf("%s %q %d %+v", src, why, len(w.users), d)
	}
	second := w.users[1]
	for _, want := range []string{"#c084fc", `"dark" (#0b0f14)`, `"light" (#d1d5db)`, "1.8:1", "only one mode"} {
		if !strings.Contains(second, want) {
			t.Errorf("the second request lacks %q:\n%s", want, second)
		}
	}
	// The reason of a fallback keeps the plain refusal, with no hint. Two tones that leave no accent with 3:1 against both of them
	// (a grey and a light grey): the accent cannot be adjusted either, so the third answer is asked for and then the fixed palette.
	noRoom := strings.NewReplacer("#0b0f14", "#595959", "#d1d5db", "#d4d4d4").Replace(bad)
	w = &systemWriter{answers: []string{noRoom}}
	_, src, why = ProposeDesignWhy(context.Background(), w, Song{Style: "pop"})
	if src != "fallback" || !strings.Contains(why, "design.accent") || strings.Contains(why, "Choose another accent") {
		t.Fatalf("%s %q", src, why)
	}
}

func TestLimits_ARefusedParameterIsAskedAgainWithItsLimits(t *testing.T) {
	cat := Catalog{Builtin: BuiltinParts()}
	var part, param string
	for _, p := range cat.List() {
		for _, q := range p.Params {
			if q.Type == "string" && q.MaxLength > 0 {
				part, param = p.Name, q.Name
			}
		}
	}
	if part == "" {
		t.Skip("no part has a text parameter with a limit")
	}
	err := errors.New(`piece p2: the parameter "` + param + `" of the part "` + part + `" has 42 characters, the most is 20`)
	got := scriptHint(cat, err)
	p, _ := cat.Get(part)
	want := 0
	for _, q := range p.Params {
		if q.Name == param {
			want = q.MaxLength
		}
	}
	if !strings.Contains(got, "at most "+itoa(want)+" characters") || !strings.Contains(got, param) {
		t.Fatalf("%q", got)
	}
	if h := scriptHint(cat, errors.New(`p1: the keyword "x" is not a word of line 1 ("a b")`)); !strings.Contains(h, "Copy the keyword") {
		t.Fatalf("%q", h)
	}
	// A refusal it does not know goes as it was.
	if h := scriptHint(cat, errors.New("the script must cover the whole song")); h != "" {
		t.Fatalf("%q", h)
	}
	if h := designHint(Design{}, errors.New("design.display must be one of x")); h != "" {
		t.Fatalf("%q", h)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

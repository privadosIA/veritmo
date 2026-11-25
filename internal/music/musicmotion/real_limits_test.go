package musicmotion

// A measure, not a check: how often a real model gets a design and a script accepted with the old texts and with the
// new ones (spec music-motion, "The assistant is told the limits it is checked against"). It runs only when
// VERITMO_REAL_OLLAMA_MODEL names an installed model of the Ollama at VERITMO_OLLAMA_URL (default http://127.0.0.1:11434).
//
//	VERITMO_REAL_OLLAMA_MODEL=qwen3:8b VERITMO_REAL_RUNS=6 go test ./internal/music/musicmotion -run RealLimits -v -timeout 90m

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

type ollamaWriter struct {
	url, model string
	// arm says which texts to give: "old" (as before the limits were stated), "rules" (the rules, no hints on a retry), "hints"
	// (the hints on a retry, no rules) or "new" (both).
	arm   string
	calls int
}

var (
	reDesignRules = regexp.MustCompile(`(?s)The design is CHECKED.*?use ONE mode for every section \("default_mode" and no "modes"\)\.\n`)
	reScriptRules = regexp.MustCompile(`(?s)The script is CHECKED.*?a word copied from the lines of its piece\.\n`)
	reHints       = regexp.MustCompile(`(?s)\n(The accent .*|The limits of the parameter .*|Copy the keyword .*)`)
)

func (w *ollamaWriter) Write(ctx context.Context, system, user string, maxTokens int) (string, error) {
	w.calls++
	if w.arm == "old" || w.arm == "hints" {
		system = reDesignRules.ReplaceAllString(system, "")
		if strings.Contains(system, "You design the look") {
			system += "\nDark and light must have a contrast of at least 4.5:1; the accent at least 3:1 against the background of every mode used."
		}
		system = reScriptRules.ReplaceAllString(system, "")
	}
	if w.arm == "old" || w.arm == "rules" {
		user = reHints.ReplaceAllString(user, "")
	}
	body, _ := json.Marshal(map[string]any{"model": w.model, "stream": false, "think": false,
		"messages": []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": user}},
		"options":  map[string]any{"temperature": 0.4, "num_predict": maxTokens, "num_ctx": 16384}})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, w.url+"/api/chat", bytes.NewReader(body))
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Message struct{ Content string } `json:"message"`
		Error   string                   `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Error != "" {
		return "", fmt.Errorf("%s", out.Error)
	}
	return out.Message.Content, nil
}

const realPrompt = "A dark, glitchy horror piece: flickering red and black text on a terminal that is being corrupted, harsh cuts on every beat, " +
	"no synthwave, no sun, no grid. The words should feel like a warning left on a broken screen: short, cold, repeated. " +
	"Use light sections only for the shock of the chorus, then go back to the dark. Neon is allowed only as a glitch, never as decoration. " +
	"Keep every label short and every number moderate; the viewer must be able to read each line at a glance, even on a small phone screen in a dark room."

func TestRealLimits_AcceptanceWithTheOldAndTheNewTexts(t *testing.T) {
	model := os.Getenv("VERITMO_REAL_OLLAMA_MODEL")
	if model == "" {
		t.Skip("VERITMO_REAL_OLLAMA_MODEL is not set")
	}
	url := os.Getenv("VERITMO_OLLAMA_URL")
	if url == "" {
		url = "http://127.0.0.1:11434"
	}
	runs := 6
	if n, err := strconv.Atoi(os.Getenv("VERITMO_REAL_RUNS")); err == nil && n > 0 {
		runs = n
	}
	type tally struct{ n, designOK, scriptOK, designCalls, scriptCalls int }
	arms := []string{"old", "new"}
	if a := os.Getenv("VERITMO_REAL_ARMS"); a != "" {
		arms = strings.Split(a, ",")
	}
	res := map[string]*tally{}
	for _, a := range arms {
		res[a] = &tally{}
	}
	m := longMap()
	for i := 0; i < runs; i++ {
		for _, arm := range arms { // alternated, so a warm model favours neither
			w := &ollamaWriter{url: url, model: model, arm: arm}
			d, src, why := ProposeDesignWhy(context.Background(), w, Song{Style: "dark synth rock", Lyrics: "line one here\nline two here\nallow allow it\nanother verse line\n", SectionTags: []string{"Intro", "Verse", "Chorus", "Verse"}, Prompt: realPrompt})
			dc := w.calls
			if src == "fallback" {
				d = good()
			}
			x := ScriptInput{Map: m, Design: d, Level: LevelMedium, Cat: NewCatalog(nil), Prompt: realPrompt}
			p, err := ProposeScript(context.Background(), w, x)
			sc := w.calls - dc
			tl := res[arm]
			tl.n++
			tl.designCalls += dc
			tl.scriptCalls += sc
			if src == "assistant" {
				tl.designOK++
			}
			if err == nil && p.Source == "assistant" {
				tl.scriptOK++
			}
			t.Logf("run %d %-3s design=%s (%d calls) script=%s (%d calls) %s", i+1, arm, src, dc, p.Source, sc, strings.ReplaceAll(why+" | "+p.FallbackReason, "\n", " "))
		}
	}
	for _, arm := range arms {
		tl := res[arm]
		t.Logf("%s: design accepted %d/%d (%d calls), script accepted %d/%d (%d calls)", arm, tl.designOK, tl.n, tl.designCalls, tl.scriptOK, tl.n, tl.scriptCalls)
	}
}

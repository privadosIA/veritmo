package musicmotion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"strings"

	"veritmo/internal/music/songmap"
	"veritmo/internal/video/motion"
)

// This file is the one place that says what a part has to pass: the parts that come with the program (their test)
// and the drafts a model writes (spec motion-part-drafts) are both checked by CheckPart.

// ParsePart reads a manifest and a fragment into a part, with the rules a part of the catalog follows (the manifest
// rules, the fragment rules). The problems are readable by the one who wrote the part, a model included.
func ParsePart(manifest []byte, fragment string) (Part, []string) {
	var head struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(manifest, &head); err != nil {
		return Part{}, []string{"manifest.json is not valid JSON: " + err.Error()}
	}
	m, err := parseManifest(manifest, head.Name)
	if err != nil {
		return Part{}, []string{err.Error()}
	}
	if strings.TrimSpace(fragment) == "" {
		return Part{}, []string{"part.html is empty"}
	}
	if probs := fragmentProblems(fragment); len(probs) > 0 {
		return Part{}, probs
	}
	for _, pr := range m.Params {
		if pr.Animatable {
			return Part{}, []string{fmt.Sprintf("the parameter %q is marked animatable: a part that is drafted cannot declare animatable parameters", pr.Name)}
		}
	}
	return Part{Manifest: m, Fragment: fragment, Source: "user"}, nil
}

// CheckOptions loosen a check for the part that needs it.
type CheckOptions struct {
	// NoMovement skips the movement over a beat: a part that draws nothing of its own (blank) has none.
	NoMovement bool
	// Scene, when given, is the scene the part is checked in (the real lines, design and length of a script piece) instead of
	// the standard one; its Part and Params are replaced by the part's.
	Scene *Scene
	// SceneRules says the part is a scene written for a verse: besides what any part passes it keeps the design's fonts and colours,
	// shows no captions, draws big and fills the frame (scenechecks.go). Set for the drafts of scenes, not for the parts of the catalog.
	SceneRules bool
	// Allow is text a scene may show besides its verse and the design's concept: the idea it was asked for and the song's title.
	// Only read with SceneRules.
	Allow string
	// Server, when set, collects what is wrong on the page's own layers (the word hit it draws) with a part that never touches them: that is
	// the server's fault, not the part's, so it is logged and written here prefixed "server:" instead of refusing the part.
	Server *[]string
}

// sceneOf is the scene a part is checked or previewed in.
func sceneOf(p Part, opts CheckOptions) (Scene, error) {
	if opts.Scene == nil {
		return checkScene(p)
	}
	params, err := p.ResolveParams(nil)
	if err != nil {
		return Scene{}, err
	}
	sc := *opts.Scene
	sc.Part, sc.Params = p, params
	if sc.Width <= 0 || sc.Height <= 0 {
		sc.Width, sc.Height = 1280, 720
	}
	return sc, nil
}

// windowTimes are the frames over which a part is looked at: the beat that starts at one second in the standard scene, and
// from the start of the first line in a real one.
func windowTimes(sc Scene, real bool) []float64 {
	if !real {
		return beatTimes()
	}
	t0 := 0.0
	if len(sc.Lines) > 0 {
		t0 = sc.Lines[0].Start
	}
	var out []float64
	for k := 0; k <= 15; k++ {
		if t := t0 + float64(k)/30; t < sc.Duration {
			out = append(out, t)
		}
	}
	if len(out) < 2 {
		out = []float64{0, math.Min(0.5, sc.Duration/2)}
	}
	return out
}

// wordsAppearProblems is the rule of a part that draws the lyric itself: at the start of each of the first words the frame
// must differ from the frame just before it, or the word was not drawn at its time.
func wordsAppearProblems(ctx context.Context, browser string, sc Scene) ([]string, error) {
	var words []Word
	for _, l := range sc.Lines {
		words = append(words, l.Words...)
	}
	var out []string
	checked := 0
	for _, w := range words {
		if w.Start < 0.25 || w.Start+0.1 >= sc.Duration {
			continue // no frame before it to compare with, or no time after it
		}
		if checked == 3 {
			break
		}
		checked++
		frames, err := renderAt(ctx, browser, sc, []float64{w.Start - 0.1, w.Start + 0.1})
		if err != nil {
			return nil, err
		}
		if string(frames[0]) == string(frames[1]) {
			out = append(out, fmt.Sprintf("the word %q did not appear at its time: the frame at %.2f s is the same as the frame at %.2f s. Draw each word of window.__scene.lines from its start time s, not before", w.Text, w.Start+0.1, w.Start-0.1))
			break
		}
	}
	return out, nil
}

// checkDesign is the design a part is checked and previewed with.
func checkDesign() Design {
	return Design{Dark: "#0b0f14", Light: "#e8f1ec", Accent: "#2bff88", Display: "heavy", Body: "mono",
		DefaultMode: ModeDark, Modes: map[string]string{"chorus": ModeDark}, Concept: "a terminal that writes the song"}
}

func checkScene(p Part) (Scene, error) {
	params, err := p.ResolveParams(nil)
	if err != nil {
		return Scene{}, err
	}
	return Scene{Part: p, Params: params, Design: checkDesign(), Mode: ModeDark, Duration: 4, Width: 1280, Height: 720,
		Lines: []Line{{Text: "one two three", Start: 0.5, End: 4, Keyword: 1, Words: []Word{{"one", 0.5, 1, true}, {"two", 1, 1.5, true}, {"three", 1.5, 2, true}}}}}, nil
}

func openCheckSession(ctx context.Context, browser string, sc Scene) (*motion.Session, []byte, error) {
	html, err := BuildPage(sc)
	if err != nil {
		return nil, nil, err
	}
	song, _ := json.Marshal(songmap.Build(songmap.Input{Duration: sc.Duration, BPM: 120}))
	s, err := motion.Open(ctx, browser, motion.Page{HTML: html, Width: sc.Width, Height: sc.Height, Duration: sc.Duration, Song: song}, motion.Limits{})
	return s, []byte(html), err
}

// beatTimes are the frames of the beat that starts at one second and the first of the next, at 30 fps.
func beatTimes() []float64 {
	var out []float64
	for k := 30; k <= 45; k++ {
		out = append(out, float64(k)/30)
	}
	return out
}

func renderAt(ctx context.Context, browser string, sc Scene, times []float64) ([][]byte, error) {
	s, _, err := openCheckSession(ctx, browser, sc)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	var out [][]byte
	for _, at := range times {
		f, err := s.Frame(ctx, at)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// settleHouse takes out of the problems of the page those about the page's own layers (marked by the motion checks) when the part never
// touches them and the caller collects them (CheckOptions.Server): they are the server's, logged and written there. In every other case the
// mark is dropped and the problem stays the part's.
func settleHouse(probs []string, p Part, sc Scene, opts CheckOptions) []string {
	var keep []string
	own := opts.Server != nil && !touchesHouseLayers(p.Fragment)
	for _, pr := range probs {
		if !strings.HasPrefix(pr, motion.HouseProblemPrefix) {
			keep = append(keep, pr)
			continue
		}
		if own {
			slog.Warn("a text of the page's own layers fails the page checks", "part", p.Name, "design_display", sc.Design.Display, "problem", pr)
			*opts.Server = append(*opts.Server, pr)
			continue
		}
		keep = append(keep, strings.TrimPrefix(pr, motion.HouseProblemPrefix))
	}
	return keep
}

var reHouseLayers = regexp.MustCompile(`(?i)#hit|#verse|['"]hit(bg)?['"]|['"]verse['"]|\bid\s*=\s*['"]?(hit|hitbg|verse)\b`)

// touchesHouseLayers says the fragment names the page's own layers (#hit, #hitbg, #verse): what goes wrong there may be its doing.
func touchesHouseLayers(fragment string) bool { return reHouseLayers.MatchString(fragment) }

// hitProblems is the rule of the word hit: on a frame inside its span (once its entrance has finished) no letter of it may be cut
// by the side of the frame. The generic check of a page looks at the first, middle and last frame of the scene, which may fall
// where no hit is on screen.
func hitProblems(ctx context.Context, browser string, sc Scene) ([]string, error) {
	if sc.Hit == nil || sc.Hit.Length <= 0 {
		return nil, nil
	}
	s, _, err := openCheckSession(ctx, browser, sc)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	at := sc.Hit.At + math.Min(0.4, sc.Hit.Length*0.75)
	// The renderer draws the frames in order and the hit is fitted again on the first of them until the fonts are in, so the frame is
	// reached the same way: after the first ones, not as the only seek of the page.
	for _, warm := range []float64{0, 0.05, 0.1} {
		if warm < at {
			if _, err := s.Frame(ctx, warm); err != nil {
				return nil, err
			}
		}
	}
	if _, err := s.Frame(ctx, at); err != nil {
		return nil, err
	}
	boxes, _, err := s.TextBoxes(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range motion.JudgeCut(boxes, sc.Width, at) {
		out = append(out, "the word hit leaves the frame: "+strings.TrimPrefix(p, motion.HouseProblemPrefix))
	}
	return out, nil
}

// CheckPart draws a part over a beat and returns what is wrong with it (empty: it passes): the checks of a motion page,
// movement (not the same frame all over a beat, with no verse on the page) and the same frames in two
// renders. A text cut by the edge of the frame is one of the checks of a motion page. The error is for what is not the part's fault (no browser).
func CheckPart(ctx context.Context, browser string, p Part, opts CheckOptions) ([]string, error) {
	if opts.Server != nil {
		*opts.Server = nil
	}
	if probs := fragmentProblems(p.Fragment); len(probs) > 0 {
		return probs, nil
	}
	if opts.SceneRules { // a scene of a script piece keeps the design's fonts and colours (scenechecks.go)
		if probs := sceneStyleProblems(p.Fragment); len(probs) > 0 {
			return probs, nil
		}
	}
	sc, err := sceneOf(p, opts)
	if err != nil {
		return []string{err.Error()}, nil
	}
	html, err := BuildPage(sc)
	if err != nil {
		return []string{err.Error()}, nil
	}
	song, _ := json.Marshal(songmap.Build(songmap.Input{Duration: sc.Duration, BPM: 120}))
	probs, err := motion.Check(ctx, browser, motion.Page{HTML: html, Width: sc.Width, Height: sc.Height, Duration: sc.Duration, Song: song}, motion.Limits{})
	if err != nil {
		return nil, err
	}
	probs = partMotionMessage(probs)
	probs = settleHouse(probs, p, sc, opts)
	if len(probs) > 0 {
		return probs, nil
	}
	if more, err := timelinePageProblems(ctx, browser, p, sc); err != nil {
		return nil, err
	} else if len(more) > 0 {
		return more, nil
	}
	var out []string
	times := windowTimes(sc, opts.Scene != nil)
	if p.Timeline() {
		// A timeline moves where it was written to, not necessarily inside the beat that is looked at: the frames are spread over the scene too.
		times = append(times, spreadTimes(sc.Duration, 12)...)
	}
	a, err := renderAt(ctx, browser, sc, times)
	if err != nil {
		return nil, err
	}
	b, err := renderAt(ctx, browser, sc, times)
	if err != nil {
		return nil, err
	}
	for i := range a {
		if string(a[i]) != string(b[i]) {
			out = append(out, fmt.Sprintf("two renders give different frames (at %.2f s): the part must not use the wall clock or Math.random(); draw from t and the beat only", times[i]))
			break
		}
	}
	if !opts.NoMovement {
		// The verse moves by itself: the movement of the part is looked at with no text on the page.
		bare := sc
		if !p.OwnsVerse() { // a part that draws the lyric moves with it: the lyric stays on its page
			bare.Lines = nil
		}
		if !p.OwnsHit() {
			bare.Hit = nil
		}
		m, err := renderAt(ctx, browser, bare, times)
		if err != nil {
			return nil, err
		}
		a = m
		distinct := map[string]bool{}
		for _, f := range a {
			distinct[string(f)] = true
		}
		if len(distinct) < 2 {
			out = append(out, "the part does not move over a beat (every frame is the same): make it change with beat.position and beat.number")
		}
	}
	if !p.OwnsHit() {
		more, err := hitProblems(ctx, browser, sc)
		if err != nil {
			return nil, err
		}
		if opts.Server != nil && !touchesHouseLayers(p.Fragment) {
			for _, m := range more { // the page's own hit, which this part never touches: not the part's fault
				slog.Warn("the house word hit fails its own check", "part", p.Name, "design_display", sc.Design.Display, "hit", sc.Hit.Word, "problem", m)
				*opts.Server = append(*opts.Server, "server: "+m)
			}
			more = nil
		}
		out = append(out, more...)
	}
	if p.OwnsVerse() {
		more, err := wordsAppearProblems(ctx, browser, sc)
		if err != nil {
			return nil, err
		}
		out = append(out, more...)
	}
	if opts.SceneRules && len(out) == 0 {
		more, err := sceneFrameProblems(ctx, browser, sc, opts.Allow, p.OwnsVerse())
		if err != nil {
			return nil, err
		}
		out = append(out, more...)
	}
	return out, nil
}

// PreviewFrames draws a few frames of a part over one beat (PNG) with the colours of the check design.
func PreviewFrames(ctx context.Context, browser string, p Part, n int) ([][]byte, error) {
	return PreviewFramesIn(ctx, browser, p, n, CheckOptions{})
}

// PreviewFramesIn is PreviewFrames in the scene the options give (the real verse of a scene draft): the frames are spread over
// the first line, or over the first second and a half when there is none.
func PreviewFramesIn(ctx context.Context, browser string, p Part, n int, opts CheckOptions) ([][]byte, error) {
	if n < 1 {
		return nil, errors.New("a preview needs at least one frame")
	}
	sc, err := sceneOf(p, opts)
	if err != nil {
		return nil, err
	}
	var times []float64
	if opts.Scene == nil {
		for i := 0; i < n; i++ {
			times = append(times, 1+0.5*float64(i)/float64(n)+0.02)
		}
	} else {
		t0, span := 0.0, math.Min(2.5, sc.Duration*0.8)
		if len(sc.Lines) > 0 {
			t0 = sc.Lines[0].Start
		}
		for i := 0; i < n; i++ {
			times = append(times, math.Min(t0+span*float64(i+1)/float64(n), math.Max(sc.Duration-0.05, 0)))
		}
	}
	return renderAt(ctx, browser, sc, times)
}

// spreadTimes are n frames spread over a scene, none at its very end.
func spreadTimes(duration float64, n int) []float64 {
	var out []float64
	for k := 0; k < n; k++ {
		out = append(out, duration*float64(k)/float64(n))
	}
	return out
}

// motionStill is the start of the message of a page that never changes; the page checks suggest CSS animations or window.__seek(t),
// which is wrong for a part: the page owns window.__seek and the part moves in window.__partSeek, drawn from t and the beat.
const motionStill = "the page does not change over time"

// partMotionMessage says what to do about a part that does not move in the terms of a part: set its styles from t and beat inside
// window.__partSeek (no CSS animations, no transitions), and make sure they end up different between the start, the middle and the end.
func partMotionMessage(probs []string) []string {
	out := make([]string, len(probs))
	for i, p := range probs {
		if strings.HasPrefix(p, motionStill) {
			p = motionStill + ": the frames at the start, the middle and the end of the part are the same; define window.__partSeek(t, beat) and set the styles of its elements directly from t and beat there (no CSS animations or transitions), so that something moves over the part's duration"
		}
		out[i] = p
	}
	return out
}

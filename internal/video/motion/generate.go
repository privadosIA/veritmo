package motion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/assistant/llm"
	"veritmo/internal/platform/settings"
)

// Writer asks a language model for text. It is the one thing the generator needs
// from the model providers, so tests can replace it.
type Writer interface {
	Write(ctx context.Context, system, user string, maxTokens int) (string, error)
}

// ModelInfo says who wrote a page, for the metadata of the clip.
type ModelInfo struct {
	Provider string
	Model    string
}

// maxTokens bounds the page the model may write.
const maxTokens = 8000

// remoteTokenFactor multiplies the limit for a provider that is not an Ollama (see llmWriter.Write).
const remoteTokenFactor = 4

// MaxAttempts is how many times the model is asked for one page: the first
// request and two repairs with the problems found.
const MaxAttempts = 3

// Request describes the page to write.
type Request struct {
	Description string // what the graphic shows, or the text of the overlay
	Narration   string // the scene's spoken text, for context; may be empty
	Style       string // the project's style, free text; may be empty
	Width       int
	Height      int
	Duration    float64 // seconds
	Overlay     bool    // transparent background, drawn over a picture or clip
	Position    string  // overlay only: top, center or bottom
}

const systemPrompt = `You write one self-contained HTML page that will be rendered to a video, frame by frame, by a headless browser. Answer with the HTML document only: no explanations and no markdown fences.

HARD RULES
- A complete document: <!doctype html>, <html>, <head>, <body>. One file with inline <style> and <script>.
- The page is offline: no <link>, no remote <script>, images, fonts, @import or url(http...). Only system font stacks; draw everything with HTML, CSS, inline SVG or <canvas>.
- The canvas is exactly the given size: html and body get that width and height, margin 0, overflow hidden.
- Time is driven by the renderer, never by the clock. Animate in these ways only: (1) the TIMELINE block described below, where you declare the entrances and every movement of the content as data and the renderer applies it; (2) CSS @keyframes (animation-fill-mode: both) ONLY for continuous decoration of elements the timeline does not target (a drifting background, a glow); (3) window.__seek = function(t){...} that sets the whole state for t seconds (0..duration), for what the timeline cannot express. No setTimeout/setInterval/Date/requestAnimationFrame. window.__motionDuration holds the duration in seconds.
- TIMELINE: put exactly one <script type="application/json" id="motion-timeline"> block in the page, {"version":1,"tracks":[{"id":"title","target":".title","start":0.2,"duration":0.9,"ease":"out","stagger":0.12,"from":{"opacity":0,"y":36},"to":{"opacity":1,"y":0}}]}. Each track has an id, a target (a CSS selector; with stagger, every matched element starts stagger seconds after the previous one, in document order), start and duration in seconds, an ease and from/to objects. Properties (to animate one, give it in BOTH from and to): opacity 0..1, x and y in px, scale, scaleX, rotate in degrees, blur in px, reveal 0..1 (shows the element from the left, for lines and bars). ease is linear, in, out, in-out or out-back (or four numbers of a cubic-bezier); use out for entrances. Before its start an element has its from values and after the end its to values, so give elements their final look in CSS and let the tracks bring them in. Every track must end before the clip does, and an element a track targets must not also have a CSS animation.
- Any element you position with position:absolute needs a position:relative ancestor that you control (a wrapper), never the body by accident. Check that every element you animate really ends up inside the frame.
- Pace to the duration: the main content is fully in place by ~35% of it, stays readable until the end, and the last frame is a finished composition (no empty screen, no half-drawn state).

DESIGN (this is what makes it look professional, follow it)
- Typography: pick ONE font stack and use a clear scale: a hero size of 8-12% of the frame height, secondary text at 40-50% of the hero, small text never under 2.2% of the frame height. Use weight contrast (800 vs 300), tight letter-spacing on big text (-0.02em), wide tracking on small caps labels (+0.18em). Never more than 3 sizes on one screen.
- Palette: 1 dark base, 1 off-white text, 1 accent. Use the project style for colors if given. Build depth with a subtle radial or linear gradient background, never a flat fill. Add one or two decorative layers (soft blurred glow shapes, thin lines, a grid or dots at 6-10% opacity) that drift slowly during the whole clip so the frame is never static.
- Layout: align to a clear grid, generous empty space, content block optically centered or left-aligned on a strong margin (6-8% of width). Keep related items close and unrelated ones far.
- Motion: declare the entrances as tracks: stagger them (0.06-0.12 s between items); each entrance combines opacity + y 24-48 (+ optional blur 8 -> 0 or scale .96 -> 1); durations 0.6-0.9 s; ease out for entrances and in-out for moves. Lines and bars grow with reveal (or scaleX with transform-origin:left in CSS). Numbers count up with __seek. Add a gentle continuous motion on the background layers (CSS) and a small slow scale track (1 -> 1.03) on the hero for the whole duration.
- TEXT: write exactly the words the description gives, spelled as given, and no others: no kicker, label, tagline, date or caption of your own. The words in the description's order of importance decide the hierarchy: the first quoted text is the hero unless the description says otherwise. If the description says items appear one by one, they do; if credits should rise, they scroll upward through the frame with a constant slow speed.
- ALIGNMENT: in lists, use a grid or fixed widths so numbers and labels line up exactly in columns.
- MOTION RICHNESS: use at least three kinds of motion in the scene: the entrances of the content, a continuous slow background drift, and one signature move that fits the topic (a line that draws, a number that counts up, a mask reveal with clip-path, a shape that wipes across). Never leave a long stretch of the clip with nothing moving.

EXAMPLE of the quality and technique expected. It is only a demonstration of structure and craft: its [BRACKETED] texts are placeholders, never text to write; NEVER copy its words, its layout choices, its colors or its extra elements; take the technique, not the content.
<!doctype html><html><head><meta charset="utf-8"><style>
html,body{margin:0;width:1280px;height:720px;overflow:hidden;background:#101418;font-family:"Helvetica Neue",Arial,sans-serif;color:#eef0f2}
.stage{position:relative;width:1280px;height:720px}
.bg{position:absolute;inset:0;background:radial-gradient(900px 600px at 15% 25%,#233042 0%,transparent 60%),radial-gradient(700px 500px at 90% 85%,#3b2830 0%,transparent 60%),#101418}
.glow{position:absolute;width:520px;height:520px;border-radius:50%;background:#e0786a;filter:blur(140px);opacity:.16;left:760px;top:-120px;animation:drift 5s ease-in-out both}
.wrap{position:absolute;left:96px;top:0;height:720px;display:flex;flex-direction:column;justify-content:center}
h1{margin:0;font-size:92px;font-weight:800;letter-spacing:-.02em;line-height:1.02}
.rule{width:380px;height:3px;background:#e0786a;margin-top:30px;transform-origin:left}
p{margin:20px 0 0;font-size:26px;font-weight:300}
@keyframes drift{from{transform:translate(0,0)}to{transform:translate(-60px,40px)}}
</style></head><body><div class="stage"><div class="bg"></div><div class="glow"></div>
<div class="wrap"><h1>[HEADING]</h1><div class="rule"></div><p>[SUPPORT LINE]</p></div></div>
<script type="application/json" id="motion-timeline">{"version":1,"tracks":[
{"id":"heading","target":"h1","start":0.2,"duration":0.9,"ease":"out","from":{"opacity":0,"y":36},"to":{"opacity":1,"y":0}},
{"id":"hero-scale","target":"h1","start":0,"duration":5,"ease":"linear","from":{"scale":1},"to":{"scale":1.03}},
{"id":"rule","target":".rule","start":0.8,"duration":1,"ease":"in-out","from":{"scaleX":0},"to":{"scaleX":1}},
{"id":"support","target":"p","start":1.2,"duration":0.8,"ease":"out","from":{"opacity":0,"y":36},"to":{"opacity":1,"y":0}}]}</script>
<script>window.__motionDuration=5;</script></body></html>
`

func userPrompt(r Request) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Canvas: %dx%d pixels. Duration: %.2f seconds.\n", r.Width, r.Height, r.Duration)
	if r.Overlay {
		pos := r.Position
		if pos == "" {
			pos = "bottom"
		}
		fmt.Fprintf(&b, "This is an OVERLAY drawn over a picture or video: the page background MUST stay fully transparent (no background color on html or body), and only the text and graphics themselves are drawn. Place it at the %s of the frame, with a title-safe margin.\n", pos)
		fmt.Fprintf(&b, "Draw ONLY this, with no other words, titles, subtitles or captions of your own: %s\n", r.Description)
	} else {
		fmt.Fprintf(&b, "This is a FULL SCENE: draw an opaque background that fills the canvas.\n")
		fmt.Fprintf(&b, "What the graphic shows: %s\n", r.Description)
	}
	// An overlay shows its own text and nothing else: the narration and the style would only tempt the model to add words.
	if t := strings.TrimSpace(r.Narration); t != "" && !r.Overlay {
		fmt.Fprintf(&b, "The scene is narrated with this text (the animation accompanies it): %s\n", t)
	}
	if t := strings.TrimSpace(r.Style); t != "" && !r.Overlay {
		fmt.Fprintf(&b, "Visual style of the project, for colors, mood and shapes only (never write this text on the screen): %s\n", t)
	}
	return b.String()
}

func repairPrompt(r Request, html string, problems []string) string {
	return userPrompt(r) + "\nYour previous page had these problems:\n- " + strings.Join(problems, "\n- ") +
		"\n\nHere is the previous page. Answer with a corrected, complete HTML document only.\n\n" + html
}

var fence = regexp.MustCompile("(?s)```(?:html)?\\s*(.*?)```")

// ExtractHTML takes the document out of a model's answer: inside a markdown fence when
// there is one, else from <!doctype or <html to the end of </html>.
func ExtractHTML(answer string) string {
	s := strings.TrimSpace(answer)
	if m := fence.FindStringSubmatch(s); m != nil {
		s = strings.TrimSpace(m[1])
	}
	low := strings.ToLower(s)
	if i := strings.Index(low, "<!doctype"); i >= 0 {
		s, low = s[i:], low[i:]
	} else if i := strings.Index(low, "<html"); i >= 0 {
		s, low = s[i:], low[i:]
	}
	if i := strings.LastIndex(low, "</html>"); i >= 0 {
		s = s[:i+len("</html>")]
	}
	return s
}

var (
	reDoc      = regexp.MustCompile(`(?is)<html[\s>].*</html>`)
	reExternal = regexp.MustCompile(`(?i)(?:\b(?:src|href|data|poster|action)\s*=\s*["']?\s*(?:https?:)?//|url\(\s*["']?\s*(?:https?:)?//|@import\s+(?:url\()?\s*["']?\s*(?:https?:)?//|<link\b[^>]*\brel\s*=\s*["']?stylesheet)`)
)

// StaticProblems are what can be found in the text of a page without running it.
func StaticProblems(html string) []string {
	var out []string
	if !reDoc.MatchString(html) {
		out = append(out, "the answer is not a complete HTML document (<html> ... </html>)")
	}
	if reExternal.MatchString(html) {
		out = append(out, "the page loads an external resource (a remote script, stylesheet, image, font or @import); everything must be inline")
	}
	return out
}

// Check loads the page and renders it at its first, middle and last frame. It returns
// the problems found: static ones, script errors, blocked requests, a page that never
// changes (full scenes) or draws nothing (overlays). No problems means the page is good.
func Check(ctx context.Context, browser string, p Page, lim Limits) ([]string, error) {
	if probs := StaticProblems(p.HTML); len(probs) > 0 {
		return probs, nil
	}
	s, err := Open(ctx, browser, p, lim)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	var frames [][]byte
	var failed, layout []string
	for i, t := range []float64{0, p.Duration / 2, p.Duration} {
		f, err := s.Frame(ctx, t)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			failed = append(failed, err.Error())
			break
		}
		frames = append(frames, f)
		if i == 0 {
			continue // the first frame is the start of the entrances: text may still be on its way in
		}
		// Where the text sits in the middle and at the end: cut by a side, and (at the end) overlapping.
		if boxes, total, err := s.TextBoxes(ctx); err == nil {
			layout = append(layout, JudgeCut(boxes, p.Width, t)...)
			if i == 2 {
				layout = append(layout, JudgeOverlap(boxes, t)...)
				layout = append(layout, JudgeInvisible(boxes, total, t)...)
			}
		} else if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	problems := append(failed, s.Problems()...)
	if len(failed) == 0 {
		var tl []string
		if err := s.EvalJSON(ctx, "JSON.stringify(window.__motionTimelineProblems ? window.__motionTimelineProblems() : [])", &tl); err == nil {
			problems = append(problems, tl...)
		} else if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	problems = append(problems, dedupe(layout)...)
	if len(failed) == 0 {
		if p.Transparent {
			switch cover := maxCoverage(frames); {
			case cover == 0:
				problems = append(problems, "the overlay draws nothing: every frame is fully transparent")
			case cover > maxOverlayCoverage:
				problems = append(problems, fmt.Sprintf("the overlay covers %.0f%% of the frame: html and body must have no background at all (transparent), and only the text and its small decorations are drawn", cover*100))
			}
		} else if bytes.Equal(frames[0], frames[1]) && bytes.Equal(frames[1], frames[2]) {
			problems = append(problems, "the page does not change over time: animate it with CSS animations, element.animate or window.__seek(t)")
		}
	}
	return problems, nil
}

// dedupe drops repeated problems (the same text cut in the middle and at the end says it twice).
func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range in {
		key := p
		if i := strings.Index(p, " at "); i > 0 {
			key = p[:i]
		}
		if !seen[key] {
			seen[key] = true
			out = append(out, p)
		}
	}
	return out
}

// maxOverlayCoverage is the share of the frame an overlay may draw on: text and its decorations,
// not a background that hides the scene below.
const maxOverlayCoverage = 0.5

// maxCoverage is, over the frames, the largest share of pixels with a non-zero alpha.
func maxCoverage(frames [][]byte) float64 {
	best, readable := 0.0, false
	for _, f := range frames {
		img, err := png.Decode(bytes.NewReader(f))
		if err != nil {
			continue
		}
		readable = true
		b := img.Bounds()
		total, drawn := b.Dx()*b.Dy(), 0
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				if _, _, _, a := img.At(x, y).RGBA(); a != 0 {
					drawn++
				}
			}
		}
		if total > 0 {
			if c := float64(drawn) / float64(total); c > best {
				best = c
			}
		}
	}
	if !readable {
		return 0.01 // cannot tell: neither empty nor covering
	}
	return best
}

// ErrPageRejected is returned when the model could not produce a page that passes the check.
var ErrPageRejected = errors.New("the model could not write a working page")

// Generate asks the model for a page and checks it, asking again with the problems up to
// MaxAttempts times. It returns the page; when no attempt passes it returns an error
// that names the last problems.
func Generate(ctx context.Context, w Writer, browser string, r Request, lim Limits) (string, error) {
	user := userPrompt(r)
	var last []string
	for attempt := 0; attempt < MaxAttempts; attempt++ {
		answer, err := w.Write(ctx, systemPrompt, user, maxTokens)
		if err != nil {
			return "", err
		}
		html := ExtractHTML(answer)
		problems, err := Check(ctx, browser, Page{HTML: html, Width: r.Width, Height: r.Height, Duration: r.Duration, Transparent: r.Overlay}, lim)
		if err != nil {
			return "", err
		}
		if len(problems) == 0 {
			return html, nil
		}
		last = problems
		user = repairPrompt(r, html, problems)
	}
	return "", fmt.Errorf("%w after %d attempts: %s", ErrPageRejected, MaxAttempts, strings.Join(last, "; "))
}

// Dir is the folder of a project that holds its stored pages.
const Dir = "motion"

// PagePath is where the page of a scene (or of its overlay) is stored in a project folder.
func PagePath(projectDir, sceneID string, overlay bool) string {
	name := sceneID + ".html"
	if overlay {
		name = sceneID + ".overlay.html"
	}
	return filepath.Join(projectDir, Dir, name)
}

// SongPath is where the song map of a scene that belongs to a music piece is stored in a project folder: the
// page of such a scene reads the beat clock from it (see beatclock.go). A scene with no such file is an ordinary one.
func SongPath(projectDir, sceneID string) string {
	return filepath.Join(projectDir, Dir, sceneID+".song.json")
}

// LoadSong reads the stored song map of a scene; nil when it has none.
func LoadSong(projectDir, sceneID string) json.RawMessage {
	data, err := os.ReadFile(SongPath(projectDir, sceneID))
	if err != nil || !json.Valid(data) {
		return nil
	}
	return data
}

// Ensure returns the stored page of a scene, writing it first when there is none: the
// model is asked only then. generated says whether this call asked the model.
func Ensure(ctx context.Context, w Writer, browser, projectDir, sceneID string, r Request, lim Limits) (html string, generated bool, err error) {
	path := PagePath(projectDir, sceneID, r.Overlay)
	if data, rerr := os.ReadFile(path); rerr == nil {
		return string(data), false, nil
	} else if !os.IsNotExist(rerr) {
		return "", false, rerr
	}
	html, err = Generate(ctx, w, browser, r, lim)
	if err != nil {
		return "", false, err
	}
	if err := Store(projectDir, sceneID, r.Overlay, html); err != nil {
		return "", false, err
	}
	return html, true, nil
}

// Store writes the page of a scene, replacing the stored one.
func Store(projectDir, sceneID string, overlay bool, html string) error {
	path := PagePath(projectDir, sceneID, overlay)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(html), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// PreferredModels are the models that write the best pages, in order; an Ollama that has one of them uses it
// when no model is saved for the task. Measured on the same three briefs: they keep the words and the
// hierarchy of the description, where the assistant's usual coding model cuts the text and copies examples.
var PreferredModels = []string{"gemma4:26b", "gemma4:31b"}

// preferredLookup bounds the question to an Ollama about its installed models.
const preferredLookup = 3 * time.Second

// ResolveModel says which model writes the pages on the provider the task resolved to: the one saved for the
// task; else, on an Ollama, the first of PreferredModels it has installed; else the one the provider declares
// for the task; else the assistant's.
func ResolveModel(ctx context.Context, res llm.Resolved) string {
	if m := modelFrom(ctx); m != "" {
		return m
	}
	if m := settings.MotionModel(); m != "" {
		return m
	}
	return DefaultModel(ctx, res)
}

// DefaultModel is ResolveModel without the model saved in the settings: what writes the pages when none is saved.
func DefaultModel(ctx context.Context, res llm.Resolved) string {
	if o, ok := res.Provider.(*llm.Ollama); ok {
		lctx, cancel := context.WithTimeout(ctx, preferredLookup)
		defer cancel()
		if installed, err := o.Installed(lctx); err == nil {
			for _, want := range PreferredModels {
				for _, m := range installed {
					if m.Name == want {
						return want
					}
				}
			}
		}
	}
	if res.Model != "" {
		return res.Model
	}
	return assist.ModelFromEnv("", os.Getenv)
}

// llmWriter writes with the provider routed to the motion-graphics task.
type llmWriter struct {
	res   llm.Resolved
	model string
}

// NewWriter builds the Writer of the provider the motion-graphics task is routed to (or the
// one the context asks for), and says which model it will use.
func NewWriter(ctx context.Context) (Writer, ModelInfo, error) {
	res, err := llm.ForContext(ctx, settings.TaskMotionGraphics)
	if err != nil {
		return nil, ModelInfo{}, err
	}
	model := ResolveModel(ctx, res)
	return &llmWriter{res: res, model: model}, ModelInfo{Provider: res.Name, Model: model}, nil
}

func (l *llmWriter) Write(ctx context.Context, system, user string, maxTokens int) (string, error) {
	// An Ollama on this machine loads the model into the GPUs; it is dropped as soon as the page is
	// written so the pictures of the same run find their memory free (other providers ignore it).
	// A provider that is not an Ollama may be a model that thinks before it answers (Gemini Pro, Claude with thinking): the
	// thinking counts against the limit, so the page gets the room the thinking took (measured: at 8000 every scene of
	// a 15-scene batch came back with no page).
	if l.res.Type != settings.TypeOllama {
		maxTokens *= remoteTokenFactor
	}
	resp, err := l.res.Provider.Chat(ctx, llm.Request{Model: l.model, System: system, User: user, MaxTokens: maxTokens, Temperature: 0.4,
		Local: &llm.LocalOptions{KeepAlive: "0"}})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(resp.Text) == "" && resp.Truncated {
		return "", fmt.Errorf("%s ran out of its %d tokens before writing the page (a model that thinks spends them first)", l.res.Name, maxTokens)
	}
	return resp.Text, nil
}

type modelKey struct{}

// WithModel makes the writers built from ctx use this model instead of the one the task resolves to; empty changes nothing.
func WithModel(ctx context.Context, model string) context.Context {
	if model == "" {
		return ctx
	}
	return context.WithValue(ctx, modelKey{}, model)
}

func modelFrom(ctx context.Context) string {
	m, _ := ctx.Value(modelKey{}).(string)
	return m
}

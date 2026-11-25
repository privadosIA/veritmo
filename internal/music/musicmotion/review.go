package musicmotion

// review.go: a draft or a whole piece graded against the rubric by a vision model (spec motion-scene-review). The reviewer
// sees the frames of a draft tiled in one picture and answers, in one call, a score of 0 to 3 and a note for each criterion; the
// score of 0 to 10 (their sum, scaled) and the verdict are worked out here, not taken from the model.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"sort"
	"strings"

	"veritmo/internal/music/hyperframes"
	"veritmo/internal/video/motion"
)

// Limits of a review.
const (
	MaxReviewNotes     = 5
	MaxReviewNoteChars = 200
	// DefaultReviewAttempts and MaxReviewAttempts are how many rewrites a draft under the minimum gets.
	DefaultReviewAttempts = 2
	MaxReviewAttempts     = 3
)

// Vision looks at pictures with a prompt and says what it saw: the model routed to the check task. It returns the text of the
// answer and the name of the model.
type Vision interface {
	Look(ctx context.Context, system, user string, images [][]byte) (text, model string, err error)
}

// ReviewAsk is what a request asks of the review of a draft: the review itself and, with Min, the rewrites.
type ReviewAsk struct {
	// Min is the score a draft has to reach (0 to 10); under it the draft is rewritten with the notes. 0 only reviews.
	Min int `json:"min,omitempty"`
	// Attempts is how many rewrites it gets (DefaultReviewAttempts when 0, at most MaxReviewAttempts).
	Attempts int `json:"attempts,omitempty"`
}

// Rewrites is how many times a draft under the minimum is rewritten.
func (a ReviewAsk) Rewrites() int {
	if a.Min <= 0 {
		return 0
	}
	switch {
	case a.Attempts <= 0:
		return DefaultReviewAttempts
	case a.Attempts > MaxReviewAttempts:
		return MaxReviewAttempts
	}
	return a.Attempts
}

// Validate checks what a request asked for.
func (a ReviewAsk) Validate() error {
	if a.Min < 0 || a.Min > 10 {
		return fmt.Errorf("review.min must be between 0 and 10, got %d", a.Min)
	}
	if a.Attempts < 0 || a.Attempts > MaxReviewAttempts {
		return fmt.Errorf("review.attempts must be between 0 and %d, got %d", MaxReviewAttempts, a.Attempts)
	}
	return nil
}

// ReviewAttempt is one version of a draft that was reviewed: the climb the person sees.
type ReviewAttempt struct {
	Score   int      `json:"score"`
	Verdict string   `json:"verdict"`
	Notes   []string `json:"notes"`
}

// CriterionScore is what the reviewer said of one criterion of the rubric.
type CriterionScore struct {
	ID    string `json:"id"`
	Score int    `json:"score"`
	Note  string `json:"note,omitempty"`
}

// Review is the grade of a draft.
type Review struct {
	Score   int      `json:"score"`
	Verdict string   `json:"verdict"`
	Notes   []string `json:"notes"`
	// Frames are the sentences the reviewer wrote first, one per frame, saying what a stranger sees there and naming every object.
	Frames []string `json:"frames,omitempty"`
	// Criteria are the scores of the reviewer, one per criterion of the rubric it answered.
	Criteria []CriterionScore `json:"criteria,omitempty"`
	Model    string           `json:"model,omitempty"`
	// Attempts are every version that was reviewed, in order, when the draft was rewritten from its review.
	Attempts []ReviewAttempt `json:"attempts,omitempty"`
	// Error says why there is no grade (the vision model failed or answered nothing readable); the draft is still ready.
	Error string `json:"error,omitempty"`
}

var errUnreadableReview = errors.New("the reviewer's answer is not readable")

const reviewSystemHead = `You are a strict art director grading ONE scene of an animated music video from one picture that tiles frames of it in time order (left to right, then top to bottom). You look as a stranger would, who sees the video once. A frame may carry a layer drawn from a whole animated block or component (a full-frame shader, an interface mock-up, a lower third): it is part of the design, not a defect, so judge whether it serves the picture, not that it is there.
FIRST describe each frame in one sentence, as that stranger would say it, naming every object you see ("a red truck on a road at dusk, the word NIGHT in white"); an object you would have to explain ("those bars are mirrors") is a fault of the metaphor. Never describe what the scene was meant to be, only what is there.
THEN score the scene on each criterion from 0 to 3 and give ONE note per criterion that is not a 3, in the form "frame N · where in the frame · the problem → the exact change" (for example "frame 2 · right edge · the word NIGHT is clipped → move it 6 vh to the left"), never praise. The criteria:
`

func reviewSystem() string {
	var keys []string
	for _, c := range RubricCriteria {
		keys = append(keys, fmt.Sprintf(`{"id": %q, "score": 0 to 3, "note": "..."}`, c.Key))
	}
	return reviewSystemHead + RubricQuestions() + `Answer ONLY a JSON object: {"frames": ["<one sentence per frame, in order>", ...], "criteria": [` + strings.Join(keys, ", ") + `]}`
}

// reviewUser says what the frames are.
func reviewUser(n int, lines []Line, brief, world string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "One picture with %d frames of one scene, in time order.\n", n)
	if w := strings.TrimSpace(world); w != "" {
		b.WriteString("The world of the video, which the metaphor should belong to (a world that is itself abstract is fine for abstract shapes): " + w + "\n")
	}
	if len(lines) > 0 {
		b.WriteString("The verse of the scene (the words should appear as they are sung):\n")
		for _, l := range lines {
			fmt.Fprintf(&b, "- %q\n", l.Text)
		}
	}
	if brief = strings.TrimSpace(brief); brief != "" {
		b.WriteString("The idea the scene was asked for: " + brief + "\n")
	}
	return b.String()
}

type reviewAnswer struct {
	Frames   []string `json:"frames"`
	Criteria []struct {
		ID    string  `json:"id"`
		Score float64 `json:"score"`
		Note  string  `json:"note"`
	} `json:"criteria"`
	// Scores and Notes are the shorter shape a model may answer with: a score per criterion and a list of notes.
	Scores map[string]float64 `json:"scores"`
	Notes  []string           `json:"notes"`
}

// unreadableWords are the words a stranger's description uses when the picture could not be told: a shape, bars, a blob, something
// abstract or unclear.
var unreadableWords = []string{"shape", "shapes", "bars", "blob", "abstract", "unclear"}

// unreadableIn is the word of unreadableWords that a frame's sentence uses as a whole word and the world of the video does not name
// (a world that says "abstract shapes" is the video's own choice; an empty world never exempts), or "".
func unreadableIn(sentence, world string) string {
	words := func(s string) map[string]bool {
		set := map[string]bool{}
		for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !(r >= 'a' && r <= 'z') }) {
			set[w] = true
		}
		return set
	}
	in, named := words(sentence), words(world)
	for _, w := range unreadableWords {
		if in[w] && !named[w] {
			return w
		}
	}
	return ""
}

func parseReview(text, world string) (Review, error) {
	raw := extractJSON(text)
	if raw == "" {
		return Review{}, errUnreadableReview
	}
	var a reviewAnswer
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return Review{}, errUnreadableReview
	}
	scores := map[string]float64{}
	var criteria []CriterionScore
	var lowNotes []string
	for _, c := range a.Criteria {
		known := false
		for _, k := range RubricCriteria {
			known = known || k.Key == c.ID
		}
		if !known {
			continue
		}
		v := max(0, min(MaxCriterionScore, c.Score))
		scores[c.ID] = v
		note := strings.Join(strings.Fields(c.Note), " ")
		criteria = append(criteria, CriterionScore{ID: c.ID, Score: int(v + 0.5), Note: note})
	}
	for k, v := range a.Scores {
		if _, there := scores[k]; !there {
			scores[k] = v
		}
	}
	if len(scores) == 0 {
		return Review{}, errUnreadableReview
	}
	// The notes are the criteria's notes, the lowest scores first, then the loose ones.
	sorted := append([]CriterionScore(nil), criteria...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Score < sorted[j].Score })
	for _, c := range sorted {
		if c.Note != "" && c.Score < MaxCriterionScore {
			lowNotes = append(lowNotes, c.Note)
		}
	}
	// The metaphor is judged on what the reviewer itself said it saw: a frame that a stranger could not name caps it at 1, so that the
	// description and the score agree.
	var frames []string
	for _, f := range a.Frames {
		if f = strings.Join(strings.Fields(f), " "); f != "" {
			frames = append(frames, f)
		}
	}
	var capNote string
	for _, f := range frames {
		if w := unreadableIn(f, world); w != "" {
			if scores["metaphor"] > 1 {
				scores["metaphor"] = 1
				for i := range criteria {
					if criteria[i].ID == "metaphor" {
						criteria[i].Score = 1
					}
				}
				capNote = fmt.Sprintf("metaphor · a stranger sees %q (%s) · the picture must be an object a stranger can name, drawn from the world of the video", clipSentence(f, 90), w)
			}
			break
		}
	}
	if capNote != "" {
		lowNotes = append([]string{capNote}, lowNotes...)
	}
	score := ScoreOf(scores)
	return Review{Score: score, Verdict: VerdictOf(score), Notes: cleanNotes(append(lowNotes, a.Notes...)), Criteria: criteria, Frames: frames}, nil
}

// clipSentence is a sentence cut to at most n characters.
func clipSentence(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func cleanNotes(in []string) []string {
	out := []string{}
	for _, n := range in {
		n = strings.Join(strings.Fields(n), " ")
		if n == "" {
			continue
		}
		if r := []rune(n); len(r) > MaxReviewNoteChars {
			n = string(r[:MaxReviewNoteChars-1]) + "…"
		}
		out = append(out, n)
		if len(out) == MaxReviewNotes {
			break
		}
	}
	return out
}

// ReviewFrames grades the frames of a scene. An unreadable answer is asked for once more; a second one, or a failure of the
// vision model, gives a Review with Error and no grade. It returns an error only when the context is cancelled.
func ReviewFrames(ctx context.Context, v Vision, frames [][]byte, lines []Line, brief, world string) (Review, error) {
	if v == nil {
		return Review{Error: "there is no vision model to review the drafts"}, nil
	}
	if len(frames) == 0 {
		return Review{Error: "there are no frames to review"}, nil
	}
	// One picture of the frames, tiled two by two for four of them: one image for the model to look at, and one call.
	pics := frames
	if len(frames) > 1 {
		cols := 2
		if len(frames) > 4 {
			cols = 3
		}
		if sheet, err := ContactSheet(frames, cols, 640, 360); err == nil {
			pics = [][]byte{sheet}
		}
	}
	for try := 0; try < 2; try++ {
		text, model, err := v.Look(ctx, reviewSystem(), reviewUser(len(frames), lines, brief, world), pics)
		if ctx.Err() != nil {
			return Review{}, ctx.Err()
		}
		if err != nil {
			return Review{Error: "the vision model failed: " + err.Error()}, nil
		}
		r, perr := parseReview(text, world)
		if perr == nil {
			r.Model = model
			return r, nil
		}
	}
	return Review{Error: "the vision model's answer could not be read as a review"}, nil
}

// ProblemFromReview is what the writer is told when a draft is rewritten from its review.
func ProblemFromReview(r Review) string {
	return fmt.Sprintf("A reviewer who looked at the previews graded it %d out of 10 and found: %s. Fix these and keep what works.", r.Score, strings.Join(r.Notes, "; "))
}

// attempt is the review as one line of the climb.
func (r Review) attempt() ReviewAttempt {
	return ReviewAttempt{Score: r.Score, Verdict: r.Verdict, Notes: r.Notes}
}

// --- a whole piece -------------------------------------------------------------------------------

// SceneReview is the grade of one scene of a piece.
type SceneReview struct {
	ScriptPiece string   `json:"script_piece"`
	Score       int      `json:"score"`
	Verdict     string   `json:"verdict"`
	Notes       []string `json:"notes"`
	Error       string   `json:"error,omitempty"`
}

// Overall is the grade of the piece as a whole: the consistency of its type, palette and frame, and its variety.
type Overall struct {
	Score int      `json:"score"`
	Notes []string `json:"notes"`
	Error string   `json:"error,omitempty"`
	// Repeated are the script pieces that use the same metaphor, when the reviewer saw any.
	Repeated [][]string `json:"repeated,omitempty"`
	// PicturesAlone and CausesNext answer, "yes" or "no", whether the frames still tell the story with the words covered and whether each
	// frame causes the next; empty when the reviewer did not answer.
	PicturesAlone string `json:"pictures_alone,omitempty"`
	CausesNext    string `json:"causes_next,omitempty"`
}

const overallSystem = `You are a strict art director grading a whole animated music video from a contact sheet: one frame of each scene, tiled in order from left to right and top to bottom, numbered by the order they are given. Score two things from 0 to 2 each: "consistency" (the same typographic system, the same palette with one accent, the same frame labels and finish across the scenes) and "variety" (each scene has its own visual metaphor: no scene repeats another's). Also answer two questions with "yes" or "no": "pictures_alone" (with every word covered, do the frames still tell a story a stranger can follow?) and "causes_next" (does each frame cause the next, or are they a list of unrelated pictures?). Give up to five concrete notes naming the scenes by their number, in the form "scenes 3 and 4 · what is wrong → the exact change"; name there the looks that read as generated and the scenes that show them (a breathing, floating or pulsing loop as the motion of a title, a title that keeps sliding or scaling after it lands, bounce on text, every scene entering the same way, a waveform or spectrum bar as the main picture, one treatment for a long stretch). "repeated": groups of scene numbers that use the same metaphor (an empty list when none). Answer ONLY a JSON object: {"scores": {"consistency": 0 to 2, "variety": 0 to 2}, "pictures_alone": "yes" or "no", "causes_next": "yes" or "no", "notes": ["..."], "repeated": [[1, 4], [7, 8, 9]]}`

// ReviewOverall grades the contact sheet of a piece: ids are the script pieces in the order of the tiles.
func ReviewOverall(ctx context.Context, v Vision, sheet []byte, ids []string) (Overall, error) {
	if v == nil {
		return Overall{Error: "there is no vision model to review the piece"}, nil
	}
	user := fmt.Sprintf("A contact sheet of %d scenes. The scenes in order are: %s.", len(ids), strings.Join(ids, ", "))
	for try := 0; try < 2; try++ {
		text, _, err := v.Look(ctx, overallSystem, user, [][]byte{sheet})
		if ctx.Err() != nil {
			return Overall{}, ctx.Err()
		}
		if err != nil {
			return Overall{Error: "the vision model failed: " + err.Error()}, nil
		}
		var a struct {
			Scores        map[string]float64 `json:"scores"`
			Notes         []string           `json:"notes"`
			Repeated      [][]int            `json:"repeated"`
			PicturesAlone any                `json:"pictures_alone"`
			CausesNext    any                `json:"causes_next"`
		}
		raw := extractJSON(text)
		if raw == "" || json.Unmarshal([]byte(raw), &a) != nil || len(a.Scores) == 0 {
			continue
		}
		clamp := func(x float64) float64 { return max(0, min(2, x)) }
		o := Overall{Score: int((clamp(a.Scores["consistency"])+clamp(a.Scores["variety"]))*10/4 + 0.5), Notes: cleanNotes(a.Notes),
			PicturesAlone: yesNo(a.PicturesAlone), CausesNext: yesNo(a.CausesNext)}
		for _, group := range a.Repeated {
			var names []string
			for _, n := range group {
				if n >= 1 && n <= len(ids) {
					names = append(names, ids[n-1])
				}
			}
			if len(names) > 1 {
				o.Repeated = append(o.Repeated, names)
			}
		}
		return o, nil
	}
	return Overall{Error: "the vision model's answer could not be read as a review"}, nil
}

// yesNo reads a yes or no answer a model may write as a string or a boolean; anything else is "".
func yesNo(v any) string {
	switch x := v.(type) {
	case bool:
		if x {
			return "yes"
		}
		return "no"
	case string:
		switch strings.ToLower(strings.Trim(strings.TrimSpace(x), ".!")) {
		case "yes", "true", "y":
			return "yes"
		case "no", "false", "n":
			return "no"
		}
	}
	return ""
}

// ContactSheet tiles one PNG frame per scene into one PNG, in order, left to right and top to bottom. Frames that are not PNG
// are skipped.
func ContactSheet(frames [][]byte, columns, tileW, tileH int) ([]byte, error) {
	if len(frames) == 0 {
		return nil, errors.New("there are no frames to tile")
	}
	if columns < 1 {
		columns = 1
	}
	rows := (len(frames) + columns - 1) / columns
	sheet := image.NewRGBA(image.Rect(0, 0, columns*tileW, rows*tileH))
	for i, f := range frames {
		img, err := png.Decode(bytes.NewReader(f))
		if err != nil {
			continue
		}
		x0, y0 := (i%columns)*tileW, (i/columns)*tileH
		b := img.Bounds()
		for y := 0; y < tileH; y++ {
			for x := 0; x < tileW; x++ {
				sx, sy := b.Min.X+x*b.Dx()/tileW, b.Min.Y+y*b.Dy()/tileH
				sheet.Set(x0+x, y0+y, img.At(sx, sy))
			}
		}
	}
	// a thin grid so the tiles can be told apart
	line := image.NewUniform(image.White)
	for c := 1; c < columns; c++ {
		draw.Draw(sheet, image.Rect(c*tileW-1, 0, c*tileW+1, rows*tileH), line, image.Point{}, draw.Src)
	}
	for r := 1; r < rows; r++ {
		draw.Draw(sheet, image.Rect(0, r*tileH-1, columns*tileW, r*tileH+1), line, image.Point{}, draw.Src)
	}
	var out bytes.Buffer
	if err := png.Encode(&out, sheet); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// PieceFrames draws frames of one script piece's page as the render draws it (its real page and song map), at the given times in
// the piece's own seconds.
func PieceFrames(ctx context.Context, browser string, pm PieceMotion, width, height int, times []float64) ([][]byte, error) {
	s, err := motionOpen(ctx, browser, pm, width, height)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	var out [][]byte
	for _, at := range times {
		f, err := s.Frame(ctx, min(max(at, 0), max(pm.Seconds-0.02, 0)))
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// ReviewTimes are the three moments of a script piece that are looked at: near its start, at its first word and near its end.
func ReviewTimes(sc Scene) []float64 {
	first := sc.Duration / 2
	for _, l := range sc.Lines {
		if len(l.Words) > 0 {
			first = l.Words[0].Start + 0.4
			break
		}
		if l.End > l.Start {
			first = l.Start + 0.4
			break
		}
	}
	return []float64{0.3, min(first, sc.Duration-0.3), sc.Duration - 0.25}
}

func motionOpen(ctx context.Context, browser string, pm PieceMotion, width, height int) (*motion.Session, error) {
	page := motion.Page{HTML: pm.HTML, Width: width, Height: height, Duration: pm.Seconds, Song: pm.Song}
	hyperframes.Extend(&page)
	return motion.Open(ctx, browser, page, motion.Limits{})
}

// ReleaseVision drops the vision model from memory when it holds some (a big local model: the next work needs the room).
func (e *DraftEngine) ReleaseVision(ctx context.Context) {
	if u, ok := e.Vision.(interface{ Unload(context.Context) }); ok {
		u.Unload(ctx)
	}
}

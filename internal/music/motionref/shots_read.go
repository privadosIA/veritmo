package motionref

// shots_read.go: the shots of a reference read by the vision model, one closed question each. It has the safeguards of the
// look: every picture goes through the age check first (a person who is or may be a minor stops the reading and the frames
// are deleted), a model that declines is reported and never replaced, and an answer that is not the JSON asked for is asked
// again once and then recorded as unknown. See specs motion-references, "The shots are read when there is a model", and
// design D5.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"veritmo/internal/assistant/describe"
)

// The states of the reading of the shots (Analysis.BreakdownState).
const (
	BreakdownPending = "pending"
	BreakdownReading = "reading"
	BreakdownDone    = "done"
	BreakdownSkipped = "skipped"
	BreakdownFailed  = "failed"
)

// MaxReadShots is how many shots are read: spread over the video, all of them when there are fewer.
const MaxReadShots = 40

// readWidth is the width the pictures given to the model are scaled to.
const readWidth = 640

// ShotAsker is what the reading of the shots needs of the vision model: the age check of a picture, a closed question
// about some pictures, and giving the model back at the end. describe.Session is one.
type ShotAsker interface {
	// Check is nil when nobody in the picture is, or may be, a minor.
	Check(ctx context.Context, picture []byte) error
	// Ask returns the model's text about the pictures; a refusal is an error that is a describe.ErrRefused.
	Ask(ctx context.Context, system, user string, pictures [][]byte, asJSON bool, maxTokens int) (string, error)
	Close()
}

// Reader opens the model for a job that will make at most planned calls. An error says why there is no model to read with.
type Reader func(ctx context.Context, planned int) (ShotAsker, error)

// The vocabularies of the answer.
var (
	subjects  = []string{"typography", "figure", "chart", "ui", "photo", "abstract", "empty"}
	textKinds = []string{"none", "word", "line", "block"}
	textSizes = []string{"small", "medium", "huge"}
	entrances = []string{"cut", "fade", "slide", "scale", "letter", "word", "unknown"}
	cameras   = []string{"static", "zoom", "pan", "shake"}
)

const shotSystem = "You look at two pictures of ONE shot of a music video: the first is the start of the shot and the second its middle. " +
	"Answer with ONE JSON object and nothing else, with exactly these keys: " +
	`"subject": one of typography|figure|chart|ui|photo|abstract|empty (what fills the picture: typography is text as the picture), ` +
	`"text": one of none|word|line|block (how much text is on screen), ` +
	`"text_size": one of small|medium|huge (how big the text is; small when there is none), ` +
	`"entrance": one of cut|fade|slide|scale|letter|word|unknown (how the text or the picture came in, judged from the two pictures), ` +
	`"camera": one of static|zoom|pan|shake. ` +
	"Do not describe people and do not write anything outside the JSON."

const shotUser = "The first picture is the start of the shot, the second its middle. Answer with the JSON."

// chooseShots picks which shots are read: up to MaxReadShots spread evenly over the list (all of them when there are fewer).
func chooseShots(n int) []int {
	if n <= 0 {
		return nil
	}
	count := MaxReadShots
	if n < count {
		count = n
	}
	out := make([]int, count)
	for i := range out {
		out[i] = (2*i + 1) * n / (2 * count)
	}
	return out
}

func shotFrame(dir string, index int, start bool) string {
	if start {
		return filepath.Join(dir, fmt.Sprintf("%d-start.jpg", index))
	}
	return filepath.Join(dir, fmt.Sprintf("%d.jpg", index))
}

// takePicture writes the picture of the video at a time as a JPEG of readWidth.
func (a *Analyser) takePicture(ctx context.Context, video string, at float64, path string) error {
	if _, err := a.run().Run(ctx, "ffmpeg", []string{"-v", "error", "-y", "-ss", strconv.FormatFloat(at, 'f', 3, 64), "-i", video,
		"-frames:v", "1", "-vf", fmt.Sprintf("scale=%d:-2,format=yuvj420p", readWidth), "-q:v", "3", path}); err != nil {
		return fmt.Errorf("cannot read a picture of the video: %w", err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Size() == 0 {
		return errors.New("cannot read a picture of the video")
	}
	return nil
}

// ReadShots reads up to MaxReadShots shots with the vision model and writes the readings, the sheet with them and the state
// of the reading in the analysis. The pictures of the shots (<index>.jpg, the middle, and <index>-start.jpg) stay in
// shotsDir. progress is called when the state changes so the caller can save it. Nothing that goes wrong in the model is
// an error of the call: it is the state of the reading (skipped or failed, with its note); only a cancelled context is.
func (a *Analyser) ReadShots(ctx context.Context, video, shotsDir string, an *Analysis, progress func()) error {
	say := func(state, note string) {
		an.BreakdownState, an.BreakdownNote = state, note
		if progress != nil {
			progress()
		}
	}
	if len(an.Shots) == 0 {
		say(BreakdownSkipped, "no shots were read: the video has no shots to read")
		return nil
	}
	if a.Reader == nil {
		say(BreakdownSkipped, "no shots were read: no vision model is available")
		return nil
	}
	chosen := chooseShots(len(an.Shots))
	asker, err := a.Reader(ctx, 3*len(chosen))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		say(BreakdownSkipped, "no shots were read: "+err.Error())
		return nil
	}
	defer asker.Close()
	say(BreakdownReading, "")
	if err := os.MkdirAll(shotsDir, 0o755); err != nil {
		return err
	}
	readings := map[int]*ShotReading{}
	for _, idx := range chosen {
		if err := ctx.Err(); err != nil {
			return err
		}
		s := an.Shots[idx]
		mid, begin := (s.Start+s.End)/2, s.Start+minf(0.1, s.Length()/4)
		var pics [][]byte
		for i, at := range []float64{begin, mid} {
			path := shotFrame(shotsDir, idx, i == 0)
			if err := a.takePicture(ctx, video, at, path); err != nil {
				_ = os.RemoveAll(shotsDir)
				say(BreakdownFailed, err.Error())
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			pics = append(pics, b)
		}
		for _, b := range pics {
			if err := asker.Check(ctx, b); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				a.stopReading(shotsDir, an, idx, err, say)
				return nil
			}
		}
		reading, err := a.readOne(ctx, asker, pics)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			a.stopReading(shotsDir, an, idx, err, say)
			return nil
		}
		readings[idx] = reading
	}
	for idx, r := range readings {
		an.Shots[idx].Read = r
	}
	an.Rhythm = buildRhythm(an.Shots, an.Duration)
	an.RhythmText = RhythmText(an.Rhythm)
	say(BreakdownDone, "")
	return nil
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// stopReading ends the reading for what happened at a shot: a minor deletes every picture of the shots and the readings
// made so far are not kept; a refusal is reported and not replaced; anything else is a failure with its reason.
func (a *Analyser) stopReading(shotsDir string, an *Analysis, idx int, err error, say func(state, note string)) {
	for i := range an.Shots {
		an.Shots[i].Read = nil
	}
	an.Rhythm = buildRhythm(an.Shots, an.Duration)
	an.RhythmText = RhythmText(an.Rhythm)
	switch {
	case errors.Is(err, describe.ErrMinor):
		_ = os.RemoveAll(shotsDir)
		say(BreakdownFailed, fmt.Sprintf("the reading stopped: a person who is, or may be, a minor appears in shot %d, and the pictures of the shots were deleted", idx))
	case errors.Is(err, describe.ErrRefused):
		say(BreakdownFailed, "the reading stopped: the vision model declined to read the shots")
	default:
		say(BreakdownFailed, "the reading stopped: "+err.Error())
	}
}

// readOne asks the closed question about the two pictures of a shot. An answer that is not the JSON asked for is asked
// again once, with the error; if it still is not, the shot is recorded as unknown. A refusal or a failure of the model is
// an error.
func (a *Analyser) readOne(ctx context.Context, asker ShotAsker, pics [][]byte) (*ShotReading, error) {
	user := shotUser
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		text, err := asker.Ask(ctx, shotSystem, user, pics, true, 200)
		if err != nil {
			return nil, err
		}
		r, perr := parseReading(text)
		if perr == nil {
			return &r, nil
		}
		lastErr = perr
		user = shotUser + "\nYour last answer was not accepted: " + perr.Error() + ". Answer with the JSON object only."
	}
	return &ShotReading{Subject: "unknown", Text: "unknown", TextSize: "unknown", Entrance: "unknown", Camera: "unknown",
		Note: "the model did not answer the JSON asked for: " + lastErr.Error()}, nil
}

// parseReading reads the model's answer as a ShotReading, in the vocabularies of the question.
func parseReading(text string) (ShotReading, error) {
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return ShotReading{}, errors.New("there is no JSON object in it")
	}
	var r ShotReading
	if err := json.Unmarshal([]byte(text[start:end+1]), &r); err != nil {
		return ShotReading{}, fmt.Errorf("it is not valid JSON (%w)", err)
	}
	for _, f := range []struct {
		name  string
		value *string
		valid []string
	}{{"subject", &r.Subject, subjects}, {"text", &r.Text, textKinds}, {"text_size", &r.TextSize, textSizes}, {"entrance", &r.Entrance, entrances}, {"camera", &r.Camera, cameras}} {
		*f.value = strings.ToLower(strings.TrimSpace(*f.value))
		ok := false
		for _, v := range f.valid {
			if *f.value == v {
				ok = true
			}
		}
		if !ok {
			return ShotReading{}, fmt.Errorf("%q must be one of %s", f.name, strings.Join(f.valid, "|"))
		}
	}
	r.Note = ""
	return r, nil
}

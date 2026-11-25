package motionref

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/assistant/describe"
)

// fakeAsker plays the vision model: answers is what it says to each question in turn (the last one repeats), minorAt is the
// number of the picture checked (from 1) that shows a minor, and refuse makes it decline.
type fakeAsker struct {
	answers []string
	asked   int
	checked int
	minorAt int
	refuse  bool
	closed  bool
	prompts []string
}

func (f *fakeAsker) Check(context.Context, []byte) error {
	f.checked++
	if f.minorAt > 0 && f.checked == f.minorAt {
		return fmt.Errorf("%w (the picture)", describe.ErrMinor)
	}
	return nil
}

func (f *fakeAsker) Ask(_ context.Context, _, user string, pics [][]byte, _ bool, _ int) (string, error) {
	f.prompts = append(f.prompts, user)
	if len(pics) != 2 {
		return "", fmt.Errorf("%d pictures: the start and the middle of the shot are given", len(pics))
	}
	if f.refuse {
		return "", describe.ErrRefused
	}
	i := f.asked
	if i >= len(f.answers) {
		i = len(f.answers) - 1
	}
	f.asked++
	return f.answers[i], nil
}

func (f *fakeAsker) Close() { f.closed = true }

const goodJSON = `{"subject":"typography","text":"word","text_size":"huge","entrance":"letter","camera":"static"}`

// analysed is the analysis of the video of three colours with its shots, ready to be read.
func analysed(t *testing.T, a *Analyser) (string, *Analysis) {
	t.Helper()
	video := threeShots(t)
	an := &Analysis{Duration: 9}
	if err := a.Breakdown(context.Background(), video, an); err != nil {
		t.Fatal(err)
	}
	return video, an
}

func readWith(t *testing.T, f *fakeAsker) (*Analysis, string, []string) {
	t.Helper()
	var states []string
	a := &Analyser{Reader: func(context.Context, int) (ShotAsker, error) { return f, nil }}
	video, an := analysed(t, a)
	if an.BreakdownState != BreakdownPending {
		t.Fatalf("a model is there and nothing was read yet: %q", an.BreakdownState)
	}
	dir := filepath.Join(t.TempDir(), "shots")
	if err := a.ReadShots(context.Background(), video, dir, an, func() { states = append(states, an.BreakdownState) }); err != nil {
		t.Fatal(err)
	}
	return an, dir, states
}

func TestReadShots_EveryShotIsReadAndTheSheetSaysHowTheTextEnters(t *testing.T) {
	needTools(t)
	f := &fakeAsker{answers: []string{goodJSON}}
	an, dir, states := readWith(t, f)
	if an.BreakdownState != BreakdownDone || strings.Join(states, ",") != "reading,done" || !f.closed {
		t.Fatalf("state %q (%v) closed %v", an.BreakdownState, states, f.closed)
	}
	if len(an.Shots) != 3 || f.asked != 3 || f.checked != 6 {
		t.Fatalf("%d shots, asked %d, checked %d pictures (two for each shot)", len(an.Shots), f.asked, f.checked)
	}
	for i, s := range an.Shots {
		if s.Read == nil || s.Read.Entrance != "letter" || s.Read.Subject != "typography" {
			t.Fatalf("shot %d: %+v", i, s.Read)
		}
		for _, p := range []string{shotFrame(dir, i, false), shotFrame(dir, i, true)} {
			if fi, err := os.Stat(p); err != nil || fi.Size() == 0 {
				t.Fatalf("the pictures of the shot stay with the reference: %v", err)
			}
		}
	}
	if an.Rhythm.TextLedShare == nil || *an.Rhythm.TextLedShare != 1 || an.Rhythm.Entrances["letter"] != 3 {
		t.Fatalf("%+v", an.Rhythm)
	}
	if !strings.Contains(an.RhythmText, "100 % of shots are text filling the frame, entering by letter (3)") {
		t.Fatal(an.RhythmText)
	}
}

func TestReadShots_ProseThenJSONIsRetriedOnceWithTheError(t *testing.T) {
	needTools(t)
	f := &fakeAsker{answers: []string{"It looks like some big text.", goodJSON}}
	an, _, _ := readWith(t, f)
	if an.Shots[0].Read == nil || an.Shots[0].Read.Subject != "typography" {
		t.Fatalf("%+v", an.Shots[0].Read)
	}
	if len(f.prompts) < 2 || !strings.Contains(f.prompts[1], "not accepted") || !strings.Contains(f.prompts[1], "no JSON object") {
		t.Fatalf("the second question carries the error: %q", f.prompts)
	}
}

func TestReadShots_ProseTwiceMakesThatShotUnknownAndKeepsTheOthers(t *testing.T) {
	needTools(t)
	// shot 0 gets prose twice; the other two get the JSON
	f := &fakeAsker{answers: []string{"words words", "more words", goodJSON}}
	an, _, _ := readWith(t, f)
	if an.BreakdownState != BreakdownDone {
		t.Fatalf("%q %q", an.BreakdownState, an.BreakdownNote)
	}
	if r := an.Shots[0].Read; r == nil || r.Subject != "unknown" || !strings.Contains(r.Note, "did not answer the JSON") {
		t.Fatalf("%+v", an.Shots[0].Read)
	}
	if r := an.Shots[1].Read; r == nil || r.Subject != "typography" {
		t.Fatalf("the others are kept: %+v", an.Shots[1].Read)
	}
	if an.Rhythm.Subjects["unknown"] != 0 || an.Rhythm.Subjects["typography"] != 2 {
		t.Fatalf("an unknown reading counts for nothing: %+v", an.Rhythm.Subjects)
	}
}

func TestReadShots_AnAnswerOutsideTheVocabularyIsNotAccepted(t *testing.T) {
	if _, err := parseReading(`{"subject":"spaceship","text":"word","text_size":"huge","entrance":"cut","camera":"static"}`); err == nil || !strings.Contains(err.Error(), `"subject"`) {
		t.Fatalf("%v", err)
	}
	r, err := parseReading("Sure!\n```json\n" + strings.ToUpper(goodJSON) + "\n```")
	if err != nil || r.Subject != "typography" {
		t.Fatalf("a fenced answer in capitals is read: %+v %v", r, err)
	}
}

func TestReadShots_AMinorStopsTheReadingAndTheFramesAreDeleted(t *testing.T) {
	needTools(t)
	f := &fakeAsker{answers: []string{goodJSON}, minorAt: 3} // the first picture of the second shot
	an, dir, _ := readWith(t, f)
	if an.BreakdownState != BreakdownFailed || !strings.Contains(an.BreakdownNote, "minor") || !strings.Contains(an.BreakdownNote, "shot 1") {
		t.Fatalf("%q %q", an.BreakdownState, an.BreakdownNote)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("the pictures of the shots were deleted")
	}
	for _, s := range an.Shots {
		if s.Read != nil {
			t.Fatalf("no reading is kept: %+v", s.Read)
		}
	}
	if an.Rhythm.TextLedShare != nil || strings.Contains(an.RhythmText, "text filling") {
		t.Fatal("the sheet has no readings")
	}
	if f.asked > 1 {
		t.Fatalf("the reading stopped: %d questions", f.asked)
	}
}

func TestReadShots_ARefusalIsReportedAndNeverReplaced(t *testing.T) {
	needTools(t)
	an, _, _ := readWith(t, &fakeAsker{refuse: true})
	if an.BreakdownState != BreakdownFailed || !strings.Contains(an.BreakdownNote, "declined") {
		t.Fatalf("%q %q", an.BreakdownState, an.BreakdownNote)
	}
	for _, s := range an.Shots {
		if s.Read != nil {
			t.Fatal("a shot was given a reading the model refused to make")
		}
	}
}

func TestReadShots_WithoutAModelTheReadingIsSkippedWithTheReason(t *testing.T) {
	needTools(t)
	a := &Analyser{}
	video, an := analysed(t, a)
	if an.BreakdownState != BreakdownSkipped || !strings.Contains(an.BreakdownNote, "no vision model") {
		t.Fatalf("%q %q", an.BreakdownState, an.BreakdownNote)
	}
	if err := a.ReadShots(context.Background(), video, filepath.Join(t.TempDir(), "s"), an, nil); err != nil || an.BreakdownState != BreakdownSkipped {
		t.Fatalf("%v %q", err, an.BreakdownState)
	}
	b := &Analyser{Reader: func(context.Context, int) (ShotAsker, error) {
		return nil, errors.New("the provider has no vision model")
	}}
	if err := b.ReadShots(context.Background(), video, filepath.Join(t.TempDir(), "s"), an, nil); err != nil || an.BreakdownState != BreakdownSkipped || !strings.Contains(an.BreakdownNote, "no vision model") {
		t.Fatalf("%v %q %q", err, an.BreakdownState, an.BreakdownNote)
	}
}

func TestChooseShots_SpreadEvenlyAndAtMostForty(t *testing.T) {
	if got := chooseShots(3); len(got) != 3 || got[0] != 0 || got[2] != 2 {
		t.Fatalf("%v", got)
	}
	got := chooseShots(200)
	if len(got) != MaxReadShots || got[0] != 2 || got[len(got)-1] != 197 {
		t.Fatalf("%v", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Fatalf("increasing: %v", got)
		}
	}
	if len(chooseShots(0)) != 0 {
		t.Fatal("no shots, nothing to read")
	}
}

func TestChooseShots_ChangesAreAmongTheChosenWhenTheyAreAmongTheShots(t *testing.T) {
	// 200 shots: every other one starts with a change; the reading picks evenly over the union
	chosen := chooseShots(200)
	changes := 0
	for _, i := range chosen {
		if i%2 == 1 {
			changes++
		}
	}
	if changes == 0 || changes == len(chosen) {
		t.Fatalf("%d of %d chosen shots are changes", changes, len(chosen))
	}
}

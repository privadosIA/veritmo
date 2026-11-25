package motionref

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"veritmo/internal/assistant/describe"
)

func analyseWith(t *testing.T, looker Looker) (*Analysis, string) {
	t.Helper()
	needTools(t)
	dir := filepath.Join(t.TempDir(), "f")
	an, err := (&Analyser{Looker: looker}).Analyse(context.Background(), makeVideo(t, changing), dir)
	if err != nil {
		t.Fatal(err)
	}
	return an, dir
}

func TestTheLookIsDescribedFromAFewFrames(t *testing.T) {
	var got int
	an, _ := analyseWith(t, func(ctx context.Context, frames [][]byte) (string, error) {
		got = len(frames)
		if len(frames[0]) == 0 {
			t.Fatal("the frames are pictures")
		}
		return "Dark, neon green, monospace.", nil
	})
	if got != lookFrames || an.Description != "Dark, neon green, monospace." || an.DescriptionNote != "" {
		t.Fatalf("%d %+v", got, an)
	}
}

func TestWithoutAModelTheAnalysisIsCompleteAndSaysWhy(t *testing.T) {
	for name, looker := range map[string]Looker{
		"no looker": nil,
		"not installed": func(context.Context, [][]byte) (string, error) {
			return "", fmt.Errorf("%w: no vision model", describe.ErrNotInstalled)
		},
	} {
		an, _ := analyseWith(t, looker)
		if an.Description != "" || !strings.Contains(an.DescriptionNote, "no description") || len(an.Frames) == 0 || len(an.Colours) == 0 {
			t.Fatalf("%s: %+v", name, an)
		}
	}
}

func TestARefusalIsReportedAndNothingIsInvented(t *testing.T) {
	an, _ := analyseWith(t, func(context.Context, [][]byte) (string, error) {
		return "", &describe.RefusedError{Model: "m"}
	})
	if an.Description != "" || !strings.Contains(an.DescriptionNote, "declined") {
		t.Fatalf("%+v", an)
	}
}

func TestAMinorStopsTheDescriptionAndThatFrameIsDeleted(t *testing.T) {
	an, dir := analyseWith(t, func(context.Context, [][]byte) (string, error) {
		return "", &describe.MinorFrameError{Frame: 1, Err: describe.ErrMinor}
	})
	if an.Description != "" || !strings.Contains(an.DescriptionNote, "minor") {
		t.Fatalf("%+v", an)
	}
	var deleted string
	if m := regexp.MustCompile(`frame (\d\d\.jpg)`).FindStringSubmatch(an.DescriptionNote); m != nil {
		deleted = m[1]
	}
	if deleted == "" {
		t.Fatalf("the note names the frame: %q", an.DescriptionNote)
	}
	if _, err := os.Stat(filepath.Join(dir, deleted)); err == nil {
		t.Fatal("the frame of a minor is deleted")
	}
	for _, f := range an.Frames {
		if f == deleted {
			t.Fatal("and it is not in the analysis")
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != len(an.Frames) {
		t.Fatalf("%d files for %d frames", len(entries), len(an.Frames))
	}
}

func TestACancelledAnalysisIsNotAFailureOfTheDescription(t *testing.T) {
	needTools(t)
	ctx, cancel := context.WithCancel(context.Background())
	_, err := (&Analyser{Looker: func(context.Context, [][]byte) (string, error) {
		cancel()
		return "", errors.New("stopped")
	}}).Analyse(ctx, makeVideo(t, changing), filepath.Join(t.TempDir(), "f"))
	if err == nil {
		t.Fatal("a cancelled context stops the analysis")
	}
}

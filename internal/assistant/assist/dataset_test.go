package assist

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseDatasetReply(t *testing.T) {
	got := ParseDatasetReply("Sure!\n<think>x</think>\n## s01 | The hive at dawn\nDialogue: \"hola\"\nMotion: push-in\na wooden beehive\n\n**## s02 | Honey**\n```\nsecond picture\n```\n## s03 | Empty\n")
	if len(got) != 2 || got[0].ID != "s01" || got[0].Title != "The hive at dawn" || !strings.Contains(got[0].Body, "Motion: push-in") ||
		got[1].ID != "s02" || got[1].Body != "second picture" {
		t.Fatalf("%+v", got)
	}
	if got := ParseDatasetReply("I cannot help with that"); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestValidateExamples(t *testing.T) {
	ok := []Example{{Prompt: "a"}, {Prompt: "b", Negative: "c", Tags: "d"}}
	if err := ValidateExamples(ok); err != nil {
		t.Fatal(err)
	}
	for name, ex := range map[string][]Example{
		"six":      {{Prompt: "1"}, {Prompt: "2"}, {Prompt: "3"}, {Prompt: "4"}, {Prompt: "5"}, {Prompt: "6"}},
		"empty":    {{Prompt: "  "}},
		"too long": {{Prompt: strings.Repeat("a", MaxExampleLength+1)}},
		"negative": {{Prompt: "a", Negative: strings.Repeat("a", MaxExampleLength+1)}},
	} {
		if err := ValidateExamples(ex); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestExamplesBlock(t *testing.T) {
	if examplesBlock(context.Background()) != "" || examplesBlock(WithExamples(context.Background(), nil)) != "" {
		t.Fatal("no examples, no block")
	}
	b := examplesBlock(WithExamples(context.Background(), []Example{{Prompt: "neon street", Negative: "blur", Tags: "night"}, {Prompt: "second"}}))
	for _, want := range []string{"never copy", "Example 1: neon street", "Negative: blur", "Tags: night", "Example 2: second"} {
		if !strings.Contains(b, want) {
			t.Errorf("block lacks %q:\n%s", want, b)
		}
	}
}

func TestDatasetModels3DAsksForOneObjectAlone(t *testing.T) {
	sys := datasetSystem(DatasetInput{Mode: DatasetModels3D})
	for _, want := range []string{"3D models", "single object alone", "no people", "NO label", "At most 12"} {
		if !strings.Contains(strings.ToLower(sys), strings.ToLower(want)) {
			t.Errorf("the system prompt lacks %q:\n%s", want, sys)
		}
	}
	if user := datasetUser(DatasetInput{Mode: DatasetModels3D, Text: "props", Engine: "x", Look: "y"}); strings.Contains(user, "Video generator") || strings.Contains(user, "Look of") {
		t.Errorf("the video's lines are not the models': %s", user)
	}
	a := &Assistant{}
	if _, err := a.Dataset(context.Background(), DatasetInput{Mode: "audio", Text: "x"}); !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "models3d") {
		t.Errorf("a wrong mode lists the accepted ones: %v", err)
	}
}

func TestKindModel3DIsTheCallersAndTheTaskIsNotOffered(t *testing.T) {
	if k, err := ParseKind(" Model3D "); err != nil || k != KindModel3D {
		t.Errorf("%v %v", k, err)
	}
	if g := ParseKindReply("KIND=model3d; LANG=es; TITLE=Un patin"); g.Understood || g.Kind != "" {
		t.Errorf("the model does not choose it: %+v", g)
	}
	if g := ParseKindReply("KIND=image; TITLE=x"); !g.Understood || g.Kind != KindImage {
		t.Errorf("the others still are read: %+v", g)
	}
	if _, err := ParseTask("model3d"); err == nil {
		t.Error("the task of the request planner is not offered through ParseTask")
	}
	if _, ok := tasks[TaskModel3D]; !ok || !strings.Contains(tasks[TaskModel3D].system, "ONE object") {
		t.Errorf("the task: %+v", tasks[TaskModel3D])
	}
}

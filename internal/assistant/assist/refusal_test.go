package assist

import (
	"context"
	"errors"
	"strings"
	"testing"

	"veritmo/internal/platform/edition"
	"veritmo/internal/platform/sched"
)

func TestIsRefusal(t *testing.T) {
	for _, s := range []string{
		"I'm sorry, but I can't help with that request.",
		"I’m sorry, I can’t do that.", // curly apostrophes
		"I can't write that.",
		"I cannot assist with this.",
		"I can not help.",
		"Sorry, no.",
		"Sorry",
		"I'm unable to comply.",
		"I am unable to do that.",
		"I won't write that.",
		"I will not write that.",
		"I apologize, but that is not something I can write.",
		"As an AI, I must decline.",
		"Lo siento, no puedo ayudar con eso.",
		"No puedo escribir eso.",
		"Lamento no poder ayudarte.",
		"No me es posible hacerlo.",
		`"I'm sorry, I can't."`,
		"  \n I CANNOT do that",
	} {
		if !IsRefusal(s) {
			t.Errorf("a refusal was not recognized: %q", s)
		}
	}
}

func TestIsRefusalLeavesPromptsAlone(t *testing.T) {
	long := "Sorry " + strings.Repeat("lovely ", 90) // a long reply is never a refusal
	for _, s := range []string{
		"",
		"A woman in black lace lingerie lies on a velvet bed, her silhouette framed by soft morning light.",
		"Sorry-looking dog sitting on a rainy porch, cinematic light, 50mm lens.",
		"Sorrysomething is not a word that opens a refusal.",
		"The photograph shows a man who says: I can't wait.",
		long,
	} {
		if IsRefusal(s) {
			t.Errorf("a prompt was taken for a refusal: %.60q", s)
		}
	}
}

func TestRun_ARefusalIsNotASuggestion(t *testing.T) {
	f := &fakeOllama{answer: "I'm sorry, but I can't help with that request."}
	a := setup(t, f)
	for _, task := range []Task{TaskImage, TaskMotion} {
		out, err := a.Run(context.Background(), task, "something")
		if !errors.Is(err, ErrRefused) || out != "" {
			t.Fatalf("%s: out %q err %v", task, out, err)
		}
		if !strings.Contains(err.Error(), "qwen3:8b") {
			t.Errorf("the message names the model: %v", err)
		}
		// Only the private edition tells how to get another answer.
		if got := strings.Contains(err.Error(), "another installed model"); got != edition.Private() {
			t.Errorf("advice to choose another model must appear only in the private edition: %v", err)
		}
	}
	if f.unloadCount() == 0 {
		t.Error("the model is unloaded after a refusal too")
	}
}

func TestMusic_ARefusalIsNotAFormatError(t *testing.T) {
	f := &fakeOllama{answer: "I cannot write lyrics about that."}
	a := setup(t, f)
	_, err := a.Music(context.Background(), "a song", false)
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v", err)
	}
	f.answer = "Lo siento, no puedo."
	if _, err := a.Music(context.Background(), "una canción", true); !errors.Is(err, ErrRefused) {
		t.Errorf("instrumental: %v", err)
	}
	// a normal answer, even one that mentions "sorry" inside the lyrics, is parsed
	f.answer = "STYLE: soft pop, piano\nLYRICS:\n[Verse]\nI'm sorry, my love\n[Chorus]\nCome home"
	m, err := a.Music(context.Background(), "a song", false)
	if err != nil || !strings.Contains(m.Lyrics, "I'm sorry, my love") {
		t.Errorf("%+v %v", m, err)
	}
}

func TestListModels(t *testing.T) {
	f := &fakeOllama{}
	a := setup(t, f)
	f.mu.Lock()
	f.models = []string{"qwen3:8b", "gemma4:31b", "nomic-embed-text:latest", "dengcao/Qwen3-Reranker-0.6B:F16"}
	f.sizes = map[string]int64{
		"qwen3:8b":                        5 * sched.GiB,
		"gemma4:31b":                      19 * sched.GiB,
		"nomic-embed-text:latest":         274 << 20,
		"dengcao/Qwen3-Reranker-0.6B:F16": 1 * sched.GiB,
	}
	f.mu.Unlock()
	got, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "qwen3:8b" || got[1].Name != "gemma4:31b" {
		t.Fatalf("only the chat models, smallest first: %+v", got)
	}
	if !got[0].Fits || got[0].SizeBytes != 5*sched.GiB {
		t.Errorf("5 GB with 27 GiB free fits: %+v", got[0])
	}
	// 19 GiB x 1.3 + 4 = 28.7 GiB > 27 GiB available
	if got[1].Fits {
		t.Errorf("the 19 GB model does not fit 27 GiB free: %+v", got[1])
	}
	a.MemAvailable = func() (int64, error) { return 0, errors.New("no meminfo") }
	got, _ = a.ListModels(context.Background())
	if !got[1].Fits {
		t.Error("when the memory cannot be read, nothing is marked as not fitting")
	}
}

// The application adds no content rule: its instructions only describe the
// format and length of the answer.
func TestInstructionsCarryNoContentRule(t *testing.T) {
	all := []string{musicSystem, musicInstrumentalSystem}
	for _, spec := range tasks {
		all = append(all, spec.system)
	}
	for _, s := range all {
		low := strings.ToLower(s)
		for _, w := range []string{"adult", "explicit", "nsfw", "appropriate", "safe", "policy", "forbidden", "must not", "never write", "inappropriate", "harmful"} {
			if strings.Contains(low, w) {
				t.Errorf("an instruction carries a content word %q: %.80s", w, s)
			}
		}
	}
}

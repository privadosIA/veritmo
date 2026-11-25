package describe

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"veritmo/internal/assistant/llm"
	"veritmo/internal/platform/settings"
)

// fakeOpenAI answers the chat completions a describe job makes, by the system prompt.
type fakeOpenAI struct {
	mu     sync.Mutex
	paths  []string
	models []string
	age    string
	reply  string
	status int // when not 0, every chat answers this status with noVisionBody
}

func (f *fakeOpenAI) describer(t *testing.T) *Describer {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		f.mu.Unlock()
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f.mu.Lock()
		f.models = append(f.models, body["model"].(string))
		f.mu.Unlock()
		if f.status != 0 {
			w.WriteHeader(f.status)
			_, _ = w.Write([]byte(`{"error":{"message":"this model does not support image input"}}`))
			return
		}
		sys := body["messages"].([]any)[0].(map[string]any)["content"].(string)
		content := f.reply
		switch {
		case strings.HasPrefix(sys, "You check a picture"):
			content = f.age
		case strings.HasPrefix(sys, "You write motion prompts"):
			content = "slow push in"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]any{"content": content}, "finish_reason": "stop"}}})
	}))
	t.Cleanup(srv.Close)
	return &Describer{
		Host: srv.URL, Generic: llm.NewOpenAI(srv.URL, ""), ModelName: "gpt-vision", ProviderID: "cloud", ProviderName: "Cloud",
		MemAvailable: func() (int64, error) { return 1, nil }, // an Ollama memory rule would refuse this
		Frames: func(ctx context.Context, path string, kind Kind) ([][]byte, error) {
			return [][]byte{{1}, {2}, {3}, {4}}[:map[Kind]int{KindImage: 1, KindVideo: 4}[kind]], nil
		},
	}
}

func newOpenAIFake() *fakeOpenAI {
	return &fakeOpenAI{age: `{"people":1,"minor":false,"unclear":false}`, reply: goodText}
}

func TestGenericProvider_DescribesWithItsOwnModelAndNothingOfOllama(t *testing.T) {
	f := newOpenAIFake()
	d := f.describer(t)
	res, err := d.Describe(context.Background(), file(t, "a.png"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Prompt != goodText || res.Model != "gpt-vision" || res.Provider != "cloud" {
		t.Fatalf("unexpected %+v", res)
	}
	for _, p := range f.paths {
		if p != "/v1/chat/completions" {
			t.Errorf("only chat calls expected (no tags, show, ps or unload), got %s", p)
		}
	}
	if len(f.paths) != 2 {
		t.Errorf("a picture is two calls (age check, description): %v", f.paths)
	}
}

func TestGenericProvider_KeepsTheSafeguards(t *testing.T) {
	f := newOpenAIFake()
	f.age = `{"people":2,"minor":true,"unclear":false}`
	d := f.describer(t)
	res, err := d.Describe(context.Background(), file(t, "a.png"), Options{})
	if !errors.Is(err, ErrMinor) || res.Prompt != "" {
		t.Fatalf("a minor must be refused with any provider: %v %+v", err, res)
	}
	g := newOpenAIFake()
	g.reply = "I'm sorry, but I can't describe this image."
	d2 := g.describer(t)
	_, err = d2.Describe(context.Background(), file(t, "a.png"), Options{})
	var re *RefusedError
	if !errors.As(err, &re) || re.Provider != "cloud" || re.Model != "gpt-vision" {
		t.Fatalf("a refusal names the provider and model: %v", err)
	}
}

func TestGenericProvider_AModelThatCannotReadPictures(t *testing.T) {
	f := newOpenAIFake()
	f.status = http.StatusBadRequest
	d := f.describer(t)
	_, err := d.Describe(context.Background(), file(t, "a.png"), Options{})
	if !errors.Is(err, llm.ErrNoVision) || !strings.Contains(err.Error(), "reads pictures") {
		t.Fatalf("want a vision error, got %v", err)
	}
}

func TestGenericProvider_NoVisionModelDeclared(t *testing.T) {
	f := newOpenAIFake()
	d := f.describer(t)
	d.ModelName = ""
	if _, err := d.Describe(context.Background(), file(t, "a.png"), Options{}); !errors.Is(err, llm.ErrNoModel) {
		t.Fatalf("want ErrNoModel, got %v", err)
	}
	if len(f.paths) != 0 {
		t.Error("nothing may be sent without a model")
	}
	// a model named by the caller is used
	d.ModelName = "declared"
	if _, err := d.Describe(context.Background(), file(t, "a.png"), Options{Model: "other"}); err != nil {
		t.Fatal(err)
	}
	if f.models[0] != "other" {
		t.Errorf("the caller's model wins: %v", f.models)
	}
}

func TestCallCap(t *testing.T) {
	// planned: picture 2; clip as one scene 2 per frame + 1 (4 frames = 9); split 3 per shot
	for _, c := range []struct {
		name    string
		file    string
		split   bool
		shots   int
		cap     int
		wantErr bool
		planned int
	}{
		{"picture under", "a.png", false, 0, 2, false, 2},
		{"picture over", "a.png", false, 0, 1, true, 2},
		{"clip one scene under", "c.mp4", false, 0, 9, false, 9},
		{"clip one scene over", "c.mp4", false, 0, 8, true, 9},
		{"split under", "c.mp4", true, 12, 36, false, 36},
		{"split over", "c.mp4", true, 12, 30, true, 36},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newOpenAIFake()
			d := f.describer(t)
			d.MaxCalls = c.cap
			f.reply = goodText
			if c.shots > 0 {
				ranges := make([]ShotRange, c.shots)
				frames := make([][]byte, c.shots)
				for i := range ranges {
					ranges[i] = ShotRange{float64(i) * 2, float64(i)*2 + 2}
					frames[i] = []byte{byte(i + 1)}
				}
				d.Shots = func(context.Context, string) ([]ShotRange, [][]byte, error) { return ranges, frames, nil }
			}
			_, err := d.Describe(context.Background(), file(t, c.file), Options{Split: c.split})
			if c.wantErr {
				var tm *TooManyCallsError
				if !errors.Is(err, ErrTooManyCalls) || !errors.As(err, &tm) || tm.Planned != c.planned || tm.Cap != c.cap {
					t.Fatalf("want a cap error with %d planned and cap %d, got %v", c.planned, c.cap, err)
				}
				if len(f.paths) != 0 {
					t.Errorf("a job over the cap must make no call: %v", f.paths)
				}
				if !strings.Contains(err.Error(), "Cloud") {
					t.Errorf("the message names the provider: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNoCapForALocalProvider(t *testing.T) {
	f := newFake() // Ollama fake, MaxCalls 0
	d := f.server(t)
	if _, err := d.Describe(context.Background(), file(t, "c.mp4"), Options{}); err != nil {
		t.Fatalf("no cap by default: %v", err)
	}
}

func TestRefusalOffersTheOtherProvidersAndNeverRetriesByItself(t *testing.T) {
	f := newOpenAIFake()
	f.reply = "I'm sorry, but I can't describe this image."
	d := f.describer(t)
	d.Others = func(ctx context.Context) []Alternative {
		return []Alternative{{Provider: "ollama", Name: "Ollama", Model: "gemma4:26b"}, {Provider: "lan", Name: "LAN", Model: "llava"}}
	}
	_, err := d.Describe(context.Background(), file(t, "a.png"), Options{})
	var re *RefusedError
	if !privateEdition {
		if !errors.As(err, &re) || len(re.Choices) != 0 || strings.Contains(err.Error(), "another provider") {
			t.Fatalf("the public edition suggests no other provider: %v", err)
		}
	} else {
		if !errors.As(err, &re) || len(re.Choices) != 2 || re.Choices[0].Provider != "ollama" || re.Choices[0].Model != "gemma4:26b" {
			t.Fatalf("the alternatives must include the other providers: %v", err)
		}
		if !strings.Contains(err.Error(), "Ollama (gemma4:26b)") {
			t.Errorf("the message lists them: %v", err)
		}
	}
	// exactly the calls of one attempt: age check + one description, nothing more
	if len(f.paths) != 2 {
		t.Errorf("no automatic retry: %d calls", len(f.paths))
	}
}

func TestRefusalByOllamaListsItsOtherModelsAsChoicesOfTheSameProvider(t *testing.T) {
	f := newFake()
	f.describeReply = "I'm sorry, but I can't describe this."
	d := f.server(t)
	_, err := d.Describe(context.Background(), file(t, "a.png"), Options{})
	var re *RefusedError
	if !errors.As(err, &re) {
		t.Fatalf("%v", err)
	}
	if !privateEdition {
		if len(re.Choices) != 0 {
			t.Fatalf("the public edition suggests nothing: %+v", re)
		}
		return
	}
	if len(re.Choices) != 1 || re.Choices[0] != (Alternative{Provider: "ollama", Name: "Ollama", Model: detailModel}) {
		t.Fatalf("%v %+v", err, re)
	}
}

func TestResolvedBuilders(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OLLAMA_HOST", "")
	if _, err := settings.AddProvider(settings.Provider{ID: "cloud", Name: "Cloud", Type: settings.TypeOpenAI, BaseURL: "https://api.example.com/v1", VisionModel: "gpt-4o"}); err != nil {
		t.Fatal(err)
	}
	if err := settings.Acknowledge("cloud"); err != nil {
		t.Fatal(err)
	}
	res, err := llm.For(settings.TaskDescribeImage, "cloud")
	if err != nil {
		t.Fatal(err)
	}
	d := FromResolved(res)
	if d.Generic == nil || d.ModelName != "gpt-4o" || d.MaxCalls != settings.DefaultMaxCalls || d.ProviderID != "cloud" {
		t.Errorf("remote openai: %+v", d)
	}
	if TaskOf(KindVideo) != settings.TaskDescribeVideo || TaskOf(KindImage) != settings.TaskDescribeImage {
		t.Error("TaskOf")
	}
	// the built-in Ollama through ForContext is the old FromEnv, with no cap
	b, err := ForContext(context.Background(), KindImage)
	if err != nil || b.Generic != nil || b.MaxCalls != 0 || b.Host != "http://127.0.0.1:11434" {
		t.Errorf("built-in: %+v %v", b, err)
	}
	// an override goes through the same checks
	c, err := ForContext(llm.WithProvider(context.Background(), "cloud"), KindVideo)
	if err != nil || c.Generic == nil {
		t.Errorf("override: %+v %v", c, err)
	}
	if err := settings.DeleteProvider("cloud"); err != nil {
		t.Fatal(err)
	}
	if _, err := ForContext(llm.WithProvider(context.Background(), "cloud"), KindImage); !errors.Is(err, llm.ErrUnknownProvider) {
		t.Errorf("unknown: %v", err)
	}
}

package assist

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"veritmo/internal/assistant/llm"
	"veritmo/internal/platform/sched"
)

// fakeOllama answers the four endpoints the assistant uses and records what
// it was asked.
type fakeOllama struct {
	mu        sync.Mutex
	models    []string // installed (name:tag)
	modelSize int64
	sizes     map[string]int64 // per-model size, overriding modelSize
	answer    string
	doneRsn   string
	vram      int64 // size_vram reported by /api/ps
	chatCode  int
	chats     []map[string]any
	unloads   int
	paths     []string
}

func (f *fakeOllama) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.paths = append(f.paths, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/api/tags":
			var ms []map[string]any
			for _, m := range f.models {
				size := f.modelSize
				if v, ok := f.sizes[m]; ok {
					size = v
				}
				ms = append(ms, map[string]any{"name": m, "model": m, "size": size})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"models": ms})
		case "/api/chat":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.chats = append(f.chats, body)
			if f.chatCode != 0 {
				w.WriteHeader(f.chatCode)
				_, _ = w.Write([]byte(`{"error":"boom"}`))
				return
			}
			rsn := f.doneRsn
			if rsn == "" {
				rsn = "stop"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"message": map[string]any{"role": "assistant", "content": f.answer}, "done_reason": rsn})
		case "/api/ps":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{
				{"name": "qwen3:8b", "model": "qwen3:8b", "size": 6 << 30, "size_vram": f.vram}}})
		case "/api/generate":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if v, ok := body["keep_alive"].(float64); ok && v == 0 {
				f.unloads++
			}
			_, _ = w.Write([]byte(`{"done":true}`))
		default:
			http.NotFound(w, r)
		}
	})
}

func (f *fakeOllama) chatCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.chats)
}

func (f *fakeOllama) unloadCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.unloads
}

func setup(t *testing.T, f *fakeOllama) *Assistant {
	t.Helper()
	if f.models == nil {
		f.models = []string{"qwen3:8b"}
	}
	if f.modelSize == 0 {
		f.modelSize = 5 * sched.GiB
	}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	a := New(srv.URL, "qwen3:8b")
	a.MemAvailable = func() (int64, error) { return 27 * sched.GiB, nil }
	return a
}

func TestRun_ImageTaskSendsCPUOnlyEnglishRequest(t *testing.T) {
	f := &fakeOllama{answer: "A photorealistic photograph of a woman reading in a sunlit cafe."}
	a := setup(t, f)
	got, err := a.Run(context.Background(), TaskImage, "Una mujer lee un libro en una cafetería soleada")
	if err != nil {
		t.Fatal(err)
	}
	if got != "A photorealistic photograph of a woman reading in a sunlit cafe." {
		t.Errorf("result = %q", got)
	}
	req := f.chats[0]
	opts := req["options"].(map[string]any)
	if opts["num_gpu"] != float64(0) || opts["temperature"] != float64(0) || opts["num_predict"] != float64(160) {
		t.Errorf("options = %v", opts)
	}
	if req["think"] != false || req["stream"] != false || req["keep_alive"] != "30s" {
		t.Errorf("request = %v", req)
	}
	msgs := req["messages"].([]any)
	sys := msgs[0].(map[string]any)["content"].(string)
	usr := msgs[1].(map[string]any)["content"].(string)
	if !strings.Contains(sys, "English") || !strings.Contains(sys, "70 to 100 words") {
		t.Errorf("system prompt = %q", sys)
	}
	if !strings.Contains(usr, "cafetería soleada") || !strings.Contains(usr, "Answer in English") {
		t.Errorf("user prompt = %q", usr)
	}
}

func TestRun_MotionTaskAsksForAtMost25Words(t *testing.T) {
	f := &fakeOllama{answer: "A soft breeze stirs a page as the camera drifts slowly."}
	a := setup(t, f)
	got, err := a.Run(context.Background(), TaskMotion, "una mujer lee un libro en una cafetería")
	if err != nil || got == "" {
		t.Fatalf("%q %v", got, err)
	}
	req := f.chats[0]
	if req["options"].(map[string]any)["num_predict"] != float64(80) {
		t.Errorf("motion limit must be smaller: %v", req["options"])
	}
	sys := req["messages"].([]any)[0].(map[string]any)["content"].(string)
	if !strings.Contains(sys, "at most 25 words") || !strings.Contains(sys, "English") || !strings.Contains(sys, "movement") {
		t.Errorf("system prompt = %q", sys)
	}
}

func TestClean(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		truncated bool
		want      string
	}{
		{"plain", "A woman reads.", false, "A woman reads."},
		{"reasoning removed", "<think>\nlet me think\n</think>\n\nA woman reads…", false, "A woman reads…"},
		{"line breaks and spaces", "  A woman\n reads   a\tbook.\n", false, "A woman reads a book."},
		{"wrapping double quotes", `"A woman reads."`, false, "A woman reads."},
		{"wrapping typographic quotes", "“A woman reads.”", false, "A woman reads."},
		{"truncated cut at last sentence", "A woman reads a book. Soft light fills the caf", true, "A woman reads a book."},
		{"truncated without a sentence end is kept", "A woman reads a book in", true, "A woman reads a book in"},
		{"unclosed reasoning is dropped", "<think>I am still thinking about", false, ""},
		{"empty", "  \n ", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Clean(tc.in, tc.truncated); got != tc.want {
				t.Errorf("Clean(%q, %v) = %q, want %q", tc.in, tc.truncated, got, tc.want)
			}
		})
	}
}

func TestRun_TruncatedGenerationEndsAtLastSentence(t *testing.T) {
	f := &fakeOllama{answer: "A woman reads a book. Warm light falls across the tab", doneRsn: "length"}
	a := setup(t, f)
	got, err := a.Run(context.Background(), TaskImage, "una mujer lee")
	if err != nil || got != "A woman reads a book." {
		t.Fatalf("%q %v", got, err)
	}
}

func TestRun_EmptyAnswerIsAnError(t *testing.T) {
	f := &fakeOllama{answer: "<think>nothing useful</think>"}
	a := setup(t, f)
	_, err := a.Run(context.Background(), TaskImage, "una mujer lee")
	if !errors.Is(err, ErrEmpty) || !strings.Contains(err.Error(), "returned nothing") {
		t.Fatalf("err = %v", err)
	}
}

func TestRun_ModelNotInstalledNeverGenerates(t *testing.T) {
	f := &fakeOllama{models: []string{"llama3:8b"}}
	a := setup(t, f)
	_, err := a.Run(context.Background(), TaskImage, "una mujer lee")
	if !errors.Is(err, ErrNotInstalled) || !strings.Contains(err.Error(), "qwen3:8b") || !strings.Contains(err.Error(), "ollama pull qwen3:8b") {
		t.Fatalf("err = %v", err)
	}
	if f.chatCount() != 0 {
		t.Errorf("%d chat requests were sent", f.chatCount())
	}
}

func TestRun_ModelNameWithoutTagMatchesLatest(t *testing.T) {
	f := &fakeOllama{models: []string{"mymodel:latest"}, answer: "ok."}
	a := setup(t, f)
	a.Model = "mymodel"
	if _, err := a.Run(context.Background(), TaskImage, "texto"); err != nil {
		t.Fatalf("err = %v", err)
	}
}

func TestRun_NotEnoughMemoryNeverGenerates(t *testing.T) {
	f := &fakeOllama{modelSize: 5 * sched.GiB}
	a := setup(t, f)
	// needs 5 GiB x 1.3 + 4 GiB = 10.5 GiB
	a.MemAvailable = func() (int64, error) { return 10 * sched.GiB, nil }
	_, err := a.Run(context.Background(), TaskImage, "una mujer lee")
	if !errors.Is(err, ErrNoMemory) || !strings.Contains(err.Error(), "available") {
		t.Fatalf("err = %v", err)
	}
	if f.chatCount() != 0 {
		t.Errorf("%d chat requests were sent", f.chatCount())
	}
	a.MemAvailable = func() (int64, error) { return 11 * sched.GiB, nil }
	f.answer = "ok."
	if _, err := a.Run(context.Background(), TaskImage, "una mujer lee"); err != nil {
		t.Fatalf("with enough memory: %v", err)
	}
}

func TestRun_OllamaDownNamesTheAddress(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close() // nothing listens there now
	a := New(addr, "qwen3:8b")
	a.MemAvailable = func() (int64, error) { return 27 * sched.GiB, nil }
	_, err = a.Run(context.Background(), TaskImage, "una mujer lee")
	if !errors.Is(err, ErrUnreachable) || !strings.Contains(err.Error(), addr) {
		t.Fatalf("err = %v, want unreachable naming %s", err, addr)
	}
}

func TestRun_ModelFoundInVRAMIsUnloadedAndRefused(t *testing.T) {
	f := &fakeOllama{answer: "A woman reads.", vram: 3 << 30}
	a := setup(t, f)
	_, err := a.Run(context.Background(), TaskImage, "una mujer lee")
	if !errors.Is(err, ErrOnGPU) || !strings.Contains(err.Error(), "VRAM") {
		t.Fatalf("err = %v", err)
	}
	if f.unloadCount() == 0 {
		t.Error("the model was not unloaded")
	}
}

func TestRun_UnloadsAfterSuccessAndAfterFailure(t *testing.T) {
	f := &fakeOllama{answer: "A woman reads."}
	a := setup(t, f)
	if _, err := a.Run(context.Background(), TaskImage, "una mujer lee"); err != nil {
		t.Fatal(err)
	}
	if f.unloadCount() == 0 {
		t.Error("no unload after a success")
	}

	f2 := &fakeOllama{chatCode: 500}
	a2 := setup(t, f2)
	if _, err := a2.Run(context.Background(), TaskImage, "una mujer lee"); err == nil {
		t.Fatal("expected a failure")
	}
	if f2.unloadCount() == 0 {
		t.Error("no unload after a failure")
	}
}

func TestRun_NotInstalledOrNoMemoryDoNotUnloadAnythingNeedlessly(t *testing.T) {
	// A refusal before loading must not leave the server in a worse state;
	// it is fine (and harmless) either way, but it must not generate.
	f := &fakeOllama{models: []string{"other:1b"}}
	a := setup(t, f)
	_, _ = a.Run(context.Background(), TaskImage, "texto")
	for _, p := range f.paths {
		if p == "POST /api/chat" {
			t.Errorf("unexpected chat request: %v", f.paths)
		}
	}
}

func TestInputValidation(t *testing.T) {
	if _, err := ParseTask("foo"); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("ParseTask(foo) = %v", err)
	}
	for _, s := range []string{"image", "motion"} {
		if _, err := ParseTask(s); err != nil {
			t.Errorf("ParseTask(%s) = %v", s, err)
		}
	}
	if err := ValidateText("   "); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("empty text: %v", err)
	}
	if err := ValidateText(strings.Repeat("a", MaxTextLen+1)); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("long text: %v", err)
	}
	if err := ValidateText(strings.Repeat("a", MaxTextLen)); err != nil {
		t.Errorf("text at the limit: %v", err)
	}
	f := &fakeOllama{}
	a := setup(t, f)
	if _, err := a.Run(context.Background(), TaskImage, ""); !errors.Is(err, ErrInvalidInput) || len(f.paths) != 0 {
		t.Errorf("empty text must be refused before contacting Ollama: %v %v", err, f.paths)
	}
}

func TestNormalizeHost(t *testing.T) {
	for in, want := range map[string]string{
		"":                      "http://127.0.0.1:11434",
		"127.0.0.1:11434":       "http://127.0.0.1:11434",
		"localhost":             "http://localhost:11434",
		"0.0.0.0":               "http://127.0.0.1:11434",
		"0.0.0.0:9999":          "http://127.0.0.1:9999",
		":11500":                "http://127.0.0.1:11500",
		"http://box.lan:11434/": "http://box.lan:11434",
		"https://box.lan":       "https://box.lan:11434",
		"192.168.1.5:11434":     "http://192.168.1.5:11434",
	} {
		if got := NormalizeHost(in); got != want {
			t.Errorf("NormalizeHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestModelFromEnv_Precedence(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == "ORCHESTRATOR_ASSIST_MODEL" {
				return v
			}
			return ""
		}
	}
	if got := ModelFromEnv("", env("")); got != DefaultModel || DefaultModel != "qwen3:8b" {
		t.Errorf("default = %q", got)
	}
	if got := ModelFromEnv("", env("gemma:7b")); got != "gemma:7b" {
		t.Errorf("env = %q", got)
	}
	if got := ModelFromEnv("flag:1b", env("gemma:7b")); got != "flag:1b" {
		t.Errorf("flag must win, got %q", got)
	}
}

// A task routed to an OpenAI-compatible provider makes exactly one chat call: none of
// Ollama's lifecycle (installed-model check, memory check, VRAM verification, unload)
// and none of its options.
func TestRun_GenericProviderSkipsEverythingOfOllama(t *testing.T) {
	var paths []string
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]any{"content": "A photorealistic photograph of a woman reading in a sunlit cafe."}, "finish_reason": "stop"}}})
	}))
	defer srv.Close()
	a := &Assistant{Host: srv.URL, Model: "gpt-x", HTTP: srv.Client(), Generic: llm.NewOpenAI(srv.URL, ""),
		MemAvailable: func() (int64, error) { return 1, nil }} // a memory rule would refuse this
	got, err := a.Run(context.Background(), TaskImage, "Una mujer lee un libro")
	if err != nil || !strings.Contains(got, "woman reading") {
		t.Fatalf("%q %v", got, err)
	}
	if len(paths) != 1 || paths[0] != "/v1/chat/completions" {
		t.Fatalf("exactly one chat call expected, got %v", paths)
	}
	raw, _ := json.Marshal(bodies[0])
	for _, bad := range []string{"num_gpu", "keep_alive", "think"} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("%s must not be sent to another provider: %s", bad, raw)
		}
	}
	if bodies[0]["model"] != "gpt-x" {
		t.Errorf("model %v", bodies[0]["model"])
	}
	// a refusal is detected the same way
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]any{"content": "I'm sorry, I can't help with that."}, "finish_reason": "stop"}}})
	}))
	defer srv2.Close()
	b := &Assistant{Host: srv2.URL, Model: "gpt-x", Generic: llm.NewOpenAI(srv2.URL, "")}
	if _, err := b.Run(context.Background(), TaskImage, "algo"); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "gpt-x") {
		t.Errorf("want a refusal naming the model, got %v", err)
	}
}

func TestFromResolved(t *testing.T) {
	t.Setenv("ORCHESTRATOR_ASSIST_MODEL", "")
	o := llm.NewOpenAI("https://api.example.com/v1", "k")
	a := FromResolved(llm.Resolved{Type: "openai", Host: "https://api.example.com/v1", Model: "gpt-4o-mini", Provider: o}, "")
	if a.Generic == nil || a.Model != "gpt-4o-mini" {
		t.Errorf("openai: %+v", a)
	}
	if b := FromResolved(llm.Resolved{Type: "openai", Model: "gpt-4o-mini", Provider: o}, "gpt-4o"); b.Model != "gpt-4o" {
		t.Errorf("a model named by the caller wins: %q", b.Model)
	}
	e := FromResolved(llm.Resolved{Type: "ollama", Host: "http://192.168.1.60:11434"}, "")
	if e.Generic != nil || e.Host != "http://192.168.1.60:11434" || e.Model != DefaultModel {
		t.Errorf("an extra Ollama keeps the Ollama lifecycle and the default model: %+v", e)
	}
	if f := FromResolved(llm.Resolved{Type: "ollama", Host: "http://h:11434", Model: "declared:8b"}, ""); f.Model != "declared:8b" {
		t.Errorf("declared model: %q", f.Model)
	}
}

func TestRun_TagsTaskAsksForShortStyleTags(t *testing.T) {
	f := &fakeOllama{answer: "romantic,wedding,piano,happy,upbeat,soft vocals"}
	a := setup(t, f)
	got, err := a.Run(context.Background(), TaskTags, "Música romántica de boda con piano y voz suave")
	if err != nil || got != "romantic,wedding,piano,happy,upbeat,soft vocals" {
		t.Fatalf("%q %v", got, err)
	}
	req := f.chats[0]
	if req["options"].(map[string]any)["num_predict"] != float64(60) {
		t.Errorf("tags are short: %v", req["options"])
	}
	sys := req["messages"].([]any)[0].(map[string]any)["content"].(string)
	usr := req["messages"].([]any)[1].(map[string]any)["content"].(string)
	if !strings.Contains(sys, "separated by commas") || !strings.Contains(sys, "genre") || !strings.Contains(usr, "boda") {
		t.Errorf("prompts = %q / %q", sys, usr)
	}
	if task, err := ParseTask("tags"); err != nil || task != TaskTags {
		t.Errorf("ParseTask: %v %v", task, err)
	}
}

// Every task that has a prompt of its own is in the table, with a system prompt and a limit: the tasks were registered by
// init functions before, and a lost one would only show when somebody asked for it.
func TestTasks_TheTableHasEveryTaskThatWritesWithAPrompt(t *testing.T) {
	for _, task := range []Task{TaskImage, TaskTags, TaskMotion, TaskListen, TaskImprove, TaskKind, TaskEdit, TaskModel3D} {
		spec, ok := tasks[task]
		if !ok || spec.system == "" || spec.user == nil || spec.maxTokens <= 0 {
			t.Errorf("the task %q is not in the table or is incomplete: %+v", task, spec)
		}
	}
	if len(tasks) != 8 {
		t.Errorf("the table has %d tasks, want 8", len(tasks))
	}
}

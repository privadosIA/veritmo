package quality

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/assistant/llm"
	"veritmo/internal/platform/runner"
)

// clip makes a 3 s test clip with ffmpeg from a filter graph.
func clip(t *testing.T, name, graph string) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	p := filepath.Join(t.TempDir(), name)
	out, err := exec.Command("ffmpeg", "-y", "-v", "error", "-filter_complex", graph, "-map", "[v]", "-pix_fmt", "yuv420p", p).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	return p
}

const (
	steady = "testsrc=s=320x180:d=3:r=16[v]"
	// a grey half and a blue half: the colour shifts at 1.5 s
	drift = "color=c=0x808080:s=320x180:d=1.5:r=16[a];color=c=0x2040ff:s=320x180:d=1.5:r=16[b];[a][b]concat=n=2:v=1:a=0[v]"
	// a sharp half and a blurred half
	blur = "testsrc=s=320x180:d=1.5:r=16[a];testsrc=s=320x180:d=1.5:r=16,gblur=sigma=25[b];[a][b]concat=n=2:v=1:a=0[v]"
)

// scripted is a Vision that answers by the order of the frames.
type scripted struct {
	calls   int
	verdict func(i int) Judgement
}

func (s *scripted) Judge(context.Context, []byte) (Judgement, error) {
	i := s.calls
	s.calls++
	return s.verdict(i), nil
}

func check(t *testing.T, path string, v Vision) *SceneReport {
	t.Helper()
	rep, err := CheckClip(context.Background(), runner.NewExecRunner(), v, "s", path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestCheckClip_ASteadyClipIsOK(t *testing.T) {
	rep := check(t, clip(t, "a.mp4", steady), nil)
	if rep.Verdict != VerdictOK || rep.GoodUntil != rep.Seconds || len(rep.Frames) != 12 {
		t.Fatalf("%+v", rep)
	}
}

func TestCheckClip_ColourDriftTrimsAtTheShift(t *testing.T) {
	rep := check(t, clip(t, "a.mp4", drift), nil)
	if rep.Verdict != VerdictTrim || rep.GoodUntil < 1.0 || rep.GoodUntil > 1.3 {
		t.Fatalf("a colour shift at 1.5 s must trim there: %+v", rep)
	}
	if !strings.Contains(strings.Join(rep.Issues, ";"), "color") {
		t.Errorf("issues = %v", rep.Issues)
	}
}

func TestCheckClip_LossOfSharpnessIsFlagged(t *testing.T) {
	rep := check(t, clip(t, "a.mp4", blur), nil)
	if rep.Verdict != VerdictTrim || !strings.Contains(strings.Join(rep.Issues, ";"), "blur") {
		t.Fatalf("%+v", rep)
	}
}

func TestCheckClip_VisionNeedsConfirmationUnlessSevere(t *testing.T) {
	path := clip(t, "a.mp4", steady)
	defect := func(sev int) Judgement { return Judgement{Defect: true, Severity: sev, Issue: "melted face"} }

	// one doubtful frame alone is ignored
	rep := check(t, path, &scripted{verdict: func(i int) Judgement {
		if i == 5 {
			return defect(7)
		}
		return Judgement{}
	}})
	if rep.Verdict != VerdictOK || !strings.Contains(strings.Join(rep.Issues, ";"), "doubtful") {
		t.Errorf("a lone doubtful frame: %+v", rep)
	}
	// a lone severe frame counts
	rep = check(t, path, &scripted{verdict: func(i int) Judgement {
		if i == 6 {
			return defect(9)
		}
		return Judgement{}
	}})
	if rep.Verdict != VerdictTrim || rep.GoodUntil != 1.2 {
		t.Errorf("a severe frame at 1.5 s trims to 1.2 s: %+v", rep)
	}
	// two doubtful frames count, from the first of them
	rep = check(t, path, &scripted{verdict: func(i int) Judgement {
		if i >= 8 {
			return defect(7)
		}
		return Judgement{}
	}})
	if rep.Verdict != VerdictTrim || rep.GoodUntil != 1.7 || !strings.Contains(rep.Summary(), "melted face") {
		t.Errorf("flagged from 2 s: %+v", rep)
	}
	// low severity is not a defect
	rep = check(t, path, &scripted{verdict: func(i int) Judgement { return defect(4) }})
	if rep.Verdict != VerdictOK {
		t.Errorf("severity 4 is below the threshold: %+v", rep)
	}
}

func TestCheckClip_BadFromTheStartIsRetried(t *testing.T) {
	path := clip(t, "a.mp4", steady)
	rep := check(t, path, &scripted{verdict: func(i int) Judgement { return Judgement{Defect: true, Severity: 8, Issue: "fused arms"} }})
	if rep.Verdict != VerdictRetry || rep.GoodUntil != 0 {
		t.Fatalf("%+v", rep)
	}
	// bad only after 0.4 s is too little to keep (the minimum is 0.8 s)
	rep = check(t, path, &scripted{verdict: func(i int) Judgement {
		if i >= 2 {
			return Judgement{Defect: true, Severity: 8}
		}
		return Judgement{}
	}})
	if rep.Verdict != VerdictRetry {
		t.Fatalf("%+v", rep)
	}
}

func TestOllama_JudgeAndModels(t *testing.T) {
	var gotModel string
	var gotImages int
	var unloaded bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/chat":
			var req struct {
				Model    string `json:"model"`
				Format   string `json:"format"`
				Think    *bool  `json:"think"`
				Messages []struct {
					Images []string `json:"images"`
				} `json:"messages"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			gotModel, gotImages = req.Model, len(req.Messages[0].Images)
			if req.Format != "json" || req.Think == nil || *req.Think {
				t.Errorf("the request must ask for JSON without thinking: %+v", req)
			}
			json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": `{"defect": true, "severity": 15, "issue": "melted"}`}})
		case "/api/generate":
			var req struct {
				Model     string `json:"model"`
				KeepAlive *int   `json:"keep_alive"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			if req.Model == "small-vision" && req.KeepAlive != nil && *req.KeepAlive == 0 {
				unloaded = true
			}
		case "/api/tags":
			json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{{"name": "big-vision", "size": 20}, {"name": "text-only", "size": 5}, {"name": "small-vision", "size": 10}}})
		case "/api/show":
			var req map[string]string
			json.NewDecoder(r.Body).Decode(&req)
			caps := []string{"completion"}
			if strings.HasSuffix(req["model"], "vision") {
				caps = append(caps, "vision")
			}
			json.NewEncoder(w).Encode(map[string]any{"capabilities": caps})
		}
	}))
	defer srv.Close()
	o := NewOllama(srv.URL, "small-vision")
	j, err := o.Judge(context.Background(), []byte("PNG"))
	if err != nil || !j.Defect || j.Severity != 10 || gotModel != "small-vision" || gotImages != 1 {
		t.Fatalf("%+v %v (model %q, images %d)", j, err, gotModel, gotImages)
	}
	o.Unload(context.Background())
	if !unloaded {
		t.Error("Unload must ask Ollama to drop the model (keep_alive 0)")
	}
	models, err := VisionModels(context.Background(), srv.URL)
	if err != nil || strings.Join(models, ",") != "small-vision,big-vision" {
		t.Fatalf("vision models, smallest first: %v %v", models, err)
	}
}

func TestOllama_Failures(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("x") == "" && strings.Contains(r.URL.Path, "chat") {
			json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": "I cannot tell"}})
		}
	}))
	defer bad.Close()
	if _, err := NewOllama(bad.URL, "m").Judge(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "did not answer JSON") {
		t.Errorf("a non-JSON answer: %v", err)
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "model not found", 404) }))
	defer down.Close()
	if _, err := NewOllama(down.URL, "m").Judge(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("an HTTP error: %v", err)
	}
	if _, err := NewOllama("http://127.0.0.1:1", "m").Judge(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "cannot reach Ollama") {
		t.Errorf("no server: %v", err)
	}
}

// Judged through an OpenAI-compatible provider: one chat call per frame with the
// provider's own model, the answer read as JSON, and no unload (that is Ollama's).
func TestOllama_GenericProviderJudgesAndDoesNotUnload(t *testing.T) {
	var paths []string
	var model string
	var images int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Content []map[string]any `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		model = req.Model
		for _, p := range req.Messages[0].Content {
			if p["type"] == "image_url" {
				images++
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]any{"content": `{"defect": true, "severity": 8, "issue": "extra hand"}`}, "finish_reason": "stop"}}})
	}))
	defer srv.Close()
	o := FromResolved(llm.Resolved{Type: "openai", Host: srv.URL, Model: "gpt-vision", Provider: llm.NewOpenAI(srv.URL, "")}, "ignored")
	j, err := o.Judge(context.Background(), []byte("\x89PNG\r\n\x1a\n"))
	if err != nil || !j.Defect || j.Severity != 8 || model != "gpt-vision" || images != 1 {
		t.Fatalf("%+v %v (model %q, images %d)", j, err, model, images)
	}
	o.Unload(context.Background())
	for _, p := range paths {
		if p != "/v1/chat/completions" {
			t.Errorf("only the chat call expected, got %s (an unload is Ollama's)", p)
		}
	}
	// an Ollama keeps its own behaviour and takes the caller's model
	e := FromResolved(llm.Resolved{Type: "ollama", Host: "http://h:11434"}, "small-vision")
	if e.Generic != nil || e.Model != "small-vision" {
		t.Errorf("%+v", e)
	}
}

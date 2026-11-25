package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/assistant/describe"
)

const cliText = "A photorealistic portrait of one adult woman in her late twenties with long wavy auburn hair, wearing a black lace bra, sitting on the edge of an unmade bed in a dim bedroom, soft warm window light, shallow depth of field, medium shot, confident smile."

func useFakeDescriber(t *testing.T, age string) deps {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{{"name": "gemma4:26b", "size": 10 << 30}}})
		case "/api/show":
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"completion", "vision"}})
		case "/api/chat":
			var body struct {
				Messages []struct{ Content string } `json:"messages"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			reply := cliText
			if strings.HasPrefix(body.Messages[0].Content, "You check a picture") {
				reply = age
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": reply}})
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	dp := realDeps()
	dp.newDescriber = func() *describe.Describer {
		return &describe.Describer{
			Host: srv.URL, HTTP: srv.Client(),
			MemAvailable: func() (int64, error) { return 64 << 30, nil },
			Frames:       func(context.Context, string, describe.Kind) ([][]byte, error) { return [][]byte{{1}}, nil },
		}
	}
	return dp
}

func TestDescribeCLI_PrintsOnlyThePrompt(t *testing.T) {
	dp := useFakeDescriber(t, `{"people":1,"minor":false,"unclear":false}`)
	img := filepath.Join(t.TempDir(), "a.png")
	if err := os.WriteFile(img, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := dp.describe([]string{img}, &out, &errb); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	if strings.TrimSpace(out.String()) != cliText {
		t.Errorf("stdout must be the prompt alone, got %q", out.String())
	}
	if !strings.Contains(errb.String(), "described by gemma4:26b") {
		t.Errorf("the model used must be shown, got %q", errb.String())
	}
}

func TestDescribeCLI_MinorIsRefusedWithNothingOnStdout(t *testing.T) {
	dp := useFakeDescriber(t, `{"people":1,"minor":true,"unclear":false}`)
	img := filepath.Join(t.TempDir(), "a.png")
	if err := os.WriteFile(img, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := dp.describe([]string{img}, &out, &errb); code != 1 {
		t.Fatalf("code %d, want 1", code)
	}
	if out.Len() != 0 {
		t.Errorf("nothing may be printed for a refused file, got %q", out.String())
	}
}

func TestDescribeCLI_Usage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runDescribe(nil, &out, &errb); code != 2 {
		t.Fatalf("code %d, want 2", code)
	}
	if code := runDescribe([]string{"notes.txt"}, &out, &errb); code == 0 {
		t.Fatal("a text file must fail")
	}
}

func TestDescribeCLI_SplitPrintsOneLinePerShot(t *testing.T) {
	dp := useFakeDescriber(t, `{"people":1,"minor":false,"unclear":false}`)
	base := dp.newDescriber
	dp.newDescriber = func() *describe.Describer {
		x := base()
		x.Shots = func(context.Context, string) ([]describe.ShotRange, [][]byte, error) {
			return []describe.ShotRange{{Start: 0, End: 4}, {Start: 4, End: 9.5}}, [][]byte{{1}, {2}}, nil
		}
		return x
	}
	clip := filepath.Join(t.TempDir(), "c.mp4")
	if err := os.WriteFile(clip, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := dp.describe([]string{"--split", clip}, &out, &errb); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var shots []string
	for _, l := range lines {
		if strings.HasPrefix(l, "[") {
			shots = append(shots, l)
		}
	}
	if len(shots) != 2 || !strings.HasPrefix(shots[0], "[0.0-4.0] ") || !strings.HasPrefix(shots[1], "[4.0-9.5] ") {
		t.Fatalf("want one [start-end] line per shot, got %q", out.String())
	}
	if !strings.Contains(errb.String(), "described by gemma4:26b") {
		t.Errorf("the model used must go to stderr: %q", errb.String())
	}
}

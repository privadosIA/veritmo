package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/platform/runner"
	"veritmo/internal/server/assistant"
)

type fakePiper struct{ args []string }

func (f *fakePiper) Run(_ context.Context, name string, args []string, _ ...runner.Option) ([]byte, error) {
	f.args = append([]string{name}, args...)
	return []byte("RIFFfake"), nil
}

func speechServer(t *testing.T) (*Server, *fakePiper) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "voices"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "voices/duo.onnx.json"), []byte(`{"num_speakers":2,"speaker_id_map":{"M":0,"F":1}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"voices/a.onnx", "voices/a.onnx.json", "voices/solo.onnx", "voices/duo.onnx"} {
		if err := os.WriteFile(filepath.Join(root, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := newTestServer(t)
	s.assetsRoot = root
	f := &fakePiper{}
	s.mediaRunner = f
	return s, f
}

func speak(s *Server, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/tts/preview", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.assistant.HandleSpeechPreview(rec, req)
	return rec
}

func TestSpeechPreview(t *testing.T) {
	s, f := speechServer(t)
	rec := speak(s, `{"text":"hola","voice":"voices/a.onnx","speed":2}`)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "audio/wav" || rec.Body.String() != "RIFFfake" {
		t.Fatalf("got %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	if f.args[0] != "piper" || !strings.Contains(strings.Join(f.args, " "), "--length-scale 0.5") {
		t.Fatalf("piper args: %v", f.args)
	}
}

func TestSpeechPreviewRefusals(t *testing.T) {
	s, _ := speechServer(t)
	for name, c := range map[string]struct {
		body string
		code int
	}{
		"empty text":        {`{"text":"  ","voice":"voices/a.onnx"}`, 400},
		"too long":          {`{"text":"` + strings.Repeat("a", assistant.MaxSpeechChars+1) + `","voice":"voices/a.onnx"}`, 400},
		"escaping path":     {`{"text":"x","voice":"voices/../../etc/passwd.onnx"}`, 400},
		"not a voice":       {`{"text":"x","voice":"models/a.onnx"}`, 400},
		"bad speed":         {`{"text":"x","voice":"voices/a.onnx","speed":9}`, 400},
		"not installed":     {`{"text":"x","voice":"voices/nope.onnx"}`, 404},
		"missing companion": {`{"text":"x","voice":"voices/solo.onnx"}`, 404},
	} {
		if rec := speak(s, c.body); rec.Code != c.code {
			t.Errorf("%s: got %d, want %d (%s)", name, rec.Code, c.code, rec.Body.String())
		}
	}
}

func TestSpeechPreviewSpeaker(t *testing.T) {
	s, f := speechServer(t)
	if rec := speak(s, `{"text":"hola","voice":"voices/duo.onnx","speaker":1}`); rec.Code != http.StatusOK || !strings.HasSuffix(strings.Join(f.args, " "), "--speaker 1") {
		t.Fatalf("got %d, piper args %v", rec.Code, f.args)
	}
	for name, body := range map[string]string{
		"past the speakers":    `{"text":"x","voice":"voices/duo.onnx","speaker":2}`,
		"single-speaker voice": `{"text":"x","voice":"voices/a.onnx","speaker":1}`,
		"negative":             `{"text":"x","voice":"voices/duo.onnx","speaker":-1}`,
	} {
		if rec := speak(s, body); rec.Code != 400 {
			t.Errorf("%s: got %d, want 400", name, rec.Code)
		}
	}
}

func TestListAssetsSpeakers(t *testing.T) {
	s, _ := speechServer(t)
	var duo, solo *assetEntry
	list := listAssets(filepath.Join(s.assetsRoot, "voices"))
	for i := range list {
		switch list[i].Name {
		case "duo.onnx":
			duo = &list[i]
		case "a.onnx":
			solo = &list[i]
		}
	}
	if duo == nil || len(duo.Speakers) != 2 || duo.Speakers[1].Name != "F" {
		t.Errorf("duo speakers = %+v", duo)
	}
	if solo == nil || solo.Speakers != nil {
		t.Errorf("a single-speaker voice lists none, got %+v", solo)
	}
}

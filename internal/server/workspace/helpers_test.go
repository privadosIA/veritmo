package workspace

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
	"veritmo/internal/server/servertest"
)

var (
	doJSON          = servertest.DoJSON
	call            = servertest.Call
	isolateSettings = servertest.IsolateSettings
	decodeAPIError  = servertest.DecodeAPIError
)

// installMusicModels puts placeholder music models where a music project is validated against them.
func installMusicModels(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join("models", "music"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{config.DefaultMusicLM, config.DefaultMusicSynth, "Qwen3-Embedding-0.6B-Q8_0.gguf", "vae-BF16.gguf"} {
		if err := os.WriteFile(filepath.Join("models", "music", f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func putConfig(t *testing.T, s *testServer, id, yaml string) *httptest.ResponseRecorder {
	t.Helper()
	return doJSON(t, s.Handler(), http.MethodPut, "/api/projects/"+id+"/config", map[string]string{"yaml": yaml})
}

// fakePeaks answers the ffmpeg that decodes a song with audio, or with the failure.
func fakePeaks(t *testing.T, s *testServer, audio []byte, fail error) *runner.FakeRunner {
	t.Helper()
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if fail != nil {
			return runner.ScriptedResult{Err: fail}
		}
		return runner.ScriptedResult{Stdout: audio}
	})
	s.mediaRunner = fr
	s.look = func(name string) (string, error) { return "/fake/" + name, nil }
	return fr
}

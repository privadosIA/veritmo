package blender

// A measure, not a check: five ideas that use the characters of a project, written by a local model (Ollama) and drawn.
// Skipped without VERITMO_REAL_BLENDER=1 and VERITMO_REAL_OLLAMA=<model>. VERITMO_MODELS_OUT is the folder the descriptions and the preview
// frames are left in.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type ollamaWriter struct {
	model   string
	answers *[]string
}

func (o ollamaWriter) Write(ctx context.Context, system, user string, maxTokens int) (string, error) {
	body, _ := json.Marshal(map[string]any{"model": o.model, "stream": false, "think": false,
		"options":  map[string]any{"num_predict": maxTokens, "temperature": 0.4},
		"messages": []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": user}}})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:11434/api/chat", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Message struct{ Content string } `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	*o.answers = append(*o.answers, out.Message.Content)
	return out.Message.Content, nil
}

func TestRealTheWriterUsesTheModelsOfTheProject(t *testing.T) {
	model := os.Getenv("VERITMO_REAL_OLLAMA")
	if model == "" {
		t.Skip("set VERITMO_REAL_OLLAMA=<model> and VERITMO_REAL_BLENDER=1")
	}
	info, r := realBlender(t)
	outDir := os.Getenv("VERITMO_MODELS_OUT")
	if outDir == "" {
		outDir = t.TempDir()
	}
	os.MkdirAll(outDir, 0o755)
	env := modelEnv(t)
	env.DesignFonts = map[string]bool{} // the previews are drawn without the server's font pack
	ideas := []string{
		"two girls racing across a rink from left to right, one a little behind the other, with the title Patinadoras",
		"a girl jumping on the beat in the middle of the rink while a second one runs past",
		"three girls running in a line toward the camera, the camera slowly moving back",
		"a girl walking slowly across an empty stage and stopping in the middle, the camera dollying in",
		"a crowd of six girls all running the same way at different speeds, a 3D title appears at the end",
	}
	ok, first, clipsOK := 0, 0, 0
	for i, idea := range ideas {
		req := WriteRequest{Idea: idea, Seconds: 5, FPS: 30, Width: 1280, Height: 720, Env: env,
			Families: map[string]string{FontDisplay: "Playfair Display"}}
		start := time.Now()
		var answers []string
		res, err := WriteDescription(context.Background(), ollamaWriter{model, &answers}, req)
		took := time.Since(start).Round(100 * time.Millisecond)
		name := fmt.Sprintf("idea-%d", i+1)
		if err != nil {
			t.Logf("%s: REJECTED after %s: %v", name, took, err)
			os.WriteFile(filepath.Join(outDir, name+".txt"), []byte(idea+"\n\n"+err.Error()), 0o644)
			continue
		}
		ok++
		if res.Attempts > 1 {
			if _, perr := Parse([]byte(ExtractDescriptionJSON(answers[0])), 5, env); perr != nil {
				t.Logf("%s: the first answer failed with: %.300s", name, perr)
			}
		}
		if res.Attempts == 1 {
			first++
		}
		uses := 0
		for _, o := range res.Spec.Objects {
			if o.Type == ModelType && o.Animation != nil {
				uses++
			}
		}
		if uses > 0 {
			clipsOK++
		}
		os.WriteFile(filepath.Join(outDir, name+".scene.json"), res.Raw, 0o644)
		os.WriteFile(filepath.Join(outDir, name+".txt"), []byte(idea), 0o644)
		for _, sec := range []float64{1, 3} {
			png, err := Preview(context.Background(), r, info.Path, res.Spec, PreviewOptions{T: sec, Seconds: 5, FPS: 30, Width: 640, AspectW: 16, AspectH: 9, Sources: env.Sources})
			if err != nil {
				t.Errorf("%s preview at %v: %v", name, sec, err)
				continue
			}
			os.WriteFile(filepath.Join(outDir, fmt.Sprintf("%s-t%.0f.png", name, sec)), png, 0o644)
		}
		t.Logf("%s: valid in %d attempt(s), %s, %d models with a clip", name, res.Attempts, took, uses)
	}
	t.Logf("valid %d of %d, at the first attempt %d, with models that play a clip %d", ok, len(ideas), first, clipsOK)
}

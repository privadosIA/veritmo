package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/video/blender"
)

// statelessScene is a description in seconds and explicit colours: with no project there is no design and no tempo.
const statelessScene = `{"version":1,"camera":{"keys":[{"t":0,"location":[0,-9,1.2],"target":[0,0,0.6]},{"t":4,"location":[0,-7,1],"target":[0,0,0.6]}]},` +
	`"materials":{"m":{"color":"#ff3b30"}},"objects":[{"id":"title","type":"text","text":"Hola","size":1,"material":"m","keys":[{"t":0,"scale":1},{"t":2,"scale":1.2,"ease":"back"}]}]}`

func TestBlenderStateless_TheWriterNeedsNoProjectAndStoresNothing(t *testing.T) {
	s, dir, _ := writerServer(t, statelessScene)
	var asked []blender.WriteRequest
	s.Overrides.BlenderWrite = func(_ context.Context, _ blender.Writer, r blender.WriteRequest) (blender.WriteResult, error) {
		asked = append(asked, r)
		if strings.Contains(r.Idea, "FAIL") {
			return blender.WriteResult{}, fmt.Errorf("%w: nothing moves", blender.ErrRejected)
		}
		spec, _ := blender.Parse([]byte(statelessScene), 4, r.Env)
		return blender.WriteResult{Raw: []byte(statelessScene), Spec: spec, Attempts: 2}, nil
	}
	post := func(body map[string]any) (int, string) {
		rec := doJSON(t, s.Handler(), http.MethodPost, "/api/blender/descriptions", body)
		return rec.Code, rec.Body.String()
	}
	code, body := post(map[string]any{"idea": "  the word Hola  ", "seconds": 5, "aspect_ratio": "9:16"})
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	var out struct {
		Description json.RawMessage
		Resolved    *blender.Spec
		Provider    string
		Model       string
		Attempts    int
	}
	_ = json.Unmarshal([]byte(body), &out)
	if out.Provider != "Ollama" || out.Model != "writer:7b" || out.Attempts != 2 || out.Resolved == nil || !strings.Contains(string(out.Description), `"Hola"`) {
		t.Errorf("answer = %s", body)
	}
	r := asked[0]
	if r.Idea != "the word Hola" || r.Seconds != 5 || r.Width != 720 || r.Height != 1280 || r.FPS != 30 || r.Env.BPM != 0 || !r.Env.NoFiles || len(r.Families) != 0 || r.Current != nil {
		t.Errorf("the writer is told a 9:16 frame, no tempo and no design: %+v", r)
	}
	// without an aspect it is 16:9; with a description it changes that one
	post(map[string]any{"idea": "make it red", "seconds": 4, "description": json.RawMessage(statelessScene)})
	if r := asked[1]; r.Width != 1280 || r.Height != 720 || !strings.Contains(string(r.Current), `"Hola"`) {
		t.Errorf("a change gets the description and a 16:9 frame: %+v", r)
	}
	// nothing was written to the project that exists, and no project was needed
	if entries, _ := os.ReadDir(filepath.Join(dir, "sources")); len(entries) != 0 {
		t.Errorf("nothing is stored: %v", entries)
	}
	for name, b := range map[string]map[string]any{
		"no idea":      {"idea": " ", "seconds": 5},
		"bad seconds":  {"idea": "x", "seconds": 0},
		"too long":     {"idea": "x", "seconds": 601},
		"bad aspect":   {"idea": "x", "seconds": 5, "aspect_ratio": "21:9"},
		"unknown prov": {"idea": "x", "seconds": 5, "provider": "nope"},
	} {
		if code, body := post(b); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	if code, body := post(map[string]any{"idea": "FAIL a scene", "seconds": 5}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "nothing moves") {
		t.Errorf("a rejected scene is 422: %d %s", code, body)
	}
}

func TestBlenderStateless_ThePreviewDrawsADescriptionOfTheBody(t *testing.T) {
	s, dir, _ := writerServer(t, statelessScene)
	s.Overrides.BlenderInfo = func(context.Context) (blender.Info, error) {
		return blender.Info{Path: "/fake/blender", Version: "5.3.0"}, nil
	}
	var got blender.PreviewOptions
	s.Overrides.BlenderPreview = func(_ context.Context, b string, _ *blender.Spec, o blender.PreviewOptions) ([]byte, error) {
		got = o
		return []byte("\x89PNG fake"), nil
	}
	post := func(body map[string]any) (int, string, string) {
		rec := doJSON(t, s.Handler(), http.MethodPost, "/api/blender/preview", body)
		return rec.Code, rec.Header().Get("Content-Type"), rec.Body.String()
	}
	code, ct, body := post(map[string]any{"description": json.RawMessage(statelessScene), "t": 1.5, "seconds": 4, "aspect_ratio": "1:1", "width": 320})
	if code != http.StatusOK || ct != "image/png" || !strings.HasPrefix(body, "\x89PNG") {
		t.Fatalf("%d %q %s", code, ct, body)
	}
	if got.T != 1.5 || got.Seconds != 4 || got.AspectW != 1 || got.AspectH != 1 || got.Width != 320 || got.Sources != "" {
		t.Errorf("options = %+v", got)
	}
	// a key beyond the seconds of the request, and a font file, are refused with their place
	late := strings.Replace(statelessScene, `{"t":4,"location"`, `{"t":9,"location"`, 1)
	if code, _, body := post(map[string]any{"description": json.RawMessage(late), "t": 1, "seconds": 4}); code != http.StatusBadRequest || !strings.Contains(body, "camera") {
		t.Errorf("a key beyond the scene: %d %s", code, body)
	}
	font := strings.Replace(statelessScene, `"size":1,`, `"size":1,"font":"x.ttf",`, 1)
	if code, _, body := post(map[string]any{"description": json.RawMessage(font), "t": 1, "seconds": 4}); code != http.StatusBadRequest || !strings.Contains(body, "no project") {
		t.Errorf("a font file with no project: %d %s", code, body)
	}
	for name, b := range map[string]map[string]any{
		"no description": {"t": 1, "seconds": 4},
		"t outside":      {"description": json.RawMessage(statelessScene), "t": 5, "seconds": 4},
		"bad aspect":     {"description": json.RawMessage(statelessScene), "t": 1, "seconds": 4, "aspect_ratio": "5:1"},
		"wide":           {"description": json.RawMessage(statelessScene), "t": 1, "seconds": 4, "width": 99999},
	} {
		if code, _, body := post(b); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "sources")); len(entries) != 0 {
		t.Errorf("nothing is stored: %v", entries)
	}
	// without Blender: 503 with how to install it
	s.Overrides.BlenderInfo = func(context.Context) (blender.Info, error) {
		return blender.Info{}, fmt.Errorf("%w: %s", blender.ErrNotInstalled, blender.InstallHint)
	}
	if code, _, body := post(map[string]any{"description": json.RawMessage(statelessScene), "t": 1, "seconds": 4}); code != http.StatusServiceUnavailable || !strings.Contains(body, "pacman -S blender") {
		t.Errorf("no Blender: %d %s", code, body)
	}
}

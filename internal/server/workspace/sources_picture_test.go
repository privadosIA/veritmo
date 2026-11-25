package workspace

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/platform/runner"
)

func putPicture(t *testing.T, s *testServer, project, name string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/projects/"+project+"/sources/"+name+"?as=picture", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

const pictureText = "a red apple\nNegative prompt: blurry\nSteps: 25, CFG scale: 7.0, Seed: 42, Size: 512x512, Model: sd15.safetensors"

// pngWithText builds a tiny PNG whose tEXt chunk holds the generation text.
func pngWithText(text string) []byte {
	chunk := func(typ string, data []byte) []byte {
		var b bytes.Buffer
		binary.Write(&b, binary.BigEndian, uint32(len(data)))
		b.WriteString(typ)
		b.Write(data)
		binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(append([]byte(typ), data...)))
		return b.Bytes()
	}
	out := []byte("\x89PNG\r\n\x1a\n")
	out = append(out, chunk("IHDR", make([]byte, 13))...)
	out = append(out, chunk("tEXt", []byte("parameters\x00"+text))...)
	out = append(out, chunk("IDAT", []byte{1})...)
	return append(out, chunk("IEND", nil)...)
}

type pictureAnswer struct {
	Name     string         `json:"name"`
	Metadata map[string]any `json:"metadata"`
}

func decodePicture(t *testing.T, rec *httptest.ResponseRecorder) pictureAnswer {
	t.Helper()
	var a pictureAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	return a
}

// pictureTools fakes ffprobe (tags JSON) and ffmpeg (writes its output file).
func pictureTools(t *testing.T, s *testServer, tags string, ffmpegErr error) *runner.FakeRunner {
	t.Helper()
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		switch inv.Name {
		case "ffprobe":
			return runner.ScriptedResult{Stdout: []byte(tags)}
		default:
			if ffmpegErr != nil {
				return runner.ScriptedResult{Err: ffmpegErr}
			}
			os.WriteFile(inv.Args[len(inv.Args)-1], []byte("FIRSTFRAME"), 0o644)
			return runner.ScriptedResult{}
		}
	})
	s.mediaRunner = fr
	s.look = func(string) (string, error) { return "/usr/bin/ffmpeg", nil }
	return fr
}

func TestPicture_APNGWithMetadataIsStoredAndAnswered(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "p")
	rec := putPicture(t, s, "p", "scene.png", pngWithText(pictureText))
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	a := decodePicture(t, rec)
	if a.Name != "scene.png" || a.Metadata["prompt"] != "a red apple" || a.Metadata["negative_prompt"] != "blurry" ||
		a.Metadata["seed"] != float64(42) || a.Metadata["model"] != "sd15.safetensors" {
		t.Errorf("answer = %+v", a)
	}
	if _, err := os.Stat(filepath.Join(projects, "p", "sources", "scene.png")); err != nil {
		t.Errorf("the picture must be stored: %v", err)
	}
}

func TestPicture_WithoutMetadataAnswersOnlyTheName(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "p")
	rec := putPicture(t, s, "p", "photo.jpg", []byte("\xff\xd8\xff jpeg"))
	if rec.Code != http.StatusCreated || strings.Contains(rec.Body.String(), "metadata") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if decodePicture(t, rec).Name != "photo.jpg" {
		t.Errorf("%s", rec.Body)
	}
}

func TestPicture_AVideoBecomesItsFirstFrameWithItsMetadata(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "p")
	tags, _ := json.Marshal(map[string]any{"format": map[string]any{"tags": map[string]string{"comment": pictureText}}})
	fr := pictureTools(t, s, string(tags), nil)
	rec := putPicture(t, s, "p", "clip.mp4", []byte("VIDEO"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	a := decodePicture(t, rec)
	if a.Name != "clip.png" || a.Metadata["prompt"] != "a red apple" {
		t.Errorf("answer = %+v", a)
	}
	b, err := os.ReadFile(filepath.Join(projects, "p", "sources", "clip.png"))
	if err != nil || string(b) != "FIRSTFRAME" {
		t.Fatalf("%v %q", err, b)
	}
	entries, _ := os.ReadDir(filepath.Join(projects, "p", "sources"))
	if len(entries) != 1 {
		t.Errorf("the video and the temporary files must not stay: %v", entries)
	}
	var sawFrame bool
	for _, c := range fr.Calls() {
		if c.Name == "ffmpeg" && strings.Contains(strings.Join(c.Args, " "), "-frames:v 1") {
			sawFrame = true
		}
	}
	if !sawFrame {
		t.Errorf("ffmpeg must take one frame: %+v", fr.Calls())
	}
}

func TestPicture_AGIFBecomesItsFirstFrame(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "p")
	pictureTools(t, s, `{}`, nil)
	rec := putPicture(t, s, "p", "anim.gif", []byte("GIF89a....."))
	if rec.Code != http.StatusCreated || decodePicture(t, rec).Name != "anim.png" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(projects, "p", "sources", "anim.png")); err != nil {
		t.Error(err)
	}
}

func TestPicture_AVideoWithoutTheOptionStillBecomesAudio(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "p")
	fakeFFmpeg(t, s, true, nil)
	rec := putSource(t, s, "p", "clip.mp4", []byte("VIDEO"))
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"clip.mp3"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(projects, "p", "sources", "clip.png")); err == nil {
		t.Error("no picture must be made without as=picture")
	}
}

func TestPicture_Refusals(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "p")
	pictureTools(t, s, `{}`, nil)
	os.MkdirAll(filepath.Join(projects, "p", "sources"), 0o755)
	os.WriteFile(filepath.Join(projects, "p", "sources", "clip.png"), []byte("MINE"), 0o644)
	if rec := putPicture(t, s, "p", "clip.mp4", []byte("V")); rec.Code != http.StatusConflict {
		t.Errorf("existing target: %d %s", rec.Code, rec.Body)
	}
	if b, _ := os.ReadFile(filepath.Join(projects, "p", "sources", "clip.png")); string(b) != "MINE" {
		t.Error("the existing picture must be unchanged")
	}
	if rec := putPicture(t, s, "p", "song.mp3", []byte("A")); rec.Code != http.StatusBadRequest {
		t.Errorf("an audio file is not a picture: %d %s", rec.Code, rec.Body)
	}
	if rec := putPicture(t, s, "p", "empty.png", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("empty: %d %s", rec.Code, rec.Body)
	}

	pictureTools(t, s, `{}`, errors.New("Invalid data found when processing input"))
	rec := putPicture(t, s, "p", "bad.mp4", []byte("V"))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Invalid data") {
		t.Errorf("undecodable: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(projects, "p", "sources", "bad.png")); err == nil {
		t.Error("nothing is stored when ffmpeg fails")
	}

	s.look = func(string) (string, error) { return "", errors.New("not found") }
	if rec := putPicture(t, s, "p", "x.gif", []byte("GIF89a")); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no ffmpeg: %d %s", rec.Code, rec.Body)
	}
	// plain pictures keep working without ffmpeg
	if rec := putPicture(t, s, "p", "ok.png", pngWithText(pictureText)); rec.Code != http.StatusCreated {
		t.Errorf("a picture needs no ffmpeg: %d %s", rec.Code, rec.Body)
	}
}

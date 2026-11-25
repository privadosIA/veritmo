package workspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/platform/runner"
)

func putSource(t *testing.T, s *testServer, project, name string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/projects/"+project+"/sources/"+name, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func putSourceAs(t *testing.T, s *testServer, project, name, as string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/projects/"+project+"/sources/"+name+"?as="+as, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func listSourceNames(t *testing.T, s *testServer, project string) []string {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/projects/"+project+"/sources", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Sources []sourceInfo `json:"sources"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, x := range out.Sources {
		names = append(names, x.Name)
	}
	return names
}

func TestSources_UploadListAndPlay(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "songs")
	if got := listSourceNames(t, s, "songs"); len(got) != 0 {
		t.Fatalf("a new project has no sources: %v", got)
	}
	if rec := putSource(t, s, "songs", "base.mp3", []byte("audio-bytes")); rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	if got := strings.Join(listSourceNames(t, s, "songs"), ","); got != "base.mp3" {
		t.Fatalf("list = %s", got)
	}
	b, err := os.ReadFile(filepath.Join(projects, "songs", "sources", "base.mp3"))
	if err != nil || string(b) != "audio-bytes" {
		t.Fatalf("%v %q", err, b)
	}
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/projects/songs/artifacts/sources/base.mp3", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "audio-bytes" {
		t.Fatalf("play: %d", rec.Code)
	}
}

func TestSources_NameRules(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "songs")
	for _, name := range []string{"base.ogg", "base", ".hidden.mp3", "a%20b.mp3", "..%2Fx.mp3"} {
		if rec := putSource(t, s, "songs", name, []byte("x")); rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 400", name, rec.Code)
		}
	}
}

func TestSources_AnExistingNameIsNeverReplaced(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "songs")
	putSource(t, s, "songs", "base.mp3", []byte("first"))
	if rec := putSource(t, s, "songs", "base.mp3", []byte("second")); rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409", rec.Code)
	}
	b, _ := os.ReadFile(filepath.Join(projects, "songs", "sources", "base.mp3"))
	if string(b) != "first" {
		t.Fatalf("overwritten: %q", b)
	}
	entries, _ := os.ReadDir(filepath.Join(projects, "songs", "sources"))
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
}

func TestSources_EmptyUploadIsRefused(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "songs")
	if rec := putSource(t, s, "songs", "base.mp3", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
	if got := listSourceNames(t, s, "songs"); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}

func TestSources_CopyAnExportedSong(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "songs")
	out := filepath.Join(projects, "songs", "out", "songs")
	os.MkdirAll(out, 0o755)
	os.WriteFile(filepath.Join(out, "ballad.mp3"), []byte("exported"), 0o644)
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/songs/sources", map[string]string{"from": "out/songs/ballad.mp3"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("copy: %d %s", rec.Code, rec.Body)
	}
	b, _ := os.ReadFile(filepath.Join(projects, "songs", "sources", "ballad.mp3"))
	if string(b) != "exported" {
		t.Fatalf("%q", b)
	}
	if _, err := os.Stat(filepath.Join(out, "ballad.mp3")); err != nil {
		t.Fatal("the original must stay")
	}
	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/projects/songs/sources", map[string]string{"from": "out/songs/ballad.mp3", "name": "version2.mp3"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("named copy: %d", rec.Code)
	}
}

func TestSources_CopyOnlyFromOut(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "songs")
	os.WriteFile(filepath.Join(projects, "songs", "scenes.yaml.mp3"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(projects, "songs", "tmp"), 0o755)
	os.WriteFile(filepath.Join(projects, "songs", "tmp", "t.mp3"), []byte("x"), 0o644)
	for _, from := range []string{"tmp/t.mp3", "scenes.yaml.mp3", "out/../tmp/t.mp3", "/etc/passwd", "out/x.txt", ""} {
		rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/songs/sources", map[string]string{"from": from})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%q: %d, want 400", from, rec.Code)
		}
	}
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/songs/sources", map[string]string{"from": "out/songs/nothing.mp3"})
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing file: %d, want 404", rec.Code)
	}
}

func TestSources_DeleteRefusedWhileATrackUsesIt(t *testing.T) {
	s, projects := newProjectsServer(t)
	installMusicModels(t)
	createProject(t, s, "songs")
	putSource(t, s, "songs", "base.mp3", []byte("x"))
	putSource(t, s, "songs", "spare.mp3", []byte("x"))
	yaml := "project: songs\noutput: {mode: music}\ntracks:\n  - {id: c, prompt: jazz, task: cover, source: base.mp3}\n"
	if rec := putConfig(t, s, "songs", yaml); rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/projects/songs/sources/base.mp3", nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `track \"c\"`) {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/projects/songs/sources/spare.mp3", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("unused source: %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(projects, "songs", "sources", "spare.mp3")); !os.IsNotExist(err) {
		t.Fatal("not deleted")
	}
	if rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/projects/songs/sources/spare.mp3", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("twice: %d", rec.Code)
	}
}

func TestSources_ASavedProjectNeedsItsSourceToExist(t *testing.T) {
	s, _ := newProjectsServer(t)
	installMusicModels(t)
	createProject(t, s, "songs")
	yaml := "project: songs\noutput: {mode: music}\ntracks:\n  - {id: c, prompt: jazz, task: cover, source: base.mp3}\n"
	if rec := putConfig(t, s, "songs", yaml); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d: a track whose source is missing must not save", rec.Code)
	}
	putSource(t, s, "songs", "base.mp3", []byte("x"))
	if rec := putConfig(t, s, "songs", yaml); rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
}

func TestSources_DuplicateCopiesThem(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "songs")
	putSource(t, s, "songs", "base.mp3", []byte("kept"))
	rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/songs/duplicate", map[string]string{"new_id": "copy"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("duplicate: %d %s", rec.Code, rec.Body)
	}
	b, err := os.ReadFile(filepath.Join(projects, "copy", "sources", "base.mp3"))
	if err != nil || string(b) != "kept" {
		t.Fatalf("%v %q", err, b)
	}
}

func TestSources_UnknownProject(t *testing.T) {
	s, _ := newProjectsServer(t)
	if rec := doJSON(t, s.Handler(), http.MethodGet, "/api/projects/nope/sources", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("%d", rec.Code)
	}
}

func TestSources_DeleteRefusedWhileTheBackgroundMusicUsesIt(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "clip")
	putSource(t, s, "clip", "theme.mp3", []byte("x"))
	cfg := doJSON(t, s.Handler(), http.MethodGet, "/api/projects/clip/config", nil)
	var got struct{ YAML string }
	json.Unmarshal(cfg.Body.Bytes(), &got)
	yaml := got.YAML + "\nbgm: {file: theme.mp3}\n"
	if rec := putConfig(t, s, "clip", yaml); rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/projects/clip/sources/theme.mp3", nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "background music") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// --- video uploads -----------------------------------------------------------

func fakeFFmpeg(t *testing.T, s *testServer, write bool, failWith error) *runner.FakeRunner {
	t.Helper()
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if failWith != nil {
			return runner.ScriptedResult{Err: failWith}
		}
		if write {
			os.WriteFile(inv.Args[len(inv.Args)-1], []byte("EXTRACTED"), 0o644)
		} else {
			os.WriteFile(inv.Args[len(inv.Args)-1], nil, 0o644)
		}
		return runner.ScriptedResult{}
	})
	s.mediaRunner = fr
	s.look = func(string) (string, error) { return "/usr/bin/ffmpeg", nil }
	return fr
}

func TestSources_AVideoBecomesItsAudio(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "songs")
	fr := fakeFFmpeg(t, s, true, nil)
	rec := putSource(t, s, "songs", "clip.MP4", []byte("VIDEO"))
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"clip.mp3"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	b, err := os.ReadFile(filepath.Join(projects, "songs", "sources", "clip.mp3"))
	if err != nil || string(b) != "EXTRACTED" {
		t.Fatalf("%v %q", err, b)
	}
	entries, _ := os.ReadDir(filepath.Join(projects, "songs", "sources"))
	if len(entries) != 1 {
		t.Fatalf("the video and the temporary files must not stay: %v", entries)
	}
	args := strings.Join(fr.Calls()[0].Args, " ")
	for _, want := range []string{"-vn", "-ac 2", "-ar 48000", "libmp3lame"} {
		if !strings.Contains(args, want) {
			t.Errorf("missing %q in %s", want, args)
		}
	}
	if got := strings.Join(listSourceNames(t, s, "songs"), ","); got != "clip.mp3" {
		t.Fatalf("list = %s", got)
	}
}

func TestSources_AVideoWithoutAudioOrABrokenOneIsRefused(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "songs")
	fakeFFmpeg(t, s, false, nil)
	if rec := putSource(t, s, "songs", "mute.webm", []byte("V")); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "no audio") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	fakeFFmpeg(t, s, false, fmt.Errorf("Invalid data found when processing input\nStream map matches no streams"))
	rec := putSource(t, s, "songs", "bad.mkv", []byte("V"))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "matches no streams") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	entries, _ := os.ReadDir(filepath.Join(projects, "songs", "sources"))
	if len(entries) != 0 {
		t.Fatalf("nothing may stay: %v", entries)
	}
}

func TestSources_AVideoNeedsFFmpegAndNeverReplaces(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "songs")
	fakeFFmpeg(t, s, true, nil)
	s.look = func(string) (string, error) { return "", fmt.Errorf("not found") }
	if rec := putSource(t, s, "songs", "clip.mp4", []byte("V")); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "ffmpeg") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	fakeFFmpeg(t, s, true, nil)
	putSource(t, s, "songs", "clip.mp3", []byte("already"))
	if rec := putSource(t, s, "songs", "clip.mp4", []byte("V")); rec.Code != http.StatusConflict {
		t.Fatalf("%d", rec.Code)
	}
}

func TestSources_ARealVideoIsConverted(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	video := filepath.Join(dir, "v.mp4")
	cmd := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "color=c=black:s=64x64:d=2:r=10", "-f", "lavfi", "-i", "sine=f=440:d=2", "-shortest", "-c:v", "libx264", "-c:a", "aac", video)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	data, _ := os.ReadFile(video)
	s, projects := newProjectsServer(t)
	createProject(t, s, "songs")
	if rec := putSource(t, s, "songs", "v.mp4", data); rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	fi, err := os.Stat(filepath.Join(projects, "songs", "sources", "v.mp3"))
	if err != nil || fi.Size() < 1000 {
		t.Fatalf("%v %v", err, fi)
	}
	// a video with no sound
	mute := filepath.Join(dir, "m.mp4")
	if out, err := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "color=c=black:s=64x64:d=1:r=10", "-c:v", "libx264", mute).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	mdata, _ := os.ReadFile(mute)
	if rec := putSource(t, s, "songs", "m.mp4", mdata); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a silent video: %d %s", rec.Code, rec.Body)
	}
}

func TestSources_DeleteRefusedWhileATrackUsesItAsReference(t *testing.T) {
	s, _ := newProjectsServer(t)
	installMusicModels(t)
	createProject(t, s, "songs")
	putSource(t, s, "songs", "sound.mp3", []byte("x"))
	yaml := "project: songs\noutput: {mode: music}\ntracks:\n  - {id: a, prompt: jazz, reference: sound.mp3}\n"
	if rec := putConfig(t, s, "songs", yaml); rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/projects/songs/sources/sound.mp3", nil); rec.Code != http.StatusConflict {
		t.Fatalf("%d", rec.Code)
	}
}

func TestSources_PicturesAreSourcesToo(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "clip")
	for _, name := range []string{"photo.jpg", "art.PNG", "shot.webp"} {
		if rec := putSource(t, s, "clip", name, []byte("img")); rec.Code != http.StatusCreated {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	putSource(t, s, "clip", "song.mp3", []byte("a"))
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/projects/clip/sources", nil)
	var out struct{ Sources []sourceInfo }
	json.Unmarshal(rec.Body.Bytes(), &out)
	kinds := map[string]string{}
	for _, x := range out.Sources {
		kinds[x.Name] = x.Kind
	}
	if kinds["photo.jpg"] != "image" || kinds["art.PNG"] != "image" || kinds["shot.webp"] != "image" || kinds["song.mp3"] != "audio" {
		t.Fatalf("%v", kinds)
	}
	if rec := putSource(t, s, "clip", "anim.gif", []byte("x")); rec.Code != http.StatusBadRequest {
		t.Fatalf("gif: %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(projects, "clip", "sources", "photo.jpg")); err != nil {
		t.Fatal(err)
	}
}

func TestSources_DeleteRefusedWhileASceneUsesThePicture(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "clip")
	putSource(t, s, "clip", "photo.jpg", []byte("img"))
	cfg := doJSON(t, s.Handler(), http.MethodGet, "/api/projects/clip/config", nil)
	var got struct{ YAML string }
	json.Unmarshal(cfg.Body.Bytes(), &got)
	yaml := "project: clip\noutput: {file: out/clip.mp4}\nscenes:\n  - {id: a, image: photo.jpg, tts_text: hola, speed: 1.0}\n"
	if rec := putConfig(t, s, "clip", yaml); rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/projects/clip/sources/photo.jpg", nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `scene \"a\"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	_ = got
}

func TestSources_DeleteRefusedWhileASceneStartsFromThePicture(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "pics")
	putSource(t, s, "pics", "photo.jpg", []byte("img"))
	yaml := "project: pics\noutput: {mode: images}\nscenes:\n  - {id: a, prompt: watercolor, init_image: photo.jpg, strength: 0.4}\n"
	if rec := putConfig(t, s, "pics", yaml); rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/projects/pics/sources/photo.jpg", nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `scene \"a\"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	// a project whose picture is gone cannot be saved
	os.Remove(filepath.Join("projects", "pics", "sources", "photo.jpg"))
}

// The default body limit of the API must not stop an upload: a source of a few MB goes through the whole server,
// while a plain JSON message of the same size is refused.
func TestBodyLimit_AnUploadOverTheDefaultStillWorks_AJSONBodyDoesNot(t *testing.T) {
	s, _ := newProjectsServer(t)
	createProject(t, s, "demo")

	if rec := putSource(t, s, "demo", "song.mp3", bytes.Repeat([]byte{7}, 3<<20)); rec.Code != http.StatusCreated {
		t.Fatalf("a 3 MB source: status %d (%s), want 201", rec.Code, rec.Body)
	}

	rec := httptest.NewRecorder()
	body := `{"id":"big","padding":"` + strings.Repeat("a", 2<<20) + `"}`
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(body)))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a 2 MB JSON body: status %d (%s), want 413", rec.Code, rec.Body)
	}
}

package workspace

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/video/motion"
)

// clipYAML is a video project whose scenes use the given clips (scene id -> file in sources/).
func clipYAML(project string, clips map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "project: %s\noutput:\n  mode: video\n  file: out/%s.mp4\ndefaults:\n  voice: v.onnx\n  resolution: 1280x720\n  sampler: euler\n  steps: 20\n  fps: 24\nscenes:\n", project, project)
	for _, id := range []string{"a", "b"} {
		if file, ok := clips[id]; ok {
			fmt.Fprintf(&b, "  - id: %s\n    silence_seconds: 1\n    clip: %s\n", id, file)
		}
	}
	return b.String()
}

type sourceList struct {
	Sources []sourceInfo `json:"sources"`
}

func listSources(t *testing.T, s *testServer, project string) []sourceInfo {
	t.Helper()
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/projects/"+project+"/sources", nil)
	var out sourceList
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Sources
}

func TestClips_UploadListAndUsedBy(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "p")
	os.WriteFile(filepath.Join(projects, "p", "v.onnx"), []byte("v"), 0o644)
	os.WriteFile(filepath.Join(projects, "p", "v.onnx.json"), []byte("{}"), 0o644)
	req := func(name string) int {
		return putSourceAs(t, s, "p", name, "clip", []byte("video")).Code
	}
	if code := req("stand-up.mp4"); code != http.StatusCreated {
		t.Fatalf("upload: %d", code)
	}
	if code := req("notes.txt"); code != http.StatusBadRequest {
		t.Fatalf("a non-video is refused: %d", code)
	}
	if rec := putConfig(t, s, "p", clipYAML("p", map[string]string{"a": "stand-up.mp4"})); rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	got := listSources(t, s, "p")
	if len(got) != 1 || got[0].Kind != "video" || got[0].UsedBy == nil || strings.Join(*got[0].UsedBy, ",") != "a" {
		t.Fatalf("sources = %+v", got)
	}
	// a video nobody uses says so
	putSourceAs(t, s, "p", "spare.mp4", "clip", []byte("video"))
	for _, x := range listSources(t, s, "p") {
		if x.Name == "spare.mp4" && (x.UsedBy == nil || len(*x.UsedBy) != 0) {
			t.Fatalf("an unused video lists no scenes: %+v", x)
		}
	}
	// deleting a clip a scene uses is refused
	rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/projects/p/sources/stand-up.mp4", nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `scene \"a\"`) {
		t.Fatalf("delete in use: %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/projects/p/sources/spare.mp4", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete unused: %d", rec.Code)
	}
}

func TestClips_SavingRemovesTheVideosNoSceneUsesAnyMore(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "p")
	os.WriteFile(filepath.Join(projects, "p", "v.onnx"), []byte("v"), 0o644)
	os.WriteFile(filepath.Join(projects, "p", "v.onnx.json"), []byte("{}"), 0o644)
	for _, n := range []string{"one.mp4", "two.mp4", "shared.mp4", "never.mp4"} {
		putSourceAs(t, s, "p", n, "clip", []byte("video"))
	}
	putConfig(t, s, "p", clipYAML("p", map[string]string{"a": "one.mp4", "b": "shared.mp4"}))
	// scene a gets another clip; scene b keeps shared
	rec := putConfig(t, s, "p", clipYAML("p", map[string]string{"a": "two.mp4", "b": "shared.mp4"}))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "one.mp4") {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	left := func() string {
		var n []string
		for _, x := range listSources(t, s, "p") {
			n = append(n, x.Name)
		}
		return strings.Join(n, ",")
	}
	if got := left(); got != "never.mp4,shared.mp4,two.mp4" {
		t.Fatalf("one.mp4 was replaced and goes; never.mp4 was never a clip of a scene and stays: %s", got)
	}
	// scene b is removed: shared.mp4 goes with it, two.mp4 stays
	putConfig(t, s, "p", clipYAML("p", map[string]string{"a": "two.mp4"}))
	if got := left(); got != "never.mp4,two.mp4" {
		t.Fatalf("after removing scene b: %s", got)
	}
	_ = projects
}

func TestClips_AClipOfAnotherProjectIsCopied(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "other")
	createProject(t, s, "p")
	writeArtifact(t, projects, "other", "out/clips/s06.mp4", []byte("the clip"))
	writeArtifact(t, projects, "p", "out/clips/mine.mp4", []byte("mine"))

	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/clips?exclude=p", nil)
	var list struct {
		Clips []keptClip `json:"clips"`
	}
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Clips) != 1 || list.Clips[0].Project != "other" || list.Clips[0].Scene != "s06" {
		t.Fatalf("the list leaves out the project itself: %+v", list.Clips)
	}

	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/projects/p/sources/clip", map[string]string{"from_project": "other", "scene": "s06"})
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), "other-s06.mp4") {
		t.Fatalf("copy: %d %s", rec.Code, rec.Body)
	}
	// a second copy takes a free name
	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/projects/p/sources/clip", map[string]string{"from_project": "other", "scene": "s06"})
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), "other-s06-2.mp4") {
		t.Fatalf("second copy: %d %s", rec.Code, rec.Body)
	}
	// deleting the other project does not touch the copy
	if rec := doJSON(t, s.Handler(), http.MethodDelete, "/api/projects/other", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete project: %d %s", rec.Code, rec.Body)
	}
	b, err := os.ReadFile(filepath.Join(projects, "p", "sources", "other-s06.mp4"))
	if err != nil || string(b) != "the clip" {
		t.Fatalf("the copy stays: %v %q", err, b)
	}
	for _, bad := range []map[string]string{
		{"from_project": "other", "scene": "s06"}, // gone now
		{"from_project": "../p", "scene": "mine"},
		{"from_project": "p", "scene": "../mine"},
	} {
		if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/p/sources/clip", bad); rec.Code < 400 {
			t.Fatalf("%v accepted: %d", bad, rec.Code)
		}
	}
}

func TestClips_TheCompleteAnimationIsListedAndCopiedWithAnotherName(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "other")
	createProject(t, s, "p")
	writeArtifact(t, projects, "other", "out/clips/s06.mp4", []byte("clip"))
	writeArtifact(t, projects, "other", "out/clips/s06.anim.mp4", []byte("the whole animation"))

	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/clips?exclude=p", nil)
	var list struct {
		Clips []keptClip `json:"clips"`
	}
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Clips) != 2 || list.Clips[0].Kind != "animation" || list.Clips[1].Kind != "clip" || list.Clips[0].Scene != "s06" || list.Clips[1].Scene != "s06" {
		t.Fatalf("both the finished clip and the animation are listed under the scene: %+v", list.Clips)
	}

	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/projects/p/sources/clip", map[string]string{"from_project": "other", "scene": "s06", "kind": "animation"})
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), "other-s06-anim.mp4") {
		t.Fatalf("copy the animation: %d %s", rec.Code, rec.Body)
	}
	b, err := os.ReadFile(filepath.Join(projects, "p", "sources", "other-s06-anim.mp4"))
	if err != nil || string(b) != "the whole animation" {
		t.Fatalf("%v %q", err, b)
	}
	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/projects/p/sources/clip", map[string]string{"from_project": "other", "scene": "s06"})
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), "other-s06.mp4") {
		t.Fatalf("the finished clip keeps its name: %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.Handler(), http.MethodPost, "/api/projects/p/sources/clip", map[string]string{"from_project": "other", "scene": "s06", "kind": "other"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("an unknown kind is refused: %d", rec.Code)
	}
	// a scene with no animation kept
	rec = doJSON(t, s.Handler(), http.MethodPost, "/api/projects/p/sources/clip", map[string]string{"from_project": "other", "scene": "nope", "kind": "animation"})
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "no kept animation") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestClips_ListCanBeLimitedToOneProject(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "a")
	createProject(t, s, "b")
	writeArtifact(t, projects, "a", "out/clips/s1.mp4", []byte("x"))
	writeArtifact(t, projects, "b", "out/clips/s2.mp4", []byte("y"))
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/clips?project=b", nil)
	var list struct {
		Clips []keptClip `json:"clips"`
	}
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Clips) != 1 || list.Clips[0].Project != "b" || list.Clips[0].Scene != "s2" {
		t.Fatalf("only project b: %+v", list.Clips)
	}
}

func TestClips_AMotionGraphicClipSaysHowItsPageWasMade(t *testing.T) {
	s, projects := newProjectsServer(t)
	createProject(t, s, "other")
	writeArtifact(t, projects, "other", "out/clips/m.mp4", []byte("clip"))
	writeArtifact(t, projects, "other", "out/clips/plain.mp4", []byte("clip"))
	page := "<html>x</html>"
	writeArtifact(t, projects, "other", "motion/m.html", []byte(page))
	if err := motion.StoreMeta(filepath.Join(projects, "other"), "m", false, motion.Meta{
		Kind: "motion_graphic", Provider: "Ollama", Model: "writer:7b", Browser: "126.0.1", GeneratedSHA256: motion.SHA256(page),
	}); err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, s.Handler(), http.MethodGet, "/api/clips?exclude=p", nil)
	var list struct {
		Clips []keptClip `json:"clips"`
	}
	json.Unmarshal(rec.Body.Bytes(), &list)
	got := map[string]string{}
	for _, c := range list.Clips {
		got[c.Scene] = c.Motion
	}
	for _, want := range []string{"motion graphic", "writer:7b", "Chrome 126.0.1"} {
		if !strings.Contains(got["m"], want) {
			t.Errorf("the motion clip's details must say %q: %q", want, got["m"])
		}
	}
	if got["plain"] != "" {
		t.Errorf("a normal clip has no motion details: %q", got["plain"])
	}
}

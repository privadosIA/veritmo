package workspace

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/assistant/assist"
	"veritmo/internal/project/export"
)

const songsYAML = `project: songs
output: {mode: music}
tracks:
  - id: night_drive
    title: Night Drive
    prompt: warm acoustic pop
    lyrics: |-
      [Verse]
      one
      two
      [Chorus]
      three
      four
`

// songsProject writes a music project with one exported song (and its manifest) and returns the project's folder.
func songsProject(t *testing.T, s *testServer, projects string) string {
	t.Helper()
	createProject(t, s, "songs")
	dir := filepath.Join(projects, "songs")
	if err := s.SaveProjectDefinition("songs", []byte(songsYAML), "save"); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out", "songs")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "night_drive.mp3"), []byte("MP3DATA"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := export.MusicManifest{Project: "songs", Mode: "music", Tracks: []export.MusicEntry{{
		TrackID: "night_drive", Caption: "warm acoustic pop", LyricsSung: "[Verse]\none\ntwo\n[Chorus]\nthree\nfour", AudioSeconds: 60,
		SynthModel: "x", File: filepath.Join("out", "songs", "night_drive.mp3"), Title: "Night Drive",
	}}}
	b, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(out, "manifest.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestVideoclipPlan_ShotsFromTheLyricsWithFallbackPrompts(t *testing.T) {
	s, projects := newProjectsServer(t)
	songsProject(t, s, projects)
	rec := call(s.Handler(), http.MethodPost, "/api/projects/songs/videoclip/plan", `{"track_id":"night_drive","shot_seconds":6}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var plan videoclipPlan
	if err := json.Unmarshal(rec.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Title != "Night Drive" || plan.AudioSeconds != 60 || plan.UsedAI || len(plan.Shots) < 8 {
		t.Fatalf("plan = %+v", plan)
	}
	var sum float64
	for _, sh := range plan.Shots {
		sum += sh.Seconds
		if !strings.Contains(sh.Prompt, "warm acoustic pop") {
			t.Errorf("the fallback prompt should carry the style: %q", sh.Prompt)
		}
	}
	if sum < 59.999 || sum > 60.001 {
		t.Errorf("the shots add up to %v s", sum)
	}
	if !strings.Contains(plan.SubjectPrompt, "warm acoustic pop") {
		t.Errorf("subject = %q", plan.SubjectPrompt)
	}
}

func TestVideoclipPlan_TheAssistantWritesThePicturesAndWhenItFailsTheFallbackStays(t *testing.T) {
	s, projects := newProjectsServer(t)
	songsProject(t, s, projects)
	s.Overrides.StoryboardRun = func(_ context.Context, style, title string, shots []assist.ShotBrief, _ string) (assist.Storyboard, error) {
		sb := assist.Storyboard{Subject: "a woman with a red scarf", Prompts: make([]string, len(shots))}
		for i := range shots {
			sb.Prompts[i] = fmt.Sprintf("picture %d of the video", i+1)
		}
		sb.Prompts[2] = "" // one the model did not write
		return sb, nil
	}
	rec := call(s.Handler(), http.MethodPost, "/api/projects/songs/videoclip/plan", `{"track_id":"night_drive","ai":true}`)
	var plan videoclipPlan
	json.Unmarshal(rec.Body.Bytes(), &plan)
	if !plan.UsedAI || plan.SubjectPrompt != "a woman with a red scarf" || plan.Shots[0].Prompt != "picture 1 of the video" || !strings.Contains(plan.Shots[2].Prompt, "warm acoustic pop") {
		t.Fatalf("plan = %+v", plan.Shots[:3])
	}
	s.Overrides.StoryboardRun = func(context.Context, string, string, []assist.ShotBrief, string) (assist.Storyboard, error) {
		return assist.Storyboard{}, assist.ErrUnreachable
	}
	rec = call(s.Handler(), http.MethodPost, "/api/projects/songs/videoclip/plan", `{"track_id":"night_drive","ai":true}`)
	plan = videoclipPlan{}
	json.Unmarshal(rec.Body.Bytes(), &plan)
	if rec.Code != http.StatusOK || plan.UsedAI || len(plan.Warnings) != 1 || !strings.Contains(plan.Shots[0].Prompt, "warm acoustic pop") {
		t.Fatalf("a failing assistant must fall back with a warning: %d %+v", rec.Code, plan.Warnings)
	}
}

func TestVideoclipPlan_RefusesWhatIsNotASong(t *testing.T) {
	s, projects := newProjectsServer(t)
	songsProject(t, s, projects)
	for _, body := range []string{`{"track_id":"nope"}`, `{"track_id":"night_drive","variant":3}`} {
		if code := call(s.Handler(), http.MethodPost, "/api/projects/songs/videoclip/plan", body).Code; code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d", body, code)
		}
	}
	createProject(t, s, "plain")
	if code := call(s.Handler(), http.MethodPost, "/api/projects/plain/videoclip/plan", `{"track_id":"x"}`).Code; code != http.StatusUnprocessableEntity {
		t.Errorf("a project that is not music: %d", code)
	}
}

const videoclipYAML = `project: clip
output: {mode: video, file: out/clip.mp4}
defaults:
  voice: voices/v.onnx
  model: models/m.safetensors
  resolution: 1280x704
  sampler: euler
  steps: 8
  fps: 24
bgm: {file: song.mp3, volume: 1, ducking: false}
scenes:
  - id: a
    prompt: a road at dawn
    silence_seconds: 6
`

func TestVideoclipCreate_BuildsTheProjectWithTheSongAsItsSoundtrack(t *testing.T) {
	s, projects := newProjectsServer(t)
	songsProject(t, s, projects)
	body, _ := json.Marshal(map[string]any{"track_id": "night_drive", "id": "night-clip", "yaml": videoclipYAML})
	rec := call(s.Handler(), http.MethodPost, "/api/projects/songs/videoclip", string(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if b, err := os.ReadFile(filepath.Join(projects, "night-clip", "sources", "song.mp3")); err != nil || string(b) != "MP3DATA" {
		t.Errorf("the song was not copied into the sources: %v %q", err, b)
	}
	if _, err := os.Stat(filepath.Join(projects, "night-clip", "scenes.yaml")); err != nil {
		t.Error("scenes.yaml missing")
	}
	// the original is untouched, and a second creation never overwrites
	if _, err := os.Stat(filepath.Join(projects, "songs", "out", "songs", "night_drive.mp3")); err != nil {
		t.Error("the song was moved instead of copied")
	}
	if code := call(s.Handler(), http.MethodPost, "/api/projects/songs/videoclip", string(body)).Code; code != http.StatusConflict {
		t.Errorf("second creation: %d", code)
	}
}

func TestVideoclipCreate_RefusesWhatIsNotAVideoWithTheSongAndLeavesNothingBehind(t *testing.T) {
	s, projects := newProjectsServer(t)
	songsProject(t, s, projects)
	cases := map[string]string{
		"no soundtrack":       strings.Replace(videoclipYAML, "bgm: {file: song.mp3, volume: 1, ducking: false}\n", "", 1),
		"wrong extension":     strings.Replace(videoclipYAML, "song.mp3", "song.wav", 1),
		"not a valid project": "project: x\noutput: {mode: video}\n",
	}
	for name, y := range cases {
		body, _ := json.Marshal(map[string]any{"track_id": "night_drive", "id": "bad", "yaml": y})
		if code := call(s.Handler(), http.MethodPost, "/api/projects/songs/videoclip", string(body)).Code; code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d", name, code)
		}
		if _, err := os.Stat(filepath.Join(projects, "bad")); err == nil {
			t.Errorf("%s: a half-made project stayed", name)
		}
	}
}

// pcm builds mono f32 samples at the rate the server decodes to: quiet until `loudAt` seconds, loud after.
func pcm(total, loudAt float64) []byte {
	n := int(total * PeaksRate)
	out := make([]byte, n*4)
	for i := 0; i < n; i++ {
		v := float32(0.1)
		if float64(i)/PeaksRate >= loudAt {
			v = 0.5
		}
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(v))
	}
	return out
}

func TestVideoclipPlan_TheCutsFollowTheChangeOfTheSong(t *testing.T) {
	s, projects := newProjectsServer(t)
	songsProject(t, s, projects)
	// the text puts the chorus at 30 s; the song gets louder at 34 s
	fakePeaks(t, s, pcm(60, 34), nil)
	rec := call(s.Handler(), http.MethodPost, "/api/projects/songs/videoclip/plan", `{"track_id":"night_drive","shot_seconds":6}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var plan videoclipPlan
	if err := json.Unmarshal(rec.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Sections != 2 || plan.AlignedCuts != 1 {
		t.Fatalf("sections %d, aligned cuts %d", plan.Sections, plan.AlignedCuts)
	}
	for _, sh := range plan.Shots {
		if sh.Section == "Chorus" {
			if sh.Start < 33.5 || sh.Start > 34.5 {
				t.Fatalf("the chorus starts at %.2f s, want about 34", sh.Start)
			}
			return
		}
	}
	t.Fatal("no chorus shot")
}

func TestVideoclipPlan_WithoutFFmpegTheCutsFollowTheTextAlone(t *testing.T) {
	s, projects := newProjectsServer(t)
	songsProject(t, s, projects)
	s.look = func(string) (string, error) { return "", fmt.Errorf("not found") }
	rec := call(s.Handler(), http.MethodPost, "/api/projects/songs/videoclip/plan", `{"track_id":"night_drive","shot_seconds":6}`)
	var plan videoclipPlan
	if err := json.Unmarshal(rec.Body.Bytes(), &plan); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("%d %s %v", rec.Code, rec.Body, err)
	}
	if plan.AlignedCuts != 0 || plan.Sections != 2 {
		t.Fatalf("aligned %d sections %d", plan.AlignedCuts, plan.Sections)
	}
}

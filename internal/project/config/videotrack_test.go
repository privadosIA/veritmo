package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// trackProject writes a project with the clips in sources/ and loads it, with the browser check left out.
func trackProject(t *testing.T, yaml string, clips ...string) (*Config, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, SourcesSubdir), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range clips {
		if err := os.WriteFile(filepath.Join(dir, SourcesSubdir, c), []byte("video"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	cfg.SkipBrowserCheck = true
	return cfg, dir
}

const trackHead = `project: p
output: {mode: video, file: out/p.mp4}
defaults: {resolution: 640x360, fps: 30}
`

const goodTrack = trackHead + `video_track:
  - {id: v1, clip: a.mp4, start: 0, end: 6, scrim: 0.4}
  - {id: v2, clip: b.mp4, start: 6, end: 12, clip_start: 1, clip_end: 5, clip_speed: 0.5, transition_seconds: 0.5}
scenes:
  - {id: s1, motion_graphic: "music piece: a", silence_seconds: 6, layer: true}
  - {id: s2, motion_part: blank, silence_seconds: 6, layer: true}
`

func TestAProjectWithAVideoTrackAndLayersLoadsAndValidates(t *testing.T) {
	cfg, dir := trackProject(t, goodTrack, "a.mp4", "b.mp4")
	if err := cfg.ValidateInDir(dir); err != nil {
		t.Fatal(err)
	}
	if !cfg.HasVideoTrack() || len(cfg.VideoTrack) != 2 || cfg.VideoTrack[1].ID != "v2" || *cfg.VideoTrack[1].ClipSpeed != 0.5 || cfg.VideoTrack[0].ScrimValue() != 0.4 || cfg.VideoTrack[1].ScrimValue() != DefaultTrackScrim {
		t.Fatalf("%+v", cfg.VideoTrack)
	}
	if !cfg.Scenes[0].IsLayer() || !cfg.Scenes[1].IsLayer() {
		t.Fatal("the scenes are layers")
	}
}

func TestEachBadVideoTrackIsRefusedAndNamed(t *testing.T) {
	cases := []struct {
		name, edit, want string
	}{
		{"an unknown clip", "clip: a.mp4, start: 0, end: 6", "clip: nope.mp4, start: 0, end: 6"},
		{"a path", "clip: a.mp4, start: 0, end: 6", "clip: ../a.mp4, start: 0, end: 6"},
		{"outside the project", "start: 0, end: 6, scrim: 0.4", "start: 0, end: 20"},
		{"ends before it starts", "start: 0, end: 6, scrim: 0.4", "start: 6, end: 6"},
		{"negative start", "start: 0, end: 6, scrim: 0.4", "start: -1, end: 6"},
		{"a bad scrim", "scrim: 0.4", "scrim: 0.9"},
		{"a bad speed", "clip_speed: 0.5", "clip_speed: 9"},
		{"an end before the start", "clip_start: 1, clip_end: 5", "clip_start: 5, clip_end: 2"},
		{"a transition longer than the shorter item", "transition_seconds: 0.5", "transition_seconds: 9"},
		{"an overlap", "{id: v2, clip: b.mp4, start: 6,", "{id: v2, clip: b.mp4, start: 5,"},
		{"a repeated id", "{id: v2,", "{id: v1,"},
		{"a bad id", "{id: v2,", "{id: V 2,"},
	}
	for _, c := range cases {
		y := strings.Replace(goodTrack, c.edit, strings.NewReplacer().Replace(c.want), 1)
		if y == goodTrack {
			t.Fatalf("%s: the edit did nothing", c.name)
		}
		cfg, dir := trackProject(t, y, "a.mp4", "b.mp4")
		err := cfg.ValidateInDir(dir)
		if err == nil || !strings.Contains(err.Error(), "video_track") {
			t.Errorf("%s: got %v", c.name, err)
		}
	}
}

func TestALayerSceneIsAPageNotAPictureOrAClip(t *testing.T) {
	for name, scene := range map[string]string{
		"an image":   `{id: s1, motion_graphic: x, silence_seconds: 6, layer: true, image: i.png}`,
		"a clip":     `{id: s1, motion_graphic: x, silence_seconds: 6, layer: true, clip: a.mp4}`,
		"animate":    `{id: s1, motion_graphic: x, silence_seconds: 6, layer: true, animate: true}`,
		"an overlay": `{id: s1, motion_graphic: x, silence_seconds: 6, layer: true, overlay: {text: t}}`,
		"not a page": `{id: s1, prompt: a city, silence_seconds: 6, layer: true}`,
	} {
		cfg, dir := trackProject(t, trackHead+"scenes:\n  - "+scene+"\n", "a.mp4")
		os.WriteFile(filepath.Join(dir, SourcesSubdir, "i.png"), []byte("png"), 0o644)
		err := cfg.ValidateInDir(dir)
		if err == nil || !strings.Contains(err.Error(), `scene "s1"`) || !strings.Contains(err.Error(), "layer") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestATrackNeedsScenesAndAVideoProject(t *testing.T) {
	cfg, dir := trackProject(t, trackHead+"video_track:\n  - {id: v1, clip: a.mp4, start: 0, end: 6}\nscenes: []\n", "a.mp4")
	if err := cfg.ValidateInDir(dir); err == nil {
		t.Fatal("a track with no scenes")
	}
	cfg, dir = trackProject(t, `project: p
output: {mode: images}
video_track:
  - {id: v1, clip: a.mp4, start: 0, end: 6}
scenes:
  - {id: s1, prompt: x}
`, "a.mp4")
	if err := cfg.ValidateInDir(dir); err == nil || !strings.Contains(err.Error(), "video mode") {
		t.Fatalf("%v", err)
	}
}

func TestAProjectWithNoTrackAndNoLayerIsAsItWas(t *testing.T) {
	cfg, dir := trackProject(t, trackHead+"scenes:\n  - {id: s1, motion_graphic: x, silence_seconds: 6}\n")
	if err := cfg.ValidateInDir(dir); err != nil {
		t.Fatal(err)
	}
	if cfg.HasVideoTrack() || cfg.Scenes[0].IsLayer() {
		t.Fatal("no track, no layer")
	}
}

func TestAVideoTrackItemKeepsItsLocalScrimsAndRefusesBadOnes(t *testing.T) {
	with := strings.Replace(goodTrack, "scrim: 0.4}", "scrim: 0.4, local_scrims: [{band: bottom, scrim: 0.5}, {band: top, scrim: 0.8}]}", 1)
	cfg, dir := trackProject(t, with, "a.mp4", "b.mp4")
	if err := cfg.ValidateInDir(dir); err != nil {
		t.Fatal(err)
	}
	got := cfg.VideoTrack[0].LocalScrims
	if len(got) != 2 || got[0].Band != "bottom" || got[0].Scrim != 0.5 || got[1].Band != "top" || got[1].Scrim != 0.8 || len(cfg.VideoTrack[1].LocalScrims) != 0 {
		t.Fatalf("%+v", cfg.VideoTrack)
	}
	for name, bad := range map[string]string{
		"an unknown band":  "{band: left, scrim: 0.5}",
		"a repeated band":  "{band: bottom, scrim: 0.5}, {band: bottom, scrim: 0.2}",
		"a value over 0.8": "{band: bottom, scrim: 0.9}",
		"a negative value": "{band: bottom, scrim: -0.1}",
	} {
		y := strings.Replace(goodTrack, "scrim: 0.4}", "scrim: 0.4, local_scrims: ["+bad+"]}", 1)
		cfg, dir := trackProject(t, y, "a.mp4", "b.mp4")
		err := cfg.ValidateInDir(dir)
		if err == nil || !strings.Contains(err.Error(), "video_track") || !strings.Contains(err.Error(), "local_scrims") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

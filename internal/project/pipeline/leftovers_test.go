package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"veritmo/internal/project/config"
)

func TestIsLeftover_OnlyArtifactsOfScenesThatAreGone(t *testing.T) {
	ids := map[string]bool{"intro": true, "s04b_hands": true}
	for name, want := range map[string]bool{
		"scene_intro.png":              false,
		"scene_intro.anim.avi":         false,
		"scene_intro.anim.mp4":         false,
		"scene_intro.cut.mp4":          false,
		"scene_intro.anim.ambient.wav": false, // the model's audio of a live scene
		"scene_old.anim.ambient.wav":   true,
		"scene_intro.init.png":         false,
		"scene_intro_v2.png":           false, // an image variant of a live scene
		"scene_s04b_hands.wav":         false,
		"scene_old.png":                true,
		"scene_old.anim.avi":           true,
		"scene_old_v1.png":             true,
		"scene_old.mp4":                true,
		"manifest.json":                false,
		"concat_list.txt":              false,
		"video_nobgm.mp4":              false,
		"scene_old.txt":                false, // not a known artifact: never touched
		"scene_.png":                   true,
		"songs_scene_old.mp3":          false,
	} {
		if got := IsLeftover(name, ids); got != want {
			t.Errorf("IsLeftover(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestPruneLeftovers_RemovesOnlyThoseAndCountsTheBytes(t *testing.T) {
	dir := t.TempDir()
	tmp := filepath.Join(dir, "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, n int) {
		if err := os.WriteFile(filepath.Join(tmp, name), make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("scene_a.png", 10)
	write("scene_gone.png", 100)
	write("scene_gone.anim.avi", 1000)
	write("manifest.json", 5)
	freed := PruneLeftovers(dir, []config.Scene{{ID: "a"}})
	if freed != 1100 {
		t.Fatalf("freed = %d", freed)
	}
	for name, want := range map[string]bool{"scene_a.png": true, "manifest.json": true, "scene_gone.png": false, "scene_gone.anim.avi": false} {
		_, err := os.Stat(filepath.Join(tmp, name))
		if (err == nil) != want {
			t.Errorf("%s present = %v, want %v", name, err == nil, want)
		}
	}
}

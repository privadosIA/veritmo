package pipeline

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"veritmo/internal/project/config"
)

// tmpSceneSuffixes are the endings of the per-scene artifacts kept in tmp/, longest first.
var tmpSceneSuffixes = []string{".anim.avi", ".anim.mp4", ".anim.ambient.wav", ".init.png", ".cut.mp4", ".motion.mp4", ".overlay.webm", ".overlay.mov", ".layer.webm", ".layer.mov", ".png", ".wav", ".mp4"}

var imageVariantName = regexp.MustCompile(`^(.*)_v[0-9]+$`)

// IsLeftover reports whether a file of tmp/ is an artifact of a scene that is
// no longer in the project (ids holds the scenes that are). Only files named as
// a scene's artifact (`scene_<id>` plus a known ending, or an image variant of
// it) can be a leftover; every other file of tmp/ (the manifest, the concat
// list, the soundtrack, music files) never is.
func IsLeftover(name string, ids map[string]bool) bool {
	if !strings.HasPrefix(name, "scene_") {
		return false
	}
	rest := strings.TrimPrefix(name, "scene_")
	for _, suf := range tmpSceneSuffixes {
		if !strings.HasSuffix(rest, suf) {
			continue
		}
		id := strings.TrimSuffix(rest, suf)
		if ids[id] {
			return false
		}
		if m := imageVariantName.FindStringSubmatch(id); m != nil && ids[m[1]] {
			return false
		}
		return true
	}
	return false
}

// SceneIDs is the set of scene ids of a configuration.
func SceneIDs(scenes []config.Scene) map[string]bool {
	ids := make(map[string]bool, len(scenes))
	for _, s := range scenes {
		ids[s.ID] = true
	}
	return ids
}

// PruneLeftovers removes from the project's tmp/ the artifacts of scenes that are
// no longer in the project, and returns how many bytes it freed.
func PruneLeftovers(base string, scenes []config.Scene) int64 {
	dir := joinDir(base, "tmp")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	ids := SceneIDs(scenes)
	var freed int64
	for _, e := range entries {
		if e.IsDir() || !IsLeftover(e.Name(), ids) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if fi, err := os.Lstat(p); err == nil && fi.Mode().IsRegular() && os.Remove(p) == nil {
			freed += fi.Size()
		}
	}
	return freed
}

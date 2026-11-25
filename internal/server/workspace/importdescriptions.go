package workspace

// importdescriptions.go: the descriptions of 3D scenes that travel with a dataset import. A dataset has the line `Blender:
// <name>.scene.json` and not the file, so the preview and the confirmation of an import take the files in `descriptions`.
// See specs/project-import.

import (
	"fmt"
	"sort"
	"strings"

	"veritmo/internal/project/config"
	"veritmo/internal/video/blender"
)

// The most descriptions an import carries, and what they add up to.
const (
	maxImportDescriptions     = 20
	maxImportDescriptionBytes = 1 << 20
)

// importDescriptions are the files of 3D scenes of an import, by name (a plain `.scene.json` name).
type importDescriptions map[string][]byte

// checkShape refuses what is not a set of plain names, too many files or too many bytes.
func checkImportShape(d importDescriptions) error {
	if len(d) > maxImportDescriptions {
		return fmt.Errorf("descriptions has %d files, the most is %d", len(d), maxImportDescriptions)
	}
	total := 0
	for name, raw := range d {
		if !config.ValidSceneSpecName(name) {
			return fmt.Errorf("description name %q must be a plain file name ending in %s", name, config.SceneSpecSuffix)
		}
		if len(raw) > blender.MaxSpecBytes {
			return fmt.Errorf("description %q is larger than %d KiB", name, blender.MaxSpecBytes>>10)
		}
		total += len(raw)
	}
	if total > maxImportDescriptionBytes {
		return fmt.Errorf("the descriptions add up to more than %d MB", maxImportDescriptionBytes>>20)
	}
	return nil
}

// importBuilds are the descriptions the scenes of a project name, sorted.
func importBuilds(cfg *config.Config) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range cfg.Scenes {
		if s.Blender != nil && s.Blender.Build != "" && !seen[s.Blender.Build] {
			seen[s.Blender.Build] = true
			out = append(out, s.Blender.Build)
		}
	}
	sort.Strings(out)
	return out
}

// problems says what is wrong with the descriptions of an import against the scenes that name them: a file that does not
// pass the checks of a hand-written description, one no scene names, and a scene whose file is missing. The length of a scene
// is not known before it is narrated, so a key is checked against the longest scene a description can be.
func (d importDescriptions) problems(cfg *config.Config, env blender.Env) (broken, unused, missing []string) {
	named := map[string]bool{}
	for _, b := range importBuilds(cfg) {
		named[b] = true
		if _, ok := d[b]; !ok {
			missing = append(missing, b)
		}
	}
	names := make([]string, 0, len(d))
	for name := range d {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !named[name] {
			unused = append(unused, name)
			continue
		}
		if _, err := blender.Parse(d[name], maxDescriptionSeconds, env); err != nil {
			broken = append(broken, fmt.Sprintf("%s: %s", name, strings.ReplaceAll(err.Error(), "\n", "; ")))
		}
	}
	return broken, unused, missing
}

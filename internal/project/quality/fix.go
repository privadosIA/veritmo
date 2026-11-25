package quality

import (
	"fmt"
	"os"
	"strconv"
	"veritmo/internal/platform/fsutil"
	"veritmo/internal/platform/media"

	"gopkg.in/yaml.v3"
)

// Fix is a change to one scene of scenes.yaml that the check asks for.
type Fix struct {
	ID string
	// SilenceSeconds, when set, becomes the scene's silence_seconds: a silent
	// scene cut to the clean part of its clip.
	SilenceSeconds *float64
	// Seed, when set, becomes the scene's seed: the scene is made again.
	Seed *int
	// MotionPrompt and Prompt, when set, replace the scene's motion_prompt and prompt.
	MotionPrompt *string
	Prompt       *string
}

// Propose says how a report's problem would be fixed, for a scene that is
// silent or not and has the given seed (nil when it has none): a trim cuts a
// silent scene to its clean part (when that is shorter than it is), anything
// else bad is made again with another seed. It returns nil for an ok scene.
func Propose(rep *SceneReport, silent bool, silenceSeconds float64, seed *int) *Fix {
	switch {
	case rep.Verdict == VerdictOK:
		return nil
	case rep.Verdict == VerdictTrim && silent:
		if rep.GoodUntil >= silenceSeconds {
			return nil
		}
		cut := rep.GoodUntil
		return &Fix{ID: rep.ID, SilenceSeconds: &cut}
	default:
		next := 1000
		if seed != nil {
			next = *seed + 1000
		}
		return &Fix{ID: rep.ID, Seed: &next}
	}
}

// ApplyFixes edits scenes.yaml in place through its YAML nodes, so comments and
// the order of the keys stay as they were. The first time it writes
// <path>.bak with the file as it was. A fix for a scene that is not in the file
// is an error and nothing is written.
func ApplyFixes(path string, fixes []Fix) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("quality: cannot parse %s: %w", path, err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("quality: %s has no mapping at its top", path)
	}
	scenes := mapValue(doc.Content[0], "scenes")
	if scenes == nil || scenes.Kind != yaml.SequenceNode {
		return fmt.Errorf("quality: %s has no scenes list", path)
	}
	byID := map[string]*yaml.Node{}
	for _, s := range scenes.Content {
		if id := mapValue(s, "id"); id != nil {
			byID[id.Value] = s
		}
	}
	for _, f := range fixes {
		scene, ok := byID[f.ID]
		if !ok {
			return fmt.Errorf("quality: scene %q is not in %s", f.ID, path)
		}
		if f.SilenceSeconds != nil {
			setScalar(scene, "silence_seconds", strconv.FormatFloat(*f.SilenceSeconds, 'f', -1, 64), "!!float")
		}
		if f.Seed != nil {
			setScalar(scene, "seed", strconv.Itoa(*f.Seed), "!!int")
		}
		if f.MotionPrompt != nil {
			setScalar(scene, "motion_prompt", *f.MotionPrompt, "!!str")
		}
		if f.Prompt != nil {
			setScalar(scene, "prompt", *f.Prompt, "!!str")
		}
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	path = media.Real(path) // scenes.yaml may be a link into the media directory: keep the link, write beside the file
	if bak := path + ".bak"; !fsutil.Exists(bak) {
		if err := os.WriteFile(bak, data, 0o644); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// mapValue returns the value node of key in a mapping node, or nil.
func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// setScalar sets key to value in a mapping node, adding it when absent.
func setScalar(m *yaml.Node, key, value, tag string) {
	if v := mapValue(m, key); v != nil {
		v.Kind, v.Tag, v.Value, v.Style = yaml.ScalarNode, tag, value, 0
		return
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value})
}

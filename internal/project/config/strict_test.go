package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStrict_AMisspeltFieldIsRefusedWithItsLine(t *testing.T) {
	yamlText := "project: demo\noutput_file: out.mp4\ndefaults:\n  step: 20\nscenes:\n  - id: s1\n    prompt: p\n    tts_text: t\n"
	_, err := LoadBytes([]byte(yamlText))
	if err == nil || !strings.Contains(err.Error(), "field step not found") || !strings.Contains(err.Error(), "line 4") {
		t.Fatalf("want an error naming the field step and line 4, got %v", err)
	}
	p := writeTempYAML(t, yamlText)
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "field step not found") {
		t.Fatalf("Load must refuse it too, got %v", err)
	}
}

func TestStrict_TheDeprecatedAliasAndKnownFieldsStillLoad(t *testing.T) {
	cfg, err := LoadBytes([]byte("project: demo\noutput_file: out.mp4\ndefaults:\n  steps: 20\nscenes:\n  - id: s1\n    prompt: p\n    tts_text: t\n"))
	if err != nil || cfg.OutputFile != "out.mp4" || cfg.Defaults.Steps != 20 {
		t.Fatalf("a valid file must load: %v %+v", err, cfg)
	}
	if _, err := LoadBytes(nil); err != nil {
		t.Fatalf("an empty file is not a strictness error: %v", err)
	}
}

// TestRealProjectsStillLoad loads the scenes.yaml of every project under $VERITMO_REAL_PROJECTS (skipped when unset) and
// checks that the strict parsing and the path and bound rules accept them. Run it before merging a rule change.
func TestRealProjectsStillLoad(t *testing.T) {
	root := os.Getenv("VERITMO_REAL_PROJECTS")
	if root == "" {
		t.Skip("set VERITMO_REAL_PROJECTS to the projects folder to check the real projects")
	}
	files, _ := filepath.Glob(filepath.Join(root, "*", "scenes.yaml"))
	if len(files) == 0 {
		t.Skip("no project found in " + root)
	}
	for _, f := range files {
		cfg, err := Load(f)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if errs := append(checkPaths(cfg), checkBounds(cfg)...); len(errs) > 0 {
			t.Errorf("%s: %v", f, errs)
		}
	}
	t.Logf("%d projects checked", len(files))
}

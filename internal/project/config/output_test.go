package config

import (
	"strings"
	"testing"
)

func hasWarning(ws []string, substr string) bool {
	for _, w := range ws {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

func TestOutput_ModeDefaultingAndAlias(t *testing.T) {
	cases := []struct {
		name     string
		cfg      *Config
		wantMode OutputMode
		wantFile string
		wantErr  string // substring expected in error; empty means no error
		wantWarn string // substring expected in warnings; empty means none required
	}{
		{
			name: "absent mode defaults to video",
			cfg: &Config{
				Project: "p",
				Output:  Output{File: "out/final.mp4"},
				Scenes:  []Scene{{ID: "s1", Prompt: "a", TTSText: "b"}},
			},
			wantMode: ModeVideo,
			wantFile: "out/final.mp4",
		},
		{
			name: "invalid mode is rejected",
			cfg: &Config{
				Project: "p",
				Output:  Output{Mode: "audio", File: "out.mp4"},
				Scenes:  []Scene{{ID: "s1", Prompt: "a", TTSText: "b"}},
			},
			wantErr: `must be "video", "images", "music", "video_edit" or "models3d"`,
		},
		{
			name: "alias-only maps to output.file with deprecation warning",
			cfg: &Config{
				Project:    "p",
				OutputFile: "out/final.mp4",
				Scenes:     []Scene{{ID: "s1", Prompt: "a", TTSText: "b"}},
			},
			wantMode: ModeVideo,
			wantFile: "out/final.mp4",
			wantWarn: "deprecated",
		},
		{
			name: "alias and output.file identical is accepted with warning",
			cfg: &Config{
				Project:    "p",
				OutputFile: "out/final.mp4",
				Output:     Output{File: "out/final.mp4"},
				Scenes:     []Scene{{ID: "s1", Prompt: "a", TTSText: "b"}},
			},
			wantMode: ModeVideo,
			wantFile: "out/final.mp4",
			wantWarn: "deprecated",
		},
		{
			name: "alias and output.file different is rejected",
			cfg: &Config{
				Project:    "p",
				OutputFile: "a.mp4",
				Output:     Output{File: "b.mp4"},
				Scenes:     []Scene{{ID: "s1", Prompt: "a", TTSText: "b"}},
			},
			wantErr: "set only one",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}
			if tc.cfg.Output.Mode != tc.wantMode {
				t.Errorf("Output.Mode = %q, want %q", tc.cfg.Output.Mode, tc.wantMode)
			}
			if tc.cfg.Output.File != tc.wantFile {
				t.Errorf("Output.File = %q, want %q", tc.cfg.Output.File, tc.wantFile)
			}
			if tc.wantWarn != "" && !hasWarning(tc.cfg.Warnings, tc.wantWarn) {
				t.Errorf("warnings %v should contain %q", tc.cfg.Warnings, tc.wantWarn)
			}
		})
	}
}

func TestOutput_AliasConflictRejectedInImagesMode(t *testing.T) {
	cfg := &Config{
		Project:    "p",
		OutputFile: "a.mp4",
		Output:     Output{Mode: ModeImages, File: "b.mp4"},
		Scenes:     []Scene{{ID: "s1", Prompt: "a"}},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "set only one") {
		t.Fatalf("expected alias-conflict error in images mode, got: %v", err)
	}
}

func TestValidate_ImagesMode(t *testing.T) {
	cases := []struct {
		name     string
		cfg      *Config
		wantErr  string
		wantWarn string
	}{
		{
			name: "empty tts_text is accepted",
			cfg: &Config{
				Project: "p",
				Output:  Output{Mode: ModeImages},
				Scenes:  []Scene{{ID: "s1", Prompt: "a"}},
			},
		},
		{
			name: "non-empty tts_text is accepted in silence",
			cfg: &Config{
				Project: "p",
				Output:  Output{Mode: ModeImages},
				Scenes:  []Scene{{ID: "s1", Prompt: "a", TTSText: "hello"}},
			},
		},
		{
			name: "non-zero speed is accepted in silence",
			cfg: &Config{
				Project: "p",
				Output:  Output{Mode: ModeImages},
				Scenes:  []Scene{{ID: "s1", Prompt: "a", Speed: 1.2}},
			},
		},
		{
			name: "empty prompt is rejected",
			cfg: &Config{
				Project: "p",
				Output:  Output{Mode: ModeImages},
				Scenes:  []Scene{{ID: "s1", Prompt: ""}},
			},
			wantErr: "empty prompt",
		},
		{
			name: "variants zero is rejected",
			cfg: &Config{
				Project: "p",
				Output:  Output{Mode: ModeImages},
				Scenes:  []Scene{{ID: "s1", Prompt: "a", Variants: intPtr(0)}},
			},
			wantErr: "variants must be a positive integer >= 1",
		},
		{
			name: "negative variants is rejected",
			cfg: &Config{
				Project: "p",
				Output:  Output{Mode: ModeImages},
				Scenes:  []Scene{{ID: "s1", Prompt: "a", Variants: intPtr(-2)}},
			},
			wantErr: "variants must be a positive integer >= 1",
		},
		{
			name: "missing output file is accepted",
			cfg: &Config{
				Project: "p",
				Output:  Output{Mode: ModeImages},
				Scenes:  []Scene{{ID: "s1", Prompt: "a"}},
			},
		},
		{
			name: "colliding export paths are rejected",
			cfg: &Config{
				Project: "p",
				Output:  Output{Mode: ModeImages},
				Scenes: []Scene{
					{ID: "a", Prompt: "x", Variants: intPtr(2)}, // a_v1.png, a_v2.png
					{ID: "a_v1", Prompt: "y"},                   // a_v1.png → collision
				},
			},
			wantErr: "resolve to output path",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}
			if tc.wantWarn != "" && !hasWarning(tc.cfg.Warnings, tc.wantWarn) {
				t.Errorf("warnings %v should contain %q", tc.cfg.Warnings, tc.wantWarn)
			}
		})
	}
}

func TestValidate_VideoModeVariantsWarning(t *testing.T) {
	cfg := &Config{
		Project: "p",
		Output:  Output{Mode: ModeVideo, File: "out.mp4"},
		Scenes:  []Scene{{ID: "s1", Prompt: "a", TTSText: "b", Variants: intPtr(3)}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !hasWarning(cfg.Warnings, "variants has no effect in video mode") {
		t.Errorf("warnings %v should mention variants having no effect, naming the scene", cfg.Warnings)
	}
	if !hasWarning(cfg.Warnings, "s1") {
		t.Errorf("warnings %v should name scene s1", cfg.Warnings)
	}
}

func TestValidate_VideoModeNoOutputPath(t *testing.T) {
	cfg := &Config{
		Project: "p",
		Output:  Output{Mode: ModeVideo},
		Scenes:  []Scene{{ID: "s1", Prompt: "a", TTSText: "b"}},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "output path") {
		t.Fatalf("expected video-mode missing-output-path error, got: %v", err)
	}
}

func TestDefaults_SubjectAndNegativePromptOptional(t *testing.T) {
	yamlContent := `
project: p
output:
  mode: images
scenes:
  - id: s1
    prompt: a cat
`
	path := writeTempYAML(t, yamlContent)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Defaults.SubjectPrompt != "" {
		t.Errorf("SubjectPrompt = %q, want empty default", cfg.Defaults.SubjectPrompt)
	}
	if cfg.Defaults.NegativePrompt != "" {
		t.Errorf("NegativePrompt = %q, want empty default", cfg.Defaults.NegativePrompt)
	}
	if cfg.Defaults.LoraModelDir != "" {
		t.Errorf("LoraModelDir = %q, want empty default", cfg.Defaults.LoraModelDir)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestDefaults_SubjectAndNegativePromptParsed(t *testing.T) {
	yamlContent := `
project: p
output:
  mode: images
defaults:
  subject_prompt: cinematic wide shot
  negative_prompt: watermark
  lora_model_dir: models/lora
scenes:
  - id: s1
    prompt: a cat
    lora: "<lora:style:0.8>"
`
	path := writeTempYAML(t, yamlContent)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Defaults.SubjectPrompt != "cinematic wide shot" {
		t.Errorf("SubjectPrompt = %q", cfg.Defaults.SubjectPrompt)
	}
	if cfg.Defaults.NegativePrompt != "watermark" {
		t.Errorf("NegativePrompt = %q", cfg.Defaults.NegativePrompt)
	}
	if cfg.Defaults.LoraModelDir != "models/lora" {
		t.Errorf("LoraModelDir = %q", cfg.Defaults.LoraModelDir)
	}
	if cfg.Scenes[0].Lora != "<lora:style:0.8>" {
		t.Errorf("Scenes[0].Lora = %q", cfg.Scenes[0].Lora)
	}
}

func TestVariants_AbsentDefaultsToOne(t *testing.T) {
	s := Scene{ID: "s1"}
	if got := s.EffectiveVariants(); got != 1 {
		t.Errorf("EffectiveVariants() = %d, want 1", got)
	}
	s.Variants = intPtr(4)
	if got := s.EffectiveVariants(); got != 4 {
		t.Errorf("EffectiveVariants() = %d, want 4", got)
	}
}

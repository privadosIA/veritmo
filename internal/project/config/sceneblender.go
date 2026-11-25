package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"veritmo/internal/platform/runner"
	"veritmo/internal/video/blender"
)

// SceneBlender makes a scene rendered by Blender (Cycles) to a video as long as the scene, from one of two things in the
// project's sources: a .blend file (File) or a scene description (Build, a .scene.json built inside Blender). Exactly one
// of the two. Inject sets things in a .blend file by name before the render: the text of a text object, the colour of a
// material, or a custom property of an object ("Object.property").
type SceneBlender struct {
	// File is a .blend file of the sources folder.
	File string `yaml:"file,omitempty" json:"file,omitempty"`
	// Build is a scene description (a .scene.json file of the sources folder) that Blender builds and renders; the
	// built scene is also kept as a .blend. Not with Inject or Frames.
	Build string `yaml:"build,omitempty" json:"build,omitempty"`
	// Inject maps a name to a text, a number, a boolean or a list of numbers (a colour: 3 or 4 values, or "#rrggbb").
	Inject map[string]any `yaml:"inject,omitempty" json:"inject,omitempty"`
	// Samples is the Cycles samples per pixel; 0 means the default (DefaultBlenderSamples for a .blend, DefaultBuiltSamples
	// for a scene built from a description).
	Samples int `yaml:"samples,omitempty" json:"samples,omitempty"`
	// Device is "auto" (a card when the run gives one, else the CPU), "gpu" or "cpu"; empty means auto.
	Device string `yaml:"device,omitempty" json:"device,omitempty"`
	// Frames is [first, last]: the frames of the file's animation to use; absent means the file's own range.
	Frames []int `yaml:"frames,omitempty" json:"frames,omitempty"`
}

// The devices a Blender scene can ask for, and the samples used when it does not say.
const (
	BlenderAuto           = "auto"
	BlenderGPU            = "gpu"
	BlenderCPU            = "cpu"
	DefaultBlenderSamples = 64
	// DefaultBuiltSamples is the default for a scene built from a description. The example of the project
	// testdata/blender-build at 16 samples against 32 gave a mean SSIM of 0.9988 over every 15th frame (measured on one
	// RTX 5060 Ti, see docs/SCENES_YAML.md): the objects of a description are simple and the denoiser does the rest, so
	// 16 is enough and cuts the render by a third. A .blend keeps 64, because its materials are the author's.
	DefaultBuiltSamples = 16
	MaxBlenderSamples   = 4096
)

// ResolvedDevice is the device asked for, with auto when none is.
func (b SceneBlender) ResolvedDevice() string {
	if b.Device == "" {
		return BlenderAuto
	}
	return b.Device
}

// ResolvedSamples is the samples to render with.
func (b SceneBlender) ResolvedSamples() int {
	if b.Samples <= 0 {
		if b.IsBuild() {
			return DefaultBuiltSamples
		}
		return DefaultBlenderSamples
	}
	return b.Samples
}

// IsBlender reports whether the scene is rendered by Blender from a .blend file.
func (s Scene) IsBlender() bool { return s.Blender != nil }

// IsBuild reports whether the scene is built from a description instead of rendered from a .blend file.
func (b SceneBlender) IsBuild() bool { return b.Build != "" }

// ValidBlendName reports whether name can be a .blend file of the sources folder: a plain file name.
func ValidBlendName(name string) bool {
	return sourceNamePattern.MatchString(name) && strings.EqualFold(filepath.Ext(name), ".blend")
}

// SceneSpecSuffix ends the name of a scene description.
const SceneSpecSuffix = ".scene.json"

// ValidSceneSpecName reports whether name can be a scene description of the sources folder: a plain file name that
// ends in .scene.json.
func ValidSceneSpecName(name string) bool {
	return sourceNamePattern.MatchString(name) && len(name) > len(SceneSpecSuffix) && strings.HasSuffix(strings.ToLower(name), SceneSpecSuffix)
}

// ValidFontName reports whether name can be a font of the sources folder (for a 3D text): a plain .ttf or .otf name.
func ValidFontName(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return sourceNamePattern.MatchString(name) && (ext == ".ttf" || ext == ".otf")
}

// ValidModelName reports whether name can be a 3D model of the sources folder: a plain .glb (binary glTF) file name.
func ValidModelName(name string) bool {
	return sourceNamePattern.MatchString(name) && strings.EqualFold(filepath.Ext(name), ".glb")
}

// insideDir reports whether the file name, given as a plain name inside dir, is a regular reading that stays inside dir
// once symbolic links are followed.
func insideDir(dir, name string) bool {
	d, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	f, err := filepath.EvalSymlinks(filepath.Join(dir, name))
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(d, f)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func (k Checks) blender() error {
	if k.Blender != nil {
		return k.Blender()
	}
	_, err := blender.Detect(context.Background(), runner.NewExecRunner())
	return err
}

// validateSceneBlender checks a scene's blender block.
func (c *Config) validateSceneBlender(s Scene, statPath func(string) string) []error {
	b := s.Blender
	if b == nil {
		return nil
	}
	var errs []error
	bad := func(format string, a ...any) {
		errs = append(errs, fmt.Errorf("config: scene %q: "+format, append([]any{s.ID}, a...)...))
	}
	switch {
	case b.File == "" && b.Build == "":
		bad("blender.file is required (the name of a .blend file in %s), or blender.build (a .scene.json description)", SourcesSubdir)
	case b.File != "" && b.Build != "":
		bad("blender.file and blender.build cannot both be given: a scene is rendered from a .blend file or built from a description")
	case b.File != "":
		if !ValidBlendName(b.File) {
			bad("blender.file %q must be the name of a .blend file, not a path", b.File)
		} else if info, err := os.Stat(statPath(filepath.Join(SourcesSubdir, b.File))); err != nil || info.IsDir() {
			bad("blender.file %q not found in %s", b.File, SourcesSubdir)
		} else if !insideDir(statPath(SourcesSubdir), b.File) {
			bad("blender.file %q resolves outside %s", b.File, SourcesSubdir)
		}
	default:
		if !ValidSceneSpecName(b.Build) {
			bad("blender.build %q must be the name of a %s file, not a path", b.Build, SceneSpecSuffix)
		} else if info, err := os.Stat(statPath(filepath.Join(SourcesSubdir, b.Build))); err != nil || info.IsDir() {
			bad("blender.build %q not found in %s", b.Build, SourcesSubdir)
		} else if !insideDir(statPath(SourcesSubdir), b.Build) {
			bad("blender.build %q resolves outside %s", b.Build, SourcesSubdir)
		}
		if len(b.Inject) > 0 {
			bad("blender.inject cannot be used with blender.build: the description says everything")
		}
		if b.Frames != nil {
			bad("blender.frames cannot be used with blender.build: the frames come from the length of the scene")
		}
	}
	switch b.Device {
	case "", BlenderAuto, BlenderGPU, BlenderCPU:
	default:
		bad("blender.device %q is invalid: must be %q, %q or %q", b.Device, BlenderAuto, BlenderGPU, BlenderCPU)
	}
	if b.Samples < 0 || b.Samples > MaxBlenderSamples {
		bad("blender.samples must be between 1 and %d, got %d", MaxBlenderSamples, b.Samples)
	}
	if b.Frames != nil {
		if len(b.Frames) != 2 || b.Frames[0] < 0 || b.Frames[1] < b.Frames[0] {
			bad("blender.frames must be [first, last] with 0 <= first <= last, got %v", b.Frames)
		}
	}
	for name, v := range b.Inject {
		if strings.TrimSpace(name) == "" {
			bad("blender.inject has an empty name")
		}
		if err := checkInjectValue(v); err != nil {
			bad("blender.inject %q: %v", name, err)
		}
	}
	for name, set := range map[string]bool{
		"image": s.Image != "", "init_image": s.InitImage != "", "clip": s.Clip != "", "animate": s.Animate,
		"motion_graphic": s.MotionGraphic != "", "motion_part": s.MotionPart != "",
		"motion_prompt": s.MotionPrompt != "", "animation": s.Animation != nil, "render": s.Render != nil,
	} {
		if set {
			bad("blender cannot be combined with %s: the scene is rendered by Blender", name)
		}
	}
	for name, set := range map[string]bool{"prompt": s.Prompt != "", "negative_prompt": s.NegativePrompt != "", "lora": s.Lora != "", "variants": s.Variants != nil && *s.Variants != 1} {
		if set {
			c.warn("scene %q: %s is ignored because the scene is rendered by Blender", s.ID, name)
		}
	}
	return errs
}

// checkInjectValue accepts a text, a number, a boolean or a list of numbers.
func checkInjectValue(v any) error {
	switch t := v.(type) {
	case string, bool, int, int64, float64, float32:
		return nil
	case []any:
		for _, e := range t {
			switch e.(type) {
			case int, int64, float64, float32:
			default:
				return fmt.Errorf("a list may only hold numbers")
			}
		}
		return nil
	case []float64, []int:
		return nil
	default:
		return fmt.Errorf("must be a text, a number, a boolean or a list of numbers")
	}
}

// validateBlenderAvailable fails a project that has Blender scenes when Blender cannot be used here.
func (c *Config) validateBlenderAvailable() []error {
	var ids []string
	for _, s := range c.Scenes {
		if s.IsBlender() {
			ids = append(ids, fmt.Sprintf("%q", s.ID))
		}
	}
	if len(ids) == 0 || c.SkipBlenderCheck {
		return nil
	}
	if err := c.Checks.blender(); err != nil {
		return []error{fmt.Errorf("config: scenes %s are rendered by Blender: %w", strings.Join(ids, ", "), err)}
	}
	return nil
}
